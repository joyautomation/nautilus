package fbd

import (
	"sort"
	"strings"
	"testing"
)

const editSrc = `PROGRAM Main
VAR_EXTERNAL
  Start : BOOL; Stop : BOOL; Run : BOOL; Started : BOOL; TempC : REAL; Hot : BOOL;
END_VAR
FBD
  seal = OR(Start, Run)
  Run := AND(seal, NOT Stop) // seal-in
  t1 : TON(IN := Run, PT := T#5S)
  Started := t1.Q
  hot = GT(TempC, 80.0)
  Hot := hot
END_FBD
END_PROGRAM`

// apply runs the edits against the source, verifying the round trip: the
// result must re-parse, and asserted substrings must appear/disappear.
func apply(t *testing.T, src string, edits []TextEdit) string {
	t.Helper()
	lines := strings.Split(src, "\n")
	// Apply bottom-up so earlier positions stay valid.
	sorted := append([]TextEdit(nil), edits...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Line != sorted[j].Line {
			return sorted[i].Line > sorted[j].Line
		}
		return sorted[i].Col > sorted[j].Col
	})
	for _, e := range sorted {
		if e.Line == e.EndLine {
			l := lines[e.Line-1]
			lines[e.Line-1] = l[:e.Col-1] + e.NewText + l[e.EndCol-1:]
			continue
		}
		// Multi-line: splice from start position to end position.
		endIdx := e.EndLine - 1
		var tail string
		if endIdx < len(lines) {
			tail = lines[endIdx][e.EndCol-1:]
		}
		head := lines[e.Line-1][:e.Col-1]
		repl := strings.Split(head+e.NewText+tail, "\n")
		rest := append([]string(nil), lines[minInt(endIdx+1, len(lines)):]...)
		lines = append(lines[:e.Line-1], append(repl, rest...)...)
	}
	out := strings.Join(lines, "\n")
	if _, err := Graph(out); err != nil {
		t.Fatalf("edited source no longer parses: %v\n---\n%s", err, out)
	}
	return out
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func mustOp(t *testing.T, src string, op EditOp) []TextEdit {
	t.Helper()
	edits, err := ApplyEdit(src, op)
	if err != nil {
		t.Fatalf("ApplyEdit(%+v): %v", op, err)
	}
	return edits
}

func TestEditSetLiteral(t *testing.T) {
	// Wires resolve before FB calls, so the GT threshold is chip k:0.
	out := apply(t, editSrc, mustOp(t, editSrc, EditOp{Type: "setLiteral", Node: "k:0", Value: "95.5"}))
	if !strings.Contains(out, "GT(TempC, 95.5)") {
		t.Errorf("literal not replaced:\n%s", out)
	}
	// Garbage is rejected before it can corrupt the source.
	if _, err := ApplyEdit(editSrc, EditOp{Type: "setLiteral", Node: "k:0", Value: "80.0)) // ha"}); err == nil {
		t.Error("non-literal value must be rejected")
	}
}

func TestEditToggleNot(t *testing.T) {
	// Remove the existing NOT on Stop.
	out := apply(t, editSrc, mustOp(t, editSrc, EditOp{Type: "toggleNot", To: "b:c.Run", ToPin: "IN2"}))
	if !strings.Contains(out, "AND(seal, Stop)") {
		t.Errorf("NOT not removed:\n%s", out)
	}
	// Add one where there is none (the TON's IN).
	out = apply(t, editSrc, mustOp(t, editSrc, EditOp{Type: "toggleNot", To: "f:t1", ToPin: "IN"}))
	if !strings.Contains(out, "TON(IN := NOT Run,") {
		t.Errorf("NOT not added:\n%s", out)
	}
}

func TestEditRewire(t *testing.T) {
	// Rewire the TON's IN from Run to the seal wire.
	out := apply(t, editSrc, mustOp(t, editSrc, EditOp{
		Type: "rewire", To: "f:t1", ToPin: "IN", Source: "b:w.seal",
	}))
	if !strings.Contains(out, "TON(IN := seal,") {
		t.Errorf("not rewired:\n%s", out)
	}
	// FB output pin as source, explicit pin.
	out = apply(t, editSrc, mustOp(t, editSrc, EditOp{
		Type: "rewire", To: "c:Hot", ToPin: "", Source: "f:t1", SourcePin: "Q",
	}))
	if !strings.Contains(out, "Hot := t1.Q") {
		t.Errorf("not rewired to pin:\n%s", out)
	}
	// An anonymous block output can't be referenced.
	if _, err := ApplyEdit(editSrc, EditOp{Type: "rewire", To: "f:t1", ToPin: "IN", Source: "b:c.Run"}); err == nil ||
		!strings.Contains(err.Error(), "name this block") {
		t.Errorf("anonymous source must be rejected, got %v", err)
	}
}

func TestEditRenameWire(t *testing.T) {
	out := apply(t, editSrc, mustOp(t, editSrc, EditOp{Type: "rename", Node: "b:w.seal", NewName: "latch"}))
	if !strings.Contains(out, "latch = OR(Start, Run)") || !strings.Contains(out, "AND(latch, NOT Stop)") {
		t.Errorf("wire rename incomplete:\n%s", out)
	}
	if strings.Contains(out, "seal") && !strings.Contains(out, "seal-in") {
		t.Errorf("stale references left:\n%s", out)
	}
	// Collisions and bad identifiers are rejected.
	if _, err := ApplyEdit(editSrc, EditOp{Type: "rename", Node: "b:w.seal", NewName: "hot"}); err == nil {
		t.Error("collision must be rejected")
	}
	if _, err := ApplyEdit(editSrc, EditOp{Type: "rename", Node: "b:w.seal", NewName: "2bad"}); err == nil {
		t.Error("invalid identifier must be rejected")
	}
}

func TestEditRenameInstance(t *testing.T) {
	out := apply(t, editSrc, mustOp(t, editSrc, EditOp{Type: "rename", Node: "f:t1", NewName: "sealTimer"}))
	if !strings.Contains(out, "sealTimer : TON(IN := Run") || !strings.Contains(out, "Started := sealTimer.Q") {
		t.Errorf("instance rename incomplete:\n%s", out)
	}
}

func TestEditRenameSplitDeclAndCall(t *testing.T) {
	// Declaration and call as separate statements: both tokens rename.
	src := strings.Replace(editSrc,
		"  t1 : TON(IN := Run, PT := T#5S)",
		"  t1 : TON\n  t1(IN := Run, PT := T#5S)", 1)
	out := apply(t, src, mustOp(t, src, EditOp{Type: "rename", Node: "f:t1", NewName: "tmr"}))
	if !strings.Contains(out, "tmr : TON\n") || !strings.Contains(out, "tmr(IN := Run") ||
		!strings.Contains(out, "Started := tmr.Q") {
		t.Errorf("split decl/call rename incomplete:\n%s", out)
	}
}

func TestEditDelete(t *testing.T) {
	// A used wire refuses to die.
	if _, err := ApplyEdit(editSrc, EditOp{Type: "deleteNode", Node: "b:w.seal"}); err == nil ||
		!strings.Contains(err.Error(), "rewire") {
		t.Errorf("used wire must be protected, got %v", err)
	}
	// A used FB refuses too.
	if _, err := ApplyEdit(editSrc, EditOp{Type: "deleteNode", Node: "f:t1"}); err == nil {
		t.Error("read FB must be protected")
	}
	// Deleting the coil that reads t1.Q frees the FB for deletion.
	out := apply(t, editSrc, mustOp(t, editSrc, EditOp{Type: "deleteNode", Node: "c:Started"}))
	if strings.Contains(out, "Started := t1.Q") {
		t.Errorf("coil statement not deleted:\n%s", out)
	}
	out2 := apply(t, out, mustOp(t, out, EditOp{Type: "deleteNode", Node: "f:t1"}))
	if strings.Contains(out2, "TON") {
		t.Errorf("FB statement not deleted:\n%s", out2)
	}
	// Whole lines vanish — no blank husks left behind.
	if strings.Contains(out2, "\n\n\n") {
		t.Errorf("deletion left blank lines:\n%s", out2)
	}
}

func TestEditInsertStatement(t *testing.T) {
	out := apply(t, editSrc, mustOp(t, editSrc, EditOp{
		Type: "insertStatement", Text: "t2 : TON(IN := hot, PT := T#3S)",
	}))
	if !strings.Contains(out, "  t2 : TON(IN := hot, PT := T#3S)\nEND_FBD") {
		t.Errorf("statement not inserted above END_FBD:\n%s", out)
	}
	// Broken fragments and name collisions never reach the file.
	if _, err := ApplyEdit(editSrc, EditOp{Type: "insertStatement", Text: "t2 : TON(IN := "}); err == nil {
		t.Error("unparseable statement must be rejected")
	}
	if _, err := ApplyEdit(editSrc, EditOp{Type: "insertStatement", Text: "seal = AND(Start, Run)"}); err == nil {
		t.Error("duplicate wire name must be rejected")
	}
	if _, err := ApplyEdit(editSrc, EditOp{Type: "insertStatement", Text: "t1 : CTU(CU := Start, R := Stop, PV := 5)"}); err == nil {
		t.Error("duplicate instance name must be rejected")
	}
}

func TestLayoutOps(t *testing.T) {
	// Pin a node: block created above END_FBD.
	x, y := 320, 64
	out := apply(t, editSrc, mustOp(t, editSrc, EditOp{Type: "setLayout", Node: "c:Run", X: &x, Y: &y}))
	if !strings.Contains(out, "(* @layout\n    c:Run 320,64\n  *)") {
		t.Errorf("layout block not written:\n%s", out)
	}
	// The pinned position rides the model.
	m := mustGraph(t, out)
	run := m.node(t, "c:Run")
	if run.X == nil || *run.X != 320 || run.Y == nil || *run.Y != 64 {
		t.Errorf("pinned position not in model: %s", mustJSON(run))
	}
	// A second pin joins the block; the first survives.
	x2, y2 := 10, 20
	out2 := apply(t, out, mustOp(t, out, EditOp{Type: "setLayout", Node: "b:w.seal", X: &x2, Y: &y2}))
	if !strings.Contains(out2, "b:w.seal 10,20") || !strings.Contains(out2, "c:Run 320,64") {
		t.Errorf("second pin wrong:\n%s", out2)
	}
	// Renaming the wire carries its pin.
	out3 := apply(t, out2, mustOp(t, out2, EditOp{Type: "rename", Node: "b:w.seal", NewName: "latch"}))
	if !strings.Contains(out3, "b:w.latch 10,20") || strings.Contains(out3, "b:w.seal") {
		t.Errorf("rename didn't remap layout:\n%s", out3)
	}
	// clearLayout for one node keeps the other; clearing all removes the block.
	out4 := apply(t, out3, mustOp(t, out3, EditOp{Type: "clearLayout", Node: "b:w.latch"}))
	if strings.Contains(out4, "b:w.latch 10,20") || !strings.Contains(out4, "c:Run 320,64") {
		t.Errorf("single clear wrong:\n%s", out4)
	}
	out5 := apply(t, out4, mustOp(t, out4, EditOp{Type: "clearLayout"}))
	if strings.Contains(out5, "@layout") {
		t.Errorf("full clear left the block:\n%s", out5)
	}
	// The layout comment never disturbs compilation.
	if _, err := Compile(out2); err != nil {
		t.Errorf("layout block broke compilation: %v", err)
	}
}

func TestEditRewireUnwiredFBPin(t *testing.T) {
	// A CTU with only CU wired: dropping a source on R adds the argument.
	src := strings.Replace(editSrc,
		"  t1 : TON(IN := Run, PT := T#5S)",
		"  t1 : TON(IN := Run, PT := T#5S)\n  c1 : CTU(CU := Start, PV := 5)", 1)
	out := apply(t, src, mustOp(t, src, EditOp{
		Type: "rewire", To: "f:c1", ToPin: "R", Source: "v:Stop",
	}))
	if !strings.Contains(out, "CTU(CU := Start, PV := 5, R := Stop)") {
		t.Errorf("unwired pin not added:\n%s", out)
	}
	// A pin the FB doesn't have is rejected.
	if _, err := ApplyEdit(src, EditOp{Type: "rewire", To: "f:c1", ToPin: "NOPE", Source: "v:Stop"}); err == nil {
		t.Error("unknown pin must be rejected")
	}
}

func TestLayoutBatch(t *testing.T) {
	// A multi-node drag pins every node in one op — one atomic block write.
	out := apply(t, editSrc, mustOp(t, editSrc, EditOp{Type: "setLayout", Entries: []LayoutOpEntry{
		{Node: "c:Run", X: 100, Y: 10},
		{Node: "f:t1", X: 200, Y: 20},
		{Node: "b:w.seal", X: 300, Y: 30},
	}}))
	for _, want := range []string{"c:Run 100,10", "f:t1 200,20", "b:w.seal 300,30"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
}

func TestLayoutBatchSkipsPhantoms(t *testing.T) {
	// Selection drags can include synthetic entries with no model id — the
	// batch pins the real nodes and ignores the phantoms.
	edits := mustOp(t, editSrc, EditOp{Type: "setLayout", Entries: []LayoutOpEntry{
		{Node: "", X: 1, Y: 2},
		{Node: "c:Run", X: 100, Y: 10},
		{Node: "nonsense", X: 3, Y: 4},
	}})
	out := apply(t, editSrc, edits)
	if !strings.Contains(out, "c:Run 100,10") {
		t.Errorf("real node not pinned:\n%s", out)
	}
	if strings.Contains(out, "nonsense") || strings.Contains(out, " 1,2") {
		t.Errorf("phantom entries leaked:\n%s", out)
	}
	// All-phantom batches are a clean no-op.
	if edits, _ := ApplyEdit(editSrc, EditOp{Type: "setLayout", Entries: []LayoutOpEntry{{Node: "", X: 1, Y: 2}}}); len(edits) != 0 {
		t.Errorf("all-phantom batch should no-op, got %v", edits)
	}
}

func TestEditDisconnect(t *testing.T) {
	// FB pin: the named argument disappears entirely.
	out := apply(t, editSrc, mustOp(t, editSrc, EditOp{Type: "disconnect", To: "f:t1", ToPin: "PT"}))
	if !strings.Contains(out, "t1 : TON(IN := Run)") {
		t.Errorf("PT arg not removed:\n%s", out)
	}
	// First named arg: the separator after it goes too.
	out = apply(t, editSrc, mustOp(t, editSrc, EditOp{Type: "disconnect", To: "f:t1", ToPin: "IN"}))
	if !strings.Contains(out, "t1 : TON(PT := T#5S)") {
		t.Errorf("IN arg not removed:\n%s", out)
	}
	// Fixed-arity operator input refuses with guidance.
	if _, err := ApplyEdit(editSrc, EditOp{Type: "disconnect", To: "b:w.hot", ToPin: "IN2"}); err == nil ||
		!strings.Contains(err.Error(), "at least 2") {
		t.Errorf("fixed-arity disconnect must refuse, got %v", err)
	}
	// Coil sources can't dangle.
	if _, err := ApplyEdit(editSrc, EditOp{Type: "disconnect", To: "c:Hot", ToPin: ""}); err == nil ||
		!strings.Contains(err.Error(), "coil") {
		t.Errorf("coil disconnect must refuse, got %v", err)
	}
}

func TestEditDisconnectExtensible(t *testing.T) {
	// A 3-input OR sheds one input and stays a valid 2-input OR.
	src := strings.Replace(editSrc, "seal = OR(Start, Run)", "seal = OR(Start, Run, Hot)", 1)
	out := apply(t, src, mustOp(t, src, EditOp{Type: "disconnect", To: "b:w.seal", ToPin: "IN2"}))
	if !strings.Contains(out, "seal = OR(Start, Hot)") {
		t.Errorf("middle input not removed:\n%s", out)
	}
	// Leading input removal keeps the list well-formed.
	out = apply(t, src, mustOp(t, src, EditOp{Type: "disconnect", To: "b:w.seal", ToPin: "IN1"}))
	if !strings.Contains(out, "seal = OR(Run, Hot)") {
		t.Errorf("leading input not removed:\n%s", out)
	}
	// At minimum arity it refuses.
	if _, err := ApplyEdit(out, EditOp{Type: "disconnect", To: "b:w.seal", ToPin: "IN1"}); err == nil {
		t.Error("2-input OR must refuse a disconnect")
	}
}

func TestEditAddInput(t *testing.T) {
	// Dropping a source on the "+" pin appends an argument.
	out := apply(t, editSrc, mustOp(t, editSrc, EditOp{Type: "addInput", Node: "b:w.seal", Source: "v:Stop"}))
	if !strings.Contains(out, "seal = OR(Start, Run, Stop)") {
		t.Errorf("input not appended:\n%s", out)
	}
	// Fixed-arity blocks refuse.
	if _, err := ApplyEdit(editSrc, EditOp{Type: "addInput", Node: "b:w.hot", Source: "v:Stop"}); err == nil ||
		!strings.Contains(err.Error(), "exactly 2") {
		t.Errorf("GT addInput must refuse, got %v", err)
	}
}
