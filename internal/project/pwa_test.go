package project

import (
	"strings"
	"testing"
	"testing/fstest"
)

// The pwa: section loads, reads its icon from the project, and refuses what
// would only fail at install time, each refusal naming the field and the fix
// (#722).
func TestLoadPWASection(t *testing.T) {
	const prog = "PROGRAM main\nVAR x : INT; END_VAR\nx := 1;\nEND_PROGRAM\n"
	load := func(pwa string, extra map[string]string) (*Project, error) {
		fsys := fstest.MapFS{
			"nautilus.yaml": {Data: []byte("name: p\ntasks:\n  - program: main.st\n    scan: 100ms\n" + pwa)},
			"main.st":       {Data: []byte(prog)},
		}
		for k, v := range extra {
			fsys[k] = &fstest.MapFile{Data: []byte(v)}
		}
		return Load(fsys, "")
	}
	p, err := load("pwa:\n  name: Grain plant\n  short-name: Grain\n  theme-color: \"#102030\"\n  icon: brand/icon.png\n", map[string]string{"brand/icon.png": "PNGBYTES"})
	if err != nil {
		t.Fatal(err)
	}
	if p.PWA == nil || p.PWA.Name != "Grain plant" || p.PWA.ShortName != "Grain" || p.PWA.ThemeColor != "#102030" {
		t.Errorf("PWA = %+v", p.PWA)
	}
	if string(p.PWAIcon) != "PNGBYTES" {
		t.Errorf("PWAIcon = %q, want the file's bytes", p.PWAIcon)
	}
	if p, err := load("", nil); err != nil || p.PWA != nil || p.PWAIcon != nil {
		t.Errorf("no section: %+v, %v", p, err)
	}
	for name, c := range map[string]struct {
		pwa   string
		extra map[string]string
		want  string
	}{
		"bad color":    {"pwa:\n  theme-color: red\n", nil, "pwa.theme-color"},
		"missing icon": {"pwa:\n  icon: nope.png\n", nil, "no nope.png in the project"},
		"escaping":     {"pwa:\n  icon: ../icon.png\n", nil, "outside the project"},
		"unknown key":  {"pwa:\n  colour: x\n", nil, "colour"},
		"huge icon":    {"pwa:\n  icon: big.png\n", map[string]string{"big.png": strings.Repeat("x", maxPWAIcon+1)}, "over 4 MB"},
	} {
		if _, err := load(c.pwa, c.extra); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
	}
}
