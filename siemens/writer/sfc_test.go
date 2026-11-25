package writer

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// sfcCharts are the conformance charts the SFC goldens write (#1302):
// every construct nautilus's SFC has, each with its own conformance test
// of what nautilus does with it.
var sfcCharts = []string{
	"sfc-alt-priority", "sfc-assoc-forms", "sfc-final-scan", "sfc-jump-back", "sfc-qualifiers",
	"sfc-sim-div-conv", "sfc-step-maxtime", "sfc-step-t", "sfc-timed-qualifiers",
}

// sfcInput is a conformance chart as the CLI hands it to the writer: one
// main task, its tags typed by the chart's VAR_EXTERNAL (as the runtime
// types a tag with no type:).
func sfcInput(t *testing.T, chart string) Input {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("../../lang/conformance", chart, chart+".sfc"))
	if err != nil {
		t.Fatal(err)
	}
	p := Program{Path: chart + ".sfc", Source: string(src)}
	name, err := ProgramName(p)
	if err != nil {
		t.Fatal(err)
	}
	ext := ExternalTypes([]Program{p})
	var names []string
	for n := range ext {
		names = append(names, n)
	}
	sort.Strings(names)
	var globals []Global
	for _, n := range names {
		globals = append(globals, Global{Name: n, Type: ext[n]})
	}
	return Input{
		Programs:    []Program{p},
		Globals:     globals,
		Tasks:       []Task{{Name: "main", Program: name, Interval: 50 * time.Millisecond}},
		BlockPrefix: "naut_",
	}
}

// TestSFCGolden: every conformance chart is written as the ST state
// machine nautilus runs (sfc.TranspileWithLines) and compared with
// testdata/sfc/<chart>/golden/ (compiled on TIA V21, README). A chart
// that changes must be regenerated and compiled again.
func TestSFCGolden(t *testing.T) {
	for _, c := range sfcCharts {
		t.Run(c, func(t *testing.T) {
			goldenInput(t, "sfc/"+c, sfcInput(t, c))
		})
	}
}

// TestSFCNamesItsLines: a writer refusal inside a chart names the .sfc
// line it came from, not a line of the transpiled ST, and a chart that
// is written reads its PROGRAM name and its tags' types for the task.
func TestSFCNamesItsLines(t *testing.T) {
	src := "PROGRAM Seq\nVAR_EXTERNAL Go : BOOL; Acc : TIME; END_VAR\nSFC\n  INITIAL_STEP Idle:\n  END_STEP\n  STEP Run:\n    N Say;\n  END_STEP\n" +
		"  TRANSITION FROM Idle TO Run := Go;\n  END_TRANSITION\n  TRANSITION FROM Run TO Idle := NOT Go;\n  END_TRANSITION\n" +
		"  ACTION Say: Acc := Acc + T#1s; END_ACTION\nEND_SFC\nEND_PROGRAM\n"
	p := Program{Path: "seq.sfc", Source: src}
	if name, err := ProgramName(p); err != nil || name != "Seq" {
		t.Fatalf("ProgramName = %q, %v", name, err)
	}
	if got := ExternalTypes([]Program{p}); got["go"] != "BOOL" || got["acc"] != "TIME" {
		t.Errorf("ExternalTypes = %v", got)
	}
	_, diags, err := Write(Input{Programs: []Program{p}, Globals: []Global{{Name: "Go", Type: "BOOL"}, {Name: "Acc", Type: "TIME"}},
		Tasks: []Task{{Name: "main", Program: "Seq", Interval: time.Second}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range diags {
		if d.Rule == "siemens/sfc" {
			t.Errorf("SFC is still refused: %v", d)
		}
	}
	var lines []int
	for _, d := range diags {
		if d.Path == "seq.sfc" {
			lines = append(lines, d.Line)
		}
	}
	if len(lines) == 0 {
		t.Fatalf("no diagnostic on the chart: %v", diags)
	}
	for _, l := range lines {
		if l != 13 {
			t.Errorf("diagnostic on line %d, want 13 (the ACTION's TIME arithmetic): %v", l, diags)
		}
	}
}

// TestSFCChartFileBlocks: what a chart file declares beside its PROGRAM
// (a TYPE, a FUNCTION_BLOCK with a timer of its own) is in the writer's
// library like an ST file's; unnamed transitions get the transpile's
// line-numbered scratch names; and a chart runs beside an ST task that
// shares a tag with it.
func TestSFCChartFileBlocks(t *testing.T) {
	chart := "TYPE Batch : STRUCT Size : INT; END_STRUCT; END_TYPE\n" +
		"PROGRAM Seq\nVAR_EXTERNAL Go : BOOL; Runs : INT; END_VAR\nVAR b : Batch; d : Dly; END_VAR\nSFC\n" +
		"  INITIAL_STEP Idle:\n  END_STEP\n  STEP Run:\n    N Work;\n  END_STEP\n" +
		"  TRANSITION FROM Idle TO Run := Go;\n  END_TRANSITION\n  TRANSITION FROM Run TO Idle := d.Q;\n  END_TRANSITION\n" +
		"  ACTION Work: d(IN := Run.X); b.Size := b.Size + 1; Runs := b.Size; END_ACTION\nEND_SFC\n" +
		"FUNCTION_BLOCK Dly\nVAR_INPUT IN : BOOL; END_VAR\nVAR_OUTPUT Q : BOOL; END_VAR\nVAR t : TON; END_VAR\nt(IN := IN, PT := T#200ms);\nQ := t.Q;\nEND_FUNCTION_BLOCK\n" +
		"END_PROGRAM\n"
	st := "PROGRAM Other\nVAR_EXTERNAL Go : BOOL; Runs : INT; END_VAR\nGo := Runs < 5;\nEND_PROGRAM\n"
	out, diags, err := Write(Input{
		Programs:    []Program{{Path: "seq.sfc", Source: chart}, {Path: "other.st", Source: st}},
		Globals:     []Global{{Name: "Go", Type: "BOOL"}, {Name: "Runs", Type: "INT"}},
		Tasks:       []Task{{Name: "main", Program: "Seq", Interval: 50 * time.Millisecond}, {Name: "other", Program: "Other", Interval: 100 * time.Millisecond}},
		BlockPrefix: "naut_",
	})
	if err != nil || len(diags) > 0 {
		t.Fatalf("%v %v", diags, err)
	}
	files := map[string]string{}
	for _, f := range out.Files {
		files[f.Path] = string(f.Data)
	}
	for _, want := range []string{"naut_Batch.udt", "naut_Dly.scl", "naut_Seq.scl", "naut_Other.scl"} {
		if files[want] == "" {
			t.Errorf("no %s in %v", want, out.Files)
		}
	}
	seq := files["naut_Seq.scl"]
	for _, want := range []string{`b : "naut_Batch";`, `d : "naut_Dly";`, "_en_t11 : BOOL;", "_en_t13 : BOOL;", "#_en_t13 := #_S_Run_X AND #d.Q;"} {
		if !strings.Contains(seq, want) {
			t.Errorf("naut_Seq.scl lacks %q:\n%s", want, seq)
		}
	}
	if !strings.Contains(files["naut_Dly.scl"], `naut_t_last`) {
		t.Errorf("the chart file's FB has no timer latch:\n%s", files["naut_Dly.scl"])
	}
}
