package runtime

import (
	"testing"

	"github.com/joyautomation/nautilus/lang/ir"
	nio "github.com/joyautomation/nautilus/io"
)

// SubstituteCovering (#715 PR 5): a path reads a substituted signal when
// the substitute is on it or on a member it is in; never by string
// prefix, never through a sibling member, and names compare as names do.
func TestSubstituteCoveringMatchesSegmentsNotPrefixes(t *testing.T) {
	const lib = "TYPE Pump : STRUCT Run : BOOL; Speed : REAL; Seal : BOOL; END_STRUCT; END_TYPE"
	fd := &fieldDriver{in: nio.Values{"LS_10": false, "LS_101": false}}
	rt, err := New(Options{
		Program:   "PROGRAM M\nEND_PROGRAM",
		Libraries: []string{lib}, Driver: fd,
		Tags:          []TagDef{Input("LS_10"), Input("LS_101"), Typed("P1", RoleInput, "Pump")},
		Substitutable: []string{"LS_10", "LS_101", "P1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	pt := rt.tags.types[ir.NameKey("P1")]
	fd.set("P1", ir.Zero(pt))
	rt.Scan()
	tags := rt.Tags()
	if _, ok := tags.SubstituteCovering("LS_10"); ok {
		t.Fatal("covered with nothing substituted")
	}
	sub(t, rt, "LS_10", true)
	sub(t, rt, "P1.Speed", 50.0)
	for _, c := range []struct {
		path, addr string
		ok         bool
	}{
		{"LS_10", "LS_10", true},
		{"ls_10", "LS_10", true}, // a name, compared as names are
		{"LS_101", "", false},    // not a prefix
		{"P1.Speed", "P1.Speed", true},
		{"p1.speed", "P1.Speed", true},
		{"P1.Seal", "", false}, // a sibling member
		{"P1", "", false},      // the whole tag is not substituted
		{"Nope.X", "", false},
	} {
		addr, ok := tags.SubstituteCovering(c.path)
		if ok != c.ok || addr != c.addr {
			t.Errorf("SubstituteCovering(%q) = %q, %v; want %q, %v", c.path, addr, ok, c.addr, c.ok)
		}
	}
	sub(t, rt, "P1", map[string]any{"Run": true, "Speed": 1.0, "Seal": false})
	if addr, ok := tags.SubstituteCovering("P1.Seal"); !ok || addr != "P1" {
		t.Errorf("a member of a whole-tag substitute: %q, %v; want P1, true", addr, ok)
	}
}
