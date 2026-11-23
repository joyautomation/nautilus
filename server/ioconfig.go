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
//	GET  /api/io/files                the project files it differs in (naut pull --io; needs the edit permission)
//	POST /api/config/commit           {id, message?}: the applied change as a git commit (#711)
//	GET  /api/config/patch            the applied change as a patch to apply by hand (#711)
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
	"strings"
	"sync"
	"time"

	"github.com/joyautomation/nautilus/audit"
)

// IOApply is what an apply is asked: By is the request's principal, ID the
// staged candidate's id, RevertIfSilent (when set) puts the apply back if a
// changed device has not read within it. A struct, so the next field is
// additive.
type IOApply struct {
	By             string
	ID             string
	RevertIfSilent time.Duration
}

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
	// by is the request's principal (who the journal names), id the
	// staged candidate's id.
	Apply func(ctx context.Context, cand any, a IOApply) (body any, err error)
	// OnRevert registers f to be called when an apply's automatic
	// revert-if-silent has run (or failed): no request made it, so the
	// server journals it as the system's.
	OnRevert func(f func(IOWatch))
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
	// Restart says whether a validated candidate is not applicable live only
	// because it changes what is fixed while the runtime runs (a restart
	// would apply it), and its changed files (path to content). Save commits
	// them for that restart (#711); it is nil where the controller cannot
	// commit.
	Restart func(cand any) (needs bool, files map[string]string)
	Save    func(IOSave) (IOCommitted, error)
	// Files is the project files the running I/O configuration differs in.
	Files func() IOFiles
	// Sections are the manifest sections the config screens can render;
	// Schema is one as a self-contained JSON Schema (an error wrapping
	// ErrConfigNoSection answers 404). names says the caller may see the
	// project's file names (profiles/, iodd/): it holds the write
	// permission, as GET /api/io/files needs. ConfigFiles is the manifest text
	// the screens edit (#711).
	Sections    func() []string
	Schema      func(section string, names bool) ([]byte, error)
	ConfigFiles func() (ConfigFiles, error)
	// Commit saves what runs, where it differs from the project, as a git
	// commit in the checkout the controller runs from; nil where the
	// controller has no git worktree (a built controller), which answers
	// 409 and names GET /api/config/patch. Errors wrapping ErrIOCommitRefused
	// answer 409 with the fix in the text, any other 500.
	Commit func(IOCommit) (IOCommitted, error)
	// Patch is that change as a unified patch against the project's files
	// ("" when nothing differs). An error wrapping ErrIOCommitRefused
	// answers 409.
	Patch func() (string, error)
}

// IOCommit is a commit asked for: By is the request's principal (the
// commit's author), ID the applied candidate (the commit's trailer),
// Message the author's subject ("" for the default).
type IOCommit struct {
	By      string
	ID      string
	Message string
}

// IOSave is a candidate that applies at the next restart, saved as a commit:
// By is the principal, ID the candidate, Files its changed files.
type IOSave struct {
	By      string
	ID      string
	Message string
	Files   map[string]string
}

// IOSaved is POST /api/config/save's answer: the commit, who saved it and
// that it takes effect at the next restart.
type IOSaved struct {
	IOCommitted
	By      string    `json:"by"`
	At      time.Time `json:"at"`
	Pending bool      `json:"pending"`
}

// IOCommitted is the commit that was made.
type IOCommitted struct {
	Commit string   `json:"commit"`
	Branch string   `json:"branch"`
	Files  []string `json:"files"`
}

// ErrIOCommitRefused is a commit (or patch) the controller will not make as
// asked; the error text says why and what to do.
var ErrIOCommitRefused = errors.New("commit refused")

// ErrConfigNoSection is a schema section the manifest does not have.
var ErrConfigNoSection = errors.New("no such manifest section")

// ConfigFiles is GET /api/config/files: the manifest file and the files its
// drivers name, as what runs now. Differs are the paths an apply changed.
type ConfigFiles struct {
	Leading  bool              `json:"leading"`
	Manifest string            `json:"manifest"`
	Files    map[string]string `json:"files"`
	Differs  []string          `json:"differs,omitempty"`
}

// IOFile is one project file the running I/O configuration differs in:
// what runs, and the hash of the file the controller booted from ("" when
// the change added the file).
type IOFile struct {
	Path    string `json:"path"`
	Running string `json:"running"`
	Booted  string `json:"booted,omitempty"`
}

// IOFiles is GET /api/io/files: what naut pull --io writes back. Leading is
// false on a standby; Conflicts are paths held with two contents; NonIO
// says the applied manifest also changes sections that never ran.
type IOFiles struct {
	Leading   bool       `json:"leading"`
	Units     []IODiffer `json:"units"`
	Files     []IOFile   `json:"files"`
	Conflicts []string   `json:"conflicts,omitempty"`
	NonIO     bool       `json:"nonIO,omitempty"`
}

// IOWatch is the last apply's watch: which changed devices have read,
// which are pending, by when, and whether it reverts the apply if one is
// still silent then. State: watching | answered | silent | reverted |
// revert-failed | cancelled | superseded.
type IOWatch struct {
	Gen      int       `json:"gen"`
	ID       string    `json:"id,omitempty"`
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
	// Saved is who saved the candidate for the next restart, when it was
	// (POST /api/config/save).
	Saved *IOSaved `json:"saved,omitempty"`

	cand any
	gen  int // bumped on every state change: a validate that raced one does not overwrite it
}

type ioStaging struct {
	mu   sync.Mutex
	next int
	all  map[string]*stagedIO
	// last is the candidate whose apply is what runs: a commit saves what
	// runs, so only it may be committed. A later apply replaces it, a revert
	// clears it.
	last string
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

// requester is who a write is from, for the record: the request's
// principal, as the audit journal names it ("anonymous", "token",
// "token:<name>", "user:<name>"). Nothing the client sends is taken as it.
func (s *Server) requester(r *http.Request) string { return s.principalOf(r) }

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

// handleIOFiles serves the project files the running I/O configuration
// differs in, for naut pull --io. It returns whole file bodies, so it needs
// the edit permission (a plain read gets /api/io, which carries no file
// text), like staging a change.
func (s *Server) handleIOFiles(w http.ResponseWriter, r *http.Request) {
	// The permission only, not the online-edits switch: changes applied
	// while edits were on must still be pullable into git after they are
	// turned off.
	if code, msg := s.authorizeWrite(r); code != 0 {
		writeJSON(w, code, map[string]string{"error": msg})
		return
	}
	if s.ioUnavailable(w) {
		return
	}
	if s.io.Files == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "this controller cannot list the files of its live I/O configuration"})
		return
	}
	writeJSON(w, http.StatusOK, s.io.Files())
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
	watch, err := s.io.CancelRevert(s.requester(r))
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	slog.Info("config.revert.cancel", "gen", watch.Gen, "by", s.requester(r))
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
	by := s.requester(r)
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
	slog.Info("config.validate", "id", st.ID, "result", out.State, "by", s.requester(r))
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

	by := s.requester(r)
	body, err := s.io.Apply(r.Context(), st.cand, IOApply{By: by, ID: st.ID, RevertIfSilent: revert})
	s.staging.mu.Lock()
	code := http.StatusOK
	switch {
	case err == nil:
		st.State, st.Result, st.Error = "applied", body, ""
		s.staging.last = st.ID
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

// recordIORevert journals an apply's automatic revert-if-silent. No request
// made it, so it is the system's, never the applier's name; the apply it
// undid and its principal are in the detail.
func (s *Server) recordIORevert(w IOWatch) {
	// What runs is no longer any candidate's apply.
	s.staging.mu.Lock()
	s.staging.last = ""
	s.staging.mu.Unlock()
	result := audit.ResultOK
	if w.State != "reverted" && w.State != "dropped" {
		result = audit.ResultError
	}
	note := "revert of " + w.ID + " applied by " + w.By
	if w.State == "dropped" {
		// A restart or takeover did not carry the apply: the apply's own
		// entry said ok; the configuration before it runs now.
		note += " (dropped at a restart or takeover; the configuration before it runs)"
	}
	s.audit.record(audit.Entry{
		User: "system", Source: audit.SourceSystem, Action: "config.revert",
		Target: w.ID, Result: result,
		Detail: map[string]any{
			"note":    note,
			"applied": w.ID, "by": w.By, "from": w.From,
			"reason": capText(w.Reason, reasonMax), "state": w.State, "gen": w.Gen,
		},
	})
}

// handleConfigSchema serves one manifest section as a JSON Schema for the
// config screens, or (no section) lists the sections. Read-only.
func (s *Server) handleConfigSchema(w http.ResponseWriter, r *http.Request) {
	if s.ioUnavailable(w) {
		return
	}
	if s.io.Schema == nil || s.io.Sections == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "this controller cannot serve its manifest schema"})
		return
	}
	section := r.URL.Query().Get("section")
	if section == "" {
		writeJSON(w, http.StatusOK, map[string]any{"sections": s.io.Sections()})
		return
	}
	// File-name suggestions only for a caller who may write, the rule
	// /api/io/files has; anyone else gets the schema without them, silently.
	code, _ := s.authorizeWrite(r)
	raw, err := s.io.Schema(section, code == 0)
	if errors.Is(err, ErrConfigNoSection) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/schema+json")
	_, _ = w.Write(raw)
}

// commitRequest is POST /api/config/commit's body: the applied candidate to
// save, and optionally the commit's subject.
type commitRequest struct {
	ID      string `json:"id"`
	Message string `json:"message"`
}

// maxCommitMessage bounds the subject a caller may give.
const maxCommitMessage = 200

// handleConfigCommit saves an applied candidate's change as a git commit in
// the checkout the controller runs from. It is the config permission and,
// for a browser sign-in, a fresh password (the route's action is elevated):
// it writes to the repo in the principal's name. Only where the controller
// has a worktree; anywhere else GET /api/config/patch is the way out.
func (s *Server) handleConfigCommit(w http.ResponseWriter, r *http.Request) {
	// The permission only, not the online-edits switch, like pulling the
	// files: a change applied while edits were on can still be committed.
	if code, msg := s.authorizeWrite(r); code != 0 {
		writeJSON(w, code, map[string]string{"error": msg})
		return
	}
	if s.ioUnavailable(w) {
		return
	}
	var req commitRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14)).Decode(&req); err != nil || req.ID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `body must be {"id": "<applied candidate>", "message": "<subject, optional>"}`})
		return
	}
	req.Message = strings.TrimSpace(req.Message)
	if len(req.Message) > maxCommitMessage || strings.ContainsAny(req.Message, "\x00\r\n") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("message: one line of at most %d characters", maxCommitMessage)})
		return
	}
	if s.io.Commit == nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "this controller runs from a build, not a git checkout, so it cannot commit: GET /api/config/patch returns the change as a patch to apply in your repo"})
		return
	}
	s.staging.mu.Lock()
	st := s.staging.all[req.ID]
	last := s.staging.last
	state := ""
	if st != nil {
		state = st.State
	}
	s.staging.mu.Unlock()
	switch {
	case st == nil:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no staged I/O configuration " + req.ID})
		return
	case state != "applied":
		writeJSON(w, http.StatusConflict, map[string]string{"error": "candidate " + req.ID + " is " + state + "; only an applied candidate is committed (stage, validate and apply it first)"})
		return
	case last != req.ID:
		writeJSON(w, http.StatusConflict, map[string]string{"error": "candidate " + req.ID + " is no longer what runs (a later apply or a revert replaced it): a commit saves what runs, so commit the candidate whose apply is in force"})
		return
	}
	res, err := s.io.Commit(IOCommit{By: s.requester(r), ID: req.ID, Message: req.Message})
	switch {
	case errors.Is(err, ErrIOCommitRefused):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	case err != nil:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
	default:
		writeJSON(w, http.StatusOK, res)
	}
}

// handleConfigSave commits a candidate that applies at the next restart: one
// that loads but changes what is fixed while the runtime runs (a device added
// or removed). Nothing live changes; the files are committed in the
// principal's name, and the answer says the change is pending a restart.
// There is no restart here: the controller stays as it is.
func (s *Server) handleConfigSave(w http.ResponseWriter, r *http.Request) {
	if code, msg := s.authorizeWrite(r); code != 0 {
		writeJSON(w, code, map[string]string{"error": msg})
		return
	}
	if s.ioUnavailable(w) {
		return
	}
	var req commitRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14)).Decode(&req); err != nil || req.ID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `body must be {"id": "<staged candidate>", "message": "<subject, optional>"}`})
		return
	}
	req.Message = strings.TrimSpace(req.Message)
	if len(req.Message) > maxCommitMessage || strings.ContainsAny(req.Message, "\x00\r\n") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("message: one line of at most %d characters", maxCommitMessage)})
		return
	}
	if s.io.Save == nil || s.io.Restart == nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "this controller runs from a build, not a git checkout, so it cannot commit: edit the files in your repository and restart"})
		return
	}
	s.staging.mu.Lock()
	st := s.staging.all[req.ID]
	var cand any
	state := ""
	if st != nil {
		cand, state = st.cand, st.State
	}
	s.staging.mu.Unlock()
	switch {
	case st == nil:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no staged I/O configuration " + req.ID})
		return
	case state != "invalid":
		writeJSON(w, http.StatusConflict, map[string]string{"error": "candidate " + req.ID + " is " + state + ": only a validated candidate that needs a restart is saved this way (a valid one is applied, then committed)"})
		return
	}
	needs, files := s.io.Restart(cand)
	if !needs {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "candidate " + req.ID + " is invalid for a reason a restart would not fix: fix what the validation names and review again"})
		return
	}
	by := s.requester(r)
	res, err := s.io.Save(IOSave{By: by, ID: req.ID, Message: req.Message, Files: files})
	switch {
	case errors.Is(err, ErrIOCommitRefused):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	case err != nil:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
	default:
		saved := &IOSaved{IOCommitted: res, By: by, At: time.Now(), Pending: true}
		s.staging.mu.Lock()
		if st := s.staging.all[req.ID]; st != nil {
			st.Saved = saved
		}
		s.staging.mu.Unlock()
		writeJSON(w, http.StatusOK, saved)
	}
}

// handleConfigPatch serves what runs, where it differs from the project, as
// a patch. It returns whole file bodies, so it needs the edit permission.
func (s *Server) handleConfigPatch(w http.ResponseWriter, r *http.Request) {
	if code, msg := s.authorizeWrite(r); code != 0 {
		writeJSON(w, code, map[string]string{"error": msg})
		return
	}
	if s.ioUnavailable(w) {
		return
	}
	if s.io.Patch == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "this controller cannot make a patch of its live I/O configuration"})
		return
	}
	p, err := s.io.Patch()
	switch {
	case errors.Is(err, ErrIOCommitRefused):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	case err != nil:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	case p == "":
		writeJSON(w, http.StatusConflict, map[string]string{"error": "nothing to patch: what runs is the project's I/O configuration"})
		return
	}
	w.Header().Set("Content-Type", "text/x-diff; charset=utf-8")
	_, _ = w.Write([]byte(p))
}

// handleConfigFiles serves the manifest text the config screens edit. It
// needs the edit permission, like staging a change.
func (s *Server) handleConfigFiles(w http.ResponseWriter, r *http.Request) {
	// The whole manifest (users, grants, hosts), not what an apply changed:
	// it needs the permission to edit it, as GET /api/io/files does.
	if code, msg := s.authorizeProgramEdit(r); code != 0 {
		writeJSON(w, code, map[string]string{"error": msg})
		return
	}
	if s.ioUnavailable(w) {
		return
	}
	if s.io.ConfigFiles == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "this controller cannot list its configuration files"})
		return
	}
	f, err := s.io.ConfigFiles()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, f)
}
