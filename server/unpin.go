package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
)

// PinDropper is a redundant pair's `naut redundancy unpin` (#710 2b). An
// Options.Cluster value that also implements it (leader.Quorum does) gets
// POST /api/cluster/unpin. name is the other controller, whose box was
// replaced or whose state directory was lost; by the principal. outcome
// is "done", "refused" (nothing changed) or "incomplete" (the witness was
// not reached); report is the command's report, as JSON, and reason one
// line saying why when the outcome is not "done".
type PinDropper interface {
	Unpin(ctx context.Context, name, by string) (report any, outcome, reason string)
}

// unpinBody is POST /api/cluster/unpin's body.
type unpinBody struct {
	Name string `json:"name"`
}

// unpinTarget reads the entry's target (the controller unpinned).
func (s *Server) unpinTarget(r *http.Request, body []byte) auditTarget {
	var b unpinBody
	_ = json.NewDecoder(bytes.NewReader(body)).Decode(&b)
	t := auditTarget{target: b.Name, detail: map[string]any{}}
	t.answered = func(body []byte) any {
		var v any
		_ = json.Unmarshal(body, &v)
		return v
	}
	t.failed = func(body []byte) map[string]any {
		var v map[string]any
		if json.Unmarshal(body, &v) != nil {
			return nil
		}
		return map[string]any{"report": v["report"]}
	}
	return t
}

// handleUnpin is `naut redundancy unpin <name>`, run on the surviving
// controller: it answers it itself, leader or standby (localClusterOps),
// since the pins are this box's. As handleClearDuplicate: the redundancy
// permission, NAUTILUS_TOKEN only without an auth: block.
func (s *Server) handleUnpin(w http.ResponseWriter, r *http.Request) {
	if code, msg := s.redundancyAuthorized(r); code != 0 {
		WriteRefusal(w, code, msg)
		return
	}
	c, ok := s.cluster.(PinDropper)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "this controller is not a member of a redundant pair (redundancy: peers:)"})
		return
	}
	var b unpinBody
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&b); err != nil || b.Name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `want {"name": "<the other controller: a or b>"}`})
		return
	}
	report, outcome, reason := c.Unpin(r.Context(), b.Name, s.principalOf(r))
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
