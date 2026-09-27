package scene

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/ir"
)

func udt(name string, fields ...string) *ir.StructDef {
	sd := &ir.StructDef{Name: name, FieldIndex: map[string]int{}}
	for i, f := range fields {
		n, kind, _ := strings.Cut(f, ":")
		t := ir.RealT
		switch kind {
		case "bool":
			t = ir.BoolT
		case "int":
			t = ir.IntT
		}
		sd.Fields = append(sd.Fields, ir.StructField{Name: n, Type: t})
		sd.FieldIndex[n] = i
	}
	return sd
}

var rigTags = []TagInfo{
	{Name: "T101", TypeName: "Tank", Struct: udt("Tank", "Level", "TempC", "HH:bool", "LL:bool")},
	{Name: "P101", TypeName: "Motor", Struct: udt("Motor", "Run:bool", "Running:bool", "Fault:bool", "Speed", "Hours")},
	{Name: "XV101", TypeName: "Valve", Struct: udt("Valve", "Cmd", "Pos", "Fault:bool")},
	{Name: "Demand"},
	{Name: "SW1", TypeName: "Switch", Struct: udt("Switch", "PortsUp:int", "Fault:bool")},
}

func parse(t *testing.T, src string) *Doc {
	t.Helper()
	d, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return d
}

func joined(msgs []string) string { return strings.Join(msgs, "\n") }

func TestCheckAcceptsTheRigScene(t *testing.T) {
	d := parse(t, `{
		"nodes": [
			{"id": "T101", "kind": "tank", "tag": "T101", "pos": [1, -0.75, 0.4]},
			{"id": "P101", "kind": "pump", "tag": "P101", "pos": [0.3, 0, 0.2], "rot": [0, 90, 0]},
			{"id": "XV101", "kind": "valve", "tag": "XV101", "pos": [1.8, 0.85, 0], "bind": {"cmd": "Demand"}}
		],
		"pipes": [{"points": [[0, 0, 0], [1, 0, 0]], "bind": {"flowing": "P101.Running"}}]
	}`)
	errs, warns := Check(d, rigTags)
	if len(errs) != 0 || len(warns) != 0 {
		t.Fatalf("want clean, got errs %v warns %v", errs, warns)
	}
}

func TestParseRejectsUnknownFields(t *testing.T) {
	if _, err := Parse([]byte(`{"nodes": [{"id": "a", "kind": "tank", "postion": [0, 0, 0]}]}`)); err == nil || !strings.Contains(err.Error(), "postion") {
		t.Fatalf("want an unknown-field error naming postion, got %v", err)
	}
}

func TestCheckStructural(t *testing.T) {
	d := parse(t, `{
		"nodes": [
			{"id": "a", "kind": "tank", "pos": [1, 2]},
			{"id": "a", "kind": "tank", "pos": [0, 0, 0]},
			{"id": "b", "kind": "", "pos": [0, 0, 0]}
		],
		"pipes": [{"points": [[0, 0, 0]], "bind": {"colour": "X"}}],
		"writable": ["Demand"]
	}`)
	errs, _ := Check(d, rigTags)
	got := joined(errs)
	for _, want := range []string{"/nodes/0/pos:", "/nodes/1/id: duplicate id \"a\"", "/nodes/2/kind:", "/pipes/0/points:", "/pipes/0/bind/colour:", "/writable:"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestCheckUnknownKind(t *testing.T) {
	d := parse(t, `{"nodes": [{"id": "s", "kind": "switch", "tag": "SW1", "pos": [0, 0, 0]}]}`)
	errs, _ := Check(d, rigTags)
	if len(errs) != 1 || !strings.Contains(errs[0], `unknown kind "switch"`) {
		t.Fatalf("got %v", errs)
	}
	// Declared under kinds:, it is held to its own contract instead.
	d = parse(t, `{"kinds": {"switch": {"type": "Switch", "members": ["PortsUp", "Temp"]}},
		"nodes": [{"id": "s", "kind": "switch", "tag": "SW1", "pos": [0, 0, 0]}]}`)
	errs, _ = Check(d, rigTags)
	if len(errs) != 1 || !strings.Contains(errs[0], `has no member Temp`) {
		t.Fatalf("got %v", errs)
	}
}

func TestCheckTypeContract(t *testing.T) {
	// A tank kind on a Motor: the members are missing, so it is an error
	// that says how to re-point the kind.
	d := parse(t, `{"nodes": [{"id": "x", "kind": "tank", "tag": "P101", "pos": [0, 0, 0]}]}`)
	errs, _ := Check(d, rigTags)
	if len(errs) != 1 || !strings.Contains(errs[0], "kinds.tank.type") || !strings.Contains(errs[0], "Level/TempC") {
		t.Fatalf("got %v", errs)
	}
	// The same node with the members bound explicitly is fine: the
	// flat-tag escape hatch.
	d = parse(t, `{"nodes": [{"id": "x", "kind": "tank", "tag": "P101", "pos": [0, 0, 0], "bind": {"level": "P101.Speed", "tempC": "P101.Hours"}}]}`)
	if errs, _ = Check(d, rigTags); len(errs) != 0 {
		t.Fatalf("bound members should satisfy the contract, got %v", errs)
	}
	// A scalar tag on a struct kind.
	d = parse(t, `{"nodes": [{"id": "x", "kind": "tank", "tag": "Demand", "pos": [0, 0, 0]}]}`)
	if errs, _ = Check(d, rigTags); len(errs) != 1 || !strings.Contains(errs[0], "is a scalar") {
		t.Fatalf("got %v", errs)
	}
	// A re-pointed built-in: kinds.pump.type = VfdPump, tag of that type.
	tags := append(rigTags, TagInfo{Name: "P201", TypeName: "VfdPump", Struct: udt("VfdPump", "Running:bool", "Fault:bool", "Speed")})
	d = parse(t, `{"kinds": {"pump": {"type": "VfdPump"}}, "nodes": [{"id": "x", "kind": "pump", "tag": "P201", "pos": [0, 0, 0]}]}`)
	if errs, _ = Check(d, tags); len(errs) != 0 {
		t.Fatalf("re-pointed kind should pass, got %v", errs)
	}
}

func TestCheckRefs(t *testing.T) {
	d := parse(t, `{"nodes": [{"id": "x", "kind": "valve", "tag": "XV101", "pos": [0, 0, 0],
		"bind": {"a": "P101.Amps", "b": "Demand.X", "c": "Nope.Y", "d": "!P101.Speed", "e": "!P101.Fault", "f": "1bad"}}]}`)
	errs, warns := Check(d, rigTags)
	got := joined(errs)
	for _, want := range []string{`/nodes/0/bind/a: "P101.Amps": Motor has no member "Amps"`, `/nodes/0/bind/b: "Demand.X": Demand is not a struct`, `/nodes/0/bind/f: "1bad" is not a tag ref`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if len(errs) != 3 {
		t.Errorf("want 3 errors, got %d:\n%s", len(errs), got)
	}
	gotW := joined(warns)
	for _, want := range []string{`/nodes/0/bind/c: "Nope.Y": the manifest declares no tag "Nope"`, `/nodes/0/bind/d: "!P101.Speed" negates a REAL member`} {
		if !strings.Contains(gotW, want) {
			t.Errorf("missing warning %q in:\n%s", want, gotW)
		}
	}
	if len(warns) != 2 {
		t.Errorf("want 2 warnings, got %d:\n%s", len(warns), gotW)
	}
}

func TestCheckUndeclaredTagIsAWarning(t *testing.T) {
	d := parse(t, `{"nodes": [{"id": "x", "kind": "pump", "tag": "P999", "pos": [0, 0, 0]}]}`)
	errs, warns := Check(d, rigTags)
	if len(errs) != 0 || len(warns) != 1 || !strings.Contains(warns[0], "declares no tag \"P999\"") {
		t.Fatalf("errs %v warns %v", errs, warns)
	}
}

func TestGenerate(t *testing.T) {
	doc, unplaced, warns := Generate(rigTags, GenOptions{Name: "rig"})
	if len(warns) != 0 {
		t.Fatalf("warns: %v", warns)
	}
	if len(doc.Nodes) != 3 {
		t.Fatalf("want 3 nodes, got %+v", doc.Nodes)
	}
	// Sorted by tag name, on a 2-column grid at the default pitch.
	if doc.Nodes[0].Tag != "P101" || doc.Nodes[0].Kind != "pump" || doc.Nodes[1].Tag != "T101" || doc.Nodes[2].Tag != "XV101" {
		t.Errorf("order/kinds: %+v", doc.Nodes)
	}
	if got := doc.Nodes[2].Pos; len(got) != 3 || got[0] != 0 || got[2] != 0.8 {
		t.Errorf("third node should be on row 2: %v", got)
	}
	if len(unplaced) != 1 || unplaced[0].Name != "SW1" {
		t.Errorf("unplaced: %+v", unplaced)
	}
	if doc.Kinds != nil {
		t.Errorf("no kinds block expected for the built-in defaults, got %v", doc.Kinds)
	}
	if doc.Camera == nil || doc.Grid == nil {
		t.Fatal("camera and grid should be fitted")
	}
	// The generated scene passes its own check.
	if errs, _ := Check(doc, rigTags); len(errs) != 0 {
		t.Errorf("generated scene should check clean: %v", errs)
	}
	// A --kind remap lands in kinds: and places the switch.
	doc, unplaced, _ = Generate(rigTags, GenOptions{KindByType: map[string]string{"Switch": "pump"}})
	if len(unplaced) != 0 || doc.Kinds["pump"].Type != "Switch" {
		t.Errorf("remap: unplaced %v kinds %v", unplaced, doc.Kinds)
	}
	// ...and that remap is checked: a Switch has no Running/Fault/Speed.
	// (Fault it has; Running and Speed it lacks.)
	if errs, _ := Check(doc, rigTags); len(errs) != 1 || !strings.Contains(errs[0], "Running/Speed") {
		t.Errorf("remap should be held to the members: %v", errs)
	}
}

func TestEncodeKeepsVectorsOnOneLine(t *testing.T) {
	doc := &Doc{Nodes: []Node{{ID: "a", Kind: "tank", Pos: Vec{1, -0.75, 0.4}}}}
	out, err := Encode(doc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"pos": [1, -0.75, 0.4]`) {
		t.Fatalf("got:\n%s", out)
	}
	if _, err := Parse(out); err != nil {
		t.Fatalf("round trip: %v", err)
	}
}

// ── kinds as data (docs/design/spatial-hmi.md §3c) ────────────────────────

const dataKinds = `{
	"kinds": {
		"pump": {
			"model": "models/pump.glb",
			"status": "{Running?run:stopped} {Speed:0} %",
			"drive": [
				{"mesh": "Coupling", "spin": {"axis": "x", "revPerS": {"bind": "Speed", "scale": 0.02}}},
				{"mesh": "Motor", "tint": {"bind": "Running", "on": "running"}},
				{"mesh": "Beacon", "emissive": {"bind": "!Fault", "on": "critical", "intensity": 2}}
			]
		},
		"tank": {
			"model": "models/tank.glb", "bounds": {"size": [0.4, 0.4, 0.4], "center": [0, 0.2, 0]}, "labelAt": [0, 0.5, 0],
			"drive": [{"mesh": "Fluid", "scale": {"axis": "y", "to": {"bind": "Level", "scale": 0.01, "min": 0.01}}}]
		},
		"beacon": {"type": "Switch", "model": "models/beacon.glb", "drive": [{"mesh": "Lamp", "visible": {"bind": "Fault"}}]}
	},
	"environment": {"hdri": "env/workshop_1k.hdr", "backdrop": "env/workshop_6k.jpg", "background": "ground", "floor": -0.75, "shadows": true, "fog": {"color": "#8d8a84", "near": 4, "far": 14}},
	"fixtures": [{"kind": "plane", "pos": [0, 0, 0], "size": [4, 3], "texture": {"map": "textures/floor_diff.jpg", "normalMap": "textures/floor_nor.jpg", "repeat": [4, 3]}}],
	"nodes": [
		{"id": "P101", "kind": "pump", "tag": "P101", "pos": [0, 0, 0]},
		{"id": "T101", "kind": "tank", "tag": "T101", "pos": [1, 0, 0]},
		{"id": "SW1", "kind": "beacon", "tag": "SW1", "pos": [2, 0, 0]}
	]
}`

func TestCheckDataKindsAcceptsTheModelledRig(t *testing.T) {
	d := parse(t, dataKinds)
	errs, warns := Check(d, rigTags)
	if len(errs) != 0 || len(warns) != 0 {
		t.Fatalf("want clean, got errs %v warns %v", errs, warns)
	}
	// The members a data kind reads are the drives' and the status'.
	k := d.EffectiveKinds()
	if got := strings.Join(k["pump"].Members, ","); got != "Running,Fault,Speed" {
		t.Errorf("pump members (built-in defaults, drives add nothing new): %s", got)
	}
	if got := strings.Join(k["beacon"].Members, ","); got != "Fault" {
		t.Errorf("beacon members from its drive: %s", got)
	}
	if k["tank"].Model != "models/tank.glb" || len(k["tank"].Drive) != 1 {
		t.Errorf("model and drives should carry through: %+v", k["tank"])
	}
	// The asset list, in path order, is what CheckAssets holds to the files.
	var paths []string
	for _, a := range d.Assets() {
		paths = append(paths, a[1])
	}
	if got := strings.Join(paths, " "); got != "models/beacon.glb models/pump.glb models/tank.glb env/workshop_1k.hdr env/workshop_6k.jpg textures/floor_diff.jpg textures/floor_nor.jpg" {
		t.Errorf("assets: %s", got)
	}
}

func TestCheckDataKindsHoldsTheNodeToTheDriveMembers(t *testing.T) {
	// A drive that reads a member the UDT lacks is the same contract error
	// as a component member: the type is right there.
	d := parse(t, `{"kinds": {"pump": {"model": "models/pump.glb", "drive": [{"mesh": "Coupling", "spin": {"axis": "x", "revPerS": {"bind": "Rpm"}}}]}},
		"nodes": [{"id": "P101", "kind": "pump", "tag": "P101", "pos": [0, 0, 0]}]}`)
	errs, _ := Check(d, rigTags)
	if len(errs) != 1 || !strings.Contains(errs[0], `has no member Rpm`) {
		t.Fatalf("got %v", errs)
	}
	// ...unless the node binds it explicitly, the flat-tag escape hatch.
	d = parse(t, `{"kinds": {"pump": {"model": "models/pump.glb", "drive": [{"mesh": "Coupling", "spin": {"axis": "x", "revPerS": {"bind": "Rpm"}}}]}},
		"nodes": [{"id": "P101", "kind": "pump", "tag": "P101", "pos": [0, 0, 0], "bind": {"rpm": "P101.Speed"}}]}`)
	if errs, _ = Check(d, rigTags); len(errs) != 0 {
		t.Fatalf("bound member should satisfy the drive, got %v", errs)
	}
}

func TestCheckDataKindsStructural(t *testing.T) {
	d := parse(t, `{"kinds": {
		"a": {"model": "http://x/a.glb"},
		"b": {"model": "../a.glb"},
		"c": {"model": "models/c.glb", "bounds": "big", "labelAt": [0, 1], "status": "{Level %"},
		"d": {"drive": [{"mesh": "M", "visible": {"bind": "X"}}]},
		"e": {"model": "models/e.glb", "drive": [
			{"spin": {"axis": "w", "revPerS": {"bind": "1bad"}}},
			{"mesh": "M", "spin": {"axis": "x", "revPerS": {"bind": "Speed"}}, "tint": {"bind": "Running", "on": "running"}},
			{"mesh": "M", "scale": {"axis": "xyz", "to": {"bind": "Level"}}},
			{"mesh": "M", "tint": {"bind": "Running"}},
			{"mesh": "M", "emissive": {"bind": "Fault", "on": "critical", "intensity": -1}}
		]}},
		"environment": {"hdri": "env/x.png", "backdrop": "env/x.txt", "background": "wall", "intensity": -1, "fog": {"near": 5, "far": 2}},
		"fixtures": [{"kind": "box", "pos": [0, 0, 0], "texture": {"map": "a.jpg"}}],
		"nodes": []}`)
	errs, _ := Check(d, rigTags)
	got := joined(errs)
	for _, want := range []string{
		"/kinds/a/model:", "/kinds/b/model:", "/kinds/c/bounds:", "/kinds/c/labelAt:", "/kinds/c/status:",
		"/kinds/d: drive, bounds and labelAt need a model",
		"/kinds/e/drive/0/mesh:", "/kinds/e/drive/0/spin/axis:", "/kinds/e/drive/0/spin/revPerS/bind:",
		"/kinds/e/drive/1: a drive has exactly one of spin, turn, scale, tint, emissive, visible",
		"/kinds/e/drive/3/tint/on:", "/kinds/e/drive/4/emissive/intensity:",
		"/environment/hdri:", "/environment/backdrop:", "/environment/fog/far:", "/environment/background:", "/environment/intensity:",
		"/fixtures/0/texture: only a plane",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "/kinds/e/drive/2") {
		t.Errorf("xyz is a valid scale axis:\n%s", got)
	}
	// A channel outside the vocabulary is an unknown field at parse time.
	if _, err := Parse([]byte(`{"kinds": {"x": {"model": "m.glb", "drive": [{"mesh": "M", "wobble": {}}]}}, "nodes": []}`)); err == nil || !strings.Contains(err.Error(), "wobble") {
		t.Fatalf("want an unknown-field error naming wobble, got %v", err)
	}
}

func TestCheckAssets(t *testing.T) {
	d := parse(t, dataKinds)
	have := map[string]bool{"models/pump.glb": true, "env/workshop_1k.hdr": true, "env/workshop_6k.jpg": true, "textures/floor_diff.jpg": true, "textures/floor_nor.jpg": true}
	errs := CheckAssets(d, func(p string) bool { return have[p] })
	if len(errs) != 2 || !strings.Contains(errs[0], `/kinds/beacon/model: "models/beacon.glb" was not found`) || !strings.Contains(errs[1], "/kinds/tank/model:") {
		t.Fatalf("got %v", errs)
	}
}

// The vocabulary lives in three places — here, hmi-3d's DRIVE_CHANNELS and
// the extension's schema. This reads the schema so a channel added on one
// side without the others fails a test rather than a user.
func TestDriveVocabularyMatchesTheSchema(t *testing.T) {
	data, err := os.ReadFile("../../tools/vscode-iec/schemas/nautilus-scene.schema.json")
	if err != nil {
		t.Skip("schema not in this checkout:", err)
	}
	var schema struct {
		Definitions struct {
			Drive struct {
				Properties map[string]any `json:"properties"`
				OneOf      []struct {
					Required []string `json:"required"`
				} `json:"oneOf"`
			} `json:"drive"`
			Environment struct {
				Properties map[string]any `json:"properties"`
			} `json:"environment"`
		} `json:"definitions"`
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	var fromSchema []string
	for _, o := range schema.Definitions.Drive.OneOf {
		fromSchema = append(fromSchema, o.Required...)
	}
	if strings.Join(fromSchema, ",") != strings.Join(DriveChannels, ",") {
		t.Errorf("schema drive channels %v, Go %v", fromSchema, DriveChannels)
	}
	for _, c := range DriveChannels {
		if _, ok := schema.Definitions.Drive.Properties[c]; !ok {
			t.Errorf("schema drive has no property %q", c)
		}
	}
	for _, f := range []string{"hdri", "backdrop", "background", "floor", "fog", "intensity", "shadows"} {
		if _, ok := schema.Definitions.Environment.Properties[f]; !ok {
			t.Errorf("schema environment has no property %q", f)
		}
	}
}

// Builtin must match hmi-3d's registry AND the models/kinds.json that
// declares the same kinds as data.
func TestBuiltinMatchesTheDataKinds(t *testing.T) {
	data, err := os.ReadFile("../../hmi-3d/models/kinds.json")
	if err != nil {
		t.Skip("hmi-3d not in this checkout:", err)
	}
	var kinds map[string]Kind
	if err := json.Unmarshal(data, &kinds); err != nil {
		t.Fatal(err)
	}
	for name, k := range kinds {
		b, ok := Builtin[name]
		if !ok {
			t.Errorf("kinds.json declares %q, which is not a built-in", name)
			continue
		}
		for _, m := range driveMembers(k.Drive) {
			if !contains(b.Members, m) {
				t.Errorf("%s's drives read %s, which the built-in contract (%v) does not list", name, m, b.Members)
			}
		}
		if k.Model != "models/"+name+".glb" {
			t.Errorf("%s model: %s", name, k.Model)
		}
	}
	if len(kinds) != len(Builtin) {
		t.Errorf("kinds.json has %d kinds, Builtin %d", len(kinds), len(Builtin))
	}
}
