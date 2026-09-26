package sfc

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/joyautomation/nautilus/lang/st"
)

// transpileProgram is the heart of Slice B: it turns a parsed SFC chart into
// equivalent per-scan ST source (design doc §3), together with a line map
// projecting each generated ST line back onto the .sfc source line it came
// from (§3.2). The generated body performs, once per scan, the fixed-order
// evolution of §2.1:
//
//  1. evaluate every transition's `enabled` from the PRE-scan step-activity
//     snapshot (_en_<t>), then resolve firing (_f_<t>) in declaration order
//     with the alternative-priority guard of §2.3 (suppressed only by a
//     higher-priority transition sharing a source that itself fires);
//  2. clear the sources of every firing transition (all clears first);
//  3. set the targets of every firing transition (all sets after all clears,
//     so set-dominates-clear per §2.4);
//  4. update hidden Step.T timers from the new activity (§2.6);
//  5. compute actions — S/R stored flags, body actions with the final-scan
//     rule, then boolean-variable associations, each written only on the
//     scans it acts (§2.5, §2.5.1);
//  6. advance per-step edge memories (used by P/P1/P0 pulses and S/R edges).
//
// All SFC state (step activity, stored flags, edge memories, final-scan
// memories, step TONs) is emitted as retained named VAR slots following a
// stable naming scheme (§2.7) so ir.MigrateFrame preserves a live token and
// every timer across an online edit.
//
// Naming scheme (latitude the doc left; chosen for stability + grep-ability):
//
//	_S_<Step>_X      step activity BOOL (initial step declared := TRUE)
//	_S_<Step>_prev   previous-scan activity, for P/P1/P0 pulses and S/R edges
//	_S_<Step>_t      hidden TON for a referenced Step.T; Step.T => _S_<Step>_t.ET
//	_en_<tid>        transition "enabled" scratch (all sources active AND cond)
//	_f_<tid>         transition "fire" scratch (enabled AND alt-priority guard)
//	_act_<Tgt>_stored   retained S/R stored flag
//	_act_<Act>_prev     retained final-scan memory for a level body action
//	_act_<Var>_lvl      retained final-scan memory for a boolean variable's
//	                    N/P/P1/P0 drive signal
//
// where <tid> is the transition's declared name, or t<line> when unnamed.
func transpileProgram(prog *Program, src string) (string, []int, error) {
	header, _, footer, _, err := splitSFC(src)
	if err != nil {
		return "", nil, err
	}

	g := newGen(prog)
	if err := g.build(); err != nil {
		return "", nil, err
	}

	var b strings.Builder
	var lineMap []int
	writeLine := func(s string, orig int) {
		// A spliced statement (a multi-line condition/body) contributes several
		// physical lines; each maps to the same source line unless the caller
		// splits it itself for finer fidelity.
		for _, ln := range strings.Split(s, "\n") {
			b.WriteString(ln)
			b.WriteByte('\n')
			lineMap = append(lineMap, orig)
		}
	}

	// Header verbatim (maps 1:1 — it is a byte-prefix of the file).
	for i, l := range strings.Split(header, "\n") {
		writeLine(l, i+1)
	}
	// Injected VAR block of hidden SFC state.
	if len(g.decls) > 0 {
		writeLine("VAR", g.bodyLine)
		for _, d := range g.decls {
			writeLine("  "+d.text, d.line)
		}
		writeLine("END_VAR", g.bodyLine)
	}
	// The ordered per-scan body.
	for _, s := range g.stmts {
		writeLine(s.text, s.line)
	}
	// Footer (END_SFC dropped; END_PROGRAM kept) written raw, byte-identical to
	// the source tail, exactly as fbd.TranspileWithLines does.
	footerLines := strings.Split(footer, "\n")
	footerStart := len(strings.Split(src, "\n")) - len(footerLines) + 1
	b.WriteString(footer)
	for i := range footerLines {
		lineMap = append(lineMap, footerStart+i)
	}
	return b.String(), lineMap, nil
}

// ─── generator ───────────────────────────────────────────────────────────────

type decl struct {
	text string
	line int
}

type stmt struct {
	text string
	line int
}

// stepAssoc pairs an association with the step it lives on.
type stepAssoc struct {
	step *Step
	a    Assoc
}

type gen struct {
	prog     *Program
	bodyLine int // 1-based file line of the SFC marker (anchor for injected code)

	stepByUpper map[string]*Step
	actionSet   map[string]bool // upper action-block name -> is a body action
	initialName string

	transID map[*Transition]string

	needPrev  map[string]bool // upper step name -> needs _S_.._prev
	needTimer map[string]bool // upper step name -> needs hidden TON
	stepOrder []*Step

	decls []decl
	stmts []stmt
}

func newGen(prog *Program) *gen {
	g := &gen{
		prog:        prog,
		stepByUpper: map[string]*Step{},
		actionSet:   map[string]bool{},
		transID:     map[*Transition]string{},
		needPrev:    map[string]bool{},
		needTimer:   map[string]bool{},
	}
	for _, s := range prog.Steps {
		g.stepByUpper[strings.ToUpper(s.Name)] = s
		g.stepOrder = append(g.stepOrder, s)
		if s.Initial {
			g.initialName = s.Name
		}
	}
	for _, a := range prog.Actions {
		g.actionSet[strings.ToUpper(a.Name)] = true
	}
	if len(prog.Steps) > 0 {
		g.bodyLine = prog.Steps[0].Pos.Line
	} else {
		g.bodyLine = 1
	}
	return g
}

// slot-name helpers (kept in one place so the naming scheme is stable).
func (g *gen) slot(name string) string  { return "_S_" + g.canon(name) + "_X" }
func (g *gen) prev(name string) string  { return "_S_" + g.canon(name) + "_prev" }
func (g *gen) timer(name string) string { return "_S_" + g.canon(name) + "_t" }
func (g *gen) stored(name string) string {
	return "_act_" + g.canon(name) + "_stored"
}
func (g *gen) prevMem(name string) string { return "_act_" + g.canon(name) + "_prev" }
func (g *gen) lvlMem(name string) string  { return "_act_" + g.canon(name) + "_lvl" }

// canon returns a step/action's canonical spelling (the declaration's spelling)
// so a slot name is stable regardless of how a condition/body cased a reference.
func (g *gen) canon(name string) string {
	if s := g.stepByUpper[strings.ToUpper(name)]; s != nil {
		return s.Name
	}
	return name
}

func (g *gen) emit(text string, line int) { g.stmts = append(g.stmts, stmt{text, line}) }
func (g *gen) comment(text string)        { g.emit("(* "+text+" *)", g.bodyLine) }

func (g *gen) build() error {
	g.assignTransIDs()
	g.scanTimerRefs()
	g.scanEdgeNeeds()

	g.buildVarBlock()

	g.emitTransitions()
	g.emitClears()
	g.emitSets()
	g.emitStepTimers()
	if err := g.emitActions(); err != nil {
		return err
	}
	g.emitEdgeUpdates()
	return nil
}

// assignTransIDs gives every transition a unique ST-identifier stem for its
// _en_/_f_ scratch flags: the declared name, else t<line>; de-duplicated.
func (g *gen) assignTransIDs() {
	used := map[string]bool{}
	for _, t := range g.prog.Transitions {
		id := t.Name
		if id == "" {
			id = fmt.Sprintf("t%d", t.Pos.Line)
		}
		base := id
		for i := 2; used[strings.ToUpper(id)]; i++ {
			id = fmt.Sprintf("%s_%d", base, i)
		}
		used[strings.ToUpper(id)] = true
		g.transID[t] = id
	}
}

var timeLitRe = regexp.MustCompile(`(?i)T#[0-9][0-9a-z_.]*`)

// scanTimerRefs marks which steps have their .T referenced anywhere (so only
// those get a hidden TON — §2.6, steps whose .T is never read cost nothing).
func (g *gen) scanTimerRefs() {
	mark := func(text string) {
		for _, loc := range stepRefRe.FindAllStringSubmatchIndex(text, -1) {
			if loc[0] > 0 && text[loc[0]-1] == '.' {
				continue // a nested field access like foo.Mix.T, not a step ref
			}
			name := text[loc[2]:loc[3]]
			suffix := text[loc[4]:loc[5]]
			if suffix == "T" {
				if s := g.stepByUpper[strings.ToUpper(name)]; s != nil {
					g.needTimer[strings.ToUpper(s.Name)] = true
				}
			}
		}
	}
	for _, t := range g.prog.Transitions {
		mark(t.Cond.Text)
	}
	for _, a := range g.prog.Actions {
		mark(a.Body.Text)
	}
}

// scanEdgeNeeds marks which steps need a _prev edge memory: any step carrying
// a pulse (P/P1/P0) or store/reset (S/R) association, whose semantics are
// defined against the step's rising (or falling) edge.
func (g *gen) scanEdgeNeeds() {
	for _, s := range g.prog.Steps {
		for _, a := range s.Actions {
			switch a.Qualifier {
			case "S", "R", "P", "P1", "P0":
				g.needPrev[strings.ToUpper(s.Name)] = true
			}
		}
	}
}

// ptCapMs computes the PT clamp for hidden Step.T timers (§2.6): strictly
// greater than the largest TIME literal the chart compares against, so the
// TON's ET-clamp-at-PT never truncates a real .T comparison. Rule: 2× the
// largest TIME literal in the chart, floored at 24h; 24h when none is found.
func (g *gen) ptCapMs() int {
	max := 0
	scan := func(text string) {
		for _, lit := range timeLitRe.FindAllString(text, -1) {
			ms := st.ParseTimeMs(strings.TrimPrefix(strings.ToUpper(lit), "T#"))
			if ms > max {
				max = ms
			}
		}
	}
	for _, t := range g.prog.Transitions {
		scan(t.Cond.Text)
	}
	for _, a := range g.prog.Actions {
		scan(a.Body.Text)
	}
	cap := max * 2
	const day = 24 * 3600 * 1000
	if cap < day {
		cap = day
	}
	return cap
}

// buildVarBlock collects every hidden VAR slot declaration.
func (g *gen) buildVarBlock() {
	for _, s := range g.stepOrder {
		init := ""
		if s.Initial {
			init = " := TRUE"
		}
		g.decls = append(g.decls, decl{fmt.Sprintf("%s : BOOL%s;", g.slot(s.Name), init), s.Pos.Line})
		if g.needPrev[strings.ToUpper(s.Name)] {
			g.decls = append(g.decls, decl{fmt.Sprintf("%s : BOOL;", g.prev(s.Name)), s.Pos.Line})
		}
		if g.needTimer[strings.ToUpper(s.Name)] {
			g.decls = append(g.decls, decl{fmt.Sprintf("%s : TON;", g.timer(s.Name)), s.Pos.Line})
		}
	}
	for _, t := range g.prog.Transitions {
		id := g.transID[t]
		g.decls = append(g.decls, decl{fmt.Sprintf("_en_%s : BOOL;", id), t.Pos.Line})
		g.decls = append(g.decls, decl{fmt.Sprintf("_f_%s : BOOL;", id), t.Pos.Line})
	}
	// Stored flags (one per S/R target) and final-scan memories (one per level
	// body action) — collected during the action pass but declared here.
	for _, name := range g.storedTargets() {
		g.decls = append(g.decls, decl{fmt.Sprintf("%s : BOOL;", g.stored(name)), g.bodyLine})
	}
	for _, name := range g.levelBodyActions() {
		g.decls = append(g.decls, decl{fmt.Sprintf("%s : BOOL;", g.prevMem(name)), g.bodyLine})
	}
	for _, name := range g.drivenBoolTargets() {
		g.decls = append(g.decls, decl{fmt.Sprintf("%s : BOOL;", g.lvlMem(name)), g.bodyLine})
	}
}

// drivenBoolTargets returns the canonical names of boolean-variable targets
// carrying at least one N/P/P1/P0 association, which need a one-scan memory of
// their drive signal for the final-scan write (§2.5).
func (g *gen) drivenBoolTargets() []string {
	seen := map[string]bool{}
	var out []string
	for _, sa := range g.assocs() {
		if g.actionSet[strings.ToUpper(sa.a.Target)] || sa.a.Qualifier == "S" || sa.a.Qualifier == "R" {
			continue
		}
		key := strings.ToUpper(sa.a.Target)
		if !seen[key] {
			seen[key] = true
			out = append(out, g.canon(sa.a.Target))
		}
	}
	return out
}

// storedTargets returns, in first-encounter order, the canonical names of every
// action target that carries at least one S or R association.
func (g *gen) storedTargets() []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range g.prog.Steps {
		for _, a := range s.Actions {
			if a.Qualifier == "S" || a.Qualifier == "R" {
				key := strings.ToUpper(a.Target)
				if !seen[key] {
					seen[key] = true
					out = append(out, g.canon(a.Target))
				}
			}
		}
	}
	return out
}

// levelBodyActions returns the canonical names of body actions that carry at
// least one level (N or S/R-stored) association and therefore need a final-scan
// memory (§2.5.1).
func (g *gen) levelBodyActions() []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range g.prog.Steps {
		for _, a := range s.Actions {
			if !g.actionSet[strings.ToUpper(a.Target)] {
				continue // a boolean-variable action, not a body
			}
			if g.isLevel(a.Qualifier) {
				key := strings.ToUpper(a.Target)
				if !seen[key] {
					seen[key] = true
					out = append(out, g.canon(a.Target))
				}
			}
		}
	}
	return out
}

func (g *gen) isLevel(q string) bool  { return q == "N" || q == "S" || q == "R" }
func (g *gen) isPulse(q string) bool  { return q == "P" || q == "P1" || q == "P0" }

// riseEdge / fallEdge render a step's rising / falling edge signal.
func (g *gen) riseEdge(name string) string {
	return fmt.Sprintf("(%s AND NOT %s)", g.slot(name), g.prev(name))
}
func (g *gen) fallEdge(name string) string {
	return fmt.Sprintf("(%s AND NOT %s)", g.prev(name), g.slot(name))
}

// ─── phase 1: transitions ────────────────────────────────────────────────────

func (g *gen) emitTransitions() {
	g.comment("1. evaluate transitions from the pre-scan step-activity snapshot")
	for _, t := range g.prog.Transitions {
		g.emit(fmt.Sprintf("_en_%s := %s;", g.transID[t], g.enabledExpr(t)), t.Pos.Line)
	}
	g.comment("2. resolve firing in declaration order (alternative-priority guard, §2.3)")
	for i, t := range g.prog.Transitions {
		guard := g.altGuard(i)
		if guard == "" {
			g.emit(fmt.Sprintf("_f_%s := _en_%s;", g.transID[t], g.transID[t]), t.Pos.Line)
		} else {
			g.emit(fmt.Sprintf("_f_%s := _en_%s AND NOT (%s);", g.transID[t], g.transID[t], guard), t.Pos.Line)
		}
	}
}

// enabledExpr renders enabled(t) = AND(all sources' .X) AND (cond) — the
// simultaneous-convergence gate falls out for free (all sources ANDed, §2.4).
func (g *gen) enabledExpr(t *Transition) string {
	var parts []string
	for _, s := range t.From {
		parts = append(parts, g.slot(s))
	}
	cond := strings.TrimSpace(g.rewriteRefs(t.Cond.Text))
	if cond == "" {
		cond = "FALSE" // defensive: Check already errors on an empty condition
	}
	parts = append(parts, "("+cond+")")
	return strings.Join(parts, " AND ")
}

// altGuard renders NOT(...) content for transition index i: the OR of fire()
// of every HIGHER-priority (earlier) transition sharing a source with it
// (§2.3). _f_ is resolved in declaration order, so every term is already
// final when this one is computed. A transition is therefore suppressed only
// by a higher-priority transition that actually takes the contended token:
// neither a convergence whose other sources are inactive (not enabled) nor an
// enabled transition that is itself suppressed by a still-higher one — which
// makes a non-transitive shared-source group (A-B share a step, B-C share
// another, A-C nothing) resolve exactly instead of one scan late.
func (g *gen) altGuard(i int) string {
	ti := g.prog.Transitions[i]
	var terms []string
	for j := 0; j < i; j++ {
		if sharesSource(g.prog.Transitions[j], ti) {
			terms = append(terms, "_f_"+g.transID[g.prog.Transitions[j]])
		}
	}
	return strings.Join(terms, " OR ")
}

func sharesSource(a, b *Transition) bool {
	for _, x := range a.From {
		for _, y := range b.From {
			if strings.EqualFold(x, y) {
				return true
			}
		}
	}
	return false
}

// ─── phases 3–4: clears then sets (set-dominates-clear, §2.4) ─────────────────

func (g *gen) emitClears() {
	g.comment("3. deactivate sources of firing transitions (all clears first)")
	for _, t := range g.prog.Transitions {
		var sb strings.Builder
		for _, s := range t.From {
			fmt.Fprintf(&sb, "%s := FALSE; ", g.slot(s))
		}
		g.emit(fmt.Sprintf("IF _f_%s THEN %sEND_IF;", g.transID[t], sb.String()), t.Pos.Line)
	}
}

func (g *gen) emitSets() {
	g.comment("4. activate targets of firing transitions (after all clears: set dominates)")
	for _, t := range g.prog.Transitions {
		var sb strings.Builder
		for _, s := range t.To {
			fmt.Fprintf(&sb, "%s := TRUE; ", g.slot(s))
		}
		g.emit(fmt.Sprintf("IF _f_%s THEN %sEND_IF;", g.transID[t], sb.String()), t.Pos.Line)
	}
}

// ─── phase 5: hidden Step.T timers ───────────────────────────────────────────

func (g *gen) emitStepTimers() {
	var any bool
	for _, s := range g.stepOrder {
		if g.needTimer[strings.ToUpper(s.Name)] {
			any = true
			break
		}
	}
	if !any {
		return
	}
	capMs := g.ptCapMs()
	g.comment("5. update hidden Step.T timers from the new activity (§2.6)")
	for _, s := range g.stepOrder {
		if g.needTimer[strings.ToUpper(s.Name)] {
			g.emit(fmt.Sprintf("%s(IN := %s, PT := T#%dms);", g.timer(s.Name), g.slot(s.Name), capMs), s.Pos.Line)
		}
	}
}

// ─── phase 6: actions ────────────────────────────────────────────────────────

func (g *gen) emitActions() error {
	g.comment("6. actions — S/R stored flags, body actions, then boolean-variable associations")

	// 6a. stored-flag updates. All sets before all resets so a reset dominates a
	// simultaneous set (disjoint S/R steps are unambiguous either way, §2.5).
	for _, sa := range g.assocs() {
		if sa.a.Qualifier == "S" {
			g.emit(fmt.Sprintf("IF %s THEN %s := TRUE; END_IF;", g.riseEdge(sa.step.Name), g.stored(sa.a.Target)), sa.a.Pos.Line)
		}
	}
	for _, sa := range g.assocs() {
		if sa.a.Qualifier == "R" {
			g.emit(fmt.Sprintf("IF %s THEN %s := FALSE; END_IF;", g.riseEdge(sa.step.Name), g.stored(sa.a.Target)), sa.a.Pos.Line)
		}
	}

	// 6b. body actions (ACTION blocks) gated by their active signal.
	for _, name := range g.bodyTargets() {
		if err := g.emitBodyAction(name); err != nil {
			return err
		}
	}

	// 6c. boolean-variable targets, written LAST so an association's write
	// wins over an ACTION body's write to the same variable in the scans the
	// association acts (§2.5 "association vs ACTION writes"), and written ONLY
	// on the scans it acts — never recomputed from scratch every scan — so an
	// association on an inactive step never touches the variable.
	for _, name := range g.boolTargets() {
		g.emitBoolTarget(name)
	}
	return nil
}

// emitBoolTarget lowers every association on one boolean variable (§2.5):
//
//   - level/pulse drive (N while its step is active; P/P1 the scan its step
//     activates; P0 the scan it deactivates): the variable is held TRUE every
//     scan the OR of those signals is TRUE, and written back once on the scan
//     it falls (the final scan) — to FALSE, or to the S/R stored value when
//     the variable also carries S/R associations (IEC: Q = N OR stored);
//   - S / R: the variable is set / reset ONCE, on the scan the step activates.
//
// Outside those scans the variable is not written, so another writer (an
// ACTION body, another task, an HMI) owns it. Order within the scan: final
// scan, then S/R edges (so a store on the scan another step's N drops still
// leaves it set), then the N hold (so an active N wins over a reset).
func (g *gen) emitBoolTarget(name string) {
	key := strings.ToUpper(name)
	v := g.canon(name)
	var drive []string
	var stores []stepAssoc
	line := 0
	for _, sa := range g.assocs() {
		if strings.ToUpper(sa.a.Target) != key {
			continue
		}
		if line == 0 {
			line = sa.a.Pos.Line
		}
		switch sa.a.Qualifier {
		case "S", "R":
			stores = append(stores, sa)
		default:
			drive = append(drive, g.activeSignal(sa))
		}
	}
	if line == 0 {
		line = g.bodyLine
	}
	lvl := strings.Join(drive, " OR ")
	if len(drive) > 0 {
		final := "FALSE"
		if len(stores) > 0 {
			final = g.stored(name)
		}
		g.emit(fmt.Sprintf("IF %s AND NOT (%s) THEN %s := %s; END_IF;", g.lvlMem(name), lvl, v, final), line)
	}
	for _, sa := range stores {
		if sa.a.Qualifier == "S" {
			g.emit(fmt.Sprintf("IF %s THEN %s := TRUE; END_IF;", g.riseEdge(sa.step.Name), v), sa.a.Pos.Line)
		}
	}
	for _, sa := range stores {
		if sa.a.Qualifier == "R" {
			g.emit(fmt.Sprintf("IF %s THEN %s := FALSE; END_IF;", g.riseEdge(sa.step.Name), v), sa.a.Pos.Line)
		}
	}
	if len(drive) > 0 {
		g.emit(fmt.Sprintf("IF %s THEN %s := TRUE; END_IF;", lvl, v), line)
		g.emit(fmt.Sprintf("%s := %s;", g.lvlMem(name), lvl), line)
	}
}

// assocs flattens all (step, association) pairs in source order.
func (g *gen) assocs() []stepAssoc {
	var out []stepAssoc
	for _, s := range g.prog.Steps {
		for _, a := range s.Actions {
			out = append(out, stepAssoc{s, a})
		}
	}
	return out
}

// activeSignal renders one association's contribution to its target's activity.
func (g *gen) activeSignal(sa stepAssoc) string {
	switch sa.a.Qualifier {
	case "N":
		return g.slot(sa.step.Name)
	case "S", "R":
		return g.stored(sa.a.Target)
	case "P", "P1":
		return g.riseEdge(sa.step.Name)
	case "P0":
		return g.fallEdge(sa.step.Name)
	}
	return "FALSE"
}

// boolTargets / bodyTargets return, in first-encounter order, the canonical
// names of action targets that are (respectively) plain declared variables and
// ACTION blocks.
func (g *gen) boolTargets() []string { return g.targetsWhere(false) }
func (g *gen) bodyTargets() []string { return g.targetsWhere(true) }

func (g *gen) targetsWhere(body bool) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range g.prog.Steps {
		for _, a := range s.Actions {
			isBody := g.actionSet[strings.ToUpper(a.Target)]
			if isBody != body {
				continue
			}
			key := strings.ToUpper(a.Target)
			if !seen[key] {
				seen[key] = true
				out = append(out, g.canon(a.Target))
			}
		}
	}
	return out
}

// emitBodyAction renders an ACTION block's body gated by its combined active
// signal. Level associations (N, S/R-stored) get the §2.5.1 final-scan wrapper
// (`IF active OR _prev THEN body; _prev := active;`) so the body runs one extra
// scan on the falling edge; a pulse-only body (P/P1/P0) runs guarded with no
// final scan. A body mixing level and pulse folds the pulse terms into the
// level active signal (slice-1 simplification — documented).
func (g *gen) emitBodyAction(name string) error {
	key := strings.ToUpper(name)
	var ab *ActionBlock
	for _, a := range g.prog.Actions {
		if strings.ToUpper(a.Name) == key {
			ab = a
			break
		}
	}
	if ab == nil {
		return fmt.Errorf("sfc: action block %q referenced but not defined", name)
	}

	var terms []string
	hasLevel := false
	storedAdded := false
	for _, sa := range g.assocs() {
		if strings.ToUpper(sa.a.Target) != key {
			continue
		}
		if g.isLevel(sa.a.Qualifier) {
			hasLevel = true
		}
		if sa.a.Qualifier == "S" || sa.a.Qualifier == "R" {
			if !storedAdded {
				terms = append(terms, g.stored(name))
				storedAdded = true
			}
			continue
		}
		terms = append(terms, g.activeSignal(sa))
	}
	if len(terms) == 0 {
		return nil // no live associations
	}
	active := strings.Join(terms, " OR ")

	body := g.rewriteRefs(ab.Body.Text)
	bodyLines := strings.Split(body, "\n")

	if hasLevel {
		g.emit(fmt.Sprintf("IF (%s) OR %s THEN", active, g.prevMem(name)), ab.Pos.Line)
	} else {
		g.emit(fmt.Sprintf("IF (%s) THEN", active), ab.Pos.Line)
	}
	for k, bl := range bodyLines {
		g.emit("  "+bl, ab.Body.Line+k)
	}
	g.emit("END_IF;", ab.Pos.Line)
	if hasLevel {
		g.emit(fmt.Sprintf("%s := (%s);", g.prevMem(name), active), ab.Pos.Line)
	}
	return nil
}

// ─── phase 6d: advance per-step edge memories (after all actions read them) ───

func (g *gen) emitEdgeUpdates() {
	var any bool
	for _, s := range g.stepOrder {
		if g.needPrev[strings.ToUpper(s.Name)] {
			any = true
			break
		}
	}
	if !any {
		return
	}
	g.comment("advance step edge memories for next scan's pulses / stored edges")
	for _, s := range g.stepOrder {
		if g.needPrev[strings.ToUpper(s.Name)] {
			g.emit(fmt.Sprintf("%s := %s;", g.prev(s.Name), g.slot(s.Name)), s.Pos.Line)
		}
	}
}

// ─── shared: rewrite Step.X / Step.T references in verbatim ST spans ──────────

// rewriteRefs rewrites `<step>.X` -> `_S_<step>_X` and `<step>.T` ->
// `_S_<step>_t.ET` inside a verbatim ST span (a transition condition or action
// body), leaving every other identifier untouched. A match whose base is not a
// declared step (an ordinary variable or struct field) passes through, and a
// dotted access like `foo.Mix.X` is not treated as a step ref. Replacements are
// within-line, so line offsets — and thus the line map — are preserved.
func (g *gen) rewriteRefs(text string) string {
	locs := stepRefRe.FindAllStringSubmatchIndex(text, -1)
	if len(locs) == 0 {
		return text
	}
	var b strings.Builder
	last := 0
	for _, loc := range locs {
		name := text[loc[2]:loc[3]]
		suffix := text[loc[4]:loc[5]]
		s := g.stepByUpper[strings.ToUpper(name)]
		dotted := loc[0] > 0 && text[loc[0]-1] == '.'
		if s == nil || dotted {
			continue // leave this match as-is
		}
		b.WriteString(text[last:loc[0]])
		if suffix == "X" {
			b.WriteString(g.slot(s.Name))
		} else {
			b.WriteString(g.timer(s.Name) + ".ET")
		}
		last = loc[1]
	}
	b.WriteString(text[last:])
	return b.String()
}
