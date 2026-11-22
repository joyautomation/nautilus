package plant

import (
	"strings"
	"testing"
	"testing/fstest"
)

const goodPlant = `
areas:
  - { id: Receiving, label: Receiving }
equipment:
  - { id: P101, type: Pump, class: CentrifugalPump, area: Receiving, tag: P101, nozzles: [N1, N2] }
  - { id: TK101, type: Tank, area: Receiving, tag: TK101, nozzles: [N1, N2] }
  - { id: CONV1, area: Receiving, device: CONV1 }
flows:
  - { from: TK101.N2, to: P101.N1, medium: water }
`

const pumpType = `
type: Pump
class: CentrifugalPump
readings:
  - { tag: "{tag}.Running", label: State, on: Running, off: Stopped }
commands:
  - { tag: "{tag}_Start", label: Start, kind: start, held: "{tag}.Running" }
trend: ["{tag}.Flow"]
`

func plantFS(files map[string]string) fstest.MapFS {
	fsys := fstest.MapFS{}
	for n, s := range files {
		fsys[n] = &fstest.MapFile{Data: []byte(s)}
	}
	return fsys
}

func good(t *testing.T) *Plant {
	t.Helper()
	p, err := Read(plantFS(map[string]string{
		"plant.yaml":                  goodPlant,
		"asset-types/Pump.asset.yaml": pumpType,
		"asset-types/Tank.asset.yaml": "type: Tank\nreadings: [{ tag: \"{tag}.Level\", unit: m }]\n",
	}), "plant.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAGoodPlantChecksClean(t *testing.T) {
	p := good(t)
	if m := p.Check(); len(m) != 0 {
		t.Fatalf("a good plant: %v", m)
	}
	if e, ok := p.Find("p101"); !ok || e.Class != "CentrifugalPump" {
		t.Errorf("find p101 (case aside): %+v %v", e, ok)
	}
	if p.Type("pump") == nil || p.Type("PUMP").Class != "CentrifugalPump" {
		t.Error("asset type lookup, case aside")
	}
}

func TestThePlantsOwnRules(t *testing.T) {
	for name, tc := range map[string]struct {
		yaml, want string
	}{
		"duplicate id":        {"equipment: [{id: A, type: Pump, tag: A}, {id: a, type: Pump, tag: B}]", "declared twice"},
		"bad id":              {"equipment: [{id: \"1x\", type: Pump, tag: A}]", "an id is a letter"},
		"neither tag nor dev": {"equipment: [{id: A, type: Pump}]", "exactly one of tag"},
		"both tag and device": {"equipment: [{id: A, type: Pump, tag: A, device: D}]", "exactly one of tag"},
		"unknown area":        {"equipment: [{id: A, type: Pump, tag: A, area: Nowhere}]", `area "Nowhere" is not declared`},
		"unknown type":        {"equipment: [{id: A, type: Mixer, tag: A}]", `no asset type "Mixer"`},
		"tag with no type":    {"equipment: [{id: A, tag: A}]", "needs a type"},
		"flow to nothing":     {"equipment: [{id: A, type: Pump, tag: A}]\nflows: [{from: A, to: Z}]", `no equipment "Z"`},
		"flow wrong nozzle":   {"equipment: [{id: A, type: Pump, tag: A, nozzles: [N1]}, {id: B, type: Pump, tag: B}]\nflows: [{from: A.N9, to: B}]", `has no nozzle "N9"`},
		"flow skips nozzle":   {"equipment: [{id: A, type: Pump, tag: A, nozzles: [N1]}, {id: B, type: Pump, tag: B}]\nflows: [{from: A, to: B}]", "say which"},
		"area twice":          {"areas: [{id: X}, {id: x}]", "declared twice"},
	} {
		p, err := Read(plantFS(map[string]string{"plant.yaml": tc.yaml, "asset-types/Pump.asset.yaml": pumpType}), "plant.yaml")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got := strings.Join(p.Check(), "\n")
		if !strings.Contains(got, tc.want) {
			t.Errorf("%s: %q does not say %q", name, got, tc.want)
		}
	}
}

func TestAssetTypeRules(t *testing.T) {
	p, err := Read(plantFS(map[string]string{
		"plant.yaml":                  "areas: []",
		"asset-types/Pump.asset.yaml": "type: Pump\nreadings: [{ tag: Flow }]\ncommands: [{ tag: \"{tag}_Go\", label: Go, kind: shout }]\n",
	}), "plant.yaml")
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(p.Check(), "\n")
	for _, want := range []string{`"Flow" does not use {tag}`, `kind "shout"`} {
		if !strings.Contains(got, want) {
			t.Errorf("asset type check %q lacks %q", got, want)
		}
	}
}

func TestUnknownKeysAreErrorsAndATypeMatchesItsFile(t *testing.T) {
	if _, err := Parse([]byte("equipmnt: []"), "plant.yaml"); err == nil {
		t.Error("a misspelt key must not be a silently empty plant")
	}
	_, err := Read(plantFS(map[string]string{"plant.yaml": "areas: []", "asset-types/Pump.asset.yaml": "type: Tank\n"}), "plant.yaml")
	if err == nil || !strings.Contains(err.Error(), "does not match the file name") {
		t.Errorf("type/file mismatch: %v", err)
	}
}

func TestTemplatesAndRefs(t *testing.T) {
	p := good(t)
	got := strings.Join(p.Type("Pump").Templates(), ",")
	if got != "{tag}.Running,{tag}_Start,{tag}.Flow" {
		t.Errorf("templates %s: each once, readings first, held and trend included", got)
	}
	if Expand("{tag}_Start", "P101") != "P101_Start" {
		t.Error("expand")
	}
	if r := ParseRef("P101.N2"); r.Equipment != "P101" || r.Nozzle != "N2" {
		t.Errorf("ref %+v", r)
	}
	if r := ParseRef(" TK1 "); r.Equipment != "TK1" || r.Nozzle != "" {
		t.Errorf("ref %+v", r)
	}
}
