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
	"slices"
	"sort"
	"strings"

	"github.com/joyautomation/nautilus/lang/ir"
)

// Doc is a *.scene.json. Field names and meaning mirror hmi-3d's scene.ts,
// which is the renderer's view of the same file.
type Doc struct {
	Name string `json:"name,omitempty"`
	// Look is how the document asks to be drawn: lit (default), flat, or
	// paper (hmi-3d's scene.ts, SceneLook).
	Look        string          `json:"look,omitempty"`
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
	Model  string          `json:"model,omitempty"`
	Bounds json.RawMessage `json:"bounds,omitempty"` // "auto" | {size, center}
	// LabelAt: [x, y, z] metres, an anchor word on the bounds ("top"), or
	// {part?, anchor, offset?} — a place on a part's box (design §3g).
	LabelAt json.RawMessage `json:"labelAt,omitempty"`
	Status  string          `json:"status,omitempty"`
	// Ports (design §3g, slice 3): where pipes meet this kind, in its own
	// frame — a point and the direction a pipe leaves it (default up).
	Ports map[string]Port `json:"ports,omitempty"`
	Drive []Drive         `json:"drive,omitempty"`
	// A component kind (§3d): a Svelte file as a source path relative to
	// the scene file. The app builds it; the checker sees the file and the
	// contract above, and never runs Svelte.
	Component string `json:"component,omitempty"`
	// An assembly (§3d): a kind made of kinds. Parts and pipes sit in the
	// kind's own frame, and a part's Tag is a MEMBER of the assembly's
	// struct — the nested UDT is the only parameter.
	Assembly *Assembly `json:"assembly,omitempty"`
	// File is set when the `kinds` entry was the path of a `{Name}.kind.json`
	// (design §3g, slice 2): a kind in a file of its own, relative to the
	// scene file like a component. The other fields are empty until
	// ResolveKindFiles reads the file; Encode writes the path back.
	File string `json:"-"`
}

// kindJSON is Kind without the methods, for the two JSON forms below.
type kindJSON Kind

// UnmarshalJSON takes a `kinds` entry in either form: the contract inline,
// or a string — the path of the kind file that holds it. Unknown fields
// are refused as Parse refuses them at the top level.
func (k *Kind) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		var path string
		if err := json.Unmarshal(b, &path); err != nil {
			return err
		}
		*k = Kind{File: path}
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var v kindJSON
	if err := dec.Decode(&v); err != nil {
		return err
	}
	*k = Kind(v)
	return nil
}

// MarshalJSON writes a file-backed kind as its path, so a document that
// names kind files round-trips through Encode as written.
func (k Kind) MarshalJSON() ([]byte, error) {
	if k.File != "" {
		return json.Marshal(k.File)
	}
	return json.Marshal(kindJSON(k))
}

// kindPath is a kind file's path: relative to the scene file, a
// `.kind.json`, no scheme, not absolute, no `..` — a component's rules.
func kindPath(p string) bool {
	return assetPath(p) && strings.HasSuffix(p, ".kind.json") && !strings.HasPrefix(p, "/")
}

// KindFiles lists every kind file the document names, with the JSON path
// of the entry — source paths relative to the scene file, like
// Components, so they are read against the same root.
func (d *Doc) KindFiles() (paths [][2]string) {
	names := make([]string, 0, len(d.Kinds))
	for k := range d.Kinds {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		if f := d.Kinds[k].File; f != "" {
			paths = append(paths, [2]string{"/kinds/" + k, f})
		}
	}
	return paths
}

// ResolveKindFiles reads every kind file the document names (`read` is
// built from the scene file's directory) and puts what it holds into the
// entry, keeping File so Encode writes the path back; Check then holds
// the kind — and its nodes — to the file's contract exactly as if it were
// inline. A file that cannot be read or parsed, or that holds a component
// or an assembly (those stay inline, where the scene can see them), is an
// error at the entry, and the entry stays empty.
func ResolveKindFiles(d *Doc, read func(path string) ([]byte, error)) (errs []string) {
	for _, kf := range d.KindFiles() {
		p, file := kf[0], kf[1]
		name := strings.TrimPrefix(p, "/kinds/")
		if !kindPath(file) {
			errs = append(errs, p+": a kind file is a {Name}.kind.json as a path relative to the scene file (hmi/src/lib/kinds/pump.kind.json)")
			continue
		}
		data, err := read(file)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %q was not found next to the scene file — a kind file's path is relative to it", p, file))
			continue
		}
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		var v kindJSON
		if err := dec.Decode(&v); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %s: %s", p, file, err))
			continue
		}
		k := Kind(v)
		if k.Component != "" || k.Assembly != nil {
			errs = append(errs, fmt.Sprintf("%s: %s holds a %s kind — a kind file is a data kind (model + drives) or a contract; a component or an assembly stays inline in the scene", p, file, k.DefinedBy()))
			continue
		}
		k.File = file
		d.Kinds[name] = k
	}
	return errs
}

// Assembly is a kind's parts and pipes (§3d).
type Assembly struct {
	Nodes []Node `json:"nodes"`
	Pipes []Pipe `json:"pipes,omitempty"`
}

// DefinedBy says how a `kinds` entry defines its kind: "model",
// "component", "assembly", or "" for a contract-only entry.
func (k Kind) DefinedBy() string {
	switch {
	case k.File != "" && k.Model == "" && k.Type == "" && k.Members == nil && k.Status == "" && k.Drive == nil:
		return "file"
	case k.Model != "":
		return "model"
	case k.Component != "":
		return "component"
	case k.Assembly != nil:
		return "assembly"
	}
	return ""
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
	Pulse    *PulseDrive    `json:"pulse,omitempty"`
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
// Blink: while true, the effect alternates at this rate (Hz).
type BoolExpr struct {
	Bind      string   `json:"bind"`
	Threshold *float64 `json:"threshold,omitempty"`
	Blink     *float64 `json:"blink,omitempty"`
}

// PulseDrive: the mesh breathes while true — its scale swings by Amount
// (default 0.1) at Hz (default 1).
type PulseDrive struct {
	BoolExpr
	Hz     *float64 `json:"hz,omitempty"`
	Amount *float64 `json:"amount,omitempty"`
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
	On  string `json:"on,omitempty"`
	Off string `json:"off,omitempty"`
	// Ramp: the colour varies with a number — stops of [value, colour].
	Ramp []RampStop `json:"ramp,omitempty"`
}
type EmissiveDrive struct {
	BoolExpr
	On        string     `json:"on,omitempty"`
	Intensity *float64   `json:"intensity,omitempty"`
	Ramp      []RampStop `json:"ramp,omitempty"`
}

// RampStop is one [value, colour] of a ramp.
type RampStop struct {
	Value  float64
	Colour string
}

func (r *RampStop) UnmarshalJSON(b []byte) error {
	var pair []json.RawMessage
	if err := json.Unmarshal(b, &pair); err != nil || len(pair) != 2 {
		return fmt.Errorf("a ramp stop is [value, colour]")
	}
	if err := json.Unmarshal(pair[0], &r.Value); err != nil {
		return fmt.Errorf("a ramp stop's value is a number")
	}
	if err := json.Unmarshal(pair[1], &r.Colour); err != nil {
		return fmt.Errorf("a ramp stop's colour is a string")
	}
	return nil
}

func (r RampStop) MarshalJSON() ([]byte, error) {
	return json.Marshal([]any{r.Value, r.Colour})
}

// checkRamp: a plain member, at least two stops going up in value.
func checkRamp(path, bind string, ramp []RampStop, errf func(path, format string, a ...any)) {
	if !memberRe.MatchString(bind) || strings.HasPrefix(bind, "!") {
		errf(path+"/bind", "must name a member of the struct (a number)")
	}
	if len(ramp) < 2 {
		errf(path+"/ramp", "must be at least two [value, colour] stops")
		return
	}
	prev := math.Inf(-1)
	for i, stop := range ramp {
		if stop.Colour == "" {
			errf(fmt.Sprintf("%s/ramp/%d", path, i), "must be [value, colour]")
		}
		if !(stop.Value > prev) {
			errf(fmt.Sprintf("%s/ramp/%d", path, i), "stops go up in value")
		}
		prev = stop.Value
	}
}

// DriveChannels is the vocabulary, in the schema's order. hmi-3d's
// DRIVE_CHANNELS and the extension schema carry the same list; a test on
// each side reads the schema so the three cannot drift.
var DriveChannels = []string{"spin", "turn", "scale", "tint", "emissive", "visible", "pulse"}

// Environment is the surroundings (§3c): an HDRI, the backdrop, shadows.
type Environment struct {
	HDRI       string   `json:"hdri,omitempty"`
	Backdrop   string   `json:"backdrop,omitempty"`
	Background string   `json:"background,omitempty"`
	Fog        *Fog     `json:"fog,omitempty"`
	Floor      *float64 `json:"floor,omitempty"`
	Intensity  *float64 `json:"intensity,omitempty"`
	Shadows    *bool    `json:"shadows,omitempty"`
}

// Fog fades the far backdrop toward a colour.
type Fog struct {
	Color string   `json:"color,omitempty"`
	Near  *float64 `json:"near,omitempty"`
	Far   *float64 `json:"far,omitempty"`
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
	// Projection is perspective (default) or orthographic; Width is the
	// metres across an orthographic view.
	Projection string  `json:"projection,omitempty"`
	Width      float64 `json:"width,omitempty"`
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

// Port is where a pipe meets a kind, in the kind's frame. At is [x, y, z]
// metres or {part?, anchor, offset?}, a place on a box (checkPoint).
type Port struct {
	At  json.RawMessage `json:"at"`
	Dir Vec             `json:"dir,omitempty"`
}

// BoxPoint is a point named by where it sits on a box: a part's, or the
// kind's bounds when Part is empty. The renderer resolves it once the
// model is loaded; the checker holds the words.
type BoxPoint struct {
	Part   string `json:"part,omitempty"`
	Anchor string `json:"anchor"`
	Offset Vec    `json:"offset,omitempty"`
}

var anchorRe = regexp.MustCompile(`^(center|top|bottom|left|right|front|back)(-(top|bottom|left|right|front|back))?$`)

var anchorAxis = map[string]int{"left": 0, "right": 0, "bottom": 1, "top": 1, "back": 2, "front": 2}

// validAnchor: one face word, center, or two faces on different axes.
func validAnchor(a string) bool {
	if !anchorRe.MatchString(a) {
		return false
	}
	w := strings.Split(a, "-")
	return len(w) == 1 || (w[0] != "center" && anchorAxis[w[0]] != anchorAxis[w[1]])
}

// checkPoint holds a point to its forms: [x, y, z], a box point, and — when
// word is true (labelAt) — a bare anchor word.
func checkPoint(path string, raw json.RawMessage, word bool, errf func(path, format string, a ...any)) {
	var v Vec
	if err := json.Unmarshal(raw, &v); err == nil {
		if len(v) != 3 || !finite(v) {
			errf(path, "must be [x, y, z] metres")
		}
		return
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if !word || !validAnchor(s) {
			errf(path, "must be [x, y, z] metres, an anchor word (top, top-front, …), or { part?, anchor, offset? }")
		}
		return
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var bp BoxPoint
	if err := dec.Decode(&bp); err != nil {
		errf(path, "must be [x, y, z] metres%s, or { part?, anchor: top | bottom | left | right | front | back | center (or two, top-front), offset? }", map[bool]string{true: ", an anchor word on the bounds", false: ""}[word])
		return
	}
	if !validAnchor(bp.Anchor) {
		errf(path+"/anchor", "must be top, bottom, left, right, front, back, center, or two on different axes (top-front)")
	}
	if bp.Offset != nil && (len(bp.Offset) != 3 || !finite(bp.Offset)) {
		errf(path+"/offset", "must be [x, y, z] metres")
	}
}

// Anchor is a pipe end on a node's port: resolved through the node's
// pos / rot / scale by the renderer, so the pipe follows the node.
type Anchor struct {
	Node string `json:"node"`
	Port string `json:"port"`
}

type Pipe struct {
	ID string `json:"id,omitempty"`
	// From / To: the ends, when anchored to ports; Points is then the run
	// between them (may be empty: a straight pipe port to port).
	From   *Anchor           `json:"from,omitempty"`
	To     *Anchor           `json:"to,omitempty"`
	Points []Vec             `json:"points,omitempty"`
	Radius float64           `json:"radius,omitempty"`
	Bind   map[string]string `json:"bind,omitempty"`
	// Arrows: chevrons travel the run while it flows. nil = true.
	Arrows *bool `json:"arrows,omitempty"`
}

// Looks are the values Doc.Look takes; hmi-3d's LOOKS.
var Looks = []string{"lit", "flat", "paper"}

// Builtin is the contract the package's built-in kinds carry. It must
// match hmi-3d's BUILTIN_CONTRACT (kinds.ts) and models/kinds.json: same
// names, same members, same ports (TestBuiltinMatchesTheDataKinds).
var Builtin = map[string]Kind{
	"tank":  {Type: "Tank", Members: []string{"Level", "TempC"}, Ports: builtinPorts(`{"inlet": {"at": [0, 0.46, 0], "dir": [0, 1, 0]}, "outlet": {"at": [0.25, 0, 0], "dir": [1, 0, 0]}}`)},
	"pump":  {Type: "Motor", Members: []string{"Running", "Fault", "Speed"}, Ports: builtinPorts(`{"suction": {"at": [-0.21, 0.09, 0], "dir": [-1, 0, 0]}, "discharge": {"at": [0, 0.21, 0], "dir": [0, 1, 0]}}`)},
	"valve": {Type: "Valve", Members: []string{"Pos", "Cmd"}, Ports: builtinPorts(`{"in": {"at": [0, -0.08, 0], "dir": [0, -1, 0]}, "out": {"at": [0, 0.08, 0], "dir": [0, 1, 0]}}`)},
}

// builtinPorts decodes a built-in's ports from the same JSON a kind file
// would hold — the one spelling, so the table reads like the contract.
func builtinPorts(src string) map[string]Port {
	var out map[string]Port
	if err := json.Unmarshal([]byte(src), &out); err != nil {
		panic("builtin ports: " + err.Error())
	}
	return out
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
		cur.Component, cur.Assembly = v.Component, v.Assembly
		if v.Ports != nil {
			cur.Ports = v.Ports
		}
		// The members a kind reads are `members` plus whatever its drives,
		// its status template and its assembly name, so nothing repeats
		// the list.
		more := append(driveMembers(v.Drive), statusMembers(v.Status)...)
		more = append(more, assemblyMembers(v.Assembly)...)
		for _, m := range more {
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
		case d.Pulse != nil:
			add(d.Pulse.Bind)
		}
	}
	return out
}

// assemblyMembers lists what an assembly reads off its struct: every
// part's tag and the root of every ref inside it, deduplicated, in order.
func assemblyMembers(a *Assembly) []string {
	if a == nil {
		return nil
	}
	var out []string
	add := func(ref string) {
		m, _, _ := strings.Cut(strings.TrimPrefix(ref, "!"), ".")
		if m != "" && !contains(out, m) {
			out = append(out, m)
		}
	}
	for _, n := range a.Nodes {
		add(n.Tag)
		for _, k := range sortedKeys(n.Bind) {
			add(n.Bind[k])
		}
	}
	for _, p := range a.Pipes {
		add(p.Bind["flowing"])
	}
	return out
}

var portRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

func sortedPortNames(m map[string]Port) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// checkPipeRun holds a pipe's ends and run to the frame it sits in: an
// anchor names a node of the frame and a port its kind has (when the
// kind's ports are known — a kind of the app's is taken on trust), and
// the run has at least two points counting the anchored ends.
func checkPipeRun(p string, pipe Pipe, nodes []Node, kinds map[string]Kind, errf func(path, format string, a ...any)) {
	ends := 0
	for _, end := range []struct {
		key string
		a   *Anchor
	}{{"from", pipe.From}, {"to", pipe.To}} {
		if end.a == nil {
			continue
		}
		ends++
		ap := p + "/" + end.key
		if end.a.Node == "" {
			errf(ap+"/node", "must name a node")
			continue
		}
		if end.a.Port == "" {
			errf(ap+"/port", "must name one of the node's kind's ports")
			continue
		}
		var node *Node
		for i := range nodes {
			if nodes[i].ID == end.a.Node {
				node = &nodes[i]
				break
			}
		}
		if node == nil {
			errf(ap+"/node", "no node %q in this frame", end.a.Node)
			continue
		}
		if k, ok := kinds[node.Kind]; ok && k.Ports != nil {
			if _, has := k.Ports[end.a.Port]; !has {
				errf(ap+"/port", "kind %q has no port %q (it has %s)", node.Kind, end.a.Port, strings.Join(sortedPortNames(k.Ports), ", "))
			}
		}
	}
	if len(pipe.Points)+ends < 2 {
		if ends > 0 {
			errf(p+"/points", "needs at least two points counting the anchored ends")
		} else {
			errf(p+"/points", "needs at least two points")
		}
	}
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
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
var imageRe = regexp.MustCompile(`(?i)\.(jpe?g|png|webp|hdr|exr)$`)

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

// componentPath: a source path relative to the scene file — a .svelte
// file, no scheme, no `..`, not absolute.
func componentPath(p string) bool {
	return assetPath(p) && strings.HasSuffix(p, ".svelte") && !strings.HasPrefix(p, "/")
}

// checkKind holds a `kinds` entry's data-kind fields to the §3c rules and
// its component / assembly fields to §3d's; the parts' kinds and tags
// need the kind table and are checked in Check.
func checkKind(k string, v Kind, errf func(path, format string, a ...any)) {
	p := "/kinds/" + k
	if v.Model != "" && !(assetPath(v.Model) && (strings.HasSuffix(v.Model, ".glb") || strings.HasSuffix(v.Model, ".gltf"))) {
		errf(p+"/model", "must be a URL path to a .glb or .gltf (models/pump.glb)")
	}
	if v.Component != "" && !componentPath(v.Component) {
		errf(p+"/component", "must be a .svelte file as a path relative to the scene file (hmi/src/lib/Skid.svelte)")
	}
	var ways []string
	for _, w := range []struct {
		name string
		set  bool
	}{{"model", v.Model != ""}, {"component", v.Component != ""}, {"assembly", v.Assembly != nil}} {
		if w.set {
			ways = append(ways, w.name)
		}
	}
	if len(ways) > 1 {
		errf(p, "a kind is defined one way, not %s", strings.Join(ways, " and "))
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
	if v.LabelAt != nil {
		checkPoint(p+"/labelAt", v.LabelAt, true, errf)
	}
	if v.Status != "" && !validStatusTemplate(v.Status) {
		errf(p+"/status", `must be a template over members: "{Level:1} %%", "{Running?run:stopped}"`)
	}
	for _, name := range sortedPortNames(v.Ports) {
		port := v.Ports[name]
		pp := p + "/ports/" + name
		if !portRe.MatchString(name) {
			errf(pp, "a port name is letters, digits, _ or -")
		}
		if port.At == nil {
			errf(pp+"/at", "must be [x, y, z] metres in the kind's frame, or { part?, anchor, offset? }")
		} else {
			checkPoint(pp+"/at", port.At, false, errf)
		}
		if port.Dir != nil && (len(port.Dir) != 3 || !finite(port.Dir) || (port.Dir[0] == 0 && port.Dir[1] == 0 && port.Dir[2] == 0)) {
			errf(pp+"/dir", "must be a non-zero [x, y, z] direction")
		}
	}
	if v.Model == "" && v.Drive != nil {
		errf(p+"/drive", "drives need a model — without one the Svelte kind of this name renders")
	}
	if len(ways) == 0 && (v.Bounds != nil || v.LabelAt != nil) {
		errf(p, "bounds and labelAt need a model, a component or an assembly — without one the registry's kind of this name renders")
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
		if e.Blink != nil && *e.Blink <= 0 {
			errf(path+"/blink", "must be a rate in Hz, above 0")
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
		for _, set := range []bool{d.Spin != nil, d.Turn != nil, d.Scale != nil, d.Tint != nil, d.Emissive != nil, d.Visible != nil, d.Pulse != nil} {
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
			if d.Tint.Ramp != nil {
				checkRamp(dp+"/tint", d.Tint.Bind, d.Tint.Ramp, errf)
				if d.Tint.On != "" || d.Tint.Off != "" || d.Tint.Threshold != nil || d.Tint.Blink != nil {
					errf(dp+"/tint", "on, off, threshold and blink are not with a ramp")
				}
			} else {
				boolean(dp+"/tint", d.Tint.BoolExpr)
				if d.Tint.On == "" {
					errf(dp+"/tint/on", "must be a palette slot or a CSS colour")
				}
			}
		case d.Emissive != nil:
			if d.Emissive.Ramp != nil {
				checkRamp(dp+"/emissive", d.Emissive.Bind, d.Emissive.Ramp, errf)
				if d.Emissive.On != "" || d.Emissive.Threshold != nil || d.Emissive.Blink != nil {
					errf(dp+"/emissive", "on, threshold and blink are not with a ramp")
				}
			} else {
				boolean(dp+"/emissive", d.Emissive.BoolExpr)
				if d.Emissive.On == "" {
					errf(dp+"/emissive/on", "must be a palette slot or a CSS colour")
				}
			}
			if d.Emissive.Intensity != nil && *d.Emissive.Intensity < 0 {
				errf(dp+"/emissive/intensity", "must be ≥ 0")
			}
		case d.Visible != nil:
			boolean(dp+"/visible", *d.Visible)
		case d.Pulse != nil:
			boolean(dp+"/pulse", d.Pulse.BoolExpr)
			if d.Pulse.Hz != nil && *d.Pulse.Hz <= 0 {
				errf(dp+"/pulse/hz", "must be a rate in Hz, above 0")
			}
			if d.Pulse.Amount != nil && (*d.Pulse.Amount <= 0 || *d.Pulse.Amount > 1) {
				errf(dp+"/pulse/amount", "must be a fraction of the size, above 0 and at most 1")
			}
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
	if d.Environment != nil && d.Environment.Backdrop != "" {
		paths = append(paths, [2]string{"/environment/backdrop", d.Environment.Backdrop})
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

// Components lists every component file the document names, with the
// JSON path that names it — source paths relative to the scene file (§3d),
// so they are held to a different root than Assets.
func (d *Doc) Components() (paths [][2]string) {
	names := make([]string, 0, len(d.Kinds))
	for k := range d.Kinds {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		if c := d.Kinds[k].Component; c != "" {
			paths = append(paths, [2]string{"/kinds/" + k + "/component", c})
		}
	}
	return paths
}

// CheckComponents holds every component path to `exists`, which the
// caller builds from the scene file's own directory.
func CheckComponents(d *Doc, exists func(path string) bool) (errs []string) {
	for _, c := range d.Components() {
		if !exists(c[1]) {
			errs = append(errs, fmt.Sprintf("%s: %q was not found next to the scene file — a component path is relative to it", c[0], c[1]))
		}
	}
	return errs
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
		if v.DefinedBy() == "file" {
			// Unresolved (Check alone, without ResolveKindFiles): the path
			// is held to its rules; what the file holds is checked once read.
			if !kindPath(v.File) {
				errf("/kinds/"+k, "a kind file is a {Name}.kind.json as a path relative to the scene file (hmi/src/lib/kinds/pump.kind.json)")
			}
			continue
		}
		checkKind(k, v, errf)
		if v.Component != "" && v.Type == "" && len(v.Members) == 0 {
			warnf("/kinds/"+k, "declares a component but no type or members, so its nodes are not checked — add the UDT it reads (kinds.%s.type) and the members", k)
		}
	}

	if e := d.Environment; e != nil {
		if e.HDRI != "" && !(assetPath(e.HDRI) && (strings.HasSuffix(strings.ToLower(e.HDRI), ".hdr") || strings.HasSuffix(strings.ToLower(e.HDRI), ".exr"))) {
			errf("/environment/hdri", "must be a URL path to an .hdr or .exr (env/workshop_1k.hdr)")
		}
		if e.Backdrop != "" && !(assetPath(e.Backdrop) && imageRe.MatchString(e.Backdrop)) {
			errf("/environment/backdrop", "must be a URL path to an equirect image (.jpg, .png, .webp, .hdr, .exr)")
		}
		if f := e.Fog; f != nil {
			if f.Near != nil && *f.Near < 0 {
				errf("/environment/fog/near", "must be ≥ 0 metres")
			}
			near := 0.0
			if f.Near != nil {
				near = *f.Near
			}
			if f.Far != nil && *f.Far <= near {
				errf("/environment/fog/far", "must be greater than near")
			}
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

	if d.Look != "" && !slices.Contains(Looks, d.Look) {
		errf("/look", "must be 'lit', 'flat' or 'paper', not %q", d.Look)
	}

	if d.Camera != nil {
		vec3("/camera/pos", d.Camera.Pos, "metres")
		vec3("/camera/target", d.Camera.Target, "metres")
		ortho := d.Camera.Projection == "orthographic"
		if d.Camera.Projection != "" && d.Camera.Projection != "perspective" && !ortho {
			errf("/camera/projection", "must be 'perspective' or 'orthographic', not %q", d.Camera.Projection)
		}
		if d.Camera.Width < 0 {
			errf("/camera/width", "must be a number of metres > 0")
		} else if d.Camera.Width != 0 && !ortho {
			errf("/camera/width", "only an orthographic camera has a width")
		}
		if d.Camera.Fov != 0 && ortho {
			errf("/camera/fov", "an orthographic camera has no field of view; use width")
		} else if d.Camera.Fov != 0 && (d.Camera.Fov <= 0 || d.Camera.Fov >= 180) {
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

	// walk follows a member path down from a struct; the error names what
	// went wrong, or the leaf type comes back (nil for a struct member's
	// own struct, which comes back as `last`).
	walk := func(sd *ir.StructDef, from string, members []string) (leaf *ir.Type, err string) {
		t := sd
		for i, m := range members {
			if t == nil {
				return nil, fmt.Sprintf("%s is not a struct, so it has no member %q", strings.Join(append([]string{from}, members[:i]...), "."), m)
			}
			idx, ok := t.FieldIndex[m]
			if !ok {
				return nil, fmt.Sprintf("%s has no member %q (it has %s)", t.Name, m, memberList(t))
			}
			leaf = t.Fields[idx].Type
			t = nil
			if leaf != nil && leaf.Kind == ir.TypeStruct {
				t = leaf.Struct
			}
		}
		return leaf, ""
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
		leaf, werr := walk(info.Struct, parts[0], parts[1:])
		if werr != "" {
			errf(path, "%q: %s", ref, werr)
			return
		}
		if negate && leaf != nil && leaf.Kind != ir.TypeBool {
			warnf(path, "%q negates a %s member; `!` reads `value !== true`, which is always true here", ref, leaf.Kind)
		}
		if negate && len(parts) == 1 && info.Struct != nil {
			warnf(path, "%q negates a whole struct tag; `!` expects a BOOL", ref)
		}
	}

	// A ref inside an assembly is a path from the assembly's struct (§3d):
	// syntax here, the member walk per placed node below.
	checkInnerRef := func(path, ref string) {
		if !refRe.MatchString(ref) {
			errf(path, "%q is not a ref (Member, Member.Member, !Member)", ref)
		}
	}

	// A node's shape, wherever it sits: a document node, or an assembly's
	// part in the kind's own frame.
	checkNodeShape := func(p string, n Node, ids map[string]int, i int) (kind Kind, known bool) {
		if n.ID == "" {
			errf(p+"/id", "must be a non-empty string")
		} else if j, dup := ids[n.ID]; dup {
			errf(p+"/id", "duplicate id %q (also %s/%d)", n.ID, p[:strings.LastIndex(p, "/")], j)
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
		kind, known = kinds[n.Kind]
		if n.Kind == "" {
			errf(p+"/kind", "must be a non-empty string")
		} else if !known {
			errf(p+"/kind", "unknown kind %q (built in: %s; declare the app's own under kinds:)", n.Kind, kindList(kinds))
		}
		return kind, known
	}

	// Assemblies: parts are nodes in the kind's frame, tags are members,
	// refs are inner, and an assembly is flat.
	for _, k := range kindNames {
		a := d.Kinds[k].Assembly
		if a == nil {
			continue
		}
		ap := "/kinds/" + k + "/assembly"
		ids := make(map[string]int, len(a.Nodes))
		for i, n := range a.Nodes {
			p := fmt.Sprintf("%s/nodes/%d", ap, i)
			part, known := checkNodeShape(p, n, ids, i)
			if known && part.Assembly != nil {
				errf(p+"/kind", "%q is an assembly; a part is a built-in, a data kind or a component kind", n.Kind)
			}
			if n.Tag != "" && !memberRe.MatchString(n.Tag) {
				errf(p+"/tag", "a part's tag is one member of the assembly's struct (Pump), not %q", n.Tag)
			}
			for _, prop := range sortedKeys(n.Bind) {
				checkInnerRef(p+"/bind/"+prop, n.Bind[prop])
			}
		}
		for i, pipe := range a.Pipes {
			p := fmt.Sprintf("%s/pipes/%d", ap, i)
			checkPipeRun(p, pipe, a.Nodes, kinds, errf)
			for j, pt := range pipe.Points {
				vec3(fmt.Sprintf("%s/points/%d", p, j), pt, "metres")
			}
			for key, ref := range pipe.Bind {
				if key != "flowing" {
					errf(p+"/bind/"+key, "a pipe binds only 'flowing'")
					continue
				}
				checkInnerRef(p+"/bind/flowing", ref)
			}
		}
	}

	ids := make(map[string]int, len(d.Nodes))
	for i, n := range d.Nodes {
		p := fmt.Sprintf("/nodes/%d", i)
		kind, known := checkNodeShape(p, n, ids, i)
		for _, prop := range sortedKeys(n.Bind) {
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
			// An assembly's parts: each member is the part kind's UDT, and
			// each inner ref walks from the struct (§3d).
			if kind.Assembly != nil && info.Struct != nil {
				for j, part := range kind.Assembly.Nodes {
					pp := fmt.Sprintf("/kinds/%s/assembly/nodes/%d", n.Kind, j)
					if part.Tag == "" {
						continue
					}
					if _, bound := n.Bind[lower(part.Tag)]; bound {
						continue
					}
					pk, ok := kinds[part.Kind]
					if !ok || pk.Type == "" {
						continue
					}
					idx := info.Struct.FieldIndex[part.Tag]
					ft := info.Struct.Fields[idx].Type
					switch {
					case ft == nil || ft.Kind != ir.TypeStruct:
						errf(p+"/tag", "%s.%s is not a struct, but part %q (kind %q) expects a %s — see %s", info.TypeName, part.Tag, part.ID, part.Kind, pk.Type, pp+"/tag")
					case ft.Struct.Name != pk.Type:
						errf(p+"/tag", "%s.%s is a %s, but part %q (kind %q) expects a %s — see %s", info.TypeName, part.Tag, ft.Struct.Name, part.ID, part.Kind, pk.Type, pp+"/tag")
					}
					for _, prop := range sortedKeys(part.Bind) {
						ref := strings.TrimPrefix(part.Bind[prop], "!")
						if _, werr := walk(info.Struct, n.Tag, strings.Split(ref, ".")); werr != "" {
							errf(p+"/tag", "%q (%s/bind/%s): %s", part.Bind[prop], pp, prop, werr)
						}
					}
				}
				for j, pipe := range kind.Assembly.Pipes {
					if ref, ok := pipe.Bind["flowing"]; ok && refRe.MatchString(ref) {
						if _, werr := walk(info.Struct, n.Tag, strings.Split(strings.TrimPrefix(ref, "!"), ".")); werr != "" {
							errf(p+"/tag", "%q (/kinds/%s/assembly/pipes/%d/bind/flowing): %s", ref, n.Kind, j, werr)
						}
					}
				}
			}
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
		checkPipeRun(p, pipe, d.Nodes, kinds, errf)
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
