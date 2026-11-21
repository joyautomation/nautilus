package virtdev

import (
	"testing"

	"github.com/joyautomation/nautilus/acceptance"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/opcua"
)

// The in-process OPC UA face writes as the opcua-client driver does: the
// first push is the start-up baseline, recorded and never sent, and after
// it only a value changed since the last one accounted for is a write. So
// a device's own value survives an output the program never changes (a
// power-up value, or one the plant set since), where writing every push
// would overwrite it each scan and pass or fail a test the controller
// would not.
func TestInProcessOPCUAWritesAsTheDriver(t *testing.T) {
	set, err := LoadSet(binFill)
	if err != nil {
		t.Fatal(err)
	}
	om, err := opcua.ParseManifest([]byte(`tags:
  - { name: Out1, nodeid: "ns=1;s=IOLM/Port 1/Attached Device/PDO Data Unsigned32", namespace: "urn:virtual:pepperl-fuchs:iolm", type: UInt32, writable: true, scanclass: none }
`))
	if err != nil {
		t.Fatal(err)
	}
	od, err := opcua.New("opc.tcp://10.77.1.103:4840", om, opcua.WithSecurity("None", "None"))
	if err != nil {
		t.Fatal(err)
	}
	ip, err := NewInProcess(set, acceptance.NewClock(), []nio.NamedDriver{{Name: "opcua-client", Driver: od}})
	if err != nil {
		t.Fatal(err)
	}
	plant := ip.Plant().Tags()
	const node = "IOLM_Port1_PdoU32"
	device := func() int64 {
		t.Helper()
		v, ok := plant.ReadPath(node)
		if !ok {
			t.Fatalf("no plant tag %s", node)
		}
		if iv, isIR := v.(ir.Value); isIR {
			return iv.I
		}
		n, _ := v.(int64)
		return n
	}
	push := func(v int64) {
		t.Helper()
		if err := ip.WriteOutputs(nio.Values{"Out1": v}); err != nil {
			t.Fatal(err)
		}
		if err := ip.Err(); err != nil {
			t.Fatal(err)
		}
	}
	if err := plant.SetPath(node, int64(5)); err != nil { // the device's power-up value
		t.Fatal(err)
	}
	push(0) // the program's first value differs from the device's: one write (#1123)
	if got := device(); got != 0 {
		t.Errorf("the start-up push did not reach a device holding 5: %d, want 0", got)
	}
	if err := plant.SetPath(node, int64(5)); err != nil { // the plant moves it
		t.Fatal(err)
	}
	push(0)
	if got := device(); got != 5 {
		t.Errorf("an unchanged output was written: %d, want 5", got)
	}
	push(9)
	if got := device(); got != 9 {
		t.Errorf("a changed output did not reach the device: %d, want 9", got)
	}
	if err := plant.SetPath(node, int64(3)); err != nil { // the plant changes it
		t.Fatal(err)
	}
	push(9)
	if got := device(); got != 3 {
		t.Errorf("an unchanged output overwrote the plant's change: %d, want 3", got)
	}
	push(4)
	if got := device(); got != 4 {
		t.Errorf("a changed output did not reach the device: %d, want 4", got)
	}
}

// The face is no looser than the driver (#1123 review): a node that cannot
// take the write is commanded once at start-up when the value differs, and
// answers Bad_NotWritable as the device would; and "held" is the driver's
// numeric equality, not a printed-form match.
func TestInProcessOPCUABaselineIsNoLooserThanTheDriver(t *testing.T) {
	set, err := LoadSet(binFill)
	if err != nil {
		t.Fatal(err)
	}
	om, err := opcua.ParseManifest([]byte(`tags:
  - { name: Out1, nodeid: "ns=1;s=IOLM/Port 1/Attached Device/PDO Data Unsigned32", namespace: "urn:virtual:pepperl-fuchs:iolm", type: UInt32, writable: true, scanclass: none }
`))
	if err != nil {
		t.Fatal(err)
	}
	od, err := opcua.New("opc.tcp://10.77.1.103:4840", om, opcua.WithSecurity("None", "None"))
	if err != nil {
		t.Fatal(err)
	}
	ip, err := NewInProcess(set, acceptance.NewClock(), []nio.NamedDriver{{Name: "opcua-client", Driver: od}})
	if err != nil {
		t.Fatal(err)
	}
	if err := ip.Plant().Tags().SetPath("IOLM_Port1_PdoU32", int64(5)); err != nil {
		t.Fatal(err)
	}
	tags := ip.Plant().Tags()
	for _, c := range []struct {
		v    any
		want bool
	}{{int64(5), true}, {float64(5), true}, {int64(6), false}, {"5", false}, {true, false}} {
		if got := uaHolds(tags, "IOLM_Port1_PdoU32", c.v); got != c.want {
			t.Errorf("uaHolds(device 5, %#v) = %v, want %v", c.v, got, c.want)
		}
	}

	f := ip.ua[0]
	n := f.nodes["Out1"]
	n.writable = false
	f.nodes["Out1"] = n
	if err := ip.WriteOutputs(nio.Values{"Out1": int64(9)}); err != nil {
		t.Fatal(err)
	}
	if err := ip.Err(); err == nil {
		t.Error("a differing start-up value for a read-only node faulted nothing; the driver would send it and get Bad_NotWritable")
	}
}
