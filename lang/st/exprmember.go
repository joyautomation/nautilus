package st

import (
	"fmt"
	"sort"
	"strings"

	"github.com/joyautomation/nautilus/lang/ir"
)

// Expression members of struct wiring (#1458 slice 8, one-struct-model §6).
//
// An expr: member is one ST expression, evaluated once per main scan. This
// file is what check, the editor and the runtime share about it: parsing
// it as exactly one expression, rewriting a bare sibling name onto its
// instance ("Running" → "P101.Running"), listing what it reads (for the
// dependency order and cycles), building the one program that compiles a
// project's expressions, and the purity check that keeps an expression an
// expression.

// ParseExpr parses src as exactly one expression running to the end of the
// input. Anything after it (a `;`, a second statement) is an error: an
// expr: is never a statement, so it can assign nothing.
func ParseExpr(src string) (Expression, error) {
	if strings.TrimSpace(src) == "" {
		return nil, fmt.Errorf("the expression is empty")
	}
	tokens, lexErrs := LexErrors(src)
	if len(lexErrs) > 0 {
		e := lexErrs[0]
		return nil, fmt.Errorf("col %d: %s", e.Pos.Col, e.Msg)
	}
	p := &Parser{tokens: tokens}
	e, err := p.parseExpression()
	if err != nil {
		return nil, err
	}
	if tok := p.peek(); tok.Type != TokenEOF {
		return nil, fmt.Errorf("col %d: %q after the expression: an expr: is one expression, with no ; and no statements", tok.Col, tok.Literal)
	}
	return e, nil
}

// ExprRef is one name an expression reads, in a value position.
type ExprRef struct {
	// Root is the name as written ("Running", "P102", "Level").
	Root string
	// Path is the member chain the read names from the root, as far as it
	// is known: "Cmd.Start", "Levels" for a variable index (the whole array
	// counts as read), "" for the bare root.
	Path string
	// Sibling is set when Root is a member of the expression's own
	// instance (RewriteExpr rewrote it onto the instance).
	Sibling bool
	Pos     Pos
}

// Full is the read as a tag path: the instance's own for a sibling.
func (r ExprRef) Full(instance string) string {
	root := r.Root
	if r.Sibling {
		root = instance + "." + r.Root
	}
	if r.Path == "" {
		return root
	}
	return root + "." + r.Path
}

// ExprRefs lists what e reads, in source order: each chain once, from its
// root identifier.
func ExprRefs(e Expression) []ExprRef {
	var out []ExprRef
	var walk func(Expression)
	walk = func(e Expression) {
		switch v := e.(type) {
		case *IdentExpr:
			out = append(out, ExprRef{Root: v.Name, Pos: v.Pos})
		case *MemberExpr, *IndexExpr:
			if root, path, ok := refChain(e); ok {
				out = append(out, ExprRef{Root: root.Name, Path: path, Pos: root.Pos})
			}
			indexExprs(e, walk)
		case *BinaryExpr:
			walk(v.Left)
			walk(v.Right)
		case *UnaryExpr:
			walk(v.Operand)
		case *CallExpr:
			if v.Callee != nil {
				walk(v.Callee)
			}
			for _, a := range v.Args {
				walk(a)
			}
			for _, a := range v.NamedArgs {
				walk(a.Value)
			}
		}
	}
	walk(e)
	return out
}

// refChain reads a member/index chain down to its root identifier. The
// path stops at the first index: Levels[i].X reads Levels, whatever i is.
func refChain(e Expression) (*IdentExpr, string, bool) {
	var segs []string
	for {
		switch v := e.(type) {
		case *IdentExpr:
			// segs were collected outermost first.
			for i, j := 0, len(segs)-1; i < j; i, j = i+1, j-1 {
				segs[i], segs[j] = segs[j], segs[i]
			}
			return v, strings.Join(segs, "."), true
		case *MemberExpr:
			segs = append(segs, v.Member)
			e = v.Object
		case *IndexExpr:
			segs = segs[:0] // everything below an index is the indexed whole
			e = v.Array
		default:
			return nil, "", false
		}
	}
}

// indexExprs walks the index expressions inside a chain (they are reads
// of their own).
func indexExprs(e Expression, walk func(Expression)) {
	for {
		switch v := e.(type) {
		case *MemberExpr:
			e = v.Object
		case *IndexExpr:
			for _, ix := range v.Indices {
				walk(ix)
			}
			e = v.Array
		default:
			if _, ok := e.(*IdentExpr); !ok && e != nil {
				walk(e)
			}
			return
		}
	}
}

// RewriteExpr parses src (one expression) and rewrites each bare name that
// isSibling says is a member of the instance onto the instance: with
// instance P101, "Running AND NOT Fault" becomes "P101.Running AND NOT
// P101.Fault". A sibling wins over a project tag of the same name (§6);
// the caller warns about that. The refs come back with Sibling set.
func RewriteExpr(instance string, isSibling func(name string) bool, src string) (string, []ExprRef, error) {
	e, err := ParseExpr(src)
	if err != nil {
		return "", nil, err
	}
	refs := ExprRefs(e)
	lineStart := []int{0}
	for i := 0; i < len(src); i++ {
		if src[i] == '\n' {
			lineStart = append(lineStart, i+1)
		}
	}
	var at []int
	for i := range refs {
		if !isSibling(refs[i].Root) {
			continue
		}
		refs[i].Sibling = true
		p := refs[i].Pos
		if p.Line < 1 || p.Line > len(lineStart) {
			return "", nil, fmt.Errorf("internal: no position for %s", refs[i].Root)
		}
		at = append(at, lineStart[p.Line-1]+p.Col-1)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(at)))
	out := src
	for _, off := range at {
		out = out[:off] + instance + "." + out[off:]
	}
	return out, refs, nil
}

// ExprUnit is one expression, rewritten, and the member it computes, as a
// tag path ("P101.Healthy").
type ExprUnit struct {
	Text   string // RewriteExpr's output
	Target string
}

// ExprProgramName is the POU the expression units compile into.
const ExprProgramName = "nautilus_exprs"

// ExprProgram is the one program a project's expressions compile into:
// unit i assigns its member, starting on its own line, so the member's own
// type checks the result (no narrowing, as any assignment) and a compile
// error's line names the unit (ExprUnitOfLine). An expression's own
// newlines are kept, never folded: a // comment ends at its own line, as
// it does when the expression is parsed alone. Composed after the library
// prelude, like any program, so the project's FUNCTIONs and TYPEs are in
// scope and its tags (the instances among them) are implicit globals.
func ExprProgram(units []ExprUnit) string {
	var b strings.Builder
	b.WriteString("PROGRAM " + ExprProgramName + "\n")
	for _, u := range units {
		b.WriteString(u.Target + " := " + u.Text + "\n;\n")
	}
	b.WriteString("END_PROGRAM\n")
	return b.String()
}

// ExprUnitOfLine maps a line of ExprProgram(units) (1-based, relative to
// the program's first line) to the unit it belongs to: -1 for the frame.
func ExprUnitOfLine(units []ExprUnit, line int) int {
	at := 2
	for i, u := range units {
		n := strings.Count(u.Text, "\n") + 2 // its lines, then the ;
		if line >= at && line < at+n {
			return i
		}
		at += n
	}
	return -1
}

// ExprProblem is one purity rule an expression breaks (§6).
type ExprProblem struct {
	// Func is the user FUNCTION that breaks the rule, "" for the
	// expression itself; Via is the chain of calls from the expression to
	// it ("FlowFromDp", "Helper").
	Func string
	Via  []string
	Pos  Pos
	// Rule says what it does ("declares VAR_EXTERNAL").
	Rule string
}

// ExprPurity checks that e only computes: it may call the standard
// functions and the project's FUNCTIONs, and nothing that keeps state,
// writes, reaches a global, or might not finish. prog holds the project's
// declarations (the library prelude parsed); the rules hold at any depth of
// calls:
//
//   - no `=>` (it assigns), in the expression or a function it calls;
//   - no function block, standard (TON, READ) or the project's;
//   - no system function (FIRST_SCAN, TAKEOVER_SCAN, ...): the host
//     answers it from controller state the expression's quality and order
//     cannot see;
//   - a FUNCTION it calls declares no VAR_EXTERNAL or VAR_GLOBAL, no
//     VAR_IN_OUT, no function block instance;
//   - no loop (WHILE, REPEAT, FOR) and no recursion: the VM has no step
//     budget, so an expression must finish by construction, or it would
//     hold the main scan.
func ExprPurity(prog *Program, e Expression) []ExprProblem {
	c := &purity{funcs: map[string]*FunctionDecl{}, fbs: map[string]bool{}, done: map[*FunctionDecl][]ExprProblem{}, onStack: map[*FunctionDecl]bool{}}
	if prog != nil {
		for _, fd := range prog.FuncDecls {
			c.funcs[ir.NameKey(fd.Name)] = fd
		}
		for _, fb := range prog.FBDecls {
			c.fbs[ir.NameKey(fb.Name)] = true
		}
	}
	return c.expr(e, nil)
}

type purity struct {
	funcs   map[string]*FunctionDecl
	fbs     map[string]bool
	done    map[*FunctionDecl][]ExprProblem
	onStack map[*FunctionDecl]bool
}

func (c *purity) isFB(name string) bool {
	if c.fbs[ir.NameKey(name)] {
		return true
	}
	t := ir.LookupFBType(name)
	return t != nil && t.FB != nil
}

// expr checks the calls in one expression; via is the call chain to the
// function the expression is in (nil for the expr: itself).
func (c *purity) expr(e Expression, via []string) []ExprProblem {
	var out []ExprProblem
	var walk func(Expression)
	walk = func(e Expression) {
		switch v := e.(type) {
		case *BinaryExpr:
			walk(v.Left)
			walk(v.Right)
		case *UnaryExpr:
			walk(v.Operand)
		case *MemberExpr:
			walk(v.Object)
		case *IndexExpr:
			walk(v.Array)
			for _, ix := range v.Indices {
				walk(ix)
			}
		case *CallExpr:
			out = append(out, c.call(v, via)...)
			for _, a := range v.Args {
				walk(a)
			}
			for _, a := range v.NamedArgs {
				walk(a.Value)
			}
		}
	}
	walk(e)
	return out
}

func (c *purity) here(via []string, pos Pos, rule string) ExprProblem {
	p := ExprProblem{Pos: pos, Rule: rule}
	if len(via) > 0 {
		p.Func = via[len(via)-1]
		p.Via = append([]string(nil), via...)
	}
	return p
}

func (c *purity) call(v *CallExpr, via []string) []ExprProblem {
	var out []ExprProblem
	if len(v.OutputBindings) > 0 {
		out = append(out, c.here(via, v.Pos, "uses => in a call to "+v.Name+", which assigns"))
	}
	if v.Callee != nil {
		return append(out, c.here(via, v.Pos, "calls a function block instance, which keeps state"))
	}
	if fd := c.funcs[ir.NameKey(v.Name)]; fd != nil {
		if c.onStack[fd] {
			return append(out, c.here(via, v.Pos, "calls "+fd.Name+" recursively, so it might not finish"))
		}
		return append(out, c.function(fd, append(append([]string(nil), via...), fd.Name))...)
	}
	if c.isFB(v.Name) {
		return append(out, c.here(via, v.Pos, "calls the function block "+v.Name+", which keeps state"))
	}
	up := strings.ToUpper(v.Name)
	if b, ok := ir.Builtins[up]; ok {
		if b.HostFn != nil {
			return append(out, c.here(via, v.Pos, "calls the system function "+up+"(), which reads controller state"))
		}
		return out // a standard function: it computes from its arguments
	}
	if ir.IsConversion(up) || strings.Contains(up, "_TO_") {
		return out // a type conversion, standard or of an enumeration
	}
	// Not a FUNCTION, a standard function or a conversion: a function block
	// instance (a project tag of an FB type, T1(...)), which keeps state.
	// What the purity check cannot name is refused, not trusted.
	return append(out, c.here(via, v.Pos, "calls "+v.Name+", which is not a FUNCTION or a standard function (a function block instance keeps state)"))
}

// function checks one FUNCTION, once; via ends with its own name.
func (c *purity) function(fd *FunctionDecl, via []string) []ExprProblem {
	if ps, ok := c.done[fd]; ok {
		// Already checked: the same problems, reached by this chain.
		out := make([]ExprProblem, len(ps))
		for i, p := range ps {
			p.Via = append(append([]string(nil), via...), p.Via[1:]...)
			out[i] = p
		}
		return out
	}
	c.onStack[fd] = true
	var out []ExprProblem
	for _, vb := range fd.VarBlocks {
		switch vb.Kind {
		case "VAR_EXTERNAL", "VAR_GLOBAL":
			out = append(out, c.here(via, vbPos(vb, fd), "declares "+vb.Kind+", so it reaches a global"))
			continue
		case "VAR_IN_OUT":
			out = append(out, c.here(via, vbPos(vb, fd), "has a VAR_IN_OUT parameter, so it writes its caller's variable"))
			continue
		}
		for _, vd := range vb.Variables {
			if c.fbTyped(vd.Type) {
				out = append(out, c.here(via, vd.Pos, "declares "+vd.Name+" : "+vd.Type.String()+", a function block instance, which keeps state"))
			}
		}
	}
	out = append(out, c.stmts(fd.Statements, via)...)
	c.onStack[fd] = false
	// Stored relative to this function (Via starting at its own name), so
	// a later caller re-roots the chain.
	stored := make([]ExprProblem, len(out))
	for i, p := range out {
		p.Via = append([]string(nil), p.Via[len(via)-1:]...)
		stored[i] = p
	}
	c.done[fd] = stored
	return out
}

func vbPos(vb VarBlock, fd *FunctionDecl) Pos {
	if len(vb.Variables) > 0 {
		return vb.Variables[0].Pos
	}
	return fd.Pos
}

func (c *purity) fbTyped(t TypeExpr) bool {
	switch v := t.(type) {
	case *ArrayType:
		return c.fbTyped(v.Elem)
	case *NamedType:
		return c.isFB(v.Name)
	case *ScalarType:
		return c.isFB(v.Name)
	}
	return false
}

func (c *purity) stmts(ss []Statement, via []string) []ExprProblem {
	var out []ExprProblem
	for _, s := range ss {
		switch v := s.(type) {
		case *AssignStmt:
			if v.TargetExpr != nil {
				out = append(out, c.expr(v.TargetExpr, via)...)
			}
			out = append(out, c.expr(v.Value, via)...)
		case *CallStmt:
			out = append(out, c.call(v.Call, via)...)
			for _, a := range v.Call.Args {
				out = append(out, c.expr(a, via)...)
			}
			for _, a := range v.Call.NamedArgs {
				out = append(out, c.expr(a.Value, via)...)
			}
		case *IfStmt:
			out = append(out, c.expr(v.Condition, via)...)
			out = append(out, c.stmts(v.Then, via)...)
			for _, ei := range v.ElsIfs {
				out = append(out, c.expr(ei.Condition, via)...)
				out = append(out, c.stmts(ei.Body, via)...)
			}
			out = append(out, c.stmts(v.Else, via)...)
		case *CaseStmt:
			out = append(out, c.expr(v.Expression, via)...)
			for _, cl := range v.Cases {
				out = append(out, c.stmts(cl.Body, via)...)
			}
			out = append(out, c.stmts(v.Else, via)...)
		case *ForStmt:
			out = append(out, c.here(via, v.Pos, "has a FOR loop; an expression must finish by construction, so it has no loops"))
		case *WhileStmt:
			out = append(out, c.here(via, v.Pos, "has a WHILE loop, which might not finish"))
		case *RepeatStmt:
			out = append(out, c.here(via, v.Pos, "has a REPEAT loop, which might not finish"))
		}
	}
	return out
}
