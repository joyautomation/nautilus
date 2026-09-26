package fbd

import (
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/fbcatalog"
)

// The palette's "function block" picker: any block type in the catalog —
// PID included, and user FUNCTION_BLOCKs from this file or a library — goes
// in as `inst : TYPE(pin := _, …)` with every input open, then the ordinary
// gestures (wire a tag ref to a pin, output reference, rename) finish it.

const levelSrc = `PROGRAM LevelControl
VAR_EXTERNAL
    LIT101_Level : REAL;
    LevelSP      : REAL;
    LeadReq      : BOOL;
    SpeedRef     : REAL;
END_VAR
FBD
  hi = GE(LIT101_Level, LevelSP)
END_FBD
END_PROGRAM`

func catalogType(t *testing.T, ts []fbcatalog.Type, name string) fbcatalog.Type {
	t.Helper()
	for _, ty := range ts {
		if ty.Name == name {
			return ty
		}
	}
	t.Fatalf("catalog has no %s: %+v", name, ts)
	return fbcatalog.Type{}
}

// insertFB is what the picker posts: the catalog's open-pin arguments.
func insertFB(t *testing.T, src, inst, typ string, libs ...string) string {
	t.Helper()
	m, err := GraphWithLibs(src, libs)
	if err != nil {
		t.Fatal(err)
	}
	ty := catalogType(t, m.FBTypes, typ)
	edits, err := ApplyEdit(src, EditOp{Type: "insertStatement", Text: inst + " : " + typ + "(" + ty.Args + ")"}, libs...)
	if err != nil {
		t.Fatal(err)
	}
	return apply(t, src, edits)
}

func fbNode(t *testing.T, m *Model, id string) *Node {
	t.Helper()
	for _, n := range m.Nodes {
		if n.ID == id {
			return n
		}
	}
	t.Fatalf("no node %s", id)
	return nil
}

func TestCatalogInModelListsPIDWithPins(t *testing.T) {
	m, err := Graph(levelSrc)
	if err != nil {
		t.Fatal(err)
	}
	pid := catalogType(t, m.FBTypes, "PID")
	if pid.User || pid.Prefix == "" {
		t.Errorf("PID is a standard block with a prefix: %+v", pid)
	}
	var ins, outs []string
	for _, p := range pid.Pins {
		switch p.Dir {
		case "in":
			ins = append(ins, p.Name)
		case "out":
			outs = append(outs, p.Name)
		}
	}
	if strings.Join(ins[:3], ",") != "AUTO,PV,SP" || len(ins) != 13 {
		t.Errorf("PID inputs = %v", ins)
	}
	if outs[0] != "CV" || !strings.Contains(strings.Join(outs, ","), "SAT_HI") {
		t.Errorf("PID outputs = %v", outs)
	}
	if !strings.HasPrefix(pid.Args, "AUTO := _, PV := _, SP := _, KP := _") || strings.Contains(pid.Args, "CV :=") {
		t.Errorf("PID open args = %q", pid.Args)
	}
	for _, std := range []string{"TON", "TOF", "TP", "CTU", "CTD", "CTUD", "R_TRIG", "F_TRIG", "SR", "RS"} {
		catalogType(t, m.FBTypes, std)
	}
}

func TestInsertPIDAllPinsOpen(t *testing.T) {
	out := insertFB(t, levelSrc, "lic", "PID")
	if !strings.Contains(out, "  lic : PID(AUTO := _, PV := _, SP := _, KP := _, KI := _, KD := _, CV_MAN := _, CV_MIN := _, CV_MAX := _, DIRECT := _, DT := _, DB := _, RESET := _)\n") {
		t.Fatalf("insert:\n%s", out)
	}
	m, err := Graph(out)
	if err != nil {
		t.Fatalf("the inserted block must parse: %v", err)
	}
	lic := fbNode(t, m, "f:lic")
	if len(lic.Inputs) != 13 || len(lic.Outputs) != 7 {
		t.Errorf("lic pins = %v / %v", lic.Inputs, lic.Outputs)
	}
	// Every input is an open pin: an edge from the `_` chip.
	open := 0
	for _, e := range m.Edges {
		if e.To == "f:lic" && e.From == "v:_" {
			open++
		}
	}
	if open != 13 {
		t.Errorf("want 13 open pins, got %d", open)
	}
	// It transpiles; the compiler names what is left — the placeholders.
	if _, err := Transpile(out); err != nil {
		t.Fatalf("transpile: %v", err)
	}
	if _, err := Compile(out); err == nil || !strings.Contains(err.Error(), "placeholder") {
		t.Errorf("open pins must be the diagnostic, got %v", err)
	}
	// The name is the author's; a taken one is refused.
	if _, err := ApplyEdit(out, EditOp{Type: "insertStatement", Text: "lic : TON(IN := _, PT := _)"}); err == nil {
		t.Error("a second lic must be refused")
	}
}

// wireTag is the "wire a tag ref to a pin" gesture: a bare input reference
// dropped from the palette, dragged onto the pin — or, for a tag the
// diagram already shows, a drag from its chip.
func wireTag(t *testing.T, src, tag, inst, pin string) string {
	t.Helper()
	m, err := Graph(src)
	if err != nil {
		t.Fatal(err)
	}
	source := "g:in." + tag
	for _, n := range m.Nodes {
		if n.ID == "v:"+tag {
			source = n.ID
		}
	}
	if source != "v:"+tag {
		x, y := 40, 40
		src = apply(t, src, mustOp(t, src, EditOp{Type: "setLayout", Node: source, X: &x, Y: &y}))
	}
	return apply(t, src, mustOp(t, src, EditOp{Type: "rewire", To: "f:" + inst, ToPin: pin, Source: source}))
}

func TestWirePIDPinsFromTagRefs(t *testing.T) {
	out := insertFB(t, levelSrc, "lic", "PID")
	out = wireTag(t, out, "LIT101_Level", "lic", "PV")
	out = wireTag(t, out, "LeadReq", "lic", "AUTO")
	if !strings.Contains(out, "lic : PID(AUTO := LeadReq, PV := LIT101_Level, SP := _,") {
		t.Fatalf("wired:\n%s", out)
	}
	if strings.Contains(out, "g:in.") {
		t.Errorf("the ghost chips became real text; their layout pins must move:\n%s", out)
	}
	m, err := Graph(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range m.Edges {
		if e.To == "f:lic" && e.ToPin == "PV" && !strings.HasPrefix(e.From, "v:LIT101_Level") {
			t.Errorf("PV fed by %s", e.From)
		}
	}
}

func TestOutputReferenceFromFBPin(t *testing.T) {
	out := insertFB(t, levelSrc, "lic", "PID")
	// The palette's output reference with a source: one statement.
	out = apply(t, out, mustOp(t, out, EditOp{Type: "insertStatement", Text: "SpeedRef := lic.CV"}))
	m, err := Graph(out)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range m.Edges {
		if e.From == "f:lic" && e.FromPin == "CV" && e.To == "c:SpeedRef" {
			found = true
		}
	}
	if !found {
		t.Fatalf("SpeedRef must be a coil fed by lic.CV:\n%s", out)
	}
	// And the drag form: a bare output reference, then a wire from lic.CV.
	x, y := 300, 40
	g := apply(t, insertFB(t, levelSrc, "lic", "PID"), nil)
	g = apply(t, g, mustOp(t, g, EditOp{Type: "setLayout", Node: "g:out.SpeedRef", X: &x, Y: &y}))
	g = apply(t, g, mustOp(t, g, EditOp{Type: "rewire", To: "g:out.SpeedRef", Source: "f:lic", SourcePin: "CV"}))
	if !strings.Contains(g, "SpeedRef := lic.CV") {
		t.Errorf("wire onto a bare output reference:\n%s", g)
	}
}

func TestRenameFBInstanceFollowsEveryReference(t *testing.T) {
	out := insertFB(t, levelSrc, "lic", "PID")
	out = wireTag(t, out, "LIT101_Level", "lic", "PV")
	out = apply(t, out, mustOp(t, out, EditOp{Type: "insertStatement", Text: "SpeedRef := lic.CV\nsat = OR(lic.SAT_HI, lic.SAT_LO)"}))
	out = apply(t, out, mustOp(t, out, EditOp{Type: "rename", Node: "f:lic", NewName: "LIC101"}))
	if strings.Contains(out, "lic") {
		t.Fatalf("a reference to lic survived the rename:\n%s", out)
	}
	for _, want := range []string{"LIC101 : PID(", "SpeedRef := LIC101.CV", "OR(LIC101.SAT_HI, LIC101.SAT_LO)"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	if _, err := Graph(out); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyEdit(out, EditOp{Type: "rename", Node: "f:LIC101", NewName: "sat"}); err == nil {
		t.Error("renaming onto a taken name must be refused")
	}
}

// A user FUNCTION_BLOCK from a library: listed, inserted with its declared
// pins open, and drawn with every output — nothing reads one yet.
func TestInsertLibraryFBAllPinsOpen(t *testing.T) {
	lib := `FUNCTION_BLOCK Starter
VAR_INPUT Req : BOOL; Mode : INT; END_VAR
VAR_IN_OUT Hours : REAL; END_VAR
VAR_OUTPUT Run : BOOL; Fault : BOOL; END_VAR
Run := Req;
END_FUNCTION_BLOCK`
	m, err := GraphWithLibs(levelSrc, []string{lib})
	if err != nil {
		t.Fatal(err)
	}
	st := catalogType(t, m.FBTypes, "Starter")
	if !st.User || st.Prefix != "s" || st.Args != "Req := _, Mode := _, Hours := _" {
		t.Fatalf("Starter = %+v", st)
	}
	if _, ok := func() (fbcatalog.Type, bool) {
		for _, ty := range func() []fbcatalog.Type { m, _ := Graph(levelSrc); return m.FBTypes }() {
			if ty.Name == "Starter" {
				return ty, true
			}
		}
		return fbcatalog.Type{}, false
	}(); ok {
		t.Error("without its library Starter is not in scope")
	}
	out := insertFB(t, levelSrc, "s1", "Starter", lib)
	g, err := GraphWithLibs(out, []string{lib})
	if err != nil {
		t.Fatal(err)
	}
	s1 := fbNode(t, g, "f:s1")
	if strings.Join(s1.Inputs, ",") != "Req,Mode,Hours" || strings.Join(s1.Outputs, ",") != "Run,Fault" {
		t.Errorf("s1 pins = %v / %v", s1.Inputs, s1.Outputs)
	}
	// An output pin nothing reads yet is still a source.
	edits, err := ApplyEdit(out, EditOp{Type: "insertStatement", Text: "SpeedRef := s1.Run"}, lib)
	if err != nil || len(edits) != 1 {
		t.Fatalf("output reference from a library block: %v", err)
	}
}

// The lift station's level loop, rebuilt by gesture: the picker's insert
// (DIRECT typed as TRUE in the args field), nine tag refs wired, the three
// optional pins unwired, the SpeedRef output reference — and it compiles.
func TestGestureBuiltLevelPIDCompiles(t *testing.T) {
	src := strings.Replace(levelSrc, "    SpeedRef     : REAL;\n", `    SpeedRef     : REAL;
    Kp : REAL; Ki : REAL; Kd : REAL;
    MinSpeedHz : REAL; MaxSpeedHz : REAL; LevelDtS : REAL;
`, 1)
	m, _ := Graph(src)
	args := strings.Replace(catalogType(t, m.FBTypes, "PID").Args, "DIRECT := _", "DIRECT := TRUE", 1)
	out := apply(t, src, mustOp(t, src, EditOp{Type: "insertStatement", Text: "lic : PID(" + args + ")"}))
	for _, w := range [][2]string{
		{"LeadReq", "AUTO"}, {"LIT101_Level", "PV"}, {"LevelSP", "SP"},
		{"Kp", "KP"}, {"Ki", "KI"}, {"Kd", "KD"},
		{"MinSpeedHz", "CV_MIN"}, {"MaxSpeedHz", "CV_MAX"}, {"LevelDtS", "DT"},
	} {
		out = wireTag(t, out, w[0], "lic", w[1])
	}
	for _, pin := range []string{"CV_MAN", "DB", "RESET"} {
		out = apply(t, out, mustOp(t, out, EditOp{Type: "disconnect", To: "f:lic", ToPin: pin}))
	}
	out = apply(t, out, mustOp(t, out, EditOp{Type: "insertStatement", Text: "SpeedRef := lic.CV"}))
	want := "lic : PID(AUTO := LeadReq, PV := LIT101_Level, SP := LevelSP, KP := Kp, KI := Ki, KD := Kd, CV_MIN := MinSpeedHz, CV_MAX := MaxSpeedHz, DIRECT := TRUE, DT := LevelDtS)"
	if !strings.Contains(out, want) {
		t.Fatalf("gesture-built PID:\n%s", out)
	}
	if _, err := Compile(out); err != nil {
		t.Fatalf("the gesture-built loop must compile: %v\n%s", err, out)
	}
}
