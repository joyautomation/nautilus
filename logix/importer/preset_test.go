package importer

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/lang/l5x"
	"github.com/joyautomation/nautilus/logix/writer"
)

// A preset the logic computes (#700): MUL(sp,1000,t.PRE) ahead of the
// timer used to come across as `t.PT := sp * 1000` beside a call bound to
// the export's PT := T#5S, so the timer ran at 5 s whatever sp was. Now the
// preset is a variable: seeded with the export's PRE, written by the logic,
// read where the logic reads .PRE, and bound at the call. A counter's the
// same, without the ms → TIME.
func TestComputedPresetsDriveTheBlock(t *testing.T) {
	tags := []string{"go:BOOL", "pulse:BOOL", "late:BOOL", "sp:DINT", "n:DINT", "t:TIMER=5000", "c:COUNTER=3", "Ts:TIMER[4]=1000"}
	doc := miniL5X(tags,
		"XIC(go)MUL(sp,1000,t.PRE)TON(t,?,?);",
		"GRT(t.PRE,20000)OTE(late);",
		"XIC(late)ADD(n,2,c.PRE);",
		"XIC(pulse)CTU(c,?,?);",
		// An element at a literal index, as a converted SLC/PLC-5 timer
		// file has them: a variable of its own too.
		"XIC(pulse)MUL(sp,100,Ts[2].PRE);",
		"XIC(go)TON(Ts[2],?,?)OTE(late);",
	)
	p := mustImport(t, doc)
	for _, nt := range p.Notes {
		if nt.Rung >= 0 {
			t.Errorf("note: %s", nt)
		}
	}
	src := string(p.Files["Main.ld"])
	// Declarations are column-aligned; compare with runs of spaces as one.
	flat := strings.Join(strings.Fields(src), " ")
	for _, want := range []string{"t_PRE : DINT := 5000;", "c_PRE : DINT := 3;", "Ts_2_PRE : DINT := 1000;"} {
		if !strings.Contains(flat, want) {
			t.Errorf("missing declaration %q in\n%s", want, src)
		}
	}
	for _, want := range []string{
		"go { t_PRE := sp * 1000 } t:TON(PT := TO_TIME(t_PRE))",
		"GT(t_PRE, 20000) ( late )",
		"late { c_PRE := n + 2 }",
		"pulse c:CTU(PV := c_PRE)",
		"pulse { Ts_2_PRE := sp * 100 }",
		"go Ts[2]:TON(PT := TO_TIME(Ts_2_PRE))",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("missing %q in\n%s", want, src)
		}
	}
	for _, gone := range []string{"PT := T#5S", "PV := 3)", "t.PT"} {
		if strings.Contains(src, gone) {
			t.Errorf("%q: the export's preset still overrides the logic's\n%s", gone, src)
		}
	}
	if p.Complete != 1 {
		t.Errorf("complete %d, want 1", p.Complete)
	}

	// It loads as naut run loads it: the program compiles.
	fsys := fstest.MapFS{}
	for name, raw := range p.Files {
		fsys[name] = &fstest.MapFile{Data: raw}
	}
	if _, err := project.Load(fsys, ""); err != nil {
		t.Fatalf("the imported project does not load: %v\n%s", err, src)
	}

	// And it writes back: the variable preset is a MOVE onto .PRE ahead of
	// the block's rung, which the import folds again.
	out, diags, err := writer.Write(src, writer.Options{Controller: "Mini"})
	if err != nil || len(diags) > 0 {
		t.Fatalf("writer: %v %v\n%s", err, diags, src)
	}
	back, _ := l5x.Parse(out)
	got := strings.Join(rungTexts(back, "Main", "MainRoutine"), "\n")
	for _, want := range []string{"MOVE(t_PRE,t.PRE)", "MOVE(c_PRE,c.PRE)", "MOVE(Ts_2_PRE,Ts[2].PRE)", "TON(t,?,?)", "CTU(c,?,?)", "TON(Ts[2],?,?)"} {
		if !strings.Contains(got, want) {
			t.Errorf("written back lacks %q:\n%s", want, got)
		}
	}

	// Imported again, the write-back comes across as the same program: the
	// MOVE ahead of each block folds into its preset binding.
	p2 := mustImport(t, out)
	for _, nt := range p2.Notes {
		if nt.Rung >= 0 {
			t.Errorf("re-import note: %s", nt)
		}
	}
	src2 := string(p2.Files["Main.ld"])
	for _, want := range []string{
		"go { t_PRE := sp * 1000 } t:TON(PT := TO_TIME(t_PRE))",
		"GT(t_PRE, 20000) ( late )",
		"late { c_PRE := n + 2 }",
		"pulse c:CTU(PV := c_PRE)",
		"go Ts[2]:TON(PT := TO_TIME(Ts_2_PRE))",
	} {
		if !strings.Contains(src2, want) {
			t.Errorf("re-import lacks %q:\n%s", want, src2)
		}
	}
	if p2.Complete != 1 {
		t.Errorf("re-import complete %d, want 1\n%s", p2.Complete, src2)
	}
}

// A computed preset with no carried form is refused by name, not imported
// with the preset quietly ignored: one on an element at a computed index,
// and one a MOVE ahead of the timer also sets.
func TestComputedPresetsWithNoFormAreRefused(t *testing.T) {
	tags := []string{"go:BOOL", "b:BOOL", "sp:DINT", "i:DINT", "Ts:TIMER[3]=1000", "u:TIMER=2000"}
	doc := miniL5X(tags,
		"XIC(b)MUL(sp,1000,Ts[i].PRE);",
		"XIC(go)TON(Ts[i],?,?);",
		"XIC(b)MUL(sp,10,u.PRE);",
		"MOVE(500,u.PRE);",
		"XIC(go)TON(u,?,?);",
	)
	p := mustImport(t, doc)
	var why []string
	for _, n := range p.Notes {
		if n.Reason == "preset" {
			why = append(why, n.Why)
		}
	}
	if len(why) != 2 || !strings.Contains(why[0], "Ts[i], an element at a computed index") || !strings.Contains(why[1], "a MOVE ahead of its block also sets") {
		t.Errorf("preset refusals = %q", why)
	}
	src := string(p.Files["Main.ld"])
	if !strings.Contains(src, "NOT deployable") {
		t.Errorf("the routine should say it is incomplete:\n%s", src)
	}
}
