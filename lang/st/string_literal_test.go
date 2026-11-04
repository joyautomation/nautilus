package st

import (
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/ir"
)

// IEC 61131-3 has two string literals: '…' is a STRING, "…" a WSTRING
// (held as STRING here). Neither one's text is ever read as code, and a $
// escape never closes one (nautilus#591).
func TestStringLiteralsAreOneToken(t *testing.T) {
	for _, tc := range []struct {
		src   string
		quote byte
		raw   string
	}{
		{`"a&b"`, '"', "a&b"},
		{`"a+b"`, '"', "a+b"},
		{`'a&b'`, '\'', "a&b"},
		{`"it's"`, '"', "it's"},
		{`'say "hi"'`, '\'', `say "hi"`},
		{`'it$'s'`, '\'', "it$'s"},
		{`"say $"hi$""`, '"', `say $"hi$"`},
		{`'$$'`, '\'', "$$"},
		{`''`, '\'', ""},
		{`""`, '"', ""},
	} {
		toks, errs := LexErrors("x := " + tc.src + ";")
		if len(errs) != 0 {
			t.Errorf("%s: lex errors %v", tc.src, errs)
		}
		if len(toks) != 5 || toks[2].Type != TokenString {
			t.Errorf("%s: tokens %v, want x := <string> ; EOF", tc.src, toks)
			continue
		}
		s := toks[2]
		if s.Literal != tc.raw || s.Quote != tc.quote {
			t.Errorf("%s: literal %q quote %q, want %q %q", tc.src, s.Literal, s.Quote, tc.raw, tc.quote)
		}
		if s.Line != 1 || s.Col != 6 {
			t.Errorf("%s: at %d:%d, want 1:6 (the opening quote)", tc.src, s.Line, s.Col)
		}
	}
}

// Each IEC escape is its character in the value: $$ $' $" $L $N $P $R $T,
// and a character code, $hh in '…' and $hhhh in "…".
func TestStringEscapesDecode(t *testing.T) {
	for _, tc := range []struct{ src, want string }{
		{`'it$'s'`, "it's"},
		{`"say $"hi$""`, `say "hi"`},
		{`'$$5'`, "$5"},
		{`'a$Nb$lc'`, "a\nb\nc"},
		{`'$R$T$P'`, "\r\t\f"},
		{`'$41$62'`, "Ab"},
		{`'$C4'`, "Ä"},
		{`"$00C4$0041"`, "ÄA"},
		{`"x$'y"`, "x'y"},
		{`STRING#'it$'s'`, "it's"},
		{`WSTRING#"a&b$"c"`, `a&b"c`},
	} {
		src := "PROGRAM P\nVAR_EXTERNAL S : STRING; END_VAR\nS := " + tc.src + ";\nEND_PROGRAM\n"
		h, _, _ := scanN(t, src, 1, nil)
		if got := h.globals["S"].S; got != tc.want {
			t.Errorf("%s: S = %q, want %q", tc.src, got, tc.want)
		}
	}
}

// A string that does not close on its line, or holds an unknown $ escape,
// is an error at the string, not a confusing one further on (or none).
func TestStringLiteralErrors(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{"S := 'abc;", "line 3:6: unterminated string: close it with '"},
		{"S := \"abc;", "line 3:6: unterminated string: close it with \""},
		{"S := 'it$'s;", "line 3:6: unterminated string"},
		{"S := 'a$Qb';", "line 3:8: unknown string escape"},
		{"S := '50$';", "line 3:6: unterminated string"},
		{"S := \"$41\";", "line 3:7: unknown string escape: $ starts $$, $', $\", $L, $N, $P, $R, $T or $hhhh (four hex digits)"},
	} {
		src := "PROGRAM P\nVAR_EXTERNAL S : STRING; END_VAR\n" + tc.body + "\nEND_PROGRAM\n"
		_, err := Parse(src)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err %v, want it to contain %q", tc.body, err, tc.want)
		}
	}
}

// QuoteString writes any value back as a literal that reads as it.
func TestQuoteStringRoundTrips(t *testing.T) {
	for _, v := range []string{"", "plain", "it's", `say "hi"`, "$5", "a\nb\tc\r\f", "\x01", "Ä", "a&b"} {
		src := "PROGRAM P\nVAR_EXTERNAL S : STRING; END_VAR\nS := " + QuoteString(v) + ";\nEND_PROGRAM\n"
		h, _, _ := scanN(t, src, 1, map[string]ir.Value{"S": ir.StringVal("unset")})
		if got := h.globals["S"].S; got != v {
			t.Errorf("QuoteString(%q) = %s reads back as %q", v, QuoteString(v), got)
		}
	}
}
