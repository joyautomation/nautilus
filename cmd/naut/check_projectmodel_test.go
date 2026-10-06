package main

// naut check's output for the parity batch's project model, as golden
// text: implicit manifest tags (#177/#210), a GVL file (#175), elementary
// tag types (#200), and a library error reported once, where it is (#199).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// checkGolden writes files into a temp project, runs `naut check .` from
// inside it (so paths print project-relative), and returns stdout + code.
func checkGolden(t *testing.T, files map[string]string) (string, int) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(dir)
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	code := runCheck([]string{"."})
	w.Close()
	os.Stdout = old
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	r.Close()
	return sb.String(), code
}

func assertGolden(t *testing.T, got string, code, wantCode int, want string) {
	t.Helper()
	if strings.TrimSpace(got) != strings.TrimSpace(want) || code != wantCode {
		t.Errorf("naut check (exit %d, want %d):\n--- got\n%s\n--- want\n%s", code, wantCode, got, want)
	}
}

// #177/#210: tags with no VAR_EXTERNAL anywhere check clean.
func TestCheckGoldenImplicitTags(t *testing.T) {
	out, code := checkGolden(t, map[string]string{
		"nautilus.yaml": `tasks:
  - program: program.st
tags:
  - { name: Sensor,   role: input, init: 0.0 }
  - { name: Setpoint, role: setpoint, init: 50.0 }
  - { name: Actuator, role: output, type: REAL }
`,
		"program.st": `PROGRAM Main
VAR
    err : REAL;
END_VAR
err := Setpoint - Sensor;
Actuator := LIMIT(0.0, err, 100.0);
END_PROGRAM
`,
	})
	assertGolden(t, out, code, 0, `naut check: 1 file(s), 0 with errors`)
}

// A local named like a tag shadows it: a warning on the declaration.
func TestCheckGoldenShadowWarning(t *testing.T) {
	out, code := checkGolden(t, map[string]string{
		"nautilus.yaml": "tasks:\n  - program: program.st\ntags:\n  - { name: Level, role: state, init: 0.0 }\n  - { name: Out, role: output, type: REAL }\n",
		"program.st":    "PROGRAM Main\nVAR\n    Level : REAL;\nEND_VAR\nLevel := 1.0;\nOut := Level;\nEND_PROGRAM\n",
	})
	assertGolden(t, out, code, 0, `program.st:3:5: warning: local Level shadows the project tag Level — this program reads and writes its own Level, not the tag; rename it, or drop the declaration to use the tag
.: warning: nautilus.yaml declares state "Level", which no program binds — dead, or a stale generated entry
naut check: 1 file(s), 0 with errors, 2 warning(s)`)
}

// An untyped tag says what to do, at the use.
func TestCheckGoldenUntypedTag(t *testing.T) {
	out, code := checkGolden(t, map[string]string{
		"nautilus.yaml": "tasks:\n  - program: program.st\ntags:\n  - { name: Valve, role: output }\n",
		"program.st":    "PROGRAM Main\nValve := TRUE;\nEND_PROGRAM\n",
	})
	assertGolden(t, out, code, 1, `program.st:2:1: "Valve" is a project tag with no type — give Valve a type: (or an init:) in the manifest, or declare it in VAR_EXTERNAL
naut check: 1 file(s), 1 with errors`)
}

// #175: a GVL file plus two programs: clean, and its globals are tags.
func TestCheckGoldenGVL(t *testing.T) {
	out, code := checkGolden(t, map[string]string{
		"nautilus.yaml": "tasks:\n  - program: a.st\n  - name: b\n    program: b.st\ntags: []\n",
		"gvl.st":        "VAR_GLOBAL\n    StartPB : BOOL;\n    Count : DINT;\nEND_VAR\n",
		"a.st":          "PROGRAM A\nIF StartPB THEN Count := Count + 1; END_IF;\nEND_PROGRAM\n",
		"b.st":          "PROGRAM B\nVAR\n    c : DINT;\nEND_VAR\nc := Count;\nEND_PROGRAM\n",
	})
	assertGolden(t, out, code, 0, `naut check: 3 file(s), 0 with errors`)
}

// #200: type: INT is an INT tag; an init: that disagrees names the type.
func TestCheckGoldenElementaryType(t *testing.T) {
	files := map[string]string{
		"nautilus.yaml": "tasks:\n  - program: program.st\ntags:\n  - { name: FT101_Raw, role: input, type: INT, init: 0 }\n  - { name: Scaled, role: output, type: REAL }\n",
		"program.st":    "PROGRAM Main\nScaled := INT_TO_REAL(FT101_Raw) / 10.0;\nEND_PROGRAM\n",
	}
	out, code := checkGolden(t, files)
	assertGolden(t, out, code, 0, `naut check: 1 file(s), 0 with errors`)

	files["nautilus.yaml"] = strings.Replace(files["nautilus.yaml"], "init: 0 }", "init: 1.5 }", 1)
	out, code = checkGolden(t, files)
	if code != 1 || !strings.Contains(out, "tag FT101_Raw (type INT): init: want INT, got a number") {
		t.Errorf("exit %d:\n%s", code, out)
	}
}

// #199: one error in a library is reported once, on its own line — not
// again at 1:1 on every file that composes it.
func TestCheckGoldenLibraryErrorOnce(t *testing.T) {
	out, code := checkGolden(t, map[string]string{
		"nautilus.yaml": "tasks:\n  - program: main.st\ntags:\n  - { name: RecipeA, role: setpoint, type: DoseRecipe }\n",
		"types.st":      "TYPE\n  DoseRecipe : STRUCT\n    TargetL : REAL;\n  END_STRUCT;\nEND_TYPE\n",
		"dosing.st": `FUNCTION_BLOCK Dosing
VAR_INPUT
    Recipe : DoseRecipe;
END_VAR
VAR_OUTPUT
    Target : REAL;
END_VAR
Target := Recipe.TargetLL;
END_FUNCTION_BLOCK
`,
		"main.st": "PROGRAM Main\nVAR\n    d : Dosing;\nEND_VAR\nd(Recipe := RecipeA);\nEND_PROGRAM\n",
	})
	assertGolden(t, out, code, 1, `dosing.st:8:18: field "TargetLL" not found on DoseRecipe
naut check: 3 file(s), 1 with errors`)
}

// A library error that only shows once composed (here: the program's own
// file is not among the checked ones) is still reported at the library.
func TestCheckLibraryErrorAttributedWhenLibraryNotChecked(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("lib.st", "FUNCTION_BLOCK Bad\nVAR_OUTPUT\n    Q : REAL;\nEND_VAR\nQ := Nope;\nEND_FUNCTION_BLOCK\n")
	write("main.st", "PROGRAM Main\nEND_PROGRAM\n")
	t.Chdir(dir)
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	code := runCheck([]string{"main.st"})
	w.Close()
	os.Stdout = old
	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	got := string(buf[:n])
	if code != 1 || !strings.Contains(got, "lib.st:5:") || strings.Contains(got, "main.st:") {
		t.Errorf("exit %d:\n%s", code, got)
	}
}
