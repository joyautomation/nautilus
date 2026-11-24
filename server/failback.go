package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/joyautomation/nautilus/leader"
)

// FailbackSetter is a redundant pair's failback suspend and resume (#710
// d2): an Options.Cluster value that also implements it (leader.Quorum
// does) gets POST /api/cluster/failback. by is the principal.
type FailbackSetter interface {
	Failback(suspend bool, by, why string) error
}

// failbackBody is POST /api/cluster/failback's body.
type failbackBody struct {
	// Suspend is required: a bare POST must not resume (Fable on d2).
	Suspend *bool  `json:"suspend"`
	Why     string `json:"why,omitempty"`
}

// failbackTarget is the journal's target: the failback of the pair whose
// leader answers (a standby forwards the command).
func (s *Server) failbackTarget(r *http.Request, body []byte) auditTarget {
	t := auditTarget{target: "failback", detail: map[string]any{}}
	if s.cluster != nil {
		if st := s.cluster.Status(); st.Pod != "" {
			t.target = "failback of " + st.Pod
		}
	}
	var b failbackBody
	if json.NewDecoder(bytes.NewReader(body)).Decode(&b) == nil && b.Suspend != nil {
		t.detail["suspend"] = *b.Suspend
		if b.Why != "" {
			t.detail["why"] = b.Why
		}
	}
	return t
}

// handleFailback is `naut redundancy failback suspend|resume`. As
// handleReleaseHold: the redundancy permission and a fresh password under
// an auth: block, NAUTILUS_TOKEN only without one; the leader answers.
func (s *Server) handleFailback(w http.ResponseWriter, r *http.Request) {
	if code, msg := s.redundancyAuthorized(r); code != 0 {
		WriteRefusal(w, code, msg)
		return
	}
	fs, ok := s.cluster.(FailbackSetter)
	if s.cluster == nil || !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "this controller is not a member of a redundant pair (redundancy: peers:)"})
		return
	}
	// The first JSON value, whatever follows, as the audit seam reads it.
	var b failbackBody
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&b); err != nil || b.Suspend == nil {
		msg := "want a body {\"suspend\": true|false, \"why\": \"...\"}"
		if err != nil {
			msg += ": " + err.Error()
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}
	if err := fs.Failback(*b.Suspend, s.principalOf(r), b.Why); err != nil {
		code := http.StatusConflict
		if errors.Is(err, leader.ErrNoFailback) {
			code = http.StatusNotFound
		}
		writeJSON(w, code, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"suspended": *b.Suspend})
}
