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
	"io"
	"log/slog"
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
	// Apply applies a validated candidate and returns its result.
	// revertIfSilent, when set, puts the apply back if a changed device has
	// not read within it. Errors wrapping ErrIOConflict answer 409 (busy,
	// stale); ErrIORollbackFailed 500; any other, 422.
	Apply func(ctx context.Context, cand any, by string, revertIfSilent time.Duration) (body any, err error)
	// Running is every unit's state, as JSON.
	Running func() any
	// Differs is the units whose running configuration is not the
	// manifest's; Fault what a failed rollback left unknown.
	Differs func() []IODiffer
	Fault   func() string
	// Discarded is what the last restart or takeover did not bring back of
	// what the retain store kept; NotKept says why what runs would not
	// survive a restart ("" when it would, or nothing differs).
	Discarded func() []IODiscard
	NotKept   func() string
	// Watch is the last apply's watch over its changed devices (nil
	// before any); CancelRevert stops a pending revert, or says why not
	// (none asked for, one under way, one done).
	Watch        func() *IOWatch
	CancelRevert func(by string) (*IOWatch, error)
}

// IOWatch is the last apply's watch: which changed devices have read,
// which are pending, by when, and whether it reverts the apply if one is
// still silent then. State: watching | answered | silent | reverted |
// revert-failed | cancelled | superseded.
type IOWatch struct {
	Gen      int       `json:"gen"`
	From     string    `json:"from"`
	By       string    `json:"by"`
	Deadline time.Time `json:"deadline"`
	Revert   bool      `json:"revert"`
	Answered []string  `json:"answered,omitempty"`
	Pending  []string  `json:"pending,omitempty"`
	Silent   []string  `json:"silent,omitempty"`
	State    string    `json:"state"`
	Reason   string    `json:"reason,omitempty"`
	Ended    time.Time `json:"ended,omitzero"`
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

// IODiscard is a kept change a restart or takeover did not bring back: the
// deployed manifest moved under it (a deploy replaces a live change), or
// what was kept no longer rebuilds. One with no driver is an apply not
// carried at all (it was waiting to revert if its devices stayed silent).
type IODiscard struct {
	Driver   string    `json:"driver,omitempty"`
	Device   string    `json:"device,omitempty"`
	Base     string    `json:"base,omitempty"`
	Manifest string    `json:"manifest,omitempty"`
	From     string    `json:"from,omitempty"`
	At       time.Time `json:"at,omitzero"`
	By       string    `json:"by,omitempty"`
	Why      string    `json:"why"`
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
	// Discarded is what the last restart or takeover dropped of what the
	// retain store kept (the I/O counterpart of retainDiscardedEdits).
	Discarded []IODiscard `json:"discarded,omitempty"`
	// NotKept says why what runs would not come back after a restart.
	NotKept string `json:"notKept,omitempty"`
	// Watch is the last apply's watch while it is still watching: its
	// pending devices, its deadline, and whether it will then revert.
	Watch *IOWatch `json:"watch,omitempty"`
}

// The errors an IOHook wraps to choose a status.
var (
	ErrIONoSnapshot     = errors.New("no file snapshot of that commit on this controller")
	ErrIOConflict       = errors.New("conflict")
	ErrIORollbackFailed = errors.New("rollback failed")
	// ErrIONotKept is an apply the retain store could not keep on a
	// redundant project: it was put back (503).
	ErrIONotKept = errors.New("not kept")
)

// maxStaged bounds the candidates kept; the oldest finished ones go first.
const maxStaged = 32

// maxRevertIfSilent bounds how long an apply may wait for its devices
// before putting itself back.
const maxRevertIfSilent = 5 * time.Minute

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
	gen  int // bumped on every state change: a validate that raced one does not overwrite it
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
	if s.io.Discarded != nil {
		st.Discarded = s.io.Discarded()
	}
	if s.io.NotKept != nil {
		st.NotKept = s.io.NotKept()
	}
	if s.io.Watch != nil {
		if w := s.io.Watch(); w != nil && w.State == "watching" {
			st.Watch = w
		}
	}
	return st
}

// requester is who a write is from, for the record: until users exist
// (#708, #894's caller identity), "token" on a server with a token (a write
// got here only by presenting it), else "anonymous".
func (s *Server) requester() string {
	if s.authToken != "" {
		return "token"
	}
	return "anonymous"
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
	var watch *IOWatch
	if s.io.Watch != nil {
		watch = s.io.Watch()
	}
	writeJSON(w, http.StatusOK, map[string]any{"config": s.ioConfigState(), "running": running, "staged": list, "watch": watch})
}

// handleIOCancelRevert stops the last apply's pending revert.
func (s *Server) handleIOCancelRevert(w http.ResponseWriter, r *http.Request) {
	if code, msg := s.authorizeProgramEdit(r); code != 0 {
		writeJSON(w, code, map[string]string{"error": msg})
		return
	}
	if s.ioUnavailable(w) {
		return
	}
	if s.io.CancelRevert == nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "no revert is pending"})
		return
	}
	watch, err := s.io.CancelRevert(s.requester())
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	slog.Info("config.revert.cancel", "gen", watch.Gen, "by", s.requester())
	writeJSON(w, http.StatusOK, watch)
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
	by := s.requester()
	var files map[string][]byte
	from := req.Sha
	if req.Sha != "" {
		// The commit as the controller's history knows it, recorded in
		// full, not the prefix typed.
		c := s.history().Find(req.Sha)
		if c == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "no commit " + req.Sha + " in this controller's history"})
			return
		}
		from = c.Sha
	} else {
		from = "upload"
		files = make(map[string][]byte, len(req.Files))
		for p, c := range req.Files {
			files[p] = []byte(c)
		}
	}
	sha := ""
	if req.Sha != "" {
		sha = from
	}
	cand, err := s.io.Stage(sha, files, by)
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
	gen := st.gen
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
	if st.gen != gen {
		// An apply (or another validate) moved it on meanwhile: what this
		// validate found is about a state that is gone.
		state := st.State
		s.staging.mu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]string{"error": "candidate " + st.ID + " changed while it was validated (now " + state + "); validate it again"})
		return
	}
	st.Validation, st.Error = body, errText
	st.State = "invalid"
	if valid {
		st.State = "valid"
	}
	st.gen++
	out := *st
	s.staging.mu.Unlock()
	slog.Info("config.validate", "id", st.ID, "result", out.State, "by", s.requester())
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
	// The body is optional: {"revertIfSilent": "10s"} has the apply's watch
	// put it back if a changed device has not read within 10 s. The apply
	// answers at once; the watch is GET /api/io's, and the config block's
	// while it watches.
	var req struct {
		RevertIfSilent string `json:"revertIfSilent"`
	}
	if r.Body != nil {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": `body must be empty or {"revertIfSilent": "<duration>"}`})
			return
		}
	}
	var revert time.Duration
	if req.RevertIfSilent != "" {
		d, err := time.ParseDuration(req.RevertIfSilent)
		if err != nil || d <= 0 || d > maxRevertIfSilent {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("revertIfSilent %q: a duration from 1ms to %s", req.RevertIfSilent, maxRevertIfSilent)})
			return
		}
		revert = d
	}
	s.staging.mu.Lock()
	if st.State != "valid" {
		state := st.State
		s.staging.mu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]string{"error": "candidate " + st.ID + " is " + state + "; only a valid candidate is applied (validate it first)"})
		return
	}
	st.State = "applying"
	st.gen++
	s.staging.mu.Unlock()

	by := s.requester()
	body, err := s.io.Apply(r.Context(), st.cand, by, revert)
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
	case errors.Is(err, ErrIONotKept):
		// Applied, not kept, put back: what runs is as before.
		st.State, st.Error = "failed", err.Error()
		code = http.StatusServiceUnavailable
	default:
		st.State, st.Error = "failed", err.Error()
		code = http.StatusUnprocessableEntity
	}
	st.gen++
	out := *st
	s.staging.mu.Unlock()
	writeJSON(w, code, out)
}
