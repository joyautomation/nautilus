package project

import (
	"os"
	"path/filepath"
	"testing"
)

// TagDefsForErr with a manifest name (`naut check -m field.yaml`): the
// nearest directory holding that manifest OR nautilus.yaml is the project,
// read through the named one when it has it. A nested project with only a
// nautilus.yaml is its own project, not the parent's field.yaml (#518).
func TestTagDefsForErrNamedManifestAndNestedProjects(t *testing.T) {
	dir := t.TempDir()
	head := "name: p\ntasks:\n  - program: main.st\n    scan: 100ms\ntags:\n"
	files := map[string]string{
		"nautilus.yaml":        head + "  - { name: Default, role: state, type: BOOL }\n",
		"field.yaml":           head + "  - { name: Field, role: state, type: BOOL }\n",
		"main.st":              "PROGRAM Main\nEND_PROGRAM\n",
		"lib/util.st":          "FUNCTION F : BOOL\nF := TRUE;\nEND_FUNCTION\n",
		"nested/nautilus.yaml": head + "  - { name: Nested, role: state, type: BOOL }\n",
		"nested/main.st":       "PROGRAM Main\nEND_PROGRAM\n",
	}
	for n, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(n))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct{ file, mname, wantDir, wantName, wantTag string }{
		{"main.st", "", ".", ManifestName, "Default"},
		{"main.st", "field.yaml", ".", "field.yaml", "Field"},
		{"lib/util.st", "field.yaml", ".", "field.yaml", "Field"},
		{"nested/main.st", "field.yaml", "nested", ManifestName, "Nested"},
	} {
		defs, mdir, name, err := TagDefsForErr(filepath.Join(dir, filepath.FromSlash(tc.file)), tc.mname)
		if err != nil {
			t.Fatalf("%s -m %q: %v", tc.file, tc.mname, err)
		}
		wantDir := filepath.Join(dir, tc.wantDir)
		if mdir != wantDir || name != tc.wantName || len(defs) != 1 || defs[0].Name != tc.wantTag {
			t.Errorf("%s -m %q = %s/%s %v, want %s/%s with %s", tc.file, tc.mname, mdir, name, defs, wantDir, tc.wantName, tc.wantTag)
		}
	}
}
