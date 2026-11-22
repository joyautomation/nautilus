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
		return rep, nil, nil
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

	// Alarms: an equipment key that names nothing in the plant is a
	// warning: the asset page of the equipment it means would not show it.
	known := map[string]bool{}
	for _, e := range pl.Equipment {
		known[strings.ToLower(e.ID)] = true
		known[strings.ToLower(e.Tag)] = true
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

	// Drawings: a `plant:` must name equipment; a drawn piece with a
	// panel and no `plant:` is a warning, once a file.
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
			switch {
			case e.Plant != "":
				if _, ok := pl.Find(e.Plant); !ok {
					errs = append(errs, fmt.Sprintf("%s: equipment %s: plant %q is not in the plant model", f, e.ID, e.Plant))
				}
			case len(e.Panel) > 0 && string(e.Panel) != "null":
				loose = append(loose, e.ID)
			}
		}
		if len(loose) > 0 {
			warns = append(warns, fmt.Sprintf("%s: %s %s a panel but no plant: (name its piece of the plant model so the asset page and the drawing are one thing)", f, fmt.Sprintf("equipment %s", strings.Join(loose, ", ")), have(len(loose))))
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
			if n.Plant == "" {
				continue
			}
			if _, ok := pl.Find(n.Plant); !ok {
				errs = append(errs, fmt.Sprintf("scene %s: node %s: plant %q is not in the plant model", f, n.ID, n.Plant))
			}
		}
	}
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
