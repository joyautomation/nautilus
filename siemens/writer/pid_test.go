package writer

import (
	"crypto/sha256"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

var pidGlobals = []Global{
	{Name: "Auto", Type: "BOOL"}, {Name: "Level", Type: "REAL"}, {Name: "LevelSP", Type: "REAL"}, {Name: "LevelDtS", Type: "REAL"},
	{Name: "Valve", Type: "REAL"}, {Name: "High", Type: "BOOL"}, {Name: "Every3", Type: "BOOL"}, {Name: "Pump", Type: "REAL"},
	{Name: "Heat", Type: "REAL"}, {Name: "Held", Type: "REAL"},
}

func pidInput(t *testing.T) Input {
	t.Helper()
	src, err := os.ReadFile("testdata/pid/Main.st")
	if err != nil {
		t.Fatal(err)
	}
	lib, err := os.ReadFile("testdata/pid/lib.st")
	if err != nil {
		t.Fatal(err)
	}
	return Input{
		Programs:    []Program{{Path: "Main.st", Source: string(src), Libs: []string{string(lib)}}},
		Globals:     pidGlobals,
		Tasks:       []Task{{Name: "main", Program: "Main", Interval: 100 * time.Millisecond, DtTag: "LevelDtS"}},
		BlockPrefix: "naut_",
	}
}

// TestPIDGolden: nautilus's PID as the SCL block naut_PID (#1301 D2),
// compared with testdata/pid/golden/ (compiled on TIA V21, README).
func TestPIDGolden(t *testing.T) {
	goldenInput(t, "pid", pidInput(t))
}

// TestPIDTwinMatchesNautilus: pid.scl is a line-by-line twin of
// nautilus's PID, proven on the CPU by a trace compare. A change to either
// side must redo that proof, so this pins both files.
func TestPIDTwinMatchesNautilus(t *testing.T) {
	for _, f := range []struct{ path, want string }{
		{"../../lang/ir/builtins_pid.go", "dff8ce7aa42a9a1a4f19a24a717652bd97529b061f3cbd9ed4689288b4d84230"},
		{"pid.scl", "888d925cbaa768227a4911c34713378209137c656fc94d90a9f2a73d1f253dc2"},
	} {
		b, err := os.ReadFile(f.path)
		if err != nil {
			t.Fatal(err)
		}
		if got := fmt.Sprintf("%x", sha256.Sum256(b)); got != f.want {
			t.Errorf("%s changed (sha256 %s): siemens/writer/pid.scl is the SCL twin of lang/ir/builtins_pid.go.\n"+
				"Make the two match, regenerate testdata/pid/golden (go test ./siemens/writer/ -update), compile it on TIA,\n"+
				"and redo the bench trace compare (docs/design/siemens-ide.md, \"PID (D2, #1301)\"); then set this test's hash to %s.", f.path, got, got)
		}
	}
}

// TestPIDNeverRetained: nautilus starts a PID over after a restart
// (runtime/retainlocals.go keeps no clock block), and TIA keeps a
// multi-instance with the section that holds it. A program's VAR RETAIN
// PID is written in its plain VAR section; a PID in a project block is
// in that block's VAR, whose instance is in plain VAR; and both ways a
// PID could still land in a RETAIN section stay refused (a project
// block's instance in a program's VAR RETAIN, VAR RETAIN in a block).
func TestPIDNeverRetained(t *testing.T) {
	out, diags, err := Write(pidInput(t))
	if err != nil || len(diags) > 0 {
		t.Fatal(err, diags)
	}
	main := fileOf(t, out, "naut_Main.scl")
	wantLines(t, main, "   VAR \n      lic : \"naut_PID\";\n      pic : \"naut_PID\";\n      heater : \"naut_Loop\";\n      hold : \"naut_PID\";   // VAR RETAIN in nautilus, which starts a PID over after a restart\n   END_VAR\n",
		"   VAR RETAIN \n      starts : DINT;\n   END_VAR\n")
	wantLines(t, fileOf(t, out, "naut_Loop.scl"), "   VAR \n      ctl : \"naut_PID\";\n   END_VAR\n")
	if strings.Contains(fileOf(t, out, "naut_Loop.scl"), "RETAIN") || strings.Contains(fileOf(t, out, "naut_PID.scl"), "RETAIN") {
		t.Error("a RETAIN section in naut_Loop or naut_PID")
	}
	lib := "FUNCTION_BLOCK L\nVAR_INPUT x : REAL; END_VAR\nVAR c : PID; END_VAR\nc(PV := x);\nEND_FUNCTION_BLOCK\n"
	if _, diags := writePOU(t, "PROGRAM P\nVAR RETAIN l : L; END_VAR\nl();\nEND_PROGRAM\n", lib); len(diags) == 0 || !strings.Contains(diags[0].Message, "an instance of FUNCTION_BLOCK L in VAR RETAIN (l)") {
		t.Errorf("a block holding a PID in VAR RETAIN: %v", diags)
	}
	lib = "FUNCTION_BLOCK L\nVAR_INPUT x : REAL; END_VAR\nVAR RETAIN c : PID; END_VAR\nc(PV := x);\nEND_FUNCTION_BLOCK\n"
	if _, diags := writePOU(t, "PROGRAM P\nVAR l : L; END_VAR\nl();\nEND_PROGRAM\n", lib); len(diags) == 0 || !strings.Contains(diags[0].Message, "VAR RETAIN in FUNCTION_BLOCK L") {
		t.Errorf("VAR RETAIN PID in a block: %v", diags)
	}
}

// TestPIDSymbolsAndPrefix: the Web API reaches a PID's pins as nautilus
// names them, a nested one's too; the block takes the writer's prefix.
func TestPIDSymbolsAndPrefix(t *testing.T) {
	out, diags, err := Write(pidInput(t))
	if err != nil || len(diags) > 0 {
		t.Fatal(err, diags)
	}
	for path, want := range map[string]string{
		"Main.lic.CV":            `"naut_Main_DB".lic.CV`,
		"Main.hold.I_TERM":       `"naut_Main_DB".hold.I_TERM`,
		"Main.heater.ctl.SAT_LO": `"naut_Main_DB".heater.ctl.SAT_LO`,
		"Main.lic.DT":            `"naut_Main_DB".lic."DT"`,
	} {
		if got, ok := out.Symbol(path); !ok || got != want {
			t.Errorf("Symbol(%q) = %q, %v; want %q", path, got, ok, want)
		}
	}
	if _, ok := out.Symbol("Main.lic._integral"); ok {
		t.Error("an internal of the PID is readable")
	}
	in := pidInput(t)
	in.BlockPrefix = "zz_"
	out, diags, err = Write(in)
	if err != nil || len(diags) > 0 {
		t.Fatal(err, diags)
	}
	if !strings.HasPrefix(fileOf(t, out, "zz_PID.scl"), "FUNCTION_BLOCK \"zz_PID\"\n{ S7_Optimized_Access := 'TRUE' }\n") {
		t.Error("zz_PID header")
	}
	wantLines(t, fileOf(t, out, "zz_Main.scl"), "      lic : \"zz_PID\";\n")
}

// TestPIDPinTypes: an argument is stored into a PID pin by the pin's
// type, as nautilus stores it: INT, DINT and an integer literal into an
// LREAL pin as they are (TIA widens them; this call compiled with no
// error on V21, its one warning the LREAL CV read into the REAL tag R),
// integer arithmetic into one refused until #1305, a comparison into a
// BOOL pin as it is.
func TestPIDPinTypes(t *testing.T) {
	out, diags := writePOU(t, "PROGRAM P\nVAR c : PID; END_VAR\nc(AUTO := A > 3, PV := A, SP := D, KP := 2, RESET := B);\nR := c.CV;\nEND_PROGRAM\n")
	if len(diags) > 0 {
		t.Fatal(diags)
	}
	wantLines(t, fileOf(t, out, "naut_P.scl"), "\t#c(AUTO := \"naut_Tags\".A > 3, PV := \"naut_Tags\".A, SP := \"naut_Tags\".D, KP := 2, RESET := \"naut_Tags\".B);\n")
	if _, diags := writePOU(t, "PROGRAM P\nVAR c : PID; END_VAR\nc(KI := A + 1);\nEND_PROGRAM\n"); len(diags) == 0 || !strings.Contains(diags[0].Message, "integer arithmetic stored into a LREAL") {
		t.Errorf("integer arithmetic into KI: %v", diags)
	}
	if _, diags := writePOU(t, "PROGRAM P\nVAR c : PID; END_VAR\nc(RESET := A + 1);\nEND_PROGRAM\n"); len(diags) == 0 {
		t.Error("an INT into RESET (BOOL) was written")
	}
}

// TestPIDBuiltinWinsOverProjectBlock: nautilus resolves its own blocks
// before the project's (lang/st resolveType), so a project FUNCTION_BLOCK
// named PID (or TON) is never an instance's type: the writer writes
// nautilus's PID, and TIA's TON, as nautilus runs them.
func TestPIDBuiltinWinsOverProjectBlock(t *testing.T) {
	lib := "FUNCTION_BLOCK PID\nVAR_INPUT PV : REAL; END_VAR\nVAR_OUTPUT CV : REAL; END_VAR\nCV := PV * 2.0;\nEND_FUNCTION_BLOCK\n" +
		"FUNCTION_BLOCK TON\nVAR_INPUT IN : BOOL; END_VAR\nVAR_OUTPUT Q : BOOL; END_VAR\nQ := IN;\nEND_FUNCTION_BLOCK\n"
	out, diags := writePOU(t, "PROGRAM P\nVAR c : PID; t : TON; END_VAR\nc(PV := R);\nR := c.CV;\nt(IN := B, PT := T#1S);\nB := t.Q;\nEND_PROGRAM\n", lib)
	if len(diags) > 0 {
		t.Fatal(diags)
	}
	if !strings.Contains(fileOf(t, out, "naut_PID.scl"), "RUNTIME(#_mem)") {
		t.Error("naut_PID is not nautilus's PID")
	}
	wantLines(t, fileOf(t, out, "naut_P.scl"), "      c : \"naut_PID\";\n", "      t : TON_TIME;\n")
	for _, f := range out.Files {
		if f.Path == "naut_TON.scl" {
			t.Error("the project's TON was written")
		}
	}
}

// TestPIDRetainOnlyGetsAVarSection: a program whose only PID is in VAR
// RETAIN, with no VAR section, gets one for it (compiled on V21).
func TestPIDRetainOnlyGetsAVarSection(t *testing.T) {
	out, diags := writePOU(t, "PROGRAM P\nVAR RETAIN n : DINT; c : PID; END_VAR\nc(PV := R);\nR := c.CV;\nEND_PROGRAM\n")
	if len(diags) > 0 {
		t.Fatal(diags)
	}
	wantLines(t, fileOf(t, out, "naut_P.scl"), "   VAR RETAIN \n      n : DINT;\n   END_VAR\n   VAR \n      c : \"naut_PID\";   // VAR RETAIN in nautilus, which starts a PID over after a restart\n   END_VAR\n")
}
