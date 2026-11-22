package project

import (
	"testing"
	"testing/fstest"
)

// What project.Load leaves for naut's own HMI build (internal/hmibuild):
// the app server.hmi names, the app behind a build that isn't there, the
// default app from mimics, or an HMI a built controller carries already.
func TestLoadFindsTheHMINautBuilds(t *testing.T) {
	const prog = "PROGRAM main\nVAR x : INT; END_VAR\nx := 1;\nEND_PROGRAM\n"
	manifest := func(hmi string) string {
		m := "name: p\ntasks:\n  - program: main.st\n    scan: 100ms\n"
		if hmi != "" {
			m += "server:\n  hmi: " + hmi + "\n"
		}
		return m
	}
	file := func(s string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(s)} }
	cases := map[string]struct {
		fs      fstest.MapFS
		app     string
		screens bool
		served  bool
		missing bool
	}{
		"server.hmi names the app": {fs: fstest.MapFS{
			"nautilus.yaml": file(manifest("hmi")), "main.st": file(prog),
			"hmi/src/routes/+page.svelte": file("<p></p>"),
		}, app: "hmi"},
		"server.hmi names its build, not built yet": {fs: fstest.MapFS{
			"nautilus.yaml": file(manifest("hmi/build")), "main.st": file(prog),
			"hmi/src/routes/+page.svelte": file("<p></p>"),
		}, app: "hmi"},
		"server.hmi names a build that is there": {fs: fstest.MapFS{
			"nautilus.yaml": file(manifest("hmi/build")), "main.st": file(prog),
			"hmi/src/routes/+page.svelte": file("<p></p>"), "hmi/build/index.html": file("<p></p>"),
		}, served: true},
		"server.hmi names a build of something else, not there": {fs: fstest.MapFS{
			"nautilus.yaml": file(manifest("web/out")), "main.st": file(prog),
		}, missing: true},
		"mimics and no server.hmi": {fs: fstest.MapFS{
			"nautilus.yaml": file(manifest("")), "main.st": file(prog),
			"screens/gallery.mimic.json": file("{}"),
		}, screens: true},
		"mimics only under node_modules": {fs: fstest.MapFS{
			"nautilus.yaml": file(manifest("")), "main.st": file(prog),
			"node_modules/kit/x.mimic.json": file("{}"),
		}},
		"a built controller carries its HMI": {fs: fstest.MapFS{
			"nautilus.yaml": file(manifest("hmi")), "main.st": file(prog),
			"hmi/src/routes/+page.svelte": file("<p></p>"), "screens/a.mimic.json": file("{}"),
			BuiltHMIDir + "/index.html": file("<p></p>"),
		}, served: true},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			p, err := Load(c.fs, ManifestName)
			if err != nil {
				t.Fatal(err)
			}
			if p.HMIApp != c.app || p.HMIScreens != c.screens || (p.Server.HMI != nil) != c.served || p.HMIMissing != c.missing {
				t.Errorf("HMIApp=%q HMIScreens=%v served=%v missing=%v; want %q %v %v %v",
					p.HMIApp, p.HMIScreens, p.Server.HMI != nil, p.HMIMissing, c.app, c.screens, c.served, c.missing)
			}
		})
	}
}

// A plant.yaml with equipment is enough for the built-in HMI's asset and
// area pages; an empty plant, a mimic, or an app of its own is not it.
func TestPlantAloneMakesTheBuiltInHMI(t *testing.T) {
	file := func(s string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(s)} }
	const prog = "PROGRAM main\nEND_PROGRAM\n"
	man := func(extra string) *fstest.MapFile {
		return file("name: p\nplant: plant.yaml\ntasks:\n  - program: main.st\n    scan: 100ms\ntags:\n  - { name: P1, type: BOOL, role: state }\n" + extra)
	}
	const plantEq = "areas: [{ id: A }]\nequipment:\n  - { id: P1, type: Pump, area: A, tag: P1 }\n"
	for name, c := range map[string]struct {
		fs      fstest.MapFS
		plant   bool
		screens bool
	}{
		"equipment, no mimic":    {fs: fstest.MapFS{"nautilus.yaml": man(""), "main.st": file(prog), "plant.yaml": file(plantEq)}, plant: true},
		"no equipment":           {fs: fstest.MapFS{"nautilus.yaml": man(""), "main.st": file(prog), "plant.yaml": file("areas: [{ id: A }]\n")}},
		"a mimic is its screens": {fs: fstest.MapFS{"nautilus.yaml": man(""), "main.st": file(prog), "plant.yaml": file(plantEq), "a.mimic.json": file("{}")}, screens: true},
	} {
		t.Run(name, func(t *testing.T) {
			p, err := Load(c.fs, ManifestName)
			if err != nil {
				t.Fatal(err)
			}
			if p.HMIPlant != c.plant || p.HMIScreens != c.screens {
				t.Errorf("HMIPlant=%v HMIScreens=%v, want %v %v", p.HMIPlant, p.HMIScreens, c.plant, c.screens)
			}
		})
	}
}
