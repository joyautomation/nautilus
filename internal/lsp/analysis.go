package lsp

import (
	"errors"
	"sort"
	"strconv"
	"strings"

	"github.com/joyautomation/nautilus/lang/fbd"
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
	// types maps lowercased type names (from this file AND the project
	// prelude) to a rendered ST definition, for hover expansion.
	types map[string]string
	// typeMembers maps lowercased UDT names to their members, for member
	// completion after a dot.
	typeMembers map[string][]TypeMember
}

// TypeMember is one member of a UDT, as declared.
type TypeMember struct {
	Name     string
	Datatype string
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
//
// prelude is library source from sibling files (see internal/stproject) that
// is in scope at runtime; it participates in lowering so cross-file types
// resolve, with diagnostic positions remapped back into the user's file.
// preludeLines is the prelude's line count.
func analyze(text, prelude string, preludeLines int) analysis {
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

	lowerProg := prog
	if prelude != "" {
		// The prelude parsed on its own (stproject filters), so a combined
		// parse only fails on pathological interactions — fall back to the
		// solo program rather than losing lowering diagnostics entirely.
		if combined, err := st.Parse(prelude + text); err == nil {
			lowerProg = combined
		} else {
			preludeLines = 0
		}
	} else {
		preludeLines = 0
	}
	a.types = typeIndex(lowerProg.TypeDecls)
	a.typeMembers = typeMemberIndex(lowerProg.TypeDecls)

	if _, err := st.Lower(lowerProg); err != nil {
		pos := st.Pos{Line: 1, Col: 1}
		msg := err.Error()
		if le, ok := st.AsLowerError(err); ok && le.Pos.Line > 0 {
			// The squiggle already marks the line; drop the "line N:"
			// prefix LowerError.Error() adds.
			pos, msg = le.Pos, le.Err.Error()
		}
		if pos.Line > preludeLines {
			pos.Line -= preludeLines
		} else if preludeLines > 0 {
			// The error sits inside a sibling library file (duplicate type,
			// broken FB, ...). Surface it here at 1:1 so it isn't silently
			// swallowed, but say where it came from.
			pos = st.Pos{Line: 1, Col: 1}
			msg = "in project library files: " + msg
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

// analyzeFBD analyzes a .fbd document: transpile the netlist to ST (the FBD
// semantics live in lang/fbd, once), run the normal ST analysis over the
// result, and project diagnostic positions back onto the .fbd source through
// the transpiler's line map. Symbol positions stay in transpiled coordinates —
// header declarations map 1:1, so hover/completion on variables still work;
// go-to-definition inside the netlist is approximate for now.
func analyzeFBD(text, prelude string, preludeLines int) analysis {
	stText, lineMap, err := fbd.TranspileWithLines(text)
	if err != nil {
		var a analysis
		line := 1
		var pe *fbd.ParseError
		if errors.As(err, &pe) {
			line = pe.Line
		}
		a.Diags = append(a.Diags, Diagnostic{
			Range:    lineRange(text, line),
			Severity: SeverityError,
			Source:   "nautilus-fbd",
			Message:  err.Error(),
		})
		return a
	}
	a := analyze(stText, prelude, preludeLines)
	for i := range a.Diags {
		// Diagnostics carry 0-based lines; the map is 1-based on both sides.
		orig := 1
		if l := a.Diags[i].Range.Start.Line; l >= 0 && l < len(lineMap) {
			orig = lineMap[l]
		}
		a.Diags[i].Range = lineRange(text, orig)
	}
	return a
}

// hoverTypeMaxLines caps a struct expansion so a 170-member AOI stays a
// tooltip, not a wall.
const hoverTypeMaxLines = 30

// typeIndex renders every TYPE declaration for hover lookup, keyed by
// lowercased name (IEC identifiers are case-insensitive).
func typeIndex(decls []st.TypeDecl) map[string]string {
	if len(decls) == 0 {
		return nil
	}
	out := make(map[string]string, len(decls))
	for i := range decls {
		out[strings.ToLower(decls[i].Name)] = renderTypeDecl(&decls[i])
	}
	return out
}

// renderTypeDecl renders a TYPE declaration back to ST, expanding STRUCT
// bodies the way TypeScript expands a type on hover.
func renderTypeDecl(td *st.TypeDecl) string {
	structType, ok := td.Type.(*st.StructType)
	if !ok {
		return td.Name + " : " + td.Type.String()
	}
	var b strings.Builder
	b.WriteString(td.Name + " : STRUCT\n")
	for i, f := range structType.Fields {
		if i >= hoverTypeMaxLines {
			b.WriteString("    … (" + strconv.Itoa(len(structType.Fields)-i) + " more members)\n")
			break
		}
		dt := f.Datatype
		if dt == "" && f.Type != nil {
			dt = f.Type.String()
		}
		b.WriteString("    " + f.Name + " : " + dt + ";\n")
	}
	b.WriteString("END_STRUCT")
	return b.String()
}

// typeMemberIndex extracts every STRUCT type's member list, keyed by
// lowercased type name, for member completion.
func typeMemberIndex(decls []st.TypeDecl) map[string][]TypeMember {
	if len(decls) == 0 {
		return nil
	}
	out := make(map[string][]TypeMember, len(decls))
	for i := range decls {
		structType, ok := decls[i].Type.(*st.StructType)
		if !ok {
			continue
		}
		members := make([]TypeMember, 0, len(structType.Fields))
		for _, f := range structType.Fields {
			dt := f.Datatype
			if dt == "" && f.Type != nil {
				dt = f.Type.String()
			}
			members = append(members, TypeMember{Name: f.Name, Datatype: dt})
		}
		out[strings.ToLower(decls[i].Name)] = members
	}
	return out
}

// memberType resolves one step of a member chain: the declared type of
// member on a value of typeName. It knows UDT members, builtin FB slots
// (TON.Q, ...), and user FB in/out/internal variables.
func (a *analysis) memberType(typeName, member string) (string, bool) {
	base := strings.ToLower(baseTypeName(typeName))
	for _, m := range a.typeMembers[base] {
		if strings.EqualFold(m.Name, member) {
			return m.Datatype, true
		}
	}
	if def, ok := ir.FBs[strings.ToUpper(base)]; ok {
		for _, slot := range def.AllSlots() {
			if strings.EqualFold(slot.Name, member) {
				return slot.Type.String(), true
			}
		}
	}
	for i := range a.Symbols {
		s := &a.Symbols[i]
		if strings.EqualFold(s.Container, baseTypeName(typeName)) && strings.EqualFold(s.Name, member) {
			return s.Datatype, true
		}
	}
	return "", false
}

// memberCompletions lists the members of typeName as completion items:
// UDT members, builtin FB slots, or a user FB's declared variables.
func (a *analysis) memberCompletions(typeName string) []CompletionItem {
	base := baseTypeName(typeName)
	if members, ok := a.typeMembers[strings.ToLower(base)]; ok {
		items := make([]CompletionItem, 0, len(members))
		for _, m := range members {
			items = append(items, CompletionItem{Label: m.Name, Kind: CompletionKindField, Detail: m.Datatype})
		}
		return items
	}
	if def, ok := ir.FBs[strings.ToUpper(base)]; ok {
		var items []CompletionItem
		for _, slot := range def.Inputs {
			items = append(items, CompletionItem{Label: slot.Name, Kind: CompletionKindField, Detail: slot.Type.String() + " input"})
		}
		for _, slot := range def.Outputs {
			items = append(items, CompletionItem{Label: slot.Name, Kind: CompletionKindField, Detail: slot.Type.String() + " output"})
		}
		return items
	}
	var items []CompletionItem
	for i := range a.Symbols {
		s := &a.Symbols[i]
		if strings.EqualFold(s.Container, base) && s.BlockKind != "FUNCTION_BLOCK" && s.BlockKind != "FUNCTION" {
			items = append(items, CompletionItem{Label: s.Name, Kind: CompletionKindField, Detail: s.Datatype})
		}
	}
	return items
}

// resolveChain walks a member-access chain from a base variable: for
// "Plt[3].Header." it looks up Plt's declared type, steps through Header,
// and returns the final type whose members should be offered.
func (a *analysis) resolveChain(base string, path []string, line int) (string, bool) {
	sym := a.lookup(base, line)
	if sym == nil {
		return "", false
	}
	t := sym.Datatype
	for _, member := range path {
		next, ok := a.memberType(t, member)
		if !ok {
			return "", false
		}
		t = next
	}
	return t, true
}

// memberContext detects a member-access completion site by scanning the line
// backwards from the cursor: "X.Header.Val|" yields base "X" and path
// ["Header"] (the partial "Val" is the client's filter text, not ours).
// Array indexing between segments ("Plt[3].") is skipped.
func memberContext(line string, col int) (base string, path []string, ok bool) {
	if col > len(line) {
		col = len(line)
	}
	i := col
	for i > 0 && isIdentByte(line[i-1]) {
		i--
	}
	if i == 0 || line[i-1] != '.' {
		return "", nil, false
	}
	var segs []string
	for i > 0 && line[i-1] == '.' {
		i-- // consume '.'
		for i > 0 && line[i-1] == ']' {
			depth := 0
			j := i
			for j > 0 {
				j--
				if line[j] == ']' {
					depth++
				} else if line[j] == '[' {
					depth--
					if depth == 0 {
						break
					}
				}
			}
			if depth != 0 {
				return "", nil, false
			}
			i = j
		}
		end := i
		for i > 0 && isIdentByte(line[i-1]) {
			i--
		}
		if i == end {
			return "", nil, false
		}
		seg := line[i:end]
		if seg[0] >= '0' && seg[0] <= '9' {
			return "", nil, false
		}
		segs = append([]string{seg}, segs...)
	}
	return segs[0], segs[1:], true
}

// baseTypeName reduces a declared datatype to the name hover expansion keys
// on: "ARRAY [0..3] OF Plt_Type" → "Plt_Type", otherwise the type as written.
func baseTypeName(datatype string) string {
	s := strings.TrimSpace(datatype)
	upper := strings.ToUpper(s)
	if strings.HasPrefix(upper, "ARRAY") {
		if i := strings.LastIndex(upper, " OF "); i >= 0 {
			return strings.TrimSpace(s[i+4:])
		}
	}
	return s
}

// typeExpansion returns the rendered definition for a datatype's base type,
// when the analysis knows it.
func (a *analysis) typeExpansion(datatype string) (string, bool) {
	def, ok := a.types[strings.ToLower(baseTypeName(datatype))]
	return def, ok
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
