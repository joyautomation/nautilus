package alarm

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The master columns load from an alarm file, unquoted date and all, and a
// rule copies them to every definition it generates.
func TestRationalizationLoadsOnDefsAndRules(t *testing.T) {
	f, err := Load(strings.NewReader(`
- tag: PIT101_HH
  consequence: Line over-pressure; relief lifts to the flare
  response: "Stop P101, close {XV101} if still open"
  rationalization: { state: approved, by: alice, at: 2026-11-20 }
- match: { type: AnalogInput, member: HH }
  consequence: Process upset
  response: Check the transmitter, then the process
  rationalization: { state: reviewed, by: bob, at: "2026-11-19" }
`))
	if err != nil {
		t.Fatal(err)
	}
	d := f.Defs[0]
	if d.Consequence != "Line over-pressure; relief lifts to the flare" ||
		d.Response != "Stop P101, close {XV101} if still open" {
		t.Errorf("def columns = %q / %q", d.Consequence, d.Response)
	}
	want := Rationalization{State: RationalizationApproved, By: "alice", At: "2026-11-20"}
	if d.Rationalization != want {
		t.Errorf("def rationalization = %+v, want %+v", d.Rationalization, want)
	}

	defs, err := Expand(f.Rules, nil, fleet())
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) == 0 {
		t.Fatal("rule generated nothing")
	}
	for _, g := range defs {
		if g.Consequence != "Process upset" || g.Response != "Check the transmitter, then the process" ||
			g.Rationalization != (Rationalization{State: RationalizationReviewed, By: "bob", At: "2026-11-19"}) {
			t.Fatalf("%s: rule columns not copied: %+v", g.ID, g)
		}
	}
}

// A block that would read as reviewed but is not signed, an unknown state,
// a bad day or a misspelled key fails the load, so naut check reports it.
func TestRationalizationRefusesAtLoad(t *testing.T) {
	for name, block := range map[string]string{
		"no by":         "{ state: approved, at: 2026-11-20 }",
		"no at":         "{ state: reviewed, by: alice }",
		"blank by":      "{ state: reviewed, by: '  ', at: 2026-11-20 }",
		"unknown state": "{ state: signed-off, by: alice, at: 2026-11-20 }",
		"bad day":       "{ state: approved, by: alice, at: 20/11/2026 }",
		"typo key":      "{ state: approved, by: alice, at: 2026-11-20, reviwer: bob }",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Load(strings.NewReader("- tag: T\n  rationalization: " + block + "\n"))
			if err == nil {
				t.Fatalf("loaded %s", block)
			}
		})
	}
	// Pending needs nobody's name.
	if _, err := Load(strings.NewReader("- tag: T\n  rationalization: { state: pending }\n")); err != nil {
		t.Fatal(err)
	}
}

// `naut alarms list -o yaml` promises YAML a manifest could take back
// verbatim; the master columns must survive that trip, and a definition
// with none prints none.
func TestRationalizationRoundTripsThroughListYAML(t *testing.T) {
	in := Def{
		ID: "A", Tag: "T", Name: "A", Priority: High,
		AckRequired: true, AutoClear: true, Shelvable: true,
		Consequence: "c: with a colon", Response: "line one\nline two",
		Rationalization: Rationalization{State: RationalizationApproved, By: "alice", At: "2026-11-20"},
	}
	b, err := yaml.Marshal([]Def{in, {ID: "B", Tag: "U", Name: "B", Priority: Low}})
	if err != nil {
		t.Fatal(err)
	}
	f, err := Load(strings.NewReader(string(b)))
	if err != nil {
		t.Fatalf("reload: %v\n%s", err, b)
	}
	got := f.Defs[0]
	if got.Consequence != in.Consequence || got.Response != in.Response || got.Rationalization != in.Rationalization {
		t.Errorf("round trip lost the columns:\n%s\ngot %+v", b, got)
	}
	if strings.Count(string(b), "rationalization:") != 1 || strings.Count(string(b), "consequence:") != 1 {
		t.Errorf("a definition with no master columns printed them:\n%s", b)
	}
}
