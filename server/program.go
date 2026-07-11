package server

import (
	"encoding/json"
	"net/http"
)

// Program endpoints — PLC-style online edits over HTTP.
//
//	GET  /api/program           what's running: source, hash, dirty, status
//	PUT  /api/program           compile + warm-swap new source (gated)
//	POST /api/program/rollback  one-step stateful undo of the last swap (gated)
//
// The gate is Options.OnlineEdits; writes additionally honor AuthToken via
// the same authorizeWrite path as tag writes. Edits are ephemeral: a restart
// boots the binary's embedded program, so committing the source to git is
// the only way an edit becomes permanent.

// programInfo is the GET /api/program response.
type programInfo struct {
	Source      string `json:"source"`
	Hash        string `json:"hash"`
	Dirty       bool   `json:"dirty"` // running source != boot source
	Editable    bool   `json:"editable"`
	CanRollback bool   `json:"canRollback"`
	CompiledAt  int64  `json:"compiledAt"`
	Scans       uint64 `json:"scans"`
	Error       string `json:"error,omitempty"`
}

func (s *Server) handleGetProgram(w http.ResponseWriter, r *http.Request) {
	p := s.rt.Program()
	st := p.Status()
	writeJSON(w, http.StatusOK, programInfo{
		Source:      p.Source(),
		Hash:        p.Hash(),
		Dirty:       p.Dirty(),
		Editable:    s.onlineEdits,
		CanRollback: p.CanRollback(),
		CompiledAt:  st.CompiledAt,
		Scans:       st.Scans,
		Error:       st.Error,
	})
}

// putProgramRequest is the PUT /api/program payload. BaseHash, when set,
// must match the running program's hash — optimistic concurrency so two
// editors can't silently stomp each other's online edits.
type putProgramRequest struct {
	Source   string `json:"source"`
	BaseHash string `json:"baseHash,omitempty"`
}

func (s *Server) handlePutProgram(w http.ResponseWriter, r *http.Request) {
	if code, msg := s.authorizeProgramEdit(r); code != 0 {
		writeJSON(w, code, map[string]string{"error": msg})
		return
	}
	var req putProgramRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Source == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body must be {\"source\": \"...\"}"})
		return
	}
	p := s.rt.Program()
	if req.BaseHash != "" && req.BaseHash != p.Hash() {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "controller program changed since your base — refresh and re-apply",
			"hash":  p.Hash(),
		})
		return
	}
	report, err := p.SwapWarm(req.Source)
	if err != nil {
		// Compile failure: the old program is still running, untouched.
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, report)
}

func (s *Server) handleRollback(w http.ResponseWriter, r *http.Request) {
	if code, msg := s.authorizeProgramEdit(r); code != 0 {
		writeJSON(w, code, map[string]string{"error": msg})
		return
	}
	report, err := s.rt.Program().Rollback()
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, report)
}

// authorizeProgramEdit gates program mutations: the OnlineEdits switch
// first, then the same write authorization as tag writes.
func (s *Server) authorizeProgramEdit(r *http.Request) (int, string) {
	if !s.onlineEdits {
		return http.StatusForbidden, "online edits are disabled on this controller (server.Options.OnlineEdits)"
	}
	return s.authorizeWrite(r)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
