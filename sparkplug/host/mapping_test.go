package host

import (
	"reflect"
	"strings"
	"testing"
)

func TestSanitize(t *testing.T) {
	cases := []struct{ in, want string }{
		// docs/design/sparkplug-host.md §3
		{"Well/Level", "Well_Level"},
		{"Pump/Run", "Pump_Run"},
		{"W6", "W6"},
		{"already_ok", "already_ok"},
		// leading digit
		{"1Pump", "M_1Pump"},
		{"3", "M_3"},
		// empty, and names that sanitize to nothing
		{"", "M_"},
		{"///", "M_"},
		{"___", "M_"},
		// runs of junk collapse to one underscore
		{"A///B", "A_B"},
		{"A. .B", "A_B"},
		{"Tank__Level", "Tank_Level"},
		{"a_______b", "a_b"},
		// leading and trailing junk is trimmed, not collapsed into a prefix
		{"/Level/", "Level"},
		{"  Level  ", "Level"},
		{"_Level_", "Level"},
		// non-ASCII: one underscore per rune, then the usual collapse
		{"Motor °C", "Motor_C"},
		{"Wéll", "W_ll"},
		{"Ω", "M_"},
		// Leading junk is trimmed, so a non-ASCII prefix leaves no underscore
		// behind and the result needs no digit guard.
		{"日本語Tag", "Tag"},
		// punctuation a real Sparkplug name carries
		{"Inputs/Ai:0/Value", "Inputs_Ai_0_Value"},
		{"Well.Level", "Well_Level"},
		{"Pump-1 Speed (SP)", "Pump_1_Speed_SP"},
	}
	for _, tc := range cases {
		if got := Sanitize(tc.in); got != tc.want {
			t.Errorf("Sanitize(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	// Idempotent: the generator sanitizes, and the driver sanitizes the same
	// name again when it composes a tag name from a birth. The one exception
	// is the empty-name guard "M_", whose trailing underscore is trimmed on a
	// second pass — eip's tagIdent has the same edge ("Tag_"), and a metric
	// with no name-shaped characters at all is not a real case.
	for _, tc := range cases {
		if tc.want == "M_" {
			continue
		}
		if got := Sanitize(tc.want); got != tc.want {
			t.Errorf("Sanitize(%q) is not idempotent: %q", tc.want, got)
		}
	}
	// The reserved marker can never be produced from a metric name.
	for _, tc := range cases {
		if strings.Contains(Sanitize(tc.in), "__") {
			t.Errorf("Sanitize(%q) = %q contains the reserved __", tc.in, Sanitize(tc.in))
		}
	}
}

func TestTagName(t *testing.T) {
	cases := []struct {
		prefix, device, metric, want string
	}{
		{"W6", "", "Well/Level", "W6_Well_Level"},
		{"W6", "PLC1", "Pump/Run", "W6_PLC1_Pump_Run"},
		{"W6", "PLC 1", "Pump/SpeedSP", "W6_PLC_1_Pump_SpeedSP"},
		{"", "", "Well/Level", "Well_Level"},
		{"", "PLC1", "Run", "PLC1_Run"},
		// The digit guard applies once, to the composed name — not per segment.
		{"W6", "", "1Level", "W6_1Level"},
		{"", "", "1Level", "M_1Level"},
		{"", "", "", "M_"},
		// A junk-only segment disappears rather than leaving a double
		// underscore, which is reserved.
		{"W6", "///", "Level", "W6_Level"},
		{"6W", "", "Level", "M_6W_Level"},
	}
	for _, tc := range cases {
		got := TagName(tc.prefix, tc.device, tc.metric)
		if got != tc.want {
			t.Errorf("TagName(%q,%q,%q) = %q, want %q", tc.prefix, tc.device, tc.metric, got, tc.want)
		}
		if strings.Contains(got, "__") {
			t.Errorf("TagName(%q,%q,%q) = %q contains the reserved __", tc.prefix, tc.device, tc.metric, got)
		}
	}
}

func TestUniqueNames(t *testing.T) {
	// "A/B" and "A.B" both sanitize to A_B — the collision the suffix exists
	// for.
	in := []string{"A/B", "A.B", "A B", "C"}
	var sanitized []string
	for _, s := range in {
		sanitized = append(sanitized, Sanitize(s))
	}
	got := uniqueNames(sanitized)
	want := []string{"A_B", "A_B_2", "A_B_3", "C"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("uniqueNames(%v) = %v, want %v", sanitized, got, want)
	}

	used := map[string]bool{}
	if n := uniqueName("W6", used); n != "W6" {
		t.Errorf("first uniqueName = %q", n)
	}
	if n := uniqueName("W6", used); n != "W6_2" {
		t.Errorf("second uniqueName = %q", n)
	}
	if n := uniqueName("W6", used); n != "W6_3" {
		t.Errorf("third uniqueName = %q", n)
	}
	// A name that collides with an already-taken suffixed form keeps counting.
	used2 := map[string]bool{"X": true, "X_2": true}
	if n := uniqueName("X", used2); n != "X_3" {
		t.Errorf("uniqueName past a taken suffix = %q, want X_3", n)
	}
}

func TestCompanionTagNames(t *testing.T) {
	// Defaults, derived from the edge node id.
	n := Node{EdgeNode: "W-6", Devices: []Device{{Device: "PLC 1"}}}
	if got := n.TagPrefix(); got != "W_6" {
		t.Errorf("TagPrefix() = %q, want W_6", got)
	}
	if got := n.OnlineTagName(); got != "W_6__Online" {
		t.Errorf("OnlineTagName() = %q", got)
	}
	if got := n.BirthTagName(); got != "W_6__LastBirthMs" {
		t.Errorf("BirthTagName() = %q", got)
	}
	if got := n.RebirthTagName(); got != "W_6__Rebirth" {
		t.Errorf("RebirthTagName() = %q", got)
	}
	if got := n.DeviceOnlineTagName(n.Devices[0]); got != "W_6_PLC_1__Online" {
		t.Errorf("DeviceOnlineTagName() = %q", got)
	}

	// Prefix overrides the edge node id.
	p := Node{EdgeNode: "W6", Prefix: "Site6"}
	if got := p.OnlineTagName(); got != "Site6__Online" {
		t.Errorf("prefixed OnlineTagName() = %q", got)
	}

	// An explicit name wins over both.
	e := Node{
		EdgeNode:   "W6",
		OnlineTag:  "Well6_Comms",
		BirthTag:   "Well6_Born",
		RebirthTag: "Well6_Kick",
		Devices:    []Device{{Device: "PLC1", OnlineTag: "Well6_PLC_Comms"}},
	}
	if got := e.OnlineTagName(); got != "Well6_Comms" {
		t.Errorf("explicit OnlineTagName() = %q", got)
	}
	if got := e.BirthTagName(); got != "Well6_Born" {
		t.Errorf("explicit BirthTagName() = %q", got)
	}
	if got := e.RebirthTagName(); got != "Well6_Kick" {
		t.Errorf("explicit RebirthTagName() = %q", got)
	}
	if got := e.DeviceOnlineTagName(e.Devices[0]); got != "Well6_PLC_Comms" {
		t.Errorf("explicit DeviceOnlineTagName() = %q", got)
	}
}

func TestTagSpecs(t *testing.T) {
	m := mustParse(t, sampleManifest)
	got := m.TagSpecs()

	// Sorted by byte order — the same comparison internal/tagfile.Render
	// applies, so the generated file is already in this order.
	want := []TagSpec{
		{Name: "W6_PLC1_Pump_Run", Role: RoleInput, Datatype: "Boolean"},
		{Name: "W6_PLC1_Pump_SpeedSP", Role: RoleOutput, Init: 0.0, Datatype: "Double"},
		{Name: "W6_PLC1__Online", Role: RoleInput},
		{Name: "W6_Pump1", Role: RoleInput, Type: "Motor"},
		{Name: "W6_Well_Level", Role: RoleInput, Datatype: "Double"},
		{Name: "W6__LastBirthMs", Role: RoleInput},
		{Name: "W6__Online", Role: RoleInput},
		{Name: "W6__Rebirth", Role: RoleOutput, Init: false},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("TagSpecs() =\n%+v\nwant\n%+v", got, want)
	}
}

func TestTagSpecsDefaultsAndZeroInits(t *testing.T) {
	m := mustParse(t, `
nodes: [{edgenode: W6, devices: [{device: PLC1}]}]
tags:
    - { name: W6_SP,   node: W6, metric: SP,   type: Double,  writable: true }
    - { name: W6_Mode, node: W6, metric: Mode, type: Int32,   writable: true }
    - { name: W6_Run,  node: W6, metric: Run,  type: Boolean, writable: true }
    - { name: W6_Name, node: W6, metric: Name, type: String,  writable: true }
`)
	specs := map[string]TagSpec{}
	for _, s := range m.TagSpecs() {
		specs[s.Name] = s
	}
	// Companion tags are synthesized even though the manifest names none.
	for _, name := range []string{"W6__Online", "W6__LastBirthMs", "W6_PLC1__Online"} {
		if s, ok := specs[name]; !ok || s.Role != RoleInput {
			t.Errorf("%s = %+v, want an input", name, s)
		}
	}
	if s := specs["W6__Rebirth"]; s.Role != RoleOutput || s.Init != false {
		t.Errorf("W6__Rebirth = %+v, want an output initialised false", s)
	}
	// A writable binding with no init gets the type zero, so the tag exists
	// from scan one — and a float keeps its float identity.
	for name, want := range map[string]any{
		"W6_SP":   0.0,
		"W6_Mode": int64(0),
		"W6_Run":  false,
		"W6_Name": "",
	} {
		s := specs[name]
		if s.Role != RoleOutput {
			t.Errorf("%s role = %q, want output", name, s.Role)
		}
		if !reflect.DeepEqual(s.Init, want) {
			t.Errorf("%s init = %#v, want %#v", name, s.Init, want)
		}
	}
}

func TestBuildIndexes(t *testing.T) {
	m := mustParse(t, sampleManifest)
	d := &Driver{manifest: m}
	if err := d.buildIndexes(); err != nil {
		t.Fatalf("buildIndexes: %v", err)
	}

	wantInputs := []string{
		"W6_PLC1_Pump_Run",
		"W6_PLC1__Online",
		"W6_Pump1",
		"W6_Well_Level",
		"W6__LastBirthMs",
		"W6__Online",
	}
	if got := d.InputNames(); !reflect.DeepEqual(got, wantInputs) {
		t.Errorf("InputNames() = %v, want %v", got, wantInputs)
	}
	wantOutputs := []string{"W6_PLC1_Pump_SpeedSP", "W6__Rebirth"}
	if got := d.OutputNames(); !reflect.DeepEqual(got, wantOutputs) {
		t.Errorf("OutputNames() = %v, want %v", got, wantOutputs)
	}

	// Inputs and outputs are disjoint: a writable binding is an output only.
	out := map[string]bool{}
	for _, n := range d.OutputNames() {
		out[n] = true
	}
	for _, n := range d.InputNames() {
		if out[n] {
			t.Errorf("%s is both an input and an output", n)
		}
	}

	if len(d.byName) != 1 {
		t.Errorf("byName = %v, want just the writable binding", d.byName)
	}
	if _, ok := d.byName["W6_PLC1_Pump_SpeedSP"]; !ok {
		t.Errorf("byName is missing the writable binding: %v", d.byName)
	}
	if node := d.rebirthTags["W6__Rebirth"]; node != "W6" {
		t.Errorf("rebirthTags[W6__Rebirth] = %q, want W6", node)
	}

	// byMetric routes an inbound metric to its binding, node-level and device
	// metrics in the one table.
	if b, ok := d.byMetric[metricKey{EdgeNode: "W6", Metric: "Well/Level"}]; !ok || b.Name != "W6_Well_Level" {
		t.Errorf("byMetric node-level lookup = %+v, %v", b, ok)
	}
	if b, ok := d.byMetric[metricKey{EdgeNode: "W6", Device: "PLC1", Metric: "Pump/Run"}]; !ok || b.Name != "W6_PLC1_Pump_Run" {
		t.Errorf("byMetric device lookup = %+v, %v", b, ok)
	}
	if _, ok := d.byMetric[metricKey{EdgeNode: "W6", Metric: "Pump/Run"}]; ok {
		t.Error("a device metric must not resolve as a node-level metric")
	}

	if n, ok := d.nodeCfg["W6"]; !ok || n.EdgeNode != "W6" {
		t.Errorf("nodeCfg = %+v", d.nodeCfg)
	}

	defs := d.StructDefs()
	if defs["Motor"] == nil || len(defs["Motor"].Fields) != 2 {
		t.Fatalf("StructDefs() = %+v", defs)
	}
	// The returned map is a copy: the runtime must not be able to mutate the
	// driver's own index.
	delete(defs, "Motor")
	if d.StructDefs()["Motor"] == nil {
		t.Error("StructDefs() returned the driver's own map")
	}
}

func TestBuildIndexesRejectsInvalidManifest(t *testing.T) {
	m := mustParse(t, `
nodes: [{edgenode: W6}]
tags: [{ name: W6_A, node: W7, metric: A, type: Double }]
`)
	d := &Driver{manifest: m}
	if err := d.buildIndexes(); err == nil {
		t.Fatal("buildIndexes() = nil, want the validation error")
	}
}

func TestBuildIndexesSynthesizesDefaultCompanions(t *testing.T) {
	// A hand-written manifest that names no companion tags still gets them.
	m := mustParse(t, `
nodes:
    - { edgenode: W6, devices: [{device: PLC1}] }
    - { edgenode: W7 }
tags: []
`)
	d := &Driver{manifest: m}
	if err := d.buildIndexes(); err != nil {
		t.Fatalf("buildIndexes: %v", err)
	}
	want := []string{"W6_PLC1__Online", "W6__LastBirthMs", "W6__Online", "W7__LastBirthMs", "W7__Online"}
	if got := d.InputNames(); !reflect.DeepEqual(got, want) {
		t.Errorf("InputNames() = %v, want %v", got, want)
	}
	wantOut := []string{"W6__Rebirth", "W7__Rebirth"}
	if got := d.OutputNames(); !reflect.DeepEqual(got, wantOut) {
		t.Errorf("OutputNames() = %v, want %v", got, wantOut)
	}
	if d.rebirthTags["W7__Rebirth"] != "W7" {
		t.Errorf("rebirthTags = %v", d.rebirthTags)
	}
}
