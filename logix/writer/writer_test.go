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
		{"CTU in branch", wrap("c : CTU; X : BOOL; Y : BOOL; Z : BOOL;", "RUNG r [ X c:CTU(PV := 2) | Z ] ( Y )"), ruleTOFPosition, "c:CTU"},
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

// The rungs form is what an online edit sends: a Rung-target partial
// export the reader parses, with every rung a target and the tags as
// context only.
func TestWriteRungsIsAPartialExport(t *testing.T) {
	doc, diags, err := WriteRungs(fixture(t, "demoline.ld"), demoOpts())
	if err != nil || len(diags) > 0 {
		t.Fatalf("%v %v", err, diags)
	}
	f, err := l5x.Parse(doc)
	if err != nil {
		t.Fatalf("does not parse: %v\n%s", err, doc)
	}
	if f.TargetType != "Rung" || !f.Partial() || !f.ContainsContext {
		t.Errorf("envelope: TargetType=%q partial=%v context=%v", f.TargetType, f.Partial(), f.ContainsContext)
	}
	s := string(doc)
	for _, want := range []string{
		`TargetCount="2"`,
		`<Controller Use="Context" Name="DemoLine">`,
		`<Tags Use="Context">`,
		`<Rung Use="Target" Number="0" Type="N">`,
		`<Rung Use="Target" Number="1" Type="N">`,
		`<DataType Name="BOOL" Family="NoFamily" Class="ProductDefined"/>`,
		`<DataType Name="REAL" Family="NoFamily" Class="ProductDefined"/>`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s", want)
		}
	}
	if strings.Contains(s, "<Tasks>") || strings.Contains(s, "<Modules>") {
		t.Error("a rung export must not carry tasks or modules")
	}
	// The exporter's rung partial carries the routine as context without
	// a Type, so the reader keeps the rungs but does not render them.
	rungs := f.Controller.Programs[0].Routines[0].Rungs
	if len(rungs) != 2 || rungs[0].Text != "[XIC(StartPB) ,XIC(RunCmd) ]XIO(StopPB)OTE(RunCmd);" {
		t.Errorf("rungs = %+v", rungs)
	}
}

// A VAR_EXTERNAL tag's seed and description live in nautilus.yaml; the
// Logix controller tag carries both.
func TestManifestInitsAndDescriptionsLandOnControllerTags(t *testing.T) {
	src := "PROGRAM P\nVAR_EXTERNAL\n  SP : REAL;\n  Run : BOOL;\n  N : INT;\nEND_VAR\nLD\n  RUNG r GT(SP, 1.0) ( Run )\nEND_LD\nEND_PROGRAM\n"
	f := mustWrite(t, src, Options{
		Inits: map[string]any{"SP": 85, "N": int64(7), "Run": true},
		Descs: map[string]string{"SP": "LAH-101 setpoint", "Run": "P-101 run <cmd>"},
	})
	byName := map[string]*l5x.Tag{}
	for _, tg := range f.Controller.Tags {
		byName[tg.Name] = tg
	}
	if byName["SP"].Value != 85.0 || byName["SP"].Description != "LAH-101 setpoint" {
		t.Errorf("SP = %v %q", byName["SP"].Value, byName["SP"].Description)
	}
	if byName["N"].Value != int64(7) || byName["Run"].Value != true || byName["Run"].Description != "P-101 run <cmd>" {
		t.Errorf("N=%v Run=%v %q", byName["N"].Value, byName["Run"].Value, byName["Run"].Description)
	}
}

// The side code lives in its own program, scheduled after the user's, and
// leaves the user's routine untouched.
func TestSideCodeHeartbeat(t *testing.T) {
	src := fixture(t, "demoline.ld")
	opts := demoOpts()
	opts.Side = Side{Heartbeat: "Nautilus_Scan"}
	f := mustWrite(t, src, opts)
	if len(f.Controller.Programs) != 2 || f.Controller.Programs[1].Name != SideProgram {
		t.Fatalf("programs = %+v", f.Controller.Programs)
	}
	user := f.Controller.Programs[0].Routines[0]
	if len(user.Rungs) != 2 {
		t.Errorf("the user's routine gained rungs: %d", len(user.Rungs))
	}
	side := f.Controller.Programs[1].Routines[0]
	if len(side.Rungs) != 1 || side.Rungs[0].Text != "ADD(Nautilus_Scan,1,Nautilus_Scan);" {
		t.Errorf("side routine = %+v", side.Rungs)
	}
	var hb *l5x.Tag
	for _, tg := range f.Controller.Tags {
		if tg.Name == "Nautilus_Scan" {
			hb = tg
		}
	}
	if hb == nil || hb.DataType != "DINT" {
		t.Fatalf("heartbeat tag = %+v", hb)
	}
	doc, _, _ := Write(src, opts)
	if !strings.Contains(string(doc), `<ScheduledProgram Name="MainProgram"/>`+"\n"+`<ScheduledProgram Name="Nautilus"/>`) {
		t.Error("the side program is not scheduled after the user's")
	}
	_, problems, err := RoundTrip(src, opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range problems {
		t.Error(p)
	}
	// A clash with the user's variables is refused.
	bad := strings.Replace(src, "    StartPB    : BOOL;", "    StartPB    : BOOL;\n    Nautilus_Scan : DINT;", 1)
	if _, diags, _ := Write(bad, opts); len(diags) != 1 || diags[0].Rule != ruleName {
		t.Errorf("clash: %v", diags)
	}
}

// A counter's done bit outlives its pulse, so power after a CTU reads as
// a new rung — Done = c.DN, not Pulse AND c.DN.
func TestCounterEndsItsRung(t *testing.T) {
	src := "PROGRAM P\nVAR\n  Pulse : BOOL; Done : BOOL; c : CTU;\nEND_VAR\nLD\n  RUNG n Pulse c:CTU(PV := 3) ( Done )\nEND_LD\nEND_PROGRAM\n"
	f := mustWrite(t, src, Options{})
	rungs := f.Controller.Programs[0].Routines[0].Rungs
	if len(rungs) != 2 || rungs[0].Text != "XIC(Pulse)CTU(c,?,?);" || rungs[1].Text != "XIC(c.DN)OTE(Done);" {
		t.Errorf("rungs = %+v", rungs)
	}
}

const udtTypes = `TYPE
  LineStatus : STRUCT
    Mode  : DINT;
    Alarm : BOOL;
  END_STRUCT;
  Pump : STRUCT
    Run    : BOOL;
    Speed  : REAL;
    Status : LineStatus;
    Hist   : ARRAY [0..3] OF REAL;
  END_STRUCT;
END_TYPE
`

const udtLadder = `PROGRAM P
VAR_EXTERNAL
    Line_Fault  : BOOL;
    Line_Status : LineStatus;
    P101        : Pump;
    Nested      : BOOL;
END_VAR
LD
  RUNG alarm  Line_Fault ( Line_Status.Alarm )
  RUNG nested P101.Status.Alarm GT(P101.Hist[2], 1.0) ( Nested )
END_LD
END_PROGRAM
`

// A STRUCT declared in a library is a UDT: nested types first, members
// in order, tags as structures with the manifest's seeds, member paths
// checked, and the result readable by the L5X reader as the same type.
func TestUserDefinedTypes(t *testing.T) {
	opts := Options{Libs: []string{udtTypes}, Inits: map[string]any{"Line_Status": map[string]any{"Mode": 1, "Alarm": true}}}
	f := mustWrite(t, udtLadder, opts)
	if len(f.Controller.DataTypes) != 2 || f.Controller.DataTypes[0].Name != "LineStatus" || f.Controller.DataTypes[1].Name != "Pump" {
		t.Fatalf("data types = %+v", f.Controller.DataTypes)
	}
	pump := f.Controller.DataTypes[1]
	if len(pump.Members) != 4 || pump.Members[2].DataType != "LineStatus" || pump.Members[3].Dimension != 4 || pump.Members[3].DataType != "REAL" {
		t.Errorf("Pump members = %+v", pump.Members)
	}
	var ls, p *l5x.Tag
	for _, tg := range f.Controller.Tags {
		switch tg.Name {
		case "Line_Status":
			ls = tg
		case "P101":
			p = tg
		}
	}
	if ls == nil || ls.DataType != "LineStatus" {
		t.Fatalf("Line_Status = %+v", ls)
	}
	if m, _ := ls.Value.(map[string]any); m["Mode"] != int64(1) || m["Alarm"] != true {
		t.Errorf("Line_Status value = %v", ls.Value)
	}
	if m, _ := p.Value.(map[string]any); m["Status"] == nil || m["Hist"] == nil {
		t.Errorf("P101 value = %v", p.Value)
	}
	rungs := f.Controller.Programs[0].Routines[0].Rungs
	if rungs[0].Text != "XIC(Line_Fault)OTE(Line_Status.Alarm);" || rungs[1].Text != "XIC(P101.Status.Alarm)GT(P101.Hist[2],1.0)OTE(Nested);" {
		t.Errorf("rungs = %+v", rungs)
	}
	// The reader renders the UDTs back as the ST they came from.
	src, unresolved, err := l5x.Types(f, l5x.TypesOptions{})
	if err != nil || len(unresolved) > 0 || !strings.Contains(src, "LineStatus") || !strings.Contains(src, "Status : LineStatus") {
		t.Errorf("types back: %v %v\n%s", err, unresolved, src)
	}
	_, problems, err := RoundTrip(udtLadder, opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, pr := range problems {
		t.Error(pr)
	}
}

func TestUserDefinedTypeRejections(t *testing.T) {
	cases := []struct{ name, types, ladder, rule, mention string }{
		{"bad member path", udtTypes, strings.Replace(udtLadder, "P101.Status.Alarm", "P101.Status.Alarmed", 1), ruleMember, "no member Alarmed"},
		{"index into a scalar", udtTypes, strings.Replace(udtLadder, "P101.Hist[2]", "P101.Speed[2]", 1), ruleMember, "not an array"},
		{"TIME member", "TYPE\n  T : STRUCT\n    D : TIME;\n  END_STRUCT;\nEND_TYPE\n", "PROGRAM P\nVAR_EXTERNAL\n  X : T;\n  Y : BOOL;\nEND_VAR\nLD\n  RUNG r Y ( Y )\nEND_LD\nEND_PROGRAM\n", ruleType, "TIME"},
		{"undeclared type", "", "PROGRAM P\nVAR_EXTERNAL\n  X : Mystery;\n  Y : BOOL;\nEND_VAR\nLD\n  RUNG r Y ( Y )\nEND_LD\nEND_PROGRAM\n", ruleType, "Mystery"},
		{"block inside a type", "TYPE\n  T : STRUCT\n    Tmr : TON;\n  END_STRUCT;\nEND_TYPE\n", "PROGRAM P\nVAR_EXTERNAL\n  X : T;\n  Y : BOOL;\nEND_VAR\nLD\n  RUNG r Y ( Y )\nEND_LD\nEND_PROGRAM\n", ruleType, "TON"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, diags, err := Write(c.ladder, Options{Libs: []string{c.types}})
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, d := range diags {
				if d.Rule == c.rule && strings.Contains(d.Message, c.mention) {
					found = true
				}
			}
			if !found {
				t.Errorf("want %s mentioning %q, got %s", c.rule, c.mention, joinDiags(diags))
			}
		})
	}
}

// The §7a yardstick: batch-skid's line/Line.L5X, written as nautilus,
// comes out as the export's own rung text — with its LES caption spelled
// the way Logix actually spells it.
func TestYardstickMatchesLineL5X(t *testing.T) {
	dir := filepath.Join("testdata", "conformance", "yardstick")
	src, err := os.ReadFile(filepath.Join(dir, "Receive.ld"))
	if err != nil {
		t.Fatal(err)
	}
	types, _ := os.ReadFile(filepath.Join(dir, "lib", "types.st"))
	f := mustWrite(t, string(src), Options{Controller: "LineController", Routine: "Receive", Libs: []string{string(types)}})
	want, err := l5x.ParseFile(filepath.Join("..", "..", "examples", "batch-skid", "line", "Line.L5X"))
	if err != nil {
		t.Fatal(err)
	}
	got := f.Controller.Programs[0].Routines[0].Rungs
	ref := want.Controller.Programs[0].Routines[0].Rungs
	if len(got) != len(ref) {
		t.Fatalf("%d rungs, want %d", len(got), len(ref))
	}
	for i := range ref {
		w := strings.Replace(ref[i].Text, "LES(", "LT(", 1)
		if got[i].Text != w {
			t.Errorf("rung %d\n got %s\nwant %s", i, got[i].Text, w)
		}
	}
	if len(f.Controller.DataTypes) != 1 || f.Controller.DataTypes[0].Name != "LineStatus" {
		t.Errorf("data types = %+v", f.Controller.DataTypes)
	}
}

const stProgram = `PROGRAM Calc
VAR_EXTERNAL
    Start    : BOOL;
    Level    : REAL;
    Mode     : INT;
    Out      : REAL;
    Alarm    : BOOL;
    Done     : BOOL;
    Elapsed  : TIME;
END_VAR
VAR
    t   : TON;
    c   : CTU;
    i   : INT;
    acc : REAL;
END_VAR
Out := Level * 2.5 + 1.0;
IF Level > 80.0 AND NOT Alarm THEN
    Alarm := TRUE;
ELSIF Level < 70.0 THEN
    Alarm := FALSE;
END_IF;
CASE Mode OF
    0: Out := 0.0;
    1, 2: Out := ABS(Out);
    3..5: Out := SQRT(Out);
ELSE
    Out := EXPT(Out, 2.0);
END_CASE;
acc := 0.0;
FOR i := 1 TO 3 DO
    acc := acc + INT_TO_REAL(i);
END_FOR;
t(IN := Start, PT := T#2S, Q => Done, ET => Elapsed);
c(CU := Done, PV := 3);
IF c.Q THEN
    Alarm := TRUE;
END_IF;
END_PROGRAM
`

// An ST program comes out as a Logix ST routine: the statements as
// written, timers and counters as FBD structures driven by TONR / CTUD,
// TIME as milliseconds, booleans as 1 and 0.
func TestStructuredText(t *testing.T) {
	doc, diags, err := WriteST(stProgram, Options{Controller: "C"})
	if err != nil {
		t.Fatal(err)
	}
	if len(diags) > 0 {
		t.Fatal(joinDiags(diags))
	}
	f, err := l5x.Parse(doc)
	if err != nil {
		t.Fatal(err)
	}
	r := f.Controller.Programs[0].Routines[0]
	if r.Type != "ST" {
		t.Fatalf("routine type %s", r.Type)
	}
	for _, want := range []string{
		"Out := ((Level * 2.5) + 1.0);",
		"IF ((Level > 80.0) AND NOT (Alarm)) THEN",
		"Alarm := 1;",
		"ELSIF (Level < 70.0) THEN",
		"CASE Mode OF",
		"1, 2:",
		"3..5:",
		"Out := (Out ** 2.0);",
		"FOR i := 1 TO 3 DO",
		"acc := (acc + i);",
		"t.PRE := 2000;",
		"t.TimerEnable := Start;",
		"t.Reset := NOT (Start);",
		"TONR(t);",
		"Done := t.DN;",
		"Elapsed := t.ACC;",
		"c.PRE := 3;",
		"c.CUEnable := Done;",
		"c.Reset := 0;",
		"CTUD(c);",
		"IF c.DN THEN",
	} {
		if !strings.Contains(r.Text, want) {
			t.Errorf("missing %q in:\n%s", want, r.Text)
		}
	}
	types := map[string]string{}
	for _, tg := range append(f.Controller.Tags, f.Controller.Programs[0].Tags...) {
		types[tg.Name] = tg.DataType
	}
	if types["t"] != "FBD_TIMER" || types["c"] != "FBD_COUNTER" || types["Elapsed"] != "DINT" || types["i"] != "INT" {
		t.Errorf("tag types = %v", types)
	}
}

func TestStructuredTextRejections(t *testing.T) {
	wrap := func(body string) string {
		return "PROGRAM P\nVAR\n  X : REAL; Y : REAL; B : BOOL; S : STRING; t : TON;\nEND_VAR\n" + body + "\nEND_PROGRAM\n"
	}
	cases := []struct{ name, src, rule, mention string }{
		{"MIN", wrap("X := MIN(X, Y);"), ruleST, "MIN()"},
		{"string", wrap("S := 'abc';"), ruleType, "STRING"},
		{"return", wrap("RETURN;"), ruleST, "RETURN"},
		{"continue", wrap("WHILE B DO\n CONTINUE;\nEND_WHILE;"), ruleST, "CONTINUE"},
		{"positional pin", wrap("t(B, T#1S);"), ruleST, "by name"},
		{"unknown pin", wrap("t(IN := B, PV := 3);"), ruleFBPin, "PV"},
		{"bad member", wrap("B := t.TT;"), ruleMember, "TT"},
		{"user block", "PROGRAM P\nVAR\n  m : Motor;\nEND_VAR\nm(Run := TRUE);\nEND_PROGRAM\n", ruleFB, "Motor"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			diags, err := CheckST(c.src, "FUNCTION_BLOCK Motor\nVAR_INPUT Run : BOOL; END_VAR\nEND_FUNCTION_BLOCK\n")
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			found := false
			for _, d := range diags {
				if d.Rule == c.rule && strings.Contains(d.Message, c.mention) {
					found = true
				}
			}
			if !found {
				t.Errorf("want %s mentioning %q, got %s", c.rule, c.mention, joinDiags(diags))
			}
		})
	}
}

// The routine partial carries an ST routine as the online import's
// target, with the tags as context.
func TestWriteRoutineForST(t *testing.T) {
	doc, diags, err := WriteRoutine("calc.st", stProgram, Options{Controller: "C"})
	if err != nil || len(diags) > 0 {
		t.Fatalf("%v %v", err, diags)
	}
	f, err := l5x.Parse(doc)
	if err != nil {
		t.Fatalf("does not parse: %v\n%s", err, doc)
	}
	if f.TargetType != "Routine" || !strings.Contains(string(doc), `TargetSubType="ST"`) || !strings.Contains(string(doc), `<Routine Use="Target" Name="MainRoutine" Type="ST">`) {
		t.Errorf("envelope: %s", string(doc)[:300])
	}
	r := f.Controller.Programs[0].Routines[0]
	if r.Type != "ST" || !strings.Contains(r.Text, "TONR(t);") {
		t.Errorf("routine = %+v", r)
	}
	if !strings.Contains(string(doc), `<Tags Use="Context">`) || strings.Contains(string(doc), "<Tasks>") {
		t.Error("context tags missing or tasks present")
	}
}
