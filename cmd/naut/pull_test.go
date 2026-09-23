package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/runtime"
	"github.com/joyautomation/nautilus/server"
)

// A real runtime + server as the fake field controller: pull is exercised
// against the exact API it will meet, not a hand-rolled JSON shape.
func pullTestController(t *testing.T) (host string, port string, rt *runtime.Runtime) {
	t.Helper()
	lib := "FUNCTION_BLOCK Doubler\nVAR_INPUT IN : REAL; END_VAR\nVAR_OUTPUT OUT : REAL; END_VAR\nOUT := IN * 2.0;\nEND_FUNCTION_BLOCK\n"
	mainProg := "PROGRAM Main\nVAR_EXTERNAL x : REAL; y : REAL; END_VAR\ny := x + 1.0;\nEND_PROGRAM\n"
	totProg := "PROGRAM Totals\nVAR_EXTERNAL y : REAL; TotalL : REAL; END_VAR\nTotalL := TotalL + y;\nEND_PROGRAM\n"
	var err error
	rt, err = runtime.New(runtime.Options{
		Program:   mainProg,
		Libraries: []string{lib},
		Seed:      map[string]any{"x": 1.0, "y": 0.0, "TotalL": 0.0},
		Tasks:     []runtime.Task{{Name: "totals", Program: totProg}},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(server.New(rt, server.Options{OnlineEdits: true}).Handler())
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	return u.Hostname(), u.Port(), rt
}

func writeProject(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, src := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// Multi-task pull: every program reconciles to the file declaring its POU;
// a controller program with no local file is created as <POU>.st.
func TestPullMultiTask(t *testing.T) {
	host, port, rt := pullTestController(t)

	lib := "FUNCTION_BLOCK Doubler\nVAR_INPUT IN : REAL; END_VAR\nVAR_OUTPUT OUT : REAL; END_VAR\nOUT := IN * 2.0;\nEND_FUNCTION_BLOCK\n"
	dir := writeProject(t, map[string]string{
		"blocks.st": lib,
		// Matches the controller's main program byte-for-byte.
		"program.st": "PROGRAM Main\nVAR_EXTERNAL x : REAL; y : REAL; END_VAR\ny := x + 1.0;\nEND_PROGRAM\n",
		// Stale local copy of the totals task.
		"totals.st": "PROGRAM Totals\nVAR_EXTERNAL y : REAL; TotalL : REAL; END_VAR\nTotalL := 0.0;\nEND_PROGRAM\n",
	})

	// --check first: totals drifts, main doesn't → exit 1.
	if code := runPull([]string{"--host", host, "--port", port, "--dir", dir, "--check"}); code != 1 {
		t.Fatalf("--check with drift = %d, want 1", code)
	}
	// Nothing was written in check mode.
	raw, _ := os.ReadFile(filepath.Join(dir, "totals.st"))
	if !strings.Contains(string(raw), "TotalL := 0.0") {
		t.Fatal("--check must not write")
	}

	// Pull: totals.st updates, program.st untouched.
	if code := runPull([]string{"--host", host, "--port", port, "--dir", dir}); code != 0 {
		t.Fatalf("pull = %d, want 0", code)
	}
	raw, _ = os.ReadFile(filepath.Join(dir, "totals.st"))
	if !strings.Contains(string(raw), "TotalL := TotalL + y") {
		t.Fatalf("totals.st not reconciled:\n%s", raw)
	}
	// Idempotent: everything in sync → 0, and --check agrees.
	if code := runPull([]string{"--host", host, "--port", port, "--dir", dir, "--check"}); code != 0 {
		t.Fatalf("post-pull --check = %d, want 0", code)
	}

	// A brand-new program on the controller lands in <POU>.st: simulate by
	// removing the local file, then pull again.
	if err := os.Remove(filepath.Join(dir, "totals.st")); err != nil {
		t.Fatal(err)
	}
	if code := runPull([]string{"--host", host, "--port", port, "--dir", dir}); code != 0 {
		t.Fatalf("pull into new file = %d, want 0", code)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "Totals.st"))
	if err != nil {
		t.Fatalf("expected Totals.st to be created: %v", err)
	}
	if !strings.Contains(string(raw), "PROGRAM Totals") {
		t.Fatalf("created file wrong:\n%s", raw)
	}
	_ = rt
}

// Single-program controllers keep the classic rename-tolerant behavior:
// the one program pulls into the one program file regardless of POU.
func TestPullSingleProgramRenameTolerant(t *testing.T) {
	prog := "PROGRAM Renamed\nVAR_EXTERNAL x : REAL; END_VAR\nx := 1.0;\nEND_PROGRAM\n"
	rt, err := runtime.New(runtime.Options{Program: prog})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(server.New(rt, server.Options{}).Handler())
	defer srv.Close()
	u, _ := url.Parse(srv.URL)

	dir := writeProject(t, map[string]string{
		"program.st": "PROGRAM Old\nVAR_EXTERNAL x : REAL; END_VAR\nx := 2.0;\nEND_PROGRAM\n",
	})
	if code := runPull([]string{"--host", u.Hostname(), "--port", u.Port(), "--dir", dir}); code != 0 {
		t.Fatalf("pull = %d, want 0", code)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "program.st"))
	if !strings.Contains(string(raw), "PROGRAM Renamed") {
		t.Fatalf("program.st not updated:\n%s", raw)
	}
}

var _ = http.StatusOK
