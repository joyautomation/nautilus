package sfc

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/joyautomation/nautilus/lang/internal/hdrvars"
	"github.com/joyautomation/nautilus/lang/internal/seed"
)

// Structural edits for the SFC diagram, mirroring lang/ld's posture more
// than lang/fbd's (design doc §4.2): chart structure is canonical in the
// text, so an op resolves against a FRESH Graph, mutates the small Model
// value it gets back, and the touched step/transition/action block is
// reprinted CANONICALLY and replaced whole — the same "rung rewrite IS the
// edit" trick lang/ld/edit.go uses for rungs. Untouched regions of the file
// never move.
//
// Never-block principle (mirrors fbd/ld editparity): an op is refused only
// when its RESULT would fail to parse, or a request is unresolvable/
// ambiguous. A structurally valid but semantically incomplete result (a new
// step with no transitions yet, an empty transition condition, a dangling
// FROM/TO left by deleteStep) is not an editing error — `naut sfc
// check` surfaces it as a diagnostic breadcrumb, exactly like typing the
// same hole directly into the text.

var sfcIdentRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ApplyEdit resolves op against a fresh parse of src and returns the
// minimal text edits realizing it — the identical contract to
// fbd.ApplyEdit/ld.ApplyEdit.
//
// Blank source (a 0-byte new file) seeds: the op applies to a PROGRAM
// skeleton named op.Pou, and the result replaces the file in one edit.
func ApplyEdit(src string, op EditOp) ([]TextEdit, error) {
	if seed.Blank(src) {
		return seedEdit(src, op)
	}
	if op.Type == "init" {
		return nil, nil // already a POU — nothing to seed
	}
	m, err := Graph(src)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(src, "\n")

	var edits []TextEdit
	switch op.Type {
	case "addStep":
		edits, err = opAddStep(lines, m, op)
	case "deleteStep":
		edits, err = opDeleteStep(lines, m, op)
	case "renameStep":
		edits, err = opRenameStep(lines, m, op)
	case "addTransition":
		edits, err = opAddTransition(lines, m, op)
	case "deleteTransition":
		edits, err = opDeleteTransition(lines, m, op)
	case "setCondition":
		edits, err = opSetCondition(m, op)
	case "setTransitionEnds":
		edits, err = opSetTransitionEnds(m, op)
	case "addAssoc":
		edits, err = opAddAssoc(m, op)
	case "setAssoc":
		edits, err = opSetAssoc(m, op)
	case "deleteAssoc":
		edits, err = opDeleteAssoc(m, op)
	case "setActionBody":
		edits, err = opSetActionBody(lines, m, op)
	case "insertAlternativeBranch":
		edits, err = opInsertAlternativeBranch(lines, m, op)
	case "insertSimultaneousBranch":
		edits, err = opInsertSimultaneousBranch(lines, m, op)
	case "setLayout":
		edits, err = opSetLayout(lines, m, op)
	case "clearLayout":
		edits, err = opClearLayout(lines, m, op)
	case "setComment":
		edits, err = opSetComment(lines, m, op)
	case "addComment":
		edits, err = opAddComment(lines, m, op)
	case "deleteComment":
		edits, err = opDeleteComment(lines, m, op)
	case "pasteSteps":
		edits, err = opPasteSteps(src, lines, m, op)
	case "deleteSelection":
		edits, err = opDeleteSelection(src, lines, m, op)
	case "declareVar":
		edits, err = opDeclareVar(lines, m, op)
	case "deleteVar":
		edits, err = opDeleteVar(lines, m, op)
	default:
		return nil, fmt.Errorf("sfc edit: unknown op %q", op.Type)
	}
	if err != nil {
		return nil, err
	}
	if len(edits) == 0 {
		return edits, nil
	}
	// Never-block gate: apply the edits to a scratch copy and confirm the
	// result still parses before handing it back — the same guarantee
	// ld.ApplyEdit enforces per rung, generalized to the whole file since
	// SFC ops can touch more than one block (rename, simultaneous branch).
	result := applyTextEdits(src, edits)
	if _, perr := Parse(result); perr != nil {
		return nil, fmt.Errorf("sfc edit: refused — the result would not parse: %w", perr)
	}
	return edits, nil
}

// ── lookups ──────────────────────────────────────────────────────────────

func findStep(m *Model, id string) (*GStep, error) {
	for i := range m.Steps {
		if m.Steps[i].ID == id || m.Steps[i].Name == id {
			return &m.Steps[i], nil
		}
	}
	return nil, fmt.Errorf("sfc edit: no step %q", id)
}

func findTransition(m *Model, id string) (*GTransition, error) {
	for i := range m.Trans {
		if m.Trans[i].ID == id || (m.Trans[i].Name != "" && m.Trans[i].Name == id) {
			return &m.Trans[i], nil
		}
	}
	return nil, fmt.Errorf("sfc edit: no transition %q", id)
}

func findAction(m *Model, id string) (*GAction, error) {
	for i := range m.Actions {
		if m.Actions[i].ID == id || m.Actions[i].Name == id {
			return &m.Actions[i], nil
		}
	}
	return nil, fmt.Errorf("sfc edit: no action %q", id)
}

// ── canonical printers ───────────────────────────────────────────────────
// These reproduce the §1.2 style (2-space indent for elements, 4-space for
// their contents) so an op's rewrite of the block it touches reads exactly
// like hand-written source; untouched blocks are never reformatted.

func printStep(s *GStep) string {
	kw := "STEP"
	if s.Initial {
		kw = "INITIAL_STEP"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "  %s %s:\n", kw, s.Name)
	for _, a := range s.Actions {
		b.WriteString("    " + printAssoc(a) + "\n")
	}
	b.WriteString("  END_STEP\n")
	return b.String()
}

func printAssoc(a GAssoc) string {
	if a.Time != "" {
		return fmt.Sprintf("%s %s(%s);", a.Qualifier, a.Target, a.Time)
	}
	return fmt.Sprintf("%s %s;", a.Qualifier, a.Target)
}

func printStepSet(names []string) string {
	if len(names) == 1 {
		return names[0]
	}
	return "(" + strings.Join(names, ", ") + ")"
}

func printTransition(t *GTransition) string {
	var b strings.Builder
	b.WriteString("  TRANSITION")
	if t.Name != "" {
		b.WriteString(" " + t.Name)
	}
	fmt.Fprintf(&b, " FROM %s TO %s := %s;\n", printStepSet(t.From), printStepSet(t.To), t.Cond)
	b.WriteString("  END_TRANSITION\n")
	return b.String()
}

func printAction(a *GAction) string {
	var b strings.Builder
	fmt.Fprintf(&b, "  ACTION %s:\n", a.Name)
	body := strings.TrimRight(a.Body, " \t\r\n")
	if body != "" {
		for _, line := range strings.Split(body, "\n") {
			b.WriteString("    " + strings.TrimSpace(line) + "\n")
		}
	}
	b.WriteString("  END_ACTION\n")
	return b.String()
}

// ── validation helpers ───────────────────────────────────────────────────

func validCond(text string) error {
	if strings.Contains(text, ";") {
		return fmt.Errorf("sfc edit: a transition condition can't contain ';'")
	}
	return nil
}

func validQualifierTargetTime(q, target, tm string) error {
	if !sfcIdentRe.MatchString(q) {
		return fmt.Errorf("sfc edit: %q is not a valid action qualifier token", q)
	}
	if !sfcIdentRe.MatchString(target) {
		return fmt.Errorf("sfc edit: %q is not a valid action/variable name", target)
	}
	if tm != "" && strings.ContainsAny(tm, "();") {
		return fmt.Errorf("sfc edit: %q is not a valid time literal", tm)
	}
	return nil
}

// ── insertion placement ──────────────────────────────────────────────────

// insertionLine computes the 1-based line at which new text of `kind`
// ("step"|"transition"|"action") should be inserted: right after the named
// anchor (after, an existing element of the SAME kind) when given, else
// right after the last existing element of that kind, else a default
// anchored to the chart's section ordering (steps, then transitions, then
// actions, then END_SFC) — the grouping docs/design/sfc.md's own worked
// example uses. The insertion is "before this line" (an empty-range
// TextEdit at Col 1 of the returned line).
func insertionLine(lines []string, m *Model, after, kind string) (int, error) {
	switch kind {
	case "step":
		if after != "" {
			s, err := findStep(m, after)
			if err != nil {
				return 0, err
			}
			return s.EndLine + 1, nil
		}
		if n := len(m.Steps); n > 0 {
			return m.Steps[n-1].EndLine + 1, nil
		}
		if l, ok := lineAfter(lines, sfcStartRe); ok {
			return l, nil
		}
	case "transition":
		if after != "" {
			t, err := findTransition(m, after)
			if err != nil {
				return 0, err
			}
			return t.EndLine + 1, nil
		}
		if n := len(m.Trans); n > 0 {
			return m.Trans[n-1].EndLine + 1, nil
		}
		if n := len(m.Steps); n > 0 {
			return m.Steps[n-1].EndLine + 1, nil
		}
	case "action":
		if after != "" {
			a, err := findAction(m, after)
			if err != nil {
				return 0, err
			}
			return a.EndLine + 1, nil
		}
		if n := len(m.Actions); n > 0 {
			return m.Actions[n-1].EndLine + 1, nil
		}
		if n := len(m.Trans); n > 0 {
			return m.Trans[n-1].EndLine + 1, nil
		}
		if n := len(m.Steps); n > 0 {
			return m.Steps[n-1].EndLine + 1, nil
		}
	}
	for i, l := range lines {
		if sfcEndRe.MatchString(l) {
			return i + 1, nil
		}
	}
	return 0, fmt.Errorf("sfc edit: no END_SFC to anchor the insertion")
}

func lineAfter(lines []string, re *regexp.Regexp) (int, bool) {
	for i, l := range lines {
		if re.MatchString(l) {
			return i + 2, true
		}
	}
	return 0, false
}

// blockDeleteEdit removes the whole-line span [start, endLine] (1-based,
// inclusive) — the inverse of an add* op's "\n" + printX(...) insertion,
// which always prefixes a blank separator line before the new block (§ the
// insertion helpers above). Without consuming that same blank line back on
// delete, an add-then-delete round trip leaves a stray blank line behind
// (worse, deleting an element from the MIDDLE of a blank-separated list
// leaves the survivors double-blank-separated — the two neighbors' own
// separator blanks, once adjacent, never collapse to one). So: if the line
// immediately above `start` is blank, the deletion consumes it too — one
// line at most, so a deliberate double-blank a user typed by hand for extra
// emphasis still loses only the single separator this block owns.
func blockDeleteEdit(lines []string, start, endLine int) TextEdit {
	from := start
	if start-2 >= 0 && start-2 < len(lines) && strings.TrimSpace(lines[start-2]) == "" {
		from = start - 1
	}
	return TextEdit{Line: from, Col: 1, EndLine: endLine + 1, EndCol: 1}
}

// ── addStep / deleteStep / renameStep ───────────────────────────────────

func opAddStep(lines []string, m *Model, op EditOp) ([]TextEdit, error) {
	name := strings.TrimSpace(op.Name)
	if !sfcIdentRe.MatchString(name) {
		return nil, fmt.Errorf("sfc edit: %q is not a valid step name", name)
	}
	if _, err := findStep(m, stepID(name)); err == nil {
		return nil, fmt.Errorf("sfc edit: a step named %q already exists", name)
	}
	if op.Initial {
		for _, s := range m.Steps {
			if s.Initial {
				return nil, fmt.Errorf("sfc edit: chart already has an INITIAL_STEP %q — a chart requires exactly one", s.Name)
			}
		}
	}
	at, err := insertionLine(lines, m, op.After, "step")
	if err != nil {
		return nil, err
	}
	ns := &GStep{Name: name, Initial: op.Initial}
	return []TextEdit{{Line: at, Col: 1, EndLine: at, EndCol: 1, NewText: "\n" + printStep(ns)}}, nil
}

func opDeleteStep(lines []string, m *Model, op EditOp) ([]TextEdit, error) {
	s, err := findStep(m, op.Step)
	if err != nil {
		return nil, err
	}
	// References the step still holds — FROM/TO members, GStep.X/.T reads in
	// condition/action spans — become "unknown step" diagnostics (Check),
	// deliberately left in place: the FBD/LD editparity breadcrumb
	// philosophy, not a cascade delete.
	edits := []TextEdit{blockDeleteEdit(lines, s.Line, s.EndLine)}
	edits = append(edits, remapLayout(lines, m, idDrop(s.ID))...)
	return edits, nil
}

func opRenameStep(lines []string, m *Model, op EditOp) ([]TextEdit, error) {
	s, err := findStep(m, op.Step)
	if err != nil {
		return nil, err
	}
	newName := strings.TrimSpace(op.NewName)
	if !sfcIdentRe.MatchString(newName) {
		return nil, fmt.Errorf("sfc edit: %q is not a valid step name", newName)
	}
	if _, err := findStep(m, stepID(newName)); err == nil {
		return nil, fmt.Errorf("sfc edit: a step named %q already exists", newName)
	}
	old := s.Name

	// Structural refs only (design doc §4.2 lists "fix FROM/TO references";
	// it is silent on rewriting GStep.X/GStep.T inside condition/action-body
	// ST spans, which are Slice B's verbatim-text territory) — leaving them
	// is the breadcrumb philosophy again: Check flags OldName.X/.T as an
	// unknown-step reference after the rename, guiding a manual fix instead
	// of an edit op guessing at ST-expression surgery.
	edits := []TextEdit{{
		Line: s.Line, Col: 1, EndLine: s.Line + 1, EndCol: 1,
		NewText: fmt.Sprintf("  %s %s:\n", stepKeyword(s), newName),
	}}
	for i := range m.Trans {
		t := &m.Trans[i]
		touched := false
		for j, n := range t.From {
			if strings.EqualFold(n, old) {
				t.From[j] = newName
				touched = true
			}
		}
		for j, n := range t.To {
			if strings.EqualFold(n, old) {
				t.To[j] = newName
				touched = true
			}
		}
		if touched {
			edits = append(edits, TextEdit{Line: t.Line, Col: 1, EndLine: t.EndLine + 1, EndCol: 1, NewText: printTransition(t)})
		}
	}
	edits = append(edits, remapLayout(lines, m, idRewrite(stepID(old), stepID(newName)))...)
	return edits, nil
}

func stepKeyword(s *GStep) string {
	if s.Initial {
		return "INITIAL_STEP"
	}
	return "STEP"
}

// ── addTransition / deleteTransition / setCondition ─────────────────────

func opAddTransition(lines []string, m *Model, op EditOp) ([]TextEdit, error) {
	if len(op.From) == 0 || len(op.To) == 0 {
		return nil, fmt.Errorf("sfc edit: a transition needs FROM and TO step-sets")
	}
	for _, n := range append(append([]string{}, op.From...), op.To...) {
		if !sfcIdentRe.MatchString(n) {
			return nil, fmt.Errorf("sfc edit: %q is not a valid step name", n)
		}
	}
	name := strings.TrimSpace(op.Name)
	if name != "" {
		if !sfcIdentRe.MatchString(name) {
			return nil, fmt.Errorf("sfc edit: %q is not a valid transition name", name)
		}
		if _, err := findTransition(m, "tr:"+name); err == nil {
			return nil, fmt.Errorf("sfc edit: a transition named %q already exists", name)
		}
	}
	cond := strings.TrimSpace(op.Cond)
	if err := validCond(cond); err != nil {
		return nil, err
	}
	at, err := insertionLine(lines, m, op.After, "transition")
	if err != nil {
		return nil, err
	}
	nt := &GTransition{Name: name, From: op.From, To: op.To, Cond: cond}
	return []TextEdit{{Line: at, Col: 1, EndLine: at, EndCol: 1, NewText: "\n" + printTransition(nt)}}, nil
}

func opDeleteTransition(lines []string, m *Model, op EditOp) ([]TextEdit, error) {
	t, err := findTransition(m, op.Transition)
	if err != nil {
		return nil, err
	}
	edits := []TextEdit{blockDeleteEdit(lines, t.Line, t.EndLine)}
	edits = append(edits, remapLayout(lines, m, idDrop(t.ID))...)
	return edits, nil
}

func opSetCondition(m *Model, op EditOp) ([]TextEdit, error) {
	t, err := findTransition(m, op.Transition)
	if err != nil {
		return nil, err
	}
	cond := strings.TrimSpace(op.Cond)
	if err := validCond(cond); err != nil {
		return nil, err
	}
	t.Cond = cond
	return []TextEdit{{Line: t.Line, Col: 1, EndLine: t.EndLine + 1, EndCol: 1, NewText: printTransition(t)}}, nil
}

// opSetTransitionEnds re-points a transition's FROM and/or TO step-sets —
// the retarget gesture on an orphaned-transition chip (a dangling FROM/TO
// left by deleteStep, or simply a typo'd name), and usable for any
// transition. From/To each default to the transition's CURRENT set when
// omitted (an empty/absent JSON array), so a caller can retarget just one
// side. Deliberately NOT stricter than typing the names directly into the
// text: targets are validated as identifiers only — they need not already
// name an existing step (never-block; Check flags an unknown step).
func opSetTransitionEnds(m *Model, op EditOp) ([]TextEdit, error) {
	t, err := findTransition(m, op.Transition)
	if err != nil {
		return nil, err
	}
	from := t.From
	if len(op.From) > 0 {
		from = op.From
	}
	to := t.To
	if len(op.To) > 0 {
		to = op.To
	}
	if len(from) == 0 || len(to) == 0 {
		return nil, fmt.Errorf("sfc edit: setTransitionEnds needs a non-empty FROM and TO")
	}
	for _, n := range append(append([]string{}, from...), to...) {
		if !sfcIdentRe.MatchString(n) {
			return nil, fmt.Errorf("sfc edit: %q is not a valid step name", n)
		}
	}
	t.From = append([]string{}, from...)
	t.To = append([]string{}, to...)
	return []TextEdit{{Line: t.Line, Col: 1, EndLine: t.EndLine + 1, EndCol: 1, NewText: printTransition(t)}}, nil
}

// ── addAssoc / setAssoc / deleteAssoc ───────────────────────────────────

func opAddAssoc(m *Model, op EditOp) ([]TextEdit, error) {
	s, err := findStep(m, op.Step)
	if err != nil {
		return nil, err
	}
	q := strings.ToUpper(strings.TrimSpace(op.Qualifier))
	target := strings.TrimSpace(op.Target)
	tm := strings.TrimSpace(op.Time)
	if err := validQualifierTargetTime(q, target, tm); err != nil {
		return nil, err
	}
	idx := op.Index
	if idx < 0 || idx > len(s.Actions) {
		idx = len(s.Actions)
	}
	a := GAssoc{Qualifier: q, Target: target, Time: tm}
	s.Actions = append(s.Actions[:idx:idx], append([]GAssoc{a}, s.Actions[idx:]...)...)
	return []TextEdit{{Line: s.Line, Col: 1, EndLine: s.EndLine + 1, EndCol: 1, NewText: printStep(s)}}, nil
}

func opSetAssoc(m *Model, op EditOp) ([]TextEdit, error) {
	s, err := findStep(m, op.Step)
	if err != nil {
		return nil, err
	}
	if op.Index < 0 || op.Index >= len(s.Actions) {
		return nil, fmt.Errorf("sfc edit: association index %d out of range", op.Index)
	}
	q := strings.ToUpper(strings.TrimSpace(op.Qualifier))
	target := strings.TrimSpace(op.Target)
	tm := strings.TrimSpace(op.Time)
	if err := validQualifierTargetTime(q, target, tm); err != nil {
		return nil, err
	}
	s.Actions[op.Index] = GAssoc{Qualifier: q, Target: target, Time: tm}
	return []TextEdit{{Line: s.Line, Col: 1, EndLine: s.EndLine + 1, EndCol: 1, NewText: printStep(s)}}, nil
}

func opDeleteAssoc(m *Model, op EditOp) ([]TextEdit, error) {
	s, err := findStep(m, op.Step)
	if err != nil {
		return nil, err
	}
	if op.Index < 0 || op.Index >= len(s.Actions) {
		return nil, fmt.Errorf("sfc edit: association index %d out of range", op.Index)
	}
	s.Actions = append(s.Actions[:op.Index], s.Actions[op.Index+1:]...)
	return []TextEdit{{Line: s.Line, Col: 1, EndLine: s.EndLine + 1, EndCol: 1, NewText: printStep(s)}}, nil
}

// ── setActionBody (create-or-update) ────────────────────────────────────

func opSetActionBody(lines []string, m *Model, op EditOp) ([]TextEdit, error) {
	name := strings.TrimPrefix(strings.TrimSpace(op.Action), "ac:")
	if !sfcIdentRe.MatchString(name) {
		return nil, fmt.Errorf("sfc edit: %q is not a valid action name", name)
	}
	if a, err := findAction(m, actionID(name)); err == nil {
		a.Body = op.Body
		return []TextEdit{{Line: a.Line, Col: 1, EndLine: a.EndLine + 1, EndCol: 1, NewText: printAction(a)}}, nil
	}
	at, err := insertionLine(lines, m, "", "action")
	if err != nil {
		return nil, err
	}
	na := &GAction{Name: name, Body: op.Body}
	return []TextEdit{{Line: at, Col: 1, EndLine: at, EndCol: 1, NewText: "\n" + printAction(na)}}, nil
}

// ── insertAlternativeBranch / insertSimultaneousBranch ──────────────────

// opInsertAlternativeBranch adds a second TRANSITION sharing a source (§4.2:
// "add a second TRANSITION FROM <same source> (priority = insertion
// order)"). From defaults to the After transition's source set when not
// given explicitly, so the common gesture — "branch off this transition's
// source" — only needs After + To + Cond.
func opInsertAlternativeBranch(lines []string, m *Model, op EditOp) ([]TextEdit, error) {
	from := append([]string{}, op.From...)
	if len(from) == 0 {
		if op.After == "" {
			return nil, fmt.Errorf("sfc edit: insertAlternativeBranch needs From or After")
		}
		anchor, err := findTransition(m, op.After)
		if err != nil {
			return nil, err
		}
		from = append([]string{}, anchor.From...)
	}
	if len(op.To) == 0 {
		return nil, fmt.Errorf("sfc edit: insertAlternativeBranch needs a TO step-set")
	}
	for _, n := range append(append([]string{}, from...), op.To...) {
		if !sfcIdentRe.MatchString(n) {
			return nil, fmt.Errorf("sfc edit: %q is not a valid step name", n)
		}
	}
	name := strings.TrimSpace(op.Name)
	if name != "" {
		if !sfcIdentRe.MatchString(name) {
			return nil, fmt.Errorf("sfc edit: %q is not a valid transition name", name)
		}
		if _, err := findTransition(m, "tr:"+name); err == nil {
			return nil, fmt.Errorf("sfc edit: a transition named %q already exists", name)
		}
	}
	cond := strings.TrimSpace(op.Cond)
	if err := validCond(cond); err != nil {
		return nil, err
	}

	var at int
	if op.After != "" {
		anchor, err := findTransition(m, op.After)
		if err != nil {
			return nil, err
		}
		at = anchor.EndLine + 1
	} else if len(from) == 1 {
		// No explicit anchor: land as the LOWEST priority in the group —
		// after the last transition sharing this exact single source, else
		// after the last transition overall (priority = declaration order,
		// §2.3).
		for _, t := range m.Trans {
			if len(t.From) == 1 && strings.EqualFold(t.From[0], from[0]) {
				at = t.EndLine + 1
			}
		}
	}
	if at == 0 {
		var err error
		at, err = insertionLine(lines, m, "", "transition")
		if err != nil {
			return nil, err
		}
	}
	nt := &GTransition{Name: name, From: from, To: op.To, Cond: cond}
	return []TextEdit{{Line: at, Col: 1, EndLine: at, EndCol: 1, NewText: "\n" + printTransition(nt)}}, nil
}

// opInsertSimultaneousBranch turns a transition's TO x into TO (x, y),
// creating step y (§4.2). When the transition already opens a simultaneous
// divergence — TO (a, b) — whose branches meet again at a convergence
// (a transition whose FROM holds every one of a, b), that join is widened
// too: FROM (a, b) becomes FROM (a, b, y). Without it the new branch is a
// dead end the join never waits for, which no one drawing a third parallel
// leg means. A plain TO x has no join to find; the new step stays a
// never-block dead end until the user wires it (Check warns).
//
// Every edit is minimal: the new step lands after the last of its sibling
// steps (in the steps section, where a reader looks for it), and each
// widened transition has only its step-set rewritten in place, so the
// transition's alignment and trailing comment survive — the change reads
// in a diff as exactly what it is.
func opInsertSimultaneousBranch(lines []string, m *Model, op EditOp) ([]TextEdit, error) {
	t, err := findTransition(m, op.Transition)
	if err != nil {
		return nil, err
	}
	newStep := strings.TrimSpace(op.NewStep)
	if !sfcIdentRe.MatchString(newStep) {
		return nil, fmt.Errorf("sfc edit: %q is not a valid step name", newStep)
	}
	if _, err := findStep(m, stepID(newStep)); err == nil {
		return nil, fmt.Errorf("sfc edit: a step named %q already exists", newStep)
	}
	for _, n := range t.To {
		if strings.EqualFold(n, newStep) {
			return nil, fmt.Errorf("sfc edit: %q is already a target of this transition", newStep)
		}
	}

	// The step: after the last sibling it joins, else after the last step.
	at := 0
	for _, n := range t.To {
		if s, err := findStep(m, stepID(n)); err == nil && s.EndLine+1 > at {
			at = s.EndLine + 1
		}
	}
	if at == 0 {
		if at, err = insertionLine(lines, m, "", "step"); err != nil {
			return nil, err
		}
	}
	edits := []TextEdit{{Line: at, Col: 1, EndLine: at, EndCol: 1, NewText: "\n" + printStep(&GStep{Name: newStep})}}

	edits = append(edits, stepSetEdit(lines, t, "TO", append(append([]string{}, t.To...), newStep)))
	if len(t.To) >= 2 {
		for i := range m.Trans {
			j := &m.Trans[i]
			if j.ID != t.ID && containsAllFold(j.From, t.To) {
				edits = append(edits, stepSetEdit(lines, j, "FROM", append(append([]string{}, j.From...), newStep)))
			}
		}
	}
	return edits, nil
}

func containsAllFold(set, want []string) bool {
	for _, w := range want {
		found := false
		for _, s := range set {
			if strings.EqualFold(s, w) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

var stepSetRe = regexp.MustCompile(`(?i)\b(FROM|TO)\s+(\([^)]*\)|[A-Za-z_][A-Za-z0-9_]*)`)

// stepSetEdit rewrites one side ("FROM"|"TO") of a transition's step-set
// in place on its header line, leaving everything else on the line alone.
// A header split across lines (legal, never how the editor prints one)
// falls back to reprinting the whole transition block.
func stepSetEdit(lines []string, t *GTransition, side string, names []string) TextEdit {
	if t.Line >= 1 && t.Line <= len(lines) {
		line := lines[t.Line-1]
		for _, mm := range stepSetRe.FindAllStringSubmatchIndex(line, -1) {
			if strings.EqualFold(line[mm[2]:mm[3]], side) {
				return TextEdit{Line: t.Line, Col: mm[4] + 1, EndLine: t.Line, EndCol: mm[5] + 1, NewText: printStepSet(names)}
			}
		}
	}
	nt := *t
	if side == "TO" {
		nt.To = names
	} else {
		nt.From = names
	}
	return TextEdit{Line: t.Line, Col: 1, EndLine: t.EndLine + 1, EndCol: 1, NewText: printTransition(&nt)}
}

// ── layout (reuses lang/fbd's (* @layout … *) format, design doc §4.2) ──

func opSetLayout(lines []string, m *Model, op EditOp) ([]TextEdit, error) {
	pins := op.Entries
	if len(pins) == 0 {
		if op.X == nil || op.Y == nil {
			return nil, fmt.Errorf("sfc edit: setLayout needs x and y")
		}
		pins = []LayoutOpEntry{{Node: op.Node, X: *op.X, Y: *op.Y}}
	}
	known := map[string]bool{}
	for _, s := range m.Steps {
		known[s.ID] = true
	}
	for _, t := range m.Trans {
		known[t.ID] = true
	}
	for _, a := range m.Actions {
		known[a.ID] = true
	}
	for i := range m.Comments {
		known[commentID(i)] = true
	}
	entries := map[string]Point{}
	for id, p := range m.Layout {
		entries[id] = p
	}
	pinned := 0
	for _, p := range pins {
		if !known[p.Node] {
			if len(pins) > 1 {
				continue // a batched drag can carry stale/unknown ids; skip, don't fail the whole gesture
			}
			return nil, fmt.Errorf("sfc edit: unknown node %q", p.Node)
		}
		entries[p.Node] = Point{X: p.X, Y: p.Y}
		pinned++
	}
	if pinned == 0 {
		return nil, nil
	}
	return writeLayoutEdit(lines, entries)
}

func opClearLayout(lines []string, m *Model, op EditOp) ([]TextEdit, error) {
	if len(m.Layout) == 0 {
		return nil, nil
	}
	if op.Node == "" {
		return writeLayoutEdit(lines, nil)
	}
	entries := map[string]Point{}
	for id, p := range m.Layout {
		if id != op.Node {
			entries[id] = p
		}
	}
	return writeLayoutEdit(lines, entries)
}

// writeLayoutEdit produces the edit that replaces (or creates, just above
// END_SFC, or removes) the @layout block.
func writeLayoutEdit(lines []string, entries map[string]Point) ([]TextEdit, error) {
	_, start, end := parseLayoutBlock(lines)
	if start > 0 {
		if len(entries) == 0 {
			return []TextEdit{{Line: start, Col: 1, EndLine: end + 1, EndCol: 1}}, nil
		}
		return []TextEdit{{Line: start, Col: 1, EndLine: end + 1, EndCol: 1, NewText: renderLayoutBlock(entries)}}, nil
	}
	if len(entries) == 0 {
		return nil, nil
	}
	for i, l := range lines {
		if sfcEndRe.MatchString(l) {
			at := i + 1
			return []TextEdit{{Line: at, Col: 1, EndLine: at, EndCol: 1, NewText: renderLayoutBlock(entries)}}, nil
		}
	}
	return nil, fmt.Errorf("sfc edit: no END_SFC to anchor the layout block")
}

// remapLayout rewrites/removes layout entries after a rename/delete —
// rewrite returns the new id and whether to keep the entry.
func remapLayout(lines []string, m *Model, rewrite func(id string) (string, bool)) []TextEdit {
	if len(m.Layout) == 0 {
		return nil
	}
	changed := false
	entries := map[string]Point{}
	for id, p := range m.Layout {
		nid, keep := rewrite(id)
		if !keep {
			changed = true
			continue
		}
		if nid != id {
			changed = true
		}
		entries[nid] = p
	}
	if !changed {
		return nil
	}
	edits, err := writeLayoutEdit(lines, entries)
	if err != nil {
		return nil
	}
	return edits
}

func idDrop(id string) func(string) (string, bool) {
	return func(x string) (string, bool) {
		if x == id {
			return "", false
		}
		return x, true
	}
}

func idRewrite(oldID, newID string) func(string) (string, bool) {
	return func(x string) (string, bool) {
		if x == oldID {
			return newID, true
		}
		return x, true
	}
}

// ── comments (reuses lang/fbd's setComment/deleteComment semantics: a note
// is a run of consecutive full-line `//` comments, addressed by its 0-based
// ordinal into Model.Comments — commentID below turns that into the same
// "cm:N" layout-block id fbd uses, so a dragged note pins exactly like a
// step/transition/action does) ───────────────────────────────────────────

func commentID(n int) string { return "cm:" + strconv.Itoa(n) }

func opSetComment(lines []string, m *Model, op EditOp) ([]TextEdit, error) {
	if op.Comment == nil || *op.Comment < 0 || *op.Comment >= len(m.Comments) {
		return nil, fmt.Errorf("sfc edit: no such comment")
	}
	text := strings.TrimSpace(op.Text)
	if text == "" {
		return opDeleteComment(lines, m, op)
	}
	c := m.Comments[*op.Comment]
	return []TextEdit{{Line: c.Line, Col: 1, EndLine: c.EndLine + 1, EndCol: 1, NewText: renderSFCComment(text)}}, nil
}

// opAddComment inserts a new note just above END_SFC — the same placement
// fbd's insertStatement uses for a `// text` fragment (a note's line
// position inside the body is otherwise meaningless: the diagram always
// renders comments as a strip ahead of rank 0, per sfc.ts's layoutSfc).
func opAddComment(lines []string, m *Model, op EditOp) ([]TextEdit, error) {
	text := strings.TrimSpace(op.Text)
	if text == "" {
		return nil, fmt.Errorf("sfc edit: nothing to insert")
	}
	for i, l := range lines {
		if sfcEndRe.MatchString(l) {
			at := i + 1
			return []TextEdit{{Line: at, Col: 1, EndLine: at, EndCol: 1, NewText: "\n" + renderSFCComment(text)}}, nil
		}
	}
	return nil, fmt.Errorf("sfc edit: no END_SFC to insert before")
}

func opDeleteComment(lines []string, m *Model, op EditOp) ([]TextEdit, error) {
	if op.Comment == nil || *op.Comment < 0 || *op.Comment >= len(m.Comments) {
		return nil, fmt.Errorf("sfc edit: no such comment")
	}
	c := m.Comments[*op.Comment]
	edits := []TextEdit{blockDeleteEdit(lines, c.Line, c.EndLine)}
	edits = append(edits, remapLayout(lines, m, idDrop(commentID(*op.Comment)))...)
	return edits, nil
}

func renderSFCComment(text string) string {
	var b strings.Builder
	for _, line := range strings.Split(text, "\n") {
		b.WriteString("  // " + strings.TrimSpace(line) + "\n")
	}
	return b.String()
}

// ── clipboard: pasteSteps / deleteSelection ─────────────────────────────

// freshName returns name when taken says it's free, else the first free
// variant: a trailing number counts up (Step3 → Step4), a bare name gains
// one (Fill → Fill2).
func freshName(name string, taken func(string) bool) string {
	if !taken(name) {
		return name
	}
	base, n := name, 2
	if m := trailingNumRe.FindStringSubmatch(name); m != nil {
		base = m[1]
		if v, err := strconv.Atoi(m[2]); err == nil {
			n = v + 1
		}
	}
	for ; ; n++ {
		if c := base + strconv.Itoa(n); !taken(c) {
			return c
		}
	}
}

var trailingNumRe = regexp.MustCompile(`^(.*?[A-Za-z_])(\d+)$`)

// opPasteSteps inserts copied steps (never INITIAL — a chart has exactly
// one) with their associations, each under its first free name, plus the
// copied transitions between them with their ends following the renames.
// Positions ride along as layout pins. The result is one edit.
func opPasteSteps(src string, lines []string, m *Model, op EditOp) ([]TextEdit, error) {
	if len(op.Steps) == 0 {
		return nil, fmt.Errorf("sfc edit: nothing to paste")
	}
	stepTaken := map[string]bool{}
	for _, s := range m.Steps {
		stepTaken[strings.ToLower(s.Name)] = true
	}
	for _, a := range m.Actions {
		stepTaken[strings.ToLower(a.Name)] = true // one namespace in the transpiled program
	}
	for _, v := range m.Vars {
		stepTaken[strings.ToLower(v.Name)] = true
	}
	transTaken := map[string]bool{}
	for _, t := range m.Trans {
		if t.Name != "" {
			transTaken[strings.ToLower(t.Name)] = true
		}
	}

	renames := map[string]string{} // lower(old) → new
	var stepText strings.Builder
	pins := map[string]Point{}
	for _, ps := range op.Steps {
		if !sfcIdentRe.MatchString(ps.Name) {
			return nil, fmt.Errorf("sfc edit: %q is not a valid step name", ps.Name)
		}
		if _, dup := renames[strings.ToLower(ps.Name)]; dup {
			return nil, fmt.Errorf("sfc edit: step %q is in the paste twice", ps.Name)
		}
		name := freshName(ps.Name, func(n string) bool { return stepTaken[strings.ToLower(n)] })
		stepTaken[strings.ToLower(name)] = true
		renames[strings.ToLower(ps.Name)] = name
		for _, a := range ps.Actions {
			if err := validQualifierTargetTime(a.Qualifier, a.Target, a.Time); err != nil {
				return nil, err
			}
		}
		stepText.WriteString("\n" + printStep(&GStep{Name: name, Actions: ps.Actions}))
		if ps.X != nil && ps.Y != nil {
			pins[stepID(name)] = Point{X: *ps.X, Y: *ps.Y}
		}
	}
	var transText strings.Builder
	for _, pt := range op.Trans {
		if len(pt.From) == 0 || len(pt.To) == 0 {
			continue
		}
		if err := validCond(pt.Cond); err != nil {
			return nil, err
		}
		t := GTransition{Cond: strings.TrimSpace(pt.Cond)}
		if t.Cond == "" {
			t.Cond = "TRUE"
		}
		ok := true
		for _, n := range pt.From {
			nn, in := renames[strings.ToLower(n)]
			ok = ok && in
			t.From = append(t.From, nn)
		}
		for _, n := range pt.To {
			nn, in := renames[strings.ToLower(n)]
			ok = ok && in
			t.To = append(t.To, nn)
		}
		if !ok {
			continue // an end outside the paste: the webview never sends one, but never dangle
		}
		if pt.Name != "" {
			if !sfcIdentRe.MatchString(pt.Name) {
				return nil, fmt.Errorf("sfc edit: %q is not a valid transition name", pt.Name)
			}
			t.Name = freshName(pt.Name, func(n string) bool { return transTaken[strings.ToLower(n)] })
			transTaken[strings.ToLower(t.Name)] = true
		}
		transText.WriteString("\n" + printTransition(&t))
	}

	stepAt, err := insertionLine(lines, m, "", "step")
	if err != nil {
		return nil, err
	}
	var edits []TextEdit
	if transText.Len() > 0 {
		transAt, err := insertionLine(lines, m, "", "transition")
		if err != nil {
			return nil, err
		}
		if transAt == stepAt {
			stepText.WriteString(transText.String())
		} else {
			edits = append(edits, TextEdit{Line: transAt, Col: 1, EndLine: transAt, EndCol: 1, NewText: transText.String()})
		}
	}
	edits = append(edits, TextEdit{Line: stepAt, Col: 1, EndLine: stepAt, EndCol: 1, NewText: stepText.String()})
	result := applyTextEdits(src, edits)

	if len(pins) > 0 {
		m2, err := Graph(result)
		if err != nil {
			return nil, fmt.Errorf("sfc edit: refused — the paste would not parse: %w", err)
		}
		entries := map[string]Point{}
		for id, p := range m2.Layout {
			entries[id] = p
		}
		for id, p := range pins {
			entries[id] = p
		}
		le, err := writeLayoutEdit(strings.Split(result, "\n"), entries)
		if err != nil {
			return nil, err
		}
		result = applyTextEdits(result, le)
	}
	return collapseEdit(src, result), nil
}

// opDeleteSelection deletes several steps/transitions in ONE op — ids
// resolve against a single parse (an unnamed transition's id is its line,
// which per-element ops would shift) and their layout pins drop together.
// Like deleteStep, transitions left pointing at a deleted step stay as
// Check breadcrumbs; the webview sends the ones wholly inside a selection.
func opDeleteSelection(src string, lines []string, m *Model, op EditOp) ([]TextEdit, error) {
	var edits []TextEdit
	drop := map[string]bool{}
	for _, id := range op.Nodes {
		if drop[id] {
			continue
		}
		switch {
		case strings.HasPrefix(id, "st:"):
			s, err := findStep(m, id)
			if err != nil {
				return nil, err
			}
			edits = append(edits, blockDeleteEdit(lines, s.Line, s.EndLine))
			drop[s.ID] = true
		case strings.HasPrefix(id, "tr:"):
			t, err := findTransition(m, id)
			if err != nil {
				return nil, err
			}
			edits = append(edits, blockDeleteEdit(lines, t.Line, t.EndLine))
			drop[t.ID] = true
		default:
			return nil, fmt.Errorf("sfc edit: deleteSelection takes step and transition ids, not %q", id)
		}
	}
	if len(edits) == 0 {
		return nil, nil
	}
	result := applyTextEdits(src, edits)
	if m2, err := Graph(result); err == nil && len(m2.Layout) > 0 {
		result = applyTextEdits(result, remapLayout(strings.Split(result, "\n"), m2, func(id string) (string, bool) {
			return id, !drop[id]
		}))
	}
	return collapseEdit(src, result), nil
}

// collapseEdit expresses src → result as ONE replacement spanning just the
// changed lines — for ops that build their result in stages (insert, then
// re-pin layout), where separate edits could touch or overlap.
func collapseEdit(src, result string) []TextEdit {
	a := strings.SplitAfter(src, "\n")
	b := strings.SplitAfter(result, "\n")
	p := 0
	for p < len(a) && p < len(b) && a[p] == b[p] {
		p++
	}
	q := 0
	for q < len(a)-p && q < len(b)-p && a[len(a)-1-q] == b[len(b)-1-q] {
		q++
	}
	if p == len(a) && p == len(b) {
		return nil
	}
	newText := strings.Join(b[p:len(b)-q], "")
	if p == len(a) { // pure append past the last line
		last := a[len(a)-1]
		return []TextEdit{{Line: len(a), Col: len(last) + 1, EndLine: len(a), EndCol: len(last) + 1, NewText: newText}}
	}
	endIdx := len(a) - q // first unchanged trailing element
	if endIdx < len(a) {
		return []TextEdit{{Line: p + 1, Col: 1, EndLine: endIdx + 1, EndCol: 1, NewText: newText}}
	}
	last := a[len(a)-1]
	return []TextEdit{{Line: p + 1, Col: 1, EndLine: len(a), EndCol: len(last) + 1, NewText: newText}}
}

// ── header variables ─────────────────────────────────────────────────────
// The vars-panel seam shared with lang/fbd and lang/ld: declarations live
// in the ST header above the SFC body, edited textually.

var sfcVarSectionRe = regexp.MustCompile(`(?i)^\s*(VAR_EXTERNAL|VAR)\s*$`)

// opDeclareVar inserts "name : TYPE;" into a header section (VAR_EXTERNAL
// default, VAR for retained locals), creating the section above SFC when
// the header has none.
func opDeclareVar(lines []string, m *Model, op EditOp) ([]TextEdit, error) {
	name := strings.TrimSpace(op.Name)
	typ := strings.TrimSpace(op.VarType)
	section := strings.ToUpper(strings.TrimSpace(op.Section))
	if section == "" {
		section = "VAR_EXTERNAL"
	}
	if section != "VAR" && section != "VAR_EXTERNAL" {
		return nil, fmt.Errorf("sfc edit: unknown section %q", section)
	}
	if !sfcIdentRe.MatchString(name) {
		return nil, fmt.Errorf("sfc edit: %q is not a valid identifier", name)
	}
	if !sfcIdentRe.MatchString(typ) {
		return nil, fmt.Errorf("sfc edit: %q is not a valid type name", typ)
	}
	for _, v := range m.Vars {
		if strings.EqualFold(v.Name, name) {
			return nil, fmt.Errorf("sfc edit: %q is already declared", name)
		}
	}
	for _, s := range m.Steps {
		if strings.EqualFold(s.Name, name) {
			return nil, fmt.Errorf("sfc edit: %q is already a step name", name)
		}
	}
	for _, a := range m.Actions {
		if strings.EqualFold(a.Name, name) {
			return nil, fmt.Errorf("sfc edit: %q is already an action name", name)
		}
	}

	// Scanned on comment-stripped text so a doc comment reading like header
	// structure isn't mistaken for it.
	stripped := strings.Split(hdrvars.StripComments(strings.Join(lines, "\n")), "\n")
	sfcLine := -1
	for i, l := range stripped {
		if sfcStartRe.MatchString(l) {
			sfcLine = i
			break
		}
	}
	if sfcLine == -1 {
		return nil, fmt.Errorf("sfc edit: no SFC block")
	}
	insertAt, inSection := -1, ""
	for i := 0; i < sfcLine; i++ {
		if mm := sfcVarSectionRe.FindStringSubmatch(stripped[i]); mm != nil {
			inSection = strings.ToUpper(mm[1])
			continue
		}
		if strings.EqualFold(strings.TrimSpace(stripped[i]), "END_VAR") {
			if inSection == section {
				insertAt = i
			}
			inSection = ""
		}
	}
	decl := "    " + name + " : " + typ + ";\n"
	if insertAt >= 0 {
		at := insertAt + 1 // 1-based line of END_VAR
		return []TextEdit{{Line: at, Col: 1, EndLine: at, EndCol: 1, NewText: decl}}, nil
	}
	at := sfcLine + 1
	return []TextEdit{{Line: at, Col: 1, EndLine: at, EndCol: 1,
		NewText: section + "\n" + decl + "END_VAR\n"}}, nil
}

// opDeleteVar removes a declaration by name — the whole line when it
// stands alone, else just its `name : TYPE;`. References the chart still
// holds become undeclared-variable diagnostics (the never-block posture).
func opDeleteVar(lines []string, m *Model, op EditOp) ([]TextEdit, error) {
	name := strings.TrimSpace(op.Name)
	for _, v := range m.Vars {
		if !strings.EqualFold(v.Name, name) {
			continue
		}
		if v.Line < 1 || v.Line > len(lines) {
			break
		}
		col, end, whole, ok := hdrvars.DeleteSpan(lines[v.Line-1], v.Name)
		if !ok {
			return nil, fmt.Errorf("sfc edit: can't locate the declaration of %q", name)
		}
		if whole {
			return []TextEdit{{Line: v.Line, Col: 1, EndLine: v.Line + 1, EndCol: 1}}, nil
		}
		return []TextEdit{{Line: v.Line, Col: col, EndLine: v.Line, EndCol: end}}, nil
	}
	return nil, fmt.Errorf("sfc edit: no declaration named %q", name)
}

// ── applying edits to a scratch copy (the never-block parse gate) ──────

// applyTextEdits applies a set of assumed-non-overlapping 1-based,
// end-exclusive edits to src, back-to-front by position so earlier offsets
// stay valid — used only for ApplyEdit's own re-parse safety check (the
// real consumer, e.g. an LSP client or the CLI caller, applies the same
// edits itself).
func applyTextEdits(src string, edits []TextEdit) string {
	starts := []int{0}
	for i, c := range src {
		if c == '\n' {
			starts = append(starts, i+1)
		}
	}
	offset := func(line, col int) int {
		idx := line - 1
		if idx < 0 {
			idx = 0
		}
		if idx >= len(starts) {
			idx = len(starts) - 1
		}
		o := starts[idx] + (col - 1)
		if o < 0 {
			o = 0
		}
		if o > len(src) {
			o = len(src)
		}
		return o
	}
	type span struct {
		lo, hi int
		text   string
	}
	spans := make([]span, len(edits))
	for i, e := range edits {
		spans[i] = span{offset(e.Line, e.Col), offset(e.EndLine, e.EndCol), e.NewText}
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].lo > spans[j].lo })
	out := src
	for _, s := range spans {
		lo, hi := s.lo, s.hi
		if hi < lo {
			hi = lo
		}
		if hi > len(out) {
			hi = len(out)
		}
		out = out[:lo] + s.text + out[hi:]
	}
	return out
}
