package project

import (
	"testing"
	"testing/fstest"

	"github.com/joyautomation/nautilus/runtime"
)

// A library FUNCTION_BLOCK's own VAR_EXTERNAL binds the tag store through
// whichever program instantiates it — it compiles and runs correctly (the
// VM resolves the reference through the instance, exactly like a program's
// own VAR_EXTERNAL). Before this fix, rt.Globals()/rt.GlobalUses() only
// walked a PROGRAM POU's own VAR_EXTERNAL block, so `nautilus check`
// reported the tag as "declared, no program binds" — a false positive that,
// on a real transpile with a lot of library blocks, forced re-declaring
// every one of them in the calling program (see the AEP transpile:
// sites/aep/README.md, "A library FUNCTION_BLOCK's VAR_EXTERNAL binding a
// tag"). rt.Globals()/rt.GlobalUses() must see through the whole instance
// tree a program reaches, nested FB-in-FB and ladder libraries included.

const fbExternManifest = `
name: fb-extern
tasks:
  - program: program.st
    scan: 100ms
tags:
  - { name: Setpoint, role: setpoint, init: 0.0 }
driver:
  type: memory
`

const fbExternWrapperST = `FUNCTION_BLOCK Wrapper
VAR_EXTERNAL
    Setpoint : REAL;
END_VAR
VAR_OUTPUT
    Q : REAL;
END_VAR
    Q := Setpoint;
END_FUNCTION_BLOCK
`

func fbExternFS(programBody string) fstest.MapFS {
	return fstest.MapFS{
		"nautilus.yaml": &fstest.MapFile{Data: []byte(fbExternManifest)},
		"blocks.st":     &fstest.MapFile{Data: []byte(fbExternWrapperST)},
		"program.st":    &fstest.MapFile{Data: []byte(programBody)},
	}
}

// The instantiating program.
const fbExternProgramInstantiated = `PROGRAM Main
VAR
    w : Wrapper;
END_VAR
w();
END_PROGRAM`

// A program that never instantiates Wrapper at all.
const fbExternProgramNotInstantiated = `PROGRAM Main
;
END_PROGRAM`

func TestGlobalsSeesTagBoundThroughInstantiatedLibraryFB(t *testing.T) {
	proj, err := Load(fbExternFS(fbExternProgramInstantiated), "")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	rt, err := runtime.New(proj.Runtime)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	globals := rt.Globals()
	if _, ok := globals["Setpoint"]; !ok {
		t.Fatalf("Setpoint must be bound via the Wrapper instance; got globals %v", globals)
	}
	// The FB body reads Setpoint (Q := Setpoint), so it must show up as a
	// read, not just a bare presence in Globals.
	uses := rt.GlobalUses()
	if !uses.Read["Setpoint"] {
		t.Errorf("Setpoint should be reported as read through the FB instance: %+v", uses)
	}

	// And it actually works at runtime, exactly like the AEP transpile
	// found: the tag is read through the instance.
	rt.Tags().SetReal("Setpoint", 42.0)
	rt.Scan()
}

func TestGlobalsExcludesTagFromUninstantiatedLibraryFB(t *testing.T) {
	proj, err := Load(fbExternFS(fbExternProgramNotInstantiated), "")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	rt, err := runtime.New(proj.Runtime)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	globals := rt.Globals()
	if _, ok := globals["Setpoint"]; ok {
		t.Fatalf("Wrapper is never instantiated; Setpoint must not appear bound: %v", globals)
	}
}

// FB-in-FB: Outer instantiates Inner, which declares the VAR_EXTERNAL.
// GlobalsDeep must walk the whole instance tree, not just a program's
// direct instances.
func TestGlobalsSeesTagBoundThroughNestedLibraryFB(t *testing.T) {
	fs := fstest.MapFS{
		"nautilus.yaml": &fstest.MapFile{Data: []byte(fbExternManifest)},
		"blocks.st": &fstest.MapFile{Data: []byte(`FUNCTION_BLOCK Inner
VAR_EXTERNAL
    Setpoint : REAL;
END_VAR
VAR_OUTPUT
    Q : REAL;
END_VAR
    Q := Setpoint;
END_FUNCTION_BLOCK

FUNCTION_BLOCK Outer
VAR
    inner : Inner;
END_VAR
VAR_OUTPUT
    Q : REAL;
END_VAR
    inner();
    Q := inner.Q;
END_FUNCTION_BLOCK
`)},
		"program.st": &fstest.MapFile{Data: []byte(`PROGRAM Main
VAR
    o : Outer;
END_VAR
o();
END_PROGRAM`)},
	}
	proj, err := Load(fs, "")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	rt, err := runtime.New(proj.Runtime)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	globals := rt.Globals()
	if _, ok := globals["Setpoint"]; !ok {
		t.Fatalf("Setpoint must be bound via Outer -> Inner: %v", globals)
	}
	uses := rt.GlobalUses()
	if !uses.Read["Setpoint"] {
		t.Errorf("Setpoint should be reported as read through the nested FB instance: %+v", uses)
	}
}

// The ladder-diagram shape: a PROGRAM-less .ld file is a project library
// exactly like a .st one, and a FUNCTION_BLOCK written as rungs declares
// VAR_EXTERNAL the same way an ST one does (examples/ladder-subroutines is
// the shipped proof of the library mechanism itself; this exercises the
// VAR_EXTERNAL binding specifically).
func TestGlobalsSeesTagBoundThroughLadderLibraryFB(t *testing.T) {
	fs := fstest.MapFS{
		"nautilus.yaml": &fstest.MapFile{Data: []byte(fbExternManifest)},
		"blocks.ld": &fstest.MapFile{Data: []byte(`FUNCTION_BLOCK LdWrapper
VAR_EXTERNAL
    Setpoint : BOOL;
END_VAR
VAR_OUTPUT
    Q : BOOL;
END_VAR
LD
  RUNG r1
    Setpoint ( Q )
END_LD
END_FUNCTION_BLOCK
`)},
		"program.ld": &fstest.MapFile{Data: []byte(`PROGRAM Main
VAR
    w : LdWrapper;
END_VAR
LD
  RUNG call
    w:LdWrapper()
END_LD
END_PROGRAM`)},
	}
	manifest := `
name: fb-extern-ld
tasks:
  - program: program.ld
    scan: 100ms
tags:
  - { name: Setpoint, role: setpoint, init: false }
driver:
  type: memory
`
	fs["nautilus.yaml"] = &fstest.MapFile{Data: []byte(manifest)}
	proj, err := Load(fs, "")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	rt, err := runtime.New(proj.Runtime)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	globals := rt.Globals()
	if _, ok := globals["Setpoint"]; !ok {
		t.Fatalf("Setpoint must be bound via the ladder library's LdWrapper instance: %v", globals)
	}
}
