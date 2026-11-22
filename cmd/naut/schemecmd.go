package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/joyautomation/nautilus/internal/plant"
	"github.com/joyautomation/nautilus/internal/project"
)

const schemeUsage = `usage: naut hmi scheme [-update] [-area id] [project]

Write a process scheme for each area of the plant model (plant.yaml): a
<area>.mimic.json with the area's equipment laid out left to right along its
flows, each drawn with its asset type's component, bound to its tags, with
its panel, and the flows as pipes between the ports the asset type names.
Then move things by hand: the file is yours.

  -update   add equipment and flows the plant has since gained to an existing
            file. Nothing already there moves, changes or goes away.
  -area id  only this area

Without -update an existing file is left alone and the command says so.
`

func runHMIScheme(args []string) int {
	fl := flag.NewFlagSet("naut hmi scheme", flag.ContinueOnError)
	fl.SetOutput(os.Stderr)
	fl.Usage = func() { fmt.Fprint(os.Stderr, schemeUsage) }
	update := fl.Bool("update", false, "add what the plant gained to an existing scheme")
	only := fl.String("area", "", "only this area")
	if err := fl.Parse(args); err != nil {
		return 2
	}
	dir := "."
	if fl.NArg() > 0 {
		dir = fl.Arg(0)
	}
	proj, err := project.Load(os.DirFS(dir), project.ManifestName)
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut hmi scheme:", err)
		return 1
	}
	if proj.Plant == nil {
		fmt.Fprintln(os.Stderr, "naut hmi scheme: the manifest has no plant: (plant.yaml): nothing to draw")
		return 1
	}
	if m := proj.Plant.Check(); len(m) > 0 {
		for _, x := range m {
			fmt.Fprintln(os.Stderr, "naut hmi scheme:", x)
		}
		return 1
	}
	wrote, exit := 0, 0
	for _, a := range proj.Plant.Areas {
		if *only != "" && !strings.EqualFold(*only, a.ID) {
			continue
		}
		path := filepath.Join(dir, plant.Slug(a.ID)+".mimic.json")
		old, err := os.ReadFile(path)
		exists := err == nil
		if exists && !*update {
			fmt.Printf("%s: exists: left alone (naut hmi scheme -update adds what the plant gained)\n", path)
			continue
		}
		out, notes, err := planScheme(proj.Plant, a, old)
		for _, n := range notes {
			fmt.Printf("%s: %s\n", path, n)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "naut hmi scheme: %s: %s\n", path, err)
			exit = 1
			continue
		}
		if out == nil {
			continue
		}
		if exists && bytes.Equal(out, old) {
			fmt.Printf("%s: nothing to add\n", path)
			continue
		}
		if err := os.WriteFile(path, out, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "naut hmi scheme:", err)
			return 1
		}
		wrote++
		fmt.Printf("%s: written\n", path)
	}
	if wrote == 0 && exit == 0 && len(proj.Plant.Areas) == 0 {
		fmt.Fprintln(os.Stderr, "naut hmi scheme: the plant has no areas: a scheme is drawn for each area")
	}
	return exit
}

// schemeSize is the box a component is drawn in on a scheme when the asset
// type says nothing else: the kit's own components' proportions.
var schemeSize = map[string][2]float64{
	"Tank": {140, 170}, "LevelTank": {120, 170}, "Pump": {90, 80}, "Valve": {70, 50}, "Heater": {110, 90},
}

// builtinPorts are the kit's own components' port names (BUILTIN_PORTS in
// hmi/src/lib/mimic.ts), enough to pick a flow's ends when an asset type
// names no ports.
var builtinPorts = map[string][]string{
	"Tank": {"top", "left", "right", "bottom"}, "LevelTank": {"top", "left", "right", "bottom"},
	"Pump": {"in", "out"}, "Valve": {"in", "out"}, "OffPage": {"top", "left", "right", "bottom"},
}

// schemeEquip is a piece of equipment as the scheme file writes it, its keys
// in the order a person reads them.
type schemeEquip struct {
	ID        string            `json:"id"`
	Component string            `json:"component"`
	X         float64           `json:"x"`
	Y         float64           `json:"y"`
	Width     float64           `json:"width"`
	Height    float64           `json:"height"`
	Label     string            `json:"label"`
	Plant     string            `json:"plant"`
	Bind      map[string]string `json:"bind,omitempty"`
	Panel     map[string]any    `json:"panel,omitempty"`
}

type schemeEnd struct {
	Equip string `json:"equip"`
	Port  string `json:"port"`
}

type schemePipe struct {
	ID      string       `json:"id"`
	Routing string       `json:"routing"`
	Points  [][2]float64 `json:"points"`
	From    schemeEnd    `json:"from"`
	To      schemeEnd    `json:"to"`
}

type schemeEq struct {
	eq   plant.Equipment
	typ  *plant.AssetType
	comp string
	w, h float64
}

func (s schemeEq) port(nozzle string, from bool) string {
	if s.typ != nil && nozzle != "" {
		if p := s.typ.Ports[nozzle]; p != "" {
			return p
		}
		for k, p := range s.typ.Ports {
			if strings.EqualFold(k, nozzle) && p != "" {
				return p
			}
		}
	}
	have := builtinPorts[s.comp]
	want := []string{"in", "left"}
	if from {
		want = []string{"out", "right"}
	}
	for _, w := range want {
		for _, h := range have {
			if h == w {
				return w
			}
		}
	}
	return ""
}

// planScheme is the scheme file for one area: from scratch, or, given the
// file's current bytes, the same file with what the plant gained added and
// nothing already in it moved, changed or removed. The notes say what it
// left out and why.
func planScheme(pl *plant.Plant, area plant.Area, existing []byte) ([]byte, []string, error) {
	var notes []string
	top := map[string]json.RawMessage{}
	eqs, pipes := []json.RawMessage{}, []json.RawMessage{}
	have := map[string]bool{}
	havePipe := map[string]bool{}
	maxX, maxY := 0.0, 0.0
	if len(bytes.TrimSpace(existing)) > 0 {
		if err := json.Unmarshal(existing, &top); err != nil {
			return nil, nil, fmt.Errorf("not a mimic: %w", err)
		}
		_ = json.Unmarshal(top["equipment"], &eqs)
		_ = json.Unmarshal(top["pipes"], &pipes)
		for _, r := range eqs {
			var e struct {
				ID, Plant string
				X, Y      float64
				Width     float64
				Height    float64
			}
			_ = json.Unmarshal(r, &e)
			have[strings.ToLower(e.ID)] = true
			if e.Plant != "" {
				have[strings.ToLower(e.Plant)] = true
				if _, ok := pl.Find(e.Plant); !ok {
					notes = append(notes, fmt.Sprintf("equipment %s names plant %q, which the plant no longer has: left as it is", e.ID, e.Plant))
				}
			}
			maxX = max(maxX, e.X+e.Width)
			maxY = max(maxY, e.Y+e.Height)
		}
		for _, r := range pipes {
			var p struct{ ID string }
			_ = json.Unmarshal(r, &p)
			havePipe[strings.ToLower(p.ID)] = true
		}
	}

	// the area's equipment that a scheme can draw
	var all []schemeEq
	for _, e := range pl.Equipment {
		if !strings.EqualFold(e.Area, area.ID) {
			continue
		}
		s := schemeEq{eq: e, typ: pl.Type(e.Type)}
		if s.typ != nil {
			s.comp = s.typ.Component
		}
		if s.comp == "" {
			notes = append(notes, fmt.Sprintf("equipment %s is not drawn: its asset type names no component", e.ID))
			continue
		}
		s.w, s.h = 100, 80
		if sz, ok := schemeSize[s.comp]; ok {
			s.w, s.h = sz[0], sz[1]
		}
		all = append(all, s)
	}
	byID := map[string]*schemeEq{}
	for i := range all {
		byID[strings.ToLower(all[i].eq.ID)] = &all[i]
	}

	// layers: the longest run of flows into each piece, left to right
	layer := map[string]int{}
	var depth func(id string, seen map[string]bool) int
	depth = func(id string, seen map[string]bool) int {
		if d, ok := layer[id]; ok {
			return d
		}
		if seen[id] {
			return 0 // a loop: break it here
		}
		seen[id] = true
		d := 0
		for _, f := range pl.Flows {
			to, from := plant.ParseRef(f.To).Equipment, plant.ParseRef(f.From).Equipment
			if strings.EqualFold(to, id) && byID[strings.ToLower(from)] != nil {
				d = max(d, depth(strings.ToLower(from), seen)+1)
			}
		}
		delete(seen, id)
		layer[id] = d
		return d
	}
	for i := range all {
		depth(strings.ToLower(all[i].eq.ID), map[string]bool{})
	}
	rows := map[int]int{}
	colW := map[int]float64{}
	for i := range all {
		l := layer[strings.ToLower(all[i].eq.ID)]
		colW[l] = max(colW[l], all[i].w)
	}
	colX := map[int]float64{}
	x := 40.0
	layers := make([]int, 0, len(colW))
	for l := range colW {
		layers = append(layers, l)
	}
	sort.Ints(layers)
	for _, l := range layers {
		colX[l] = x
		x += colW[l] + 110
	}
	startY := 40.0
	if len(eqs) > 0 {
		// new equipment goes in a band below what is already drawn
		startY = maxY + 60
	}
	placed := false
	for _, s := range all {
		if have[strings.ToLower(s.eq.ID)] {
			continue
		}
		l := layer[strings.ToLower(s.eq.ID)]
		px, py := colX[l], startY+float64(rows[l])*(170+50)
		rows[l]++
		label := s.eq.Label
		if label == "" {
			label = s.eq.ID
		}
		e := schemeEquip{ID: s.eq.ID, Component: s.comp, X: px, Y: py, Width: s.w, Height: s.h, Label: label, Plant: s.eq.ID}
		if s.typ != nil && s.eq.Tag != "" {
			if len(s.typ.Bind) > 0 {
				e.Bind = map[string]string{}
				for k, v := range s.typ.Bind {
					e.Bind[k] = plant.Expand(v, s.eq.Tag)
				}
			}
			panel := typePanel(s.typ, s.eq.Tag)
			panel["title"] = label
			e.Panel = panel
		}
		raw, err := json.Marshal(e)
		if err != nil {
			return nil, nil, err
		}
		eqs = append(eqs, raw)
		maxX, maxY = max(maxX, px+s.w), max(maxY, py+s.h)
		placed = true
	}

	// flows as pipes, those between drawn pieces of this area
	n := map[string]int{}
	for _, f := range pl.Flows {
		from, to := plant.ParseRef(f.From), plant.ParseRef(f.To)
		a, b := byID[strings.ToLower(from.Equipment)], byID[strings.ToLower(to.Equipment)]
		if a == nil && b == nil {
			continue
		}
		if a == nil || b == nil {
			notes = append(notes, fmt.Sprintf("flow %s -> %s leaves the area: not drawn", f.From, f.To))
			continue
		}
		base := "flow_" + from.Equipment + "_" + to.Equipment
		n[base]++
		id := base
		if n[base] > 1 {
			id = fmt.Sprintf("%s_%d", base, n[base])
		}
		if havePipe[strings.ToLower(id)] {
			continue
		}
		fp, tp := a.port(from.Nozzle, true), b.port(to.Nozzle, false)
		if fp == "" || tp == "" {
			notes = append(notes, fmt.Sprintf("flow %s -> %s is not drawn: no port for %s (name the nozzle's port in the asset type's ports:)", f.From, f.To, map[bool]string{true: from.Equipment, false: to.Equipment}[fp == ""]))
			continue
		}
		pipe := schemePipe{ID: id, Routing: "auto", Points: [][2]float64{}, From: schemeEnd{a.eq.ID, fp}, To: schemeEnd{b.eq.ID, tp}}
		raw, err := json.Marshal(pipe)
		if err != nil {
			return nil, nil, err
		}
		pipes = append(pipes, raw)
		placed = true
	}

	if len(existing) > 0 && !placed {
		return existing, notes, nil
	}
	if len(eqs) == 0 {
		return nil, append(notes, "nothing to draw: no equipment of this area has a component"), nil
	}
	set := func(k string, v any) error {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		top[k] = b
		return nil
	}
	if _, ok := top["name"]; !ok {
		label := area.Label
		if label == "" {
			label = area.ID
		}
		_ = set("name", label)
	}
	canvas := map[string]float64{"width": max(maxX+40, 400), "height": max(maxY+40, 240)}
	_ = set("canvas", canvas)
	_ = set("equipment", eqs)
	_ = set("pipes", pipes)
	out, err := marshalOrdered(top)
	return out, notes, err
}

// marshalOrdered writes the mimic's keys in a readable order, the rest
// after them alphabetically, indented.
func marshalOrdered(top map[string]json.RawMessage) ([]byte, error) {
	order := []string{"name", "canvas"}
	seen := map[string]bool{}
	var keys []string
	for _, k := range order {
		if _, ok := top[k]; ok {
			keys = append(keys, k)
			seen[k] = true
		}
	}
	var rest []string
	for k := range top {
		if !seen[k] && k != "equipment" && k != "pipes" {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	keys = append(keys, rest...)
	keys = append(keys, "equipment", "pipes")
	var buf bytes.Buffer
	buf.WriteString("{\n")
	first := true
	for _, k := range keys {
		v, ok := top[k]
		if !ok {
			continue
		}
		if !first {
			buf.WriteString(",\n")
		}
		first = false
		var ind bytes.Buffer
		if err := json.Indent(&ind, v, "  ", "  "); err != nil {
			return nil, err
		}
		fmt.Fprintf(&buf, "  %q: %s", k, ind.String())
	}
	buf.WriteString("\n}\n")
	return buf.Bytes(), nil
}
