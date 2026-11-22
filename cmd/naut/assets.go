package main

import (
	"encoding/json"
	"strings"

	"github.com/joyautomation/nautilus/devbind"
	"github.com/joyautomation/nautilus/devprofile"
	"github.com/joyautomation/nautilus/internal/plant"
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
	inPlant := map[string]bool{}
	if proj.Plant != nil {
		for _, e := range proj.Plant.Equipment {
			if e.Device != "" {
				inPlant[strings.ToLower(e.Device)] = true
			}
		}
	}
	for _, d := range proj.Devices().DevicePoints() {
		if inPlant[strings.ToLower(d.Device)] {
			continue // the plant's equipment for it makes the page
		}
		if b, err := json.Marshal(deviceAsset(d)); err == nil {
			out = append(out, b)
		}
	}
	return out
}

type assetReading struct {
	Tag      string   `json:"tag"`
	Label    string   `json:"label,omitempty"`
	Unit     string   `json:"unit,omitempty"`
	Decimals *int     `json:"decimals,omitempty"`
	Min      *float64 `json:"min,omitempty"`
	Max      *float64 `json:"max,omitempty"`
	On       string   `json:"on,omitempty"`
	Off      string   `json:"off,omitempty"`
	Alarm    bool     `json:"alarm,omitempty"`
}

type assetCommand struct {
	Tag   string `json:"tag"`
	Label string `json:"label"`
}

type assetSetpoint struct {
	Tag      string   `json:"tag"`
	Label    string   `json:"label,omitempty"`
	Unit     string   `json:"unit,omitempty"`
	Decimals *int     `json:"decimals,omitempty"`
	Step     *float64 `json:"step,omitempty"`
	Min      *float64 `json:"min,omitempty"`
	Max      *float64 `json:"max,omitempty"`
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

// plantAssets are the asset pages the plant model makes (#714): one per
// equipment, titled and placed by the plant. A device's page is made from
// its profile's points; a tag's from its asset type, each entry's {tag}
// filled with the equipment's tag.
func plantAssets(proj *project.Project) []json.RawMessage {
	pl := proj.Plant
	if pl == nil {
		return nil
	}
	areas := map[string]string{}
	for _, a := range pl.Areas {
		label := a.Label
		if label == "" {
			label = a.ID
		}
		areas[strings.ToLower(a.ID)] = label
	}
	devices := map[string]devbind.DevicePoints{}
	for _, d := range proj.Devices().DevicePoints() {
		devices[strings.ToLower(d.Device)] = d
	}
	var out []json.RawMessage
	for _, e := range pl.Equipment {
		var a map[string]any
		key := tagRoot(e.Tag)
		if e.Device != "" {
			d, ok := devices[strings.ToLower(e.Device)]
			if !ok {
				continue // naut check says so
			}
			a = deviceAsset(d)
			key = e.Device
		} else if t := pl.Type(e.Type); t != nil {
			a = map[string]any{"panel": typePanel(t, e.Tag)}
		} else {
			continue
		}
		title := e.Label
		if title == "" {
			title = e.ID
		}
		a["id"], a["title"], a["equipment"] = e.ID, title, key
		if p, ok := a["panel"].(map[string]any); ok {
			p["title"] = title
		}
		if t := pl.Type(e.Type); t != nil && len(t.Trend) > 0 && e.Tag != "" {
			var tr []string
			for _, tpl := range t.Trend {
				tr = append(tr, plant.Expand(tpl, e.Tag))
			}
			a["trend"] = tr
		}
		var sub []string
		if e.Class != "" {
			sub = append(sub, e.Class)
		} else if t := pl.Type(e.Type); t != nil && t.Class != "" {
			sub = append(sub, t.Class)
		}
		if label, ok := areas[strings.ToLower(e.Area)]; ok {
			a["area"] = label
			sub = append(sub, label)
		}
		if len(sub) > 0 {
			a["subtitle"] = strings.Join(sub, " · ")
		}
		if b, err := json.Marshal(a); err == nil {
			out = append(out, b)
		}
	}
	return out
}

// typePanel is an asset type's panel for the equipment whose tag is tag.
func typePanel(t *plant.AssetType, tag string) map[string]any {
	x := func(s string) string { return plant.Expand(s, tag) }
	var readings []assetReading
	for _, r := range t.Readings {
		readings = append(readings, assetReading{Tag: x(r.Tag), Label: r.Label, Unit: r.Unit, Decimals: r.Decimals, Min: r.Min, Max: r.Max, On: r.On, Off: r.Off, Alarm: r.Alarm})
	}
	panel := map[string]any{"readings": readings}
	if len(t.Setpoints) > 0 {
		var sp []assetSetpoint
		for _, s := range t.Setpoints {
			sp = append(sp, assetSetpoint{Tag: x(s.Tag), Label: s.Label, Unit: s.Unit, Decimals: s.Decimals, Step: s.Step, Min: s.Min, Max: s.Max})
		}
		panel["setpoints"] = sp
	}
	if len(t.Commands) > 0 {
		var cs []map[string]any
		for _, c := range t.Commands {
			m := map[string]any{"tag": x(c.Tag), "label": c.Label}
			if c.Kind != "" {
				m["kind"] = c.Kind
			}
			if c.Held != "" {
				m["held"] = x(c.Held)
			}
			cs = append(cs, m)
		}
		panel["commands"] = cs
	}
	if len(t.Toggles) > 0 {
		var ts []map[string]any
		for _, g := range t.Toggles {
			m := map[string]any{"tag": x(g.Tag), "label": g.Label}
			if g.Invert {
				m["invert"] = true
			}
			ts = append(ts, m)
		}
		panel["toggles"] = ts
	}
	return panel
}

// tagRoot is a tag path's first element: the alarm engine's default
// equipment for an alarm on Plant.Pump1.Fault is "Plant".
func tagRoot(tag string) string {
	if i := strings.IndexAny(tag, ".["); i > 0 {
		return tag[:i]
	}
	return tag
}
