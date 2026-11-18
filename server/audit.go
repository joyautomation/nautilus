package server

// The audit seam (#709, docs/design/identity-audit.md §4): every route
// that changes something is registered through s.audited, which records
// exactly one audit.Entry per request, accepted or refused, before the
// client is answered. TestEveryWriteRouteIsAudited fails on a write route
// registered without it.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/joyautomation/nautilus/audit"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/runtime"
)

const (
	// refuseFull is how many refusals of one action from one address a
	// refuseWindow journals in full; the rest of the window's become one
	// summary entry, so an unauthenticated client cannot force an fsynced
	// entry per request. Accepted actions are never coalesced.
	refuseFull   = 5
	refuseWindow = time.Minute
	// coalesceKeys bounds the refusal windows kept at once; past it, new
	// addresses share one window per action.
	coalesceKeys = 4096
	// pendingMax bounds the entries kept in memory while the journal
	// cannot be written; past it the oldest are dropped, and counted.
	pendingMax = 10000
	// auditRing is how many recent entries GET /api/audit can serve.
	auditRing = 1000
	// reasonMax caps a refusal's reason: the text can echo what a client
	// sent (a Host header, a tag name).
	reasonMax = 512
	byMax     = 128
)

// auditUpkeep is how often Server.Run calls the auditor's tick.
var auditUpkeep = 5 * time.Second

// auditTarget is what a request acts on, read from its body and the
// store before the handler runs.
type auditTarget struct {
	target        string
	before, after any
	detail        map[string]any
	// done, when set, is called after an accepted action; its value
	// replaces after (what a rollback went back to, say).
	done func() any
}

// targetFunc reads a request's auditTarget. body is the request body,
// which the handler still gets in full.
type targetFunc func(r *http.Request, body []byte) auditTarget

// auditor records entries: to the journal (Options.Audit), and always to
// a ring that GET /api/audit serves. It coalesces refusals and, while the
// journal cannot be written, keeps the entries and writes them, with the
// same ids, once it can.
type auditor struct {
	rec  audit.Recorder // the journal; the ring itself when there is none
	ring *audit.Ring
	now  func() time.Time

	mu      sync.Mutex
	pending []audit.Entry // entries rec failed to take, oldest first
	dropped int           // pending entries dropped past pendingMax
	failing bool
	windows map[refusalKey]*refusals
	swept   time.Time
}

type refusalKey struct{ from, action string }

type refusals struct {
	since time.Time
	n     int
	last  audit.Entry // the latest refusal not journalled in full
}

func newAuditor(rec audit.Recorder) *auditor {
	ring := audit.NewRing(auditRing)
	if rec == nil {
		rec = ring
	}
	return &auditor{rec: rec, ring: ring, now: time.Now, windows: map[refusalKey]*refusals{}}
}

// record journals e, unless it is a refusal past its window's allowance.
func (a *auditor) record(e audit.Entry) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	if e.TS.IsZero() {
		e.TS = now
	}
	e.TS = e.TS.UTC()
	if e.ID == "" {
		e.ID = audit.NewID(e.TS)
	}
	a.sweepLocked(now, false)
	if e.Result == audit.ResultRefused && !a.admitLocked(e, now) {
		return
	}
	a.writeLocked(e)
}

// tick writes what is owed: entries kept while the journal failed, and
// the summaries of refusal windows that have closed. Server.Run calls it,
// so a burst of refusals followed by silence still gets its summary.
func (a *auditor) tick() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.retryLocked()
	a.sweepLocked(a.now(), true)
}

// writeLocked puts e in the journal, after any entries still owed to it,
// and in the ring. When the journal fails the action is still allowed
// (the interim policy until design §6 question 5 is answered): the
// failure is logged and e is kept, to be written with the same id later.
func (a *auditor) writeLocked(e audit.Entry) {
	got := e
	if a.retryLocked() {
		var err error
		if got, err = a.rec.Record(e); err != nil {
			got = e
			a.keepLocked(e, err)
		}
	} else {
		a.keepLocked(e, nil)
	}
	if a.rec != audit.Recorder(a.ring) {
		a.ring.Record(got)
	}
}

// retryLocked writes the kept entries in order and reports whether none
// is left.
func (a *auditor) retryLocked() bool {
	for len(a.pending) > 0 {
		if _, err := a.rec.Record(a.pending[0]); err != nil {
			return false
		}
		a.pending = a.pending[1:]
	}
	if a.failing {
		a.failing = false
		slog.Warn("audit journal: writable again; the entries kept meanwhile are written", "dropped", a.dropped)
		if a.dropped > 0 {
			n := a.dropped
			a.dropped = 0
			ts := a.now().UTC()
			a.rec.Record(audit.Entry{ID: audit.NewID(ts), TS: ts, User: "system", Source: audit.SourceSystem,
				Action: "audit.dropped", Result: audit.ResultError,
				Detail: map[string]any{"entries": n, "reason": "the journal could not be written and more than " + strconv.Itoa(pendingMax) + " entries were waiting"}})
		}
	}
	return true
}

func (a *auditor) keepLocked(e audit.Entry, err error) {
	a.pending = append(a.pending, e)
	if len(a.pending) > pendingMax {
		a.pending = a.pending[1:]
		a.dropped++
	}
	if !a.failing || len(a.pending)%100 == 0 {
		slog.Error("audit journal: unwritable; the action was allowed and its entry is kept in memory until the journal can be written",
			"err", err, "pending", len(a.pending), "dropped", a.dropped)
	}
	a.failing = true
}

// admitLocked counts a refusal in its (address, action) window and
// reports whether it is journalled in full.
func (a *auditor) admitLocked(e audit.Entry, now time.Time) bool {
	key := refusalKey{hostOf(e.From), e.Action}
	w := a.windows[key]
	if w != nil && now.Sub(w.since) >= refuseWindow {
		a.closeLocked(key, w)
		w = nil
	}
	if w == nil {
		if len(a.windows) >= coalesceKeys {
			key.from = "*"
			w = a.windows[key]
		}
		if w == nil {
			w = &refusals{since: now}
			a.windows[key] = w
		}
	}
	w.n++
	if w.n <= refuseFull && key.from != "*" {
		return true
	}
	w.last = e
	if key.from == "*" {
		w.last.From = "*"
	}
	return false
}

// sweepLocked closes the windows a minute old (at most once a second,
// unless forced).
func (a *auditor) sweepLocked(now time.Time, force bool) {
	if !force && now.Sub(a.swept) < time.Second {
		return
	}
	a.swept = now
	for k, w := range a.windows {
		if now.Sub(w.since) >= refuseWindow {
			a.closeLocked(k, w)
		}
	}
}

// closeLocked ends a window, journalling one "refused ×n" summary for the
// refusals it did not journal in full.
func (a *auditor) closeLocked(k refusalKey, w *refusals) {
	delete(a.windows, k)
	n := w.n - refuseFull
	if k.from == "*" {
		n = w.n
	}
	if n <= 0 {
		return
	}
	s := w.last
	ts := a.now().UTC()
	s.ID, s.TS = audit.NewID(ts), ts
	s.Target, s.Before, s.After = "", nil, nil
	s.Detail = map[string]any{
		"coalesced": n,
		"since":     w.since.UTC().Format(time.RFC3339Nano),
		"reason":    w.last.Detail["reason"],
	}
	a.writeLocked(s)
}

// audited wraps a write route's handler: it reads the target, runs the
// handler into a buffer, records one entry from the outcome, and only then
// answers the client, so an action is never answered before it is
// journalled.
func (s *Server) audited(action string, tf targetFunc, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body []byte
		if r.Body != nil {
			body, _ = io.ReadAll(r.Body)
			r.Body.Close()
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		var t auditTarget
		if tf != nil {
			t = tf(r, body)
		}
		buf := &bufferedResponse{header: w.Header()}
		h(buf, r)
		status := buf.status
		if status == 0 {
			status = http.StatusOK
		}
		e := audit.Entry{
			User:   s.principal(r),
			Source: clientSource(r),
			Action: action,
			Target: t.target,
			Before: t.before,
			After:  t.after,
			Detail: t.detail,
			From:   r.RemoteAddr,
			Result: resultOf(status),
		}
		if e.Result == audit.ResultOK {
			if t.done != nil {
				e.After = t.done()
			}
		} else {
			if e.Detail == nil {
				e.Detail = map[string]any{}
			}
			e.Detail["status"] = status
			e.Detail["reason"] = reasonOf(buf.body.Bytes())
		}
		s.audit.record(e)
		w.WriteHeader(status)
		w.Write(buf.body.Bytes())
	}
}

// bufferedResponse holds a handler's answer until its entry is recorded.
type bufferedResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (b *bufferedResponse) Header() http.Header { return b.header }
func (b *bufferedResponse) Write(p []byte) (int, error) {
	if b.status == 0 {
		b.status = http.StatusOK
	}
	return b.body.Write(p)
}
func (b *bufferedResponse) WriteHeader(code int) {
	if b.status == 0 {
		b.status = code
	}
}

func resultOf(status int) string {
	switch {
	case status >= 500:
		return audit.ResultError
	case status >= 400:
		return audit.ResultRefused
	}
	return audit.ResultOK
}

// principal is who the request acts as, with today's identities: "token"
// when it presents the server's token, else "anonymous". Nothing the
// client names (an alarm's "by") is ever taken as the principal.
func (s *Server) principal(r *http.Request) string {
	if s.authToken != "" && tokenMatches(r, s.authToken) {
		return "token"
	}
	return "anonymous"
}

// clientSource is the surface a request came through: X-Nautilus-Client
// when it names a known one, else "hmi" for a browser (it sends an Origin
// on a write) and "api" for anything else.
func clientSource(r *http.Request) string {
	switch c := strings.ToLower(strings.TrimSpace(r.Header.Get("X-Nautilus-Client"))); c {
	case audit.SourceHMI, audit.SourceIDE, audit.SourceCLI, audit.SourceAPI:
		return c
	}
	if r.Header.Get("Origin") != "" {
		return audit.SourceHMI
	}
	return audit.SourceAPI
}

// hostOf is a remote address without its port, the key refusals are
// coalesced by: a client's every connection has a new port.
func hostOf(addr string) string {
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return addr
}

// reasonOf is a refusal's reason: a JSON body's "error", else the text,
// capped.
func reasonOf(body []byte) string {
	var j struct {
		Error string `json:"error"`
	}
	msg := strings.TrimSpace(string(body))
	if json.Unmarshal(body, &j) == nil && j.Error != "" {
		msg = j.Error
	}
	return capText(msg, reasonMax)
}

func capText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s + "…"
}

// valueOf is a tag's or member's current value as plain JSON values, its
// NaN and ±Inf kept as floats for the journal to write as strings. Unlike
// runtime.Plain, which renders them null for the live frames, the journal
// keeps what the value was.
func (s *Server) valueOf(name string) any {
	if name == "" {
		return nil
	}
	v, ok := s.rt.Tags().ReadPath(name)
	if !ok {
		return nil
	}
	if iv, ok := v.(ir.Value); ok {
		return journalValue(iv)
	}
	return v
}

func journalValue(v ir.Value) any {
	switch v.Kind {
	case ir.TypeReal:
		return v.F
	case ir.TypeArray:
		out := make([]any, len(v.Arr))
		for i, e := range v.Arr {
			out[i] = journalValue(e)
		}
		return out
	case ir.TypeStruct:
		out := make(map[string]any, len(v.Fld))
		for i, f := range v.Fld {
			name := ""
			if v.Struct != nil && i < len(v.Struct.Fields) {
				name = v.Struct.Fields[i].Name
			}
			if name == "" {
				name = "_" + strconv.Itoa(i)
			}
			out[name] = journalValue(f)
		}
		return out
	case ir.TypeFB:
		if v.FB == nil || v.FB.Def == nil {
			return nil
		}
		slots := v.FB.Def.AllSlots()
		out := make(map[string]any, len(slots))
		for i, sl := range slots {
			if i >= len(v.FB.Slots) || strings.HasPrefix(sl.Name, "_") {
				continue
			}
			out[sl.Name] = journalValue(v.FB.Slots[i])
		}
		return out
	}
	return runtime.Plain(v)
}

// forcedValue is the value name is forced to, or nil.
func (s *Server) forcedValue(name string) any {
	for k, v := range s.rt.Tags().ForcedValues() {
		if strings.EqualFold(k, name) {
			return v
		}
	}
	return nil
}

func byDetail(by string) map[string]any {
	if by = strings.TrimSpace(by); by == "" {
		return nil
	}
	// "by" is the name the client typed; it is kept as said, never taken
	// as the principal.
	return map[string]any{"by": capText(by, byMax)}
}

// The targets of the write routes.

func (s *Server) tagTarget(_ *http.Request, body []byte) auditTarget {
	var req writeTagRequest
	json.Unmarshal(body, &req)
	return auditTarget{target: req.Name, before: s.valueOf(req.Name), after: req.Value}
}

func (s *Server) forceTarget(_ *http.Request, body []byte) auditTarget {
	var req forceRequest
	json.Unmarshal(body, &req)
	return auditTarget{target: req.Name, before: s.forcedValue(req.Name), after: req.Value}
}

func (s *Server) unforceTarget(r *http.Request, _ []byte) auditTarget {
	name := r.PathValue("name")
	return auditTarget{target: name, before: s.forcedValue(name)}
}

func (s *Server) unforceAllTarget(*http.Request, []byte) auditTarget {
	return auditTarget{before: s.rt.Tags().ForcedValues()}
}

func sfcTarget(step bool) targetFunc {
	return func(_ *http.Request, body []byte) auditTarget {
		var req sfcCommand
		json.Unmarshal(body, &req)
		name := req.Transition
		if step {
			name = req.Step
		}
		if req.POU != "" {
			name = req.POU + "." + name
		}
		return auditTarget{target: name}
	}
}

func ackTarget(_ *http.Request, body []byte) auditTarget {
	var req ackRequest
	json.Unmarshal(body, &req)
	target := strings.Join(req.IDs, ",")
	if req.All {
		target = "*"
	}
	return auditTarget{target: target, detail: byDetail(req.By)}
}

func shelveTarget(_ *http.Request, body []byte) auditTarget {
	var req shelveRequest
	json.Unmarshal(body, &req)
	var after any
	if req.Until != "" {
		after = map[string]any{"until": req.Until}
	} else if req.Seconds != 0 {
		after = map[string]any{"seconds": req.Seconds}
	}
	return auditTarget{target: req.ID, after: after, detail: byDetail(req.By)}
}

func unshelveTarget(_ *http.Request, body []byte) auditTarget {
	var req unshelveRequest
	json.Unmarshal(body, &req)
	return auditTarget{target: req.ID, detail: byDetail(req.By)}
}

// programTarget names the task an edit goes to, as handlePutProgram
// resolves it. The source itself is never journalled: before and after
// are the program hashes GET /api/program shows, and detail has the
// source's full SHA-256 and length.
func (s *Server) programTarget(r *http.Request, body []byte) auditTarget {
	var req putProgramRequest
	json.Unmarshal(body, &req)
	p, task, _ := s.selectProgram(r)
	if r.URL.Query().Get("task") == "" && r.URL.Query().Get("pou") == "" {
		if pou := runtime.POUOf(req.Source); pou != "" {
			if byPou, byTask := s.rt.ProgramByPOU(pou); byPou != nil {
				p, task = byPou, byTask
			}
		}
	}
	t := auditTarget{target: task}
	if p != nil {
		t.before = p.Hash()
	}
	if req.Source != "" {
		sum := sha256.Sum256([]byte(req.Source))
		t.after = hex.EncodeToString(sum[:6])
		t.detail = map[string]any{"sha256": hex.EncodeToString(sum[:]), "bytes": len(req.Source)}
	}
	return t
}

func (s *Server) rollbackTarget(r *http.Request, _ []byte) auditTarget {
	p, task, _ := s.selectProgram(r)
	t := auditTarget{target: task}
	if p != nil {
		t.before = p.Hash()
		t.done = func() any { return p.Hash() }
	}
	return t
}

func (s *Server) activateTarget(_ *http.Request, body []byte) auditTarget {
	var req struct {
		Sha string `json:"sha"`
	}
	json.Unmarshal(body, &req)
	s.mu.Lock()
	before := s.active
	s.mu.Unlock()
	t := auditTarget{target: req.Sha, after: req.Sha}
	if before != "" {
		t.before = before
	}
	return t
}

// auditResponse is GET /api/audit.
type auditResponse struct {
	Entries   []audit.Entry `json:"entries"`
	Truncated bool          `json:"truncated"`
}

// handleAudit serves the recent entries, newest first, filtered by
// from/to (epoch ms or RFC3339), user, action, target and result, at most
// limit (default 100). With a token configured it needs the token: the
// entries say who did what from where. The users and roles PRs give it
// the "audit" permission (design §4).
func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	if s.authToken != "" && !tokenMatches(r, s.authToken) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "the audit journal needs the auth token"})
		return
	}
	q := r.URL.Query()
	from, err := parseTime(q.Get("from"), time.Time{})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "from: " + err.Error()})
		return
	}
	to, err := parseTime(q.Get("to"), time.Time{})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "to: " + err.Error()})
		return
	}
	limit := 100
	if l := q.Get("limit"); l != "" {
		if limit, err = strconv.Atoi(l); err != nil || limit < 1 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "limit: want a positive integer"})
			return
		}
	}
	out := auditResponse{Entries: []audit.Entry{}}
	for _, e := range s.audit.ring.Recent() {
		if (!from.IsZero() && e.TS.Before(from)) || (!to.IsZero() && e.TS.After(to)) ||
			!matches(q.Get("user"), e.User) || !matches(q.Get("action"), e.Action) ||
			!matches(q.Get("target"), e.Target) || !matches(q.Get("result"), e.Result) {
			continue
		}
		if len(out.Entries) == limit {
			out.Truncated = true
			break
		}
		out.Entries = append(out.Entries, e)
	}
	writeJSON(w, http.StatusOK, out)
}

// matches is a filter: empty matches everything, else equal ignoring case.
func matches(want, got string) bool {
	return want == "" || strings.EqualFold(want, got)
}
