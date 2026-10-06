package st

import (
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/ir"
)

// Identifiers are case-insensitive (IEC 61131-3, #197): every name below is
// referenced in a casing other than its declaration's, and every reference
// resolves to the declared one. The tag store sees the DECLARED spelling.
func TestLowerCaseInsensitiveIdentifiers(t *testing.T) {
	src := `
TYPE Motor : STRUCT
  Speed : REAL;
END_STRUCT
END_TYPE

FUNCTION SCALEANALOG : REAL
VAR_INPUT
  Raw : REAL;
  Lo : REAL;
END_VAR
  ScaleAnalog := RAW + lo;
END_FUNCTION

FUNCTION_BLOCK Dosing
VAR_INPUT
  Amount : REAL;
END_VAR
VAR_OUTPUT
  Done : BOOL;
END_VAR
  done := AMOUNT > 1.0;
END_FUNCTION_BLOCK

PROGRAM main
VAR_GLOBAL
  Level : REAL;
  Out : REAL;
  Flag : BOOL;
  Pump : MOTOR;
END_VAR
VAR
  Step : INT;
  State : INT := 3;
  startEdge : R_TRIG;
  doseA : DOSING;
  t1 : ton;
END_VAR
  Step := STATE;
  startedge(CLK := flag);
  DOSEA(amount := LEVEL);
  out := scaleanalog(level, 2.0) + INT_TO_REAL(step);
  IF doseA.DONE THEN pump.SPEED := 1.0; END_IF;
  T1(in := TRUE, pt := T#1s);
END_PROGRAM
`
	host := newFakeHost()
	host.vals["Level"] = ir.RealVal(5)
	prog, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	irProg, err := Lower(prog)
	if err != nil {
		t.Fatalf("lower: %v", err)
	}
	for _, name := range []string{"Level", "Out", "Flag", "Pump"} {
		if _, ok := irProg.Globals[name]; !ok {
			t.Errorf("Globals lacks the declared spelling %q: %v", name, irProg.Globals)
		}
	}
	frame := ir.NewFrame(irProg)
	host.vals["Pump"] = ir.Zero(irProg.Globals["Pump"])
	if err := ir.Run(irProg, frame, host); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := host.vals["Out"]; got.Kind != ir.TypeReal || got.F != 10 {
		t.Errorf("Out = %+v, want 10 (5 + 2 + Step 3)", got)
	}
	for k := range host.vals {
		if k != "Level" && k != "Out" && k != "Pump" && k != "Flag" {
			t.Errorf("the VM wrote %q — a folded spelling leaked into the IR", k)
		}
	}
}

// Two declarations differing only in case in ONE scope are a duplicate; the
// error names both spellings so a project that relied on case can find them.
func TestLowerCaseOnlyDuplicates(t *testing.T) {
	cases := []struct{ name, src, want string }{
		{"var", `PROGRAM p
VAR
  Level : REAL;
  LEVEL : INT;
END_VAR
END_PROGRAM`, `duplicate declaration "LEVEL": "Level" is already declared`},
		{"function", `FUNCTION F : INT
  F := 1;
END_FUNCTION
FUNCTION f : INT
  f := 2;
END_FUNCTION
PROGRAM p
END_PROGRAM`, `duplicate FUNCTION "f": "F" is already declared`},
		{"fb", `FUNCTION_BLOCK Fb
END_FUNCTION_BLOCK
FUNCTION_BLOCK FB
END_FUNCTION_BLOCK
PROGRAM p
END_PROGRAM`, `duplicate FUNCTION_BLOCK "FB": "Fb" is already declared`},
		{"type", `TYPE T1 : STRUCT a : INT; END_STRUCT
END_TYPE
TYPE t1 : STRUCT b : INT; END_STRUCT
END_TYPE
PROGRAM p
END_PROGRAM`, `duplicate TYPE "t1": "T1" is already declared`},
		{"fb pin", `FUNCTION_BLOCK Fb
VAR_INPUT
  In : BOOL;
END_VAR
VAR
  IN : BOOL;
END_VAR
END_FUNCTION_BLOCK
PROGRAM p
END_PROGRAM`, `duplicate slot "IN": "In" is already declared`},
		{"return shadow", `FUNCTION Scale : REAL
VAR_INPUT
  scale : REAL;
END_VAR
  Scale := 1.0;
END_FUNCTION
PROGRAM p
END_PROGRAM`, `shadows the return name`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prog, err := Parse(tc.src)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			_, err = Lower(prog)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

// The same name in DIFFERENT scopes (a program local and an FB local, an FB
// pin and a program variable) is not a clash, whatever the casing.
func TestLowerCaseDifferentScopesCoexist(t *testing.T) {
	src := `
FUNCTION_BLOCK Fb
VAR_INPUT
  LEVEL : REAL;
END_VAR
END_FUNCTION_BLOCK
PROGRAM p
VAR
  level : REAL;
  f : FB;
END_VAR
  f(Level := level);
END_PROGRAM`
	prog, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, err := Lower(prog); err != nil {
		t.Fatalf("lower: %v", err)
	}
}

// A user FUNCTION from the engine's registry resolves however a call site
// cases it, and a file's own POU hides a registry entry of any casing.
func TestLowerCaseRegistryLookup(t *testing.T) {
	lib, err := Parse(`FUNCTION ScaleAnalog : REAL
VAR_INPUT x : REAL; END_VAR
  ScaleAnalog := x * 2.0;
END_FUNCTION`)
	if err != nil {
		t.Fatal(err)
	}
	libIR, err := Lower(lib)
	if err != nil {
		t.Fatal(err)
	}
	funcs := map[string]*ir.FuncDef{}
	for _, f := range libIR.UserFuncs {
		funcs[f.Name] = f
	}
	prog, err := Parse(`PROGRAM p
VAR_GLOBAL y : REAL; END_VAR
  y := SCALEANALOG(1.5);
END_PROGRAM`)
	if err != nil {
		t.Fatal(err)
	}
	irProg, err := LowerWithOpts(prog, LowerOpts{UserFuncs: funcs})
	if err != nil {
		t.Fatalf("lower: %v", err)
	}
	host := newFakeHost()
	if err := ir.Run(irProg, ir.NewFrame(irProg), host); err != nil {
		t.Fatal(err)
	}
	if got := host.vals["y"]; got.F != 3 {
		t.Fatalf("y = %+v, want 3", got)
	}

	shadow := map[string]*ir.FuncDef{"F": {Name: "F"}}
	merged := mergeShadowing(shadow, map[string]*ir.FuncDef{"f": {Name: "f"}})
	if len(merged) != 1 || merged["f"] == nil {
		t.Fatalf("mergeShadowing kept both spellings: %v", merged)
	}
}
