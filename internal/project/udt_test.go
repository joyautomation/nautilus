package project

// A tag may name a UDT the project's ST declares. The point is that ONE
// declaration serves the logic, the tag store, the driver, and the HMI — so
// `type: Motor` resolves against the very TYPE the programs bind, and there
// is no second place for the shape to be described (or to drift).

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/runtime"
)

const motorLib = `TYPE
  Motor : STRUCT
    Running : BOOL;
    Speed   : REAL;
  END_STRUCT;
END_TYPE
`

const motorProgram = `PROGRAM Main
VAR_EXTERNAL
    P101 : Motor;
    Duty : REAL;
END_VAR
Duty := P101.Speed;
END_PROGRAM`

func udtProject(tags string) fstest.MapFS {
	return fstest.MapFS{
		"nautilus.yaml": &fstest.MapFile{Data: []byte(
			"tasks:\n  - program: program.st\ntags:\n" + tags)},
		"motor.st":   &fstest.MapFile{Data: []byte(motorLib)},
		"program.st": &fstest.MapFile{Data: []byte(motorProgram)},
	}
}

// A typed setpoint seeds zero-of-type: it exists on scan one with its fields
// correctly NAMED and its StructDef attached, which is what lets an HMI or a
// field-bus driver bind fields without a second schema.
func TestTypedTagSeedsZeroOfType(t *testing.T) {
	proj, err := Load(udtProject(
		"  - { name: P101, role: setpoint, type: Motor }\n"+
			"  - { name: Duty, role: state, init: 0.0 }\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	rt, err := runtime.New(proj.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	v, err := rt.Tags().ReadGlobal("P101")
	if err != nil {
		t.Fatalf("a typed setpoint should exist on scan one: %v", err)
	}
	if v.Kind != ir.TypeStruct {
		t.Fatalf("P101 seeded as %v, want a struct", v.Kind)
	}
	if v.Struct == nil || v.Struct.Name != "Motor" {
		t.Fatalf("P101 carries no Motor StructDef: %+v", v.Struct)
	}
	if len(v.Fld) != 2 {
		t.Fatalf("Motor seeded with %d fields, want 2", len(v.Fld))
	}
	if got := v.Struct.Fields[1].Name; got != "Speed" {
		t.Errorf("second field is %q, want Speed", got)
	}
}

// An INPUT stays unseeded even when typed. Seeding it would erase the loud
// failure RoleInput exists to produce: a driver that never delivers must
// fault, not run on a silent zero.
func TestTypedInputStaysUnseeded(t *testing.T) {
	proj, err := Load(udtProject(
		"  - { name: P101, role: input, type: Motor }\n"+
			"  - { name: Duty, role: state, init: 0.0 }\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	rt, err := runtime.New(proj.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.Tags().ReadGlobal("P101"); err == nil {
		t.Error("a typed input was seeded — a driver that never delivers must fault")
	}
}

// The type comes from the project's ST, so a name the ST does not declare is
// an error naming what it does — not a tag that silently becomes something
// else.
func TestUnknownTypeIsAnError(t *testing.T) {
	proj, err := Load(udtProject(
		"  - { name: P101, role: setpoint, type: Pump }\n"+
			"  - { name: Duty, role: state, init: 0.0 }\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.New(proj.Runtime)
	if err == nil {
		t.Fatal("a tag naming an undeclared TYPE was accepted")
	}
	if !strings.Contains(err.Error(), "Pump") || !strings.Contains(err.Error(), "Motor") {
		t.Errorf("error should name the missing type and list what is declared, got: %v", err)
	}
}

// Before type:, a setpoint or state REQUIRED init — there was no other way
// to know its shape. A type supplies one, so the rule is now init-or-type.
func TestSeededRolesAcceptTypeInsteadOfInit(t *testing.T) {
	for _, role := range []string{"setpoint", "state"} {
		if _, err := tagDefs([]TagConfig{{Name: "P101", Role: role, Type: "Motor"}}); err != nil {
			t.Errorf("%s with a type but no init was rejected: %v", role, err)
		}
		_, err := tagDefs([]TagConfig{{Name: "P101", Role: role}})
		if err == nil {
			t.Errorf("%s with neither init nor type was accepted", role)
		} else if !strings.Contains(err.Error(), "type") {
			t.Errorf("%s error should mention type as the alternative: %v", role, err)
		}
	}
}

// tag-meta layers documentation onto a tag declared elsewhere — the case
// that makes a generated tag file usable when the generator cannot supply
// descriptions. It must WIN over what the generator wrote, or it could not
// correct a stale one.
func TestTagMetaLayersOntoDeclaredTags(t *testing.T) {
	fs := fstest.MapFS{
		"nautilus.yaml": &fstest.MapFile{Data: []byte(`
tasks:
  - program: program.st
tag-files: [tags/gen.yaml]
tags:
  - { name: Duty, role: state, init: 0.0 }
tag-meta:
  P101: { unit: rpm, desc: "Transfer pump" }
  P101.Speed: { unit: rpm }
`)},
		"tags/gen.yaml": &fstest.MapFile{Data: []byte(
			"- { name: P101, role: setpoint, type: Motor, desc: \"generated, stale\" }\n")},
		"motor.st":   &fstest.MapFile{Data: []byte(motorLib)},
		"program.st": &fstest.MapFile{Data: []byte(motorProgram)},
	}
	proj, err := Load(fs, "")
	if err != nil {
		t.Fatal(err)
	}
	var p101 *runtime.TagDef
	for i := range proj.Runtime.Tags {
		if proj.Runtime.Tags[i].Name == "P101" {
			p101 = &proj.Runtime.Tags[i]
		}
	}
	if p101 == nil {
		t.Fatal("P101 did not compose into the tag list")
	}
	if p101.Meta.Unit != "rpm" {
		t.Errorf("tag-meta unit did not reach the tag: %+v", p101.Meta)
	}
	if p101.Meta.Desc != "Transfer pump" {
		t.Errorf("tag-meta desc must win over the generated one, got %q", p101.Meta.Desc)
	}

	// A dotted key names a field, so it matches no tag and passes through to
	// the meta map — the key space is plain strings, which is why per-field
	// documentation needed no new type.
	rt, err := runtime.New(proj.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	if got := rt.Meta()["P101.Speed"].Unit; got != "rpm" {
		t.Errorf("per-field meta did not survive to the runtime: %q", got)
	}
}
