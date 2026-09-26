package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/internal/stproject"
	"github.com/joyautomation/nautilus/lang/fbd"
)

// The FBD palette's function-block picker, end to end against `naut check`:
// the catalog `naut fbd graph` sends lists PID and a lib/ ladder block;
// each goes in as `inst : TYPE(pin := _, …)` (every input open — a check
// error that says "placeholder"), gets its pins wired, an output reference
// reads it, and the finished program checks clean.
func TestFBDPaletteBlocksCheck(t *testing.T) {
	files := libDirFiles(t)
	files["lib/starter.ld"] = `FUNCTION_BLOCK Starter
VAR_INPUT
    Req  : BOOL;
    Mode : INT;
END_VAR
VAR_OUTPUT
    Run : BOOL;
END_VAR
LD
  RUNG run
    Req EQ(Mode, 2) ( Run )
END_LD
END_FUNCTION_BLOCK
`
	files["level.fbd"] = `PROGRAM Level
VAR_EXTERNAL
    Lvl  : REAL;
    LvlSP : REAL;
    Auto : BOOL;
    Speed : REAL;
    Pump2Run : BOOL;
END_VAR
FBD
  hi = GT(Lvl, LvlSP)
  Pump2Run := hi
END_FBD
END_PROGRAM
`
	files["nautilus.yaml"] = strings.Replace(files["nautilus.yaml"], "tags:\n",
		"  - program: level.fbd\n    scan: 100ms\ntags:\n"+
			"  - { name: Lvl,   role: input,    init: 0.0 }\n"+
			"  - { name: LvlSP, role: setpoint, init: 50.0 }\n"+
			"  - { name: Auto,  role: setpoint, init: false }\n"+
			"  - { name: Speed, role: output,   init: 0.0 }\n"+
			"  - { name: Pump2Run, role: output, init: false }\n", 1)
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
	_, libs, _ := stproject.PreludeSources(filepath.Join(dir, "level.fbd"), nil)
	src := files["level.fbd"]
	step := func(op fbd.EditOp) {
		t.Helper()
		edits, err := fbd.ApplyEdit(src, op, libs...)
		if err != nil {
			t.Fatalf("%s: %v", op.Type, err)
		}
		src = applyFbdEdits(src, edits)
		files["level.fbd"] = src
	}

	m, err := fbd.GraphWithLibs(src, libs)
	if err != nil {
		t.Fatal(err)
	}
	args := map[string]string{}
	for _, ty := range m.FBTypes {
		args[ty.Name] = ty.Args
	}
	if args["PID"] == "" || args["Starter"] != "Req := _, Mode := _" {
		t.Fatalf("catalog args: PID %q, Starter %q", args["PID"], args["Starter"])
	}

	step(fbd.EditOp{Type: "insertStatement", Text: "lic : PID(" + args["PID"] + ")"})
	step(fbd.EditOp{Type: "insertStatement", Text: "s1 : Starter(" + args["Starter"] + ")"})
	if out, code := checkIn(t, files); code == 0 || !strings.Contains(out, "placeholder") {
		t.Fatalf("open pins must be a check error that says so (code %d):\n%s", code, out)
	}
	wire := func(tag, to, pin string) {
		t.Helper()
		x, y := 20, 20
		g, err := fbd.GraphWithLibs(src, libs)
		if err != nil {
			t.Fatal(err)
		}
		source := "g:in." + tag
		for _, n := range g.Nodes {
			if strings.HasPrefix(n.ID, "v:"+tag) && (n.ID == "v:"+tag || strings.HasPrefix(n.ID, "v:"+tag+"#")) {
				source = n.ID
			}
		}
		if strings.HasPrefix(source, "g:") {
			step(fbd.EditOp{Type: "setLayout", Node: source, X: &x, Y: &y})
		}
		step(fbd.EditOp{Type: "rewire", To: to, ToPin: pin, Source: source})
	}
	wire("Auto", "f:lic", "AUTO")
	wire("Lvl", "f:lic", "PV")
	wire("LvlSP", "f:lic", "SP")
	for _, p := range []string{"KP", "KI", "KD", "CV_MAN", "CV_MIN", "CV_MAX", "DIRECT", "DT", "DB", "RESET"} {
		step(fbd.EditOp{Type: "disconnect", To: "f:lic", ToPin: p})
	}
	step(fbd.EditOp{Type: "insertStatement", Text: "Speed := lic.CV"})
	wire("Auto", "f:s1", "Req")
	step(fbd.EditOp{Type: "disconnect", To: "f:s1", ToPin: "Mode"})
	// Rename both instances: declaration and every inst.pin reference.
	step(fbd.EditOp{Type: "rename", Node: "f:lic", NewName: "LIC101"})
	step(fbd.EditOp{Type: "rename", Node: "f:s1", NewName: "p2"})
	for _, want := range []string{"LIC101 : PID(AUTO := Auto, PV := Lvl, SP := LvlSP)", "Speed := LIC101.CV", "p2 : Starter(Req := Auto)"} {
		if !strings.Contains(src, want) {
			t.Fatalf("missing %q:\n%s", want, src)
		}
	}
	// The library block draws with its outputs, read or not.
	g, _ := fbd.GraphWithLibs(src, libs)
	for _, n := range g.Nodes {
		if n.ID == "f:p2" && strings.Join(n.Outputs, ",") != "Run" {
			t.Errorf("p2 outputs = %v", n.Outputs)
		}
	}
	if out, code := checkIn(t, files); code != 0 {
		t.Fatalf("naut check = %d, want clean:\n%s\n--- level.fbd\n%s", code, out, src)
	}
}

// applyFbdEdits applies `naut fbd edit`'s 1-based, end-exclusive edits
// bottom-up (they never overlap).
func applyFbdEdits(src string, edits []fbd.TextEdit) string {
	sorted := append([]fbd.TextEdit(nil), edits...)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && (sorted[j].Line > sorted[j-1].Line || sorted[j].Line == sorted[j-1].Line && sorted[j].Col > sorted[j-1].Col); j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	lines := strings.Split(src, "\n")
	for _, e := range sorted {
		head := lines[e.Line-1][:e.Col-1]
		tail := ""
		if e.EndLine-1 < len(lines) {
			tail = lines[e.EndLine-1][e.EndCol-1:]
		}
		merged := strings.Split(head+e.NewText+tail, "\n")
		rest := []string{}
		if e.EndLine < len(lines) {
			rest = lines[e.EndLine:]
		}
		lines = append(append(append([]string{}, lines[:e.Line-1]...), merged...), rest...)
	}
	return strings.Join(lines, "\n")
}
