package fbd

// stripComments returns src with every comment's body blanked to spaces —
// `(* ... *)` block comments (which nest, IEC 61131-3 3rd edition) and
// `//` line comments — while leaving line and column positions untouched:
// comment characters (delimiters included) become ' ', newlines stay
// newlines. The result has exactly the same length and line count as src.
//
// String literals ('...') are skipped whole and never mistaken for a
// comment delimiter — a "(*", "*)" or "//" quoted inside one does not open
// or close a comment. This mirrors lang/st's lexer, which never looks for
// a comment from inside a string token either. (Kept as a package-local
// copy of lang/ld's identical helper — these two leaf packages have no
// dependency on each other and the function has none of its own.)
//
// Every STRUCTURAL scan in this package — is this line FBD / END_FBD, a
// PROGRAM / FUNCTION_BLOCK header, a VAR* section opener / END_VAR — must
// test against stripComments' output, not the raw source. Otherwise a
// `(* ... *)` block comment whose text happens to start a line with one of
// those keywords (documentation showing example FBD, say) gets scanned as
// if it were real code.
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
