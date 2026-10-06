package runtime

import (
	"fmt"
	"sort"
	"strings"

	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/lang/st"
)

// The project's tags are in scope in every PROGRAM without a VAR_EXTERNAL
// (#177/#210): the tag table is the declaration, as a Codesys GVL, a TIA
// PLC tag table or a Logix controller scope is. VAR_EXTERNAL stays legal
// as the explicit form, and must then agree with a type the tag states.
// A local VAR of the same name shadows the tag inside that program (naut
// check warns). FUNCTION_BLOCK and FUNCTION bodies do NOT see tags
// implicitly: a block that reaches a tag says so in its own VAR_EXTERNAL,
// so it stays self-contained and reusable across projects.
//
// TagScope is that table, resolved: each tag's type, from
//
//  1. its `type:` — any IEC elementary type (BOOL, SINT…ULINT, BYTE…LWORD,
//     REAL, LREAL, TIME, STRING), a TYPE the project's ST declares, or an
//     ARRAY of either; or else
//  2. its `init:` value — TRUE/FALSE is BOOL, a number REAL, a `T#…`
//     duration TIME, other text STRING (the inference a seed has always
//     had); or else
//  3. nothing: the tag is known but untyped, and a program naming it
//     without declaring it is told to give it a type.
//
// A program that declares the tag itself (VAR_EXTERNAL) keeps its own
// type, as before; programs that disagree about one tag's type are an
// error (CheckBindings), which a `type:` on the tag resolves.
type TagScope struct {
	// Implicit maps every tag name to its type; nil for an untyped tag.
	Implicit map[string]*ir.Type
	// Typed holds the tags whose type: states their type — the ones an
	// explicit declaration must agree with.
	Typed map[string]*ir.Type
}

// LowerOpts is the compile context a program in this project lowers with.
func (s TagScope) LowerOpts() st.LowerOpts {
	return st.LowerOpts{ImplicitGlobals: s.Implicit, TagTypes: s.Typed}
}

// ResolveTagScope resolves tags against types, the project's TYPE table.
// A tag whose type: does not resolve is reported in errs (in tag order)
// and left untyped in the scope, so a lenient caller — the language
// server, mid-edit — still sees the rest.
func ResolveTagScope(tags []TagDef, types map[string]*ir.Type) (TagScope, []error) {
	s := TagScope{Implicit: map[string]*ir.Type{}, Typed: map[string]*ir.Type{}}
	var errs []error
	for _, d := range tags {
		if d.Name == "" {
			continue
		}
		if d.Type != "" {
			t, err := ResolveTagType(d.Type, types)
			if err != nil {
				errs = append(errs, fmt.Errorf("tag %s: %w", d.Name, err))
				s.Implicit[d.Name] = nil
				continue
			}
			s.Implicit[d.Name], s.Typed[d.Name] = t, t
			continue
		}
		s.Implicit[d.Name] = typeOfInit(d.Init)
	}
	return s, errs
}

// ResolveTagType resolves a tag's `type:` — an elementary type, a project
// TYPE, or an ARRAY of either — with a message that names what the
// project does declare when it is none of those.
func ResolveTagType(name string, types map[string]*ir.Type) (*ir.Type, error) {
	t, err := st.ResolveTypeName(name, types)
	if err == nil {
		return t, nil
	}
	return nil, fmt.Errorf("type %s is neither an IEC elementary type (BOOL, INT, DINT, REAL, "+
		"TIME, STRING, …) nor a TYPE this project's ST declares (known: %s) — %v", name, knownTypes(types), err)
}

// typeOfInit is the type a seed implies: the same inference the tag store
// makes from a bare value. A struct-shaped init with no type: has none.
func typeOfInit(v any) *ir.Type {
	switch v.(type) {
	case bool:
		return ir.BoolT
	case string:
		// An ST duration literal is a TIME; any other text a STRING.
		if x := strings.ToUpper(strings.TrimSpace(v.(string))); strings.HasPrefix(x, "T#") || strings.HasPrefix(x, "TIME#") {
			if _, err := ir.ParseDuration(x); err == nil {
				return ir.TimeT
			}
		}
		return ir.StringT
	case int, int64, uint64, float64:
		return ir.RealT
	}
	return nil
}

// sourceTypes is the TYPE table a program's composed source declares,
// resolved without lowering it: what a tag's type: resolves against
// before any program has compiled. Best effort — a source that does not
// parse contributes nothing here and reports its own error when it
// compiles.
func sourceTypes(src string) map[string]*ir.Type {
	prog, err := parseSource(src)
	if err != nil {
		return nil
	}
	types, err := st.Types(prog)
	if err != nil {
		return nil
	}
	return types
}

// binding is one program's view of one tag.
type binding struct {
	task string
	t    *ir.Type
}

// checkBindings reports a tag that two programs bind with different types
// — one declaring `Count : DINT` while another reads Count implicitly as
// the REAL its `init: 0` implies, say. The tag store holds one value, so
// one of them would read it wrong.
func checkBindings(main *Program, tasks []*taskRun, tags []TagDef) error {
	tagged := make(map[string]string, len(tags))
	for _, d := range tags {
		tagged[ir.NameKey(d.Name)] = d.Name
	}
	seen := map[string]binding{}
	check := func(task string, globals map[string]*ir.Type) error {
		names := make([]string, 0, len(globals))
		for n := range globals {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			t := globals[n]
			k := ir.NameKey(n)
			tag, isTag := tagged[k]
			if !isTag || t == nil {
				continue
			}
			prev, ok := seen[k]
			if !ok {
				seen[k] = binding{task, t}
				continue
			}
			if !ir.SameShape(prev.t, t) {
				return fmt.Errorf("tag %s is %s in task %s but %s in task %s — a tag has one type; "+
					"give it a type: in the manifest (a program that does not declare it takes its "+
					"type from type:, else from init:)", tag, prev.t, prev.task, t, task)
			}
		}
		return nil
	}
	if err := check(MainTaskName, main.Globals()); err != nil {
		return err
	}
	for _, tr := range tasks {
		if err := check(tr.name, tr.prog.Globals()); err != nil {
			return err
		}
	}
	return nil
}

// ShadowedTags lists a PROGRAM's own variables — VAR, VAR_TEMP, VAR
// CONSTANT, … — that share a name with a project tag. IEC scoping lets a
// local hide the global of the same name, so this compiles; inside that
// program the name is the local, and the tag goes unread and unwritten,
// which is `naut check`'s warning to give. prog is the file's own parse
// (no prelude); FUNCTION_BLOCK and FUNCTION locals never shadow a tag,
// since a block does not see tags implicitly.
func ShadowedTags(prog *st.Program, tags []TagDef) []st.VarDecl {
	if prog == nil || len(tags) == 0 {
		return nil
	}
	names := make(map[string]bool, len(tags))
	for _, d := range tags {
		names[ir.NameKey(d.Name)] = true
	}
	var out []st.VarDecl
	for _, vb := range prog.VarBlocks {
		if vb.FileScope || vb.Kind == "VAR_GLOBAL" || vb.Kind == "VAR_EXTERNAL" {
			continue
		}
		for _, vd := range vb.Variables {
			if names[ir.NameKey(vd.Name)] {
				out = append(out, vd)
			}
		}
	}
	return out
}

// enumTags finds the tags whose type is an enumeration: by a program's
// binding, or by the tag's own type:. Keyed by ir.NameKey.
func enumTags(tags []TagDef, types, globals map[string]*ir.Type) map[string]*ir.Type {
	out := map[string]*ir.Type{}
	for name, t := range globals {
		if t != nil && t.Enum != nil {
			out[ir.NameKey(name)] = t
		}
	}
	for _, d := range tags {
		if d.Type == "" {
			continue
		}
		if t, err := ResolveTagType(d.Type, types); err == nil && t.Enum != nil {
			out[ir.NameKey(d.Name)] = t
		}
	}
	return out
}
