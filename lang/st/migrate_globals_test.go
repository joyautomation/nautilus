package st

import (
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/ir"
)

func TestMigrateProgramGlobals_AddsVarExternalForReferencedGlobals(t *testing.T) {
	src := `PROGRAM main
VAR
  local_v : INT;
END_VAR

  local_v := tank_level + 1;
  pump_on := tank_level > 50;

END_PROGRAM
`
	globals := map[string]*ir.Type{
		"tank_level": ir.IntT,
		"pump_on":    ir.BoolT,
		"unused":     ir.RealT,
	}
	out, added, err := MigrateProgramGlobals(src, globals)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if len(added) != 2 {
		t.Fatalf("expected 2 added, got %d: %v", len(added), added)
	}
	if added[0] != "pump_on" || added[1] != "tank_level" {
		t.Fatalf("unexpected added order: %v", added)
	}
	if !strings.Contains(out, "VAR_EXTERNAL\n  pump_on : BOOL;\n  tank_level : INT;\nEND_VAR") {
		t.Fatalf("expected VAR_EXTERNAL block in output, got:\n%s", out)
	}
	if !strings.Contains(out, "unused") == false {
		// We don't want unused to appear since the source doesn't reference it.
		if strings.Contains(out, "unused") {
			t.Fatalf("unused global should not appear in output")
		}
	}
}

func TestMigrateProgramGlobals_SkipsAlreadyDeclared(t *testing.T) {
	src := `PROGRAM main
VAR_EXTERNAL
  tank_level : INT;
END_VAR

  tank_level := tank_level + 1;

END_PROGRAM
`
	globals := map[string]*ir.Type{"tank_level": ir.IntT}
	out, added, err := MigrateProgramGlobals(src, globals)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if len(added) != 0 {
		t.Fatalf("expected no additions, got %v", added)
	}
	if out != src {
		t.Fatalf("source should be unchanged when nothing missing")
	}
}

func TestMigrateProgramGlobals_SkipsFunctionBlockFiles(t *testing.T) {
	src := `FUNCTION_BLOCK ramp
VAR_INPUT
  rate : REAL;
END_VAR
VAR
  step : REAL;
END_VAR

  step := step + rate;

END_FUNCTION_BLOCK
`
	globals := map[string]*ir.Type{"step": ir.RealT, "rate": ir.RealT}
	out, added, err := MigrateProgramGlobals(src, globals)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if len(added) != 0 || out != src {
		t.Fatalf("FB file should be untouched; added=%v", added)
	}
}

func TestMigrateProgramGlobals_SkipsFBTypeGlobals(t *testing.T) {
	src := `PROGRAM main
VAR
END_VAR

  motor_timer(IN := TRUE, PT := T#5s);

END_PROGRAM
`
	// FB-typed global — should NOT be added to VAR_EXTERNAL.
	fbType := &ir.Type{Kind: ir.TypeFB}
	globals := map[string]*ir.Type{"motor_timer": fbType}
	out, added, err := MigrateProgramGlobals(src, globals)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if len(added) != 0 {
		t.Fatalf("FB-typed global should be skipped, got %v", added)
	}
	if out != src {
		t.Fatalf("source should be unchanged")
	}
}

func TestMigrateProgramGlobals_BareStatementsLibrary(t *testing.T) {
	src := `tank_level := tank_level + 1;
`
	globals := map[string]*ir.Type{"tank_level": ir.IntT}
	out, added, err := MigrateProgramGlobals(src, globals)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if len(added) != 1 || added[0] != "tank_level" {
		t.Fatalf("expected tank_level added, got %v", added)
	}
	if !strings.HasPrefix(out, "VAR_EXTERNAL\n  tank_level : INT;\nEND_VAR\n") {
		t.Fatalf("expected VAR_EXTERNAL prepended, got:\n%s", out)
	}
}
