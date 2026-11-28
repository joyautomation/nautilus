package runtime

import (
	"testing"

	nio "github.com/joyautomation/nautilus/io"
)

// declPush is a pushDriver that declares the inputs and outputs given, so
// an io.Multi routes to it.
type declPush struct {
	*pushDriver
	ins, outs []string
}

func (n declPush) InputNames() []string  { return n.ins }
func (n declPush) OutputNames() []string { return n.outs }

// A push that one io.Multi child failed is still handed over to the
// children that took it (#1497). The event: command went to its own driver
// (A) while a sibling (B) failed its share; when A later asks for every
// output (its gate reopened, so it forgot what it wrote), the held TRUE is
// no change and is not handed to it again, where A would send it late.
func TestPushEventTakenByOneChildWhileASiblingFailsIsHanded(t *testing.T) {
	a := &pushDriver{}
	b := &pushDriver{}
	instIns := []string{"P101.Running", "P101.Speed", "P101.Fault", "P101.Status.Code"}
	multi, err := nio.NewMulti(
		nio.NamedDriver{Name: "a", Driver: declPush{a, instIns, []string{"P101.Cmd.Start", "P101.Cmd.Stop", "P101.Cmd.SpeedRef"}}},
		nio.NamedDriver{Name: "b", Driver: declPush{b, nil, []string{"Lamp"}}},
	)
	if err != nil {
		t.Fatal(err)
	}
	r, err := New(Options{
		Program:   "PROGRAM Main\nVAR_EXTERNAL P101 : Pump; Lamp : BOOL; END_VAR\n" + eventProg + "\nLamp := P101.RunHours > 0.0;\nEND_PROGRAM",
		Libraries: []string{pumpLib},
		Driver:    multi,
		Tags:      []TagDef{InstanceTag("P101", "Pump", pumpDef()), Typed("Lamp", RoleOutput, "BOOL")},
	})
	if err != nil {
		t.Fatal(err)
	}
	r.Scan() // the baseline: FALSE
	write(t, r, "P101.RunHours", 1.0)
	b.fail = true // the sibling fails its share of this push
	r.Scan()
	wantHanded(t, a.last(t), "P101.Cmd.Stop", true)
	b.fail = false

	n := a.count()
	a.ask() // A's gate reopened: it forgot what it wrote
	r.Scan()
	write(t, r, "P101.Cmd.SpeedRef", 5.0) // another output moves
	r.Scan()
	if got := a.since(n, "P101.Cmd.Stop"); len(got) != 0 {
		t.Fatalf("the held TRUE A took was handed to it again after its ask: %v", got)
	}
	// Its next change still goes out.
	write(t, r, "P101.RunHours", 0.0)
	r.Scan()
	wantHanded(t, a.last(t), "P101.Cmd.Stop", false)
}

// The child that failed did not take its share: its event: command is
// handed again on the next push, as a single driver's failed push is
// (TestPushEventRetriedAfterAFailedWrite).
func TestPushEventOfTheFailingChildIsRetried(t *testing.T) {
	a := &pushDriver{}
	b := &pushDriver{}
	instIns := []string{"P101.Running", "P101.Speed", "P101.Fault", "P101.Status.Code"}
	multi, err := nio.NewMulti(
		nio.NamedDriver{Name: "a", Driver: declPush{a, instIns, []string{"P101.Cmd.Start", "P101.Cmd.Stop", "P101.Cmd.SpeedRef"}}},
		nio.NamedDriver{Name: "b", Driver: declPush{b, nil, []string{"Lamp"}}},
	)
	if err != nil {
		t.Fatal(err)
	}
	r, err := New(Options{
		Program:   "PROGRAM Main\nVAR_EXTERNAL P101 : Pump; Lamp : BOOL; END_VAR\n" + eventProg + "\nLamp := P101.RunHours > 0.0;\nEND_PROGRAM",
		Libraries: []string{pumpLib},
		Driver:    multi,
		Tags:      []TagDef{InstanceTag("P101", "Pump", pumpDef()), Typed("Lamp", RoleOutput, "BOOL")},
	})
	if err != nil {
		t.Fatal(err)
	}
	r.Scan()
	write(t, r, "P101.RunHours", 1.0)
	a.fail = true // the event command's own driver fails
	r.Scan()
	a.fail = false
	n := a.count()
	a.ask()
	r.Scan()
	if got := a.since(n, "P101.Cmd.Stop"); len(got) == 0 || got[0] != true {
		t.Fatalf("the event command its driver failed was not handed again: %v", got)
	}
}

// bothMulti is a tank whose peer P (echo: it takes what it is handed) owns
// the both: member Valve, beside a sibling S that owns a plain output and
// fails on cue. Valve is delivered at 12.
func bothMulti(t *testing.T) (*Runtime, *pushDriver, *pushDriver) {
	t.Helper()
	p := &pushDriver{echo: true}
	s := &pushDriver{}
	multi, err := nio.NewMulti(
		nio.NamedDriver{Name: "p", Driver: declPush{p, []string{"T1.Valve", "T1.Levels[1]", "T1.Sensor"}, []string{"T1.Valve"}}},
		nio.NamedDriver{Name: "s", Driver: declPush{s, nil, []string{"Lamp"}}},
	)
	if err != nil {
		t.Fatal(err)
	}
	r, _ := newTankRuntime(t, func(o *Options) {
		o.Program = "PROGRAM Main\nVAR_EXTERNAL T1 : Tank; Lamp : BOOL; END_VAR\nLamp := T1.Note > 0;\nEND_PROGRAM"
		o.Driver = multi
		o.Tags = append(o.Tags, Typed("Lamp", RoleOutput, "BOOL"))
	})
	p.set("T1.Valve", 12.0)
	r.Scan()
	return r, p, s
}

// A both: command whose own driver failed its share is still pending: the
// peer's next delivery (still the old value) does not un-write it, and it
// goes out once the driver takes it.
func TestPushBothOfTheFailingChildStaysPending(t *testing.T) {
	r, p, _ := bothMulti(t)
	write(t, r, "T1.Valve", 20.0)
	p.fail = true
	r.Scan() // P fails the push; the peer still shows 12
	p.fail = false
	n := p.count()
	r.Scan()
	if got := p.since(n, "T1.Valve"); len(got) == 0 || got[0] != 20.0 {
		t.Fatalf("the both: command its driver failed was not handed again: %v", got)
	}
}

// A both: command its driver took while a sibling failed is handed over:
// once the peer moves off it (another client wrote it), the stale copy is
// not written back on the driver's next ask.
func TestPushBothTakenByOneChildWhileASiblingFailsIsHanded(t *testing.T) {
	r, p, s := bothMulti(t)
	write(t, r, "T1.Valve", 20.0)
	write(t, r, "T1.Note", 1) // Lamp moves, so S has a share to fail
	s.fail = true
	r.Scan() // P takes 20 (and echoes it), S fails
	r.Scan() // the peer shows 20; S still fails, so no push succeeds whole
	n := p.count()
	p.set("T1.Valve", 30.0) // another client of the peer wrote it
	r.Scan()
	p.ask()
	r.Scan()
	if got := p.since(n, "T1.Valve"); len(got) != 0 {
		t.Errorf("the stale 20 was written over the peer's 30: %v", got)
	}
}
