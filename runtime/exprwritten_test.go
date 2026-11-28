package runtime

import (
	"strings"
	"testing"

	nio "github.com/joyautomation/nautilus/io"
)

// WRITTEN(<path>) in an expr: member (exprwritten.go; one-struct-model §10):
// the push rules' "written in this leadership", read by an expression.

const driveLib = `
TYPE
  DrivePair : STRUCT
    Got : INT;
    Put : INT;
  END_STRUCT;
  DriveCmd : STRUCT
    Start    : BOOL;
    SpeedRef : REAL;
  END_STRUCT;
  Drive : STRUCT
    Speed      : REAL;
    PowerOn    : BOOL;
    Power      : INT;
    Ref        : REAL;
    Cmd        : DriveCmd;
    Lim        : ARRAY[0..1] OF INT;
    Pair       : DrivePair;
    AskedPut   : BOOL;
    Peer       : INT;
    Note       : INT;
    Asked      : BOOL;
    AskedRef   : BOOL;
    AskedStart : BOOL;
    AskedLim   : BOOL;
    Off        : BOOL;
    Other      : BOOL;
  END_STRUCT;
END_TYPE
`

// driveDef: Power reads back as PowerOn (another type: no adoption), Ref as
// Speed (its own type: adopted), Cmd and Lim are listed whole, Pair is
// delivered whole but for its to: leaf Put.
func driveDef() InstanceDef {
	return InstanceDef{Members: map[string]MemberDef{
		"Speed":      {Kind: MemberFrom},
		"PowerOn":    {Kind: MemberFrom},
		"Power":      {Kind: MemberTo, Readback: "PowerOn"},
		"Ref":        {Kind: MemberTo, Readback: "Speed"},
		"Cmd":        {Kind: MemberTo},
		"Lim":        {Kind: MemberTo},
		"Pair":       {Kind: MemberFrom},
		"Pair.Put":   {Kind: MemberTo},
		"AskedPut":   {Kind: MemberExpr, Expr: "WRITTEN(Pair.Put)"},
		"Peer":       {Kind: MemberBoth},
		"Asked":      {Kind: MemberExpr, Expr: "WRITTEN(Power)"},
		"AskedRef":   {Kind: MemberExpr, Expr: "written(Ref)"},
		"AskedStart": {Kind: MemberExpr, Expr: "WRITTEN(Cmd.Start)"},
		"AskedLim":   {Kind: MemberExpr, Expr: "WRITTEN(Lim[1])"},
		"Off":        {Kind: MemberExpr, Expr: "WRITTEN(Power) AND NOT PowerOn"},
		"Other":      {Kind: MemberExpr, Expr: "WRITTEN(D2.Power)"},
	}}
}

func driveOpts(def InstanceDef, lib string, extra ...TagDef) Options {
	return Options{
		Program:   "PROGRAM Main\nVAR_EXTERNAL D1 : Drive; Go : BOOL; END_VAR\nIF Go THEN D1.Power := 1; END_IF\nEND_PROGRAM",
		Libraries: []string{driveLib + lib},
		Tags:      append([]TagDef{InstanceTag("D1", "Drive", def), InstanceTag("D2", "Drive", def), State("Go", false)}, extra...),
	}
}

func newDriveRig(t testing.TB, def InstanceDef) *pushRig {
	t.Helper()
	p := &pushRig{d: &pushDriver{}, lead: &pushLead{lead: true}}
	o := driveOpts(def, "")
	o.Driver, o.Coordinator = p.d, p.lead
	var err error
	if p.r, err = New(o); err != nil {
		t.Fatal(err)
	}
	return p
}

// WRITTEN follows the push rules' written: FALSE until a program, an outside
// write or a force writes the leaf in this leadership; an adopted read-back
// is no write; a takeover starts over. The stage runs before the program, so
// the program's write shows on the next scan. It waits for nothing (it reads
// no value), and its quality is Good.
func TestWrittenFollowsThePushRules(t *testing.T) {
	p := newDriveRig(t, driveDef())
	p.r.Scan()
	for _, m := range []string{"D1.Asked", "D1.AskedRef", "D1.AskedStart", "D1.AskedLim", "D1.Other"} {
		wantPath(t, p.r, m, false)
	}
	// WRITTEN reads no value, so it waits for no delivery: not for Pair,
	// which holds the leaf it asks about.
	for _, m := range []string{"D1.Asked", "D1.AskedPut"} {
		if q := p.r.TagQuality(m); q != nio.Good {
			t.Errorf("%s quality %v, want Good: WRITTEN reads no input", m, q)
		}
	}
	write(t, p.r, "D1.Pair.Put", 2)
	p.r.Scan()
	wantPath(t, p.r, "D1.AskedPut", true)

	// The read-back of Ref's own type is adopted: a value, not a write.
	p.d.set("D1.Speed", 42.0)
	p.d.set("D1.PowerOn", false)
	p.r.Scan()
	wantPath(t, p.r, "D1.Ref", 42.0)
	wantPath(t, p.r, "D1.AskedRef", false)

	// The program's write: seen on the scan after.
	write(t, p.r, "Go", true)
	p.r.Scan()
	wantPath(t, p.r, "D1.Asked", false)
	p.r.Scan()
	wantPath(t, p.r, "D1.Asked", true)
	wantPath(t, p.r, "D1.Off", true) // commanded, PowerOn not on
	wantPath(t, p.r, "D1.Other", false)
	wantPath(t, p.r, "D2.Asked", false) // per instance

	// An outside write, to another instance and into a whole-listed member.
	write(t, p.r, "D2.Power", 1)
	write(t, p.r, "D1.Cmd.Start", true)
	write(t, p.r, "D1.Lim[1]", 5)
	p.r.Scan()
	wantPath(t, p.r, "D1.Other", true)
	wantPath(t, p.r, "D2.Asked", true)
	wantPath(t, p.r, "D1.AskedStart", true)
	wantPath(t, p.r, "D1.AskedLim", true)

	// A hot install recompiles the stage; the written state is the push
	// rules', so it stands.
	if _, err := p.r.prog.SwapWarm(p.r.prog.Source()); err != nil {
		t.Fatal(err)
	}
	p.r.Scan()
	wantPath(t, p.r, "D1.AskedStart", true)
	if _, err := p.r.prog.Rollback(); err != nil {
		t.Fatal(err)
	}
	p.r.Scan()
	wantPath(t, p.r, "D1.AskedStart", true)

	// A takeover: nothing is written in the new leadership; the program
	// writes Power again on its first scan, seen on the next.
	p.takeover()
	wantPath(t, p.r, "D1.AskedStart", false)
	wantPath(t, p.r, "D1.Other", false)
	wantPath(t, p.r, "D1.Asked", false)
	p.r.Scan()
	wantPath(t, p.r, "D1.Asked", true)
	wantPath(t, p.r, "D1.AskedStart", false)
}

// A force writes a to: leaf for as long as it holds (it goes out, as a
// forced output does); released, the leaf is unwritten again, and so is
// WRITTEN.
func TestWrittenForceHoldsThenReleases(t *testing.T) {
	p := newDriveRig(t, driveDef())
	p.r.Scan()
	if err := p.r.Tags().Force("D1.Cmd.Start", true); err != nil {
		t.Fatal(err)
	}
	p.r.Scan()
	wantPath(t, p.r, "D1.AskedStart", true)
	p.r.Tags().Unforce("D1.Cmd.Start")
	p.r.Scan()
	wantPath(t, p.r, "D1.AskedStart", false)
}

// A path that is not one to: leaf is refused at New, naming the member, its
// kind and a leaf that would do.
func TestWrittenRefusals(t *testing.T) {
	for name, c := range map[string]struct{ expr, want string }{
		"from":         {"WRITTEN(Speed)", "Speed of Drive is a from: member, and WRITTEN tells whether a to: member has been written in this leadership; Power of Drive is one"},
		"both":         {"WRITTEN(Peer)", "Peer of Drive is a both: member"},
		"memory":       {"WRITTEN(Note)", "Note of Drive is a memory: member"},
		"expr":         {"WRITTEN(Other)", "Other of Drive is an expr: member"},
		"group":        {"WRITTEN(Cmd)", "Cmd of Drive is not one leaf: name one to: member; Power of Drive is one"},
		"no member":    {"WRITTEN(Cmd.Nope)", "Drive has no member Cmd.Nope"},
		"no name":      {"WRITTEN(Nope)", "Nope is no member of the expression's instance and no instance"},
		"no index":     {"WRITTEN(Lim[7])", "Drive has no member Lim[7]"},
		"plain tag":    {"WRITTEN(Go)", "Go is no member of the expression's instance and no instance: WRITTEN takes a to: member of an instance"},
		"the instance": {"WRITTEN(D2)", "it names the instance D2, not one of its to: members"},
		"not a path":   {"WRITTEN(Power + 1)", "WRITTEN takes one argument, the path of a to: member with integer literal indices, one per bracket (Grid[0][1])"},
		"var index":    {"WRITTEN(Lim[Power])", "WRITTEN takes one argument"},
		"two args":     {"WRITTEN(Power, Ref)", "WRITTEN takes one argument"},
		"no args":      {"WRITTEN()", "WRITTEN takes one argument"},
		"named":        {"WRITTEN(x := Power)", "WRITTEN takes one argument"},
		"plus a named": {"WRITTEN(Power, x := Ref)", "WRITTEN takes one argument"},
		// A FUNCTION never has WRITTEN, so the program's own compile refuses it.
		"in a FUNCTION": {"Wr(Power)", `unknown function "WRITTEN": WRITTEN(<path>) is an expr: member's built-in, not a program's or a FUNCTION's`},
	} {
		def := driveDef()
		def.Members["Asked"] = MemberDef{Kind: MemberExpr, Expr: c.expr}
		lib := ""
		if name == "in a FUNCTION" {
			lib = "FUNCTION Wr : BOOL\nVAR_INPUT x : INT; END_VAR\nWr := WRITTEN(x);\nEND_FUNCTION\n"
		}
		o := driveOpts(def, lib)
		o.Program = "PROGRAM Main\nEND_PROGRAM"
		o.Driver = &countDriver{}
		_, err := New(o)
		if err == nil || !strings.Contains(err.Error(), c.want) || (name != "in a FUNCTION" && !strings.Contains(err.Error(), "D1.Asked")) {
			t.Errorf("%s: %v\nwant %q", name, err, c.want)
		}
	}
}

// WRITTEN is no name a program loses: a project FUNCTION, block or tag of
// that name works as before in programs, beside expressions that do not
// call WRITTEN, and a program has no WRITTEN of its own. An expression that
// calls WRITTEN while one of them is in scope is refused, saying to rename
// it: it could mean either.
func TestWrittenIsNotReserved(t *testing.T) {
	fn := "FUNCTION Written : INT\nVAR_INPUT x : INT; END_VAR\nWritten := x + 1;\nEND_FUNCTION\n"
	fb := "FUNCTION_BLOCK WRITTEN\nVAR_INPUT x : INT; END_VAR\nEND_FUNCTION_BLOCK\n"
	plain := driveDef()
	for k, m := range plain.Members {
		if m.Kind == MemberExpr {
			delete(plain.Members, k)
		}
	}
	plain.Members["Asked"] = MemberDef{Kind: MemberExpr, Expr: "Power > 1"}
	o := driveOpts(plain, fn)
	o.Program = "PROGRAM Main\nVAR_EXTERNAL D1 : Drive; Go : BOOL; END_VAR\nD1.Note := Written(4);\nEND_PROGRAM"
	d := &countDriver{}
	o.Driver = d
	r, err := New(o)
	if err != nil {
		t.Fatalf("a project FUNCTION Written: %v", err)
	}
	write(t, r, "D1.Power", 3)
	r.Scan()
	wantPath(t, r, "D1.Note", int64(5))
	wantPath(t, r, "D1.Asked", true)

	// A program does not have WRITTEN.
	o = driveOpts(driveDef(), "")
	o.Program = "PROGRAM Main\nVAR_EXTERNAL D1 : Drive; Go : BOOL; END_VAR\nGo := WRITTEN(D1.Power);\nEND_PROGRAM"
	o.Driver = &countDriver{}
	if _, err := New(o); err == nil || !strings.Contains(err.Error(), `unknown function "WRITTEN": WRITTEN(<path>) is an expr: member's built-in`) {
		t.Errorf("a program calling WRITTEN: %v, want unknown function", err)
	}

	// Each clash, with an expression calling WRITTEN.
	for name, c := range map[string]struct {
		lib  string
		tags []TagDef
		want string
	}{
		"FUNCTION":       {fn, nil, "the project's FUNCTION Written hides the built-in WRITTEN(<path>) this expression calls: rename it"},
		"FUNCTION_BLOCK": {fb, nil, "the project's FUNCTION_BLOCK WRITTEN hides"},
		"tag":            {"", []TagDef{State("written", false)}, "the project's tag written hides"},
	} {
		o := driveOpts(driveDef(), c.lib, c.tags...)
		o.Program = "PROGRAM Main\nEND_PROGRAM"
		o.Driver = &countDriver{}
		if _, err := New(o); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v\nwant %q", name, err, c.want)
		}
	}
}

// WRITTEN costs a scan no allocation.
func TestWrittenAllocatesNothing(t *testing.T) {
	if raceOn {
		t.Skip("the race detector allocates")
	}
	measure := func(def InstanceDef) float64 {
		p := newDriveRig(t, def)
		p.d.set("D1.Speed", 1.0)
		p.d.set("D1.PowerOn", true)
		write(t, p.r, "Go", true)
		p.r.Scan()
		p.r.Scan()
		return testing.AllocsPerRun(100, p.r.Scan)
	}
	plain := driveDef()
	for k, m := range plain.Members {
		if m.Kind == MemberExpr {
			delete(plain.Members, k)
		}
	}
	base := measure(plain)
	if with := measure(driveDef()); with != base {
		t.Errorf("a scan with WRITTEN expressions: %v allocs, without: %v", with, base)
	}
}

// An index literal names the element a read of it names: 16#10 is element
// 16, -1 is element -1 (from the review: a based literal was read as
// decimal digits).
func TestWrittenIndexIsTheElementARead(t *testing.T) {
	lib := "TYPE Big : STRUCT A : ARRAY[0..20] OF INT; Neg : ARRAY[-1..1] OF INT; W : BOOL; V : BOOL; N : BOOL; END_STRUCT; END_TYPE\n"
	rig := func() *Runtime {
		r, err := New(Options{
			Program: "PROGRAM Main\nEND_PROGRAM", Libraries: []string{lib}, Driver: &countDriver{},
			Tags: []TagDef{InstanceTag("B1", "Big", InstanceDef{Members: map[string]MemberDef{
				"A":   {Kind: MemberTo},
				"Neg": {Kind: MemberTo},
				"W":   {Kind: MemberExpr, Expr: "WRITTEN(A[16#10])"},
				"V":   {Kind: MemberExpr, Expr: "A[16#10] > 0"},
				"N":   {Kind: MemberExpr, Expr: "WRITTEN(Neg[-1])"},
			}})},
		})
		if err != nil {
			t.Fatal(err)
		}
		r.Scan()
		return r
	}
	r := rig()
	write(t, r, "B1.A[16]", 1)
	write(t, r, "B1.Neg[-1]", 1)
	r.Scan()
	wantPath(t, r, "B1.V", true)
	wantPath(t, r, "B1.W", true)
	wantPath(t, r, "B1.N", true)
	r = rig()
	write(t, r, "B1.A[10]", 1)
	write(t, r, "B1.Neg[1]", 1)
	r.Scan()
	wantPath(t, r, "B1.W", false)
	wantPath(t, r, "B1.N", false)
}
