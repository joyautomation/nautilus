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
	e, err := b.findEdge(op)
	if err != nil {
		return nil, err
	}
	ref, err := b.refText(op.Source, op.SourcePin)
	if err != nil {
		return nil, err
	}
	if ref == e.Arg.Text {
		return nil, nil
	}
	return []TextEdit{spanEdit(e.Arg, ref)}, nil
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
	return edits, nil
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

// ── deleteNode ─────────────────────────────────────────────────────────────

func (b *modelBuilder) opDelete(op EditOp) ([]TextEdit, error) {
	switch {
	case strings.HasPrefix(op.Node, "b:w."):
		name := strings.TrimPrefix(op.Node, "b:w.")
		span, ok := b.nl.wireSpan[name]
		if !ok {
			return nil, fmt.Errorf("fbd edit: no wire named %q", name)
		}
		used := 0
		b.eachExpr(func(e expr) {
			if r, ok := e.(refExpr); ok && r.name == name {
				used++
			}
		})
		if used > 0 {
			return nil, fmt.Errorf("fbd edit: wire %q feeds %d input(s) — rewire them first", name, used)
		}
		return []TextEdit{b.deleteSpan(span)}, nil

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
		return edits, nil

	case strings.HasPrefix(op.Node, "f:"):
		inst := strings.TrimPrefix(op.Node, "f:")
		reads := 0
		b.eachExpr(func(e expr) {
			if pe, ok := e.(pinExpr); ok && pe.inst == inst {
				reads++
			}
		})
		if reads > 0 {
			return nil, fmt.Errorf("fbd edit: %s's outputs are read %d time(s) — rewire those inputs first", inst, reads)
		}
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
		return edits, nil
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
