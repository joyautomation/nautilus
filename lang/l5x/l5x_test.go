package l5x

import (
	"os"
	"strings"
	"testing"
)

func load(t *testing.T, name string) *File {
	t.Helper()
	f, err := ParseFile("testdata/" + name)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return f
}

func TestParseControllerExport(t *testing.T) {
	f := load(t, "demoline.L5X")
	if f.TargetType != "Controller" || f.Partial() {
		t.Errorf("TargetType = %q, Partial = %v; want a whole-project export", f.TargetType, f.Partial())
	}
	if f.Controller.Name != "DemoLine" || f.Controller.ProcessorType != "1756-L85E" {
		t.Errorf("controller = %q on %q", f.Controller.Name, f.Controller.ProcessorType)
	}
	if len(f.Controller.Programs) != 1 || f.Controller.Programs[0].MainRoutineName != "MainRoutine" {
		t.Fatalf("programs = %+v", f.Controller.Programs)
	}
	// DemoLine keeps its tags in the program, not on the controller.
	if got := len(f.Controller.Programs[0].Tags); got != 6 {
		t.Errorf("program tags = %d, want 6", got)
	}
	// The one value the v80 sibling differs by, decoded from the
	// Decorated copy rather than the L5K CDATA.
	for _, tag := range f.Controller.Programs[0].Tags {
		if tag.Name != "HiLevelSP" {
			continue
		}
		if tag.Value != 85.0 {
			t.Errorf("HiLevelSP = %#v, want 85.0", tag.Value)
		}
	}
}

// A partial export — TargetType="Program", the shape File → Import takes —
// nests its target under a Controller marked Use="Context". It must parse
// as readily as a whole project, because that is what a per-program
// workflow produces.
func TestParsePartialExport(t *testing.T) {
	f := load(t, "DemoProgram.L5X")
	if !f.Partial() || f.TargetType != "Program" || f.TargetName != "MainProgram" {
		t.Fatalf("TargetType=%q TargetName=%q Partial=%v", f.TargetType, f.TargetName, f.Partial())
	}
	if len(f.Controller.Programs) != 1 {
		t.Fatalf("programs = %d", len(f.Controller.Programs))
	}
	p := f.Controller.Programs[0]
	if len(p.Tags) != 6 || len(p.Routines) != 1 || len(p.Routines[0].Rungs) != 2 {
		t.Fatalf("program %s: %d tags, %d routines", p.Name, len(p.Tags), len(p.Routines))
	}
}

func TestParseRecordsSourceLines(t *testing.T) {
	f := load(t, "variety.L5X")
	r := f.Controller.Programs[0].Routines[0]
	if r.Line != 145 {
		t.Errorf("routine line = %d, want 145", r.Line)
	}
	want := []int{147, 155, 163, 171, 179, 184}
	if len(r.Rungs) != len(want) {
		t.Fatalf("rungs = %d, want %d", len(r.Rungs), len(want))
	}
	for i, rg := range r.Rungs {
		if rg.Line != want[i] {
			t.Errorf("rung %d line = %d, want %d", i, rg.Line, want[i])
		}
	}
}

func TestParseTagDetail(t *testing.T) {
	f := load(t, "variety.L5X")
	byName := map[string]*Tag{}
	for _, tag := range f.Controller.Tags {
		byName[tag.Name] = tag
	}

	// The description is the payload a live CIP browse cannot reach.
	if got := byName["StartPB"].Description; got != "HS-101 start pushbutton" {
		t.Errorf("StartPB desc = %q", got)
	}
	if got := byName["StartPB"].Comments[".0"]; !strings.Contains(got, "bit-level comment") {
		t.Errorf("StartPB operand comment = %q", got)
	}
	if a := byName["LocalStart"]; a.TagType != "Alias" || a.AliasFor != "Local:1:I.Data.3" {
		t.Errorf("alias = %+v", a)
	}
	if !byName["MaxStarts"].Constant {
		t.Error("MaxStarts should be Constant")
	}

	lvl, ok := byName["P101_Level"].Value.(map[string]any)
	if !ok {
		t.Fatalf("P101_Level value = %#v, want a structure", byName["P101_Level"].Value)
	}
	alarms, ok := lvl["Alarms"].(map[string]any)
	if !ok || alarms["Hi"] != 85.0 {
		t.Errorf("nested structure member = %#v", lvl["Alarms"])
	}
	// A Logix STRING is a LEN + SINT-array structure; what it means is
	// the string, $20 escape and all.
	if lvl["Label"] != "Wet Well" {
		t.Errorf("STRING member = %#v, want \"Wet Well\"", lvl["Label"])
	}
	if hist, ok := lvl["History"].([]any); !ok || len(hist) != 2 {
		t.Errorf("array member = %#v", lvl["History"])
	}
	if lvl["Fault"] != false {
		t.Errorf("BIT member = %#v, want false", lvl["Fault"])
	}
	if got := f.Controller.Programs[0].Tags[0].Name; got != "Scratch" {
		t.Errorf("program-scoped tag = %q", got)
	}
}

// An ST routine is captured verbatim rather than modelled — lowering it to
// the lang/st AST is deliberately out of scope — but it must not be lost.
func TestParseSTRoutineText(t *testing.T) {
	f := load(t, "variety.L5X")
	var st *Routine
	for _, r := range f.Controller.Programs[0].Routines {
		if r.Type == "ST" {
			st = r
		}
	}
	if st == nil {
		t.Fatal("no ST routine parsed")
	}
	if !strings.Contains(st.Text, "Scratch := Scratch + 1;") || !strings.Contains(st.Text, "END_IF;") {
		t.Errorf("ST body = %q", st.Text)
	}
}

func TestParseRejectsNonL5X(t *testing.T) {
	if _, err := Parse([]byte(`<?xml version="1.0"?><project/>`)); err == nil {
		t.Error("a non-L5X document should not parse as one")
	}
}

// A truncated export must be an error, not half a project. Drift
// detection compares whole exports; a short read would surface as a very
// large change rather than as the corruption it is.
func TestParseRejectsTruncation(t *testing.T) {
	raw, err := os.ReadFile("testdata/variety.L5X")
	if err != nil {
		t.Fatal(err)
	}
	for _, frac := range []float64{0.3, 0.6, 0.9} {
		cut := raw[:int(float64(len(raw))*frac)]
		if _, err := Parse(cut); err == nil {
			t.Errorf("%.0f%% of an export parsed without error", frac*100)
		}
	}
}

// The fixtures carry a UTF-8 BOM, because Studio 5000 writes one. A reader
// that choked on it would fail on every real export.
func TestParseHandlesBOM(t *testing.T) {
	raw, err := os.ReadFile("testdata/demoline.L5X")
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) < 3 || raw[0] != 0xef || raw[1] != 0xbb || raw[2] != 0xbf {
		t.Fatal("fixture lost its BOM; the test no longer covers what it claims")
	}
	if _, err := Parse(raw); err != nil {
		t.Fatalf("BOM: %v", err)
	}
}
