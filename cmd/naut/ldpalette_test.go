package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/internal/stproject"
	"github.com/joyautomation/nautilus/lang/ld"
)

// The ladder palette's FB picker, end to end against `naut check`: a
// library block from lib/ (the libdir fixture plus a lib/starter.ld written
// as rungs — a ladder library, like the lift station's motor.ld) is picked
// from the catalog `naut ld graph` sends, inserted under the author's
// instance name with the catalog's placeholder args, filled in, an output
// captured with `=>`, the instance renamed, and the captured tag declared
// the way the palette's "declare" offer does it. The `_` placeholder is a
// diagnostic until retagged; the finished program checks clean.
func TestLadderPaletteLibraryBlockChecks(t *testing.T) {
	files := libDirFiles(t)
	files["lib/starter.ld"] = `FUNCTION_BLOCK Starter
VAR_INPUT
    Mode   : INT;
    Req    : BOOL;
    Permit : BOOL;
END_VAR
VAR_OUTPUT
    Run   : BOOL;
    Fault : BOOL;
    Ready : BOOL;
END_VAR
LD
  RUNG run
    [ EQ(Mode, 2) Req | EQ(Mode, 1) ] Permit ( Run )
  RUNG ready
    Permit ( Ready )
END_LD
END_FUNCTION_BLOCK
`
	files["pumps.ld"] = `PROGRAM Pumps
VAR_EXTERNAL
    Pump2Cmd : BOOL;
    Pump2Run : BOOL;
    Pump2Fault : BOOL;
END_VAR
LD
  RUNG pump2
    Pump2Cmd ( Pump2Run )
  RUNG alarm
    Pump2Cmd ( Pump2Fault )
END_LD
END_PROGRAM
`
	files["nautilus.yaml"] = strings.Replace(files["nautilus.yaml"], "tags:\n",
		"  - program: pumps.ld\n    scan: 100ms\ntags:\n"+
			"  - { name: Pump2Cmd,   role: setpoint, init: false }\n"+
			"  - { name: Pump2Run,   role: output,   init: false }\n"+
			"  - { name: Pump2Fault, role: output,   init: false }\n"+
			"  - { name: Pump2Ready, role: output,   init: false }\n", 1)

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
	pumps := filepath.Join(dir, "pumps.ld")
	_, libs, _ := stproject.PreludeSources(pumps, nil)
	src := files["pumps.ld"]
	step := func(op ld.EditOp) {
		t.Helper()
		edits, err := ld.ApplyEdit(src, op, libs...)
		if err != nil {
			t.Fatalf("%s: %v", op.Type, err)
		}
		src = applyLdEdits(src, edits)
		files["pumps.ld"] = src
	}

	// The catalog lists the lib/ ladder block with its placeholder args.
	m, err := ld.Graph(src, libs...)
	if err != nil {
		t.Fatal(err)
	}
	var starter *ld.FBType
	for i := range m.FBTypes {
		if m.FBTypes[i].Name == "Starter" {
			starter = &m.FBTypes[i]
		}
	}
	if starter == nil || starter.Args != "Mode := _" || starter.PowerIn != "Req" {
		t.Fatalf("catalog entry for lib/starter.ld = %+v", starter)
	}

	// Insert under the author's name, catalog args as they come.
	step(ld.EditOp{Type: "insert", Rung: "pump2", Kind: "fb", Index: 1, Inst: "m1", FbType: "Starter", Args: starter.Args})
	if out, code := checkIn(t, files); code == 0 || !strings.Contains(out, "placeholder") {
		t.Fatalf("an unfilled `_` must be a check error that says so (code %d):\n%s", code, out)
	}
	// Fill the placeholder, capture Ready, reference Fault from another rung.
	step(ld.EditOp{Type: "setArgs", Rung: "pump2", Path: []int{1}, Args: "Mode := 2, Permit := TRUE, Ready => Pump2Ready"})
	step(ld.EditOp{Type: "setRef", Rung: "alarm", Path: []int{0}, Ref: "m1.Fault"})
	// Pump2Ready is a manifest tag the program doesn't declare yet: the
	// palette's offer declares it in VAR_EXTERNAL, typed from the manifest.
	step(ld.EditOp{Type: "declareVar", Name: "Pump2Ready", VarType: "BOOL", Section: "VAR_EXTERNAL"})
	// Rename the instance — declaration and the other rung's reference.
	step(ld.EditOp{Type: "renameInst", Rung: "pump2", Path: []int{1}, Name: "pump2"})
	if !strings.Contains(src, "Pump2Cmd pump2:Starter(Mode := 2, Permit := TRUE, Ready => Pump2Ready) ( Pump2Run )") ||
		!strings.Contains(src, "pump2.Fault ( Pump2Fault )") {
		t.Fatalf("after the edits:\n%s", src)
	}
	if out, code := checkIn(t, files); code != 0 {
		t.Fatalf("naut check = %d, want clean:\n%s\n--- pumps.ld\n%s", code, out, src)
	}
}

// applyLdEdits applies `naut ld edit`'s 1-based, end-exclusive edits,
// last first (they never overlap).
func applyLdEdits(src string, edits []ld.TextEdit) string {
	lines := strings.Split(src, "\n")
	for i := len(edits) - 1; i >= 0; i-- {
		e := edits[i]
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
