package stgen

import (
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/st"
)

func TestRenderOrdersDependenciesAndCompiles(t *testing.T) {
	// Declared dependent-first on purpose; Render must reorder so Header
	// precedes Plt, and the result must compile.
	plt := Struct("Plt_Type",
		Field("Header", Ref("Header_Type")),
		Field("Count", DINT),
		Field("Samples", ArrayOf(REAL, 0, 9)),
	)
	header := Struct("Header_Type",
		Field("Displacement", REAL),
		FieldInit("Valid", BOOL, "TRUE"),
	)

	src, err := Render(plt, header)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Index(src, "Header_Type : STRUCT") > strings.Index(src, "Plt_Type : STRUCT") {
		t.Errorf("dependency order wrong:\n%s", src)
	}
	if !strings.Contains(src, "Samples : ARRAY [0..9] OF REAL;") {
		t.Errorf("array not rendered:\n%s", src)
	}
	if !strings.Contains(src, "Valid : BOOL := TRUE;") {
		t.Errorf("init not rendered:\n%s", src)
	}

	// The generated types are usable by a real program.
	program := src + `
PROGRAM Main
VAR_EXTERNAL
  P : Plt_Type;
  Out : REAL;
END_VAR
IF P.Header.Valid THEN
  Out := P.Header.Displacement + P.Samples[3];
END_IF;
END_PROGRAM`
	prog, err := st.Parse(program)
	if err != nil {
		t.Fatalf("generated types don't parse with a program: %v", err)
	}
	if _, err := st.Lower(prog); err != nil {
		t.Fatalf("generated types don't lower with a program: %v", err)
	}
}

func TestRenderRejectsDanglingRefAndCycles(t *testing.T) {
	// Ref to a type that isn't declared anywhere → validation (lowering) fails.
	if _, err := Render(Struct("A", Field("b", Ref("Missing")))); err == nil {
		t.Error("dangling ref should error")
	}
	// A cycle is caught before compilation.
	a := Struct("A", Field("b", Ref("B")))
	b := Struct("B", Field("a", Ref("A")))
	if _, err := Render(a, b); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Errorf("cycle should error, got %v", err)
	}
	// A bad identifier is rejected by the parser.
	if _, err := Render(Struct("1Bad", Field("x", INT))); err == nil {
		t.Error("invalid struct name should error")
	}
}

func TestBuildProgrammatically(t *testing.T) {
	// The intended use: assemble a struct from a schema in a loop.
	schema := []struct {
		name string
		typ  Type
	}{
		{"AI", REAL}, {"Alarm", BOOL}, {"Count", DINT}, {"History", ArrayOf(REAL, 0, 23)},
	}
	s := Struct("Point")
	for _, col := range schema {
		s.AddField(Field(col.name, col.typ))
	}
	src, err := Render(s)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{"AI : REAL;", "Alarm : BOOL;", "History : ARRAY [0..23] OF REAL;"} {
		if !strings.Contains(src, want) {
			t.Errorf("missing %q in:\n%s", want, src)
		}
	}
}

func TestEmptyAndVarBlock(t *testing.T) {
	if src, err := Render(); err != nil || src != "" {
		t.Errorf("empty render = %q, %v", src, err)
	}
	vb := VarBlock("VAR_EXTERNAL", Field("Speed", REAL), Field("Motor", Ref("Motor_Type")))
	if !strings.Contains(vb, "VAR_EXTERNAL") || !strings.Contains(vb, "Speed : REAL;") ||
		!strings.Contains(vb, "Motor : Motor_Type;") || !strings.Contains(vb, "END_VAR") {
		t.Errorf("var block wrong:\n%s", vb)
	}
}
