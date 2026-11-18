package devprofile

import (
	"fmt"
	"path"
	"sort"
	"strings"
)

// Struct is one device type: its name and members, the points a device
// of it selects, in profile order.
type Struct struct {
	Name   string
	Points []Point
}

// Select is the points a device row selects with globs over point names
// (case-insensitive), in profile order; every point for none. A glob that
// matches nothing is an error, so a typo is not a device that silently
// lacks a point.
func (p *Profile) Select(globs []string) ([]Point, error) {
	for _, g := range globs {
		if _, err := path.Match(strings.ToLower(g), ""); err != nil {
			return nil, fmt.Errorf("points: %q: %w", g, err)
		}
	}
	matched := make([]bool, len(globs))
	var out []Point
	for _, pt := range p.Points {
		take := len(globs) == 0
		for i, g := range globs {
			if ok, _ := path.Match(strings.ToLower(g), strings.ToLower(pt.Name)); ok {
				take, matched[i] = true, true
			}
		}
		if take {
			out = append(out, pt)
		}
	}
	for i, ok := range matched {
		if !ok {
			return nil, fmt.Errorf("points: %q matches no point of %s (naut devices show %s lists them)", globs[i], p.ID, p.ID)
		}
	}
	return out, nil
}

// TypesST renders each device type, one TYPE block, a member per selected
// point in profile order: the order a device's struct is delivered in. A
// name given twice renders once (whether the two agree is the manifest's
// check). "" for none.
func TypesST(structs []Struct) string {
	byName := map[string]Struct{}
	var names []string
	for _, st := range structs {
		if _, ok := byName[st.Name]; ok {
			continue
		}
		byName[st.Name] = st
		names = append(names, st.Name)
	}
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("(* The struct types of this project's devices: (nautilus.yaml), from the\n" +
		"   profiles it vendored in profiles/. Derived when the project loads. *)\n")
	b.WriteString("TYPE\n")
	for _, n := range names {
		fmt.Fprintf(&b, "  %s : STRUCT\n", n)
		for _, pt := range byName[n].Points {
			fmt.Fprintf(&b, "    %s : %s;", pt.Name, strings.ToUpper(pt.Type))
			if d := comment(pt.Desc); d != "" {
				fmt.Fprintf(&b, " (* %s *)", d)
			}
			b.WriteString("\n")
		}
		b.WriteString("  END_STRUCT;\n")
	}
	b.WriteString("END_TYPE\n")
	return b.String()
}

// comment makes text safe inside an ST comment.
func comment(s string) string {
	s = strings.ReplaceAll(s, "*)", "* )")
	s = strings.ReplaceAll(s, "(*", "( *")
	return strings.Join(strings.Fields(s), " ")
}

// TypesPath is the virtual project path the derived types are composed
// under, so a diagnostic names where they come from.
const TypesPath = "nautilus.yaml#devices"
