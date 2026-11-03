package server

import (
	"encoding/json"
	"net/http"

	"github.com/joyautomation/nautilus/runtime"
)

// Program endpoints — PLC-style online edits over HTTP, for EVERY program
// in the resource (the main task and each additional task).
//
//	GET  /api/program           what's running: source, hash, dirty, status
//	PUT  /api/program           compile + warm-swap new source (gated)
//	POST /api/program/rollback  one-step stateful undo of the last swap (gated)
//
// The provenance pair — GET /api/program/history and POST
// /api/program/activate (warm-swap to any captured commit) — lives in
// history.go.
//
// Programs are addressed by POU name — `PROGRAM <Name>` is a program's
// identity. GET/rollback take `?pou=<name>` (or `?task=<taskName>`); a PUT
// routes automatically by the POU name in the submitted source, so an
// editor downloads any program file and the right task picks it up. No
// selector = the main task, which keeps the single-program API unchanged.
//
// The gate is Options.OnlineEdits; writes additionally honor AuthToken via
// the same authorizeWrite path as tag writes. Edits are ephemeral: a restart
// boots the binary's embedded programs, so committing the source to git is
// the only way an edit becomes permanent.

// programInfo is the GET /api/program response. Source is the ORIGINAL
// program source — for an FBD controller that's the .fbd netlist, so a
// workspace file diffs 1:1 against it; Language says which ("st" | "fbd").
type programInfo struct {
	Task        string `json:"task"` // task name; "main" for the main task
	POU         string `json:"pou"`  // `PROGRAM <Name>` — the edit-routing identity
	Source      string `json:"source"`
	Language    string `json:"language"`
	Hash        string `json:"hash"`
	Dirty       bool   `json:"dirty"` // running source != boot source
	Editable    bool   `json:"editable"`
	CanRollback bool   `json:"canRollback"`
	CompiledAt  int64  `json:"compiledAt"`
	Scans       uint64 `json:"scans"`
	Error       string `json:"error,omitempty"`
	// Every program in the resource, source omitted — the directory an
	// editor uses to discover what's addressable.
	Programs []programSummary `json:"programs,omitempty"`
}

// programSummary is one resource program in the GET directory.
type programSummary struct {
	Task        string `json:"task"`
	POU         string `json:"pou"`
	Language    string `json:"language"`
	Hash        string `json:"hash"`
	Dirty       bool   `json:"dirty"`
	CanRollback bool   `json:"canRollback"`
	Scans       uint64 `json:"scans"`
	Error       string `json:"error,omitempty"`
}

// selectProgram resolves ?task= / ?pou= to a program; no selector = main.
func (s *Server) selectProgram(r *http.Request) (*runtime.Program, string, string) {
	if task := r.URL.Query().Get("task"); task != "" {
		if p := s.rt.TaskProgram(task); p != nil {
			name := task
			if p == s.rt.Program() {
				name = runtime.MainTaskName
			}
			return p, name, ""
		}
		return nil, "", "no task named " + task
	}
	if pou := r.URL.Query().Get("pou"); pou != "" {
		if p, task := s.rt.ProgramByPOU(pou); p != nil {
			return p, task, ""
		}
		return nil, "", "no program named " + pou
	}
	return s.rt.Program(), runtime.MainTaskName, ""
}

func (s *Server) handleGetProgram(w http.ResponseWriter, r *http.Request) {
	p, task, errMsg := s.selectProgram(r)
	if p == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": errMsg})
		return
	}
	st := p.Status()
	info := programInfo{
		Task:        task,
		POU:         p.POU(),
		Source:      p.Source(),
		Language:    runtime.Language(p.Source()),
		Hash:        p.Hash(),
		Dirty:       p.Dirty(),
		Editable:    s.onlineEdits,
		CanRollback: p.CanRollback(),
		CompiledAt:  st.CompiledAt,
		Scans:       st.Scans,
		Error:       st.Error,
	}
	names := append([]string{runtime.MainTaskName}, s.rt.TaskNames()...)
	for _, name := range names {
		tp := s.rt.TaskProgram(name)
		tst := tp.Status()
		info.Programs = append(info.Programs, programSummary{
			Task:        name,
			POU:         tp.POU(),
			Language:    runtime.Language(tp.Source()),
			Hash:        tp.Hash(),
			Dirty:       tp.Dirty(),
			CanRollback: tp.CanRollback(),
			Scans:       tst.Scans,
			Error:       tst.Error,
		})
	}
	writeJSON(w, http.StatusOK, info)
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
	// Route by explicit selector first, else by the POU name in the
	// submitted source. An unmatched POU falls through to the main task
	// only when the resource runs a single program (renaming it is a
	// legitimate single-program edit); with tasks present that would
	// silently retarget, so it 404s with the directory instead.
	p, task, errMsg := s.selectProgram(r)
	if p == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": errMsg})
		return
	}
	if r.URL.Query().Get("task") == "" && r.URL.Query().Get("pou") == "" {
		if pou := runtime.POUOf(req.Source); pou != "" {
			if byPou, byTask := s.rt.ProgramByPOU(pou); byPou != nil {
				p, task = byPou, byTask
			} else if len(s.rt.TaskNames()) > 0 {
				writeJSON(w, http.StatusNotFound, map[string]string{
					"error": "no program named " + pou + " on this controller — its tasks are listed in GET /api/program",
				})
				return
			}
		}
	}
	if req.BaseHash != "" && req.BaseHash != p.Hash() {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "controller program changed since your base — refresh and re-apply",
			"hash":  p.Hash(),
			"task":  task,
		})
		return
	}
	report, err := p.SwapWarm(req.Source)
	if err != nil {
		// Compile failure: the old program is still running, untouched.
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	// A hand edit orphans any "running commit X" claim — the source now
	// matches no captured commit until the next activation.
	s.setActive("")
	writeJSON(w, http.StatusOK, struct {
		runtime.SwapReport
		Task string `json:"task"`
	}{report, task})
}

func (s *Server) handleRollback(w http.ResponseWriter, r *http.Request) {
	if code, msg := s.authorizeProgramEdit(r); code != 0 {
		writeJSON(w, code, map[string]string{"error": msg})
		return
	}
	p, _, errMsg := s.selectProgram(r)
	if p == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": errMsg})
		return
	}
	report, err := p.Rollback()
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	s.setActive("")
	writeJSON(w, http.StatusOK, report)
}

// authorizeProgramEdit gates program mutations: the OnlineEdits switch
// first, then the same write authorization as tag writes. The refusal names
// the manifest key, which is what a user sets; Options.OnlineEdits is how
// naut run passes it in.
func (s *Server) authorizeProgramEdit(r *http.Request) (int, string) {
	if !s.onlineEdits {
		return http.StatusForbidden, "online edits are disabled on this controller (set server.online-edits: true in nautilus.yaml)"
	}
	return s.authorizeWrite(r)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
