package fbd

import (
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/lang/st"
)

// #208: the @layout block lives at one fixed place — right after END_FBD —
// so statements added later never land after it, and a block an older
// version wrote mid-body moves there on the next layout write.
func TestLayoutBlockFixedPlace(t *testing.T) {
	x, y := 40, 60
	out := apply(t, editSrc, mustOp(t, editSrc, EditOp{Type: "setLayout", Node: "c:Run", X: &x, Y: &y}))
	endFBD := strings.Index(out, "END_FBD")
	lay := strings.Index(out, "(* @layout")
	if lay < endFBD || lay > strings.Index(out, "END_PROGRAM") {
		t.Fatalf("layout block must sit between END_FBD and END_PROGRAM:\n%s", out)
	}
	// A statement added after the pin goes into the body, above END_FBD.
	out = apply(t, out, mustOp(t, out, EditOp{Type: "insertStatement", Text: "Lamp := Run"}))
	if !(strings.Index(out, "Lamp := Run") < strings.Index(out, "END_FBD")) {
		t.Fatalf("statement must land inside the body:\n%s", out)
	}

	legacy := `PROGRAM Main
VAR_EXTERNAL A : BOOL; B : BOOL; C : BOOL; END_VAR
FBD
  B := A
  (* @layout
    c:B 10,20
  *)
  C := NOT A
END_FBD
END_PROGRAM
`
	x2, y2 := 99, 98
	out = apply(t, legacy, mustOp(t, legacy, EditOp{Type: "setLayout", Node: "c:C", X: &x2, Y: &y2}))
	want := `PROGRAM Main
VAR_EXTERNAL A : BOOL; B : BOOL; C : BOOL; END_VAR
FBD
  B := A
  C := NOT A
END_FBD
  (* @layout
    c:B 10,20
    c:C 99,98
  *)
END_PROGRAM
`
	if out != want {
		t.Fatalf("legacy mid-body block must migrate after END_FBD:\n%s", out)
	}
	m := mustGraph(t, out)
	if n := m.node(t, "c:B"); n.X == nil || *n.X != 10 {
		t.Fatalf("migrated pins must still apply: %s", mustJSON(n))
	}
	if _, err := Compile(out); err != nil {
		t.Fatal(err)
	}
}

const dosingLib = `FUNCTION_BLOCK Dosing
VAR_INPUT
    Start      : BOOL;
    FlowLpm    : REAL;  (* measured flow *)
    NoFlowTime : TIME := T#5S;
    Gain, Bias : REAL := 1.0;
END_VAR
VAR_OUTPUT
    ValveOpen : BOOL;
END_VAR
VAR_IN_OUT
    Recipe : INT;
END_VAR
ValveOpen := Start AND FlowLpm > 0.0;
END_FUNCTION_BLOCK

FUNCTION ScaleAnalog : REAL
VAR_INPUT
    Raw   : INT;
    EngLo : REAL;
    EngHi : REAL;
END_VAR
ScaleAnalog := INT_TO_REAL(Raw) / 27648.0 * (EngHi - EngLo) + EngLo;
END_FUNCTION
`

// #205: the FB picker leaves an input with a declared initial value
// unbound — it keeps that value, as an unconnected FB input does — and
// writes `_` only on inputs with no default and on every VAR_IN_OUT.
func TestPickerLeavesDefaultedInputsUnbound(t *testing.T) {
	m, err := GraphWithLibs(levelSrc, []string{dosingLib})
	if err != nil {
		t.Fatal(err)
	}
	d := catalogType(t, m.FBTypes, "Dosing")
	if d.Args != "Start := _, FlowLpm := _, Recipe := _" {
		t.Fatalf("Dosing open args = %q", d.Args)
	}
	for _, p := range d.Pins {
		if p.Name == "NoFlowTime" && p.Init != "T#5S" || p.Name == "Bias" && p.Init != "1.0" {
			t.Fatalf("pin init not carried: %+v", p)
		}
	}
	// The unbound pin still draws (from the signature), unwired.
	out := insertFB(t, levelSrc, "doseA", "Dosing", dosingLib)
	mm, err := GraphWithLibs(out, []string{dosingLib})
	if err != nil {
		t.Fatal(err)
	}
	if n := fbNode(t, mm, "f:doseA"); !strings.Contains(strings.Join(n.Inputs, ","), "NoFlowTime") {
		t.Fatalf("NoFlowTime pin must still draw: %v", n.Inputs)
	}
	// Wired, the call compiles with NoFlowTime/Gain/Bias left out.
	src := strings.Replace(levelSrc, "  hi = GE(LIT101_Level, LevelSP)",
		"  doseA : Dosing(Start := LeadReq, FlowLpm := SpeedRef, Recipe := cnt)", 1)
	src = strings.Replace(src, "END_VAR\nFBD", "END_VAR\nVAR cnt : INT; END_VAR\nFBD", 1)
	if _, err := compileWithLibs(src, dosingLib); err != nil {
		t.Fatalf("a call leaving defaulted inputs unbound must compile: %v", err)
	}
}

// #204: the model carries the project's FUNCTIONs by their declared names
// (with inputs and return type) for the palette's function field.
func TestModelListsUserFunctions(t *testing.T) {
	m, err := GraphWithLibs(levelSrc, []string{dosingLib})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Funcs) != 1 || m.Funcs[0].Name != "ScaleAnalog" || m.Funcs[0].Result != "REAL" {
		t.Fatalf("funcs = %+v", m.Funcs)
	}
	if m.Funcs[0].Detail != "(Raw, EngLo, EngHi) → REAL" {
		t.Fatalf("detail = %q", m.Funcs[0].Detail)
	}
	for _, ty := range m.FBTypes {
		if ty.Name == "ScaleAnalog" {
			t.Fatal("a FUNCTION is not a function block")
		}
	}
	blank, _ := GraphWithLibs("", []string{dosingLib})
	if len(blank.Funcs) != 1 {
		t.Fatalf("a blank file's model must list them too: %+v", blank.Funcs)
	}
}

// compileWithLibs compiles .fbd source with library ST prepended — the
// shape `naut check` composes (the libraries' prelude, then the program).
func compileWithLibs(src string, libs ...string) (*ir.Program, error) {
	stSrc, err := Transpile(src)
	if err != nil {
		return nil, err
	}
	prog, err := st.Parse(strings.Join(libs, "\n") + "\n" + stSrc)
	if err != nil {
		return nil, err
	}
	return st.Lower(prog)
}

// ── #206 EN/ENO ──────────────────────────────────────────────────────────────

const enoSrc = `PROGRAM Eno
VAR_EXTERNAL
  En : BOOL; X : REAL; Y : REAL; D : REAL; Out : REAL; Ok : BOOL; Q : DINT; DivOk : BOOL; Gate : BOOL;
END_VAR
FBD
  sp = LIMIT(EN := En, MN := 0.0, IN := X, MX := 10.0, ENO => Ok)
  Out := sp
  q = DIV(EN := TRUE, X, D)
  Y := q
  DivOk := q.ENO
  t1 : TON(EN := En, IN := TRUE, PT := T#1S)
  Gate := t1.ENO
END_FBD
END_PROGRAM`

type scanHost struct {
	vals map[string]ir.Value
	now  int64
}

func (h *scanHost) ReadGlobal(name string) (ir.Value, error) { return h.vals[name], nil }
func (h *scanHost) WriteGlobal(name string, v ir.Value) error {
	h.vals[name] = v
	return nil
}
func (h *scanHost) NowMs() int64 { return h.now }

func TestENOFunctionGatesItsCoil(t *testing.T) {
	prog, err := Compile(enoSrc)
	if err != nil {
		t.Fatalf("compile: %v\n%s", err, mustTranspile(enoSrc))
	}
	h := &scanHost{vals: map[string]ir.Value{
		"En": ir.BoolVal(true), "X": ir.RealVal(42), "D": ir.RealVal(2), "Out": ir.RealVal(-1),
	}, now: 1000}
	f := ir.NewFrame(prog)
	step := func() {
		t.Helper()
		if err := ir.Run(prog, f, h); err != nil {
			t.Fatal(err)
		}
	}
	step()
	if h.vals["Out"].F != 10 || !h.vals["Ok"].B || !h.vals["Gate"].B {
		t.Fatalf("EN TRUE: Out=%v Ok=%v Gate=%v", h.vals["Out"].F, h.vals["Ok"].B, h.vals["Gate"].B)
	}
	if h.vals["Y"].F != 21 || !h.vals["DivOk"].B {
		t.Fatalf("DIV: Y=%v DivOk=%v", h.vals["Y"].F, h.vals["DivOk"].B)
	}
	// EN FALSE: LIMIT's result is not assigned (Out holds), ENO FALSE.
	h.vals["En"], h.vals["X"] = ir.BoolVal(false), ir.RealVal(3)
	step()
	if h.vals["Out"].F != 10 || h.vals["Ok"].B || h.vals["Gate"].B {
		t.Fatalf("EN FALSE: Out=%v (want held 10) Ok=%v Gate=%v", h.vals["Out"].F, h.vals["Ok"].B, h.vals["Gate"].B)
	}
	// ÷0: ENO is FALSE (the error); the result follows the ÷0 rule (0).
	h.vals["D"] = ir.RealVal(0)
	step()
	if h.vals["DivOk"].B {
		t.Fatalf("DIV by zero: ENO must be FALSE")
	}
}

func TestENOModelAndTranspile(t *testing.T) {
	stSrc := mustTranspile(enoSrc)
	for _, want := range []string{
		"IF En THEN Ok := TRUE; Out := LIMIT(0.0, X, 10.0); ELSE Ok := FALSE; END_IF;",
		"q__ENO := (D <> 0); Y := (X / D);",
		"t1(EN := En, IN := TRUE, PT := T#1S, ENO => t1__ENO);",
		"Gate := t1__ENO;", "t1__ENO : BOOL;", "q__ENO : BOOL;",
	} {
		if !strings.Contains(stSrc, want) {
			t.Errorf("transpiled ST lacks %q:\n%s", want, stSrc)
		}
	}
	m := mustGraph(t, enoSrc)
	sp := m.node(t, "b:w.sp")
	if strings.Join(sp.Inputs, ",") != "EN,MN,IN,MX" || strings.Join(sp.Outputs, ",") != "OUT,ENO" {
		t.Fatalf("LIMIT pins: %v → %v", sp.Inputs, sp.Outputs)
	}
	if q := m.node(t, "b:w.q"); strings.Join(q.Outputs, ",") != "OUT,ENO" {
		t.Fatalf("DIV outputs: %v", q.Outputs)
	}
	if t1 := m.node(t, "f:t1"); t1.Inputs[0] != "EN" || !strings.Contains(strings.Join(t1.Outputs, ","), "ENO") {
		t.Fatalf("TON pins: %v → %v", t1.Inputs, t1.Outputs)
	}
	found := false
	for _, e := range m.Edges {
		if e.From == "b:w.q" && e.FromPin == "ENO" && e.To == "c:DivOk" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no wire from q.ENO to DivOk: %s", mustJSON(m.Edges))
	}
}

func TestENOErrors(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{"  Out := ADD(LIMIT(EN := En, MN := 0.0, IN := X, MX := 1.0), 1.0)", "must drive a variable"},
		{"  w = LIMIT(EN := En, 0.0, IN := X, MX := 1.0)\n  Out := w", "by name or none"},
		{"  w = LIMIT(0.0, X, 1.0)\n  Ok := w.ENO", "drives no variable"},
		{"  w = LIMIT(0.0, X, 1.0, Q => Ok)", "=> binds ENO only"},
	} {
		src := strings.Replace(enoSrc, enoSrc[strings.Index(enoSrc, "FBD\n")+4:strings.Index(enoSrc, "END_FBD")], tc.body+"\n", 1)
		_, err := Transpile(src)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", tc.body, err, tc.want)
		}
	}
}

func TestENOEdits(t *testing.T) {
	src := `PROGRAM P
VAR_EXTERNAL En : BOOL; X : REAL; Out : REAL; Ok : BOOL; END_VAR
FBD
  sp = LIMIT(0.0, X, 10.0)
  Out := sp
  t1 : TON(IN := En, PT := T#1S)
  Ok := t1.Q
END_FBD
END_PROGRAM`
	// Drop a wire on a block's (shown, unbound) EN: EN := goes in first.
	out := apply(t, src, mustOp(t, src, EditOp{Type: "rewire", To: "b:w.sp", ToPin: "EN", Source: "v:En"}))
	if !strings.Contains(out, "sp = LIMIT(EN := En, 0.0, X, 10.0)") {
		t.Fatalf("EN on a block:\n%s", out)
	}
	// …and on an FB.
	out = apply(t, out, mustOp(t, out, EditOp{Type: "rewire", To: "f:t1", ToPin: "EN", Source: "v:En"}))
	if !strings.Contains(out, "t1 : TON(IN := En, PT := T#1S, EN := En)") {
		t.Fatalf("EN on an FB:\n%s", out)
	}
	m := mustGraph(t, out)
	if m.node(t, "f:t1").Inputs[0] != "EN" {
		t.Fatalf("EN draws first: %v", m.node(t, "f:t1").Inputs)
	}
	// Drag from a block's ENO onto a coil: the coil reads sp.ENO.
	out = apply(t, out, mustOp(t, out, EditOp{Type: "rewire", To: "c:Ok", Source: "b:w.sp", SourcePin: "ENO"}))
	if !strings.Contains(out, "Ok := sp.ENO") {
		t.Fatalf("ENO read:\n%s", out)
	}
	if _, err := Compile(out); err != nil {
		t.Fatalf("compile: %v\n%s", err, mustTranspile(out))
	}
	// Disconnecting EN unbinds it (the block runs every scan again).
	out = apply(t, out, mustOp(t, out, EditOp{Type: "disconnect", To: "b:w.sp", ToPin: "EN"}))
	if !strings.Contains(out, "sp = LIMIT(0.0, X, 10.0)") {
		t.Fatalf("EN disconnect:\n%s", out)
	}
	// A formal call stays formal when an input is added.
	f := strings.Replace(src, "sp = LIMIT(0.0, X, 10.0)", "sp = MAX(IN1 := X, IN2 := 1.0)", 1)
	out = apply(t, f, mustOp(t, f, EditOp{Type: "addInput", Node: "b:w.sp", Source: "v:X"}))
	if !strings.Contains(out, "sp = MAX(IN1 := X, IN2 := 1.0, IN3 := X)") {
		t.Fatalf("formal addInput:\n%s", out)
	}
}

// ── #207 networks ────────────────────────────────────────────────────────────

const netSrc = `PROGRAM Nets
VAR_EXTERNAL A : BOOL; B : BOOL; C : BOOL; Cnt : DINT; END_VAR
FBD
  B := A
  NETWORK 'Count A'
  // counts rising edges of A
  Cnt := c1.CV
  c1 : CTU(CU := A, R := FALSE, PV := 10)
  NETWORK
  C := c1.Q
END_FBD
END_PROGRAM`

func TestNetworksParseModelAndOrder(t *testing.T) {
	m := mustGraph(t, netSrc)
	if len(m.Networks) != 3 || !m.Networks[0].Implicit || m.Networks[1].Title != "Count A" ||
		m.Networks[1].Line != 5 || m.Networks[2].Title != "" {
		t.Fatalf("networks: %s", mustJSON(m.Networks))
	}
	for id, net := range map[string]int{"c:B": 1, "c:Cnt": 2, "f:c1": 2, "c:C": 3, "cm:0": 2} {
		if n := m.node(t, id); n.Net != net {
			t.Errorf("%s in network %d, want %d", id, n.Net, net)
		}
	}
	// Within network 2 the call runs before the coil reading it.
	if m.node(t, "f:c1").Exec != 1 || m.node(t, "c:Cnt").Exec != 2 || m.node(t, "c:C").Exec != 1 {
		t.Fatalf("exec order: c1=%d Cnt=%d C=%d", m.node(t, "f:c1").Exec, m.node(t, "c:Cnt").Exec, m.node(t, "c:C").Exec)
	}
	// Network 3 reads c1.Q across the boundary: a box, not a wire.
	m.node(t, "v:c1.Q")
	stSrc := mustTranspile(netSrc)
	b, call, cnt, c := strings.Index(stSrc, "B := A"), strings.Index(stSrc, "c1(CU"), strings.Index(stSrc, "Cnt := c1.CV"), strings.Index(stSrc, "C := c1.Q")
	if !(b < call && call < cnt && cnt < c) {
		t.Fatalf("execution must follow network order:\n%s", stSrc)
	}
	if _, err := Compile(netSrc); err != nil {
		t.Fatal(err)
	}
}

func TestNetworksRunInOrder(t *testing.T) {
	// A read of a LATER network's FB output is last scan's value: network
	// order wins over the dependency sort, which only works inside one.
	src := `PROGRAM Nets
VAR_EXTERNAL A : BOOL; Early : BOOL; END_VAR
FBD
  NETWORK 'reader'
  Early := r.Q
  NETWORK 'edge'
  r : R_TRIG(CLK := A)
END_FBD
END_PROGRAM`
	prog, err := Compile(src)
	if err != nil {
		t.Fatal(err)
	}
	h := &scanHost{vals: map[string]ir.Value{"A": ir.BoolVal(true)}}
	f := ir.NewFrame(prog)
	_ = ir.Run(prog, f, h)
	if h.vals["Early"].B {
		t.Fatal("network 1 ran before network 2: it must see last scan's (FALSE) R_TRIG.Q")
	}
	_ = ir.Run(prog, f, h)
	if !h.vals["Early"].B {
		t.Fatal("second scan: network 1 sees the edge network 2 produced last scan")
	}
	// Without NETWORK lines the same statements are one network: the call
	// is sorted before its reader — the program it always was.
	one := strings.NewReplacer("  NETWORK 'reader'\n", "", "  NETWORK 'edge'\n", "").Replace(src)
	stSrc := mustTranspile(one)
	if strings.Index(stSrc, "r(CLK") > strings.Index(stSrc, "Early := r.Q") {
		t.Fatalf("one network: dependency order:\n%s", stSrc)
	}
	if m := mustGraph(t, one); len(m.Networks) != 0 || m.node(t, "c:Early").Net != 0 {
		t.Fatal("a body without NETWORK lines reports no networks")
	}
}

func TestNetworkParseEdges(t *testing.T) {
	// A tag named Network is still a tag.
	src := `PROGRAM P
VAR_EXTERNAL Network : BOOL; A : BOOL; END_VAR
FBD
  Network := A
END_FBD
END_PROGRAM`
	if m := mustGraph(t, src); len(m.Networks) != 0 {
		t.Fatal("Network := A is a coil")
	}
	bad := strings.Replace(src, "  Network := A", "  NETWORK 'x' Network := A", 1)
	if _, err := Graph(bad); err == nil || !strings.Contains(err.Error(), "only the word NETWORK") {
		t.Fatalf("a statement on the NETWORK line: %v", err)
	}
}

func TestNetworkOps(t *testing.T) {
	out := apply(t, netSrc, mustOp(t, netSrc, EditOp{Type: "renameNetwork", Node: "n:3", Text: "Lamp's output"}))
	if !strings.Contains(out, "  NETWORK 'Lamp’s output'\n  C := c1.Q") {
		t.Fatalf("rename:\n%s", out)
	}
	out = apply(t, out, mustOp(t, out, EditOp{Type: "renameNetwork", Node: "n:1", Text: "Copy"}))
	if !strings.Contains(out, "FBD\n  NETWORK 'Copy'\n  B := A") {
		t.Fatalf("titling the implicit network gives it a header:\n%s", out)
	}
	out = apply(t, out, mustOp(t, out, EditOp{Type: "addNetwork", Node: "n:1", Text: "New"}))
	if !strings.Contains(out, "  B := A\n  NETWORK 'New'\n  NETWORK 'Count A'") {
		t.Fatalf("add after 1:\n%s", out)
	}
	out = apply(t, out, mustOp(t, out, EditOp{Type: "insertStatement", Node: "n:2", Text: "B := C"}))
	if !strings.Contains(out, "  NETWORK 'New'\n  B := C\n  NETWORK 'Count A'") {
		t.Fatalf("insert into network 2:\n%s", out)
	}
	out = apply(t, out, mustOp(t, out, EditOp{Type: "moveNetwork", Node: "n:3", Value: "up"}))
	m := mustGraph(t, out)
	if m.Networks[1].Title != "Count A" || m.Networks[2].Title != "New" {
		t.Fatalf("move up:\n%s", out)
	}
	if !strings.Contains(out, "  NETWORK 'Count A'\n  // counts rising edges of A\n  Cnt := c1.CV") {
		t.Fatalf("a network moves with its comment and statements:\n%s", out)
	}
	out = apply(t, out, mustOp(t, out, EditOp{Type: "removeNetwork", Node: "n:3"}))
	if strings.Contains(out, "NETWORK 'New'") || !strings.Contains(out, "B := C") {
		t.Fatalf("remove keeps the statements:\n%s", out)
	}
	// Moving the implicit first network down gives it a header.
	out = apply(t, netSrc, mustOp(t, netSrc, EditOp{Type: "moveNetwork", Node: "n:1", Value: "down"}))
	if !strings.Contains(out, "FBD\n  NETWORK 'Count A'") || !strings.Contains(out, "  NETWORK\n  B := A\n  NETWORK\n  C := c1.Q") {
		t.Fatalf("implicit moved down:\n%s", out)
	}
	if _, err := ApplyEdit(netSrc, EditOp{Type: "moveNetwork", Node: "n:3", Value: "down"}); err == nil {
		t.Fatal("the last network cannot move down")
	}
	// Adding a network to a body that has none starts network 2.
	plain := "PROGRAM P\nVAR_EXTERNAL A : BOOL; B : BOOL; END_VAR\nFBD\n  B := A\nEND_FBD\nEND_PROGRAM\n"
	out = apply(t, plain, mustOp(t, plain, EditOp{Type: "addNetwork", Text: "Second"}))
	if m := mustGraph(t, out); len(m.Networks) != 2 || m.Networks[1].Title != "Second" {
		t.Fatalf("add to a plain body:\n%s", out)
	}
}
