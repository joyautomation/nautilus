package project

import (
	"fmt"
	"io/fs"
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
// expr: that reads it back. The runtime does not evaluate expressions yet
// (slice 8b), so a project whose expressions all check clean is still
// refused at load, naming the slice.
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
// holds the instance tags (composeWiring appends them first).
func checkExprs(fsys fs.FS, m *Manifest, set *wirebind.Set, types map[string]*ir.Type) []wiring.Problem {
	var units []*exprUnit
	var f exprFindings
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
		}
	}
	if len(units) == 0 {
		return f.problems()
	}
	libs, err := projectLibs(fsys, m)
	if err != nil {
		return append(f.problems(), wiring.Problem{Msg: "expr: members cannot be checked: the project's libraries do not read: " + err.Error()})
	}
	srcs := make([]string, len(libs))
	for i, l := range libs {
		srcs[i] = l.ST
	}
	prelude := stproject.Join(srcs, "")
	preludeLines := strings.Count(prelude, "\n")
	where := libLocator(libs)

	exprCompile(units, prelude, preludeLines, m, &f)
	exprPurity(units, prelude, where, &f)
	exprCycles(units, &f)
	return f.problems()
}

// exprCompile lowers the units as one program; a unit that does not
// compile is reported on its member's line and dropped, and the rest are
// lowered again, so every bad unit is named in one check.
func exprCompile(units []*exprUnit, prelude string, preludeLines int, m *Manifest, f *exprFindings) {
	defs, err := TagDefs(m)
	if err != nil {
		return // the tags report their own problem
	}
	live := append([]*exprUnit(nil), units...)
	for len(live) > 0 {
		us := make([]st.ExprUnit, len(live))
		for i, u := range live {
			us[i] = st.ExprUnit{Text: u.text, Result: u.mem.Type.String()}
		}
		prog, err := st.Parse(prelude + st.ExprProgram(us))
		if err != nil {
			// The units parsed one by one, so this is the prelude's own
			// error, which its file's check reports.
			return
		}
		types, _ := st.Types(prog)
		scope, _ := runtime.ResolveTagScope(defs, types)
		_, err = st.LowerWithOpts(prog, scope.LowerOpts())
		if err == nil {
			return
		}
		le, ok := st.AsLowerError(err)
		i := -1
		if ok {
			i = st.ExprUnitOfLine(len(live), le.Pos.Line-preludeLines)
		}
		if i < 0 {
			return // not a unit's: a library error, reported where it is
		}
		u := live[i]
		f.add(u.mem.Pos, u.mem.Path, u.in.Name, fmt.Sprintf("%q does not compile: %s", u.mem.Expr, exprErrText(le.Err.Error())))
		live = append(live[:i], live[i+1:]...)
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
	idx := map[string]int{}
	for i, u := range units {
		idx[ir.NameKey(u.full)] = i
	}
	edges := make([][]int, len(units))
	for i, u := range units {
		seen := map[int]bool{}
		for _, r := range u.refs {
			read := ir.NameKey(r.Full(u.in.Name))
			for j, v := range units {
				n := ir.NameKey(v.full)
				if !seen[j] && (read == n || strings.HasPrefix(n, read+".") || strings.HasPrefix(n, read+"[") || strings.HasPrefix(read, n+".")) {
					seen[j] = true
					edges[i] = append(edges[i], j)
				}
			}
		}
	}
	// Each simple cycle once, from its lowest unit: a DFS from each start
	// that only visits higher units finds it exactly once.
	said := map[string]bool{}
	for s := range units {
		var path []int
		on := map[int]bool{}
		var dfs func(int)
		dfs = func(v int) {
			path = append(path, v)
			on[v] = true
			for _, w := range edges[v] {
				if w == s {
					names := make([]string, 0, len(path)+1)
					for _, p := range path {
						names = append(names, units[p].full)
					}
					names = append(names, units[s].full)
					key := strings.Join(names, ">")
					if !said[key] {
						said[key] = true
						u := units[s]
						msg := "it is in a cycle, " + strings.Join(names, " → ") + ": an expression cannot read itself, directly or through another expr:; break the cycle with a memory: member a program sets"
						if len(path) == 1 {
							msg = "it reads itself (" + u.full + "): an expression cannot read its own value; keep the previous value in a memory: member a program sets"
						}
						f.add(u.mem.Pos, u.mem.Path, u.in.Name, msg)
					}
					continue
				}
				if w > s && !on[w] {
					dfs(w)
				}
			}
			on[v] = false
			path = path[:len(path)-1]
		}
		dfs(s)
	}
}

// exprFence refuses what checked clean: this build does not evaluate
// expressions yet.
func exprFence(set *wirebind.Set) []wiring.Problem {
	var f exprFindings
	for _, in := range set.Instances {
		for _, mem := range in.Members {
			if mem.Kind == wiring.Expr {
				f.add(mem.Pos, mem.Path, in.Name, "checks clean, but this build does not evaluate expressions yet: they run from slice 8b of struct wiring (#1458)")
			}
		}
	}
	return f.problems()
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
