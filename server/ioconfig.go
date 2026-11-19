package server

// Live I/O configuration over HTTP (#706 PR 3b): a candidate configuration
// is staged (pending), validated (valid | invalid), applied (applying →
// applied | failed), each step visible:
//
//	POST /api/io/staged               {sha} or {files, driver?}   → pending
//	POST /api/io/staged/{id}/validate                             → valid | invalid
//	POST /api/io/staged/{id}/apply                                → applied | failed
//	GET  /api/io/staged/{id}
//	GET  /api/io                      what runs, against the manifest
//
// The engine is the project loader's (internal/project.IOConfig), handed in
// as Options.IO so this package stays free of it. Writes need the same
// authorisation as an online program edit. A standby forwards these
// requests to the leader like every other /api/ request.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"
)

// IOHook is a running project's live I/O configuration, as the server
// drives it. Candidates are opaque to the server.
type IOHook struct {
	// Stage reads a candidate: from commit sha, or (sha "") the project's
	// files with files laid over them. An error wrapping ErrIONoSnapshot
	// answers 409; any other, 422.
	Stage func(sha string, files map[string][]byte, by string) (any, error)
	// Validate checks a candidate against what runs. drivers are the
	// drivers it changes; body is the validation, as JSON.
	Validate func(cand any) (valid bool, drivers []string, body any)
	// Apply applies a validated candidate and returns its result. Errors
	// wrapping ErrIOConflict answer 409 (busy, stale); ErrIORollbackFailed
	// 500; any other, 422.
	Apply func(ctx context.Context, cand any, by string) (body any, err error)
	// Running is every unit's state, as JSON.
	Running func() any
	// Differs is the units whose running configuration is not the
	// manifest's; Fault what a failed rollback left unknown.
	Differs func() []IODiffer
	Fault   func() string
}

// IODiffer is one driver (or Modbus device) whose running configuration is
// not the manifest's.
type IODiffer struct {
	Driver       string    `json:"driver"`
	Device       string    `json:"device,omitempty"`
	ManifestHash string    `json:"manifestHash"`
	RunningHash  string    `json:"runningHash"`
	AppliedFrom  string    `json:"appliedFrom,omitempty"` // a commit, or "upload"
	At           time.Time `json:"at,omitzero"`
	By           string    `json:"by,omitempty"`
}

// IOConfigState is the frame's config block: what runs against the
// manifest the controller booted from.
type IOConfigState struct {
	// Source is "manifest" when what runs is exactly the manifest, else
	// "applied".
	Source  string     `json:"source"`
	Differs []IODiffer `json:"differs,omitempty"`
	// Fault is what a failed rollback left unknown.
	Fault string `json:"fault,omitempty"`
}

// The errors an IOHook wraps to choose a status.
var (
	ErrIONoSnapshot     = errors.New("no file snapshot of that commit on this controller")
	ErrIOConflict       = errors.New("conflict")
	ErrIORollbackFailed = errors.New("rollback failed")
)

// maxStaged bounds the candidates kept; the oldest finished ones go first.
const maxStaged = 32

// maxUpload bounds an uploaded candidate's body.
const maxUpload = 4 << 20

type stagedIO struct {
	ID         string    `json:"id"`
	State      string    `json:"state"` // pending | valid | invalid | applying | applied | failed
	From       string    `json:"from"`  // the commit, or "upload"
	By         string    `json:"by"`
	At         time.Time `json:"at"`
	Driver     string    `json:"driver,omitempty"` // an upload's declared driver
	Error      string    `json:"error,omitempty"`
	Validation any       `json:"validation,omitempty"`
	Result     any       `json:"result,omitempty"`

	cand any
}

type ioStaging struct {
	mu   sync.Mutex
	next int
	all  map[string]*stagedIO
}

func (s *Server) ioConfigState() *IOConfigState {
	if s.io == nil || s.io.Differs == nil {
		return nil
	}
	st := &IOConfigState{Source: "manifest", Differs: s.io.Differs()}
	if s.io.Fault != nil {
		st.Fault = s.io.Fault()
	}
	if len(st.Differs) > 0 {
		st.Source = "applied"
	}
	return st
}

// requester is who a request is from, for the record: the auth token's
// holder until users exist (#708), else the client's address.
func requester(r *http.Request) string {
	if r.Header.Get("Authorization") != "" || r.Header.Get("X-Nautilus-Token") != "" {
		return "token"
	}
	host := r.RemoteAddr
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return host
}

func (s *Server) ioUnavailable(w http.ResponseWriter) bool {
	if s.io == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "live I/O configuration is not available on this controller"})
		return true
	}
	return false
}

func (s *Server) handleIO(w http.ResponseWriter, r *http.Request) {
	if s.ioUnavailable(w) {
		return
	}
	s.staging.mu.Lock()
	list := make([]*stagedIO, 0, len(s.staging.all))
	for _, c := range s.staging.all {
		cp := *c
		cp.Validation, cp.Result = nil, nil
		list = append(list, &cp)
	}
	s.staging.mu.Unlock()
	sort.Slice(list, func(i, j int) bool { return list[i].At.Before(list[j].At) })
	var running any
	if s.io.Running != nil {
		running = s.io.Running()
	}
	writeJSON(w, http.StatusOK, map[string]any{"config": s.ioConfigState(), "running": running, "staged": list})
}

type stageRequest struct {
	Sha    string            `json:"sha"`
	Driver string            `json:"driver"`
	Files  map[string]string `json:"files"`
}

func (s *Server) handleIOStage(w http.ResponseWriter, r *http.Request) {
	if code, msg := s.authorizeProgramEdit(r); code != 0 {
		writeJSON(w, code, map[string]string{"error": msg})
		return
	}
	if s.ioUnavailable(w) {
		return
	}
	var req stageRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxUpload)).Decode(&req); err != nil ||
		(req.Sha == "") == (len(req.Files) == 0) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `body must be {"sha": "<commit>"} or {"files": {"<path>": "<content>"}, "driver": "<name>"}`})
		return
	}
	by := requester(r)
	var files map[string][]byte
	from := req.Sha
	if req.Sha == "" {
		from = "upload"
		files = make(map[string][]byte, len(req.Files))
		for p, c := range req.Files {
			files[p] = []byte(c)
		}
	}
	cand, err := s.io.Stage(req.Sha, files, by)
	if err != nil {
		code := http.StatusUnprocessableEntity
		if errors.Is(err, ErrIONoSnapshot) {
			code = http.StatusConflict
		}
		writeJSON(w, code, map[string]string{"error": err.Error()})
		return
	}
	st := &stagedIO{State: "pending", From: from, By: by, At: time.Now(), Driver: req.Driver, cand: cand}
	s.staging.mu.Lock()
	if s.staging.all == nil {
		s.staging.all = map[string]*stagedIO{}
	}
	s.staging.next++
	st.ID = strconv.Itoa(s.staging.next)
	s.staging.all[st.ID] = st
	s.pruneStagedLocked()
	out := *st
	s.staging.mu.Unlock()
	slog.Info("config.stage", "id", st.ID, "from", from, "by", by, "driver", req.Driver)
	writeJSON(w, http.StatusCreated, out)
}

// pruneStagedLocked drops the oldest finished candidates past maxStaged.
func (s *Server) pruneStagedLocked() {
	if len(s.staging.all) <= maxStaged {
		return
	}
	var done []*stagedIO
	for _, c := range s.staging.all {
		if c.State != "applying" {
			done = append(done, c)
		}
	}
	sort.Slice(done, func(i, j int) bool { return done[i].At.Before(done[j].At) })
	for i := 0; len(s.staging.all) > maxStaged && i < len(done); i++ {
		delete(s.staging.all, done[i].ID)
	}
}

func (s *Server) stagedByID(w http.ResponseWriter, r *http.Request) *stagedIO {
	s.staging.mu.Lock()
	st := s.staging.all[r.PathValue("id")]
	s.staging.mu.Unlock()
	if st == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no staged I/O configuration " + r.PathValue("id")})
	}
	return st
}

func (s *Server) handleIOStaged(w http.ResponseWriter, r *http.Request) {
	if s.ioUnavailable(w) {
		return
	}
	st := s.stagedByID(w, r)
	if st == nil {
		return
	}
	s.staging.mu.Lock()
	out := *st
	s.staging.mu.Unlock()
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleIOValidate(w http.ResponseWriter, r *http.Request) {
	if code, msg := s.authorizeProgramEdit(r); code != 0 {
		writeJSON(w, code, map[string]string{"error": msg})
		return
	}
	if s.ioUnavailable(w) {
		return
	}
	st := s.stagedByID(w, r)
	if st == nil {
		return
	}
	s.staging.mu.Lock()
	if st.State == "applying" || st.State == "applied" {
		state := st.State
		s.staging.mu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]string{"error": "candidate " + st.ID + " is " + state})
		return
	}
	s.staging.mu.Unlock()
	valid, drivers, body := s.io.Validate(st.cand)
	errText := ""
	if valid && st.Driver != "" {
		for _, d := range drivers {
			if d != st.Driver {
				valid = false
				errText = fmt.Sprintf("the upload was declared for driver %s but also changes driver %s", st.Driver, d)
			}
		}
	}
	s.staging.mu.Lock()
	st.Validation, st.Error = body, errText
	st.State = "invalid"
	if valid {
		st.State = "valid"
	}
	out := *st
	s.staging.mu.Unlock()
	slog.Info("config.validate", "id", st.ID, "result", out.State, "by", requester(r))
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleIOApply(w http.ResponseWriter, r *http.Request) {
	if code, msg := s.authorizeProgramEdit(r); code != 0 {
		writeJSON(w, code, map[string]string{"error": msg})
		return
	}
	if s.ioUnavailable(w) {
		return
	}
	st := s.stagedByID(w, r)
	if st == nil {
		return
	}
	s.staging.mu.Lock()
	if st.State != "valid" {
		state := st.State
		s.staging.mu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]string{"error": "candidate " + st.ID + " is " + state + "; only a valid candidate is applied (validate it first)"})
		return
	}
	st.State = "applying"
	s.staging.mu.Unlock()

	by := requester(r)
	body, err := s.io.Apply(r.Context(), st.cand, by)
	s.staging.mu.Lock()
	code := http.StatusOK
	switch {
	case err == nil:
		st.State, st.Result, st.Error = "applied", body, ""
	case errors.Is(err, ErrIOConflict):
		// Busy or stale: nothing ran; it can be validated again.
		st.State, st.Error = "invalid", err.Error()
		code = http.StatusConflict
	case errors.Is(err, ErrIORollbackFailed):
		st.State, st.Error = "failed", err.Error()
		code = http.StatusInternalServerError
	default:
		st.State, st.Error = "failed", err.Error()
		code = http.StatusUnprocessableEntity
	}
	out := *st
	s.staging.mu.Unlock()
	writeJSON(w, code, out)
}
