package st

import (
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/ir"
)

// #175: a file-level VAR_GLOBAL ahead of a PROGRAM (a GVL composed into
// its prelude) declares globals for it, instead of ending the prelude and
// turning `PROGRAM Washer` into a bare statement.
func TestFileScopeVarGlobalAheadOfProgram(t *testing.T) {
	src := "VAR_GLOBAL\n    StartPB : BOOL;\nEND_VAR\nPROGRAM Washer\nVAR_EXTERNAL\n    StartPB : BOOL;\nEND_VAR\nIF StartPB THEN StartPB := FALSE; END_IF;\nEND_PROGRAM\n"
	prog, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if prog.Name != "Washer" || prog.TopKeyword != "PROGRAM" {
		t.Fatalf("name %q top %q", prog.Name, prog.TopKeyword)
	}
	if g := FileGlobals(prog); len(g) != 1 || g[0].Name != "StartPB" {
		t.Fatalf("FileGlobals = %+v", g)
	}
	ir, err := Lower(prog)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ir.Globals["StartPB"]; !ok {
		t.Error("StartPB not bound")
	}
	// A disagreeing VAR_EXTERNAL is an error, not a second variable.
	bad, _ := Parse(strings.Replace(src, "VAR_EXTERNAL\n    StartPB : BOOL;", "VAR_EXTERNAL\n    StartPB : INT;", 1))
	if _, err := Lower(bad); err == nil || !strings.Contains(err.Error(), "disagrees with the global's declaration") {
		t.Errorf("err = %v", err)
	}
}

func TestTagTypesAndUntypedTags(t *testing.T) {
	lower := func(src string, opts LowerOpts) error {
		prog, err := Parse(src)
		if err != nil {
			t.Fatal(err)
		}
		_, err = LowerWithOpts(prog, opts)
		return err
	}
	typed := LowerOpts{ImplicitGlobals: map[string]*ir.Type{"Speed": ir.RealT}, TagTypes: map[string]*ir.Type{"Speed": ir.RealT}}
	if err := lower("PROGRAM P\nVAR_EXTERNAL\n    speed : INT;\nEND_VAR\nspeed := 1;\nEND_PROGRAM", typed); err == nil ||
		!strings.Contains(err.Error(), "disagrees with the tag Speed, whose type is REAL") {
		t.Errorf("err = %v", err)
	}
	// An inferred type (not in TagTypes) never overrides a declaration.
	inferred := LowerOpts{ImplicitGlobals: map[string]*ir.Type{"Count": ir.RealT}}
	if err := lower("PROGRAM P\nVAR_EXTERNAL\n    Count : DINT;\nEND_VAR\nCount := 1;\nEND_PROGRAM", inferred); err != nil {
		t.Error(err)
	}
	untyped := LowerOpts{ImplicitGlobals: map[string]*ir.Type{"Valve": nil}}
	if err := lower("PROGRAM P\nValve := TRUE;\nEND_PROGRAM", untyped); err == nil ||
		!strings.Contains(err.Error(), `"Valve" is a project tag with no type`) {
		t.Errorf("err = %v", err)
	}
	// A FUNCTION_BLOCK body never sees implicit tags.
	if err := lower("FUNCTION_BLOCK F\nVAR_OUTPUT\n    Q : REAL;\nEND_VAR\nQ := Speed;\nEND_FUNCTION_BLOCK\nPROGRAM P\nEND_PROGRAM", typed); err == nil ||
		!strings.Contains(err.Error(), `undeclared identifier "Speed"`) {
		t.Errorf("err = %v", err)
	}
}

func TestImplicitGlobalsBindOnUse(t *testing.T) {
	prog, _ := Parse("PROGRAM P\nA := 1.0;\nEND_PROGRAM")
	out, err := LowerWithOpts(prog, LowerOpts{ImplicitGlobals: map[string]*ir.Type{"A": ir.RealT, "B": ir.RealT}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := out.Globals["A"]; !ok {
		t.Error("A is used: bound")
	}
	if _, ok := out.Globals["B"]; ok {
		t.Error("B is never named: not bound")
	}
}

func TestResolveTypeName(t *testing.T) {
	types := map[string]*ir.Type{"Motor": {Kind: ir.TypeStruct, Struct: &ir.StructDef{Name: "Motor"}}}
	for name, kind := range map[string]ir.TypeKind{
		"INT": ir.TypeInt, "lreal": ir.TypeReal, "TIME": ir.TypeTime, "STRING": ir.TypeString,
		"motor": ir.TypeStruct, "ARRAY[1..4] OF REAL": ir.TypeArray, "WORD": ir.TypeInt,
	} {
		got, err := ResolveTypeName(name, types)
		if err != nil || got.Kind != kind {
			t.Errorf("%s → %v, %v; want %v", name, got, err, kind)
		}
	}
	for _, bad := range []string{"Widget", "TON", "", "INT;"} {
		if _, err := ResolveTypeName(bad, types); err == nil {
			t.Errorf("%q resolved", bad)
		}
	}
}
