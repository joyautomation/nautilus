package alarm

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/lang/ir"
)

// structDef builds a UDT with one REAL member (so the expander has
// something it must ignore) followed by the named BOOLs, in order.
func structDef(name string, bools ...string) *ir.StructDef {
	sd := &ir.StructDef{Name: name, FieldIndex: map[string]int{}}
	sd.Fields = append(sd.Fields, ir.StructField{Name: "PV", Type: ir.RealT})
	sd.FieldIndex["PV"] = 0
	for i, m := range bools {
		sd.Fields = append(sd.Fields, ir.StructField{Name: m, Type: ir.BoolT})
		sd.FieldIndex[m] = i + 1
	}
	return sd
}

var (
	analogInput = structDef("AnalogInput", "LOS", "H", "HH", "L", "LL", "OOR")
	motor1Speed = structDef("Motor1Speed", "FAIL1ALM", "FAIL2ALM", "COSALM", "ANYFAULT")
)

// fleet is a miniature of the real shape: two sites, each with a couple of
// analog inputs, a motor, and a standalone security contact.
func fleet() []TagInfo {
	var out []TagInfo
	for _, site := range []string{"RTU9", "RTU12"} {
		for _, eq := range []string{"FIT_001", "PIT_002"} {
			out = append(out, TagInfo{
				Name: site + "_" + eq, TypeName: "AnalogInput", Struct: analogInput,
				Site: site, Area: eq, Desc: site + " " + eq,
			})
		}
		out = append(out, TagInfo{
			Name: site + "_PMP_001", TypeName: "Motor1Speed", Struct: motor1Speed,
			Site: site, Area: "PMP_001", Desc: site + " Pump 1",
		})
		out = append(out, TagInfo{
			Name: site + "_INT_001_YA", Site: site, Area: "INT_001",
			Desc: site + " Intrusion",
		})
	}
	return out
}

// fleetRules is the shape the brief argues for: a handful of rules covering
// the UDT members, plus one glob for the standalone contacts.
func fleetRules() []Rule {
	high, critical := High, Critical
	return []Rule{
		{
			Match: Match{Type: "AnalogInput", Member: "HH"},
			Name:  "{desc} High High", Priority: &high, Class: "process",
			Enable: "{site}__Online",
		},
		{
			Match: Match{Type: "AnalogInput", Member: "LL"},
			Name:  "{desc} Low Low", Priority: &high, Class: "process",
			Enable: "{site}__Online",
		},
		{
			Match: Match{Type: "AnalogInput", Member: "*"},
			Name:  "{desc} {member}", Class: "process",
			Enable: "{site}__Online",
		},
		{
			Match: Match{Type: "Motor1Speed", Member: "*"},
			Name:  "{desc} {member}", Class: "process",
			Enable: "{site}__Online",
		},
		{
			Match: Match{Tag: "*_YA"},
			Name:  "{desc}", Priority: &critical, Class: "security",
		},
	}
}

// TestExpandCoversTheFleetFromFewRules is the model's central claim: a
// handful of rules produce an instance per (tag, member) across the fleet.
func TestExpandCoversTheFleetFromFewRules(t *testing.T) {
	rules := fleetRules()
	defs, err := Expand(rules, nil, fleet())
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	// 2 sites × (2 analog × 6 bools + 1 motor × 4 bools + 1 flat) = 34.
	if len(defs) != 34 {
		t.Fatalf("Expand produced %d defs, want 34", len(defs))
	}
	if len(rules) != 5 {
		t.Fatalf("guard: this test is about few rules producing many defs; rules = %d", len(rules))
	}

	byID := map[string]Def{}
	for _, d := range defs {
		byID[d.ID] = d
	}
	hh, ok := byID["RTU9_FIT_001.HH"]
	if !ok {
		t.Fatalf("no RTU9_FIT_001.HH; got %v", ids(defs))
	}
	if hh.Name != "RTU9 FIT_001 High High" {
		t.Errorf("name = %q, want %q", hh.Name, "RTU9 FIT_001 High High")
	}
	if hh.Priority != High {
		t.Errorf("priority = %v, want high", hh.Priority)
	}
	if hh.Enable != "RTU9__Online" {
		t.Errorf("enable = %q, want RTU9__Online", hh.Enable)
	}
	if hh.Site != "RTU9" || hh.Area != "FIT_001" {
		t.Errorf("site/area = %q/%q, want RTU9/FIT_001", hh.Site, hh.Area)
	}
	if hh.Tag != "RTU9_FIT_001.HH" {
		t.Errorf("tag = %q, want the member path", hh.Tag)
	}

	// First match wins: the HH rule claimed HH before the catch-all, but
	// the catch-all still claims H at the default priority.
	if got := byID["RTU9_FIT_001.H"].Priority; got != Medium {
		t.Errorf("H priority = %v, want the medium default", got)
	}
	// The glob rule claims the flat contact and nothing else.
	ya, ok := byID["RTU9_INT_001_YA"]
	if !ok {
		t.Fatalf("no RTU9_INT_001_YA")
	}
	if ya.Priority != Critical || ya.Class != "security" || ya.Enable != "" {
		t.Errorf("flat contact = %+v, want critical/security/no enable", ya)
	}
	if ya.Name != "RTU9 Intrusion" {
		t.Errorf("flat contact name = %q", ya.Name)
	}
}

// TestExpandIsDeterministic pins the property that makes a generated set
// reviewable: the same inputs must produce the same bytes, whatever order
// the tags arrived in.
func TestExpandIsDeterministic(t *testing.T) {
	tags := fleet()
	a, err := Expand(fleetRules(), nil, tags)
	if err != nil {
		t.Fatal(err)
	}
	// Reverse the tag list; the output must not move.
	rev := make([]TagInfo, len(tags))
	for i, tg := range tags {
		rev[len(tags)-1-i] = tg
	}
	b, err := Expand(fleetRules(), nil, rev)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ids(a), ids(b)) {
		t.Fatalf("Expand is order-dependent:\n%v\n%v", ids(a), ids(b))
	}
	if !sortedStrings(ids(a)) {
		t.Fatalf("Expand output is not sorted by id: %v", ids(a))
	}
}

// TestExpandRejectsDuplicateIDs — two rules whose id templates collapse to
// the same string, which is the realistic way a generator goes wrong.
func TestExpandRejectsDuplicateIDs(t *testing.T) {
	rules := []Rule{
		{Match: Match{Type: "AnalogInput", Member: "HH"}, ID: "{tag}"},
		{Match: Match{Type: "AnalogInput", Member: "LL"}, ID: "{tag}"},
	}
	_, err := Expand(rules, nil, fleet())
	if err == nil {
		t.Fatal("Expand accepted two rules producing the same id")
	}
	if !strings.Contains(err.Error(), "rule 0") || !strings.Contains(err.Error(), "rule 1") {
		t.Errorf("error should name both sources, got: %v", err)
	}
}

// TestExpandRejectsRuleCollidingWithExplicitAlarm — the case the tag-files
// precedent cares about: a generated file and a hand-written one.
func TestExpandRejectsRuleCollidingWithExplicitAlarm(t *testing.T) {
	rules := []Rule{{Match: Match{Type: "AnalogInput", Member: "HH"}}}
	defs := []Def{{ID: "RTU9_FIT_001.HH", Tag: "RTU9_FIT_001.HH"}}
	_, err := Expand(rules, defs, fleet())
	if err == nil {
		t.Fatal("Expand accepted a rule colliding with an explicit alarm")
	}
	if !strings.Contains(err.Error(), "RTU9_FIT_001.HH") {
		t.Errorf("error should name the id, got: %v", err)
	}
}

func TestExpandRejectsUnknownPlaceholder(t *testing.T) {
	rules := []Rule{{Match: Match{Tag: "*_YA"}, Name: "{sight} intrusion"}}
	_, err := Expand(rules, nil, fleet())
	if err == nil || !strings.Contains(err.Error(), "{sight}") {
		t.Fatalf("want an unknown-placeholder error, got %v", err)
	}
}

func TestExpandRejectsEmptyMatch(t *testing.T) {
	if _, err := Expand([]Rule{{}}, nil, fleet()); err == nil {
		t.Fatal("a rule with no match should be rejected")
	}
}

// TestMatchMemberDiscriminatesStructFromFlat pins the rule that a member-less
// rule never claims a struct member and vice versa.
func TestMatchMemberDiscriminatesStructFromFlat(t *testing.T) {
	cases := []struct {
		m                     Match
		tag, typeName, member string
		want                  bool
	}{
		{Match{Type: "AnalogInput", Member: "HH"}, "A", "AnalogInput", "HH", true},
		{Match{Type: "AnalogInput", Member: "HH"}, "A", "AnalogInput", "LL", false},
		{Match{Type: "AnalogInput"}, "A", "AnalogInput", "HH", false}, // no member: flat only
		{Match{Tag: "*_YA"}, "X_YA", "", "", true},
		{Match{Tag: "*_YA"}, "X_YA", "AnalogInput", "HH", false}, // has member: struct only
		{Match{Member: "*ALM"}, "A", "Motor1Speed", "FAIL1ALM", true},
		{Match{Member: "*ALM"}, "A", "Motor1Speed", "ANYFAULT", false},
	}
	for _, c := range cases {
		if got := c.m.matches(c.tag, c.typeName, c.member); got != c.want {
			t.Errorf("%+v.matches(%q,%q,%q) = %v, want %v", c.m, c.tag, c.typeName, c.member, got, c.want)
		}
	}
}

func TestExpandFillsExplicitAlarmDefaults(t *testing.T) {
	defs := []Def{{Tag: "HiTempAlm", AckRequired: true, AutoClear: true, Shelvable: true}}
	out, err := Expand(nil, defs, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out[0].ID != "HiTempAlm" || out[0].Name != "HiTempAlm" {
		t.Fatalf("id/name should default to the condition path, got %+v", out[0])
	}
}

func TestExpandRequiresATag(t *testing.T) {
	if _, err := Expand(nil, []Def{{ID: "x"}}, nil); err == nil {
		t.Fatal("an alarm with no tag should be rejected")
	}
}

const alarmFile = `
- id: RTU9_WEL15_INT_001_YA
  tag: RTU9_WEL15_INT_001_YA
  name: RTU 9 Well 15 Intrusion
  priority: critical
  class: security
  site: RTU9
  area: WEL15
  on-delay: 5m
  enable: RTU9__Online
- tag: RTU9_ANNUNCIATE_ONLY
  ack-required: false
  shelve: false
- match: { type: AnalogInput, member: HH }
  name: "{desc} High High"
  priority: high
  class: process
  enable: "{site}__Online"
`

func TestLoadAlarmFile(t *testing.T) {
	f, err := Load(strings.NewReader(alarmFile))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(f.Defs) != 2 || len(f.Rules) != 1 {
		t.Fatalf("got %d defs / %d rules, want 2 / 1", len(f.Defs), len(f.Rules))
	}
	d := f.Defs[0]
	if d.Priority != Critical || d.OnDelay != 5*time.Minute || d.Enable != "RTU9__Online" {
		t.Errorf("first alarm decoded as %+v", d)
	}
	if !d.AckRequired || !d.AutoClear || !d.Shelvable {
		t.Errorf("policy bools should default to true, got %+v", d)
	}
	if a := f.Defs[1]; a.AckRequired || a.Shelvable || !a.AutoClear {
		t.Errorf("explicit false should stick: %+v", a)
	}
	if f.Rules[0].Match.Type != "AnalogInput" || f.Rules[0].Match.Member != "HH" {
		t.Errorf("rule decoded as %+v", f.Rules[0])
	}
}

func TestLoadRejectsUnknownKey(t *testing.T) {
	_, err := Load(strings.NewReader("- tag: X\n  pryority: high\n"))
	if err == nil || !strings.Contains(err.Error(), "pryority") {
		t.Fatalf("want an unknown-key error naming the typo, got %v", err)
	}
}

func TestLoadRejectsUnknownKeyInsideMatch(t *testing.T) {
	_, err := Load(strings.NewReader("- match: { typ: AnalogInput, member: HH }\n"))
	if err == nil || !strings.Contains(err.Error(), "typ") {
		t.Fatalf("want an unknown-key error inside match, got %v", err)
	}
}

func TestLoadEmptyFileIsEmptyList(t *testing.T) {
	f, err := Load(strings.NewReader(""))
	if err != nil {
		t.Fatalf("an empty alarm file should load as an empty list, got %v", err)
	}
	if len(f.Defs) != 0 || len(f.Rules) != 0 {
		t.Fatalf("got %+v", f)
	}
}

func TestLoadRejectsBadPriority(t *testing.T) {
	_, err := Load(strings.NewReader("- tag: X\n  priority: urgent\n"))
	if err == nil || !strings.Contains(err.Error(), "urgent") {
		t.Fatalf("want a priority error, got %v", err)
	}
}

// TestApplyDefaultsFillsOnlyWhatWasNotSpelled is the whole point of the
// `defaults:` block: it must not overwrite a deliberate value, including a
// deliberate `false`.
func TestApplyDefaultsFillsOnlyWhatWasNotSpelled(t *testing.T) {
	f, err := Load(strings.NewReader(alarmFile))
	if err != nil {
		t.Fatal(err)
	}
	low, no := Low, false
	onDelay := Duration(30 * time.Second)
	ApplyDefaults(Defaults{
		Priority: &low, Shelve: &no, OnDelay: &onDelay, Class: "generic",
	}, f.Defs)

	if f.Defs[0].Priority != Critical {
		t.Errorf("an explicit priority must survive defaults, got %v", f.Defs[0].Priority)
	}
	if f.Defs[0].OnDelay != 5*time.Minute {
		t.Errorf("an explicit on-delay must survive defaults, got %v", f.Defs[0].OnDelay)
	}
	if f.Defs[0].Class != "security" {
		t.Errorf("an explicit class must survive defaults, got %q", f.Defs[0].Class)
	}
	if f.Defs[1].Priority != Low {
		t.Errorf("an unspelled priority should take the default, got %v", f.Defs[1].Priority)
	}
	if f.Defs[1].OnDelay != 30*time.Second {
		t.Errorf("an unspelled on-delay should take the default, got %v", f.Defs[1].OnDelay)
	}
	if f.Defs[1].Class != "generic" {
		t.Errorf("an unspelled class should take the default, got %q", f.Defs[1].Class)
	}
	// shelve: false was spelled on Defs[1] and matches the default anyway;
	// Defs[0] never spelled it, so the default applies.
	if f.Defs[0].Shelvable {
		t.Errorf("an unspelled shelve should take the default false")
	}
}

func TestComposeAppliesDefaultsToRuleOutput(t *testing.T) {
	c := &Config{
		Defaults: Defaults{Class: "process"},
		Rules:    []Rule{{Match: Match{Tag: "*_YA"}}},
	}
	out, err := c.Compose(nil, fleet())
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("got %d defs, want 2: %v", len(out), ids(out))
	}
	for _, d := range out {
		if d.Class != "process" {
			t.Errorf("%s: class = %q, want the default", d.ID, d.Class)
		}
	}
}

func TestConfigShelveDurationsFallBack(t *testing.T) {
	var c Config
	if got := c.ShelveDurations(); !reflect.DeepEqual(got, DefaultShelveTimes) {
		t.Fatalf("ShelveDurations = %v, want the defaults", got)
	}
	c.ShelveTimes = []Duration{Duration(time.Minute)}
	if got := c.ShelveDurations(); len(got) != 1 || got[0] != time.Minute {
		t.Fatalf("ShelveDurations = %v", got)
	}
}

func TestPriorityWireTokens(t *testing.T) {
	for i, want := range priorityNames {
		if got := Priority(i).String(); got != want {
			t.Errorf("Priority(%d) = %q, want %q", i, got, want)
		}
		p, err := ParsePriority(strings.ToUpper(want))
		if err != nil || p != Priority(i) {
			t.Errorf("ParsePriority(%q) = %v, %v", want, p, err)
		}
	}
	if _, err := ParsePriority("urgent"); err == nil {
		t.Error("ParsePriority should reject an unknown token")
	}
}

func ids(defs []Def) []string {
	out := make([]string, len(defs))
	for i, d := range defs {
		out[i] = d.ID
	}
	return out
}

func sortedStrings(s []string) bool {
	for i := 1; i < len(s); i++ {
		if s[i-1] > s[i] {
			return false
		}
	}
	return true
}
