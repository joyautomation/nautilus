// Package sfc compiles IEC 61131-3 Sequential Function Chart programs
// written in nautilus's textual `.sfc` form — a POU whose body is a
// standard-textual SFC block using the IEC keywords verbatim:
//
//	PROGRAM TankBatch
//	VAR_EXTERNAL Start : BOOL; ... END_VAR
//	SFC
//	  INITIAL_STEP Idle:
//	    N RunLamp;
//	  END_STEP
//	  STEP Fill:
//	    N FillValve;
//	  END_STEP
//	  TRANSITION t_start FROM Idle TO Fill := Start;
//	  END_TRANSITION
//	  ACTION Stir:
//	    Mixer := TRUE;
//	  END_ACTION
//	END_SFC
//	END_PROGRAM
//
// See docs/design/sfc.md for the full grammar (§1), semantics (§2), and
// compilation strategy (§3). Like FBD and LD, SFC transpiles toward ST and
// reuses st.Parse/st.Lower — it is a THIRD, independent front-end (not a
// stage in the LD→FBD chain): step activity, timers, and qualifier state
// are ordinary retained VAR slots, so no VM or runtime-core change is
// needed (§3).
//
// This file holds the language-wiring surface every other package calls
// through (HasBlock for runtime.Language; Compile/Transpile/
// TranspileWithLines for the compile and check pipelines) — the same role
// lang/fbd/fbd.go and lang/ld/ld.go play for their languages. The parser,
// AST, and structural checks live in parser.go/ast.go/lexer.go/check.go.
// The diagram render model and structural edit ops (§4) live in graph.go
// and edit.go respectively — this file keeps only their shared wire types
// (TextEdit, EditOp, LayoutOpEntry).
package sfc

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/lang/st"
)

var (
	sfcStartRe = regexp.MustCompile(`(?i)^\s*SFC\s*$`)
	sfcEndRe   = regexp.MustCompile(`(?i)^\s*END_SFC\s*$`)
)

// HasBlock reports whether src contains an SFC ... END_SFC body — the same
// line-based detection ld.HasBlock/the FBD block regex use for their
// languages, and what runtime.Language uses to route a .sfc source.
func HasBlock(src string) bool {
	for _, line := range strings.Split(src, "\n") {
		if sfcStartRe.MatchString(line) {
			return true
		}
	}
	return false
}

// Compile parses and structurally checks .sfc source, transpiles it to ST
// (design doc §3), then lowers it to an ir.Program via the ST front-end —
// the same shape as fbd.Compile.
func Compile(src string) (*ir.Program, error) {
	prog, err := Parse(src)
	if err != nil {
		return nil, err
	}
	for _, d := range Check(prog) {
		if d.Severity == SeverityError {
			return nil, fmt.Errorf("sfc: %s", d)
		}
	}
	stSrc, err := Transpile(src)
	if err != nil {
		return nil, err
	}
	stProg, err := st.Parse(stSrc)
	if err != nil {
		return nil, err
	}
	return st.Lower(stProg)
}

// Transpile converts .sfc source to equivalent ST source (design doc §3):
// one retained _S_<Step>_X BOOL per step, edge-driven transition firing,
// and action qualifiers lowered per §3.2.
func Transpile(src string) (string, error) {
	stSrc, _, err := TranspileWithLines(src)
	return stSrc, err
}

// TranspileWithLines is Transpile plus a line map (the lineMap[i] convention
// of fbd.TranspileWithLines / ld.TranspileWithLines).
func TranspileWithLines(src string) (string, []int, error) {
	if !HasBlock(src) {
		return "", nil, fmt.Errorf("sfc: source has no SFC ... END_SFC body")
	}
	prog, err := Parse(src)
	if err != nil {
		return "", nil, err
	}
	return transpileProgram(prog, src)
}

// Graph emits the SFC diagram render model (design doc §4.1). See graph.go.
//
// ApplyEdit resolves an EditOp against src and returns the minimal
// TextEdits realizing it — the identical contract to fbd.ApplyEdit /
// ld.ApplyEdit. See edit.go for the op set (design doc §4.2) and printers.

// TextEdit mirrors fbd.TextEdit/ld.TextEdit exactly (design doc §4.2: "the
// identical contract to fbd.ApplyEdit"): 1-based positions, end exclusive.
type TextEdit struct {
	Line    int    `json:"line"`
	Col     int    `json:"col"`
	EndLine int    `json:"endLine"`
	EndCol  int    `json:"endCol"`
	NewText string `json:"newText"`
}

// EditOp is one structural edit (design doc §4.2), addressed by the render
// model's stable ids (st:<step>, tr:<transition>, ac:<action>, cm:<n>).
// Resolution and printers live in edit.go; this is the wire shape shared
// with the CLI (`naut sfc edit`) and, eventually, the diagram webview.
//
//	addStep                  Name, Initial, After (existing step id, optional)
//	deleteStep               Step
//	renameStep               Step, NewName
//	addTransition            Name (optional), From, To, Cond, After (optional)
//	deleteTransition         Transition
//	setCondition             Transition, Cond
//	setTransitionEnds        Transition, From (optional, keeps current if omitted), To (optional, keeps current if omitted) — re-point a transition's FROM/TO step-sets, e.g. to fix a dangling reference left by deleteStep or an orphan chip's retarget popover. Names are validated as identifiers only; they need NOT already exist (never-block: Check flags an unknown step, ApplyEdit does not refuse it).
//	addAssoc                 Step, Qualifier, Target, Time (optional), Index (optional, append if omitted/out of range)
//	setAssoc                 Step, Index, Qualifier, Target, Time (optional)
//	deleteAssoc              Step, Index
//	setActionBody            Action (name; created if it doesn't exist yet), Body
//	insertAlternativeBranch  From (or After to inherit its source), To, Cond, Name (optional), After (optional, priority placement)
//	insertSimultaneousBranch Transition, NewStep (created; appended to the transition's TO set)
//	setLayout                Node, X, Y — or Entries for a batched multi-node drag
//	clearLayout              Node (one entry) or nothing (whole block)
//	setComment               Comment (index into Model.Comments), Text ("" deletes)
//	addComment               Text — appends a new note just above END_SFC
//	deleteComment            Comment (index into Model.Comments)
//	declareVar / deleteVar   Name, VarType, Section (the vars panel; lang/ld's shape)
//	pasteSteps               Steps (+ Trans between them) — clipboard paste; names freshened
//	deleteSelection          Nodes (st:/tr: ids) — one op for a multi-selection or a cut
type EditOp struct {
	Type string `json:"type"`

	// step-addressed ops
	Step    string `json:"step,omitempty"`
	Name    string `json:"name,omitempty"` // new step/transition name (create ops)
	NewName string `json:"newName,omitempty"`
	Initial bool   `json:"initial,omitempty"`
	After   string `json:"after,omitempty"` // insertion/priority anchor: an existing element id of the same kind

	// transition-addressed ops
	Transition string   `json:"transition,omitempty"`
	From       []string `json:"from,omitempty"`
	To         []string `json:"to,omitempty"`
	Cond       string   `json:"cond,omitempty"`

	// association ops (within Step)
	Index     int    `json:"index,omitempty"`
	Qualifier string `json:"qualifier,omitempty"`
	Target    string `json:"target,omitempty"`
	Time      string `json:"time,omitempty"`

	// action ops
	Action string `json:"action,omitempty"`
	Body   string `json:"body,omitempty"`

	// branch ops
	NewStep string `json:"newStep,omitempty"`

	// layout ops (mirrors fbd.EditOp)
	Node    string          `json:"node,omitempty"`
	X       *int            `json:"x,omitempty"`
	Y       *int            `json:"y,omitempty"`
	Entries []LayoutOpEntry `json:"entries,omitempty"`

	// comment ops
	Comment *int   `json:"comment,omitempty"`
	Text    string `json:"text,omitempty"`

	// pasteSteps: the clipboard's steps (with their associations and an
	// optional pinned position) and the transitions wholly between them.
	Steps []PasteStep  `json:"steps,omitempty"`
	Trans []PasteTrans `json:"trans,omitempty"`
	// deleteSelection: st:/tr: ids, resolved against ONE parse.
	Nodes []string `json:"nodes,omitempty"`

	// declareVar / deleteVar (Name is the variable) — the same payload
	// shape as lang/ld's, so the shared vars panel posts one form.
	VarType string `json:"varType,omitempty"`
	Section string `json:"section,omitempty"`

	// Pou names the PROGRAM a blank file is seeded with (see ApplyEdit).
	Pou string `json:"pou,omitempty"`
}

// LayoutOpEntry is one node's pinned position in a batched setLayout — the
// same shape as fbd.LayoutOpEntry.
type LayoutOpEntry struct {
	Node string `json:"node"`
	X    int    `json:"x"`
	Y    int    `json:"y"`
}

// PasteStep is one copied step: its (pre-paste) name, associations, and
// where to pin the copy (both X and Y, or neither → auto-layout).
type PasteStep struct {
	Name    string   `json:"name"`
	Actions []GAssoc `json:"actions,omitempty"`
	X       *int     `json:"x,omitempty"`
	Y       *int     `json:"y,omitempty"`
}

// PasteTrans is one copied transition; From/To name copied steps by their
// pre-paste names.
type PasteTrans struct {
	Name string   `json:"name,omitempty"`
	From []string `json:"from"`
	To   []string `json:"to"`
	Cond string   `json:"cond"`
}
