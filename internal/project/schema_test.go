package project

// The JSON Schema shipped with the VS Code extension is what an author
// actually sees while editing nautilus.yaml: completion, hover text, and a
// squiggle on a typo. It is hand-written, so the one thing that must not
// happen is it drifting away from this loader — a key the schema knows and
// Load rejects, or worse, a key Load honors that the schema flags as
// invalid.
//
// The manifest is also the file a generator emits (docs/design/tags.md §3.1),
// so the schema is the contract every generator in every language targets.
// That makes drift more expensive here than for *_test.yaml: a generator
// emitting a valid manifest the editor squiggles is a bug report against
// nautilus, not against the generator.
//
// This mirrors acceptance/schema_test.go, including its deliberate limit:
// there is no JSON Schema validator in the module, so these tests compare
// structure and cross-check behaviour rather than validating documents.

import (
	"encoding/json"
	"os"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/joyautomation/nautilus/alarm"
	"gopkg.in/yaml.v3"
)

const schemaPath = "../../tools/vscode-iec/schemas/nautilus.schema.json"

type schemaDoc struct {
	Required    []string                   `json:"required"`
	Properties  map[string]json.RawMessage `json:"properties"`
	Definitions map[string]schemaNode      `json:"definitions"`
}

type schemaNode struct {
	Required   []string                   `json:"required"`
	Properties map[string]json.RawMessage `json:"properties"`
}

func loadSchema(t *testing.T) schemaDoc {
	t.Helper()
	raw, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("the extension's manifest schema is missing: %v", err)
	}
	var s schemaDoc
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	return s
}

// goKeys is the set of yaml keys a struct actually accepts. The decoder runs
// with KnownFields(true), so this is exactly what will not error.
func goKeys(v any) []string {
	t := reflect.TypeOf(v)
	var out []string
	for i := range t.NumField() {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("yaml"), ",")
		if name == "" {
			name = strings.ToLower(t.Field(i).Name)
		}
		if name != "-" {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func schemaKeys(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// enumOf reads the allowed values of a scalar property, e.g. a tag's role.
func enumOf(t *testing.T, s schemaDoc, def, prop string) []string {
	t.Helper()
	raw, ok := s.Definitions[def].Properties[prop]
	if !ok {
		t.Fatalf("schema has no %s.%s — did the definition move?", def, prop)
	}
	var node struct {
		Enum []string `json:"enum"`
	}
	if err := json.Unmarshal(raw, &node); err != nil {
		t.Fatal(err)
	}
	if len(node.Enum) == 0 {
		t.Fatalf("schema offers no enum for %s.%s", def, prop)
	}
	sort.Strings(node.Enum)
	return node.Enum
}

func TestSchemaMatchesLoader(t *testing.T) {
	s := loadSchema(t)
	for _, tc := range []struct {
		what   string
		got    []string
		schema []string
	}{
		{"the manifest itself", goKeys(Manifest{}), schemaKeys(s.Properties)},
		{"server", goKeys(ServerConfig{}), schemaKeys(s.Definitions["server"].Properties)},
		{"a task", goKeys(TaskConfig{}), schemaKeys(s.Definitions["task"].Properties)},
		{"a tag", goKeys(TagConfig{}), schemaKeys(s.Definitions["tag"].Properties)},
		{"the driver", goKeys(DriverConfig{}), schemaKeys(s.Definitions["driver"].Properties)},
		{"sparkplug", goKeys(SparkplugConfig{}), schemaKeys(s.Definitions["sparkplug"].Properties)},
		{"an rbe class", goKeys(RBEConfig{}), schemaKeys(s.Definitions["rbe"].Properties)},
		{"tag-meta", goKeys(MetaConfig{}), schemaKeys(s.Definitions["meta"].Properties)},
		{"alarms", goKeys(AlarmsConfig{}), schemaKeys(s.Definitions["alarms"].Properties)},
		{"alarm defaults", goKeys(alarm.Defaults{}), schemaKeys(s.Definitions["alarm-defaults"].Properties)},
		{"the alarm journal", goKeys(AlarmJournalConfig{}), schemaKeys(s.Definitions["alarm-journal"].Properties)},
		{"an alarm notifier", goKeys(AlarmNotifyConfig{}), schemaKeys(s.Definitions["alarm-notify"].Properties)},
		{"an alarm rule", goKeys(alarm.Rule{}), schemaKeys(s.Definitions["alarm-rule"].Properties)},
		{"an alarm rule's match", goKeys(alarm.Match{}), schemaKeys(s.Definitions["alarm-match"].Properties)},
	} {
		if !reflect.DeepEqual(tc.got, tc.schema) {
			t.Errorf("%s: the loader accepts %v, the extension's schema declares %v\n"+
				"update %s to match", tc.what, tc.got, tc.schema, schemaPath)
		}
	}
}

// Every role the schema offers must be one tagDefs implements — otherwise
// completion suggests a role that fails at load.
func TestSchemaRolesAreImplemented(t *testing.T) {
	s := loadSchema(t)
	for _, role := range enumOf(t, s, "tag", "role") {
		// init satisfies setpoint/state; it is legal on the other roles too,
		// so one config shape exercises every offered role.
		_, err := tagDefs([]TagConfig{{Name: "T", Role: role, Init: 1.0}})
		if err != nil {
			t.Errorf("schema offers role %q but the loader rejects it: %v", role, err)
		}
	}
	if _, err := tagDefs([]TagConfig{{Name: "T", Role: "bogus", Init: 1.0}}); err == nil {
		t.Error("loader accepted a role the schema's enum does not offer")
	}
}

// Same for driver types. A valid eip driver dials hardware, so this asserts
// only that the offered types are recognised — not that they construct.
func TestSchemaDriverTypesAreImplemented(t *testing.T) {
	s := loadSchema(t)
	const unknown = "manifest projects support" // the unrecognised-type error
	for _, typ := range enumOf(t, s, "driver", "type") {
		_, err := buildDriver(fstest.MapFS{}, DriverConfig{Type: typ})
		if err != nil && strings.Contains(err.Error(), unknown) {
			t.Errorf("schema offers driver type %q but the loader does not recognise it", typ)
		}
	}
	_, err := buildDriver(fstest.MapFS{}, DriverConfig{Type: "modbus"})
	if err == nil || !strings.Contains(err.Error(), unknown) {
		t.Errorf("loader accepted a driver type the schema's enum does not offer: %v", err)
	}
}

// The schema's `required` lists are the other half of the contract: a key the
// loader insists on but the schema calls optional means the editor stays
// silent on a manifest that cannot run.
func TestSchemaRequiresWhatLoaderRequires(t *testing.T) {
	s := loadSchema(t)
	requires := func(t *testing.T, what string, list []string, key string) {
		t.Helper()
		if !slices.Contains(list, key) {
			t.Errorf("the loader requires %s.%s but the schema lists required=%v", what, key, list)
		}
	}

	// The loader rejects a manifest with no tasks...
	if _, err := Load(fsys("name: x\n"), ""); err == nil {
		t.Fatal("loader accepted a manifest with no tasks")
	}
	requires(t, "manifest", s.Required, "tasks")

	// ...a task with no program...
	if _, err := Load(fsys("tasks:\n  - name: t\n"), ""); err == nil {
		t.Fatal("loader accepted a task with no program")
	}
	requires(t, "task", s.Definitions["task"].Required, "program")

	// ...a tag with no name, and one with an unusable role.
	if _, err := tagDefs([]TagConfig{{Role: "state", Init: 1.0}}); err == nil {
		t.Fatal("loader accepted a tag with no name")
	}
	requires(t, "tag", s.Definitions["tag"].Required, "name")
	if _, err := tagDefs([]TagConfig{{Name: "T"}}); err == nil {
		t.Fatal("loader accepted a tag with no role")
	}
	requires(t, "tag", s.Definitions["tag"].Required, "role")

	// eip needs a host and a manifest before it can be built.
	for _, key := range []string{"host", "manifest"} {
		cfg := DriverConfig{Type: "eip", Host: "plc", Manifest: "eip_manifest.yaml"}
		switch key {
		case "host":
			cfg.Host = ""
		case "manifest":
			cfg.Manifest = ""
		}
		if _, err := buildDriver(fstest.MapFS{}, cfg); err == nil {
			t.Fatalf("loader accepted an eip driver with no %s", key)
		}
	}
}

// A `pattern` is the easiest way to make the schema STRICTER than the loader,
// which squiggles a manifest that runs perfectly — the worst failure this file
// exists to prevent, and worse than the reverse because the author has no
// recourse. The duration pattern is the only one left in the schema (the
// others were over-constraints on file extensions and URL schemes that the
// loader never checked), so it gets cross-checked against the parser itself.
func TestSchemaDurationPatternMatchesParseDuration(t *testing.T) {
	raw, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Definitions map[string]struct {
			Pattern string `json:"pattern"`
		} `json:"definitions"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	pat := doc.Definitions["duration"].Pattern
	if pat == "" {
		t.Fatal("the duration definition has no pattern — did it move?")
	}
	re, err := regexp.Compile(pat)
	if err != nil {
		t.Fatalf("duration pattern does not compile: %v", err)
	}
	for _, s := range []string{
		"0", "1ns", "100ms", "2s", "1h30m", "1.5s", ".5s", "1.s",
		"-1s", "+2h", "1us", "1µs", "1μs", "10m", "1h2m3s", "300ms",
		"0s", "1000000ns", "2.25h",
	} {
		_, perr := time.ParseDuration(s)
		if perr != nil {
			t.Fatalf("test table is wrong: %q is not a Go duration (%v)", s, perr)
		}
		if !re.MatchString(s) {
			t.Errorf("schema rejects %q, but time.ParseDuration accepts it — "+
				"the editor would squiggle a manifest that runs", s)
		}
	}
	for _, s := range []string{
		"250", "", "s", "abc", "1", "1 s", "1sec", "ms", "1.2.3s", "0.5",
	} {
		if _, perr := time.ParseDuration(s); perr == nil {
			t.Fatalf("test table is wrong: %q IS a Go duration", s)
		}
		if re.MatchString(s) {
			t.Errorf("schema accepts %q, but time.ParseDuration rejects it — "+
				"the editor would stay silent on a manifest that fails to load", s)
		}
	}
}

// A tag file is a bare list of the SAME entries as tags:, so its schema
// refs this one rather than copying the definition — two copies would drift
// and only one of them would be tested.
func TestTagFileSchemaRefsTheTagDefinition(t *testing.T) {
	const tagsSchemaPath = "../../tools/vscode-iec/schemas/nautilus-tags.schema.json"
	raw, err := os.ReadFile(tagsSchemaPath)
	if err != nil {
		t.Fatalf("the extension's tag-file schema is missing: %v", err)
	}
	var doc struct {
		Type  string `json:"type"`
		Items struct {
			Ref string `json:"$ref"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("tag-file schema is not valid JSON: %v", err)
	}
	if doc.Type != "array" {
		t.Errorf("a tag file is a bare YAML list, but its schema declares type %q", doc.Type)
	}
	file, frag, ok := strings.Cut(doc.Items.Ref, "#")
	if !ok || file != "nautilus.schema.json" || frag != "/definitions/tag" {
		t.Fatalf("tag-file items $ref = %q, want nautilus.schema.json#/definitions/tag "+
			"— a copied definition would drift from the loader untested", doc.Items.Ref)
	}
	if _, ok := loadSchema(t).Definitions["tag"]; !ok {
		t.Error("the tag-file schema refs #/definitions/tag, which does not exist")
	}
}

// A struct-typed tag's init may be a mapping of member name to value
// (internal/project/udt_test.go's TestStructInit* prove the loader side);
// this is the schema half — the editor must not squiggle the shape the
// loader actually accepts.
func TestSchemaInitAllowsAnObject(t *testing.T) {
	s := loadSchema(t)
	raw, ok := s.Definitions["tag"].Properties["init"]
	if !ok {
		t.Fatal("schema has no tag.init — did it move?")
	}
	var node struct {
		Type []string `json:"type"`
	}
	if err := json.Unmarshal(raw, &node); err != nil {
		t.Fatal(err)
	}
	types := node.Type
	if !slices.Contains(types, "object") {
		t.Errorf("tag.init's schema type is %v — a struct tag's per-member init "+
			"(a YAML mapping) needs \"object\" alongside the scalar kinds", types)
	}

	// And the loader really does accept it — this is the schema/loader pair
	// TestSchemaRolesAreImplemented and friends check for every other field.
	if _, err := tagDefs([]TagConfig{{
		Name: "T", Role: "state", Type: "Motor",
		Init: map[string]any{"Speed": 1.0},
	}}); err != nil {
		t.Errorf("loader rejected an object init the schema now offers: %v", err)
	}
}

// setpoint and state are seeded roles: without init they have no value on
// scan one, which the loader treats as an error. The schema says the same
// with an if/then, and this proves the loader half of that pair.
func TestSeededRolesNeedInit(t *testing.T) {
	for _, role := range []string{"setpoint", "state"} {
		if _, err := tagDefs([]TagConfig{{Name: "T", Role: role}}); err == nil {
			t.Errorf("loader accepted %s with no init", role)
		}
	}
	for _, role := range []string{"input", "output"} {
		if _, err := tagDefs([]TagConfig{{Name: "T", Role: role}}); err != nil {
			t.Errorf("loader rejected %s with no init, but unseeded is the point: %v", role, err)
		}
	}
}

// alarm.Def cannot be reflected the way every other definition can: its
// three policy bools default to TRUE and its durations arrive as strings,
// so the yaml keys live on an unexported wire struct with Def itself
// tagged `yaml:"-"`. Reflection would under-declare the schema and pass.
//
// So this asserts the thing that actually matters, one key at a time:
// every key the schema offers on an alarm, the loader accepts — and a key
// it does not offer, the loader rejects. That is a stronger check than
// name equality, since it exercises the decoder rather than a struct tag.
func TestSchemaAlarmKeysAreAccepted(t *testing.T) {
	s := loadSchema(t)
	props := s.Definitions["alarm"].Properties
	if len(props) == 0 {
		t.Fatal("schema has no definitions.alarm — did it move?")
	}
	// A value of the right YAML shape for each key the schema declares.
	sample := map[string]string{
		"id": "A", "tag": "T.HH", "name": "High High", "priority": "high",
		"class": "process", "site": "RTU9", "area": "WEL15",
		"display": "Wells/15", "notes": "n", "on-delay": "5m",
		"off-delay": "2s", "ack-required": "false", "auto-clear": "false",
		"shelve": "false", "enable": "RTU9__Online",
	}
	for _, key := range schemaKeys(props) {
		v, ok := sample[key]
		if !ok {
			t.Errorf("schema offers alarm.%s but this test has no sample value for it — "+
				"add one, so the loader half is actually checked", key)
			continue
		}
		doc := "- tag: T.HH\n  " + key + ": " + v + "\n"
		if key == "tag" {
			doc = "- tag: " + v + "\n"
		}
		if _, err := alarm.Load(strings.NewReader(doc)); err != nil {
			t.Errorf("schema offers alarm.%s but the loader rejects it: %v", key, err)
		}
	}
	if _, err := alarm.Load(strings.NewReader("- tag: T.HH\n  pryority: high\n")); err == nil {
		t.Error("loader accepted a key the schema does not offer — a typo that looks configured " +
			"and is not is the worst outcome available")
	}
}

// Every priority and every journal sink the schema offers must be one the
// loader implements, the same pairing TestSchemaRolesAreImplemented makes
// for tag roles.
func TestSchemaAlarmEnumsAreImplemented(t *testing.T) {
	s := loadSchema(t)
	for _, p := range enumOf(t, s, "alarm", "priority") {
		if _, err := alarm.ParsePriority(p); err != nil {
			t.Errorf("schema offers priority %q but the loader rejects it: %v", p, err)
		}
	}
	if _, err := alarm.ParsePriority("urgent"); err == nil {
		t.Error("loader accepted a priority the schema's enum does not offer")
	}
	for _, sink := range enumOf(t, s, "alarm-journal", "sink") {
		c := AlarmsConfig{Journal: AlarmJournalConfig{Sink: sink, Path: "alarms.jsonl"}}
		if err := c.validate(); err != nil {
			t.Errorf("schema offers journal sink %q but the loader rejects it: %v", sink, err)
		}
	}
	c := AlarmsConfig{Journal: AlarmJournalConfig{Sink: "sqlite"}}
	if err := c.validate(); err == nil {
		t.Error("loader accepted a journal sink the schema's enum does not offer")
	}
}

// An alarm file is a bare list of the SAME entries as `alarms.defs:` and
// `alarms.rules:`, so its schema refs those definitions rather than
// copying them — two copies would drift and only one would be tested.
// Exactly the argument TestTagFileSchemaRefsTheTagDefinition makes.
func TestAlarmFileSchemaRefsTheAlarmDefinitions(t *testing.T) {
	const path = "../../tools/vscode-iec/schemas/nautilus-alarms.schema.json"
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the extension's alarm-file schema is missing: %v", err)
	}
	var doc struct {
		Type  string `json:"type"`
		Items struct {
			AnyOf []struct {
				Ref string `json:"$ref"`
			} `json:"anyOf"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("alarm-file schema is not valid JSON: %v", err)
	}
	if doc.Type != "array" {
		t.Errorf("an alarm file is a bare YAML list, but its schema declares type %q", doc.Type)
	}
	var refs []string
	for _, a := range doc.Items.AnyOf {
		refs = append(refs, a.Ref)
	}
	sort.Strings(refs)
	want := []string{"nautilus.schema.json#/definitions/alarm", "nautilus.schema.json#/definitions/alarm-rule"}
	if !reflect.DeepEqual(refs, want) {
		t.Fatalf("alarm-file items $ref = %v, want %v — a copied definition would drift "+
			"from the loader untested", refs, want)
	}
	s := loadSchema(t)
	for _, def := range []string{"alarm", "alarm-rule"} {
		if _, ok := s.Definitions[def]; !ok {
			t.Errorf("the alarm-file schema refs #/definitions/%s, which does not exist", def)
		}
	}
}

// Durations in the alarm section must ref the shared duration definition,
// never `type: string` — the same rule the rest of the manifest follows,
// and the reason TestSchemaDurationPatternMatchesParseDuration is worth
// anything.
func TestAlarmDurationsRefTheDurationDefinition(t *testing.T) {
	raw, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Definitions map[string]struct {
			Properties map[string]struct {
				Ref  string `json:"$ref"`
				Type any    `json:"type"`
			} `json:"properties"`
		} `json:"definitions"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for _, def := range []string{"alarm", "alarm-rule", "alarm-defaults"} {
		for _, key := range []string{"on-delay", "off-delay"} {
			p, ok := doc.Definitions[def].Properties[key]
			if !ok {
				t.Errorf("schema has no %s.%s", def, key)
				continue
			}
			if p.Ref != "#/definitions/duration" {
				t.Errorf("%s.%s is %v/%q, want $ref #/definitions/duration", def, key, p.Type, p.Ref)
			}
		}
	}
}

// The alarm section round-trips through the manifest decoder with
// KnownFields on, which is the only thing standing between a mistyped
// alarm key and silence.
func TestManifestAlarmsDecodeStrictly(t *testing.T) {
	const good = `
name: x
alarms:
  defaults: { priority: high }
  shelve-times: [5m, 1h]
  journal: { keep: 100 }
  notify: [{ log: true }]
  site-from: "^([A-Z]+[0-9]+)_"
  rules:
    - match: { type: AnalogInput, member: HH }
      name: "{desc} High High"
      priority: high
  defs:
    - id: A
      tag: T
      on-delay: 5m
`
	var m Manifest
	dec := yaml.NewDecoder(strings.NewReader(good))
	dec.KnownFields(true)
	if err := dec.Decode(&m); err != nil {
		t.Fatalf("a valid alarms section failed to decode: %v", err)
	}
	if m.Alarms == nil || len(m.Alarms.Rules) != 1 || len(m.Alarms.Defs) != 1 {
		t.Fatalf("alarms decoded as %+v", m.Alarms)
	}
	if got := m.Alarms.Defs[0].OnDelay; got != 5*time.Minute {
		t.Errorf("on-delay = %s, want 5m", got)
	}
	for _, bad := range []string{
		"alarms: { defaultz: {} }",
		"alarms: { rules: [{ match: { typ: X } }] }",
		"alarms: { defs: [{ tag: T, pryority: high }] }",
	} {
		var m Manifest
		dec := yaml.NewDecoder(strings.NewReader(bad))
		dec.KnownFields(true)
		if err := dec.Decode(&m); err == nil {
			t.Errorf("decoder accepted %q — a typo must be an error, not silence", bad)
		}
	}
}
