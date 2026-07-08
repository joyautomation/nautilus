package st

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/joyautomation/nautilus/lang/ir"
)

// MigrateProgramGlobals rewrites src so every PLC-global referenced in the
// PROGRAM body is explicitly declared in a VAR_EXTERNAL block. Sources that
// don't define a PROGRAM body (FUNCTION_BLOCK / FUNCTION libraries) are
// returned unchanged — those already require explicit declarations.
//
// The migration exists to flip the historical "implicit globals" injection
// off without breaking existing PROGRAMs: a one-shot pass converts every
// bare global reference into a real declaration, matching the FB shape.
// Returns the rewritten source and the list of names that were added.
func MigrateProgramGlobals(src string, globalTypes map[string]*ir.Type) (string, []string, error) {
	if src == "" || len(globalTypes) == 0 {
		return src, nil, nil
	}
	prog, err := Parse(src)
	if err != nil {
		// Parse error means we can't analyze — leave the source alone so the
		// user sees the underlying compile error on their next save.
		return src, nil, nil
	}
	// FB / FUNCTION files never had implicit injection; only the top-level
	// PROGRAM body (or a bare-statements library) needs migration.
	if prog.TopKeyword != "" && prog.TopKeyword != "PROGRAM" {
		return src, nil, nil
	}
	if len(prog.Statements) == 0 {
		return src, nil, nil
	}

	declared := map[string]struct{}{}
	for _, vb := range prog.VarBlocks {
		for _, vd := range vb.Variables {
			declared[vd.Name] = struct{}{}
		}
	}

	used := map[string]struct{}{}
	for _, s := range prog.Statements {
		collectStmtIdents(s, used)
	}

	var missing []string
	for name := range used {
		if _, ok := declared[name]; ok {
			continue
		}
		t, ok := globalTypes[name]
		if !ok || t == nil {
			continue
		}
		if t.Kind == ir.TypeFB {
			// FB-instance globals shouldn't be re-declared as VAR_EXTERNAL —
			// they need to be passed as parameters or instantiated locally.
			continue
		}
		missing = append(missing, name)
	}
	if len(missing) == 0 {
		return src, nil, nil
	}
	sort.Strings(missing)

	var lines []string
	for _, n := range missing {
		lines = append(lines, fmt.Sprintf("  %s : %s;", n, renderTypeForExternal(globalTypes[n])))
	}
	block := "VAR_EXTERNAL\n" + strings.Join(lines, "\n") + "\nEND_VAR\n"

	return insertAfterProgramHeader(src, block), missing, nil
}

// collectStmtIdents walks a statement tree and adds every identifier-like
// name it sees to out. Member-access right-hand sides are skipped — those
// are field names, not standalone identifiers. Call targets and FOR loop
// variables are recorded since they can resolve to globals.
func collectStmtIdents(s Statement, out map[string]struct{}) {
	switch st := s.(type) {
	case *AssignStmt:
		collectExprIdents(st.TargetExpr, out)
		collectExprIdents(st.Value, out)
	case *IfStmt:
		collectExprIdents(st.Condition, out)
		for _, c := range st.Then {
			collectStmtIdents(c, out)
		}
		for _, e := range st.ElsIfs {
			collectExprIdents(e.Condition, out)
			for _, c := range e.Body {
				collectStmtIdents(c, out)
			}
		}
		for _, c := range st.Else {
			collectStmtIdents(c, out)
		}
	case *ForStmt:
		out[st.Variable] = struct{}{}
		collectExprIdents(st.Start, out)
		collectExprIdents(st.End, out)
		if st.Step != nil {
			collectExprIdents(st.Step, out)
		}
		for _, c := range st.Body {
			collectStmtIdents(c, out)
		}
	case *WhileStmt:
		collectExprIdents(st.Condition, out)
		for _, c := range st.Body {
			collectStmtIdents(c, out)
		}
	case *RepeatStmt:
		collectExprIdents(st.Condition, out)
		for _, c := range st.Body {
			collectStmtIdents(c, out)
		}
	case *CaseStmt:
		collectExprIdents(st.Expression, out)
		for _, cc := range st.Cases {
			for _, v := range cc.Values {
				collectExprIdents(v, out)
			}
			for _, c := range cc.Body {
				collectStmtIdents(c, out)
			}
		}
		for _, c := range st.Else {
			collectStmtIdents(c, out)
		}
	case *CallStmt:
		collectExprIdents(st.Call, out)
	}
}

func collectExprIdents(e Expression, out map[string]struct{}) {
	if e == nil {
		return
	}
	switch ex := e.(type) {
	case *IdentExpr:
		out[ex.Name] = struct{}{}
	case *BinaryExpr:
		collectExprIdents(ex.Left, out)
		collectExprIdents(ex.Right, out)
	case *UnaryExpr:
		collectExprIdents(ex.Operand, out)
	case *CallExpr:
		out[ex.Name] = struct{}{}
		for _, a := range ex.Args {
			collectExprIdents(a, out)
		}
		for _, na := range ex.NamedArgs {
			collectExprIdents(na.Value, out)
		}
	case *MemberExpr:
		collectExprIdents(ex.Object, out)
	case *IndexExpr:
		collectExprIdents(ex.Array, out)
		for _, i := range ex.Indices {
			collectExprIdents(i, out)
		}
	}
}

// renderTypeForExternal renders an ir.Type back to ST syntax suitable for a
// VAR_EXTERNAL declaration. Primitives map 1:1; structs use their declared
// name; arrays render as `ARRAY [lo..hi] OF Elem`. FB types should be
// filtered out before calling this — they have no useful VAR_EXTERNAL form.
func renderTypeForExternal(t *ir.Type) string {
	if t == nil {
		return "BOOL"
	}
	switch t.Kind {
	case ir.TypeBool:
		return "BOOL"
	case ir.TypeInt:
		return "INT"
	case ir.TypeReal:
		return "REAL"
	case ir.TypeTime:
		return "TIME"
	case ir.TypeString:
		return "STRING"
	case ir.TypeStruct:
		if t.Struct != nil && t.Struct.Name != "" {
			return t.Struct.Name
		}
		return "STRUCT"
	case ir.TypeArray:
		if t.Elem != nil {
			hi := t.ArrLoBound + t.ArrLen - 1
			return fmt.Sprintf("ARRAY [%d..%d] OF %s", t.ArrLoBound, hi, renderTypeForExternal(t.Elem))
		}
		return "ARRAY OF ?"
	}
	return "BOOL"
}

var programHeaderRe = regexp.MustCompile(`(?m)^([ \t]*PROGRAM[ \t]+[A-Za-z_][A-Za-z0-9_]*[ \t]*\r?\n)`)

func insertAfterProgramHeader(src, block string) string {
	loc := programHeaderRe.FindStringIndex(src)
	if loc == nil {
		// Bare-statements library: prepend the block at the top of the file.
		return block + "\n" + src
	}
	return src[:loc[1]] + block + src[loc[1]:]
}
