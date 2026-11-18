package virtdev

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/acceptance"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/modbus"
	"github.com/joyautomation/nautilus/opcua"
)

const binFill = "../examples/bin-fill"

func fixtureDrivers(t *testing.T, mopts []modbus.Option, endpoint string) (*modbus.Driver, *opcua.Driver) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(binFill, "modbus_manifest.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	mm, err := modbus.ParseManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	md, err := modbus.New(mm, mopts...)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(filepath.Join(binFill, "opcua_manifest.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	om, err := opcua.ParseManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	od, err := opcua.New(endpoint, om, opcua.WithScanRate(50*time.Millisecond), opcua.WithSecurity("None", "None"))
	if err != nil {
		t.Fatal(err)
	}
	return md, od
}

// The wire face and the in-process face agree: the project's real Modbus
// and OPC UA drivers, on sockets to the virtual devices, deliver what the
// in-process face delivers for the same writes. Wall clock.
func TestWireAndInProcessAgree(t *testing.T) {
	set, err := LoadSet(binFill)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	run, err := Start(ctx, set, StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer run.Stop()

	// The real drivers, dialing the addresses the project names, redirected
	// to the wire face.
	dial := func(ctx context.Context, addr string) (net.Conn, error) {
		to, ok := run.Redirect(addr)
		if !ok {
			return nil, fmt.Errorf("no virtual device at %s", addr)
		}
		var d net.Dialer
		return d.DialContext(ctx, "tcp", to)
	}
	url, ok := run.Redirect("opc.tcp://10.77.1.103:4840")
	if !ok {
		t.Fatalf("no OPC UA redirect: %+v", run.Endpoints)
	}
	wmb, wua := fixtureDrivers(t, []modbus.Option{modbus.WithDialer(dial), modbus.WithScanRate(50 * time.Millisecond)}, url)
	wmb.Start(ctx)
	defer wmb.Stop()
	wua.Start(ctx)
	defer wua.Stop()

	// The first push is the driver's start-up baseline (recorded, not
	// sent); the second is a change and goes out. Both faces get both.
	baseline := nio.Values{"Conv_CW": int64(0), "Conv_Ref": int64(0), "RB_Out": int64(0)}
	writes := nio.Values{"Conv_CW": int64(0x0476), "Conv_Ref": int64(0), "RB_Out": int64(0)}
	if err := wmb.WriteOutputs(baseline); err != nil {
		t.Fatal(err)
	}
	wire := nio.Values{}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := wmb.WriteOutputs(writes); err != nil {
			t.Fatal(err)
		}
		mv, _ := wmb.ReadInputs()
		uv, _ := wua.ReadInputs()
		clear(wire)
		for k, v := range mv {
			wire[k] = v
		}
		for k, v := range uv {
			wire[k] = v
		}
		sw, _ := wire["Conv_SW"].(int64)
		if sw&1 != 0 && wire[wua.OnlineTagName()] == true && wire["BinHigh"] != nil {
			break // READY TO SWITCH ON, and the master answered
		}
		if time.Now().After(deadline) {
			t.Fatalf("the wire face never settled: %v", wire)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// The in-process face, the same drivers' bindings (the OPC UA one at
	// the address the project names), the same write, one second on.
	imb, iua := fixtureDrivers(t, nil, "opc.tcp://10.77.1.103:4840")
	clk := acceptance.NewClock()
	ip, err := NewInProcess(set, clk, []nio.NamedDriver{{Name: "modbus", Driver: imb}, {Name: "opcua-client", Driver: iua}})
	if err != nil {
		t.Fatal(err)
	}
	if err := ip.WriteOutputs(baseline); err != nil {
		t.Fatal(err)
	}
	if err := ip.WriteOutputs(writes); err != nil {
		t.Fatal(err)
	}
	clk.Set(acceptance.Epoch.Add(time.Second))
	inproc, _ := ip.ReadInputs()
	if err := ip.Err(); err != nil {
		t.Fatalf("in-process faults: %v", err)
	}

	// Every binding both faces deliver, with the same value. The OPC UA
	// driver names its companion after itself; compare it by role.
	delete(wire, wua.OnlineTagName())
	delete(inproc, iua.OnlineTagName())
	var diffs []string
	keys := map[string]bool{}
	for k := range wire {
		keys[k] = true
	}
	for k := range inproc {
		keys[k] = true
	}
	var names []string
	for k := range keys {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		if !reflect.DeepEqual(wire[k], inproc[k]) {
			diffs = append(diffs, fmt.Sprintf("%s: wire %#v, in process %#v", k, wire[k], inproc[k]))
		}
	}
	if len(diffs) > 0 {
		t.Fatalf("the faces disagree:\n  %s", strings.Join(diffs, "\n  "))
	}
}

// An OPC UA binding that names its namespace by URI resolves by URI, as
// the driver does at connect; one naming a URI the device does not have
// is a fault, not a silent read of something else.
func TestInProcessResolvesNamespaceURI(t *testing.T) {
	set, err := LoadSet(binFill)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		uri   string
		fault string
	}{
		{"urn:virtual:pepperl-fuchs:iolm", ""},
		{"urn:somebody-else", "is not in IOLM's namespace array"},
	} {
		om, err := opcua.ParseManifest([]byte(`tags:
  - { name: BinHigh, nodeid: "ns=7;s=IOLM/Port 1/SIO Input", namespace: "` + tc.uri + `", type: Boolean }
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
		vals, _ := ip.ReadInputs()
		err = ip.Err()
		if tc.fault == "" {
			if err != nil || vals["BinHigh"] != false {
				t.Errorf("%s: BinHigh = %v, err %v", tc.uri, vals["BinHigh"], err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tc.fault) {
			t.Errorf("%s: err %v, want %q", tc.uri, err, tc.fault)
		}
	}
}
