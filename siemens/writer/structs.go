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

// Structured types (#1299, #1301 D1b). Every STRUCT the project declares
// (in a library file or in a program's own TYPE block) that the written
// SCL reaches is a PLC data type "<prefix><Name>" (a .udt source, tiad
// generates it into the data types folder of nautilus's group), and every
// variable, tag, member and pin of it names that type: TIA shows the
// type as nautilus reads it, a block's pin can take one, and a function
// can return one. A type nothing reaches is not written (as the project's
// blocks, pous.go): a library TYPE no program uses is not the CPU's
// business.
//
// Start values follow nautilus exactly: every member starts at its type's
// declared initial value (`Temp : REAL := 72.5;` inside the STRUCT), which
// the data type carries, and a manifest tag's init: map overrides the
// members it names, at any depth, in the tags DB's BEGIN section
// (`WL101.Pump.Run := TRUE;`). nautilus's own seeding (ir.SeedFromInit)
// checks the map first, so a member it does not know is refused with
// nautilus's words.

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
	// constDecls are the global constants' declarations (#1301), by
	// lower-cased name; pous the project's FUNCTION_BLOCKs and FUNCTIONs,
	// each name with every declaration of it (pous.go).
	constDecls map[string]st.VarDecl
	pous       map[string][]*pou
	// enumMembers are the enumerations' member names, each with the
	// names of the types that declare it: a bare member (Idle) is an
	// enumeration value (enums.go).
	enumMembers map[string][]string
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
	for _, l := range libraryParts(libs) {
		out = append(out, l.src)
	}
	return out
}

// libSource is one library source as ST; st is set when it was written
// in ST (its lines are the file's own).
type libSource struct {
	src string
	st  bool
}

func libraryParts(libs []string) []libSource {
	var out []libSource
	for _, src := range libs {
		if _, err := st.Parse(src); err == nil {
			out = append(out, libSource{src, true})
			continue
		}
		if f, _, err := ld.TranspileWithLines(src, libs...); err == nil {
			if s, _, err := fbd.TranspileWithLines(f); err == nil {
				if _, err := st.Parse(s); err == nil {
					out = append(out, libSource{s, false})
					continue
				}
			}
		}
		if s, _, err := fbd.TranspileWithLines(src); err == nil {
			if _, err := st.Parse(s); err == nil {
				out = append(out, libSource{s, false})
			}
		}
	}
	return out
}

// loadLibrary reads the library sources and the programs' own TYPE
// blocks.
func loadLibrary(libs []string, own []*st.Program) *library {
	lib := &library{structs: map[string]*structDef{}, otherTypes: map[string]string{}, conflicts: map[string]string{},
		fbs: map[string]string{}, funcs: map[string]string{}, consts: map[string]string{}, enumMembers: map[string][]string{},
		constDecls: map[string]st.VarDecl{}, pous: map[string][]*pou{}}
	parts := libraryParts(libs)
	var srcs []string
	var all st.Program
	for _, part := range parts {
		src := part.src
		srcs = append(srcs, src)
		prog, err := st.Parse(src)
		if err != nil {
			continue
		}
		lib.addPOUs(prog, "", nil, true, part.st)
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
					if vb.Constant {
						lib.constDecls[strings.ToLower(v.Name)] = v
					}
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
		// A program file's own file-level constants (#176) too: nautilus
		// lowers each file with its own, and refuses a name another file
		// does not declare before the writer sees it.
		for _, vb := range p.GlobalConsts {
			if vb.Kind == "VAR_GLOBAL" && vb.Constant {
				for _, v := range vb.Variables {
					if _, ok := lib.constDecls[strings.ToLower(v.Name)]; !ok {
						lib.consts[strings.ToLower(v.Name)] = v.Name
						lib.constDecls[strings.ToLower(v.Name)] = v
					}
				}
			}
		}
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
				km := strings.ToLower(m.Name)
				lib.enumMembers[km] = append(lib.enumMembers[km], td.Name)
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
		return gapMessage(gapOther, "the recursive type "+sd.name, "")
	}
	seen[k] = true
	defer delete(seen, k)
	for _, f := range sd.fields {
		if why := boundsRefusal(f.Type); why != "" {
			return fmt.Sprintf("member %s: %s", f.Name, why)
		}
		if why := w.typeRefusal(f.Datatype, seen); why != "" {
			return fmt.Sprintf("member %s: %s", f.Name, why)
		}
		if f.Initial != nil && w.structOf(f.Datatype) != nil {
			return fmt.Sprintf("member %s: %s", f.Name, gapMessage(gapOther, "a struct member's initializer", ""))
		}
		if f.Initial != nil {
			if _, why := w.memberInit(f); why != "" {
				return fmt.Sprintf("member %s: its initial value: %s", f.Name, why)
			}
		}
	}
	return ""
}

// sclType maps a declared type that is not a struct to its SCL spelling,
// or says why not.
func (w *writer) sclType(t string) (string, string) {
	if isArrayType(t) {
		return w.arrayDecl(t, nil)
	}
	typ, why := sclType(t)
	if why == "" {
		return typ, ""
	}
	k := strings.ToLower(strings.TrimSpace(t))
	if name, ok := w.lib.conflicts[k]; ok {
		return "", gapMessage(gapOther, "the TYPE "+name+", which two programs declare differently", "nautilus gives each program its own; the tags DB takes one")
	}
	if name, ok := w.lib.fbs[k]; ok {
		return "", gapMessage(gapOther, "the project's function block "+name+" here", "")
	}
	if en := w.enumOf(t); en != nil {
		base, why := w.enumBase(en)
		if why != "" {
			return "", why
		}
		return sclType(base)
	}
	if name, ok := w.lib.otherTypes[k]; ok {
		return "", gapMessage(gapOther, "the type alias "+name, "")
	}
	if fb := ir.LookupFBType(t); fb != nil && fb.Kind == ir.TypeFB && !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(t)), "ARRAY") {
		// A block of nautilus's library where the writer has no form for
		// it (nautilus's PID is written as a block of its own, pid.go,
		// where a variable declares it).
		return "", gapMessage(gapOther, "nautilus's function block "+strings.ToUpper(strings.TrimSpace(t))+" here", "")
	}
	return "", why
}

// declType prints a declared type for a declaration: an elementary type
// as SCL spells it, a struct as its PLC data type (typeName). path is the
// nautilus file that declares it, for a compile message on the type.
func (w *writer) declType(typ, path string) (string, string) {
	if sd := w.structOf(typ); sd != nil {
		return w.typeName(sd, path), ""
	}
	return w.sclType(typ)
}

// typeName is the PLC data type of the STRUCT sd, "<prefix><Name>", and
// records it as reached: Write writes its .udt source (writeTypes), and
// those of the structs it holds. path is the first nautilus file that
// names it.
func (w *writer) typeName(sd *structDef, path string) string {
	k := strings.ToLower(sd.name)
	if w.types == nil {
		w.types = map[string]string{}
	}
	if _, ok := w.types[k]; !ok {
		w.types[k] = path
	}
	return quote(w.prefix + sd.name)
}

// overrides are the BEGIN-section assignments that give a tag of type
// typ, at path, the start value start (a manifest init: map, already
// checked by nautilus's seeding), member by member in declaration order:
// `WL101.Pump.Run := TRUE;`. What the map does not name keeps the data
// type's own start value. A map that names a member twice or one the
// type lacks, or a struct given something else than a map, is refused,
// never dropped.
func (w *writer) overrides(typ, path string, start any) ([]string, string) {
	if isArrayType(typ) {
		return w.arrayOverrides(typ, path, start)
	}
	sd := w.structOf(typ)
	if sd == nil && w.enumOf(typ) != nil {
		v, why := w.enumInit(typ, fmt.Sprint(start))
		if why != "" {
			return nil, why
		}
		return []string{path + " := " + v + ";"}, ""
	}
	if sd == nil {
		lit, err := InitLiteral(start, typ)
		if err != nil {
			return nil, err.Error()
		}
		v, why := literal(lit, typ)
		if why != "" {
			return nil, why
		}
		return []string{path + " := " + v + ";"}, ""
	}
	m, isMap := start.(map[string]any)
	if !isMap {
		return nil, fmt.Sprintf("the start value %v of the struct %s is not a mapping of member: value", start, sd.name)
	}
	keys := map[string]string{}
	sorted := make([]string, 0, len(m))
	for k := range m {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted) // the same message every run
	for _, k := range sorted {
		if other, dup := keys[strings.ToLower(k)]; dup {
			return nil, fmt.Sprintf("the start value names %s and %s, one member of %s", other, k, sd.name)
		}
		keys[strings.ToLower(k)] = k
	}
	used := 0
	var out []string
	for _, f := range sd.fields {
		k, has := keys[strings.ToLower(f.Name)]
		if !has {
			continue
		}
		used++
		ls, why := w.overrides(f.Datatype, path+"."+sclName(f.Name), m[k])
		if why != "" {
			return nil, fmt.Sprintf("member %s: %s", f.Name, why)
		}
		out = append(out, ls...)
	}
	if used != len(m) {
		// nautilus's seeding refused an unknown member already; never
		// drop one silently should it not have run.
		return nil, fmt.Sprintf("the start value %v names a member %s does not have", start, sd.name)
	}
	return out, ""
}

// writeTypes adds a .udt source for every STRUCT the written SCL reached,
// and every struct those hold, each after the types it holds (TIA
// generates a type only once the types it names exist). It answers the
// files in that order. claim reserves the type's name: blocks and data
// types share one name space in TIA (measured on V21, "Type conflict:
// Object X cannot be overwritten").
func (w *writer) writeTypes(out *Output, claim func(name, what string) bool) []string {
	keys := make([]string, 0, len(w.types))
	for k := range w.types {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	state := map[string]int{}
	var order []string
	var visit func(k, path string)
	visit = func(k, path string) {
		if state[k] != 0 {
			return // a recursive type is refused where it is declared (typeRefusal)
		}
		state[k] = 1
		sd := w.lib.structs[k]
		for _, f := range sd.fields {
			if m := w.structOf(f.Datatype); m != nil {
				mk := strings.ToLower(m.name)
				if _, ok := w.types[mk]; !ok {
					w.types[mk] = path
				}
				visit(mk, w.types[mk])
			}
		}
		state[k] = 2
		order = append(order, k)
	}
	for _, k := range keys {
		visit(k, w.types[k])
	}
	var files []string
	for _, k := range order {
		sd := w.lib.structs[k]
		if w.prefix == "" {
			// tiad keeps nautilus's types apart from a customer's by the
			// group and prunes only what carries the prefix; the CLI
			// never passes an empty one.
			w.diag("siemens/type", w.types[k], 0, fmt.Sprintf("the TYPE %s needs a block prefix to be written as a PLC data type", sd.name))
			continue
		}
		name := w.prefix + sd.name
		if !claim(name, "the PLC data type of TYPE "+sd.name) {
			continue
		}
		data, why := w.udtSource(sd)
		if why != "" {
			w.diag("siemens/type", w.types[k], 0, fmt.Sprintf("the type %s: %s", sd.name, why))
			continue
		}
		out.Files = append(out.Files, File{Path: name + ".udt", Data: data})
		out.Sources[name+".udt"] = Source{Path: w.types[k]}
		files = append(files, name+".udt")
	}
	return files
}

// udtSource is the PLC data type of sd: its members with their declared
// initial values, a struct member naming its own type.
func (w *writer) udtSource(sd *structDef) ([]byte, string) {
	var b strings.Builder
	fmt.Fprintf(&b, "TYPE %s\n", quote(w.prefix+sd.name))
	b.WriteString("VERSION : 0.1\n")
	b.WriteString("   STRUCT\n")
	for _, f := range sd.fields {
		t, why := w.declType(f.Datatype, "")
		if why != "" {
			return nil, fmt.Sprintf("member %s: %s", f.Name, why)
		}
		line := "      " + sclName(f.Name) + " : " + t
		if f.Initial != nil {
			v, why := w.memberInit(f)
			if why != "" {
				return nil, fmt.Sprintf("member %s: its initial value: %s", f.Name, why)
			}
			line += " := " + v
		} else if n, ok := w.enumStart(f.Datatype); ok {
			line += " := " + n
		} else if n := w.arrayStart(f.Datatype, nil); n != "" {
			line += " := " + n
		}
		b.WriteString(line + ";\n")
	}
	b.WriteString("   END_STRUCT;\n\nEND_TYPE\n")
	return []byte(b.String()), ""
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
		init, ok := w.zeroOf(u)
		if !ok {
			return nil, gapMessage(gapOther, "a "+typ+" member here", "")
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
			v, why := w.memberInit(f)
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
// for a function block instance; for an array, its dimensions and what
// each element holds; none of these for a scalar.
type shape struct {
	name     string
	instance bool
	members  map[string]*shape // lower-cased
	dims     []arrayDim
	elem     *shape
}

// shapeOf is the shape of a name declared with type typ.
func (w *writer) shapeOf(name, typ string) *shape {
	s := &shape{name: name}
	if isArrayType(typ) {
		if a, _ := w.arrayOf(typ, nil); a != nil {
			s.dims = a.dims
			s.elem = w.shapeOf("", a.elem)
		}
		return s
	}
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
	// The block kinds (#1301: an FB instance named db failed the import of
	// its block, "The specified value "db" is invalid", on TIA V21).
	"DB": true, "DI": true, "FB": true, "FC": true, "OB": true, "UDT": true, "SFB": true, "SFC": true,
	// Data type names nautilus takes as names (#1310: a tag named Dt
	// failed the import of the tags DB, "The specified value "Dt" is
	// invalid", as did a temp named dt).
	"DT": true, "TOD": true, "DATE": true, "TIME": true, "LDT": true, "LTOD": true,
	"LDATE": true, "LTIME": true, "DATE_AND_TIME": true, "TIME_OF_DAY": true,
	"BYTE": true, "WORD": true, "DWORD": true, "LWORD": true, "SINT": true, "LINT": true,
	"USINT": true, "UINT": true, "UDINT": true, "ULINT": true, "WSTRING": true,
	"CHAR": true, "WCHAR": true, "S5TIME": true, "DTL": true,
}

// sclName writes a name, quoted when SCL reserves it.
func sclName(n string) string {
	// A name with two underscores in a row is no SCL identifier (TIA V21:
	// "Syntax error at letter '_'" on a tag EPIC__Online, #1301); quoted,
	// any name is.
	if sclKeywords[strings.ToUpper(n)] || strings.Contains(n, "__") {
		return `"` + n + `"`
	}
	return n
}
