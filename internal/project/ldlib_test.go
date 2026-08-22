package project

import (
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/joyautomation/nautilus/runtime"
)

// A `.ld` file with no PROGRAM is a project LIBRARY: its FUNCTION_BLOCKs
// are ladder subroutines, transpiled into the prelude ahead of every task
// exactly as a PROGRAM-less `.st` file's declarations are. This is the
// composition `nautilus check`, `run`, `build` and `test` all take.

const ldLibManifest = `
name: ld-lib
tasks:
  - program: main.ld
    scan: 100ms
tags:
  - { name: Cmd,   role: setpoint, init: false }
  - { name: Halt,  role: setpoint, init: false }
  - { name: Lvl,   role: setpoint, init: 50.0 }
  - { name: Run,   role: output,   init: false }
  - { name: Scaled, role: state,   init: 0.0 }
driver:
  type: memory
`

const blocksLD = `FUNCTION_BLOCK PumpSeq
VAR_INPUT  Start : BOOL; Stop : BOOL; Level : REAL; END_VAR
VAR_OUTPUT Run : BOOL; END_VAR
LD
  RUNG seal  [ Start | Run ] /Stop GT(Level, 10.0) ( Run )
END_LD
END_FUNCTION_BLOCK`

const mainLD = `PROGRAM Main
VAR_EXTERNAL Cmd : BOOL; Halt : BOOL; Lvl : REAL; Run : BOOL; Scaled : REAL; END_VAR
VAR p1 : PumpSeq; END_VAR
LD
  RUNG call   Cmd p1:PumpSeq(Stop := Halt, Level := Lvl, Run => Run)
  RUNG scale  Run sc:Scale(IN := Lvl, OUT => Scaled)
END_LD
END_PROGRAM`

// scale.st is an ST library the ladder library and the ladder program both
// see — the tier order (.st, then .ld/.fbd) in one project.
const scaleST = `FUNCTION_BLOCK Scale
VAR_INPUT  EN : BOOL; IN : REAL; END_VAR
VAR_OUTPUT OUT : REAL; END_VAR
IF EN THEN OUT := IN * 2.0; END_IF;
END_FUNCTION_BLOCK
`

func ldLibFS() fstest.MapFS {
	return fstest.MapFS{
		"nautilus.yaml": &fstest.MapFile{Data: []byte(ldLibManifest)},
		"blocks.ld":     &fstest.MapFile{Data: []byte(blocksLD)},
		"main.ld":       &fstest.MapFile{Data: []byte(mainLD)},
		"scale.st":      &fstest.MapFile{Data: []byte(scaleST)},
	}
}

func TestLadderLibraryComposes(t *testing.T) {
	libs, err := libraries(ldLibFS())
	if err != nil {
		t.Fatalf("libraries: %v", err)
	}
	if len(libs) != 2 {
		t.Fatalf("want 2 libraries (scale.st, blocks.ld), got %d: %q", len(libs), libs)
	}
	// Tier order: the .st library leads, the transpiled .ld follows.
	if !strings.Contains(libs[0], "FUNCTION_BLOCK Scale") {
		t.Errorf("library 0 should be scale.st:\n%s", libs[0])
	}
	if !strings.Contains(libs[1], "FUNCTION_BLOCK PumpSeq") {
		t.Errorf("library 1 should be blocks.ld, transpiled:\n%s", libs[1])
	}
	// Transpiled, not raw: no LD block survives into the prelude.
	if strings.Contains(libs[1], "RUNG") || strings.Contains(libs[1], "END_LD") {
		t.Errorf("the ladder library must reach the prelude as ST:\n%s", libs[1])
	}
	if !strings.Contains(libs[1], "Run := ((Start OR Run) AND NOT (Stop) AND (Level > 10.0));") {
		t.Errorf("the block's rung did not lower:\n%s", libs[1])
	}
}

// The whole project loads, compiles, and scans: the ladder program calls a
// block from the ladder library and a block from the ST library.
func TestLadderLibraryProjectRuns(t *testing.T) {
	proj, err := Load(ldLibFS(), "")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	rt, err := runtime.New(proj.Runtime)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	tags := rt.Tags()
	tags.SetBool("Cmd", true)
	tags.SetReal("Lvl", 50)
	rt.Scan()
	if !tags.Bool("Run") {
		t.Fatal("the ladder library's block should have latched Run")
	}
	if got := tags.Real("Scaled"); got != 100 {
		t.Fatalf("Scaled = %v, want 100 (the ST library's block, gated by the rung)", got)
	}
	// The seal-in lives inside the block and survives the command dropping.
	tags.SetBool("Cmd", false)
	rt.Scan()
	if !tags.Bool("Run") {
		t.Fatal("the block's seal-in should hold Run")
	}
}

// A ladder library that does not transpile is an error at load, not a
// silently missing block — `nautilus check` and `run` take this path.
func TestLadderLibraryErrorSurfaces(t *testing.T) {
	fs := ldLibFS()
	fs["blocks.ld"] = &fstest.MapFile{Data: []byte(`FUNCTION_BLOCK Broken
VAR_INPUT Start : BOOL; END_VAR
LD
  RUNG bad  Start [ OnlyOneLeg ] ( Start )
END_LD
END_FUNCTION_BLOCK`)}
	if _, err := Load(fs, ""); err == nil || !strings.Contains(err.Error(), "blocks.ld") {
		t.Fatalf("err = %v, want one naming blocks.ld", err)
	}
}

// The shipped example is the end-to-end proof: two instances of one
// ladder block, each with its own retained state.
func TestLadderSubroutinesExampleLoads(t *testing.T) {
	proj, err := Load(os.DirFS("../../examples/ladder-subroutines"), "")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	rt, err := runtime.New(proj.Runtime)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	tags := rt.Tags()
	tags.SetBool("P101Start", true)
	rt.Scan()
	if !tags.Bool("P101Run") || tags.Bool("P102Run") {
		t.Fatal("only the instance that was started should run")
	}
}
