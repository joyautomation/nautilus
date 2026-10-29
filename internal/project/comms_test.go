package project

// Protocol inputs read as their init (or zero) until first delivered; local
// I/O inputs still fault. The decision is per tag, by the driver that
// delivers it, so a drivers: set mixing a Modbus skid with a groov EPIC's
// own modules seeds exactly the Modbus tags — and the EPIC logic in the same
// program keeps running while the skid is unplugged.

import (
	"context"
	"fmt"
	"net"
	"os"
	"path"
	"strings"
	"testing"
	"testing/fstest"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/optommp/optommptest"
	"github.com/joyautomation/nautilus/runtime"
)

// closedPort is a loopback port nothing listens on: a Modbus device that is
// down (connection refused) for as long as the test runs.
func closedPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

// mixedProject is a Modbus skid (down) beside a groov EPIC's local I/O (the
// in-process bench chassis on epicPort): Main reads both, Local reads only
// the EPIC.
func mixedProject(modbusPort, epicPort int) fstest.MapFS {
	return fstest.MapFS{
		"nautilus.yaml": &fstest.MapFile{Data: []byte(`name: mixed
tasks:
  - program: main.st
    scan: 50ms
  - name: local
    program: local.st
    scan: 50ms
tags:
  - { name: SkidFlow, role: input }
  - { name: SkidRun, role: input, init: true }
  - { name: Skid__Online, role: input }
  - { name: SkidCount, role: input }
  - { name: EPIC_M0_DI03, role: input }
  - { name: Flow, role: state, init: -1.0 }
  - { name: Run, role: state, init: false }
  - { name: Seen, role: state, init: false }
  - { name: LocalSeen, role: state, init: false }
drivers:
  - type: modbus
    manifest: modbus_manifest.yaml
  - type: optommp
    manifest: optommp_manifest.yaml
    scan-rate: 10ms
`)},
		"main.st": &fstest.MapFile{Data: []byte(`PROGRAM Main
VAR_EXTERNAL
  SkidFlow : REAL; SkidRun : BOOL; Skid__Online : BOOL;
  EPIC_M0_DI03 : BOOL;
  Flow : REAL; Run : BOOL; Seen : BOOL;
END_VAR
Flow := SkidFlow;
Run := SkidRun AND Skid__Online;
Seen := EPIC_M0_DI03 OR NOT EPIC_M0_DI03;
END_PROGRAM`)},
		"local.st": &fstest.MapFile{Data: []byte(`PROGRAM Local
VAR_EXTERNAL EPIC_M0_DI03 : BOOL; LocalSeen : BOOL; END_VAR
LocalSeen := EPIC_M0_DI03 OR NOT EPIC_M0_DI03;
END_PROGRAM`)},
		"modbus_manifest.yaml": &fstest.MapFile{Data: []byte(fmt.Sprintf(`sources:
  - id: Skid
    host: 127.0.0.1
    port: %d
    timeout: 200ms
tags:
  - {name: SkidFlow, source: Skid, table: holding, address: 0, format: float32}
  - {name: SkidRun, source: Skid, table: coil, address: 0}
  - {name: SkidCount, source: Skid, table: holding, address: 4, format: uint16}
`, modbusPort))},
		"optommp_manifest.yaml": &fstest.MapFile{Data: []byte(fmt.Sprintf(`host: 127.0.0.1
port: %d
modules:
  - { slot: 0, part: "GRV-IACDCTTL-24", type: 0x50000005, channels: 24 }
tags:
  - { name: EPIC_M0_DI03, point: di, slot: 0, channel: 3 }
`, epicPort))},
	}
}

// The classification itself: which tags the loader marks Comms, and the
// type each falls back to when no program or type: says.
func TestCommsClassificationIsPerTagByDriver(t *testing.T) {
	p, err := Load(mixedProject(closedPort(t), closedPort(t)), "")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]runtime.TagDef{}
	for _, d := range p.Runtime.Tags {
		got[d.Name] = d
	}
	for name, want := range map[string]bool{
		"SkidFlow": true, "SkidRun": true, "Skid__Online": true, "SkidCount": true,
		"EPIC_M0_DI03": false, // optommp: local I/O
		"Flow":         false, // not an input at all
	} {
		if got[name].Comms != want {
			t.Errorf("%s Comms = %v, want %v", name, got[name].Comms, want)
		}
	}
	// SkidCount is read by no program and has no type: — the Modbus
	// binding's unscaled uint16 types it.
	if dt := got["SkidCount"].DriverType; dt != "DINT" {
		t.Errorf("SkidCount DriverType = %q, want DINT from the uint16 binding", dt)
	}
	if dt := got["Skid__Online"].DriverType; dt != "BOOL" {
		t.Errorf("Skid__Online DriverType = %q, want BOOL", dt)
	}
}

// The scenario the change exists for: the Modbus skid is down when the
// controller starts. Its inputs read their init or zero, __Online is false,
// and the program that reads them runs — including the EPIC logic in the
// same program. An input the EPIC (local I/O) has not delivered still
// faults, exactly as before.
func TestModbusDownReadsInitOrZeroWhileLocalIOStillFaults(t *testing.T) {
	dev := optommptest.NewBench()
	epicPort := dev.Listen(t)
	p, err := Load(mixedProject(closedPort(t), epicPort), "")
	if err != nil {
		t.Fatal(err)
	}
	rt, err := runtime.New(p.Runtime)
	if err != nil {
		t.Fatal(err)
	}

	// Before any driver has delivered: the Modbus tags exist at their
	// init/zero, the EPIC tag does not.
	tags := rt.Tags()
	if v, err := tags.ReadGlobal("SkidFlow"); err != nil || v.F != 0 {
		t.Errorf("SkidFlow before delivery = %+v (%v), want REAL 0", v, err)
	}
	if v, err := tags.ReadGlobal("SkidRun"); err != nil || !v.B {
		t.Errorf("SkidRun before delivery = %+v (%v), want its init TRUE", v, err)
	}
	if v, err := tags.ReadGlobal("Skid__Online"); err != nil || v.B {
		t.Errorf("Skid__Online before delivery = %+v (%v), want FALSE", v, err)
	}
	if _, err := tags.ReadGlobal("EPIC_M0_DI03"); err == nil {
		t.Error("the local-I/O input was seeded; it must stay absent until the EPIC delivers it")
	}
	// The local task reads only the EPIC input: it faults, as before.
	if err := rt.ScanTask("local"); err != nil {
		t.Fatal(err)
	}
	if st := rt.Stats(); st.Tasks[0].LogicErrors != 1 || !strings.Contains(st.Tasks[0].LastError, "EPIC_M0_DI03") {
		t.Fatalf("local task before the EPIC delivered: %+v, want an undefined-tag fault naming EPIC_M0_DI03", st.Tasks[0])
	}

	// Start both drivers: the EPIC answers, the skid never does.
	drv := p.Runtime.Driver.(*nio.Multi)
	drv.Start(context.Background())
	t.Cleanup(drv.Stop)
	waitFor(t, "the EPIC to deliver", func() bool {
		v, err := drv.ReadInputs()
		return err == nil && v["EPIC_M0_DI03"] != nil
	})
	for range 3 {
		rt.Scan()
		if err := rt.ScanTask("local"); err != nil {
			t.Fatal(err)
		}
	}
	st := rt.Stats()
	if st.LogicErrors != 0 {
		t.Fatalf("Main faulted with the skid down: %s — it must run on the seeds", st.LastError)
	}
	if st.Tasks[0].LogicErrors != 1 {
		t.Errorf("local task faulted again after the EPIC delivered: %s", st.Tasks[0].LastError)
	}
	if got := tags.Real("Flow"); got != 0 {
		t.Errorf("Flow = %v, want the skid's seeded 0", got)
	}
	if tags.Bool("Run") {
		t.Error("Run is true: the logic must see Skid__Online FALSE while the skid is down")
	}
	if !tags.Bool("Seen") || !tags.Bool("LocalSeen") {
		t.Error("the EPIC logic did not run while the skid was down")
	}
	// A seed is not a reading: the driver reports the never-delivered value
	// NotConnected, which is what the HMI, Sparkplug and OPC UA carry.
	if q := rt.TagQuality("SkidFlow"); q != nio.NotConnected {
		t.Errorf("SkidFlow quality = %v, want NotConnected", q)
	}
}

// The bench examples are local I/O end to end: every input stays strict.
func TestBenchExamplesKeepLocalInputsStrict(t *testing.T) {
	for _, dir := range []string{"../../examples/epic-bench", "../../examples/plcnext-bench"} {
		t.Run(path.Base(dir), func(t *testing.T) {
			p, err := Load(os.DirFS(dir), "")
			if err != nil {
				t.Fatal(err)
			}
			n := 0
			for _, d := range p.Runtime.Tags {
				if d.Role != runtime.RoleInput {
					continue
				}
				n++
				if d.Comms {
					t.Errorf("%s is marked Comms on a local-I/O driver", d.Name)
				}
			}
			if n == 0 {
				t.Fatal("no inputs to check")
			}
		})
	}
}

// eip's companion is named after the driver, reduced to an identifier.
func TestOnlineTagFor(t *testing.T) {
	for in, want := range map[string]string{"": "eip__Online", "eip": "eip__Online", "eip-2": "eip_2__Online", "Line 3": "Line_3__Online", "2nd": "_nd__Online"} {
		if got := onlineTagFor(in, "eip"); got != want {
			t.Errorf("onlineTagFor(%q) = %q, want %q", in, got, want)
		}
	}
}
