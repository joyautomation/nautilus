package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// DuplicateClearer is a redundant pair's clear-duplicate command (#1029
// 3c-3a). An Options.Cluster value that also implements it (leader.Quorum
// does) gets POST /api/cluster/clear-duplicate. name is the marked member,
// by the principal, for the members' logs. outcome is "done" (no member
// marks the name a lease after the clear), "refused" (the check phase
// refused: nothing was cleared) or "incomplete" (cleared, but a member
// still marks the name or was not reached); report is the command's
// member-by-member report, as JSON, and reason one line saying why when
// the outcome is not "done".
type DuplicateClearer interface {
	ClearDuplicate(ctx context.Context, name, by string) (report any, outcome, reason string)
}

// clearDuplicateBody is POST /api/cluster/clear-duplicate's body.
type clearDuplicateBody struct {
	// Name is the member the alarm names ("two processes claim to be a").
	Name string `json:"name"`
	// CopyStopped is the operator's word that the copy is stopped and
	// confirmed down (naut-lead's condition (ii)): required, and journalled
	// with the principal. The members check what they can see on top.
	CopyStopped bool `json:"copyStopped"`
}

// localClusterOps are the pair's operator routes a standby answers itself
// (proxyStandby): named one by one, so a later /api/cluster/ route is
// not exempted by accident.
var localClusterOps = map[string]bool{
	"/api/cluster/clear-duplicate": true,
	"/api/cluster/commission":      true,
	"/api/cluster/recommission":    true,
}

// copyStoppedAttest is the journal's words for the operator's attestation.
const copyStoppedAttest = "the operator states the copy is stopped and confirmed down"

// clearDuplicateTarget reads the entry's target (the member's name) and
// the operator's attestation.
func (s *Server) clearDuplicateTarget(r *http.Request, body []byte) auditTarget {
	var b clearDuplicateBody
	// As the handler reads it: the first JSON value, whatever follows.
	_ = json.NewDecoder(bytes.NewReader(body)).Decode(&b)
	t := auditTarget{target: b.Name, detail: map[string]any{}}
	if b.CopyStopped {
		t.detail["attest"] = copyStoppedAttest
	}
	if st := s.clusterStatus(); st != nil {
		t.before = map[string]any{"marked": st.Marked, "held": st.Held}
	}
	t.answered = func(body []byte) any {
		// Numbers as written: a boot nonce is 64 bits, past a float's.
		var v any
		d := json.NewDecoder(bytes.NewReader(body))
		d.UseNumber()
		_ = d.Decode(&v)
		return v
	}
	t.failed = func(body []byte) map[string]any {
		var v map[string]any
		d := json.NewDecoder(bytes.NewReader(body))
		d.UseNumber()
		if d.Decode(&v) != nil {
			return nil
		}
		return map[string]any{"report": v["report"]}
	}
	return t
}

// handleClearDuplicate is the operator's clear-duplicate command. The
// audit seam has checked the redundancy permission (and, for a browser,
// a fresh password) under an auth: block. Without one, only NAUTILUS_TOKEN
// may: these commands decide which process a pair counts as its member, so
// a same-origin request with no credential, which other writes accept
// without an auth: block, is refused (naut-lead, 2026-11-20).
func (s *Server) handleClearDuplicate(w http.ResponseWriter, r *http.Request) {
	if code, msg := s.redundancyAuthorized(r); code != 0 {
		WriteRefusal(w, code, msg)
		return
	}
	c, ok := s.cluster.(DuplicateClearer)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "this controller is not a member of a redundant pair (redundancy: peers:)"})
		return
	}
	var b clearDuplicateBody
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&b); err != nil || b.Name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `want {"name": "<the member the alarm names>", "copyStopped": true}`})
		return
	}
	if !b.CopyStopped {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("stop the copy of %s and confirm it is down first, then say so: copyStopped: true (naut redundancy clear-duplicate --copy-stopped)", b.Name)})
		return
	}
	report, outcome, reason := c.ClearDuplicate(r.Context(), b.Name, s.principalOf(r))
	code := http.StatusOK
	switch outcome {
	case "refused":
		code = http.StatusConflict
	case "incomplete":
		// Something was cleared, but not everything: an error, so the
		// journal says so, with the report.
		code = http.StatusBadGateway
	}
	out := map[string]any{"outcome": outcome, "report": report}
	if code != http.StatusOK {
		out["error"] = reason
	}
	writeJSON(w, code, out)
}

// redundancyAuthorized is the redundancy permission's rule without an
// auth: block: NAUTILUS_TOKEN only. With one, the audit seam decided.
func (s *Server) redundancyAuthorized(r *http.Request) (int, string) {
	if s.policy != nil || mutServer("redundancy_same_origin") {
		return 0, "" // the seam checked PermRedundancy and elevation
	}
	if s.authToken == "" {
		return http.StatusForbidden, "the redundant pair's operator commands need a credential: start naut with NAUTILUS_TOKEN (or an auth: block) and send it as a bearer token"
	}
	if !tokenMatches(r, s.authToken) {
		return http.StatusUnauthorized, "missing or invalid auth token: the redundant pair's operator commands take NAUTILUS_TOKEN as a bearer token"
	}
	return 0, ""
}

// clusterStatus is the cluster's status when this server runs in one.
func (s *Server) clusterStatus() *clusterBrief {
	if s.cluster == nil {
		return nil
	}
	st := s.cluster.Status()
	return &clusterBrief{Marked: st.Marked, Held: st.Held}
}

type clusterBrief struct {
	Marked []string
	Held   bool
}

// Commissioner is a redundant pair's commissioning commands (#1029 3c-3c).
// An Options.Cluster value that also implements it (leader.Quorum does)
// gets POST /api/cluster/commission and /api/cluster/recommission. by is
// the principal. outcome is "done", "refused" (nothing changed) or
// "incomplete" (--from-spare cleared at one voter only); report is the
// command's report, as JSON, and reason one line saying why when the
// outcome is not "done".
type Commissioner interface {
	Commission(ctx context.Context, by string) (report any, outcome, reason string)
	Recommission(ctx context.Context, by string, incarnation, floor uint64, boxReplaced, fromSpare bool) (report any, outcome, reason string)
}

// recommissionBody is POST /api/cluster/recommission's body.
type recommissionBody struct {
	Incarnation uint64 `json:"incarnation,string,omitempty"`
	Floor       uint64 `json:"floor,omitempty"`
	BoxReplaced bool   `json:"boxReplaced,omitempty"`
	FromSpare   bool   `json:"fromSpare,omitempty"`
	// CopyStopped is the operator's word, required with FromSpare, that
	// the box the voters held under this name is stopped and confirmed
	// down. Journalled with the principal.
	CopyStopped bool `json:"copyStopped,omitempty"`
}

// The journal's words for what the operator states.
const (
	incumbentStoppedAttest = "the operator states the controller the voters held under this name is stopped, confirmed down, and kept from starting again"
	boxReplacedAttest      = "the operator states this controller's box was replaced, not copied"
)

// commissionTarget is the pair's entry: the cluster's status before.
func (s *Server) commissionTarget(r *http.Request, body []byte) auditTarget {
	t := auditTarget{target: "pair", detail: map[string]any{}}
	t.answered, t.failed = answeredReport, failedReport
	return t
}

// recommissionTarget reads the entry's flags, each named, and the
// operator's statements.
func (s *Server) recommissionTarget(r *http.Request, body []byte) auditTarget {
	var b recommissionBody
	_ = json.NewDecoder(bytes.NewReader(body)).Decode(&b)
	t := auditTarget{target: "this controller", detail: map[string]any{}}
	if s.cluster != nil {
		// The route is there on any server: one outside a pair answers 404.
		if st := s.cluster.Status(); st.Pod != "" {
			t.target = st.Pod
		}
	}
	if b.Incarnation != 0 {
		t.detail["incarnation"] = fmt.Sprintf("%x", b.Incarnation)
	}
	if b.Floor != 0 {
		t.detail["floor"] = b.Floor
	}
	if b.BoxReplaced {
		t.detail["boxReplaced"] = true
		t.detail["attestBox"] = boxReplacedAttest
	}
	if b.FromSpare {
		t.detail["fromSpare"] = true
		if b.CopyStopped {
			t.detail["attest"] = incumbentStoppedAttest
		}
	}
	t.answered, t.failed = answeredReport, failedReport
	return t
}

// answeredReport reads an accepted command's report from its answer.
func answeredReport(body []byte) any {
	// Numbers as written: a boot nonce is 64 bits, past a float's.
	var v any
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	_ = d.Decode(&v)
	return v
}

// failedReport adds a refused command's report to its entry.
func failedReport(body []byte) map[string]any {
	var v map[string]any
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	if d.Decode(&v) != nil {
		return nil
	}
	return map[string]any{"report": v["report"]}
}

// handleCommission is the operator's commission of a new pair. As
// handleClearDuplicate: the redundancy permission, NAUTILUS_TOKEN only
// without an auth: block.
func (s *Server) handleCommission(w http.ResponseWriter, r *http.Request) {
	if code, msg := s.redundancyAuthorized(r); code != 0 {
		WriteRefusal(w, code, msg)
		return
	}
	c, ok := s.cluster.(Commissioner)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "this controller is not a member of a redundant pair (redundancy: peers:)"})
		return
	}
	report, outcome, reason := c.Commission(r.Context(), s.principalOf(r))
	writeOutcome(w, report, outcome, reason)
}

// handleRecommission is the operator's re-commission of this controller,
// or with fromSpare, its recommission --from-spare.
func (s *Server) handleRecommission(w http.ResponseWriter, r *http.Request) {
	if code, msg := s.redundancyAuthorized(r); code != 0 {
		WriteRefusal(w, code, msg)
		return
	}
	c, ok := s.cluster.(Commissioner)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "this controller is not a member of a redundant pair (redundancy: peers:)"})
		return
	}
	var b recommissionBody
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `want {"floor": <n>, "incarnation": "<w>", "boxReplaced": bool} or {"fromSpare": true, "copyStopped": true}`})
		return
	}
	if b.FromSpare && (b.Floor != 0 || b.Incarnation != 0 || b.BoxReplaced) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "fromSpare takes no floor, incarnation or boxReplaced: the spare re-adopts from its peer"})
		return
	}
	if b.FromSpare && !b.CopyStopped {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "stop the controller the voters hold under this name, confirm it is down (powered off, or its process ended) and keep it from starting again, then say so: copyStopped: true (naut redundancy recommission --from-spare --copy-stopped)"})
		return
	}
	report, outcome, reason := c.Recommission(r.Context(), s.principalOf(r), b.Incarnation, b.Floor, b.BoxReplaced, b.FromSpare)
	writeOutcome(w, report, outcome, reason)
}

// writeOutcome writes a commissioning command's answer: 200 done, 409
// refused, 502 incomplete (so the journal records an error).
func writeOutcome(w http.ResponseWriter, report any, outcome, reason string) {
	code := http.StatusOK
	switch outcome {
	case "refused":
		code = http.StatusConflict
	case "incomplete":
		code = http.StatusBadGateway
	}
	out := map[string]any{"outcome": outcome, "report": report}
	if code != http.StatusOK {
		out["error"] = reason
	}
	writeJSON(w, code, out)
}
