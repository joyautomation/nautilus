package stproject

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const typesSrc = `TYPE
  Header_Type : STRUCT
    Displacement : REAL;
    Valid : BOOL;
  END_STRUCT;
END_TYPE
`

const programSrc = `PROGRAM Main
VAR_EXTERNAL
  H : Header_Type;
  Out1 : REAL;
END_VAR
Out1 := H.Displacement;
END_PROGRAM
`

func TestPreludeGathersLibrarySiblings(t *testing.T) {
	dir := t.TempDir()
	prog := filepath.Join(dir, "program.st")
	types := filepath.Join(dir, "eip_types.st")
	other := filepath.Join(dir, "other_program.st")
	broken := filepath.Join(dir, "broken.st")
	for p, src := range map[string]string{
		prog:   programSrc,
		types:  typesSrc,
		other:  "PROGRAM Other\nEND_PROGRAM\n", // programs never join the prelude
		broken: "TYPE oops",                    // unparsable files are skipped
	} {
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	prelude, lines := Prelude(prog, nil)
	if !strings.Contains(prelude, "Header_Type") {
		t.Fatalf("prelude missing types file:\n%s", prelude)
	}
	if strings.Contains(prelude, "PROGRAM") || strings.Contains(prelude, "oops") {
		t.Fatalf("prelude includes non-library sources:\n%s", prelude)
	}
	if lines != strings.Count(prelude, "\n") {
		t.Fatalf("line count %d != actual %d", lines, strings.Count(prelude, "\n"))
	}
}

func TestPreludeOverridePrefersBuffer(t *testing.T) {
	dir := t.TempDir()
	prog := filepath.Join(dir, "program.st")
	types := filepath.Join(dir, "types.st")
	if err := os.WriteFile(prog, []byte(programSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(types, []byte(typesSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	edited := strings.ReplaceAll(typesSrc, "Header_Type", "Edited_Type")
	prelude, _ := Prelude(prog, map[string]string{types: edited})
	if !strings.Contains(prelude, "Edited_Type") || strings.Contains(prelude, "Header_Type") {
		t.Fatalf("override not honored:\n%s", prelude)
	}
}

func TestIsLibrary(t *testing.T) {
	if !IsLibrary(typesSrc) {
		t.Error("types-only file should be a library")
	}
	if IsLibrary(programSrc) {
		t.Error("program file must not be a library")
	}
	if IsLibrary("TYPE broken") {
		t.Error("unparsable file must not be a library")
	}
}

func TestJoinSplitRoundTrip(t *testing.T) {
	libs := []string{typesSrc, "TYPE Extra : STRUCT\n  A : INT;\nEND_STRUCT;\nEND_TYPE\n"}
	composed := Join(libs, programSrc)
	prelude := Join(libs, "")
	prog, ok := SplitProgram(composed, prelude)
	if !ok || prog != programSrc {
		t.Fatalf("round trip: ok=%v, program mismatch:\n%q", ok, prog)
	}
	// A library file that lacks a trailing newline still round-trips: Join
	// adds the separator, and SplitProgram tolerates the trimmed prelude.
	noNL := []string{strings.TrimRight(typesSrc, "\n")}
	if prog, ok := SplitProgram(Join(noNL, programSrc), Join(noNL, "")); !ok || prog != programSrc {
		t.Fatalf("no-trailing-newline library split failed: ok=%v %q", ok, prog)
	}
	// Libraries that don't match yield ok=false rather than a wrong split.
	if _, ok := SplitProgram(composed, Join([]string{"TYPE Other : STRUCT\n  Z : INT;\nEND_STRUCT;\nEND_TYPE\n"}, "")); ok {
		t.Fatal("mismatched prelude should not split")
	}
}

func TestComposeDecomposesProject(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "eip_types.st", typesSrc)
	writeFile(t, dir, "program.st", programSrc)

	comp, err := Compose(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if comp.ProgramFile != "program.st" || comp.ProgramBody != programSrc {
		t.Fatalf("program file = %q", comp.ProgramFile)
	}
	// Compose then split recovers the program exactly.
	prog, ok := SplitProgram(comp.Composed, comp.Prelude)
	if !ok || prog != programSrc {
		t.Fatalf("compose→split mismatch: ok=%v %q", ok, prog)
	}
	// Overrides win over disk.
	edited := strings.ReplaceAll(programSrc, "H.Displacement", "H.Displacement * 2.0")
	comp2, err := Compose(dir, map[string]string{"program.st": edited})
	if err != nil {
		t.Fatal(err)
	}
	if comp2.ProgramBody != edited {
		t.Fatal("override not applied")
	}
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestComposeFBDProgram(t *testing.T) {
	// A .fbd file holding the PROGRAM composes like an .st program: the ST
	// libraries form the prelude, the netlist is the program body — the same
	// shape the runtime boots and serves over /api/program.
	fbdSrc := `PROGRAM Main
VAR_EXTERNAL X : BOOL; Y : BOOL; END_VAR
FBD
  Y := NOT X
END_FBD
END_PROGRAM
`
	dir := t.TempDir()
	writeFile(t, dir, "eip_types.st", typesSrc)
	writeFile(t, dir, "program.fbd", fbdSrc)

	comp, err := Compose(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if comp.ProgramFile != "program.fbd" || comp.ProgramBody != fbdSrc {
		t.Fatalf("program file = %q", comp.ProgramFile)
	}
	prog, ok := SplitProgram(comp.Composed, comp.Prelude)
	if !ok || prog != fbdSrc {
		t.Fatalf("compose→split mismatch: ok=%v", ok)
	}
}
