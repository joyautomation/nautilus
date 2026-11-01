package project

// The OPC UA client driver (`type: opcua-client`) through the loader: it builds
// offline, names its companion as every single-connection driver does, is a
// communications driver (its inputs read init/zero until delivered), shows
// up as one status row and one Sparkplug device, and `naut opcua import`
// resolves the companion name Load gives it.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"testing/fstest"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/runtime"
)

const opcuaClientManifest = `namespaces: ["http://opcfoundation.org/UA/", "urn:example:gateway"]
tags:
  - {name: GW_Level, nodeid: "ns=1;s=Plant/Level", namespace: "urn:example:gateway", type: Double}
  - {name: GW_Count, nodeid: "ns=1;s=Plant/Count", namespace: "urn:example:gateway", type: Int32}
  - {name: GW_Run, nodeid: "ns=1;s=Plant/Run", namespace: "urn:example:gateway", type: Boolean}
  - {name: GW_SP, nodeid: "ns=1;s=Plant/SP", namespace: "urn:example:gateway", type: Float, writable: true}
`

func opcuaClientProject(endpoint string) fstest.MapFS {
	return fstest.MapFS{
		"nautilus.yaml": &fstest.MapFile{Data: []byte(fmt.Sprintf(`name: gw
tasks:
  - program: main.st
    scan: 50ms
tags:
  - { name: GW_Level, role: input }
  - { name: GW_Count, role: input }
  - { name: GW_Run, role: input, init: true }
  - { name: GW_SP, role: output, init: 0.0 }
  - { name: opcua_client__Online, role: input }
  - { name: Level, role: state, init: -1.0 }
  - { name: Running, role: state, init: false }
  - { name: Ran, role: state, init: false }
driver:
  type: opcua-client
  endpoint: %s
  manifest: opcua_manifest.yaml
  scan-rate: 100ms
`, endpoint))},
		"opcua_manifest.yaml": &fstest.MapFile{Data: []byte(opcuaClientManifest)},
		"main.st": &fstest.MapFile{Data: []byte(`PROGRAM Main
VAR_EXTERNAL GW_Level : LREAL; GW_Run : BOOL; opcua_client__Online : BOOL;
  Level : LREAL; Running : BOOL; Ran : BOOL; END_VAR
  Level := GW_Level;
  Running := GW_Run AND opcua_client__Online;
  Ran := TRUE;
END_PROGRAM
`)},
	}
}

func TestOPCUAClientDriverIsACommsDriver(t *testing.T) {
	p, err := Load(opcuaClientProject(fmt.Sprintf("opc.tcp://127.0.0.1:%d", closedPort(t))), "")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]runtime.TagDef{}
	for _, d := range p.Runtime.Tags {
		got[d.Name] = d
	}
	for name, want := range map[string]string{
		"GW_Level": "LREAL", "GW_Count": "DINT", "GW_Run": "BOOL", "opcua_client__Online": "BOOL",
	} {
		if !got[name].Comms {
			t.Errorf("%s is not marked Comms", name)
		}
		if got[name].DriverType != want {
			t.Errorf("%s DriverType = %q, want %q", name, got[name].DriverType, want)
		}
	}
	if got["GW_SP"].Comms {
		t.Error("an output was marked Comms")
	}

	// One status row, kind "opcua-client" (the served side is "opcua-server").
	rows := p.DriverStatus(nil, nil)()
	if len(rows) != 1 || rows[0].Kind != "opcua-client" || rows[0].Name != "opcua-client" {
		t.Fatalf("status rows = %+v, want one opcua-client row named opcua-client", rows)
	}

	// One Sparkplug device, named after the driver, carrying its inputs,
	// unhealthy while the server is down.
	devs, err := sparkplugDevices([]nio.NamedDriver{{Name: "opcua-client", Driver: p.Runtime.Driver}})
	if err != nil {
		t.Fatal(err)
	}
	if len(devs) != 1 || devs[0].ID != "opcua-client" || devs[0].Health == nil || devs[0].Health() {
		t.Fatalf("devices = %+v, want one opcua-client device, unhealthy before connect", devs)
	}
	if !strings.Contains(strings.Join(devs[0].Tags, ","), "opcua_client__Online") {
		t.Errorf("device tags %v lack the companion", devs[0].Tags)
	}
}

// The server is down when the controller starts: its inputs read their
// init or zero, __Online is false, the program runs, and the values report
// NotConnected rather than Good.
func TestOPCUAClientServerDownReadsInitOrZero(t *testing.T) {
	p, err := Load(opcuaClientProject(fmt.Sprintf("opc.tcp://127.0.0.1:%d", closedPort(t))), "")
	if err != nil {
		t.Fatal(err)
	}
	rt, err := runtime.New(p.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	drv := p.Runtime.Driver.(interface {
		nio.Driver
		Start(context.Context)
		Stop()
	})
	drv.Start(context.Background())
	t.Cleanup(drv.Stop)
	for range 3 {
		rt.Scan()
	}
	tags := rt.Tags()
	if st := rt.Stats(); st.LogicErrors != 0 {
		t.Fatalf("Main faulted with the server down: %s", st.LastError)
	}
	if !tags.Bool("Ran") {
		t.Error("the program did not run")
	}
	if tags.Bool("Running") {
		t.Error("Running is true: the logic must see opcua_client__Online FALSE while the server is down")
	}
	if got := tags.Real("Level"); got != 0 {
		t.Errorf("Level = %v, want the seeded 0", got)
	}
	if !tags.Bool("GW_Run") {
		t.Error("GW_Run does not read its init: TRUE before delivery")
	}
	if q := rt.TagQuality("GW_Level"); q != nio.NotConnected {
		t.Errorf("GW_Level quality = %v, want NotConnected", q)
	}
}

// OPCUAOnlineTag names the companion exactly as Load will: by endpoint,
// then manifest, then the only opcua-client driver, else the default.
func TestOPCUAOnlineTagResolution(t *testing.T) {
	two := "drivers:\n" +
		"  - { type: modbus, manifest: m.yaml }\n" +
		"  - { type: opcua-client, endpoint: 'opc.tcp://10.0.0.1:4840', manifest: a.yaml }\n" +
		"  - { type: opcua-client, endpoint: 'opc.tcp://10.0.0.2:4840/', manifest: b.yaml }\n" +
		"  - { type: opcua-client, name: Line 3, endpoint: 'opc.tcp://10.0.0.3:4840', manifest: c.yaml }\n"
	for _, tc := range []struct {
		name, manifest, endpoint, file, want string
	}{
		{"no nautilus.yaml", "", "opc.tcp://10.0.0.1:4840", "", "opcua_client__Online"},
		{"no opcua-client driver", "driver: { type: modbus, manifest: m.yaml }\n", "opc.tcp://x:4840", "", "opcua_client__Online"},
		{"lone named driver, other endpoint", "driver: { type: opcua-client, name: gateway, endpoint: 'opc.tcp://a:4840', manifest: o.yaml }\n", "opc.tcp://b:4840", "", "gateway__Online"},
		{"first of several", two, "opc.tcp://10.0.0.1:4840", "", "opcua_client__Online"},
		{"deduped second, trailing slash", two, "opc.tcp://10.0.0.2:4840", "", "opcua_client_2__Online"},
		{"named, reduced to an identifier", two, "opc.tcp://10.0.0.3:4840", "", "Line_3__Online"},
		{"by manifest", two, "", "./b.yaml", "opcua_client_2__Online"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fsys := fstest.MapFS{}
			if tc.manifest != "" {
				fsys["nautilus.yaml"] = &fstest.MapFile{Data: []byte(tc.manifest)}
			}
			got, how, err := OPCUAOnlineTag(fsys, tc.endpoint, tc.file)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("got %q (%s), want %q", got, how, tc.want)
			}
		})
	}
	fsys := fstest.MapFS{"nautilus.yaml": &fstest.MapFile{Data: []byte(two)}}
	if _, _, err := OPCUAOnlineTag(fsys, "opc.tcp://10.9.9.9:4840", ""); err == nil || !strings.Contains(err.Error(), "--name") {
		t.Errorf("ambiguous project: err = %v, want the drivers listed and --name", err)
	}
}

// The name the import resolves is one Load's drivers deliver.
func TestOPCUAOnlineTagAgreesWithLoad(t *testing.T) {
	files := fsys(multiBase() + "drivers:\n" +
		"  - { type: opcua-client, endpoint: 'opc.tcp://192.0.2.1:4840', manifest: a.yaml }\n" +
		"  - { type: opcua-client, endpoint: 'opc.tcp://192.0.2.2:4840', manifest: b.yaml }\n")
	files["a.yaml"] = &fstest.MapFile{Data: []byte("tags:\n  - {name: A, nodeid: \"ns=1;s=A\", type: Double}\n")}
	files["b.yaml"] = &fstest.MapFile{Data: []byte("tags:\n  - {name: B, nodeid: \"ns=1;s=B\", type: Double}\n")}
	p, err := Load(files, "")
	if err != nil {
		t.Fatal(err)
	}
	want, _, err := OPCUAOnlineTag(files, "opc.tcp://192.0.2.2:4840", "")
	if err != nil {
		t.Fatal(err)
	}
	if want != "opcua_client_2__Online" {
		t.Fatalf("resolved %q, want opcua_client_2__Online", want)
	}
	type owner interface{ InputNames() []string }
	found := false
	for _, n := range p.Runtime.Driver.(owner).InputNames() {
		found = found || n == want
	}
	if !found {
		t.Errorf("Load's drivers deliver %v; the import's %q is not among them", p.Runtime.Driver.(owner).InputNames(), want)
	}
}
