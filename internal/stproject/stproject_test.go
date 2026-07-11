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
