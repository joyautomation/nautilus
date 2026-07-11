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
// statement order), so they double as diff keys.
type Node struct {
	ID      string   `json:"id"`
	Kind    string   `json:"kind"`
	Label   string   `json:"label"`
	Type    string   `json:"type,omitempty"` // FB type name, kind == "fb" only
	Wire    string   `json:"wire,omitempty"` // wire name this block drives
	Inputs  []string `json:"inputs,omitempty"`
	Outputs []string `json:"outputs,omitempty"`
	Layer   int      `json:"layer"`
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
}

// Graph parses .fbd source and builds its render model. It walks the netlist
// WITHOUT the transpiler's inlining: each operator/function call becomes a
// block node, a wire's fan-out becomes multiple edges from one output pin,
// and inst.pin becomes an edge from an FB output pin. userFBs (optional)
// resolve pin lists for user-defined FB types; unknown types fall back to the
// pins the source actually uses.
func Graph(src string, userFBs ...map[string]*ir.FBDef) (*Model, error) {
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
	b.layer()
	return b.m, nil
}

// outRef identifies the output pin an expression's value comes from, plus
// negation/feedback accumulated on the way.
type outRef struct {
	node     string
	pin      string
	wire     string // named-wire label, if routed through one
	negated  bool
	feedback bool
}

type modelBuilder struct {
	nl      *netlist
	m       *Model
	nodes   map[string]*Node  // id -> node
	inputs  map[string]*Node  // input chip per variable name / member path
	coils   map[string]*Node  // coil node per target (first write wins)
	fbs     map[string]*Node  // fb node per instance name
	wireOut map[string]outRef // memoized wire resolutions (fan-out shares them)
	userFBs map[string]*ir.FBDef
	litSeq  int
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
		b.coils[n.target] = b.add(&Node{ID: "c:" + n.target, Kind: "coil", Label: n.target})
	}
	for _, d := range b.nl.fbDecls {
		b.fbNode(d.name, d.typ)
	}
	for _, n := range b.nl.nodes { // calls of instances declared in the ST header
		if n.isCall {
			b.fbNode(n.inst, "")
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
				b.m.Edges = append(b.m.Edges, &Edge{
					From: r.node, FromPin: r.pin, To: fb.ID, ToPin: a.pin,
					Wire: r.wire, Negated: r.negated, Feedback: r.feedback,
				})
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
			b.m.Edges = append(b.m.Edges, &Edge{
				From: r.node, FromPin: r.pin, To: b.coils[n.target].ID,
				Wire: r.wire, Negated: r.negated, Feedback: r.feedback,
			})
		}
	}
	return nil
}

// fbNode returns the node for an FB instance, creating it on first sight.
// Pin lists come from the built-in/user FB registries when the type is
// known; otherwise they accumulate from usage.
func (b *modelBuilder) fbNode(inst, typ string) *Node {
	if n, ok := b.fbs[inst]; ok {
		if n.Type == "" && typ != "" {
			n.Type = typ
			b.fbPins(n)
		}
		return n
	}
	n := b.add(&Node{ID: "f:" + inst, Kind: "fb", Label: inst, Type: typ})
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
		b.add(&Node{ID: id, Kind: "input", Label: x.text})
		return outRef{node: id}, nil
	case notExpr:
		r, err := b.source(x.inner, baseID, visited)
		r.negated = !r.negated
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
		return outRef{node: b.inputChip(x.name).ID}, nil
	case pinExpr:
		if fb, ok := b.fbs[x.inst]; ok {
			ensurePin(&fb.Outputs, x.pin)
			return outRef{node: fb.ID, pin: x.pin}, nil
		}
		// Not an FB instance: a struct-member read like M.Speed.
		return outRef{node: b.inputChip(x.inst + "." + x.pin).ID}, nil
	case callExpr:
		n := b.add(&Node{
			ID: baseID, Kind: "block", Label: x.fn,
			Inputs:  blockPins(x.fn, len(x.args)),
			Outputs: []string{"OUT"},
		})
		for i, a := range x.args {
			r, err := b.source(a, baseID+"."+strconv.Itoa(i), visited)
			if err != nil {
				return outRef{}, err
			}
			b.m.Edges = append(b.m.Edges, &Edge{
				From: r.node, FromPin: r.pin, To: n.ID, ToPin: n.Inputs[i],
				Wire: r.wire, Negated: r.negated, Feedback: r.feedback,
			})
		}
		return outRef{node: n.ID, pin: "OUT"}, nil
	}
	return outRef{}, fmt.Errorf("fbd: unrenderable expression %T", e)
}

// inputChip returns the shared input node for a variable (or struct member
// path), creating it on first use.
func (b *modelBuilder) inputChip(name string) *Node {
	if n, ok := b.inputs[name]; ok {
		return n
	}
	n := b.add(&Node{ID: "v:" + name, Kind: "input", Label: name})
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
