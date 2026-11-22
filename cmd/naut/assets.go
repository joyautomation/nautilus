package main

import (
	"encoding/json"
	"strings"

	"github.com/joyautomation/nautilus/devbind"
	"github.com/joyautomation/nautilus/devprofile"
	"github.com/joyautomation/nautilus/internal/project"
)

// deviceAssets are the asset pages (#714) a project's devices: rows make:
// one descriptor per device, for the HMI kit's <AssetPage/>. What the
// device answers is a reading (a true/false as words, a number with its
// unit), what the controller may write is a command (a BOOL) or a setpoint
// (a number), through the command tag the row declares. A device with no
// rows makes none.
func deviceAssets(proj *project.Project) []json.RawMessage {
	var out []json.RawMessage
	for _, d := range proj.Devices().DevicePoints() {
		if b, err := json.Marshal(deviceAsset(d)); err == nil {
			out = append(out, b)
		}
	}
	return out
}

type assetReading struct {
	Tag      string `json:"tag"`
	Label    string `json:"label,omitempty"`
	Unit     string `json:"unit,omitempty"`
	Decimals *int   `json:"decimals,omitempty"`
	On       string `json:"on,omitempty"`
	Off      string `json:"off,omitempty"`
	Alarm    bool   `json:"alarm,omitempty"`
}

type assetCommand struct {
	Tag   string `json:"tag"`
	Label string `json:"label"`
}

type assetSetpoint struct {
	Tag      string `json:"tag"`
	Label    string `json:"label,omitempty"`
	Unit     string `json:"unit,omitempty"`
	Decimals *int   `json:"decimals,omitempty"`
}

func deviceAsset(d devbind.DevicePoints) map[string]any {
	var readings []assetReading
	var commands []assetCommand
	var setpoints []assetSetpoint
	readings = append(readings, assetReading{Tag: d.Device + "__Online", Label: "Communication", On: "Answering", Off: "Not answering"})
	for _, pt := range d.Points {
		label := pointLabel(pt)
		member := d.Device + "." + pt.Name
		if pt.Writable {
			cmd := devbind.CommandTag(d.Device, pt.Name)
			if strings.EqualFold(pt.Type, "BOOL") {
				commands = append(commands, assetCommand{Tag: cmd, Label: label})
			} else {
				setpoints = append(setpoints, assetSetpoint{Tag: cmd, Label: label, Unit: pt.Unit, Decimals: decimalsOf(pt)})
			}
			continue
		}
		r := assetReading{Tag: member, Label: label, Unit: pt.Unit}
		if strings.EqualFold(pt.Type, "BOOL") {
			r.On, r.Off = "On", "Off"
		} else {
			r.Decimals = decimalsOf(pt)
		}
		readings = append(readings, r)
	}
	title := d.Desc
	if title == "" {
		title = d.Device
	}
	panel := map[string]any{"title": title, "readings": readings}
	if len(commands) > 0 {
		panel["commands"] = commands
	}
	if len(setpoints) > 0 {
		panel["setpoints"] = setpoints
	}
	return map[string]any{"id": d.Device, "title": title, "subtitle": d.Title, "equipment": d.Device, "panel": panel}
}

// pointLabel is a point's words: its description, else its name.
func pointLabel(pt devprofile.Point) string {
	if pt.Desc != "" {
		return pt.Desc
	}
	return pt.Name
}

// decimalsOf: a whole-number type shows none, a REAL one.
func decimalsOf(pt devprofile.Point) *int {
	n := 1
	switch strings.ToUpper(pt.Type) {
	case "INT", "UINT", "DINT", "UDINT", "WORD", "DWORD", "SINT", "USINT", "LINT", "ULINT", "BYTE":
		n = 0
	}
	return &n
}
