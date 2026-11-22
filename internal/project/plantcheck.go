package project

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/joyautomation/nautilus/internal/mimicdoc"
	"github.com/joyautomation/nautilus/internal/plant"
	"github.com/joyautomation/nautilus/internal/scene"
	"github.com/joyautomation/nautilus/runtime"
)

// PlantReport is what CheckPlant counted, for the summary line.
type PlantReport struct {
	Areas, Equipment, Flows, Types int
}

// CheckPlant is `naut check` on the plant model (#714): its own rules, then
// what only the manifest can say: each row's tag or device exists, each of
// its asset type's tags expands to a declared tag, every drawing's `plant:`
// names a piece of equipment, and the alarms' `equipment:` keys and the
// drawn pieces that carry a panel agree with it. nil, nil, nil when the
// manifest has no plant:. alarmEquipment is every alarm definition's
// equipment key.
func (p *Project) CheckPlant(fsys fs.FS, alarmEquipment []string) (rep PlantReport, errs, warns []string) {
	pl := p.Plant
	if pl == nil {
		// no plant: a drawing's plant: names nothing, and its page is its own
		return rep, p.checkDrawingPlants(fsys, nil), nil
	}
	rep = PlantReport{Areas: len(pl.Areas), Equipment: len(pl.Equipment), Flows: len(pl.Flows), Types: len(pl.Types)}
	errs = append(errs, pl.Check()...)

	declared := map[string]bool{}
	for _, d := range p.Runtime.Tags {
		declared[strings.ToLower(d.Name)] = true
	}
	devices := map[string]bool{}
	for _, d := range p.devices.Rows() {
		devices[strings.ToLower(d.Name)] = true
	}
	root := func(name string) string {
		name = strings.TrimSpace(name)
		if i := strings.IndexAny(name, ".["); i > 0 {
			name = name[:i]
		}
		return name
	}
	for _, e := range pl.Equipment {
		switch {
		case e.Tag != "" && !declared[strings.ToLower(root(e.Tag))]:
			errs = append(errs, fmt.Sprintf("plant: equipment %s: tag %s is not declared%s", e.ID, e.Tag, nearestTag(root(e.Tag), p.Runtime.Tags)))
		case e.Device != "" && !devices[strings.ToLower(e.Device)]:
			errs = append(errs, fmt.Sprintf("plant: equipment %s: device %s is not a devices: row", e.ID, e.Device))
		}
		if t := pl.Type(e.Type); t != nil && e.Tag != "" {
			for _, tpl := range t.Templates() {
				name := plant.Expand(tpl, e.Tag)
				if !declared[strings.ToLower(root(name))] {
					errs = append(errs, fmt.Sprintf("plant: equipment %s (type %s): %s expands to %s, which is not a declared tag", e.ID, t.Type, tpl, name))
				}
			}
		}
	}

	// a device no plant equipment names keeps a page of its own, under its
	// name: a plant id with the same page would collide with it
	named := map[string]bool{}
	for _, e := range pl.Equipment {
		named[strings.ToLower(e.Device)] = true
	}
	for _, d := range p.devices.Rows() {
		if named[strings.ToLower(d.Name)] {
			continue
		}
		for _, e := range pl.Equipment {
			if plant.Slug(e.ID) == plant.Slug(d.Name) {
				errs = append(errs, fmt.Sprintf("plant: equipment %s and device %s would share the page /assets/%s (name the device in the plant, or rename one)", e.ID, d.Name, plant.Slug(e.ID)))
			}
		}
	}

	// Alarms: an equipment key that names nothing in the plant is a
	// warning: the asset page of the equipment it means would not show it.
	known := map[string]bool{}
	for _, e := range pl.Equipment {
		known[strings.ToLower(e.ID)] = true
		known[strings.ToLower(root(e.Tag))] = true
		known[strings.ToLower(e.Device)] = true
	}
	var stray []string
	seen := map[string]bool{}
	for _, k := range alarmEquipment {
		lk := strings.ToLower(k)
		if k == "" || seen[lk] || known[lk] || strings.HasPrefix(lk, "__nautilus") || strings.HasPrefix(lk, "nautilus.") {
			continue
		}
		seen[lk] = true
		stray = append(stray, k)
	}
	sort.Strings(stray)
	if len(stray) > 0 {
		warns = append(warns, fmt.Sprintf("plant: alarm equipment %s names nothing in the plant (an asset page lists the alarms of its equipment's id, tag or device)", strings.Join(stray, ", ")))
	}

	errs = append(errs, p.checkDrawingPlants(fsys, pl)...)
	warns = append(warns, p.drawingWarnings(fsys, pl)...)
	return rep, errs, warns
}

func have(n int) string {
	if n == 1 {
		return "has"
	}
	return "have"
}

// nearestTag is " (did you mean X?)" when a declared tag differs only in case.
func nearestTag(name string, tags []runtime.TagDef) string {
	for _, t := range tags {
		if strings.EqualFold(t.Name, name) {
			return " (did you mean " + t.Name + "?)"
		}
	}
	return ""
}

// checkDrawingPlants: every mimic equipment's and scene node's `plant:`
// must name equipment of pl; with no plant file (pl nil) none can.
func (p *Project) checkDrawingPlants(fsys fs.FS, pl *plant.Plant) []string {
	var errs []string
	named := func(id string) bool {
		if pl == nil {
			return false
		}
		_, ok := pl.Find(id)
		return ok
	}
	why := func(id string) string {
		if pl == nil {
			return fmt.Sprintf("plant %q: the manifest has no plant: (plant.yaml), so nothing names it", id)
		}
		return fmt.Sprintf("plant %q is not in the plant model", id)
	}
	files, _, _, _ := scanScreens(fsys)
	for _, f := range files {
		raw, err := fs.ReadFile(fsys, f)
		if err != nil {
			continue
		}
		if x, _, err := mimicdoc.ExpandUnits(raw); err == nil {
			raw = x
		}
		var m mimicFile
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		for _, e := range m.Equipment {
			if e.Plant != "" && !named(e.Plant) {
				errs = append(errs, fmt.Sprintf("%s: equipment %s: %s", f, e.ID, why(e.Plant)))
			}
		}
	}
	sfiles, _ := SceneFiles(fsys)
	for _, f := range sfiles {
		data, err := fs.ReadFile(fsys, f)
		if err != nil {
			continue
		}
		doc, err := scene.Parse(data)
		if err != nil {
			continue
		}
		for _, n := range doc.Nodes {
			if n.Plant != "" && !named(n.Plant) {
				errs = append(errs, fmt.Sprintf("scene %s: node %s: %s", f, n.ID, why(n.Plant)))
			}
		}
	}
	return errs
}

// drawingWarnings: a drawn piece with a panel and no `plant:`, once a file.
func (p *Project) drawingWarnings(fsys fs.FS, pl *plant.Plant) []string {
	var warns []string
	files, _, _, _ := scanScreens(fsys)
	for _, f := range files {
		raw, err := fs.ReadFile(fsys, f)
		if err != nil {
			continue
		}
		if x, _, err := mimicdoc.ExpandUnits(raw); err == nil {
			raw = x
		}
		var m mimicFile
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		var loose []string
		for _, e := range m.Equipment {
			if e.Plant == "" && len(e.Panel) > 0 && string(e.Panel) != "null" {
				loose = append(loose, e.ID)
			}
		}
		if len(loose) > 0 {
			warns = append(warns, fmt.Sprintf("%s: equipment %s %s a panel but no plant: (name its piece of the plant model so the asset page and the drawing are one thing)", f, strings.Join(loose, ", "), have(len(loose))))
		}
	}
	return warns
}
