package writer

import (
	"github.com/joyautomation/nautilus/lang/fbd"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/lang/ld"
	"github.com/joyautomation/nautilus/lang/st"
)

// Implicit manifest tags (#177/#210, #248). A PROGRAM names a project tag
// without declaring it, and the compiler binds it: lang/st's lowerer puts
// the manifest's tags in the program's scope behind its own declarations,
// and an identifier resolves case-insensitively (lang/ir/names.go). On
// Logix the tag is a controller tag, exactly as if the program had said
// VAR_EXTERNAL, so the writer has to know which tags the program names.
//
// It finds out the compiler's way, on the compiler's AST: an ST program
// is the parse the writer already has; a ladder program is its
// transpilation through FBD, the ST the compiler actually compiles. Identifiers are
// resolved in the lowerer's order — the program's own declarations, then
// the manifest's tags — and only where the lowerer resolves a variable:
// a name expression, an assignment or FOR target, an array index, a
// member access's base, an argument's value, an output binding's target.
// A member name, a pin name, a function's name and an enumeration
// literal's member (`Mode#Run`) are never a tag. FUNCTION_BLOCK bodies
// are skipped: a block reaches a tag only through its own VAR_EXTERNAL.

// implicitFrom returns the manifest tags (Options.Tags) prog's PROGRAM body
// names without declaring, as VAR_EXTERNAL declarations in first-use order,
// with the manifest's spelling and type. line maps a position in prog to a
// line of the user's source (ladder: through the transpilation).
func (lw *lowered) implicitFrom(prog *st.Program, line func(int) int) []ld.VarDecl {
	if len(lw.opts.Tags) == 0 || prog == nil {
		return nil
	}
	declared := map[string]bool{}
	for _, vb := range prog.VarBlocks {
		for _, v := range vb.Variables {
			declared[ir.NameKey(v.Name)] = true
		}
	}
	for _, v := range lw.vars {
		declared[ir.NameKey(v.Name)] = true
	}
	var out []ld.VarDecl
	seen := map[string]bool{}
	u := &useWalker{note: func(name string, pos st.Pos) {
		k := ir.NameKey(name)
		if declared[k] || seen[k] {
			return
		}
		typ, tag, ok := ir.Lookup(lw.opts.Tags, name)
		if !ok {
			return
		}
		seen[k] = true
		out = append(out, ld.VarDecl{Name: tag, Type: typ, Section: "VAR_EXTERNAL", Line: line(pos.Line)})
	}}
	u.stmts(prog.Statements)
	return out
}

// implicitLadder is implicitFrom for a ladder program, on the ST the
// compiler compiles: lang/ld transpiles the rungs to FBD, lang/fbd the FBD
// to ST (runtime.Program does the same). A program either step refuses
// does not compile, so which tags it names is unknowable; that is a
// diagnostic, never a project silently missing its tags.
func (lw *lowered) implicitLadder(src string) []ld.VarDecl {
	if len(lw.opts.Tags) == 0 || src == "" {
		return nil
	}
	fbdSrc, ldLines, err := ld.TranspileWithLines(src, lw.opts.Libs...)
	var stSrc string
	var fbdLines []int
	if err == nil {
		stSrc, fbdLines, err = fbd.TranspileWithLines(fbdSrc)
	}
	var prog *st.Program
	if err == nil {
		prog, err = st.Parse(stSrc)
	}
	if err != nil {
		lw.diag(ruleVarSection, 0, "", "the program does not compile (%v), so the manifest tags it names cannot be resolved; `naut check` names the problem", err)
		return nil
	}
	back := func(lines []int, n int) int {
		if n >= 1 && n <= len(lines) {
			return lines[n-1]
		}
		return n
	}
	return lw.implicitFrom(prog, func(n int) int { return back(ldLines, back(fbdLines, n)) })
}

// useWalker visits every identifier the lowerer would resolve as a
// variable.
type useWalker struct {
	note func(name string, pos st.Pos)
}

func (u *useWalker) stmts(ss []st.Statement) {
	for _, s := range ss {
		u.stmt(s)
	}
}

func (u *useWalker) stmt(s st.Statement) {
	switch v := s.(type) {
	case *st.AssignStmt:
		if v.TargetExpr != nil {
			u.expr(v.TargetExpr)
		} else {
			u.note(v.Target, v.Pos)
		}
		u.expr(v.Value)
	case *st.IfStmt:
		u.expr(v.Condition)
		u.stmts(v.Then)
		for _, e := range v.ElsIfs {
			u.expr(e.Condition)
			u.stmts(e.Body)
		}
		u.stmts(v.Else)
	case *st.CaseStmt:
		u.expr(v.Expression)
		for _, c := range v.Cases {
			for _, x := range c.Values {
				u.expr(x)
			}
			for _, r := range c.Ranges {
				u.expr(r.Lo)
				u.expr(r.Hi)
			}
			u.stmts(c.Body)
		}
		u.stmts(v.Else)
	case *st.ForStmt:
		u.note(v.Variable, v.Pos)
		u.expr(v.Start)
		u.expr(v.End)
		u.expr(v.Step)
		u.stmts(v.Body)
	case *st.WhileStmt:
		u.expr(v.Condition)
		u.stmts(v.Body)
	case *st.RepeatStmt:
		u.stmts(v.Body)
		u.expr(v.Condition)
	case *st.CallStmt:
		u.expr(v.Call)
	}
}

func (u *useWalker) expr(e st.Expression) {
	switch v := e.(type) {
	case nil:
	case *st.IdentExpr:
		u.note(v.Name, v.Pos)
	case *st.BinaryExpr:
		u.expr(v.Left)
		u.expr(v.Right)
	case *st.UnaryExpr:
		u.expr(v.Operand)
	case *st.MemberExpr:
		u.expr(v.Object)
	case *st.IndexExpr:
		u.expr(v.Array)
		for _, i := range v.Indices {
			u.expr(i)
		}
	case *st.CallExpr:
		// The head is a function or an instance, never a manifest tag
		// (a tag is never a block); an indexed head's index can be.
		u.expr(v.Callee)
		for _, a := range v.Args {
			u.expr(a)
		}
		for _, a := range v.NamedArgs {
			u.expr(a.Value)
		}
		for _, b := range v.OutputBindings {
			u.expr(b.Target)
		}
	case *st.TypedLit:
		// INT#5 wraps a number; Mode#Run names a member, not a variable.
	}
}
