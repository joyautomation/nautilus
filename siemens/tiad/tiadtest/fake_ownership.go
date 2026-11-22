package tiadtest

import (
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/joyautomation/nautilus/siemens/tiad"
)

// The agent's Ownership (tools/tiad/core/Ownership.cs, #789): a software
// import never touches a program block outside its group. The fake's
// project holds one such block, Main [OB1] at the root, as TIA makes a
// CPU (an FB once SetImportReplacesForeign had an import replace it).

// projectBlock is the agent's ProjectBlock: a program block with its
// group path ("" for the root).
type projectBlock struct {
	name, kind string
	number     int
	group      string
	// class is an OB's SecondaryType, cycle a cyclic interrupt OB's
	// CyclicTime in microseconds.
	class string
	cycle int64
}

var (
	headWord = regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9_])(ORGANIZATION_BLOCK|FUNCTION_BLOCK|FUNCTION|DATA_BLOCK|TYPE)(?:[\s\x{a0}\x{feff}]+([A-Za-z_][A-Za-z0-9_]*)|[\s\x{a0}\x{feff}]*"([^"]+)")`)
	endType  = regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9_])END_TYPE(?:[^A-Za-z0-9_]|$)`)
)

// declaredBlocks is Ownership.Sources on text: a block keyword anywhere,
// any case, as a whole word, then a name: quoted (white space before it
// optional) or bare after white space (a bare keyword is the next header,
// not a name). A data type counts only with an END_TYPE in the source.
// The agent also reads every encoding the bytes could be and skips
// comments between keyword and name; the fake's sources are plain UTF-8.
func declaredBlocks(text string) []tiad.Block {
	hasEndType := endType.MatchString(text)
	var out []tiad.Block
	for _, m := range headWord.FindAllStringSubmatch(text, -1) {
		name := m[3]
		if name == "" {
			name = m[2]
			if _, kw := blockKind[strings.ToUpper(name)]; kw {
				continue
			}
		}
		kind := blockKind[strings.ToUpper(m[1])]
		if kind == "UDT" && !hasEndType {
			continue
		}
		out = append(out, tiad.Block{Name: name, Kind: kind})
	}
	return out
}

// owned is Ownership.Owned.
func owned(blockGroup, group string) bool {
	if strings.EqualFold(blockGroup, group) {
		return true
	}
	return len(blockGroup) > len(group) && blockGroup[len(group)] == '/' && strings.EqualFold(blockGroup[:len(group)], group)
}

// projectBlocks is the fake project's program blocks as the agent's
// Blocks reads them: Main at the root (OB1, program cycle, or an FB once
// replaced), then every imported block but data types in its group: an
// OB with the class, number and cycle it was made with, any other block
// numbered from 1 per kind in name order after Main's.
func (s *session) projectBlocks() []projectBlock {
	next := map[string]int{"FB": 1, "FC": 1, "DB": 1}
	var l []projectBlock
	if s.mainReplaced {
		l = append(l, projectBlock{name: "Main", kind: "FB", number: 1})
		next["FB"]++
	} else {
		l = append(l, projectBlock{name: "Main", kind: "OB", number: 1, class: "ProgramCycle"})
	}
	for _, b := range s.blocks {
		g := s.groups[strings.ToLower(b.Name)]
		if ob, ok := s.obInfo[strings.ToLower(b.Name)]; ok && b.Kind == "OB" {
			ob.name, ob.group = b.Name, g
			l = append(l, ob)
			continue
		}
		if _, ok := next[b.Kind]; !ok {
			continue
		}
		l = append(l, projectBlock{name: b.Name, kind: b.Kind, number: next[b.Kind], group: g})
		next[b.Kind]++
	}
	return l
}

func (b projectBlock) label() string {
	return fmt.Sprintf("%q [%s%d]", b.name, b.kind, b.number)
}

func groupName(g string) string {
	if g == "" {
		return "the program blocks' root"
	}
	return fmt.Sprintf("group %q", g)
}

// declared is one block a source declares, with the source's path.
type declared struct {
	tiad.Block
	source string
}

// foreignConflicts is Ownership.Conflicts plus its Refusal: nil when no
// declared block (data types aside) names a block outside group.
func foreignConflicts(ds []declared, blocks []projectBlock, group string) *failure {
	by := map[string][]string{}
	for _, d := range ds {
		k := strings.ToLower(d.Name)
		if d.Kind != "UDT" && !slices.Contains(by[k], d.source) {
			by[k] = append(by[k], d.source)
		}
	}
	var named []string
	for _, b := range blocks {
		if srcs, ok := by[strings.ToLower(b.name)]; ok && !owned(b.group, group) {
			named = append(named, strings.Join(srcs, ", ")+" declares "+b.label()+" in "+groupName(b.group))
		}
	}
	if len(named) == 0 {
		return nil
	}
	return fail(http.StatusConflict, tiad.KindForeignBlock,
		"%s, outside group %q: importing it would replace that block. Nothing was imported. Rename the block in the source (a prefix the project does not use)",
		strings.Join(named, "; "), group)
}

// typesRefused is Ownership.TypesRefused.
func typesRefused(ds []declared) *failure {
	var named []string
	for _, d := range ds {
		if d.Kind == "UDT" {
			named = append(named, fmt.Sprintf("%s declares data type %q", d.source, d.Name))
		}
	}
	if len(named) == 0 {
		return nil
	}
	return fail(http.StatusBadRequest, tiad.KindBadRequest,
		"%s: data types are not imported yet (TIA puts them beside the project's own, so tiad cannot keep a customer's apart). Nothing was imported", strings.Join(named, "; "))
}

// foreignChanges is Ownership.Changed.
func foreignChanges(before, after []projectBlock, group string) []string {
	index := func(l []projectBlock) map[string]projectBlock {
		m := map[string]projectBlock{}
		for _, b := range l {
			if !owned(b.group, group) {
				m[strings.ToLower(b.name)] = b
			}
		}
		return m
	}
	was, now := index(before), index(after)
	var changed []string
	for k, b := range was {
		n, ok := now[k]
		switch {
		case !ok:
			changed = append(changed, b.label()+" in "+groupName(b.group)+" is gone")
		case n.kind != b.kind || n.number != b.number || !strings.EqualFold(n.group, b.group):
			changed = append(changed, b.label()+" in "+groupName(b.group)+" is now "+n.label()+" in "+groupName(n.group))
		}
	}
	for k, b := range now {
		if _, ok := was[k]; !ok {
			changed = append(changed, b.label()+" in "+groupName(b.group)+" is new")
		}
	}
	sort.Strings(changed)
	return changed
}

// constantClash is the fake TIA's refusal of a block named OB_<x> while
// an OB x exists: TIA keeps a system constant OB_<name> for every OB
// (TIA VM, 2026-11-22). It returns the clashing name, or "".
func constantClash(blocks []projectBlock, heads []tiad.Block) string {
	obs := map[string]bool{}
	for _, b := range blocks {
		if b.kind == "OB" {
			obs[strings.ToLower(b.name)] = true
		}
	}
	for _, h := range heads {
		if len(h.Name) > 3 && strings.EqualFold(h.Name[:3], "OB_") && obs[strings.ToLower(h.Name[3:])] {
			return h.Name
		}
	}
	return ""
}

// freeOB is the first OB number from from that no OB holds.
func freeOB(blocks []projectBlock, from int) int {
	used := map[int]bool{}
	for _, b := range blocks {
		if b.kind == "OB" {
			used[b.number] = true
		}
	}
	for used[from] {
		from++
	}
	return from
}

const cyclicClass = "CyclicInterrupt"

// readOBs is CyclicObs.Read: every entry in range, no name or number twice.
func readOBs(obs []tiad.CyclicOB) *failure {
	names, numbers := map[string]bool{}, map[int]bool{}
	for _, ob := range obs {
		switch {
		case ob.Name == "":
			return fail(http.StatusBadRequest, tiad.KindBadRequest, "obs: an entry has no name")
		case names[strings.ToLower(ob.Name)]:
			return fail(http.StatusBadRequest, tiad.KindBadRequest, "obs: %q is given twice", ob.Name)
		case !(ob.Number >= 30 && ob.Number <= 38) && !(ob.Number >= 123 && ob.Number <= 32767):
			return fail(http.StatusBadRequest, tiad.KindBadRequest, "obs: %q number %d is no cyclic interrupt OB number (30-38, or 123-32767)", ob.Name, ob.Number)
		case numbers[ob.Number]:
			return fail(http.StatusBadRequest, tiad.KindBadRequest, "obs: OB%d is given twice", ob.Number)
		case ob.CycleMS < 1 || ob.CycleMS > 60000:
			return fail(http.StatusBadRequest, tiad.KindBadRequest, "obs: %q cycleMs %d is outside 1 ms to 60 s", ob.Name, ob.CycleMS)
		}
		names[strings.ToLower(ob.Name)], numbers[ob.Number] = true, true
	}
	return nil
}

// obSourcesRefused is CyclicObs.SourcesRefused.
func obSourcesRefused(ds []declared, obs []tiad.CyclicOB) *failure {
	asked, sourced := map[string]bool{}, map[string]bool{}
	for _, ob := range obs {
		asked[strings.ToLower(ob.Name)] = true
	}
	var problems []string
	for _, d := range ds {
		if d.Kind != "OB" {
			continue
		}
		sourced[strings.ToLower(d.Name)] = true
		if !asked[strings.ToLower(d.Name)] {
			problems = append(problems, fmt.Sprintf("%s declares OB %q, which obs does not name: SCL source alone makes a program cycle OB, not a cyclic interrupt one. Add it to obs with its number and cycle", d.source, d.Name))
		}
	}
	for _, ob := range obs {
		if !sourced[strings.ToLower(ob.Name)] {
			problems = append(problems, fmt.Sprintf("obs names %q, which no source declares: its body comes from an ORGANIZATION_BLOCK source", ob.Name))
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return fail(http.StatusBadRequest, tiad.KindBadRequest, "%s. Nothing was imported", strings.Join(problems, "; "))
}

// obConstantClash is CyclicObs.ConstantClash.
func obConstantClash(ds []declared, blocks []projectBlock, obs []tiad.CyclicOB) *failure {
	names := map[string]bool{}
	for _, b := range blocks {
		if b.kind == "OB" {
			names[strings.ToLower(b.name)] = true
		}
	}
	for _, ob := range obs {
		names[strings.ToLower(ob.Name)] = true
	}
	var problems []string
	for _, d := range ds {
		if d.Kind == "UDT" || len(d.Name) <= 3 || !strings.EqualFold(d.Name[:3], "OB_") {
			continue
		}
		if of := d.Name[3:]; names[strings.ToLower(of)] {
			problems = append(problems, fmt.Sprintf("%s declares %q, the name of TIA's system constant for OB %q (TIA keeps OB_<name> for every OB)", d.source, d.Name, of))
		}
	}
	for _, ob := range obs {
		for _, b := range blocks {
			if strings.EqualFold(b.name, "OB_"+ob.Name) {
				problems = append(problems, fmt.Sprintf("obs asks OB %q, whose system constant OB_%s would take the name of the project's block %q", ob.Name, ob.Name, b.name))
			}
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return fail(http.StatusBadRequest, tiad.KindBadRequest, "%s. Nothing was imported. Rename the block (a name that does not start with OB_)", strings.Join(problems, "; "))
}

func describeOB(number int, class string, cycle int64) string {
	if class != cyclicClass {
		if class == "" {
			class = "class unknown"
		}
		return fmt.Sprintf("OB%d, %s", number, class)
	}
	if cycle%1000 == 0 {
		return fmt.Sprintf("OB%d, cyclic interrupt, %d ms", number, cycle/1000)
	}
	return fmt.Sprintf("OB%d, cyclic interrupt, %d \u00b5s", number, cycle)
}

func describeBlock(b projectBlock) string {
	if b.kind != "OB" {
		return fmt.Sprintf("%s %d", b.kind, b.number)
	}
	return describeOB(b.number, b.class, b.cycle)
}

// planOBs is CyclicObs.Plan: removals first, then each asked OB kept,
// created or recreated.
func planOBs(obs []tiad.CyclicOB, blocks []projectBlock, group string, prune bool) ([]tiad.OBStep, *failure) {
	asked := map[string]bool{}
	for _, ob := range obs {
		asked[strings.ToLower(ob.Name)] = true
	}
	var removals, steps []tiad.OBStep
	for _, ob := range obs {
		var same *projectBlock
		for i, b := range blocks {
			if strings.EqualFold(b.name, ob.Name) {
				same = &blocks[i]
			}
		}
		for _, b := range blocks {
			if b.kind != "OB" || b.number != ob.Number || strings.EqualFold(b.name, ob.Name) {
				continue
			}
			if !owned(b.group, group) {
				return nil, fail(http.StatusConflict, tiad.KindForeignBlock, "obs asks OB%d for %q, but %s outside group %q holds it. Nothing was imported. Choose another number", ob.Number, ob.Name, b.label(), group)
			}
			// An OB asked for under another number frees this one (it is
			// recreated; every delete comes before any import).
			if asked[strings.ToLower(b.name)] {
				continue
			}
			if !prune {
				return nil, fail(http.StatusBadRequest, tiad.KindBadRequest, "obs asks OB%d for %q, but %s in the group holds it. Nothing was imported. Choose another number, or import with prune to remove it", ob.Number, ob.Name, b.label())
			}
			removals = append(removals, tiad.OBStep{Name: b.name, Action: tiad.OBRemoved, Was: describeBlock(b)})
		}
		st := tiad.OBStep{Name: ob.Name, Now: describeOB(ob.Number, cyclicClass, int64(ob.CycleMS)*1000)}
		switch {
		case same == nil:
			st.Action = tiad.OBCreated
		case same.kind == "OB" && same.class == cyclicClass && same.number == ob.Number && same.cycle == int64(ob.CycleMS)*1000:
			st.Action = tiad.OBKept
		default:
			st.Action, st.Was = tiad.OBRecreated, describeBlock(*same)
		}
		steps = append(steps, st)
	}
	return append(removals, steps...), nil
}

// failedOBSources is CyclicObs.FailedSources.
func failedOBSources(ds []declared, obs []tiad.CyclicOB, results []tiad.SourceResult) []string {
	asked := map[string]bool{}
	for _, ob := range obs {
		asked[strings.ToLower(ob.Name)] = true
	}
	failed := map[string]tiad.SourceResult{}
	for _, r := range results {
		if !r.OK {
			failed[r.Path] = r
		}
	}
	var l []string
	for _, d := range ds {
		if r, ok := failed[d.source]; ok && d.Kind == "OB" && asked[strings.ToLower(d.Name)] {
			l = append(l, fmt.Sprintf("the source of OB %q (%s) did not import: %s", d.Name, d.source, strings.Join(r.Messages, " ")))
		}
	}
	return l
}

// verifyOBs is CyclicObs.Verify.
func verifyOBs(obs []tiad.CyclicOB, blocks []projectBlock, group string) []string {
	var bad []string
	for _, ob := range obs {
		var b *projectBlock
		for i := range blocks {
			if strings.EqualFold(blocks[i].name, ob.Name) {
				b = &blocks[i]
			}
		}
		want := describeOB(ob.Number, cyclicClass, int64(ob.CycleMS)*1000)
		switch {
		case b == nil:
			bad = append(bad, fmt.Sprintf("%q is missing", ob.Name))
		case b.kind != "OB" || b.class != cyclicClass || b.number != ob.Number || b.cycle != int64(ob.CycleMS)*1000 || !owned(b.group, group):
			out := ""
			if !owned(b.group, group) {
				out = " outside the group"
			}
			bad = append(bad, fmt.Sprintf("%q is %s%s, not %s", ob.Name, describeBlock(*b), out, want))
		}
	}
	return bad
}

func foreignUnusable(what string) *failure {
	return fail(http.StatusConflict, tiad.KindUncleanSession,
		"refusing %s: an import on this session left the project as tiad did not ask (blocks outside its group changed, or a cyclic interrupt OB is not as asked). Close it without saving", what)
}

// SetOBImportIgnoresClass makes the fake TIA's OB import make a program
// cycle OB, whatever was asked.
func (f *Fake) SetOBImportIgnoresClass(on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.obImportIgnoresClass = on
}

// SetImportReplacesForeign makes every later import replace Main [OB1]
// with an FB, whatever the sources declare: TIA replacing a block the
// agent's reading missed, which the agent's check after the import must
// catch.
func (f *Fake) SetImportReplacesForeign(on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.importReplacesForeign = on
}
