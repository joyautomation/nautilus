package server

// The live I/O routes (#706 PR 3b) over a fake engine: every state, every
// refusal and its status, and the config block on /api/state and the
// stream. The engine itself is tested in internal/project.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeIO struct {
	gate     chan struct{} // when set, Validate waits for it
	staged   []string      // what Stage was asked for
	valid    bool
	drivers  []string
	applyErr error
	differs  []IODiffer
	fault    string
	applied  int
	revert   time.Duration // what the last apply was asked to revert after
	watch    *IOWatch
	cancels  int
	// who the engine was told acted, and the apply id
	stageBy, applyBy, applyID, cancelBy string
	applyBody                           any
	changes                             []any // validation\'s changes
	onRevert                            func(IOWatch)
	// commit, when set, is the hook's Commit; commits what it was asked
	commit  func(IOCommit) (IOCommitted, error)
	commits []IOCommit
	patch   func() (string, error)
	// restart, when set, is the hook's Restart (the candidate needs a restart,
	// and its files); save its Save; saves what it was asked
	restart func(any) (bool, map[string]string)
	save    func(IOSave) (IOCommitted, error)
	saves   []IOSave
}

func (f *fakeIO) hook() *IOHook {
	return &IOHook{
		Stage: func(sha string, files map[string][]byte, by string) (any, error) {
			if strings.HasPrefix(sha, "deadbeef") {
				return nil, fmt.Errorf("%w: nosnap", ErrIONoSnapshot)
			}
			f.stageBy = by
			what := sha
			if sha == "" {
				what = "files:" + string(files["modbus.yaml"])
			}
			f.staged = append(f.staged, what)
			return what, nil
		},
		Validate: func(cand any) (bool, []string, any) {
			if f.gate != nil {
				<-f.gate
			}
			return f.valid, f.drivers, map[string]any{"valid": f.valid, "cand": cand, "changes": f.changes}
		},
		Apply: func(ctx context.Context, cand any, a IOApply) (any, error) {
			by, id, revertIfSilent := a.By, a.ID, a.RevertIfSilent
			f.revert = revertIfSilent
			f.applyBy, f.applyID = by, id
			if f.applyErr != nil {
				return nil, f.applyErr
			}
			f.applied++
			f.differs = []IODiffer{{Driver: "modbus", Device: "DEV_A", ManifestHash: "a", RunningHash: "b", AppliedFrom: "upload", By: by}}
			if f.applyBody != nil {
				return f.applyBody, nil
			}
			return map[string]any{"commanded": []any{}}, nil
		},
		OnRevert: func(fn func(IOWatch)) { f.onRevert = fn },
		Running:  func() any { return []string{"modbus"} },
		Watch:    func() *IOWatch { return f.watch },
		CancelRevert: func(by string) (*IOWatch, error) {
			if f.watch == nil || f.watch.State != "watching" || !f.watch.Revert {
				return nil, errors.New("no revert is pending")
			}
			f.cancels++
			f.cancelBy = by
			w := *f.watch
			w.State, w.Reason = "cancelled", "the revert was cancelled by "+by
			f.watch = &w
			return &w, nil
		},
		Differs: func() []IODiffer { return f.differs },
		Fault:   func() string { return f.fault },
		Patch:   f.patch,
		Restart: f.restart,
		Save: func() func(IOSave) (IOCommitted, error) {
			if f.save == nil {
				return nil
			}
			return func(c IOSave) (IOCommitted, error) {
				f.saves = append(f.saves, c)
				return f.save(c)
			}
		}(),
		Commit: func() func(IOCommit) (IOCommitted, error) {
			if f.commit == nil {
				return nil
			}
			return func(c IOCommit) (IOCommitted, error) {
				f.commits = append(f.commits, c)
				return f.commit(c)
			}
		}(),
	}
}

// The controller's history: abc1234… has a file snapshot, deadbeef… not.
const shaA, shaNoSnap = "abc1234567890abc1234567890abc1234567890a", "deadbeef0123456789deadbeef0123456789dead"

func ioOptions(f *fakeIO, online bool) Options {
	h := &ProgramHistory{Commits: []ProgramCommit{{Sha: shaA, Short: "abc1234"}, {Sha: shaNoSnap, Short: "deadbee"}}}
	return Options{OnlineEdits: online, IO: f.hook(), History: func() *ProgramHistory { return h }}
}

func ioServer(t *testing.T, f *fakeIO, online bool) http.Handler {
	t.Helper()
	return New(newTestRuntime(t), ioOptions(f, online)).Handler()
}

// pending → valid → applying → applied, each visible, and the drift on
// /api/state and /api/io after it.
func TestIORoutesStageValidateApply(t *testing.T) {
	f := &fakeIO{valid: true, drivers: []string{"modbus"}}
	h := ioServer(t, f, true)

	if _, st := doJSON(t, h, "GET", "/api/state", nil); fmt.Sprint(st["config"]) != "map[source:manifest]" {
		t.Fatalf("config before any apply = %v, want source manifest", st["config"])
	}
	w, st := doJSON(t, h, "POST", "/api/io/staged", map[string]any{"files": map[string]string{"modbus.yaml": "x"}, "driver": "modbus"})
	if w.Code != http.StatusCreated || st["state"] != "pending" || st["from"] != "upload" || st["id"] == "" {
		t.Fatalf("stage: %d %v", w.Code, st)
	}
	id := st["id"].(string)
	if w, st = doJSON(t, h, "POST", "/api/io/staged/"+id+"/validate", nil); w.Code != http.StatusOK || st["state"] != "valid" || st["validation"] == nil {
		t.Fatalf("validate: %d %v", w.Code, st)
	}
	if w, st = doJSON(t, h, "POST", "/api/io/staged/"+id+"/apply", nil); w.Code != http.StatusOK || st["state"] != "applied" || st["result"] == nil {
		t.Fatalf("apply: %d %v", w.Code, st)
	}
	if w, st = doJSON(t, h, "GET", "/api/io/staged/"+id, nil); w.Code != http.StatusOK || st["state"] != "applied" {
		t.Fatalf("get: %d %v", w.Code, st)
	}
	if w, _ = doJSON(t, h, "POST", "/api/io/staged/"+id+"/apply", nil); w.Code != http.StatusConflict {
		t.Fatalf("applying twice: %d, want 409", w.Code)
	}
	if w, _ = doJSON(t, h, "POST", "/api/io/staged/"+id+"/validate", nil); w.Code != http.StatusConflict {
		t.Fatalf("validating an applied candidate: %d, want 409", w.Code)
	}
	_, st = doJSON(t, h, "GET", "/api/state", nil)
	c, _ := st["config"].(map[string]any)
	if c["source"] != "applied" || !strings.Contains(fmt.Sprint(c["differs"]), "DEV_A") {
		t.Fatalf("config after the apply = %v, want applied, DEV_A differing", st["config"])
	}
	if _, st = doJSON(t, h, "GET", "/api/io", nil); st["running"] == nil || st["config"] == nil || len(st["staged"].([]any)) != 1 {
		t.Fatalf("/api/io = %v", st)
	}
	if f.applied != 1 || len(f.staged) != 1 || f.staged[0] != "files:x" {
		t.Fatalf("engine saw staged %v, applied %d", f.staged, f.applied)
	}
}

// Every refusal, with its status, and nothing applied.
func TestIORoutesRefuse(t *testing.T) {
	stage := func(t *testing.T, h http.Handler, body any) string {
		t.Helper()
		w, st := doJSON(t, h, "POST", "/api/io/staged", body)
		if w.Code != http.StatusCreated {
			t.Fatalf("stage: %d %v", w.Code, st)
		}
		return st["id"].(string)
	}
	t.Run("online edits off", func(t *testing.T) {
		f := &fakeIO{valid: true}
		w, _ := doJSON(t, ioServer(t, f, false), "POST", "/api/io/staged", map[string]any{"sha": "abc1234"})
		if w.Code != http.StatusForbidden || len(f.staged) != 0 {
			t.Fatalf("%d, staged %v; want 403, nothing staged", w.Code, f.staged)
		}
	})
	t.Run("a body with neither or both", func(t *testing.T) {
		h := ioServer(t, &fakeIO{}, true)
		for _, b := range []any{map[string]any{}, map[string]any{"sha": "a", "files": map[string]string{"x": "y"}}} {
			if w, _ := doJSON(t, h, "POST", "/api/io/staged", b); w.Code != http.StatusBadRequest {
				t.Fatalf("%v: %d, want 400", b, w.Code)
			}
		}
	})
	t.Run("no snapshot of the commit", func(t *testing.T) {
		if w, _ := doJSON(t, ioServer(t, &fakeIO{}, true), "POST", "/api/io/staged", map[string]any{"sha": "deadbee"}); w.Code != http.StatusConflict {
			t.Fatalf("%d, want 409", w.Code)
		}
	})
	t.Run("a commit not in the history", func(t *testing.T) {
		f := &fakeIO{}
		if w, _ := doJSON(t, ioServer(t, f, true), "POST", "/api/io/staged", map[string]any{"sha": "1234567"}); w.Code != http.StatusNotFound || len(f.staged) != 0 {
			t.Fatalf("%d, staged %v; want 404, nothing staged", w.Code, f.staged)
		}
	})
	t.Run("a commit is recorded resolved", func(t *testing.T) {
		f := &fakeIO{}
		w, st := doJSON(t, ioServer(t, f, true), "POST", "/api/io/staged", map[string]any{"sha": "abc1234"})
		if w.Code != http.StatusCreated || st["from"] != shaA || fmt.Sprint(f.staged) != "["+shaA+"]" {
			t.Fatalf("%d from %v, engine got %v; want the full commit %s", w.Code, st["from"], f.staged, shaA)
		}
	})
	t.Run("an unknown candidate", func(t *testing.T) {
		if w, _ := doJSON(t, ioServer(t, &fakeIO{}, true), "POST", "/api/io/staged/99/validate", nil); w.Code != http.StatusNotFound {
			t.Fatalf("%d, want 404", w.Code)
		}
	})
	t.Run("apply before validate", func(t *testing.T) {
		f := &fakeIO{valid: true}
		h := ioServer(t, f, true)
		id := stage(t, h, map[string]any{"sha": "abc1234"})
		if w, _ := doJSON(t, h, "POST", "/api/io/staged/"+id+"/apply", nil); w.Code != http.StatusConflict || f.applied != 0 {
			t.Fatalf("%d, applied %d; want 409, nothing applied", w.Code, f.applied)
		}
	})
	t.Run("an invalid candidate", func(t *testing.T) {
		f := &fakeIO{valid: false}
		h := ioServer(t, f, true)
		id := stage(t, h, map[string]any{"sha": "abc1234"})
		if _, st := doJSON(t, h, "POST", "/api/io/staged/"+id+"/validate", nil); st["state"] != "invalid" {
			t.Fatalf("validate: %v", st)
		}
		if w, _ := doJSON(t, h, "POST", "/api/io/staged/"+id+"/apply", nil); w.Code != http.StatusConflict || f.applied != 0 {
			t.Fatalf("%d, applied %d; want 409, nothing applied", w.Code, f.applied)
		}
	})
	t.Run("an upload that changes another driver", func(t *testing.T) {
		f := &fakeIO{valid: true, drivers: []string{"modbus", "opcua-client"}}
		h := ioServer(t, f, true)
		id := stage(t, h, map[string]any{"files": map[string]string{"modbus.yaml": "x"}, "driver": "modbus"})
		if _, st := doJSON(t, h, "POST", "/api/io/staged/"+id+"/validate", nil); st["state"] != "invalid" || !strings.Contains(fmt.Sprint(st["error"]), "opcua-client") {
			t.Fatalf("validate: %v", st)
		}
	})
	for _, tc := range []struct {
		name  string
		err   error
		code  int
		state string
	}{
		{"busy or stale", fmt.Errorf("%w: stale", ErrIOConflict), http.StatusConflict, "invalid"},
		{"a failed rollback", fmt.Errorf("%w: DEV_A unknown", ErrIORollbackFailed), http.StatusInternalServerError, "failed"},
		{"a failed apply", errors.New("device DEV_A: refused (nothing was changed)"), http.StatusUnprocessableEntity, "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeIO{valid: true, applyErr: tc.err}
			h := ioServer(t, f, true)
			id := stage(t, h, map[string]any{"sha": "abc1234"})
			doJSON(t, h, "POST", "/api/io/staged/"+id+"/validate", nil)
			w, st := doJSON(t, h, "POST", "/api/io/staged/"+id+"/apply", nil)
			if w.Code != tc.code || st["state"] != tc.state || st["error"] == "" {
				t.Fatalf("%d %v; want %d, state %s, the error", w.Code, st, tc.code, tc.state)
			}
		})
	}
}

// The config block rides the stream's frames, plain and delta, so the
// dashboard's line shows a drift (and a fault) without waiting for a resync.
func TestIOConfigOnTheStream(t *testing.T) {
	f := &fakeIO{fault: "device DEV_A: putting back failed", differs: []IODiffer{{Driver: "modbus", Device: "DEV_A"}}}
	for _, q := range []string{"", "?delta=1"} {
		st := openStream(t, newTestRuntime(t), q, Options{IO: f.hook()})
		for i := 0; i < 2; i++ { // the first frame and the next, a delta on ?delta=1
			st.tick()
			fr := st.next()
			if fr.Config == nil || fr.Config.Source != "applied" || fr.Config.Fault == "" {
				t.Fatalf("%q frame %d: config = %+v", q, i, fr.Config)
			}
		}
	}
	if New(newTestRuntime(t), Options{}).ioConfigState() != nil {
		t.Fatal("a controller without live I/O configuration put a config block on its frames")
	}
}

// Every write route needs online edits AND the write authorisation: with
// either missing, stage, validate and apply are refused and nothing moves.
func TestIORoutesAuthorise(t *testing.T) {
	type call struct {
		path string
		body any
	}
	calls := func(id string) []call {
		return []call{
			{"/api/io/staged", map[string]any{"sha": "abc1234"}},
			{"/api/io/staged/" + id + "/validate", nil},
			{"/api/io/staged/" + id + "/apply", nil},
		}
	}
	do := func(h http.Handler, path string, body any, token string) int {
		var buf bytes.Buffer
		if body != nil {
			json.NewEncoder(&buf).Encode(body)
		}
		req := httptest.NewRequest("POST", "http://localhost"+path, &buf)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Code
	}
	// A candidate already staged and valid, put in directly: validate and
	// apply must refuse it on their own check, not for want of one.
	seeded := func(srv *Server) {
		srv.staging.all = map[string]*stagedIO{"1": {ID: "1", State: "valid", cand: shaA}}
	}
	t.Run("online edits off", func(t *testing.T) {
		f := &fakeIO{valid: true}
		srv := New(newTestRuntime(t), ioOptions(f, false))
		seeded(srv)
		for _, c := range calls("1") {
			if code := do(srv.Handler(), c.path, c.body, ""); code != http.StatusForbidden {
				t.Errorf("%s: %d, want 403", c.path, code)
			}
		}
		if len(f.staged) != 0 || f.applied != 0 || srv.staging.all["1"].State != "valid" {
			t.Fatalf("refused writes moved something: staged %v applied %d state %s", f.staged, f.applied, srv.staging.all["1"].State)
		}
	})
	t.Run("no token", func(t *testing.T) {
		f := &fakeIO{valid: true}
		o := ioOptions(f, true)
		o.AuthToken = "s3cret"
		srv := New(newTestRuntime(t), o)
		seeded(srv)
		for _, c := range calls("1") {
			if code := do(srv.Handler(), c.path, c.body, ""); code != http.StatusUnauthorized {
				t.Errorf("%s without the token: %d, want 401", c.path, code)
			}
		}
		if len(f.staged) != 0 || f.applied != 0 || srv.staging.all["1"].State != "valid" {
			t.Fatalf("refused writes moved something: staged %v applied %d state %s", f.staged, f.applied, srv.staging.all["1"].State)
		}
		if code := do(srv.Handler(), "/api/io/staged/1/apply", nil, "s3cret"); code != http.StatusOK || f.applied != 1 {
			t.Fatalf("with the token: %d, applied %d", code, f.applied)
		}
		if by := srv.staging.all["1"]; by.State != "applied" {
			t.Fatalf("state %s", by.State)
		}
	})
}

// A validate that was still running when an apply finished does not put
// the candidate back to valid: it answers 409 and leaves "applied".
func TestIOValidateDoesNotOverwriteAnApply(t *testing.T) {
	f := &fakeIO{valid: true}
	h := ioServer(t, f, true)
	_, st := doJSON(t, h, "POST", "/api/io/staged", map[string]any{"sha": "abc1234"})
	id := st["id"].(string)
	doJSON(t, h, "POST", "/api/io/staged/"+id+"/validate", nil) // valid

	f.gate = make(chan struct{})
	done := make(chan int)
	go func() {
		w, _ := doJSON(t, h, "POST", "/api/io/staged/"+id+"/validate", nil)
		done <- w.Code
	}()
	time.Sleep(50 * time.Millisecond) // the second validate is in the engine
	if w, st := doJSON(t, h, "POST", "/api/io/staged/"+id+"/apply", nil); w.Code != http.StatusOK || st["state"] != "applied" {
		t.Fatalf("apply: %d %v", w.Code, st)
	}
	close(f.gate)
	if code := <-done; code != http.StatusConflict {
		t.Fatalf("the raced validate answered %d, want 409", code)
	}
	if _, st := doJSON(t, h, "GET", "/api/io/staged/"+id, nil); st["state"] != "applied" {
		t.Fatalf("after the raced validate, state %v, want applied", st["state"])
	}
}

// revertIfSilent rides the apply's optional body to the engine; a bad one
// is refused before anything is applied.
func TestIORoutesApplyRevertIfSilent(t *testing.T) {
	for _, tc := range []struct {
		body any
		code int
		want time.Duration
	}{
		{nil, http.StatusOK, 0},
		{map[string]any{"revertIfSilent": "2s"}, http.StatusOK, 2 * time.Second},
		{map[string]any{"revertIfSilent": "soon"}, http.StatusBadRequest, 0},
		{map[string]any{"revertIfSilent": "1h"}, http.StatusBadRequest, 0},
		{map[string]any{"revertIfSilent": "-1s"}, http.StatusBadRequest, 0},
	} {
		f := &fakeIO{valid: true}
		h := ioServer(t, f, true)
		_, st := doJSON(t, h, "POST", "/api/io/staged", map[string]any{"sha": "abc1234"})
		id := st["id"].(string)
		doJSON(t, h, "POST", "/api/io/staged/"+id+"/validate", nil)
		w, _ := doJSON(t, h, "POST", "/api/io/staged/"+id+"/apply", tc.body)
		applied := 1
		if tc.code != http.StatusOK {
			applied = 0
		}
		if w.Code != tc.code || f.revert != tc.want || f.applied != applied {
			t.Errorf("%v: %d, revert %v, applied %d; want %d, %v, %d", tc.body, w.Code, f.revert, f.applied, tc.code, tc.want, applied)
		}
	}
}

// A pending revert is on the config block while it watches, and the cancel
// route stops it; with none pending it answers 409. Cancelling needs the
// write authorisation like every I/O write.
func TestIORoutesPendingRevert(t *testing.T) {
	f := &fakeIO{}
	h := ioServer(t, f, true)
	if w, _ := doJSON(t, h, "POST", "/api/io/revert/cancel", nil); w.Code != http.StatusConflict {
		t.Fatalf("no watch: %d, want 409", w.Code)
	}
	f.watch = &IOWatch{Gen: 3, Revert: true, State: "watching", Pending: []string{"device DEV_A"}, Deadline: time.Now().Add(time.Minute)}
	_, st := doJSON(t, h, "GET", "/api/state", nil)
	if c, _ := st["config"].(map[string]any); !strings.Contains(fmt.Sprint(c["watch"]), "DEV_A") {
		t.Fatalf("config = %v, want the pending revert", st["config"])
	}
	if _, st := doJSON(t, h, "GET", "/api/io", nil); !strings.Contains(fmt.Sprint(st["watch"]), "watching") {
		t.Fatalf("/api/io watch = %v", st["watch"])
	}
	if code := ioPost(t, New(newTestRuntime(t), func() Options { o := ioOptions(f, false); return o }()).Handler(), "/api/io/revert/cancel", nil, ""); code != http.StatusForbidden || f.cancels != 0 {
		t.Fatalf("online edits off: %d, cancels %d", code, f.cancels)
	}
	w, st := doJSON(t, h, "POST", "/api/io/revert/cancel", nil)
	if w.Code != http.StatusOK || st["state"] != "cancelled" || f.cancels != 1 {
		t.Fatalf("cancel: %d %v", w.Code, st)
	}
	_, st = doJSON(t, h, "GET", "/api/state", nil)
	if c, _ := st["config"].(map[string]any); c["watch"] != nil {
		t.Fatalf("config = %v after the cancel, want no pending watch", st["config"])
	}
}

func ioPost(t *testing.T, h http.Handler, path string, body any, token string) int {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest("POST", "http://localhost"+path, &buf)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w.Code
}

// GET /api/io/files serves what naut pull --io writes: the running bytes
// per file, who leads, and the conflicts; a controller without the hook
// says so.
func TestIOFilesRoute(t *testing.T) {
	f := &fakeIO{}
	hook := f.hook()
	hook.Files = func() IOFiles {
		return IOFiles{Leading: true, Units: []IODiffer{{Driver: "modbus", Device: "DEV_A"}},
			Files: []IOFile{{Path: "modbus.yaml", Running: "a: 2\n", Booted: "abc"}}, NonIO: true}
	}
	o := ioOptions(f, true)
	o.IO = hook
	h := New(newTestRuntime(t), o).Handler()
	w, body := doJSON(t, h, "GET", "/api/io/files", nil)
	if w.Code != http.StatusOK || body["leading"] != true || body["nonIO"] != true {
		t.Fatalf("files: %d %v", w.Code, body)
	}
	files, _ := body["files"].([]any)
	if len(files) != 1 || fmt.Sprint(files[0]) != "map[booted:abc path:modbus.yaml running:a: 2\n]" {
		t.Fatalf("files = %v", body["files"])
	}
	hook.Files = nil
	if w, _ := doJSON(t, h, "GET", "/api/io/files", nil); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("without the hook: %d, want 503", w.Code)
	}
	if w, _ := doJSON(t, New(newTestRuntime(t), Options{OnlineEdits: true}).Handler(), "GET", "/api/io/files", nil); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("without live I/O: %d, want 503", w.Code)
	}
}

// The config screens' two read routes (#711): a schema slice per section,
// a list of sections without one, and the manifest text.
func TestConfigSchemaAndFilesRoutes(t *testing.T) {
	f := &fakeIO{}
	hook := f.hook()
	hook.Sections = func() []string { return []string{"devices", "drivers"} }
	hook.Schema = func(s string, _ bool) ([]byte, error) {
		if s != "drivers" {
			return nil, fmt.Errorf("%w %q", ErrConfigNoSection, s)
		}
		return []byte(`{"title":"drivers"}`), nil
	}
	hook.ConfigFiles = func() (ConfigFiles, error) {
		return ConfigFiles{Leading: true, Manifest: "nautilus.yaml", Files: map[string]string{"nautilus.yaml": "name: x\n"}, Differs: []string{"modbus.yaml"}}, nil
	}
	o := ioOptions(f, true)
	o.IO = hook
	h := New(newTestRuntime(t), o).Handler()

	w, body := doJSON(t, h, "GET", "/api/config/schema", nil)
	if w.Code != http.StatusOK || fmt.Sprint(body["sections"]) != "[devices drivers]" {
		t.Fatalf("list: %d %v", w.Code, body)
	}
	w, body = doJSON(t, h, "GET", "/api/config/schema?section=drivers", nil)
	if w.Code != http.StatusOK || body["title"] != "drivers" || !strings.Contains(w.Header().Get("Content-Type"), "schema+json") {
		t.Fatalf("slice: %d %v %q", w.Code, body, w.Header().Get("Content-Type"))
	}
	if w, _ := doJSON(t, h, "GET", "/api/config/schema?section=nope", nil); w.Code != http.StatusNotFound {
		t.Fatalf("unknown section: %d, want 404", w.Code)
	}
	w, body = doJSON(t, h, "GET", "/api/config/files", nil)
	if w.Code != http.StatusOK || body["manifest"] != "nautilus.yaml" || body["leading"] != true ||
		fmt.Sprint(body["files"]) != "map[nautilus.yaml:name: x\n]" || fmt.Sprint(body["differs"]) != "[modbus.yaml]" {
		t.Fatalf("files: %d %v", w.Code, body)
	}

	hook.Schema, hook.ConfigFiles = nil, nil
	for _, p := range []string{"/api/config/schema?section=drivers", "/api/config/files"} {
		if w, _ := doJSON(t, h, "GET", p, nil); w.Code != http.StatusServiceUnavailable {
			t.Errorf("%s without the hook: %d, want 503", p, w.Code)
		}
		if w, _ := doJSON(t, New(newTestRuntime(t), Options{OnlineEdits: true}).Handler(), "GET", p, nil); w.Code != http.StatusServiceUnavailable {
			t.Errorf("%s without live I/O: %d, want 503", p, w.Code)
		}
	}
}

// config.commit is classed strict: refused while the journal is down, like
// the other configuration actions.
func TestConfigCommitIsStrict(t *testing.T) {
	if actionClass["config.commit"] != strict {
		t.Fatalf("config.commit class = %q, want strict", actionClass["config.commit"])
	}
}

// GET /api/config/files is the whole manifest: it needs the permission to
// edit it (online edits on, the token when one is set), not the plain read
// of /api/io/files. The schema is static and stays a plain read.
func TestConfigFilesNeedTheEditPermission(t *testing.T) {
	f := &fakeIO{}
	hook := f.hook()
	hook.Files = func() IOFiles { return IOFiles{Leading: true} }
	hook.Sections = func() []string { return []string{"drivers"} }
	hook.Schema = func(string, bool) ([]byte, error) { return []byte(`{}`), nil }
	hook.ConfigFiles = func() (ConfigFiles, error) {
		return ConfigFiles{Manifest: "nautilus.yaml", Files: map[string]string{"nautilus.yaml": "x"}}, nil
	}
	get := func(h http.Handler, path, token string) int {
		req := httptest.NewRequest("GET", "http://localhost"+path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Code
	}
	o := ioOptions(f, false)
	o.IO = hook
	if code := get(New(newTestRuntime(t), o).Handler(), "/api/config/files", ""); code != http.StatusForbidden {
		t.Errorf("/api/config/files, online edits off: %d, want 403", code)
	}
	// Changes applied while edits were on stay pullable into git once they
	// are off: /api/io/files wants the permission (the token), not the
	// online-edits switch.
	o.AuthToken = "s3cret"
	off := New(newTestRuntime(t), o).Handler()
	if code := get(off, "/api/io/files", ""); code != http.StatusUnauthorized {
		t.Errorf("/api/io/files, edits off, no token: %d, want 401", code)
	}
	if code := get(off, "/api/io/files", "s3cret"); code != http.StatusOK {
		t.Errorf("/api/io/files, edits off, with the token: %d, want 200", code)
	}
	o = ioOptions(f, true)
	o.IO, o.AuthToken = hook, "s3cret"
	h := New(newTestRuntime(t), o).Handler()
	for _, p := range []string{"/api/config/files", "/api/io/files"} {
		if code := get(h, p, ""); code != http.StatusUnauthorized {
			t.Errorf("%s, no token: %d, want 401", p, code)
		}
		if code := get(h, p, "s3cret"); code != http.StatusOK {
			t.Errorf("%s, with the token: %d, want 200", p, code)
		}
	}
	if code := get(h, "/api/config/schema?section=drivers", ""); code != http.StatusOK {
		t.Errorf("schema without a token: %d, want 200", code)
	}
}

// The schema route offers the project's file names only to a caller who
// holds the write permission (the rule GET /api/io/files has); anyone else
// gets the schema, and no error, without them.
func TestConfigSchemaNamesNeedTheWritePermission(t *testing.T) {
	f := &fakeIO{}
	hook := f.hook()
	hook.Sections = func() []string { return []string{"devices"} }
	var asked []bool
	hook.Schema = func(_ string, names bool) ([]byte, error) {
		asked = append(asked, names)
		return []byte(`{}`), nil
	}
	o := ioOptions(f, true)
	o.IO, o.AuthToken = hook, "s3cret"
	h := New(newTestRuntime(t), o).Handler()
	get := func(token string) int {
		req := httptest.NewRequest("GET", "http://localhost/api/config/schema?section=devices", nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Code
	}
	if code := get(""); code != http.StatusOK {
		t.Fatalf("no token: %d, want 200 (a plain read)", code)
	}
	if code := get("s3cret"); code != http.StatusOK {
		t.Fatalf("with the token: %d, want 200", code)
	}
	if len(asked) != 2 || asked[0] || !asked[1] {
		t.Fatalf("names asked = %v, want [false true]", asked)
	}
}
