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
		"screens/FilterCell.component.json": file(`{"ports":[{"name":"influent","x":0.5,"y":0}],"examples":{"washing":{"level":5.2}}}`),
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

// A unit's copies are checked like any other equipment: a tag its {n}
// names that isn't declared is an error at the copy that names it, and its
// pipes end on the copies' ports.
func TestCheckMimicsUnits(t *testing.T) {
	file := func(s string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(s)} }
	fsys := fstest.MapFS{
		"nautilus.yaml": file(`name: p
tasks:
  - program: main.st
    scan: 100ms
tags:
  - { name: F1_Level, type: REAL, role: state }
  - { name: F2_Level, type: REAL, role: state }
`),
		"main.st":                           file("PROGRAM main\nEND_PROGRAM\n"),
		"screens/FilterCell.svelte":         file("<p></p>"),
		"screens/FilterCell.component.json": file(`{"ports":[{"name":"inf","x":0.5,"y":0}]}`),
		"screens/g.mimic.json": file(`{"canvas":{"width":900,"height":400},
		  "units":[{"id":"filter","each":[1,2,3],"step":[300,0],
		    "equipment":[{"id":"F{n}","component":"FilterCell","x":10,"y":100,"bind":{"level":"F{n}_Level"}},
		                 {"id":"V{n}","component":"Valve","x":50,"y":20}],
		    "pipes":[{"id":"p{n}","from":{"equip":"V{n}","port":"out"},"to":{"equip":"F{n}","port":"inf"}},
		             {"id":"q{n}","from":{"equip":"V{n}","port":"out"},"to":{"equip":"F{n}","port":"outlet"}}]}]}`),
	}
	p, err := Load(fsys, "")
	if err != nil {
		t.Fatal(err)
	}
	reports, errs, warns := p.CheckMimics(fsys, true)
	all := strings.Join(errs, "\n")
	// FilterCell has no examples: said once, however many copies
	if len(warns) != 1 || !strings.Contains(warns[0], `FilterCell has no examples: give its FilterCell.component.json "examples"`) {
		t.Errorf("warnings = %q, want the one about FilterCell's examples", warns)
	}
	if len(reports) != 1 || reports[0].Equipment != 6 || reports[0].Pipes != 6 {
		t.Errorf("reports = %+v, want 6 equipment and 6 pipes", reports)
	}
	for _, want := range []string{"F3_Level", "pipe q1", "pipe q2", "pipe q3"} {
		if !strings.Contains(all, want) {
			t.Errorf("no error naming %s in:\n%s", want, all)
		}
	}
	if strings.Contains(all, "pipe p1") || strings.Contains(all, "F1_Level") {
		t.Errorf("a good copy was reported:\n%s", all)
	}
}

// A SetpointGroup writes each tag its items name: each must be declared,
// and an input (a driver's) is a warning, as for a CommandButton.
func TestCheckMimicsSetpointGroup(t *testing.T) {
	file := func(s string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(s)} }
	fsys := fstest.MapFS{
		"nautilus.yaml": file(`name: p
tasks:
  - program: main.st
    scan: 100ms
tags:
  - { name: Kp, type: REAL, role: setpoint }
  - { name: LT101, type: REAL, role: input }
`),
		"main.st": file("PROGRAM main\nEND_PROGRAM\n"),
		"screen.mimic.json": file(`{"equipment":[
			{"id":"S1","component":"SetpointGroup","x":0,"y":0,"props":{"title":"Tuning","items":[
				{"tag":"Kp","label":"Gain"},{"tag":"kd","label":"Derivative"},{"label":"No tag"},{"tag":"LT101"}]}},
			{"id":"S2","component":"SetpointGroup","x":0,"y":0},
			{"id":"V1","component":"Valve","x":0,"y":0},
			{"id":"P1","component":"Pump","x":0,"y":0}],
		  "pipes":[{"id":"p1","from":{"equip":"V1","port":"out"},"to":{"equip":"P1","port":"in"},"points":[]},
			{"id":"p2","from":{"equip":"P1","port":"out"},"to":{"equip":"V1","port":"left"},"points":[]}]}`),
	}
	p, err := Load(fsys, ManifestName)
	if err != nil {
		t.Fatal(err)
	}
	_, errs, warns := p.CheckMimics(fsys, true)
	all := strings.Join(errs, "\n")
	for _, want := range []string{
		"equipment S1's item 2 writes kd, which the project doesn't declare",
		"equipment S1's item 3 has no tag",
		"equipment S2 is a SetpointGroup with no items",
		`pipe p2 ends on V1's port "left"; a Valve has ports in, out`,
	} {
		if !strings.Contains(all, want) {
			t.Errorf("no error %q in:\n%s", want, all)
		}
	}
	if len(errs) != 4 {
		t.Errorf("%d errors, want 4:\n%s", len(errs), all)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "S1's item 4 writes LT101, an input") {
		t.Errorf("warnings = %q", warns)
	}
}

func TestCheckMimicsKitProps(t *testing.T) {
	file := func(s string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(s)} }
	fsys := fstest.MapFS{
		"nautilus.yaml": file(`name: p
tasks:
  - program: main.st
    scan: 100ms
tags:
  - { name: XV101_ZSO, type: BOOL, role: input }
  - { name: P101_Run, type: BOOL, role: input }
`),
		"main.st": file("PROGRAM main\nEND_PROGRAM\n"),
		"screen.mimic.json": file(`{"equipment":[
			{"id":"V1","component":"Valve","x":0,"y":0,"openPct":50,"bind":{"open":"XV101_ZSO"}},
			{"id":"V2","component":"Valve","x":0,"y":0,"props":{"openPct":50}},
			{"id":"P1","component":"Pump","x":0,"y":0,"label":"Pump","bind":{"running":"P101_Run"},"props":{"fault":false}}]}`),
	}
	p, err := Load(fsys, ManifestName)
	if err != nil {
		t.Fatal(err)
	}
	_, errs, warns := p.CheckMimics(fsys, true)
	if len(errs) != 0 {
		t.Errorf("errors: %v", errs)
	}
	all := strings.Join(warns, "\n")
	for _, want := range []string{
		`equipment V1 has "openPct", which equipment doesn't have`,
		`put it in "props": {"openPct": 50}`,
		"equipment V1's bind has open, which Valve doesn't take, so it shows nothing (Valve takes openPct)",
		"equipment P1's props has fault, which Pump doesn't take",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("no warning %q in:\n%s", want, all)
		}
	}
	if len(warns) != 3 {
		t.Errorf("%d warnings, want 3:\n%s", len(warns), all)
	}
}

func TestCheckMimicsPanels(t *testing.T) {
	file := func(s string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(s)} }
	fsys := fstest.MapFS{
		"nautilus.yaml": file(`name: p
tasks:
  - program: main.st
    scan: 100ms
tags:
  - { name: FlowSP, type: REAL, role: setpoint }
  - { name: Start, type: BOOL, role: setpoint }
  - { name: Running, type: BOOL, role: state }
  - { name: Flow, type: REAL, role: input }
`),
		"main.st": file("PROGRAM main\nEND_PROGRAM\n"),
		"screen.mimic.json": file(`{"equipment":[
			{"id":"P1","component":"Pump","x":0,"y":0,"panel":{
				"readings":[{"tag":"Running"},{"tag":"Flw"}],
				"setpoints":[{"tag":"FlowSP"},{"tag":"Flow"}],
				"commands":[{"tag":"Start"},{"tag":"Strat","label":"Go","held":"Runing"}],
				"toggles":[{}],
				"links":["V9"]}},
			{"id":"FT1","component":"Gauge","x":0,"y":0,"panel":"P1"},
			{"id":"V1","component":"Valve","x":0,"y":0,"panel":"P9"},
			{"id":"V2","component":"Valve","x":0,"y":0,"panel":{}}]}`),
	}
	p, err := Load(fsys, ManifestName)
	if err != nil {
		t.Fatal(err)
	}
	_, errs, warns := p.CheckMimics(fsys, true)
	all := strings.Join(errs, "\n")
	for _, want := range []string{
		"equipment P1's panel's reading 2 binds Flw, which the project doesn't declare",
		"equipment P1's panel's command 1 has no label",
		"equipment P1's panel's command 2 writes Strat, which the project doesn't declare",
		"equipment P1's panel's command 2's held binds Runing",
		"equipment P1's panel's toggle 1 has no tag",
		"equipment P1's panel links to V9, which isn't on the screen",
		"equipment V1's panel opens P9's, which isn't on the screen",
		"equipment V2's panel is empty",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("no error %q in:\n%s", want, all)
		}
	}
	if len(errs) != 8 {
		t.Errorf("%d errors, want 8:\n%s", len(errs), all)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "P1's panel's setpoint 2 writes Flow, an input") {
		t.Errorf("warnings = %q", warns)
	}
}

func TestCheckMimicsTankMarks(t *testing.T) {
	file := func(s string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(s)} }
	fsys := fstest.MapFS{
		"nautilus.yaml": file(`name: p
tasks:
  - program: main.st
    scan: 100ms
tags:
  - { name: LeadOnSP, type: REAL, role: setpoint }
`),
		"main.st": file("PROGRAM main\nEND_PROGRAM\n"),
		"screen.mimic.json": file(`{"equipment":[
			{"id":"T1","component":"Tank","x":0,"y":0,"props":{"marks":[
				{"label":"Lead on","tag":"LeadOnSP"},{"label":"High","value":90,"alarm":true},
				{"label":"Lag on","tag":"LagOnSp"},{"label":"Off"},{"value":10}]}}]}`),
	}
	p, err := Load(fsys, ManifestName)
	if err != nil {
		t.Fatal(err)
	}
	_, errs, warns := p.CheckMimics(fsys, true)
	all := strings.Join(errs, "\n")
	for _, want := range []string{
		"equipment T1's mark 3 binds LagOnSp, which the project doesn't declare",
		"equipment T1's mark 4 has no tag and no value",
		"equipment T1's mark 5 has no label",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("no error %q in:\n%s", want, all)
		}
	}
	if len(errs) != 3 || len(warns) != 0 {
		t.Errorf("errors %q, warnings %q", errs, warns)
	}
}

// A label's sense line point and its states are checked; a key a label
// doesn't have is said once.
func TestCheckMimicsLabelStates(t *testing.T) {
	file := func(s string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(s)} }
	fsys := fstest.MapFS{
		"nautilus.yaml": file(`name: p
tasks:
  - program: main.st
    scan: 100ms
tags:
  - { name: P1_Running, type: BOOL, role: state }
  - { name: P1_Fault, type: BOOL, role: state }
  - { name: FIT1, type: REAL, role: state }
`),
		"main.st": file("PROGRAM main\nEND_PROGRAM\n"),
		"screen.mimic.json": file(`{"canvas":{"width":1000,"height":600},"equipment":[
			{"id":"T1","component":"Tank","x":0,"y":0,"props":{"alarm":true,"marks":[{"label":"High","value":90,"alarm":"high"},{"label":"Low","value":5,"alarm":"under"}]}}],
		"labels":[
			{"text":"P-101","x":10,"y":10,"state":[{"when":"P1_Fault","text":"Fault","kind":"critical"},{"when":"P1_Running","text":"Running","kind":"good"},{"text":"Stopped"}]},
			{"text":"P-102","x":10,"y":40,"state":[{"text":"Stopped"},{"when":"P2_Running","text":"Running","kind":"green"}]},
			{"text":"FIT-101","x":10,"y":70,"bind":"FIT1","unit":"gpm","at":[400,300]},
			{"text":"FIT-102","x":10,"y":100,"bind":"FIT1","at":[1400,300],"colour":"red"},
			{"text":"FIT-103","x":10,"y":130,"bind":"FIT1","at":[1],"colour":"red"},
			{"text":"Both","x":10,"y":160,"bind":"FIT1","state":[{"text":"x"}]}]}`),
	}
	p, err := Load(fsys, ManifestName)
	if err != nil {
		t.Fatal(err)
	}
	_, errs, warns := p.CheckMimics(fsys, true)
	all := strings.Join(errs, "\n")
	for _, want := range []string{
		`equipment T1's mark 2 has alarm "under"; it is true, "high" or "low"`,
		"label P-102's state 1 has no when, so it always shows and the states after it never do; put it last",
		"label P-102's state 2 binds P2_Running, which the project doesn't declare",
		`label P-102's state 2's kind is "green"`,
		"label FIT-102's at [1400, 300] is off the 1000x600 canvas",
		"label FIT-103's at is [1]; give the point it measures as [x, y]",
		"label Both has both bind and state",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("no error %q in:\n%s", want, all)
		}
	}
	if len(errs) != 7 {
		t.Errorf("%d errors, want 7:\n%s", len(errs), all)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], `label FIT-102 has "colour", which a label doesn't have`) {
		t.Errorf("warnings %q", warns)
	}
}
