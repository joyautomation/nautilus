package st

import (
	"testing"

	"github.com/joyautomation/nautilus/lang/ir"
)

// The Codesys GVL of constants from #176, as its own library file joined
// ahead of the program the way stproject.Join composes a project.
const gvlConstants = `
VAR_GLOBAL CONSTANT
    tMaxFill : TIME := T#60S;
    ST_IDLE  : INT := 0;
    ST_FILL  : INT := 1;
    N_TANKS  : INT := 3;
    LIMIT_HI : REAL := 2 * 40.5;
END_VAR
`

// #176: VAR_GLOBAL CONSTANT in a library declares constants every POU sees:
// the program, a FUNCTION_BLOCK and a FUNCTION, in a CASE label, an array
// bound and an initial value. They are not tags.
func TestGlobalConstantsVisibleEverywhere(t *testing.T) {
	src := gvlConstants + `
FUNCTION_BLOCK Filler
VAR_INPUT state : INT; END_VAR
VAR_OUTPUT limit : TIME; END_VAR
IF state = ST_FILL THEN limit := tMaxFill; END_IF;
END_FUNCTION_BLOCK

FUNCTION Tanks : INT
VAR_INPUT k : INT; END_VAR
Tanks := N_TANKS * k;
END_FUNCTION

PROGRAM Washer
VAR_EXTERNAL St : INT; Lim : TIME; Count : INT; Hi : REAL; END_VAR
VAR f : Filler; levels : ARRAY[1..N_TANKS] OF INT; start : INT := ST_FILL + 10; END_VAR
CASE St OF
  ST_IDLE: St := ST_FILL;
  ST_FILL: St := start;
END_CASE;
f(state := ST_FILL);
Lim := f.limit;
Count := Tanks(2) + levels[N_TANKS];
Hi := LIMIT_HI;
END_PROGRAM`
	ast, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if ast.Name != "Washer" {
		t.Errorf("program name = %q: the constants block must not swallow the PROGRAM", ast.Name)
	}
	prog, err := Lower(ast)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"tMaxFill", "ST_IDLE", "N_TANKS"} {
		if _, isTag := prog.Globals[name]; isTag {
			t.Errorf("%s became a tag; a constant is not one", name)
		}
	}
	h := newStubHost()
	h.globals["St"] = ir.IntVal(0)
	frame := ir.NewFrame(prog)
	for i := 0; i < 2; i++ {
		if err := ir.Run(prog, frame, h); err != nil {
			t.Fatal(err)
		}
	}
	if got := h.globals["St"].I; got != 11 {
		t.Errorf("St = %d, want 11", got)
	}
	if got := h.globals["Lim"].I; got != 60000 {
		t.Errorf("Lim = %d ms, want 60000", got)
	}
	if got := h.globals["Count"].I; got != 6 {
		t.Errorf("Count = %d, want 6", got)
	}
	if got := h.globals["Hi"].F; got != 81 {
		t.Errorf("Hi = %v, want 81", got)
	}
}

// The library file on its own (what the LSP and naut check see) compiles.
func TestGlobalConstantsLibraryAlone(t *testing.T) {
	ast, err := Parse(gvlConstants)
	if err != nil {
		t.Fatal(err)
	}
	if len(ast.GlobalConsts) != 1 || len(ast.VarBlocks) != 0 {
		t.Fatalf("GlobalConsts = %d, VarBlocks = %d; want 1, 0", len(ast.GlobalConsts), len(ast.VarBlocks))
	}
	if _, err := Lower(ast); err != nil {
		t.Fatal(err)
	}
}

func TestGlobalConstantCannotBeWritten(t *testing.T) {
	lowerExpectErr(t, gvlConstants+"PROGRAM P\nST_FILL := 2;\nEND_PROGRAM\n",
		"ST_FILL is a constant (VAR_GLOBAL CONSTANT) and cannot be written")
	lowerExpectErr(t, gvlConstants+"FUNCTION_BLOCK F\nN_TANKS := 2;\nEND_FUNCTION_BLOCK\n",
		"N_TANKS is a constant (VAR_GLOBAL CONSTANT) and cannot be written")
}

// A POU's own declaration shadows a project constant of the same name.
func TestLocalShadowsGlobalConstant(t *testing.T) {
	src := gvlConstants + `
PROGRAM P
VAR_EXTERNAL Out : INT; END_VAR
VAR N_TANKS : INT := 7; END_VAR
N_TANKS := N_TANKS + 1;
Out := N_TANKS;
END_PROGRAM`
	h, _, _ := scanN(t, src, 1, map[string]ir.Value{"Out": ir.IntVal(0)})
	if got := h.globals["Out"].I; got != 8 {
		t.Errorf("Out = %d, want 8", got)
	}
}

// A plain VAR_GLOBAL (tags) is untouched: an initial value there is still
// the "give it an init: in the manifest" error, and a constant needs an
// elementary or enumerated type.
func TestGlobalConstantRules(t *testing.T) {
	lowerExpectErr(t, "PROGRAM P\nVAR_GLOBAL g : INT := 3; END_VAR\nEND_PROGRAM\n", "an initial value is not applied to a tag")
	lowerExpectErr(t, "VAR_GLOBAL CONSTANT a : ARRAY[1..2] OF INT; END_VAR\n", "a constant must have an elementary or enumerated type")
	lowerExpectErr(t, "VAR_GLOBAL CONSTANT a : INT := b; END_VAR\n", `undeclared identifier "b"`)
}
