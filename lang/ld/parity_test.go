package ld

import (
	"strings"
	"testing"
)

// Editor parity with the Studio 5000 habits the logix-shaped build probes
// (#213 edge contacts, #214 a block's pins, #217 rung copy): every gesture
// lands as one `ld edit` op whose result parses and compiles.

const parityProg = `PROGRAM p
VAR_EXTERNAL Start : BOOL; Stop : BOOL; Run : BOOL; END_VAR
LD
  RUNG seal (* keep the comment *)
    Start /Stop ( Run )
END_LD
END_PROGRAM`

// #213: a retag may name the contact's form the way the rung grammar does.
func TestRetagSetsContactForm(t *testing.T) {
	out := applyStep(t, parityProg, EditOp{Type: "setRef", Rung: "seal", Path: []int{0}, Ref: "+Start"})
	if !strings.Contains(out, "+Start /Stop ( Run )") {
		t.Fatalf("+Tag makes a rising-edge contact:\n%s", out)
	}
	m, _ := Graph(out)
	if e := m.Rungs[0].Elements[0]; e.Kind != "edge" || e.Mode != "P" || e.Ref != "Start" {
		t.Fatalf("graph: %+v", e)
	}
	// A bare retag of an edge keeps it an edge.
	out = applyStep(t, out, EditOp{Type: "setRef", Rung: "seal", Path: []int{0}, Ref: "Stop"})
	if !strings.Contains(out, "+Stop /Stop ( Run )") {
		t.Fatalf("a bare retag keeps the edge:\n%s", out)
	}
	out = applyStep(t, out, EditOp{Type: "setRef", Rung: "seal", Path: []int{0}, Ref: "-Start"})
	if !strings.Contains(out, "-Start /Stop") {
		t.Fatalf("-Tag makes a falling-edge contact:\n%s", out)
	}
	out = applyStep(t, out, EditOp{Type: "setRef", Rung: "seal", Path: []int{0}, Ref: "/Start"})
	if !strings.Contains(out, "/Start /Stop") {
		t.Fatalf("/Tag makes an NC contact:\n%s", out)
	}
	// A bare retag of an NC contact keeps it NC (the editor shows the tag
	// without its slash).
	out = applyStep(t, out, EditOp{Type: "setRef", Rung: "seal", Path: []int{1}, Ref: "Run"})
	if !strings.Contains(out, "/Start /Run ( Run )") {
		t.Fatalf("bare retag keeps NC:\n%s", out)
	}
	if _, err := Transpile(out); err != nil {
		t.Fatalf("transpile: %v\n%s", err, out)
	}
	// A coil takes no form prefix; garbage after one is still refused.
	if _, err := ApplyEdit(parityProg, EditOp{Type: "setRef", Rung: "seal", Coil: coilIdx(0), Ref: "+Run"}); err == nil {
		t.Fatal("a coil retag to +Run must be refused")
	}
	if _, err := ApplyEdit(parityProg, EditOp{Type: "setRef", Rung: "seal", Path: []int{0}, Ref: "+"}); err == nil {
		t.Fatal("a bare + must be refused")
	}
	if _, err := ApplyEdit(parityProg, EditOp{Type: "setRef", Rung: "seal", Path: []int{0}, Ref: "+a b"}); err == nil {
		t.Fatal("+ with an invalid reference must be refused")
	}
}

// #213: the P key's op — one contact, four forms.
func TestSetContactForm(t *testing.T) {
	want := map[string]string{"P": "+Start /Stop", "N": "-Start /Stop", "NC": "/Start /Stop", "NO": "    Start /Stop"}
	for form, frag := range want {
		out := applyStep(t, parityProg, EditOp{Type: "setContactForm", Rung: "seal", Path: []int{0}, Mode: form})
		if !strings.Contains(out, frag) {
			t.Fatalf("%s:\n%s", form, out)
		}
	}
	// From an edge back to NO.
	edge := strings.Replace(parityProg, "Start /Stop", "+Start /Stop", 1)
	out := applyStep(t, edge, EditOp{Type: "setContactForm", Rung: "seal", Path: []int{0}, Mode: "NO"})
	if !strings.Contains(out, "    Start /Stop ( Run )") {
		t.Fatalf("edge → NO:\n%s", out)
	}
	if _, err := ApplyEdit(parityProg, EditOp{Type: "setContactForm", Rung: "seal", Coil: coilIdx(0), Mode: "P"}); err == nil {
		t.Fatal("a coil has no contact form")
	}
	if _, err := ApplyEdit(parityProg, EditOp{Type: "setContactForm", Rung: "seal", Path: []int{0}, Mode: "X"}); err == nil {
		t.Fatal("an unknown form must be refused")
	}
}

// #213: the palette's P / N contacts insert an edge with a "_" placeholder.
func TestInsertEdgeContact(t *testing.T) {
	out := applyStep(t, parityProg, EditOp{Type: "insert", Rung: "seal", Kind: "edge", Mode: "P", Index: 0})
	if !strings.Contains(out, "+_ Start /Stop ( Run )") {
		t.Fatalf("insert P:\n%s", out)
	}
	out = applyStep(t, out, EditOp{Type: "setRef", Rung: "seal", Path: []int{0}, Ref: "Start"})
	if !strings.Contains(out, "+Start Start /Stop") {
		t.Fatalf("retag the P placeholder:\n%s", out)
	}
}

const parityFB = `FUNCTION_BLOCK Starter
LD
END_LD
END_FUNCTION_BLOCK

PROGRAM p
VAR_EXTERNAL Go : BOOL; Halt : BOOL; Running : BOOL; END_VAR
LD
  RUNG call
    s1:Starter(Stop := Halt) ( Running )
END_LD
END_PROGRAM`

// #214: a FUNCTION_BLOCK's pins are declared, renamed and deleted from the
// diagram, into that block's header — in IEC's section order.
func TestBlockPins(t *testing.T) {
	out := applyStep(t, parityFB, EditOp{Type: "declareVar", Block: "Starter", Name: "Run", VarType: "BOOL", Section: "VAR_OUTPUT"})
	out = applyStep(t, out, EditOp{Type: "declareVar", Block: "Starter", Name: "Start", VarType: "BOOL", Section: "VAR_INPUT"})
	out = applyStep(t, out, EditOp{Type: "declareVar", Block: "Starter", Name: "Stop", VarType: "BOOL", Section: "VAR_INPUT"})
	out = applyStep(t, out, EditOp{Type: "declareVar", Block: "Starter", Name: "Hours", VarType: "REAL", Section: "VAR_IN_OUT"})
	out = applyStep(t, out, EditOp{Type: "declareVar", Block: "Starter", Name: "n", VarType: "INT", Section: "VAR"})
	wantHdr := "FUNCTION_BLOCK Starter\nVAR_INPUT\n    Start : BOOL;\n    Stop : BOOL;\nEND_VAR\nVAR_IN_OUT\n    Hours : REAL;\nEND_VAR\nVAR_OUTPUT\n    Run : BOOL;\nEND_VAR\nVAR\n    n : INT;\nEND_VAR\nLD\n"
	if !strings.HasPrefix(out, wantHdr) {
		t.Fatalf("block header:\n%s", out)
	}
	m, err := Graph(out)
	if err != nil {
		t.Fatal(err)
	}
	pins := []string{}
	for _, p := range m.Blocks[0].Pins {
		pins = append(pins, p.Dir+":"+p.Name)
	}
	if got := strings.Join(pins, " "); got != "in:Start in:Stop out:Run" {
		t.Fatalf("block pins: %s", got)
	}
	// The PROGRAM's header was not touched; a pin name is free there.
	if !strings.Contains(out, "VAR_EXTERNAL Go : BOOL; Halt : BOOL; Running : BOOL; END_VAR") {
		t.Fatalf("program header changed:\n%s", out)
	}
	// Duplicates within the block refuse; a PROGRAM takes no pins.
	if _, err := ApplyEdit(out, EditOp{Type: "declareVar", Block: "Starter", Name: "start", VarType: "BOOL", Section: "VAR_OUTPUT"}); err == nil {
		t.Fatal("a duplicate pin must be refused")
	}
	if _, err := ApplyEdit(out, EditOp{Type: "declareVar", Name: "X", VarType: "BOOL", Section: "VAR_INPUT"}); err == nil {
		t.Fatal("VAR_INPUT on the PROGRAM must be refused")
	}
	if _, err := ApplyEdit(out, EditOp{Type: "declareVar", Block: "Nope", Name: "X", VarType: "BOOL", Section: "VAR_INPUT"}); err == nil {
		t.Fatal("an unknown block must be refused")
	}

	// A rung in the block that uses the pins, then rename one: the
	// declaration, the block's rungs, and the caller's named binding.
	withRung := strings.Replace(out, "LD\nEND_LD\nEND_FUNCTION_BLOCK", "LD\n  RUNG seal\n    Start /Stop ( Run )\nEND_LD\nEND_FUNCTION_BLOCK", 1)
	out = applyStep(t, withRung, EditOp{Type: "renameVar", Block: "Starter", Name: "Stop", NewName: "StopPB"})
	for _, want := range []string{"    StopPB : BOOL;", "Start /StopPB ( Run )", "s1:Starter(StopPB := Halt)"} {
		if !strings.Contains(out, want) {
			t.Fatalf("renameVar: want %q in\n%s", want, out)
		}
	}
	if _, err := Transpile(out); err != nil {
		t.Fatalf("renamed source no longer transpiles: %v\n%s", err, out)
	}
	if _, err := ApplyEdit(out, EditOp{Type: "renameVar", Block: "Starter", Name: "StopPB", NewName: "Start"}); err == nil {
		t.Fatal("a rename onto another pin must be refused")
	}

	// Delete a pin by its block — not the program's variable of that name.
	out = applyStep(t, out, EditOp{Type: "deleteVar", Block: "Starter", Name: "Hours"})
	if strings.Contains(out, "Hours") {
		t.Fatalf("deleteVar in block:\n%s", out)
	}
	if _, err := ApplyEdit(out, EditOp{Type: "deleteVar", Block: "Starter", Name: "Go"}); err == nil {
		t.Fatal("Go is the program's, not the block's")
	}
}

// A program variable's rename follows every reference in the program —
// edge contacts and block arguments included — and nothing in a block.
func TestRenameProgramVar(t *testing.T) {
	src := `FUNCTION_BLOCK B
VAR_INPUT Go : BOOL; END_VAR
VAR_OUTPUT Done : BOOL; END_VAR
LD
  RUNG inner
    Go ( Done )
END_LD
END_FUNCTION_BLOCK

PROGRAM p
VAR_EXTERNAL
    Go : BOOL;
    Y : BOOL;
END_VAR
LD
  RUNG r
    +Go b1:B(Go := Go) ( Y )
END_LD
END_PROGRAM`
	out := applyStep(t, src, EditOp{Type: "renameVar", Name: "Go", NewName: "StartPB"})
	for _, want := range []string{"    StartPB : BOOL;", "+StartPB b1:B(Go := StartPB) ( Y )", "VAR_INPUT Go : BOOL; END_VAR", "    Go ( Done )"} {
		if !strings.Contains(out, want) {
			t.Fatalf("want %q in\n%s", want, out)
		}
	}
}

// #217: a whole rung pastes below another with a fresh name, its block
// instances renamed (m1 → m2) and its comment kept.
func TestPasteRung(t *testing.T) {
	src := `PROGRAM p
VAR_EXTERNAL A : BOOL; B : BOOL; Y : BOOL; Z : BOOL; END_VAR
LD
  RUNG m1 (* motor one *)
    +A m1:TON(PT := T#1S) ( Y )
  RUNG perm
    B ( Z )
END_LD
END_PROGRAM`
	m, err := Graph(src)
	if err != nil {
		t.Fatal(err)
	}
	r := m.Rungs[0]
	op := EditOp{Type: "pasteRung", Name: "m1", After: "perm", Body: &RungBody{Comment: r.Comment, Elements: r.Elements, Coils: r.Coils}}
	out := applyStep(t, src, op)
	if !strings.Contains(out, "  RUNG perm\n    B ( Z )\n\n  RUNG m2  (* motor one *)\n    +A m2:TON(PT := T#1S) ( Y )\nEND_LD") {
		t.Fatalf("paste rung:\n%s", out)
	}
	// Again: m3, with its instance m3 (m2 is taken by now).
	out = applyStep(t, out, op)
	if !strings.Contains(out, "RUNG m3  (* motor one *)\n    +A m3:TON(PT := T#1S) ( Y )") {
		t.Fatalf("second paste:\n%s", out)
	}
	if _, err := Transpile(out); err != nil {
		t.Fatalf("pasted rungs no longer transpile: %v\n%s", err, out)
	}
	// No After: before END_LD; a name with no number gets one.
	out = applyStep(t, src, EditOp{Type: "pasteRung", Name: "perm", Body: &RungBody{Elements: m.Rungs[1].Elements, Coils: m.Rungs[1].Coils}})
	if !strings.Contains(out, "  RUNG perm2\n    B ( Z )\nEND_LD") {
		t.Fatalf("paste at the end:\n%s", out)
	}
	// A header-declared name is taken too.
	hdr := strings.Replace(src, "Z : BOOL; END_VAR", "Z : BOOL; END_VAR\nVAR m2 : TON; END_VAR", 1)
	out = applyStep(t, hdr, op)
	if !strings.Contains(out, "+A m3:TON(PT := T#1S) ( Y )") {
		t.Fatalf("paste past a header-declared m2:\n%s", out)
	}
	// Garbage is refused.
	for _, bad := range []*RungBody{nil, {}, {Elements: []Element{{Kind: "coil", Ref: "Y"}}}, {Coils: []Element{{Kind: "contact", Ref: "A"}}}, {Comment: "x *) y", Coils: []Element{{Kind: "coil", Ref: "Y"}}}} {
		if _, err := ApplyEdit(src, EditOp{Type: "pasteRung", Name: "m1", Body: bad}); err == nil {
			t.Fatalf("pasteRung %+v must be refused", bad)
		}
	}
}
