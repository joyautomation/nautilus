// Package lint holds the shared half of the diagram structure warnings
// `naut check` and the language server report for ladder, FBD and SFC
// sources (nautilus#561): the finding type, the rule catalog, the project's
// thresholds, and the `(* @allow … *)` pragma that switches a rule off for
// one POU. The rules themselves live with their language (ld.Lint,
// fbd.Lint, sfc.Lint), where the parsed program is.
//
// A program that compiles can still draw as a knot. The diagrams are laid
// out from the text, so the text's structure alone decides whether they
// read: these rules turn the structure that draws badly into warnings that
// say what to write instead. They are deterministic, cheap (one parse of
// the file), and chosen to be precise: each fires on a shape that draws
// badly or hides the logic, not on a matter of taste.
package lint

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/joyautomation/nautilus/lang/fbcatalog"
)

// Finding is one structure warning, positioned in the checked file.
type Finding struct {
	Rule    string // stable ID: LD001, FBD002, SFC001, …
	Line    int    // 1-based
	Col     int    // 1-based; 1 when the rule marks a whole line
	Message string // what is wrong and what to write instead
}

func (f Finding) String() string {
	return fmt.Sprintf("%d:%d: %s [%s]", f.Line, f.Col, f.Message, f.Rule)
}

// Rule is one entry of the catalog.
type Rule struct {
	ID      string
	Summary string
}

// Rules is every structure rule, in ID order. The reference page
// (docs/structure-warnings.md, synced to the website) documents each
// one; a test holds the two together.
var Rules = []Rule{
	{"LD001", "a BOOL driven by coils on more than one rung, or a plain coil mixed with set/reset coils"},
	{"LD002", "branches nested deeper than max-branch-depth"},
	{"LD003", "more than max-series elements in series on one rung"},
	{"LD004", "the same contact on every leg of a branch"},
	{"LD005", "a rung with no comment"},
	{"LD006", "a comparison contact against a literal threshold"},
	{"FBD001", "calls nested deeper than max-nesting"},
	{"FBD002", "an untitled network holding more than one function"},
	{"FBD003", "a comparison block against a literal threshold"},
	{"SFC001", "a transition that only tests a state variable the chart's own actions set"},
	{"SFC002", "a step named S1, Step2, … instead of for its process state"},
}

// Known reports whether id names a rule (case-insensitive).
func Known(id string) bool {
	id = strings.ToUpper(id)
	for _, r := range Rules {
		if r.ID == id {
			return true
		}
	}
	return false
}

// The default thresholds. Each is the largest structure the AGENTS.md
// investigation (#556) found still draws cleanly.
const (
	DefaultMaxSeries      = 6 // LD003
	DefaultMaxBranchDepth = 1 // LD002
	DefaultMaxNesting     = 2 // FBD001
)

// Config is a project's structure-rule settings: the manifest's `check:`
// section. The zero value is not usable; start from Defaults.
type Config struct {
	Disabled       map[string]bool // rule IDs not reported in this project
	MaxSeries      int
	MaxBranchDepth int
	MaxNesting     int
}

// Defaults is every rule on, at the default thresholds.
func Defaults() Config {
	return Config{MaxSeries: DefaultMaxSeries, MaxBranchDepth: DefaultMaxBranchDepth, MaxNesting: DefaultMaxNesting}
}

// On reports whether rule id is reported in this project.
func (c Config) On(id string) bool { return !c.Disabled[id] }

// ── the @allow pragma ──────────────────────────────────────────────────────

var (
	allowRe   = regexp.MustCompile(`\(\*\s*@allow\b((?:[\s,]+[A-Za-z]+[0-9]+)+)`)
	ruleIDRe  = regexp.MustCompile(`[A-Za-z]+[0-9]+`)
	pouOpenRe = regexp.MustCompile(`(?i)^\s*(PROGRAM|FUNCTION_BLOCK|FUNCTION)\b`)
	pouEndRe  = regexp.MustCompile(`(?i)^\s*END_(PROGRAM|FUNCTION_BLOCK|FUNCTION)\b`)
)

// allowance is one pragma: the rules it switches off and the lines it
// covers (the POU it sits in, or the whole file outside every POU).
type allowance struct {
	rules    map[string]bool
	from, to int
}

// Filter drops the findings cfg disables and those an `(* @allow ID … *)`
// pragma in src switches off, and sorts the rest by position.
//
// A pragma covers the POU it is written in — put it in the header, next to
// the POU's description comment — so it never reaches past END_PROGRAM or
// END_FUNCTION_BLOCK. One written outside every POU (above the first) covers
// the whole file. Text after the IDs is a free reason:
//
//	(* @allow LD003 LD005: the vendor's rungs, kept as imported *)
func Filter(src string, cfg Config, fs []Finding) []Finding {
	allows := pragmas(src)
	out := fs[:0:0]
	for _, f := range fs {
		if !cfg.On(f.Rule) || allowed(allows, f) {
			continue
		}
		out = append(out, f)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Line != out[j].Line {
			return out[i].Line < out[j].Line
		}
		return out[i].Col < out[j].Col
	})
	return out
}

func allowed(allows []allowance, f Finding) bool {
	for _, a := range allows {
		if a.rules[f.Rule] && f.Line >= a.from && f.Line <= a.to {
			return true
		}
	}
	return false
}

// pragmas finds every @allow pragma in src and the lines it covers.
func pragmas(src string) []allowance {
	if !strings.Contains(src, "@allow") {
		return nil
	}
	lines := strings.Split(src, "\n")
	// POU spans, read on comment-stripped text so a commented-out header
	// does not open one.
	type span struct{ from, to int }
	var pous []span
	open := 0
	for i, l := range strings.Split(fbcatalog.StripComments(src), "\n") {
		switch {
		case pouEndRe.MatchString(l):
			if open > 0 {
				pous = append(pous, span{open, i + 1})
				open = 0
			}
		case pouOpenRe.MatchString(l):
			open = i + 1
		}
	}
	if open > 0 {
		pous = append(pous, span{open, len(lines)})
	}
	var out []allowance
	for _, loc := range allowRe.FindAllStringSubmatchIndex(src, -1) {
		line := strings.Count(src[:loc[0]], "\n") + 1
		a := allowance{rules: map[string]bool{}, from: 1, to: len(lines)}
		for _, id := range ruleIDRe.FindAllString(src[loc[2]:loc[3]], -1) {
			a.rules[strings.ToUpper(id)] = true
		}
		for _, p := range pous {
			if line >= p.from && line <= p.to {
				a.from, a.to = p.from, p.to
				break
			}
		}
		out = append(out, a)
	}
	return out
}

// ── shared rule helpers ────────────────────────────────────────────────────

// thresholdCmp are the ordering comparisons: the blocks that test a value
// against a threshold, where a literal is a setpoint nobody can tune.
var thresholdCmp = map[string]bool{"GT": true, "GE": true, "LT": true, "LE": true}

// IsThresholdCompare reports whether fn (any case) is GT, GE, LT or LE.
func IsThresholdCompare(fn string) bool { return thresholdCmp[strings.ToUpper(fn)] }

var numLitRe = regexp.MustCompile(`^(?i)(?:[A-Z]+#)?[+-]?(?:[0-9][0-9_]*)(?:\.[0-9_]+)?(?:E[+-]?[0-9]+)?$`)

// MagicNumber reports whether arg is a numeric literal that a threshold
// should not be: any number but zero (`0`, `0.0`, `REAL#0`), which reads as
// itself. TIME literals are not numbers here: a delay written in the block
// that times it (`PT := T#5S`) is the usual, readable form.
func MagicNumber(arg string) bool {
	a := strings.TrimSpace(arg)
	if !numLitRe.MatchString(a) {
		return false
	}
	if i := strings.IndexByte(a, '#'); i >= 0 {
		a = a[i+1:]
	}
	v, err := strconv.ParseFloat(strings.ReplaceAll(a, "_", ""), 64)
	return err == nil && v != 0
}

// ThresholdMessage is the shared LD006/FBD003 text.
func ThresholdMessage(call, lit string) string {
	return fmt.Sprintf("%s compares against the literal %s: make the threshold a setpoint tag "+
		"(role: setpoint, with init: %s and a desc:) or a named VAR CONSTANT, and compare against that name",
		call, lit, lit)
}
