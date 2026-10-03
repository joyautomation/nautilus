package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/l5x"
)

// capture runs fn with stdout redirected and returns what it printed.
func capture(t *testing.T, fn func() int) (string, int) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	code := fn()
	w.Close()
	os.Stdout = old
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

func writeFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const cleanLadder = `PROGRAM Main
VAR
    Start : BOOL;
    Stop  : BOOL;
    Run   : BOOL;
END_VAR
LD
  RUNG seal [ Start | Run ] /Stop ( Run )
END_LD
END_PROGRAM
`

const pulseLadder = `PROGRAM Main
VAR
    Start : BOOL;
    Out   : BOOL;
    tp    : TP;
END_VAR
LD
  RUNG pulse
    Start tp:TP(PT := T#1S) ( Out )
END_LD
END_PROGRAM
`

// `naut check --target logix` runs the writer's rules after the compile,
// in the same gcc-style lines, naming the rule — so an editor shows the
// construct the Logix target lacks on the line that introduced it.
func TestCheckTargetLogix(t *testing.T) {
	dir := writeFiles(t, map[string]string{"main.ld": cleanLadder})
	out, code := capture(t, func() int { return runCheck([]string{"--target", "logix", dir}) })
	if code != 0 || strings.Contains(out, "logix target") {
		t.Fatalf("clean ladder: exit %d\n%s", code, out)
	}

	dir = writeFiles(t, map[string]string{"main.ld": pulseLadder})
	out, code = capture(t, func() int { return runCheck([]string{"--target", "logix", dir}) })
	if code != 1 {
		t.Fatalf("TP: exit %d, want 1\n%s", code, out)
	}
	if !strings.Contains(out, "main.ld:8:1: logix target: tp:TP: not in the Logix v1 subset") || !strings.Contains(out, "[logix/fb]") {
		t.Errorf("TP diagnostic missing or unlocated:\n%s", out)
	}

	// Without the target the same file is fine: TP is a nautilus block.
	out, code = capture(t, func() int { return runCheck([]string{dir}) })
	if code != 0 {
		t.Fatalf("no target: exit %d\n%s", code, out)
	}
}

func TestCheckTargetLogixRefusesOtherLanguages(t *testing.T) {
	dir := writeFiles(t, map[string]string{"calc.st": "PROGRAM Calc\nVAR x : INT; END_VAR\nx := x + 1;\nEND_PROGRAM\n"})
	out, code := capture(t, func() int { return runCheck([]string{"--target", "logix", dir}) })
	if code != 1 || !strings.Contains(out, "calc.st: logix target: only ladder (.ld) programs are in the v1 subset") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if _, code := capture(t, func() int { return runCheck([]string{"--target", "siemens", dir}) }); code != 2 {
		t.Errorf("unknown target: exit %d, want 2", code)
	}
}

func TestLogixWrite(t *testing.T) {
	dir := writeFiles(t, map[string]string{"main.ld": cleanLadder, "pulse.ld": pulseLadder})
	out := filepath.Join(dir, "main.L5X")
	_, code := capture(t, func() int {
		return runLogix([]string{"write", "-o", out, "--controller", "Skid", "--period", "50", filepath.Join(dir, "main.ld")})
	})
	if code != 0 {
		t.Fatalf("write: exit %d", code)
	}
	f, err := l5x.ParseFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if f.Controller.Name != "Skid" || f.Controller.Programs[0].Name != "Main" {
		t.Errorf("controller %s program %s", f.Controller.Name, f.Controller.Programs[0].Name)
	}
	if got := f.Controller.Programs[0].Routines[0].Rungs[0].Text; got != "[XIC(Start) ,XIC(Run) ]XIO(Stop)OTE(Run);" {
		t.Errorf("rung 0 = %s", got)
	}

	bad := filepath.Join(dir, "pulse.L5X")
	printed, code := capture(t, func() int { return runLogix([]string{"write", "-o", bad, filepath.Join(dir, "pulse.ld")}) })
	if code != 1 || !strings.Contains(printed, "[logix/fb]") {
		t.Fatalf("pulse: exit %d\n%s", code, printed)
	}
	if _, err := os.Stat(bad); err == nil {
		t.Error("a refused program still produced a file")
	}
}
