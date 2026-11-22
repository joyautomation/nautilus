package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/joyautomation/nautilus/devbind"
	"github.com/joyautomation/nautilus/devprofile"
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
