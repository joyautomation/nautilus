package project

// A tag may name a UDT the project's ST declares. The point is that ONE
// declaration serves the logic, the tag store, the driver, and the HMI — so
// `type: Motor` resolves against the very TYPE the programs bind, and there
// is no second place for the shape to be described (or to drift).

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/joyautomation/nautilus/internal/tagfile"
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

// ---------------------------------------------------------------------------
// Per-member init: a struct-typed tag's `init:` may be a (possibly nested)
// map of member name to value instead of only zero-of-type. See
// docs/design/tags.md §4.4 ("Deferred: init: as a field mapping") and
// lang/ir/seed.go (SeedFromInit).

const levelLib = `TYPE
  Level : STRUCT
    CTL1HSP : REAL;
    CTL1LSP : REAL;
  END_STRUCT;
  Pump : STRUCT
    STRTTMRSP : INT;
    REMOTE    : BOOL;
    LVL       : Level;
  END_STRUCT;
END_TYPE
`

const pumpProgram = `PROGRAM Main
VAR_EXTERNAL
    P101 : Pump;
END_VAR
END_PROGRAM`

// pumpProject builds a project around the Level/Pump UDTs above, with the
// given tags: block (and, optionally, a tags/gen.yaml tag-files: entry —
// pass "" for tagFile to skip it).
func pumpProject(tags, tagFile string) fstest.MapFS {
	manifest := "tasks:\n  - program: program.st\n"
	if tagFile != "" {
		manifest += "tag-files: [tags/gen.yaml]\n"
	}
	manifest += "tags:\n" + tags
	fs := fstest.MapFS{
		"nautilus.yaml": &fstest.MapFile{Data: []byte(manifest)},
		"level.st":      &fstest.MapFile{Data: []byte(levelLib)},
		"program.st":    &fstest.MapFile{Data: []byte(pumpProgram)},
	}
	if tagFile != "" {
		fs["tags/gen.yaml"] = &fstest.MapFile{Data: []byte(tagFile)}
	}
	return fs
}

// A nested init map seeds exactly the members it names — at any depth — and
// leaves everything else at zero-of-field-type, which is the whole point:
// a site's ~55-field first-scan CfgDone block collapses into the manifest.
func TestStructInitSeedsNestedMembersOthersZero(t *testing.T) {
	proj, err := Load(pumpProject(
		"  - { name: P101, role: state, type: Pump, init: { STRTTMRSP: 30, LVL: { CTL1HSP: 60.0, CTL1LSP: 40.0 } } }\n", ""), "")
	if err != nil {
		t.Fatal(err)
	}
	rt, err := runtime.New(proj.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	v, err := rt.Tags().ReadGlobal("P101")
	if err != nil {
		t.Fatal(err)
	}
	if got := v.Fld[0].I; got != 30 {
		t.Errorf("STRTTMRSP = %d, want 30", got)
	}
	if got := v.Fld[1].B; got != false {
		t.Errorf("REMOTE = %v, want zero (false) — not named in init", got)
	}
	lvl := v.Fld[2]
	if got := lvl.Fld[0].F; got != 60.0 {
		t.Errorf("LVL.CTL1HSP = %v, want 60", got)
	}
	if got := lvl.Fld[1].F; got != 40.0 {
		t.Errorf("LVL.CTL1LSP = %v, want 40", got)
	}
}

// The same shape, composed through tag-files: rather than the inline tags:
// block — composeTags folds both into one list ahead of tagDefs/expandTags,
// so a generated tag file gets exactly the same per-member seeding.
func TestStructInitWorksThroughTagFiles(t *testing.T) {
	proj, err := Load(pumpProject("",
		"- { name: P101, role: state, type: Pump, init: { STRTTMRSP: 45 } }\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	rt, err := runtime.New(proj.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	v, err := rt.Tags().ReadGlobal("P101")
	if err != nil {
		t.Fatal(err)
	}
	if got := v.Fld[0].I; got != 45 {
		t.Errorf("STRTTMRSP = %d, want 45", got)
	}
}

// An unknown member name is a load error naming the tag and the member —
// not a silently ignored key.
func TestStructInitUnknownMemberIsAnError(t *testing.T) {
	proj, err := Load(pumpProject(
		"  - { name: P101, role: state, type: Pump, init: { STRTTMRS: 30 } }\n", ""), "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.New(proj.Runtime)
	if err == nil {
		t.Fatal("an unknown init member was accepted")
	}
	const want = "tag P101: init: unknown member STRTTMRS (did you mean STRTTMRSP?)"
	if !strings.Contains(err.Error(), want) {
		t.Errorf("error = %q, want it to contain %q", err.Error(), want)
	}
}

// A struct-typed tag given a bare scalar init is a load error, not a value
// that quietly does nothing (Tags.Set's switch has no case for a struct
// tag's raw manifest value, so before SeedFromInit this failed silently).
func TestStructInitScalarOnStructIsAnError(t *testing.T) {
	proj, err := Load(pumpProject(
		"  - { name: P101, role: state, type: Pump, init: 42.0 }\n", ""), "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.New(proj.Runtime)
	if err == nil {
		t.Fatal("a scalar init against a struct-typed tag was accepted")
	}
	if !strings.Contains(err.Error(), "P101") {
		t.Errorf("error should name the tag: %v", err)
	}
}

// The generator round trip: internal/tagfile.Render emits a struct's
// per-member init as a flow-style mapping (tagfile_test.go), and this proves
// the other half — that a tag file containing exactly that shape composes
// and seeds the same way a hand-written one does. This is the path
// `nautilus eip tags` / `nautilus tags import-csv` output would take.
func TestStructInitRenderRoundTripsThroughTagFile(t *testing.T) {
	raw, err := tagfile.Render(nil, []tagfile.Tag{{
		Name: "P101", Role: "state", Type: "Pump",
		Init: map[string]any{
			"STRTTMRSP": 30,
			"LVL":       map[string]any{"CTL1HSP": 60.0, "CTL1LSP": 40.0},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	proj, err := Load(pumpProject("", string(raw)), "")
	if err != nil {
		t.Fatal(err)
	}
	rt, err := runtime.New(proj.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	v, err := rt.Tags().ReadGlobal("P101")
	if err != nil {
		t.Fatal(err)
	}
	if got := v.Fld[0].I; got != 30 {
		t.Errorf("STRTTMRSP = %d, want 30", got)
	}
	lvl := v.Fld[2]
	if got := lvl.Fld[0].F; got != 60.0 {
		t.Errorf("LVL.CTL1HSP = %v, want 60", got)
	}
	if got := lvl.Fld[1].F; got != 40.0 {
		t.Errorf("LVL.CTL1LSP = %v, want 40", got)
	}
}
