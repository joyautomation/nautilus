package codegen

import (
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/st"
	"github.com/joyautomation/nautilus/sparkplug"
	"github.com/joyautomation/nautilus/sparkplug/host"
	"github.com/joyautomation/nautilus/sparkplug/spb"
)

var update = flag.Bool("update", false, "rewrite the testdata golden files")

// ── the sample fleet ─────────────────────────────────────────────────────
//
// Two sites on one group, built the way a real edge builds them: metrics go
// out as sparkplug.Payload, are ENCODED to protobuf, and are decoded back —
// so the generator is fed exactly the bytes the driver's handleMessage would
// see, not a hand-built Go value that skips the wire.

func sampleBirths(t *testing.T) []host.Birth {
	t.Helper()

	// Template definitions, emitted in the WRONG order on purpose (Skid
	// references Motor): the generator must reorder them dependencies-first.
	skidDef := &sparkplug.Template{IsDefinition: true, Version: "1.0", Metrics: []sparkplug.Metric{
		{Name: "Main", Datatype: spb.DataType_Template, Value: &sparkplug.Template{TemplateRef: "Motor"}},
		{Name: "Hours", Datatype: spb.DataType_Int32, IsNull: true},
	}}
	motorDef := &sparkplug.Template{IsDefinition: true, Version: "1.0", Metrics: []sparkplug.Metric{
		{Name: "Speed", Datatype: spb.DataType_Double, IsNull: true},
		{Name: "Run", Datatype: spb.DataType_Boolean, IsNull: true},
	}}
	motor := func(speed float64, run bool) *sparkplug.Template {
		return &sparkplug.Template{TemplateRef: "Motor", Version: "1.0", Metrics: []sparkplug.Metric{
			{Name: "Speed", Datatype: spb.DataType_Double, Value: speed},
			{Name: "Run", Datatype: spb.DataType_Boolean, Value: run},
		}}
	}

	const ts = 1755800000000

	w6 := sparkplug.Payload{Timestamp: ts, Seq: 0, Metrics: []sparkplug.Metric{
		// Sparkplug plumbing — never bound.
		{Name: "bdSeq", Datatype: spb.DataType_Int64, Value: int64(3)},
		{Name: "Node Control/Rebirth", Datatype: spb.DataType_Boolean, Value: false},
		{Name: "Skid", Datatype: spb.DataType_Template, Value: skidDef},
		{Name: "Motor", Datatype: spb.DataType_Template, Value: motorDef},
		// Process data.
		{Name: "Well/Level", Datatype: spb.DataType_Double, Value: 12.5},
		{Name: "Well/LastSample", Datatype: spb.DataType_DateTime, Value: int64(ts)},
		{Name: "Site Name", Datatype: spb.DataType_String, Value: "Well 6"},
		{Name: "Pump1", Datatype: spb.DataType_Template, Value: motor(1750, true)},
		{Name: "Skid A", Datatype: spb.DataType_Template, Value: &sparkplug.Template{
			TemplateRef: "Skid", Version: "1.0", Metrics: []sparkplug.Metric{
				{Name: "Main", Datatype: spb.DataType_Template, Value: motor(0, false)},
				{Name: "Hours", Datatype: spb.DataType_Int32, Value: int64(41)},
			}}},
	}}
	w6plc1 := sparkplug.Payload{Timestamp: ts, Seq: 1, Metrics: []sparkplug.Metric{
		{Name: "Pump/Run", Datatype: spb.DataType_Boolean, Value: true},
		{Name: "Pump/SpeedSP", Datatype: spb.DataType_Double, Value: 60.0},
		{Name: "Device Control/Rebirth", Datatype: spb.DataType_Boolean, Value: false},
	}}
	w7 := sparkplug.Payload{Timestamp: ts, Seq: 0, Metrics: []sparkplug.Metric{
		{Name: "bdSeq", Datatype: spb.DataType_Int64, Value: int64(9)},
		{Name: "Node Control/Rebirth", Datatype: spb.DataType_Boolean, Value: false},
		{Name: "Motor", Datatype: spb.DataType_Template, Value: motorDef},
		{Name: "Well/Level", Datatype: spb.DataType_Double, Value: 3.25},
		{Name: "Pump1", Datatype: spb.DataType_Template, Value: motor(0, false)},
	}}

	return []host.Birth{
		{Group: "PomonaWRD", EdgeNode: "W6", Payload: wire(t, w6)},
		{Group: "PomonaWRD", EdgeNode: "W6", Device: "PLC1", Payload: wire(t, w6plc1)},
		{Group: "PomonaWRD", EdgeNode: "W7", Payload: wire(t, w7)},
	}
}

// wire encodes and decodes a payload, so tests see what the broker delivers.
func wire(t *testing.T, p sparkplug.Payload) sparkplug.Payload {
	t.Helper()
	raw, err := p.Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	out, err := sparkplug.DecodePayload(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

// sampleOptions exercises both shapes of --writable: a plain glob marking a
// SCALAR metric writable as a whole, and dotted MEMBER globs — one naming a
// member of a flat template (Pump1.Speed), one reaching through a nested one
// (*.Main.Run, which resolves inside "Skid A" : Skid → Main : Motor).
func sampleOptions() Options {
	return Options{Writable: []string{"PLC1/Pump/SpeedSP", "Pump1.Speed", "*.Main.Run"}}
}

// ── golden files ─────────────────────────────────────────────────────────

func TestGenerateGolden(t *testing.T) {
	m, err := FromBirths(sampleBirths(t), sampleOptions())
	if err != nil {
		t.Fatalf("FromBirths: %v", err)
	}

	typesST, err := TypesST(m)
	if err != nil {
		t.Fatalf("TypesST: %v", err)
	}
	manifestYAML, err := ManifestYAML(m)
	if err != nil {
		t.Fatalf("ManifestYAML: %v", err)
	}
	tagsYAML, err := TagsYAML(m, nil)
	if err != nil {
		t.Fatalf("TagsYAML: %v", err)
	}

	golden(t, "sparkplug_types.st", typesST)
	golden(t, "sparkplug_manifest.yaml", manifestYAML)
	golden(t, filepath.Join("tags", "sparkplug.yaml"), tagsYAML)

	// The generated ST must actually compile together with a program that
	// uses it — a broken import fails here, not on disk.
	program := string(typesST) + `
PROGRAM Main
VAR_EXTERNAL
  W6__Online : BOOL;
  W6_Pump1 : Motor;
  W6_Skid_A : Skid;
  W6_Well_Level : LREAL;
  W6_Well_LastSample : LINT;
END_VAR
IF W6__Online AND W6_Pump1.Run AND W6_Skid_A.Main.Run THEN
  W6_Well_Level := W6_Pump1.Speed + W6_Skid_A.Hours;
  W6_Well_LastSample := W6_Well_LastSample + 1;
END_IF;
END_PROGRAM`
	prog, err := st.Parse(program)
	if err != nil {
		t.Fatalf("generated ST does not parse: %v\n%s", err, typesST)
	}
	if _, err := st.Lower(prog); err != nil {
		t.Fatalf("generated ST does not lower: %v\n%s", err, typesST)
	}
}

// TestTagsRoundTrip is `nautilus sparkplug tags`: the committed manifest
// alone, with no broker and no births, must re-derive the committed tag file
// byte for byte.
func TestTagsRoundTrip(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "sparkplug_manifest.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	m, err := host.ParseManifest(raw)
	if err != nil {
		t.Fatalf("parse golden manifest: %v", err)
	}
	tags, err := TagsYAML(m, nil)
	if err != nil {
		t.Fatalf("TagsYAML: %v", err)
	}
	want, err := os.ReadFile(filepath.Join("testdata", "tags", "sparkplug.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(tags) != string(want) {
		t.Errorf("re-derived tag file differs from the committed one:\n%s", tags)
	}
}

// golden compares out with testdata/name, rewriting it under -update.
func golden(t *testing.T, name string, out []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, out, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run: go test ./sparkplug/host/codegen -update)", err)
	}
	if string(out) != string(want) {
		t.Errorf("%s differs from the golden file.\n--- got ---\n%s", name, out)
	}
}

// ── the offline path ─────────────────────────────────────────────────────

// TestFromSitesMatchesFromBirths is the contract that makes --sites usable:
// a site list describing the sample fleet must generate the SAME manifest the
// broker path generates from that fleet's births. If the two ever diverge, a
// project generated offline stops matching the wire.
func TestFromSitesMatchesFromBirths(t *testing.T) {
	sites := []byte(`
group: PomonaWRD
types:
  - name: Motor
    fields:
      - {name: Speed, type: Double}
      - {name: Run, type: Boolean}
  - name: Skid
    fields:
      - {name: Main, type: Motor}
      - {name: Hours, type: Int32}
sites:
  - node: W6
    metrics:
      - {name: Pump1, type: Motor, writable: [Speed]}
      - {name: Site Name, type: String}
      - {name: Skid A, type: Skid, writable: [Main.Run]}
      - {name: Well/LastSample, type: DateTime}
      - {name: Well/Level, type: Double}
    devices:
      - device: PLC1
        metrics:
          - {name: Pump/Run, type: Boolean}
          - {name: Pump/SpeedSP, type: Double, writable: true}
  - node: W7
    metrics:
      - {name: Pump1, type: Motor, writable: [Speed]}
      - {name: Well/Level, type: Double}
`)
	specs, err := ParseSites(sites)
	if err != nil {
		t.Fatalf("ParseSites: %v", err)
	}
	fromSites, err := FromSites(specs, Options{})
	if err != nil {
		t.Fatalf("FromSites: %v", err)
	}
	fromBirths, err := FromBirths(sampleBirths(t), sampleOptions())
	if err != nil {
		t.Fatalf("FromBirths: %v", err)
	}
	if !reflect.DeepEqual(fromSites, fromBirths) {
		t.Errorf("--sites and --broker disagree\nsites:  %+v\nbirths: %+v", fromSites, fromBirths)
	}
}

func TestParseSitesRejectsUnknownKeys(t *testing.T) {
	_, err := ParseSites([]byte("group: G\nsites:\n  - node: W6\n    metrix: []\n"))
	if err == nil {
		t.Fatal("want an error for a misspelled key")
	}
}

func TestSitesWritableInit(t *testing.T) {
	specs, err := ParseSites([]byte(`
group: G
sites:
  - node: W6
    metrics:
      - {name: SP, type: Double, writable: true, init: 12.5}
`))
	if err != nil {
		t.Fatal(err)
	}
	m, err := FromSites(specs, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Tags) != 1 || !m.Tags[0].Writable || m.Tags[0].Init != 12.5 {
		t.Fatalf("binding = %+v", m.Tags)
	}
	out, err := TagsYAML(m, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := "- { name: W6_SP, role: output, init: 12.5 }"; !strings.Contains(string(out), want) {
		t.Errorf("tag file missing %q:\n%s", want, out)
	}
}

// TestManifestKeepsFloatInits pins the round trip that silently retypes a
// tag: yaml.v3 writes float64(0) as "0", which decodes back as an int, and an
// int init seeds a DINT where the driver delivers a REAL.
func TestManifestKeepsFloatInits(t *testing.T) {
	specs, err := ParseSites([]byte(`
group: G
sites:
  - node: W6
    metrics:
      - {name: SP, type: Double, writable: true, init: 0.0}
      - {name: Count, type: Int32, writable: true, init: 0}
`))
	if err != nil {
		t.Fatal(err)
	}
	m, err := FromSites(specs, Options{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := ManifestYAML(m)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "init: 0.0") {
		t.Errorf("manifest lost the REAL init:\n%s", raw)
	}
	back, err := host.ParseManifest(raw)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	tags, err := TagsYAML(back, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"- { name: W6_Count, role: output, init: 0 }",
		"- { name: W6_SP, role: output, init: 0.0 }",
	} {
		if !strings.Contains(string(tags), want) {
			t.Errorf("tag file missing:\n  %s\ngot:\n%s", want, tags)
		}
	}
}

// ── filters, collisions, rejections ──────────────────────────────────────

func TestNodeAndMetricGlobs(t *testing.T) {
	m, err := FromBirths(sampleBirths(t), Options{
		Nodes:    []string{"W6"},
		Metrics:  []string{"Well/*", "Pump/*"},
		Writable: []string{"Pump/*"},
	})
	if err != nil {
		t.Fatalf("FromBirths: %v", err)
	}
	if len(m.Nodes) != 1 || m.Nodes[0].EdgeNode != "W6" {
		t.Fatalf("nodes = %+v", m.Nodes)
	}
	var names []string
	for _, b := range m.Tags {
		names = append(names, b.Name)
	}
	want := []string{"W6_PLC1_Pump_Run", "W6_PLC1_Pump_SpeedSP", "W6_Well_LastSample", "W6_Well_Level"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("tags = %v, want %v", names, want)
	}
	for _, b := range m.Tags {
		if got := b.Writable; got != (b.Device == "PLC1") {
			t.Errorf("%s writable = %v", b.Name, got)
		}
	}
	// "*" does not cross "/", so a folder-shaped name needs its folder glob:
	// the top-level metrics are excluded by the patterns above.
	for _, b := range m.Tags {
		if b.Metric == "Pump1" || b.Metric == "Site Name" {
			t.Errorf("%q should not have matched", b.Metric)
		}
	}
}

// TestNameCollision covers two distinct Sparkplug names that sanitize onto
// one identifier: the second must take _2 rather than silently replacing the
// first (composeTags rejects duplicates outright).
func TestNameCollision(t *testing.T) {
	p := sparkplug.Payload{Timestamp: 1, Seq: 0, Metrics: []sparkplug.Metric{
		{Name: "bdSeq", Datatype: spb.DataType_Int64, Value: int64(1)},
		{Name: "Well/Level", Datatype: spb.DataType_Double, Value: 1.0},
		{Name: "Well.Level", Datatype: spb.DataType_Double, Value: 2.0},
		{Name: "Well Level", Datatype: spb.DataType_Double, Value: 3.0},
		{Name: "Online", Datatype: spb.DataType_Boolean, Value: true},
	}}
	m, err := FromBirths([]host.Birth{
		{Group: "G", EdgeNode: "W6", Payload: wire(t, p)},
	}, Options{})
	if err != nil {
		t.Fatalf("FromBirths: %v", err)
	}
	got := map[string]string{}
	for _, b := range m.Tags {
		got[b.Metric] = b.Name
	}
	want := map[string]string{
		"Well Level": "W6_Well_Level",
		"Well.Level": "W6_Well_Level_2",
		"Well/Level": "W6_Well_Level_3",
		"Online":     "W6_Online",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("names = %v, want %v", got, want)
	}
	if err := m.Validate(); err != nil {
		t.Errorf("generated manifest does not validate: %v", err)
	}
}

func TestPrefixModes(t *testing.T) {
	births := []host.Birth{{Group: "G", EdgeNode: "W6", Payload: wire(t, sparkplug.Payload{
		Metrics: []sparkplug.Metric{{Name: "Well/Level", Datatype: spb.DataType_Double, Value: 1.0}},
	})}}
	for _, tc := range []struct{ prefix, want string }{
		{"", "W6_Well_Level"},
		{PrefixNode, "W6_Well_Level"},
		{PrefixNone, "Well_Level"},
		{"Site1", "Site1_Well_Level"},
	} {
		m, err := FromBirths(births, Options{Prefix: tc.prefix})
		if err != nil {
			t.Fatalf("prefix %q: %v", tc.prefix, err)
		}
		if m.Tags[0].Name != tc.want {
			t.Errorf("prefix %q → %q, want %q", tc.prefix, m.Tags[0].Name, tc.want)
		}
		// The companion tags keep a prefix even under --prefix none: they
		// have to stay unique per node.
		if m.Nodes[0].OnlineTag == "__Online" {
			t.Errorf("prefix %q: companion tag lost its prefix", tc.prefix)
		}
	}
}

func TestLiteralPrefixRejectsTwoNodes(t *testing.T) {
	if _, err := FromBirths(sampleBirths(t), Options{Prefix: "Site1"}); err == nil {
		t.Fatal("want an error: two nodes cannot share one literal prefix")
	}
}

func TestLayoutStructRejected(t *testing.T) {
	_, err := FromBirths(sampleBirths(t), Options{Layout: LayoutStruct})
	if err == nil || !strings.Contains(err.Error(), "not yet supported") {
		t.Fatalf("err = %v, want a \"not yet supported\" rejection", err)
	}
	if _, err := FromBirths(sampleBirths(t), Options{Layout: "nested"}); err == nil {
		t.Fatal("want an error for an unknown layout")
	}
}

func TestGroupsMustNotSpan(t *testing.T) {
	births := append(sampleBirths(t), host.Birth{
		Group: "Other", EdgeNode: "W9", Payload: wire(t, sparkplug.Payload{
			Metrics: []sparkplug.Metric{{Name: "X", Datatype: spb.DataType_Double, Value: 1.0}},
		}),
	})
	if _, err := FromBirths(births, Options{}); err == nil {
		t.Fatal("want an error for births spanning two groups")
	}
}

func TestSkipPatternMustMatch(t *testing.T) {
	m, err := FromBirths(sampleBirths(t), sampleOptions())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := TagsYAML(m, []string{"W6__Online"}); err != nil {
		t.Fatalf("skipping a real tag: %v", err)
	}
	if _, err := TagsYAML(m, []string{"W6__Online", "W9_*"}); err == nil {
		t.Fatal("want an error: a skip pattern matching nothing is a stale exclusion")
	}
	out, err := TagsYAML(m, []string{"W6__Online"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "name: W6__Online,") {
		t.Errorf("skipped tag still generated:\n%s", out)
	}
}

// TestUnrepresentableDatatypeReported: a metric nautilus has no value shape
// for is skipped and REPORTED, never silently dropped.
func TestUnrepresentableDatatypeReported(t *testing.T) {
	var skipped []string
	p := sparkplug.Payload{Metrics: []sparkplug.Metric{
		{Name: "Good", Datatype: spb.DataType_Double, Value: 1.0},
		{Name: "Blob", Datatype: spb.DataType_Bytes, IsNull: true},
	}}
	m, err := FromBirths([]host.Birth{{Group: "G", EdgeNode: "W6", Payload: wire(t, p)}},
		Options{OnSkip: func(s string) { skipped = append(skipped, s) }})
	if err != nil {
		t.Fatalf("FromBirths: %v", err)
	}
	if len(m.Tags) != 1 || m.Tags[0].Metric != "Good" {
		t.Errorf("tags = %+v", m.Tags)
	}
	if len(skipped) != 1 || !strings.Contains(skipped[0], "Blob") {
		t.Errorf("skipped = %v", skipped)
	}
}

// ── writable members ─────────────────────────────────────────────────────

// bindingNamed finds one binding by nautilus tag name.
func bindingNamed(t *testing.T, m host.Manifest, name string) host.Binding {
	t.Helper()
	for _, b := range m.Tags {
		if b.Name == name {
			return b
		}
	}
	t.Fatalf("no binding named %q in %d bindings", name, len(m.Tags))
	return host.Binding{}
}

// TestWritableWholeTemplateIsAnError — a --writable glob matching a Template
// metric fails LOUD. Writing the struct back would clobber every member the
// edge is driving, so the fix is to name the member, and the message says so.
func TestWritableWholeTemplateIsAnError(t *testing.T) {
	_, err := FromBirths(sampleBirths(t), Options{Writable: []string{"Pump1"}})
	if err == nil {
		t.Fatal("want an error: a Template metric cannot be written as a whole")
	}
	for _, want := range []string{"templates are written per member", "Pump1.Speed"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %v should contain %q", err, want)
		}
	}
	// A --sites file saying writable: true about a Template fails the same way.
	specs, err := ParseSites([]byte(`
group: G
types: [{name: Motor, fields: [{name: Speed, type: Double}]}]
sites:
  - node: W6
    metrics:
      - {name: Pump1, type: Motor, writable: true}
`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := FromSites(specs, Options{}); err == nil ||
		!strings.Contains(err.Error(), "templates are written per member") {
		t.Errorf("FromSites(writable: true on a Template) = %v, want the per-member error", err)
	}
}

// TestWritableMemberGlobs — a dotted pattern selects template MEMBERS, one
// scalar output tag each, and leaves the metric's own struct binding an input.
func TestWritableMemberGlobs(t *testing.T) {
	m, err := FromBirths(sampleBirths(t), Options{Writable: []string{"Pump1.*", "*.Main.Speed"}})
	if err != nil {
		t.Fatalf("FromBirths: %v", err)
	}

	// The struct binding is untouched: reads still come from it.
	if b := bindingNamed(t, m, "W6_Pump1"); b.Writable || b.Member != "" {
		t.Errorf("W6_Pump1 = %+v, want a plain input binding", b)
	}
	// Pump1.* takes every leaf of Motor, on both sites that carry a Pump1.
	for _, name := range []string{"W6_Pump1_Speed", "W6_Pump1_Run", "W7_Pump1_Speed", "W7_Pump1_Run"} {
		b := bindingNamed(t, m, name)
		if !b.Writable || b.Metric != "Pump1" || b.Type != "Motor" {
			t.Errorf("%s = %+v, want a writable member of Pump1 : Motor", name, b)
		}
	}
	// A nested path: "Skid A" : Skid → Main : Motor → Speed.
	if b := bindingNamed(t, m, "W6_Skid_A_Main_Speed"); b.Member != "Main.Speed" || b.Type != "Skid" {
		t.Errorf("W6_Skid_A_Main_Speed = %+v, want member Main.Speed of Skid", b)
	}
	// "*" does not cross ".", so Pump1.* never reaches into a nested template
	// and *.Main.Speed never matches a flat one.
	for _, b := range m.Tags {
		if b.Member == "Main.Run" {
			t.Errorf("*.Main.Speed matched %q too", b.Member)
		}
	}
}

// TestSitesWritableMemberList — the offline path names members explicitly, and
// a path the type does not have is a typo, not a silently missing tag.
func TestSitesWritableMemberList(t *testing.T) {
	const src = `
group: G
types:
  - {name: Motor, fields: [{name: Speed, type: Double}, {name: START, type: Boolean}]}
sites:
  - node: W6
    metrics:
      - {name: Motor1, type: Motor, writable: [START]}
`
	specs, err := ParseSites([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	m, err := FromSites(specs, Options{})
	if err != nil {
		t.Fatalf("FromSites: %v", err)
	}
	if b := bindingNamed(t, m, "W6_Motor1_START"); b.Member != "START" || !b.Writable {
		t.Errorf("W6_Motor1_START = %+v", b)
	}
	if b := bindingNamed(t, m, "W6_Motor1"); b.Writable {
		t.Error("the struct binding must stay an input")
	}
	// Speed was not listed, so no tag for it.
	for _, b := range m.Tags {
		if b.Member == "Speed" {
			t.Error("an unlisted member generated a tag")
		}
	}
	// The generated tag file types the member by its LEAF, not the struct.
	out, err := TagsYAML(m, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := "- { name: W6_Motor1_START, role: output, init: false }"; !strings.Contains(string(out), want) {
		t.Errorf("tag file missing %q:\n%s", want, out)
	}

	bad := strings.Replace(src, "writable: [START]", "writable: [Nope]", 1)
	specs, err = ParseSites([]byte(bad))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := FromSites(specs, Options{}); err == nil ||
		!strings.Contains(err.Error(), `has no scalar member "Nope"`) {
		t.Errorf("FromSites(bad member) = %v, want a no-such-member error", err)
	}
}

// TestSitesWritableRejectsBadShapes — writable: must be `true` or a list of
// member paths, and a member list on a scalar metric is a mistake.
func TestSitesWritableRejectsBadShapes(t *testing.T) {
	cases := []struct{ name, src, want string }{{
		name: "a bare string",
		src: `
group: G
sites: [{node: W6, metrics: [{name: SP, type: Double, writable: "yes"}]}]
`,
		want: "want `true` for a scalar metric",
	}, {
		name: "a member list on a scalar",
		src: `
group: G
sites: [{node: W6, metrics: [{name: SP, type: Double, writable: [START]}]}]
`,
		want: "not a Template",
	}, {
		name: "init with a member list",
		src: `
group: G
types: [{name: Motor, fields: [{name: Speed, type: Double}]}]
sites: [{node: W6, metrics: [{name: M, type: Motor, writable: [Speed], init: 1.0}]}]
`,
		want: "init: applies to a whole-metric writable",
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			specs, err := ParseSites([]byte(tc.src))
			if err == nil {
				_, err = FromSites(specs, Options{})
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}
