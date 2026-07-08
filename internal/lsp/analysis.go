package lsp

import (
	"sort"
	"strings"

	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/lang/st"
)

// Symbol is a declared name the LSP can navigate to: a variable, an FB
// instance, a user FB/FUNCTION type, or a UDT.
type Symbol struct {
	Name      string
	Datatype  string // textual type as written ("REAL", "ARRAY[1..10] OF INT", "TON")
	BlockKind string // "VAR", "VAR_INPUT", ... — or "FUNCTION_BLOCK"/"FUNCTION"/"TYPE" for POU/type decls
	Container string // enclosing POU name; "" for file scope
	Pos       st.Pos // 1-based declaration site
}

// analysis is everything the server derives from one version of a document.
type analysis struct {
	Symbols []Symbol
	Diags   []Diagnostic
	scopes  []scope // POU body line ranges, for scoped lookup
}

// scope is the 1-based line span of a FUNCTION_BLOCK/FUNCTION body, used to
// prefer locally-declared symbols when resolving a name at a position.
type scope struct {
	name       string
	start, end int
}

// analyze runs the real compiler over the source: st.Parse for syntax
// diagnostics, st.Lower for semantic ones (undeclared identifiers, type
// errors), and walks the AST for the symbol index.
func analyze(text string) analysis {
	var a analysis
	a.scopes = scanScopes(text)

	prog, err := st.Parse(text)
	if err != nil {
		// Anchor on the position the parser reported, falling back to line 1.
		// st.ParseErrorPos is the single source of truth so `nautilus check`
		// and this diagnostic agree.
		line := 1
		if pos, ok := st.ParseErrorPos(err); ok {
			line = pos.Line
		}
		a.Diags = append(a.Diags, Diagnostic{
			Range:    lineRange(text, line),
			Severity: SeverityError,
			Source:   "nautilus-st",
			Message:  err.Error(),
		})
		return a
	}

	a.Symbols = collectSymbols(prog)

	if _, err := st.Lower(prog); err != nil {
		pos := st.Pos{Line: 1, Col: 1}
		msg := err.Error()
		if le, ok := st.AsLowerError(err); ok && le.Pos.Line > 0 {
			// The squiggle already marks the line; drop the "line N:"
			// prefix LowerError.Error() adds.
			pos, msg = le.Pos, le.Err.Error()
		}
		a.Diags = append(a.Diags, Diagnostic{
			Range:    posRange(text, pos),
			Severity: SeverityError,
			Source:   "nautilus-st",
			Message:  msg,
		})
	}
	return a
}

// collectSymbols flattens the program's declarations into a lookup index.
func collectSymbols(prog *st.Program) []Symbol {
	var syms []Symbol
	addBlocks := func(container string, blocks []st.VarBlock) {
		for _, b := range blocks {
			for _, v := range b.Variables {
				syms = append(syms, Symbol{
					Name: v.Name, Datatype: v.Datatype,
					BlockKind: b.Kind, Container: container, Pos: v.Pos,
				})
			}
		}
	}
	// Program-level vars use container "" — the same value containerAt
	// returns for lines outside any FB/FUNCTION body — so scoped lookup
	// and completion treat the program body as the default scope.
	addBlocks("", prog.VarBlocks)
	for _, t := range prog.TypeDecls {
		syms = append(syms, Symbol{Name: t.Name, Datatype: t.Type.String(), BlockKind: "TYPE", Pos: t.Pos})
	}
	for _, fb := range prog.FBDecls {
		syms = append(syms, Symbol{Name: fb.Name, Datatype: fb.Name, BlockKind: "FUNCTION_BLOCK", Pos: fb.Pos})
		addBlocks(fb.Name, fb.VarBlocks)
	}
	for _, fn := range prog.FuncDecls {
		dt := ""
		if fn.ReturnType != nil {
			dt = fn.ReturnType.String()
		}
		syms = append(syms, Symbol{Name: fn.Name, Datatype: dt, BlockKind: "FUNCTION", Pos: fn.Pos})
		addBlocks(fn.Name, fn.VarBlocks)
	}
	return syms
}

// scanScopes finds FUNCTION_BLOCK/FUNCTION body line spans textually. The
// AST records where a POU starts but not where it ends, and for scoped name
// lookup an approximate keyword scan is all that's needed (POU keywords
// can't appear mid-expression in valid ST).
func scanScopes(text string) []scope {
	var scopes []scope
	var open *scope
	for i, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		upper := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(upper, "FUNCTION_BLOCK") || (strings.HasPrefix(upper, "FUNCTION") && !strings.HasPrefix(upper, "FUNCTION_")):
			fields := strings.Fields(line)
			if len(fields) >= 2 && open == nil {
				name := strings.TrimSuffix(fields[1], ";")
				name, _, _ = strings.Cut(name, ":")
				open = &scope{name: name, start: i + 1}
			}
		case strings.HasPrefix(upper, "END_FUNCTION_BLOCK") || strings.HasPrefix(upper, "END_FUNCTION"):
			if open != nil {
				open.end = i + 1
				scopes = append(scopes, *open)
				open = nil
			}
		}
	}
	if open != nil { // unterminated POU while typing — treat as open to EOF
		open.end = strings.Count(text, "\n") + 1
		scopes = append(scopes, *open)
	}
	return scopes
}

// containerAt returns the POU name whose body contains the 1-based line,
// or "" for program/file scope.
func (a *analysis) containerAt(line int) string {
	for _, s := range a.scopes {
		if line >= s.start && line <= s.end {
			return s.name
		}
	}
	return ""
}

// lookup resolves name at a position: symbols in the enclosing POU first,
// then program/file scope, then anywhere — matching IEC's case-insensitive
// identifiers (exact-case match wins within each tier).
func (a *analysis) lookup(name string, line int) *Symbol {
	container := a.containerAt(line)
	tiers := [][]func(*Symbol) bool{
		{func(s *Symbol) bool { return s.Container == container }},
		{func(s *Symbol) bool { return true }},
	}
	for _, tier := range tiers {
		var ci *Symbol
		for i := range a.Symbols {
			s := &a.Symbols[i]
			if !tier[0](s) {
				continue
			}
			if s.Name == name {
				return s
			}
			if ci == nil && strings.EqualFold(s.Name, name) {
				ci = s
			}
		}
		if ci != nil {
			return ci
		}
	}
	return nil
}

// ─── Positions ──────────────────────────────────────────────────────────────

// lineText returns the content of a 1-based line.
func lineText(text string, line int) string {
	lines := strings.Split(text, "\n")
	if line < 1 || line > len(lines) {
		return ""
	}
	return lines[line-1]
}

// lineRange spans a whole 1-based line (as a 0-based LSP range).
func lineRange(text string, line int) Range {
	l := lineText(text, line)
	start := len(l) - len(strings.TrimLeft(l, " \t"))
	if start == len(l) {
		start = 0
	}
	return Range{
		Start: Position{Line: line - 1, Character: start},
		End:   Position{Line: line - 1, Character: len(l)},
	}
}

// posRange spans the identifier starting at a 1-based compiler position,
// or the rest of the line when no identifier starts there.
func posRange(text string, pos st.Pos) Range {
	l := lineText(text, pos.Line)
	col := pos.Col - 1
	if col < 0 || col > len(l) {
		return lineRange(text, pos.Line)
	}
	end := col
	for end < len(l) && isIdentByte(l[end]) {
		end++
	}
	if end == col {
		end = len(l)
	}
	return Range{
		Start: Position{Line: pos.Line - 1, Character: col},
		End:   Position{Line: pos.Line - 1, Character: end},
	}
}

func isIdentByte(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// wordAt extracts the identifier under a 0-based LSP position, plus its range.
func wordAt(text string, p Position) (string, Range) {
	l := lineText(text, p.Line+1)
	c := p.Character
	if c > len(l) {
		c = len(l)
	}
	start, end := c, c
	for start > 0 && isIdentByte(l[start-1]) {
		start--
	}
	for end < len(l) && isIdentByte(l[end]) {
		end++
	}
	if start == end {
		return "", Range{}
	}
	word := l[start:end]
	if word[0] >= '0' && word[0] <= '9' { // numbers aren't identifiers
		return "", Range{}
	}
	return word, Range{
		Start: Position{Line: p.Line, Character: start},
		End:   Position{Line: p.Line, Character: end},
	}
}

// ─── Static completion sets ─────────────────────────────────────────────────

// staticCompletions is the position-independent completion set, derived
// entirely from the compiler's own tables so it never drifts from what the
// compiler accepts: elementary types (st.ScalarTypeNames), keywords
// (st.KeywordNames), builtin functions (ir.Builtins), and standard function
// blocks (ir.FBs). A few names (INT, REAL, BOOL, STRING, DINT, LREAL) are
// both a keyword token and an elementary type; they're offered once, as the
// type.
func staticCompletions() []CompletionItem {
	var items []CompletionItem
	types := map[string]bool{}
	for _, t := range st.ScalarTypeNames() {
		types[t] = true
		items = append(items, CompletionItem{Label: t, Kind: CompletionKindStruct, Detail: "elementary type"})
	}
	for _, k := range st.KeywordNames() {
		if types[k] {
			continue // already offered as a type
		}
		items = append(items, CompletionItem{Label: k, Kind: CompletionKindKeyword})
	}
	for name := range ir.Builtins {
		items = append(items, CompletionItem{Label: name, Kind: CompletionKindFunction, Detail: "builtin function"})
	}
	for name := range ir.FBs {
		items = append(items, CompletionItem{Label: name, Kind: CompletionKindClass, Detail: "standard function block"})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Label < items[j].Label })
	return items
}
