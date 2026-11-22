package tiadtest

import (
	"fmt"
	"net/http"
	"regexp"
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
}

var (
	headLine = regexp.MustCompile(`(?i)^[ \t]*(ORGANIZATION_BLOCK|FUNCTION_BLOCK|FUNCTION|DATA_BLOCK|TYPE)[ \t]+("([^"]+)"|([A-Za-z_][A-Za-z0-9_]*))`)
	comments = regexp.MustCompile(`(?s)\(\*.*?\*\)|/\*.*?\*/`)
	lineNote = regexp.MustCompile(`//[^\n]*`)
)

// declaredBlocks is Ownership.Sources: every header's kind and name, any
// case, quoted or bare, comments blanked first (line breaks kept).
func declaredBlocks(text string) []tiad.Block {
	text = comments.ReplaceAllStringFunc(text, func(c string) string { return strings.Repeat("\n", strings.Count(c, "\n")) + " " })
	var out []tiad.Block
	for _, line := range strings.Split(text, "\n") {
		if !strings.Contains(line, `"`) {
			line = lineNote.ReplaceAllString(line, "")
		}
		m := headLine.FindStringSubmatch(strings.TrimRight(line, "\r"))
		if m == nil {
			continue
		}
		name := m[3]
		if name == "" {
			name = m[4]
		}
		out = append(out, tiad.Block{Name: name, Kind: blockKind[strings.ToUpper(m[1])]})
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
// Blocks reads them: Main at the root, then every imported block but data
// types in its group, numbered from 1 per kind in name order after Main's.
func (s *session) projectBlocks() []projectBlock {
	next := map[string]int{"OB": 1, "FB": 1, "FC": 1, "DB": 1}
	mainKind := "OB"
	if s.mainReplaced {
		mainKind = "FB"
	}
	l := []projectBlock{{name: "Main", kind: mainKind, number: next[mainKind], group: ""}}
	next[mainKind]++
	for _, b := range s.blocks {
		if _, ok := next[b.Kind]; !ok {
			continue
		}
		l = append(l, projectBlock{name: b.Name, kind: b.Kind, number: next[b.Kind], group: s.groups[strings.ToLower(b.Name)]})
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

// foreignConflicts is Ownership.Conflicts plus its Refusal: nil when no
// declared block (data types aside) names a block outside group.
func foreignConflicts(declared []tiad.Block, blocks []projectBlock, group string) *failure {
	names := map[string]bool{}
	for _, d := range declared {
		if d.Kind != "UDT" {
			names[strings.ToLower(d.Name)] = true
		}
	}
	var named []string
	for _, b := range blocks {
		if !owned(b.group, group) && names[strings.ToLower(b.name)] {
			named = append(named, b.label()+" in "+groupName(b.group))
		}
	}
	if len(named) == 0 {
		return nil
	}
	return fail(http.StatusConflict, tiad.KindForeignBlock,
		"a source declares %s, outside group %q: importing it would replace that block. Nothing was imported. Rename the block in the source (a prefix the project does not use)",
		strings.Join(named, ", "), group)
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

// obConflict is the fake TIA's refusal of an OB source, as seen on the
// TIA VM (2026-11-22): while another OB holds number 1, the source's
// block generation stops. The cause (an OB source is a program cycle OB,
// numbered 1) is inferred, not proven.
func obConflict(blocks []projectBlock, heads []tiad.Block) string {
	for _, h := range heads {
		if h.Kind != "OB" {
			continue
		}
		for _, b := range blocks {
			if b.kind == "OB" && b.number == 1 && !strings.EqualFold(b.name, h.Name) {
				return h.Name
			}
		}
	}
	return ""
}

func foreignUnusable(what string) *failure {
	return fail(http.StatusConflict, tiad.KindUncleanSession,
		"refusing %s: an import on this session changed program blocks outside its group. Close it without saving", what)
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
