package project

import (
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/joyautomation/nautilus/internal/stproject"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/lang/st"
	"github.com/joyautomation/nautilus/runtime"
	"github.com/joyautomation/nautilus/wirebind"
	"github.com/joyautomation/nautilus/wiring"
)

// The check of a project's expr: members (#1458 slice 8a, one-struct-model
// §6 and §15): each must be one expression that compiles in the project's
// scope (a bare name is a sibling member first, then a project tag), give
// its member's type without narrowing, call only what is pure, and read no
// expr: that reads it back. The runtime evaluates them
// at each main scan (runtime/exprstage.go), from the same text.
//
// One compile for the whole project: every unit is one line of one
// generated program after the library prelude (st.ExprProgram), so a
// manifest read lowers the prelude once, not once per member.

// exprUnit is one instance's expr: member.
type exprUnit struct {
	in   *wirebind.Instance
	mem  wirebind.Member
	full string // "P101.Healthy"
	text string // rewritten onto the instance
	expr st.Expression
	refs []st.ExprRef
}

// exprFinding is one problem, grouped across the instances of a wiring
// that share it (one line in the wiring, one message).
type exprFinding struct {
	pos       wiring.Pos
	member    string
	msg       string
	instances []string
}

type exprFindings struct {
	order []string
	by    map[string]*exprFinding
}

func (f *exprFindings) add(pos wiring.Pos, member, instance, msg string) {
	if f.by == nil {
		f.by = map[string]*exprFinding{}
	}
	key := fmt.Sprintf("%s:%d|%s|%s", pos.File, pos.Line, member, msg)
	x := f.by[key]
	if x == nil {
		x = &exprFinding{pos: pos, member: member, msg: msg}
		f.by[key] = x
		f.order = append(f.order, key)
	}
	x.instances = append(x.instances, instance)
}

func (f *exprFindings) problems() []wiring.Problem {
	var out []wiring.Problem
	for _, k := range f.order {
		x := f.by[k]
		who := "instance " + x.instances[0]
		if len(x.instances) > 1 {
			who = "instances " + strings.Join(x.instances, ", ")
		}
		out = append(out, wiring.Problem{Pos: x.pos, Msg: fmt.Sprintf("%s: member %q: expr: %s", who, x.member, x.msg)})
	}
	return out
}

// checkExprs checks every expr: member of set in m's scope. m.Tags already
// holds the instance tags (composeWiring appends them first). shadows are
// warnings: a bare name that reads a sibling where a project tag of that
// name exists (the sibling wins, §6).
func checkExprs(fsys fs.FS, m *Manifest, set *wirebind.Set, types map[string]*ir.Type) (shadows []string, _ []wiring.Problem) {
	var units []*exprUnit
	var f exprFindings
	tagNames := map[string]string{}
	for _, t := range m.Tags {
		tagNames[ir.NameKey(t.Name)] = t.Name
	}
	for _, in := range set.Instances {
		siblings := map[string]bool{}
		if t, _, ok := ir.Lookup(types, in.Type); ok && t.Kind == ir.TypeStruct && t.Struct != nil {
			for _, fl := range t.Struct.Fields {
				siblings[ir.NameKey(fl.Name)] = true
			}
		}
		for _, mem := range in.Members {
			if mem.Kind != wiring.Expr {
				continue
			}
			text, refs, err := st.RewriteExpr(in.Name, func(n string) bool { return siblings[ir.NameKey(n)] }, mem.Expr)
			if err != nil {
				f.add(mem.Pos, mem.Path, in.Name, fmt.Sprintf("%q is not one expression: %v", mem.Expr, err))
				continue
			}
			e, err := st.ParseExpr(text)
			if err != nil {
				f.add(mem.Pos, mem.Path, in.Name, fmt.Sprintf("%q is not one expression: %v", mem.Expr, err))
				continue
			}
			units = append(units, &exprUnit{in: in, mem: mem, full: in.Name + "." + mem.Path, text: text, expr: e, refs: refs})
			seen := map[string]bool{}
			for _, r := range refs {
				k := ir.NameKey(r.Root)
				if tag, ok := tagNames[k]; ok && r.Sibling && !seen[k] {
					seen[k] = true
					shadows = append(shadows, fmt.Sprintf("%s:%d: instance %s: member %q: expr: %s reads the member %s.%s, not the project tag %s: a sibling member wins over a tag of the same name; rename one to read the tag",
						mem.Pos.File, mem.Pos.Line, in.Name, mem.Path, r.Root, in.Name, r.Root, tag))
				}
			}
		}
	}
	if len(units) == 0 {
		return shadows, f.problems()
	}
	libs, err := projectLibs(fsys, m)
	if err != nil {
		return shadows, append(f.problems(), wiring.Problem{Msg: "expr: members cannot be checked: the project's libraries do not read: " + err.Error()})
	}
	srcs := make([]string, len(libs))
	for i, l := range libs {
		srcs[i] = l.ST
	}
	prelude := stproject.Join(srcs, "")
	preludeLines := strings.Count(prelude, "\n")
	where := libLocator(libs)

	exprCompile(units, prelude, preludeLines, m, &f)
	exprMainClash(fsys, m, units, &f)
	exprPurity(units, prelude, where, &f)
	exprCycles(units, &f)
	return shadows, f.problems()
}

// exprCompile lowers the units as one program; a unit that does not
// compile is reported on its member's line and dropped, and the rest are
// lowered again, so every bad unit is named in one check.
func exprCompile(units []*exprUnit, prelude string, preludeLines int, m *Manifest, f *exprFindings) {
	defs, err := TagDefs(m)
	if err != nil {
		return // the tags report their own problem
	}
	if _, err := st.Parse(prelude + "PROGRAM nautilus_prelude\nEND_PROGRAM\n"); err != nil {
		return // the library's own error, which its file's check reports
	}
	live := append([]*exprUnit(nil), units...)
	for len(live) > 0 {
		us := make([]st.ExprUnit, len(live))
		for i, u := range live {
			us[i] = st.ExprUnit{Text: u.text, Target: u.full}
		}
		prog, err := st.Parse(prelude + st.ExprProgram(us))
		if err != nil {
			// Each unit parsed alone and the prelude parses: never
			// expected, and never passed silently.
			for _, u := range live {
				f.add(u.mem.Pos, u.mem.Path, u.in.Name, fmt.Sprintf("%q could not be compiled with the project's expressions: %v", u.mem.Expr, err))
			}
			return
		}
		types, _ := st.Types(prog)
		scope, _ := runtime.ResolveTagScope(defs, types)
		opts := scope.LowerOpts()
		opts.Written = runtime.WrittenResolver(defs, types) // WRITTEN(<path>), as the stage resolves it
		_, err = st.LowerWithOpts(prog, opts)
		if err == nil {
			return
		}
		le, ok := st.AsLowerError(err)
		i := -1
		if ok {
			i = st.ExprUnitOfLine(us, le.Pos.Line-preludeLines)
		}
		if i < 0 {
			if ok && le.Pos.Line > 0 && le.Pos.Line <= preludeLines {
				return // a library error, reported where it is
			}
			for _, u := range live {
				f.add(u.mem.Pos, u.mem.Path, u.in.Name, fmt.Sprintf("%q could not be compiled with the project's expressions: %v", u.mem.Expr, err))
			}
			return
		}
		u := live[i]
		f.add(u.mem.Pos, u.mem.Path, u.in.Name, fmt.Sprintf("%q does not compile: %s", u.mem.Expr, exprErrText(le.Err.Error())))
		live = append(live[:i], live[i+1:]...)
	}
}

// exprMainClash: the stage compiles with the main program's own source,
// whose FUNCTIONs and blocks the check's prelude lacks, so one named WRITTEN
// there is refused here as the stage refuses it, for each expression that
// calls WRITTEN.
func exprMainClash(fsys fs.FS, m *Manifest, units []*exprUnit, f *exprFindings) {
	var calls []*exprUnit
	for _, u := range units {
		if st.CallsWritten(u.expr) {
			calls = append(calls, u)
		}
	}
	if len(calls) == 0 || len(m.Tasks) == 0 {
		return
	}
	src, err := fs.ReadFile(fsys, path.Clean(m.Tasks[0].Program))
	if err != nil {
		return
	}
	// By content, as the runtime reads it: a ladder or FBD main file
	// declares its blocks in its own language.
	text, err := stproject.LibraryST("main."+runtime.Language(string(src)), string(src))
	if err != nil {
		return // the program's own error
	}
	prog, err := st.Parse(text)
	if err != nil {
		return
	}
	what := ""
	for _, fd := range prog.FuncDecls {
		if strings.EqualFold(fd.Name, st.WrittenName) {
			what = "FUNCTION " + fd.Name
		}
	}
	for _, fb := range prog.FBDecls {
		if strings.EqualFold(fb.Name, st.WrittenName) {
			what = "FUNCTION_BLOCK " + fb.Name
		}
	}
	if what == "" {
		return
	}
	for _, u := range calls {
		f.add(u.mem.Pos, u.mem.Path, u.in.Name, fmt.Sprintf("%q does not compile: the project's %s (%s) hides the built-in WRITTEN(<path>) this expression calls: rename it", u.mem.Expr, what, m.Tasks[0].Program))
	}
}

// exprErrText says a compile error in the member's words, not the
// generated program's: "cannot assign REAL to INT" is the expression
// giving REAL to an INT member.
func exprErrText(s string) string {
	if m := assignRE.FindStringSubmatch(s); m != nil {
		out := "it gives " + m[1] + ", but the member is " + m[2] + "; an expr: must give its member's type without narrowing"
		if m[3] != "" {
			out += " (" + strings.TrimSpace(strings.TrimPrefix(m[3], ";")) + ")"
		}
		return out
	}
	return s
}

var assignRE = regexp.MustCompile(`^cannot assign (\S+) to (\S+?)(?: implicitly)?(;.*)?$`)

// exprPurity applies st.ExprPurity to each unit, naming the function, its
// file and line, the rule, and the fix (§6).
func exprPurity(units []*exprUnit, prelude string, where func(line int) string, f *exprFindings) {
	prog, err := st.Parse(prelude + "PROGRAM nautilus_purity\nEND_PROGRAM\n")
	if err != nil {
		return
	}
	for _, u := range units {
		for _, p := range st.ExprPurity(prog, u.expr) {
			const fix = "an expression only computes from what it reads"
			if p.Func == "" {
				f.add(u.mem.Pos, u.mem.Path, u.in.Name, fmt.Sprintf("%q %s; %s: compute this in a program or a block instead", u.mem.Expr, p.Rule, fix))
				continue
			}
			chain := ""
			if len(p.Via) > 1 {
				chain = " (through " + strings.Join(p.Via, " → ") + ")"
			}
			f.add(u.mem.Pos, u.mem.Path, u.in.Name, fmt.Sprintf("calls %s%s, which at %s %s; %s, so a function it calls must too: make %s a program or a block instead",
				p.Via[0], chain, where(p.Pos.Line), p.Rule, fix, p.Func))
		}
	}
}

// libLocator maps a prelude line to "lib/file.st:12".
func libLocator(libs []stproject.Library) func(int) string {
	starts := make([]int, len(libs))
	line := 1
	for i, l := range libs {
		starts[i] = line
		n := strings.Count(l.ST, "\n")
		if !strings.HasSuffix(l.ST, "\n") {
			n++
		}
		line += n
	}
	return func(at int) string {
		for i := len(libs) - 1; i >= 0; i-- {
			if at >= starts[i] {
				return fmt.Sprintf("%s:%d", libs[i].Path, at-starts[i]+1)
			}
		}
		return fmt.Sprintf("line %d", at)
	}
}

// exprCycles refuses expressions that read each other in a ring, naming
// every member in it (§6). A read of a struct or an array covers every
// expr: inside it; a variable index reads the whole array.
func exprCycles(units []*exprUnit, f *exprFindings) {
	targets := make([]string, len(units))
	reads := make([][]string, len(units))
	for i, u := range units {
		targets[i] = u.full
		for _, r := range u.refs {
			if !r.Written { // WRITTEN's path reads the push state, not the value
				reads[i] = append(reads[i], r.Full(u.in.Name))
			}
		}
	}
	edges := st.ExprEdges(targets, reads)
	// One problem per ring (strongly connected component), naming every
	// member in it and one shortest cycle through its first member: linear
	// in the reads, however densely the members read each other.
	for _, comp := range st.ExprSCCs(edges) {
		first := comp[0]
		self := false
		for _, w := range edges[first] {
			self = self || w == first
		}
		if len(comp) == 1 && !self {
			continue
		}
		u := units[first]
		if len(comp) == 1 {
			f.add(u.mem.Pos, u.mem.Path, u.in.Name, "it reads itself ("+u.full+"): an expression cannot read its own value; keep the previous value in a memory: member a program sets")
			continue
		}
		in := map[int]bool{}
		for _, v := range comp {
			in[v] = true
		}
		names := make([]string, len(comp))
		for i, v := range comp {
			names[i] = units[v].full
		}
		ring := shortestCycle(first, edges, in)
		path := make([]string, len(ring))
		for i, v := range ring {
			path[i] = units[v].full
		}
		f.add(u.mem.Pos, u.mem.Path, u.in.Name, "it is in a cycle, "+strings.Join(path, " → ")+": the expr: members "+strings.Join(names, ", ")+
			" read each other, and an expression cannot read itself, directly or through another expr:; break the cycle with a memory: member a program sets")
	}
}

// shortestCycle is a shortest path from s back to s inside the component
// in (breadth first), as nodes, s first and last.
func shortestCycle(s int, edges [][]int, in map[int]bool) []int {
	prev := map[int]int{}
	queue := []int{s}
	seen := map[int]bool{}
	for len(queue) > 0 {
		v := queue[0]
		queue = queue[1:]
		for _, w := range edges[v] {
			if w == s {
				path := []int{s}
				for x := v; x != s; x = prev[x] {
					path = append(path, x)
				}
				path = append(path, s)
				// path is s, back to front, s: put it in reading order.
				for i, j := 1, len(path)-2; i < j; i, j = i+1, j-1 {
					path[i], path[j] = path[j], path[i]
				}
				return path
			}
			if in[w] && !seen[w] {
				seen[w] = true
				prev[w] = v
				queue = append(queue, w)
			}
		}
	}
	return []int{s, s}
}

// projectLibs is the project's library prelude, as runtime.New composes it.
func projectLibs(fsys fs.FS, m *Manifest) ([]stproject.Library, error) {
	libs, err := stproject.Libraries(fsys, nil)
	if err != nil {
		return nil, err
	}
	return withDeviceTypes(libs, m.devices), nil
}

// sortedFindings is a stable order for problems from several passes.
func sortedFindings(ps []wiring.Problem) []wiring.Problem {
	sort.SliceStable(ps, func(i, j int) bool {
		if ps[i].Pos.File != ps[j].Pos.File {
			return ps[i].Pos.File < ps[j].Pos.File
		}
		return ps[i].Pos.Line < ps[j].Pos.Line
	})
	return ps
}
