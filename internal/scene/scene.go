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
	Name        string          `json:"name,omitempty"`
	Kinds       map[string]Kind `json:"kinds,omitempty"`
	Environment *Environment    `json:"environment,omitempty"`
	Camera      *Camera         `json:"camera,omitempty"`
	Fixtures    []Fixture       `json:"fixtures,omitempty"`
	Grid        *Grid           `json:"grid,omitempty"`
	Nodes       []Node          `json:"nodes"`
	Pipes       []Pipe          `json:"pipes,omitempty"`
	Writable    []string        `json:"writable,omitempty"`
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
	// A data kind (docs/design/spatial-hmi.md §3c): a glTF as a URL path
	// from the app root, and what live values do to its named meshes.
	Model   string          `json:"model,omitempty"`
	Bounds  json.RawMessage `json:"bounds,omitempty"` // "auto" | {size, center}
	LabelAt Vec             `json:"labelAt,omitempty"`
	Status  string          `json:"status,omitempty"`
	Drive   []Drive         `json:"drive,omitempty"`
}

// Drive is one mesh, one channel. Exactly one of the channel fields is
// set; Parse rejects a field outside the vocabulary.
type Drive struct {
	Mesh     string         `json:"mesh"`
	Spin     *SpinDrive     `json:"spin,omitempty"`
	Turn     *TurnDrive     `json:"turn,omitempty"`
	Scale    *ScaleDrive    `json:"scale,omitempty"`
	Tint     *TintDrive     `json:"tint,omitempty"`
	Emissive *EmissiveDrive `json:"emissive,omitempty"`
	Visible  *BoolExpr      `json:"visible,omitempty"`
}

// NumExpr is member × Scale + Offset, clamped to [Min, Max].
type NumExpr struct {
	Bind   string   `json:"bind"`
	Scale  *float64 `json:"scale,omitempty"`
	Offset *float64 `json:"offset,omitempty"`
	Min    *float64 `json:"min,omitempty"`
	Max    *float64 `json:"max,omitempty"`
}

// BoolExpr is true, or a number above Threshold; a leading `!` negates.
type BoolExpr struct {
	Bind      string   `json:"bind"`
	Threshold *float64 `json:"threshold,omitempty"`
}

type SpinDrive struct {
	Axis    string  `json:"axis"`
	RevPerS NumExpr `json:"revPerS"`
}
type TurnDrive struct {
	Axis string  `json:"axis"`
	Deg  NumExpr `json:"deg"`
}
type ScaleDrive struct {
	Axis string  `json:"axis"`
	To   NumExpr `json:"to"`
}
type TintDrive struct {
	BoolExpr
	On  string `json:"on"`
	Off string `json:"off,omitempty"`
}
type EmissiveDrive struct {
	BoolExpr
	On        string   `json:"on"`
	Intensity *float64 `json:"intensity,omitempty"`
}

// DriveChannels is the vocabulary, in the schema's order. hmi-3d's
// DRIVE_CHANNELS and the extension schema carry the same list; a test on
// each side reads the schema so the three cannot drift.
var DriveChannels = []string{"spin", "turn", "scale", "tint", "emissive", "visible"}

// Environment is the surroundings (§3c): an HDRI, the backdrop, shadows.
type Environment struct {
	HDRI       string   `json:"hdri,omitempty"`
	Background string   `json:"background,omitempty"`
	Floor      *float64 `json:"floor,omitempty"`
	Intensity  *float64 `json:"intensity,omitempty"`
	Shadows    *bool    `json:"shadows,omitempty"`
}

// Texture is a plane fixture's PBR maps.
type Texture struct {
	Map          string `json:"map"`
	NormalMap    string `json:"normalMap,omitempty"`
	RoughnessMap string `json:"roughnessMap,omitempty"`
	Repeat       Vec    `json:"repeat,omitempty"`
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
	Texture *Texture `json:"texture,omitempty"`
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
		cur.Model, cur.Bounds, cur.LabelAt, cur.Status, cur.Drive = v.Model, v.Bounds, v.LabelAt, v.Status, v.Drive
		// The members a kind reads are `members` plus whatever its drives
		// and status template name, so a drive never repeats the list.
		for _, m := range append(driveMembers(v.Drive), statusMembers(v.Status)...) {
			if !contains(cur.Members, m) {
				cur.Members = append(cur.Members, m)
			}
		}
		out[k] = cur
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// driveMembers lists the members a drive list reads, without negation,
// deduplicated, in order.
func driveMembers(drives []Drive) []string {
	var out []string
	add := func(bind string) {
		m := strings.TrimPrefix(bind, "!")
		if m != "" && !contains(out, m) {
			out = append(out, m)
		}
	}
	for _, d := range drives {
		switch {
		case d.Spin != nil:
			add(d.Spin.RevPerS.Bind)
		case d.Turn != nil:
			add(d.Turn.Deg.Bind)
		case d.Scale != nil:
			add(d.Scale.To.Bind)
		case d.Tint != nil:
			add(d.Tint.Bind)
		case d.Emissive != nil:
			add(d.Emissive.Bind)
		case d.Visible != nil:
			add(d.Visible.Bind)
		}
	}
	return out
}

// statusRe matches one field of a status template: {Level}, {Level:1},
// {Running?run:stopped}. The same grammar as hmi-3d's formatStatus.
var statusRe = regexp.MustCompile(`\{([A-Za-z_][A-Za-z0-9_]*)(?::(\d))?(?:\?([^:}]*):([^}]*))?\}`)

func statusMembers(tpl string) []string {
	var out []string
	for _, m := range statusRe.FindAllStringSubmatch(tpl, -1) {
		if !contains(out, m[1]) {
			out = append(out, m[1])
		}
	}
	return out
}

// validStatusTemplate: braces balanced and every field a member.
func validStatusTemplate(tpl string) bool {
	rest := statusRe.ReplaceAllString(tpl, "")
	return !strings.ContainsAny(rest, "{}")
}

var memberRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// assetPath: a URL path from the app root — no scheme, no `..`.
func assetPath(p string) bool {
	if p == "" || strings.Contains(p, ":") {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return false
		}
	}
	return true
}

// checkKind holds a `kinds` entry's data-kind fields to the §3c rules.
func checkKind(k string, v Kind, errf func(path, format string, a ...any)) {
	p := "/kinds/" + k
	if v.Model != "" && !(assetPath(v.Model) && (strings.HasSuffix(v.Model, ".glb") || strings.HasSuffix(v.Model, ".gltf"))) {
		errf(p+"/model", "must be a URL path to a .glb or .gltf (models/pump.glb)")
	}
	if v.Bounds != nil {
		var auto string
		var box struct {
			Size   Vec `json:"size"`
			Center Vec `json:"center"`
		}
		if err := json.Unmarshal(v.Bounds, &auto); err == nil {
			if auto != "auto" {
				errf(p+"/bounds", "must be 'auto' or { size: [w, h, d], center: [x, y, z] }")
			}
		} else if err := json.Unmarshal(v.Bounds, &box); err != nil || len(box.Size) != 3 || len(box.Center) != 3 || !finite(box.Size) || !finite(box.Center) {
			errf(p+"/bounds", "must be 'auto' or { size: [w, h, d], center: [x, y, z] }")
		}
	}
	if v.LabelAt != nil && (len(v.LabelAt) != 3 || !finite(v.LabelAt)) {
		errf(p+"/labelAt", "must be [x, y, z] metres")
	}
	if v.Status != "" && !validStatusTemplate(v.Status) {
		errf(p+"/status", `must be a template over members: "{Level:1} %%", "{Running?run:stopped}"`)
	}
	if v.Model == "" && (v.Drive != nil || v.Bounds != nil || v.LabelAt != nil) {
		errf(p, "drive, bounds and labelAt need a model — without one the Svelte kind of this name renders")
	}
	num := func(path string, e NumExpr) {
		if !memberRe.MatchString(e.Bind) {
			errf(path+"/bind", "must name a member of the struct")
		}
	}
	boolean := func(path string, e BoolExpr) {
		if !memberRe.MatchString(strings.TrimPrefix(e.Bind, "!")) {
			errf(path+"/bind", "must name a member of the struct (a leading ! negates)")
		}
	}
	axis := func(path, a string, xyz bool) {
		switch a {
		case "x", "y", "z":
		case "xyz":
			if xyz {
				return
			}
			fallthrough
		default:
			if xyz {
				errf(path, "must be 'x', 'y', 'z' or 'xyz'")
			} else {
				errf(path, "must be 'x', 'y' or 'z'")
			}
		}
	}
	for i, d := range v.Drive {
		dp := fmt.Sprintf("%s/drive/%d", p, i)
		if d.Mesh == "" {
			errf(dp+"/mesh", "must name a mesh in the model")
		}
		n := 0
		for _, set := range []bool{d.Spin != nil, d.Turn != nil, d.Scale != nil, d.Tint != nil, d.Emissive != nil, d.Visible != nil} {
			if set {
				n++
			}
		}
		if n != 1 {
			errf(dp, "a drive has exactly one of %s", strings.Join(DriveChannels, ", "))
			continue
		}
		switch {
		case d.Spin != nil:
			axis(dp+"/spin/axis", d.Spin.Axis, false)
			num(dp+"/spin/revPerS", d.Spin.RevPerS)
		case d.Turn != nil:
			axis(dp+"/turn/axis", d.Turn.Axis, false)
			num(dp+"/turn/deg", d.Turn.Deg)
		case d.Scale != nil:
			axis(dp+"/scale/axis", d.Scale.Axis, true)
			num(dp+"/scale/to", d.Scale.To)
		case d.Tint != nil:
			boolean(dp+"/tint", d.Tint.BoolExpr)
			if d.Tint.On == "" {
				errf(dp+"/tint/on", "must be a palette slot or a CSS colour")
			}
		case d.Emissive != nil:
			boolean(dp+"/emissive", d.Emissive.BoolExpr)
			if d.Emissive.On == "" {
				errf(dp+"/emissive/on", "must be a palette slot or a CSS colour")
			}
			if d.Emissive.Intensity != nil && *d.Emissive.Intensity < 0 {
				errf(dp+"/emissive/intensity", "must be ≥ 0")
			}
		case d.Visible != nil:
			boolean(dp+"/visible", *d.Visible)
		}
	}
}

// Assets lists every file the document refers to — models, the HDRI,
// texture maps — each with the JSON path that names it, so CheckAssets and
// a packager see the same list.
func (d *Doc) Assets() (paths [][2]string) {
	names := make([]string, 0, len(d.Kinds))
	for k := range d.Kinds {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		if m := d.Kinds[k].Model; m != "" {
			paths = append(paths, [2]string{"/kinds/" + k + "/model", m})
		}
	}
	if d.Environment != nil && d.Environment.HDRI != "" {
		paths = append(paths, [2]string{"/environment/hdri", d.Environment.HDRI})
	}
	for i, f := range d.Fixtures {
		if f.Texture == nil {
			continue
		}
		p := fmt.Sprintf("/fixtures/%d/texture", i)
		paths = append(paths, [2]string{p + "/map", f.Texture.Map})
		if f.Texture.NormalMap != "" {
			paths = append(paths, [2]string{p + "/normalMap", f.Texture.NormalMap})
		}
		if f.Texture.RoughnessMap != "" {
			paths = append(paths, [2]string{p + "/roughnessMap", f.Texture.RoughnessMap})
		}
	}
	return paths
}

// CheckAssets holds every asset path to `exists`, which the caller builds
// from where the app serves files (next to the scene, the HMI's static/,
// the build). Separate from Check so the structural pass stays pure.
func CheckAssets(d *Doc, exists func(path string) bool) (errs []string) {
	for _, a := range d.Assets() {
		if !exists(a[1]) {
			errs = append(errs, fmt.Sprintf("%s: %q was not found (next to the scene file, under the HMI app's static/, or in its build) — paths are URL paths from the app root", a[0], a[1]))
		}
	}
	return errs
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

	kindNames := make([]string, 0, len(d.Kinds))
	for k := range d.Kinds {
		kindNames = append(kindNames, k)
	}
	sort.Strings(kindNames)
	for _, k := range kindNames {
		v := d.Kinds[k]
		if strings.TrimSpace(k) == "" {
			errf("/kinds", "a kind name is empty")
		}
		for i, m := range v.Members {
			if m == "" {
				errf(fmt.Sprintf("/kinds/%s/members/%d", k, i), "empty member name")
			}
		}
		checkKind(k, v, errf)
	}

	if e := d.Environment; e != nil {
		if e.HDRI != "" && !(assetPath(e.HDRI) && (strings.HasSuffix(strings.ToLower(e.HDRI), ".hdr") || strings.HasSuffix(strings.ToLower(e.HDRI), ".exr"))) {
			errf("/environment/hdri", "must be a URL path to an .hdr or .exr (env/workshop_1k.hdr)")
		}
		switch e.Background {
		case "", "none", "sky", "ground":
		default:
			errf("/environment/background", "must be 'none', 'sky' or 'ground', not %q", e.Background)
		}
		if e.Intensity != nil && *e.Intensity < 0 {
			errf("/environment/intensity", "must be ≥ 0")
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
		if t := f.Texture; t != nil {
			if f.Kind != "plane" {
				errf(p+"/texture", "only a plane takes a texture")
			}
			if !assetPath(t.Map) {
				errf(p+"/texture/map", "must be a URL path to an image")
			}
			if t.NormalMap != "" && !assetPath(t.NormalMap) {
				errf(p+"/texture/normalMap", "must be a URL path to an image")
			}
			if t.RoughnessMap != "" && !assetPath(t.RoughnessMap) {
				errf(p+"/texture/roughnessMap", "must be a URL path to an image")
			}
			if t.Repeat != nil && (len(t.Repeat) != 2 || !finite(t.Repeat)) {
				errf(p+"/texture/repeat", "must be [w, d] tiles")
			}
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
