package virtdev

import (
	"testing"

	"github.com/joyautomation/nautilus/acceptance"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/opcua"
	"github.com/joyautomation/nautilus/runtime"
)

// statusStub is a `type: runtime` status driver as virtdev sees one: a
// runtime.StatsReader that delivers its own values (#850).
type statusStub struct{ *nio.Memory }

func (statusStub) ReadStatsFrom(func() runtime.ScanStats) {}
func (statusStub) InputNames() []string                   { return []string{"Main__Faulting"} }
func (statusStub) OutputNames() []string                  { return nil }

// A drivers: set with a runtime status driver beside the device drivers is
// not refused: no device answers it, and its own values still arrive.
func TestInProcessKeepsARuntimeStatusDriver(t *testing.T) {
	set, err := LoadSet(binFill)
	if err != nil {
		t.Fatal(err)
	}
	om, err := opcua.ParseManifest([]byte(`tags:
  - { name: BinHigh, nodeid: "ns=7;s=IOLM/Port 1/SIO Input", namespace: "urn:virtual:pepperl-fuchs:iolm", type: Boolean }
`))
	if err != nil {
		t.Fatal(err)
	}
	od, err := opcua.New("opc.tcp://10.77.1.103:4840", om, opcua.WithSecurity("None", "None"))
	if err != nil {
		t.Fatal(err)
	}
	st := statusStub{nio.NewMemory()}
	if err := st.WriteOutputs(nio.Values{"Main__Faulting": false}); err != nil {
		t.Fatal(err)
	}
	ip, err := NewInProcess(set, acceptance.NewClock(), []nio.NamedDriver{
		{Name: "opcua-client", Driver: od}, {Name: "health", Driver: st},
	})
	if err != nil {
		t.Fatalf("a set with a runtime status driver: %v", err)
	}
	vals, _ := ip.ReadInputs()
	if v, ok := vals["Main__Faulting"]; !ok || v != false {
		t.Errorf("Main__Faulting = %v (%v), want the status driver's own false", v, ok)
	}
	if _, ok := vals["BinHigh"]; !ok {
		t.Error("the device input did not arrive beside it")
	}
}
