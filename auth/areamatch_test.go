package auth

import (
	"encoding/json"
	"os"
	"path"
	"strings"
	"testing"
)

// The HMI kit greys out a control the way the controller would refuse it
// only if both match an area's tag pattern the same way. One table,
// hmi/tests/areamatch.json, is checked here against the controller's own
// matching (a pattern lowercased as Compile stores it, the target cut by
// tagOf, path.Match) and by the kit's areaMatch in hmi/tests/areas.test.ts.
func TestAreaMatchFixtureIsTheControllers(t *testing.T) {
	raw, err := os.ReadFile("../hmi/tests/areamatch.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Cases []struct {
			Pattern, Target string
			Want            bool
		}
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Cases) < 30 {
		t.Fatalf("%d cases: the table was cut", len(f.Cases))
	}
	for _, c := range f.Cases {
		got, _ := path.Match(strings.ToLower(c.Pattern), tagOf(c.Target))
		if got != c.Want {
			t.Errorf("pattern %q target %q: the controller says %v, the table %v", c.Pattern, c.Target, got, c.Want)
		}
	}
	// And Can agrees, for every pattern Compile accepts.
	for _, c := range f.Cases {
		p, err := Compile(Settings{
			Areas: map[string][]string{"a": {c.Pattern}},
			Users: map[string][]Grant{"u": {{Role: Operator, Areas: []string{"a"}}}},
		})
		// Can also refuses what no pattern decides: an empty tag (".x",
		// "[0]") and a patterned one. The kit's can() refuses those too.
		if err != nil || tagOf(c.Target) == "" || strings.ContainsAny(tagOf(c.Target), "*?[") {
			continue
		}
		if got := p.Can(User("u"), Write, c.Target).Allowed; got != c.Want {
			t.Errorf("Can(write %q) under area %q = %v, the table says %v", c.Target, c.Pattern, got, c.Want)
		}
	}
}
