package sfc

import "strings"

// ParseOutline parses the SFC body's steps, transitions and action blocks
// for an editor outline. It is lenient where Parse is strict, because an
// outline has to keep working while the user types: the header is not
// parsed at all (Name and VarBlocks stay empty), an SFC block with no
// END_SFC runs to the end of the file, and an element that does not parse
// is skipped — the parser resumes at the next STEP / INITIAL_STEP /
// TRANSITION / ACTION keyword. Positions are 1-based in the whole file.
func ParseOutline(source string) *Program {
	prog := &Program{}
	lines := strings.Split(source, "\n")
	start, end := -1, len(lines)
	for i, l := range lines {
		if start == -1 && sfcStartRe.MatchString(l) {
			start = i
		} else if start != -1 && sfcEndRe.MatchString(l) {
			end = i
		}
	}
	if start == -1 {
		return prog
	}
	body := strings.Join(lines[start+1:end], "\n")
	p := &bodyParser{tokens: Lex(body, start+1), src: body}
	for p.peek().Type != TokenEOF {
		from := p.pos
		if err := p.parseBody(prog); err == nil {
			break
		}
		if p.pos <= from {
			p.pos = from + 1
		}
		for p.pos < len(p.tokens) && p.peek().Type != TokenEOF && !elementStart(p.peek().Type) {
			p.pos++
		}
	}
	return prog
}

func elementStart(t TokenType) bool {
	switch t {
	case TokenInitialStep, TokenStep, TokenTransition, TokenAction:
		return true
	}
	return false
}
