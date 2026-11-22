package project

import (
	"strings"
	"testing"
	"testing/fstest"
)

func plantProject(t *testing.T, plantYAML string, extra map[string]string) (*Project, fstest.MapFS) {
	t.Helper()
	file := func(s string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(s)} }
	fsys := fstest.MapFS{
		"nautilus.yaml": file(`name: p
plant: plant.yaml
tasks:
  - program: main.st
    scan: 100ms
tags:
  - { name: P101, type: BOOL, role: state }
  - { name: P101_Start, type: BOOL, role: setpoint }
  - { name: TK101, type: REAL, role: input }
`),
		"main.st":    file("PROGRAM main\nEND_PROGRAM\n"),
		"plant.yaml": file(plantYAML),
		"asset-types/Pump.asset.yaml": file(`type: Pump
readings: [{ tag: "{tag}", label: Running, on: Running, off: Stopped }]
commands: [{ tag: "{tag}_Start", label: Start, kind: start }]
`),
		"asset-types/Tank.asset.yaml": file("type: Tank\nreadings: [{ tag: \"{tag}\", unit: m }]\n"),
	}
	for n, s := range extra {
		fsys[n] = file(s)
	}
	p, err := Load(fsys, ManifestName)
	if err != nil {
		t.Fatal(err)
	}
	return p, fsys
}

const okPlant = `
areas: [{ id: Receiving }]
equipment:
  - { id: P101, type: Pump, area: Receiving, tag: P101 }
  - { id: TK101, type: Tank, area: Receiving, tag: TK101 }
`

// A manifest without plant: is untouched: no plant, no message.
func TestNoPlantNoCheck(t *testing.T) {
	fsys := fstest.MapFS{"nautilus.yaml": {Data: []byte("name: p\ntasks:\n  - program: main.st\n    scan: 100ms\n")}, "main.st": {Data: []byte("PROGRAM main\nEND_PROGRAM\n")}}
	p, err := Load(fsys, ManifestName)
	if err != nil {
		t.Fatal(err)
	}
	rep, errs, warns := p.CheckPlant(fsys, nil, []string{"Whatever"})
	if p.Plant != nil || len(errs)+len(warns) != 0 || rep.Equipment != 0 {
		t.Errorf("a project with no plant: got %+v %v %v", rep, errs, warns)
	}
}

func TestAGoodPlantChecksClean(t *testing.T) {
	p, fsys := plantProject(t, okPlant, nil)
	rep, errs, warns := p.CheckPlant(fsys, nil, []string{"P101", "TK101"})
	if len(errs)+len(warns) != 0 {
		t.Fatalf("errors %v warnings %v", errs, warns)
	}
	if rep.Equipment != 2 || rep.Areas != 1 || rep.Types != 2 {
		t.Errorf("report %+v", rep)
	}
}

func TestPlantAgainstTheManifest(t *testing.T) {
	p, fsys := plantProject(t, `
equipment:
  - { id: P101, type: Pump, tag: P101 }
  - { id: P102, type: Pump, tag: P102 }
  - { id: TK101, type: Tank, tag: tk101 }
  - { id: CONV1, device: CONV1 }
`, nil)
	_, errs, _ := p.CheckPlant(fsys, nil, nil)
	all := strings.Join(errs, "\n")
	for _, want := range []string{
		"equipment P102: tag P102 is not declared",
		"equipment P102 (type Pump): {tag}_Start expands to P102_Start, which is not a declared tag",
		"equipment CONV1: device CONV1 is not a devices: row",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("errors lack %q:\n%s", want, all)
		}
	}
	// a tag that differs only in case is declared (tag names compare as tag names do)
	if strings.Contains(all, "tk101 is not declared") {
		t.Errorf("tk101 names TK101: %s", all)
	}
}

func TestAlarmEquipmentAndDrawingsAgreeWithThePlant(t *testing.T) {
	p, fsys := plantProject(t, okPlant, map[string]string{
		"screens/a.mimic.json": `{"equipment":[
			{"id":"P101","component":"Pump","plant":"P101","panel":{"readings":[]}},
			{"id":"X","component":"Pump","plant":"Nope"},
			{"id":"Loose","component":"Pump","panel":{"readings":[]}},
			{"id":"V","component":"Valve"}]}`,
		"a.scene.json": `{"nodes":[{"id":"n1","kind":"tank","plant":"Ghost","pos":[0,0,0]}]}`,
	})
	_, errs, warns := p.CheckPlant(fsys, nil, []string{"P101", "Skid", "p101", "__nautilus"})
	all, w := strings.Join(errs, "\n"), strings.Join(warns, "\n")
	for _, want := range []string{`equipment X: plant "Nope" is not in the plant model`, `node n1: plant "Ghost" is not in the plant model`} {
		if !strings.Contains(all, want) {
			t.Errorf("errors lack %q:\n%s", want, all)
		}
	}
	if strings.Contains(all, "equipment P101") || strings.Contains(all, "equipment V") {
		t.Errorf("a named piece and a piece with no panel are fine:\n%s", all)
	}
	for _, want := range []string{"alarm equipment Skid names nothing in the plant", "equipment Loose has a panel but no plant:"} {
		if !strings.Contains(w, want) {
			t.Errorf("warnings lack %q:\n%s", want, w)
		}
	}
	if strings.Contains(w, "P101,") || strings.Contains(w, "__nautilus") {
		t.Errorf("the plant's own id (any case) and the controller's alarms are not strays: %s", w)
	}
}

// An alarm's equipment key is the tag's or the device's name as often as the
// plant id (a rule defaults it to the tag), and none of them is a stray.
func TestAlarmEquipmentMayBeTheTagNotTheID(t *testing.T) {
	p, fsys := plantProject(t, "equipment:\n  - { id: Feed, type: Pump, tag: P101 }\n", nil)
	_, _, warns := p.CheckPlant(fsys, nil, []string{"P101", "Feed"})
	if len(warns) != 0 {
		t.Errorf("warnings %v: P101 is Feed's tag", warns)
	}
}

// With no plant: a drawing's plant: names nothing: an error, not silence.
func TestADrawingNamesAPlantTheManifestDoesNotHave(t *testing.T) {
	file := func(x string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(x)} }
	fsys := fstest.MapFS{
		"nautilus.yaml": file("name: p\ntasks:\n  - program: main.st\n    scan: 100ms\n"),
		"main.st":       file("PROGRAM main\nEND_PROGRAM\n"),
		"a.mimic.json":  file(`{"equipment":[{"id":"P1","component":"Pump","plant":"P101","panel":{}}]}`),
	}
	p, err := Load(fsys, ManifestName)
	if err != nil {
		t.Fatal(err)
	}
	_, errs, _ := p.CheckPlant(fsys, nil, nil)
	if all := strings.Join(errs, "\n"); !strings.Contains(all, "the manifest has no plant:") {
		t.Errorf("errors %q", all)
	}
}

// A dotted tag's alarms key on its root, as the engine defaults them.
func TestAlarmEquipmentKeysOnTheTagRoot(t *testing.T) {
	p, fsys := plantProject(t, "equipment:\n  - { id: Feed, type: Tank, tag: TK101.Inner }\n", nil)
	_, _, warns := p.CheckPlant(fsys, nil, []string{"TK101"})
	if len(warns) != 0 {
		t.Errorf("warnings %v: the alarm key is the tag's root TK101", warns)
	}
}

// A path is checked down to the struct member (#714): a misspelt member is
// an error that names the members there are, not a blank reading.
func TestPlantPathsAreCheckedToTheMember(t *testing.T) {
	fsys := alarmFS("name: p\nplant: plant.yaml\n"+alarmTags, map[string]string{
		"plant.yaml": `
equipment:
  - { id: FIT1, type: Meter, tag: RTU9_ZONE15_FIT_001 }
  - { id: Bad, type: Meter, tag: RTU9_ZONE15_FIT_001.Nope }
  - { id: Flat, type: Meter, tag: Duty }
`,
		"asset-types/Meter.asset.yaml": `type: Meter
readings:
  - { tag: "{tag}.PV", unit: m3 }
  - { tag: "{tag}.pv" }
  - { tag: "{tag}.HHH", label: Wrong }
`,
	})
	proj, rt := loadAlarms(t, fsys)
	_, errs, _ := proj.CheckPlant(fsys, rt, nil)
	all := strings.Join(errs, "\n")
	for _, want := range []string{
		"equipment FIT1 (type Meter): {tag}.HHH expands to RTU9_ZONE15_FIT_001.HHH: RTU9_ZONE15_FIT_001 has no member HHH (it has PV, HH, L)",
		"equipment Bad: tag RTU9_ZONE15_FIT_001.Nope: RTU9_ZONE15_FIT_001 has no member Nope",
		"equipment Flat (type Meter): {tag}.PV expands to Duty.PV: Duty is not a struct, so it has no member PV",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("errors lack %q:\n%s", want, all)
		}
	}
	if strings.Contains(all, "FIT1 (type Meter): {tag}.PV expands") || strings.Contains(all, "FIT1 (type Meter): {tag}.pv expands") {
		t.Errorf("PV is a member (member names compare as names do):\n%s", all)
	}
	// a bit of a value (Duty.3) is not claimed either way
	if _, errs, _ := (func() (PlantReport, []string, []string) {
		fs2 := alarmFS("name: p\nplant: plant.yaml\n"+alarmTags, map[string]string{
			"plant.yaml":                  "equipment:\n  - { id: D, type: Bits, tag: Duty }\n",
			"asset-types/Bits.asset.yaml": "type: Bits\nreadings:\n  - { tag: \"{tag}.3\" }\n",
		})
		pr, rt2 := loadAlarms(t, fs2)
		return pr.CheckPlant(fs2, rt2, nil)
	})(); strings.Contains(strings.Join(errs, "\n"), "no member 3") {
		t.Errorf("Duty.3 is a bit, not a member: %v", errs)
	}
	// without types (nil rt) nothing past the tag is claimed
	if _, errs, _ := proj.CheckPlant(fsys, nil, nil); strings.Contains(strings.Join(errs, "\n"), "no member") {
		t.Errorf("no runtime, no member claims: %v", errs)
	}
}

func TestPathSegments(t *testing.T) {
	for in, want := range map[string]string{
		"A":           "A",
		"A.B":         "A,B",
		"A[3].B.C":    "A,[3],B,C",
		"Tags[0]":     "Tags,[0]",
		"A.B[1][2].C": "A,B,[1],[2],C",
	} {
		var got []string
		for _, s := range pathSegments(in) {
			if s.index {
				got = append(got, "["+s.name+"]")
			} else {
				got = append(got, s.name)
			}
		}
		if strings.Join(got, ",") != want {
			t.Errorf("%s -> %v, want %s", in, got, want)
		}
	}
}
