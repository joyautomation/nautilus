// Package plant is the plant model (#714, docs/design/plant-model.md):
// plant.yaml, the one list of what is in the plant, what kind each piece is,
// where it is and what feeds what, and the asset types that say what a
// piece's page shows. Mimics and 3D scenes draw it and reference its ids.
//
// A published language like devprofile: it reads and checks its own file and
// knows no manifest, tag, device or alarm. Whether a row's tag exists is for
// the manifest to say (internal/project), which hands the checks it can make
// here the sets they need.
package plant

import (
	"bytes"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Plant is a plant.yaml.
type Plant struct {
	Areas     []Area      `yaml:"areas"`
	Equipment []Equipment `yaml:"equipment"`
	Flows     []Flow      `yaml:"flows"`

	// Types are the asset types read from asset-types/, by name.
	Types map[string]*AssetType `yaml:"-"`
}

// Area is a part of the plant: a page lists its equipment.
type Area struct {
	ID    string `yaml:"id"`
	Label string `yaml:"label"`
}

// Equipment is one piece of the plant.
type Equipment struct {
	// ID names it: a letter, then letters, digits and underscores. A
	// drawing's `plant:` and an alarm's `equipment:` use it.
	ID string `yaml:"id"`
	// Type is its asset type (asset-types/<Type>.asset.yaml); "" takes the
	// page a device's profile gives it.
	Type string `yaml:"type"`
	// Class is the DEXPI-style class name ("CentrifugalPump", "Tank"):
	// optional, informative, kept so a DEXPI importer is a mapping.
	Class string `yaml:"class"`
	Area  string `yaml:"area"`
	Label string `yaml:"label"`
	// Tag is the declared tag (usually a UDT instance) it is; Device the
	// devices: row it is. Exactly one.
	Tag    string `yaml:"tag"`
	Device string `yaml:"device"`
	// Nozzles names its connection points; flows may end on one. A piece
	// that names none takes flows on the piece itself.
	Nozzles []string `yaml:"nozzles"`
}

// Flow is material (or signal) going from one piece to another, through a
// nozzle at each end where the piece names them.
type Flow struct {
	From   string `yaml:"from"`
	To     string `yaml:"to"`
	Medium string `yaml:"medium"`
}

// Ref is a flow end: the equipment and the nozzle ("" for the piece itself).
type Ref struct{ Equipment, Nozzle string }

// ParseRef reads "P101" or "P101.N2".
func ParseRef(s string) Ref {
	eq, noz, _ := strings.Cut(strings.TrimSpace(s), ".")
	return Ref{Equipment: eq, Nozzle: noz}
}

// AssetType says what a page of one type shows. Each entry's Tag is a
// template in which {tag} is the equipment's tag.
type AssetType struct {
	Type      string     `yaml:"type"`
	Class     string     `yaml:"class"`
	Readings  []Reading  `yaml:"readings"`
	Setpoints []Setpoint `yaml:"setpoints"`
	Commands  []Command  `yaml:"commands"`
	Toggles   []Toggle   `yaml:"toggles"`
	// Trend names the tags (templates) the page trends; empty takes the
	// first numeric readings.
	Trend []string `yaml:"trend"`
}

// Reading is a live value: a number with its unit, or a true/false as words.
type Reading struct {
	Tag      string   `yaml:"tag"`
	Label    string   `yaml:"label"`
	Unit     string   `yaml:"unit"`
	Decimals *int     `yaml:"decimals"`
	Min      *float64 `yaml:"min"`
	Max      *float64 `yaml:"max"`
	On       string   `yaml:"on"`
	Off      string   `yaml:"off"`
	Alarm    bool     `yaml:"alarm"`
}

// Setpoint is a number an operator may write.
type Setpoint struct {
	Tag      string   `yaml:"tag"`
	Label    string   `yaml:"label"`
	Unit     string   `yaml:"unit"`
	Decimals *int     `yaml:"decimals"`
	Step     *float64 `yaml:"step"`
	Min      *float64 `yaml:"min"`
	Max      *float64 `yaml:"max"`
}

// Command is a momentary button; Held names the tag that reads it back.
type Command struct {
	Tag   string `yaml:"tag"`
	Label string `yaml:"label"`
	Kind  string `yaml:"kind"` // start | stop | neutral
	Held  string `yaml:"held"`
}

// Toggle is a switch that holds a bit.
type Toggle struct {
	Tag    string `yaml:"tag"`
	Label  string `yaml:"label"`
	Invert bool   `yaml:"invert"`
}

var identRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)

// Parse reads a plant.yaml: unknown keys are errors, so a misspelt one is
// not silently a missing equipment.
func Parse(data []byte, src string) (*Plant, error) {
	var p Plant
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&p); err != nil && err.Error() != "EOF" {
		return nil, fmt.Errorf("%s: %w", src, err)
	}
	p.Types = map[string]*AssetType{}
	return &p, nil
}

// ParseAssetType reads an asset-types/<Type>.asset.yaml.
func ParseAssetType(data []byte, src string) (*AssetType, error) {
	var t AssetType
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&t); err != nil {
		return nil, fmt.Errorf("%s: %w", src, err)
	}
	return &t, nil
}

// Read reads the plant file at name in fsys and the asset types in the
// asset-types directory beside it.
func Read(fsys fs.FS, name string) (*Plant, error) {
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		return nil, err
	}
	p, err := Parse(data, name)
	if err != nil {
		return nil, err
	}
	dir := path.Join(path.Dir(name), "asset-types")
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return p, nil // no asset types: only device-backed equipment then
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".asset.yaml") {
			continue
		}
		file := path.Join(dir, e.Name())
		raw, err := fs.ReadFile(fsys, file)
		if err != nil {
			return nil, err
		}
		t, err := ParseAssetType(raw, file)
		if err != nil {
			return nil, err
		}
		want := strings.TrimSuffix(e.Name(), ".asset.yaml")
		if t.Type == "" {
			t.Type = want
		}
		if t.Type != want {
			return nil, fmt.Errorf("%s: type %q does not match the file name (%s.asset.yaml)", file, t.Type, t.Type)
		}
		p.Types[strings.ToLower(t.Type)] = t
	}
	return p, nil
}

// Type is the asset type a name means, case aside.
func (p *Plant) Type(name string) *AssetType { return p.Types[strings.ToLower(name)] }

// Find is the equipment with the id, case aside.
func (p *Plant) Find(id string) (Equipment, bool) {
	for _, e := range p.Equipment {
		if strings.EqualFold(e.ID, id) {
			return e, true
		}
	}
	return Equipment{}, false
}

// Check is the model's own rules, as messages: duplicate or malformed ids,
// an area or type that does not exist, a row with both or neither of tag and
// device, a flow end that names no equipment or a nozzle the piece does not
// have. Nothing here knows the project's tags or devices.
func (p *Plant) Check() []string {
	var out []string
	areas := map[string]bool{}
	for _, a := range p.Areas {
		switch {
		case !identRe.MatchString(a.ID):
			out = append(out, fmt.Sprintf("plant: area %q: an id is a letter, then letters, digits and underscores", a.ID))
		case areas[strings.ToLower(a.ID)]:
			out = append(out, fmt.Sprintf("plant: area %s is declared twice", a.ID))
		}
		areas[strings.ToLower(a.ID)] = true
	}
	ids := map[string]bool{}
	for _, e := range p.Equipment {
		switch {
		case !identRe.MatchString(e.ID):
			out = append(out, fmt.Sprintf("plant: equipment %q: an id is a letter, then letters, digits and underscores", e.ID))
			continue
		case ids[strings.ToLower(e.ID)]:
			out = append(out, fmt.Sprintf("plant: equipment %s is declared twice", e.ID))
			continue
		}
		ids[strings.ToLower(e.ID)] = true
		if (e.Tag == "") == (e.Device == "") {
			out = append(out, fmt.Sprintf("plant: equipment %s: name exactly one of tag (a declared tag) or device (a devices: row)", e.ID))
		}
		if e.Area != "" && !areas[strings.ToLower(e.Area)] {
			out = append(out, fmt.Sprintf("plant: equipment %s: area %q is not declared in areas", e.ID, e.Area))
		}
		if e.Type != "" && p.Type(e.Type) == nil {
			out = append(out, fmt.Sprintf("plant: equipment %s: no asset type %q (asset-types/%s.asset.yaml)", e.ID, e.Type, e.Type))
		}
		if e.Type == "" && e.Device == "" {
			out = append(out, fmt.Sprintf("plant: equipment %s: a tag needs a type, to say what its page shows", e.ID))
		}
		seen := map[string]bool{}
		for _, n := range e.Nozzles {
			if !identRe.MatchString(n) || seen[strings.ToLower(n)] {
				out = append(out, fmt.Sprintf("plant: equipment %s: nozzle %q is malformed or repeated", e.ID, n))
			}
			seen[strings.ToLower(n)] = true
		}
	}
	for i, f := range p.Flows {
		for _, end := range []struct{ role, ref string }{{"from", f.From}, {"to", f.To}} {
			r := ParseRef(end.ref)
			e, ok := p.Find(r.Equipment)
			switch {
			case !ok:
				out = append(out, fmt.Sprintf("plant: flow %d %s: no equipment %q", i+1, end.role, r.Equipment))
			case r.Nozzle != "" && !hasNozzle(e, r.Nozzle):
				out = append(out, fmt.Sprintf("plant: flow %d %s: %s has no nozzle %q", i+1, end.role, e.ID, r.Nozzle))
			case r.Nozzle == "" && len(e.Nozzles) > 0:
				out = append(out, fmt.Sprintf("plant: flow %d %s: %s names its nozzles (%s): say which", i+1, end.role, e.ID, strings.Join(e.Nozzles, ", ")))
			}
		}
		if strings.EqualFold(ParseRef(f.From).Equipment, ParseRef(f.To).Equipment) && f.From == f.To {
			out = append(out, fmt.Sprintf("plant: flow %d: %s feeds itself", i+1, f.From))
		}
	}
	for _, t := range p.sortedTypes() {
		out = append(out, t.check()...)
	}
	return out
}

func hasNozzle(e Equipment, n string) bool {
	for _, x := range e.Nozzles {
		if strings.EqualFold(x, n) {
			return true
		}
	}
	return false
}

func (p *Plant) sortedTypes() []*AssetType {
	var ts []*AssetType
	for _, t := range p.Types {
		ts = append(ts, t)
	}
	sort.Slice(ts, func(i, j int) bool { return ts[i].Type < ts[j].Type })
	return ts
}

// Templates are every tag template the type names, once each: what the
// manifest checks against its tags for each equipment of the type.
func (t *AssetType) Templates() []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, r := range t.Readings {
		add(r.Tag)
	}
	for _, s := range t.Setpoints {
		add(s.Tag)
	}
	for _, c := range t.Commands {
		add(c.Tag)
		add(c.Held)
	}
	for _, x := range t.Toggles {
		add(x.Tag)
	}
	for _, s := range t.Trend {
		add(s)
	}
	return out
}

// Expand fills a template for an equipment's tag.
func Expand(template, tag string) string { return strings.ReplaceAll(template, "{tag}", tag) }

func (t *AssetType) check() []string {
	var out []string
	where := fmt.Sprintf("plant: asset type %s", t.Type)
	for _, r := range t.Readings {
		if r.Tag == "" {
			out = append(out, where+": a reading has no tag")
		}
	}
	for _, s := range t.Setpoints {
		if s.Tag == "" {
			out = append(out, where+": a setpoint has no tag")
		}
	}
	for _, c := range t.Commands {
		if c.Tag == "" || c.Label == "" {
			out = append(out, where+": a command needs a tag and a label")
		}
		if c.Kind != "" && c.Kind != "start" && c.Kind != "stop" && c.Kind != "neutral" {
			out = append(out, fmt.Sprintf("%s: command %s: kind %q is start, stop or neutral", where, c.Tag, c.Kind))
		}
	}
	for _, x := range t.Toggles {
		if x.Tag == "" || x.Label == "" {
			out = append(out, where+": a toggle needs a tag and a label")
		}
	}
	for _, tpl := range t.Templates() {
		if !strings.Contains(tpl, "{tag}") {
			out = append(out, fmt.Sprintf("%s: %q does not use {tag}: every equipment of the type would show the same tag", where, tpl))
		}
	}
	return out
}
