package server

// The live I/O routes (#706 PR 3b) over a fake engine: every state, every
// refusal and its status, and the config block on /api/state and the
// stream. The engine itself is tested in internal/project.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

type fakeIO struct {
	staged   []string // what Stage was asked for
	valid    bool
	drivers  []string
	applyErr error
	differs  []IODiffer
	fault    string
	applied  int
}

func (f *fakeIO) hook() *IOHook {
	return &IOHook{
		Stage: func(sha string, files map[string][]byte, by string) (any, error) {
			if sha == "nosnap" {
				return nil, fmt.Errorf("%w: nosnap", ErrIONoSnapshot)
			}
			what := sha
			if sha == "" {
				what = "files:" + string(files["modbus.yaml"])
			}
			f.staged = append(f.staged, what)
			return what, nil
		},
		Validate: func(cand any) (bool, []string, any) {
			return f.valid, f.drivers, map[string]any{"valid": f.valid, "cand": cand}
		},
		Apply: func(ctx context.Context, cand any, by string) (any, error) {
			if f.applyErr != nil {
				return nil, f.applyErr
			}
			f.applied++
			f.differs = []IODiffer{{Driver: "modbus", Device: "DEV_A", ManifestHash: "a", RunningHash: "b", AppliedFrom: "upload", By: by}}
			return map[string]any{"commanded": []any{}}, nil
		},
		Running: func() any { return []string{"modbus"} },
		Differs: func() []IODiffer { return f.differs },
		Fault:   func() string { return f.fault },
	}
}

func ioServer(t *testing.T, f *fakeIO, online bool) http.Handler {
	t.Helper()
	return New(newTestRuntime(t), Options{OnlineEdits: online, IO: f.hook()}).Handler()
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
		w, _ := doJSON(t, ioServer(t, f, false), "POST", "/api/io/staged", map[string]any{"sha": "abc"})
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
		if w, _ := doJSON(t, ioServer(t, &fakeIO{}, true), "POST", "/api/io/staged", map[string]any{"sha": "nosnap"}); w.Code != http.StatusConflict {
			t.Fatalf("%d, want 409", w.Code)
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
		id := stage(t, h, map[string]any{"sha": "abc"})
		if w, _ := doJSON(t, h, "POST", "/api/io/staged/"+id+"/apply", nil); w.Code != http.StatusConflict || f.applied != 0 {
			t.Fatalf("%d, applied %d; want 409, nothing applied", w.Code, f.applied)
		}
	})
	t.Run("an invalid candidate", func(t *testing.T) {
		f := &fakeIO{valid: false}
		h := ioServer(t, f, true)
		id := stage(t, h, map[string]any{"sha": "abc"})
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
			id := stage(t, h, map[string]any{"sha": "abc"})
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
