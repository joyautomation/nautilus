package sfc

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/joyautomation/nautilus/lang/st"
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

// untimedQualifiers take no time argument; timedQualifiers require one (the
// IEC 61131-3 action-control qualifiers with a duration: time Limited,
// time Delayed, Stored-and-Delayed, Delayed-and-Stored, Stored-and-time-
// Limited — design §2.5). Every one of them is implemented.
var (
	untimedQualifiers = map[string]bool{"N": true, "S": true, "R": true, "P": true, "P0": true, "P1": true}
	timedQualifiers   = map[string]bool{"L": true, "D": true, "SD": true, "DS": true, "SL": true}
)

// IsTimedQualifier reports whether q (upper-case) is one of the timed
// qualifiers L, D, SD, DS, SL, which take a duration.
func IsTimedQualifier(q string) bool { return timedQualifiers[q] }

// qualifierHelp is the tail of every qualifier diagnostic: the whole
// supported set, and the Step.T recipe for timing the qualifiers can't say.
const qualifierHelp = "supported qualifiers are N, S, R, P, P0, P1 and the timed L, D, SD, DS, SL (`D Valve(T#3S);` or IEC's `Valve(D, T#3S);`); " +
	"for timing none of them expresses, associate N with an ACTION that reads the step's elapsed time (`Valve := Fill.X AND Fill.T >= T#3S;`)"

// durationRe accepts what a timed association or MAXTIME can take: a TIME
// literal (T#3S, TIME#1m30s, t#250ms) or, for a timed association only, a
// variable or constant name holding a TIME.
var (
	timeLiteralRe = regexp.MustCompile(`(?i)^(T|TIME)#-?[0-9][0-9a-z_.]*$`)
	durationIdent = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

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

	// ── unreachable / dead-end steps (warn) ───────────────────────────────
	// Both are incomplete wiring, not a malformed chart: an unreachable
	// step simply never activates (dead code — the chart still compiles and
	// runs exactly as wired). A step added or pasted in the editor is
	// unreachable until its first transition lands; reading that as a hard
	// error failed check on every in-progress edit.
	for _, s := range prog.Steps {
		key := strings.ToUpper(s.Name)
		if !s.Initial && !isTarget[key] {
			add(s.Pos, SeverityWarning, "step %q is unreachable: no transition's TO targets it", s.Name)
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
			switch {
			case timedQualifiers[a.Qualifier]:
				switch {
				case a.Time == "":
					add(a.Pos, SeverityError, "timed qualifier %s needs a duration: `%s %s(T#3S);` (or IEC's `%s(%s, T#3S);`)", a.Qualifier, a.Qualifier, a.Target, a.Target, a.Qualifier)
				case !timeLiteralRe.MatchString(a.Time) && !durationIdent.MatchString(a.Time):
					add(a.Pos, SeverityError, "timed qualifier %s: %q is not a duration — write a TIME literal like T#3S, or the name of a TIME variable", a.Qualifier, a.Time)
				}
			case untimedQualifiers[a.Qualifier]:
				if a.Time != "" {
					// The parser keeps the `(time)` argument so the editor can
					// round-trip it, but this qualifier never reads it: refuse
					// rather than run a chart that silently ignores a duration.
					add(a.Pos, SeverityError, "qualifier %s does not take a time argument (%q); only the timed qualifiers L, D, SD, DS, SL do", a.Qualifier, a.Time)
				}
			default:
				add(a.Pos, SeverityError, "unknown action qualifier %q; %s", a.Qualifier, qualifierHelp)
			}
			key := strings.ToUpper(a.Target)
			if actionByName[key] == nil && !varNames[key] {
				add(a.Pos, SeverityError, "step %s: action association %s %s references neither an ACTION block nor a declared variable", s.Name, a.Qualifier, a.Target)
			}
		}
	}

	// ── step attributes: MAXTIME (supervision) and ERROR (its flag's tag) ──
	for _, s := range prog.Steps {
		seen := map[string]bool{}
		for _, at := range s.Attrs {
			if seen[at.Name] {
				add(at.Pos, SeverityError, "step %s: attribute %s is given twice", s.Name, at.Name)
				continue
			}
			seen[at.Name] = true
			switch at.Name {
			case "MAXTIME":
				if !timeLiteralRe.MatchString(at.Value) || st.ParseTimeMs(strings.SplitN(strings.ToUpper(at.Value), "#", 2)[1]) <= 0 {
					add(at.Pos, SeverityError, "step %s: MAXTIME takes a positive TIME literal, e.g. (MAXTIME := T#30S); got %q", s.Name, at.Value)
				}
			case "ERROR":
				if !durationIdent.MatchString(at.Value) || !varNames[strings.ToUpper(at.Value)] {
					add(at.Pos, SeverityError, "step %s: ERROR names the BOOL variable (usually a tag, for an alarm) that mirrors %s.ERR; %q is not a declared variable", s.Name, s.Name, at.Value)
				}
			default:
				add(at.Pos, SeverityError, "step %s: unknown step attribute %s; a step takes MAXTIME := <TIME> (its supervision limit) and ERROR := <BOOL variable> (where the overrun flag goes)", s.Name, at.Name)
			}
		}
		if seen["ERROR"] && !seen["MAXTIME"] {
			add(s.Pos, SeverityError, "step %s: ERROR without MAXTIME — the error flag is set when the step stays active longer than its MAXTIME", s.Name)
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
	for _, s := range prog.Steps {
		if v := s.Attr("ERROR"); v != "" && stepByName[strings.ToUpper(v)] != nil {
			add(s.Pos, SeverityError, "step %s: ERROR := %s names a step; it takes a BOOL variable", s.Name, v)
		}
	}

	// ── simultaneous-convergence sources reachable from a common
	// simultaneous divergence (warn) ───────────────────────────────────────
	diags = append(diags, checkConvergenceReachability(prog)...)

	// ── a variable written both by a qualifier association and inside an
	// ACTION body (warn) ──────────────────────────────────────────────────
	diags = append(diags, checkAssocBodyWrites(prog, actionByName)...)

	sort.SliceStable(diags, func(i, j int) bool {
		if diags[i].Pos.Line != diags[j].Pos.Line {
			return diags[i].Pos.Line < diags[j].Pos.Line
		}
		return diags[i].Pos.Col < diags[j].Pos.Col
	})
	return diags
}

var stepRefRe = regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_]*)\.((?i:X|T|ERR))\b`)

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
		suffix := strings.ToUpper(sp.Text[loc[4]:loc[5]])
		key := strings.ToUpper(name)
		if s := stepByName[key]; s != nil {
			if suffix == "ERR" && s.MaxTime() == "" && !(loc[0] > 0 && sp.Text[loc[0]-1] == '.') {
				diags = append(diags, Diagnostic{Pos: spanOffsetPos(sp, loc[0]), Severity: SeverityError,
					Message: fmt.Sprintf("%s: %s.ERR is the step's overrun flag, but step %s has no MAXTIME — write STEP %s (MAXTIME := T#30S):", context, name, s.Name, s.Name)})
			}
			continue
		}
		if varNames[key] {
			continue
		}
		if suffix == "ERR" {
			continue // an ordinary struct member named ERR, not a step reference
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

// Non-transitive alternative-priority groups (A shares a step with B, B with
// C, A and C nothing) used to be flagged here as ambiguous. They are not: the
// §2.3 guard resolves firing in declaration order and suppresses a transition
// only by a higher-priority transition sharing a source that itself FIRES, so
// every such group has one well-defined outcome and needs no diagnostic.

// checkAssocBodyWrites warns when a boolean variable is the target of a bare
// qualifier association (N/S/R/P/P1/P0/L/D/SD/DS/SL X) AND is assigned inside
// an ACTION body. Both are legal and the rule is fixed (§2.5): the association
// writes the variable last in the scan, but only on the scans it acts — N
// while its step is active (plus the one final scan), S/R once on its step's
// activation, a pulse on its one edge scan, a timed qualifier over its timed
// window — and the variable is the ACTION's otherwise. The message states the
// window for the qualifier actually written; an unknown qualifier gets no
// warning (its own error says what is wrong).
// It is worth a warning because it reads as "two owners" and a test that only
// samples a few scans can't tell which one produced a value.
func checkAssocBodyWrites(prog *Program, actionByName map[string]*ActionBlock) []Diagnostic {
	if len(prog.Actions) == 0 {
		return nil
	}
	// Which ACTION bodies assign which identifiers.
	assigned := map[string][]string{} // upper var name -> ACTION names, in declaration order
	for _, a := range prog.Actions {
		for _, v := range assignedIdents(a.Body.Text) {
			assigned[v] = append(assigned[v], a.Name)
		}
	}
	var diags []Diagnostic
	for _, s := range prog.Steps {
		for _, a := range s.Actions {
			key := strings.ToUpper(a.Target)
			if actionByName[key] != nil {
				continue // an ACTION-block association, not a variable
			}
			writers := assigned[key]
			if len(writers) == 0 {
				continue
			}
			var rule string
			switch a.Qualifier {
			case "N":
				rule = fmt.Sprintf("the association wins while %s is active (and on the scan it deactivates, when it writes FALSE); the ACTION's writes stand otherwise", s.Name)
			case "S", "R":
				verb := "sets"
				if a.Qualifier == "R" {
					verb = "resets"
				}
				rule = fmt.Sprintf("the association %s it once, on the scan %s activates; the ACTION's writes stand otherwise", verb, s.Name)
			case "P", "P1", "P0":
				edge := "activates"
				if a.Qualifier == "P0" {
					edge = "deactivates"
				}
				rule = fmt.Sprintf("the association wins on its one pulse scan, the scan %s %s (and writes FALSE the scan after); the ACTION's writes stand otherwise", s.Name, edge)
			case "L":
				rule = fmt.Sprintf("the association wins for the first %s %s is active (and writes FALSE once when that ends); the ACTION's writes stand otherwise", a.Time, s.Name)
			case "D":
				rule = fmt.Sprintf("the association wins once %s has been active %s, until it deactivates (and writes FALSE once then); the ACTION's writes stand otherwise", s.Name, a.Time)
			case "SD":
				rule = fmt.Sprintf("the association wins from %s after %s activates until an R resets it, even after the step is left (and writes FALSE once then); the ACTION's writes stand otherwise", a.Time, s.Name)
			case "DS":
				rule = fmt.Sprintf("the association wins once %s has been active %s, and keeps winning after the step is left until an R resets it (and writes FALSE once then); the ACTION's writes stand otherwise", s.Name, a.Time)
			case "SL":
				rule = fmt.Sprintf("the association wins for %s from %s's activation, even after the step is left, or until an R (and writes FALSE once then); the ACTION's writes stand otherwise", a.Time, s.Name)
			default:
				continue // an unknown qualifier: Check already reports it, and it has no rule to describe
			}
			diags = append(diags, Diagnostic{Pos: a.Pos, Severity: SeverityWarning,
				Message: fmt.Sprintf("%s is driven by a qualifier association (%s) on step %s and assigned in ACTION %s — %s",
					a.Target, a.Qualifier, s.Name, strings.Join(writers, ", "), rule)})
		}
	}
	return diags
}

var (
	blockCommentRe = regexp.MustCompile(`(?s)\(\*.*?\*\)`)
	lineCommentRe  = regexp.MustCompile(`//[^\n]*`)
	stringLitRe    = regexp.MustCompile(`'(?:[^'$]|\$.)*'|"(?:[^"$]|\$.)*"`)
	assignRe       = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*)\s*:=`)
)

// assignedIdents returns the (upper-cased, de-duplicated) plain identifiers
// an ST statement list assigns: `X := ...` at statement level, outside
// comments and string literals. Named arguments of a call (`fb(IN := X)`,
// inside parentheses) and member/element targets (`s.X :=`, `a[i] :=`) are
// not variable assignments and are skipped.
func assignedIdents(body string) []string {
	blank := func(re *regexp.Regexp, s string) string {
		return re.ReplaceAllStringFunc(s, func(m string) string { return strings.Repeat(" ", len(m)) })
	}
	text := blank(blockCommentRe, body)
	text = blank(lineCommentRe, text)
	text = blank(stringLitRe, text)
	// Parenthesis depth at every byte, so a named call argument is skipped.
	depth := make([]int, len(text)+1)
	d := 0
	for i := 0; i < len(text); i++ {
		depth[i] = d
		switch text[i] {
		case '(':
			d++
		case ')':
			if d > 0 {
				d--
			}
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, loc := range assignRe.FindAllStringSubmatchIndex(text, -1) {
		start := loc[2]
		if depth[start] != 0 {
			continue
		}
		if start > 0 {
			switch text[start-1] {
			case '.', ']':
				continue
			}
		}
		key := strings.ToUpper(text[loc[2]:loc[3]])
		if !seen[key] {
			seen[key] = true
			out = append(out, key)
		}
	}
	return out
}
