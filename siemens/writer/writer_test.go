package writer

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite the golden files")

var benchGlobals = []Global{
	{Name: "Heartbeat", Type: "UINT"},
	{Name: "Echo", Type: "UINT"},
	{Name: "Command", Type: "UINT"},
	{Name: "Start", Type: "BOOL"},
	{Name: "Running", Type: "BOOL"},
	{Name: "Fault", Type: "BOOL"},
	{Name: "Reset", Type: "BOOL"},
	{Name: "Done", Type: "BOOL"},
	{Name: "Level", Type: "REAL", Init: "12.5", Retain: true},
}

// TestGolden writes the bench fixture and compares every generated file
// with testdata/bench/golden/. The goldens encode what TIA's exported
// sources look like as far as public knowledge goes; they are unverified
// against a TIA compile until S2b, which corrects them from real exports.
func TestGolden(t *testing.T) {
	src, err := os.ReadFile("testdata/bench/Main.st")
	if err != nil {
		t.Fatal(err)
	}
	out, diags, err := Write(Input{
		Programs: []Program{{Path: "Main.st", Source: string(src)}},
		Globals:  benchGlobals,
		Tasks:    []Task{{Name: "main", Program: "Main", Interval: 100 * time.Millisecond}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(diags) > 0 {
		t.Fatalf("diagnostics: %v", diags)
	}
	dir := "testdata/bench/golden"
	if *update {
		os.RemoveAll(dir)
		os.MkdirAll(dir, 0o755)
		for _, f := range out.Files {
			os.WriteFile(filepath.Join(dir, f.Path), f.Data, 0o644)
		}
	}
	got := map[string]string{}
	for _, f := range out.Files {
		got[f.Path] = string(f.Data)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("no goldens (run go test ./siemens/writer/ -update): %v", err)
	}
	if len(entries) != len(out.Files) {
		t.Errorf("golden files = %d, generated = %d", len(entries), len(out.Files))
	}
	for _, e := range entries {
		want, _ := os.ReadFile(filepath.Join(dir, e.Name()))
		if got[e.Name()] != string(want) {
			t.Errorf("%s differs from its golden (go test ./siemens/writer/ -update, then review the diff):\n%s", e.Name(), got[e.Name()])
		}
	}

	if len(out.OBs) != 1 || out.OBs[0].Number != 30 || out.OBs[0].Cycle != 100*time.Millisecond {
		t.Errorf("OBs: %+v", out.OBs)
	}
	for path, want := range map[string]string{
		"Level":        `"Tags".Level`,
		"Main.count":   `"Main_DB".count`,
		"main.dwell.Q": `"Main_DB".dwell.Q`,
	} {
		if got, ok := out.Symbol(path); !ok || got != want {
			t.Errorf("Symbol(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestContinuousTaskIsOB1(t *testing.T) {
	out, diags, err := Write(Input{
		Programs: []Program{{Path: "p.st", Source: "PROGRAM P\nVAR x : INT; END_VAR\nx := x + 1;\nEND_PROGRAM\n"}},
		Tasks:    []Task{{Name: "free", Program: "P"}},
	})
	if err != nil || len(diags) > 0 {
		t.Fatal(err, diags)
	}
	if out.OBs[0].Number != 1 || out.OBs[0].Cycle != 0 {
		t.Fatalf("OBs: %+v", out.OBs)
	}
}

// TestRefusals: what the first subset does not write is a diagnostic on
// the line that wrote it, and no files at all.
func TestRefusals(t *testing.T) {
	cases := []struct {
		name, decls, body, rule string
		line                    int
	}{
		{"division by a variable", "a, b : INT;", "a := a / b;", "siemens/division", 3},
		{"division by zero constant", "a : INT;", "a := a / 0;", "siemens/division", 3},
		{"mod by a variable", "a, b : INT;", "a := a MOD b;", "siemens/division", 3},
		{"bare string", "s : STRING;", "", "siemens/type", 2},
		{"array", "a : ARRAY[1..3] OF INT;", "", "siemens/type", 2},
		{"sr", "f : SR;", "", "siemens/type", 2},
		{"overloaded conversion", "a : INT; r : REAL;", "r := TO_REAL(a);", "siemens/conversion", 3},
		{"unknown function", "a : INT;", "a := FOO(a);", "siemens/function", 3},
		{"unknown name", "a : INT;", "a := Nope;", "siemens/name", 3},
		{"positional instance args", "t : TON; b : BOOL;", "t(b, T#1s);", "siemens/call", 3},
		{"instance in temp", "", "", "siemens/var", 0},
		{"enum literal", "a : INT;", "a := Mode#Run;", "siemens/enum", 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := "PROGRAM P\nVAR " + c.decls + " END_VAR\n" + c.body + "\nEND_PROGRAM\n"
			if c.name == "instance in temp" {
				src = "PROGRAM P\nVAR_TEMP t : TON; END_VAR\nEND_PROGRAM\n"
				c.line = 2
			}
			out, diags, err := Write(Input{Programs: []Program{{Path: "p.st", Source: src}}})
			if err != nil {
				t.Fatal(err)
			}
			if len(out.Files) != 0 {
				t.Fatal("a refused program must write no files")
			}
			for _, d := range diags {
				if d.Rule == c.rule && d.Line == c.line {
					return
				}
			}
			t.Fatalf("want %s on line %d, got %v", c.rule, c.line, diags)
		})
	}
}

func TestDivisionByAConstantIsWritten(t *testing.T) {
	out, diags, err := Write(Input{Programs: []Program{{Path: "p.st", Source: "PROGRAM P\nVAR a : INT; r : REAL; END_VAR\na := a / 2;\nr := r / 2.5;\na := a MOD 3;\nEND_PROGRAM\n"}}})
	if err != nil || len(diags) > 0 {
		t.Fatal(err, diags)
	}
	fb := string(out.Files[0].Data)
	for _, want := range []string{"#a := #a / 2;", "#r := #r / 2.5;", "#a := #a MOD 3;"} {
		if !strings.Contains(fb, want) {
			t.Errorf("missing %q in\n%s", want, fb)
		}
	}
}

func TestPrecedenceKeepsMeaning(t *testing.T) {
	out, diags, err := Write(Input{Programs: []Program{{Path: "p.st", Source: "PROGRAM P\nVAR a, b, c : INT; x, y, z : BOOL; END_VAR\n" +
		"a := (a - b) - c;\na := a - (b - c);\na := (a + b) * c;\nx := (x OR y) AND z;\nx := NOT (x AND y);\nEND_PROGRAM\n"}}})
	if err != nil || len(diags) > 0 {
		t.Fatal(err, diags)
	}
	fb := string(out.Files[0].Data)
	for _, want := range []string{
		"#a := #a - #b - #c;",
		"#a := #a - (#b - #c);",
		"#a := (#a + #b) * #c;",
		"#x := (#x OR #y) AND #z;",
		"#x := NOT (#x AND #y);",
	} {
		if !strings.Contains(fb, want) {
			t.Errorf("missing %q in\n%s", want, fb)
		}
	}
}

func TestGlobalTypeRefusedInTheTagsDB(t *testing.T) {
	_, diags, err := Write(Input{Globals: []Global{{Name: "Name", Type: "STRING"}}})
	if err != nil || len(diags) != 1 || diags[0].Rule != "siemens/type" {
		t.Fatalf("%v %v", err, diags)
	}
}

// TestLadderIsWrittenThroughST: a ladder file goes through nautilus's own
// LD → FBD → ST lowering, and a refusal points at the ladder's line.
func TestLadderIsWrittenThroughST(t *testing.T) {
	src, err := os.ReadFile("../../logix/writer/testdata/demoline.ld")
	if err != nil {
		t.Fatal(err)
	}
	out, diags, err := Write(Input{Programs: []Program{{Path: "demoline.ld", Source: string(src)}}})
	if err != nil || len(diags) > 0 {
		t.Fatal(err, diags)
	}
	fb := string(out.Files[0].Data)
	for _, want := range []string{
		`FUNCTION_BLOCK "MainProgram"`,
		"#RunCmd := (#StartPB OR #RunCmd) AND NOT #StopPB;",
		"#HiLevelAlm := #LevelPct >= #HiLevelSP;",
		"HiLevelSP : REAL := 85.0;",
	} {
		if !strings.Contains(fb, want) {
			t.Errorf("missing %q in\n%s", want, fb)
		}
	}
}
