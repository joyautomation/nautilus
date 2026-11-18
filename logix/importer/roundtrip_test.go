package importer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/lang/l5x"
	"github.com/joyautomation/nautilus/logix/writer"
)

// TestRoundTripPublicYardstick is the offline, public form of the corpus
// round-trip measurement in TestCorpus (docs/design/logix-authoring.md):
// import batch-skid's examples/batch-skid/line/Line.L5X, write the
// imported nautilus project back as Logix, and compare every complete
// program routine's rung text with the original's. "Identical" is the
// corpus comparison, rung text equal modulo generated names (norm); a
// routine that is only textually different is "equivalent" by the design
// doc's definition (the writer moved a rung, e.g. RES ahead of its timer)
// and must be named here, not silently accepted.
//
// Current, documented state: Line.L5X has one program routine,
// MainProgram/Receive (5 rungs), imported complete (no notes) and written
// back IDENTICAL: every rung byte-identical. (Until #372 the example's
// rung 0 spelled the editor caption LES, which came back as the neutral
// LT the v38 SDK builds; the example now carries LT, as an export does.)
//
// The user's flow at the CLI — import --project, write, normalize --logic
// --check — and the whole-project differences it leaves (tags, values,
// descriptions, the envelope) are pinned by cmd/naut's
// TestLogixRoundTripYardstickCLI (#276).
func TestRoundTripPublicYardstick(t *testing.T) {
	orig, err := l5x.ParseFile(filepath.Join("..", "..", "examples", "batch-skid", "line", "Line.L5X"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := Import(orig, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Notes) != 0 {
		t.Errorf("import notes: %+v", p.Notes)
	}
	libs := libsOf(p)
	// The manifest says where the program goes back to (the routine, a
	// CONTINUOUS task), that every controller tag is written back named
	// or not (#370), and that the timer the program now owns goes back to
	// controller scope with its description (#371); it is not also a
	// project tag.
	for _, want := range []string{
		"    routine: Receive\n", "    task: MainTask\n", "    continuous: true\n",
		"    all-tags: true\n",
		"    controller-scoped:\n      - { name: AcceptTmr, desc: \"Accept-handshake settling delay\" }\n",
	} {
		if !strings.Contains(string(p.Files["nautilus.yaml"]), want) {
			t.Errorf("nautilus.yaml lacks %q:\n%s", want, p.Files["nautilus.yaml"])
		}
	}
	// The UDT keeps its description, as the declaration's trailing
	// comment the writer reads back (#373).
	if want := "  LineStatus : STRUCT (* Mode/alarm summary the skid polls alongside the handshake tags *)\n"; !strings.Contains(string(p.Files["lib/logix_types.st"]), want) {
		t.Errorf("lib/logix_types.st lacks %q:\n%s", want, p.Files["lib/logix_types.st"])
	}
	if strings.Contains(string(p.Files["tags/logix.yaml"]), "AcceptTmr") {
		t.Errorf("tags/logix.yaml carries the program's timer:\n%s", p.Files["tags/logix.yaml"])
	}
	// The DINT no routine names keeps its type: init: 0 alone is a REAL.
	if !strings.Contains(string(p.Files["tags/logix.yaml"]), "- { name: Line_BatchId, role: input, type: DINT, init: 0,") {
		t.Errorf("tags/logix.yaml does not type Line_BatchId:\n%s", p.Files["tags/logix.yaml"])
	}

	var identical, differ []string
	var total int
	for _, prog := range orig.Controller.Programs {
		for _, r := range prog.Routines {
			name := prog.Name + "/" + r.Name
			if r.Type != "RLL" {
				t.Errorf("%s: unexpected %s routine", name, r.Type)
				continue
			}
			total++
			file := l5x.Ident(prog.Name) + ".ld"
			src, ok := p.Files[file]
			if !ok {
				t.Errorf("%s: not imported (no %s)", name, file)
				continue
			}
			if strings.Contains(string(src), "NOT deployable") {
				t.Errorf("%s: imported incomplete", name)
				continue
			}
			out, diags, err := writer.Write(string(src), writer.Options{Controller: orig.Controller.Name, Libs: libs})
			if err != nil || len(diags) > 0 {
				t.Errorf("%s: writer refused: %v %v", name, err, diags)
				continue
			}
			back, err := l5x.Parse(out)
			if err != nil {
				t.Fatalf("%s: written export does not parse: %v", name, err)
			}
			if len(back.Controller.DataTypes) != 1 || back.Controller.DataTypes[0].Description != orig.Controller.DataTypes[0].Description {
				t.Errorf("%s: data types written back %+v, want LineStatus with its description", name, back.Controller.DataTypes)
			}
			a := rungTexts(orig, prog.Name, r.Name)
			b := rungTexts(back, l5x.Ident(prog.Name), "MainRoutine")
			if strings.Join(a, "\n") == strings.Join(b, "\n") {
				identical = append(identical, name)
				continue
			}
			for i := range a {
				if i < len(b) && a[i] != b[i] {
					t.Errorf("%s rung %d differs:\n orig %s\n back %s", name, i, a[i], b[i])
				}
			}
			differ = append(differ, name)
		}
	}
	if total != 1 || len(identical) != 1 || identical[0] != "MainProgram/Receive" || len(differ) != 0 {
		t.Errorf("routines %d; identical %v; differ %v; want 1 routine, MainProgram/Receive identical", total, identical, differ)
	}
}

// An array of timers called by literal and by computed index (the writer's
// arrays golden): written, imported back, and written again, every call
// keeps its PT := — the MOVE helpers fold back into presets, and the
// literal-index elements' presets in the tag's data agree with them.
func TestRoundTripArrayPresets(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "writer", "testdata", "arrays.ld"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	out, diags, err := writer.Write(src, writer.Options{})
	if err != nil || len(diags) > 0 {
		t.Fatalf("writer: %v %v", err, diags)
	}
	f, err := l5x.Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	p, err := Import(f, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Notes) != 0 {
		t.Errorf("import notes: %+v", p.Notes)
	}
	ld := string(p.Files["MainProgram.ld"])
	for _, want := range []string{
		"Delays[0]:TON(PT := T#2S)",
		"Delays[3]:TON(PT := T#500MS)",
		"Delays[Sel]:TON(PT := T#1S)",
		"Starts[1]:CTU(PV := 3)",
	} {
		if !strings.Contains(ld, want) {
			t.Errorf("imported program lacks %q:\n%s", want, ld)
		}
	}
	if strings.Contains(ld, "MOVE") {
		t.Errorf("a preset helper MOVE was imported as a rung:\n%s", ld)
	}
	again, diags, err := writer.Write(ld, writer.Options{Libs: libsOf(p)})
	if err != nil || len(diags) > 0 {
		t.Fatalf("write-back: %v %v", err, diags)
	}
	back, err := l5x.Parse(again)
	if err != nil {
		t.Fatal(err)
	}
	a, b := rungTexts(f, "MainProgram", "MainRoutine"), rungTexts(back, "MainProgram", "MainRoutine")
	if strings.Join(a, "\n") != strings.Join(b, "\n") {
		t.Errorf("write-back differs:\n first %s\n again %s", strings.Join(a, "\n       "), strings.Join(b, "\n       "))
	}
}

// Every controller tag goes back on a write (#370, target.logix.all-tags),
// so the tag file types each one whose init: does not already say its
// type — a DINT (init: 0 alone is a REAL), an array, a tag with no value
// in the export — and reports each it cannot type, which a write-back
// therefore does not create, as it reports a LINT, which the Logix
// target's subset lacks. A controller-scoped timer the logic drives
// is the program's block instance, listed under
// target.logix.controller-scoped so it goes back to controller scope
// (#371).
func TestImportTypesEveryControllerTag(t *testing.T) {
	doc := miniL5X([]string{"Run:BOOL", "Count:DINT", "Hist:INT[10]", "Grid:DINT[2 3]", "Msg:MESSAGE", "Spare:TIMER=500", "Delay:TIMER=1000", "Big:LINT"},
		"XIC(Run)TON(Delay,?,?);")
	p := mustImport(t, doc)
	tags := string(p.Files["tags/logix.yaml"])
	for _, want := range []string{
		"- { name: Run, role: input, type: BOOL }",
		"- { name: Count, role: input, type: DINT }",
		// An array starts at the export's values, here none: all zero (#774).
		"- { name: Hist, role: input, type: \"ARRAY [0..9] OF INT\", init: [] }",
		"- { name: Grid, role: input }",
		"- { name: Msg, role: input }",
		"- { name: Spare, role: input }",
		"- { name: Big, role: input, type: LINT }",
	} {
		if !strings.Contains(tags, want+"\n") {
			t.Errorf("tags/logix.yaml lacks %q:\n%s", want, tags)
		}
	}
	if strings.Contains(tags, "Delay") {
		t.Errorf("the program's timer is also a project tag:\n%s", tags)
	}
	// And the project loads as `naut run` loads it (project.Load: manifest,
	// tag files, programs): an array type left bare made the whole tag file
	// unparseable, so `naut run` refused every import with one.
	fsys := fstest.MapFS{}
	for name, raw := range p.Files {
		fsys[name] = &fstest.MapFile{Data: raw}
	}
	if _, err := project.Load(fsys, ""); err != nil {
		t.Errorf("the imported project does not load: %v", err)
	}
	var notes []string
	for _, n := range p.Notes {
		notes = append(notes, n.String())
	}
	want := []string{
		"tag: Big (LINT): the Logix target's subset has no LINT, so a write-back does not create it",
		"tag: Grid (DINT[2 3]): no nautilus type, so the tag file documents it untyped and a write-back does not create it",
		"tag: Msg (MESSAGE): no nautilus type, so the tag file documents it untyped and a write-back does not create it",
		"tag: Spare (TIMER): no nautilus type, so the tag file documents it untyped and a write-back does not create it",
	}
	if strings.Join(notes, "\n") != strings.Join(want, "\n") {
		t.Errorf("notes:\n  %s\nwant\n  %s", strings.Join(notes, "\n  "), strings.Join(want, "\n  "))
	}
	m := string(p.Files["nautilus.yaml"])
	if !strings.Contains(m, "    all-tags: true\n    controller-scoped:\n      - { name: Delay }\n") {
		t.Errorf("nautilus.yaml:\n%s", m)
	}
	if ld := string(p.Files["Main.ld"]); !strings.Contains(ld, "Delay : TON; (* controller-scoped in the export, and written back there") {
		t.Errorf("Main.ld:\n%s", ld)
	}
}
