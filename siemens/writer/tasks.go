package writer

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/joyautomation/nautilus/lang/st"
)

// More than one task (#1300).
//
// nautilus runs each task's scan against its own copy of the tags it
// shares with other tasks: the copy is taken at the scan's start, and at
// its end only what the scan ASSIGNED lands in the store, member by
// member and bit by bit, the last commit winning (runtime/scanview.go,
// membermerge.go). A scan never sees another task's half-done scan, and a
// tag a scan did not assign keeps whatever another task or an operator
// stored meanwhile.
//
// The writer gives each task its own cyclic interrupt OB and does the
// same in SCL. A tag that two or more tasks' programs name is shared;
// every other tag stays where it is, in the tags DB, and the program
// reads and writes it there, as with one task. For a task that shares
// tags, a scan DB ("naut_Scan_<task>") holds:
//
//   - tags: the task's working copy of each shared tag it names;
//   - assigned: a flag for each elementary member of each shared tag the
//     program can write, set by the statement that writes it. A member
//     the program writes single bits of has a bit string of its width
//     instead, one bit per bit assigned (all set when the whole member
//     is).
//
// The program's FB reads and writes the copy. The task's OB copies the
// shared tags in, calls the FB, lands what the flags mark, and clears
// them. Flags, not a comparison with the copy taken at the start: a
// seal-in (Run := (Start OR Run) AND NOT Stop) assigns an unchanged value
// on most scans, and nautilus lands that assignment over another writer's.
//
// The OBs all run at TIA's default priority, so none preempts another
// and the copy and the commit cannot be split by another task's scan. A
// faster task can wait behind a slower one, which nautilus's tasks never
// do: different priorities, with the copy under interrupts held off, are
// the follow-up.

// tagUse is what a program does with one manifest tag.
type tagUse struct {
	name    string // as the manifest declares it
	written bool
	// bits are the members (by leaf path, "" for the tag itself) the
	// program writes single bits of, and which bits.
	bits map[string]map[int]bool
}

// share is what a task's program shares with the other tasks.
type share struct {
	task string
	db   string // the scan DB, with the prefix, unquoted
	// tags are the shared tags the program names, lower-cased.
	tags map[string]*tagUse
}

// use records that the program names manifest tag g.
func (pw *progWriter) use(g Global) *tagUse {
	k := strings.ToLower(g.Name)
	u := pw.uses[k]
	if u == nil {
		u = &tagUse{name: g.Name}
		pw.uses[k] = u
	}
	return u
}

// assignTarget resolves the target of a write to a manifest tag: the tag,
// the path of the member written below it (as leaves() spells paths), the
// member's declared type, and the bit written (-1: the whole member). ok
// is false for anything else (a local, an FB pin).
func (pw *progWriter) assignTarget(e st.Expression) (g Global, path, typ string, bit int, ok bool) {
	switch e := e.(type) {
	case *st.IdentExpr:
		if _, local := pw.locals[strings.ToLower(e.Name)]; local {
			return Global{}, "", "", 0, false
		}
		g, ok := pw.w.globals[strings.ToLower(e.Name)]
		return g, "", g.Type, -1, ok
	case *st.MemberExpr:
		if n, isBit := bitIndex(e.Member); isBit {
			g, path, typ, b, ok := pw.assignTarget(e.Object)
			if !ok || b >= 0 {
				return Global{}, "", "", 0, false
			}
			return g, path, typ, n, true
		}
		g, path, typ, b, ok := pw.assignTarget(e.Object)
		if !ok || b >= 0 {
			return Global{}, "", "", 0, false
		}
		sd := pw.w.structOf(typ)
		if sd == nil {
			return Global{}, "", "", 0, false
		}
		f, found := sd.fieldOf(e.Member)
		if !found {
			return Global{}, "", "", 0, false
		}
		return g, path + "." + sclName(f.Name), f.Datatype, -1, true
	}
	return Global{}, "", "", 0, false
}

// assigned follows every statement that writes target (an assignment, an
// instance's output binding; nautilus refuses a tag as a FOR variable):
// it records the write and, for a shared tag, sets the flags of what it
// wrote.
func (pw *progWriter) assigned(b *strings.Builder, in string, at int, target st.Expression) {
	g, path, typ, bit, ok := pw.assignTarget(target)
	if !ok {
		return
	}
	u := pw.use(g)
	u.written = true
	if bit >= 0 {
		if u.bits == nil {
			u.bits = map[string]map[int]bool{}
		}
		if u.bits[path] == nil {
			u.bits[path] = map[int]bool{}
		}
		u.bits[path][bit] = true
	}
	sh := pw.share
	if sh == nil {
		return
	}
	su, isShared := sh.tags[strings.ToLower(g.Name)]
	if !isShared {
		return
	}
	flag := quote(sh.db) + ".assigned." + sclName(g.Name)
	if bit >= 0 {
		pw.emit(b, at, "%s%s%s.%%X%d := TRUE;\n", in, flag, path, bit)
		return
	}
	ls, why := pw.w.paths(typ, path)
	if why != "" {
		pw.diag("siemens/task", at, why)
		return
	}
	for _, l := range ls {
		pw.emit(b, at, "%s%s%s := %s;\n", in, flag, l.path, flagAll(su, l))
	}
}

// flagAll is the value that marks leaf l of a shared tag wholly assigned:
// TRUE, or every bit of its mask.
func flagAll(u *tagUse, l leaf) string {
	if u.bits[l.path] != nil {
		return bitsLit(-1, bitsFor(l.typ))
	}
	return "TRUE"
}

// sharing finds the tags more than one task names. Each program is
// written once into a scratch writer to learn which manifest tags it
// names, through the same name resolution the real pass uses (a bare
// name with no VAR_EXTERNAL names a tag too). A program that is refused
// shares nothing: Write refuses the project anyway.
func (w *writer) sharing(in Input, progs []parsed) map[string]*share {
	uses := map[string]map[string]*tagUse{} // program, lower-cased
	for _, p := range progs {
		scratch := &writer{globals: w.globals, lib: w.lib, tagsDB: w.tagsDB, prefix: w.prefix}
		if pw := scratch.program(p.path, p.libs, p.src, p.prog); pw != nil {
			uses[strings.ToLower(p.prog.Name)] = pw.uses
		}
	}
	count := map[string]int{}
	seen := map[string]bool{}
	for _, t := range in.Tasks {
		k := strings.ToLower(t.Program)
		if seen[k] {
			continue // a program run twice is refused below
		}
		seen[k] = true
		for tag := range uses[k] {
			count[tag]++
		}
	}
	out := map[string]*share{}
	for _, t := range in.Tasks {
		k := strings.ToLower(t.Program)
		if out[k] != nil {
			continue
		}
		var sh *share
		for tag, u := range uses[k] {
			if count[tag] < 2 {
				continue
			}
			if sh == nil {
				sh = &share{task: t.Name, db: w.prefix + "Scan_" + t.Name, tags: map[string]*tagUse{}}
			}
			sh.tags[tag] = u
		}
		if sh != nil {
			out[k] = sh
		}
	}
	return out
}

// parsed is a program ready to be written.
type parsed struct {
	path, src string
	libs      []string
	lines     []int
	prog      *st.Program
}

// sharedNames are a share's tags in a fixed order.
func (sh *share) sharedNames() []string {
	names := make([]string, 0, len(sh.tags))
	for k := range sh.tags {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// scanDB writes a task's scan DB: the copy of each shared tag, and the
// flags of the ones the program writes.
func (w *writer) scanDB(sh *share) ([]byte, string) {
	var tags, flags strings.Builder
	for _, k := range sh.sharedNames() {
		u := sh.tags[k]
		g := w.globals[k]
		if w.typeRefusal(g.Type, map[string]bool{}) != "" {
			continue // the tags DB refuses the tag's type, once
		}
		t, why := w.declType(g.Type, "         ", nil, false)
		if why != "" {
			return nil, fmt.Sprintf("tag %s: %s", g.Name, why)
		}
		fmt.Fprintf(&tags, "         %s : %s;\n", sclName(g.Name), t)
		if !u.written {
			continue
		}
		ft, why := w.flagType(g.Type, "", "         ", u)
		if why != "" {
			return nil, fmt.Sprintf("tag %s: %s", g.Name, why)
		}
		fmt.Fprintf(&flags, "         %s : %s;\n", sclName(g.Name), ft)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "DATA_BLOCK %s\n", quote(sh.db))
	b.WriteString("{ S7_Optimized_Access := 'TRUE' }\n")
	b.WriteString("VERSION : 0.1\n")
	b.WriteString("   VAR \n")
	b.WriteString("      tags : Struct\n" + tags.String() + "      END_STRUCT;\n")
	if flags.Len() > 0 {
		b.WriteString("      assigned : Struct\n" + flags.String() + "      END_STRUCT;\n")
	}
	b.WriteString("   END_VAR\n")
	b.WriteString("\nBEGIN\n\nEND_DATA_BLOCK\n")
	return []byte(b.String()), ""
}

// flagType is the type of a written tag's flags: Bool for an elementary
// member, a bit string of its width for one written bit by bit, and a
// Struct of those for a struct.
func (w *writer) flagType(typ, path, ind string, u *tagUse) (string, string) {
	sd := w.structOf(typ)
	if sd == nil {
		if u.bits[path] != nil {
			bits := bitsFor(strings.ToUpper(strings.TrimSpace(typ)))
			if bits == "" {
				return "", fmt.Sprintf("bits written of a %s", typ)
			}
			return bits, ""
		}
		return "BOOL", ""
	}
	var b strings.Builder
	b.WriteString("Struct\n")
	for _, f := range sd.fields {
		t, why := w.flagType(f.Datatype, path+"."+sclName(f.Name), ind+"   ", u)
		if why != "" {
			return "", why
		}
		fmt.Fprintf(&b, "%s   %s : %s;\n", ind, sclName(f.Name), t)
	}
	b.WriteString(ind + "END_STRUCT")
	return b.String(), ""
}

// obSource writes a task's OB: it calls the program's FB, and when the
// task shares tags, copies them in first and lands what the scan
// assigned after.
func (w *writer) obSource(ob OB, instDB string, sh *share) ([]byte, string) {
	var b strings.Builder
	fmt.Fprintf(&b, "ORGANIZATION_BLOCK %s\n", quote(ob.Name))
	fmt.Fprintf(&b, "TITLE = \"Cyclic interrupt, %s\"\n", ob.Cycle)
	b.WriteString("{ S7_Optimized_Access := 'TRUE' }\n")
	b.WriteString("VERSION : 0.1\n")
	b.WriteString("\nBEGIN\n")
	if sh == nil {
		fmt.Fprintf(&b, "\t%s();\n", quote(instDB))
		b.WriteString("END_ORGANIZATION_BLOCK\n")
		return []byte(b.String()), ""
	}
	tagsDB, scan := quote(w.tagsDB), quote(sh.db)
	type leafOf struct {
		u    *tagUse
		name string
		l    leaf
	}
	var all []leafOf
	for _, k := range sh.sharedNames() {
		u := sh.tags[k]
		g := w.globals[k]
		if w.typeRefusal(g.Type, map[string]bool{}) != "" {
			continue // the tags DB refuses the tag's type, once
		}
		ls, why := w.paths(g.Type, "")
		if why != "" {
			return nil, fmt.Sprintf("tag %s: %s", g.Name, why)
		}
		for _, l := range ls {
			all = append(all, leafOf{u, sclName(g.Name), l})
		}
	}
	b.WriteString("\t// The tags this task shares with others, copied in: the program works on the copy.\n")
	for _, x := range all {
		fmt.Fprintf(&b, "\t%s.tags.%s%s := %s.%s%s;\n", scan, x.name, x.l.path, tagsDB, x.name, x.l.path)
	}
	fmt.Fprintf(&b, "\t%s();\n", quote(instDB))
	b.WriteString("\t// What the scan assigned, back into the tags DB, member by member and bit by bit.\n")
	for _, x := range all {
		if !x.u.written {
			continue
		}
		flag := scan + ".assigned." + x.name + x.l.path
		from := scan + ".tags." + x.name + x.l.path
		to := tagsDB + "." + x.name + x.l.path
		bits := x.u.bits[x.l.path]
		if bits == nil {
			fmt.Fprintf(&b, "\tIF %s THEN\n\t\t%s := %s;\n\t\t%s := FALSE;\n\tEND_IF;\n", flag, to, from, flag)
			continue
		}
		ns := make([]int, 0, len(bits))
		for n := range bits {
			ns = append(ns, n)
		}
		sort.Ints(ns)
		mask := bitsFor(x.l.typ)
		fmt.Fprintf(&b, "\tIF %s = %s THEN\n\t\t%s := %s;\n\tELSE\n", flag, bitsLit(-1, mask), to, from)
		for _, n := range ns {
			bit := ".%X" + strconv.Itoa(n)
			fmt.Fprintf(&b, "\t\tIF %s%s THEN\n\t\t\t%s%s := %s%s;\n\t\tEND_IF;\n", flag, bit, to, bit, from, bit)
		}
		fmt.Fprintf(&b, "\tEND_IF;\n\t%s := %s;\n", flag, bitsLit(0, mask))
	}
	b.WriteString("END_ORGANIZATION_BLOCK\n")
	return []byte(b.String()), ""
}

// paths are the elementary members of a declared type at any depth (the
// type itself when it is not a struct), spelt as leaves() spells them:
// what a copy and its flags walk. Unlike leaves() it needs no zero value
// (a shared TOD or LWORD is copied all the same); a type the tags DB
// refuses never gets here.
func (w *writer) paths(typ, path string) ([]leaf, string) {
	sd := w.structOf(typ)
	if sd == nil {
		return []leaf{{path: path, typ: strings.ToUpper(strings.TrimSpace(typ))}}, ""
	}
	var out []leaf
	for _, f := range sd.fields {
		ls, why := w.paths(f.Datatype, path+"."+sclName(f.Name))
		if why != "" {
			return nil, why
		}
		out = append(out, ls...)
	}
	return out, ""
}
