package scene

import (
	"fmt"
	"math"
	"sort"
)

// GenOptions steers Generate.
type GenOptions struct {
	// Name is the scene's display name (the project name, usually).
	Name string
	// KindByType maps a UDT name to the kind that draws it, on top of the
	// built-ins' defaults (Tank→tank, Motor→pump, Valve→valve). This is
	// `naut scene init --kind VfdPump=pump`.
	KindByType map[string]string
	// Spacing between nodes on the grid, metres. Default 0.8.
	Spacing float64
}

// Generate writes the starter scene: one node per struct tag whose UDT maps
// to a kind, laid out on a grid, the camera fitted, and a `kinds` block
// re-pointing any built-in whose UDT the project names differently. Struct
// tags whose type maps to nothing are returned so the caller can say what
// `--kind` would place them. Nobody types a node.
func Generate(tags []TagInfo, opts GenOptions) (doc *Doc, unplaced []TagInfo, warns []string) {
	spacing := opts.Spacing
	if spacing <= 0 {
		spacing = 0.8
	}
	// Type → kind: the built-ins' defaults first, then the caller's, which win.
	byType := map[string]string{}
	for k, def := range Builtin {
		byType[def.Type] = k
	}
	for t, k := range opts.KindByType {
		byType[t] = k
	}

	var placed []TagInfo
	for _, t := range tags {
		if t.Struct == nil {
			continue
		}
		if _, ok := byType[t.TypeName]; ok {
			placed = append(placed, t)
		} else {
			unplaced = append(unplaced, t)
		}
	}
	sort.Slice(placed, func(i, j int) bool { return placed[i].Name < placed[j].Name })
	sort.Slice(unplaced, func(i, j int) bool { return unplaced[i].Name < unplaced[j].Name })

	doc = &Doc{Name: opts.Name, Nodes: []Node{}}

	// A built-in kind re-pointed to this project's UDT goes in `kinds`, so
	// the checker holds the nodes to that type. Two UDTs on one built-in
	// kind cannot both be the kind's type; the first (sorted) wins and the
	// other's nodes will be checked member-by-member instead.
	kindType := map[string]string{}
	types := make([]string, 0, len(byType))
	for t := range byType {
		types = append(types, t)
	}
	sort.Strings(types)
	for _, t := range types {
		k := byType[t]
		def, builtin := Builtin[k]
		if builtin && def.Type == t {
			continue
		}
		if prev, taken := kindType[k]; taken {
			warns = append(warns, fmt.Sprintf("kind %q is mapped from both %s and %s; kinds.%s.type is %s, and %s nodes are checked member by member", k, prev, t, k, prev, t))
			continue
		}
		kindType[k] = t
	}
	for k, t := range kindType {
		if doc.Kinds == nil {
			doc.Kinds = map[string]Kind{}
		}
		doc.Kinds[k] = Kind{Type: t}
	}

	n := len(placed)
	cols := int(math.Ceil(math.Sqrt(float64(n))))
	if cols < 1 {
		cols = 1
	}
	rows := (n + cols - 1) / cols
	for i, t := range placed {
		col, row := i%cols, i/cols
		doc.Nodes = append(doc.Nodes, Node{
			ID:    t.Name,
			Kind:  byType[t.TypeName],
			Tag:   t.Name,
			Label: t.Name,
			Pos:   Vec{round(float64(col) * spacing), 0, round(float64(row) * spacing)},
		})
	}

	// Camera and grid fitted to the layout: target at the centre, eye up
	// and back along +z, far enough that the whole grid fits at 45°.
	w := float64(max(cols-1, 0)) * spacing
	d := float64(max(rows-1, 0)) * spacing
	span := math.Max(math.Max(w, d), spacing)
	cx, cz := w/2, d/2
	doc.Camera = &Camera{
		Pos:    Vec{round(cx + span*0.6), round(span*0.9 + 0.8), round(cz + span*1.2 + 1)},
		Target: Vec{round(cx), 0.1, round(cz)},
	}
	doc.Grid = &Grid{Pos: Vec{round(cx), -0.001, round(cz)}, Size: Vec{round(w + 2), round(d + 2)}}
	return doc, unplaced, warns
}

func round(v float64) float64 { return math.Round(v*1000) / 1000 }
