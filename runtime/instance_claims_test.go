package runtime

import (
	"strings"
	"testing"

	nio "github.com/joyautomation/nautilus/io"
)

// claimDriver declares the tags it owns, as every field driver does.
type claimDriver struct {
	*pushDriver
	ins, outs []string
}

func (c claimDriver) InputNames() []string  { return c.ins }
func (c claimDriver) OutputNames() []string { return c.outs }

var pumpIns = []string{"P101.Running", "P101.Speed", "P101.Fault", "P101.Status.Code"}
var pumpOuts = []string{"P101.Cmd.Start", "P101.Cmd.Stop", "P101.Cmd.SpeedRef"}

func newClaimed(ins, outs []string) (*Runtime, error) {
	return New(Options{
		Program:   "PROGRAM Main\nVAR_EXTERNAL P101 : Pump; END_VAR\nP101.Cmd.Start := TRUE;\nEND_PROGRAM",
		Libraries: []string{pumpLib},
		Driver:    claimDriver{&pushDriver{}, ins, outs},
		Tags:      []TagDef{InstanceTag("P101", "Pump", pumpDef())},
	})
}

func without(s []string, drop string) []string {
	var out []string
	for _, x := range s {
		if x != drop {
			out = append(out, x)
		}
	}
	return out
}

// Hard condition 1 of #1458 slice 5: no push key is dropped without a word.
// A driver that declares its tags must take every command an instance
// pushes and deliver every member it reads, or New refuses, naming the
// member and the key; a binding spelled otherwise than the TYPE is named
// too.
func TestInstanceKeysMustReachADriver(t *testing.T) {
	if _, err := newClaimed(pumpIns, pumpOuts); err != nil {
		t.Fatalf("every key claimed: %v", err)
	}
	_, err := newClaimed(pumpIns, without(pumpOuts, "P101.Cmd.SpeedRef"))
	if err == nil || !strings.Contains(err.Error(), `instance P101: command Cmd.SpeedRef (pushed as "P101.Cmd.SpeedRef") reaches no driver`) {
		t.Errorf("an unclaimed command: %v", err)
	}
	_, err = newClaimed(pumpIns, append(without(pumpOuts, "P101.Cmd.Start"), "P101.cmd.start"))
	if err == nil || !strings.Contains(err.Error(), `(a driver has "P101.cmd.start": a key is spelled as the TYPE spells the member)`) {
		t.Errorf("a command spelled in another case: %v", err)
	}
	_, err = newClaimed(without(pumpIns, "P101.Speed"), pumpOuts)
	if err == nil || !strings.Contains(err.Error(), `instance P101: member Speed (delivered as "P101.Speed") comes from no driver`) {
		t.Errorf("an undelivered member: %v", err)
	}
	// A driver that delivers the whole instance delivers every member.
	if _, err := newClaimed([]string{"P101"}, pumpOuts); err != nil {
		t.Errorf("the whole instance delivered: %v", err)
	}
	// A struct above a member is no delivery key: the runtime takes a
	// delivery only under a key or the instance's name.
	_, err = newClaimed(append(without(pumpIns, "P101.Status.Code"), "P101.Status"), pumpOuts)
	if err == nil || !strings.Contains(err.Error(), `member Status.Code (delivered as "P101.Status.Code") comes from no driver`) ||
		!strings.Contains(err.Error(), `a driver delivers "P101.Status", which is no from: or both: member of the instance`) {
		t.Errorf("a struct above a member: %v", err)
	}
	// A binding under the instance that is none of its keys goes nowhere:
	// an output on a memory member, an input on a command.
	_, err = newClaimed(pumpIns, append(pumpOuts, "P101.RunHours"))
	if err == nil || !strings.Contains(err.Error(), `a driver takes "P101.RunHours" as an output, which is no to: or both: member of the instance, so nothing would ever be sent through it (it is memory)`) {
		t.Errorf("an output on a memory member: %v", err)
	}
	_, err = newClaimed(append(pumpIns, "P101.Cmd.Start"), pumpOuts)
	if err == nil || !strings.Contains(err.Error(), `a driver delivers "P101.Cmd.Start", which is no from: or both: member of the instance, so the reading would go nowhere (it is to)`) {
		t.Errorf("an input on a command: %v", err)
	}
	// A driver that declares nothing (a loopback) owns whatever it is
	// handed.
	if _, err := New(Options{
		Program:   "PROGRAM Main\nVAR_EXTERNAL P101 : Pump; END_VAR\nEND_PROGRAM",
		Libraries: []string{pumpLib},
		Driver:    nio.NewMemory(),
		Tags:      []TagDef{InstanceTag("P101", "Pump", pumpDef())},
	}); err != nil {
		t.Errorf("a loopback: %v", err)
	}
}
