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
	// Deleting a used wire is ALLOWED — the dangling reference becomes an
	// undeclared-identifier diagnostic, not a blocked edit.
	out0 := apply(t, editSrc, mustOp(t, editSrc, EditOp{Type: "deleteNode", Node: "b:w.seal"}))
	if strings.Contains(out0, "seal = OR(") {
		t.Errorf("wire statement not deleted:\n%s", out0)
	}
	// Same for an FB whose outputs are still read.
	out0 = apply(t, editSrc, mustOp(t, editSrc, EditOp{Type: "deleteNode", Node: "f:t1"}))
	if strings.Contains(out0, "TON(") {
		t.Errorf("read FB statement not deleted:\n%s", out0)
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
	// Fixed-arity pins keep their position with a placeholder — the edit is
	// never blocked; the undeclared "_" is the diagnostic breadcrumb.
	out = apply(t, editSrc, mustOp(t, editSrc, EditOp{Type: "disconnect", To: "b:w.hot", ToPin: "IN2"}))
	if !strings.Contains(out, "GT(TempC, _)") {
		t.Errorf("fixed-arity disconnect must placehold:\n%s", out)
	}
	// A coil can't dangle in text form — unwiring deletes the statement
	// (the coil lives on as a ghost when it has a canvas position; see
	// TestEditDisconnectCoilGhosts). The wire feeding it keeps its own
	// statement.
	out = apply(t, editSrc, mustOp(t, editSrc, EditOp{Type: "disconnect", To: "c:Hot", ToPin: ""}))
	if strings.Contains(out, "Hot :=") {
		t.Errorf("coil statement must be deleted:\n%s", out)
	}
	if !strings.Contains(out, "hot = GT(TempC, 80.0)") || !strings.Contains(out, "END_FBD") {
		t.Errorf("wire statement and END_FBD must survive:\n%s", out)
	}
	if _, err := Graph(out); err != nil {
		t.Fatalf("source no longer parses after coil disconnect: %v\n%s", err, out)
	}
}

// Unwiring a coil reverts both endpoints to floating ghost references — the
// inverse of wiring a ghost output — instead of leaving `X := _` diagnostics.
func TestEditDisconnectCoilGhosts(t *testing.T) {
	src := `PROGRAM Main
VAR_EXTERNAL
  Tag1 : REAL; Out1 : REAL;
END_VAR
FBD
  Out1 := Tag1
  (* @layout
    c:Out1 312,110
    v:Tag1 23,119
  *)
END_FBD
END_PROGRAM`
	out := apply(t, src, mustOp(t, src, EditOp{Type: "disconnect", To: "c:Out1", ToPin: ""}))
	if strings.Contains(out, "Out1 := Tag1") {
		t.Errorf("coil statement must be deleted:\n%s", out)
	}
	// Both endpoints keep their pinned spots as ghosts, old ids remapped.
	if !strings.Contains(out, "g:out.Out1 312,110") || !strings.Contains(out, "g:in.Tag1 23,119") {
		t.Errorf("endpoints must revert to ghost entries:\n%s", out)
	}
	if strings.Contains(out, "c:Out1") || strings.Contains(out, "v:Tag1") {
		t.Errorf("old layout ids must be remapped away:\n%s", out)
	}
	m, err := Graph(out)
	if err != nil {
		t.Fatalf("source no longer parses: %v\n%s", err, out)
	}
	ghosts := map[string]bool{}
	for _, n := range m.Nodes {
		if n.Ghost {
			ghosts[n.ID] = true
		}
	}
	if !ghosts["g:out.Out1"] || !ghosts["g:in.Tag1"] {
		t.Errorf("both ghost chips must render, got %v", ghosts)
	}

	// Never-dragged endpoints have no pinned entry — the live positions the
	// webview rides on the op keep them on the canvas.
	bare := strings.Replace(src, `  (* @layout
    c:Out1 312,110
    v:Tag1 23,119
  *)
`, "", 1)
	out = apply(t, bare, mustOp(t, bare, EditOp{
		Type: "disconnect", To: "c:Out1", ToPin: "",
		Entries: []LayoutOpEntry{{Node: "c:Out1", X: 5, Y: 6}, {Node: "v:Tag1", X: 7, Y: 8}},
	}))
	if !strings.Contains(out, "g:out.Out1 5,6") || !strings.Contains(out, "g:in.Tag1 7,8") {
		t.Errorf("live positions must pin the ghosts:\n%s", out)
	}

	// A stale ghost entry left over from the original wiring dedupes — the
	// remapped pin wins and the id appears exactly once.
	stale := strings.Replace(src, "c:Out1 312,110", "c:Out1 312,110\n    g:in.Tag1 1,1", 1)
	out = apply(t, stale, mustOp(t, stale, EditOp{Type: "disconnect", To: "c:Out1", ToPin: ""}))
	if strings.Count(out, "g:in.Tag1") != 1 || !strings.Contains(out, "g:in.Tag1 23,119") {
		t.Errorf("stale ghost entry must dedupe to the remapped position:\n%s", out)
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
	// At minimum arity the pin placeholds instead of refusing.
	out = apply(t, out, mustOp(t, out, EditOp{Type: "disconnect", To: "b:w.seal", ToPin: "IN1"}))
	if !strings.Contains(out, "seal = OR(_, Hot)") {
		t.Errorf("min-arity disconnect must placehold:\n%s", out)
	}
}

// Double-clicking a variable chip retargets what it reads: every argument
// the chip feeds rewrites in place, NOT bubbles stay, and "Name : TYPE"
// declares the new tag in the same gesture.
func TestEditRetarget(t *testing.T) {
	src := `PROGRAM Main
VAR_EXTERNAL
  a : BOOL; b : BOOL; c : BOOL; Out : BOOL;
END_VAR
FBD
  w = AND(a, NOT b, a)
  Out := w
END_FBD
END_PROGRAM`

	// Fan-out: both reads of `a` (one chip) move; the NOT on b is untouched.
	out := apply(t, src, mustOp(t, src, EditOp{Type: "retarget", Node: "v:a", NewName: "c"}))
	if !strings.Contains(out, "w = AND(c, NOT b, c)") {
		t.Errorf("both refs must retarget:\n%s", out)
	}

	// A negated read keeps its bubble — only the name inside changes.
	out = apply(t, src, mustOp(t, src, EditOp{Type: "retarget", Node: "v:b", NewName: "c"}))
	if !strings.Contains(out, "w = AND(a, NOT c, a)") {
		t.Errorf("NOT must survive retarget:\n%s", out)
	}

	// Declare-and-wire: an undeclared open pin picks a brand-new tag.
	severed := strings.Replace(src, "AND(a, NOT b, a)", "AND(_, NOT b, a)", 1)
	out = apply(t, severed, mustOp(t, severed, EditOp{
		Type: "retarget", Node: "v:_", NewName: "Fresh", Value: "BOOL", Text: "VAR_EXTERNAL",
	}))
	if !strings.Contains(out, "AND(Fresh, NOT b, a)") || !strings.Contains(out, "Fresh : BOOL;") {
		t.Errorf("declare-and-wire must rewrite the ref and add the declaration:\n%s", out)
	}
	if _, err := Graph(out); err != nil {
		t.Fatalf("retargeted source no longer graphs: %v\n%s", err, out)
	}

	// Same name, no declare → nothing to do; that's an error, not a no-op.
	if _, err := ApplyEdit(src, EditOp{Type: "retarget", Node: "v:a", NewName: "a"}); err == nil {
		t.Error("retarget to the same name must error")
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

func TestEditDeclareVar(t *testing.T) {
	// Into the existing VAR_EXTERNAL section, before its END_VAR.
	out := apply(t, editSrc, mustOp(t, editSrc, EditOp{Type: "declareVar", NewName: "condition", Value: "BOOL"}))
	if !strings.Contains(out, "condition : BOOL;\nEND_VAR") {
		t.Errorf("not declared in VAR_EXTERNAL:\n%s", out)
	}
	// A VAR section doesn't exist in editSrc — created above FBD.
	out2 := apply(t, editSrc, mustOp(t, editSrc, EditOp{Type: "declareVar", NewName: "accum", Value: "REAL", Text: "VAR"}))
	if !strings.Contains(out2, "VAR\n  accum : REAL;\nEND_VAR\nFBD") {
		t.Errorf("VAR section not created:\n%s", out2)
	}
	// The declared program still compiles end to end.
	if _, err := Compile(out2); err != nil {
		t.Errorf("declared program broke compilation: %v", err)
	}
	// Duplicates and collisions refuse.
	if _, err := ApplyEdit(editSrc, EditOp{Type: "declareVar", NewName: "TempC", Value: "REAL"}); err == nil {
		t.Error("existing declaration must be rejected")
	}
	if _, err := ApplyEdit(editSrc, EditOp{Type: "declareVar", NewName: "seal", Value: "BOOL"}); err == nil {
		t.Error("netlist name collision must be rejected")
	}
	if _, err := ApplyEdit(editSrc, EditOp{Type: "declareVar", NewName: "2bad", Value: "BOOL"}); err == nil {
		t.Error("invalid identifier must be rejected")
	}
}

// #244: a conversion block takes exactly its one input, from the registry
// rather than a hand-kept list, so the editor never offers a second pin.
func TestOpArityConversionsFromRegistry(t *testing.T) {
	for _, fn := range []string{"DINT_TO_REAL", "STRING_TO_TIME", "TO_DWORD", "TRUNC_INT", "LREAL_TRUNC_DINT"} {
		if lo, hi := opArity(fn); lo != 1 || hi != 1 {
			t.Errorf("opArity(%s) = %d, %d; want 1, 1", fn, lo, hi)
		}
	}
	if lo, hi := opArity("MyFunc"); lo != 1 || hi != -1 {
		t.Errorf("an unknown function is unrestricted: got %d, %d", lo, hi)
	}
}
