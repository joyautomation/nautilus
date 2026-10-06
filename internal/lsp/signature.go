package lsp

import (
	"strings"
	"sync"

	"github.com/joyautomation/nautilus/internal/stproject"
	"github.com/joyautomation/nautilus/lang/fbcatalog"
	"github.com/joyautomation/nautilus/lang/fbd"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/lang/st"
)

// Signature help: while the cursor sits inside a call's parentheses, the
// editor shows the callee's parameters with the one being typed
// highlighted — what Codesys, Studio 5000 and TIA show while a call is
// written.
//
// The call is found textually (callAt), on the real lexer's tokens, so it
// works mid-keystroke while the line cannot parse; the callee is resolved
// against the compiler's own registries (ir.Builtins, ir.FBs) and the
// document's declarations, which declSkeleton recovers even then.

// SignatureHelpOpts advertises textDocument/signatureHelp. `(` opens the
// widget; `,` re-asks so the highlight moves to the next parameter.
type SignatureHelpOpts struct {
	TriggerCharacters   []string `json:"triggerCharacters,omitempty"`
	RetriggerCharacters []string `json:"retriggerCharacters,omitempty"`
}

// SignatureHelp is the textDocument/signatureHelp result.
type SignatureHelp struct {
	Signatures      []SignatureInformation `json:"signatures"`
	ActiveSignature int                    `json:"activeSignature"`
	ActiveParameter int                    `json:"activeParameter"`
}

// SignatureInformation is one callable's signature.
type SignatureInformation struct {
	Label         string                 `json:"label"`
	Documentation *MarkupContent         `json:"documentation,omitempty"`
	Parameters    []ParameterInformation `json:"parameters"`
}

// ParameterInformation names one parameter by its [start, end) offsets in
// the signature label — offsets, not a substring, because a pin name like
// IN also occurs inside MIN or another pin's type.
type ParameterInformation struct {
	Label         [2]int         `json:"label"`
	Documentation *MarkupContent `json:"documentation,omitempty"`
}

func signatureHelpOptions() *SignatureHelpOpts {
	return &SignatureHelpOpts{TriggerCharacters: []string{"(", ","}, RetriggerCharacters: []string{","}}
}

func (s *Server) handleSignatureHelp(m *message) {
	doc, uri, _, _, pos, ok := s.positional(m)
	if !ok {
		return
	}
	if doc.test != nil {
		s.w.respond(m.ID, nil)
		return
	}
	frame := callAt(doc.text, pos)
	if frame == nil {
		s.w.respond(m.ID, nil)
		return
	}
	r := sigResolver{an: &doc.an, line: pos.Line + 1, graphical: isGraphical(uri)}
	r.prelude = func() []Symbol { return s.preludeSymbols(uri) }
	sig := r.resolve(frame)
	if sig == nil {
		s.w.respond(m.ID, nil)
		return
	}
	s.w.respond(m.ID, sig.help(frame))
}

// isGraphical reports a .fbd/.ld document, where the operator blocks
// (ADD, GT, AND, …) are written as calls.
func isGraphical(uri string) bool {
	u := strings.ToLower(uri)
	return strings.HasSuffix(u, ".fbd") || strings.HasSuffix(u, ".ld")
}

// preludeSymbols indexes the project's library files (the prelude
// setDocument compiles against), for a call to a FUNCTION or
// FUNCTION_BLOCK declared in a sibling file. Only consulted when the
// document itself does not know the callee.
func (s *Server) preludeSymbols(uri string) []Symbol {
	path, ok := uriToPath(uri)
	if !ok {
		return nil
	}
	overrides := map[string]string{}
	for otherURI, otherDoc := range s.docs {
		if otherURI == uri {
			continue
		}
		if p, ok := uriToPath(otherURI); ok {
			overrides[p] = otherDoc.text
		}
	}
	prelude, _ := stproject.Prelude(path, overrides)
	if prelude == "" {
		return nil
	}
	prog, err := st.Parse(prelude)
	if err != nil {
		return nil
	}
	return collectSymbols(prog)
}

// ─── Finding the call ──────────────────────────────────────────────────────

// callFrame is one open parenthesis between the start of the statement and
// the cursor.
type callFrame struct {
	name string // callee; "" for a grouping parenthesis
	// unresolvable marks a call the server cannot name (a.b(…)): answer
	// nothing rather than the enclosing call's signature.
	unresolvable bool
	// inlineType is the graphical `inst : TYPE(` form — name is the type.
	inlineType bool
	commas     int
	argToks    int      // tokens in the current argument so far
	named      string   // pin the current argument binds (`PT :=`), if any
	bound      []string // pins bound by name so far in this call
}

// callAt finds the innermost call whose argument list encloses the cursor,
// or nil. It lexes the text before the cursor with the real lexer, so
// comments and string literals never contribute parentheses or commas,
// and tracks the open parentheses: a `;` (or a declaration keyword) ends a
// statement and drops any left open. Grouping parentheses are skipped on
// the way out, so `LIMIT(0.0, (a + |` is still LIMIT's second argument.
func callAt(text string, pos Position) *callFrame {
	toks := st.Lex(text[:offsetAt(text, pos)])
	if n := len(toks); n > 0 && toks[n-1].Type == st.TokenEOF {
		toks = toks[:n-1]
	}
	var stack []*callFrame
	top := func() *callFrame {
		if len(stack) == 0 {
			return nil
		}
		return stack[len(stack)-1]
	}
	for i, t := range toks {
		switch {
		case t.Type == st.TokenSemicolon || t.Type == st.TokenEndVar || isVarStart(t.Type):
			stack = stack[:0]
			continue
		case t.Type == st.TokenLParen:
			if f := top(); f != nil {
				f.argToks++
			}
			f := &callFrame{}
			if i > 0 && isCalleeTok(toks[i-1].Type) {
				f.name = toks[i-1].Literal
				switch {
				case i > 1 && toks[i-2].Type == st.TokenDot:
					f.unresolvable = true
				case i > 2 && toks[i-2].Type == st.TokenColon && toks[i-3].Type == st.TokenIdent:
					f.inlineType = true
				}
			}
			stack = append(stack, f)
			continue
		case t.Type == st.TokenRParen:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		case t.Type == st.TokenComma:
			if f := top(); f != nil {
				f.commas++
				f.argToks = 0
				f.named = ""
				continue
			}
		case t.Type == st.TokenAssign || t.Type == st.TokenOutputAssign:
			// `PIN :=` / `PIN =>` as the argument's first two tokens.
			if f := top(); f != nil && f.argToks == 1 && toks[i-1].Type == st.TokenIdent {
				f.named = toks[i-1].Literal
				f.bound = append(f.bound, f.named)
			}
		}
		if f := top(); f != nil {
			f.argToks++
		}
	}
	for i := len(stack) - 1; i >= 0; i-- {
		f := stack[i]
		if f.unresolvable {
			return nil
		}
		if f.name != "" {
			return f
		}
	}
	return nil
}

// isCalleeTok: an identifier, or one of the operator keywords that the
// graphical languages write as blocks (AND(a, b), NOT(x), MOD(a, b)).
func isCalleeTok(t st.TokenType) bool {
	switch t {
	case st.TokenIdent, st.TokenAnd, st.TokenOr, st.TokenXor, st.TokenNot, st.TokenMod:
		return true
	}
	return false
}

// offsetAt converts a 0-based LSP position to a byte offset into text,
// clamped to the line's end.
func offsetAt(text string, pos Position) int {
	off := 0
	for line := 0; line < pos.Line; line++ {
		nl := strings.IndexByte(text[off:], '\n')
		if nl < 0 {
			return len(text)
		}
		off += nl + 1
	}
	end := len(text)
	if nl := strings.IndexByte(text[off:], '\n'); nl >= 0 {
		end = off + nl
	}
	return min(off+max(pos.Character, 0), end)
}

// ─── Resolving the callee ──────────────────────────────────────────────────

// Parameter directions; "" for a function's parameters.
const (
	dirIn    = "input"
	dirInOut = "in_out"
	dirOut   = "output"
)

type sigParam struct{ name, typ, dir string }

// callSig is a resolved callable.
type callSig struct {
	name     string
	params   []sigParam
	result   string // functions only
	variadic bool   // the last parameter repeats
	fb       bool
	doc      string // short behaviour text, when the registry has one
}

type sigResolver struct {
	an        *analysis
	line      int  // 1-based cursor line, for scoped lookup
	graphical bool // .fbd/.ld: operator blocks are calls too
	prelude   func() []Symbol
	preSyms   []Symbol
	preDone   bool
}

func (r *sigResolver) preludeSyms() []Symbol {
	if !r.preDone && r.prelude != nil {
		r.preSyms, r.preDone = r.prelude(), true
	}
	return r.preSyms
}

// resolve names the frame's callee: a builtin function or conversion, a
// graphical operator, a user FUNCTION, or a function block — called
// through an instance (`t1(`), or by type in the graphical `inst : TYPE(`
// form. Unknown callees resolve to nil.
func (r *sigResolver) resolve(f *callFrame) *callSig {
	name := f.name
	if f.inlineType {
		return r.fbSig(name)
	}
	upper := strings.ToUpper(name)
	if b, ok := ir.Builtins[upper]; ok {
		return builtinSig(b)
	}
	if r.graphical {
		if sig := operatorSig(upper); sig != nil {
			return sig
		}
	}
	if sig := r.userPOU(name, r.an.Symbols); sig != nil {
		return sig
	}
	if sym := r.an.lookup(name, r.line); sym != nil {
		switch sym.BlockKind {
		case "FUNCTION", "FUNCTION_BLOCK", "TYPE":
		default:
			return r.fbSig(baseTypeName(sym.Datatype))
		}
	}
	if _, ok := ir.FBs[upper]; ok {
		return r.fbSig(upper)
	}
	return r.userPOU(name, r.preludeSyms())
}

// userPOU resolves a user FUNCTION (called by name) or FUNCTION_BLOCK
// type (the graphical forms call a block by type) among syms.
func (r *sigResolver) userPOU(name string, syms []Symbol) *callSig {
	for i := range syms {
		s := &syms[i]
		if s.Container != "" || !strings.EqualFold(s.Name, name) {
			continue
		}
		switch s.BlockKind {
		case "FUNCTION":
			sig := &callSig{name: s.Name, result: s.Datatype}
			for _, p := range pouPins(syms, s.Name) {
				if p.dir == dirIn {
					p.dir = ""
					sig.params = append(sig.params, p)
				}
			}
			return sig
		case "FUNCTION_BLOCK":
			return userFBSig(s.Name, syms)
		}
	}
	return nil
}

// fbSig resolves a function block type: a standard block, or a user block
// declared in this document or a project library.
func (r *sigResolver) fbSig(typ string) *callSig {
	if def, ok := ir.FBs[strings.ToUpper(typ)]; ok {
		sig := &callSig{name: def.Name, fb: true, doc: standardFBDetail()[def.Name]}
		add := func(slots []ir.FBSlot, dir string) {
			for _, s := range slots {
				sig.params = append(sig.params, sigParam{s.Name, s.Type.String(), dir})
			}
		}
		add(def.Inputs, dirIn)
		add(def.InOuts, dirInOut)
		add(def.Outputs, dirOut)
		return sig
	}
	for _, syms := range [][]Symbol{r.an.Symbols, r.preludeSyms()} {
		for i := range syms {
			if syms[i].BlockKind == "FUNCTION_BLOCK" && syms[i].Container == "" && strings.EqualFold(syms[i].Name, typ) {
				return userFBSig(syms[i].Name, syms)
			}
		}
	}
	return nil
}

// userFBSig lists a user block's pins: inputs, then in-outs, then
// outputs, each in declaration order.
func userFBSig(name string, syms []Symbol) *callSig {
	sig := &callSig{name: name, fb: true}
	pins := pouPins(syms, name)
	for _, dir := range []string{dirIn, dirInOut, dirOut} {
		for _, p := range pins {
			if p.dir == dir {
				sig.params = append(sig.params, p)
			}
		}
	}
	return sig
}

// pouPins is a POU's VAR_INPUT / VAR_IN_OUT / VAR_OUTPUT declarations.
func pouPins(syms []Symbol, pou string) []sigParam {
	var out []sigParam
	for i := range syms {
		s := &syms[i]
		if !strings.EqualFold(s.Container, pou) {
			continue
		}
		dir := ""
		switch strings.ToUpper(s.BlockKind) {
		case "VAR_INPUT":
			dir = dirIn
		case "VAR_IN_OUT":
			dir = dirInOut
		case "VAR_OUTPUT":
			dir = dirOut
		default:
			continue
		}
		out = append(out, sigParam{s.Name, s.Datatype, dir})
	}
	return out
}

var (
	fbDetailOnce sync.Once
	fbDetail     map[string]string
)

// standardFBDetail is the palette's one-line description of each standard
// block (lang/fbcatalog) — the short behaviour text a block carries.
func standardFBDetail() map[string]string {
	fbDetailOnce.Do(func() {
		fbDetail = map[string]string{}
		for _, t := range fbcatalog.Standard() {
			fbDetail[t.Name] = t.Detail
		}
	})
	return fbDetail
}

// builtinSig renders a registry function. The registry types parameters
// but does not name them; the names are the ones the FBD diagram gives the
// same block's pins (fbd.BlockPins), so both views agree. A nil (generic)
// parameter is ANY_NUM for the numeric functions and ANY for SEL/MUX.
func builtinSig(b ir.BuiltinSig) *callSig {
	generic := "ANY_NUM"
	if b.Name == "SEL" || b.Name == "MUX" {
		generic = "ANY"
	}
	if b.Generic != "" {
		generic = b.Generic // TO_<type>: ANY_ELEMENTARY
	}
	n := len(b.Params)
	if b.Name == "MUX" && n < 3 {
		n = 3 // K plus at least two inputs
	}
	names := fbd.BlockPins(b.Name, n)
	sig := &callSig{name: b.Name, variadic: b.Variadic, result: generic}
	if b.Result != nil {
		sig.result = b.Result.String()
	}
	for i, nm := range names {
		t := generic
		if p := b.Params[min(i, len(b.Params)-1)]; p != nil {
			t = p.String()
		}
		sig.params = append(sig.params, sigParam{name: nm, typ: t})
	}
	return sig
}

// operatorSig covers the operator blocks the graphical languages call by
// name (docs/functions.md "Operators"); ST writes these as operators.
func operatorSig(name string) *callSig {
	var typ, result string
	n, variadic := 2, false
	switch name {
	case "AND", "OR", "XOR":
		typ, result, variadic = "ANY_BIT", "ANY_BIT", true
	case "NOT":
		typ, result, n = "ANY_BIT", "ANY_BIT", 1
	case "ADD", "MUL":
		typ, result, variadic = "ANY_NUM", "ANY_NUM", true
	case "SUB", "DIV":
		typ, result = "ANY_NUM", "ANY_NUM"
	case "MOD":
		typ, result = "INT", "INT"
	case "MOVE":
		typ, result, n = "ANY", "ANY", 1
	case "GT", "GE", "LT", "LE", "EQ", "NE":
		typ, result = "ANY", "BOOL"
	default:
		return nil
	}
	sig := &callSig{name: name, result: result, variadic: variadic}
	for _, nm := range fbd.BlockPins(name, n) {
		sig.params = append(sig.params, sigParam{name: nm, typ: typ})
	}
	return sig
}

// ─── Rendering ─────────────────────────────────────────────────────────────

// help renders the signature for the cursor's position in frame.
//
// Functions: `LIMIT(MN : ANY_NUM, IN : ANY_NUM, MX : ANY_NUM) : ANY_NUM`,
// the active parameter counted by top-level commas. A variadic function
// grows a parameter per argument typed, so MAX's fifth argument is IN5.
//
// Function blocks: `TON(IN : BOOL, PT : TIME) => Q : BOOL, ET : TIME` —
// inputs and in-outs in the parentheses, outputs after `=>`. The active
// parameter is the pin the current argument names (`PT :=`); before a pin
// is named, the first input not yet bound.
func (c *callSig) help(f *callFrame) SignatureHelp {
	params := c.params
	if c.variadic && len(params) > 0 && f.commas >= len(params) {
		last := params[len(params)-1]
		names := fbd.BlockPins(c.name, f.commas+1)
		params = append([]sigParam{}, params...)
		for i := len(params); i <= f.commas; i++ {
			nm := last.name
			if i < len(names) {
				nm = names[i]
			}
			params = append(params, sigParam{name: nm, typ: last.typ})
		}
	}

	var b strings.Builder
	var infos []ParameterInformation
	b.WriteString(c.name + "(")
	wrote := 0
	param := func(p sigParam) {
		start := b.Len()
		b.WriteString(p.name + " : " + p.typ)
		pi := ParameterInformation{Label: [2]int{start, b.Len()}}
		if p.dir != "" {
			pi.Documentation = &MarkupContent{Kind: "plaintext", Value: p.dir + " " + p.typ}
		}
		infos = append(infos, pi)
	}
	var outs []sigParam
	for _, p := range params {
		if p.dir == dirOut {
			outs = append(outs, p)
			continue
		}
		if wrote > 0 {
			b.WriteString(", ")
		}
		param(p)
		wrote++
	}
	if c.variadic {
		b.WriteString(", …")
	}
	b.WriteString(")")
	if c.fb {
		for i, p := range outs {
			if i == 0 {
				b.WriteString(" => ")
			} else {
				b.WriteString(", ")
			}
			param(p)
		}
	} else if c.result != "" {
		b.WriteString(" : " + c.result)
	}

	// Order of infos: non-outputs then outputs — the same order the label
	// lists them; rebuild params in that order for the active lookup.
	ordered := make([]sigParam, 0, len(params))
	for _, p := range params {
		if p.dir != dirOut {
			ordered = append(ordered, p)
		}
	}
	ordered = append(ordered, outs...)

	si := SignatureInformation{Label: b.String(), Parameters: infos}
	if si.Parameters == nil {
		si.Parameters = []ParameterInformation{}
	}
	if c.doc != "" {
		si.Documentation = &MarkupContent{Kind: "plaintext", Value: c.doc}
	}
	return SignatureHelp{
		Signatures:      []SignatureInformation{si},
		ActiveParameter: activeParam(c, f, ordered),
	}
}

// activeParam picks the highlighted parameter; len(params) (out of range,
// nothing highlighted) when no parameter fits.
func activeParam(c *callSig, f *callFrame, params []sigParam) int {
	if f.named != "" {
		for i, p := range params {
			if strings.EqualFold(p.name, f.named) {
				return i
			}
		}
		return len(params)
	}
	if !c.fb {
		return min(f.commas, len(params))
	}
	isBound := func(name string) bool {
		for _, b := range f.bound {
			if strings.EqualFold(b, name) {
				return true
			}
		}
		return false
	}
	for _, out := range []bool{false, true} {
		for i, p := range params {
			if (p.dir == dirOut) == out && !isBound(p.name) {
				return i
			}
		}
	}
	return len(params)
}
