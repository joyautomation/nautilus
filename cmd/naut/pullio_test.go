package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/server"
)

func sha256hex(s string) string { b := sha256.Sum256([]byte(s)); return hex.EncodeToString(b[:]) }

// ioController serves st on /api/io/files and returns its host and port.
func ioController(t *testing.T, st server.IOFiles) (string, int) {
	t.Helper()
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/io/files" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(st)
	}))
	t.Cleanup(src.Close)
	host, p, _ := net.SplitHostPort(src.Listener.Addr().String())
	port, _ := strconv.Atoi(p)
	return host, port
}

func ioState(booted, running string) server.IOFiles {
	return server.IOFiles{
		Leading: true,
		Units:   []server.IODiffer{{Driver: "modbus", Device: "DEV_A", AppliedFrom: "upload"}},
		Files:   []server.IOFile{{Path: "modbus.yaml", Running: running, Booted: sha256hex(booted)}},
	}
}

func projectWith(t *testing.T, name, body string) string {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func readFile(t *testing.T, dir, name string) string {
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestPullIOWritesTheRunningFiles(t *testing.T) {
	host, port := ioController(t, ioState("a: 1\nb: 2\n", "a: 1\nb: 3\n"))
	dir := projectWith(t, "modbus.yaml", "a: 1\nb: 2\n")
	if c := pullIO(host, port, dir, false, false, false); c != 0 {
		t.Fatalf("exit %d, want 0", c)
	}
	if got := readFile(t, dir, "modbus.yaml"); got != "a: 1\nb: 3\n" {
		t.Fatalf("modbus.yaml = %q, want the running bytes", got)
	}
	// Pulled again, it is in sync.
	if c := pullIO(host, port, dir, true, false, false); c != 0 {
		t.Fatalf("--check after a pull exit %d, want 0", c)
	}
}

func TestPullIODryRunWritesNothing(t *testing.T) {
	host, port := ioController(t, ioState("a: 1\nb: 2\n", "a: 1\nb: 3\n"))
	dir := projectWith(t, "modbus.yaml", "a: 1\nb: 2\n")
	out := captureStdout(t, func() int { return pullIO(host, port, dir, false, true, false) })
	if got := readFile(t, dir, "modbus.yaml"); got != "a: 1\nb: 2\n" {
		t.Fatalf("--dry-run wrote %q", got)
	}
	if !strings.Contains(out, "-b: 2") || !strings.Contains(out, "+b: 3") {
		t.Fatalf("--dry-run printed %q, want the diff", out)
	}
}

func TestPullIOCheckExitsNonZero(t *testing.T) {
	host, port := ioController(t, ioState("a: 1\n", "a: 2\n"))
	dir := projectWith(t, "modbus.yaml", "a: 1\n")
	if c := pullIO(host, port, dir, true, false, false); c != 1 {
		t.Fatalf("--check exit %d, want 1", c)
	}
	if got := readFile(t, dir, "modbus.yaml"); got != "a: 1\n" {
		t.Fatalf("--check wrote %q", got)
	}
	// --check wins over --dry-run.
	if c := pullIO(host, port, dir, true, true, false); c != 1 {
		t.Fatalf("--check --dry-run exit %d, want 1", c)
	}
}

func TestPullIORefusesALocallyEditedFile(t *testing.T) {
	host, port := ioController(t, ioState("a: 1\n", "a: 2\n"))
	dir := projectWith(t, "modbus.yaml", "a: 1\nmine: true\n")
	if c := pullIO(host, port, dir, false, false, false); c != 2 {
		t.Fatalf("exit %d, want 2: the local copy is not the booted one", c)
	}
	if got := readFile(t, dir, "modbus.yaml"); got != "a: 1\nmine: true\n" {
		t.Fatalf("a refused pull wrote %q", got)
	}
	if c := pullIO(host, port, dir, false, true, false); c != 0 {
		t.Fatalf("--dry-run exit %d, want 0 even for a refused file", c)
	}
	if c := pullIO(host, port, dir, false, false, true); c != 0 {
		t.Fatalf("--force exit %d, want 0", c)
	}
	if got := readFile(t, dir, "modbus.yaml"); got != "a: 2\n" {
		t.Fatalf("--force wrote %q", got)
	}
}

func TestPullIOWritesNothingWhenOneFileIsRefused(t *testing.T) {
	st := ioState("a: 1\n", "a: 2\n")
	st.Files = append(st.Files, server.IOFile{Path: "profiles/x.yaml", Running: "new\n", Booted: sha256hex("old\n")})
	host, port := ioController(t, st)
	dir := projectWith(t, "modbus.yaml", "a: 1\n")
	os.MkdirAll(filepath.Join(dir, "profiles"), 0o755)
	os.WriteFile(filepath.Join(dir, "profiles/x.yaml"), []byte("edited\n"), 0o644)
	if c := pullIO(host, port, dir, false, false, false); c != 2 {
		t.Fatalf("exit %d, want 2", c)
	}
	if got := readFile(t, dir, "modbus.yaml"); got != "a: 1\n" {
		t.Fatalf("modbus.yaml = %q: written although another file was refused", got)
	}
}

func TestPullIOStandbyRefused(t *testing.T) {
	st := ioState("a: 1\n", "a: 2\n")
	st.Leading = false
	host, port := ioController(t, st)
	dir := projectWith(t, "modbus.yaml", "a: 1\n")
	if c := pullIO(host, port, dir, false, false, false); c != 2 {
		t.Fatalf("exit %d, want 2 on a standby", c)
	}
	if got := readFile(t, dir, "modbus.yaml"); got != "a: 1\n" {
		t.Fatalf("a standby pull wrote %q", got)
	}
}

func TestPullIONewFileAndPathsOutsideTheProject(t *testing.T) {
	st := server.IOFiles{Leading: true, Units: []server.IODiffer{{Driver: "d"}},
		Files: []server.IOFile{{Path: "profiles/new.yaml", Running: "x\n"}}}
	host, port := ioController(t, st)
	dir := t.TempDir()
	if c := pullIO(host, port, dir, false, false, false); c != 0 {
		t.Fatalf("exit %d, want 0: a file the change added", c)
	}
	if got := readFile(t, dir, "profiles/new.yaml"); got != "x\n" {
		t.Fatalf("new file = %q", got)
	}
	for _, bad := range []string{"../escape.yaml", "/etc/passwd", "a/../../b"} {
		st.Files = []server.IOFile{{Path: bad, Running: "x"}}
		host, port := ioController(t, st)
		if c := pullIO(host, port, dir, false, false, true); c != 2 {
			t.Fatalf("%q: exit %d, want 2", bad, c)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "escape.yaml")); err == nil {
		t.Fatal("a path outside the project was written")
	}
}

func TestPullIONothingDiffers(t *testing.T) {
	host, port := ioController(t, server.IOFiles{Leading: true})
	if c := pullIO(host, port, t.TempDir(), true, false, false); c != 0 {
		t.Fatalf("exit %d, want 0 when the controller runs the manifest", c)
	}
}

func TestPullIOConflictRefused(t *testing.T) {
	st := ioState("a: 1\n", "a: 2\n")
	st.Conflicts = []string{"modbus.yaml"}
	host, port := ioController(t, st)
	dir := projectWith(t, "modbus.yaml", "a: 1\n")
	if c := pullIO(host, port, dir, false, false, true); c != 2 {
		t.Fatalf("exit %d, want 2", c)
	}
}

func TestUnifiedDiff(t *testing.T) {
	if d := unifiedDiff("a", "b", "x\n", "x\n"); d != "" {
		t.Fatalf("equal files diff = %q", d)
	}
	d := unifiedDiff("a", "b", "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n", "1\n2\n3\n4\n5\nsix\n7\n8\n9\n10\n")
	want := "--- a\n+++ b\n@@ -3,7 +3,7 @@\n 3\n 4\n 5\n-6\n+six\n 7\n 8\n 9\n"
	if d != want {
		t.Fatalf("diff =\n%s\nwant\n%s", d, want)
	}
	if d := unifiedDiff("a", "b", "", "x\n"); !strings.Contains(d, "@@ -0,0 +1,1 @@") || !strings.Contains(d, "+x") {
		t.Fatalf("new-file diff = %q", d)
	}
}
