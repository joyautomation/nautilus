package server

import (
	"errors"
	"net/http"

	"github.com/joyautomation/nautilus/runtime"
)

// HoldReleaser is the runtime's takeover hold release (#710 (b2)): a pair
// member's runtime has it.
type HoldReleaser interface {
	ReleaseHold(by string) error
}

// releaseHoldTarget is the journal's target for a release: the hold on the
// controller that answers (the leader: a standby forwards the command).
func (s *Server) releaseHoldTarget(r *http.Request, body []byte) auditTarget {
	t := auditTarget{target: "takeover hold", detail: map[string]any{}}
	if s.cluster != nil {
		if st := s.cluster.Status(); st.Pod != "" {
			t.target = "takeover hold on " + st.Pod
		}
	}
	return t
}

// handleReleaseHold is the operator's release of the takeover hold
// (`naut redundancy release-hold`): the program's outputs go out on the
// next scan, before its release tag said they may. As handleClearDuplicate:
// the redundancy permission and a fresh password under an auth: block,
// NAUTILUS_TOKEN only without one. A standby forwards it to the leader
// (it is not a localClusterOps route): the hold holds only there.
func (s *Server) handleReleaseHold(w http.ResponseWriter, r *http.Request) {
	if code, msg := s.redundancyAuthorized(r); code != 0 {
		WriteRefusal(w, code, msg)
		return
	}
	// The runtime holds the hold; a Cluster that releases one itself (a
	// test's) answers instead.
	hr, ok := s.cluster.(HoldReleaser)
	if !ok {
		hr, ok = any(s.rt).(HoldReleaser)
	}
	if s.cluster == nil || !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "this controller is not a member of a redundant pair (redundancy: peers:)"})
		return
	}
	if err := hr.ReleaseHold(s.principalOf(r)); err != nil {
		if errors.Is(err, runtime.ErrNotHolding) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "no takeover hold is holding on this controller: the outputs are already the program's"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"released": true})
}
