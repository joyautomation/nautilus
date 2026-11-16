package tagfile

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Sorted output is what makes the review model work: a regenerated file must
// diff against the last one, showing what changed upstream rather than
// reshuffling and burying it.
func TestRenderSorts(t *testing.T) {
	raw, err := Render(nil, []Tag{
		{Name: "Zeta", Role: "input"},
		{Name: "Alpha", Role: "input"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(raw), "- { name: Alpha") {
		t.Errorf("output is not sorted:\n%s", raw)
	}
}

// Whole numbers keep a decimal point. YAML decodes 0 as int and 0.0 as
// float64, and the tag store distinguishes DINT from REAL — emitting "0" for
// a REAL seed would silently retype the tag.
func TestRenderKeepsFloatsFloat(t *testing.T) {
	raw, err := Render(nil, []Tag{{Name: "T", Role: "state", Init: 0.0}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "init: 0.0") {
		t.Errorf("a whole float lost its decimal point:\n%s", raw)
	}
}

// Units and descriptions are full of characters that end a YAML flow mapping
// early — commas, braces, colons. Quoting is not optional here.
func TestRenderQuotesText(t *testing.T) {
	raw, err := Render(nil, []Tag{
		{Name: "T", Role: "input", Unit: "L/s", Desc: `Pump 1, "main" feed: high`},
	})
	if err != nil {
		t.Fatal(err)
	}
	out := string(raw)
	if !strings.Contains(out, `unit: "L/s"`) {
		t.Errorf("unit not quoted:\n%s", out)
	}
	if !strings.Contains(out, `\"main\"`) {
		t.Errorf("embedded quotes not escaped:\n%s", out)
	}
}

// Empty fields are omitted so a generator supplies only what its source
// knows — an importer that cannot read descriptions leaves them out rather
// than inventing blanks that would later look like real (empty) values.
func TestRenderOmitsEmptyFields(t *testing.T) {
	raw, err := Render(nil, []Tag{{Name: "T", Role: "input"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(raw)); got != "- { name: T, role: input }" {
		t.Errorf("empty fields were emitted: %q", got)
	}
}

// A generator that emits one name twice would produce a file the loader
// rejects with a message about two SOURCES. Catching it here names the
// generator, which is where the fix is.
func TestRenderRejectsDuplicates(t *testing.T) {
	_, err := Render(nil, []Tag{{Name: "T", Role: "input"}, {Name: "T", Role: "state"}})
	if err == nil {
		t.Fatal("a duplicate name was rendered")
	}
	if !strings.Contains(err.Error(), "once") {
		t.Errorf("error should explain the once-only rule: %v", err)
	}
}

// A struct-typed tag's per-member init renders as a flow-style mapping, not
// as `%v`-formatted Go syntax — the shape ir.SeedFromInit reads back, so a
// generator can emit `init: { RAWMIN: 6553.0, HHSP: 2800.0 }` and it loads.
func TestRenderStructInitAsFlowMapping(t *testing.T) {
	raw, err := Render(nil, []Tag{{
		Name: "T", Role: "state", Type: "AnalogInput",
		Init: map[string]any{"RAWMIN": 6553.0, "ALMDLY": 5},
	}})
	if err != nil {
		t.Fatal(err)
	}
	out := strings.TrimSpace(string(raw))
	want := "- { name: T, role: state, type: AnalogInput, init: { ALMDLY: 5, RAWMIN: 6553.0 } }"
	if out != want {
		t.Errorf("got:\n%s\nwant:\n%s", out, want)
	}
}

// Nested struct members render as nested flow mappings, keys sorted at
// every level — the same shape a manifest author would hand-write.
func TestRenderNestedStructInit(t *testing.T) {
	raw, err := Render(nil, []Tag{{
		Name: "T", Role: "state", Type: "Motor1Speed",
		Init: map[string]any{
			"STRTTMRSP": 30,
			"LVL":       map[string]any{"HSP": 60.0, "LSP": 40.0},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	out := strings.TrimSpace(string(raw))
	want := "- { name: T, role: state, type: Motor1Speed, init: " +
		"{ LVL: { HSP: 60.0, LSP: 40.0 }, STRTTMRSP: 30 } }"
	if out != want {
		t.Errorf("got:\n%s\nwant:\n%s", out, want)
	}
}

func TestRenderHeader(t *testing.T) {
	raw, err := Render([]string{"line one", "", "line two"}, []Tag{{Name: "T", Role: "input"}})
	if err != nil {
		t.Fatal(err)
	}
	want := "# line one\n#\n# line two\n"
	if !strings.HasPrefix(string(raw), want) {
		t.Errorf("header not rendered as comments:\n%s", raw)
	}
}

// An array type's brackets open a flow sequence inside the tag's flow
// mapping: left bare, the whole file fails to parse (`naut logix import` of
// any export with an array tag produced a project `naut run` refused). The
// output must read back as YAML, with the type intact; a plain type name
// stays bare.
func TestRenderArrayTypeParses(t *testing.T) {
	raw, err := Render(nil, []Tag{
		{Name: "Trend", Role: "input", Type: "ARRAY [0..3] OF REAL"},
		{Name: "Grid", Role: "input", Type: "ARRAY [0..1, 0..2] OF DINT"},
		{Name: "P101", Role: "input", Type: "Analog_Input"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got []map[string]any
	if err := yaml.Unmarshal(raw, &got); err != nil {
		t.Fatalf("output does not parse: %v\n%s", err, raw)
	}
	want := map[string]string{"Trend": "ARRAY [0..3] OF REAL", "Grid": "ARRAY [0..1, 0..2] OF DINT", "P101": "Analog_Input"}
	for _, g := range got {
		if g["type"] != want[g["name"].(string)] {
			t.Errorf("%v: type %q, want %q", g["name"], g["type"], want[g["name"].(string)])
		}
	}
	if !strings.Contains(string(raw), "type: Analog_Input }") {
		t.Errorf("a plain type name was quoted:\n%s", raw)
	}
}
