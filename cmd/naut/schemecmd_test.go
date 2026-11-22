package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/joyautomation/nautilus/internal/plant"
)

const schemePlant = `
areas:
  - { id: Receiving, label: "Receiving dock" }
  - { id: Roasting, label: Roasting }
equipment:
  - { id: TK1, type: Tank, area: Receiving, tag: TK1 }
  - { id: P1, type: Pump, area: Receiving, tag: P1, label: "Feed pump", nozzles: [N1, N2] }
  - { id: P2, type: Pump, area: Receiving, tag: P2, nozzles: [N1, N2] }
  - { id: R1, type: Tank, area: Roasting, tag: R1 }
  - { id: Belt, area: Receiving, device: CONV }
flows:
  - { from: TK1, to: P1.N1 }
  - { from: P1.N2, to: P2.N1 }
  - { from: P2.N2, to: R1 }
`

func schemePlantModel(t *testing.T, y string) *plant.Plant {
	t.Helper()
	p, err := plant.Read(fstest.MapFS{
		"plant.yaml":                  {Data: []byte(y)},
		"asset-types/Tank.asset.yaml": {Data: []byte("type: Tank\ncomponent: Tank\nbind: { levelPct: \"{tag}.Level\" }\nreadings: [{ tag: \"{tag}.Level\", unit: \"%\" }]\n")},
		"asset-types/Pump.asset.yaml": {Data: []byte("type: Pump\ncomponent: Pump\nports: { N1: in, N2: out }\nbind: { running: \"{tag}.Run\" }\nreadings: [{ tag: \"{tag}.Run\", on: Running, off: Stopped }]\n")},
	}, "plant.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if m := p.Check(); len(m) != 0 {
		t.Fatalf("the test plant: %v", m)
	}
	return p
}

type schemeDoc struct {
	Name      string
	Canvas    struct{ Width, Height float64 }
	Equipment []struct {
		ID, Component, Label, Plant string
		X, Y, Width, Height         float64
		Bind                        map[string]string
		Panel                       struct{ Title string }
		Extra                       string
	}
	Pipes []struct {
		ID, Routing string
		From, To    struct{ Equip, Port string }
	}
}

func readScheme(t *testing.T, b []byte) schemeDoc {
	t.Helper()
	var d schemeDoc
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatalf("%v\n%s", err, b)
	}
	return d
}

// A scheme from the plant: the area's pieces left to right along its flows,
// each with its component, label, bindings and panel, and the flows as
// auto-routed pipes between the ports the asset type names (the kit's own
// component ports when it names none).
func TestSchemeFromScratch(t *testing.T) {
	p := schemePlantModel(t, schemePlant)
	out, notes, err := planScheme(p, p.Areas[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	d := readScheme(t, out)
	if d.Name != "Receiving dock" || len(d.Equipment) != 3 {
		t.Fatalf("scheme %+v", d)
	}
	at := map[string]int{}
	for i, e := range d.Equipment {
		at[e.ID] = i
	}
	tk, p1, p2 := d.Equipment[at["TK1"]], d.Equipment[at["P1"]], d.Equipment[at["P2"]]
	if !(tk.X < p1.X && p1.X < p2.X) {
		t.Errorf("x: TK1 %v, P1 %v, P2 %v: left to right along TK1 -> P1 -> P2", tk.X, p1.X, p2.X)
	}
	if p1.Label != "Feed pump" || p1.Plant != "P1" || p1.Component != "Pump" || p1.Bind["running"] != "P1.Run" || p1.Panel.Title != "Feed pump" {
		t.Errorf("P1 %+v", p1)
	}
	if tk.Bind["levelPct"] != "TK1.Level" || tk.Width != 140 {
		t.Errorf("TK1 %+v", tk)
	}
	if d.Canvas.Width < p2.X+p2.Width || d.Canvas.Height < tk.Y+tk.Height {
		t.Errorf("canvas %v does not hold the equipment", d.Canvas)
	}
	pipes := map[string]string{}
	for _, pp := range d.Pipes {
		if pp.Routing != "auto" {
			t.Errorf("pipe %s routing %q", pp.ID, pp.Routing)
		}
		pipes[pp.ID] = pp.From.Equip + "." + pp.From.Port + ">" + pp.To.Equip + "." + pp.To.Port
	}
	// TK1 names no nozzles: the kit's Tank right port; P1.N1 by the asset type's ports, the Pump's in; P1.N2 -> P2.N1 by the asset type's ports
	if pipes["flow_TK1_P1"] != "TK1.right>P1.in" || pipes["flow_P1_P2"] != "P1.out>P2.in" || len(pipes) != 2 {
		t.Errorf("pipes %v", pipes)
	}
	all := strings.Join(notes, "\n")
	for _, want := range []string{"equipment Belt is not drawn: its asset type names no component", "flow P2.N2 -> R1 leaves the area: not drawn"} {
		if !strings.Contains(all, want) {
			t.Errorf("notes lack %q:\n%s", want, all)
		}
	}
}

func TestSchemeNothingToDraw(t *testing.T) {
	p := schemePlantModel(t, "areas: [{ id: Empty }]\nequipment: [{ id: B, area: Empty, device: CONV }]\n")
	out, notes, err := planScheme(p, p.Areas[0], nil)
	if err != nil || out != nil || !strings.Contains(strings.Join(notes, " "), "nothing to draw") {
		t.Errorf("an area with nothing to draw: %q %v %v", out, notes, err)
	}
}

// -update adds what the plant gained and never moves, changes or removes what
// is there: a hand-moved piece, a key the generator does not write, a pipe a
// person redrew.
func TestSchemeUpdateTouchesNothingThatIsThere(t *testing.T) {
	p := schemePlantModel(t, schemePlant)
	first, _, _ := planScheme(p, p.Areas[0], nil)
	// a person moves P1, adds a key of their own, renames a pipe's route
	var top map[string]json.RawMessage
	_ = json.Unmarshal(first, &top)
	var eqs []map[string]any
	_ = json.Unmarshal(top["equipment"], &eqs)
	for _, e := range eqs {
		if e["id"] == "P1" {
			e["x"], e["y"], e["extra"] = 777.0, 333.0, "mine"
		}
	}
	// ... and P2 is gone from the file (they deleted it): it is the plant's, so -update adds it back
	var kept []map[string]any
	for _, e := range eqs {
		if e["id"] != "P2" {
			kept = append(kept, e)
		}
	}
	top["equipment"], _ = json.Marshal(kept)
	top["background"] = json.RawMessage(`"grid"`)
	edited, _ := json.MarshalIndent(top, "", "  ")

	out, _, err := planScheme(p, p.Areas[0], edited)
	if err != nil {
		t.Fatal(err)
	}
	var d struct {
		Background string
		Equipment  []struct {
			ID, Extra    string
			X, Y, Height float64
		}
	}
	_ = json.Unmarshal(out, &d)
	for _, e := range d.Equipment {
		if e.ID == "P2" {
			// the band below everything already drawn, whichever piece reaches lowest
			for _, o := range d.Equipment {
				if o.ID != "P2" && e.Y < o.Y+o.Height {
					t.Errorf("P2 (y %v) overlaps %s (y %v..%v): new equipment goes below what is drawn", e.Y, o.ID, o.Y, o.Y+o.Height)
				}
			}
		}
	}
	got := map[string]float64{}
	var ids []string
	for _, e := range d.Equipment {
		got[e.ID] = e.X
		ids = append(ids, e.ID)
		if e.ID == "P1" && (e.X != 777 || e.Y != 333 || e.Extra != "mine") {
			t.Errorf("P1 was moved or changed by -update: %+v", e)
		}
	}
	if d.Background != "grid" {
		t.Errorf("a key of the person's own is gone: %q", d.Background)
	}
	if strings.Join(ids, ",") != "TK1,P1,P2" {
		t.Errorf("equipment %v: P2 added back after the others", ids)
	}
	// run again: nothing to add, the file is byte for byte the same
	again, _, _ := planScheme(p, p.Areas[0], out)
	if !bytes.Equal(again, out) {
		t.Errorf("a second -update changed the file:\n%s", again)
	}
}

func TestSchemeUpdateNotesAPlantPieceThatWentAway(t *testing.T) {
	p := schemePlantModel(t, schemePlant)
	first, _, _ := planScheme(p, p.Areas[0], nil)
	smaller := schemePlantModel(t, strings.Replace(strings.Replace(schemePlant, "  - { id: P2, type: Pump, area: Receiving, tag: P2, nozzles: [N1, N2] }\n", "", 1), "  - { from: P1.N2, to: P2.N1 }\n  - { from: P2.N2, to: R1 }\n", "", 1))
	_, notes, err := planScheme(smaller, smaller.Areas[0], first)
	if err != nil || !strings.Contains(strings.Join(notes, "\n"), `names plant "P2", which the plant no longer has: left as it is`) {
		t.Errorf("%v %v", notes, err)
	}
}

func TestSchemeLoopsAndMissingPorts(t *testing.T) {
	p := schemePlantModel(t, "areas: [{ id: A }]\nequipment:\n  - { id: X, type: Pump, area: A, tag: X, nozzles: [N1, N2] }\n  - { id: Y, type: Pump, area: A, tag: Y, nozzles: [N1, N2] }\n  - { id: Z, type: Tank, area: A, tag: Z }\nflows:\n  - { from: X.N2, to: Y.N1 }\n  - { from: Y.N2, to: X.N1 }\n")
	out, _, err := planScheme(p, p.Areas[0], nil)
	if err != nil {
		t.Fatalf("a loop in the flows: %v", err)
	}
	if d := readScheme(t, out); len(d.Equipment) != 3 || len(d.Pipes) != 2 {
		t.Errorf("a loop is drawn: %+v", d)
	}
	if _, _, err := planScheme(p, p.Areas[0], []byte("not json")); err == nil {
		t.Error("a file that is not a mimic must be an error, not overwritten")
	}
	// a component the generator knows no ports for and an asset type that names none
	q, _ := plant.Read(fstest.MapFS{
		"plant.yaml":                   {Data: []byte("areas: [{ id: A }]\nequipment:\n  - { id: M1, type: Mixer, area: A, tag: M1 }\n  - { id: M2, type: Mixer, area: A, tag: M2 }\nflows: [{ from: M1, to: M2 }]\n")},
		"asset-types/Mixer.asset.yaml": {Data: []byte("type: Mixer\ncomponent: Mixer\n")},
	}, "plant.yaml")
	out, notes, _ := planScheme(q, q.Areas[0], nil)
	if d := readScheme(t, out); len(d.Pipes) != 0 || !strings.Contains(strings.Join(notes, " "), "no port for") {
		t.Errorf("a flow with no ports is not drawn, and says so: %v %v", d.Pipes, notes)
	}
}

// The command: nothing is overwritten without -update.
func TestSchemeCommandLeavesAFileAlone(t *testing.T) {
	dir := t.TempDir()
	write := func(name, s string) {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("nautilus.yaml", "name: p\nplant: plant.yaml\ntasks:\n  - program: main.st\n    scan: 100ms\ntags:\n  - { name: TK1, type: REAL, role: input }\n  - { name: TK1.Level, type: REAL, role: input }\n")
	write("main.st", "PROGRAM main\nEND_PROGRAM\n")
	write("plant.yaml", "areas: [{ id: Receiving }]\nequipment:\n  - { id: TK1, type: Tank, area: Receiving, tag: TK1 }\n")
	write("asset-types/Tank.asset.yaml", "type: Tank\ncomponent: Tank\nreadings: [{ tag: \"{tag}\" }]\n")
	if code := runHMIScheme([]string{dir}); code != 0 {
		t.Fatalf("first run: %d", code)
	}
	path := filepath.Join(dir, "receiving.mimic.json")
	if err := os.WriteFile(path, []byte(`{"name":"mine","equipment":[],"pipes":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := runHMIScheme([]string{dir}); code != 0 {
		t.Fatalf("second run: %d", code)
	}
	if b, _ := os.ReadFile(path); !strings.Contains(string(b), `"mine"`) || strings.Contains(string(b), "TK1") {
		t.Errorf("without -update the file was written: %s", b)
	}
	if code := runHMIScheme([]string{"-update", dir}); code != 0 {
		t.Fatalf("update: %d", code)
	}
	if b, _ := os.ReadFile(path); !strings.Contains(string(b), `"mine"`) || !strings.Contains(string(b), "TK1") {
		t.Errorf("-update keeps the name and adds TK1: %s", b)
	}
}

// -update keeps a person's work whole: a renamed piece's pipes end on its new
// name, a pipe drawn by hand is not drawn twice, the canvas only grows and
// keeps its keys, a file it cannot safely edit is refused.
func TestSchemeUpdateAfterAPersonEdited(t *testing.T) {
	p := schemePlantModel(t, schemePlant)
	first, _, _ := planScheme(p, p.Areas[0], nil)
	var top map[string]json.RawMessage
	_ = json.Unmarshal(first, &top)
	var eqs []map[string]any
	_ = json.Unmarshal(top["equipment"], &eqs)
	var kept []map[string]any
	for _, e := range eqs {
		if e["id"] == "P1" {
			e["id"] = "pump-a" // renamed by hand, plant: P1 kept
		}
		if e["id"] != "P2" {
			kept = append(kept, e)
		}
	}
	top["equipment"], _ = json.Marshal(kept)
	// the TK1 -> P1 pipe drawn by hand under another name between the same ports
	top["pipes"] = json.RawMessage(`[{"id":"my-feed","routing":"auto","points":[],"from":{"equip":"TK1","port":"right"},"to":{"equip":"pump-a","port":"in"}}]`)
	top["canvas"] = json.RawMessage(`{"width":5000,"height":3000,"grid":true}`)
	edited, _ := json.Marshal(top)
	out, _, err := planScheme(p, p.Areas[0], edited)
	if err != nil {
		t.Fatal(err)
	}
	d := readScheme(t, out)
	ends := map[string]string{}
	for _, pp := range d.Pipes {
		ends[pp.ID] = pp.From.Equip + ">" + pp.To.Equip
	}
	if len(d.Pipes) != 2 || ends["my-feed"] != "TK1>pump-a" || ends["flow_P1_P2"] != "pump-a>P2" {
		t.Errorf("pipes %v: the hand-drawn feed stays alone, the new P1 -> P2 pipe starts on pump-a (the piece's name in the file)", ends)
	}
	if d.Canvas.Width != 5000 || d.Canvas.Height != 3000 || !strings.Contains(string(out), `"grid": true`) {
		t.Errorf("canvas %+v: it only grows and keeps what a person put in it\n%s", d.Canvas, out)
	}
	// a file with units, or a list that is not a list, is refused, not rewritten
	for name, doc := range map[string]string{
		"units":    `{"units":[{"id":"u"}],"equipment":[],"pipes":[]}`,
		"not list": `{"equipment":{"a":1},"pipes":[]}`,
	} {
		if got, _, err := planScheme(p, p.Areas[0], []byte(doc)); err == nil || got != nil {
			t.Errorf("%s: %q %v, want a refusal", name, got, err)
		}
	}
}

func TestSchemeWritesLabelsAsAPersonWould(t *testing.T) {
	p := schemePlantModel(t, "areas: [{ id: A }]\nequipment:\n  - { id: T, type: Tank, area: A, tag: T, label: \"Tank & Heater <1>\" }\n")
	out, _, _ := planScheme(p, p.Areas[0], nil)
	if !strings.Contains(string(out), `"Tank & Heater <1>"`) {
		t.Errorf("label escaped:\n%s", out)
	}
}

func TestSchemeSaysWhyAFlowEndIsMissing(t *testing.T) {
	p := schemePlantModel(t, "areas: [{ id: A }]\nequipment:\n  - { id: T, type: Tank, area: A, tag: T }\n  - { id: B, area: A, device: CONV }\nflows: [{ from: T, to: B }]\n")
	_, notes, _ := planScheme(p, p.Areas[0], nil)
	if all := strings.Join(notes, "\n"); !strings.Contains(all, "one end is a piece with no component") || strings.Contains(all, "leaves the area") {
		t.Errorf("notes %q", all)
	}
}

// -update on a scheme that already has its pipes says so once ("N already
// drawn, unchanged"), not one "left as it is" line a pipe, and says when it
// grew the canvas.
func TestSchemeUpdateSummarisesWhatIsAlreadyThere(t *testing.T) {
	p := schemePlantModel(t, schemePlant)
	first, _, _ := planScheme(p, p.Areas[0], nil)
	// a person shrinks the canvas below what the drawing needs; the update grows it back
	var top map[string]json.RawMessage
	_ = json.Unmarshal(first, &top)
	top["canvas"] = json.RawMessage(`{"width":100,"height":100}`)
	top["pipes"] = json.RawMessage(`[]`) // so the update has something to add
	old, _ := json.Marshal(top)
	_, notes, err := planScheme(p, p.Areas[0], old)
	if err != nil {
		t.Fatal(err)
	}
	all := strings.Join(notes, "\n")
	if !strings.Contains(all, "canvas height grew from 100 to") {
		t.Errorf("no note on the canvas growing: %q", all)
	}
	dir := t.TempDir()
	for name, s := range map[string]string{
		"nautilus.yaml":               "name: p\nplant: plant.yaml\ntasks:\n  - program: main.st\n    scan: 100ms\ntags:\n  - { name: TK1, type: REAL, role: input }\n  - { name: P1, type: BOOL, role: state }\n",
		"main.st":                     "PROGRAM main\nEND_PROGRAM\n",
		"plant.yaml":                  "areas: [{ id: Receiving }]\nequipment:\n  - { id: TK1, type: Tank, area: Receiving, tag: TK1 }\n  - { id: P1, type: Pump, area: Receiving, tag: P1, nozzles: [N1, N2] }\nflows:\n  - { from: TK1, to: P1.N1 }\n",
		"asset-types/Tank.asset.yaml": "type: Tank\ncomponent: Tank\nreadings: [{ tag: \"{tag}\" }]\n",
		"asset-types/Pump.asset.yaml": "type: Pump\ncomponent: Pump\nreadings: [{ tag: \"{tag}\" }]\n",
	} {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runHMIScheme([]string{dir})
	// the second run: the id of the pipe is already there
	r, w, _ := os.Pipe()
	saved := os.Stdout
	os.Stdout = w
	code := runHMIScheme([]string{"-update", dir})
	os.Stdout = saved
	w.Close()
	out, _ := io.ReadAll(r)
	if code != 0 {
		t.Fatalf("update: %d", code)
	}
	if strings.Contains(string(out), "left as it is") {
		t.Errorf("a pipe already drawn reads like a problem:\n%s", out)
	}
	if !strings.Contains(string(out), "already drawn, unchanged") && !strings.Contains(string(out), "nothing to add") {
		t.Errorf("no summary:\n%s", out)
	}
}
