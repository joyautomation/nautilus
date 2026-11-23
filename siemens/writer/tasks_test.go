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
	if db := f["naut_Scan_main.db"]; strings.Contains(db[strings.Index(db, "assigned : Struct"):], " Stop : ") {
		t.Errorf("naut_Scan_main.db flags the read-only Stop:\n%s", db)
	}
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

// TestSharedTagsOfEveryElementaryType: a shared tag is copied and
// written back whatever its type, also one with no zero value for a
// temp's reset (LWORD, ULINT, CHAR, WCHAR).
func TestSharedTagsOfEveryElementaryType(t *testing.T) {
	types := []string{"BOOL", "BYTE", "WORD", "DWORD", "LWORD", "SINT", "INT", "DINT", "LINT", "USINT", "UINT", "UDINT", "ULINT", "REAL", "LREAL", "TIME", "CHAR", "WCHAR"}
	var globals []Global
	var ext, body strings.Builder
	for i, typ := range types {
		name := fmt.Sprintf("T%d", i)
		globals = append(globals, Global{Name: name, Type: typ})
		fmt.Fprintf(&ext, "%s : %s; ", name, typ)
		fmt.Fprintf(&body, "%s := %s;\n", name, name)
	}
	prog := func(name string) Program {
		return Program{Path: name + ".st", Source: "PROGRAM " + name + "\nVAR_EXTERNAL " + ext.String() + "END_VAR\n" + body.String() + "END_PROGRAM\n"}
	}
	out, diags, err := Write(Input{
		Programs: []Program{prog("A"), prog("B")},
		Globals:  globals,
		Tasks:    []Task{{Name: "main", Program: "A", Interval: time.Second}, {Name: "b", Program: "B", Interval: time.Second}},
	})
	if err != nil || len(diags) > 0 {
		t.Fatal(err, diags)
	}
	for _, f := range out.Files {
		if f.Path != "OB_b.scl" {
			continue
		}
		for i := range types {
			want := fmt.Sprintf("\tIF \"Scan_b\".assigned.T%d THEN\n\t\t\"Tags\".T%d := \"Scan_b\".tags.T%d;\n", i, i, i)
			if !strings.Contains(string(f.Data), want) {
				t.Errorf("OB_b lacks the write-back of %s:\n%s", types[i], f.Data)
			}
		}
		return
	}
	t.Fatalf("no OB_b.scl in %v", out.Files)
}

// structTwoTasks writes two programs over a shared struct tag S (with a
// nested member and a word leaf) and a shared REAL Level, each task its
// own; it returns the files by path.
func structTwoTasks(t *testing.T, a, b string, extra ...Program) map[string]string {
	t.Helper()
	lib := []string{"TYPE\n  Inner : STRUCT\n    Flags : WORD;\n    Run : BOOL;\n  END_STRUCT;\n  Outer : STRUCT\n    In : Inner;\n    Speed : REAL;\n  END_STRUCT;\nEND_TYPE\n"}
	progs := []Program{
		{Path: "A.st", Source: a, Libs: lib},
		{Path: "B.st", Source: b, Libs: lib},
	}
	for _, p := range extra {
		p.Libs = lib
		progs = append(progs, p)
	}
	out, diags, err := Write(Input{
		Programs: progs,
		Globals:  []Global{{Name: "S", Type: "Outer"}, {Name: "O", Type: "Outer"}, {Name: "Level", Type: "REAL"}, {Name: "X", Type: "REAL"}},
		Tasks:    []Task{{Name: "main", Program: "A", Interval: time.Second}, {Name: "b", Program: "B", Interval: time.Second}},
	})
	if err != nil || len(diags) > 0 {
		t.Fatal(err, diags)
	}
	f := map[string]string{}
	for _, x := range out.Files {
		f[x.Path] = string(x.Data)
	}
	return f
}

// TestSharedStructWrites: a whole struct assigned to a shared tag flags
// every leaf, and a leaf the program also writes bits of takes its mask's
// every bit, not TRUE (Fable on #1317).
func TestSharedStructWrites(t *testing.T) {
	f := structTwoTasks(t,
		"PROGRAM A\nVAR_EXTERNAL S : Outer; O : Outer; END_VAR\nS.In.Flags.2 := TRUE;\nS := O;\nEND_PROGRAM\n",
		"PROGRAM B\nVAR_EXTERNAL S : Outer; END_VAR\nS.Speed := S.Speed + 1.0;\nEND_PROGRAM\n")
	fb := f["A.scl"]
	for _, want := range []string{
		"\t\"Scan_main\".assigned.S.In.Flags.%X2 := TRUE;\n",
		"\t\"Scan_main\".tags.S.In.Flags := \"Tags\".O.In.Flags;\n",
		"\t\"Scan_main\".assigned.S.In.Flags := WORD#16#FFFF;\n",
		"\t\"Scan_main\".assigned.S.In.Run := TRUE;\n",
		"\t\"Scan_main\".assigned.S.Speed := TRUE;\n",
	} {
		if !strings.Contains(fb, want) {
			t.Errorf("A.scl lacks %q:\n%s", want, fb)
		}
	}
	if db := f["Scan_main.db"]; !strings.Contains(db, "            In : Struct\n               Flags : WORD;\n               Run : BOOL;") {
		t.Errorf("Scan_main.db flags:\n%s", db)
	}
	ob := f["OB_main.scl"]
	for _, want := range []string{
		"\tIF \"Scan_main\".assigned.S.In.Flags = WORD#16#FFFF THEN\n\t\t\"Tags\".S.In.Flags := \"Scan_main\".tags.S.In.Flags;\n",
		"\t\tIF \"Scan_main\".assigned.S.In.Flags.%X2 THEN\n",
		"\tIF \"Scan_main\".assigned.S.In.Run THEN\n",
	} {
		if !strings.Contains(ob, want) {
			t.Errorf("OB_main.scl lacks %q:\n%s", want, ob)
		}
	}
}

// TestLocalShadowsATag: a program's local named like a manifest tag is
// the local; it neither names the tag nor makes it shared.
func TestLocalShadowsATag(t *testing.T) {
	f := structTwoTasks(t,
		"PROGRAM A\nVAR Level : REAL; END_VAR\nLevel := Level + 1.0;\nEND_PROGRAM\n",
		"PROGRAM B\nVAR_EXTERNAL Level : REAL; END_VAR\nLevel := 2.0;\nEND_PROGRAM\n")
	for path, src := range f {
		if strings.Contains(src, "Scan_") {
			t.Errorf("%s copies a tag only one task names:\n%s", path, src)
		}
	}
}

// TestAProgramNoTaskRunsSharesNothing: only the tasks' programs count
// towards a tag's being shared.
func TestAProgramNoTaskRunsSharesNothing(t *testing.T) {
	f := structTwoTasks(t,
		"PROGRAM A\nVAR_EXTERNAL X : REAL; END_VAR\nX := 1.0;\nEND_PROGRAM\n",
		"PROGRAM B\nVAR_EXTERNAL Level : REAL; END_VAR\nLevel := 2.0;\nEND_PROGRAM\n",
		Program{Path: "C.st", Source: "PROGRAM C\nVAR_EXTERNAL X : REAL; END_VAR\nX := 3.0;\nEND_PROGRAM\n"})
	for path, src := range f {
		if strings.Contains(src, "Scan_") {
			t.Errorf("%s copies X, which one task names:\n%s", path, src)
		}
	}
}

// TestScanDBNameCollides: a task's scan DB claims its block name like any
// other generated block.
func TestScanDBNameCollides(t *testing.T) {
	prog := func(name string) Program {
		return Program{Path: name + ".st", Source: "PROGRAM " + name + "\nVAR_EXTERNAL Level : REAL; END_VAR\nLevel := Level + 1.0;\nEND_PROGRAM\n"}
	}
	_, diags, err := Write(Input{
		Programs: []Program{prog("P"), prog("Scan_main")},
		Globals:  []Global{{Name: "Level", Type: "REAL"}},
		Tasks:    []Task{{Name: "main", Program: "P", Interval: time.Second}, {Name: "x", Program: "Scan_main", Interval: time.Second}},
	})
	if err != nil || !strings.Contains(fmt.Sprint(diags), `block "Scan_main" (the scan DB of task main) collides with program Scan_main`) {
		t.Fatalf("want the scan DB collision, got %v %v", diags, err)
	}
}
