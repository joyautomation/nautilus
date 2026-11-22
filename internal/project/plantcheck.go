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
	"github.com/joyautomation/nautilus/lang/ir"
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
// manifest has no plant:. rt gives the tags' types, so a path is checked down
// to the struct member (nil: to the tag only); alarmEquipment is every alarm
// definition's equipment key.
func (p *Project) CheckPlant(fsys fs.FS, rt *runtime.Runtime, alarmEquipment []string) (rep PlantReport, errs, warns []string) {
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
		case e.Tag != "":
			if why := p.pathProblem(rt, e.Tag); why != "" {
				errs = append(errs, fmt.Sprintf("plant: equipment %s: tag %s: %s", e.ID, e.Tag, why))
				continue // its type's entries would only say it again
			}
		case e.Device != "" && !devices[strings.ToLower(e.Device)]:
			errs = append(errs, fmt.Sprintf("plant: equipment %s: device %s is not a devices: row", e.ID, e.Device))
		}
		if t := pl.Type(e.Type); t != nil && e.Tag != "" {
			for _, tpl := range t.Templates() {
				name := plant.Expand(tpl, e.Tag)
				if !declared[strings.ToLower(root(name))] {
					errs = append(errs, fmt.Sprintf("plant: equipment %s (type %s): %s expands to %s, which is not a declared tag", e.ID, t.Type, tpl, name))
				} else if why := p.pathProblem(rt, name); why != "" {
					errs = append(errs, fmt.Sprintf("plant: equipment %s (type %s): %s expands to %s: %s", e.ID, t.Type, tpl, name, why))
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

// pathProblem walks a tag path (`A.B`, `A[3].B`) down the declared types and
// says what is wrong with it: a member the struct does not have (naming the
// ones it has), a member of something that is not a struct. "" when it is
// fine, or when the types are not known (rt nil, an array whose element is
// not known): a blank reading is the failure this exists to stop, so it
// errs toward saying so only when it is sure.
func (p *Project) pathProblem(rt *runtime.Runtime, path string) string {
	if rt == nil {
		return ""
	}
	segs := pathSegments(path)
	if len(segs) < 2 {
		return ""
	}
	t := p.tagType(rt, segs[0].name)
	at := segs[0].name
	if t == nil {
		return ""
	}
	for _, s := range segs[1:] {
		switch {
		case s.index:
			if t.Kind != ir.TypeArray || t.Elem == nil {
				return ""
			}
			t = t.Elem
			at += "[" + s.name + "]"
		default:
			if t.Kind != ir.TypeStruct || t.Struct == nil {
				return fmt.Sprintf("%s is not a struct, so it has no member %s", at, s.name)
			}
			f := memberOf(t.Struct, s.name)
			if f == nil {
				var names []string
				for _, sf := range t.Struct.Fields {
					names = append(names, sf.Name)
				}
				return fmt.Sprintf("%s has no member %s (it has %s)", at, s.name, strings.Join(names, ", "))
			}
			t = f.Type
			at += "." + f.Name
		}
		if t == nil {
			return ""
		}
	}
	return ""
}

type pathSeg struct {
	name  string
	index bool
}

// pathSegments splits "A.B[3].C" into A, B, [3], C.
func pathSegments(path string) []pathSeg {
	var out []pathSeg
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, pathSeg{name: cur.String()})
			cur.Reset()
		}
	}
	for i := 0; i < len(path); i++ {
		switch c := path[i]; c {
		case '.':
			flush()
		case '[':
			flush()
			j := strings.IndexByte(path[i:], ']')
			if j < 0 {
				return append(out, pathSeg{name: path[i:]})
			}
			out = append(out, pathSeg{name: path[i+1 : i+j], index: true})
			i += j
		default:
			cur.WriteByte(c)
		}
	}
	flush()
	return out
}

func memberOf(sd *ir.StructDef, name string) *ir.StructField {
	for i := range sd.Fields {
		if ir.SameName(sd.Fields[i].Name, name) {
			return &sd.Fields[i]
		}
	}
	return nil
}

// tagType is a declared tag's type, as the scene check reads it: its live
// value's struct, else its declared type name.
func (p *Project) tagType(rt *runtime.Runtime, name string) *ir.Type {
	for _, d := range p.Runtime.Tags {
		if !ir.SameName(d.Name, name) {
			continue
		}
		if v, err := rt.Tags().ReadGlobal(d.Name); err == nil {
			switch v.Kind {
			case ir.TypeStruct:
				if v.Struct != nil {
					return &ir.Type{Kind: ir.TypeStruct, Struct: v.Struct}
				}
			case ir.TypeArray:
				// element type unknown here: nothing past the index is claimed
			default:
				return &ir.Type{Kind: v.Kind} // a value, so no members
			}
		}
		if t, ok := rt.Types()[d.Type]; ok && d.Type != "" {
			return t
		}
		if t := elementary(d.Type); t != nil {
			return t
		}
		return nil
	}
	return nil
}

// elementary is the type of a tag declared with an elementary type name
// (BOOL, REAL, ...), so a member of it is said not to be one.
func elementary(name string) *ir.Type {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "BOOL", "BYTE", "WORD", "DWORD", "LWORD", "SINT", "INT", "DINT", "LINT", "USINT", "UINT", "UDINT", "ULINT", "REAL", "LREAL", "STRING", "TIME":
		return &ir.Type{Kind: ir.TypeInt}
	}
	return nil
}
