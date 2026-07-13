// Package codegen turns a browsed Logix tag database into committed nautilus
// project files: an ST TYPE block mirroring the UDTs, a Go manifest wiring
// tag bindings for the eip driver, and suggested VAR_EXTERNAL declarations.
// It is the engine behind `nautilus eip import`.
package codegen

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/joyautomation/nautilus/eip"
	"github.com/joyautomation/nautilus/eip/logix"
	"github.com/joyautomation/nautilus/lang/stgen"
)

// Options select and shape the generation.
type Options struct {
	// Patterns are path.Match globs against device tag names ("Motor*",
	// "Program:MainProgram.*"). Empty selects every user tag except module
	// I/O ("Local:1:I"-style names), which must be requested explicitly.
	Patterns []string
	// WritablePatterns mark matching device tags as Writable — the runtime's
	// values propagate back to the controller. Everything else is read-only.
	WritablePatterns []string
	// Package is the Go package name for the manifest file (default "main").
	Package string
	// Host/Slot/Port are echoed into the generated wiring hint.
	Host string
	Slot int
}

// Output is the generated file set.
type Output struct {
	TypesST    string       // eip_types.st content
	ManifestGo string       // eip_manifest.go content
	Manifest   eip.Manifest // the same manifest, as a value (for tests)
	Skipped    []string     // tags that matched but could not be bound, with reasons
}

// Generate renders the file set for the selected tags of a browse.
func Generate(br *logix.BrowseResult, opts Options) (Output, error) {
	if opts.Package == "" {
		opts.Package = "main"
	}
	reg := logix.NewRegistry(br.Templates)

	var out Output
	selected := selectSymbols(br, opts.Patterns, &out.Skipped)
	if len(selected) == 0 {
		return out, fmt.Errorf("no tags matched (controller has %d user tags)", len(br.Symbols))
	}

	// Collect the templates the selected tags depend on, then order them
	// dependencies-first for both the ST block and the manifest.
	needed := map[uint16]*logix.Template{}
	for _, s := range selected {
		if s.IsStruct() {
			collectTemplates(br, s.TemplateID(), needed)
		}
	}
	ordered := orderTemplates(needed)

	// Manifest types (device-exact names — validated against the controller
	// at driver startup).
	m := eip.Manifest{}
	for _, t := range ordered {
		if t.IsString() {
			continue // string templates surface as type "STRING"
		}
		td := eip.TypeDef{Name: t.Name}
		for _, mem := range t.VisibleMembers() {
			f, ok := fieldDef(br, mem)
			if !ok {
				out.Skipped = append(out.Skipped, fmt.Sprintf("%s.%s: unsupported member type 0x%04x", t.Name, mem.Name, mem.Type))
				continue
			}
			td.Fields = append(td.Fields, f)
		}
		m.Types = append(m.Types, td)
	}

	// Tag bindings.
	names := map[string]bool{}
	for _, s := range selected {
		b, why := binding(br, reg, s, names)
		if why != "" {
			out.Skipped = append(out.Skipped, fmt.Sprintf("%s: %s", s.Name, why))
			continue
		}
		for _, p := range opts.WritablePatterns {
			if ok, _ := path.Match(p, s.Name); ok {
				b.Writable = true
				break
			}
		}
		m.Tags = append(m.Tags, b)
	}
	sort.Slice(m.Tags, func(i, j int) bool { return m.Tags[i].Name < m.Tags[j].Name })
	out.Manifest = m

	typesST, err := renderTypesST(m, opts)
	if err != nil {
		return out, err
	}
	out.TypesST = typesST
	out.ManifestGo = renderManifestGo(m, opts)
	return out, nil
}

// selectSymbols filters the browse to the requested tags.
func selectSymbols(br *logix.BrowseResult, patterns []string, skipped *[]string) []logix.Symbol {
	var out []logix.Symbol
	for _, s := range br.Symbols {
		if len(patterns) == 0 {
			// Module-defined I/O tags ("Local:1:I") need explicit opt-in:
			// their type names don't sanitize into anything readable.
			bare := strings.TrimPrefix(s.Name, "Program:")
			if strings.Contains(bare, ":") {
				continue
			}
			out = append(out, s)
			continue
		}
		for _, p := range patterns {
			if ok, _ := path.Match(p, s.Name); ok {
				out = append(out, s)
				break
			}
		}
	}
	return out
}

// collectTemplates walks nested template references.
func collectTemplates(br *logix.BrowseResult, id uint16, into map[uint16]*logix.Template) {
	if _, done := into[id]; done {
		return
	}
	t, ok := br.Templates[id]
	if !ok {
		return
	}
	into[id] = t
	for _, m := range t.Members {
		if m.IsStruct() {
			collectTemplates(br, m.NestedID(), into)
		}
	}
}

// orderTemplates returns dependencies before dependents (Logix UDTs are
// acyclic), alphabetical among peers for stable output.
func orderTemplates(ts map[uint16]*logix.Template) []*logix.Template {
	var order []*logix.Template
	state := map[uint16]int{} // 0 new, 1 visiting, 2 done
	var visit func(id uint16)
	visit = func(id uint16) {
		t, ok := ts[id]
		if !ok || state[id] != 0 {
			return
		}
		state[id] = 1
		var deps []uint16
		for _, m := range t.Members {
			if m.IsStruct() {
				deps = append(deps, m.NestedID())
			}
		}
		sort.Slice(deps, func(i, j int) bool { return deps[i] < deps[j] })
		for _, d := range deps {
			visit(d)
		}
		state[id] = 2
		order = append(order, t)
	}
	var ids []uint16
	for id := range ts {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ts[ids[i]].Name < ts[ids[j]].Name })
	for _, id := range ids {
		visit(id)
	}
	return order
}

// fieldDef maps a template member to a manifest FieldDef.
func fieldDef(br *logix.BrowseResult, m logix.Member) (eip.FieldDef, bool) {
	f := eip.FieldDef{Name: m.Name}
	if m.IsArray() {
		f.ArrayLen = int(m.Info)
	}
	if m.IsStruct() {
		nested, ok := br.Templates[m.NestedID()]
		if !ok {
			return f, false
		}
		if nested.IsString() {
			f.Type = "STRING"
			f.ArrayLen = 0 // a string member is one value, not an array
			return f, true
		}
		f.Type = nested.Name
		return f, true
	}
	t, ok := logix.TypeByCode(m.ElementaryCode())
	if !ok {
		return f, false
	}
	f.Type = t.Name
	return f, true
}

// binding maps a selected symbol to a TagBinding.
func binding(br *logix.BrowseResult, reg *logix.Registry, s logix.Symbol, names map[string]bool) (eip.TagBinding, string) {
	b := eip.TagBinding{Device: s.Name, Name: uniqueName(tagIdent(s.Name), names)}
	if s.IsStruct() {
		tmpl, ok := br.Templates[s.TemplateID()]
		if !ok {
			return b, fmt.Sprintf("template 0x%x not readable", s.TemplateID())
		}
		if tmpl.IsString() {
			b.Type = "STRING"
		} else {
			b.Type = tmpl.Name
		}
	} else {
		t, ok := logix.TypeByCode(s.ElementaryCode())
		if !ok {
			return b, fmt.Sprintf("unsupported atomic type 0x%04x", s.Type)
		}
		b.Type = t.Name
	}
	if n := s.DimCount(); n > 0 {
		if n > 1 {
			return b, "multi-dimensional arrays not supported yet"
		}
		b.ArrayLen = int(s.Dims[0])
	}
	return b, ""
}

// tagIdent converts a device tag path into an ST/Go-safe identifier:
// "Program:MainProgram.Motor" → "MainProgram_Motor".
func tagIdent(device string) string {
	s := strings.TrimPrefix(device, "Program:")
	var sb strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			sb.WriteRune(r)
		default:
			sb.WriteByte('_')
		}
	}
	id := strings.Trim(sb.String(), "_")
	for strings.Contains(id, "__") {
		id = strings.ReplaceAll(id, "__", "_")
	}
	if id == "" || (id[0] >= '0' && id[0] <= '9') {
		id = "Tag_" + id
	}
	return id
}

func uniqueName(base string, used map[string]bool) string {
	name := base
	for i := 2; used[name]; i++ {
		name = fmt.Sprintf("%s_%d", base, i)
	}
	used[name] = true
	return name
}

// typeIdent sanitizes a template name for ST (module-defined names carry
// colons).
func typeIdent(name string) string { return tagIdent(name) }

// stgenType maps a manifest type name (+ array length) to an stgen type
// expression: elementary names map to their stgen constants, the width-only
// date/time codes to the matching integer, and any other name is a reference
// to another generated UDT.
func stgenType(name string, arrayLen int) stgen.Type {
	var elem stgen.Type
	switch name {
	case "BOOL":
		elem = stgen.BOOL
	case "SINT":
		elem = stgen.SINT
	case "INT":
		elem = stgen.INT
	case "DINT":
		elem = stgen.DINT
	case "LINT":
		elem = stgen.LINT
	case "USINT":
		elem = stgen.USINT
	case "UINT":
		elem = stgen.UINT
	case "UDINT":
		elem = stgen.UDINT
	case "ULINT":
		elem = stgen.ULINT
	case "BYTE":
		elem = stgen.BYTE
	case "WORD":
		elem = stgen.WORD
	case "DWORD":
		elem = stgen.DWORD
	case "LWORD":
		elem = stgen.LWORD
	case "REAL":
		elem = stgen.REAL
	case "LREAL":
		elem = stgen.LREAL
	case "STRING":
		elem = stgen.STRING
	case "STIME", "TIME_OF_DAY", "DATE":
		elem = stgen.DINT // ST has no direct equivalent; DINT matches the width
	case "DATE_AND_TIME":
		elem = stgen.LINT
	default:
		elem = stgen.Ref(typeIdent(name))
	}
	if arrayLen > 0 {
		return stgen.ArrayOf(elem, 0, arrayLen-1)
	}
	return elem
}

// renderTypesST emits the TYPE block (built and validated by stgen) plus a
// commented VAR_EXTERNAL suggestion for every binding. It errors if the
// generated types don't compile — a broken import is caught here, not on
// disk.
func renderTypesST(m eip.Manifest, opts Options) (string, error) {
	var sb strings.Builder
	fmt.Fprintf(&sb, "(*\n  Generated by `nautilus eip import` from %s (slot %d).\n", opts.Host, opts.Slot)
	sb.WriteString("  Do not edit — re-run the import when the controller program changes.\n*)\n\n")

	structs := make([]*stgen.StructDef, 0, len(m.Types))
	for _, t := range m.Types {
		s := stgen.Struct(typeIdent(t.Name))
		for _, f := range t.Fields {
			s.AddField(stgen.Field(f.Name, stgenType(f.Type, f.ArrayLen)))
		}
		structs = append(structs, s)
	}
	block, err := stgen.Render(structs...)
	if err != nil {
		return "", err
	}
	if block != "" {
		sb.WriteString(block)
		sb.WriteString("\n")
	}

	// A ready-to-paste VAR_EXTERNAL block, in a comment (it has meaning only
	// inside a POU, so it isn't compiled here).
	varFields := make([]stgen.FieldDef, 0, len(m.Tags))
	for _, b := range m.Tags {
		varFields = append(varFields, stgen.Field(b.Name, stgenType(b.Type, b.ArrayLen)))
	}
	sb.WriteString("(* Add the tags your program uses to its VAR_EXTERNAL block:\n\n")
	sb.WriteString(stgen.VarBlock("VAR_EXTERNAL", varFields...))
	sb.WriteString("*)\n")
	return sb.String(), nil
}

// renderManifestGo emits the Go manifest file.
func renderManifestGo(m eip.Manifest, opts Options) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "// Code generated by `nautilus eip import` from %s (slot %d). DO NOT EDIT.\n", opts.Host, opts.Slot)
	sb.WriteString("// Re-run the import when the controller program changes.\n\n")
	fmt.Fprintf(&sb, "package %s\n\n", opts.Package)
	sb.WriteString("import \"github.com/joyautomation/nautilus/eip\"\n\n")
	sb.WriteString("// EIPManifest binds this project to the controller's tags. Wire it up:\n//\n")
	fmt.Fprintf(&sb, "//\tdriver, err := eip.New(%q, EIPManifest, eip.WithSlot(%d))\n", opts.Host, opts.Slot)
	sb.WriteString("//\tdriver.Start(ctx)\n")
	sb.WriteString("//\trt, err := runtime.New(runtime.Options{\n")
	sb.WriteString("//\t\tProgram: program,\n//\t\tDriver:  driver,\n")
	sb.WriteString("//\t\tInputs:  driver.InputNames(),\n//\t\tOutputs: driver.OutputNames(),\n//\t})\n")
	sb.WriteString("var EIPManifest = eip.Manifest{\n")
	if len(m.Types) > 0 {
		sb.WriteString("\tTypes: []eip.TypeDef{\n")
		for _, t := range m.Types {
			fmt.Fprintf(&sb, "\t\t{Name: %q, Fields: []eip.FieldDef{\n", t.Name)
			for _, f := range t.Fields {
				if f.ArrayLen > 0 {
					fmt.Fprintf(&sb, "\t\t\t{Name: %q, Type: %q, ArrayLen: %d},\n", f.Name, f.Type, f.ArrayLen)
				} else {
					fmt.Fprintf(&sb, "\t\t\t{Name: %q, Type: %q},\n", f.Name, f.Type)
				}
			}
			sb.WriteString("\t\t}},\n")
		}
		sb.WriteString("\t},\n")
	}
	sb.WriteString("\tTags: []eip.TagBinding{\n")
	for _, b := range m.Tags {
		fmt.Fprintf(&sb, "\t\t{Name: %q, Device: %q, Type: %q", b.Name, b.Device, b.Type)
		if b.ArrayLen > 0 {
			fmt.Fprintf(&sb, ", ArrayLen: %d", b.ArrayLen)
		}
		if b.Writable {
			sb.WriteString(", Writable: true")
		}
		sb.WriteString("},\n")
	}
	sb.WriteString("\t},\n}\n")
	return sb.String()
}
