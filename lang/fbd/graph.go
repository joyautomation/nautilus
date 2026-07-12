package fbd

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/joyautomation/nautilus/lang/ir"
)

// Model is the FBD render model consumed by diagram tooling (the VS Code
// preview webview via `nautilus fbd graph`). It is a pure projection of the
// netlist — no coordinates, just topology plus a deterministic left-to-right
// layer per node — so the same source always yields the same JSON and the
// renderer owns all geometry.
//
// The model is pre-shaped for human-friendly drawing: each network (connected
// logic cone) is self-contained — input variable chips repeat per network and
// a read of another network's coil is a variable chip, not a wire across the
// sheet — so a renderer can recover the networks by connectivity alone and
// stack them as independent bands, the way FBD editors draw sheets.
type Model struct {
	Name  string  `json:"name"` // POU name (PROGRAM/FUNCTION_BLOCK ident)
	Nodes []*Node `json:"nodes"`
	Edges []*Edge `json:"edges"`
}

// Node is one diagram element. Kind decides the shape:
//
//	input — a variable/constant chip (far left; label is the name or literal)
//	block — an operator/function block (AND, ADD, GT, MIN, …)
//	fb    — a function-block instance (label = instance name, Type = FB type)
//	coil  — an output variable chip (far right; label is the variable)
//
// IDs are stable across edits that don't change the netlist structure (they
// derive from wire/instance/coil names plus argument position, never from
// statement order), so they double as diff keys. Layer is the left-to-right
// column within the node's network: longest-path depth for blocks/FBs, one
// left of the nearest consumer for input chips, the network's deepest layer
// for coils.
type Node struct {
	ID      string   `json:"id"`
	Kind    string   `json:"kind"`
	Label   string   `json:"label"`
	Type    string   `json:"type,omitempty"` // FB type name, kind == "fb" only
	Wire    string   `json:"wire,omitempty"` // wire name this block drives
	Inputs  []string `json:"inputs,omitempty"`
	Outputs []string `json:"outputs,omitempty"`
	Layer   int      `json:"layer"`
	// Src locates a constant chip's literal token in the .fbd source, so a
	// diagram editor can rewrite the value in place. Set for literal input
	// chips only.
	Src *Span `json:"src,omitempty"`
	// X/Y are a user-pinned position from the (* @layout *) block; absent
	// means auto-layout. Renderers place pinned nodes exactly here.
	X *int `json:"x,omitempty"`
	Y *int `json:"y,omitempty"`
	// Line is the 1-based source line this element comes from, so renderers
	// can join compiler diagnostics (which carry lines) onto diagram nodes.
	Line int `json:"line,omitempty"`
}

// Span locates an editable region in the .fbd source: the 1-based line/col
// of its first character, optionally the end (just past the last character),
// plus the source text so an editor can verify the document before applying
// a change.
type Span struct {
	Line    int    `json:"line"`
	Col     int    `json:"col"`
	EndLine int    `json:"endLine,omitempty"`
	EndCol  int    `json:"endCol,omitempty"`
	Text    string `json:"text,omitempty"`
}

// Edge is a wire from an output pin to an input pin. Pin names are "" for
// input/coil chips (they have a single implicit anchor). Feedback marks edges
// that close a loop (a coil read back into the logic, or an FB-call cycle);
// they are excluded from layering and typically routed around the diagram.
type Edge struct {
	From     string `json:"from"`
	FromPin  string `json:"fromPin,omitempty"`
	To       string `json:"to"`
	ToPin    string `json:"toPin,omitempty"`
	Wire     string `json:"wire,omitempty"` // signal name, when fed by a named wire
	Negated  bool   `json:"negated,omitempty"`
	Feedback bool   `json:"feedback,omitempty"`
	// Edit anchors. Arg spans the consumer's whole argument expression —
	// the insertion point for a new NOT, and the replacement range when the
	// input is rewired to a different source. When the edge is negated, Not
	// is the NOT keyword and Inner the expression it negates (deleting
	// [Not, Inner) removes the negation); they may sit inside a wire
	// definition when the negation rides a shared wire.
	Arg   *Span `json:"arg,omitempty"`
	Not   *Span `json:"not,omitempty"`
	Inner *Span `json:"inner,omitempty"`
}

// Graph parses .fbd source and builds its render model. It walks the netlist
// WITHOUT the transpiler's inlining: each operator/function call becomes a
// block node, a wire's fan-out becomes multiple edges from one output pin,
// and inst.pin becomes an edge from an FB output pin. userFBs (optional)
// resolve pin lists for user-defined FB types; unknown types fall back to the
// pins the source actually uses.
func Graph(src string, userFBs ...map[string]*ir.FBDef) (*Model, error) {
	b, err := buildModel(src, userFBs...)
	if err != nil {
		return nil, err
	}
	return b.m, nil
}

// buildModel is Graph exposing the builder — the edit service needs the
// netlist and node index behind the model, not just the JSON shape.
func buildModel(src string, userFBs ...map[string]*ir.FBDef) (*modelBuilder, error) {
	header, body, _, bodyLine, err := splitFBD(src)
	if err != nil {
		return nil, err
	}
	nl, err := parseNetlist(body, bodyLine)
	if err != nil {
		return nil, err
	}
	b := &modelBuilder{
		nl:      nl,
		src:     strings.Split(src, "\n"),
		exprOf:  map[string]callExpr{},
		m:       &Model{Name: pouName(header)},
		nodes:   map[string]*Node{},
		inputs:  map[string]*Node{},
		coils:   map[string]*Node{},
		fbs:     map[string]*Node{},
		wireOut: map[string]outRef{},
	}
	for _, reg := range userFBs {
		for name, def := range reg {
			if b.userFBs == nil {
				b.userFBs = map[string]*ir.FBDef{}
			}
			b.userFBs[strings.ToUpper(name)] = def
		}
	}
	if err := b.build(); err != nil {
		return nil, err
	}
	comp := b.components()
	b.splitByNetwork(comp)
	b.layer()
	b.placeInputs()
	b.alignCoils(comp)
	b.layout, b.layoutStart, b.layoutEnd = parseLayout(b.src)
	for id, e := range b.layout {
		if n, ok := b.nodes[id]; ok {
			x, y := e.x, e.y
			n.X, n.Y = &x, &y
		}
	}
	return b, nil
}

// outRef identifies the output pin an expression's value comes from, plus
// negation/feedback accumulated on the way. notSpan/innerSpan locate the
// outermost NOT that produced the negation (possibly inside a wire def).
type outRef struct {
	node      string
	pin       string
	wire      string // named-wire label, if routed through one
	negated   bool
	feedback  bool
	notSpan   *Span
	innerSpan *Span
}

type modelBuilder struct {
	nl  *netlist
	src []string // source lines, for slicing argument text into spans
	// pinned positions from the @layout block (nil when absent) and the
	// block's 1-based line span, for layout ops.
	layout                 map[string]layoutEntry
	layoutStart, layoutEnd int
	m                      *Model
	nodes                  map[string]*Node  // id -> node
	inputs                 map[string]*Node  // input chip per variable name / member path
	coils                  map[string]*Node  // coil node per target (first write wins)
	fbs                    map[string]*Node  // fb node per instance name
	wireOut                map[string]outRef // memoized wire resolutions (fan-out shares them)
	userFBs                map[string]*ir.FBDef
	exprOf                 map[string]callExpr // block node id -> the call that produced it
	litSeq                 int
}

func (b *modelBuilder) add(n *Node) *Node {
	b.nodes[n.ID] = n
	b.m.Nodes = append(b.m.Nodes, n)
	return n
}

// build creates all nodes and edges. Order is deterministic: coils and FB
// instances first (so references to them resolve), then wires in source
// order, then the FB-call and coil edges in source order.
func (b *modelBuilder) build() error {
	for _, n := range b.nl.nodes {
		if n.isCall {
			continue
		}
		if _, dup := b.coils[n.target]; dup {
			continue // repeated writes share one coil node (each adds an edge)
		}
		b.coils[n.target] = b.add(&Node{ID: "c:" + n.target, Kind: "coil", Label: n.target, Line: n.line})
	}
	for _, d := range b.nl.fbDecls {
		b.fbNode(d.name, d.typ, d.line)
	}
	for _, n := range b.nl.nodes { // calls of instances declared in the ST header
		if n.isCall {
			b.fbNode(n.inst, "", n.line)
		}
	}
	for _, w := range b.nl.wireSrc {
		if _, err := b.resolveWire(w, nil); err != nil {
			return err
		}
	}
	coilWrites := map[string]int{}
	for _, n := range b.nl.nodes {
		if n.isCall {
			fb := b.fbs[n.inst]
			for _, a := range n.args {
				r, err := b.source(a.val, "b:f."+n.inst+"."+a.pin, nil)
				if err != nil {
					return err
				}
				ensurePin(&fb.Inputs, a.pin)
				b.m.Edges = append(b.m.Edges, b.edgeWithSpans(&Edge{
					From: r.node, FromPin: r.pin, To: fb.ID, ToPin: a.pin,
					Wire: r.wire, Negated: r.negated, Feedback: r.feedback,
				}, a.val, r))
			}
		} else {
			baseID := "b:c." + n.target
			if k := coilWrites[n.target]; k > 0 {
				baseID += "#" + strconv.Itoa(k+1)
			}
			coilWrites[n.target]++
			r, err := b.source(n.source, baseID, nil)
			if err != nil {
				return err
			}
			b.m.Edges = append(b.m.Edges, b.edgeWithSpans(&Edge{
				From: r.node, FromPin: r.pin, To: b.coils[n.target].ID,
				Wire: r.wire, Negated: r.negated, Feedback: r.feedback,
			}, n.source, r))
		}
	}
	return nil
}

// edgeWithSpans anchors an edge for editing: Arg spans the consumer-side
// argument expression with its source text (where an inserted NOT goes, and
// what a rewire replaces — local to this consumer even when the value rides
// a shared wire), and, when negated, the spans of the NOT that did it.
func (b *modelBuilder) edgeWithSpans(e *Edge, arg expr, r outRef) *Edge {
	line, col := arg.pos()
	endLine, endCol := arg.end()
	e.Arg = &Span{
		Line: line, Col: col, EndLine: endLine, EndCol: endCol,
		Text: b.slice(line, col, endLine, endCol),
	}
	if r.negated {
		e.Not, e.Inner = r.notSpan, r.innerSpan
	}
	return e
}

// slice returns the source text between two 1-based positions (end
// exclusive), "" when out of range — a "" Text makes editors refuse the
// gesture rather than guess.
func (b *modelBuilder) slice(l1, c1, l2, c2 int) string {
	if l1 < 1 || l2 < l1 || l2 > len(b.src) {
		return ""
	}
	if l1 == l2 {
		line := b.src[l1-1]
		if c1 < 1 || c2 < c1 || c2-1 > len(line) {
			return ""
		}
		return line[c1-1 : c2-1]
	}
	var parts []string
	for l := l1; l <= l2; l++ {
		line := b.src[l-1]
		lo, hi := 0, len(line)
		if l == l1 {
			lo = c1 - 1
		}
		if l == l2 {
			hi = c2 - 1
		}
		if lo < 0 || hi < lo || hi > len(line) {
			return ""
		}
		parts = append(parts, line[lo:hi])
	}
	return strings.Join(parts, "\n")
}

// fbNode returns the node for an FB instance, creating it on first sight.
// Pin lists come from the built-in/user FB registries when the type is
// known; otherwise they accumulate from usage.
func (b *modelBuilder) fbNode(inst, typ string, line int) *Node {
	if n, ok := b.fbs[inst]; ok {
		if n.Type == "" && typ != "" {
			n.Type = typ
			b.fbPins(n)
		}
		return n
	}
	n := b.add(&Node{ID: "f:" + inst, Kind: "fb", Label: inst, Type: typ, Line: line})
	b.fbPins(n)
	b.fbs[inst] = n
	return n
}

func (b *modelBuilder) fbPins(n *Node) {
	var def *ir.FBDef
	if d, ok := b.userFBs[strings.ToUpper(n.Type)]; ok {
		def = d
	} else if d, ok := ir.FBs[strings.ToUpper(n.Type)]; ok {
		def = d
	}
	if def == nil {
		return
	}
	for _, s := range def.Inputs {
		ensurePin(&n.Inputs, s.Name)
	}
	for _, s := range def.Outputs {
		ensurePin(&n.Outputs, s.Name)
	}
}

// resolveWire memoizes a named wire's source so fan-out shares one output
// pin. visited guards wire→wire cycles (same rule as the transpiler).
func (b *modelBuilder) resolveWire(name string, visited []string) (outRef, error) {
	if r, ok := b.wireOut[name]; ok {
		return r, nil
	}
	for _, v := range visited {
		if v == name {
			return outRef{}, fmt.Errorf("fbd: combinational loop through wire %q", name)
		}
	}
	r, err := b.source(b.nl.wires[name], "b:w."+name, append(visited, name))
	if err != nil {
		return outRef{}, err
	}
	if n, ok := b.nodes[r.node]; ok && n.Kind == "block" && n.ID == "b:w."+name {
		n.Wire = name
	}
	r.wire = name
	b.wireOut[name] = r
	return r, nil
}

// source resolves an expression to the output pin that carries its value,
// creating block/input nodes (and their internal edges) as needed. baseID is
// the node id to use if the expression itself is a call; nested calls extend
// it with their argument position, which keeps ids stable across reordering.
func (b *modelBuilder) source(e expr, baseID string, visited []string) (outRef, error) {
	switch x := e.(type) {
	case litExpr:
		id := "k:" + strconv.Itoa(b.litSeq)
		b.litSeq++
		// Src.Text is the literal AS WRITTEN (sliced from the source, e.g.
		// TIME#5s), not the canonical label — editors verify and replace the
		// real bytes.
		raw := b.slice(x.line, x.col, x.endLine, x.endCol)
		if raw == "" {
			raw = x.text
		}
		b.add(&Node{
			ID: id, Kind: "input", Label: x.text, Line: x.line,
			Src: &Span{Line: x.line, Col: x.col, EndLine: x.endLine, EndCol: x.endCol, Text: raw},
		})
		return outRef{node: id}, nil
	case notExpr:
		r, err := b.source(x.inner, baseID, visited)
		r.negated = !r.negated
		il, ic := x.inner.pos()
		r.notSpan = &Span{Line: x.line, Col: x.col, Text: "NOT"}
		r.innerSpan = &Span{Line: il, Col: ic}
		return r, err
	case refExpr:
		if _, isWire := b.nl.wires[x.name]; isWire {
			return b.resolveWire(x.name, visited)
		}
		if c, ok := b.coils[x.name]; ok {
			// A variable read that is also coil-written: the classic seal-in
			// feedback wire, drawn from the coil back into the logic.
			return outRef{node: c.ID, feedback: true}, nil
		}
		return outRef{node: b.inputChip(x.name, x.line).ID}, nil
	case pinExpr:
		if fb, ok := b.fbs[x.inst]; ok {
			ensurePin(&fb.Outputs, x.pin)
			return outRef{node: fb.ID, pin: x.pin}, nil
		}
		// Not an FB instance: a struct-member read like M.Speed.
		return outRef{node: b.inputChip(x.inst+"."+x.pin, x.line).ID}, nil
	case callExpr:
		b.exprOf[baseID] = x
		n := b.add(&Node{
			ID: baseID, Kind: "block", Label: x.fn, Line: x.line,
			Inputs:  blockPins(x.fn, len(x.args)),
			Outputs: []string{"OUT"},
		})
		for i, a := range x.args {
			r, err := b.source(a, baseID+"."+strconv.Itoa(i), visited)
			if err != nil {
				return outRef{}, err
			}
			b.m.Edges = append(b.m.Edges, b.edgeWithSpans(&Edge{
				From: r.node, FromPin: r.pin, To: n.ID, ToPin: n.Inputs[i],
				Wire: r.wire, Negated: r.negated, Feedback: r.feedback,
			}, a, r))
		}
		return outRef{node: n.ID, pin: "OUT"}, nil
	}
	return outRef{}, fmt.Errorf("fbd: unrenderable expression %T", e)
}

// inputChip returns the shared input node for a variable (or struct member
// path), creating it on first use.
func (b *modelBuilder) inputChip(name string, line int) *Node {
	if n, ok := b.inputs[name]; ok {
		return n
	}
	n := b.add(&Node{ID: "v:" + name, Kind: "input", Label: name, Line: line})
	b.inputs[name] = n
	return n
}

// layer assigns longest-path layers: inputs at 0, every other node one past
// its deepest forward input. Feedback edges don't count, and any cycle among
// FB calls (legal — scan semantics) is broken deterministically by marking
// the closing edge as feedback. Coils are then aligned to the rightmost
// column, the IEC visual convention.
func (b *modelBuilder) layer() {
	inEdges := map[string][]*Edge{}
	for _, e := range b.m.Edges {
		inEdges[e.To] = append(inEdges[e.To], e)
	}
	const (
		white = 0 // unvisited
		grey  = 1 // in progress (on the DFS stack)
		black = 2 // done
	)
	state := map[string]int{}
	var visit func(id string) int
	visit = func(id string) int {
		n := b.nodes[id]
		if state[id] == black {
			return n.Layer
		}
		if state[id] == grey {
			return -1 // cycle: caller marks the closing edge as feedback
		}
		state[id] = grey
		layer := 0
		if n.Kind != "input" {
			deepest := -1
			for _, e := range inEdges[id] {
				if e.Feedback {
					continue
				}
				if l := visit(e.From); l < 0 {
					e.Feedback = true
				} else if l > deepest {
					deepest = l
				}
			}
			layer = deepest + 1
			if layer == 0 {
				layer = 1 // e.g. an FB with only feedback/constant-free inputs
			}
		}
		n.Layer = layer
		state[id] = black
		return layer
	}
	max := 0
	for _, n := range b.m.Nodes {
		if l := visit(n.ID); l > max {
			max = l
		}
	}
	for _, n := range b.m.Nodes {
		if n.Kind == "coil" {
			n.Layer = max
		}
	}
}

// components groups the non-input nodes (blocks, FBs, coils) into networks —
// the independently-drawn logic cones of an FBD sheet — via union-find over
// non-feedback edges. Input chips deliberately don't union: two networks
// reading the same variable stay separate sheets, each with its own copy of
// the variable box (splitByNetwork makes the copies). Component indices are
// deterministic: numbered by first appearance in the node list.
func (b *modelBuilder) components() map[string]int {
	parent := map[string]string{}
	var find func(string) string
	find = func(x string) string {
		if parent[x] == "" || parent[x] == x {
			parent[x] = x
			return x
		}
		r := find(parent[x])
		parent[x] = r
		return r
	}
	for _, n := range b.m.Nodes {
		if n.Kind != "input" {
			find(n.ID)
		}
	}
	for _, e := range b.m.Edges {
		if e.Feedback {
			continue
		}
		from, to := b.nodes[e.From], b.nodes[e.To]
		if from.Kind == "input" || to.Kind == "input" {
			continue
		}
		parent[find(e.From)] = find(e.To)
	}
	comp := map[string]int{}
	next := 0
	for _, n := range b.m.Nodes {
		if n.Kind == "input" {
			continue
		}
		r := find(n.ID)
		if _, ok := comp[r]; !ok {
			comp[r] = next
			next++
		}
		comp[n.ID] = comp[r]
	}
	return comp
}

// splitByNetwork gives each network its own copy of every input chip it
// reads (the IEC convention: a variable box repeats per network) and turns
// cross-network coil reads into ordinary variable chips — only a read inside
// the coil's own cone stays drawn as a seal-in feedback wire. The first
// network to use a chip keeps the original id; later copies append #2, #3, …
// so ids stay deterministic diff keys.
func (b *modelBuilder) splitByNetwork(comp map[string]int) {
	// chip instance per (variable label, network)
	byComp := map[string]map[int]*Node{}
	count := map[string]int{}
	instance := func(baseID, label string, c int) *Node {
		if byComp[baseID] == nil {
			byComp[baseID] = map[int]*Node{}
		}
		if n, ok := byComp[baseID][c]; ok {
			return n
		}
		count[baseID]++
		k := count[baseID]
		if orig, ok := b.nodes[baseID]; ok && orig.Kind == "input" && k == 1 {
			byComp[baseID][c] = orig // first network keeps the original chip
			return orig
		}
		id := baseID
		if k > 1 {
			id = fmt.Sprintf("%s#%d", baseID, k)
		}
		line := 0
		if orig, ok := b.nodes[baseID]; ok {
			line = orig.Line
		}
		n := b.add(&Node{ID: id, Kind: "input", Label: label, Line: line})
		byComp[baseID][c] = n
		return n
	}
	for _, e := range b.m.Edges {
		tc := comp[e.To]
		from := b.nodes[e.From]
		switch {
		case from.Kind == "input":
			// Literals are per-use already; shared variable chips re-home to
			// the reading network's copy.
			if strings.HasPrefix(from.ID, "v:") {
				e.From = instance(from.ID, from.Label, tc).ID
			}
		case from.Kind == "coil" && e.Feedback && comp[e.From] != tc:
			// Reading another network's coil: a variable box, not a wire
			// across the sheet.
			e.From = instance("v:"+from.Label, from.Label, tc).ID
			e.Feedback = false
		}
	}
	// Drop variable chips whose consumers all moved to copies.
	used := map[string]bool{}
	for _, e := range b.m.Edges {
		used[e.From] = true
		used[e.To] = true
	}
	kept := b.m.Nodes[:0]
	for _, n := range b.m.Nodes {
		if n.Kind == "input" && !used[n.ID] {
			delete(b.nodes, n.ID)
			continue
		}
		kept = append(kept, n)
	}
	b.m.Nodes = kept
}

// placeInputs pulls every input chip up against its nearest consumer —
// layer = min(consumer layer) − 1 — instead of a single far-left column, so
// wires stay short. Runs after layer(); chips contribute 0 to block depths
// either way.
func (b *modelBuilder) placeInputs() {
	minTo := map[string]int{}
	for _, e := range b.m.Edges {
		if from := b.nodes[e.From]; from == nil || from.Kind != "input" {
			continue
		}
		l := b.nodes[e.To].Layer
		if cur, ok := minTo[e.From]; !ok || l < cur {
			minTo[e.From] = l
		}
	}
	for _, n := range b.m.Nodes {
		if n.Kind != "input" {
			continue
		}
		if l, ok := minTo[n.ID]; ok && l > 0 {
			n.Layer = l - 1
		}
	}
}

// alignCoils right-aligns each coil to its own network's deepest layer (each
// network is drawn as an independent band, so a global rail would just
// stretch wires).
func (b *modelBuilder) alignCoils(comp map[string]int) {
	maxOf := map[int]int{}
	for _, n := range b.m.Nodes {
		if c, ok := comp[n.ID]; ok && n.Layer > maxOf[c] {
			maxOf[c] = n.Layer
		}
	}
	for _, n := range b.m.Nodes {
		if n.Kind == "coil" {
			n.Layer = maxOf[comp[n.ID]]
		}
	}
}

func ensurePin(pins *[]string, name string) {
	for _, p := range *pins {
		if p == name {
			return
		}
	}
	*pins = append(*pins, name)
}

// blockPins names an operator/function block's input pins per IEC
// convention: IN for unary, IN1..INn for extensible/binary operators, and
// the standard formal names for the few functions that have them.
func blockPins(fn string, n int) []string {
	switch fn {
	case "LIMIT":
		if n == 3 {
			return []string{"MN", "IN", "MX"}
		}
	case "SEL":
		if n == 3 {
			return []string{"G", "IN0", "IN1"}
		}
	case "MUX":
		if n >= 2 {
			pins := []string{"K"}
			for i := 0; i < n-1; i++ {
				pins = append(pins, "IN"+strconv.Itoa(i))
			}
			return pins
		}
	}
	if n == 1 {
		return []string{"IN"}
	}
	pins := make([]string, n)
	for i := range pins {
		pins[i] = "IN" + strconv.Itoa(i+1)
	}
	return pins
}

var pouNameRe = regexp.MustCompile(`(?im)^\s*(?:PROGRAM|FUNCTION_BLOCK)\s+([A-Za-z_][A-Za-z0-9_]*)`)

// pouName extracts the POU identifier from the ST header, "" if not found.
func pouName(header string) string {
	if m := pouNameRe.FindStringSubmatch(header); m != nil {
		return m[1]
	}
	return ""
}
