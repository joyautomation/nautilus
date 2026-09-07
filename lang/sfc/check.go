package sfc

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Severity is a structural diagnostic's level.
type Severity int

const (
	SeverityError Severity = iota
	SeverityWarning
)

func (s Severity) String() string {
	if s == SeverityWarning {
		return "warning"
	}
	return "error"
}

// Diagnostic is one structural-check finding, positioned on the offending
// .sfc construct — the same "diagnostics carry positions" convention
// lang/st's LowerError follows (see lang/st/diag.go).
type Diagnostic struct {
	Pos      Pos
	Severity Severity
	Message  string
}

func (d Diagnostic) String() string {
	return fmt.Sprintf("%d:%d: %s: %s", d.Pos.Line, d.Pos.Col, d.Severity, d.Message)
}

// supportedQualifiers are the action qualifiers the transpiler implements
// (design §2.5). None of them takes a time argument.
var supportedQualifiers = map[string]bool{"N": true, "S": true, "R": true, "P": true, "P0": true, "P1": true}

// timedQualifiers are the IEC qualifiers that take a time argument and are
// not implemented (§2.5, §7) — distinguished from a genuinely unknown token
// so the diagnostic says "not implemented" rather than "unknown".
var timedQualifiers = map[string]bool{"L": true, "D": true, "SD": true, "DS": true, "SL": true}

// Check runs the structural checks of design doc §5.1 against a parsed
// chart and returns every finding, positioned and sorted by location.
// It does not parse or lower transition conditions / action bodies (that's
// the ST-level hop — Transpile, then st.Parse/st.Lower — which is Slice B);
// this is purely chart-shape validation.
func Check(prog *Program) []Diagnostic {
	var diags []Diagnostic
	add := func(pos Pos, sev Severity, format string, args ...any) {
		diags = append(diags, Diagnostic{Pos: pos, Severity: sev, Message: fmt.Sprintf(format, args...)})
	}

	// ── step name uniqueness + exactly one INITIAL_STEP ──────────────────
	stepByName := map[string]*Step{}
	var initialSteps []*Step
	for _, s := range prog.Steps {
		key := strings.ToUpper(s.Name)
		if existing, ok := stepByName[key]; ok {
			add(s.Pos, SeverityError, "duplicate step name %q (also declared at line %d)", s.Name, existing.Pos.Line)
		} else {
			stepByName[key] = s
		}
		if s.Initial {
			initialSteps = append(initialSteps, s)
		}
	}
	switch len(initialSteps) {
	case 0:
		add(prog.Pos, SeverityError, "no INITIAL_STEP declared — a chart requires exactly one")
	case 1:
		// fine
	default:
		for _, s := range initialSteps[1:] {
			add(s.Pos, SeverityError, "multiple INITIAL_STEP declarations (first at line %d) — a chart requires exactly one", initialSteps[0].Pos.Line)
		}
	}

	// ── action block name uniqueness ─────────────────────────────────────
	actionByName := map[string]*ActionBlock{}
	for _, a := range prog.Actions {
		key := strings.ToUpper(a.Name)
		if existing, ok := actionByName[key]; ok {
			add(a.Pos, SeverityError, "duplicate ACTION name %q (also declared at line %d)", a.Name, existing.Pos.Line)
		} else {
			actionByName[key] = a
		}
	}

	// ── transition name uniqueness (named transitions only) ──────────────
	trByName := map[string]*Transition{}
	for _, t := range prog.Transitions {
		if t.Name == "" {
			continue
		}
		key := strings.ToUpper(t.Name)
		if existing, ok := trByName[key]; ok {
			add(t.Pos, SeverityError, "duplicate TRANSITION name %q (also declared at line %d)", t.Name, existing.Pos.Line)
		} else {
			trByName[key] = t
		}
	}

	// ── declared variable names (any VAR* kind), for assoc-target and
	// Step.X/.T resolution ────────────────────────────────────────────────
	varNames := map[string]bool{}
	for _, vb := range prog.VarBlocks {
		for _, v := range vb.Variables {
			varNames[strings.ToUpper(v.Name)] = true
		}
	}

	// ── FROM/TO resolve to existing steps; empty condition; reachability
	// bookkeeping ─────────────────────────────────────────────────────────
	isTarget := map[string]bool{} // step -> is some transition's TO member
	isSource := map[string]bool{} // step -> is some transition's FROM member
	for _, t := range prog.Transitions {
		for _, name := range t.From {
			if stepByName[strings.ToUpper(name)] == nil {
				add(t.Pos, SeverityError, "transition %s: FROM references unknown step %q", trName(t), name)
			} else {
				isSource[strings.ToUpper(name)] = true
			}
		}
		for _, name := range t.To {
			if stepByName[strings.ToUpper(name)] == nil {
				add(t.Pos, SeverityError, "transition %s: TO references unknown step %q", trName(t), name)
			} else {
				isTarget[strings.ToUpper(name)] = true
			}
		}
		if strings.TrimSpace(t.Cond.Text) == "" {
			pos := t.Pos
			if t.Cond.Line != 0 {
				pos = Pos{Line: t.Cond.Line, Col: t.Cond.Col}
			}
			add(pos, SeverityError, "transition %s: empty condition", trName(t))
		}
	}

	// ── unreachable (error) / dead-end (warn) steps ───────────────────────
	for _, s := range prog.Steps {
		key := strings.ToUpper(s.Name)
		if !s.Initial && !isTarget[key] {
			add(s.Pos, SeverityError, "step %q is unreachable: no transition's TO targets it", s.Name)
		}
		// A chart that is one INITIAL_STEP and nothing else is a valid
		// degenerate chart (one continuously active step), so the dead-end
		// warning is for charts that have somewhere else to go.
		if !isSource[key] && len(prog.Steps) > 1 {
			add(s.Pos, SeverityWarning, "step %q is a dead end: no transition's FROM sources it", s.Name)
		}
	}

	// ── action associations: qualifier support + target resolution ───────
	for _, s := range prog.Steps {
		for _, a := range s.Actions {
			if !supportedQualifiers[a.Qualifier] {
				if timedQualifiers[a.Qualifier] {
					add(a.Pos, SeverityError, "timed qualifier %q is not implemented; supported qualifiers are N, S, R, P, P0, P1", a.Qualifier)
				} else {
					add(a.Pos, SeverityError, "unknown action qualifier %q; supported qualifiers are N, S, R, P, P0, P1", a.Qualifier)
				}
			} else if a.Time != "" {
				// The parser keeps the `(time)` argument so the editor can
				// round-trip it, but no supported qualifier reads it: refuse
				// rather than run a chart that silently ignores a duration.
				add(a.Pos, SeverityError, "qualifier %s does not take a time argument (%q); only the timed qualifiers L, D, SD, DS, SL do, and those are not implemented", a.Qualifier, a.Time)
			}
			key := strings.ToUpper(a.Target)
			if actionByName[key] == nil && !varNames[key] {
				add(a.Pos, SeverityError, "step %s: action association %s %s references neither an ACTION block nor a declared variable", s.Name, a.Qualifier, a.Target)
			}
		}
	}

	// ── Step.X / Step.T references to an unknown step, scanned in
	// condition/action-body text spans ────────────────────────────────────
	for _, t := range prog.Transitions {
		diags = append(diags, checkStepRefs(t.Cond, fmt.Sprintf("transition %s", trName(t)), stepByName, varNames)...)
	}
	for _, a := range prog.Actions {
		diags = append(diags, checkStepRefs(a.Body, fmt.Sprintf("action %s", a.Name), stepByName, varNames)...)
	}

	// ── simultaneous-convergence sources reachable from a common
	// simultaneous divergence (warn) ───────────────────────────────────────
	diags = append(diags, checkConvergenceReachability(prog)...)

	// ── ambiguous (non-transitive) alternative-priority groups (warn) ────
	diags = append(diags, checkAltGroups(prog)...)

	sort.SliceStable(diags, func(i, j int) bool {
		if diags[i].Pos.Line != diags[j].Pos.Line {
			return diags[i].Pos.Line < diags[j].Pos.Line
		}
		return diags[i].Pos.Col < diags[j].Pos.Col
	})
	return diags
}

var stepRefRe = regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_]*)\.(X|T)\b`)

// checkStepRefs scans a text span for `Ident.X` / `Ident.T` references and
// flags any whose base identifier resolves to neither a declared step nor a
// declared variable — almost always a misspelled step name, since .X/.T is
// otherwise meaningless (§2.6).
func checkStepRefs(sp Span, context string, stepByName map[string]*Step, varNames map[string]bool) []Diagnostic {
	if sp.Text == "" {
		return nil
	}
	var diags []Diagnostic
	for _, loc := range stepRefRe.FindAllStringSubmatchIndex(sp.Text, -1) {
		name := sp.Text[loc[2]:loc[3]]
		suffix := sp.Text[loc[4]:loc[5]]
		key := strings.ToUpper(name)
		if stepByName[key] != nil || varNames[key] {
			continue
		}
		pos := spanOffsetPos(sp, loc[0])
		diags = append(diags, Diagnostic{Pos: pos, Severity: SeverityError,
			Message: fmt.Sprintf("%s: %s.%s references unknown step %q", context, name, suffix, name)})
	}
	return diags
}

// spanOffsetPos converts a byte offset within a Span's Text back to an
// absolute (Line, Col) in the original file.
func spanOffsetPos(sp Span, offset int) Pos {
	prefix := sp.Text[:offset]
	nl := strings.Count(prefix, "\n")
	if nl == 0 {
		return Pos{Line: sp.Line, Col: sp.Col + offset}
	}
	lastNL := strings.LastIndex(prefix, "\n")
	return Pos{Line: sp.Line + nl, Col: offset - lastNL}
}

// checkConvergenceReachability implements the §5.1 warning for a
// simultaneous convergence (FROM (A,B,...)) whose sources are not
// structurally reachable from a common simultaneous divergence — i.e. the
// chart can't actually put concurrent tokens on all of them at once.
// Reachability is computed on the plain step graph (ignoring alt/priority
// gating, which is a runtime concern, not a structural one); a source set
// "counts" as covered when each source is reachable from a distinct target
// of the SAME divergence transition (bipartite matching).
func checkConvergenceReachability(prog *Program) []Diagnostic {
	adj := map[string][]string{}
	for _, t := range prog.Transitions {
		for _, from := range t.From {
			for _, to := range t.To {
				adj[strings.ToUpper(from)] = append(adj[strings.ToUpper(from)], strings.ToUpper(to))
			}
		}
	}
	reach := func(start string) map[string]bool {
		seen := map[string]bool{start: true}
		queue := []string{start}
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			for _, n := range adj[cur] {
				if !seen[n] {
					seen[n] = true
					queue = append(queue, n)
				}
			}
		}
		return seen
	}

	var divergences []*Transition
	for _, t := range prog.Transitions {
		if len(t.From) == 1 && len(t.To) > 1 {
			divergences = append(divergences, t)
		}
	}

	var diags []Diagnostic
	for _, t := range prog.Transitions {
		if len(t.From) <= 1 {
			continue // not a simultaneous convergence
		}
		covered := false
		for _, d := range divergences {
			reachSets := make([]map[string]bool, len(d.To))
			for i, tgt := range d.To {
				reachSets[i] = reach(strings.ToUpper(tgt))
			}
			if bipartiteCovers(t.From, reachSets) {
				covered = true
				break
			}
		}
		if !covered {
			diags = append(diags, Diagnostic{Pos: t.Pos, Severity: SeverityWarning,
				Message: fmt.Sprintf("transition %s: simultaneous convergence sources {%s} are not structurally reachable from a common simultaneous divergence", trName(t), strings.Join(t.From, ", "))})
		}
	}
	return diags
}

// bipartiteCovers reports whether every source can be assigned a distinct
// target (from reachSets, indexed the same as the divergence's targets)
// that reaches it — a standard augmenting-path bipartite match (Kuhn's
// algorithm); the graphs here are tiny so no faster algorithm is warranted.
func bipartiteCovers(sources []string, reachSets []map[string]bool) bool {
	if len(sources) > len(reachSets) {
		return false
	}
	matchOf := make([]int, len(reachSets)) // matchOf[j] = index into sources, or -1
	for i := range matchOf {
		matchOf[i] = -1
	}
	var tryAssign func(i int, visited []bool) bool
	tryAssign = func(i int, visited []bool) bool {
		for j := range reachSets {
			if visited[j] || !reachSets[j][strings.ToUpper(sources[i])] {
				continue
			}
			visited[j] = true
			if matchOf[j] == -1 || tryAssign(matchOf[j], visited) {
				matchOf[j] = i
				return true
			}
		}
		return false
	}
	matched := 0
	for i := range sources {
		if tryAssign(i, make([]bool, len(reachSets))) {
			matched++
		}
	}
	return matched == len(sources)
}

// checkAltGroups implements the §2.3/§5.1 warning for an ambiguous
// alternative-priority group: alt-groups are defined by "shares any
// source" (§2.3), which is not transitive, so a 3+ group where some pair
// doesn't directly share a source is flagged for the author to
// disambiguate by reordering or restructuring.
func checkAltGroups(prog *Program) []Diagnostic {
	n := len(prog.Transitions)
	shares := func(a, b *Transition) bool {
		for _, x := range a.From {
			for _, y := range b.From {
				if strings.EqualFold(x, y) {
					return true
				}
			}
		}
		return false
	}
	adj := make([][]bool, n)
	for i := range adj {
		adj[i] = make([]bool, n)
	}
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			if shares(prog.Transitions[i], prog.Transitions[j]) {
				adj[i][j], adj[j][i] = true, true
			}
		}
	}

	visited := make([]bool, n)
	var diags []Diagnostic
	for i := 0; i < n; i++ {
		if visited[i] {
			continue
		}
		comp := []int{i}
		visited[i] = true
		queue := []int{i}
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			for j := 0; j < n; j++ {
				if adj[cur][j] && !visited[j] {
					visited[j] = true
					comp = append(comp, j)
					queue = append(queue, j)
				}
			}
		}
		if len(comp) < 3 {
			continue
		}
		clique := true
	outer:
		for _, a := range comp {
			for _, b := range comp {
				if a != b && !adj[a][b] {
					clique = false
					break outer
				}
			}
		}
		if clique {
			continue // every pair shares a source directly — unambiguous pairwise priority
		}
		var names []string
		for _, idx := range comp {
			names = append(names, trName(prog.Transitions[idx]))
		}
		sort.Strings(names)
		diags = append(diags, Diagnostic{Pos: prog.Transitions[comp[0]].Pos, Severity: SeverityWarning,
			Message: fmt.Sprintf("ambiguous alternative-priority group (non-transitive shared-source overlap) among transitions %s — priority is resolved pairwise per docs/design/sfc.md §2.3; consider reordering or restructuring", strings.Join(names, ", "))})
	}
	return diags
}
