package sfc

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// The SFC render model, per docs/design/sfc.md §4.1. Unlike FBD (a netlist
// whose drawing is entirely derived) SFC follows LD's posture: chart
// structure — which steps, which transitions, which branches — is CANONICAL
// IN THE TEXT, so the model is a direct projection of the parsed chart, not
// a rebuilt graph. The one 2-D need (parallel-branch placement) reuses
// FBD's optional `(* @layout … *)` pin block as an aesthetic override; the
// webview computes the default top-to-bottom / fan-out-columns layout from
// Steps/Trans topology itself (§4.1's "auto-layout is topological").
//
// Determinism: Model is built straight from a fresh Parse with no
// non-deterministic iteration (slices preserve source/declaration order,
// the one map — Layout — is emitted sorted by id in JSON only insofar as
// Go's encoding/json does NOT sort map keys; callers that need byte-stable
// JSON should treat Layout as a set). Two calls to Graph on identical
// source produce field-for-field identical Models.

// Model is the JSON `naut sfc graph` emits.
type Model struct {
	Name     string        `json:"name"`
	Vars     []VarDecl     `json:"vars,omitempty"`
	Steps    []GStep       `json:"steps"`
	Trans    []GTransition `json:"trans"`
	Actions  []GAction     `json:"actions,omitempty"`
	Comments []Comment     `json:"comments,omitempty"`
	// Layout holds user-pinned node positions parsed from an optional
	// `(* @layout … *)` block (reused verbatim from lang/fbd's format) —
	// nil when the chart has none. Keys are the same stable ids used
	// elsewhere in the model (st:/tr:/ac:).
	Layout map[string]Point `json:"layout,omitempty"`
}

// Point is one pinned x/y position.
type Point struct {
	X int `json:"x"`
	Y int `json:"y"`
}

// VarDecl mirrors lang/ld's VarDecl shape (design doc §4.1: "reused shape
// from lang/ld VarDecl") — the diagram's header-declarations panel.
type VarDecl struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Init    string `json:"init,omitempty"`
	Section string `json:"section"`
	Line    int    `json:"line"`
}

// GAssoc is one action association rendered inline in its GStep.
type GAssoc struct {
	Qualifier string `json:"qualifier"`
	Target    string `json:"target"`
	Time      string `json:"time,omitempty"`
	Line      int    `json:"line"`
}

// GStep is one INITIAL_STEP/STEP element. ID is "st:<Name>" — stable across
// edits that don't rename the step.
type GStep struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Initial bool     `json:"initial"`
	Actions []GAssoc `json:"actions,omitempty"`
	Line    int      `json:"line"`    // the STEP/INITIAL_STEP keyword
	EndLine int      `json:"endLine"` // the matching END_STEP
}

// GTransition is one TRANSITION element. ID is "tr:<Name>" for a named
// transition, else "tr:<line>" (its TRANSITION keyword's line) — stable
// unless the transition is renamed or its whole block is deleted/reordered
// (an unnamed transition's id then legitimately shifts, matching how a
// diagram would re-address a moved anonymous element).
//
// Kind is derived from arity + shared-source grouping (§4.1):
//
//	simDiverge — len(To) > 1  (one source, many targets)
//	simConverge — len(From) > 1 (many sources, one target)
//	alt — single-source/single-target AND shares that source with at
//	    least one other single-source/single-target transition
//	normal — none of the above
type GTransition struct {
	ID      string   `json:"id"`
	Name    string   `json:"name,omitempty"`
	From    []string `json:"from"`
	To      []string `json:"to"`
	Cond    string   `json:"cond"`
	Kind    string   `json:"kind"`
	Line    int      `json:"line"`    // the TRANSITION keyword
	EndLine int      `json:"endLine"` // the matching END_TRANSITION
}

// GAction is one ACTION element. ID is "ac:<Name>".
type GAction struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Body    string `json:"body"`
	Line    int    `json:"line"`
	EndLine int    `json:"endLine"`
}

// Comment is a run of consecutive full-line `//` comments inside the SFC
// body — a diagram note, the same shape lang/ld/lang/fbd use.
type Comment struct {
	Line    int    `json:"line"`
	EndLine int    `json:"endLine"`
	Text    string `json:"text"`
}

// shareAnyStep reports whether two step-sets have any member in common
// (case-insensitive) — §2.3's alt-group test.
func shareAnyStep(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if strings.EqualFold(x, y) {
				return true
			}
		}
	}
	return false
}

func stepID(name string) string   { return "st:" + name }
func actionID(name string) string { return "ac:" + name }

// transID is the stable id for a transition (see GTransition doc above):
// its declared name when it has one, else its TRANSITION keyword's line.
func transID(name string, line int) string {
	if name != "" {
		return "tr:" + name
	}
	return "tr:" + strconv.Itoa(line)
}

// Graph parses .sfc source and builds its render model (design doc §4.1).
func Graph(src string) (*Model, error) {
	prog, err := Parse(src)
	if err != nil {
		return nil, err
	}
	m := &Model{Name: prog.Name}

	header, _, _, bodyLine, err := splitSFC(src)
	if err != nil {
		return nil, err
	}
	m.Vars = scanVars(header)

	for _, s := range prog.Steps {
		ms := GStep{
			ID: stepID(s.Name), Name: s.Name, Initial: s.Initial,
			Line: s.Pos.Line, EndLine: s.EndPos.Line,
		}
		for _, a := range s.Actions {
			ms.Actions = append(ms.Actions, GAssoc{
				Qualifier: a.Qualifier, Target: a.Target, Time: a.Time, Line: a.Pos.Line,
			})
		}
		m.Steps = append(m.Steps, ms)
	}

	// Alt-group membership per §2.3's "share-any-source" definition: a
	// transition shares a priority group with another iff their FROM sets
	// overlap at all, REGARDLESS of either side's arity (a plain FROM S
	// competing with a simultaneous convergence FROM (S, other) both want
	// S's token — see §2.3's own worked example). simDiverge/simConverge
	// (arity > 1 on the transition's OWN To/From) still take precedence for
	// Kind, since those name the glyph at THIS transition's own fan
	// point; "alt" names a 1-to-1 transition that also competes for a
	// shared source with something else.
	sharesSource := make([]bool, len(prog.Transitions))
	for i, t := range prog.Transitions {
		for j, u := range prog.Transitions {
			if i == j {
				continue
			}
			if shareAnyStep(t.From, u.From) {
				sharesSource[i] = true
				break
			}
		}
	}
	for i, t := range prog.Transitions {
		mt := GTransition{
			ID: transID(t.Name, t.Pos.Line), Name: t.Name, From: append([]string{}, t.From...), To: append([]string{}, t.To...),
			Cond: t.Cond.Text, Line: t.Pos.Line, EndLine: t.EndPos.Line,
		}
		switch {
		case len(t.To) > 1:
			mt.Kind = "simDiverge"
		case len(t.From) > 1:
			mt.Kind = "simConverge"
		case sharesSource[i]:
			mt.Kind = "alt"
		default:
			mt.Kind = "normal"
		}
		m.Trans = append(m.Trans, mt)
	}

	for _, a := range prog.Actions {
		m.Actions = append(m.Actions, GAction{
			ID: actionID(a.Name), Name: a.Name, Body: a.Body.Text, Line: a.Pos.Line, EndLine: a.EndPos.Line,
		})
	}

	lines := strings.Split(src, "\n")
	m.Comments = scanSFCComments(lines, bodyLine, endOfBody(lines))
	if layout, _, _ := parseLayoutBlock(lines); len(layout) > 0 {
		m.Layout = layout
	}
	return m, nil
}

// endOfBody returns the 1-based line of the END_SFC marker, 0 if absent.
func endOfBody(lines []string) int {
	for i, l := range lines {
		if sfcEndRe.MatchString(l) {
			return i + 1
		}
	}
	return 0
}

// scanSFCComments finds `//` comment runs strictly between the SFC and
// END_SFC marker lines — the same convention lang/fbd's scanComments uses
// (block `(* … *)` comments, including @layout, stay invisible here).
func scanSFCComments(lines []string, startLine, endLine int) []Comment {
	if startLine <= 0 {
		return nil
	}
	last := len(lines)
	if endLine > 0 {
		last = endLine - 1
	}
	var out []Comment
	var cur *Comment
	flush := func() {
		if cur != nil {
			out = append(out, *cur)
			cur = nil
		}
	}
	for i := startLine; i < last; i++ { // 0-based index i = file line i+1
		t := strings.TrimSpace(lines[i])
		if strings.HasPrefix(t, "//") {
			text := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(t, "//"), " "))
			if cur == nil {
				cur = &Comment{Line: i + 1, EndLine: i + 1, Text: text}
			} else {
				cur.EndLine = i + 1
				cur.Text += "\n" + text
			}
			continue
		}
		flush()
	}
	flush()
	return out
}

// ── header variable scan ─────────────────────────────────────────────────
// Line-based, mirroring lang/ld's scanVars / lang/fbd's parseVarDecls —
// deliberately re-derived from raw text (rather than prog.VarBlocks, which
// holds parsed Expression initializers, not source text) so Init is the
// verbatim initializer text an editor can round-trip.

var sfcVarSectionRe = regexp.MustCompile(`(?i)^\s*(VAR_EXTERNAL|VAR_INPUT|VAR_OUTPUT|VAR_IN_OUT|VAR)\s*$`)
var sfcVarDeclLineRe = regexp.MustCompile(`^\s*([A-Za-z_][A-Za-z0-9_]*)\s*:\s*([^;:=]+?)\s*(?::=\s*([^;]+?)\s*)?;`)

func scanVars(header string) []VarDecl {
	var out []VarDecl
	section := ""
	for i, line := range strings.Split(header, "\n") {
		if m := sfcVarSectionRe.FindStringSubmatch(line); m != nil {
			section = strings.ToUpper(m[1])
			continue
		}
		if strings.EqualFold(strings.TrimSpace(line), "END_VAR") {
			section = ""
			continue
		}
		if section == "" {
			continue
		}
		if m := sfcVarDeclLineRe.FindStringSubmatch(line); m != nil {
			out = append(out, VarDecl{
				Name: m[1], Type: strings.TrimSpace(m[2]), Init: strings.TrimSpace(m[3]),
				Section: section, Line: i + 1,
			})
		}
	}
	return out
}

// ── @layout block (reuses lang/fbd/layout.go's format verbatim) ──────────
//
// The design doc directs SFC to reuse FBD's `(* @layout id x,y *)` pin
// block rather than invent a new mechanism (§4.2). fbd.parseLayout/
// renderLayoutBlock are unexported (tied to fbd's modelBuilder), so this is
// a small, format-compatible re-implementation local to this package —
// same block syntax, same one-`id x,y`-pair-per-line rendering, so a
// hand-written or FBD-authored block round-trips identically.

var sfcLayoutBlockRe = regexp.MustCompile(`\(\*\s*@layout\b`)

// parseLayoutBlock extracts the @layout block: the pinned-position map, plus
// the 1-based [start, end] line span of the whole comment (0,0 if absent).
func parseLayoutBlock(lines []string) (map[string]Point, int, int) {
	start := -1
	for i, l := range lines {
		if sfcLayoutBlockRe.MatchString(l) {
			start = i
			break
		}
	}
	if start == -1 {
		return nil, 0, 0
	}
	entries := map[string]Point{}
	end := start
	for i := start; i < len(lines); i++ {
		end = i
		line := lines[i]
		if i == start {
			line = sfcLayoutBlockRe.ReplaceAllString(line, "")
		}
		closed := false
		if idx := strings.Index(line, "*)"); idx >= 0 {
			line = line[:idx]
			closed = true
		}
		parts := strings.Fields(line)
		for j := 0; j+1 < len(parts); j += 2 {
			xy := strings.SplitN(parts[j+1], ",", 2)
			if len(xy) != 2 {
				continue
			}
			x, errX := strconv.Atoi(strings.TrimSpace(xy[0]))
			y, errY := strconv.Atoi(strings.TrimSpace(xy[1]))
			if errX == nil && errY == nil {
				entries[parts[j]] = Point{X: x, Y: y}
			}
		}
		if closed {
			break
		}
	}
	return entries, start + 1, end + 1
}

// renderLayoutBlock emits the canonical block text, sorted by id for a
// stable diff.
func renderLayoutBlock(entries map[string]Point) string {
	ids := make([]string, 0, len(entries))
	for id := range entries {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var b strings.Builder
	b.WriteString("  (* @layout\n")
	for _, id := range ids {
		e := entries[id]
		fmt.Fprintf(&b, "    %s %d,%d\n", id, e.X, e.Y)
	}
	b.WriteString("  *)\n")
	return b.String()
}
