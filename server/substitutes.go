package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/audit"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/runtime"
)

// Substitute values (#707): while a sensor is bad, or plausibly wrong, the
// program runs on a value someone chose, flagged as such everywhere, for a
// bounded time.
//
//	GET    /api/substitutes        the active ones, and the rules that say what may be:
//	                               {"substitutes": [{id, name, value, live, liveQuality, by, startMs,
//	                               untilMs, grantedMs, remainingMs, ackOnStart, acked, role, maxMs,
//	                               operatorValue}], "rules": [{tag, value, maxMs, role, ackOnStart,
//	                               operatorValue}]}
//	POST   /api/substitutes        {"name": "LS_1", "value"?: …, "seconds"?: n} — start one. The
//	                               value is the rule's unless the rule has operator-value: true;
//	                               seconds defaults to the rule's max and may not exceed it
//	DELETE /api/substitutes/{id}   end one (404 when no such entry is active)
//	POST   /api/substitutes/ack    {"ids": [1, 2]} — acknowledge the ones that wait for it
//
// Every mutating call is a write: it passes s.authorize (today the write
// policy of POST /api/tags), and leaves one audit entry through the seam
// (substitute.start / .stop / .ack, the target the tag). An expiry has no
// request: the server journals it directly (substitute.expire, by
// "system"). A start and an acknowledgement are elevated actions, refused
// while the journal cannot be written; a stop is a way back to normal, and
// is allowed (recovery, as clearing a force is).

// SubstituteRule is one substitutable address as the server answers for
// it: what a manifest's `substitutes:` rule resolved to (Options.Substitutes).
type SubstituteRule struct {
	// Tag is the substitutable tag, or a member path of one.
	Tag string `json:"tag"`
	// Value is the substitute the program runs on.
	Value any `json:"value"`
	// MaxMs is the longest grant.
	MaxMs int64 `json:"maxMs"`
	// Role is the role allowed to start one (shown now, enforced with
	// roles, #708).
	Role          string `json:"role"`
	AckOnStart    bool   `json:"ackOnStart"`
	OperatorValue bool   `json:"operatorValue"`
	// Rule is the index of the manifest rule that decided it.
	Rule int `json:"rule"`
}

type substituteRequest struct {
	Name    string   `json:"name"`
	Value   any      `json:"value"`
	Seconds *float64 `json:"seconds"`
	// RequestID makes a retry idempotent: a start retried after an
	// unknown outcome with the same ID is answered with the entry the
	// first one started, never a second entry.
	RequestID string `json:"requestId"`
}

type substituteView struct {
	runtime.Substitution
	Role          string `json:"role,omitempty"`
	MaxMs         int64  `json:"maxMs,omitempty"`
	OperatorValue bool   `json:"operatorValue,omitempty"`
}

type substitutesResponse struct {
	Substitutes []substituteView `json:"substitutes"`
	Rules       []SubstituteRule `json:"rules"`
	// Started is the entry a POST just began.
	Started *substituteView `json:"started,omitempty"`
	// Kept says whether the change this answer reports is saved in the
	// retain store, so it survives a restart or takeover. A start is
	// always kept (one that cannot be saved is refused); a stop or an
	// acknowledgement that could not be saved still took effect, and
	// __Substitute_Fault says so until the next save.
	Kept *bool `json:"kept,omitempty"`
	// Warning says what an answer with kept false means for the plant.
	Warning string `json:"warning,omitempty"`
	// Lapsed are the recent substitute starts a redundant pair's log
	// committed too late to carry out (runtime.SubstituteLapse): never
	// started, or ended at a takeover; Note says which.
	Lapsed []lapseView `json:"lapsed,omitempty"`
}

// lapseView is a lapsed start as GET /api/substitutes lists it.
type lapseView struct {
	runtime.SubstituteLapse
	Note string `json:"note"`
}

// Lapse notes, as the HMI and the journal say them.
const (
	lapseNotStarted = "lapsed (outcome was unknown; not started)"
	lapseEnded      = "substitute ended: lapsed at takeover (its commit could not be confirmed)"
)

// substituteStartRefusal is the answer to a refused start: 503 when it
// could not be saved (nothing runs; retry), 409 when a newer change to
// the address won while it was being saved (re-read and decide again),
// 400 for a start that was never valid.
func substituteStartRefusal(err error) (int, string) {
	switch {
	case errors.Is(err, runtime.ErrSubstituteNotSaved):
		return http.StatusServiceUnavailable, err.Error()
	case errors.Is(err, runtime.ErrSubstituteSuperseded):
		return http.StatusConflict, err.Error() + ": re-read GET /api/substitutes and decide again"
	}
	return http.StatusBadRequest, err.Error()
}

// notKept is the warning for a stop or acknowledgement that took effect
// here but was not saved.
func notKept(serr error) string {
	if errors.Is(serr, runtime.ErrNotLeading) {
		return "this controller lost the lease during the save: the change took effect here, but the substitute may still be active (or unacknowledged) on the next leader"
	}
	return "the change took effect but could not be saved (__Substitute_Fault is up): a restart before the next save may bring the old state back"
}

func (s *Server) substituteRule(name string) (SubstituteRule, bool) {
	for _, r := range s.subRules {
		if strings.EqualFold(r.Tag, name) {
			return r, true
		}
	}
	return SubstituteRule{}, false
}

func (s *Server) substituteView(e runtime.Substitution) substituteView {
	v := substituteView{Substitution: e}
	if r, ok := s.substituteRule(e.Name); ok {
		v.Role, v.MaxMs, v.OperatorValue = r.Role, r.MaxMs, r.OperatorValue
	}
	return v
}

func (s *Server) substitutesResponse() substitutesResponse {
	resp := substitutesResponse{Substitutes: []substituteView{}, Rules: s.subRules}
	if resp.Rules == nil {
		resp.Rules = []SubstituteRule{}
	}
	for _, e := range s.rt.Substitutes() {
		resp.Substitutes = append(resp.Substitutes, s.substituteView(e))
	}
	for _, l := range s.rt.SubstituteLapses() {
		note := lapseNotStarted
		if l.Ended {
			note = lapseEnded
		}
		resp.Lapsed = append(resp.Lapsed, lapseView{SubstituteLapse: l, Note: note})
	}
	return resp
}

func (s *Server) handleSubstitutes(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.substitutesResponse())
}

// refuseSubstitute answers when this server cannot hold a substitute at
// all: one that fronts a controller it does not run.
func (s *Server) refuseSubstitute(w http.ResponseWriter) bool {
	if s.tagWriter != nil {
		refuse(w, "this server mirrors a controller it does not run — substitute on that controller", http.StatusNotImplemented)
		return true
	}
	return false
}

// substituteSaveWait bounds how long a substitute request waits for the
// saver to take its save; the store bounds the save itself.
const substituteSaveWait = 10 * time.Second

// refuseNotLeading answers 503 when this replica does not lead: a start,
// stop or acknowledgement it took would be undone at its next takeover
// (the shared store's set replaces its table). The request reaches a
// standby only when no leader is known to forward it to.
func (s *Server) refuseNotLeading(w http.ResponseWriter) bool {
	if s.rt.Leading() {
		return false
	}
	refuse(w, "no leader: this replica is standing by; retry once a leader is elected", http.StatusServiceUnavailable)
	return true
}

func (s *Server) handleSubstituteStart(w http.ResponseWriter, r *http.Request) {
	var req substituteRequest
	derr := json.NewDecoder(r.Body).Decode(&req)
	if code, msg := s.authorize(r, PermSubstitute, req.Name); code != 0 {
		refuse(w, msg, code)
		return
	}
	if s.refuseSubstitute(w) {
		return
	}
	if derr != nil || req.Name == "" {
		refuse(w, `expected {"name": ..., "value"?: ..., "seconds"?: ...}`, http.StatusBadRequest)
		return
	}
	if s.refuseNotLeading(w) {
		return
	}
	rule, ok := s.substituteRule(req.Name)
	if !ok {
		refuse(w, fmt.Sprintf("%s is not substitutable: no substitutes: rule names it", req.Name), http.StatusBadRequest)
		return
	}
	value := rule.Value
	if req.Value != nil && fmt.Sprint(req.Value) != fmt.Sprint(rule.Value) {
		if !rule.OperatorValue {
			refuse(w, fmt.Sprintf("the rule for %s substitutes %v and does not let the operator choose another value (operator-value: true)", rule.Tag, rule.Value), http.StatusForbidden)
			return
		}
		value = req.Value
	}
	d := time.Duration(rule.MaxMs) * time.Millisecond
	if req.Seconds != nil {
		// Compared as seconds, before any conversion to a Duration: a huge
		// value overflows the conversion into a misleading "not positive".
		switch sec := *req.Seconds; {
		case math.IsNaN(sec):
			refuse(w, "seconds is not a number", http.StatusBadRequest)
			return
		case sec > d.Seconds():
			refuse(w, fmt.Sprintf("seconds exceeds the rule's maximum of %s", d), http.StatusBadRequest)
			return
		case sec < 1:
			refuse(w, "seconds must be at least 1", http.StatusBadRequest)
			return
		default:
			d = time.Duration(sec * float64(time.Second))
		}
	}
	if req.RequestID != "" {
		if e, ok := s.rt.SubstituteByRequest(req.RequestID); ok {
			if !runtime.SameRequest(e, rule.Tag, value, d) {
				refuse(w, fmt.Sprintf("%v: entry %d was started with it for something else", runtime.ErrRequestReused, e.ID), http.StatusConflict)
				return
			}
			// A retry of a start that did start: the same entry, not a second.
			resp := s.substitutesResponse()
			v := s.substituteView(e)
			resp.Started = &v
			kept := true
			resp.Kept = &kept
			writeJSON(w, http.StatusOK, resp)
			return
		}
	}
	if s.subLog != nil {
		s.startThroughLog(w, r, rule.Tag, value, d, rule.AckOnStart, req.RequestID)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), substituteSaveWait)
	defer cancel()
	e, err := s.rt.SubstituteSaved(ctx, rule.Tag, runtime.SubstituteRequest{Value: value, For: d, By: s.principal(r), AckOnStart: rule.AckOnStart, RequestID: req.RequestID})
	if err != nil {
		code, msg := substituteStartRefusal(err)
		refuse(w, msg, code)
		return
	}
	logAction(r, "substitute: started", "tag", e.Name, "id", e.ID, "for", d)
	kept := true
	resp := s.substitutesResponse()
	v := s.substituteView(e)
	resp.Started = &v
	resp.Kept = &kept
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleSubstituteStop(w http.ResponseWriter, r *http.Request) {
	id, idErr := strconv.ParseUint(r.PathValue("id"), 10, 64)
	name, _ := s.substituteByID(id)
	if code, msg := s.authorize(r, PermSubstitute, name); code != 0 {
		refuse(w, msg, code)
		return
	}
	if s.refuseSubstitute(w) {
		return
	}
	if idErr != nil {
		refuse(w, "the substitute's id is a number", http.StatusBadRequest)
		return
	}
	if s.refuseNotLeading(w) {
		return
	}
	if s.subLog != nil {
		s.stopThroughLog(w, r, id)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), substituteSaveWait)
	defer cancel()
	e, ok, kept, serr := s.rt.RestoreSaved(ctx, id, s.principal(r))
	if !ok {
		refuse(w, fmt.Sprintf("no substitute %d is active (it ended, expired, or a newer one replaced it)", id), http.StatusNotFound)
		return
	}
	logAction(r, "substitute: ended", "tag", e.Name, "id", e.ID, "kept", kept, "saveError", serr)
	resp := s.substitutesResponse()
	resp.Kept = &kept
	if !kept {
		resp.Warning = notKept(serr)
	}
	writeJSON(w, http.StatusOK, resp)
}

type substituteAckRequest struct {
	IDs []uint64 `json:"ids"`
}

func (s *Server) handleSubstituteAck(w http.ResponseWriter, r *http.Request) {
	var req substituteAckRequest
	derr := json.NewDecoder(r.Body).Decode(&req)
	if code, msg := s.authorize(r, PermSubstitute, s.substituteNames(req.IDs)); code != 0 {
		refuse(w, msg, code)
		return
	}
	if s.refuseSubstitute(w) {
		return
	}
	if derr != nil || len(req.IDs) == 0 {
		refuse(w, `expected {"ids": [1, 2]}: an empty list acknowledges nothing`, http.StatusBadRequest)
		return
	}
	if s.refuseNotLeading(w) {
		return
	}
	if s.subLog != nil {
		s.ackThroughLog(w, r, req.IDs)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), substituteSaveWait)
	defer cancel()
	acked, kept, serr := s.rt.AckSubstitutesSaved(ctx, req.IDs, s.principal(r))
	if len(acked) == 0 {
		refuse(w, "nothing to acknowledge: those substitutes ended, were replaced, do not wait for an acknowledgement, or already have one", http.StatusConflict)
		return
	}
	logAction(r, "substitute: acknowledged", "count", len(acked), "kept", kept, "saveError", serr)
	resp := s.substitutesResponse()
	resp.Kept = &kept
	if !kept {
		resp.Warning = notKept(serr)
	}
	writeJSON(w, http.StatusOK, resp)
}

// substituteByID finds the active entry with this ID.
func (s *Server) substituteByID(id uint64) (string, runtime.Substitution) {
	for _, e := range s.rt.Substitutes() {
		if e.ID == id {
			return e.Name, e
		}
	}
	return "", runtime.Substitution{}
}

// substituteNames is the tags of the active entries with these IDs,
// comma-separated, for a target.
func (s *Server) substituteNames(ids []uint64) string {
	var names []string
	for _, id := range ids {
		if n, _ := s.substituteByID(id); n != "" {
			names = append(names, n)
		}
	}
	return strings.Join(names, ",")
}

// The audit targets of the substitute routes.

// substituteLive is what an address is without any substitute: its stored
// value and quality, or, when it is substituted already, the entry's own
// record of the live reading (the stored view is the substitute).
func (s *Server) substituteLive(name string) any {
	root, _ := ir.SplitAddress(name)
	value, quality := s.valueOf(name), s.rt.TagQuality(root).String()
	for _, e := range s.rt.Substitutes() {
		eroot, _ := ir.SplitAddress(e.Name)
		if !strings.EqualFold(eroot, root) {
			continue
		}
		quality = e.LiveQuality // the root's quality without its substitutes
		if strings.EqualFold(e.Name, name) {
			value = e.Live
		}
	}
	return map[string]any{"value": value, "quality": quality}
}

func (s *Server) substituteStartTarget(_ *http.Request, body []byte) auditTarget {
	var req substituteRequest
	decodeBody(body, &req)
	t := auditTarget{target: req.Name}
	rule, ok := s.substituteRule(req.Name)
	if !ok {
		return t
	}
	t.before = s.substituteLive(req.Name)
	t.after = req.Value
	if t.after == nil {
		t.after = rule.Value
	}
	t.detail = map[string]any{"rule": rule.Rule, "role": rule.Role, "ackOnStart": rule.AckOnStart, "maxMs": rule.MaxMs}
	// Starting an address that already has a substitute ends that entry
	// and begins a new one: the entry it replaces is named here, since its
	// end is not a request of its own.
	for _, e := range s.rt.Substitutes() {
		if strings.EqualFold(e.Name, req.Name) {
			t.detail["replaces"] = map[string]any{"id": e.ID, "by": e.By, "value": e.Value, "untilMs": e.UntilMs}
		}
	}
	t.answered = func(body []byte) any {
		var resp struct {
			Started *runtime.Substitution `json:"started"`
		}
		if json.Unmarshal(body, &resp) != nil || resp.Started == nil {
			return nil
		}
		return map[string]any{"id": resp.Started.ID, "value": resp.Started.Value, "untilMs": resp.Started.UntilMs, "grantedMs": resp.Started.GrantedMs}
	}
	return t
}

func (s *Server) substituteStopTarget(r *http.Request, _ []byte) auditTarget {
	id, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
	if err != nil {
		return auditTarget{}
	}
	name, e := s.substituteByID(id)
	if name == "" {
		return auditTarget{}
	}
	return auditTarget{target: name, before: map[string]any{"id": id, "value": e.Value, "live": e.Live, "liveQuality": e.LiveQuality}}
}

func (s *Server) substituteAckTarget(_ *http.Request, body []byte) auditTarget {
	var req substituteAckRequest
	decodeBody(body, &req)
	return auditTarget{target: s.substituteNames(req.IDs), detail: map[string]any{"ids": req.IDs}}
}

// auditSubstituteEvent journals what the runtime did on its own:
//
//   - an expiry (substitute.expire) has no request, so it is recorded here,
//     by "system", with the value the program ran on and the live value it
//     went back to; its reason says expired, expired-while-down (a restart)
//     or refused-at-load (a deploy no longer allows it);
//
//   - a fault and its clearing (substitute.fault, substitute.fault-clear),
//     so a transient __Substitute_Fault is never invisible: the alarm
//     layer latches, the journal records.
//
//   - a takeover's superseded stop (substitute.stop by system): the entry
//     was not in the shared store, and if it began just before this
//     replica lost the lease, nobody else recorded its end.
//
// A start, stop and acknowledgement are already journalled by the route
// that made them.
func (s *Server) auditSubstituteEvent(ev runtime.SubstituteEvent) {
	if ev.Replicated {
		return // an oplog entry applied here: the instance that took it journals it
	}
	e := ev.Entry
	switch ev.Kind {
	case runtime.SubstituteEventExpire:
		s.audit.record(audit.Entry{
			TS:     time.UnixMilli(ev.AtMs),
			User:   ev.By,
			Source: audit.SourceSystem,
			Action: "substitute.expire",
			Target: e.Name,
			Before: map[string]any{"id": e.ID, "value": e.Value, "by": e.By},
			After:  map[string]any{"live": e.Live},
			Detail: map[string]any{"reason": ev.Reason, "grantedMs": e.GrantedMs, "untilMs": e.UntilMs},
			Result: audit.ResultOK,
		})
	case runtime.SubstituteEventLapse:
		s.audit.record(audit.Entry{
			TS:     time.UnixMilli(ev.AtMs),
			User:   ev.By,
			Source: audit.SourceSystem,
			Action: "substitute.lapse",
			Target: e.Name,
			Before: map[string]any{"id": e.ID, "value": e.Value, "by": e.By, "proposedMs": e.StartMs},
			Detail: map[string]any{"reason": ev.Reason, "note": lapseNotStarted},
			Result: audit.ResultOK,
		})
	case runtime.SubstituteEventStop:
		if ev.Reason != runtime.SubstituteReasonSuperseded && ev.Reason != runtime.SubstituteReasonLapsed {
			return // a person's stop or a replace: its route journalled it
		}
		detail := map[string]any{"reason": ev.Reason, "untilMs": e.UntilMs}
		if ev.Reason == runtime.SubstituteReasonLapsed {
			detail["note"] = lapseEnded
		}
		s.audit.record(audit.Entry{
			TS:     time.UnixMilli(ev.AtMs),
			User:   ev.By,
			Source: audit.SourceSystem,
			Action: "substitute.stop",
			Target: e.Name,
			Before: map[string]any{"id": e.ID, "value": e.Value, "by": e.By},
			After:  map[string]any{"live": e.Live},
			Detail: detail,
			Result: audit.ResultOK,
		})
	case runtime.SubstituteEventFault, runtime.SubstituteEventFaultClear:
		detail := map[string]any{"reason": ev.Reason}
		if e.ID != 0 {
			detail["id"], detail["untilMs"], detail["grantedMs"] = e.ID, e.UntilMs, e.GrantedMs
		}
		s.audit.record(audit.Entry{
			TS:     time.UnixMilli(ev.AtMs),
			User:   ev.By,
			Source: audit.SourceSystem,
			Action: "substitute." + ev.Kind,
			Target: e.Name,
			Detail: detail,
			Result: audit.ResultOK,
		})
	}
}

// refuse answers a request that was refused BEFORE it could change anything,
// and says so in a header: only an answer carrying X-Nautilus-Outcome:
// refused is certain. Any other failure (a 5xx from a proxy, a dropped
// connection) leaves the outcome unknown to a client, which must then
// re-read and retry with the same requestId.
func refuse(w http.ResponseWriter, msg string, code int) {
	w.Header().Set(outcomeHeader, "refused")
	http.Error(w, msg, code)
}
