package l5x

import (
	"os"
	"strings"
	"testing"
)

// Measured on ECHO1 (docs/design/logix-target.md §11): the same unchanged
// ACD, exported twice through the SDK, produced two files differing on one
// line of 13,194 — ExportDate. Pinning it is the whole of what drift
// detection needs, and this is that case in miniature.
func TestNormalizePinsTheVolatileAttributes(t *testing.T) {
	const header = `<RSLogix5000Content SchemaRevision="1.0" TargetName="P" ExportDate="%s">
<Controller Name="P" ProjectCreationDate="%s" LastModifiedDate="%s" ProjectSN="16#0000_0001" DataExchangeId="{AAAA-1}">
<Tag Name="SP"><Data Format="L5K">
<![CDATA[8.5e+001]]>
</Data>
<Data Format="Decorated"><DataValue Value="85.0"/></Data></Tag>
</Controller></RSLogix5000Content>`
	first := []byte(strings.NewReplacer("%s", "Thu Sep 17 08:14:03 2026").Replace(header))
	second := []byte(strings.NewReplacer("%s", "Fri Sep 18 11:02:41 2026").Replace(header))

	if !Equivalent(first, second, NormalizeOptions{}) {
		t.Errorf("two exports of unchanged code should normalize equal:\n%s\n%s",
			Normalize(first, NormalizeOptions{}), Normalize(second, NormalizeOptions{}))
	}
	got := string(Normalize(first, NormalizeOptions{}))
	for _, want := range []string{
		`ExportDate="` + Pinned + `"`,
		`LastModifiedDate="` + Pinned + `"`,
		`ProjectCreationDate="` + Pinned + `"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in:\n%s", want, got)
		}
	}
	// Identity is not noise. DataExchangeId and ProjectSN measured stable
	// across a re-export, so pinning them by default would hide a project
	// actually being replaced.
	if !strings.Contains(got, `ProjectSN="16#0000_0001"`) || !strings.Contains(got, `DataExchangeId="{AAAA-1}"`) {
		t.Errorf("identity attributes should survive by default:\n%s", got)
	}
	got = string(Normalize(first, NormalizeOptions{PinIDs: true}))
	if strings.Contains(got, "{AAAA-1}") || strings.Contains(got, "16#0000_0001") {
		t.Errorf("PinIDs should pin both:\n%s", got)
	}
	// Dropping the redundant L5K copy of every value leaves the typed one.
	got = string(Normalize(first, NormalizeOptions{DropL5K: true}))
	if strings.Contains(got, "L5K") || !strings.Contains(got, `Format="Decorated"`) {
		t.Errorf("DropL5K:\n%s", got)
	}
}

// The reviewable-diff claim, on real exports: two Studio 5000 exports of
// the same project differing by one setpoint. They must NOT normalize
// equal — a normalizer that swallowed a real change would be worse than
// none — and the difference must stay small enough to read.
func TestNormalizeKeepsARealChange(t *testing.T) {
	a, err := os.ReadFile("testdata/demoline.L5X")
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile("testdata/demoline.v80.L5X")
	if err != nil {
		t.Fatal(err)
	}
	if !Equivalent(a, a, NormalizeOptions{}) {
		t.Error("an export must normalize equal to itself")
	}
	if Equivalent(a, b, NormalizeOptions{}) {
		t.Fatal("a changed setpoint must survive normalization")
	}
	// 85.0 → 80.0 shows up twice, because the value is carried twice.
	if n := changedLines(string(Normalize(a, NormalizeOptions{})), string(Normalize(b, NormalizeOptions{}))); n != 2 {
		t.Errorf("changed lines = %d, want 2 (the L5K copy and the Decorated one)", n)
	}
	// Collapsing the redundant copy halves it — the §11 lever, measured.
	opts := NormalizeOptions{DropL5K: true}
	if n := changedLines(string(Normalize(a, opts)), string(Normalize(b, opts))); n != 1 {
		t.Errorf("changed lines with DropL5K = %d, want 1", n)
	}
}

// changedLines counts the lines that differ position-for-position. The
// fixtures differ only in place, never in length, which is itself the
// finding: L5X is far more diff-friendly than its reputation.
func changedLines(a, b string) int {
	la, lb := strings.Split(a, "\n"), strings.Split(b, "\n")
	if len(la) != len(lb) {
		return -1
	}
	n := 0
	for i := range la {
		if la[i] != lb[i] {
			n++
		}
	}
	return n
}

func TestNormalizeIsIdempotent(t *testing.T) {
	raw, err := os.ReadFile("testdata/DemoProgram.L5X")
	if err != nil {
		t.Fatal(err)
	}
	once := Normalize(raw, NormalizeOptions{PinIDs: true, DropL5K: true})
	if twice := Normalize(once, NormalizeOptions{PinIDs: true, DropL5K: true}); string(once) != string(twice) {
		t.Error("normalizing a normalized export must change nothing")
	}
	// And the result is still an L5X.
	if _, err := Parse(once); err != nil {
		t.Errorf("normalized export no longer parses: %v", err)
	}
}
