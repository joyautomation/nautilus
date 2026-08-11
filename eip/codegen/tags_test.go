package codegen

import (
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/eip"
)

func tagsFixture() eip.Manifest {
	return eip.Manifest{
		Types: []eip.TypeDef{{
			Name:   "Analog_Input",
			Fields: []eip.FieldDef{{Name: "VALUE", Type: "REAL"}, {Name: "LOS", Type: "BOOL"}},
		}},
		Tags: []eip.TagBinding{
			{Name: "Level", Device: "Level", Type: "REAL"},
			{Name: "Cmd", Device: "Cmd", Type: "DINT", Writable: true},
			{Name: "PIT_001", Device: "PIT_001", Type: "Analog_Input"},
			{Name: "Hist", Device: "Hist", Type: "REAL", ArrayLen: 10},
		},
	}
}

func render(t *testing.T, m eip.Manifest, skip ...string) string {
	t.Helper()
	raw, err := TagsYAML(m, "plc1", skip)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// Writable maps to output, everything else to input — the one direction the
// import genuinely knows, so it should not be re-stated by hand.
func TestTagsYAMLDerivesRoles(t *testing.T) {
	out := render(t, tagsFixture())
	for _, want := range []string{
		"- { name: Cmd, role: output }",
		"- { name: Level, role: input }",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

// A UDT binding carries type:, which is the line that replaces the prose
// desc: examples/client60 used to need. Elementary types do not — their
// shape comes from the value the driver delivers.
func TestTagsYAMLEmitsTypeForUDTsOnly(t *testing.T) {
	out := render(t, tagsFixture())
	if !strings.Contains(out, "- { name: PIT_001, role: input, type: Analog_Input }") {
		t.Errorf("UDT binding did not get a type::\n%s", out)
	}
	if strings.Contains(out, "type: REAL") || strings.Contains(out, "type: DINT") {
		t.Errorf("an elementary type leaked into type::\n%s", out)
	}
	// An array's value is an array of the element type, which a UDT name
	// cannot express.
	if strings.Contains(out, "name: Hist, role: input, type") {
		t.Errorf("an array binding got a type::\n%s", out)
	}
}

// Output order is by name so a regenerated file diffs against the last one
// rather than reshuffling — the whole review model depends on it.
func TestTagsYAMLIsSorted(t *testing.T) {
	m := tagsFixture()
	m.Tags[0], m.Tags[3] = m.Tags[3], m.Tags[0]
	a := render(t, m)
	b := render(t, tagsFixture())
	if a != b {
		t.Errorf("binding order changed the output:\n%s\n---\n%s", a, b)
	}
}

// The escape hatch: a tag the project declares by hand is skipped here, so
// it can carry something the import cannot know (an init:) without needing
// an override — which would survive regeneration silently.
func TestTagsYAMLSkip(t *testing.T) {
	out := render(t, tagsFixture(), "Level")
	if strings.Contains(out, "name: Level,") {
		t.Errorf("a skipped tag was still generated:\n%s", out)
	}
	if !strings.Contains(out, "Not generated") || !strings.Contains(out, "Level") {
		t.Errorf("the header does not record what was skipped:\n%s", out)
	}
	if !strings.Contains(out, "name: Cmd,") {
		t.Errorf("skipping one tag dropped another:\n%s", out)
	}
}

// A skip pattern matching nothing is a stale exclusion — usually a tag
// renamed on the controller — which would silently regenerate a tag the
// manifest also declares, and that collision is an error at load.
func TestTagsYAMLRejectsStaleSkip(t *testing.T) {
	_, err := TagsYAML(tagsFixture(), "plc1", []string{"Nonexistent*"})
	if err == nil {
		t.Fatal("a skip pattern matching nothing was accepted")
	}
	if !strings.Contains(err.Error(), "Nonexistent*") {
		t.Errorf("error does not name the stale pattern: %v", err)
	}
}

// The generated file must say it is generated, and where documentation
// belongs — a Logix browse cannot recover descriptions, so a desc: written
// here would be erased on the next import.
func TestTagsYAMLHeader(t *testing.T) {
	out := render(t, tagsFixture())
	for _, want := range []string{"Do not edit", "tag-files:", "tag-meta:"} {
		if !strings.Contains(out, want) {
			t.Errorf("header is missing %q:\n%s", want, out)
		}
	}
}

func TestTagsYAMLRejectsUndeclaredType(t *testing.T) {
	m := tagsFixture()
	m.Tags = append(m.Tags, eip.TagBinding{Name: "X", Device: "X", Type: "Ghost_UDT"})
	if _, err := TagsYAML(m, "plc1", nil); err == nil {
		t.Fatal("a binding naming an undeclared UDT was accepted")
	}
}
