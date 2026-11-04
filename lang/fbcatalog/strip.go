package fbcatalog

import "github.com/joyautomation/nautilus/lang/st"

// StripComments returns src with every comment's body blanked to spaces —
// `(* ... *)` block comments (which nest) and `//` line comments — leaving
// line and column positions untouched: the result has exactly the same
// length and line count as src. String literals ('...' and "...") are
// skipped whole, ending where lang/st's lexer ends them (st.StringEnd), so
// a "(*" or "//" quoted inside one never opens a comment.
//
// Structural scans (is this line a FUNCTION_BLOCK, a VAR section, …) test
// the stripped text, so a doc comment showing example code is never read
// as real code; the text a caller keeps is still read from the original.
func StripComments(src string) string {
	out := []byte(src)
	for i := 0; i < len(out); {
		switch {
		case out[i] == '\'' || out[i] == '"':
			// A string, '…' or "…", skipped whole as st reads it ($'
			// and $" never close it), so a (* or // inside is text.
			i, _ = st.StringEnd(src, i)
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
