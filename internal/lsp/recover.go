package lsp

import "github.com/joyautomation/nautilus/lang/st"

// declSkeleton reduces ST source to its declarations: POU headers and
// END_ keywords, VAR…END_VAR blocks and TYPE…END_TYPE blocks survive, and
// every body statement is blanked to spaces (newlines kept), so each
// surviving byte stays at its original line and column.
//
// It exists for the moment between keystrokes. A buffer the user is typing
// in rarely parses ("settle." on a fresh line never does), yet hover and
// member completion only need the declarations, and those are almost always
// intact while the body is being edited. Parsing the skeleton recovers the
// symbol table with exact positions; the real parse error is still the
// document's diagnostic.
//
// The scan runs on the real lexer's tokens, so comments and strings never
// look like keywords. A blanked span runs from the start of one body token
// to the start of the next kept token, so it never cuts a comment or string
// in half.
func declSkeleton(text string) string {
	toks := st.Lex(text)
	lineStart := []int{0}
	for i := 0; i < len(text); i++ {
		if text[i] == '\n' {
			lineStart = append(lineStart, i+1)
		}
	}
	offset := func(t st.Token) int {
		if t.Line < 1 || t.Line > len(lineStart) {
			return len(text)
		}
		return min(lineStart[t.Line-1]+t.Col-1, len(text))
	}

	keep := make([]bool, len(toks))
	n := len(toks) - 1 // the trailing EOF token is not source text
	// keepThrough keeps toks[i] up to and including the first token of type
	// end (and a ';' right after it), returning the index after that.
	keepThrough := func(i int, end st.TokenType) int {
		for ; i < n; i++ {
			keep[i] = true
			if toks[i].Type == end {
				i++
				if i < n && toks[i].Type == st.TokenSemicolon {
					keep[i] = true
					i++
				}
				return i
			}
		}
		return i
	}
	for i := 0; i < n; {
		switch t := toks[i].Type; {
		case t == st.TokenTypeKw:
			i = keepThrough(i, st.TokenEndType)
		case isVarStart(t):
			i = keepThrough(i, st.TokenEndVar)
		case t == st.TokenProgram || t == st.TokenFunctionBlock:
			keep[i] = true // keyword and name
			if i+1 < n {
				keep[i+1] = true
			}
			i += 2
		case t == st.TokenFunction:
			// FUNCTION Name : ReturnType [;] — the header is one line; stop
			// early at a VAR block written on the same line.
			line := toks[i].Line
			for ; i < n && toks[i].Line == line && !isVarStart(toks[i].Type); i++ {
				keep[i] = true
			}
		case t == st.TokenEndProgram || t == st.TokenEndFunctionBlock || t == st.TokenEndFunction:
			keep[i] = true
			i++
		default:
			i++
		}
	}

	b := []byte(text)
	blank := func(from, to int) {
		for j := from; j < to; j++ {
			if b[j] != '\n' {
				b[j] = ' '
			}
		}
	}
	for i := 0; i < n; {
		if keep[i] {
			i++
			continue
		}
		j := i
		for j < n && !keep[j] {
			j++
		}
		end := len(text) // a body running to EOF
		if j < n {
			end = offset(toks[j])
		}
		blank(offset(toks[i]), end)
		i = j
	}
	return string(b)
}

func isVarStart(t st.TokenType) bool {
	switch t {
	case st.TokenVar, st.TokenVarInput, st.TokenVarOutput, st.TokenVarInOut,
		st.TokenVarTemp, st.TokenVarGlobal, st.TokenVarExternal:
		return true
	}
	return false
}

// carryDeclarations gives an analysis whose text recovered no declarations
// at all (the skeleton did not parse either — e.g. a VAR block mid-edit)
// the symbol table and type indexes of the document's previous analysis.
// Positions may be a few lines stale; names and declared types are what
// hover and member completion need, and they rarely change mid-keystroke.
// Diagnostics and scopes stay this text's own.
func (a *analysis) carryDeclarations(prev *analysis) {
	if a.indexed || !prev.indexed {
		return
	}
	a.Symbols = prev.Symbols
	a.types = prev.types
	a.typeMembers = prev.typeMembers
	a.indexed = true
}
