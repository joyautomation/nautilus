package fbd

import (
	"encoding/json"
	"strings"
	"testing"
)

func mustGraph(t *testing.T, src string) *Model {
	t.Helper()
	m, err := Graph(src)
	if err != nil {
		t.Fatalf("Graph: %v", err)
	}
	return m
}

func (m *Model) node(t *testing.T, id string) *Node {
	t.Helper()
	for _, n := range m.Nodes {
		if n.ID == id {
			return n
		}
	}
	t.Fatalf("node %q not in model; have %s", id, mustJSON(m))
	return nil
}

func (m *Model) edge(t *testing.T, from, to string) *Edge {
	t.Helper()
	for _, e := range m.Edges {
		if e.From == from && e.To == to {
			return e
		}
	}
	t.Fatalf("edge %s -> %s not in model; have %s", from, to, mustJSON(m))
	return nil
}

func mustJSON(v any) string {
	b, _ := json.MarshalIndent(v, "", " ")
	return string(b)
}

func TestGraphSealInLatch(t *testing.T) {
	src := `PROGRAM Latch
VAR_EXTERNAL
  Start : BOOL; Stop : BOOL; Run : BOOL;
END_VAR
FBD
  seal  = OR(Start, Run)
  Run  := AND(seal, NOT Stop)
END_FBD
END_PROGRAM`
	m := mustGraph(t, src)

	if m.Name != "Latch" {
		t.Errorf("Name = %q, want Latch", m.Name)
	}

	or := m.node(t, "b:w.seal")
	if or.Kind != "block" || or.Label != "OR" || or.Wire != "seal" {
		t.Errorf("OR block wrong: %s", mustJSON(or))
	}
	and := m.node(t, "b:c.Run")
	if and.Kind != "block" || and.Label != "AND" {
		t.Errorf("AND block wrong: %s", mustJSON(and))
	}
	coil := m.node(t, "c:Run")
	if coil.Kind != "coil" || coil.Label != "Run" {
		t.Errorf("coil wrong: %s", mustJSON(coil))
	}

	// Start feeds the OR normally; Run feeds it back from the coil.
	if e := m.edge(t, "v:Start", "b:w.seal"); e.ToPin != "IN1" || e.Feedback {
		t.Errorf("Start edge wrong: %s", mustJSON(e))
	}
	fb := m.edge(t, "c:Run", "b:w.seal")
	if !fb.Feedback || fb.ToPin != "IN2" {
		t.Errorf("Run feedback edge wrong: %s", mustJSON(fb))
	}
	// The seal wire fans into the AND, carrying its name; Stop is negated.
	if e := m.edge(t, "b:w.seal", "b:c.Run"); e.Wire != "seal" || e.FromPin != "OUT" || e.ToPin != "IN1" {
		t.Errorf("seal wire edge wrong: %s", mustJSON(e))
	}
	if e := m.edge(t, "v:Stop", "b:c.Run"); !e.Negated || e.ToPin != "IN2" {
		t.Errorf("NOT Stop edge wrong: %s", mustJSON(e))
	}
	m.edge(t, "b:c.Run", "c:Run")

	// Longest-path layers: OR 1, AND 2, coil right-aligned at 3. Inputs sit
	// one left of their nearest consumer, not in a global first column.
	for id, want := range map[string]int{
		"v:Start": 0, "v:Stop": 1, "b:w.seal": 1, "b:c.Run": 2, "c:Run": 3,
	} {
		if got := m.node(t, id).Layer; got != want {
			t.Errorf("layer(%s) = %d, want %d", id, got, want)
		}
	}
}

func TestGraphNetworksSplitInputChips(t *testing.T) {
	// Two independent networks both read TempC: each gets its own chip (the
	// IEC variable-box-per-network convention), so a renderer can band the
	// networks by connectivity.
	src := `PROGRAM P
VAR_EXTERNAL TempC : REAL; Hot : BOOL; Cold : BOOL; END_VAR
FBD
  Hot := GT(TempC, 80.0)
  Cold := LT(TempC, 5.0)
END_FBD
END_PROGRAM`
	m := mustGraph(t, src)
	c1 := m.node(t, "v:TempC")
	c2 := m.node(t, "v:TempC#2")
	if c1.Label != "TempC" || c2.Label != "TempC" {
		t.Errorf("chip labels: %q, %q", c1.Label, c2.Label)
	}
	if e := m.edge(t, "v:TempC", "b:c.Hot"); e.Feedback {
		t.Errorf("chip edge wrong: %s", mustJSON(e))
	}
	m.edge(t, "v:TempC#2", "b:c.Cold")
}

func TestGraphCrossNetworkCoilReadBecomesChip(t *testing.T) {
	// Run's seal-in stays a feedback wire inside its own network, but the TON
	// network reading Run gets a variable chip instead of a sheet-crossing
	// wire.
	src := `PROGRAM Motor
VAR_EXTERNAL Start : BOOL; Stop : BOOL; Run : BOOL; Started : BOOL; END_VAR
FBD
  seal = OR(Start, Run)
  Run := AND(seal, NOT Stop)
  t1 : TON(IN := Run, PT := T#5S)
  Started := t1.Q
END_FBD
END_PROGRAM`
	m := mustGraph(t, src)
	if e := m.edge(t, "c:Run", "b:w.seal"); !e.Feedback {
		t.Errorf("in-network seal-in must stay a feedback wire: %s", mustJSON(e))
	}
	runChip := m.node(t, "v:Run")
	if runChip.Kind != "input" || runChip.Label != "Run" {
		t.Errorf("cross-network read should be a chip: %s", mustJSON(runChip))
	}
	if e := m.edge(t, "v:Run", "f:t1"); e.Feedback || e.ToPin != "IN" {
		t.Errorf("chip->TON edge wrong: %s", mustJSON(e))
	}
	for _, e := range m.Edges {
		if e.From == "c:Run" && e.To == "f:t1" {
			t.Errorf("coil->TON wire should have been replaced: %s", mustJSON(e))
		}
	}
}

func TestGraphFBInstancePins(t *testing.T) {
	src := `PROGRAM Timed
VAR_EXTERNAL
  Run : BOOL; Started : BOOL;
END_VAR
FBD
  t1 : TON(IN := Run, PT := T#5S)
  Started := t1.Q
END_FBD
END_PROGRAM`
	m := mustGraph(t, src)

	ton := m.node(t, "f:t1")
	if ton.Kind != "fb" || ton.Label != "t1" || ton.Type != "TON" {
		t.Errorf("TON node wrong: %s", mustJSON(ton))
	}
	// Pins come from the registered FBDef, not just usage.
	if strings.Join(ton.Inputs, ",") != "IN,PT" {
		t.Errorf("TON inputs = %v, want [IN PT]", ton.Inputs)
	}
	if strings.Join(ton.Outputs, ",") != "Q,ET" {
		t.Errorf("TON outputs = %v, want [Q ET]", ton.Outputs)
	}

	if e := m.edge(t, "v:Run", "f:t1"); e.ToPin != "IN" {
		t.Errorf("Run edge wrong: %s", mustJSON(e))
	}
	// The PT literal renders as a constant input chip.
	lit := m.node(t, "k:0")
	if lit.Kind != "input" || lit.Label != "T#5S" {
		t.Errorf("literal chip wrong: %s", mustJSON(lit))
	}
	if e := m.edge(t, "k:0", "f:t1"); e.ToPin != "PT" {
		t.Errorf("PT edge wrong: %s", mustJSON(e))
	}
	// Q pin read feeds the coil; TON at layer 1, coil right-aligned.
	if e := m.edge(t, "f:t1", "c:Started"); e.FromPin != "Q" {
		t.Errorf("Q edge wrong: %s", mustJSON(e))
	}
	if ton.Layer != 1 || m.node(t, "c:Started").Layer != 2 {
		t.Errorf("layers: TON %d (want 1), coil %d (want 2)",
			ton.Layer, m.node(t, "c:Started").Layer)
	}
}

func TestGraphFanoutAndNesting(t *testing.T) {
	src := `PROGRAM Calc
VAR_EXTERNAL
  A : REAL; B : REAL; Sum : REAL; Doubled : REAL; Big : BOOL;
END_VAR
FBD
  s = ADD(A, B)
  Sum := s
  Doubled := MUL(s, 2.0)
  Big := GT(s, 100.0)
END_FBD
END_PROGRAM`
	m := mustGraph(t, src)

	// One ADD block; three edges fan out from its OUT pin.
	add := m.node(t, "b:w.s")
	if add.Label != "ADD" || add.Wire != "s" {
		t.Errorf("ADD block wrong: %s", mustJSON(add))
	}
	fanout := 0
	for _, e := range m.Edges {
		if e.From == "b:w.s" {
			fanout++
			if e.Wire != "s" {
				t.Errorf("fan-out edge missing wire label: %s", mustJSON(e))
			}
		}
	}
	if fanout != 3 {
		t.Errorf("fan-out = %d, want 3 (Sum, MUL, GT)", fanout)
	}
	// Nested blocks hang off their coil's id.
	m.edge(t, "b:w.s", "b:c.Doubled")
	m.edge(t, "b:c.Doubled", "c:Doubled")
	if m.node(t, "b:c.Big").Label != "GT" {
		t.Errorf("GT block wrong: %s", mustJSON(m.node(t, "b:c.Big")))
	}
}

func TestGraphStructMemberIsInputChip(t *testing.T) {
	src := `PROGRAM UsesUDT
VAR_EXTERNAL
  M : Motor_Type; FastOut : BOOL;
END_VAR
FBD
  FastOut := GT(M.Speed, 50.0)
END_FBD
END_PROGRAM`
	m := mustGraph(t, src)
	chip := m.node(t, "v:M.Speed")
	if chip.Kind != "input" || chip.Label != "M.Speed" {
		t.Errorf("struct member chip wrong: %s", mustJSON(chip))
	}
	if e := m.edge(t, "v:M.Speed", "b:c.FastOut"); e.ToPin != "IN1" {
		t.Errorf("member edge wrong: %s", mustJSON(e))
	}
}

func TestGraphCombinationalLoopRejected(t *testing.T) {
	src := `PROGRAM Loop
VAR_EXTERNAL X : BOOL; END_VAR
FBD
  a = AND(b, X)
  b = OR(a, X)
  X := a
END_FBD
END_PROGRAM`
	if _, err := Graph(src); err == nil || !strings.Contains(err.Error(), "combinational loop") {
		t.Fatalf("expected combinational-loop error, got %v", err)
	}
}

func TestGraphFBCallCycleBreaksDeterministically(t *testing.T) {
	// Two FBs feeding each other is legal (scan semantics); layering must
	// terminate and mark one closing edge as feedback.
	src := `PROGRAM Cycle
FBD
  t1 : TON(IN := t2.Q, PT := T#1S)
  t2 : TON(IN := t1.Q, PT := T#1S)
END_FBD
END_PROGRAM`
	m := mustGraph(t, src)
	e12 := m.edge(t, "f:t1", "f:t2")
	e21 := m.edge(t, "f:t2", "f:t1")
	if e12.Feedback == e21.Feedback {
		t.Errorf("exactly one cycle edge should be feedback: t1->t2 %v, t2->t1 %v",
			e12.Feedback, e21.Feedback)
	}
	if m.node(t, "f:t1").Layer < 1 || m.node(t, "f:t2").Layer < 1 {
		t.Errorf("cycle nodes must still get layers: %s", mustJSON(m.Nodes))
	}
}

func TestGraphDeterministicJSON(t *testing.T) {
	src := `PROGRAM Latch
VAR_EXTERNAL Start : BOOL; Stop : BOOL; Run : BOOL; END_VAR
FBD
  seal = OR(Start, Run)
  Run := AND(seal, NOT Stop)
END_FBD
END_PROGRAM`
	a := mustJSON(mustGraph(t, src))
	for i := 0; i < 5; i++ {
		if b := mustJSON(mustGraph(t, src)); b != a {
			t.Fatalf("model JSON not deterministic:\n%s\n---\n%s", a, b)
		}
	}
}

func TestGraphEditSpans(t *testing.T) {
	// Spans anchor diagram gestures to text edits: a literal chip knows its
	// token, an edge knows where its consumer argument starts (NOT insertion
	// point) and, when negated, where the NOT keyword and its operand sit.
	src := `PROGRAM T
VAR_EXTERNAL A : BOOL; B : BOOL; C : BOOL; END_VAR
FBD
  B := AND(A, NOT C)
  t1 : TON(IN := B, PT := T#5S)
END_FBD
END_PROGRAM`
	m := mustGraph(t, src)

	lit := m.node(t, "k:0")
	if lit.Src == nil || lit.Src.Line != 5 || lit.Src.Text != "T#5S" {
		t.Errorf("literal span wrong: %s", mustJSON(lit.Src))
	}
	// Line 4: "  B := AND(A, NOT C)" — A at col 12, NOT at col 15, C at 19.
	andBlock := "b:c.B"
	plain := m.edge(t, "v:A", andBlock)
	if plain.Arg == nil || plain.Arg.Line != 4 || plain.Arg.Col != 12 || plain.Not != nil {
		t.Errorf("plain edge spans wrong: %s", mustJSON(plain))
	}
	if plain.Arg.Text != "A" || plain.Arg.EndCol != 13 {
		t.Errorf("plain arg text/end wrong: %s", mustJSON(plain.Arg))
	}
	neg := m.edge(t, "v:C", andBlock)
	if !neg.Negated || neg.Arg == nil || neg.Arg.Col != 15 {
		t.Errorf("negated edge arg wrong: %s", mustJSON(neg))
	}
	if neg.Arg.Text != "NOT C" || neg.Arg.EndCol != 20 {
		t.Errorf("negated arg text/end wrong: %s", mustJSON(neg.Arg))
	}
	// The coil's whole source expression is replaceable for rewiring.
	if e := m.edge(t, andBlock, "c:B"); e.Arg == nil || e.Arg.Text != "AND(A, NOT C)" {
		t.Errorf("coil arg span wrong: %s", mustJSON(e.Arg))
	}
	if neg.Not == nil || neg.Not.Col != 15 || neg.Not.Text != "NOT" ||
		neg.Inner == nil || neg.Inner.Col != 19 {
		t.Errorf("NOT spans wrong: not=%s inner=%s", mustJSON(neg.Not), mustJSON(neg.Inner))
	}
}

func TestGraphVars(t *testing.T) {
	src := `PROGRAM Main
VAR_EXTERNAL
  TempC : REAL;
  Heater : REAL;
END_VAR
VAR
  // retained state
  integral : REAL := 0.0;
END_VAR
FBD
  Heater := TempC
END_FBD
END_PROGRAM`
	m, err := Graph(src)
	if err != nil {
		t.Fatal(err)
	}
	want := []VarDecl{
		{Name: "TempC", Type: "REAL", Section: "VAR_EXTERNAL", Line: 3},
		{Name: "Heater", Type: "REAL", Section: "VAR_EXTERNAL", Line: 4},
		{Name: "integral", Type: "REAL", Init: "0.0", Section: "VAR", Line: 8},
	}
	if len(m.Vars) != len(want) {
		t.Fatalf("vars = %+v, want %d entries", m.Vars, len(want))
	}
	for i, w := range want {
		if m.Vars[i] != w {
			t.Errorf("vars[%d] = %+v, want %+v", i, m.Vars[i], w)
		}
	}
}
