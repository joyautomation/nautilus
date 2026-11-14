package project

import (
	"strings"
	"testing"
	"testing/fstest"
)

func TestCheckMimics(t *testing.T) {
	file := func(s string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(s)} }
	fsys := fstest.MapFS{
		"nautilus.yaml": file(`name: p
tasks:
  - program: main.st
    scan: 100ms
tags:
  - { name: F1_Level, type: REAL, role: state }
  - { name: F1_Req, type: BOOL, role: setpoint }
  - { name: LT101, type: REAL, role: input }
`),
		"main.st":                           file("PROGRAM main\nEND_PROGRAM\n"),
		"screens/FilterCell.svelte":         file("<p></p>"),
		"screens/FilterCell.component.json": file(`{"ports":[{"name":"influent","x":0.5,"y":0}]}`),
		"screens/gallery.mimic.json": file(`{"equipment":[
			{"id":"F1","component":"FilterCell","x":0,"y":0,"bind":{"level":"F1_Level","busy":"!F1_Busy"}},
			{"id":"T1","component":"Tank","x":0,"y":0,"bind":{"levelPct":"f1_level"}},
			{"id":"B1","component":"CommandButton","x":0,"y":0,"props":{"tag":"F1_Req"}},
			{"id":"B2","component":"CommandButton","x":0,"y":0,"props":{"tag":"LT101"}},
			{"id":"X","component":"Gizmo","x":0,"y":0},
			{"id":"T1","component":"Tank","x":0,"y":0}],
		  "pipes":[
			{"id":"p1","from":{"equip":"T1","port":"bottom"},"to":{"equip":"F1","port":"influent"},"points":[]},
			{"id":"p2","from":{"equip":"F1","port":"effluent"},"to":{"equip":"Nope","port":"in"},"points":[]},
			{"id":"p3","from":{"equip":"T1","port":"bottom"},"to":{"equip":"F1","port":"influent"}},
			{"id":"p4","from":{"equip":"T1","port":"bottom"}},
			{"id":"p5","points":[[0,0]]}],
		  "labels":[{"text":"LT-101","bind":"LT101"}]}`),
		"scratch/nautilus.yaml": file("name: other\n"),
		"scratch/x.mimic.json":  file(`{"equipment":[{"id":"A","component":"Nothing"}]}`),
	}
	p, err := Load(fsys, ManifestName)
	if err != nil {
		t.Fatal(err)
	}
	reports, errs, warns := p.CheckMimics(fsys, true)
	all := strings.Join(errs, "\n")
	for _, want := range []string{
		"equipment F1's busy binds !F1_Busy, which the project doesn't declare",
		"equipment T1's levelPct binds f1_level, which the project doesn't declare (did you mean F1_Level?)",
		"equipment X is a Gizmo, and there is no Gizmo.svelte in the project",
		"two pieces of equipment are T1",
		`pipe p2 ends on F1's port "effluent"; a FilterCell has ports influent (a component's ports are in its FilterCell.component.json)`,
		"pipe p2 ends on Nope, which isn't on the screen",
		// p3 is anchored at both ends and needs no points (bw01 haiku-1)
		"pipe p4 has nothing to draw: give it points, or anchor both ends with from and to",
		"pipe p5 has nothing to draw",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("no error %q in:\n%s", want, all)
		}
	}
	if len(errs) != 8 {
		t.Errorf("%d errors, want 8:\n%s", len(errs), all)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "B2 writes LT101, an input") {
		t.Errorf("warnings = %q", warns)
	}
	if len(reports) != 1 || reports[0].File != "screens/gallery.mimic.json" {
		t.Errorf("reports = %+v (a nested project's mimic is its own)", reports)
	}
}

// Against a second manifest (-m), a tag it leaves out is a warning.
func TestCheckMimicsAgainstAnotherManifest(t *testing.T) {
	file := func(s string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(s)} }
	fsys := fstest.MapFS{
		"line.yaml":       file("name: p\ntasks:\n  - program: main.st\n    scan: 100ms\n"),
		"main.st":         file("PROGRAM main\nEND_PROGRAM\n"),
		"skid.mimic.json": file(`{"equipment":[{"id":"V","component":"Valve","x":0,"y":0,"bind":{"openPct":"XV1_Open"}}]}`),
	}
	p, err := Load(fsys, "line.yaml")
	if err != nil {
		t.Fatal(err)
	}
	_, errs, warns := p.CheckMimics(fsys, false)
	if len(errs) != 0 || len(warns) != 1 || !strings.Contains(warns[0], "this manifest doesn't declare") {
		t.Errorf("errs = %q, warns = %q", errs, warns)
	}
}
