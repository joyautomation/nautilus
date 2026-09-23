package l5x

import (
	"strings"
	"testing"
)

// render rebuilds neutral text from a parsed rung, so a round-trip proves
// the parse kept everything rather than merely not erroring.
func render(terms []Term) string {
	var b strings.Builder
	for _, t := range terms {
		if t.Instr != nil {
			b.WriteString(t.Instr.String())
			continue
		}
		b.WriteByte('[')
		for i, leg := range t.Legs {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(render(leg))
		}
		b.WriteByte(']')
	}
	return b.String()
}

func TestParseRungRoundTrips(t *testing.T) {
	cases := []struct{ in, want string }{
		{"OTE(Run);", "OTE(Run)"},
		{"[XIC(StartPB) ,XIC(RunCmd) ]XIO(StopPB)OTE(RunCmd);",
			"[XIC(StartPB),XIC(RunCmd)]XIO(StopPB)OTE(RunCmd)"},
		// Nested branches, five deep in the wild.
		{"[XIC(a) [XIO(b) ,XIC(c) [XIC(d) ,XIC(e) ] ] ,XIC(f) ]OTE(g);",
			"[XIC(a)[XIO(b),XIC(c)[XIC(d),XIC(e)]],XIC(f)]OTE(g)"},
		// An unset operand is a bare "?" and must survive as one.
		{"TON(t1,?,?)OTE(q);", "TON(t1,?,?)OTE(q)"},
		// CPT and CMP carry whole expressions, parens and all.
		{"CPT(dest,(RAWMAX - RAWMIN) / 2)OTE(d);", "CPT(dest,(RAWMAX - RAWMIN) / 2)OTE(d)"},
		{"CMP(N7[297]+N7[298] > 10)OTE(x);", "CMP(N7[297]+N7[298] > 10)OTE(x)"},
		// A subscripted, dotted operand — and a branch that IS the output.
		{"XIC(I_Img[19].14)[OTE(O_Img[46].15) ,OTL(M) ];",
			"XIC(I_Img[19].14)[OTE(O_Img[46].15),OTL(M)]"},
		// Instructions that take no operand at all.
		{"NOP();", "NOP()"},
		// A string literal operand, whose comma must not split the args.
		{"FIND(src,'a,b',0,pos,res);", "FIND(src,'a,b',0,pos,res)"},
		// An empty branch leg is a straight wire, and legal.
		{"[XIC(a) , ]OTE(b);", "[XIC(a),]OTE(b)"},
	}
	for _, c := range cases {
		got, err := ParseRung(c.in)
		if err != nil {
			t.Errorf("%s: %v", c.in, err)
			continue
		}
		if r := render(got); r != c.want {
			t.Errorf("%s\n got %s\nwant %s", c.in, r, c.want)
		}
	}
}

func TestParseRungRejectsMalformed(t *testing.T) {
	for _, in := range []string{
		"XIC(a",         // unterminated operand list
		"[XIC(a)",       // unterminated branch
		"XIC(a)]",       // unbalanced close
		"XIC(a),XIC(b)", // a top-level comma is only meaningful in a branch
	} {
		if terms, err := ParseRung(in); err == nil {
			t.Errorf("%q parsed as %s, want an error", in, render(terms))
		}
	}
}
