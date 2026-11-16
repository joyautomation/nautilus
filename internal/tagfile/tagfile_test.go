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

// Names, roles and types come straight from a generator's source (a
// spreadsheet's Name column, an L5X type), so any of them can hold what a
// bare flow-mapping scalar cannot: an array type's brackets (`naut logix
// import` of any export with an array tag produced a project `naut run`
// refused), a comma, a colon, or `null`/`true`, which decode as something
// other than the text. Descriptions can hold CR, tab and other control
// characters. Every one must read back exactly; a plain identifier stays
// bare so regenerated files diff as before.
func TestRenderReadsBack(t *testing.T) {
	in := []Tag{
		{Name: "Trend", Role: "input", Type: "ARRAY [0..3] OF REAL"},
		{Name: "Grid", Role: "input", Type: "ARRAY [0..1, 0..2] OF DINT"},
		{Name: "P101", Role: "input", Type: "Analog_Input"},
		{Name: "Flow, Total", Role: "input"},
		{Name: "Pump 1: Run", Role: "output"},
		{Name: "[spare]", Role: "input"},
		{Name: "NullType", Role: "input", Type: "NULL"},
		{Name: "true", Role: "state"},
		{Name: "Crlf", Role: "input", Desc: "line one\r\nline two\tend\x01"},
	}
	raw, err := Render(nil, in)
	if err != nil {
		t.Fatal(err)
	}
	var got []struct {
		Name string `yaml:"name"`
		Role string `yaml:"role"`
		Type string `yaml:"type"`
		Desc string `yaml:"desc"`
	}
	if err := yaml.Unmarshal(raw, &got); err != nil {
		t.Fatalf("output does not parse: %v\n%s", err, raw)
	}
	byName := map[string]Tag{}
	for _, g := range got {
		byName[g.Name] = Tag{Name: g.Name, Role: g.Role, Type: g.Type, Desc: g.Desc}
	}
	if len(got) != len(in) {
		t.Errorf("read back %d tags, wrote %d:\n%s", len(got), len(in), raw)
	}
	for _, w := range in {
		g, ok := byName[w.Name]
		if !ok {
			t.Errorf("tag %q did not read back:\n%s", w.Name, raw)
			continue
		}
		if g != w {
			t.Errorf("tag %q read back as %+v, wrote %+v", w.Name, g, w)
		}
	}
	out := string(raw)
	for _, bare := range []string{"- { name: P101, role: input, type: Analog_Input }", "- { name: Trend, role: input, type: \"ARRAY [0..3] OF REAL\" }"} {
		if !strings.Contains(out, bare+"\n") {
			t.Errorf("want the line %q:\n%s", bare, out)
		}
	}
}
