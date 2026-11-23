package writer

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// tasksGlobals are the tasks fixture's tags. Offset, Big and Hold are
// start values B1 (#1299) writes that the first TIA compile of this
// fixture also checks: a negative literal, a REAL exponent, a TIME.
var tasksGlobals = []Global{
	{Name: "Start", Type: "BOOL"},
	{Name: "Stop", Type: "BOOL"},
	{Name: "Run", Type: "BOOL"},
	{Name: "Pump", Type: "Motor"},
	{Name: "Level", Type: "REAL", Init: "50.0"},
	{Name: "Done", Type: "BOOL"},
	{Name: "Alarm", Type: "BOOL"},
	{Name: "Status", Type: "WORD"},
	{Name: "Local1", Type: "DINT"},
	{Name: "Offset", Type: "DINT", Init: "-5"},
	{Name: "Big", Type: "REAL", Init: "1.0e+20"},
	{Name: "Hold", Type: "TIME", Init: "T#5s"},
}

func tasksInput(t *testing.T) Input {
	t.Helper()
	read := func(name string) string {
		b, err := os.ReadFile("testdata/tasks/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	lib := []string{read("types.st")}
	return Input{
		Programs: []Program{
			{Path: "Main.st", Source: read("Main.st"), Libs: lib},
			{Path: "Fast.st", Source: read("Fast.st"), Libs: lib},
			{Path: "Slow.st", Source: read("Slow.st"), Libs: lib},
		},
		Globals: tasksGlobals,
		Tasks: []Task{
			{Name: "main", Program: "Main", Interval: 100 * time.Millisecond},
			{Name: "fast", Program: "Fast", Interval: 10 * time.Millisecond},
			{Name: "slow", Program: "Slow", Interval: time.Second},
		},
		BlockPrefix: "naut_",
	}
}

// TestTasksGolden: three tasks, each its own cyclic interrupt OB, sharing
// tags through per-task copies (#1300).
func TestTasksGolden(t *testing.T) {
	out := goldenInput(t, "tasks", tasksInput(t))
	if len(out.OBs) != 3 {
		t.Fatalf("OBs: %+v", out.OBs)
	}
	for i, want := range []OB{
		{Name: "naut_OB_main", Number: 30, Cycle: 100 * time.Millisecond},
		{Name: "naut_OB_fast", Number: 31, Cycle: 10 * time.Millisecond},
		{Name: "naut_OB_slow", Number: 32, Cycle: time.Second},
	} {
		if out.OBs[i] != want {
			t.Errorf("OB %d = %+v, want %+v", i, out.OBs[i], want)
		}
	}
	// The committed value is the tags DB's: what the Web API reads.
	if got, ok := out.Symbol("Level"); !ok || got != `"naut_Tags".Level` {
		t.Errorf("Symbol(Level) = %q %v", got, ok)
	}
}

// TestTasksShareAsNautilusCommits pins the rules of the per-task copy,
// one by one, beside the goldens (#1300; runtime/scanview.go).
func TestTasksShareAsNautilusCommits(t *testing.T) {
	out, diags, err := Write(tasksInput(t))
	if err != nil || len(diags) > 0 {
		t.Fatal(err, diags)
	}
	f := map[string]string{}
	for _, x := range out.Files {
		f[x.Path] = string(x.Data)
	}
	has := func(file, s string) {
		t.Helper()
		if !strings.Contains(f[file], s) {
			t.Errorf("%s lacks %q:\n%s", file, s, f[file])
		}
	}
	lacks := func(file, s string) {
		t.Helper()
		if strings.Contains(f[file], s) {
			t.Errorf("%s has %q:\n%s", file, s, f[file])
		}
	}
	// A tag one task names stays in the tags DB, read and written there.
	has("naut_Main.scl", `"naut_Tags".Run := ("naut_Scan_main".tags.Start OR "naut_Tags".Run) AND NOT "naut_Scan_main".tags.Stop;`)
	lacks("naut_Scan_main.db", "\n         Run : BOOL")
	// A tag another task names is read from the task's copy, which the
	// OB takes before the scan; a tag the task only reads is never
	// written back.
	has("naut_OB_main.scl", "\t\"naut_Scan_main\".tags.Stop := \"naut_Tags\".Stop;\n")
	lacks("naut_OB_main.scl", `"naut_Tags".Stop :=`)
	lacks("naut_Scan_main.db", "assigned : Struct\n         Stop")
	// The copy is taken before the call and landed after it.
	ob := f["naut_OB_main.scl"]
	in, call, back := strings.Index(ob, ".tags.Stop :="), strings.Index(ob, `"naut_Main_DB"();`), strings.Index(ob, `"naut_Tags".Pump.Run :=`)
	if !(in >= 0 && in < call && call < back) {
		t.Errorf("OB order: copy %d, call %d, commit %d", in, call, back)
	}
	// A write lands only when the scan made it, and its flag is set
	// right where it is made (a write the scan skips, past a RETURN
	// here, does not land).
	has("naut_OB_main.scl", "\tIF \"naut_Scan_main\".assigned.Pump.Speed THEN\n\t\t\"naut_Tags\".Pump.Speed := \"naut_Scan_main\".tags.Pump.Speed;\n\t\t\"naut_Scan_main\".assigned.Pump.Speed := FALSE;\n\tEND_IF;\n")
	has("naut_Main.scl", "\t\"naut_Scan_main\".tags.Pump.Speed := 0.0;\n\t\"naut_Scan_main\".assigned.Pump.Speed := TRUE;\n")
	// A member write flags that member only: Pump.Run here, never the
	// whole Pump, so a Speed the scan did not assign is left alone.
	has("naut_Main.scl", "\"naut_Scan_main\".assigned.Pump.Run := TRUE;\n")
	// An output binding (a timer's, a latch's) is a write too.
	has("naut_Main.scl", "Q => \"naut_Scan_main\".tags.Done);\n\t\"naut_Scan_main\".assigned.Done := TRUE;\n")
	has("naut_Main.scl", "\t\"naut_Scan_main\".tags.Alarm := #latch.Q1;\n\t\"naut_Scan_main\".assigned.Alarm := TRUE;\n")
	// A bit write flags its bit; a whole write every bit; the commit
	// lands the word only when every bit is flagged, else bit by bit.
	has("naut_Slow.scl", "\"naut_Scan_slow\".assigned.Status.%X3 := TRUE;\n")
	has("naut_Slow.scl", "\"naut_Scan_slow\".assigned.Status := WORD#16#FFFF;\n")
	has("naut_Scan_slow.db", "assigned : Struct\n         Status : WORD;\n")
	has("naut_OB_slow.scl", "\tIF \"naut_Scan_slow\".assigned.Status = WORD#16#FFFF THEN\n\t\t\"naut_Tags\".Status := \"naut_Scan_slow\".tags.Status;\n\tELSE\n")
	has("naut_OB_slow.scl", "\t\tIF \"naut_Scan_slow\".assigned.Status.%X3 THEN\n\t\t\t\"naut_Tags\".Status.%X3 := \"naut_Scan_slow\".tags.Status.%X3;\n\t\tEND_IF;\n")
	has("naut_OB_slow.scl", "\t\"naut_Scan_slow\".assigned.Status := WORD#16#0;\n")
	// Implicit scope: Slow declares no VAR_EXTERNAL and still shares.
	has("naut_OB_slow.scl", "\"naut_Scan_slow\".tags.Level := \"naut_Tags\".Level;\n")
}

// TestOneTaskIsUnchanged: with one task nothing is copied, whatever the
// program names (the bench, coverage and structs goldens pin the rest).
func TestOneTaskIsUnchanged(t *testing.T) {
	in := tasksInput(t)
	in.Programs, in.Tasks = in.Programs[:1], in.Tasks[:1]
	out, diags, err := Write(in)
	if err != nil || len(diags) > 0 {
		t.Fatal(err, diags)
	}
	for _, x := range out.Files {
		if strings.Contains(string(x.Data), "Scan_") {
			t.Errorf("%s names a scan DB:\n%s", x.Path, x.Data)
		}
	}
	if len(out.OBs) != 1 {
		t.Errorf("OBs: %+v", out.OBs)
	}
}

// TestManyTasksNumberTheirOBs: OB30 to OB38, then from 123.
func TestManyTasksNumberTheirOBs(t *testing.T) {
	var in Input
	for i := range 11 {
		name := fmt.Sprintf("P%d", i)
		in.Programs = append(in.Programs, Program{Path: name + ".st", Source: "PROGRAM " + name + "\nVAR x : REAL; END_VAR\nx := 1.0;\nEND_PROGRAM\n"})
		in.Tasks = append(in.Tasks, Task{Name: fmt.Sprintf("t%d", i), Program: name, Interval: time.Second})
	}
	out, diags, err := Write(in)
	if err != nil || len(diags) > 0 {
		t.Fatal(err, diags)
	}
	var got []int
	for _, ob := range out.OBs {
		got = append(got, ob.Number)
	}
	if fmt.Sprint(got) != "[30 31 32 33 34 35 36 37 38 123 124]" {
		t.Errorf("OB numbers %v", got)
	}
}

// TestSharedTagOfARefusedTypeIsReportedOnce: the tags DB refuses a type
// the writer does not translate; the copy does not say it again.
func TestSharedTagOfARefusedTypeIsReportedOnce(t *testing.T) {
	prog := func(name string) Program {
		return Program{Path: name + ".st", Source: "PROGRAM " + name + "\nVAR_EXTERNAL Msg : STRING; END_VAR\nMsg := 'x';\nEND_PROGRAM\n"}
	}
	_, diags, err := Write(Input{
		Programs: []Program{prog("A"), prog("B")},
		Globals:  []Global{{Name: "Msg", Type: "STRING"}},
		Tasks:    []Task{{Name: "main", Program: "A", Interval: time.Second}, {Name: "b", Program: "B", Interval: time.Second}},
	})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, d := range diags {
		if strings.Contains(d.Message, "STRING") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("STRING reported %d times: %v", n, diags)
	}
}
