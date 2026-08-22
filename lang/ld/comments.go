package ld

// stripComments returns src with every comment's body blanked to spaces —
// `(* ... *)` block comments (which nest, IEC 61131-3 3rd edition) and
// `//` line comments — while leaving line and column positions untouched:
// comment characters (delimiters included) become ' ', newlines stay
// newlines. The result has exactly the same length and line count as src.
//
// String literals ('...') are skipped whole and never mistaken for a
// comment delimiter — a "(*", "*)" or "//" quoted inside one does not
// open or close a comment. This mirrors lang/st's lexer, which never looks
// for a comment from inside a string token either.
//
// Every STRUCTURAL scan in this package — is this line a FUNCTION_BLOCK /
// END_FUNCTION_BLOCK, an LD / END_LD, a RUNG header, a VAR* / END_VAR —
// must test against stripComments' output, not the raw source. Otherwise a
// `(* ... *)` block comment whose text happens to start a line with one of
// those keywords (a doc comment showing example ladder, say) gets scanned
// as if it were real code. Content that belongs to the comment itself — a
// rung's `(* ... *)` header comment, a `//` diagram note — is still read
// from the ORIGINAL source: stripComments exists only to decide what's
// real code, never to supply the text a caller actually wants to keep.
func stripComments(src string) string {
	out := []byte(src)
	inString := false
	for i := 0; i < len(out); {
		switch {
		case inString:
			if out[i] == '\'' {
				inString = false
			}
			i++
		case out[i] == '\'':
			inString = true
			i++
		case out[i] == '/' && i+1 < len(out) && out[i+1] == '/':
			for i < len(out) && out[i] != '\n' {
				out[i] = ' '
				i++
			}
		case out[i] == '(' && i+1 < len(out) && out[i+1] == '*':
			depth := 1
			out[i], out[i+1] = ' ', ' '
			i += 2
			for i < len(out) && depth > 0 {
				switch {
				case out[i] == '(' && i+1 < len(out) && out[i+1] == '*':
					depth++
					out[i], out[i+1] = ' ', ' '
					i += 2
				case out[i] == '*' && i+1 < len(out) && out[i+1] == ')':
					depth--
					out[i], out[i+1] = ' ', ' '
					i += 2
				case out[i] == '\n':
					i++ // keep newlines so line numbers stay aligned
				default:
					out[i] = ' '
					i++
				}
			}
		default:
			i++
		}
	}
	return string(out)
}
