package codegen

// The byte-for-byte goldens live with the command
// (cmd/naut/modbus_test.go, testdata/modbus). What is pinned here is
// the SEMANTICS: the map parses strictly, the generator is deterministic,
// and — the load-bearing one — the rendered manifest decodes back through
// modbus.LoadManifest into exactly the Manifest that was rendered.

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/modbus"
)

const sampleMap = `
devices:
  temp-controller:
    description: PID loop behind a gateway
    scan-class: slow
    registers:
      - {tag: Tpv, address: 0, format: int32, scale: 0.1, unit: degC, desc: process temperature}
      - {tag: Tsp, address: 262, format: int32, scale: 0.1, writable: true, rewrite: 2.5s, init: 0.0, desc: setpoint}
      - {tag: AlmH, address: 12, table: discrete, desc: high alarm}
  analyser:
    word-order: little
    timeout: 2s
    max-block: 64
    registers:
      - {tag: CH1, address: 0, format: float32, unit: ppm}
      - {tag: Scaled, address: 2, scale: 1, offset: -40}
instances:
  - {id: TC_A, type: temp-controller, host: 10.0.0.10, unit-id: 1, enable-tag: HeatersOn, desc: zone A}
  - {id: TC_B, type: temp-controller, host: 10.0.0.10, unit-id: 2, enable-tag: HeatersOn, desc: zone B}
  - {id: GAS, type: analyser, host: 10.0.0.51, unit-id: 1}
`

func parse(t *testing.T, src string) DeviceMap {
	t.Helper()
	dm, err := ParseDeviceMap([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return dm
}

func generate(t *testing.T, src string, opts Options) Output {
	t.Helper()
	out, err := Generate(parse(t, src), opts)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	return out
}

func TestParseRejectsUnknownKey(t *testing.T) {
	bad := strings.Replace(sampleMap, "enable-tag:", "enable-tga:", 1)
	if _, err := ParseDeviceMap([]byte(bad)); err == nil || !strings.Contains(err.Error(), "enable-tga") {
		t.Errorf("a typo'd key did not error usefully: %v", err)
	}
}

// Every finding at once: a map with three problems is three lines, not
// three edit-run cycles.
func TestParseReportsEverything(t *testing.T) {
	_, err := ParseDeviceMap([]byte(`
devices:
  x:
    registers:
      - {tag: A, address: 0}
      - {tag: A, address: 1}
instances:
  - {id: D1, type: x, host: 10.0.0.1}
  - {id: D1, type: nope, host: 10.0.0.2}
  - {id: D2, type: x}
`))
	if err == nil {
		t.Fatal("a broken map parsed cleanly")
	}
	for _, want := range []string{`duplicate register tag "A"`, `duplicate instance id "D1"`, `unknown device type "nope"`, "D2: missing host"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in:\n%v", want, err)
		}
	}
}

// The one that matters: the rendered YAML decodes through the core's
// strict loader into exactly what was rendered — no struct tags to drift,
// no silently dropped key.
func TestManifestRoundTrips(t *testing.T) {
	out := generate(t, sampleMap, Options{})
	raw := ManifestYAML(out.Manifest, "naut modbus import --map devices.yaml")
	loaded, err := modbus.ParseManifest(raw)
	if err != nil {
		t.Fatalf("the generated manifest does not load:\n%s\n%v", raw, err)
	}
	if !reflect.DeepEqual(loaded, out.Manifest) {
		t.Errorf("round trip drifted:\n rendered %#v\n loaded   %#v", out.Manifest, loaded)
	}
	if err := loaded.Validate(); err != nil {
		t.Errorf("generated manifest invalid: %v", err)
	}
	if _, err := modbus.BuildPlan(loaded, -1); err != nil {
		t.Errorf("generated manifest does not plan: %v", err)
	}
}

func TestGenerateDeterministic(t *testing.T) {
	a := generate(t, sampleMap, Options{})
	b := generate(t, sampleMap, Options{})
	if !reflect.DeepEqual(a.Manifest, b.Manifest) {
		t.Errorf("two generations differ")
	}
	ya := ManifestYAML(a.Manifest, "cmd")
	yb := ManifestYAML(b.Manifest, "cmd")
	if string(ya) != string(yb) {
		t.Errorf("two renders differ:\n%s\n---\n%s", ya, yb)
	}
	ta, err := TagsYAML(a.Manifest, a.Meta, nil)
	if err != nil {
		t.Fatal(err)
	}
	tb, err := TagsYAML(b.Manifest, b.Meta, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(ta) != string(tb) {
		t.Errorf("two tag files differ")
	}
}

func TestGenerateAppliesOverridesAndDefaults(t *testing.T) {
	out := generate(t, sampleMap, Options{})
	byID := map[string]modbus.Source{}
	for _, s := range out.Manifest.Sources {
		byID[s.ID] = s
	}
	// Gateway drops: same host, different unit-id, each its own source.
	if byID["TC_A"].UnitID != 1 || byID["TC_B"].UnitID != 2 || byID["TC_A"].Host != byID["TC_B"].Host {
		t.Errorf("gateway drops wrong: %+v %+v", byID["TC_A"], byID["TC_B"])
	}
	if byID["TC_A"].Enable != "HeatersOn" {
		t.Errorf("enable-tag lost: %+v", byID["TC_A"])
	}
	gas := byID["GAS"]
	if gas.WordOrder != "little" || gas.Timeout != 2*time.Second || gas.MaxBlock != 64 {
		t.Errorf("type-level source settings lost: %+v", gas)
	}

	byName := map[string]modbus.TagBinding{}
	for _, b := range out.Manifest.Tags {
		byName[b.Name] = b
	}
	tsp := byName["TC_A_Tsp"]
	if !tsp.Writable || tsp.Rewrite != 2500*time.Millisecond || tsp.ScanClass != "slow" {
		t.Errorf("Tsp binding wrong: %+v", tsp)
	}
	if alm := byName["TC_A_AlmH"]; alm.Table != modbus.TableDiscrete || alm.Format != "bool" {
		t.Errorf("bit-table default format wrong: %+v", alm)
	}
	if sc := byName["GAS_Scaled"]; sc.Format != "uint16" || sc.Scale != 0 || sc.Offset != -40 {
		// scale: 1 normalizes away (0 means 1 in the manifest shape).
		t.Errorf("register defaults wrong: %+v", sc)
	}
	if out.Meta["TC_A_Tpv"].Desc != "zone A — process temperature" {
		t.Errorf("desc composition wrong: %q", out.Meta["TC_A_Tpv"].Desc)
	}
	if out.Meta["TC_A_Tsp"].Init != 0.0 {
		t.Errorf("init lost: %#v", out.Meta["TC_A_Tsp"].Init)
	}
}

func TestGenerateInstanceFilter(t *testing.T) {
	out := generate(t, sampleMap, Options{Instance: "GAS"})
	if len(out.Manifest.Sources) != 1 || out.Manifest.Sources[0].ID != "GAS" {
		t.Errorf("instance filter kept %+v", out.Manifest.Sources)
	}
	for _, b := range out.Manifest.Tags {
		if b.Source != "GAS" {
			t.Errorf("stray binding %+v", b)
		}
	}
	if _, err := Generate(parse(t, sampleMap), Options{Instance: "NOPE"}); err == nil || !strings.Contains(err.Error(), "NOPE") {
		t.Errorf("unknown instance did not error usefully: %v", err)
	}
}

func TestGenerateWritableAndTagGlobs(t *testing.T) {
	out := generate(t, sampleMap, Options{Writable: []string{"GAS_CH*"}, Tags: []string{"GAS_*"}})
	if len(out.Manifest.Tags) != 2 {
		t.Fatalf("tag filter kept %+v", out.Manifest.Tags)
	}
	for _, b := range out.Manifest.Tags {
		if b.Name == "GAS_CH1" && !b.Writable {
			t.Errorf("--writable glob ignored: %+v", b)
		}
	}
}

func TestTagsYAMLRolesAndSkip(t *testing.T) {
	out := generate(t, sampleMap, Options{})
	raw, err := TagsYAML(out.Manifest, out.Meta, []string{"TC_B_*"})
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, want := range []string{
		`- { name: TC_A_Tpv, role: input, unit: "degC", desc: "zone A — process temperature" }`,
		`- { name: TC_A_Tsp, role: output, init: 0.0, desc: "zone A — setpoint" }`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("tag file missing:\n  %s\ngot:\n%s", want, body)
		}
	}
	if strings.Contains(body, "name: TC_B_") {
		t.Errorf("skip glob ignored:\n%s", body)
	}
	// A stale exclusion must be loud, not a silent regeneration.
	if _, err := TagsYAML(out.Manifest, out.Meta, []string{"Zz*"}); err == nil {
		t.Errorf("a skip pattern matching nothing passed silently")
	}
}
