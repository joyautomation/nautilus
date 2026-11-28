package runtime

import (
	"errors"
	"strings"
	"testing"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
)

// The cases the slice 3a review raised: index spellings, a nested kind
// override, a driver's own StructDef, a recompiled TYPE, a dropped delivery,
// a hot install, a root-only verdict, both: members and the retain list.

const tankLib = `
TYPE
  Sensor : STRUCT
    Gain   : REAL;
    Offset : REAL;
  END_STRUCT;
  Tank : STRUCT
    Levels : ARRAY[1..3] OF REAL;
    Sensor : Sensor;
    Valve  : REAL;
    Note   : INT;
  END_STRUCT;
END_TYPE
`

func tankDef() InstanceDef {
	return InstanceDef{Members: map[string]MemberDef{
		"Levels[1]":     {Kind: MemberFrom},
		"Sensor":        {Kind: MemberFrom},
		"Sensor.Offset": {Kind: MemberMemory},
		"Valve":         {Kind: MemberBoth},
	}}
}

func newTankRuntime(t *testing.T, opts ...func(*Options)) (*Runtime, *instDriver) {
	t.Helper()
	d := &instDriver{}
	o := Options{
		Program:   "PROGRAM Main\nVAR_EXTERNAL T1 : Tank; END_VAR\nEND_PROGRAM",
		Libraries: []string{tankLib}, Driver: d,
		Tags: []TagDef{InstanceTag("T1", "Tank", tankDef())},
	}
	for _, f := range opts {
		f(&o)
	}
	r, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	return r, d
}

// An index is resolved as its number, however it is spelled, so no
// spelling slips a write past the front door.
func TestInstanceIndexSpellingsAreOnePath(t *testing.T) {
	r, _ := newTankRuntime(t)
	for _, p := range []string{"T1.Levels[1]", "T1.Levels[01]", "T1.Levels[+1]", "T1.Levels[ 1 ]", "t1.levels[1]"} {
		if err := writeErr(r, p, 5.0); !errors.Is(err, ErrReadOnly) {
			t.Errorf("write %s = %v, want ErrReadOnly", p, err)
		}
	}
	wantPath(t, r, "T1.Levels[1]", 0.0)
	if err := writeErr(r, "T1.Levels[02]", 5.0); err != nil {
		t.Errorf("Levels[2] is memory: %v", err)
	}
	if !r.substitutableAt("T1.Levels[01]", "T1") || r.substitutableAt("T1.Levels[2]", "T1") {
		t.Error("substitutableAt does not resolve index spellings as one path")
	}
}

// A memory member below a from: struct is memory to every rule: a delivery
// of the struct leaves it alone, the front door writes it, a substitute does
// not cover the struct, and its quality is Good.
func TestInstanceNestedOverrideHoldsEverywhere(t *testing.T) {
	r, d := newTankRuntime(t)
	write(t, r, "T1.Sensor.Offset", 2.0)
	typ := r.tags.types[ir.NameKey("T1")]
	sensor := ir.Zero(typ.Struct.Fields[1].Type)
	sensor.Fld[0], sensor.Fld[1] = ir.RealVal(3), ir.RealVal(9)
	d.set("T1.Sensor", sensor)
	r.Scan()
	wantPath(t, r, "T1.Sensor.Gain", 3.0)
	wantPath(t, r, "T1.Sensor.Offset", 2.0)
	if err := writeErr(r, "T1.Sensor.Gain", 1.0); !errors.Is(err, ErrReadOnly) {
		t.Errorf("Sensor.Gain is from: %v", err)
	}
	if err := writeErr(r, "T1.Sensor.Offset", 4.0); err != nil {
		t.Errorf("Sensor.Offset is memory: %v", err)
	}
	if err := writeErr(r, "T1.Sensor", map[string]any{"Offset": 5.0}); err != nil {
		t.Errorf("Sensor by name, memory only: %v", err)
	}
	if r.substitutableAt("T1.Sensor", "T1") || !r.substitutableAt("T1.Sensor.Gain", "T1") {
		t.Error("a substitute may cover Sensor, which holds memory, or may not cover Sensor.Gain")
	}
	if q := r.TagQuality("T1.Sensor.Offset"); q != nio.Good {
		t.Errorf("TagQuality(Sensor.Offset) = %v", q)
	}
}

// A driver builds its own StructDef: a value of the same members delivers,
// and the store keeps its own shape. One of other members is dropped,
// counted, and makes the instance Bad until a delivery lands.
func TestInstanceDeliveryMatchesByMemberNotPointer(t *testing.T) {
	r, d := newPumpRuntime(t, "")
	write(t, r, "P101.RunHours", 5.0)
	own := pumpValue(t, r)
	foreign := *own.Struct
	v := own
	v.Struct = &foreign
	d.set("P101", v)
	r.Scan()
	wantPath(t, r, "P101.Speed", 42.0)
	wantPath(t, r, "P101.RunHours", 5.0)
	if got, _ := r.Tags().ReadGlobal("P101"); got.Struct != own.Struct {
		t.Error("the store took the driver's StructDef")
	}

	other := foreign
	other.Fields = append([]ir.StructField(nil), foreign.Fields...)
	other.Fields[1].Name = "Spd"
	v.Struct = &other
	d.set("P101", v)
	r.Scan()
	in := r.tags.instanceOf("P101")
	if in.drops.Load() == 0 || r.TagQuality("P101") != nio.Bad {
		t.Errorf("a delivery of other members: drops %d, root %v, want counted and Bad", in.drops.Load(), r.TagQuality("P101"))
	}
	wantPath(t, r, "P101.RunHours", 5.0)
	d.set("P101", own)
	r.Scan()
	if in.dropped.Load() || r.TagQuality("P101") != nio.Good {
		t.Errorf("after a good delivery: dropped %v, root %v", in.dropped.Load(), r.TagQuality("P101"))
	}
}

// A program's whole assignment of a value whose StructDef is not the
// store's (a TYPE recompiled by an online edit) still lands its writable
// members, the read-only ones restored.
func TestInstanceCommitAfterARecompiledType(t *testing.T) {
	r, _ := newPumpRuntime(t, "")
	in := r.tags.instanceOf("P101")
	stored, _ := r.Tags().ReadGlobal("P101")
	committed := pumpValue(t, r)
	recompiled := *committed.Struct
	committed.Struct = &recompiled
	got := in.stripReadOnly(committed, stored)
	if f := got.Fld[4].F; f != 999 { // RunHours: memory, the program's
		t.Errorf("RunHours = %v, want the program's 999", f)
	}
	if f := got.Fld[1].F; f != 0 { // Speed: from, the store's
		t.Errorf("Speed = %v, want the store's 0", f)
	}
	if !got.Fld[6].Fld[0].B { // Cmd.Start: to, the program's
		t.Error("Cmd.Start lost")
	}
}

// A hot takeover's image brings memory and commands, never a delivered
// member: like an input, that is the new leader's to read.
func TestInstanceHotInstallKeepsDeliveredMembers(t *testing.T) {
	r, d := newPumpRuntime(t, "")
	d.set("P101.Speed", 30.0)
	r.Scan()
	img := pumpValue(t, r) // Speed 42, RunHours 999, Cmd.Start TRUE
	r.tags.installCarried("P101", img)
	wantPath(t, r, "P101.Speed", 30.0)
	wantPath(t, r, "P101.RunHours", 999.0)
	wantPath(t, r, "P101.Cmd.Start", true)
}

// A driver's verdict on the root reaches each member it said nothing about.
func TestInstanceRootVerdictReachesMembers(t *testing.T) {
	r, d := newPumpRuntime(t, "")
	d.set("P101", pumpValue(t, r))
	r.Scan()
	d.setQ("P101", nio.NotConnected)
	q := r.Quality()
	if q["P101"] != nio.NotConnected || q["P101.Speed"] != nio.NotConnected {
		t.Errorf("root-only verdict: %v", q)
	}
	if got := r.TagQuality("P101.Speed"); got != nio.NotConnected {
		t.Errorf("TagQuality(P101.Speed) = %v", got)
	}
}

// A both: member is delivered, and written from outside.
func TestInstanceBothMember(t *testing.T) {
	r, d := newTankRuntime(t)
	d.set("T1.Valve", 12.0)
	r.Scan()
	wantPath(t, r, "T1.Valve", 12.0)
	if err := writeErr(r, "T1.Valve", 20.0); err != nil {
		t.Errorf("a both: member is writable: %v", err)
	}
}

// An instance named in the retain list is refused: a restart must not
// bring back a command or an old reading.
func TestInstanceInRetainTagsRefused(t *testing.T) {
	_, err := New(Options{
		Program: "PROGRAM Main\nEND_PROGRAM", Libraries: []string{pumpLib}, Driver: &instDriver{},
		Tags: []TagDef{InstanceTag("P101", "Pump", pumpDef())}, Retain: &memStore{}, RetainTags: []string{"p101"},
	})
	if err == nil || !strings.Contains(err.Error(), "never retained whole") {
		t.Errorf("New = %v", err)
	}
}

// Through a real hot takeover: the image brings the old leader's memory
// and commands, and never its readings, which the new leader reads itself.
func TestInstanceHotTakeoverKeepsDeliveredMembers(t *testing.T) {
	withPump := func(o *Options) {
		o.Libraries = []string{pumpLib}
		o.Tags = append(o.Tags, InstanceTag("P101", "Pump", pumpDef()))
	}
	r := newHotRig(t, hotMain, hotMain, withPump)
	if err := r.lead.Tags().SetPath("P101.Speed", 42.0); err != nil {
		t.Fatal(err)
	}
	write(t, r.lead, "P101.RunHours", 7.0)
	write(t, r.lead, "P101.Cmd.Start", true)
	if err := r.stby.Tags().SetPath("P101.Speed", 30.0); err != nil {
		t.Fatal(err)
	}
	r.stream(t, 3)
	// On the standby, before it leads: the readings and the commands are
	// Stale (the scan refreshes them), memory is the operator's.
	for path, want := range map[string]nio.Quality{"P101": nio.Stale, "P101.Speed": nio.Stale, "P101.Cmd.Start": nio.Stale, "P101.RunHours": nio.Good} {
		if q := r.stby.TagQuality(path); q != want {
			t.Errorf("standby TagQuality(%s) = %v, want %v", path, q, want)
		}
	}
	if rep := r.takeOver(t); !rep.Hot {
		t.Fatalf("takeover %+v, want hot", rep)
	}
	wantPath(t, r.stby, "P101.RunHours", 7.0)
	wantPath(t, r.stby, "P101.Cmd.Start", true)
	wantPath(t, r.stby, "P101.Speed", 30.0)
}

// A member declared with an odd index spelling is the same member.
func TestInstanceDeclaredIndexSpelling(t *testing.T) {
	def := InstanceDef{Members: map[string]MemberDef{"Levels[02]": {Kind: MemberFrom}}}
	r, _ := newTankRuntime(t, func(o *Options) { o.Tags = []TagDef{InstanceTag("T1", "Tank", def)} })
	if err := writeErr(r, "T1.Levels[2]", 1.0); !errors.Is(err, ErrReadOnly) {
		t.Errorf("Levels[02] declared from:, a write to Levels[2] = %v", err)
	}
}

// A path below a read-only leaf (a bit of a from: word) is refused too.
func TestInstanceBitOfAReadOnlyMember(t *testing.T) {
	r, _ := newPumpRuntime(t, "")
	if err := writeErr(r, "P101.Status.Code.3", true); !errors.Is(err, ErrReadOnly) {
		t.Errorf("a bit of a from: member = %v, want ErrReadOnly", err)
	}
	wantPath(t, r, "P101.Status.Code", int64(0))
	// A path that names no member, inside a struct holding a read-only one,
	// fails closed.
	if err := writeErr(r, "P101.Status.Bogus", 1); !errors.Is(err, ErrReadOnly) {
		t.Errorf("an unresolvable path into Status = %v, want ErrReadOnly", err)
	}
}

// Names resolve exactly as ir resolves them (case folding, not upper
// case): the Kelvin sign K folds to k, so it must not slip a write past
// the front door to a from: member, nor a declaration miss its member.
func TestInstanceNamesFoldAsIRFoldsThem(t *testing.T) {
	const lib = "TYPE Loop : STRUCT Kp : REAL; Ki : REAL; END_STRUCT; END_TYPE"
	kelvin := "Kp" // the Kelvin sign, then p
	for _, decl := range []string{"Kp", kelvin} {
		r, err := New(Options{
			Program: "PROGRAM Main\nEND_PROGRAM", Libraries: []string{lib}, Driver: &instDriver{},
			Tags: []TagDef{InstanceTag("L1", "Loop", InstanceDef{Members: map[string]MemberDef{decl: {Kind: MemberFrom}}})},
		})
		if err != nil {
			t.Fatalf("declared %q: %v", decl, err)
		}
		for _, w := range []struct {
			path string
			v    any
		}{{"L1.Kp", 5.0}, {"L1." + kelvin, 5.0}, {"L1", map[string]any{kelvin: 6.0}}} {
			if err := writeErr(r, w.path, w.v); !errors.Is(err, ErrReadOnly) {
				t.Errorf("declared %q, write %q = %v, want ErrReadOnly", decl, w.path, err)
			}
		}
		wantPath(t, r, "L1.Kp", 0.0)
		if err := writeErr(r, "L1.Ki", 1.0); err != nil {
			t.Errorf("Ki is memory: %v", err)
		}
		r.Scan()
		if q := r.TagQuality("L1." + kelvin); q != nio.NotConnected {
			t.Errorf("TagQuality via the Kelvin spelling = %v, want the member's NotConnected", q)
		}
	}
}

// A struct delivered under a key whose leaves are all delivered still has
// to have the member's shape: another is refused and counted, and the key
// reads Bad.
func TestInstanceMisShapedStructUnderOneKeyIsRefused(t *testing.T) {
	def := InstanceDef{Members: map[string]MemberDef{"Sensor": {Kind: MemberFrom}}}
	r, d := newTankRuntime(t, func(o *Options) { o.Tags = []TagDef{InstanceTag("T1", "Tank", def)} })
	one := ir.StructDef{Name: "One", Fields: []ir.StructField{{Name: "X", Type: &ir.Type{Kind: ir.TypeReal}}}}
	d.set("T1.Sensor", ir.Value{Kind: ir.TypeStruct, Struct: &one, Fld: []ir.Value{ir.RealVal(1)}})
	r.Scan()
	in := r.tags.instanceOf("T1")
	if in.drops.Load() == 0 {
		t.Error("a mis-shaped struct was not counted")
	}
	if got, _ := r.Tags().ReadGlobal("T1"); len(got.Fld[1].Fld) != 2 {
		t.Fatalf("the store took the mis-shaped struct: %+v", got.Fld[1])
	}
	if q := r.TagQuality("T1.Sensor"); q != nio.Bad {
		t.Errorf("the refused key reads %v, want Bad", q)
	}
}

// The driver's own quality map is read, never edited, also on a standby:
// a driver may hand back the same map every call.
type ownMapDriver struct {
	instDriver
	q map[string]nio.Quality
}

func (d *ownMapDriver) Quality() map[string]nio.Quality { return d.q }

func TestInstanceQualityLeavesTheDriversMapAlone(t *testing.T) {
	d := &ownMapDriver{q: map[string]nio.Quality{"P101.Speed": nio.Good, "P101.Fault": nio.Substituted}}
	r, err := New(Options{
		Program: "PROGRAM Main\nEND_PROGRAM", Libraries: []string{pumpLib}, Driver: d,
		Tags: []TagDef{InstanceTag("P101", "Pump", pumpDef())},
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = r.driverSaid(true)
	_ = r.TagQuality("P101.Speed")
	if len(d.q) != 2 {
		t.Errorf("the driver's map was edited: %v", d.q)
	}
}
