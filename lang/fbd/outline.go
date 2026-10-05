package fbd

import (
	"strings"

	"github.com/joyautomation/nautilus/lang/st"
)

// Statement is one netlist statement as an editor outline lists it: the
// name it assigns or calls, and where it sits in the .fbd source.
type Statement struct {
	// Kind is "instance" (inst : TYPE[(...)]), "call" (inst(...)), "coil"
	// (target := expr) or "wire" (name = block).
	Kind string
	Name string // the instance, coil target (accessor chain included) or wire
	Type string // the FB type, Kind "instance" only
	// Rest is the statement's text after its name, whitespace collapsed:
	// "= GE(Level, 90.0)", ":= latch", ": TON(IN := Run, PT := T#5s)".
	Rest string
	// Line/Col .. EndLine/EndCol span the whole statement (through a
	// trailing ';'), 1-based, the end just past the last character.
	Line, Col, EndLine, EndCol int
	// NameLine/NameCol .. NameEndCol span the leading name token.
	NameLine, NameCol, NameEndCol int
}

// Statements lists every netlist statement of every FBD … END_FBD block in
// source order. It is lenient where Transpile is strict, because an outline
// has to keep working while the user types: an unclosed block runs to the
// end of the file, and a statement that does not parse is skipped (the
// parser resumes on the next line) instead of failing the whole body.
func Statements(src string) []Statement {
	lines := strings.Split(src, "\n")
	stripped := strings.Split(stripComments(src), "\n")
	var out []Statement
	start := -1
	emit := func(end int) {
		body := strings.Join(lines[start+1:end], "\n")
		out = append(out, bodyStatements(lines, body, start+1)...)
	}
	for i, l := range stripped {
		switch strings.ToUpper(strings.TrimSpace(l)) {
		case "FBD":
			if start != -1 {
				emit(i)
			}
			start = i
		case "END_FBD":
			if start != -1 {
				emit(i)
				start = -1
			}
		}
	}
	if start != -1 {
		emit(len(lines))
	}
	return out
}

// bodyStatements parses one netlist body item by item, each into a fresh
// netlist so the statement it produced is the only thing in it.
func bodyStatements(fileLines []string, body string, lineOffset int) []Statement {
	p := &netParser{toks: st.Lex(body), lineOffset: lineOffset, lines: strings.Split(body, "\n")}
	var out []Statement
	for !p.at(st.TokenEOF) {
		from := p.pos
		nl := &netlist{wires: map[string]expr{}, wirePos: map[string]exprPos{}, wireSpan: map[string]exprPos{}}
		if err := p.item(nl); err != nil {
			// Resume on the first token of a later line than the one the
			// parse stopped at — always moving forward.
			stop := p.toks[min(p.pos, len(p.toks)-1)].Line
			if p.pos <= from {
				p.pos = from + 1
			}
			for !p.at(st.TokenEOF) && p.peek().Line <= stop {
				p.pos++
			}
			continue
		}
		var s Statement
		var name, span exprPos
		switch {
		case len(nl.fbDecls) == 1:
			d := nl.fbDecls[0]
			s = Statement{Kind: "instance", Name: d.name, Type: d.typ}
			name, span = d.namePos, d.span
		case len(nl.wireSrc) == 1:
			w := nl.wireSrc[0]
			s = Statement{Kind: "wire", Name: w}
			name, span = nl.wirePos[w], nl.wireSpan[w]
		case len(nl.nodes) == 1 && nl.nodes[0].isCall:
			n := nl.nodes[0]
			s = Statement{Kind: "call", Name: n.inst}
			name, span = n.lhs, n.span
		case len(nl.nodes) == 1:
			n := nl.nodes[0]
			s = Statement{Kind: "coil", Name: n.target}
			name, span = n.lhs, n.span
		default:
			continue
		}
		s.Line, s.Col, s.EndLine, s.EndCol = span.line, span.col, span.endLine, span.endCol
		s.NameLine, s.NameCol, s.NameEndCol = name.line, name.col, name.endCol
		s.Rest = strings.Join(strings.Fields(stripComments(sliceSpan(fileLines, name.endLine, name.endCol, span.endLine, span.endCol))), " ")
		out = append(out, s)
	}
	return out
}

// sliceSpan returns the source text between two 1-based positions (the end
// exclusive), clamped to the lines that exist.
func sliceSpan(lines []string, line, col, endLine, endCol int) string {
	if line < 1 || endLine < line || endLine > len(lines) {
		return ""
	}
	clamp := func(s string, c int) int { return max(0, min(c-1, len(s))) }
	if line == endLine {
		l := lines[line-1]
		a, b := clamp(l, col), clamp(l, endCol)
		if a >= b {
			return ""
		}
		return l[a:b]
	}
	var b strings.Builder
	b.WriteString(lines[line-1][clamp(lines[line-1], col):])
	for i := line; i < endLine-1; i++ {
		b.WriteString("\n" + lines[i])
	}
	last := lines[endLine-1]
	b.WriteString("\n" + last[:clamp(last, endCol)])
	return b.String()
}
