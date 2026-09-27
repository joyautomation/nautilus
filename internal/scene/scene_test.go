package scene

import (
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
