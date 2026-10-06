package sfc

import (
	"strings"

	"github.com/joyautomation/nautilus/lang/st"
)

// Pos is a 1-based source location, field-compatible with st.Pos so values
// move between the two packages without conversion boilerplate (see
// Check's use of st.Pos-shaped literals in the LSP bridge).
type Pos struct {
	Line int
	Col  int
}

// Node is implemented by every SFC AST node that carries a source position.
type Node interface {
	NodePos() Pos
}

// Program is a parsed .sfc file: the ST-standard header — PROGRAM name plus
// VAR/VAR_INPUT/.../VAR_EXTERNAL blocks — and the SFC body's steps,
// transitions, and action blocks. The header is parsed by reusing
// lang/st's own declaration grammar verbatim (see Parse), so VarBlocks is
// the exact st.VarBlock/st.VarDecl shape the rest of the toolchain already
// knows how to render, hover, and complete against.
type Program struct {
	Name      string
	VarBlocks []st.VarBlock

	Steps       []*Step
	Transitions []*Transition
	Actions     []*ActionBlock

	Pos Pos // the header's PROGRAM keyword, best-effort (line 1 if not found)
}

func (p *Program) NodePos() Pos { return p.Pos }

// Step is an INITIAL_STEP or STEP ... END_STEP element.
type Step struct {
	Name    string
	Initial bool
	Actions []Assoc
	// Attrs is the optional step attribute list written between the name and
	// the colon — `STEP Fill (MAXTIME := T#30S, ERROR := FillOverrun):` —
	// in source order. AttrText is that list verbatim (without the
	// parentheses) so an edit op that reprints the step keeps it byte-exact,
	// including any attribute Check rejects. Empty when the step has none.
	Attrs    []StepAttr
	AttrText string
	Pos      Pos // the INITIAL_STEP/STEP keyword
	EndPos   Pos // the matching END_STEP
}

// StepAttr is one `NAME := value` entry of a step's attribute list. Name is
// upper-cased; Value is the raw text after ":=" (trimmed). Check validates
// both: MAXTIME takes a TIME literal, ERROR a declared BOOL variable.
type StepAttr struct {
	Name  string
	Value string
	Pos   Pos
}

// Attr returns the value of the named attribute (case-insensitive), "" if
// the step does not carry it.
func (s *Step) Attr(name string) string {
	for _, a := range s.Attrs {
		if strings.EqualFold(a.Name, name) {
			return a.Value
		}
	}
	return ""
}

// MaxTime is the step's supervision limit (the MAXTIME attribute's raw TIME
// literal), "" when the step is not supervised.
func (s *Step) MaxTime() string { return s.Attr("MAXTIME") }

func (s *Step) NodePos() Pos { return s.Pos }

// Assoc is one action association inside a step, in either of the two
// accepted spellings: nautilus's `qualifier target [(time)];` or the IEC
// textual form `target(qualifier[, time]);` — both parse to the same Assoc.
// Target names either an ACTION block or a plain declared variable (a
// Boolean-variable action, §1.1/§2.5 of the design doc).
type Assoc struct {
	Qualifier string // as written, upper-cased: "N", "S", "R", "P", "P0", "P1", "L", "D", "SD", "DS", "SL", or an unknown one
	Target    string
	Time      string // raw text between "(" ")", "" if absent
	Pos       Pos
}

func (a Assoc) NodePos() Pos { return a.Pos }

// Transition is `TRANSITION [name] FROM step-set TO step-set := cond;
// END_TRANSITION`. Cond is captured as a verbatim text Span — an arbitrary
// ST boolean expression — not parsed here (see Span).
type Transition struct {
	Name   string // "" if the transition is unnamed
	From   []string
	To     []string
	Cond   Span
	Pos    Pos // the TRANSITION keyword
	EndPos Pos // the matching END_TRANSITION
}

func (t *Transition) NodePos() Pos { return t.Pos }

// ActionBlock is `ACTION name: <ST statement list> END_ACTION`. Body is
// captured as a verbatim text Span — an arbitrary ST statement list — not
// parsed here (see Span).
type ActionBlock struct {
	Name   string
	Body   Span
	Pos    Pos // the ACTION keyword
	EndPos Pos // the matching END_ACTION
}

func (a *ActionBlock) NodePos() Pos { return a.Pos }

// Span is a verbatim slice of .sfc source text captured for a later ST
// parse (a transition condition or an action body) — Slice B's transpiler
// re-lexes Text with full ST knowledge and uses Line/Col to project its own
// diagnostics back onto this file. The text runs from the first non-trivia
// character after ":=" (or the action name's ":") up to — but excluding —
// the terminating ";" (conditions) or the END_ACTION keyword (action
// bodies), trimmed of trailing whitespace. It may span multiple lines;
// Line/Col mark only its first character.
type Span struct {
	Text string
	Line int
	Col  int
}
