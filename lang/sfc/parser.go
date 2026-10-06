package sfc

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/joyautomation/nautilus/lang/st"
)

// Parse parses a full .sfc source file into an AST: the ST-standard header
// (PROGRAM name + VAR blocks) plus the SFC ... END_SFC body.
//
// The header is parsed by feeding it to st.Parse with a synthesized
// trailing END_PROGRAM — the real END_PROGRAM sits after END_SFC in the
// actual file, out of reach of a header-only parse. This reuses lang/st's
// complete declaration grammar (multi-name decls, arrays, structs, RETAIN/
// CONSTANT, initializers — everything st.Parse already knows) without
// forking it or modifying lang/st; see the package doc in sfc.go for why
// this is the chosen reuse seam. Because the header is a byte-for-byte
// prefix of the original file, st.Parse's line numbers land on the exact
// original file lines with no remapping needed.
func Parse(source string) (*Program, error) {
	header, body, _, bodyLine, err := splitSFC(source)
	if err != nil {
		return nil, err
	}
	headerProg, err := st.Parse(header + "\nEND_PROGRAM\n")
	if err != nil {
		return nil, fmt.Errorf("sfc: header: %w", err)
	}
	prog := &Program{
		Name:      headerProg.Name,
		VarBlocks: headerProg.VarBlocks,
		Pos:       headerPos(header),
	}
	p := &bodyParser{tokens: Lex(body, bodyLine), src: body}
	if err := p.parseBody(prog); err != nil {
		return nil, err
	}
	return prog, nil
}

var programLineRe = regexp.MustCompile(`(?im)^\s*PROGRAM\s+`)

// headerPos finds the PROGRAM keyword's line for a friendlier anchor on
// program-wide diagnostics (e.g. "no INITIAL_STEP"); Pos{1,1} if absent.
func headerPos(header string) Pos {
	lines := strings.Split(header, "\n")
	for i, l := range lines {
		if programLineRe.MatchString(l) {
			return Pos{Line: i + 1, Col: 1}
		}
	}
	return Pos{Line: 1, Col: 1}
}

// splitSFC separates source into the ST header (everything before the SFC
// line), the SFC body (between the SFC and END_SFC marker lines), and the
// footer (END_SFC onward). bodyLine is the 1-based file line of the SFC
// marker itself; adding it to a 1-based line computed relative to body
// recovers the original file line — the same convention lang/fbd's
// splitFBD uses for bodyLine.
func splitSFC(source string) (header, body, footer string, bodyLine int, err error) {
	lines := strings.Split(source, "\n")
	start, end := -1, -1
	for i, l := range lines {
		if start == -1 && sfcStartRe.MatchString(l) {
			start = i
		}
		if sfcEndRe.MatchString(l) {
			end = i
		}
	}
	if start == -1 || end == -1 || end < start {
		return "", "", "", 0, fmt.Errorf("sfc: source must contain an SFC ... END_SFC body")
	}
	header = strings.Join(lines[:start], "\n")
	body = strings.Join(lines[start+1:end], "\n")
	footer = strings.Join(lines[end+1:], "\n")
	return header, body, footer, start + 1, nil
}

// ─── body parser ────────────────────────────────────────────────────────────

// bodyParser parses the token stream produced by Lex over one .sfc file's
// SFC body into steps, transitions, and action blocks.
type bodyParser struct {
	tokens []Token
	pos    int
	src    string // the body text Lex tokenized; spans slice into this
}

func (p *bodyParser) peek() Token {
	if p.pos >= len(p.tokens) {
		return Token{Type: TokenEOF}
	}
	return p.tokens[p.pos]
}

func (p *bodyParser) advance() Token {
	tok := p.peek()
	if p.pos < len(p.tokens)-1 {
		p.pos++
	}
	return tok
}

func (p *bodyParser) expect(tt TokenType) (Token, error) {
	tok := p.peek()
	if tok.Type != tt {
		return tok, fmt.Errorf("sfc: line %d: unexpected %q", tok.Line, tok.Literal)
	}
	return p.advance(), nil
}

// spanBetween slices the verbatim body text from start's offset up to (not
// including) end's offset, trimmed of trailing whitespace.
func (p *bodyParser) spanBetween(start, end Token) Span {
	text := p.src[start.Offset:end.Offset]
	text = strings.TrimRight(text, " \t\r\n")
	return Span{Text: text, Line: start.Line, Col: start.Col}
}

func (p *bodyParser) parseBody(prog *Program) error {
	for p.peek().Type != TokenEOF {
		switch p.peek().Type {
		case TokenInitialStep, TokenStep:
			step, err := p.parseStep()
			if err != nil {
				return err
			}
			prog.Steps = append(prog.Steps, step)
		case TokenTransition:
			tr, err := p.parseTransition()
			if err != nil {
				return err
			}
			prog.Transitions = append(prog.Transitions, tr)
		case TokenAction:
			ab, err := p.parseAction()
			if err != nil {
				return err
			}
			prog.Actions = append(prog.Actions, ab)
		default:
			tok := p.peek()
			return fmt.Errorf("sfc: line %d: expected STEP, INITIAL_STEP, TRANSITION, or ACTION, got %q", tok.Line, tok.Literal)
		}
	}
	return nil
}

// parseStep parses `(INITIAL_STEP|STEP) name: { assoc } END_STEP`.
func (p *bodyParser) parseStep() (*Step, error) {
	kwTok := p.advance()
	initial := kwTok.Type == TokenInitialStep
	nameTok, err := p.expect(TokenIdent)
	if err != nil {
		return nil, fmt.Errorf("sfc: line %d: expected a step name after %s", kwTok.Line, kwTok.Literal)
	}
	step := &Step{Name: nameTok.Literal, Initial: initial, Pos: Pos{Line: kwTok.Line, Col: kwTok.Col}}
	if p.peek().Type == TokenLParen {
		if err := p.parseStepAttrs(step); err != nil {
			return nil, err
		}
	}
	if _, err := p.expect(TokenColon); err != nil {
		return nil, fmt.Errorf("sfc: line %d: step %q: expected ':'", kwTok.Line, nameTok.Literal)
	}
	for p.peek().Type != TokenEndStep {
		if p.peek().Type == TokenEOF {
			return nil, fmt.Errorf("sfc: line %d: step %q: missing END_STEP", kwTok.Line, step.Name)
		}
		assoc, err := p.parseAssoc()
		if err != nil {
			return nil, err
		}
		step.Actions = append(step.Actions, assoc)
	}
	endTok := p.advance()
	step.EndPos = Pos{Line: endTok.Line, Col: endTok.Col}
	return step, nil
}

// parseStepAttrs parses a step's attribute list, `( NAME := value { ,
// NAME := value } )`, between the step name and its colon:
//
//	STEP Fill (MAXTIME := T#30S, ERROR := FillOverrun):
//
// Names and values are kept raw (Check validates them), and the whole list
// is kept verbatim in Step.AttrText so a reprint is byte-exact.
func (p *bodyParser) parseStepAttrs(step *Step) error {
	open := p.advance() // '('
	parts, closeTok, err := p.parenList(open)
	if err != nil {
		return fmt.Errorf("sfc: line %d: step %q: %v", open.Line, step.Name, err)
	}
	step.AttrText = strings.TrimSpace(p.src[open.Offset+1 : closeTok.Offset])
	for _, part := range parts {
		if len(part) == 0 {
			continue
		}
		if len(part) < 3 || part[0].Type != TokenIdent || part[1].Type != TokenAssign {
			return fmt.Errorf("sfc: line %d: step %q: a step attribute is NAME := value, e.g. STEP %s (MAXTIME := T#30S):", part[0].Line, step.Name, step.Name)
		}
		val := strings.TrimSpace(p.src[part[2].Offset:p.tokEnd(part[len(part)-1])])
		step.Attrs = append(step.Attrs, StepAttr{
			Name: strings.ToUpper(part[0].Literal), Value: val,
			Pos: Pos{Line: part[0].Line, Col: part[0].Col},
		})
	}
	return nil
}

// parenList consumes tokens after an already-consumed '(' up to its matching
// ')', splitting them at top-level commas. It returns the comma-separated
// groups (each a token slice, possibly empty) and the closing ')' token,
// which it also consumes.
func (p *bodyParser) parenList(open Token) ([][]Token, Token, error) {
	var parts [][]Token
	var cur []Token
	depth := 1
	for {
		t := p.peek()
		switch t.Type {
		case TokenEOF:
			return nil, t, fmt.Errorf("unclosed '(' (opened at column %d)", open.Col)
		case TokenSemicolon, TokenColon:
			if depth == 1 {
				return nil, t, fmt.Errorf("expected ')' before %q", t.Literal)
			}
		case TokenLParen:
			depth++
		case TokenRParen:
			depth--
			if depth == 0 {
				p.advance()
				return append(parts, cur), t, nil
			}
		case TokenComma:
			if depth == 1 {
				parts = append(parts, cur)
				cur = nil
				p.advance()
				continue
			}
		}
		cur = append(cur, t)
		p.advance()
	}
}

// tokEnd is the body offset just past tok's text.
func (p *bodyParser) tokEnd(tok Token) int {
	if tok.Type == TokenString || tok.Type == TokenIdent || tok.Type == TokenAssign {
		return tok.Offset + len(tok.Literal)
	}
	return tok.Offset + 1
}

// assocForms is the one-line reminder both association parse errors end
// with: the two spellings an association can take.
const assocForms = "an association is `Q Target;` / `Q Target(T#3S);` (nautilus) or `Target(Q);` / `Target(Q, T#3S);` (IEC 61131-3), with Q one of N, S, R, P, P0, P1 or the timed L, D, SD, DS, SL"

// parseAssoc parses one action association, in either accepted spelling:
//
//	Q Target [(time)];        nautilus's form: D Detergent(T#3S);
//	Target([Q] [, time]);     IEC 61131-3's textual form: Detergent(D, T#3S);
//
// Both produce the same Assoc. In the IEC form an omitted qualifier is N
// (the standard's default), and a third entry — an indicator variable — is
// refused: indicators are not supported.
func (p *bodyParser) parseAssoc() (Assoc, error) {
	first, err := p.expect(TokenIdent)
	if err != nil {
		return Assoc{}, fmt.Errorf("sfc: line %d: expected an action association: %s", first.Line, assocForms)
	}
	pos := Pos{Line: first.Line, Col: first.Col}
	var a Assoc
	switch p.peek().Type {
	case TokenIdent:
		// nautilus form: qualifier target [(time)]
		targetTok := p.advance()
		a = Assoc{Qualifier: strings.ToUpper(first.Literal), Target: targetTok.Literal, Pos: pos}
		if p.peek().Type == TokenLParen {
			open := p.advance()
			parts, closeTok, err := p.parenList(open)
			if err != nil {
				return Assoc{}, fmt.Errorf("sfc: line %d: association %s %s: %v", a.Pos.Line, a.Qualifier, a.Target, err)
			}
			if len(parts) > 1 {
				return Assoc{}, fmt.Errorf("sfc: line %d: association %s %s: the parentheses take one duration, e.g. %s %s(T#3S); — %s", a.Pos.Line, a.Qualifier, a.Target, a.Qualifier, a.Target, assocForms)
			}
			a.Time = strings.TrimSpace(p.src[open.Offset+1 : closeTok.Offset])
		}
	case TokenLParen:
		// IEC form: target([qualifier] [, time] [, indicator...])
		open := p.advance()
		parts, closeTok, err := p.parenList(open)
		if err != nil {
			return Assoc{}, fmt.Errorf("sfc: line %d: association %s(...): %v", pos.Line, first.Literal, err)
		}
		a = Assoc{Qualifier: "N", Target: first.Literal, Pos: pos}
		if q := parts[0]; len(q) > 0 {
			if len(q) != 1 || q[0].Type != TokenIdent {
				raw := strings.TrimSpace(p.src[open.Offset+1 : closeTok.Offset])
				return Assoc{}, fmt.Errorf("sfc: line %d: association %s(%s): expected a qualifier first — %s", pos.Line, first.Literal, raw, assocForms)
			}
			a.Qualifier = strings.ToUpper(q[0].Literal)
		}
		if len(parts) > 1 {
			t := parts[1]
			if len(t) == 0 {
				return Assoc{}, fmt.Errorf("sfc: line %d: association %s(%s, ): expected a duration after the comma — %s", pos.Line, first.Literal, a.Qualifier, assocForms)
			}
			a.Time = strings.TrimSpace(p.src[t[0].Offset:p.tokEnd(t[len(t)-1])])
		}
		if len(parts) > 2 {
			return Assoc{}, fmt.Errorf("sfc: line %d: association %s: indicator variables (a third entry in the parentheses) are not supported — %s", pos.Line, first.Literal, assocForms)
		}
	default:
		return Assoc{}, fmt.Errorf("sfc: line %d: association starting %q: %s", first.Line, first.Literal, assocForms)
	}
	if _, err := p.expect(TokenSemicolon); err != nil {
		return Assoc{}, fmt.Errorf("sfc: line %d: association %s %s: expected ';'", a.Pos.Line, a.Qualifier, a.Target)
	}
	return a, nil
}

// parseTransition parses `TRANSITION [name] FROM step-set TO step-set :=
// <raw condition text> ; END_TRANSITION`.
func (p *bodyParser) parseTransition() (*Transition, error) {
	kwTok := p.advance()
	tr := &Transition{Pos: Pos{Line: kwTok.Line, Col: kwTok.Col}}
	if p.peek().Type == TokenIdent {
		tr.Name = p.advance().Literal
	}
	if _, err := p.expect(TokenFrom); err != nil {
		return nil, fmt.Errorf("sfc: line %d: transition %s: expected FROM", kwTok.Line, trName(tr))
	}
	from, err := p.parseStepSet()
	if err != nil {
		return nil, err
	}
	tr.From = from
	if _, err := p.expect(TokenTo); err != nil {
		return nil, fmt.Errorf("sfc: line %d: transition %s: expected TO", kwTok.Line, trName(tr))
	}
	to, err := p.parseStepSet()
	if err != nil {
		return nil, err
	}
	tr.To = to
	if _, err := p.expect(TokenAssign); err != nil {
		return nil, fmt.Errorf("sfc: line %d: transition %s: expected ':='", kwTok.Line, trName(tr))
	}
	startTok := p.peek()
	for p.peek().Type != TokenSemicolon {
		if p.peek().Type == TokenEOF {
			return nil, fmt.Errorf("sfc: line %d: transition %s: missing terminating ';'", kwTok.Line, trName(tr))
		}
		p.advance()
	}
	semiTok := p.peek()
	tr.Cond = p.spanBetween(startTok, semiTok)
	p.advance() // consume ';'
	endTok, err := p.expect(TokenEndTransition)
	if err != nil {
		return nil, fmt.Errorf("sfc: line %d: transition %s: expected END_TRANSITION", semiTok.Line, trName(tr))
	}
	tr.EndPos = Pos{Line: endTok.Line, Col: endTok.Col}
	return tr, nil
}

// parseStepSet parses `ident | "(" ident { "," ident } ")"`.
func (p *bodyParser) parseStepSet() ([]string, error) {
	if p.peek().Type == TokenLParen {
		p.advance()
		var names []string
		for {
			idTok, err := p.expect(TokenIdent)
			if err != nil {
				return nil, fmt.Errorf("sfc: line %d: expected a step name in a step-set", idTok.Line)
			}
			names = append(names, idTok.Literal)
			if p.peek().Type == TokenComma {
				p.advance()
				continue
			}
			break
		}
		if _, err := p.expect(TokenRParen); err != nil {
			return nil, fmt.Errorf("sfc: line %d: step-set: expected ')'", p.peek().Line)
		}
		return names, nil
	}
	idTok, err := p.expect(TokenIdent)
	if err != nil {
		return nil, fmt.Errorf("sfc: line %d: expected a step name or '(' to start a step-set", idTok.Line)
	}
	return []string{idTok.Literal}, nil
}

// parseAction parses `ACTION name: <raw statement-list text> END_ACTION`.
func (p *bodyParser) parseAction() (*ActionBlock, error) {
	kwTok := p.advance()
	nameTok, err := p.expect(TokenIdent)
	if err != nil {
		return nil, fmt.Errorf("sfc: line %d: expected an action name after ACTION", kwTok.Line)
	}
	if _, err := p.expect(TokenColon); err != nil {
		return nil, fmt.Errorf("sfc: line %d: action %q: expected ':'", kwTok.Line, nameTok.Literal)
	}
	ab := &ActionBlock{Name: nameTok.Literal, Pos: Pos{Line: kwTok.Line, Col: kwTok.Col}}
	startTok := p.peek()
	for p.peek().Type != TokenEndAction {
		if p.peek().Type == TokenEOF {
			return nil, fmt.Errorf("sfc: line %d: action %q: missing END_ACTION", kwTok.Line, ab.Name)
		}
		p.advance()
	}
	endTok := p.peek()
	ab.Body = p.spanBetween(startTok, endTok)
	p.advance() // consume END_ACTION
	ab.EndPos = Pos{Line: endTok.Line, Col: endTok.Col}
	return ab, nil
}

// trName renders a transition's diagnostic label: its declared name, or a
// line-anchored placeholder for the (legal) unnamed case.
func trName(t *Transition) string {
	if t.Name != "" {
		return t.Name
	}
	return fmt.Sprintf("(unnamed, line %d)", t.Pos.Line)
}
