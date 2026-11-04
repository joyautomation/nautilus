package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/joyautomation/nautilus/lang/fbd"
	"github.com/joyautomation/nautilus/lang/ld"
	"github.com/joyautomation/nautilus/lang/sfc"
)

// Structure thresholds. They restate AGENTS.md's rules as numbers, so a
// run is judged against exactly what the file asks for. Change them here
// and in the README together.
const (
	maxLDSeries      = 6 // "at most about 6 elements in series"
	maxLDBranchDepth = 1 // "branches at most one level deep"
	maxFBDNesting    = 2 // "Nest calls at most two deep"
)

// genericName matches the placeholder names AGENTS.md rules out for rungs,
// wires and steps: r1, rung3, w1, S2, Step4, n1, net2, t1, tmp1, x1.
var genericName = regexp.MustCompile(`(?i)^(r|rung|w|wire|s|step|n|net|network|t|tmp|temp|x|state)_?\d+$`)

// stateVarCond matches a transition condition that only compares one
// variable with an integer literal (`State = 2`, `(Step = 10)`): the CASE
// machine pattern that hides the process logic from the chart.
var stateVarCond = regexp.MustCompile(`^\(?\s*[A-Za-z_][A-Za-z0-9_.]*\s*=\s*\d+\s*\)?$`)

var ifOrCase = regexp.MustCompile(`(?i)\b(IF|CASE)\b`)

// LDMetrics describes one .ld file's rungs.
type LDMetrics struct {
	Rungs          int     `json:"rungs"`
	MaxSeries      int     `json:"maxSeries"`      // longest run of elements in series, top level or inside a leg
	MaxBranchDepth int     `json:"maxBranchDepth"` // 0 = no branch, 1 = a branch, 2 = a branch inside a leg, …
	MaxElements    int     `json:"maxElements"`    // most leaf elements (contacts, blocks, coils) on one rung
	MeanElements   float64 `json:"meanElements"`
	MaxCoils       int     `json:"maxCoils"` // most coils on one rung
	Uncommented    int     `json:"uncommented"`
	GenericNames   int     `json:"genericNames"`
	// RepeatedContacts counts branches where one contact appears on every
	// leg (it belongs once, in series, outside the branch).
	RepeatedContacts int `json:"repeatedContacts"`
	// MultiAssign counts `{ … }` move boxes holding more than one
	// assignment (math that belongs in FBD or ST).
	MultiAssign int `json:"multiAssign"`
}

// FBDMetrics describes one .fbd file's networks.
type FBDMetrics struct {
	Networks     int  `json:"networks"` // NETWORK lines
	Untitled     int  `json:"untitled"` // NETWORK lines with no title
	Implicit     bool `json:"implicit"` // statements outside any NETWORK
	Statements   int  `json:"statements"`
	MaxNesting   int  `json:"maxNesting"`   // deepest chain of unnamed block calls feeding one another
	GenericWires int  `json:"genericWires"` // wires and instances named w1, t1, …
	Layout       bool `json:"layout"`       // a (* @layout *) block is present
	// MaxChipFanout is the most consumers one input chip feeds inside a
	// network ("compare once, reuse the wire"). Informational.
	MaxChipFanout int `json:"maxChipFanout"`
}

// SFCMetrics describes one .sfc file's chart.
type SFCMetrics struct {
	Steps               int `json:"steps"`
	InitialSteps        int `json:"initialSteps"`
	Transitions         int `json:"transitions"`
	Associations        int `json:"associations"`
	Actions             int `json:"actions"`
	GenericSteps        int `json:"genericSteps"`
	UnnamedTransitions  int `json:"unnamedTransitions"`
	StateVarTransitions int `json:"stateVarTransitions"`
	ActionsWithIF       int `json:"actionsWithIF"` // ACTION bodies holding IF or CASE (informational)
	// ActionsSteeringChart counts ACTION bodies with an IF or CASE that
	// assign a variable some transition condition reads: the decision about
	// where the chart goes next is hidden in the action ("no IF deciding
	// where the chart goes next"). A conditional counter is allowed.
	ActionsSteeringChart int `json:"actionsSteeringChart"`
}

// FileMetrics is one program source's diagram metrics.
type FileMetrics struct {
	Path       string      `json:"path"`
	Lang       string      `json:"lang"`
	ParseError string      `json:"parseError,omitempty"`
	LD         *LDMetrics  `json:"ld,omitempty"`
	FBD        *FBDMetrics `json:"fbd,omitempty"`
	SFC        *SFCMetrics `json:"sfc,omitempty"`
}

// coilUse is one coil on one rung, for the one-writer-per-BOOL rule.
type coilUse struct {
	file, rung, mode string
}

// ProjectMetrics is every diagram file in a project, plus the cross-rung
// coil analysis.
type ProjectMetrics struct {
	Files []FileMetrics `json:"files"`
	// MultiWriter lists BOOLs driven by `( X )` coils on more than one
	// rung, or by `( X )` mixed with `( S X )` / `( R X )`.
	MultiWriter []string `json:"multiWriter,omitempty"`
}

// MeasureProject walks dir for .ld, .fbd and .sfc sources (skipping .git
// and hidden directories) and measures each.
func MeasureProject(dir string) (*ProjectMetrics, error) {
	pm := &ProjectMetrics{Files: []FileMetrics{}}
	coils := map[string][]coilUse{} // upper-cased tag → uses
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != dir && strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".ld" && ext != ".fbd" && ext != ".sfc" {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		fm := FileMetrics{Path: filepath.ToSlash(rel), Lang: ext[1:]}
		switch ext {
		case ".ld":
			m, uses, err := measureLD(string(src))
			if err != nil {
				fm.ParseError = err.Error()
			}
			fm.LD = m
			for tag, us := range uses {
				for _, u := range us {
					u.file = fm.Path
					coils[tag] = append(coils[tag], u)
				}
			}
		case ".fbd":
			m, err := measureFBD(string(src))
			if err != nil {
				fm.ParseError = err.Error()
			}
			fm.FBD = m
		case ".sfc":
			m, err := measureSFC(string(src))
			if err != nil {
				fm.ParseError = err.Error()
			}
			fm.SFC = m
		}
		pm.Files = append(pm.Files, fm)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(pm.Files, func(i, j int) bool { return pm.Files[i].Path < pm.Files[j].Path })
	pm.MultiWriter = multiWriters(coils)
	return pm, nil
}

// multiWriters applies the one-coil-per-BOOL rule: a plain coil on more
// than one rung, or a plain coil and a set/reset coil on the same tag.
// Several S and R coils on one latch are fine; so is one S and one R.
func multiWriters(coils map[string][]coilUse) []string {
	var out []string
	for tag, uses := range coils {
		plainRungs := map[string]bool{}
		latch := false
		for _, u := range uses {
			switch u.mode {
			case "":
				plainRungs[u.file+"#"+u.rung] = true
			case "S", "R":
				latch = true
			}
		}
		if len(plainRungs) > 1 || (len(plainRungs) > 0 && latch) {
			out = append(out, tag)
		}
	}
	sort.Strings(out)
	return out
}

func measureLD(src string) (*LDMetrics, map[string][]coilUse, error) {
	model, err := ld.Graph(src)
	if err != nil {
		return nil, nil, err
	}
	m := &LDMetrics{Rungs: len(model.Rungs)}
	uses := map[string][]coilUse{}
	notes := map[int]bool{} // the line just below a `//` note
	for _, c := range model.Comments {
		notes[c.EndLine+1] = true
	}
	total := 0
	for _, r := range model.Rungs {
		if strings.TrimSpace(r.Comment) == "" && !notes[r.Line] {
			m.Uncommented++
		}
		if genericName.MatchString(r.Name) {
			m.GenericNames++
		}
		w := walkSeries(r.Elements, 0, m)
		n := w + countLeaves(r.Coils)
		total += n
		if n > m.MaxElements {
			m.MaxElements = n
		}
		if len(r.Coils) > m.MaxCoils {
			m.MaxCoils = len(r.Coils)
		}
		for _, c := range r.Coils {
			switch c.Kind {
			case "coil":
				if c.Ref != "" {
					tag := strings.ToUpper(c.Ref)
					uses[tag] = append(uses[tag], coilUse{rung: r.Name, mode: c.Mode})
				}
			case "assign":
				if strings.Count(strings.TrimRight(strings.TrimSpace(c.Text), ";"), ";") >= 1 {
					m.MultiAssign++
				}
			}
		}
		for _, e := range r.Elements {
			if e.Kind == "assign" && strings.Count(strings.TrimRight(strings.TrimSpace(e.Text), ";"), ";") >= 1 {
				m.MultiAssign++
			}
		}
	}
	if m.Rungs > 0 {
		m.MeanElements = float64(total) / float64(m.Rungs)
	}
	return m, uses, nil
}

// walkSeries measures one series list (a rung's conditions or one leg of a
// branch) at the given branch depth, updating m's maxima, and returns how
// many leaf elements it holds.
func walkSeries(els []ld.Element, depth int, m *LDMetrics) int {
	if len(els) > m.MaxSeries {
		m.MaxSeries = len(els)
	}
	if depth > m.MaxBranchDepth {
		m.MaxBranchDepth = depth
	}
	leaves := 0
	for _, e := range els {
		if e.Kind != "branch" {
			leaves++
			continue
		}
		if repeatedOnEveryLeg(e.Legs) {
			m.RepeatedContacts++
		}
		for _, leg := range e.Legs {
			leaves += walkSeries(leg, depth+1, m)
		}
	}
	return leaves
}

func countLeaves(els []ld.Element) int {
	n := 0
	for _, e := range els {
		if e.Kind == "branch" {
			for _, leg := range e.Legs {
				n += countLeaves(leg)
			}
			continue
		}
		n++
	}
	return n
}

// repeatedOnEveryLeg reports whether some plain contact sits directly on
// every leg of a branch of two or more legs.
func repeatedOnEveryLeg(legs [][]ld.Element) bool {
	if len(legs) < 2 {
		return false
	}
	key := func(e ld.Element) string {
		if e.Kind != "contact" {
			return ""
		}
		return fmt.Sprintf("%s/%v", strings.ToUpper(e.Ref), e.Neg)
	}
	common := map[string]bool{}
	for _, e := range legs[0] {
		if k := key(e); k != "" {
			common[k] = true
		}
	}
	for _, leg := range legs[1:] {
		here := map[string]bool{}
		for _, e := range leg {
			if k := key(e); k != "" && common[k] {
				here[k] = true
			}
		}
		common = here
	}
	return len(common) > 0
}

func measureFBD(src string) (*FBDMetrics, error) {
	m := &FBDMetrics{Layout: strings.Contains(src, "@layout")}
	model, err := fbd.Graph(src)
	if err != nil {
		return m, err
	}
	m.Networks = len(model.Networks)
	for _, n := range model.Networks {
		if n.Implicit {
			m.Implicit = true
			m.Networks--
			continue
		}
		if strings.TrimSpace(n.Title) == "" {
			m.Untitled++
		}
	}
	if len(model.Networks) == 0 && len(model.Nodes) > 0 {
		m.Implicit = true
	}
	m.Statements = len(fbd.Statements(src))

	byID := map[string]*fbd.Node{}
	for _, n := range model.Nodes {
		byID[n.ID] = n
		if n.Wire != "" && genericName.MatchString(n.Wire) {
			m.GenericWires++
		}
		if n.Kind == "fb" && genericName.MatchString(n.Label) {
			m.GenericWires++ // an instance named t1, x2
		}
	}
	in := map[string][]*fbd.Edge{}
	fanout := map[string]int{}
	for _, e := range model.Edges {
		in[e.To] = append(in[e.To], e)
		if src := byID[e.From]; src != nil && src.Kind == "input" && src.Src == nil {
			fanout[e.From]++ // a variable chip (not a literal)
		}
	}
	for _, n := range fanout {
		if n > m.MaxChipFanout {
			m.MaxChipFanout = n
		}
	}
	// depth(n) = 1 + the deepest unnamed block feeding n. A named wire
	// (`w = BLOCK(...)`) ends the chain: that is the point of naming it.
	memo := map[string]int{}
	var depth func(id string, guard int) int
	depth = func(id string, guard int) int {
		if d, ok := memo[id]; ok {
			return d
		}
		if guard > len(model.Nodes) {
			return 0
		}
		best := 0
		for _, e := range in[id] {
			if e.Feedback {
				continue
			}
			src := byID[e.From]
			if src == nil || src.Kind != "block" || src.Wire != "" {
				continue
			}
			if d := depth(src.ID, guard+1); d > best {
				best = d
			}
		}
		memo[id] = best + 1
		return best + 1
	}
	for _, n := range model.Nodes {
		if n.Kind != "block" && n.Kind != "fb" {
			continue
		}
		if d := depth(n.ID, 0); d > m.MaxNesting {
			m.MaxNesting = d
		}
	}
	return m, nil
}

func measureSFC(src string) (*SFCMetrics, error) {
	model, err := sfc.Graph(src)
	if err != nil {
		return nil, err
	}
	m := &SFCMetrics{Steps: len(model.Steps), Transitions: len(model.Trans), Actions: len(model.Actions)}
	for _, s := range model.Steps {
		if s.Initial {
			m.InitialSteps++
		}
		if genericName.MatchString(s.Name) {
			m.GenericSteps++
		}
		m.Associations += len(s.Actions)
	}
	inConds := map[string]bool{} // identifiers any transition condition reads
	for _, t := range model.Trans {
		if t.Name == "" {
			m.UnnamedTransitions++
		}
		if stateVarCond.MatchString(strings.TrimSpace(t.Cond)) {
			m.StateVarTransitions++
		}
		for _, id := range ident.FindAllString(t.Cond, -1) {
			inConds[strings.ToUpper(id)] = true
		}
	}
	for _, a := range model.Actions {
		body := stripSTComments(a.Body)
		if !ifOrCase.MatchString(body) {
			continue
		}
		m.ActionsWithIF++
		for _, as := range assignTarget.FindAllStringSubmatch(body, -1) {
			if inConds[strings.ToUpper(as[1])] {
				m.ActionsSteeringChart++
				break
			}
		}
	}
	return m, nil
}

var (
	ident        = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)
	assignTarget = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*)\s*:=`)
)

var stComment = regexp.MustCompile(`(?s)\(\*.*?\*\)|//[^\n]*`)

func stripSTComments(s string) string { return stComment.ReplaceAllString(s, "") }

// Violations lists the structure rules a project breaks, as stable IDs with
// the file they were found in ("ld.series>6 tank.ld"). Empty means the
// diagrams follow AGENTS.md's structure rules.
func (pm *ProjectMetrics) Violations() []string {
	var v []string
	add := func(id, file string) { v = append(v, id+" "+file) }
	for _, f := range pm.Files {
		if f.ParseError != "" {
			add(f.Lang+".parse-error", f.Path)
			continue
		}
		switch {
		case f.LD != nil:
			l := f.LD
			if l.MaxSeries > maxLDSeries {
				add(fmt.Sprintf("ld.series>%d", maxLDSeries), f.Path)
			}
			if l.MaxBranchDepth > maxLDBranchDepth {
				add(fmt.Sprintf("ld.branch-depth>%d", maxLDBranchDepth), f.Path)
			}
			if l.Uncommented > 0 {
				add("ld.uncommented-rung", f.Path)
			}
			if l.GenericNames > 0 {
				add("ld.generic-rung-name", f.Path)
			}
			if l.RepeatedContacts > 0 {
				add("ld.contact-on-every-leg", f.Path)
			}
			if l.MultiAssign > 0 {
				add("ld.multi-statement-move", f.Path)
			}
		case f.FBD != nil:
			b := f.FBD
			if b.Networks == 0 || b.Implicit {
				add("fbd.logic-outside-network", f.Path)
			}
			if b.Untitled > 0 {
				add("fbd.untitled-network", f.Path)
			}
			if b.MaxNesting > maxFBDNesting {
				add(fmt.Sprintf("fbd.nesting>%d", maxFBDNesting), f.Path)
			}
			if b.GenericWires > 0 {
				add("fbd.generic-name", f.Path)
			}
			if b.Layout {
				add("fbd.layout-block", f.Path)
			}
		case f.SFC != nil:
			s := f.SFC
			if s.StateVarTransitions > 0 {
				add("sfc.state-variable-transition", f.Path)
			}
			if s.GenericSteps > 0 {
				add("sfc.generic-step-name", f.Path)
			}
			if s.UnnamedTransitions > 0 {
				add("sfc.unnamed-transition", f.Path)
			}
			if s.ActionsSteeringChart > 0 {
				add("sfc.action-steers-chart", f.Path)
			}
			if s.InitialSteps != 1 {
				add("sfc.initial-steps!=1", f.Path)
			}
		}
	}
	for _, tag := range pm.MultiWriter {
		add("ld.multiple-coils", tag)
	}
	return v
}

// ViolationID strips the location from a Violations entry.
func ViolationID(v string) string {
	if i := strings.IndexByte(v, ' '); i >= 0 {
		return v[:i]
	}
	return v
}
