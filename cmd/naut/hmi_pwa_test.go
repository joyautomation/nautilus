package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/joyautomation/nautilus/internal/project"
)

// The manifest's pwa: section reaches the HMI naut builds: the installed
// app's name and theme are the project's (#722).
func TestBuildHMICarriesThePWASection(t *testing.T) {
	dir := t.TempDir()
	put := func(rel, text string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	put("nautilus.yaml", "name: grain\ntasks:\n  - program: main.st\n    scan: 100ms\npwa:\n  name: Grain plant\n  short-name: Grain\n  theme-color: \"#102030\"\n")
	put("main.st", "PROGRAM main\nVAR x : INT; END_VAR\nx := 1;\nEND_PROGRAM\n")
	put("screens/main.mimic.json", `{"name":"Main","equipment":[{"id":"T1","component":"Tank","x":10,"y":10}]}`)
	proj, err := project.Load(os.DirFS(dir), "")
	if err != nil {
		t.Fatal(err)
	}
	res, err := buildHMI(proj, dir)
	if err != nil || res == nil {
		t.Fatalf("buildHMI = %v, %v", res, err)
	}
	var m struct {
		Name      string `json:"name"`
		ShortName string `json:"short_name"`
		Theme     string `json:"theme_color"`
	}
	if err := json.Unmarshal(res.Files["manifest.webmanifest"], &m); err != nil {
		t.Fatal(err)
	}
	if m.Name != "Grain plant" || m.ShortName != "Grain" || m.Theme != "#102030" {
		t.Errorf("manifest = %+v", m)
	}
}
