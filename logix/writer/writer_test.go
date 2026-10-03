package writer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/l5x"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func demoOpts() Options {
	return Options{Controller: "DemoLine"}
}

func mustWrite(t *testing.T, src string, opts Options) *l5x.File {
	t.Helper()
	doc, diags, err := Write(src, opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range diags {
		t.Error(d)
	}
	if t.Failed() {
		t.FailNow()
	}
	f, err := l5x.Parse(doc)
	if err != nil {
		t.Fatalf("emitted L5X does not parse: %v\n%s", err, doc)
	}
	return f
}

// Phase A's definition of done: DemoLine's two rungs, written as nautilus
// ladder, come out as the SAME neutral text Studio 5000 exported for the
// same logic — byte for byte, spacing included — with the same tags.
func TestDemoLineMatchesTheRealExport(t *testing.T) {
	got := mustWrite(t, fixture(t, "demoline.ld"), demoOpts())
	want, err := l5x.ParseFile(filepath.Join("..", "..", "lang", "l5x", "testdata", "demoline.L5X"))
	if err != nil {
		t.Fatal(err)
	}
	gp, wp := got.Controller.Programs[0], want.Controller.Programs[0]
	if gp.Name != wp.Name || gp.MainRoutineName != wp.MainRoutineName {
		t.Errorf("program %s/%s, want %s/%s", gp.Name, gp.MainRoutineName, wp.Name, wp.MainRoutineName)
	}
	gr, wr := gp.Routines[0], wp.Routines[0]
	if len(gr.Rungs) != len(wr.Rungs) {
		t.Fatalf("%d rungs, want %d", len(gr.Rungs), len(wr.Rungs))
	}
	for i := range wr.Rungs {
		if gr.Rungs[i].Text != wr.Rungs[i].Text {
			t.Errorf("rung %d text\n got %s\nwant %s", i, gr.Rungs[i].Text, wr.Rungs[i].Text)
		}
		if gr.Rungs[i].Comment != wr.Rungs[i].Comment {
			t.Errorf("rung %d comment\n got %q\nwant %q", i, gr.Rungs[i].Comment, wr.Rungs[i].Comment)
		}
	}
	wantTags := map[string]*l5x.Tag{}
	for _, tg := range wp.Tags {
		wantTags[tg.Name] = tg
	}
	for _, tg := range gp.Tags {
		w := wantTags[tg.Name]
		if w == nil {
			t.Errorf("unexpected program tag %s", tg.Name)
			continue
		}
		if tg.DataType != w.DataType || tg.Radix != w.Radix || tg.Value != w.Value {
			t.Errorf("tag %s = %s/%s/%v, want %s/%s/%v", tg.Name, tg.DataType, tg.Radix, tg.Value, w.DataType, w.Radix, w.Value)
		}
		delete(wantTags, tg.Name)
	}
	for name := range wantTags {
		t.Errorf("missing program tag %s", name)
	}
	if len(got.Controller.Tags) != 0 {
		t.Errorf("controller tags = %d, want none (DemoLine's tags are program-scoped)", len(got.Controller.Tags))
	}
	if got.Controller.Name != "DemoLine" || got.Controller.ProcessorType != want.Controller.ProcessorType || got.Controller.MajorRev != want.Controller.MajorRev {
		t.Errorf("controller %s %s v%s, want %s %s v%s", got.Controller.Name, got.Controller.ProcessorType, got.Controller.MajorRev,
			want.Controller.Name, want.Controller.ProcessorType, want.Controller.MajorRev)
	}
}

// The task and scheduling must be present: an unscheduled program was a
// real defect on DemoLine once (logix-target.md §21).
func TestTaskSchedulesTheProgram(t *testing.T) {
	doc, _, err := Write(fixture(t, "demoline.ld"), Options{PeriodMs: 100})
	if err != nil {
		t.Fatal(err)
	}
	s := string(doc)
	for _, want := range []string{
		`<Task Name="MainTask" Type="PERIODIC" Rate="100"`,
		`<ScheduledProgram Name="MainProgram"/>`,
		`MainRoutineName="MainRoutine"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s", want)
		}
	}
}

// Every rewrite, read back by the reader and compared with the source.
func TestSubsetRoundTrips(t *testing.T) {
	_, problems, err := RoundTrip(fixture(t, "subset.ld"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range problems {
		t.Error(p)
	}
}

func TestDemoLineRoundTrips(t *testing.T) {
	_, problems, err := RoundTrip(fixture(t, "demoline.ld"), demoOpts())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range problems {
		t.Error(p)
	}
}

// The comparator has to be able to fail, or the round trip proves nothing:
// a hand-broken export must be caught.
func TestRoundTripCatchesABrokenRung(t *testing.T) {
	src := fixture(t, "demoline.ld")
	doc, _, err := Write(src, demoOpts())
	if err != nil {
		t.Fatal(err)
	}
	broken := strings.Replace(string(doc), "XIO(StopPB)", "XIC(StopPB)", 1)
	f, err := l5x.Parse([]byte(broken))
	if err != nil {
		t.Fatal(err)
	}
	m, _ := l5x.Ladder(f, l5x.LadderOptions{})
	if m.Rungs[0].Elements[1].Neg {
		t.Fatal("the break did not take")
	}
	// Drive the comparator directly over the broken document.
	_, problems, err := roundTripAgainst(src, demoOpts(), []byte(broken))
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) == 0 {
		t.Fatal("comparator accepted a rung with the wrong contact")
	}
}

// Rung text is exactly what the exporter writes for the same shapes
// (variety.L5X and the DemoLine export): the one place the writer's
// spelling is pinned independently of the reader.
func TestNeutralTextSpelling(t *testing.T) {
	src := `PROGRAM P
VAR
  Remote : BOOL; Auto : BOOL; Interlock : BOOL; Local : BOOL;
  Dwelling : BOOL; Ready : BOOL; Maint : BOOL; Fault : BOOL; t : TON;
END_VAR
LD
  RUNG a  [ Remote [ /Auto | Interlock ] | Local ] t:TON(PT := T#30S) ( Dwelling )
  RUNG b  t.Q ( Ready ) ( S Maint ) ( R Fault )
END_LD
END_PROGRAM`
	f := mustWrite(t, src, Options{})
	rungs := f.Controller.Programs[0].Routines[0].Rungs
	want := []string{
		"[XIC(Remote) [XIO(Auto) ,XIC(Interlock) ] ,XIC(Local) ]TON(t,?,?)XIC(t.DN)OTE(Dwelling);",
		"XIC(t.DN)[OTE(Ready) ,OTL(Maint) ,OTU(Fault) ];",
	}
	for i, w := range want {
		if rungs[i].Text != w {
			t.Errorf("rung %d\n got %s\nwant %s", i, rungs[i].Text, w)
		}
	}
}

func TestTimerPresetLandsInTheTag(t *testing.T) {
	f := mustWrite(t, fixture(t, "subset.ld"), Options{})
	var tags []*l5x.Tag
	for _, p := range f.Controller.Programs {
		tags = append(tags, p.Tags...)
	}
	pre := func(name string) int64 {
		for _, tg := range tags {
			if tg.Name == name {
				s, _ := tg.Value.(map[string]any)
				v, _ := s["PRE"].(int64)
				return v
			}
		}
		t.Fatalf("no tag %s", name)
		return 0
	}
	if pre("t1") != 10000 || pre("t3") != 500 || pre("c2") != 3 {
		t.Errorf("presets t1=%d t3=%d c2=%d", pre("t1"), pre("t3"), pre("c2"))
	}
	for _, tg := range tags {
		if tg.Name == "Dwell" {
			if tg.DataType != "DINT" || tg.Value != int64(2000) {
				t.Errorf("Dwell = %s %v, want DINT 2000", tg.DataType, tg.Value)
			}
		}
	}
}

func TestParseTime(t *testing.T) {
	cases := map[string]int64{
		"T#10S": 10000, "t#500ms": 500, "T#1h30m": 5400000, "TIME#1.5s": 1500, "T#2d": 172800000, "T#1_000ms": 1000,
	}
	for in, want := range cases {
		got, ok := parseTime(in)
		if !ok || got != want {
			t.Errorf("parseTime(%q) = %d,%v want %d", in, got, ok, want)
		}
	}
	for _, bad := range []string{"10S", "T#", "T#10x", "T#1s2"} {
		if _, ok := parseTime(bad); ok {
			t.Errorf("parseTime(%q) accepted", bad)
		}
	}
}

func TestL5KReal(t *testing.T) {
	if got := l5kReal(85); got != "8.50000000e+001" {
		t.Errorf("l5kReal(85) = %s", got)
	}
	if got := l5kReal(0); got != "0.00000000e+000" {
		t.Errorf("l5kReal(0) = %s", got)
	}
}

// Every rejection names its rule, and the rule it names is the one the
// table documents for the construct. One case per rule.
func TestRejections(t *testing.T) {
	wrap := func(vars, rungs string) string {
		return "PROGRAM P\nVAR\n" + vars + "\nEND_VAR\nLD\n" + rungs + "\nEND_LD\nEND_PROGRAM\n"
	}
	cases := []struct {
		name, src, rule, mention string
	}{
		{"function block", "FUNCTION_BLOCK Seq\nVAR_INPUT A : BOOL; END_VAR\nVAR_OUTPUT Q : BOOL; END_VAR\nLD\n RUNG r A ( Q )\nEND_LD\nEND_FUNCTION_BLOCK\n" + wrap("X : BOOL; Y : BOOL;", "RUNG r X ( Y )"), ruleFunctionBlock, "Seq"},
		{"var section", "PROGRAM P\nVAR_INPUT X : BOOL; END_VAR\nVAR Y : BOOL; END_VAR\nLD\n RUNG r X ( Y )\nEND_LD\nEND_PROGRAM", ruleVarSection, "VAR_INPUT X"},
		{"type", wrap("S : STRING; X : BOOL; Y : BOOL;", "RUNG r X ( Y )"), ruleType, "STRING"},
		{"unsigned", wrap("U : UINT; X : BOOL; Y : BOOL;", "RUNG r X ( Y )"), ruleType, "UINT"},
		{"time outside preset", wrap("T : TIME; X : BOOL; Y : BOOL;", "RUNG r X ( Y )"), ruleTime, "T:"},
		{"time as operand", wrap("T : TIME; t1 : TON; X : BOOL; Y : BOOL;", "RUNG r GT(T, 5) t1:TON(PT := T) ( Y )"), ruleTime, "T:"},
		{"array lower bound", wrap("A : ARRAY [1..4] OF INT; X : BOOL; Y : BOOL;", "RUNG r X ( Y )"), ruleArrayShape, "starts at 1"},
		{"array multi-dim", wrap("A : ARRAY [0..1, 0..1] OF INT; X : BOOL; Y : BOOL;", "RUNG r X ( Y )"), ruleArrayShape, "multi-dimensional"},
		{"bool array x32", wrap("A : ARRAY [0..7] OF BOOL; X : BOOL; Y : BOOL;", "RUNG r X ( Y )"), ruleArrayShape, "multiple of 32"},
		{"array init", wrap("A : ARRAY [0..1] OF INT := [1, 2]; X : BOOL; Y : BOOL;", "RUNG r X ( Y )"), ruleArrayInit, "initializer"},
		{"init", wrap("N : INT := 'x'; X : BOOL; Y : BOOL;", "RUNG r X ( Y )"), ruleInit, "'x'"},
		{"name length", wrap("A_very_long_tag_name_that_logix_will_not_take_at_all : BOOL; Y : BOOL;", "RUNG r A_very_long_tag_name_that_logix_will_not_take_at_all ( Y )"), ruleName, "40"},
		{"name underscores", wrap("Bad__Name : BOOL; Y : BOOL;", "RUNG r Bad__Name ( Y )"), ruleName, "consecutive"},
		{"TP block", wrap("tp : TP; X : BOOL; Y : BOOL;", "RUNG r X tp:TP(PT := T#1S) ( Y )"), ruleFB, "tp:TP"},
		{"CTUD block", wrap("c : CTUD; X : BOOL; Y : BOOL;", "RUNG r X c:CTUD(PV := 3) ( Y )"), ruleFB, "CTUD"},
		{"output capture", wrap("t1 : TON; X : BOOL; Y : BOOL; E : TIME;", "RUNG r X t1:TON(PT := T#1S, ET => E) ( Y )"), ruleFBPin, "ET => E"},
		{"IN bound", wrap("t1 : TON; X : BOOL; Y : BOOL;", "RUNG r t1:TON(IN := X, PT := T#1S) ( Y )"), ruleFBPin, "IN"},
		{"missing preset", wrap("t1 : TON; X : BOOL; Y : BOOL;", "RUNG r X t1:TON() ( Y )"), rulePreset, "PT"},
		{"preset expression", wrap("t1 : TON; X : BOOL; Y : BOOL; N : INT;", "RUNG r X t1:TON(PT := T#1S + T#2S) ( Y )"), rulePreset, "T#1S + T#2S"},
		{"preset wrong type", wrap("c : CTU; X : BOOL; Y : BOOL; F : REAL;", "RUNG r X c:CTU(PV := F) ( Y )"), rulePreset, "F"},
		{"reset expression", wrap("c : CTU; X : BOOL; Y : BOOL; A : BOOL;", "RUNG r X c:CTU(PV := 3, R := A.Q) ( Y )"), ruleReset, "A.Q"},
		{"TOF in branch", wrap("t : TOF; X : BOOL; Y : BOOL; Z : BOOL;", "RUNG r [ X t:TOF(PT := T#1S) | Z ] ( Y )"), ruleTOFPosition, "t:TOF"},
		{"fn not compare", wrap("X : BOOL; Y : BOOL; N : INT;", "RUNG r X ODD(N) ( Y )"), ruleFn, "ODD"},
		{"operand expression", wrap("X : BOOL; Y : BOOL; N : INT;", "RUNG r GT(N + 1, 5) ( Y )"), ruleOperand, "N + 1"},
		{"edge coil", wrap("X : BOOL; Y : BOOL;", "RUNG r X ( P Y )"), ruleCoilEdge, "( P Y )"},
		{"member of scalar", wrap("X : BOOL; Y : BOOL; N : INT;", "RUNG r N.Hi ( Y )"), ruleMember, "N.Hi"},
		{"member of timer", wrap("t : TON; X : BOOL; Y : BOOL;", "RUNG r t.TT ( Y )"), ruleMember, "TT"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			diags, err := Check(c.src)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if len(diags) == 0 {
				t.Fatalf("no diagnostic; want %s", c.rule)
			}
			found := false
			for _, d := range diags {
				if d.Rule == c.rule && strings.Contains(d.Message, c.mention) {
					found = true
					if d.Line == 0 {
						t.Errorf("%s has no line", d)
					}
				}
			}
			if !found {
				t.Errorf("want rule %s mentioning %q, got:\n%s", c.rule, c.mention, joinDiags(diags))
			}
			if doc, _, _ := Write(c.src, Options{}); doc != nil {
				t.Error("Write emitted a document despite the diagnostic")
			}
		})
	}
}

// Generated edge tags are a pure function of rung and reference, so
// writing the same source twice yields identical bytes — regenerating is
// never drift.
func TestWriteIsDeterministic(t *testing.T) {
	src := fixture(t, "subset.ld")
	a, _, _ := Write(src, Options{})
	b, _, _ := Write(src, Options{})
	if string(a) != string(b) {
		t.Fatal("two writes of the same source differ")
	}
	if !strings.Contains(string(a), `Name="rt_edges_Local"`) || !strings.Contains(string(a), `Name="ft_edges_Remote_Q"`) {
		t.Error("generated edge tags are not named after the rung and reference")
	}
}

func joinDiags(ds []Diag) string {
	var out []string
	for _, d := range ds {
		out = append(out, d.String())
	}
	return strings.Join(out, "\n")
}
