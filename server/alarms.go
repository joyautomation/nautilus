package server

// The alarm API: the active list, the journal, and the three operator
// verbs. Five routes, all GET/POST, registered beside /api/drivers.
//
//	GET  /api/alarms                    active + unack-RTN + shelved, plus the summary
//	GET  /api/alarms/journal?from&to&…  the append-only event log
//	POST /api/alarms/ack       {"ids":[…]|{"all":true}, "by":"rchon"}
//	POST /api/alarms/shelve    {"id":…, "until"|"seconds":…, "by":…}
//	POST /api/alarms/unshelve  {"id":…, "by":…}
//
// Reads are ungated like every other read; writes go through authorizeWrite
// unchanged — same Bearer/X-Nautilus-Token rules, same same-origin base
// layer, same proxyStandby forwarding as a tag write, because an ack made
// against a standby has to reach the replica that is actually scanning.
//
// `by` is an AUDIT STRING the server does not authenticate: nautilus has
// one token, not user accounts. The HMI supplies the operator's name and
// the journal records what it was told. Saying that plainly beats implying
// an identity the system does not have.
//
// With no engine configured every route answers 404, which is the answer
// the HMI kit's AlarmClient is written against: it flips `supported` to
// false and renders nothing, rather than showing an empty alarm list that
// looks like a quiet plant.

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/alarm"
)

// noAlarms is the 404 body. It names the manifest key, because "404" on a
// route the HMI just asked for is otherwise indistinguishable from a
// version mismatch.
const noAlarms = "this controller has no alarm engine (add an `alarms:` section to nautilus.yaml)"

// alarmsOK answers the request with 404 unless an engine is configured.
func (s *Server) alarmsOK(w http.ResponseWriter) bool {
	if s.alarms == nil {
		http.Error(w, noAlarms, http.StatusNotFound)
		return false
	}
	return true
}

// alarmsResponse is GET /api/alarms.
type alarmsResponse struct {
	TS      int64          `json:"ts"`
	Summary alarm.Summary  `json:"summary"`
	Alarms  []alarm.Record `json:"alarms"`
}

func (s *Server) handleAlarms(w http.ResponseWriter, r *http.Request) {
	if !s.alarmsOK(w) {
		return
	}
	list := s.alarms.Active()
	if list == nil {
		list = []alarm.Record{}
	}
	writeJSON(w, http.StatusOK, alarmsResponse{
		TS:      time.Now().UnixMilli(),
		Summary: s.alarms.Summary(),
		Alarms:  list,
	})
}

// journalResponse is GET /api/alarms/journal. Truncated says the limit cut
// the result, so a journal page can offer "narrow the range" instead of
// silently showing a window that isn't the whole story.
type journalResponse struct {
	Events    []alarm.Event `json:"events"`
	Truncated bool          `json:"truncated"`
}

// defaultJournalSpan is how far back a journal query with no `from` looks.
// A day is what an operator means by "what happened".
const defaultJournalSpan = 24 * time.Hour

func (s *Server) handleAlarmJournal(w http.ResponseWriter, r *http.Request) {
	if !s.alarmsOK(w) {
		return
	}
	q := r.URL.Query()
	now := time.Now()
	to, err := parseTime(q.Get("to"), now)
	if err != nil {
		http.Error(w, "to: "+err.Error(), http.StatusBadRequest)
		return
	}
	from, err := parseTime(q.Get("from"), to.Add(-defaultJournalSpan))
	if err != nil {
		http.Error(w, "from: "+err.Error(), http.StatusBadRequest)
		return
	}
	limit := 0
	if v := q.Get("limit"); v != "" {
		if limit, err = strconv.Atoi(v); err != nil || limit < 0 {
			http.Error(w, "limit: want a non-negative integer", http.StatusBadRequest)
			return
		}
	}
	f := alarm.Filter{
		Sites:      csv(q.Get("site")),
		Priorities: csv(q.Get("priority")),
		IDs:        csv(q.Get("id")),
		Kinds:      csv(q.Get("kind")),
		Limit:      limit,
	}
	events, err := s.alarms.Journal(from, to, f)
	if err != nil {
		http.Error(w, "journal: "+err.Error(), http.StatusBadGateway)
		return
	}
	// state= and q= are applied here rather than in alarm.Filter: a state
	// is a property of the event's OUTCOME and a text search is a UI
	// affordance, and neither belongs in an interface three journal
	// implementations (ring, file, Postgres) each have to reimplement.
	events = filterEvents(events, csv(q.Get("state")), q.Get("q"))
	if events == nil {
		events = []alarm.Event{}
	}
	writeJSON(w, http.StatusOK, journalResponse{
		Events:    events,
		Truncated: len(events) >= f.Limit && f.Limit > 0 || len(events) >= alarm.DefaultQueryLimit,
	})
}

// filterEvents applies the two filters the Journal interface deliberately
// does not carry.
func filterEvents(events []alarm.Event, states []string, text string) []alarm.Event {
	if len(states) == 0 && text == "" {
		return events
	}
	text = strings.ToLower(text)
	out := events[:0]
	for _, e := range events {
		if len(states) > 0 && !containsFold(states, e.State) {
			continue
		}
		if text != "" &&
			!strings.Contains(strings.ToLower(e.Name), text) &&
			!strings.Contains(strings.ToLower(e.ID), text) {
			continue
		}
		out = append(out, e)
	}
	return out
}

func containsFold(set []string, v string) bool {
	for _, s := range set {
		if strings.EqualFold(s, v) {
			return true
		}
	}
	return false
}

// ackRequest is POST /api/alarms/ack. Either a list of ids, or all: true —
// the "acknowledge everything" button, which is a real thing operators do
// and deserves to be one request rather than two thousand.
type ackRequest struct {
	IDs []string `json:"ids"`
	All bool     `json:"all"`
	By  string   `json:"by"`
}

func (s *Server) handleAlarmAck(w http.ResponseWriter, r *http.Request) {
	if code, msg := s.authorizeWrite(r); code != 0 {
		http.Error(w, msg, code)
		return
	}
	if !s.alarmsOK(w) {
		return
	}
	var req ackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `expected {"ids": [...], "by": "..."} or {"all": true, "by": "..."}`, http.StatusBadRequest)
		return
	}
	ids := req.IDs
	if req.All {
		ids = nil // nil means everything, per Engine.Ack
	} else if len(ids) == 0 {
		http.Error(w, `ack needs "ids" or "all": true — an empty ids list would silently ack nothing`,
			http.StatusBadRequest)
		return
	}
	n, err := s.alarms.Ack(ids, req.By)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"acked": n})
}

// shelveRequest is POST /api/alarms/shelve. Until is an RFC3339 timestamp
// or epoch milliseconds; Seconds is the duration-picker form ("shelve for
// 30m"), resolved against the server's clock. One or the other.
type shelveRequest struct {
	ID      string  `json:"id"`
	Until   string  `json:"until"`
	Seconds float64 `json:"seconds"`
	By      string  `json:"by"`
}

func (s *Server) handleAlarmShelve(w http.ResponseWriter, r *http.Request) {
	if code, msg := s.authorizeWrite(r); code != 0 {
		http.Error(w, msg, code)
		return
	}
	if !s.alarmsOK(w) {
		return
	}
	var req shelveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		http.Error(w, `expected {"id": "...", "until"|"seconds": ..., "by": "..."}`, http.StatusBadRequest)
		return
	}
	var until time.Time
	switch {
	case req.Until != "":
		t, err := parseTime(req.Until, time.Time{})
		if err != nil {
			http.Error(w, "until: "+err.Error(), http.StatusBadRequest)
			return
		}
		until = t
	case req.Seconds > 0:
		until = time.Now().Add(time.Duration(req.Seconds * float64(time.Second)))
	default:
		http.Error(w, `shelve needs "until" (RFC3339 or epoch ms) or "seconds" — there is no permanent shelf`,
			http.StatusBadRequest)
		return
	}
	if err := s.alarms.Shelve(req.ID, until, req.By); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.writeAlarmRecord(w, req.ID)
}

type unshelveRequest struct {
	ID string `json:"id"`
	By string `json:"by"`
}

func (s *Server) handleAlarmUnshelve(w http.ResponseWriter, r *http.Request) {
	if code, msg := s.authorizeWrite(r); code != 0 {
		http.Error(w, msg, code)
		return
	}
	if !s.alarmsOK(w) {
		return
	}
	var req unshelveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		http.Error(w, `expected {"id": "...", "by": "..."}`, http.StatusBadRequest)
		return
	}
	if err := s.alarms.Unshelve(req.ID, req.By); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.writeAlarmRecord(w, req.ID)
}

// writeAlarmRecord answers a shelve/unshelve with the row as it now
// stands, so the caller does not have to refetch the whole list to render
// one changed cell.
func (s *Server) writeAlarmRecord(w http.ResponseWriter, id string) {
	for _, rec := range s.alarms.Records() {
		if rec.ID == id {
			writeJSON(w, http.StatusOK, rec)
			return
		}
	}
	// Unreachable: Shelve/Unshelve already resolved the id.
	http.Error(w, "no alarm "+id, http.StatusNotFound)
}

// alarmSummary is the frame's optional alarms field: counts only, never
// two thousand rows on a 250 ms tick. Nil when no engine is configured, so
// an older HMI and a controller without alarms see exactly the frame they
// saw before.
func (s *Server) alarmSummary() *alarm.Summary {
	if s.alarms == nil {
		return nil
	}
	sum := s.alarms.Summary()
	return &sum
}

// ── small helpers ──────────────────────────────────────────────────────

// parseTime accepts epoch milliseconds (what every other timestamp in this
// API is) or RFC3339 (what a human types and what JavaScript's toISOString
// emits). Empty returns the fallback.
func parseTime(s string, fallback time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return fallback, nil
	}
	if ms, err := strconv.ParseInt(s, 10, 64); err == nil {
		return time.UnixMilli(ms), nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, errBadTime
	}
	return t, nil
}

// errBadTime is a fixed message rather than time's own, which quotes the
// reference layout at a caller who never wrote it.
var errBadTime = errors.New("want epoch milliseconds or an RFC3339 timestamp")

// csv splits a comma-separated query parameter, dropping blanks — so
// `?site=` means "any site", not "the site named empty string".
func csv(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
