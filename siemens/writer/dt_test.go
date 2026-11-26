package writer

import (
	"os"
	"strings"
	"testing"
	"time"
)

func dtInput(t *testing.T) Input {
	t.Helper()
	read := func(name string) string {
		b, err := os.ReadFile("testdata/dt/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	return Input{
		Programs: []Program{
			{Path: "Main.st", Source: read("Main.st")},
			{Path: "Fast.st", Source: read("Fast.st")},
		},
		Globals: []Global{
			{Name: "MainDtS", Type: "REAL"},
			{Name: "Hours", Type: "REAL"},
			{Name: "FastDt", Type: "LREAL"},
			{Name: "Seen", Type: "LREAL"},
			{Name: "Ramp", Type: "LREAL"},
		},
		Tasks: []Task{
			{Name: "main", Program: "Main", Interval: 100 * time.Millisecond, DtTag: "MainDtS"},
			{Name: "fast", Program: "Fast", Interval: 10 * time.Millisecond, DtTag: "FastDt"},
		},
		BlockPrefix: "naut_",
	}
}

func fileOf(t *testing.T, out Output, path string) string {
	t.Helper()
	for _, f := range out.Files {
		if f.Path == path {
			return string(f.Data)
		}
	}
	t.Fatalf("no %s in %v", path, out.Files)
	return ""
}

// TestDtGolden: each task's OB writes its dt-tag (#1310).
func TestDtGolden(t *testing.T) {
	goldenInput(t, "dt", dtInput(t))
}

// TestDtSemantics pins what the dt-tag means, as nautilus writes it
// (runtime.go): the seconds since the task's previous scan started,
// written before the scan copies its tags; the interval on the first scan.
func TestDtSemantics(t *testing.T) {
	out, diags, err := Write(dtInput(t))
	if err != nil || len(diags) > 0 {
		t.Fatal(diags, err)
	}
	ob := fileOf(t, out, "naut_OB_fast.scl")
	at := func(s string) int {
		t.Helper()
		i := strings.Index(ob, s)
		if i < 0 {
			t.Fatalf("no %q in:\n%s", s, ob)
		}
		return i
	}
	// RUNTIME runs on every scan, the first included: only its return is
	// replaced then, so MEM holds a start time by the second scan.
	call := at("\t#elapsed := RUNTIME(MEM := \"naut_Clock\".\"fast\".mem);\n")
	first := at("\tIF NOT \"naut_Clock\".\"fast\".started OR #elapsed <= 0.0 THEN\n\t\t#elapsed := 0.01;\n\t\t\"naut_Clock\".\"fast\".started := TRUE;\n\tEND_IF;\n")
	// An LREAL tag takes the LREAL as it is.
	write := at("\t\"naut_Tags\".FastDt := #elapsed;\n")
	copyIn := at("\t\"naut_Scan_fast\".tags.FastDt := \"naut_Tags\".FastDt;\n")
	if !(call < first && first < write && write < copyIn) {
		t.Errorf("order: RUNTIME %d, first scan %d, write %d, copy-in %d:\n%s", call, first, write, copyIn, ob)
	}
	if strings.Count(ob, "RUNTIME(") != 1 {
		t.Errorf("RUNTIME once per scan:\n%s", ob)
	}

	// A task with nothing shared (no copy-in) writes it all the same; a
	// REAL tag takes the LREAL rounded.
	in := dtInput(t)
	in.Programs = in.Programs[:1]
	in.Programs[0].Source = "PROGRAM Main\nVAR_EXTERNAL MainDtS : REAL; Hours : REAL; END_VAR\nHours := Hours + MainDtS / 3600.0;\nEND_PROGRAM\n"
	in.Tasks = in.Tasks[:1]
	out, diags, err = Write(in)
	if err != nil || len(diags) > 0 {
		t.Fatal(diags, err)
	}
	ob = fileOf(t, out, "naut_OB_main.scl")
	if !strings.Contains(ob, "\t\t#elapsed := 0.1;\n") || !strings.Contains(ob, "\t\"naut_Tags\".MainDtS := LREAL_TO_REAL(#elapsed);\n\t\"naut_Main_DB\"();\n") {
		t.Errorf("one task:\n%s", ob)
	}

	// The clock DB resets when the CPU goes to RUN, so every RUN start is
	// a first scan, as every runtime start is in nautilus.
	clock := fileOf(t, out, "naut_Clock.db")
	if !strings.Contains(clock, "VERSION : 0.1\nNON_RETAIN\n") || !strings.Contains(clock, "      \"main\" : Struct\n         mem : LReal;\n         started : Bool;\n") || strings.Contains(clock, ":= TRUE") {
		t.Errorf("clock DB:\n%s", clock)
	}
}

// TestDtSecondsLiteral: the first scan's interval in seconds, as SCL
// spells an LREAL.
func TestDtSecondsLiteral(t *testing.T) {
	for d, want := range map[time.Duration]string{
		time.Millisecond:         "0.001",
		10 * time.Millisecond:    "0.01",
		100 * time.Millisecond:   "0.1",
		time.Second:              "1.0",
		1500 * time.Millisecond:  "1.5",
		60 * time.Second:         "60.0",
		1234 * time.Millisecond:  "1.234",
		59999 * time.Millisecond: "59.999",
	} {
		if got := secondsLiteral(d.Seconds()); got != want {
			t.Errorf("%s: %s, want %s", d, got, want)
		}
	}
}

// TestDtRefusals: a dt-tag the writer cannot write is refused, not left
// a tag nothing writes.
func TestDtRefusals(t *testing.T) {
	in := dtInput(t)
	in.Programs[0].Source = "PROGRAM Main\nVAR_EXTERNAL Hours : REAL; FastDt : LREAL; Seen : LREAL; MainDtS : DINT; END_VAR\nSeen := FastDt;\nEND_PROGRAM\n"
	in.Globals[0].Type = "DINT"
	_, diags, _ := Write(in)
	if len(diags) != 1 || diags[0].Rule != "siemens/dt-tag" || diags[0].Message != "task main's dt-tag MainDtS: it is a DINT; nautilus writes seconds into a REAL or an LREAL" {
		t.Errorf("DINT: %v", diags)
	}
	in = dtInput(t)
	in.Globals = in.Globals[1:]
	in.Programs[0].Source = "PROGRAM Main\nVAR_EXTERNAL Hours : REAL; FastDt : LREAL; Seen : LREAL; END_VAR\nSeen := FastDt;\nEND_PROGRAM\n"
	_, diags, _ = Write(in)
	if len(diags) != 1 || diags[0].Rule != "siemens/dt-tag" || !strings.Contains(diags[0].Message, "MainDtS: the writer has no type for it") {
		t.Errorf("untyped: %v", diags)
	}
}

// TestTypeNamesQuoted: a name nautilus takes that TIA reads as a data
// type (DT, TIME, WORD...) is written quoted wherever it lands: TIA V21
// refused a tags DB with a tag named Dt unquoted (#1310).
func TestTypeNamesQuoted(t *testing.T) {
	src := "PROGRAM Main\nVAR_EXTERNAL Dt : REAL; Hours : REAL; END_VAR\nVAR Time : REAL; END_VAR\nTime := Dt;\nHours := Hours + Time;\nEND_PROGRAM\n"
	in := Input{
		Programs:    []Program{{Path: "Main.st", Source: src}},
		Globals:     []Global{{Name: "Dt", Type: "REAL"}, {Name: "Hours", Type: "REAL"}},
		Tasks:       []Task{{Name: "main", Program: "Main", Interval: 100 * time.Millisecond, DtTag: "Dt"}},
		BlockPrefix: "naut_",
	}
	out, diags, err := Write(in)
	if err != nil || len(diags) > 0 {
		t.Fatal(diags, err)
	}
	all := ""
	for _, f := range out.Files {
		all += string(f.Data)
	}
	for _, want := range []string{"\"naut_Tags\".\"Dt\" := LREAL_TO_REAL(#elapsed);", "      \"Dt\" : REAL;", "#\"Time\" := \"naut_Tags\".\"Dt\";", "      \"Time\" : REAL;"} {
		if !strings.Contains(all, want) {
			t.Errorf("no %q in:\n%s", want, all)
		}
	}
	for _, bare := range []string{".Dt ", ".Dt;", "#Time", " Dt :", " Time :"} {
		if strings.Contains(all, bare) {
			t.Errorf("unquoted %q in:\n%s", bare, all)
		}
	}
}

// TestDtTaskNamesQuoted: a task name comes from the manifest or a file
// name, not through the ST lexer, so its clock member is always quoted; a
// task without a dt-tag has none.
func TestDtTaskNamesQuoted(t *testing.T) {
	src := "PROGRAM Main\nVAR_EXTERNAL MainDtS : REAL; Hours : REAL; END_VAR\nHours := Hours + MainDtS;\nEND_PROGRAM\n"
	other := "PROGRAM Other\nVAR x : INT; END_VAR\nx := x + 1;\nEND_PROGRAM\n"
	in := Input{
		Programs: []Program{{Path: "Main.st", Source: src}, {Path: "Other.st", Source: other}},
		Globals:  []Global{{Name: "MainDtS", Type: "REAL"}, {Name: "Hours", Type: "REAL"}},
		Tasks: []Task{
			{Name: "fast-loop", Program: "Main", Interval: 20 * time.Millisecond, DtTag: "MainDtS"},
			{Name: "plain", Program: "Other", Interval: time.Second},
		},
		BlockPrefix: "naut_",
	}
	out, diags, err := Write(in)
	if err != nil || len(diags) > 0 {
		t.Fatal(diags, err)
	}
	ob := fileOf(t, out, "naut_OB_fast-loop.scl")
	if !strings.Contains(ob, "RUNTIME(MEM := \"naut_Clock\".\"fast-loop\".mem);") || !strings.Contains(ob, "\t\t#elapsed := 0.02;\n") {
		t.Errorf("OB:\n%s", ob)
	}
	clock := fileOf(t, out, "naut_Clock.db")
	if !strings.Contains(clock, "      \"fast-loop\" : Struct\n") || strings.Contains(clock, "plain") {
		t.Errorf("clock DB:\n%s", clock)
	}
	if strings.Contains(fileOf(t, out, "naut_OB_plain.scl"), "RUNTIME") {
		t.Error("a task without a dt-tag measures nothing")
	}
}

// TestEveryTypeNameQuoted: each data type name nautilus takes as an
// identifier is written quoted, as a tag and as a local.
func TestEveryTypeNameQuoted(t *testing.T) {
	for _, n := range []string{"DT", "TOD", "DATE", "TIME", "LDT", "LTOD", "LDATE", "LTIME", "DATE_AND_TIME", "TIME_OF_DAY",
		"BYTE", "WORD", "DWORD", "LWORD", "SINT", "LINT", "USINT", "UINT", "UDINT", "ULINT", "WSTRING", "CHAR", "WCHAR", "S5TIME", "DTL"} {
		src := "PROGRAM Main\nVAR_EXTERNAL " + n + " : REAL; END_VAR\nVAR Hours : REAL; END_VAR\nHours := " + n + ";\nEND_PROGRAM\n"
		out, diags, err := Write(Input{
			Programs:    []Program{{Path: "Main.st", Source: src}},
			Globals:     []Global{{Name: n, Type: "REAL"}},
			Tasks:       []Task{{Name: "main", Program: "Main", Interval: time.Second}},
			BlockPrefix: "naut_",
		})
		if err != nil || len(diags) > 0 {
			t.Errorf("%s: %v %v", n, diags, err)
			continue
		}
		if !strings.Contains(fileOf(t, out, "naut_Tags.db"), "      \""+n+"\" : REAL;") || !strings.Contains(fileOf(t, out, "naut_Main.scl"), "#Hours := \"naut_Tags\".\""+n+"\";") {
			t.Errorf("%s not quoted", n)
		}
	}
}
