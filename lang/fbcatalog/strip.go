package fbcatalog

// StripComments returns src with every comment's body blanked to spaces —
// `(* ... *)` block comments (which nest) and `//` line comments — leaving
// line and column positions untouched: the result has exactly the same
// length and line count as src. String literals ('...') are skipped whole,
// so a "(*" or "//" quoted inside one never opens a comment (lang/st's
// lexer never looks for a comment inside a string token either).
//
// Structural scans (is this line a FUNCTION_BLOCK, a VAR section, …) test
// the stripped text, so a doc comment showing example code is never read
// as real code; the text a caller keeps is still read from the original.
func StripComments(src string) string {
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
