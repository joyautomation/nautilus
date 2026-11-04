package sfc

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/joyautomation/nautilus/lang/lint"
)

// Lint reports the SFC structure warnings (lang/lint, nautilus#561) for one
// .sfc source: SFC001 and SFC002, less what cfg disables and what an
// `(* @allow … *)` pragma switches off. A source that does not parse has
// no findings: its parse error is the diagnostic.
func Lint(src string, cfg lint.Config) []lint.Finding {
	prog, err := Parse(src)
	if err != nil {
		return nil
	}
	var fs []lint.Finding

	// SFC001: a transition that only compares a variable the chart's own
	// actions assign — the CASE machine written as a chart. The chart then
	// shows `State = 2` on every transition and the real conditions hide in
	// the action bodies' IFs.
	for _, t := range prog.Transitions {
		m := stateTestRe.FindStringSubmatch(strings.TrimSpace(stripCondComments(t.Cond.Text)))
		if m == nil {
			continue
		}
		v := m[1]
		if v == "" {
			v = m[4]
		}
		if !assignedInActions(prog, v) {
			continue
		}
		name := t.Name
		if name == "" {
			name = "from " + strings.Join(t.From, ", ")
		}
		fs = append(fs, lint.Finding{Rule: "SFC001", Line: t.Cond.Line, Col: t.Cond.Col, Message: fmt.Sprintf(
			"transition %s only tests %s, which this chart's own actions set: the chart then shows "+
				"%q and hides the real condition. Write the process condition in the transition "+
				"(LT101_Level >= FullSP, Fill.T >= T#30S), drive outputs with step associations, and "+
				"drop the state variable", name, v, strings.TrimSpace(t.Cond.Text))})
	}

	// SFC002: steps named for their number.
	for _, s := range prog.Steps {
		if placeholderStepRe.MatchString(s.Name) {
			fs = append(fs, lint.Finding{Rule: "SFC002", Line: s.Pos.Line, Col: s.Pos.Col, Message: fmt.Sprintf(
				"step %s is named for its number: name it for the state the process is in (Idle, "+
					"Fill, Heat, Drain), which is what the chart shows", s.Name)})
		}
	}
	return lint.Filter(src, cfg, fs)
}

var (
	// `State = 2`, `Step <> 3`, `Mode = Mode#Fill`, `SeqStep = 'Lead'`, in
	// either order, optionally parenthesised.
	stateLit    = `(?:[0-9]+|[A-Za-z_][A-Za-z0-9_]*#[A-Za-z_][A-Za-z0-9_]*|'[^']*')`
	stateVar    = `([A-Za-z_][A-Za-z0-9_.]*)`
	stateTestRe = regexp.MustCompile(`^\(?\s*(?:` + stateVar + `\s*(=|<>)\s*` + stateLit + `|` +
		stateLit + `\s*(=|<>)\s*` + stateVar + `)\s*\)?$`)
	placeholderStepRe = regexp.MustCompile(`(?i)^(?:s|step)_?[0-9]+$`)
	condCommentRe     = regexp.MustCompile(`\(\*.*?\*\)|//[^\n]*`)
)

func stripCondComments(s string) string { return condCommentRe.ReplaceAllString(s, "") }

// assignedInActions reports whether some ACTION body of the chart assigns v.
func assignedInActions(prog *Program, v string) bool {
	re, err := regexp.Compile(`(?i)(?:^|[^A-Za-z0-9_.])` + regexp.QuoteMeta(v) + `\s*:=`)
	if err != nil {
		return false
	}
	for _, a := range prog.Actions {
		if re.MatchString(a.Body.Text) {
			return true
		}
	}
	return false
}
