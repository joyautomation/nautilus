package project

import (
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/joyautomation/nautilus/runtime"
)

func subTags() []runtime.TagDef {
	return []runtime.TagDef{
		runtime.Input("LS_High"), runtime.Input("LS_Low"), runtime.Input("TT_101"), runtime.Input("Pump"),
		runtime.Setpoint("SP", 1.0), runtime.Output("Valve"),
	}
}

// What a rule resolves to, against a table written by hand from #707: the
// first rule that matches an input decides it; the rule's keys beat the
// block's defaults beat the built-ins; a glob is case-insensitive; a dotted
// match names one member of each matching tag.
func TestResolveSubstitutes(t *testing.T) {
	yes := true
	cfg := &SubstitutesConfig{
		Defaults: SubstituteDefaults{Max: Duration(2 * time.Hour), Role: "operator", AckOnStart: &yes},
		Rules: []SubstituteRule{
			{Match: "LS_High", Substitute: true, Max: Duration(10 * time.Minute)}, // a specific rule first
			{Match: "ls_*", Substitute: false},                                    // the glob decides the rest
			{Match: "TT_101", Substitute: 21.5, Max: Duration(30 * time.Minute), Role: "supervisor", OperatorValue: &yes},
			{Match: "Pump.Speed", Substitute: 0.0},
		},
	}
	got, err := resolveSubstitutes(cfg, subTags())
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		addr, role string
		max        time.Duration
		ack, op    bool
		value      any
		rule       int
	}
	want := []row{
		{"LS_High", "operator", 10 * time.Minute, true, false, true, 0}, // rule 0 won, not the glob after it
		{"LS_Low", "operator", 2 * time.Hour, true, false, false, 1},
		{"TT_101", "supervisor", 30 * time.Minute, true, true, 21.5, 2},
		{"Pump.Speed", "operator", 2 * time.Hour, true, false, 0.0, 3},
	}
	if len(got) != len(want) {
		t.Fatalf("resolved %d specs: %+v; want %d", len(got), got, len(want))
	}
	for i, w := range want {
		g := got[i]
		if g.Addr != w.addr || g.Role != w.role || g.Max != w.max || g.AckOnStart != w.ack || g.OperatorValue != w.op || g.Value != w.value || g.Rule != w.rule {
			t.Errorf("spec %d = %+v; want %+v", i, g, w)
		}
	}
	if g := got[3]; g.Root != "Pump" {
		t.Errorf("a member rule's root = %q; want Pump", g.Root)
	}
	if roots := substitutableRoots(got); strings.Join(roots, ",") != "LS_High,LS_Low,TT_101,Pump" {
		t.Errorf("substitutable roots = %v", roots)
	}
	// Built-ins when nothing says.
	got, err = resolveSubstitutes(&SubstitutesConfig{Rules: []SubstituteRule{{Match: "TT_101", Substitute: 1.0}}}, subTags())
	if err != nil || got[0].Max != DefaultSubstituteMax || got[0].Role != DefaultSubstituteRole || got[0].AckOnStart || got[0].OperatorValue {
		t.Errorf("built-in defaults: %+v, %v", got, err)
	}
}

// Everything that could never work is refused at load, naming the rule.
func TestResolveSubstitutesRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		cfg  SubstitutesConfig
		want string
	}{
		"no rules":                           {SubstitutesConfig{}, "at least one rule"},
		"an empty match":                     {SubstitutesConfig{Rules: []SubstituteRule{{Substitute: 1}}}, "match: is required"},
		"no value":                           {SubstitutesConfig{Rules: []SubstituteRule{{Match: "TT_101"}}}, "substitute: is required"},
		"matches nothing":                    {SubstitutesConfig{Rules: []SubstituteRule{{Match: "XX_*", Substitute: 1}}}, "matches no input tag"},
		"matches an output":                  {SubstitutesConfig{Rules: []SubstituteRule{{Match: "Valve", Substitute: 1}}}, "Valve, which is not an input"},
		"matches a setpoint":                 {SubstitutesConfig{Rules: []SubstituteRule{{Match: "SP", Substitute: 1}}}, "SP, which is not an input"},
		"the same match twice":               {SubstitutesConfig{Rules: []SubstituteRule{{Match: "TT_101", Substitute: 1}, {Match: "tt_101", Substitute: 2}}}, "repeats rules[0]"},
		"max too short":                      {SubstitutesConfig{Rules: []SubstituteRule{{Match: "TT_101", Substitute: 1, Max: Duration(30 * time.Second)}}}, "outside 1m0s to 168h0m0s"},
		"max too long":                       {SubstitutesConfig{Rules: []SubstituteRule{{Match: "TT_101", Substitute: 1, Max: Duration(169 * time.Hour)}}}, "outside"},
		"defaults too long":                  {SubstitutesConfig{Defaults: SubstituteDefaults{Max: Duration(200 * time.Hour)}, Rules: []SubstituteRule{{Match: "TT_101", Substitute: 1}}}, "substitutes.defaults"},
		"a rule shadowed by an earlier glob": {SubstitutesConfig{Rules: []SubstituteRule{{Match: "LS_*", Substitute: 1}, {Match: "LS_High", Substitute: 2}}}, "shadowed by rules[0]"},
		"a bad glob":                         {SubstitutesConfig{Rules: []SubstituteRule{{Match: "[", Substitute: 1}}}, "match"},
	} {
		cfg := tc.cfg
		_, err := resolveSubstitutes(&cfg, subTags())
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v; want an error containing %q", name, err, tc.want)
		}
	}
}

const subLoadManifest = `
name: subs
tasks:
  - program: main.st
tags:
  - {name: Level, role: input, type: REAL, init: 0.0}
  - {name: Out, role: output, init: 0.0}
substitutes:
  rules:
    - {match: Level, substitute: %s}
`

func loadSubs(t *testing.T, value string) (*Project, error) {
	t.Helper()
	return Load(fstest.MapFS{
		"nautilus.yaml": {Data: []byte(strings.Replace(subLoadManifest, "%s", value, 1))},
		"main.st":       {Data: []byte("PROGRAM Main\nVAR_EXTERNAL Level : REAL; Out : REAL; END_VAR\nOut := Level;\nEND_PROGRAM\n")},
	}, "")
}

// Load hands the resolved inputs to the runtime, which then refuses any
// other and answers on the companion; the project carries the specs.
func TestLoadWiresSubstitutes(t *testing.T) {
	p, err := loadSubs(t, "90.0")
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Runtime.Substitutable; len(got) != 1 || got[0] != "Level" {
		t.Fatalf("Options.Substitutable = %v; want [Level]", got)
	}
	if s := p.Substitutes(); len(s) != 1 || s[0].Addr != "Level" || s[0].Value != 90.0 {
		t.Fatalf("Substitutes() = %+v", s)
	}
	rt, err := runtime.New(p.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	if rt.Tags().TypeOf("Level__Substituted") == nil && !strings.Contains(strings.Join(namesOf(rt), ","), "Level__Substituted") {
		t.Errorf("the runtime declares no Level__Substituted: %v", namesOf(rt))
	}
	if errs := p.CheckSubstitutes(rt); len(errs) != 0 {
		t.Errorf("CheckSubstitutes on a good value: %v", errs)
	}
}

func namesOf(rt *runtime.Runtime) []string {
	var out []string
	for n := range rt.Tags().All() {
		out = append(out, n)
	}
	return out
}

// A value the tag's type cannot hold is `naut check`'s error, held to the
// very rule a write is (a REAL takes no string).
func TestCheckSubstitutesRefusesAValueTheTypeCannotHold(t *testing.T) {
	p, err := loadSubs(t, `"high"`)
	if err != nil {
		t.Fatal(err)
	}
	rt, err := runtime.New(p.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	errs := p.CheckSubstitutes(rt)
	if len(errs) != 1 || !strings.Contains(errs[0], "substitutes.rules[0]") || !strings.Contains(errs[0], "Level") {
		t.Fatalf("CheckSubstitutes = %v; want one error naming rule 0 and Level", errs)
	}
}

// The manifest reaches the loader's strict decode: a typo is an error.
func TestSubstitutesKeysAreChecked(t *testing.T) {
	_, err := Load(fstest.MapFS{
		"nautilus.yaml": {Data: []byte("tasks:\n  - program: main.st\ntags:\n  - {name: Level, role: input, type: REAL, init: 0.0}\nsubstitutes:\n  rules:\n    - {match: Level, substitue: 1.0}\n")},
		"main.st":       {Data: []byte("PROGRAM Main\nVAR_EXTERNAL Level : REAL; END_VAR\nEND_PROGRAM\n")},
	}, "")
	if err == nil || !strings.Contains(err.Error(), "substitue") {
		t.Fatalf("a typo'd key: %v", err)
	}
}
