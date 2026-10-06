package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const demoProject = "../../logix/writer/testdata/project"

// The guardrails are in the flag parsing, as for download: a deploy that
// could stop a controller never runs on a flag given by accident.
func TestLogixDeployRefusals(t *testing.T) {
	cases := []struct {
		name string
		args []string
		code int
		want string
	}{
		{"download without yes", []string{"--download", "--comm-path", `AB_ETH-1\1.2.3.4\Backplane\0`, demoProject}, 2, "refusing a download without --yes"},
		{"both flags", []string{"--online", "--download", "--yes", demoProject}, 2, "alternatives"},
		{"no target", []string{t.TempDir()}, 2, "no nautilus.yaml"},
	}
	noPath := writeFiles(t, map[string]string{
		"nautilus.yaml":  "name: x\ntasks:\n  - program: MainProgram.ld\ntarget:\n  logix:\n    controller: X\n",
		"MainProgram.ld": cleanLadder,
	})
	cases = append(cases, struct {
		name string
		args []string
		code int
		want string
	}{"online without a comm path", []string{"--online", noPath}, 2, "no comm path"})
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, code := captureStderr(t, func() int { return runLogixDeploy(c.args) })
			if code != c.code || !strings.Contains(out, c.want) {
				t.Fatalf("exit %d, want %d; output:\n%s", code, c.code, out)
			}
		})
	}
}

func TestLogixDeployNeedsATargetSection(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "nautilus.yaml"), []byte("name: x\ntasks:\n  - name: main\n    program: main.ld\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "main.ld"), []byte("PROGRAM Main\nVAR A : BOOL; B : BOOL; END_VAR\nLD\n RUNG r A ( B )\nEND_LD\nEND_PROGRAM\n"), 0o644)
	out, code := captureStderr(t, func() int { return runLogixDeploy([]string{dir}) })
	if code != 2 || !strings.Contains(out, "declares no target: logix section") {
		t.Fatalf("exit %d\n%s", code, out)
	}
}

// With a target in the manifest, `naut check` runs the Logix rules without
// being asked.
func TestCheckRunsTheManifestTarget(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"nautilus.yaml": "name: x\ntasks:\n  - name: main\n    program: main.ld\ntarget:\n  logix:\n    controller: X\n",
		"main.ld":       pulseLadder,
	})
	out, code := capture(t, func() int { return runCheck([]string{dir}) })
	if code != 1 || !strings.Contains(out, "[logix/fb]") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	// Without the target the same project checks clean.
	dir = writeFiles(t, map[string]string{
		"nautilus.yaml": "name: x\ntasks:\n  - name: main\n    program: main.ld\n",
		"main.ld":       pulseLadder,
	})
	if out, code := capture(t, func() int { return runCheck([]string{dir}) }); code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
}

func captureStderr(t *testing.T, fn func() int) (string, int) {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	code := fn()
	w.Close()
	os.Stderr = old
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return sb.String(), code
}

func TestLogixModeRefusesAChangeWithoutConfirmation(t *testing.T) {
	out, code := captureStderr(t, func() int { return runLogixMode([]string{"--comm-path", `AB_ETH-1\1.2.3.4\Backplane\0`, "run"}) })
	if code != 2 || !strings.Contains(out, "without --yes") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if _, code := captureStderr(t, func() int { return runLogixMode([]string{"--comm-path", `x`, "--yes", "fast"}) }); code != 2 {
		t.Errorf("unknown mode: exit %d", code)
	}
	if _, code := captureStderr(t, func() int { return runLogixMode([]string{"run"}) }); code != 2 {
		t.Errorf("no comm path: exit %d", code)
	}
}

func TestLogixModeTakesTheProjectDirectory(t *testing.T) {
	out, code := captureStderr(t, func() int { return runLogixMode([]string{"run", demoProject}) })
	if code != 2 || !strings.Contains(out, "without --yes") || !strings.Contains(out, `AB_ETH-1\100.93.56.45\Backplane\0`) {
		t.Fatalf("exit %d\n%s", code, out)
	}
}
