package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
	req := httptest.NewRequest(method, path, &buf)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	out := map[string]any{}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w, out
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
