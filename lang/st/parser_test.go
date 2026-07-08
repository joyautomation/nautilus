package st

import (
	"testing"
)

func mustParse(t *testing.T, src string) *Program {
	t.Helper()
	prog, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v\nsource:\n%s", err, src)
	}
	return prog
}

// ─── Based and typed literals ──────────────────────────────────────────────

func TestParseBasedNumbers(t *testing.T) {
	cases := []struct {
		src  string
		base int
		raw  string
	}{
		{"x := 16#FF;", 16, "FF"},
		{"x := 2#1010;", 2, "1010"},
		{"x := 8#777;", 8, "777"},
	}
	for _, tc := range cases {
		prog := mustParse(t, tc.src)
		if len(prog.Statements) != 1 {
			t.Fatalf("%q: want 1 stmt, got %d", tc.src, len(prog.Statements))
		}
		asn := prog.Statements[0].(*AssignStmt)
		lit, ok := asn.Value.(*NumberLit)
		if !ok {
			t.Fatalf("%q: want NumberLit, got %T", tc.src, asn.Value)
		}
		if lit.Base != tc.base || lit.Value != tc.raw {
			t.Errorf("%q: got base=%d value=%q, want base=%d value=%q",
				tc.src, lit.Base, lit.Value, tc.base, tc.raw)
		}
	}
}

func TestParseTypedLiterals(t *testing.T) {
	cases := []struct {
		src      string
		typeName string
		check    func(t *testing.T, inner Expression)
	}{
		{"x := INT#42;", "INT", func(t *testing.T, e Expression) {
			if n, ok := e.(*NumberLit); !ok || n.Value != "42" {
				t.Errorf("inner = %#v, want NumberLit(42)", e)
			}
		}},
		{"x := REAL#3.14;", "REAL", func(t *testing.T, e Expression) {
			if n, ok := e.(*NumberLit); !ok || n.Value != "3.14" {
				t.Errorf("inner = %#v, want NumberLit(3.14)", e)
			}
		}},
		{"x := BOOL#TRUE;", "BOOL", func(t *testing.T, e Expression) {
			if b, ok := e.(*BoolLit); !ok || !b.Value {
				t.Errorf("inner = %#v, want BoolLit(true)", e)
			}
		}},
		{"x := STRING#'hi';", "STRING", func(t *testing.T, e Expression) {
			if s, ok := e.(*StringLit); !ok || s.Value != "hi" {
				t.Errorf("inner = %#v, want StringLit(hi)", e)
			}
		}},
	}
	for _, tc := range cases {
		prog := mustParse(t, tc.src)
		tl, ok := prog.Statements[0].(*AssignStmt).Value.(*TypedLit)
		if !ok {
			t.Fatalf("%q: want TypedLit, got %T", tc.src, prog.Statements[0].(*AssignStmt).Value)
		}
		if tl.TypeName != tc.typeName {
			t.Errorf("%q: typeName=%q, want %q", tc.src, tl.TypeName, tc.typeName)
		}
		tc.check(t, tl.Inner)
	}
}

// TIME#5s and T#5s are aliases. Both produce a bare TimeLit (not a TypedLit)
// so downstream code has exactly one shape for time literals to handle.
func TestParseTimePrefixAliasesToTimeLit(t *testing.T) {
	for _, src := range []string{"d := TIME#5s;", "d := LTIME#100ms;"} {
		prog := mustParse(t, src)
		if _, ok := prog.Statements[0].(*AssignStmt).Value.(*TimeLit); !ok {
			t.Errorf("%q: want TimeLit, got %T", src, prog.Statements[0].(*AssignStmt).Value)
		}
	}
}

// ─── Time literals ─────────────────────────────────────────────────────────

func TestParseTimeLiterals(t *testing.T) {
	cases := []string{"5s", "100ms", "1h30m", "2d4h"}
	for _, raw := range cases {
		prog := mustParse(t, "delay := T#"+raw+";")
		tl, ok := prog.Statements[0].(*AssignStmt).Value.(*TimeLit)
		if !ok {
			t.Fatalf("T#%s: want TimeLit, got %T", raw, prog.Statements[0].(*AssignStmt).Value)
		}
		if tl.Raw != raw {
			t.Errorf("T#%s: raw=%q, want %q", raw, tl.Raw, raw)
		}
	}
}

// ─── Array types and indexing ──────────────────────────────────────────────

func TestParseArrayVarDecl(t *testing.T) {
	prog := mustParse(t, `
VAR
  a : ARRAY[1..10] OF INT;
END_VAR`)
	if len(prog.VarBlocks) != 1 || len(prog.VarBlocks[0].Variables) != 1 {
		t.Fatalf("want 1 var decl, got blocks=%d", len(prog.VarBlocks))
	}
	decl := prog.VarBlocks[0].Variables[0]
	arr, ok := decl.Type.(*ArrayType)
	if !ok {
		t.Fatalf("Type = %T, want *ArrayType", decl.Type)
	}
	if len(arr.Dims) != 1 {
		t.Fatalf("Dims = %d, want 1", len(arr.Dims))
	}
	if decl.Datatype != "ARRAY[1..10] OF INT" {
		t.Errorf("Datatype = %q, want ARRAY[1..10] OF INT", decl.Datatype)
	}
	elem, ok := arr.Elem.(*ScalarType)
	if !ok || elem.Name != "INT" {
		t.Errorf("elem = %#v, want ScalarType(INT)", arr.Elem)
	}
}

func TestParseArrayIndexExpression(t *testing.T) {
	prog := mustParse(t, "x := a[i];")
	asn := prog.Statements[0].(*AssignStmt)
	idx, ok := asn.Value.(*IndexExpr)
	if !ok {
		t.Fatalf("Value = %T, want *IndexExpr", asn.Value)
	}
	if _, ok := idx.Array.(*IdentExpr); !ok {
		t.Errorf("Array base = %T, want IdentExpr", idx.Array)
	}
	if len(idx.Indices) != 1 {
		t.Errorf("Indices = %d, want 1", len(idx.Indices))
	}
}

func TestParseArrayIndexAssignmentTarget(t *testing.T) {
	prog := mustParse(t, "a[i] := 42;")
	asn := prog.Statements[0].(*AssignStmt)
	if _, ok := asn.TargetExpr.(*IndexExpr); !ok {
		t.Errorf("TargetExpr = %T, want *IndexExpr", asn.TargetExpr)
	}
}

func TestParseMultiDimArray(t *testing.T) {
	prog := mustParse(t, `
VAR
  grid : ARRAY[0..4, 0..4] OF REAL;
END_VAR`)
	decl := prog.VarBlocks[0].Variables[0]
	arr := decl.Type.(*ArrayType)
	if len(arr.Dims) != 2 {
		t.Errorf("Dims = %d, want 2", len(arr.Dims))
	}
}

// ─── UDT / STRUCT ──────────────────────────────────────────────────────────

func TestParseStructTypeDecl(t *testing.T) {
	prog := mustParse(t, `
TYPE
  Point : STRUCT
    x : INT;
    y : INT := 5;
  END_STRUCT;
END_TYPE`)
	if len(prog.TypeDecls) != 1 {
		t.Fatalf("TypeDecls = %d, want 1", len(prog.TypeDecls))
	}
	td := prog.TypeDecls[0]
	if td.Name != "Point" {
		t.Errorf("Name = %q, want Point", td.Name)
	}
	st, ok := td.Type.(*StructType)
	if !ok {
		t.Fatalf("Type = %T, want *StructType", td.Type)
	}
	if len(st.Fields) != 2 {
		t.Fatalf("Fields = %d, want 2", len(st.Fields))
	}
	if st.Fields[1].Initial == nil {
		t.Errorf("y should have an initializer")
	}
}

func TestParseUDTInstance(t *testing.T) {
	prog := mustParse(t, `
VAR
  p : Point;
END_VAR
p.x := 10;`)
	decl := prog.VarBlocks[0].Variables[0]
	nt, ok := decl.Type.(*NamedType)
	if !ok || nt.Name != "Point" {
		t.Fatalf("Type = %#v, want NamedType(Point)", decl.Type)
	}
	asn := prog.Statements[0].(*AssignStmt)
	if _, ok := asn.TargetExpr.(*MemberExpr); !ok {
		t.Errorf("TargetExpr = %T, want MemberExpr", asn.TargetExpr)
	}
	if asn.Target != "p.x" {
		t.Errorf("Target shim = %q, want p.x", asn.Target)
	}
}

// ─── FB instance + named-argument call ─────────────────────────────────────

func TestParseFBInstanceCall(t *testing.T) {
	prog := mustParse(t, `
VAR
  tmr : TON;
END_VAR
tmr(IN := start, PT := T#5s);
done := tmr.Q;`)
	if prog.VarBlocks[0].Variables[0].Datatype != "TON" {
		t.Errorf("Datatype = %q, want TON", prog.VarBlocks[0].Variables[0].Datatype)
	}
	call, ok := prog.Statements[0].(*CallStmt)
	if !ok {
		t.Fatalf("Statements[0] = %T, want *CallStmt", prog.Statements[0])
	}
	if call.Call.Name != "tmr" {
		t.Errorf("Name = %q, want tmr", call.Call.Name)
	}
	if len(call.Call.NamedArgs) != 2 {
		t.Fatalf("NamedArgs = %d, want 2", len(call.Call.NamedArgs))
	}
	if call.Call.NamedArgs[0].Name != "IN" || call.Call.NamedArgs[1].Name != "PT" {
		t.Errorf("NamedArgs names = %v, want [IN, PT]", []string{call.Call.NamedArgs[0].Name, call.Call.NamedArgs[1].Name})
	}
	asn := prog.Statements[1].(*AssignStmt)
	if _, ok := asn.Value.(*MemberExpr); !ok {
		t.Errorf("tmr.Q value = %T, want MemberExpr", asn.Value)
	}
}

func TestParsePositionalCall(t *testing.T) {
	// Ensure positional calls still work unchanged.
	prog := mustParse(t, "x := max(a, b, 3);")
	call := prog.Statements[0].(*AssignStmt).Value.(*CallExpr)
	if len(call.Args) != 3 || len(call.NamedArgs) != 0 {
		t.Errorf("positional call: Args=%d NamedArgs=%d, want 3/0", len(call.Args), len(call.NamedArgs))
	}
}

// ─── Flow-control additions ────────────────────────────────────────────────

func TestParseExitAndContinue(t *testing.T) {
	prog := mustParse(t, `
FOR i := 1 TO 10 DO
  IF i = 3 THEN CONTINUE; END_IF;
  IF i = 7 THEN EXIT; END_IF;
END_FOR`)
	body := prog.Statements[0].(*ForStmt).Body
	if len(body) != 2 {
		t.Fatalf("body stmts = %d, want 2", len(body))
	}
	contIf := body[0].(*IfStmt)
	if _, ok := contIf.Then[0].(*ContinueStmt); !ok {
		t.Errorf("first body stmt = %T, want ContinueStmt", contIf.Then[0])
	}
	exitIf := body[1].(*IfStmt)
	if _, ok := exitIf.Then[0].(*ExitStmt); !ok {
		t.Errorf("second body stmt = %T, want ExitStmt", exitIf.Then[0])
	}
}

// ─── VAR block kinds and modifiers ─────────────────────────────────────────

func TestParseVarBlockKinds(t *testing.T) {
	prog := mustParse(t, `
VAR_INPUT start : BOOL; END_VAR
VAR_OUTPUT done : BOOL; END_VAR
VAR_IN_OUT count : INT; END_VAR
VAR_TEMP scratch : REAL; END_VAR`)
	if len(prog.VarBlocks) != 4 {
		t.Fatalf("blocks = %d, want 4", len(prog.VarBlocks))
	}
	wantKinds := []string{"VAR_INPUT", "VAR_OUTPUT", "VAR_IN_OUT", "VAR_TEMP"}
	for i, k := range wantKinds {
		if prog.VarBlocks[i].Kind != k {
			t.Errorf("block[%d] kind = %q, want %q", i, prog.VarBlocks[i].Kind, k)
		}
	}
}

func TestParseVarRetainConstant(t *testing.T) {
	prog := mustParse(t, `
VAR RETAIN
  counter : INT := 0;
END_VAR
VAR CONSTANT
  max : INT := 100;
END_VAR`)
	if !prog.VarBlocks[0].Retain {
		t.Error("first block should be Retain")
	}
	if !prog.VarBlocks[1].Constant {
		t.Error("second block should be Constant")
	}
}

func TestParseMultiNameDecl(t *testing.T) {
	prog := mustParse(t, `
VAR
  a, b, c : INT;
END_VAR`)
	vars := prog.VarBlocks[0].Variables
	if len(vars) != 3 {
		t.Fatalf("vars = %d, want 3", len(vars))
	}
	for i, want := range []string{"a", "b", "c"} {
		if vars[i].Name != want {
			t.Errorf("vars[%d].Name = %q, want %q", i, vars[i].Name, want)
		}
		if vars[i].Datatype != "INT" {
			t.Errorf("vars[%d].Datatype = %q, want INT", i, vars[i].Datatype)
		}
	}
}

// ─── Combined program sanity check ─────────────────────────────────────────

func TestParseFullProgram(t *testing.T) {
	src := `
TYPE
  Motor : STRUCT
    running : BOOL;
    speed : REAL;
  END_STRUCT;
END_TYPE

PROGRAM MotorControl
  VAR
    m1 : Motor;
    history : ARRAY[1..10] OF REAL;
    i : INT;
    timer : TON;
  END_VAR

  VAR_INPUT
    command : INT;
  END_VAR

  CASE command OF
    16#01: m1.running := TRUE;
    16#02: m1.running := FALSE;
    ELSE
      RETURN;
  END_CASE;

  FOR i := 1 TO 10 DO
    history[i] := m1.speed;
  END_FOR;

  timer(IN := m1.running, PT := T#5s);
END_PROGRAM`

	prog := mustParse(t, src)
	if prog.Name != "MotorControl" {
		t.Errorf("Name = %q, want MotorControl", prog.Name)
	}
	if len(prog.TypeDecls) != 1 {
		t.Errorf("TypeDecls = %d, want 1", len(prog.TypeDecls))
	}
	if len(prog.VarBlocks) != 2 {
		t.Errorf("VarBlocks = %d, want 2", len(prog.VarBlocks))
	}
	if len(prog.Statements) != 3 {
		t.Errorf("Statements = %d, want 3", len(prog.Statements))
	}
}
