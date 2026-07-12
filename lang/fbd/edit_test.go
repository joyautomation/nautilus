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
