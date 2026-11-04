package main

// The brownfield round trip on the public yardstick, at the CLI: the four
// commands the video beat shows (#276), run as a user runs them, on
// examples/batch-skid/line/Line.L5X. logix/importer's
// TestRoundTripPublicYardstick is the library-level form of the rung
// comparison; this pins the user's flow and everything it puts back —
// controller, task, routine, initial values, descriptions — and names
// every difference that remains: none in the logic, none in a tag's
// value or description.

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/l5x"
)

func TestLogixRoundTripYardstickCLI(t *testing.T) {
	src := filepath.Join("..", "..", "examples", "batch-skid", "line", "Line.L5X")
	dir := t.TempDir()
	proj := filepath.Join(dir, "line")
	back := filepath.Join(dir, "back.L5X")

	// 1. naut logix import --project line Line.L5X
	out, code := capture(t, func() int { return runLogix([]string{"import", "--project", proj, src}) })
	if code != 0 || !strings.Contains(out, "1 ladder routine, 1 complete; 5 rungs, 5 carried") {
		t.Fatalf("import: exit %d\n%s", code, out)
	}
	// The project it writes checks clean: no errors, and no warning (the
	// timer the program owns is not also a project tag). The structure
	// warnings (#561) are the exception: they judge the vendor's rungs as
	// imported — this yardstick has an OTE and an OTU on Line_Accept and
	// literal thresholds — and the round trip keeps those rungs verbatim.
	for _, args := range [][]string{{proj}, {"--target", "logix", proj}} {
		out, code = capture(t, func() int { return runCheck(args) })
		if code != 0 || strings.Contains(structureWarnRe.ReplaceAllString(out, ""), ": warning:") {
			t.Errorf("naut check %v: exit %d\n%s", args, code, out)
		}
	}

	// 2. naut logix write -o back.L5X line/MainProgram.ld — no flags: the
	// envelope and the tags' seeds come from the imported nautilus.yaml.
	out, code = capture(t, func() int {
		return runLogix([]string{"write", "-o", back, filepath.Join(proj, "MainProgram.ld")})
	})
	if code != 0 {
		t.Fatalf("write: exit %d\n%s", code, out)
	}

	// 3. naut logix normalize --logic --check back.L5X Line.L5X — the
	// logic is the same: every tag back at its own scope (AcceptTmr at
	// controller scope, #371; Line_BatchId, which no routine names, #370)
	// and every rung byte-identical (#372). A difference fails here
	// (docs/design/logix-authoring.md, "Round trip on the yardstick").
	out, code = capture(t, func() int { return runLogix([]string{"normalize", "--logic", "--check", back, src}) })
	if want := fmt.Sprintf("%s and %s have the same logic\n", src, back); code != 0 || out != want {
		t.Errorf("logic check: exit %d\n%s\nwant\n%s", code, out, want)
	}

	// What the logic comparison does not look at, compared here: the
	// controller and its task, and each tag's initial value and description.
	orig, err := l5x.ParseFile(src)
	if err != nil {
		t.Fatal(err)
	}
	got, err := l5x.ParseFile(back)
	if err != nil {
		t.Fatal(err)
	}
	oc, gc := orig.Controller, got.Controller
	if gc.Name != oc.Name || gc.ProcessorType != oc.ProcessorType || gc.MajorRev != oc.MajorRev || gc.MinorRev != oc.MinorRev {
		t.Errorf("controller %s %s %s.%s, want %s %s %s.%s", gc.Name, gc.ProcessorType, gc.MajorRev, gc.MinorRev,
			oc.Name, oc.ProcessorType, oc.MajorRev, oc.MinorRev)
	}
	// The export claims the target's own Logix Designer release (#373).
	if got.SoftwareRevision != orig.SoftwareRevision {
		t.Errorf("SoftwareRevision %s, want %s", got.SoftwareRevision, orig.SoftwareRevision)
	}
	// Each UDT comes back with its description and its members' (#373).
	if len(gc.DataTypes) != len(oc.DataTypes) {
		t.Errorf("%d data types, want %d", len(gc.DataTypes), len(oc.DataTypes))
	}
	for i := 0; i < len(gc.DataTypes) && i < len(oc.DataTypes); i++ {
		g, o := gc.DataTypes[i], oc.DataTypes[i]
		if g.Name != o.Name || g.Description != o.Description || len(g.Members) != len(o.Members) {
			t.Errorf("data type %s %q (%d members), want %s %q (%d)", g.Name, g.Description, len(g.Members), o.Name, o.Description, len(o.Members))
			continue
		}
		for j := range o.Members {
			if g.Members[j].Name != o.Members[j].Name || g.Members[j].Description != o.Members[j].Description {
				t.Errorf("%s member %s %q, want %s %q", o.Name, g.Members[j].Name, g.Members[j].Description, o.Members[j].Name, o.Members[j].Description)
			}
		}
	}
	if len(gc.Tasks) != 1 || !reflect.DeepEqual(*gc.Tasks[0], *oc.Tasks[0]) {
		t.Errorf("tasks %+v, want %+v", gc.Tasks, oc.Tasks)
	}
	if p := gc.Programs[0]; p.Name != "MainProgram" || p.MainRoutineName != "Receive" || p.Routines[0].Name != "Receive" {
		t.Errorf("program %s main routine %s routine %s, want MainProgram/Receive", p.Name, p.MainRoutineName, p.Routines[0].Name)
	}
	gotTags := map[string]*l5x.Tag{}
	for _, tg := range gc.Tags {
		gotTags[tg.Name] = tg
	}
	for _, p := range gc.Programs {
		for _, tg := range p.Tags {
			gotTags[tg.Name] = tg
		}
	}
	var diffs []string
	for _, o := range oc.Tags {
		g := gotTags[o.Name]
		switch {
		case g == nil:
			diffs = append(diffs, o.Name+": missing")
			continue
		case !reflect.DeepEqual(g.Value, o.Value):
			diffs = append(diffs, fmt.Sprintf("%s: value %v, want %v", o.Name, g.Value, o.Value))
		}
		if g.Description != o.Description {
			diffs = append(diffs, fmt.Sprintf("%s: description %q, want %q", o.Name, g.Description, o.Description))
		}
	}
	sort.Strings(diffs)
	if len(diffs) > 0 {
		t.Errorf("tag differences:\n  %s", strings.Join(diffs, "\n  "))
	}
	// Each written where the export had it: every tag at controller
	// scope, none in the program.
	if n := len(gc.Programs[0].Tags); n != 0 || len(gc.Tags) != len(oc.Tags) {
		t.Errorf("%d controller tags and %d program tags, want %d and 0", len(gc.Tags), n, len(oc.Tags))
	}

	// A flag still overrides the manifest.
	again := filepath.Join(dir, "again.L5X")
	stderr, code := captureStderr(t, func() int {
		return runLogix([]string{"write", "-o", again, "--routine", "Main", "--period", "50", filepath.Join(proj, "MainProgram.ld")})
	})
	if code != 0 {
		t.Fatalf("write with flags: exit %d\n%s", code, stderr)
	}
	// The 1769-L33ER's own Local module, from Logix Designer's export of
	// one, and so no warning (#373): it was the 1756-L85E's before.
	if strings.Contains(stderr, "Local module") {
		t.Errorf("write warned about the Local module:\n%s", stderr)
	}
	if raw, err := os.ReadFile(again); err != nil {
		t.Fatal(err)
	} else if !strings.Contains(string(raw), `<Module Name="Local" CatalogNumber="1769-L33ER" Vendor="1" ProductType="14" ProductCode="107" Major="36" Minor="11"`) ||
		!strings.Contains(string(raw), `<Port Id="1" Address="0" Type="Compact" Upstream="false">`) {
		t.Errorf("the write-back's Local module is not the 1769-L33ER's")
	}
	f, err := l5x.ParseFile(again)
	if err != nil {
		t.Fatal(err)
	}
	if r := f.Controller.Programs[0].Routines[0].Name; r != "Main" || f.Controller.Tasks[0].Type != "PERIODIC" || f.Controller.Tasks[0].RateMs != 50 || f.Controller.Name != "LineController" {
		t.Errorf("flags: routine %s task %+v controller %s", r, *f.Controller.Tasks[0], f.Controller.Name)
	}
}
