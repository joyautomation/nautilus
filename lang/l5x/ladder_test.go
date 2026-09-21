package l5x

import (
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/ld"
)

func rungs(t *testing.T, name string, opts LadderOptions) []ld.Rung {
	t.Helper()
	m, err := Ladder(load(t, name), opts)
	if err != nil {
		t.Fatalf("Ladder: %v", err)
	}
	return m.Rungs
}

// The shape every ladder consumer already understands: contacts along the
// rail, a branch as legs, a coil at the right. A Logix rung lands in it
// unchanged, which is what makes the viewer and the revision diff work on
// Allen-Bradley code without touching either.
func TestLadderContactsAndCoils(t *testing.T) {
	r := rungs(t, "variety.L5X", LadderOptions{})[0]
	if r.Comment == "" || r.Line != 147 {
		t.Errorf("rung header = %q at line %d", r.Comment, r.Line)
	}
	if len(r.Elements) != 2 || r.Elements[0].Kind != "branch" {
		t.Fatalf("elements = %+v", r.Elements)
	}
	legs := r.Elements[0].Legs
	if len(legs) != 2 || legs[0][0].Ref != "StartPB" || legs[1][0].Ref != "RunCmd" {
		t.Errorf("branch legs = %+v", legs)
	}
	stop := r.Elements[1]
	if stop.Kind != "contact" || stop.Ref != "StopPB" || !stop.Neg {
		t.Errorf("XIO should be a negated contact, got %+v", stop)
	}
	if len(r.Coils) != 1 || r.Coils[0].Ref != "RunCmd" || r.Coils[0].Mode != "" {
		t.Errorf("coils = %+v", r.Coils)
	}
}

func TestLadderNestedBranchesAndBlocks(t *testing.T) {
	r := rungs(t, "variety.L5X", LadderOptions{})[1]
	outer := r.Elements[0]
	if outer.Kind != "branch" || len(outer.Legs) != 2 {
		t.Fatalf("outer branch = %+v", outer)
	}
	if inner := outer.Legs[0][1]; inner.Kind != "branch" || len(inner.Legs) != 2 {
		t.Errorf("nested branch = %+v", inner)
	}
	// A timer owns the structure its first operand names, so it draws as
	// a block with an instance — the way a nautilus TON does.
	tmr := r.Elements[1]
	if tmr.Kind != "fb" || tmr.Inst != "DwellTmr" || tmr.Type != "TON" || tmr.Args != "?, ?" {
		t.Errorf("TON = %+v", tmr)
	}
}

// Logix writes parallel outputs as a branch of coils. They are coils, and
// the coil zone is where a ladder draws them.
func TestLadderParallelOutputs(t *testing.T) {
	r := rungs(t, "variety.L5X", LadderOptions{})[2]
	if len(r.Elements) != 1 || r.Elements[0].Ref != "DwellTmr.DN" {
		t.Errorf("condition zone = %+v", r.Elements)
	}
	want := []struct{ ref, mode string }{{"Ready", ""}, {"Maint", "S"}, {"Fault", "R"}}
	if len(r.Coils) != len(want) {
		t.Fatalf("coils = %+v", r.Coils)
	}
	for i, w := range want {
		if r.Coils[i].Ref != w.ref || r.Coils[i].Mode != w.mode {
			t.Errorf("coil %d = %+v, want %s mode %q", i, r.Coils[i], w.ref, w.mode)
		}
	}
}

// An instruction with no nautilus equivalent still draws: a box with its
// mnemonic and operands, verbatim, which is what Studio 5000 draws too.
// Rendering is the whole contract — nothing here claims the rung executes
// the same way.
func TestLadderUnknownInstructionsDraw(t *testing.T) {
	all := rungs(t, "variety.L5X", LadderOptions{})
	boxes := all[3].Elements
	if len(boxes) != 3 {
		t.Fatalf("elements = %+v", boxes)
	}
	for i, want := range []struct{ fn, args string }{
		{"GEQ", "P101_Level.EU, P101_Level.Alarms.Hi"},
		{"MOV", "1, Scratch"},
		{"CPT", "RunHours, (RunHours + 1) / 2"}, // an expression operand, intact
	} {
		if boxes[i].Kind != "fn" || boxes[i].Fn != want.fn || boxes[i].Args != want.args {
			t.Errorf("element %d = %+v, want %s(%s)", i, boxes[i], want.fn, want.args)
		}
	}
	// An Add-On Instruction call is just another box.
	if aoi := all[4].Elements[1]; aoi.Kind != "fn" || aoi.Fn != "Pump_Control" {
		t.Errorf("AOI call = %+v", aoi)
	}
	// And a rung with no output at all is still a rung.
	if last := all[5]; len(last.Coils) != 0 || last.Elements[0].Fn != "NOP" {
		t.Errorf("NOP rung = %+v", last)
	}
}

func TestLadderGroupsRoutines(t *testing.T) {
	f := load(t, "variety.L5X")
	// One routine: no grouping, because there is nothing to group.
	m, err := Ladder(f, LadderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Blocks) != 0 || m.Rungs[0].POU != "" {
		t.Errorf("a lone routine should not be grouped: %+v", m.Blocks)
	}

	// Several: each becomes a Block, and its rungs carry the POU, so a
	// viewer draws them under headings.
	f2 := load(t, "demoline.L5X")
	f2.Controller.Programs = append(f2.Controller.Programs, &Program{
		Name:     "Second",
		Routines: []*Routine{{Name: "R", Type: "RLL", Rungs: []Rung{{Number: 0, Text: "OTE(x);"}}}},
	})
	m, err = Ladder(f2, LadderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Blocks) != 2 {
		t.Fatalf("blocks = %+v", m.Blocks)
	}
	if m.Rungs[len(m.Rungs)-1].POU != "Second/R" {
		t.Errorf("rung POU = %q", m.Rungs[len(m.Rungs)-1].POU)
	}
}

func TestLadderSelectsARoutine(t *testing.T) {
	f := load(t, "variety.L5X")
	for _, sel := range []string{"MainRoutine", "MainProgram/MainRoutine", "mainroutine"} {
		if _, err := Ladder(f, LadderOptions{Routine: sel}); err != nil {
			t.Errorf("select %q: %v", sel, err)
		}
	}
	// An ST routine is not ladder, and the error says what is available
	// rather than returning an empty diagram.
	_, err := Ladder(f, LadderOptions{Routine: "Calcs"})
	if err == nil || !strings.Contains(err.Error(), "MainProgram/MainRoutine") {
		t.Errorf("err = %v", err)
	}
}

func TestLadderReportsABadRung(t *testing.T) {
	f := load(t, "variety.L5X")
	f.Controller.Programs[0].Routines[0].Rungs[0].Text = "XIC(a"
	_, err := Ladder(f, LadderOptions{})
	if err == nil || !strings.Contains(err.Error(), "MainProgram/MainRoutine rung 0") {
		t.Errorf("err = %v, want the rung named", err)
	}
}
