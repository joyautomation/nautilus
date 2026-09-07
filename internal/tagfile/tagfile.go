// Package tagfile renders a nautilus tag file — the bare YAML list a project
// composes through `tag-files:`.
//
// Every generator emits this shape: `nautilus eip import` from a live
// controller, `nautilus tags import-csv` from a spreadsheet export, and any
// project-shaped script that targets the published JSON Schema. The renderer
// is shared so the shipped generators cannot drift from each other, and the
// schema is what keeps everyone else honest.
package tagfile

import (
	"fmt"
	"sort"
	"strings"
)

// Tag is one entry. Empty fields are omitted, so a generator supplies only
// what its source actually knows — an importer that cannot read descriptions
// leaves Desc empty rather than inventing one.
type Tag struct {
	Name string
	Role string
	Type string
	// Init is a scalar (bool/float64/int/int64/string) for an untyped or
	// scalar tag, or a map[string]any for a struct-typed tag's per-member
	// seed — nested map[string]any for a nested struct member. nil omits
	// init: entirely (zero-of-type, for a Type tag).
	Init any
	Unit string
	Desc string
}

// Render writes the header comment (each line prefixed with "# ") followed by
// the tags, sorted by name.
//
// Sorted because the review model depends on it: a regenerated file must diff
// against the last one, showing what changed on the controller or in the
// spreadsheet, rather than reshuffling and hiding it.
func Render(header []string, tags []Tag) ([]byte, error) {
	var b strings.Builder
	for _, line := range header {
		if line == "" {
			b.WriteString("#\n")
			continue
		}
		fmt.Fprintf(&b, "# %s\n", line)
	}

	sorted := append([]Tag(nil), tags...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	seen := map[string]bool{}
	for _, t := range sorted {
		if t.Name == "" {
			return nil, fmt.Errorf("a tag has no name")
		}
		if seen[t.Name] {
			// The loader would reject this on load with a message about two
			// sources; catching it here names the generator instead, which is
			// where the fix is.
			return nil, fmt.Errorf("tag %q generated twice — a tag may be declared once", t.Name)
		}
		seen[t.Name] = true

		fmt.Fprintf(&b, "- { name: %s, role: %s", t.Name, t.Role)
		if t.Type != "" {
			fmt.Fprintf(&b, ", type: %s", t.Type)
		}
		if t.Init != nil {
			fmt.Fprintf(&b, ", init: %s", renderInit(t.Init))
		}
		if t.Unit != "" {
			fmt.Fprintf(&b, ", unit: %s", quote(t.Unit))
		}
		if t.Desc != "" {
			fmt.Fprintf(&b, ", desc: %s", quote(t.Desc))
		}
		b.WriteString(" }\n")
	}
	return []byte(b.String()), nil
}

// renderInit renders a tag's init value: a scalar as before, or — for a
// struct-typed tag's per-member seed (internal/project's `init:` mapping) —
// a flow-style YAML mapping, recursively for a nested struct member. This is
// what lets a generator emit `init: { RAWMIN: 6553.0, LVL: { CTL1HSP: 85.0 } }`
// instead of only ever seeding zero-of-type, and it is the same shape the
// loader (`ir.SeedFromInit`) accepts back — round-tripping a manifest tag
// through `nautilus eip tags` / `nautilus tags import-csv` and back.
//
// Keys are sorted for the same reason the tag list itself is: a regenerated
// file must diff cleanly, not reshuffle.
func renderInit(v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		return scalar(v)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s: %s", k, renderInit(m[k]))
	}
	return "{ " + strings.Join(parts, ", ") + " }"
}

// scalar renders a leaf init value. Floats keep a decimal point so YAML
// decodes them as float64 and not int — the tag store distinguishes DINT
// from REAL, and "0" instead of "0.0" would silently retype a tag.
func scalar(v any) string {
	switch x := v.(type) {
	case bool:
		return fmt.Sprintf("%t", x)
	case float64:
		if x == float64(int64(x)) {
			return fmt.Sprintf("%.1f", x)
		}
		return fmt.Sprintf("%v", x)
	case int, int64:
		return fmt.Sprintf("%d", x)
	case string:
		return quote(x)
	}
	return quote(fmt.Sprintf("%v", v))
}

// quote emits a double-quoted YAML scalar. Flow mappings make bare strings
// ambiguous the moment they contain a comma or a brace, and units are full
// of neither-quite-safe characters ("°C", "%", "L/s").
func quote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return `"` + r.Replace(s) + `"`
}
