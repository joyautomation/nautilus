package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/joyautomation/nautilus/devbind"
	"github.com/joyautomation/nautilus/devprofile"
	"github.com/joyautomation/nautilus/internal/project"
)

// A device row becomes an asset descriptor (#714): readings for what the
// device answers, commands and setpoints for what the controller writes,
// through the row's command tags.
func TestDeviceAssetSplitsReadingsFromWrites(t *testing.T) {
	d := devbind.DevicePoints{Device: "CONV", Title: "Drive", Desc: "Conveyor drive", Points: []devprofile.Point{
		{Name: "Speed", Type: "INT", Unit: "rpm", Desc: "Actual speed"},
		{Name: "Running", Type: "BOOL"},
		{Name: "SpeedRef", Type: "REAL", Unit: "rpm", Writable: true, Desc: "Speed reference"},
		{Name: "Start", Type: "BOOL", Writable: true},
	}}
	b, err := json.Marshal(deviceAsset(d))
	if err != nil {
		t.Fatal(err)
	}
	var a struct {
		ID        string
		Title     string
		Subtitle  string
		Equipment string
		Panel     struct {
			Readings []struct {
				Tag, Label, Unit, On, Off string
				Decimals                  *int
			}
			Commands  []struct{ Tag, Label string }
			Setpoints []struct {
				Tag, Label, Unit string
				Decimals         *int
			}
		}
	}
	if err := json.Unmarshal(b, &a); err != nil {
		t.Fatal(err)
	}
	if a.ID != "CONV" || a.Title != "Conveyor drive" || a.Subtitle != "Drive" || a.Equipment != "CONV" {
		t.Errorf("identity %+v", a)
	}
	var reads []string
	for _, r := range a.Panel.Readings {
		reads = append(reads, r.Tag)
	}
	if strings.Join(reads, ",") != "CONV__Online,CONV.Speed,CONV.Running" {
		t.Errorf("readings %v: the device's communication, then what it answers (members by path), never what is written", reads)
	}
	if r := a.Panel.Readings[1]; r.Unit != "rpm" || r.Decimals == nil || *r.Decimals != 0 || r.Label != "Actual speed" {
		t.Errorf("an INT reading is whole numbers with its unit: %+v", r)
	}
	if r := a.Panel.Readings[2]; r.On != "On" || r.Off != "Off" {
		t.Errorf("a BOOL reading has words: %+v", r)
	}
	if len(a.Panel.Commands) != 1 || a.Panel.Commands[0].Tag != "CONV_Start" {
		t.Errorf("commands %+v, want Start through its command tag CONV_Start", a.Panel.Commands)
	}
	if len(a.Panel.Setpoints) != 1 || a.Panel.Setpoints[0].Tag != "CONV_SpeedRef" || *a.Panel.Setpoints[0].Decimals != 1 {
		t.Errorf("setpoints %+v, want SpeedRef through CONV_SpeedRef with a decimal", a.Panel.Setpoints)
	}
}

// With a real profile and a row, through devbind: one descriptor each.
func TestDeviceAssetsFromARow(t *testing.T) {
	raw, err := os.ReadFile("../../devprofile/profiles/abb-acs355-fmbt21.yaml")
	if err != nil {
		t.Skip("no builtin profile:", err)
	}
	set, err := devbind.Resolve(fstest.MapFS{"profiles/abb-acs355-fmbt21.yaml": {Data: raw}},
		[]devbind.Device{{Name: "CONV", Profile: "abb-acs355-fmbt21", Endpoint: "10.0.0.5"}})
	if err != nil {
		t.Fatal(err)
	}
	pts := set.DevicePoints()
	if len(pts) != 1 || pts[0].Device != "CONV" || len(pts[0].Points) == 0 {
		t.Fatalf("device points %+v", pts)
	}
	if deviceAsset(pts[0])["id"] != "CONV" {
		t.Errorf("asset %v", deviceAsset(pts[0]))
	}
}

func plantTestProject(t *testing.T) *project.Project { return plantTestProjectWith(t, "") }

// plantTestProjectWith is the same project with more equipment in its plant.yaml.
func plantTestProjectWith(t *testing.T, moreEquipment string) *project.Project {
	t.Helper()
	raw, err := os.ReadFile("../../devprofile/profiles/abb-acs355-fmbt21.yaml")
	if err != nil {
		t.Skip("no builtin profile:", err)
	}
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
  - { name: P101_Flow, type: REAL, role: input }
devices:
  - { name: CONV, profile: abb-acs355-fmbt21, endpoint: "127.0.0.1:15502" }
  - { name: CONV2, profile: abb-acs355-fmbt21, endpoint: "127.0.0.1:15503" }
`),
		"main.st":                         file("PROGRAM main\nEND_PROGRAM\n"),
		"profiles/abb-acs355-fmbt21.yaml": {Data: raw},
		"plant.yaml": file(`
areas: [{ id: Receiving, label: "Receiving dock" }]
equipment:
  - { id: P101, type: Pump, area: Receiving, label: "Transfer pump", tag: P101 }
  - { id: Belt1, class: ConveyorBelt, area: Receiving, device: CONV }
` + moreEquipment),
		"asset-types/Pump.asset.yaml": file(`type: Pump
class: CentrifugalPump
readings:
  - { tag: "{tag}", label: State, on: Running, off: Stopped }
  - { tag: "{tag}_Flow", label: Flow, unit: gpm, decimals: 0, min: 0, max: 500 }
commands: [{ tag: "{tag}_Start", label: Start, kind: start }]
trend: ["{tag}_Flow"]
`),
	}
	p, err := project.Load(fsys, project.ManifestName)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// The plant model makes the asset pages (#714 slice 2): a tag's from its
// asset type with {tag} filled in, a device's from its profile, each titled,
// placed and keyed by the plant; a device the plant names makes no second
// page of its own.
func TestPlantAssets(t *testing.T) {
	p := plantTestProject(t)
	list := plantAssets(p)
	if len(list) != 2 {
		t.Fatalf("%d assets, want the pump and the belt", len(list))
	}
	var pump struct {
		ID, Title, Subtitle, Area, Equipment string
		Trend                                []string
		Panel                                struct {
			Title    string
			Readings []struct {
				Tag      string
				Max      *float64
				Decimals *int
			}
			Commands []struct{ Tag, Kind string }
		}
	}
	if err := json.Unmarshal(list[0], &pump); err != nil {
		t.Fatal(err)
	}
	if pump.ID != "P101" || pump.Title != "Transfer pump" || pump.Panel.Title != "Transfer pump" || pump.Area != "Receiving dock" || pump.Equipment != "P101" {
		t.Errorf("identity %+v", pump)
	}
	if pump.Subtitle != "CentrifugalPump · Receiving dock" {
		t.Errorf("subtitle %q: the class, then the area", pump.Subtitle)
	}
	if len(pump.Panel.Readings) != 2 || pump.Panel.Readings[1].Tag != "P101_Flow" || pump.Panel.Readings[1].Max == nil || *pump.Panel.Readings[1].Max != 500 {
		t.Errorf("readings %+v: {tag} filled in, the range kept", pump.Panel.Readings)
	}
	if len(pump.Panel.Commands) != 1 || pump.Panel.Commands[0].Tag != "P101_Start" || pump.Panel.Commands[0].Kind != "start" {
		t.Errorf("commands %+v", pump.Panel.Commands)
	}
	if len(pump.Trend) != 1 || pump.Trend[0] != "P101_Flow" {
		t.Errorf("trend %v", pump.Trend)
	}
	var belt struct {
		ID, Title, Subtitle, Equipment string
		Panel                          struct{ Readings []struct{ Tag string } }
	}
	if err := json.Unmarshal(list[1], &belt); err != nil {
		t.Fatal(err)
	}
	if belt.ID != "Belt1" || belt.Title != "Belt1" || belt.Equipment != "CONV" || belt.Subtitle != "ConveyorBelt · Receiving dock" {
		t.Errorf("belt %+v", belt)
	}
	if len(belt.Panel.Readings) < 2 || belt.Panel.Readings[0].Tag != "CONV__Online" {
		t.Errorf("a device's page comes from its profile: %+v", belt.Panel.Readings)
	}
	// CONV is the plant's Belt1; CONV2 is in no plant equipment and keeps its own page
	var ids []string
	for _, d := range deviceAssets(p) {
		var a struct{ ID string }
		_ = json.Unmarshal(d, &a)
		ids = append(ids, a.ID)
	}
	if strings.Join(ids, ",") != "CONV2" {
		t.Errorf("device pages %v: CONV has its plant page, CONV2 keeps its own", ids)
	}
}

func TestNoPlantNoPlantAssets(t *testing.T) {
	p := &project.Project{}
	if got := plantAssets(p); got != nil {
		t.Errorf("a project with no plant makes %d", len(got))
	}
}

func TestTagRootIsTheAlarmEnginesDefaultEquipment(t *testing.T) {
	for in, want := range map[string]string{"P101": "P101", "Plant.Pump1": "Plant", "Tags[3].Run": "Tags"} {
		if got := tagRoot(in); got != want {
			t.Errorf("tagRoot(%q) = %q, want %q", in, got, want)
		}
	}
}

// A plant id whose page is the page of a device no plant equipment names is
// refused: the two would be one page (#714).
func TestPlantIDAgainstAnUnnamedDevicesPage(t *testing.T) {
	p := plantTestProjectWith(t, "  - { id: conv2, type: Pump, tag: P101 }\n")
	_, errs, _ := p.CheckPlant(fstest.MapFS{}, nil, nil)
	if all := strings.Join(errs, "\n"); !strings.Contains(all, "equipment conv2 and device CONV2 would share the page /assets/conv2") {
		t.Errorf("errors %q", all)
	}
	p = plantTestProject(t)
	if _, errs, _ := p.CheckPlant(fstest.MapFS{}, nil, nil); len(errs) != 0 {
		t.Errorf("the plain test plant: %v", errs)
	}
}

// A dotted tag's page keys its alarms on the tag's root, as the engine
// defaults an alarm's equipment.
func TestPlantAssetKeysAlarmsOnTheTagRoot(t *testing.T) {
	p := plantTestProjectWith(t, "  - { id: Dot1, type: Pump, tag: P101.Inner }\n")
	var found bool
	for _, raw := range plantAssets(p) {
		var a struct{ ID, Equipment string }
		_ = json.Unmarshal(raw, &a)
		if a.ID == "Dot1" {
			found = true
			if a.Equipment != "P101" {
				t.Errorf("equipment key %q, want the tag's root P101", a.Equipment)
			}
		}
	}
	if !found {
		t.Fatal("no page for Dot1")
	}
}

func TestPlantAreasListTheEquipmentThatHasAPage(t *testing.T) {
	p := plantTestProject(t)
	assets := plantAssets(p)
	areas := plantAreas(p, assets)
	if len(areas) != 1 {
		t.Fatalf("%d areas", len(areas))
	}
	var a struct {
		ID, Label string
		Assets    []string
	}
	if err := json.Unmarshal(areas[0], &a); err != nil {
		t.Fatal(err)
	}
	if a.ID != "Receiving" || a.Label != "Receiving dock" || strings.Join(a.Assets, ",") != "P101,Belt1" {
		t.Errorf("area %+v: the equipment with a page, in the plant's order", a)
	}
	if got := plantAreas(p, nil); len(got) != 1 || strings.Contains(string(got[0]), "P101") {
		t.Errorf("with no pages the area lists none: %s", got)
	}
	if plantAreas(&project.Project{}, nil) != nil {
		t.Error("no plant, no areas")
	}
}

// A profile's derived members are readings (words, not numbers), and a
// member the profile raises an alarm on is flagged so the screen shows it
// as abnormal; the command path is the row's command tags and nothing else.
func TestDeviceAssetShowsDerivedMembersAndFlagsAlarmed(t *testing.T) {
	bit := 3
	d := devbind.DevicePoints{
		Device: "CONV", Title: "Drive", Desc: "Conveyor drive",
		Points: []devprofile.Point{
			{Name: "StatusWord", Type: "WORD"},
			{Name: "Fault", Type: "BOOL"},
			{Name: "ControlWord", Type: "WORD", Writable: true},
		},
		Derived: []devprofile.Derived{{Name: "Tripped", Desc: "The drive has tripped", From: "StatusWord", Bit: &bit}},
		Alarms:  map[string]bool{"tripped": true, "fault": true},
	}
	b, _ := json.Marshal(deviceAsset(d))
	var a struct {
		Panel struct {
			Readings []struct {
				Tag, Label, On, Off string
				Alarm               bool
			}
			Commands  []struct{ Tag string }
			Setpoints []struct{ Tag string }
		}
	}
	if err := json.Unmarshal(b, &a); err != nil {
		t.Fatal(err)
	}
	var reads []string
	alarmed := map[string]bool{}
	for _, r := range a.Panel.Readings {
		reads = append(reads, r.Tag)
		alarmed[r.Tag] = r.Alarm
	}
	if strings.Join(reads, ",") != "CONV__Online,CONV.StatusWord,CONV.Fault,CONV.Tripped" {
		t.Errorf("readings %v, want the derived member after the points", reads)
	}
	last := a.Panel.Readings[len(a.Panel.Readings)-1]
	if last.Label != "The drive has tripped" || last.On != "Yes" || last.Off != "No" {
		t.Errorf("derived reading %+v", last)
	}
	if !alarmed["CONV.Tripped"] || !alarmed["CONV.Fault"] || alarmed["CONV.StatusWord"] || alarmed["CONV__Online"] {
		t.Errorf("alarm flags %v: only the members the profile alarms on", alarmed)
	}
	if len(a.Panel.Commands) != 0 || len(a.Panel.Setpoints) != 1 || a.Panel.Setpoints[0].Tag != "CONV_ControlWord" {
		t.Errorf("writes %+v %+v: only the row's command tags", a.Panel.Commands, a.Panel.Setpoints)
	}
}

// End to end from a project: the built-in ABB profile's Tripped shows on the
// device's page and is flagged as alarmed.
func TestDeviceAssetFromAProfileCarriesItsTrippedAlarm(t *testing.T) {
	prof, err := os.ReadFile("../../devprofile/profiles/abb-acs355-fmbt21.yaml")
	if err != nil {
		t.Fatal(err)
	}
	set, err := devbind.Resolve(fstest.MapFS{"profiles/abb-acs355-fmbt21.yaml": {Data: prof}},
		[]devbind.Device{{Name: "CONV", Profile: "abb-acs355-fmbt21", Endpoint: "h", Points: []string{"Tripped", "Current"}}})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(deviceAsset(set.DevicePoints()[0]))
	if !strings.Contains(string(b), `"tag":"CONV.Tripped"`) || !strings.Contains(string(b), `"alarm":true`) {
		t.Errorf("asset %s", b)
	}
}
