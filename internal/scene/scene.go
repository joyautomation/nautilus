// Package scene is the offline half of the 3D HMI's scene document
// (*.scene.json, rendered by @joyautomation/nautilus-hmi-3d): the shape,
// the kind ↔ UDT contract, `naut check`'s pass over it, and the generator
// behind `naut scene init`.
//
// The document binds nodes to STRUCT tags and reads members off them, so
// the contract a scene is held to is the same one alarm rules and Sparkplug
// Templates already use: a UDT's name and its members. Nothing here opens
// a browser or a renderer; it is the same offline discipline as
// project.CheckAlarms. Brief: docs/design/spatial-hmi.md §3b.
package scene

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"

	"github.com/joyautomation/nautilus/lang/ir"
)

// Doc is a *.scene.json. Field names and meaning mirror hmi-3d's scene.ts,
// which is the renderer's view of the same file.
type Doc struct {
	Name     string          `json:"name,omitempty"`
	Kinds    map[string]Kind `json:"kinds,omitempty"`
	Camera   *Camera         `json:"camera,omitempty"`
	Fixtures []Fixture       `json:"fixtures,omitempty"`
	Grid     *Grid           `json:"grid,omitempty"`
	Nodes    []Node          `json:"nodes"`
	Pipes    []Pipe          `json:"pipes,omitempty"`
	Writable []string        `json:"writable,omitempty"`
}

// Vec is a numeric array — a position, a rotation, a size. Encode keeps
// one on ONE line (`[1, -0.75, 0.4]`), the way a person writes it.
type Vec []float64

// Kind is the contract for one kind: the UDT a node of it binds and the
// members its component reads. A scene re-points a built-in kind's Type
// for a project whose UDT is named differently, and declares the app's
// own kinds so the checker can hold them to the same standard.
type Kind struct {
	Type    string   `json:"type,omitempty"`
	Members []string `json:"members,omitempty"`
}

type Camera struct {
	Pos    Vec     `json:"pos"`
	Target Vec     `json:"target"`
	Fov    float64 `json:"fov,omitempty"`
}

type Fixture struct {
	Kind    string   `json:"kind"`
	Pos     Vec      `json:"pos"`
	Size    Vec      `json:"size,omitempty"`
	Rot     Vec      `json:"rot,omitempty"`
	Color   string   `json:"color,omitempty"`
	Opacity *float64 `json:"opacity,omitempty"`
}

type Grid struct {
	Pos     Vec     `json:"pos,omitempty"`
	Size    Vec     `json:"size,omitempty"`
	Cell    float64 `json:"cell,omitempty"`
	Section float64 `json:"section,omitempty"`
}

type Node struct {
	ID    string            `json:"id"`
	Kind  string            `json:"kind"`
	Tag   string            `json:"tag,omitempty"`
	Label string            `json:"label,omitempty"`
	Pos   Vec               `json:"pos"`
	Rot   Vec               `json:"rot,omitempty"`
	Scale float64           `json:"scale,omitempty"`
	Props map[string]any    `json:"props,omitempty"`
	Bind  map[string]string `json:"bind,omitempty"`
}

type Pipe struct {
	ID     string            `json:"id,omitempty"`
	Points []Vec             `json:"points"`
	Radius float64           `json:"radius,omitempty"`
	Bind   map[string]string `json:"bind,omitempty"`
}

// Builtin is the contract the package's built-in kinds carry. It must
// match hmi-3d's builtinRegistry (registry.ts): same names, same members.
var Builtin = map[string]Kind{
	"tank":  {Type: "Tank", Members: []string{"Level", "TempC"}},
	"pump":  {Type: "Motor", Members: []string{"Running", "Fault", "Speed"}},
	"valve": {Type: "Valve", Members: []string{"Pos", "Cmd"}},
}

// Parse decodes a scene file. Unknown fields are errors, so a typo'd key
// (`postion`) is reported instead of silently ignored — the one place a
// JSON decoder's default is the wrong default for a file people edit.
func Parse(data []byte) (*Doc, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var d Doc
	if err := dec.Decode(&d); err != nil {
		return nil, err
	}
	return &d, nil
}

// Encode renders a document the way `naut scene init` writes it: tabs, a
// trailing newline, keys in struct order — so a generated file diffs
// cleanly against a hand-edited one.
func Encode(d *Doc) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "\t")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(d); err != nil {
		return nil, err
	}
	// json.Encoder's indent puts every array element on its own line, a
	// custom MarshalJSON included — so numeric arrays are folded back onto
	// one line afterwards. Only arrays holding nothing but numbers match.
	return numArrayRe.ReplaceAllFunc(buf.Bytes(), func(m []byte) []byte {
		fields := strings.Fields(strings.NewReplacer("[", "", "]", "", ",", " ").Replace(string(m)))
		return []byte("[" + strings.Join(fields, ", ") + "]")
	}), nil
}

var numArrayRe = regexp.MustCompile(`\[\s*-?[0-9][0-9.eE+-]*(?:\s*,\s*-?[0-9][0-9.eE+-]*)*\s*\]`)

// TagInfo is what the checker and generator need to know about a declared
// tag: its name, its UDT (if any) and the UDT's members.
type TagInfo struct {
	Name     string
	TypeName string // "" for a scalar
	Desc     string
	Struct   *ir.StructDef // nil for a scalar
}

// EffectiveKinds resolves the document's effective kind table: the built-ins,
// with the document's `kinds` block overriding a Type (members kept) or
// declaring a kind of the app's own.
func (d *Doc) EffectiveKinds() map[string]Kind {
	out := make(map[string]Kind, len(Builtin)+len(d.Kinds))
	for k, v := range Builtin {
		out[k] = v
	}
	for k, v := range d.Kinds {
		cur := out[k]
		if v.Type != "" {
			cur.Type = v.Type
		}
		if v.Members != nil {
			cur.Members = v.Members
		}
		out[k] = cur
	}
	return out
}

var refRe = regexp.MustCompile(`^!?[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)*$`)

// Check holds a parsed document to the project's tags, offline. It returns
// the problems that must be fixed and the ones that merely look wrong,
// each prefixed with a JSON-pointer path (`/nodes/2/tag`). The split is
// the one CheckAlarms makes: a member the declared type does not have is
// an ERROR (the type is right here; nothing at run time makes `.Level`
// appear), a root tag the manifest does not declare is a WARNING (on a
// Sparkplug host, tags arrive from the field).
func Check(d *Doc, tags []TagInfo) (errs, warns []string) {
	byName := make(map[string]TagInfo, len(tags))
	for _, t := range tags {
		byName[t.Name] = t
	}
	kinds := d.EffectiveKinds()
	errf := func(path, format string, a ...any) { errs = append(errs, path+": "+fmt.Sprintf(format, a...)) }
	warnf := func(path, format string, a ...any) { warns = append(warns, path+": "+fmt.Sprintf(format, a...)) }

	vec3 := func(path string, v Vec, what string) {
		if len(v) != 3 || !finite(v) {
			errf(path, "must be [x, y, z] %s", what)
		}
	}

	for k, v := range d.Kinds {
		if strings.TrimSpace(k) == "" {
			errf("/kinds", "a kind name is empty")
		}
		for i, m := range v.Members {
			if m == "" {
				errf(fmt.Sprintf("/kinds/%s/members/%d", k, i), "empty member name")
			}
		}
	}

	if d.Camera != nil {
		vec3("/camera/pos", d.Camera.Pos, "metres")
		vec3("/camera/target", d.Camera.Target, "metres")
		if d.Camera.Fov != 0 && (d.Camera.Fov <= 0 || d.Camera.Fov >= 180) {
			errf("/camera/fov", "must be between 0 and 180 degrees")
		}
	}
	if d.Grid != nil {
		if d.Grid.Pos != nil {
			vec3("/grid/pos", d.Grid.Pos, "metres")
		}
		if d.Grid.Size != nil && (len(d.Grid.Size) != 2 || !finite(d.Grid.Size)) {
			errf("/grid/size", "must be [w, d] metres")
		}
		if d.Grid.Cell < 0 || d.Grid.Section < 0 {
			errf("/grid", "cell and section must be > 0")
		}
	}
	for i, f := range d.Fixtures {
		p := fmt.Sprintf("/fixtures/%d", i)
		switch f.Kind {
		case "box", "plane", "marker":
		default:
			errf(p+"/kind", "must be 'box', 'plane' or 'marker', not %q", f.Kind)
		}
		vec3(p+"/pos", f.Pos, "metres")
		if f.Size != nil && (len(f.Size) < 2 || len(f.Size) > 3 || !finite(f.Size)) {
			errf(p+"/size", "must be [w, h, d] or [w, d]")
		}
		if f.Rot != nil {
			vec3(p+"/rot", f.Rot, "degrees")
		}
		if f.Opacity != nil && (*f.Opacity < 0 || *f.Opacity > 1) {
			errf(p+"/opacity", "must be between 0 and 1")
		}
	}

	// A ref is checked the same way wherever it appears: syntax, then the
	// root tag, then the member path through the struct.
	checkRef := func(path, ref string) {
		if !refRe.MatchString(ref) {
			errf(path, "%q is not a tag ref (Tag, Tag.Member, !Tag)", ref)
			return
		}
		negate := strings.HasPrefix(ref, "!")
		full := strings.TrimPrefix(ref, "!")
		parts := strings.Split(full, ".")
		info, declared := byName[parts[0]]
		if !declared {
			warnf(path, "%q: the manifest declares no tag %q — fine on a Sparkplug host, where it arrives from the field; a typo anywhere else", ref, parts[0])
			return
		}
		t := info.Struct
		var leaf *ir.Type
		for i, m := range parts[1:] {
			if t == nil {
				errf(path, "%q: %s is not a struct, so it has no member %q", ref, strings.Join(parts[:i+1], "."), m)
				return
			}
			idx, ok := t.FieldIndex[m]
			if !ok {
				errf(path, "%q: %s has no member %q (it has %s)", ref, t.Name, m, memberList(t))
				return
			}
			leaf = t.Fields[idx].Type
			t = nil
			if leaf != nil && leaf.Kind == ir.TypeStruct {
				t = leaf.Struct
			}
		}
		if negate && leaf != nil && leaf.Kind != ir.TypeBool {
			warnf(path, "%q negates a %s member; `!` reads `value !== true`, which is always true here", ref, leaf.Kind)
		}
		if negate && len(parts) == 1 && info.Struct != nil {
			warnf(path, "%q negates a whole struct tag; `!` expects a BOOL", ref)
		}
	}

	ids := make(map[string]int, len(d.Nodes))
	for i, n := range d.Nodes {
		p := fmt.Sprintf("/nodes/%d", i)
		if n.ID == "" {
			errf(p+"/id", "must be a non-empty string")
		} else if j, dup := ids[n.ID]; dup {
			errf(p+"/id", "duplicate id %q (also /nodes/%d)", n.ID, j)
		} else {
			ids[n.ID] = i
		}
		vec3(p+"/pos", n.Pos, "metres")
		if n.Rot != nil {
			vec3(p+"/rot", n.Rot, "degrees")
		}
		if n.Scale < 0 {
			errf(p+"/scale", "must be > 0")
		}
		kind, known := kinds[n.Kind]
		if n.Kind == "" {
			errf(p+"/kind", "must be a non-empty string")
		} else if !known {
			errf(p+"/kind", "unknown kind %q (built in: %s; declare the app's own under kinds:)", n.Kind, kindList(kinds))
		}
		bindKeys := make([]string, 0, len(n.Bind))
		for prop := range n.Bind {
			bindKeys = append(bindKeys, prop)
		}
		sort.Strings(bindKeys)
		for _, prop := range bindKeys {
			checkRef(p+"/bind/"+prop, n.Bind[prop])
		}
		if n.Tag == "" {
			continue
		}
		info, declared := byName[n.Tag]
		if !declared {
			warnf(p+"/tag", "the manifest declares no tag %q — fine on a Sparkplug host, where it arrives from the field; a typo anywhere else", n.Tag)
			continue
		}
		if !known || kind.Type == "" && len(kind.Members) == 0 {
			continue
		}
		// The contract: the tag is the kind's UDT, or at least carries the
		// members the kind reads, or those members are bound explicitly.
		var missing []string
		for _, m := range kind.Members {
			if _, bound := n.Bind[lower(m)]; bound {
				continue
			}
			if info.Struct != nil {
				if _, ok := info.Struct.FieldIndex[m]; ok {
					continue
				}
			}
			missing = append(missing, m)
		}
		switch {
		case len(missing) == 0:
		case info.Struct == nil:
			errf(p+"/tag", "%q is a scalar, but kind %q reads %s off a %s — bind a struct tag, or bind the members explicitly", n.Tag, n.Kind, strings.Join(kind.Members, ", "), kind.Type)
		case kind.Type != "" && info.TypeName != kind.Type:
			errf(p+"/tag", "%q is a %s, but kind %q expects a %s and finds no %s on it — re-point the kind (kinds.%s.type: %q) or bind %s explicitly", n.Tag, info.TypeName, n.Kind, kind.Type, strings.Join(missing, "/"), n.Kind, info.TypeName, strings.Join(missing, ", "))
		default:
			errf(p+"/tag", "%s %q has no member %s, which kind %q reads (it has %s)", info.TypeName, n.Tag, strings.Join(missing, "/"), n.Kind, memberList(info.Struct))
		}
	}

	for i, pipe := range d.Pipes {
		p := fmt.Sprintf("/pipes/%d", i)
		if len(pipe.Points) < 2 {
			errf(p+"/points", "needs at least two points")
		}
		for j, pt := range pipe.Points {
			vec3(fmt.Sprintf("%s/points/%d", p, j), pt, "metres")
		}
		if pipe.Radius < 0 {
			errf(p+"/radius", "must be > 0")
		}
		for k, ref := range pipe.Bind {
			if k != "flowing" {
				errf(p+"/bind/"+k, "a pipe binds only 'flowing'")
				continue
			}
			checkRef(p+"/bind/flowing", ref)
		}
	}

	if len(d.Writable) > 0 {
		errf("/writable", "writes from a scene are not supported yet; the list must be empty")
	}
	return errs, warns
}

// lower is the prop name a kind's member is bound under: hmi-3d's built-in
// components take `level`, `running`, `pos`... for Level, Running, Pos.
func lower(member string) string {
	if member == "" {
		return member
	}
	return strings.ToLower(member[:1]) + member[1:]
}

func finite(v Vec) bool {
	for _, x := range v {
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return false
		}
	}
	return true
}

func memberList(sd *ir.StructDef) string {
	if sd == nil || len(sd.Fields) == 0 {
		return "no members"
	}
	names := make([]string, len(sd.Fields))
	for i, f := range sd.Fields {
		names[i] = f.Name
	}
	return strings.Join(names, ", ")
}

func kindList(kinds map[string]Kind) string {
	names := make([]string, 0, len(kinds))
	for k := range kinds {
		names = append(names, k)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}
