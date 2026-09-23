package l5x

import (
	"strings"
	"testing"
)

func tagsByName(t *testing.T, f *File, opts TagsOptions) map[string]string {
	t.Helper()
	raw, err := TagsYAML(f, opts)
	if err != nil {
		t.Fatalf("TagsYAML: %v", err)
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "- { name: ") {
			continue
		}
		rest := strings.TrimPrefix(line, "- { name: ")
		name, _, _ := strings.Cut(rest, ",")
		out[name] = line
	}
	return out
}

// The point of the whole exercise: a live CIP browse cannot see tag
// documentation, because Logix keeps it in the offline project file — so
// `naut eip import` leaves desc: empty (eip/codegen/tags.go). The same
// tags read from an L5X arrive documented.
func TestTagsCarryDescriptions(t *testing.T) {
	f := load(t, "variety.L5X")
	known := map[string]bool{"Analog_Input": true, "Limits": true, "TIMER": true}
	got := tagsByName(t, f, TagsOptions{KnownTypes: known})

	if line := got["StartPB"]; !strings.Contains(line, `desc: "HS-101 start pushbutton"`) {
		t.Errorf("StartPB: %s", line)
	}
	if line := got["LocalStart"]; !strings.Contains(line, `desc: "Field start contact, rack slot 1 point 3"`) {
		t.Errorf("an alias tag's description is documentation too: %s", line)
	}
	if line := got["RunHours"]; strings.Contains(line, "desc:") {
		t.Errorf("an undocumented tag must stay undocumented, not be invented: %s", line)
	}
}

func TestTagsSeedInitialValues(t *testing.T) {
	f := load(t, "variety.L5X")
	known := map[string]bool{"Analog_Input": true, "Limits": true, "TIMER": true}
	got := tagsByName(t, f, TagsOptions{KnownTypes: known})

	if line := got["RunHours"]; !strings.Contains(line, "init: 1274") {
		t.Errorf("DINT init: %s", line)
	}
	if line := got["StartPB"]; !strings.Contains(line, "init: false") {
		t.Errorf("BOOL init: %s", line)
	}
	// A structure seeds per member, nested members included — the shape
	// tagfile's init mapping was built for.
	line := got["P101_Level"]
	for _, want := range []string{
		"type: Analog_Input",
		"Alarms: { Hi: 85.0, HiHi: 90.0, Lo: 10.0 }",
		`Label: "Wet Well"`,
		"retain_: false", // keyed the way the generated type declares it
	} {
		if !strings.Contains(line, want) {
			t.Errorf("P101_Level missing %q: %s", want, line)
		}
	}
	if line := got["DwellTmr"]; !strings.Contains(line, "type: TIMER") || !strings.Contains(line, "PRE: 30000") {
		t.Errorf("a predefined-type tag: %s", line)
	}
}

func TestTagsOmitWhatItCannotExpress(t *testing.T) {
	f := load(t, "variety.L5X")
	known := map[string]bool{"Analog_Input": true, "Limits": true, "TIMER": true}
	got := tagsByName(t, f, TagsOptions{KnownTypes: known})

	// An array's shape comes from the driver; a tag file has no syntax
	// for one, so it is declared untyped rather than wrongly.
	if line := got["Trend"]; strings.Contains(line, "type:") || strings.Contains(line, "init:") {
		t.Errorf("array tag: %s", line)
	}
	// MESSAGE has no public shape, so no generated type declares it —
	// pointing type: at it would name something that does not exist.
	if line := got["Handle"]; strings.Contains(line, "type:") {
		t.Errorf("unrenderable type: %s", line)
	}
	// A Constant is a literal the controller will not let anything write.
	if _, ok := got["MaxStarts"]; ok {
		t.Error("a Constant tag should not be bound by default")
	}
	if got := tagsByName(t, f, TagsOptions{KnownTypes: known, Constants: true}); got["MaxStarts"] == "" {
		t.Error("Constants: true should include it")
	}
}

func TestTagsScopes(t *testing.T) {
	f := load(t, "variety.L5X")
	if got := tagsByName(t, f, TagsOptions{}); got["MainProgram_Scratch"] != "" {
		t.Error("controller scope should not reach into a program")
	}
	got := tagsByName(t, f, TagsOptions{Scope: "*"})
	if got["MainProgram_Scratch"] == "" {
		t.Errorf("program tag missing from scope \"*\": %v", got)
	}
	if got["StartPB"] == "" {
		t.Error("scope \"*\" should still carry controller tags")
	}
	if got := tagsByName(t, f, TagsOptions{Scope: "MainProgram"}); got["StartPB"] != "" {
		t.Error("a named program scope should not carry controller tags")
	}
	if _, err := TagsYAML(f, TagsOptions{Scope: "NoSuchProgram"}); err == nil {
		t.Error("an unknown scope should be an error, not an empty file")
	}
}

func TestTagsSkip(t *testing.T) {
	f := load(t, "variety.L5X")
	got := tagsByName(t, f, TagsOptions{Skip: []string{"P101_*"}})
	if got["P101_Level"] != "" {
		t.Error("skip pattern did not exclude the tag")
	}
	if got["StartPB"] == "" {
		t.Error("skip pattern excluded too much")
	}
}
