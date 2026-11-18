package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/runtime"
)

// newTaskRuntime is a controller whose second task ("totals") keeps retained
// state in the program: a counter and a running TON.
func newTaskRuntime(t *testing.T) *runtime.Runtime {
	t.Helper()
	rt, err := runtime.New(runtime.Options{
		Program: testProgram,
		Driver:  nio.NewMemory(),
		Seed:    nio.Values{"Level": 40.0, "SP": 65.0, "Out": 0.0, "Count": 0.0, "Ticks": 0.0},
		Tasks: []runtime.Task{{
			Name: "totals",
			Program: `PROGRAM Totals
VAR_EXTERNAL Count : REAL; END_VAR
VAR n : REAL; END_VAR
n := n + 1.0;
Count := n;
END_PROGRAM`,
			Scan: time.Second,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return rt
}

// Online edits: retained state carries across a swap of a non-main task's
// program the same way as the main task's — pushed over the API, routed by
// the POU name.
func TestTaskProgramOnlineEditCarriesRetainedState(t *testing.T) {
	rt := newTaskRuntime(t)
	h := New(rt, Options{OnlineEdits: true}).Handler()
	for i := 0; i < 5; i++ {
		if err := rt.ScanTask("totals"); err != nil {
			t.Fatal(err)
		}
	}
	if got := rt.Tags().Real("Count"); got != 5 {
		t.Fatalf("Count before the edit = %v, want 5", got)
	}

	// The edit counts by 10 now, and adds a new local (which must reset).
	edited := `PROGRAM Totals
VAR_EXTERNAL Count : REAL; END_VAR
VAR n : REAL; fresh : REAL; END_VAR
n := n + 10.0;
fresh := fresh + 1.0;
Count := n;
END_PROGRAM`
	w, body := doJSON(t, h, "PUT", "/api/program", map[string]string{"source": edited})
	if w.Code != http.StatusOK || body["task"] != "totals" {
		t.Fatalf("PUT = %d %v, want 200 on task totals", w.Code, body)
	}
	if err := rt.ScanTask("totals"); err != nil {
		t.Fatal(err)
	}
	// 5 carried across + 10, not 0 + 10.
	if got := rt.Tags().Real("Count"); got != 15 {
		t.Errorf("Count after the swap = %v, want 15 (the task's n carried across)", got)
	}
	if resets, _ := body["resets"].([]any); len(resets) == 0 {
		t.Errorf("the new variable was not reported as a reset: %v", body)
	}
}

// Online edits: ?task= selects a program explicitly, for GET and rollback,
// just as ?pou= does.
func TestProgramTaskSelectorOnGetAndRollback(t *testing.T) {
	rt := newTaskRuntime(t)
	h := New(rt, Options{OnlineEdits: true}).Handler()

	w, body := doJSON(t, h, "GET", "/api/program?task=totals", nil)
	if w.Code != http.StatusOK || body["task"] != "totals" || body["pou"] != "Totals" {
		t.Fatalf("GET ?task=totals = %d %v", w.Code, body)
	}
	w, body = doJSON(t, h, "GET", "/api/program?task=main", nil)
	if w.Code != http.StatusOK || body["pou"] != "Test" {
		t.Fatalf("GET ?task=main = %d %v", w.Code, body)
	}
	if w, _ = doJSON(t, h, "GET", "/api/program?task=nope", nil); w.Code != http.StatusNotFound {
		t.Fatalf("GET ?task=nope = %d, want 404", w.Code)
	}

	edited := strings.Replace(rt.TaskProgram("totals").Source(), "n + 1.0", "n + 2.0", 1)
	if w, _ = doJSON(t, h, "PUT", "/api/program", map[string]string{"source": edited}); w.Code != http.StatusOK {
		t.Fatalf("PUT = %d", w.Code)
	}
	if !strings.Contains(rt.TaskProgram("totals").Source(), "n + 2.0") {
		t.Fatal("the edit did not reach the task")
	}
	if w, _ = doJSON(t, h, "POST", "/api/program/rollback?task=totals", nil); w.Code != http.StatusOK {
		t.Fatalf("rollback ?task=totals = %d", w.Code)
	}
	if !strings.Contains(rt.TaskProgram("totals").Source(), "n + 1.0") {
		t.Error("rollback ?task=totals did not restore the task's prior source")
	}
	if strings.Contains(rt.Program().Source(), "n + ") {
		t.Error("the main program was disturbed")
	}
}

// Online edits: with a token configured, online-edit endpoints (PUT
// /api/program, rollback, activate) require it while reads — which is all
// `naut pull` does — stay open.
func TestOnlineEditsRequireTheTokenButReadsStayOpen(t *testing.T) {
	rt := newTaskRuntime(t)
	h := New(rt, Options{OnlineEdits: true, AuthToken: "s3cret"}).Handler()

	send := func(method, path, body, auth string) int {
		req := httptest.NewRequest(method, "http://localhost"+path, bytes.NewBufferString(body))
		req.Header.Set("Origin", "http://plc.local") // same-origin: only the token can tell these apart
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	put := `{"source": ` + jsonString(editedProgram) + `}`

	for _, tc := range []struct{ method, path, body string }{
		{"PUT", "/api/program", put},
		{"POST", "/api/program/rollback", ""},
		{"POST", "/api/program/activate", `{"sha": "abc123"}`},
	} {
		if code := send(tc.method, tc.path, tc.body, ""); code != http.StatusUnauthorized {
			t.Errorf("%s %s without the token = %d, want 401", tc.method, tc.path, code)
		}
		if code := send(tc.method, tc.path, tc.body, "Bearer wrong"); code != http.StatusUnauthorized {
			t.Errorf("%s %s with a wrong token = %d, want 401", tc.method, tc.path, code)
		}
	}
	if got := rt.Program().Source(); strings.Contains(got, "* 2.0") {
		t.Fatal("a refused PUT changed the running program")
	}

	// With the token the same push is accepted.
	if code := send("PUT", "/api/program", put, "Bearer s3cret"); code != http.StatusOK {
		t.Errorf("PUT with the token = %d, want 200", code)
	}
	if !strings.Contains(rt.Program().Source(), "* 2.0") {
		t.Error("the authorised push did not land")
	}

	// Reads (what `naut pull` uses) need no token.
	for _, path := range []string{"/api/program", "/api/program?task=totals", "/api/state", "/api/meta"} {
		if code := send("GET", path, "", ""); code != http.StatusOK {
			t.Errorf("GET %s without a token = %d, want 200", path, code)
		}
	}
}

func jsonString(s string) string {
	var b bytes.Buffer
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
