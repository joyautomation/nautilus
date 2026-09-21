package l5x

import (
	"fmt"
	"strings"
)

// Rung neutral text is what Logix exports as a ladder rung's body:
// instructions concatenated left to right along the power rail, with
// `[leg , leg]` for a parallel branch and a trailing semicolon.
//
//	[XIC(StartPB) ,XIC(RunCmd) ]XIO(StopPB)OTE(RunCmd);
//
// It is a small, regular language — no precedence, no keywords — and the
// grammar below is the whole of it:
//
//	rung   := term* ';'
//	term   := branch | instr
//	branch := '[' term* (',' term*)* ']'
//	instr  := MNEMONIC [ '(' arg (',' arg)* ')' ]
//
// An argument is opaque. Logix lets CPT and CMP carry a whole expression
// there ("RAWMAX - RAWMIN", "N7[297]+N7[298]"), and an unset operand is a
// bare "?". The reader keeps arguments verbatim: it is rendering a rung,
// not evaluating one, and a faithful string is worth more than a parse
// tree nobody would agree with.

// Term is one element of a rung: an instruction, or a parallel branch.
// Exactly one of Instr and Legs is set.
type Term struct {
	Instr *Instr
	Legs  [][]Term
}

// Branch reports whether the term is a parallel branch.
func (t Term) Branch() bool { return t.Instr == nil }

// Instr is one instruction call.
type Instr struct {
	Mnemonic string
	Args     []string
}

// String renders the instruction back to neutral text.
func (i *Instr) String() string {
	if len(i.Args) == 0 {
		return i.Mnemonic + "()"
	}
	return i.Mnemonic + "(" + strings.Join(i.Args, ",") + ")"
}

// ParseRung parses a rung's neutral text into its term tree.
func ParseRung(src string) ([]Term, error) {
	p := &rungParser{src: src}
	terms, err := p.terms(0)
	if err != nil {
		return nil, err
	}
	p.space()
	if p.pos < len(p.src) && p.src[p.pos] == ';' {
		p.pos++
		p.space()
	}
	if p.pos < len(p.src) {
		return nil, fmt.Errorf("rung: unexpected %q at offset %d", p.src[p.pos], p.pos)
	}
	return terms, nil
}

type rungParser struct {
	src string
	pos int
}

func (p *rungParser) space() {
	for p.pos < len(p.src) {
		switch p.src[p.pos] {
		case ' ', '\t', '\r', '\n':
			p.pos++
		default:
			return
		}
	}
}

// terms reads a run of terms, stopping at ';', ']' or ',' (the last two
// only inside a branch, which depth tracks).
func (p *rungParser) terms(depth int) ([]Term, error) {
	var out []Term
	for {
		p.space()
		if p.pos >= len(p.src) {
			return out, nil
		}
		switch c := p.src[p.pos]; c {
		case ';':
			return out, nil
		case ']', ',':
			if depth == 0 {
				return nil, fmt.Errorf("rung: unbalanced %q at offset %d", c, p.pos)
			}
			return out, nil
		case '[':
			p.pos++
			var legs [][]Term
			for {
				leg, err := p.terms(depth + 1)
				if err != nil {
					return nil, err
				}
				legs = append(legs, leg)
				p.space()
				if p.pos >= len(p.src) {
					return nil, fmt.Errorf("rung: unterminated branch")
				}
				if p.src[p.pos] == ',' {
					p.pos++
					continue
				}
				if p.src[p.pos] == ']' {
					p.pos++
					break
				}
				return nil, fmt.Errorf("rung: unexpected %q in branch at offset %d", p.src[p.pos], p.pos)
			}
			out = append(out, Term{Legs: legs})
		default:
			in, err := p.instr()
			if err != nil {
				return nil, err
			}
			out = append(out, Term{Instr: in})
		}
	}
}

func (p *rungParser) instr() (*Instr, error) {
	start := p.pos
	for p.pos < len(p.src) && isMnemonicByte(p.src[p.pos]) {
		p.pos++
	}
	name := p.src[start:p.pos]
	if name == "" {
		return nil, fmt.Errorf("rung: expected an instruction at offset %d, got %q", start, p.src[start])
	}
	p.space()
	if p.pos >= len(p.src) || p.src[p.pos] != '(' {
		// A few instructions take no operand at all (NOP, TND, RET).
		return &Instr{Mnemonic: name}, nil
	}
	p.pos++
	args, err := p.args()
	if err != nil {
		return nil, err
	}
	return &Instr{Mnemonic: name, Args: args}, nil
}

// args reads to the instruction's closing paren, splitting on top-level
// commas. Nesting ((), [], ”) is tracked so an expression operand — which
// CPT and CMP carry whole — survives intact.
func (p *rungParser) args() ([]string, error) {
	var out []string
	var cur strings.Builder
	depth := 0
	inStr := false
	flush := func() {
		out = append(out, strings.TrimSpace(cur.String()))
		cur.Reset()
	}
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		p.pos++
		if inStr {
			cur.WriteByte(c)
			if c == '\'' {
				inStr = false
			}
			continue
		}
		switch c {
		case '\'':
			inStr = true
			cur.WriteByte(c)
		case '(', '[':
			depth++
			cur.WriteByte(c)
		case ']':
			depth--
			cur.WriteByte(c)
		case ')':
			if depth == 0 {
				flush()
				if len(out) == 1 && out[0] == "" {
					return nil, nil
				}
				return out, nil
			}
			depth--
			cur.WriteByte(c)
		case ',':
			if depth == 0 {
				flush()
				continue
			}
			cur.WriteByte(c)
		default:
			cur.WriteByte(c)
		}
	}
	return nil, fmt.Errorf("rung: unterminated operand list")
}

func isMnemonicByte(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_'
}
