package writer

import (
	"fmt"
	"sort"
	"strings"

	"github.com/joyautomation/nautilus/lang/fbd"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/lang/ld"
	"github.com/joyautomation/nautilus/lang/st"
)

// Structured types (#1299). A STRUCT the project declares (in a library
// file or in a program's own TYPE block) is written inline wherever it is
// used: a manifest tag of the type becomes a `Struct … END_STRUCT` member
// of the tags DB, a program variable one of the FB's. TIA would also take
// it as a PLC data type, but tiad does not import data types yet (TIA puts
// them beside a customer's own, Ownership.TypesRefused), so nothing here
// needs one: member access, member-wise assignment and start values work
// the same on an anonymous struct. Named types come with user function
// blocks (#1301), whose struct parameters need them.
//
// Start values follow nautilus exactly: every member starts at its type's
// declared initial value (`Temp : REAL := 72.5;` inside the STRUCT), and a
// manifest tag's init: map overrides the members it names, at any depth.
// nautilus's own seeding (ir.SeedFromInit) checks the map first, so a
// member it does not know is refused with nautilus's words.

// library is what the project's library sources declare: the TYPEs the
// writer writes, and the names it does not translate yet.
type library struct {
	// structs are the STRUCT TYPEs, by lower-cased name: the library's and
	// the programs' own.
	structs map[string]*structDef
	// other TYPEs: enumerations, aliases (lower-cased name -> as declared).
	otherTypes map[string]string
	// conflicts are TYPE names two sources declare differently: nautilus
	// lowers each program with its own, while the writer's tags DB and
	// member typing take one per name, so it refuses them.
	conflicts map[string]string
	// The library's FUNCTION_BLOCKs, FUNCTIONs and global constants
	// (lower-cased name -> as declared): with the library in scope
	// nautilus compiles a program that uses them, and the writer refuses
	// them as its gap (#1301).
	fbs, funcs, consts map[string]string
	// enumMembers are the enumerations' member names, each with its
	// type's name: a bare member (Idle) is an enumeration value, the
	// writer's gap until enums are written.
	enumMembers map[string]string
	// types is nautilus's own resolution of every TYPE, for seeding a start
	// value the way the runtime does (nil when the TYPEs do not resolve:
	// nautilus's lowering then refuses the program anyway).
	types map[string]*ir.Type
	// src is the library as one ST source, put ahead of each program for
	// nautilus's lowering as `naut check` composes it.
	src string
}

type structDef struct {
	name   string // as declared
	fields []st.VarDecl
}

// libraryST is each library source as ST: an .st library as it is, a
// ladder or FBD library through its transpilers. A source that reads as
// none is left out (check reports it).
func libraryST(libs []string) []string {
	var out []string
	for _, src := range libs {
		if _, err := st.Parse(src); err == nil {
			out = append(out, src)
			continue
		}
		if f, _, err := ld.TranspileWithLines(src, libs...); err == nil {
			if s, _, err := fbd.TranspileWithLines(f); err == nil {
				if _, err := st.Parse(s); err == nil {
					out = append(out, s)
					continue
				}
			}
		}
		if s, _, err := fbd.TranspileWithLines(src); err == nil {
			if _, err := st.Parse(s); err == nil {
				out = append(out, s)
			}
		}
	}
	return out
}

// loadLibrary reads the library sources and the programs' own TYPE
// blocks.
func loadLibrary(libs []string, own []*st.Program) *library {
	lib := &library{structs: map[string]*structDef{}, otherTypes: map[string]string{}, conflicts: map[string]string{},
		fbs: map[string]string{}, funcs: map[string]string{}, consts: map[string]string{}, enumMembers: map[string]string{}}
	srcs := libraryST(libs)
	var all st.Program
	for _, src := range srcs {
		prog, err := st.Parse(src)
		if err != nil {
			continue
		}
		all.TypeDecls = append(all.TypeDecls, prog.TypeDecls...)
		all.FBDecls = append(all.FBDecls, prog.FBDecls...)
		all.GlobalConsts = append(all.GlobalConsts, prog.GlobalConsts...)
		for _, f := range prog.FBDecls {
			lib.fbs[strings.ToLower(f.Name)] = f.Name
		}
		for _, f := range prog.FuncDecls {
			lib.funcs[strings.ToLower(f.Name)] = f.Name
		}
		for _, vb := range append(prog.GlobalConsts, prog.VarBlocks...) {
			if vb.Kind == "VAR_GLOBAL" {
				for _, v := range vb.Variables {
					lib.consts[strings.ToLower(v.Name)] = v.Name
				}
			}
		}
	}
	if len(srcs) > 0 {
		lib.src = strings.Join(srcs, "\n") + "\n"
	}
	// The programs' own TYPEs: one declared the same way in several
	// programs is one type; declared differently, it is refused.
	seen := map[string]string{}
	for _, td := range all.TypeDecls {
		seen[strings.ToLower(td.Name)] = typeSignature(td)
	}
	for _, p := range own {
		for _, td := range p.TypeDecls {
			k, sig := strings.ToLower(td.Name), typeSignature(td)
			if prev, ok := seen[k]; ok {
				if prev != sig {
					lib.conflicts[k] = td.Name
				}
				continue
			}
			seen[k] = sig
			all.TypeDecls = append(all.TypeDecls, td)
		}
	}
	for _, td := range all.TypeDecls {
		k := strings.ToLower(td.Name)
		if s, ok := td.Type.(*st.StructType); ok {
			lib.structs[k] = &structDef{name: td.Name, fields: s.Fields}
			continue
		}
		lib.otherTypes[k] = td.Name
		if e, ok := td.Type.(*st.EnumType); ok {
			for _, m := range e.Members {
				lib.enumMembers[strings.ToLower(m.Name)] = td.Name
			}
		}
	}
	if types, err := st.Types(&all); err == nil {
		lib.types = types
	}
	return lib
}

// typeSignature is a TYPE's declaration with its positions left out: two
// declarations with one signature are one type.
func typeSignature(td st.TypeDecl) string {
	s, ok := td.Type.(*st.StructType)
	if !ok {
		return "other:" + td.Type.String()
	}
	var b strings.Builder
	for _, f := range s.Fields {
		init := ""
		if f.Initial != nil {
			init, _ = constValue(loadLibrary(nil, nil), f.Initial)
			if init == "" {
				init = "?"
			}
		}
		fmt.Fprintf(&b, "%s:%s:%s;", strings.ToLower(f.Name), strings.ToUpper(strings.TrimSpace(f.Datatype)), init)
	}
	return b.String()
}

// structOf is the STRUCT a declared type names, or nil (also for a name
// two programs declare differently: sclType refuses it).
func (w *writer) structOf(typ string) *structDef {
	u := strings.TrimSpace(typ)
	if u == "" || elementary[strings.ToUpper(u)] {
		return nil
	}
	if _, ok := w.lib.conflicts[strings.ToLower(u)]; ok {
		return nil
	}
	return w.lib.structs[strings.ToLower(u)]
}

// fieldOf is the member of a struct, case-insensitively.
func (sd *structDef) fieldOf(name string) (st.VarDecl, bool) {
	for _, f := range sd.fields {
		if strings.EqualFold(f.Name, name) {
			return f, true
		}
	}
	return st.VarDecl{}, false
}

// typeRefusal words why a declared type cannot be written, or "". A
// struct is refused for its first member that cannot be (once per type:
// the caller reports it once, not once per tag of the type).
func (w *writer) typeRefusal(typ string, seen map[string]bool) string {
	sd := w.structOf(typ)
	if sd == nil {
		_, why := w.sclType(typ)
		return why
	}
	k := strings.ToLower(sd.name)
	if seen[k] {
		return gapMessage(gapStructured, "the recursive type "+sd.name, "")
	}
	seen[k] = true
	defer delete(seen, k)
	for _, f := range sd.fields {
		if why := w.typeRefusal(f.Datatype, seen); why != "" {
			return fmt.Sprintf("member %s: %s", f.Name, why)
		}
		if f.Initial != nil && w.structOf(f.Datatype) != nil {
			return fmt.Sprintf("member %s: %s", f.Name, gapMessage(gapStructured, "a struct member's initializer", ""))
		}
		if f.Initial != nil {
			if _, why := constValue(w.lib, f.Initial); why != "" {
				return fmt.Sprintf("member %s: its initial value: %s", f.Name, why)
			}
		}
	}
	return ""
}

// sclType maps a declared type that is not a struct to its SCL spelling,
// or says why not.
func (w *writer) sclType(t string) (string, string) {
	typ, why := sclType(t)
	if why == "" {
		return typ, ""
	}
	k := strings.ToLower(strings.TrimSpace(t))
	if name, ok := w.lib.conflicts[k]; ok {
		return "", gapMessage(gapStructured, "the TYPE "+name+", which two programs declare differently", "nautilus gives each program its own; the tags DB takes one")
	}
	if name, ok := w.lib.fbs[k]; ok {
		return "", gapMessage(gapPOUs, "the project's function block "+name, "")
	}
	if name, ok := w.lib.otherTypes[k]; ok {
		return "", gapMessage(gapStructured, "the type "+name, "enumerations and type aliases")
	}
	if fb := ir.LookupFBType(t); fb != nil && fb.Kind == ir.TypeFB && !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(t)), "ARRAY") {
		// A block of nautilus's library with no TIA instruction to map
		// to (PID, TONR, …).
		return "", gapMessage(gapPOUs, "nautilus's function block "+strings.ToUpper(strings.TrimSpace(t)), "it has no TIA instruction of the same behaviour")
	}
	return "", why
}

// declType prints a declared type for a declaration: an elementary type
// as SCL spells it, a struct as an inline `Struct … END_STRUCT` whose
// members are at ind plus one level. With values, every member gets its
// start value: its declared initial value, or what start (a manifest
// init: map, already checked by nautilus's seeding) gives it. Temps take
// no start values (values false): the body sets them.
func (w *writer) declType(typ, ind string, start any, values bool) (string, string) {
	sd := w.structOf(typ)
	if sd == nil {
		t, why := w.sclType(typ)
		if why != "" || start == nil {
			return t, why
		}
		lit, err := InitLiteral(start, typ)
		if err != nil {
			return "", err.Error()
		}
		v, why := literal(lit)
		if why != "" {
			return "", why
		}
		return t + " := " + v, ""
	}
	m, isMap := start.(map[string]any)
	if start != nil && !isMap {
		return "", fmt.Sprintf("the start value %v of the struct %s is not a mapping of member: value", start, sd.name)
	}
	used := 0
	keys := map[string]string{}
	sorted := make([]string, 0, len(m))
	for k := range m {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted) // the same message every run
	for _, k := range sorted {
		if other, dup := keys[strings.ToLower(k)]; dup {
			return "", fmt.Sprintf("the start value names %s and %s, one member of %s", other, k, sd.name)
		}
		keys[strings.ToLower(k)] = k
	}
	var b strings.Builder
	b.WriteString("Struct\n")
	for _, f := range sd.fields {
		var fs any
		has := false
		for k, v := range m {
			if strings.EqualFold(k, f.Name) {
				fs, has = v, true
				used++
			}
		}
		t, why := w.declType(f.Datatype, ind+"   ", fs, values)
		if why != "" {
			return "", fmt.Sprintf("member %s: %s", f.Name, why)
		}
		line := ind + "   " + sclName(f.Name) + " : " + t
		if values && !has && f.Initial != nil {
			v, why := constValue(w.lib, f.Initial)
			if why != "" {
				return "", fmt.Sprintf("member %s: its initial value: %s", f.Name, why)
			}
			line += " := " + v
		}
		b.WriteString(line + ";\n")
	}
	if used != len(m) {
		// nautilus's seeding refused an unknown member already; never
		// drop one silently should it not have run.
		return "", fmt.Sprintf("the start value %v names a member %s does not have", start, sd.name)
	}
	b.WriteString(ind + "END_STRUCT")
	return b.String(), ""
}

// leaf is one elementary member of a struct, by its path below the
// struct (".Pump.Run"), with its declared type and start value in SCL.
type leaf struct {
	path, typ, init string
}

// leaves are the elementary members of a struct type, at any depth, in
// declaration order; why says why one cannot be listed.
func (w *writer) leaves(typ, path string) ([]leaf, string) {
	sd := w.structOf(typ)
	if sd == nil {
		u := strings.ToUpper(strings.TrimSpace(typ))
		init, ok := zero(u)
		if !ok {
			return nil, gapMessage(gapStructured, "a "+typ+" member here", "")
		}
		return []leaf{{path: path, typ: u, init: init}}, ""
	}
	var out []leaf
	for _, f := range sd.fields {
		ls, why := w.leaves(f.Datatype, path+"."+sclName(f.Name))
		if why != "" {
			return nil, why
		}
		if f.Initial != nil && len(ls) == 1 {
			v, why := constValue(w.lib, f.Initial)
			if why != "" {
				return nil, fmt.Sprintf("member %s: its initial value: %s", f.Name, why)
			}
			ls[0].init = v
		}
		out = append(out, ls...)
	}
	return out, ""
}

// seedCheck runs nautilus's own seeding of a structured start value, so
// what the runtime refuses (a member the type does not have, a value of
// the wrong kind) is refused here in its words.
func (w *writer) seedCheck(typ string, start any) string {
	if w.lib.types == nil {
		return "the project's TYPEs do not resolve, so nautilus's seeding cannot check it"
	}
	t, err := st.ResolveTypeName(typ, w.lib.types)
	if err != nil {
		return err.Error()
	}
	if _, err := ir.SeedFromInit(t, start); err != nil {
		return err.Error()
	}
	return ""
}

// shape is what a readable name holds below it, for Output.Symbol, with
// the name as declared: members for a struct; any member one level down
// for a function block instance; neither for a scalar.
type shape struct {
	name     string
	instance bool
	members  map[string]*shape // lower-cased
}

// shapeOf is the shape of a name declared with type typ.
func (w *writer) shapeOf(name, typ string) *shape {
	s := &shape{name: name}
	sd := w.structOf(typ)
	if sd == nil {
		return s
	}
	s.members = map[string]*shape{}
	for _, f := range sd.fields {
		s.members[strings.ToLower(f.Name)] = w.shapeOf(f.Name, f.Datatype)
	}
	return s
}

// lower runs nautilus's own lowering of the program, with the library
// ahead of it as naut check composes them, and reports what it refuses.
func (pw *progWriter) lower(src string) bool {
	w := pw.w
	shift := strings.Count(w.lib.src, "\n")
	prog, err := st.Parse(w.lib.src + src)
	if err != nil {
		// The program parsed alone: the library is what does not.
		pw.diag("siemens/nautilus", 0, "nautilus refuses the project's library: "+err.Error())
		return false
	}
	types, _ := st.Types(prog)
	globals := map[string]*ir.Type{}
	for _, g := range w.globals {
		if t, err := st.ResolveTypeName(g.Type, types); err == nil {
			globals[g.Name] = t
		} else if t := st.DatatypeToIRType(g.Type); t != nil {
			globals[g.Name] = t
		}
	}
	if _, err := st.LowerWithOpts(prog, st.LowerOpts{ImplicitGlobals: globals}); err != nil {
		line := 0
		if le, ok := st.AsLowerError(err); ok {
			line = le.Pos.Line
		}
		if line > 0 && line <= shift {
			pw.diag("siemens/nautilus", 0, "nautilus refuses the project's library: "+err.Error())
			return false
		}
		if line > shift {
			line -= shift
		}
		msg := err.Error()
		if le, ok := st.AsLowerError(err); ok && shift > 0 {
			msg = le.Err.Error()
			if line > 0 {
				msg = fmt.Sprintf("line %d: %s", line, msg)
			}
		}
		pw.diag("siemens/nautilus", line, "nautilus refuses this program: "+msg)
		return false
	}
	return true
}

// sclKeywords are names SCL reserves beyond IEC's (which nautilus refuses
// as names anyway): a member, variable or tag so named is written quoted.
// Measured on TIA V21 (#1299): a DB member named Name unquoted fails the
// import ("The specified value "Name" is invalid"); quoted, it compiles.
// The rest are the block header's words of the same kind, and STATIC,
// which compiled unquoted too: quoting a name TIA does not reserve
// compiles all the same.
var sclKeywords = map[string]bool{
	"VERSION": true, "TITLE": true, "AUTHOR": true, "FAMILY": true, "NAME": true,
	"KNOW_HOW_PROTECT": true, "STATIC": true, "VAR_STAT": true, "BEGIN": true,
	"DATA_BLOCK": true, "ORGANIZATION_BLOCK": true, "NON_RETAIN": true, "GOTO": true,
	"REGION": true, "END_REGION": true, "LABEL": true, "CONST": true, "VARIANT": true,
	"POINTER": true, "ANY": true, "REF": true, "DATA": true, "VOID": true, "NULL": true,
}

// sclName writes a name, quoted when SCL reserves it.
func sclName(n string) string {
	if sclKeywords[strings.ToUpper(n)] {
		return `"` + n + `"`
	}
	return n
}
