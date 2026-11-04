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
	{"SFC001", "a transition that only tests a state variable the chart's own actions step through constants"},
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
	allowRe   = regexp.MustCompile(`\(\*\s*@allow\b`)
	idTokRe   = regexp.MustCompile(`^[A-Za-z]+[0-9][A-Za-z0-9]*$`)
	pouOpenRe = regexp.MustCompile(`(?i)^\s*(PROGRAM|FUNCTION_BLOCK|FUNCTION)\b`)
	pouEndRe  = regexp.MustCompile(`(?i)^\s*END_(PROGRAM|FUNCTION_BLOCK|FUNCTION)\b`)
	rungRe    = regexp.MustCompile(`(?i)^\s*RUNG\b`)
	ldEndRe   = regexp.MustCompile(`(?i)^\s*END_LD\b`)
)

// PragmaRule is the rule name on the warning a pragma itself gets when it
// names an ID that is not a structure rule (a typo such as LD0O5). It is
// not in the catalog and cannot be switched off: the fix is the typo.
const PragmaRule = "@allow"

// allowance is one pragma: the rules it switches off and the lines it
// covers (the rung whose header it is in, the POU it sits in, or the whole
// file outside every POU).
type allowance struct {
	rules    map[string]bool
	from, to int
}

// Filter drops the findings cfg disables and those an `(* @allow ID … *)`
// pragma in src switches off, adds a warning for each ID a pragma names
// that is not a rule, and sorts the result by position.
//
// A pragma covers the POU it is written in — put it in the header, next to
// the POU's description comment — so it never reaches past END_PROGRAM or
// END_FUNCTION_BLOCK. One written outside every POU (above the first) covers
// the whole file. One in a ladder rung's header, `RUNG r4 (* @allow LD006:
// … *)`, covers that rung only. The IDs come first, separated by spaces or
// commas; a colon, or the first word not shaped like an ID (letters, then
// digits), ends the list and starts the reason:
//
//	(* @allow LD003 LD005: the vendor's rungs, kept as imported *)
func Filter(src string, cfg Config, fs []Finding) []Finding {
	allows, bad := pragmas(src)
	out := fs[:0:0]
	for _, f := range fs {
		if !cfg.On(f.Rule) || allowed(allows, f) {
			continue
		}
		out = append(out, f)
	}
	out = append(out, bad...)
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

// IsPragma reports whether a comment's text (without its `(*` and `*)`) is
// an @allow pragma. A rung whose only header comment is a pragma has no
// comment as far as LD005 is concerned: the pragma says which rule is off,
// not why the rung exists.
func IsPragma(comment string) bool {
	return strings.HasPrefix(strings.TrimSpace(comment), "@allow")
}

// pragmas finds every @allow pragma in src and the lines it covers, and a
// warning for every ID one names that is not a rule.
func pragmas(src string) ([]allowance, []Finding) {
	if !strings.Contains(src, "@allow") {
		return nil, nil
	}
	lines := strings.Split(src, "\n")
	// POU spans and rung headers, read on comment-stripped text so a
	// commented-out header does not open one.
	type span struct{ from, to int }
	var pous []span
	var rungs []int // RUNG header lines
	var ends []int  // END_LD and END_ POU lines: each closes a rung too
	open := 0
	for i, l := range strings.Split(fbcatalog.StripComments(src), "\n") {
		switch {
		case pouEndRe.MatchString(l):
			ends = append(ends, i+1)
			if open > 0 {
				pous = append(pous, span{open, i + 1})
				open = 0
			}
		case pouOpenRe.MatchString(l):
			open = i + 1
		case rungRe.MatchString(l):
			rungs = append(rungs, i+1)
		case ldEndRe.MatchString(l):
			ends = append(ends, i+1)
		}
	}
	if open > 0 {
		pous = append(pous, span{open, len(lines)})
	}
	var out []allowance
	var bad []Finding
	for _, loc := range allowRe.FindAllStringIndex(src, -1) {
		line := strings.Count(src[:loc[0]], "\n") + 1
		a := allowance{rules: map[string]bool{}, from: 1, to: len(lines)}
		// The ID list runs from after @allow to a colon, the first word not
		// shaped like an ID, or the comment's end.
		rest := src[loc[1]:]
		if e := strings.Index(rest, "*)"); e >= 0 {
			rest = rest[:e]
		}
		off := loc[1]
		for {
			skip := len(rest) - len(strings.TrimLeft(rest, " \t\r\n,"))
			rest, off = rest[skip:], off+skip
			if rest == "" || rest[0] == ':' {
				break
			}
			n := strings.IndexAny(rest, " \t\r\n,:")
			if n < 0 {
				n = len(rest)
			}
			tok := rest[:n]
			if !idTokRe.MatchString(tok) {
				break
			}
			if id := strings.ToUpper(tok); Known(id) {
				a.rules[id] = true
			} else {
				col := off - strings.LastIndex(src[:off], "\n")
				bad = append(bad, Finding{Rule: PragmaRule, Line: strings.Count(src[:off], "\n") + 1, Col: col,
					Message: fmt.Sprintf("%s in this @allow pragma is not a structure rule, so the pragma "+
						"does not switch it off: write the rule's ID (LD001…LD006, FBD001…FBD003, SFC001, "+
						"SFC002; see /reference/structure-warnings/)", tok)})
			}
			rest, off = rest[n:], off+n
		}
		for _, p := range pous {
			if line >= p.from && line <= p.to {
				a.from, a.to = p.from, p.to
				break
			}
		}
		// In a rung header: that rung, to the line before the next RUNG
		// header, END_LD or the POU's end.
		for k, r := range rungs {
			if r != line {
				continue
			}
			to := len(lines)
			if k+1 < len(rungs) {
				to = rungs[k+1] - 1
			}
			for _, e := range ends {
				if e > line && e-1 < to {
					to = e - 1
				}
			}
			a.from, a.to = line, to
			break
		}
		out = append(out, a)
	}
	return out, bad
}

// ── shared rule helpers ────────────────────────────────────────────────────

// thresholdCmp are the ordering comparisons: the blocks that test a value
// against a threshold, where a literal is a setpoint nobody can tune.
var thresholdCmp = map[string]bool{"GT": true, "GE": true, "LT": true, "LE": true}

// IsThresholdCompare reports whether fn (any case) is GT, GE, LT or LE.
func IsThresholdCompare(fn string) bool { return thresholdCmp[strings.ToUpper(fn)] }

var numLitRe = regexp.MustCompile(`^(?i)(?:[A-Z]+#)?[+-]?(?:[0-9][0-9_]*)(?:\.[0-9_]+)?(?:E[+-]?[0-9]+)?$`)

// MagicNumber reports whether arg is a numeric literal that a threshold
// should not be. Not reported, because they read as themselves: zero in
// any spelling (`0`, `0.0`, `REAL#0`), and the integer literals 1 and -1,
// which make a count compare (`GE(Count, 1)` is `GT(Count, 0)`, "at least
// one"). A real 1.0 is a threshold like any other. TIME literals are not
// numbers here: a delay written in the block that times it (`PT := T#5S`)
// is the usual, readable form.
func MagicNumber(arg string) bool {
	a := strings.TrimSpace(arg)
	if !numLitRe.MatchString(a) {
		return false
	}
	isReal := false
	if i := strings.IndexByte(a, '#'); i >= 0 {
		isReal = strings.Contains(strings.ToUpper(a[:i]), "REAL")
		a = a[i+1:]
	}
	isReal = isReal || strings.ContainsAny(a, ".eE")
	v, err := strconv.ParseFloat(strings.ReplaceAll(a, "_", ""), 64)
	if err != nil || v == 0 {
		return false
	}
	return isReal || (v != 1 && v != -1)
}

// ThresholdMessage is the shared LD006/FBD003 text.
func ThresholdMessage(call, lit string) string {
	return fmt.Sprintf("%s compares against the literal %s: make the threshold a setpoint tag "+
		"(role: setpoint, with init: %s and a desc:) or a named VAR CONSTANT, and compare against that name",
		call, lit, lit)
}
