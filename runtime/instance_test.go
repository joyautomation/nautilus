package runtime

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/retain"
)

// Instances (struct wiring, #1458 slice 3; instance.go): one struct tag whose
// members each have a kind. These tests hold the runtime to the brief's
// rules (docs/design/one-struct-model.md §2): a delivery lands only device
// members, an outside write never sets a from: or expr: member, a program's
// commit never does either, and quality is per member with a rolled-up root.

const pumpLib = `
TYPE
  PumpStatus : STRUCT
    Code : INT;
    Note : INT;
  END_STRUCT;
  PumpCmd : STRUCT
    Start    : BOOL;
    Stop     : BOOL;
    SpeedRef : REAL;
  END_STRUCT;
  Pump : STRUCT
    Running  : BOOL;
    Speed    : REAL;
    Fault    : BOOL;
    Healthy  : BOOL;
    RunHours : REAL;
    SpeedSP  : REAL := 50.0;
    Cmd      : PumpCmd;
    Status   : PumpStatus;
  END_STRUCT;
END_TYPE
`

func pumpDef() InstanceDef {
	return InstanceDef{Members: map[string]MemberDef{
		"Running":      {Kind: MemberFrom},
		"Speed":        {Kind: MemberFrom},
		"Fault":        {Kind: MemberFrom},
		"Healthy":      {Kind: MemberExpr, Expr: "Running AND NOT Fault"},
		"RunHours":     {Kind: MemberMemory},
		"Cmd.Start":    {Kind: MemberTo},
		"Cmd.Stop":     {Kind: MemberTo, Event: true},
		"Cmd.SpeedRef": {Kind: MemberTo, Readback: "Speed"},
		"Status.Code":  {Kind: MemberFrom},
	}}
}

// instDriver delivers whatever in holds, reports q, and fails reads on cue.
type instDriver struct {
	mu   sync.Mutex
	in   nio.Values
	q    map[string]nio.Quality
	fail bool
}

func (d *instDriver) ReadInputs() (nio.Values, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.fail {
		return nil, errors.New("the bus is down")
	}
	out := make(nio.Values, len(d.in))
	for k, v := range d.in {
		out[k] = v
	}
	return out, nil
}

func (d *instDriver) WriteOutputs(nio.Values) error { return nil }

func (d *instDriver) Quality() map[string]nio.Quality {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make(map[string]nio.Quality, len(d.q))
	for k, q := range d.q {
		out[k] = q
	}
	return out
}

func (d *instDriver) set(k string, v any) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.in == nil {
		d.in = nio.Values{}
	}
	d.in[k] = v
}

func (d *instDriver) setQ(k string, q nio.Quality) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.q == nil {
		d.q = map[string]nio.Quality{}
	}
	d.q[k] = q
}

func newPumpRuntime(t *testing.T, body string, extra ...TagDef) (*Runtime, *instDriver) {
	t.Helper()
	d := &instDriver{}
	tags := append([]TagDef{InstanceTag("P101", "Pump", pumpDef())}, extra...)
	r, err := New(Options{
		Program:   "PROGRAM Main\nVAR_EXTERNAL P101 : Pump; END_VAR\n" + body + "\nEND_PROGRAM",
		Libraries: []string{pumpLib},
		Driver:    d,
		Tags:      tags,
	})
	if err != nil {
		t.Fatal(err)
	}
	return r, d
}

// pumpValue is a whole Pump value with every member set to something
// recognisable, as a driver that fills the struct whole would deliver it.
func pumpValue(t *testing.T, r *Runtime) ir.Value {
	t.Helper()
	typ := r.tags.types[ir.NameKey("P101")]
	v := ir.Zero(typ)
	for path, val := range map[string]any{
		"Running": true, "Speed": 42.0, "Fault": true, "Healthy": true,
		"RunHours": 999.0, "SpeedSP": 999.0, "Cmd.Start": true, "Cmd.SpeedRef": 999.0,
	} {
		var err error
		if v, err = ir.SetFieldTyped(v, typ, ir.PathSegments(path), val, "test"); err != nil {
			t.Fatal(err)
		}
	}
	return v
}

func writeErr(r *Runtime, path string, v any) error {
	return r.Tags().Write([]WriteOp{{Path: path, Value: v}})[0]
}

// A delivery lands the device's members only: memory, commands and
// expressions keep what they hold, whether the driver sends the struct
// whole or one member at a time.
func TestInstanceDeliveryLeavesMemoryAndCommandsAlone(t *testing.T) {
	r, d := newPumpRuntime(t, "")
	write(t, r, "P101.RunHours", 5.0)
	write(t, r, "P101.Cmd.SpeedRef", 30.0)
	d.set("P101", pumpValue(t, r))
	r.Scan()
	wantPath(t, r, "P101.Running", true)
	wantPath(t, r, "P101.Fault", true)
	wantPath(t, r, "P101.Speed", 42.0)
	wantPath(t, r, "P101.RunHours", 5.0)
	wantPath(t, r, "P101.SpeedSP", 50.0)
	wantPath(t, r, "P101.Cmd.SpeedRef", 30.0)
	wantPath(t, r, "P101.Cmd.Start", false)
	wantPath(t, r, "P101.Healthy", false)

	// Member by member, which mixes across drivers.
	d.set("P101.Speed", 12.5)
	d.set("P101.Running", false)
	d.set("P101.RunHours", 777.0) // not a delivered member: ignored
	r.Scan()
	wantPath(t, r, "P101.Speed", 12.5)
	wantPath(t, r, "P101.Running", false)
	wantPath(t, r, "P101.RunHours", 5.0)
}

// A delivery of another type, or of a value a member cannot hold, changes
// nothing: a mismatched merge must never replace the memory members.
func TestInstanceMismatchedDeliveryChangesNothing(t *testing.T) {
	r, d := newPumpRuntime(t, "")
	write(t, r, "P101.RunHours", 5.0)
	d.set("P101", ir.RealVal(1))
	d.set("P101.Speed", "not a number")
	r.Scan()
	wantPath(t, r, "P101.RunHours", 5.0)
	wantPath(t, r, "P101.Speed", 0.0)
}

// The front door every outside writer uses (the API, OPC UA, Sparkplug)
// refuses a from: or expr: member, and anything holding one, by name.
func TestInstanceFrontDoorRefusesReadOnlyMembers(t *testing.T) {
	r, _ := newPumpRuntime(t, "")
	for _, tc := range []struct {
		path string
		v    any
		want string
	}{
		{"P101.Speed", 1.0, "P101.Speed is read from the device (from:)"},
		{"p101.speed", 1.0, "P101.Speed is read from the device"},
		{"P101.Healthy", true, "P101.Healthy is computed each scan (expr:)"},
		{"P101", map[string]any{"RunHours": 1.0, "Fault": true}, "P101.Fault is read from the device"},
		{"P101", pumpValue(t, r), "write its members by name"},
		{"P101", 1.0, "write its members by name"},
	} {
		err := writeErr(r, tc.path, tc.v)
		if !errors.Is(err, ErrReadOnly) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("write %s = %v: want ErrReadOnly naming %q", tc.path, err, tc.want)
		}
	}
	wantPath(t, r, "P101.Speed", 0.0)
	wantPath(t, r, "P101.Fault", false)
	wantPath(t, r, "P101.RunHours", 0.0) // the refused object wrote nothing

	for _, tc := range []struct {
		path string
		v    any
	}{
		{"P101.RunHours", 3.0},
		{"P101.SpeedSP", 40.0},
		{"P101.Cmd.Start", true},
		{"P101.Cmd", map[string]any{"Stop": true, "SpeedRef": 20.0}},
		{"P101", map[string]any{"RunHours": 4.0, "Cmd": map[string]any{"Start": false}}},
	} {
		if err := writeErr(r, tc.path, tc.v); err != nil {
			t.Errorf("write %s: %v, want it to land", tc.path, err)
		}
	}
	wantPath(t, r, "P101.RunHours", 4.0)
	wantPath(t, r, "P101.SpeedSP", 40.0)
	wantPath(t, r, "P101.Cmd.Stop", true)
	wantPath(t, r, "P101.Cmd.SpeedRef", 20.0)
	wantPath(t, r, "P101.Cmd.Start", false)

	// The all-or-nothing parameter path asks the same question.
	out := r.Tags().WriteSet([]WriteOp{{Path: "P101.Speed", Value: 1.0}}, "op", false)
	if !errors.Is(out[0].Err, ErrReadOnly) {
		t.Errorf("WriteSet of a from: member = %v, want ErrReadOnly", out[0].Err)
	}
}

// Nothing else about the front door changes for a tag that is no instance.
func TestFrontDoorUnchangedForPlainStructs(t *testing.T) {
	r, _ := newPumpRuntime(t, "", Typed("Plain", RoleState, "Pump"))
	for _, p := range []string{"Plain.Speed", "Plain.Healthy"} {
		if err := writeErr(r, p, true); err != nil && errors.Is(err, ErrReadOnly) {
			t.Errorf("write %s on a plain struct: %v", p, err)
		}
	}
}

// A program's commit never lands a from: or expr: member, member assignment
// or whole assignment alike; what it may write, it writes.
func TestInstanceCommitNeverLandsReadOnlyMembers(t *testing.T) {
	r, d := newPumpRuntime(t, "P101.Speed := 99.0;\nP101.Healthy := TRUE;\nP101.RunHours := 3.0;\nP101.Cmd.Start := TRUE;")
	d.set("P101.Speed", 30.0)
	r.Scan()
	wantPath(t, r, "P101.Speed", 30.0)
	wantPath(t, r, "P101.Healthy", false)
	wantPath(t, r, "P101.RunHours", 3.0)
	wantPath(t, r, "P101.Cmd.Start", true)

	// A whole assignment: the read-only members keep the store's.
	spare := Typed("Spare", RoleState, "Pump")
	r2, d2 := newPumpRuntime(t, "VAR_EXTERNAL Spare : Pump; END_VAR\nSpare.Speed := 77.0;\nSpare.Fault := TRUE;\nSpare.RunHours := 8.0;\nP101 := Spare;", spare)
	d2.set("P101.Speed", 30.0)
	r2.Scan()
	wantPath(t, r2, "P101.Speed", 30.0)
	wantPath(t, r2, "P101.Fault", false)
	wantPath(t, r2, "P101.RunHours", 8.0)
}

// The #578 merge holds for an instance: a member an operator writes while
// the program assigns another lands too.
func TestInstanceOperatorWriteMidScanSurvives(t *testing.T) {
	clk := newGateClock()
	d := &instDriver{}
	r, err := New(Options{
		Program: "PROGRAM Main\nVAR_EXTERNAL P101 : Pump; END_VAR\nVAR wall : LOCAL_TIME; END_VAR\nwall();\n" +
			"P101.Cmd.Start := TRUE;\nEND_PROGRAM",
		Libraries: []string{pumpLib}, Driver: d, Clock: clk,
		Tags: []TagDef{InstanceTag("P101", "Pump", pumpDef())},
	})
	if err != nil {
		t.Fatal(err)
	}
	parkedScan(t, r, clk, func() { write(t, r, "P101.RunHours", 6.0) })
	wantPath(t, r, "P101.Cmd.Start", true)
	wantPath(t, r, "P101.RunHours", 6.0)
}

// Quality is per member, and the root is the worst of its delivered
// members by the device library's rule. Memory and commands are Good.
func TestInstanceQualityRollsUp(t *testing.T) {
	r, d := newPumpRuntime(t, "")
	r.Scan()
	if q := r.Quality(); q["P101"] != nio.NotConnected || q["P101.Speed"] != nio.NotConnected {
		t.Errorf("never delivered: quality = %v, want the device members and the root NotConnected", q)
	}
	if q := r.TagQuality("P101.RunHours"); q != nio.Good {
		t.Errorf("a memory member never delivered reads %v, want Good", q)
	}
	d.set("P101", pumpValue(t, r))
	r.Scan()
	if q := r.Quality(); len(q) != 0 {
		t.Fatalf("a healthy instance reports %v", q)
	}
	d.setQ("P101.Speed", nio.Bad)
	q := r.Quality()
	if q["P101.Speed"] != nio.Bad || q["P101"] != nio.Bad {
		t.Errorf("one Bad member: quality = %v, want P101.Speed and P101 Bad", q)
	}
	for path, want := range map[string]nio.Quality{
		"P101": nio.Bad, "P101.Speed": nio.Bad, "P101.Running": nio.Good,
		"P101.RunHours": nio.Good, "P101.Cmd.Start": nio.Good,
	} {
		if got := r.TagQuality(path); got != want {
			t.Errorf("TagQuality(%s) = %v, want %v", path, got, want)
		}
	}
	for _, m := range []string{"P101.Running", "P101.Speed", "P101.Fault", "P101.Status.Code"} {
		d.setQ(m, nio.NotConnected)
	}
	if q := r.Quality(); q["P101"] != nio.NotConnected {
		t.Errorf("no member arrived: root = %v, want NotConnected", q["P101"])
	}
	d.setQ("P101.Running", nio.Good)
	d.setQ("P101.Fault", nio.Good)
	d.setQ("P101.Status.Code", nio.Good)
	d.setQ("P101.Speed", nio.Stale)
	if q := r.Quality(); q["P101"] != nio.Stale {
		t.Errorf("one Stale member: root = %v, want Stale", q["P101"])
	}

	// A failed read makes every delivered member Stale, and the root.
	d.setQ("P101.Speed", nio.Good)
	d.fail = true
	r.Scan()
	q = r.Quality()
	for _, m := range []string{"P101", "P101.Running", "P101.Speed", "P101.Fault", "P101.Status.Code"} {
		if q[m] != nio.Stale {
			t.Errorf("after a failed read: %s = %v, want Stale", m, q[m])
		}
	}
	if _, said := q["P101.RunHours"]; said {
		t.Errorf("a memory member is not a reading, yet reports %v", q["P101.RunHours"])
	}
	if got := r.TagQuality("P101.RunHours"); got != nio.Good {
		t.Errorf("TagQuality of a memory member after a failed read = %v", got)
	}
}

// A force on a from: member holds over the device's value, which becomes
// the member's actual, as for a forced input.
func TestInstanceForceHoldsOverDelivery(t *testing.T) {
	r, d := newPumpRuntime(t, "")
	r.Scan()
	if err := r.Tags().Force("P101.Speed", 1.0); err != nil {
		t.Fatal(err)
	}
	d.set("P101.Speed", 30.0)
	r.Scan()
	wantPath(t, r, "P101.Speed", 1.0)
	r.Tags().Unforce("P101.Speed")
	r.Scan()
	wantPath(t, r, "P101.Speed", 30.0)
}

// A substitute may stand in for a member the instance reads from a device,
// and only for one: memory and commands are not readings.
func TestInstanceSubstituteOnDeviceMembersOnly(t *testing.T) {
	d := &instDriver{}
	r, err := New(Options{
		Program:       "PROGRAM Main\nVAR_EXTERNAL P101 : Pump; END_VAR\nEND_PROGRAM",
		Libraries:     []string{pumpLib},
		Driver:        d,
		Tags:          []TagDef{InstanceTag("P101", "Pump", pumpDef())},
		Substitutable: []string{"P101"},
	})
	if err != nil {
		t.Fatal(err)
	}
	d.set("P101", pumpValue(t, r))
	d.set("P101.Speed", 30.0)
	r.Scan()
	subFor(t, r, "P101.Speed", 55.0, time.Hour, false)
	r.Scan()
	wantPath(t, r, "P101.Speed", 55.0)
	if q := r.TagQuality("P101"); q != nio.Substituted {
		t.Errorf("root quality with a substituted member = %v, want Substituted", q)
	}
	for path, want := range map[string]nio.Quality{"P101.Speed": nio.Substituted, "P101.RunHours": nio.Good, "P101.Cmd.Start": nio.Good, "P101.Running": nio.Good} {
		if q := r.TagQuality(path); q != want {
			t.Errorf("TagQuality(%s) with P101.Speed substituted = %v, want %v", path, q, want)
		}
	}
	if q := r.Quality(); q["P101.Speed"] != nio.Substituted || q["P101"] != nio.Substituted {
		t.Errorf("Quality() with P101.Speed substituted = %v", q)
	}
	// The other gates that admit a substitute ask the same question: a
	// start's validation, and a retained entry's load after a restart.
	if err := r.ValidateStart("P101.Speed", 1.0); err != nil {
		t.Errorf("ValidateStart(P101.Speed): %v", err)
	}
	if err := r.ValidateStart("P101.RunHours", 1.0); err == nil {
		t.Error("ValidateStart(P101.RunHours) passed")
	}
	if _, err := r.loadableLocked(retain.SubstituteRetain{Name: "P101.Speed"}); err != nil {
		t.Errorf("a retained substitute of P101.Speed does not load: %v", err)
	}
	if _, err := r.loadableLocked(retain.SubstituteRetain{Name: "P101.RunHours"}); err == nil {
		t.Error("a retained substitute of a memory member loads")
	}
	for _, addr := range []string{"P101.RunHours", "P101.Cmd.Start", "P101.Healthy", "P101"} {
		if _, err := r.Substitute(addr, SubstituteRequest{Value: 1.0, For: time.Hour, By: "op"}); err == nil {
			t.Errorf("a substitute for %s started; only a device member takes one", addr)
		}
		if err := r.ValidateSubstitute(addr, 1.0); err == nil {
			t.Errorf("ValidateSubstitute(%s) passed", addr)
		}
	}
}

// What a test's given: and a hand composition use (Tags.Set and SetPath)
// may set any member, a from: member included: those are not outside
// writes.
func TestInstanceSetPathIsNotTheFrontDoor(t *testing.T) {
	r, _ := newPumpRuntime(t, "")
	if err := r.Tags().SetPath("P101.Speed", 12.0); err != nil {
		t.Fatalf("SetPath of a from: member: %v", err)
	}
	wantPath(t, r, "P101.Speed", 12.0)
}

// An instance is not a driver input: the outside writers that refuse a
// whole input (OPC UA, Sparkplug, the API) leave it to the front door's
// per-member rule. It is not retained either, not yet.
func TestInstanceIsNoInputAndNotRetained(t *testing.T) {
	r, _ := newPumpRuntime(t, "")
	for _, n := range r.Inputs() {
		if ir.SameName(n, "P101") {
			t.Fatal("an instance is listed as a driver input")
		}
	}
	if !r.isScanFed("P101") {
		t.Error("an instance is fed by each scan's delivery, so a standby marks it Stale")
	}
	r2, err := New(Options{
		Program: "PROGRAM Main\nVAR_EXTERNAL P101 : Pump; END_VAR\nEND_PROGRAM", Libraries: []string{pumpLib},
		Driver: &instDriver{}, Tags: []TagDef{InstanceTag("P101", "Pump", pumpDef())}, Retain: &memStore{},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range r2.retainTags {
		if ir.SameName(n, "P101") {
			t.Error("an instance is in the retain set; memory retention is a later slice")
		}
	}
}

// New refuses an instance it could not hold to its kinds, naming why.
func TestInstanceRefusedAtNew(t *testing.T) {
	bad := func(m map[string]MemberDef) InstanceDef { return InstanceDef{Members: m} }
	for _, tc := range []struct {
		name string
		def  TagDef
		opt  func(*Options)
		want string
	}{
		{"not a struct", InstanceTag("P101", "REAL", bad(nil)), nil, "type REAL is not a STRUCT"},
		{"no type", TagDef{Name: "P101", Role: RoleState, Instance: &InstanceDef{}}, nil, "an instance needs its TYPE"},
		{"a role of its own", TagDef{Name: "P101", Role: RoleInput, Type: "Pump", Instance: &InstanceDef{}}, nil, "an instance has no role of its own"},
		{"a member the type lacks", InstanceTag("P101", "Pump", bad(map[string]MemberDef{"Sped": {Kind: MemberFrom}})), nil, "member Sped: type Pump has no member Sped"},
		{"an index outside the array", InstanceTag("P101", "Pump", bad(map[string]MemberDef{"Speed[2]": {Kind: MemberFrom}})), nil, "indexes something that is not an array"},
		{"a read-back of memory", InstanceTag("P101", "Pump", bad(map[string]MemberDef{"Cmd.SpeedRef": {Kind: MemberTo, Readback: "RunHours"}})), nil, "readback RunHours is memory"},
		{"a read-back on a from", InstanceTag("P101", "Pump", bad(map[string]MemberDef{"Speed": {Kind: MemberFrom, Readback: "Fault"}})), nil, "Readback is for a MemberTo"},
		{"event on a from", InstanceTag("P101", "Pump", bad(map[string]MemberDef{"Speed": {Kind: MemberFrom, Event: true}})), nil, "Event is for a MemberTo"},
		{"retained whole", InstanceTag("P101", "Pump", pumpDef(), Retained()), nil, "an instance is never retained whole"},
		{"also an input", InstanceTag("P101", "Pump", pumpDef()), func(o *Options) { o.Inputs = []string{"P101"} }, "is also a driver input or output"},
		{"listed twice", InstanceTag("P101", "Pump", bad(map[string]MemberDef{"Speed": {Kind: MemberFrom}, "speed": {Kind: MemberTo}})), nil, "member speed is listed twice"},
	} {
		o := Options{Program: "PROGRAM Main\nEND_PROGRAM", Libraries: []string{pumpLib}, Driver: &instDriver{}, Tags: []TagDef{tc.def}}
		if tc.opt != nil {
			tc.opt(&o)
		}
		_, err := New(o)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: New = %v, want an error containing %q", tc.name, err, tc.want)
		}
	}
}

// A delivery merges onto the member values beneath any force: a force on a
// memory member must not become that member's value when a device member
// is delivered meanwhile and the force then ends.
func TestInstanceDeliveryKeepsForcesOutOfTheActual(t *testing.T) {
	r, d := newPumpRuntime(t, "")
	write(t, r, "P101.RunHours", 5.0)
	r.Scan()
	if err := r.Tags().Force("P101.RunHours", 9.0); err != nil {
		t.Fatal(err)
	}
	d.set("P101.Speed", 30.0)
	r.Scan()
	wantPath(t, r, "P101.RunHours", 9.0)
	r.Tags().Unforce("P101.RunHours")
	wantPath(t, r, "P101.RunHours", 5.0)
	wantPath(t, r, "P101.Speed", 30.0)
}

// A whole sub-struct that holds a from: member is refused as a value, since
// it would set that member too; by name, its other members are written.
func TestInstanceFrontDoorRefusesAStructHoldingAReadOnlyMember(t *testing.T) {
	r, _ := newPumpRuntime(t, "")
	st := ir.Zero(r.tags.types[ir.NameKey("P101")].Struct.Fields[7].Type)
	if err := writeErr(r, "P101.Status", st); !errors.Is(err, ErrReadOnly) || !strings.Contains(err.Error(), "P101.Status.Code is read from the device") {
		t.Errorf("write of the whole Status = %v, want ErrReadOnly naming Status.Code", err)
	}
	if err := writeErr(r, "P101.Status", map[string]any{"Note": 4}); err != nil {
		t.Errorf("write of Status.Note by name: %v", err)
	}
	wantPath(t, r, "P101.Status.Note", int64(4))
}
