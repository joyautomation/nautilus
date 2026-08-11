package tagfile

import (
	"strings"
	"testing"
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
