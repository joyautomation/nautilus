package virtdev

import (
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/joyautomation/nautilus/acceptance"
	"github.com/joyautomation/nautilus/devbind"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/opcua"
)

// A master with IO-Link port devices under naut test: the in-process face
// answers the master's command bindings itself, and a port's command tags
// and __Rearm are no node of the device. They go through the same gate the
// controller's WriteOutputs does (#705), never silently dropped: a test of
// a port's outputs would otherwise pass or fail for nothing the controller
// does (#982 was the same kind). Nothing reaches the device until the pulse,
// then the image and the valid flag a push after, and a swapped device
// takes the flag away again.
func TestInProcessGatesPortOutputs(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"virtual-devices.yaml": "devices:\n  - { name: D, profile: pepperl-fuchs-ice3, endpoint: 10.77.1.103 }\nmodel:\n  program: plant.st\n  scan: 100ms\n",
		// The sensor is swapped for another model from the 30th scan.
		"plant.st": `PROGRAM Plant
VAR
n : INT;
END_VAR
n := n + 1;
D_Port1_PdiBytes := 'fe0dffd1';
D_Port1_VendorID := 65261;
D_Port1_DeviceID := 1048577;
IF n > 30 THEN D_Port1_DeviceID := 1048999; END_IF;
D_Port1_PdiValid := TRUE;
END_PROGRAM
`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	set, err := LoadSet(dir)
	if err != nil {
		t.Fatal(err)
	}
	prof, err := os.ReadFile("../devprofile/profiles/pepperl-fuchs-ice3.yaml")
	if err != nil {
		t.Fatal(err)
	}
	level, err := os.ReadFile("../iolink/iodd/testdata/acme-level100-20261118-IODD1.1.xml")
	if err != nil {
		t.Fatal(err)
	}
	devs, err := devbind.Resolve(fstest.MapFS{
		"profiles/pepperl-fuchs-ice3.yaml":        {Data: prof},
		"iodd/acme-level100-20261118-IODD1.1.xml": {Data: level},
	}, []devbind.Device{{
		Name: "M", Profile: "pepperl-fuchs-ice3", Endpoint: "10.77.1.103", Points: []string{"Port1_*"},
		Ports: map[int]devbind.Port{1: {Name: "LEVEL1", IODD: "acme-level100-20261118-IODD1.1.xml"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	od := devs.OPCUADevices()[0]
	d, err := opcua.New("opc.tcp://10.77.1.103:4840", od.Manifest, opcua.WithName("M"), opcua.WithOnlineTag("M__Online"), opcua.WithSecurity("None", "None"))
	if err != nil {
		t.Fatal(err)
	}
	w, err := devs.WrapOPCUA("M", d, "None", "None")
	if err != nil {
		t.Fatal(err)
	}
	clk := acceptance.NewClock()
	ip, err := NewInProcess(set, clk, []nio.NamedDriver{{Name: "opcua-client", Driver: w}})
	if err != nil {
		t.Fatal(err)
	}
	plant := ip.Plant().Tags()
	str := func(path string) string {
		v, _ := plant.ReadPath(path)
		s, _ := v.(string)
		if s == "" {
			if x, ok := v.(interface{ String() string }); ok {
				return x.String()
			}
		}
		return s
	}
	flag := func() bool {
		v, _ := plant.ReadPath("D_Port1_PdoValid")
		b, _ := v.(bool)
		return b
	}
	tick := func() nio.Values {
		t.Helper()
		clk.Set(clk.Now().Add(100 * time.Millisecond))
		vals, _ := ip.ReadInputs()
		return vals
	}
	push := func(teach bool, mode int64, lock, rearm bool) {
		t.Helper()
		if err := ip.WriteOutputs(nio.Values{"LEVEL1_Teach": teach, "LEVEL1_Mode": mode, "LEVEL1_Lock": lock, "LEVEL1__Rearm": rearm}); err != nil {
			t.Fatal(err)
		}
	}
	if vals := tick(); vals["LEVEL1__Identity"] != int64(4) {
		t.Fatalf("the sensor under naut test reads identity %v, want AwaitingRearm (4): %v", vals["LEVEL1__Identity"], vals)
	}
	push(false, 0, false, false)
	push(true, 5, true, false)
	tick()
	if str("D_Port1_PdoBytes") != "" || flag() {
		t.Fatalf("an unarmed port reached the device: pdo %q valid %v", str("D_Port1_PdoBytes"), flag())
	}
	push(true, 5, true, true) // the pulse
	tick()
	if got := str("D_Port1_PdoBytes"); got != "8b" {
		t.Fatalf("after the pulse the device holds %q, want 8b: the port's command tags must reach the device through the gate", got)
	}
	if flag() {
		t.Error("the valid flag came with the image: it follows a push later")
	}
	push(true, 5, true, true)
	tick()
	if !flag() {
		t.Error("the valid flag never came")
	}
	if err := ip.Err(); err != nil {
		t.Fatalf("protocol faults: %v", err)
	}

	// The swap, thirty scans in: the fault, and the flag taken away.
	for i := 0; i < 40; i++ {
		clk.Set(clk.Now().Add(100 * time.Millisecond))
		vals, _ := ip.ReadInputs()
		push(true, 5, true, false)
		if vals["LEVEL1__Identity"] == int64(2) {
			break
		}
	}
	tick()
	push(true, 5, true, false)
	if flag() {
		t.Error("the valid flag stayed true with a swapped device on the port")
	}
}
