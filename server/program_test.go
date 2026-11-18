package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/runtime"
)

const editedProgram = `PROGRAM Test
VAR_EXTERNAL
  Level : REAL;
  SP : REAL;
  Out : REAL;
END_VAR
Out := (SP - Level) * 2.0;
END_PROGRAM
`

func doJSON(t *testing.T, h http.Handler, method, path string, body any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, "http://localhost"+path, &buf)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	out := map[string]any{}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w, out
}

// Task programs are addressed by POU name: a PUT routes automatically by
// the submitted source's PROGRAM name, GET/rollback take ?pou= / ?task=.
func TestProgramTaskRouting(t *testing.T) {
	drv := nio.NewMemory()
	rt, err := runtime.New(runtime.Options{
		Program: testProgram, // PROGRAM Test
		Driver:  drv,
		Seed:    nio.Values{"Level": 40.0, "SP": 65.0, "Out": 0.0, "TotalL": 0.0},
		Tasks: []runtime.Task{{
			Name: "totals",
			Program: `PROGRAM Totals
VAR_EXTERNAL Out : REAL; TotalL : REAL; END_VAR
TotalL := TotalL + Out;
END_PROGRAM`,
			Scan: time.Second,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	h := New(rt, Options{OnlineEdits: true}).Handler()

	// The directory lists every program with its POU.
	w, body := doJSON(t, h, "GET", "/api/program", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET = %d", w.Code)
	}
	progs, _ := body["programs"].([]any)
	if len(progs) != 2 {
		t.Fatalf("directory should list 2 programs, got %v", body["programs"])
	}

	// ?pou= selects the task's program.
	w, body = doJSON(t, h, "GET", "/api/program?pou=Totals", nil)
	if w.Code != http.StatusOK || body["task"] != "totals" || body["pou"] != "Totals" {
		t.Fatalf("GET ?pou=Totals = %d %v", w.Code, body)
	}
	w, _ = doJSON(t, h, "GET", "/api/program?pou=Nope", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown pou = %d, want 404", w.Code)
	}

	// A PUT with the task's POU routes to the task, not main.
	edited := `PROGRAM Totals
VAR_EXTERNAL Out : REAL; TotalL : REAL; END_VAR
TotalL := TotalL + Out * 2.0;
END_PROGRAM`
	w, body = doJSON(t, h, "PUT", "/api/program", map[string]string{"source": edited})
	if w.Code != http.StatusOK || body["task"] != "totals" {
		t.Fatalf("PUT by POU = %d %v, want task totals", w.Code, body)
	}
	if src := rt.TaskProgram("totals").Source(); !strings.Contains(src, "* 2.0") {
		t.Fatalf("task program not swapped:\n%s", src)
	}
	if src := rt.Program().Source(); strings.Contains(src, "* 2.0") {
		t.Fatal("main program must be untouched")
	}

	// With tasks present, an unknown POU must NOT silently retarget main.
	w, _ = doJSON(t, h, "PUT", "/api/program", map[string]string{"source": "PROGRAM Ghost\nEND_PROGRAM"})
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown POU with tasks = %d, want 404", w.Code)
	}

	// Rollback per program.
	w, _ = doJSON(t, h, "POST", "/api/program/rollback?pou=Totals", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("rollback ?pou= = %d", w.Code)
	}
	if src := rt.TaskProgram("totals").Source(); strings.Contains(src, "* 2.0") {
		t.Fatal("task rollback did not restore the prior source")
	}
}

func TestProgramEndpointsGated(t *testing.T) {
	rt := newTestRuntime(t)
	h := New(rt).Handler() // OnlineEdits not enabled

	w, _ := doJSON(t, h, "GET", "/api/program", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET gated? code %d — reads must stay open", w.Code)
	}
	w, body := doJSON(t, h, "PUT", "/api/program", map[string]string{"source": editedProgram})
	if w.Code != http.StatusForbidden {
		t.Fatalf("PUT without OnlineEdits = %d, want 403 (%v)", w.Code, body)
	}
	w, _ = doJSON(t, h, "POST", "/api/program/rollback", nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("rollback without OnlineEdits = %d, want 403", w.Code)
	}
}

func TestProgramOnlineEditFlow(t *testing.T) {
	rt := newTestRuntime(t)
	h := New(rt, Options{OnlineEdits: true}).Handler()

	// Baseline.
	w, info := doJSON(t, h, "GET", "/api/program", nil)
	if w.Code != http.StatusOK || info["dirty"] != false || info["editable"] != true {
		t.Fatalf("GET = %d %v", w.Code, info)
	}
	baseHash, _ := info["hash"].(string)

	// Push with a stale base → conflict.
	w, _ = doJSON(t, h, "PUT", "/api/program", map[string]string{"source": editedProgram, "baseHash": "wrong"})
	if w.Code != http.StatusConflict {
		t.Fatalf("stale base = %d, want 409", w.Code)
	}

	// Push a broken program → 422, still running the original.
	w, body := doJSON(t, h, "PUT", "/api/program", map[string]string{"source": "PROGRAM X\nOut := ;\nEND_PROGRAM"})
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("broken program = %d %v, want 422", w.Code, body)
	}
	if _, info = doJSON(t, h, "GET", "/api/program", nil); info["hash"] != baseHash {
		t.Fatalf("failed push changed the running program")
	}

	// A good push with the right base succeeds and flips dirty.
	w, body = doJSON(t, h, "PUT", "/api/program", map[string]string{"source": editedProgram, "baseHash": baseHash})
	if w.Code != http.StatusOK {
		t.Fatalf("push = %d %v", w.Code, body)
	}
	rt.Scan()
	if _, info = doJSON(t, h, "GET", "/api/program", nil); info["dirty"] != true || info["canRollback"] != true {
		t.Fatalf("after push: %v", info)
	}
	if !strings.Contains(info["source"].(string), "* 2.0") {
		t.Fatalf("controller source not updated")
	}
	// The edited logic actually ran: Out = (65-40)*2.
	if out := rt.Tags().Real("Out"); out != 50.0 {
		t.Fatalf("Out after edited scan = %v, want 50", out)
	}

	// Rollback restores the original, clean.
	w, _ = doJSON(t, h, "POST", "/api/program/rollback", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("rollback = %d", w.Code)
	}
	rt.Scan()
	if out := rt.Tags().Real("Out"); out != 25.0 {
		t.Fatalf("Out after rollback scan = %v, want 25", out)
	}
	if _, info = doJSON(t, h, "GET", "/api/program", nil); info["dirty"] != false {
		t.Fatalf("rollback should clear dirty: %v", info)
	}
	// Second rollback has nothing to restore.
	if w, _ = doJSON(t, h, "POST", "/api/program/rollback", nil); w.Code != http.StatusConflict {
		t.Fatalf("second rollback = %d, want 409", w.Code)
	}
}
