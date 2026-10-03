package l5x

import (
	"fmt"
	"sort"
	"strings"
)

// The logic-only view of an export, for drift (docs/design/logix-authoring.md
// §5.7). Normalize answers "is this the same export?", which is the right
// question for a project file. A RUNNING controller's export is never the
// same: its tag values move every scan. Drift against a controller asks a
// narrower question — is the LOGIC the same? — and that is: the programs,
// their routines, every rung's text and comment, and every tag's name,
// scope, type and shape. Never a value.

// Logic is what a controller runs, with no values in it.
type Logic struct {
	Tags     map[string]string // "<scope>/<name>" → "<DataType>[dims]"
	Routines map[string]*RoutineLogic
}

// RoutineLogic is one routine's body, keyed "Program/Routine".
type RoutineLogic struct {
	Type  string
	Rungs []Rung // RLL: Text and Comment are compared
	Text  string // ST
}

// LogicOf extracts the logic-only view.
func LogicOf(f *File) *Logic {
	l := &Logic{Tags: map[string]string{}, Routines: map[string]*RoutineLogic{}}
	if f == nil || f.Controller == nil {
		return l
	}
	addTag := func(scope string, t *Tag) {
		shape := t.DataType
		if t.Dimensions != "" {
			shape += "[" + t.Dimensions + "]"
		}
		l.Tags[scope+"/"+t.Name] = shape
	}
	for _, t := range f.Controller.Tags {
		addTag("", t)
	}
	for _, p := range f.Controller.Programs {
		for _, t := range p.Tags {
			addTag(p.Name, t)
		}
		for _, r := range p.Routines {
			l.Routines[p.Name+"/"+r.Name] = &RoutineLogic{Type: r.Type, Rungs: r.Rungs, Text: r.Text}
		}
	}
	return l
}

// LogicDiff lists every logic difference between two exports, in the
// words an engineer needs: which tag, which rung, what each side has.
// Empty means the same logic.
func LogicDiff(a, b *Logic) []string {
	var out []string
	for _, k := range sortedTagKeys(a.Tags, b.Tags) {
		av, aok := a.Tags[k]
		bv, bok := b.Tags[k]
		switch {
		case !bok:
			out = append(out, fmt.Sprintf("tag %s (%s): only on the first side", tagLabel(k), av))
		case !aok:
			out = append(out, fmt.Sprintf("tag %s (%s): only on the second side", tagLabel(k), bv))
		case av != bv:
			out = append(out, fmt.Sprintf("tag %s: %s vs %s", tagLabel(k), av, bv))
		}
	}
	for _, k := range sortedRoutineKeys(a.Routines, b.Routines) {
		ar, aok := a.Routines[k]
		br, bok := b.Routines[k]
		switch {
		case !bok:
			out = append(out, fmt.Sprintf("routine %s: only on the first side", k))
			continue
		case !aok:
			out = append(out, fmt.Sprintf("routine %s: only on the second side", k))
			continue
		}
		if ar.Type != br.Type {
			out = append(out, fmt.Sprintf("routine %s: %s vs %s", k, ar.Type, br.Type))
			continue
		}
		if ar.Text != br.Text {
			out = append(out, fmt.Sprintf("routine %s: ST body differs", k))
		}
		n := len(ar.Rungs)
		if len(br.Rungs) != n {
			out = append(out, fmt.Sprintf("routine %s: %d rungs vs %d", k, n, len(br.Rungs)))
			if len(br.Rungs) < n {
				n = len(br.Rungs)
			}
		}
		for i := 0; i < n; i++ {
			x, y := ar.Rungs[i], br.Rungs[i]
			if x.Text != y.Text {
				out = append(out, fmt.Sprintf("routine %s rung %d:\n    %s\n    %s", k, i, x.Text, y.Text))
			} else if x.Comment != y.Comment {
				out = append(out, fmt.Sprintf("routine %s rung %d: comment differs", k, i))
			}
		}
	}
	return out
}

func tagLabel(k string) string {
	scope, name, _ := strings.Cut(k, "/")
	if scope == "" {
		return name
	}
	return scope + "." + name
}

func sortedTagKeys(a, b map[string]string) []string {
	seen := map[string]bool{}
	for k := range a {
		seen[k] = true
	}
	for k := range b {
		seen[k] = true
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedRoutineKeys(a, b map[string]*RoutineLogic) []string {
	seen := map[string]bool{}
	for k := range a {
		seen[k] = true
	}
	for k := range b {
		seen[k] = true
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
