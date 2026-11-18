package devprofile

import (
	"sort"
	"strings"
)

// TypesST renders the struct type of each profile, one TYPE block, a
// member per point in profile order: the order a device's struct is
// delivered in. Profiles sharing a type name render it once (whether
// their points agree is the manifest's check). "" for none.
func TypesST(profiles []*Profile) string {
	byName := map[string]*Profile{}
	var names []string
	for _, p := range profiles {
		n := p.TypeName()
		if _, ok := byName[n]; ok {
			continue
		}
		byName[n] = p
		names = append(names, n)
	}
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("(* The struct types of this project's devices: (nautilus.yaml), one per\n" +
		"   profile it vendored in profiles/. Derived when the project loads. *)\n")
	b.WriteString("TYPE\n")
	for _, n := range names {
		p := byName[n]
		b.WriteString("  " + n + " : STRUCT\n")
		for _, pt := range p.Points {
			b.WriteString("    " + pt.Name + " : " + strings.ToUpper(pt.Type) + ";")
			if d := comment(pt.Desc); d != "" {
				b.WriteString(" (* " + d + " *)")
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
