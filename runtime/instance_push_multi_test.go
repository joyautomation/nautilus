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
