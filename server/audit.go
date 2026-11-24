package server

// The audit seam (#709, docs/design/identity-audit.md §4): every route
// that changes something is registered through s.audited, which records
// exactly one audit.Entry per request, accepted or refused, before the
// client is answered. TestEveryWriteRouteIsAudited fails on a write route
// registered without it.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/joyautomation/nautilus/alarm"
	"github.com/joyautomation/nautilus/audit"
	"github.com/joyautomation/nautilus/auth"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/runtime"
)

const (
	// refuseFull is how many refusals (or errors) of one action from one
	// address a refuseWindow journals in full; the rest of the window's
	// become one summary entry, so an unauthenticated client cannot force an
	// fsynced entry per request. Accepted actions are never coalesced.
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
	// maxWriteBody caps a write request's body, which the seam reads
	// before the handler authorizes it: a client without the token must not
	// make the controller hold an arbitrarily large body. The largest real
	// body is an online edit's source.
	maxWriteBody = 16 << 20
	// targetMax caps an entry's target, and valueMax its before and after
	// (their JSON; audit.Cap). A refused or failed action's values are
	// capped harder: they are what a client sent, and are kept anyway.
	targetMax       = 1024
	valueMax        = 64 << 10
	refusedValueMax = 4 << 10
	// refusedBodyMax and refusedBodyWait bound what the seam reads of a
	// refused request to name its target: a known principal's, refused
	// by role or for a fresh password.
	refusedBodyMax = 8 << 10
)

// actionClass classes every action the seam journals, for when the journal
// cannot be written (#709, design §6 question 5, decided 2026-11-18):
//   - strict actions (setting a force, a program edit or activation)
//     must never happen unrecorded, and are refused;
//   - operator actions (tag writes, alarm acknowledgement and shelving,
//     SFC commands) are allowed;
//   - recovery actions (clearing forces, rolling an edit back) are the
//     ways back to normal, and are allowed too (James, 2026-11-18).
//
// An allowed action's entry is kept in memory and written once the journal
// recovers, and its response carries X-Nautilus-Audit-Journal: down.
//
// Classification fails closed: an action missing here is strict, and
// TestEveryActionIsClassed fails on it, so a new route is classed on
// purpose.
//
// The strict class is the journal's, not auth's elevated permissions
// (auth.ElevatedPermissions, which need a fresh password): the two sets
// differ (a substitute's start is strict and not elevated; clearing a
// force is elevated and not strict).
var actionClass = map[string]string{
	"tag.write": operator,
	// A person's own phone on the pager (#722): journalled, and allowed
	// while the journal is down like the other operator actions.
	"push.subscribe":   operator,
	"push.unsubscribe": operator,
	// Watching a camera live (#724): journalled when it starts and ends, and, like
	// the other operator actions, allowed while the journal is down (the entry is
	// kept in memory and written later).
	"camera.view": operator,
	// a picture kept when an alarm activates is the system's own record; purging
	// them is an engineer's, never unrecorded (#724)
	"camera.snapshot":       operator,
	"camera.snapshot-view":  operator, // a person looking at a kept picture
	"camera.snapshot-purge": strict,
	"camera.ptz":            operator, // a move or a preset: allowed while the journal is down like a tag write
	"camera.ptz-stop":       recovery, // a stop is a way back
	"alarm.ack":             operator,
	"alarm.shelve":          operator,
	"alarm.unshelve":        operator,
	"sfc.step":              operator,
	// Back in service shows alarms again: a way back, as a force's clear.
	"alarm.in-service": recovery,
	"sfc.transition":   operator,

	"force.clear": recovery,
	// letting a checkout force go is a way back, like a force's clear
	"checkout.release": recovery,
	"substitute.stop":  recovery, // a way back to the real reading
	"force.clear-all":  recovery,
	"program.rollback": recovery,

	"force.set":        strict,
	"substitute.start": strict,
	// Hides a piece of equipment's alarms for up to a week: never unrecorded.
	"alarm.out-of-service": strict,
	"substitute.ack":       strict,
	"program.edit":         strict,
	"program.activate":     strict,
	// Ending the subscriptions a mass revocation held (#722): phones stop
	// being paged on a person's word.
	"push.confirm-revocations": strict,
	// A checkout result is a person signing for a point (#719): never unrecorded.
	"checkout.begin":   strict,
	"checkout.result":  strict,
	"checkout.abandon": strict,
	"checkout.seal":    strict,
	"checkout.force":   strict,
	// A note is operator intent with words (#720): never unrecorded.
	"note.add":        strict,
	"note.revise":     strict,
	"note.retract":    strict,
	"config.stage":    strict,
	"config.validate": strict,
	"config.apply":    strict,
	// Saving an applied configuration as a commit (#711) writes to the
	// repo the controller runs from, in the principal's name.
	"config.commit": strict,
	// A parameter is written to the device on an IO-Link port, through its
	// master: a change to a device, like a configuration apply.
	"param.write": strict,
	// A tuned setpoint put back in the repo by the controller itself.
	"param.commit": strict,
	// A set of tuned setpoints written at once (#718): a bulk write.
	"param.import": strict,
	// Cancelling a pending revert keeps a change its device has not
	// answered: it overrides a safeguard the apply asked for.
	"config.revert.cancel": strict,
	// Which process a redundant pair counts as its member.
	"redundancy.clear-duplicate": strict,
	"redundancy.commission":      strict,
	"redundancy.recommission":    strict,
	// A person's word for the operator-intent log, which may accept losing
	// what other copies hold (#1029 3c-3c-2a).
	"redundancy.vouch": strict,
	// Outputs to the field before the program released them (#710 (b2)).
	"redundancy.release-hold": strict,
}

const (
	operator = "operator"
	recovery = "recovery"
	strict   = "strict"

	// auditJournalHeader is set to "down" on the response to an action
	// carried out while the journal cannot be written: its entry is kept
	// in memory until the journal recovers.
	auditJournalHeader = "X-Nautilus-Audit-Journal"
)

// journalStrict reports whether action is refused while the journal is down:
// every action not classed as an operator's or a recovery.
func journalStrict(action string) bool {
	c := actionClass[action]
	return c != operator && c != recovery
}

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
	// answered, when set, reads after from an accepted action's response
	// body (the id a stage was given, the state an apply reached).
	answered func(body []byte) any
	// failed, when set, adds to the detail of a refused or failed action
	// what its answer says (an apply that failed: where it came from and
	// the changes validation had computed).
	failed func(body []byte) map[string]any
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

	// unavailable is why the configured journal could not be opened
	// (Options.AuditUnavailable): the entries are in the ring only.
	unavailable string

	mu      sync.Mutex
	pending []audit.Entry // entries rec failed to take, oldest first
	dropped int           // pending entries dropped past pendingMax
	failing bool
	failErr string    // the last write error, while failing
	failAt  time.Time // when the journal began failing

	// health is what status shows, kept apart from mu: the broadcast loop
	// renders it every frame, and must not wait on a.mu while a write or
	// a catch-up holds it through an fsync.
	health  atomic.Pointer[journalHealth]
	windows map[refusalKey]*refusals
	swept   time.Time
}

// journalHealth is the journal's state for its card: nil while it is
// written as it should be.
type journalHealth struct {
	since            time.Time
	err              string
	pending, dropped int
}

// refusalKey is a refusal window's key: the client's address, the action
// and result, and the surface (so a broker on localhost does not share
// HTTP's window from 127.0.0.1). A refusal of class audit.ClassAuth has
// its own window per target: a flood of write-gate refusals of one tag
// must not fold an auth refusal of another tag into a summary that
// drops its target.
type refusalKey struct{ from, action, result, source, class, target, user string }

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

// record journals e, unless it is a refusal or an error past its window's
// allowance.
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
	if e.Result != audit.ResultOK && !resolves(e) && !a.admitLocked(e, now) {
		return
	}
	a.writeLocked(e)
}

// resolves reports an entry that resolves another (detail.resolves,
// oplog_unknown.go): never coalesced, since a summary would lose which
// entry it resolves. Only this server writes them, bounded by the
// unknowns it answered.
//
// detail.resolves must only ever be set server-side (oplog_unknown.go):
// it exempts an entry from the refusal rate limit, so a value a client
// or a peer could put there would let it force an fsynced entry per
// request.
func resolves(e audit.Entry) bool { return e.Detail["resolves"] != nil }

// recordBatch journals es as one durable batch: each refusal is counted in
// its window as record would (and those past their allowance are left
// out), then what is left is written with one fsync when the journal can
// take a batch (audit.BatchRecorder), else one by one. A batch the journal
// fails to take is kept whole, entry by entry, to be written later.
func (a *auditor) recordBatch(es []audit.Entry) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	a.sweepLocked(now, false)
	keep := make([]audit.Entry, 0, len(es))
	for _, e := range es {
		if e.TS.IsZero() {
			e.TS = now
		}
		e.TS = e.TS.UTC()
		if e.ID == "" {
			e.ID = audit.NewID(e.TS)
		}
		if e.Result != audit.ResultOK && !resolves(e) && !a.admitLocked(e, now) {
			continue
		}
		keep = append(keep, e)
	}
	br, batches := a.rec.(audit.BatchRecorder)
	if !batches || len(keep) < 2 {
		for _, e := range keep {
			a.writeLocked(e)
		}
		return
	}
	got := keep
	if a.retryLocked() {
		out, err := br.RecordBatch(keep)
		if err != nil {
			for _, e := range keep {
				a.keepLocked(e, err)
			}
		} else {
			got = out
		}
	} else {
		for _, e := range keep {
			a.keepLocked(e, nil)
		}
	}
	if a.rec != audit.Recorder(a.ring) {
		for _, e := range got {
			a.ring.Record(e)
		}
	}
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

// upkeep ticks until ctx ends, then flushes.
func (a *auditor) upkeep(ctx context.Context) {
	t := time.NewTicker(auditUpkeep)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			a.flush()
			return
		case <-t.C:
			a.tick()
		}
	}
}

// down reports a journal that could not be opened or cannot be written:
// strict actions are refused, and the system alarm is up.
func (a *auditor) down() bool {
	return a.unavailable != "" || a.health.Load() != nil
}

// RecordSystem journals, as the system, a fact the composition root
// learned while starting that an auditor must be able to find: an alarm
// channel configured not to verify its server's certificate (#715),
// for one. User and Source are the system's; Result is ok.
func (s *Server) RecordSystem(action, target string, detail map[string]any) {
	s.audit.record(audit.Entry{User: "system", Source: audit.SourceSystem, Action: action,
		Target: target, Detail: detail, Result: audit.ResultOK})
}

// AuditJournalDown reports whether the audit journal could not be opened
// or cannot be written. While it is, strict actions (forces, program
// edits) are refused; naut run raises the system alarm from it.
func (s *Server) AuditJournalDown() bool { return s.audit.down() }

// status is the journal's card on the drivers block, shown only while
// something is wrong with it: it could not be opened, or it cannot be
// written. Either way strict actions are refused and operator actions
// are allowed, journalled in memory only: this card (and naut run's
// system alarm) is how an operator learns why.
func (a *auditor) status() *DriverStatus {
	h := a.health.Load()
	switch {
	case a.unavailable != "":
		return &DriverStatus{Kind: "audit", Name: "audit journal", State: "error",
			Message: "the audit journal could not be opened: setting forces and program edits are refused; other actions are allowed and journalled in memory only (the last " +
				strconv.Itoa(auditRing) + ", lost on restart)",
			LastError: a.unavailable}
	case h != nil:
		return &DriverStatus{Kind: "audit", Name: "audit journal", State: "error",
			Message: "the audit journal cannot be written: setting forces and program edits are refused until it can be; other actions are allowed, and their entries are kept in memory until then",
			SinceMs: h.since.UnixMilli(), LastError: h.err,
			Metrics: []DriverMetric{
				{Label: "waiting", Value: float64(h.pending), Volatile: true},
				{Label: "dropped", Value: float64(h.dropped), Volatile: true},
			}}
	}
	return nil
}

// copyCard is the journal's copy in Postgres on the drivers block (#709
// PR 9), shown whenever audit.sink is set: whether it holds every entry,
// how far behind it is, and why it stopped if it did.
func copyCard(st audit.CopyStatus) DriverStatus {
	d := DriverStatus{Kind: "audit", Name: "audit copy", Detail: "postgres", LastError: st.LastError,
		Metrics: []DriverMetric{
			{Label: "copied seq", Value: float64(st.Copied), Volatile: true},
			{Label: "journal seq", Value: float64(st.Head), Volatile: true},
		}}
	if !st.Since.IsZero() {
		d.SinceMs = st.Since.UnixMilli()
	}
	if st.Gaps > 0 {
		d.Metrics = append(d.Metrics, DriverMetric{Label: "gaps", Value: float64(st.Gaps)})
	}
	behind := uint64(0)
	if st.Head > st.Copied {
		behind = st.Head - st.Copied
	}
	switch st.State {
	case audit.CopyCurrent:
		d.State, d.Message = "connected", "the copy in Postgres holds every journal entry"
	case audit.CopyStarting:
		d.State, d.Message = "connecting", "reaching Postgres; the copy starts from the journal's files"
	case audit.CopyBehind:
		if st.LastError == "" {
			d.State, d.Message = "connecting", fmt.Sprintf("catching up: %d entries to copy", behind)
		} else {
			d.State, d.Message = "degraded", fmt.Sprintf("Postgres is unreachable or refusing: the copy is %d entries behind, and catches up from the journal's files when it is back (nothing is lost while they are kept)", behind)
		}
	default: // stopped
		d.State, d.Message = "error", "the copy stopped: the journal's files no longer match what the copy holds, so it is left as it is (it is the evidence). Run naut audit verify, and compare the journal with the copy before restarting"
	}
	// The anchor (#709 PR 9b): the copy's head, kept where the
	// controller's service user cannot rewrite it, unless the copy's own
	// role can.
	if st.Anchored > 0 {
		d.Metrics = append(d.Metrics, DriverMetric{Label: "anchored seq", Value: float64(st.Anchored), Volatile: true})
	}
	if st.AnchorError != "" {
		if d.State == "connected" {
			d.State = "degraded"
		}
		d.Message += "; the anchor could not be written (" + st.AnchorError + "): it is retried"
	}
	if st.Unprotected != "" {
		if d.State == "connected" {
			d.State = "degraded"
		}
		d.Message += "; " + st.Unprotected
	}
	return d
}

// flush writes everything owed now: the kept entries, and a summary for
// every refusal window still open, however young. Called on shutdown,
// before the journal is closed.
func (a *auditor) flush() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.retryLocked()
	for k, w := range a.windows {
		a.closeLocked(k, w)
	}
}

// writeLocked puts e in the journal, after any entries still owed to it,
// and in the ring. When the journal fails, the action this entry records
// has already happened: the failure is logged and e is kept, to be
// written with the same id later; from then on strict actions are
// refused (journalStrict) until the journal catches up.
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
			a.failErr = err.Error()
			a.health.Store(&journalHealth{since: a.failAt, err: a.failErr, pending: len(a.pending), dropped: a.dropped})
			return false
		}
		a.pending = a.pending[1:]
	}
	if a.failing {
		a.failing, a.failErr = false, ""
		a.health.Store(nil)
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
	if err != nil {
		a.failErr = err.Error()
	}
	if !a.failing {
		a.failAt = a.now()
	}
	if !a.failing || len(a.pending)%100 == 0 {
		slog.Error("audit journal: unwritable; the action was allowed and its entry is kept in memory until the journal can be written",
			"err", err, "pending", len(a.pending), "dropped", a.dropped)
	}
	a.failing = true
	a.health.Store(&journalHealth{since: a.failAt, err: a.failErr, pending: len(a.pending), dropped: a.dropped})
}

// admitLocked counts a refusal in its (address, action) window and
// reports whether it is journalled in full.
func (a *auditor) admitLocked(e audit.Entry, now time.Time) bool {
	// The principal is in the key: a named person's refusals are counted
	// apart from anyone else's from the same address, so a flood of
	// anonymous refusals cannot fold a person's into a summary.
	key := refusalKey{from: hostOf(e.From), action: e.Action, result: e.Result, source: e.Source, user: e.User}
	if c, _ := e.Detail["class"].(string); c == audit.ClassAuth {
		key.class, key.target = c, e.Target
	}
	w := a.windows[key]
	if w != nil && now.Sub(w.since) >= refuseWindow {
		a.closeLocked(key, w)
		w = nil
	}
	if w == nil {
		if len(a.windows) >= coalesceKeys {
			key.from, key.target, key.user = "*", "", ""
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
	s.Target, s.Before, s.After = k.target, nil, nil
	s.Detail = map[string]any{
		"coalesced": n,
		"since":     w.since.UTC().Format(time.RFC3339Nano),
		"reason":    w.last.Detail["reason"],
	}
	if k.class != "" {
		s.Detail["class"] = k.class
	}
	a.writeLocked(s)
}

// FlushAudit writes what the audit seam still owes the journal: entries
// kept while it could not be written, and the summary of every open
// refusal window. Call it on shutdown, before closing the journal
// (Server.Run does too, when its context ends).
func (s *Server) FlushAudit() { s.audit.flush() }

// auditedRoute is a write route's handler behind the audit seam. The
// route table registers it with mux.Handle, so a standby that answers a
// write itself (proxyStandby) finds the route's action and journals that
// answer through the same seam.
type auditedRoute struct {
	s      *Server
	action string
	tf     targetFunc
	h      http.HandlerFunc
	pre    preStep
}

// preStep runs before the seam decides anything: it may fix who the
// request acts as (an ack link's person, acklink.go) and return the
// request to go on with, or answer it itself (through a.answer, so the
// refusal is journalled) and return false.
type preStep func(a *auditedRoute, w http.ResponseWriter, r *http.Request) (*http.Request, bool)

// audited wraps a write route's handler: it reads the target, runs the
// handler into a buffer, records one entry from the outcome, and only then
// answers the client, so an action is never answered before it is
// journalled. A request the write policy refuses (authorizeWrite: no
// token, a cross-origin page) has its body neither read nor journalled:
// its entry says who, from where and which action, and every handler
// refuses it before reading anything. A write handler must therefore
// apply authorizeWrite (or a stricter check that includes it) before it
// reads its body: given an empty one, a handler that did not would
// answer 400 where it should have said 401.
//
// pre, at most one, is a preStep for a route whose credential is not the
// request's own (an ack link).
func (s *Server) audited(action string, tf targetFunc, h http.HandlerFunc, pre ...preStep) *auditedRoute {
	a := &auditedRoute{s: s, action: action, tf: tf, h: h}
	if len(pre) > 0 {
		a.pre = pre[0]
	}
	return a
}

func (a *auditedRoute) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if a.pre != nil {
		var ok bool
		if r, ok = a.pre(a, w, r); !ok {
			return
		}
	}
	r = a.s.withActor(r)
	if code, msg, known := a.s.preAuthorize(r, a.action); code != 0 {
		// A known person refused by role (or for a fresh password) is
		// journalled with what they tried to change; anyone else's body
		// is never read. Only a parse: nothing is looked up for a request
		// that will not run (no before, no device read). A body that is
		// not read (past refusedBodyMax, slower than refusedBodyWait, too
		// many refused reads at once, any read error) names only what the
		// path does, and the entry says the body went unread.
		var t auditTarget
		if rf := refusalTargets[a.action]; known && rf != nil {
			if body, ok := readRefusedBody(w, r); ok {
				t = rf(r, body)
			} else {
				t = rf(r, nil)
				t.detail = map[string]any{"body": "unread"}
			}
		}
		r.Body, r.ContentLength = http.NoBody, 0
		if a.s.policy == nil {
			// The handler answers, as it always has: it refuses first.
			a.answer(w, r, auditTarget{}, a.h)
			return
		}
		a.answer(w, r, t, func(w http.ResponseWriter, _ *http.Request) {
			// Nothing ran: a client that reads the outcome (the HMI kit's
			// substitutes and IO-Link parameters) must not take a refusal
			// by role as an action that may still take effect.
			w.Header().Set(outcomeHeader, "refused")
			WriteRefusal(w, code, msg)
		})
		return
	}
	r = withSeamDecision(r)
	var body []byte
	if r.Body != nil {
		body, _ = io.ReadAll(io.LimitReader(r.Body, maxWriteBody+1))
		r.Body.Close()
		if len(body) > maxWriteBody {
			a.answer(w, r, auditTarget{}, func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "request body over "+strconv.Itoa(maxWriteBody>>20)+" MiB", http.StatusRequestEntityTooLarge)
			})
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
	}
	var t auditTarget
	if a.tf != nil {
		t = a.tf(r, body)
	}
	if a.s.audit.down() {
		if journalStrict(a.action) {
			a.answer(w, r, t, func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{
					"error": "the audit journal cannot be written, so " + a.action + " is refused until it can be (operator actions, clearing forces and rolling back are still allowed)",
				})
			})
			return
		}
		w.Header().Set(auditJournalHeader, "down")
	}
	a.answer(w, r, t, a.h)
}

// refuse answers r with h instead of the route's handler, through the
// seam: a standby that cannot pass a write on still journals its answer.
func (a *auditedRoute) refuse(w http.ResponseWriter, r *http.Request, h http.HandlerFunc) {
	a.answer(w, a.s.withActor(r), auditTarget{}, h)
}

// auditedAnswer answers r with h; when r is for a write route, through
// that route's seam, so the answer is journalled.
func auditedAnswer(routes *http.ServeMux, w http.ResponseWriter, r *http.Request, h http.HandlerFunc) {
	if route, _ := routes.Handler(r); route != nil {
		if a, ok := route.(*auditedRoute); ok {
			a.refuse(w, r, h)
			return
		}
	}
	h(w, r)
}

// answer runs h into a buffer, records the entry, then answers.
func (a *auditedRoute) answer(w http.ResponseWriter, r *http.Request, t auditTarget, h http.HandlerFunc) {
	buf := &bufferedResponse{header: w.Header()}
	// The entry's id, decided now: a route that answers "outcome unknown"
	// links its later resolution to it (oplog_unknown.go).
	ref := seamRef{id: audit.NewID(a.s.audit.now()), action: a.action, target: capText(t.target, targetMax)}
	r = withSeamRef(r, ref)
	h(buf, r)
	status := buf.status
	if status == 0 {
		status = http.StatusOK
	}
	e := audit.Entry{
		ID:      ref.id,
		User:    a.s.principal(r),
		Source:  clientSource(r),
		Action:  a.action,
		Target:  capText(t.target, targetMax),
		Before:  t.before,
		After:   t.after,
		Detail:  t.detail,
		From:    a.s.clientAddr(r),
		Session: a.s.sessionOfEntry(r),
		Result:  resultOf(status),
	}
	if a.s.policy != nil && auth.Elevated(actionPerm[a.action]) {
		// Which credential an elevated action was allowed by (or refused
		// to): a browser's fresh password, or a token.
		d := map[string]any{"credential": credentialKind(a.s.actorOf(r))}
		for k, v := range e.Detail {
			d[k] = v
		}
		e.Detail = d
	}
	if buf.header.Get(outcomeHeader) == "unknown" {
		// Passed to the replicated log, not confirmed: it may still take
		// effect (substitutes_oplog.go).
		e.Result = audit.ResultUnknown
	}
	valueCap := valueMax
	if e.Result == audit.ResultOK {
		if t.done != nil {
			e.After = t.done()
		}
		if t.answered != nil {
			e.After = t.answered(buf.body.Bytes())
		}
	} else {
		valueCap = refusedValueMax
		if e.Detail == nil {
			e.Detail = map[string]any{}
		}
		e.Detail["status"] = status
		e.Detail["reason"] = reasonOf(buf.body.Bytes())
		if m := buf.header.Get(linkReasonHeader); m != "" && !mutServer("link_reason_html") {
			// A page (an ack link's, #715): its words, not its HTML.
			e.Detail["reason"] = capText(m, reasonMax)
		}
		if t.failed != nil {
			for k, v := range t.failed(buf.body.Bytes()) {
				e.Detail[k] = v
			}
		}
	}
	e.Before, e.After = audit.Cap(e.Before, valueCap), audit.Cap(e.After, valueCap)
	a.s.audit.record(e)
	buf.header.Del(linkReasonHeader) // the seam's, never the client's
	if buf.header.Get(providerAnswerHeader) != "" {
		// An SMS or voice provider (#715 inbound) acts on a TwiML reply
		// only from a 2xx: the entry keeps the refusal's status, the wire
		// says 200.
		buf.header.Del(providerAnswerHeader)
		status = http.StatusOK
	}
	w.WriteHeader(status)
	w.Write(buf.body.Bytes())
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

// principal is who the request acts as (principalOf): "anonymous",
// "token" or "token:<name>". Nothing the client names (an alarm's "by")
// is ever taken as the principal.
func (s *Server) principal(r *http.Request) string { return s.principalOf(r) }

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
	tags := s.rt.Tags()
	key := ir.NameKey(tags.ForceKey(name)) // by the table's own spelling of the address
	for k, v := range tags.ForcedValues() {
		if ir.NameKey(k) == key {
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

// decodeBody reads a request body into v as the handlers do, with a
// json.Decoder: the first JSON value, whatever follows it. json.Unmarshal
// refuses trailing bytes, so a body the handler accepted and acted on
// (`{"name":"SP","value":70} x`) would be journalled with no target.
func decodeBody(body []byte, v any) {
	json.NewDecoder(bytes.NewReader(body)).Decode(v)
}

func (s *Server) tagTarget(_ *http.Request, body []byte) auditTarget {
	var req writeTagRequest
	decodeBody(body, &req)
	return auditTarget{target: req.Name, before: s.valueOf(req.Name), after: req.Value}
}

func (s *Server) forceTarget(_ *http.Request, body []byte) auditTarget {
	var req forceRequest
	decodeBody(body, &req)
	return auditTarget{target: req.Name, before: s.forcedValue(req.Name), after: req.Value}
}

func (s *Server) unforceTarget(r *http.Request, _ []byte) auditTarget {
	name := r.PathValue("name")
	return auditTarget{target: name, before: s.forcedValue(name)}
}

func (s *Server) unforceAllTarget(*http.Request, []byte) auditTarget {
	return auditTarget{before: s.rt.Tags().ForcedValues()}
}

// refusedBodyWait bounds how long the seam waits for a refused request's
// body (a var so a test can shorten it).
var refusedBodyWait = 5 * time.Second

// refusedReads holds a slot for every refused body being read: past
// cap(refusedReads) at once, a refusal names only what its path does.
var refusedReads = make(chan struct{}, 16)

// readRefusedBody reads up to refusedBodyMax of a refused request's body,
// for its journal entry, and reports whether it got all of it. It never
// waits past refusedBodyWait: the read has its own goroutine, holding a
// refusedReads slot until it really ends (bounded by the connection's read
// deadline where the writer lets the seam set one), so a body that never
// comes cannot pile reads up past the cap.
//
// On the timeout path that goroutine may still be reading r.Body after the
// handler returns. net/http's body Close takes the same lock as Read, so
// on a writer with no read deadline the server's own close of that body
// waits on the read: at most cap(refusedReads) connections, one stuck
// each, until their clients send or go away.
func readRefusedBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	if r.Body == nil || r.Body == http.NoBody {
		return nil, true
	}
	select {
	case refusedReads <- struct{}{}:
	default:
		return nil, false
	}
	rc := http.NewResponseController(w)
	deadline := rc.SetReadDeadline(time.Now().Add(refusedBodyWait)) == nil
	type read struct {
		b   []byte
		err error
	}
	done := make(chan read, 1)
	body := r.Body
	go func() {
		defer func() { <-refusedReads }()
		b, err := io.ReadAll(io.LimitReader(body, refusedBodyMax+1))
		done <- read{b, err}
	}()
	timer := time.NewTimer(refusedBodyWait)
	defer timer.Stop()
	select {
	case got := <-done:
		body.Close()
		if deadline {
			_ = rc.SetReadDeadline(time.Time{})
		}
		return got.b, got.err == nil && len(got.b) <= refusedBodyMax
	case <-timer.C:
		return nil, false
	}
}

// refusalTargets name what a refused request tried to change, by parsing
// it alone: they read no state (no before value, no device), so a refusal
// costs the controller nothing but the parse. None is a *Server method:
// four (alarm ack and shelving, SFC) are the routes' own targetFuncs,
// which are parses too; the rest are separate because the routes' read
// state. An action not listed is journalled without a target.
// An ack link's refusals never reach the "alarm.ack" entry below with a
// form body: checkLink answers them itself, with its own target, before
// the seam's preAuthorize (acklink.go).
var refusalTargets = map[string]targetFunc{
	"tag.write":            nameValueTarget,
	"force.set":            nameValueTarget,
	"substitute.start":     nameValueTarget,
	"force.clear":          pathTarget("name"),
	"param.import":         paramImportRefusal,
	"alarm.ack":            ackTarget,
	"alarm.shelve":         shelveTarget,
	"alarm.unshelve":       unshelveTarget,
	"alarm.out-of-service": oosTarget,
	"alarm.in-service":     inServiceTarget,
	"sfc.step":             sfcTarget(true),
	"sfc.transition":       sfcTarget(false),
	"param.write": func(r *http.Request, body []byte) auditTarget {
		t := auditTarget{target: r.PathValue("port") + "/" + r.PathValue("ref")}
		if v, ok := decodeParamBody(body); ok {
			t.after = v
		}
		return t
	},
}

// nameValueTarget is {"name", "value"}: the target and what was asked.
func nameValueTarget(_ *http.Request, body []byte) auditTarget {
	var req struct {
		Name  string `json:"name"`
		Value any    `json:"value"`
	}
	decodeBody(body, &req)
	return auditTarget{target: req.Name, after: req.Value}
}

func pathTarget(key string) targetFunc {
	return func(r *http.Request, _ []byte) auditTarget { return auditTarget{target: r.PathValue(key)} }
}

func sfcTarget(step bool) targetFunc {
	return func(_ *http.Request, body []byte) auditTarget {
		var req sfcCommand
		decodeBody(body, &req)
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
	decodeBody(body, &req)
	target := strings.Join(req.IDs, ",")
	t := auditTarget{target: target, detail: byDetail(req.By)}
	if occ := namedOccurrences(req); len(occ) > 0 {
		if t.detail == nil {
			t.detail = map[string]any{}
		}
		t.detail["occurrences"] = occ
	}
	if req.everything() {
		// One entry for the one request, target "*"; what it acknowledged
		// goes in after (the alarm ids the answer lists), since "*" alone
		// would not say which alarms an operator accepted.
		t.target = "*"
		t.answered = func(body []byte) any {
			var a ackAnswer
			if json.Unmarshal(body, &a) != nil {
				return nil
			}
			if len(a.Skipped) > 0 {
				// An area-limited acknowledge-all: what was acknowledged,
				// and what was left alone and why.
				return map[string]any{"acked": a.IDs, "skipped": a.Skipped, "why": capText(a.SkippedWhy, 200)}
			}
			return a.IDs
		}
	}
	return t
}

// namedOccurrences is the occurrences an ack names for its own ids (#715),
// bounded: never the whole of a map a client may have padded.
func namedOccurrences(req ackRequest) map[string]string {
	if req.everything() || len(req.Occurrences) == 0 {
		return nil
	}
	out := map[string]string{}
	for _, id := range req.IDs {
		if occ, ok := req.Occurrences[id]; ok && len(out) < 256 {
			out[id] = capText(occ, 32)
		}
	}
	return out
}

func shelveTarget(_ *http.Request, body []byte) auditTarget {
	var req shelveRequest
	decodeBody(body, &req)
	var after any
	if req.Until != "" {
		after = map[string]any{"until": req.Until}
	} else if req.Seconds != 0 {
		after = map[string]any{"seconds": req.Seconds}
	}
	detail := byDetail(req.By)
	if reason := strings.TrimSpace(req.Reason); reason != "" || req.Via != "" {
		if detail == nil {
			detail = map[string]any{}
		}
		if reason != "" {
			detail["reason"] = capText(reason, shelveReasonMax*4) // the cap counts bytes; the route allows 200 characters
		}
		if req.Via != "" {
			detail["via"] = capText(req.Via, byMax)
		}
	}
	return auditTarget{target: req.ID, after: after, detail: detail}
}

func unshelveTarget(_ *http.Request, body []byte) auditTarget {
	var req unshelveRequest
	decodeBody(body, &req)
	return auditTarget{target: req.ID, detail: byDetail(req.By)}
}

func oosTarget(_ *http.Request, body []byte) auditTarget {
	var req oosRequest
	decodeBody(body, &req)
	after := map[string]any{"reason": capText(req.Reason, alarm.MaxOutOfServiceReason)}
	if req.Until != "" {
		after["until"] = req.Until
	} else if req.Seconds != 0 {
		after["seconds"] = req.Seconds
	}
	return auditTarget{target: req.Equipment, after: after}
}

// inServiceTarget's after is which of the equipment's alarms the person
// may acknowledge and which not (back in service needs ack over one).
func inServiceTarget(_ *http.Request, body []byte) auditTarget {
	var req inServiceRequest
	decodeBody(body, &req)
	return auditTarget{target: req.Equipment, answered: func(b []byte) any {
		var a oosAnswer
		if json.Unmarshal(b, &a) != nil {
			return nil
		}
		return map[string]any{"mayAck": a.MayAck, "mayNotAck": a.MayNotAck}
	}}
}

// programTarget names the task an edit goes to, as handlePutProgram
// resolves it. The source itself is never journalled: before and after
// are the program hashes GET /api/program shows, and detail has the
// source's full SHA-256 and length.
func (s *Server) programTarget(r *http.Request, body []byte) auditTarget {
	var req putProgramRequest
	decodeBody(body, &req)
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
	decodeBody(body, &req)
	s.mu.Lock()
	before := s.active
	s.mu.Unlock()
	t := auditTarget{target: req.Sha, after: req.Sha}
	if before != "" {
		t.before = before
	}
	return t
}

// ioStageTarget is a live I/O stage (#706): the commit it is staged from,
// or "upload" with the driver and the files' names and size (never their
// contents); after is the id and state the staged candidate was given.
func ioStageTarget(_ *http.Request, body []byte) auditTarget {
	var req stageRequest
	decodeBody(body, &req)
	t := auditTarget{target: req.Sha, answered: stagedState}
	if req.Sha == "" {
		t.target = "upload"
		names, n := make([]string, 0, len(req.Files)), 0
		for p, c := range req.Files {
			names = append(names, p)
			n += len(c)
		}
		sort.Strings(names)
		t.detail = map[string]any{"driver": capText(req.Driver, byMax), "files": capText(strings.Join(names, ","), reasonMax), "bytes": n}
	}
	return t
}

// ioStagedTarget is a validate or apply of the staged candidate named in
// the path; after is the state it reached.
func ioStagedTarget(r *http.Request, _ []byte) auditTarget {
	return auditTarget{target: r.PathValue("id"), answered: stagedState}
}

// ioApplyTarget is an apply: after is the state it reached plus what it
// changed (each unit's before and after hash, what the HTTP guide
// says they hash), where the candidate came from, and whether the change
// is kept across a restart.
func ioApplyTarget(r *http.Request, _ []byte) auditTarget {
	return auditTarget{target: r.PathValue("id"), answered: appliedState, failed: failedApply}
}

// failedApply reads a failed apply's answer: where the candidate came from
// and the changes validation computed for it (omitted when there are none).
func failedApply(body []byte) map[string]any {
	var st struct {
		From       string `json:"from"`
		Validation *struct {
			Changes []struct {
				Driver string `json:"driver"`
				Device string `json:"device"`
				Before string `json:"before"`
				After  string `json:"after"`
			} `json:"changes"`
		} `json:"validation"`
	}
	if json.Unmarshal(body, &st) != nil || st.From == "" {
		return nil
	}
	out := map[string]any{"from": capText(st.From, byMax)}
	if v := st.Validation; v != nil && len(v.Changes) > 0 {
		ch := make([]map[string]string, 0, len(v.Changes))
		for _, c := range v.Changes {
			m := map[string]string{"driver": c.Driver, "before": c.Before, "after": c.After}
			if c.Device != "" {
				m["device"] = c.Device
			}
			ch = append(ch, m)
		}
		out["changes"] = ch
	}
	return out
}

func appliedState(body []byte) any {
	var st struct {
		ID     string `json:"id"`
		State  string `json:"state"`
		From   string `json:"from"`
		Result *struct {
			Changes []struct {
				Driver string `json:"driver"`
				Device string `json:"device"`
				Before string `json:"before"`
				After  string `json:"after"`
			} `json:"changes"`
			Kept  bool `json:"kept"`
			Watch *struct {
				Revert bool   `json:"revert"`
				State  string `json:"state"`
			} `json:"watch"`
		} `json:"result"`
	}
	if json.Unmarshal(body, &st) != nil || st.State == "" {
		return nil
	}
	out := map[string]any{"id": st.ID, "state": st.State, "from": capText(st.From, byMax)}
	if r := st.Result; r != nil {
		ch := make([]map[string]string, 0, len(r.Changes))
		for _, c := range r.Changes {
			m := map[string]string{"driver": c.Driver, "before": c.Before, "after": c.After}
			if c.Device != "" {
				m["device"] = c.Device
			}
			ch = append(ch, m)
		}
		out["changes"], out["kept"] = ch, r.Kept
		if r.Watch != nil {
			out["revertIfSilent"] = r.Watch.Revert
		}
	}
	return out
}

// configCommitTarget is a commit of an applied candidate: the target is the
// candidate, and the entry's after is the commit it made (and the branch and
// files), read from the answer.
func configCommitTarget(_ *http.Request, body []byte) auditTarget {
	var req commitRequest
	decodeBody(body, &req)
	return auditTarget{target: req.ID, answered: func(b []byte) any {
		var c IOCommitted
		if json.Unmarshal(b, &c) != nil || c.Commit == "" {
			return nil
		}
		return map[string]any{"commit": c.Commit, "branch": capText(c.Branch, byMax), "files": capText(strings.Join(c.Files, ","), reasonMax)}
	}}
}

// stagedState reads a staged candidate's id and state from an answer.
func stagedState(body []byte) any {
	var st struct {
		ID    string `json:"id"`
		State string `json:"state"`
	}
	if json.Unmarshal(body, &st) != nil || st.State == "" {
		return nil
	}
	return map[string]any{"id": st.ID, "state": st.State}
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
	if s.policy != nil {
		// With an auth: block, reading the journal is the audit permission
		// (design §6 Q1).
		if code, msg := s.authorize(r, PermAudit, ""); code != 0 {
			writeJSON(w, code, map[string]string{"error": msg})
			return
		}
	} else if s.authToken != "" && !tokenMatches(r, s.authToken) {
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
			!matchesUser(q.Get("user"), e.User) || !matches(q.Get("action"), e.Action) ||
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

// matchesUser is the user filter: the full principal ("user:alice"), or the
// bare name the Who column shows ("alice"), which matches a person's
// principal only: "token" or "system" are not names, and a bare "user"
// does not match everyone.
func matchesUser(want, got string) bool {
	if matches(want, got) {
		return true
	}
	name, ok := strings.CutPrefix(got, "user:")
	return ok && strings.EqualFold(want, name)
}
