package lsp

import (
	"regexp"
	"sort"
	"strings"

	"github.com/joyautomation/nautilus/lang/fbd"
	"github.com/joyautomation/nautilus/lang/ld"
	"github.com/joyautomation/nautilus/lang/sfc"
	"github.com/joyautomation/nautilus/lang/st"
)

// textDocument/documentSymbol: the hierarchical outline behind VS Code's
// Outline view, breadcrumbs and "Go to Symbol in Editor".
//
//	.st   POUs (PROGRAM / FUNCTION_BLOCK / FUNCTION) and TYPE declarations;
//	      under a POU its VAR sections, under a section its declarations,
//	      under a STRUCT its fields
//	.fbd  the same header outline, then one symbol per netlist statement
//	.ld   the same header outline, then one symbol per RUNG
//	.sfc  the same header outline, then steps, transitions and actions
//
// The outline is read from the user's own text — the header with the real
// ST lexer, the diagram bodies with lenient outline parsers in lang/fbd,
// lang/ld and lang/sfc — so every position is already in the user's file
// (no transpile line map to project through) and nothing needs the
// document to parse. A buffer mid-edit keeps its outline: a statement or
// declaration that does not parse is skipped, the rest stay.

// SymbolKind values (LSP 3.17, the subset used).
const (
	SymbolKindModule        = 2
	SymbolKindNamespace     = 3
	SymbolKindClass         = 5
	SymbolKindMethod        = 6
	SymbolKindProperty      = 7
	SymbolKindField         = 8
	SymbolKindEnum          = 10
	SymbolKindFunction      = 12
	SymbolKindVariable      = 13
	SymbolKindConstant      = 14
	SymbolKindObject        = 19
	SymbolKindEnumMember    = 22
	SymbolKindStruct        = 23
	SymbolKindEvent         = 24
	SymbolKindOperator      = 25
	SymbolKindTypeParameter = 26
)

// DocumentSymbol is one node of the outline. SelectionRange (the name) must
// lie inside Range (the whole construct) or VS Code rejects the answer.
type DocumentSymbol struct {
	Name           string           `json:"name"`
	Detail         string           `json:"detail,omitempty"`
	Kind           int              `json:"kind"`
	Range          Range            `json:"range"`
	SelectionRange Range            `json:"selectionRange"`
	Children       []DocumentSymbol `json:"children,omitempty"`
}

// DocumentSymbolParams is the textDocument/documentSymbol request.
type DocumentSymbolParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
}

func (s *Server) handleDocumentSymbol(m *message) {
	var p DocumentSymbolParams
	if !unmarshal(m.Params, &p) {
		s.w.respondError(m.ID, codeInvalidParams, "bad documentSymbol params")
		return
	}
	doc, ok := s.docs[p.TextDocument.URI]
	if !ok {
		s.w.respond(m.ID, nil)
		return
	}
	if doc.test != nil {
		// A test suite is YAML; its outline is the YAML extension's.
		s.w.respond(m.ID, []DocumentSymbol{})
		return
	}
	s.w.respond(m.ID, documentSymbols(p.TextDocument.URI, doc.text))
}

// documentSymbols builds the outline of one document; the URI's extension
// picks the diagram body, if any.
func documentSymbols(uri, text string) []DocumentSymbol {
	o := newOutliner(text)
	roots := o.header()
	lower := strings.ToLower(uri)
	switch {
	case strings.HasSuffix(lower, ".fbd"):
		roots = o.place(roots, o.fbdStatements())
	case strings.HasSuffix(lower, ".ld"):
		roots = o.place(roots, o.ldRungs())
	case strings.HasSuffix(lower, ".sfc"):
		roots = o.place(roots, o.sfcElements())
	}
	return finish(roots)
}

// symNode is a DocumentSymbol under construction.
type symNode struct {
	sym      DocumentSymbol
	children []*symNode
	pou      bool         // a POU: diagram elements nest under the one containing them
	endTok   st.TokenType // the END_ keyword that closes this POU
}

// finish converts the tree, children sorted by position, and makes every
// node one VS Code accepts (a non-empty name, the selection inside the
// range).
func finish(nodes []*symNode) []DocumentSymbol {
	out := make([]DocumentSymbol, 0, len(nodes))
	sort.SliceStable(nodes, func(i, j int) bool { return before(nodes[i].sym.Range.Start, nodes[j].sym.Range.Start) })
	for _, n := range nodes {
		d := n.sym
		if strings.TrimSpace(d.Name) == "" {
			d.Name = "?"
		}
		if before(d.Range.End, d.Range.Start) {
			d.Range.End = d.Range.Start
		}
		if before(d.SelectionRange.Start, d.Range.Start) || before(d.Range.End, d.SelectionRange.End) {
			d.SelectionRange = Range{Start: d.Range.Start, End: d.Range.Start}
		}
		if len(n.children) > 0 {
			d.Children = finish(n.children)
		}
		out = append(out, d)
	}
	return out
}

func before(a, b Position) bool {
	return a.Line < b.Line || (a.Line == b.Line && a.Character < b.Character)
}

// ─── header: POUs, VAR sections, declarations, TYPEs ───────────────────────

// outliner walks the ST lexer's tokens over the whole document. A diagram
// body lexes as harmless noise between the header and the END_ keyword,
// and the body's own marker line (FBD / LD / SFC) stops a VAR section that
// is missing its END_VAR.
type outliner struct {
	text      string
	lines     []string
	toks      []st.Token // without the trailing EOF
	lineStart []int
}

func newOutliner(text string) *outliner {
	toks := st.Lex(text)
	if n := len(toks); n > 0 && toks[n-1].Type == st.TokenEOF {
		toks = toks[:n-1]
	}
	o := &outliner{text: text, lines: strings.Split(text, "\n"), toks: toks, lineStart: []int{0}}
	for i := 0; i < len(text); i++ {
		if text[i] == '\n' {
			o.lineStart = append(o.lineStart, i+1)
		}
	}
	return o
}

func (o *outliner) typ(i int) st.TokenType {
	if i < 0 || i >= len(o.toks) {
		return st.TokenEOF
	}
	return o.toks[i].Type
}

// start / end are a token's LSP positions (end just past it).
func (o *outliner) start(i int) Position {
	t := o.toks[i]
	return Position{Line: t.Line - 1, Character: t.Col - 1}
}

func (o *outliner) end(i int) Position {
	t := o.toks[i]
	w := len(t.Literal)
	line := ""
	if t.Line >= 1 && t.Line <= len(o.lines) {
		line = o.lines[t.Line-1]
	}
	// Strings and time literals drop part of their source text from
	// Literal; measure those from the line instead.
	switch t.Type {
	case st.TokenString:
		w = len(t.Literal) + 2
		if c := t.Col - 1; c >= 0 && c < len(line) {
			if j := strings.IndexByte(line[c+1:], line[c]); j >= 0 {
				w = j + 2
			}
		}
	case st.TokenTimeLiteral, st.TokenTypedLiteral:
		if c := t.Col - 1; c >= 0 && c < len(line) {
			if j := strings.IndexByte(line[c:], '#'); j >= 0 && j+1+len(t.Literal) > w {
				w = j + 1 + len(t.Literal)
			}
		}
	}
	return Position{Line: t.Line - 1, Character: min(t.Col-1+w, max(len(line), t.Col-1))}
}

func (o *outliner) tokRange(i int) Range { return Range{Start: o.start(i), End: o.end(i)} }

// docEnd is the position just past the document's last character.
func (o *outliner) docEnd() Position {
	return Position{Line: len(o.lines) - 1, Character: len(o.lines[len(o.lines)-1])}
}

// sourceText is the user's text from token a through token b, comments
// dropped and whitespace collapsed — a declared type as written.
func (o *outliner) sourceText(a, b int) string {
	if a > b || a < 0 || b >= len(o.toks) {
		return ""
	}
	off := func(p Position) int { return min(o.lineStart[p.Line]+p.Character, len(o.text)) }
	s := o.text[off(o.start(a)):off(o.end(b))]
	return strings.Join(strings.Fields(commentRe.ReplaceAllString(s, " ")), " ")
}

var commentRe = regexp.MustCompile(`(?s)\(\*.*?\*\)|//[^\n]*`)

func isPOUStart(t st.TokenType) bool {
	return t == st.TokenProgram || t == st.TokenFunctionBlock || t == st.TokenFunction
}

func isPOUEnd(t st.TokenType) bool {
	return t == st.TokenEndProgram || t == st.TokenEndFunctionBlock || t == st.TokenEndFunction
}

// bodyMarker reports a diagram body's opening line: FBD, LD or SFC alone
// on its line.
func (o *outliner) bodyMarker(i int) bool {
	if o.typ(i) != st.TokenIdent {
		return false
	}
	switch strings.ToUpper(o.toks[i].Literal) {
	case "FBD", "LD", "SFC":
	default:
		return false
	}
	line := o.toks[i].Line
	return (i == 0 || o.toks[i-1].Line != line) && (i+1 >= len(o.toks) || o.toks[i+1].Line != line)
}

// structural reports a token that ends whatever declaration list is open.
func (o *outliner) structural(i int) bool {
	t := o.typ(i)
	return t == st.TokenEOF || isPOUStart(t) || isPOUEnd(t) || isVarStart(t) ||
		t == st.TokenTypeKw || t == st.TokenEndType || t == st.TokenEndVar || o.bodyMarker(i)
}

// header outlines the POUs, VAR sections and TYPE declarations. POUs nest
// as written (an FB declared inside a PROGRAM's text sits under it); one
// left open runs to the end of the document.
func (o *outliner) header() []*symNode {
	var roots, stack []*symNode
	add := func(n *symNode) {
		if len(stack) > 0 {
			top := stack[len(stack)-1]
			top.children = append(top.children, n)
		} else {
			roots = append(roots, n)
		}
	}
	for i := 0; i < len(o.toks); {
		t := o.typ(i)
		switch {
		case isPOUStart(t):
			n := o.pou(i)
			add(n)
			stack = append(stack, n)
			i++
		case isPOUEnd(t):
			end := o.end(i)
			if o.typ(i+1) == st.TokenSemicolon && o.toks[i+1].Line == o.toks[i].Line {
				end = o.end(i + 1)
			}
			for j := len(stack) - 1; j >= 0; j-- {
				if stack[j].endTok == t {
					for _, n := range stack[j:] {
						n.sym.Range.End = end
					}
					stack = stack[:j]
					break
				}
			}
			i++
		case isVarStart(t):
			var n *symNode
			n, i = o.varSection(i)
			add(n)
		case t == st.TokenTypeKw:
			var ns []*symNode
			ns, i = o.typeBlock(i)
			for _, n := range ns {
				add(n)
			}
		default:
			i++
		}
	}
	for _, n := range stack {
		n.sym.Range.End = o.docEnd()
	}
	return roots
}

// pou opens a PROGRAM / FUNCTION_BLOCK / FUNCTION symbol at its keyword;
// header() closes its range at the matching END_ keyword.
func (o *outliner) pou(i int) *symNode {
	kw := o.toks[i]
	n := &symNode{pou: true}
	n.sym.Detail = strings.ToUpper(kw.Literal)
	switch kw.Type {
	case st.TokenProgram:
		n.sym.Kind, n.endTok = SymbolKindModule, st.TokenEndProgram
	case st.TokenFunctionBlock:
		n.sym.Kind, n.endTok = SymbolKindClass, st.TokenEndFunctionBlock
	default:
		n.sym.Kind, n.endTok = SymbolKindFunction, st.TokenEndFunction
	}
	n.sym.Range = o.tokRange(i)
	n.sym.Name, n.sym.SelectionRange = n.sym.Detail, o.tokRange(i)
	if o.typ(i+1) == st.TokenIdent {
		n.sym.Name, n.sym.SelectionRange = o.toks[i+1].Literal, o.tokRange(i+1)
		// FUNCTION Name : ReturnType — the return type, up to the end of
		// the header line.
		if kw.Type == st.TokenFunction && o.typ(i+2) == st.TokenColon {
			a, b := i+3, i+2
			for j := a; j < len(o.toks) && o.toks[j].Line == kw.Line && !o.structural(j) && o.typ(j) != st.TokenSemicolon; j++ {
				b = j
			}
			if b >= a {
				n.sym.Detail += " : " + o.sourceText(a, b)
			}
		}
	}
	return n
}

// varSection outlines VAR… END_VAR (or up to whatever ends it, mid-edit):
// the section named by its keyword, its declarations as children.
func (o *outliner) varSection(i int) (*symNode, int) {
	n := &symNode{}
	n.sym.Name = strings.ToUpper(o.toks[i].Literal)
	n.sym.Kind = SymbolKindNamespace
	n.sym.Range, n.sym.SelectionRange = o.tokRange(i), o.tokRange(i)
	kind := SymbolKindVariable
	j := i + 1
	var quals []string
	for ; j < len(o.toks); j++ {
		q := strings.ToUpper(o.toks[j].Literal)
		if o.typ(j) == st.TokenConstant {
			kind = SymbolKindConstant
		} else if o.typ(j) != st.TokenRetain && (o.typ(j) != st.TokenIdent || (q != "NON_RETAIN" && q != "PERSISTENT")) {
			break
		}
		quals = append(quals, q)
		n.sym.Range.End = o.end(j)
	}
	n.sym.Detail = strings.Join(quals, " ")
	var last int
	n.children, j, last = o.decls(j, st.TokenEndVar, kind)
	if last >= 0 {
		n.sym.Range.End = o.end(last)
	}
	if o.typ(j) == st.TokenEndVar {
		n.sym.Range.End = o.end(j)
		j++
		if o.typ(j) == st.TokenSemicolon && o.toks[j].Line == o.toks[j-1].Line {
			n.sym.Range.End = o.end(j)
			j++
		}
	}
	return n, j
}

// decls outlines `name [, name] [AT addr] : type [:= init];` declarations
// from token j until the stop keyword or a structural token. It returns
// the declarations, the index of the token that stopped it, and the last
// token consumed (-1 if none). A declaration's type is its text as
// written. A declaration missing its ';' ends where the next line starts
// a new `name :` — the moment of typing one.
func (o *outliner) decls(j int, stop st.TokenType, kind int) ([]*symNode, int, int) {
	var out []*symNode
	last := -1
	for j < len(o.toks) && o.typ(j) != stop && !o.structural(j) {
		if o.typ(j) != st.TokenIdent {
			last = j
			j++
			continue
		}
		names := []int{j}
		j++
		for o.typ(j) == st.TokenComma && o.typ(j+1) == st.TokenIdent {
			names = append(names, j+1)
			j += 2
		}
		if o.typ(j) == st.TokenIdent && strings.EqualFold(o.toks[j].Literal, "AT") {
			for j < len(o.toks) && o.typ(j) != st.TokenColon && o.typ(j) != st.TokenSemicolon && !o.structural(j) {
				j++
			}
		}
		if o.typ(j) != st.TokenColon {
			// Not a declaration (yet): skip to the next one.
			last = j - 1
			j = o.skipDecl(j, stop)
			if o.typ(j) == st.TokenSemicolon {
				last = j
				j++
			}
			continue
		}
		last = j
		j++
		var fields []*symNode
		detail := ""
		typeKind := kind
		if o.typ(j) == st.TokenStruct {
			// An inline STRUCT: its fields nest under the variable.
			last = j
			fields, j, last = o.structFields(j)
			detail = "STRUCT"
		} else {
			a := j
			j = o.skipExpr(j, stop, true)
			if j > a {
				detail, last = o.sourceText(a, j-1), j-1
			}
		}
		if o.typ(j) == st.TokenAssign {
			last = j
			if k := o.skipExpr(j+1, stop, false); k > j+1 {
				last = k - 1
				j = k
			} else {
				j++
			}
		}
		if o.typ(j) == st.TokenSemicolon {
			last = j
			j++
		}
		for _, k := range names {
			out = append(out, &symNode{
				sym: DocumentSymbol{
					Name: o.toks[k].Literal, Detail: detail, Kind: typeKind,
					Range:          Range{Start: o.start(names[0]), End: o.end(last)},
					SelectionRange: o.tokRange(k),
				},
				children: fields,
			})
		}
	}
	return out, j, last
}

// skipExpr advances over a type or initializer from j to the token that
// ends it at bracket depth 0: ';', ':=' (types only), the stop keyword, a
// structural token, or a new line starting `name :` / `name ,`.
func (o *outliner) skipExpr(j int, stop st.TokenType, isType bool) int {
	depth := 0
	from := j
	for ; j < len(o.toks); j++ {
		t := o.typ(j)
		if o.structural(j) {
			return j
		}
		if depth == 0 {
			if t == stop || t == st.TokenSemicolon || (isType && t == st.TokenAssign) {
				return j
			}
			if j > from && o.lineEnds(j, isType) {
				return j
			}
		}
		switch t {
		case st.TokenLParen, st.TokenLBracket, st.TokenStruct:
			depth++
		case st.TokenRParen, st.TokenRBracket, st.TokenEndStruct:
			if depth > 0 {
				depth--
			}
		}
	}
	return j
}

// skipDecl advances past a malformed declaration to its ';' (returned, not
// consumed), the stop keyword, a structural token, or the next line that
// starts a declaration.
func (o *outliner) skipDecl(j int, stop st.TokenType) int {
	from := j
	for ; j < len(o.toks); j++ {
		t := o.typ(j)
		if t == stop || t == st.TokenSemicolon || o.structural(j) || (j > from && o.newDecl(j)) {
			return j
		}
	}
	return j
}

// lineEnds reports that token j, at bracket depth 0, starts a line that is
// no longer part of the declaration before it — what ends a declaration
// whose ';' is not typed yet. A type continues onto the next line only
// mid-ARRAY (after OF / ARRAY / ',' / '..', or a line starting OF or '[');
// an initializer until a line starts a new declaration or a statement.
func (o *outliner) lineEnds(j int, isType bool) bool {
	if o.toks[j].Line == o.toks[j-1].Line {
		return false
	}
	if isType {
		switch o.typ(j - 1) {
		case st.TokenOf, st.TokenArray, st.TokenComma, st.TokenDotDot:
			return false
		}
		t := o.typ(j)
		return t != st.TokenOf && t != st.TokenLBracket
	}
	switch o.typ(j) {
	case st.TokenIf, st.TokenFor, st.TokenWhile, st.TokenCase, st.TokenRepeat, st.TokenReturn:
		return true
	case st.TokenIdent:
		next := o.typ(j + 1)
		return next == st.TokenColon || next == st.TokenComma || next == st.TokenAssign || next == st.TokenLParen
	}
	return false
}

// newDecl reports that token j starts a line with `name :` or `name ,`.
func (o *outliner) newDecl(j int) bool {
	return j > 0 && o.toks[j].Line > o.toks[j-1].Line && o.typ(j) == st.TokenIdent &&
		(o.typ(j+1) == st.TokenColon || o.typ(j+1) == st.TokenComma)
}

// structFields outlines STRUCT … END_STRUCT starting at the STRUCT token:
// the fields, the index after END_STRUCT, and the last token consumed.
func (o *outliner) structFields(j int) ([]*symNode, int, int) {
	last := j
	fields, k, l := o.decls(j+1, st.TokenEndStruct, SymbolKindField)
	if l >= 0 {
		last = l
	}
	if o.typ(k) == st.TokenEndStruct {
		last = k
		k++
	}
	return fields, k, last
}

// typeBlock outlines TYPE … END_TYPE: one symbol per declared type (a
// STRUCT with its fields, an enumeration with its values, or an alias).
// The first type's range starts at the TYPE keyword and the last one's
// runs through END_TYPE, so the block has no gaps for the breadcrumbs.
func (o *outliner) typeBlock(i int) ([]*symNode, int) {
	var out []*symNode
	j := i + 1
	for j < len(o.toks) && o.typ(j) != st.TokenEndType && !o.structural(j) {
		if o.typ(j) != st.TokenIdent || o.typ(j+1) != st.TokenColon {
			j = o.skipDecl(j+1, st.TokenEndType)
			if o.typ(j) == st.TokenSemicolon {
				j++
			}
			continue
		}
		name := j
		j += 2
		n := &symNode{}
		n.sym.Name, n.sym.SelectionRange = o.toks[name].Literal, o.tokRange(name)
		last := name + 1
		switch o.typ(j) {
		case st.TokenStruct:
			n.sym.Kind, n.sym.Detail = SymbolKindStruct, "STRUCT"
			n.children, j, last = o.structFields(j)
		case st.TokenLParen:
			// (Red, Green := 2, Blue) — the values, as EnumMembers.
			n.sym.Kind = SymbolKindEnum
			var vals []string
			k := j + 1
			for ; k < len(o.toks) && o.typ(k) != st.TokenRParen && !o.structural(k); k++ {
				if o.typ(k) == st.TokenIdent && (o.typ(k-1) == st.TokenLParen || o.typ(k-1) == st.TokenComma) {
					vals = append(vals, o.toks[k].Literal)
					n.children = append(n.children, &symNode{sym: DocumentSymbol{
						Name: o.toks[k].Literal, Kind: SymbolKindEnumMember,
						Range: o.tokRange(k), SelectionRange: o.tokRange(k),
					}})
				}
				last = k
			}
			if o.typ(k) == st.TokenRParen {
				last = k
				k++
			}
			j = k
			n.sym.Detail = "(" + strings.Join(vals, ", ") + ")"
		default:
			a := j
			j = o.skipExpr(j, st.TokenEndType, true)
			if j > a {
				n.sym.Kind, n.sym.Detail, last = SymbolKindTypeParameter, o.sourceText(a, j-1), j-1
			} else {
				n.sym.Kind = SymbolKindTypeParameter
			}
		}
		if o.typ(j) == st.TokenAssign {
			if k := o.skipExpr(j+1, st.TokenEndType, false); k > j+1 {
				last, j = k-1, k
			} else {
				last, j = j, j+1
			}
		}
		if o.typ(j) == st.TokenSemicolon {
			last = j
			j++
		}
		n.sym.Range = Range{Start: o.start(name), End: o.end(last)}
		if len(out) == 0 {
			n.sym.Range.Start = o.start(i)
		}
		out = append(out, n)
	}
	if o.typ(j) == st.TokenEndType {
		end := o.end(j)
		j++
		if o.typ(j) == st.TokenSemicolon && o.toks[j].Line == o.toks[j-1].Line {
			end = o.end(j)
			j++
		}
		if len(out) > 0 {
			out[len(out)-1].sym.Range.End = end
		}
	}
	return out, j
}

// ─── diagram bodies ─────────────────────────────────────────────────────────

// place nests each diagram element under the innermost POU whose range
// contains it (the file's PROGRAM, or the FUNCTION_BLOCK a library .ld
// defines), or at the root when none does.
func (o *outliner) place(roots, elems []*symNode) []*symNode {
	var into func(nodes []*symNode, e *symNode) bool
	into = func(nodes []*symNode, e *symNode) bool {
		for _, n := range nodes {
			if !n.pou || before(e.sym.Range.Start, n.sym.Range.Start) || before(n.sym.Range.End, e.sym.Range.Start) {
				continue
			}
			if !into(n.children, e) {
				n.children = append(n.children, e)
			}
			return true
		}
		return false
	}
	for _, e := range elems {
		if !into(roots, e) {
			roots = append(roots, e)
		}
	}
	return roots
}

// clip shortens a one-line detail for the outline.
func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// fbdStatements: one symbol per netlist statement, named by what it
// assigns or calls — `t1 : TON(...)` is t1, `Run := latch` is Run.
func (o *outliner) fbdStatements() []*symNode {
	var out []*symNode
	for _, s := range fbd.Statements(o.text) {
		n := &symNode{sym: DocumentSymbol{
			Name:           s.Name,
			Detail:         clip(s.Rest, 60),
			Range:          Range{Start: Position{Line: s.Line - 1, Character: s.Col - 1}, End: Position{Line: s.EndLine - 1, Character: s.EndCol - 1}},
			SelectionRange: Range{Start: Position{Line: s.NameLine - 1, Character: s.NameCol - 1}, End: Position{Line: s.NameLine - 1, Character: s.NameEndCol - 1}},
		}}
		switch s.Kind {
		case "instance":
			n.sym.Kind, n.sym.Detail = SymbolKindObject, s.Type
		case "call":
			n.sym.Kind = SymbolKindMethod
		case "coil":
			n.sym.Kind = SymbolKindProperty
		default: // wire
			n.sym.Kind = SymbolKindVariable
		}
		out = append(out, n)
	}
	return out
}

// ldRungs: one symbol per RUNG, named as written (Graph's "rung<line>"
// when unnamed), its header comment as the detail.
func (o *outliner) ldRungs() []*symNode {
	var out []*symNode
	for _, r := range ld.Rungs(o.text) {
		start := Position{Line: r.Line - 1, Character: r.Col - 1}
		sel := Range{Start: start, End: Position{Line: r.Line - 1, Character: r.Col - 1 + len("RUNG")}}
		if r.NameCol > 0 {
			sel = Range{
				Start: Position{Line: r.Line - 1, Character: r.NameCol - 1},
				End:   Position{Line: r.Line - 1, Character: r.NameCol - 1 + len(r.Name)},
			}
		}
		out = append(out, &symNode{sym: DocumentSymbol{
			Name: r.Name, Detail: clip(r.Comment, 80), Kind: SymbolKindEvent,
			Range:          Range{Start: start, End: Position{Line: r.EndLine - 1, Character: len(lineText(o.text, r.EndLine))}},
			SelectionRange: sel,
		}})
	}
	return out
}

// sfcElements: steps (the initial one's detail says so), transitions named
// "From → To", and action blocks.
func (o *outliner) sfcElements() []*symNode {
	prog := sfc.ParseOutline(o.text)
	var out []*symNode
	// span runs from a keyword to just past its END_ keyword.
	span := func(p, end sfc.Pos, endKw string) Range {
		r := Range{Start: Position{Line: p.Line - 1, Character: p.Col - 1}}
		r.End = Position{Line: end.Line - 1, Character: end.Col - 1 + len(endKw)}
		if l := lineText(o.text, end.Line); len(l) > r.End.Character && l[r.End.Character] == ';' {
			r.End.Character++
		}
		return r
	}
	for _, s := range prog.Steps {
		detail := "STEP"
		if s.Initial {
			detail = "INITIAL_STEP"
		}
		out = append(out, &symNode{sym: DocumentSymbol{
			Name: s.Name, Detail: detail, Kind: SymbolKindEvent,
			Range:          span(s.Pos, s.EndPos, "END_STEP"),
			SelectionRange: o.nameAfter(s.Pos, s.Name),
		}})
	}
	for _, t := range prog.Transitions {
		detail := clip(strings.Join(strings.Fields(t.Cond.Text), " "), 60)
		sel := o.nameAfter(t.Pos, "TRANSITION")
		if t.Name != "" {
			detail = t.Name + ": " + detail
			sel = o.nameAfter(t.Pos, t.Name)
		}
		out = append(out, &symNode{sym: DocumentSymbol{
			Name:   stepSet(t.From) + " → " + stepSet(t.To),
			Detail: detail, Kind: SymbolKindOperator,
			Range:          span(t.Pos, t.EndPos, "END_TRANSITION"),
			SelectionRange: sel,
		}})
	}
	for _, a := range prog.Actions {
		out = append(out, &symNode{sym: DocumentSymbol{
			Name: a.Name, Detail: "ACTION", Kind: SymbolKindMethod,
			Range:          span(a.Pos, a.EndPos, "END_ACTION"),
			SelectionRange: o.nameAfter(a.Pos, a.Name),
		}})
	}
	return out
}

// stepSet renders a transition's FROM/TO set: "Fill", or "(A, B)".
func stepSet(names []string) string {
	if len(names) == 1 {
		return names[0]
	}
	return "(" + strings.Join(names, ", ") + ")"
}

// nameAfter is the range of name's first whole-word occurrence on p's line
// at or after p — a step / transition / action name after its keyword —
// or the keyword position when it is not there.
func (o *outliner) nameAfter(p sfc.Pos, name string) Range {
	l := lineText(o.text, p.Line)
	from := max(p.Col-1, 0)
	for from <= len(l) {
		k := strings.Index(strings.ToUpper(l[from:]), strings.ToUpper(name))
		if k < 0 {
			break
		}
		a := from + k
		b := a + len(name)
		if (a == 0 || !isIdentByte(l[a-1])) && (b >= len(l) || !isIdentByte(l[b])) {
			return Range{Start: Position{Line: p.Line - 1, Character: a}, End: Position{Line: p.Line - 1, Character: b}}
		}
		from = a + 1
	}
	at := Position{Line: p.Line - 1, Character: max(p.Col-1, 0)}
	return Range{Start: at, End: at}
}
