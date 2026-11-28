package main

// Claims about `naut test` itself (TEST-014, TEST-027): the exit code and
// -run, and that a test never opens a socket whatever `driver:` says.

import (
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func claimsProject(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const claimsProgram = `PROGRAM Main
VAR_EXTERNAL
    In  : BOOL;
    Out : BOOL;
END_VAR
Out := In;
END_PROGRAM`

// TEST-027: a failing test makes `naut test` exit non-zero, and -run keeps
// only the tests whose name matches.
func TestNautTestExitCodeAndRunFilter(t *testing.T) {
	dir := claimsProject(t, map[string]string{
		"nautilus.yaml": "tasks:\n  - program: main.st\ntags:\n  - { name: In, role: input, init: false }\n  - { name: Out, role: output, init: false }\n",
		"main.st":       claimsProgram,
		"a_test.yaml": `tests:
  - name: follows the input
    steps:
      - given: { In: true }
        scans: 1
        expect: { Out: true }
  - name: broken expectation
    steps:
      - given: { In: true }
        scans: 1
        expect: { Out: false }
`,
	})
	for _, c := range []struct {
		args []string
		want int
	}{
		{[]string{dir}, 1},
		{[]string{"-run", "^follows", dir}, 0},
		{[]string{"-run", "^broken", dir}, 1},
	} {
		if got := runTest(c.args); got != c.want {
			t.Errorf("naut test %v = %d, want %d", c.args, got, c.want)
		}
	}
}

// TEST-014: a project whose driver is eip, pointing at a socket that is
// listening, is tested without one connection reaching it.
func TestNautTestNeverOpensASocket(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var conns atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			conns.Add(1)
			c.Close()
		}
	}()
	// The control: the listener does count a dial, so zero means zero.
	if c, err := net.Dial("tcp", ln.Addr().String()); err == nil {
		c.Close()
	}
	for i := 0; i < 100 && conns.Load() != 1; i++ {
		time.Sleep(20 * time.Millisecond)
	}
	if conns.Load() != 1 {
		t.Fatalf("the listener counted %d dials of the control, want 1", conns.Load())
	}
	conns.Store(0)
	dir := claimsProject(t, map[string]string{
		"nautilus.yaml":     "tasks:\n  - program: main.st\ntags:\n  - { name: In, role: input, type: BOOL, init: false }\n  - { name: Out, role: output, init: false }\ndriver:\n  type: eip\n  host: " + ln.Addr().String() + "\n  manifest: eip_manifest.yaml\n  scan-rate: 100ms\n",
		"eip_manifest.yaml": "tags:\n  - name: In\n    device: In\n    type: BOOL\n    arraylen: 0\n    writable: false\n",
		"main.st":           claimsProgram,
		"a_test.yaml":       "tests:\n  - name: follows the input\n    steps:\n      - given: { In: true }\n        scans: 1\n        expect: { Out: true }\n",
	})
	if got := runTest([]string{dir}); got != 0 {
		t.Fatalf("naut test = %d, want 0", got)
	}
	time.Sleep(300 * time.Millisecond) // a driver would have dialled by now
	if n := conns.Load(); n != 0 {
		t.Errorf("the test dialled the driver's host %d time(s)", n)
	}
}
