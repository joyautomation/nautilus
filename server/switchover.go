package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/joyautomation/nautilus/runtime"
)

// Switchoverer is a redundant pair's planned switchover (#710 d1c): an
// Options.Cluster value that also implements it (leader.Quorum does) gets
// POST /api/cluster/switchover. by is the principal. outcome is "done"
// (the standby takes over hot now), "refused" (nothing was done),
// "aborted" (stopped before the resign: this controller leads on, nothing
// lost) or "failover" (past the resign, no RESIGN sent: the lease runs out
// as in a failover); report is the switchover's report, as JSON, and
// reason one line saying why when the outcome is not "done".
type Switchoverer interface {
	Switchover(ctx context.Context, by string) (report any, outcome, reason string)
}

// refuseSwitching answers 503 with Retry-After for a change refused
// because a planned switchover is under way (runtime.ErrSwitching): it
// lands on the new leader in a moment. It reports whether it answered.
func refuseSwitching(w http.ResponseWriter, err error) bool {
	if !errors.Is(err, runtime.ErrSwitching) {
		return false
	}
	w.Header().Set("Retry-After", "1")
	writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
	return true
}

// switchoverTarget is the journal's target: the lead of the controller
// that answers (the leader: a standby forwards the command).
func (s *Server) switchoverTarget(r *http.Request, body []byte) auditTarget {
	t := auditTarget{target: "the lead", detail: map[string]any{}}
	if s.cluster != nil {
		st := s.cluster.Status()
		if st.Pod != "" {
			t.target = "the lead of " + st.Pod
		}
		t.before = map[string]any{"leader": st.Leader}
	}
	read := func(body []byte) map[string]any {
		var v map[string]any
		d := json.NewDecoder(bytes.NewReader(body))
		d.UseNumber()
		if d.Decode(&v) != nil {
			return nil
		}
		return v
	}
	t.answered = func(body []byte) any { return read(body) }
	t.failed = func(body []byte) map[string]any {
		v := read(body)
		if v == nil {
			return nil
		}
		return map[string]any{"outcome": v["outcome"], "report": v["report"]}
	}
	return t
}

// handleSwitchover is the operator's planned switchover (`naut redundancy
// switchover`). As handleClearDuplicate: the redundancy permission and a
// fresh password under an auth: block, NAUTILUS_TOKEN only without one. A
// standby forwards it to the leader (it is not a localClusterOps route).
func (s *Server) handleSwitchover(w http.ResponseWriter, r *http.Request) {
	if code, msg := s.redundancyAuthorized(r); code != 0 {
		WriteRefusal(w, code, msg)
		return
	}
	c, ok := s.cluster.(Switchoverer)
	if s.cluster == nil || !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "this controller is not a member of a redundant pair (redundancy: peers:)"})
		return
	}
	report, outcome, reason := c.Switchover(r.Context(), s.principalOf(r))
	code := http.StatusOK
	switch outcome {
	case "done":
	case "failover":
		code = http.StatusBadGateway // the lead moves, but by the lease running out
	default:
		code = http.StatusConflict // refused or aborted: this controller leads on
	}
	out := map[string]any{"outcome": outcome, "report": report}
	if code != http.StatusOK {
		out["error"] = reason
	}
	writeJSON(w, code, out)
}
