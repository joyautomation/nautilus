package project

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/runtime"
	"github.com/joyautomation/nautilus/server"
)

// Online edits: "Edits are ephemeral by design — a restart reverts to the
// program the binary embeds." A project with online-edits on (and no retain
// store) is edited through PUT /api/program; a restart — a fresh runtime from
// the same embedded project — runs the original again.
func TestOnlineEditDoesNotSurviveARestart(t *testing.T) {
	files := map[string]string{
		"nautilus.yaml": "server:\n  online-edits: true\ntasks:\n  - program: main.st\ntags:\n  - { name: Out, role: state, init: 0.0 }\n",
		"main.st":       "PROGRAM Main\nOut := 1.0;\nEND_PROGRAM\n",
	}
	boot := func() (*runtime.Runtime, http.Handler) {
		t.Helper()
		proj, err := Load(mapProject(files), "")
		if err != nil {
			t.Fatal(err)
		}
		rt, err := runtime.New(proj.Runtime)
		if err != nil {
			t.Fatal(err)
		}
		return rt, server.New(rt, proj.Server).Handler()
	}

	rt, h := boot()
	rt.Scan()
	if got := readReal(t, rt, "Out"); got != 1 {
		t.Fatalf("embedded program: Out = %v, want 1", got)
	}
	req := httptest.NewRequest("PUT", "http://localhost/api/program",
		bytes.NewBufferString(`{"source": "PROGRAM Main\nOut := 2.0;\nEND_PROGRAM\n"}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("online edit = %d %s", rec.Code, rec.Body)
	}
	rt.Scan()
	if got := readReal(t, rt, "Out"); got != 2 {
		t.Fatalf("after the online edit Out = %v, want 2", got)
	}
	if !strings.Contains(rt.Program().Source(), "2.0") {
		t.Fatal("the edit is not the running program")
	}

	// Restart: the process is gone, the binary's embedded project is all there is.
	rt2, _ := boot()
	rt2.Scan()
	if got := readReal(t, rt2, "Out"); got != 1 {
		t.Errorf("after a restart Out = %v, want 1 (the embedded program, not the edit)", got)
	}
	if strings.Contains(rt2.Program().Source(), "2.0") {
		t.Error("the online edit survived a restart")
	}
}
