package fbd

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/joyautomation/nautilus/lang/st"
)

// Structural edit operations for diagram tooling. A gesture in the FBD
// editor becomes an EditOp addressed by the render model's stable ids; the
// op resolves against a FRESH parse of the current source (so a moved buffer
// can't misfire) and returns the minimal text edits that realize it. The
// text stays the source of truth; no consumer ever computes spans itself.

// TextEdit is one replacement in the .fbd source: 1-based positions, end
// exclusive. An empty NewText deletes; Line==EndLine+Col==EndCol inserts.
type TextEdit struct {
	Line    int    `json:"line"`
	Col     int    `json:"col"`
	EndLine int    `json:"endLine"`
	EndCol  int    `json:"endCol"`
	NewText string `json:"newText"`
}

// EditOp is one structural edit, addressed by render-model ids:
//
//	setLiteral  Node (a k: constant chip), Value
//	toggleNot   To/ToPin (+From/FromPin to disambiguate fan-in)
//	rewire      To/ToPin (+From/FromPin), Source node id (+SourcePin for FBs)
//	rename      Node (b:w.* wire or f:* instance), NewName
//	deleteNode  Node (b:w.* wire, c:* coil, or f:* instance)
//	insertStatement  Text (netlist statement(s), validated before insert)
//	setLayout   Node, X, Y — pin a dragged node's position
//	clearLayout Node (one entry) or nothing (whole block → full auto-layout)
//	disconnect  To/ToPin (+From/FromPin) — remove the connection into a pin
//	addInput    Node (extensible block), Source (+SourcePin) — append an arg
//	declareVar  NewName, Value (type), Text (section) — add a declaration
type EditOp struct {
	Type      string `json:"type"`
	Node      string `json:"node,omitempty"`
	To        string `json:"to,omitempty"`
	ToPin     string `json:"toPin,omitempty"`
	From      string `json:"from,omitempty"`
	FromPin   string `json:"fromPin,omitempty"`
	Value     string `json:"value,omitempty"`
	NewName   string `json:"newName,omitempty"`
	Source    string `json:"source,omitempty"`
	SourcePin string `json:"sourcePin,omitempty"`
	Text      string `json:"text,omitempty"`
	X         *int   `json:"x,omitempty"`
	Y         *int   `json:"y,omitempty"`
	// Entries batches setLayout: a multi-node drag pins every moved node in
	// ONE op — one text edit, no lost updates.
	Entries []LayoutOpEntry `json:"entries,omitempty"`
	// Nodes lists the selection for duplicate (copy/paste).
	Nodes []string `json:"nodes,omitempty"`
}

// LayoutOpEntry is one node's pinned position in a batched setLayout.
type LayoutOpEntry struct {
	Node string `json:"node"`
	X    int    `json:"x"`
	Y    int    `json:"y"`
}

// ApplyEdit resolves op against src and returns the text edits realizing it.
func ApplyEdit(src string, op EditOp) ([]TextEdit, error) {
	b, err := buildModel(src)
	if err != nil {
		return nil, err
	}
	switch op.Type {
	case "setLiteral":
		return b.opSetLiteral(op)
	case "toggleNot":
		return b.opToggleNot(op)
	case "rewire":
		return b.opRewire(op)
	case "rename":
		return b.opRename(op)
	case "deleteNode":
		return b.opDelete(op)
	case "insertStatement":
		return b.opInsert(op)
	case "setLayout":
		return b.opSetLayout(op)
	case "clearLayout":
		return b.opClearLayout(op)
	case "disconnect":
		return b.opDisconnect(op)
	case "addInput":
		return b.opAddInput(op)
	case "declareVar":
		return b.opDeclareVar(op)
	case "deleteVar":
		return b.opDeleteVar(op)
	case "setComment":
		return b.opSetComment(op)
	case "duplicate":
		return b.opDuplicate(op)
	}
	return nil, fmt.Errorf("fbd edit: unknown op %q", op.Type)
}

func spanEdit(s *Span, newText string) TextEdit {
	return TextEdit{Line: s.Line, Col: s.Col, EndLine: s.EndLine, EndCol: s.EndCol, NewText: newText}
}

func posEdit(s exprPos, newText string) TextEdit {
	return TextEdit{Line: s.line, Col: s.col, EndLine: s.endLine, EndCol: s.endCol, NewText: newText}
}

// ── setLiteral ─────────────────────────────────────────────────────────────

func (b *modelBuilder) opSetLiteral(op EditOp) ([]TextEdit, error) {
	n, ok := b.nodes[op.Node]
	if !ok || n.Src == nil {
		return nil, fmt.Errorf("fbd edit: %q is not an editable constant", op.Node)
	}
	v := strings.TrimSpace(op.Value)
	if !isLiteral(v) {
		return nil, fmt.Errorf("fbd edit: %q is not a literal value", v)
	}
	if v == n.Src.Text {
		return nil, nil
	}
	return []TextEdit{spanEdit(n.Src, v)}, nil
}

// isLiteral accepts exactly the constants the netlist grammar does: one
// number/string/time/typed-literal token, or TRUE/FALSE.
func isLiteral(v string) bool {
	toks := st.Lex(v)
	if len(toks) != 2 { // token + EOF
		return false
	}
	switch toks[0].Type {
	case st.TokenNumber, st.TokenString, st.TokenTimeLiteral, st.TokenTypedLiteral:
		return true
	case st.TokenIdent:
		u := strings.ToUpper(toks[0].Literal)
		return u == "TRUE" || u == "FALSE"
	}
	return false
}

// ── toggleNot / rewire ─────────────────────────────────────────────────────

// findEdge locates the edge for an input pin; From/FromPin disambiguate when
// a pin legitimately has fan-in (repeated coil writes).
func (b *modelBuilder) findEdge(op EditOp) (*Edge, error) {
	var match *Edge
	for _, e := range b.m.Edges {
		if e.To != op.To || e.ToPin != op.ToPin {
			continue
		}
		if op.From != "" && (e.From != op.From || e.FromPin != op.FromPin) {
			continue
		}
		if match != nil {
			return nil, fmt.Errorf("fbd edit: ambiguous input %s.%s", op.To, op.ToPin)
		}
		match = e
	}
	if match == nil {
		return nil, fmt.Errorf("fbd edit: no connection into %s.%s", op.To, op.ToPin)
	}
	if match.Arg == nil || match.Arg.Text == "" {
		return nil, fmt.Errorf("fbd edit: connection into %s.%s has no editable source span", op.To, op.ToPin)
	}
	return match, nil
}

func (b *modelBuilder) opToggleNot(op EditOp) ([]TextEdit, error) {
	e, err := b.findEdge(op)
	if err != nil {
		return nil, err
	}
	if e.Negated && e.Not != nil && e.Inner != nil {
		// Delete [NOT, operand) — the keyword and its whitespace.
		return []TextEdit{{
			Line: e.Not.Line, Col: e.Not.Col,
			EndLine: e.Inner.Line, EndCol: e.Inner.Col,
		}}, nil
	}
	return []TextEdit{{
		Line: e.Arg.Line, Col: e.Arg.Col,
		EndLine: e.Arg.Line, EndCol: e.Arg.Col,
		NewText: "NOT ",
	}}, nil
}

func (b *modelBuilder) opRewire(op EditOp) ([]TextEdit, error) {
	// Dropping onto a ghost output chip writes its first (real) coil.
	if strings.HasPrefix(op.To, "g:out.") {
		return b.wireGhostCoil(op)
	}
	e, err := b.findEdge(op)
	if err != nil {
		// Dropping a wire on a currently-unwired FB input pin ADDS the named
		// argument to the call — how a freshly inserted block gets hooked up.
		if strings.HasPrefix(op.To, "f:") && strings.Contains(err.Error(), "no connection") {
			return b.wireNewFBPin(op)
		}
		return nil, err
	}
	ref, err := b.refText(op.Source, op.SourcePin)
	if err != nil {
		return nil, err
	}
	if ref == e.Arg.Text {
		return nil, nil
	}
	// A ghost source just became real text: its pin moves to the real chip id.
	return append([]TextEdit{spanEdit(e.Arg, ref)}, b.ghostConsumed(op.Source)...), nil
}

// wireNewFBPin appends "PIN := ref" to an FB call for a pin that has no
// argument yet.
func (b *modelBuilder) wireNewFBPin(op EditOp) ([]TextEdit, error) {
	inst := strings.TrimPrefix(op.To, "f:")
	fbNode, ok := b.nodes[op.To]
	if !ok {
		return nil, fmt.Errorf("fbd edit: unknown instance %q", inst)
	}
	valid := false
	for _, p := range fbNode.Inputs {
		if p == op.ToPin {
			valid = true
		}
	}
	if !valid {
		return nil, fmt.Errorf("fbd edit: %s has no input pin %q", inst, op.ToPin)
	}
	ref, err := b.refText(op.Source, op.SourcePin)
	if err != nil {
		return nil, err
	}
	for _, n := range b.nl.nodes {
		if n.isCall && n.inst == inst && len(n.args) > 0 {
			l, c := n.args[len(n.args)-1].val.end()
			return append([]TextEdit{{Line: l, Col: c, EndLine: l, EndCol: c,
				NewText: ", " + op.ToPin + " := " + ref}}, b.ghostConsumed(op.Source)...), nil
		}
	}
	return nil, fmt.Errorf("fbd edit: %s has no call statement to extend", inst)
}

// refText is the netlist expression that reads a node's output: the variable
// or wire name, or inst.pin for a function block.
func (b *modelBuilder) refText(nodeID, pin string) (string, error) {
	n, ok := b.nodes[nodeID]
	if !ok {
		return "", fmt.Errorf("fbd edit: unknown source %q", nodeID)
	}
	switch n.Kind {
	case "input", "coil":
		return n.Label, nil
	case "fb":
		if pin == "" {
			if len(n.Outputs) == 0 {
				return "", fmt.Errorf("fbd edit: %s has no output pins", n.Label)
			}
			pin = n.Outputs[0]
		}
		return n.Label + "." + pin, nil
	case "block":
		if n.Wire == "" {
			return "", fmt.Errorf("fbd edit: name this block's output wire before connecting it elsewhere")
		}
		return n.Wire, nil
	}
	return "", fmt.Errorf("fbd edit: %q cannot be a source", nodeID)
}

// ── rename ─────────────────────────────────────────────────────────────────

var identRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func (b *modelBuilder) opRename(op EditOp) ([]TextEdit, error) {
	newName := strings.TrimSpace(op.NewName)
	if !identRe.MatchString(newName) {
		return nil, fmt.Errorf("fbd edit: %q is not a valid identifier", newName)
	}
	if b.nameTaken(newName) {
		return nil, fmt.Errorf("fbd edit: the name %q is already in use", newName)
	}
	switch {
	case strings.HasPrefix(op.Node, "b:w."):
		return b.renameWire(strings.TrimPrefix(op.Node, "b:w."), newName)
	case strings.HasPrefix(op.Node, "f:"):
		return b.renameInstance(strings.TrimPrefix(op.Node, "f:"), newName)
	case strings.HasPrefix(op.Node, "c:"), strings.HasPrefix(op.Node, "v:"):
		return nil, fmt.Errorf("fbd edit: %q is a variable — rename it in the declarations (the netlist follows the declaration)", op.Node)
	}
	return nil, fmt.Errorf("fbd edit: %q is not renameable", op.Node)
}

func (b *modelBuilder) nameTaken(name string) bool {
	if _, ok := b.nl.wires[name]; ok {
		return true
	}
	for _, d := range b.nl.fbDecls {
		if d.name == name {
			return true
		}
	}
	for _, n := range b.nl.nodes {
		if !n.isCall && n.target == name {
			return true
		}
	}
	return false
}

func (b *modelBuilder) renameWire(name, newName string) ([]TextEdit, error) {
	lhs, ok := b.nl.wirePos[name]
	if !ok {
		return nil, fmt.Errorf("fbd edit: no wire named %q", name)
	}
	edits := []TextEdit{posEdit(lhs, newName)}
	b.eachExpr(func(e expr) {
		if r, ok := e.(refExpr); ok && r.name == name {
			edits = append(edits, posEdit(r.exprPos, newName))
		}
	})
	edits = append(edits, b.remapLayout(idPrefixRewrite("b:w."+name, "b:w."+newName))...)
	return edits, nil
}

// idPrefixRewrite moves layout entries when a rename changes node ids: the
// exact id and any nested ".suffix" children follow the new name.
func idPrefixRewrite(oldPrefix, newPrefix string) func(string) (string, bool) {
	return func(id string) (string, bool) {
		if id == oldPrefix {
			return newPrefix, true
		}
		if strings.HasPrefix(id, oldPrefix+".") {
			return newPrefix + id[len(oldPrefix):], true
		}
		return id, true
	}
}

// idPrefixDrop removes layout entries for a deleted node and its children.
func idPrefixDrop(prefixes ...string) func(string) (string, bool) {
	return func(id string) (string, bool) {
		for _, p := range prefixes {
			if id == p || strings.HasPrefix(id, p+".") || strings.HasPrefix(id, p+"#") {
				return "", false
			}
		}
		return id, true
	}
}

func (b *modelBuilder) renameInstance(inst, newName string) ([]TextEdit, error) {
	var edits []TextEdit
	for _, d := range b.nl.fbDecls {
		if d.name == inst {
			edits = append(edits, posEdit(d.namePos, newName))
		}
	}
	if len(edits) == 0 {
		return nil, fmt.Errorf("fbd edit: no instance named %q", inst)
	}
	for _, n := range b.nl.nodes {
		// A call statement separate from the declaration repeats the name.
		if n.isCall && n.inst == inst && n.lhs != (exprPos{}) {
			dup := false
			for _, d := range b.nl.fbDecls {
				if d.name == inst && d.namePos == n.lhs {
					dup = true // inline decl+call: one token, already edited
				}
			}
			if !dup {
				edits = append(edits, posEdit(n.lhs, newName))
			}
		}
	}
	b.eachExpr(func(e expr) {
		if pe, ok := e.(pinExpr); ok && pe.inst == inst {
			// The span covers inst.pin; replace just the instance part.
			edits = append(edits, TextEdit{
				Line: pe.line, Col: pe.col,
				EndLine: pe.line, EndCol: pe.col + len(inst),
				NewText: newName,
			})
		}
	})
	instRewrite := idPrefixRewrite("f:"+inst, "f:"+newName)
	argRewrite := idPrefixRewrite("b:f."+inst, "b:f."+newName)
	edits = append(edits, b.remapLayout(func(id string) (string, bool) {
		id, _ = instRewrite(id)
		id, _ = argRewrite(id)
		return id, true
	})...)
	return edits, nil
}

// eachExpr walks every expression in the netlist (wire definitions, coil
// sources, FB call arguments), depth-first.
func (b *modelBuilder) eachExpr(visit func(expr)) {
	var walk func(e expr)
	walk = func(e expr) {
		visit(e)
		switch x := e.(type) {
		case notExpr:
			walk(x.inner)
		case callExpr:
			for _, a := range x.args {
				walk(a)
			}
		}
	}
	for _, name := range b.nl.wireSrc {
		walk(b.nl.wires[name])
	}
	for _, n := range b.nl.nodes {
		if n.isCall {
			for _, a := range n.args {
				walk(a.val)
			}
		} else {
			walk(n.source)
		}
	}
}

// ── insertStatement ────────────────────────────────────────────────────────

// opInsert appends netlist statement(s) just above END_FBD — validated as a
// parseable fragment with no name collisions BEFORE anything is written, so
// a bad palette entry becomes an explanation instead of a broken file.
func (b *modelBuilder) opInsert(op EditOp) ([]TextEdit, error) {
	stmt := strings.TrimSpace(op.Text)
	if stmt == "" {
		return nil, fmt.Errorf("fbd edit: nothing to insert")
	}
	frag, err := parseNetlist(stmt, 0)
	if err != nil {
		return nil, fmt.Errorf("fbd edit: not a valid netlist statement: %v", err)
	}
	for _, w := range frag.wireSrc {
		if b.nameTaken(w) {
			return nil, fmt.Errorf("fbd edit: the name %q is already in use", w)
		}
	}
	for _, d := range frag.fbDecls {
		if b.nameTaken(d.name) {
			return nil, fmt.Errorf("fbd edit: the name %q is already in use", d.name)
		}
	}
	endFBD := -1
	for i, line := range b.src {
		if strings.EqualFold(strings.TrimSpace(line), "END_FBD") {
			endFBD = i + 1 // 1-based
		}
	}
	if endFBD == -1 {
		return nil, fmt.Errorf("fbd edit: no END_FBD to insert before")
	}
	var text strings.Builder
	for _, line := range strings.Split(stmt, "\n") {
		text.WriteString("  " + strings.TrimSpace(line) + "\n")
	}
	return []TextEdit{{Line: endFBD, Col: 1, EndLine: endFBD, EndCol: 1, NewText: text.String()}}, nil
}

// ── deleteNode ─────────────────────────────────────────────────────────────

func (b *modelBuilder) opDelete(op EditOp) ([]TextEdit, error) {
	switch {
	case strings.HasPrefix(op.Node, "b:w."):
		name := strings.TrimPrefix(op.Node, "b:w.")
		span, ok := b.nl.wireSpan[name]
		if !ok {
			return nil, fmt.Errorf("fbd edit: no wire named %q", name)
		}
		// References the wire still feeds become undeclared identifiers —
		// allowed by design: the edit lands, diagnostics mark the holes.
		return append([]TextEdit{b.deleteSpan(span)},
			b.remapLayout(idPrefixDrop("b:w."+name))...), nil

	case strings.HasPrefix(op.Node, "c:"):
		target := strings.TrimPrefix(op.Node, "c:")
		var edits []TextEdit
		for _, n := range b.nl.nodes {
			if !n.isCall && n.target == target {
				edits = append(edits, b.deleteSpan(n.span))
			}
		}
		if len(edits) == 0 {
			return nil, fmt.Errorf("fbd edit: no coil writing %q", target)
		}
		return append(edits, b.remapLayout(idPrefixDrop("c:"+target, "b:c."+target))...), nil

	case strings.HasPrefix(op.Node, "f:"):
		inst := strings.TrimPrefix(op.Node, "f:")
		// Remaining inst.pin reads become diagnostics, not a blocked edit.
		var edits []TextEdit
		seen := map[exprPos]bool{}
		for _, d := range b.nl.fbDecls {
			if d.name == inst && !seen[d.span] {
				seen[d.span] = true
				edits = append(edits, b.deleteSpan(d.span))
			}
		}
		for _, n := range b.nl.nodes {
			if n.isCall && n.inst == inst && !seen[n.span] {
				seen[n.span] = true
				edits = append(edits, b.deleteSpan(n.span))
			}
		}
		if len(edits) == 0 {
			return nil, fmt.Errorf("fbd edit: no instance named %q", inst)
		}
		return append(edits, b.remapLayout(idPrefixDrop("f:"+inst, "b:f."+inst))...), nil

	case strings.HasPrefix(op.Node, "cm:"):
		n, ok := commentOrdinal(op.Node)
		if !ok || n < 0 || n >= len(b.comments) {
			return nil, fmt.Errorf("fbd edit: unknown comment %q", op.Node)
		}
		return append(b.deleteComment(n), b.remapLayout(idPrefixDrop(op.Node))...), nil

	case strings.HasPrefix(op.Node, "g:"):
		// A ghost lives only in the layout block — deleting is dropping it.
		if _, _, ok := ghostName(op.Node); !ok {
			return nil, fmt.Errorf("fbd edit: unknown ghost %q", op.Node)
		}
		return b.remapLayout(idPrefixDrop(op.Node)), nil
	}
	return nil, fmt.Errorf("fbd edit: %q is not deletable", op.Node)
}

// deleteSpan removes a statement, taking its whole line(s) when nothing else
// shares them — so deletions don't leave blank husks — but preserving any
// trailing comment by shrinking to the exact span.
func (b *modelBuilder) deleteSpan(span exprPos) TextEdit {
	wholeLines := true
	if span.line-1 < len(b.src) {
		before := b.src[span.line-1][:span.col-1]
		if strings.TrimSpace(before) != "" {
			wholeLines = false
		}
	}
	if span.endLine-1 < len(b.src) {
		line := b.src[span.endLine-1]
		if span.endCol-1 <= len(line) && strings.TrimSpace(line[span.endCol-1:]) != "" {
			wholeLines = false
		}
	}
	if wholeLines {
		return TextEdit{Line: span.line, Col: 1, EndLine: span.endLine + 1, EndCol: 1}
	}
	return TextEdit{Line: span.line, Col: span.col, EndLine: span.endLine, EndCol: span.endCol}
}

// ── disconnect / addInput ──────────────────────────────────────────────────

// opArity is the pin-count contract of an operator/function block: min
// inputs and max (-1 = extensible). Unknown functions are unrestricted —
// compile diagnostics own their arity.
func opArity(fn string) (min, max int) {
	switch fn {
	case "AND", "OR", "XOR", "ADD", "MUL", "MIN", "MAX", "MUX":
		return 2, -1
	case "SUB", "DIV", "MOD", "GT", "GE", "LT", "LE", "EQ", "NE":
		return 2, 2
	case "NOT", "MOVE":
		return 1, 1
	case "LIMIT", "SEL":
		return 3, 3
	}
	return 1, -1
}

// opDisconnect removes the connection into an input pin. FB pins drop their
// named argument; extensible operator inputs drop the argument when the
// block keeps its minimum arity; fixed-arity inputs and coil sources cannot
// dangle in text form, so those explain what to do instead.
func (b *modelBuilder) opDisconnect(op EditOp) ([]TextEdit, error) {
	if _, err := b.findEdge(op); err != nil {
		return nil, err
	}
	switch {
	case strings.HasPrefix(op.To, "f:"):
		inst := strings.TrimPrefix(op.To, "f:")
		for _, n := range b.nl.nodes {
			if !n.isCall || n.inst != inst {
				continue
			}
			for i, a := range n.args {
				if a.pin != op.ToPin {
					continue
				}
				if len(n.args) == 1 {
					// The call needs an argument list to stay parseable — a
					// placeholder leaves the open pin as a diagnostic.
					l, c := n.args[i].val.pos()
					el, ec := n.args[i].val.end()
					return []TextEdit{posEdit(exprPos{l, c, el, ec}, "_")}, nil
				}
				return []TextEdit{argRemoval(i, len(n.args),
					func(j int) exprPos { return n.args[j].pinPos },
					func(j int) expr { return n.args[j].val })}, nil
			}
		}
		return nil, fmt.Errorf("fbd edit: %s has no argument for pin %q", inst, op.ToPin)

	case strings.HasPrefix(op.To, "c:"):
		// `X := _`: parses, and the undeclared placeholder marks the open pin.
		target := strings.TrimPrefix(op.To, "c:")
		for _, n := range b.nl.nodes {
			if !n.isCall && n.target == target {
				l, c := n.source.pos()
				el, ec := n.source.end()
				return []TextEdit{posEdit(exprPos{l, c, el, ec}, "_")}, nil
			}
		}
		return nil, fmt.Errorf("fbd edit: no coil writing %q", target)

	default: // operator/function block
		call, ok := b.exprOf[op.To]
		if !ok {
			return nil, fmt.Errorf("fbd edit: unknown block %q", op.To)
		}
		node := b.nodes[op.To]
		idx := -1
		for i, p := range node.Inputs {
			if p == op.ToPin {
				idx = i
			}
		}
		if idx < 0 || idx >= len(call.args) {
			return nil, fmt.Errorf("fbd edit: %s has no input %q", node.Label, op.ToPin)
		}
		minA, _ := opArity(call.fn)
		if len(call.args)-1 < minA {
			// Fixed-arity (and minimum-arity) pins keep their POSITION with a
			// placeholder — removing the arg would silently shift the others.
			l, c := call.args[idx].pos()
			el, ec := call.args[idx].end()
			return []TextEdit{posEdit(exprPos{l, c, el, ec}, "_")}, nil
		}
		return []TextEdit{argRemoval(idx, len(call.args),
			func(j int) exprPos {
				l, c := call.args[j].pos()
				el, ec := call.args[j].end()
				return exprPos{l, c, el, ec}
			},
			func(j int) expr { return call.args[j] })}, nil
	}
}

// argRemoval spans one argument plus its list separator: interior/trailing
// args take the comma before them, a leading arg takes the comma after.
func argRemoval(i, n int, headOf func(int) exprPos, valOf func(int) expr) TextEdit {
	endL, endC := valOf(i).end()
	if i > 0 {
		prevL, prevC := valOf(i - 1).end()
		return TextEdit{Line: prevL, Col: prevC, EndLine: endL, EndCol: endC}
	}
	start := headOf(i)
	next := headOf(i + 1)
	return TextEdit{Line: start.line, Col: start.col, EndLine: next.line, EndCol: next.col}
}

// opAddInput appends an argument to an extensible block — the "+" pin: the
// input EXISTS because it is wired, so no dangling placeholder state.
func (b *modelBuilder) opAddInput(op EditOp) ([]TextEdit, error) {
	call, ok := b.exprOf[op.Node]
	if !ok {
		return nil, fmt.Errorf("fbd edit: unknown block %q", op.Node)
	}
	_, maxA := opArity(call.fn)
	if maxA != -1 && len(call.args) >= maxA {
		return nil, fmt.Errorf("fbd edit: %s takes exactly %d inputs", call.fn, maxA)
	}
	if len(call.args) == 0 {
		return nil, fmt.Errorf("fbd edit: %s has no argument list to extend", call.fn)
	}
	ref, err := b.refText(op.Source, op.SourcePin)
	if err != nil {
		return nil, err
	}
	l, c := call.args[len(call.args)-1].end()
	return append([]TextEdit{{Line: l, Col: c, EndLine: l, EndCol: c, NewText: ", " + ref}},
		b.ghostConsumed(op.Source)...), nil
}

// ── declareVar ─────────────────────────────────────────────────────────────

var varSectionRe = regexp.MustCompile(`(?i)^\s*(VAR_EXTERNAL|VAR)\s*$`)
var declNameRe = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*)\s*:`)

// opDeclareVar inserts "name : TYPE;" into a header section — the one edit
// the netlist can't express, so the diagram can introduce variables. NewName
// is the variable, Value the type, Text the section ("VAR_EXTERNAL" default,
// or "VAR" for retained locals). A missing section is created above FBD.
func (b *modelBuilder) opDeclareVar(op EditOp) ([]TextEdit, error) {
	name := strings.TrimSpace(op.NewName)
	typ := strings.TrimSpace(op.Value)
	section := strings.ToUpper(strings.TrimSpace(op.Text))
	if section == "" {
		section = "VAR_EXTERNAL"
	}
	if section != "VAR" && section != "VAR_EXTERNAL" {
		return nil, fmt.Errorf("fbd edit: unknown section %q", section)
	}
	if !identRe.MatchString(name) {
		return nil, fmt.Errorf("fbd edit: %q is not a valid identifier", name)
	}
	if !identRe.MatchString(typ) {
		return nil, fmt.Errorf("fbd edit: %q is not a valid type name", typ)
	}

	// The header ends where the FBD block begins.
	fbdLine := -1
	for i, l := range b.src {
		if strings.EqualFold(strings.TrimSpace(l), "FBD") {
			fbdLine = i
			break
		}
	}
	if fbdLine == -1 {
		return nil, fmt.Errorf("fbd edit: no FBD block")
	}

	// Scan header sections: existing declarations (duplicate check across
	// ALL sections) and the insertion point for the requested one.
	insertAt := -1 // 0-based line index of the target section's END_VAR
	inSection := ""
	for i := 0; i < fbdLine; i++ {
		line := b.src[i]
		if m := varSectionRe.FindStringSubmatch(line); m != nil {
			inSection = strings.ToUpper(m[1])
			continue
		}
		trimmed := strings.ToUpper(strings.TrimSpace(line))
		if trimmed == "END_VAR" {
			if inSection == section {
				insertAt = i
			}
			inSection = ""
			continue
		}
		if inSection != "" {
			for _, m := range declNameRe.FindAllStringSubmatch(line, -1) {
				if strings.EqualFold(m[1], name) {
					return nil, fmt.Errorf("fbd edit: %q is already declared", name)
				}
			}
		}
	}
	if b.nameTaken(name) {
		return nil, fmt.Errorf("fbd edit: the name %q is already in use", name)
	}

	decl := "  " + name + " : " + typ + ";\n"
	if insertAt >= 0 {
		at := insertAt + 1 // 1-based line of END_VAR
		return []TextEdit{{Line: at, Col: 1, EndLine: at, EndCol: 1, NewText: decl}}, nil
	}
	// No such section yet: create it just above FBD.
	at := fbdLine + 1
	return []TextEdit{{Line: at, Col: 1, EndLine: at, EndCol: 1,
		NewText: section + "\n" + decl + "END_VAR\n"}}, nil
}
