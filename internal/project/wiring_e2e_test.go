package project

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/modbus/slave"
	"github.com/joyautomation/nautilus/runtime"
)

const pumpTypes = `TYPE PumpCmd : STRUCT
  Start    : BOOL;
  SpeedRef : REAL;
END_STRUCT; END_TYPE
TYPE Pump : STRUCT
  Running  : BOOL;
  Speed    : REAL;
  SpeedSP  : REAL := 50.0;
  Cmd      : PumpCmd;
END_STRUCT; END_TYPE
`

const pumpWiring = `wirings:
  - type: Pump
    params: { endpoint: null, unit-id: 1 }
    sources:
      drive: { driver: modbus, endpoint: "{endpoint}", unit-id: "{unit-id}" }
    members:
      Running:      { from: drive, table: coil, address: 0 }
      Speed:        { from: drive, table: holding, address: 4, format: int16, scale: 0.1, unit: "%" }
      Cmd.Start:    { to: drive, table: coil, address: 10 }
      Cmd.SpeedRef: { to: drive, table: holding, address: 20, format: int16, scale: 0.1, readback: Speed }
`

func wiredProject(manifest, program string) fstest.MapFS {
	return fstest.MapFS{
		"nautilus.yaml": {Data: []byte(manifest)},
		"main.st":       {Data: []byte(program)},
		"types.st":      {Data: []byte(pumpTypes)},
	}
}

// startWired runs a loaded project's runtime and driver until the test ends.
func startWired(t *testing.T, proj *Project) *runtime.Runtime {
	t.Helper()
	rt, err := runtime.New(proj.Runtime)
	if err != nil {
		t.Fatalf("the wired project does not start: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	drv := proj.Runtime.Driver
	drv.(interface{ Start(context.Context) }).Start(ctx)
	go func() { rt.Run(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		<-done
		drv.(interface{ Stop() }).Stop()
	})
	return rt
}

// A Modbus instance end to end (#1458 slice 5), through the real Modbus
// driver against a slave standing in for the drive: its from: members read
// the device into the one struct tag, a program's command reaches the coil,
// a command nothing has written is never sent (its read-back is adopted,
// which is no command), an operator's write to a to: member is sent, and the
// connection's companion says the drive answers.
func TestModbusInstanceEndToEnd(t *testing.T) {
	srv := slave.NewServer("", nil)
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Stop)
	drive := srv.Unit(1)
	drive.SetCoil(0, true)
	drive.SetHolding(4, 500) // 50.0 %
	drive.SetHolding(20, 77) // the drive's own reference: nothing commands it yet

	proj, err := Load(wiredProject(`name: w
tasks: [{ program: main.st, scan: 20ms }]
drivers: [{ type: modbus, scan-rate: 20ms }]
tags:
  - { name: Demand, role: setpoint, type: BOOL, init: false }
`+pumpWiring+`instances:
  - { name: P101, type: Pump, desc: Lift pump 1, params: { endpoint: "`+srv.Addr()+`" } }
`, `PROGRAM Main
P101.Cmd.Start := Demand;
END_PROGRAM
`), "")
	if err != nil {
		t.Fatal(err)
	}
	rt := startWired(t, proj)

	eventually(t, "the from: members read the drive", func() bool {
		run, _ := rt.Tags().ReadPath("P101.Running")
		spd, _ := rt.Tags().ReadPath("P101.Speed")
		f, _ := spd.(float64)
		return run == true && f > 49.9 && f < 50.1
	})
	eventually(t, "the companion says the drive answers", func() bool { return rt.Tags().Bool("P101__Online") })
	if v, _ := rt.Tags().ReadPath("P101.SpeedSP"); v != 50.0 {
		t.Errorf("a memory member starts at its TYPE initial value: %v", v)
	}
	// Several scans and pushes: the program writes Cmd.Start FALSE every
	// scan (the device holds FALSE), and nothing writes Cmd.SpeedRef.
	time.Sleep(200 * time.Millisecond)
	if got := drive.Holding(20); got != 77 {
		t.Fatalf("Cmd.SpeedRef, which nothing wrote, was commanded to %d", got)
	}
	if v, _ := rt.Tags().ReadPath("P101.Cmd.SpeedRef"); v.(float64) < 49.9 || v.(float64) > 50.1 {
		t.Errorf("an unwritten Cmd.SpeedRef adopts its read-back Speed (50.0): %v", v)
	}

	if err := rt.Tags().SetPath("Demand", true); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the program's command reaches the coil", func() bool { return drive.Coil(10) })

	if err := rt.Tags().Write([]runtime.WriteOp{{Path: "P101.Cmd.SpeedRef", Value: 40.0}})[0]; err != nil {
		t.Fatalf("an operator's write to a to: member: %v", err)
	}
	eventually(t, "the operator's command reaches the register", func() bool { return drive.Holding(20) == 400 })

	if err := rt.Tags().Write([]runtime.WriteOp{{Path: "P101.Speed", Value: 1.0}})[0]; err == nil {
		t.Error("a write to a from: member must be refused")
	}
}

// An event: command assigned while its device is unreachable is not
// delivered late when the device answers again: a Start pressed during the
// outage must not fire on the reconnect (lead ruling on #1458 slice 9; the
// Modbus driver drops it, modbus TagBinding.Event). The command's next
// change goes out as usual.
func TestModbusEventCommandNotDeliveredLateAfterAnOutage(t *testing.T) {
	srv := slave.NewServer("127.0.0.1:0", nil)
	srv.Unit(1)
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	addr := srv.Addr()
	proj, err := Load(wiredProject(`name: w
tasks: [{ program: main.st, scan: 20ms }]
drivers: [{ type: modbus, scan-rate: 20ms }]
tags:
  - { name: Demand, role: setpoint, type: BOOL, init: false }
wirings:
  - type: Pump
    params: { endpoint: null }
    sources:
      drive: { driver: modbus, endpoint: "{endpoint}", unit-id: 1, timeout: 200ms }
    members:
      Running:   { from: drive, table: coil, address: 0 }
      Cmd.Start: { to: drive, table: coil, address: 10, event: true }
instances:
  - { name: P101, type: Pump, params: { endpoint: "`+addr+`" } }
`, `PROGRAM Main
P101.Cmd.Start := Demand;
END_PROGRAM
`), "")
	if err != nil {
		t.Fatal(err)
	}
	rt := startWired(t, proj)
	eventually(t, "the drive answers", func() bool { return rt.Tags().Bool("P101__Online") })

	srv.Stop()
	eventually(t, "the drive is gone", func() bool { return !rt.Tags().Bool("P101__Online") })
	if err := rt.Tags().SetPath("Demand", true); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond) // several scans assign Cmd.Start TRUE while it is gone

	back := slave.NewServer(addr, nil)
	drive := back.Unit(1)
	if err := back.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(back.Stop)
	eventually(t, "the drive answers again", func() bool { return rt.Tags().Bool("P101__Online") })
	time.Sleep(200 * time.Millisecond)
	if drive.Coil(10) {
		t.Fatal("Cmd.Start, assigned TRUE while the drive was gone, went out late on the reconnect")
	}

	if err := rt.Tags().SetPath("Demand", false); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if err := rt.Tags().SetPath("Demand", true); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the command's next change goes out", func() bool { return drive.Coil(10) })
}

// The hard condition of slice 5 (docs/design/one-struct-model.md §18): no
// push key is silently dropped. A project whose instance command no driver
// takes does not start: the runtime names the command and its key.
func TestInstanceCommandNoDriverTakesFailsLoudly(t *testing.T) {
	proj, err := Load(wiredProject(`name: w
tasks: [{ program: main.st }]
drivers: [{ type: modbus }]
`+pumpWiring+`instances:
  - { name: P101, type: Pump, params: { endpoint: 192.0.2.1 } }
`, "PROGRAM Main\nP101.Cmd.Start := TRUE;\nEND_PROGRAM\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	// What a binding spelled otherwise than the TYPE, or lost on the way to
	// the driver, would leave: the driver no longer takes Cmd.Start.
	proj.Runtime.Driver = withoutOutput{proj.Runtime.Driver, "P101.Cmd.Start"}
	_, err = runtime.New(proj.Runtime)
	if err == nil || !strings.Contains(err.Error(), `instance P101: command Cmd.Start (pushed as "P101.Cmd.Start") reaches no driver`) {
		t.Fatalf("a command no driver takes must refuse the start, naming it: %v", err)
	}
}

// withoutOutput is a driver that no longer takes one output.
type withoutOutput struct {
	nio.Driver
	drop string
}

func (w withoutOutput) InputNames() []string {
	return w.Driver.(interface{ InputNames() []string }).InputNames()
}

func (w withoutOutput) OutputNames() []string {
	var out []string
	for _, n := range w.Driver.(interface{ OutputNames() []string }).OutputNames() {
		if n != w.drop {
			out = append(out, n)
		}
	}
	return out
}

// What this build does not run is refused at load, naming the slice that
// brings it; a member the Modbus driver cannot bind is refused naming the
// instance, the member and why. Nothing is run half-way.
func TestWiredLoadRefusals(t *testing.T) {
	base := "name: w\ntasks: [{ program: main.st }]\n"
	inst := "instances:\n  - { name: P101, type: Pump, params: { endpoint: 192.0.2.1 } }\n"
	wire := func(members string) string {
		return base + `wirings:
  - type: Pump
    params: { endpoint: null }
    sources:
      drive: { driver: modbus, endpoint: "{endpoint}" }
    members:
` + members + inst
	}
	for name, tc := range map[string]struct{ manifest, want string }{
		"opcua source": {base + `wirings:
  - type: Pump
    sources: { ua: { driver: opcua-client } }
    members:
      Running: { from: ua, node: "ns=2;s=x" }
` + "instances:\n  - { name: P101, type: Pump }\n", `source "ua" uses the opcua-client driver: this build runs struct wiring on Modbus only (#1458 slice 5); OPC UA sources arrive with slice 9`},
		"wiring alarms": {wire("      Running: { from: drive, table: coil, address: 0 }\n    alarms:\n      - { member: Running, name: run }\n"), "alarms: in a wiring arrive with slice 9"},
		"set": {strings.Replace(wire("      Running: { from: drive, table: coil, address: 0 }\n"), "192.0.2.1 }", "192.0.2.1 }, set: { SpeedSP: 1.0 }", 1),
			"set: (initial values for memory members) arrives with slice 6"},
		"two sources": {base + `wirings:
  - type: Pump
    params: { a: null, b: null }
    sources:
      x: { driver: modbus, endpoint: "{a}" }
      y: { driver: modbus, endpoint: "{b}" }
    members:
      Running: { from: x, table: coil, address: 0 }
      Speed:   { from: y, table: holding, address: 0, format: float32 }
instances:
  - { name: P101, type: Pump, params: { a: 192.0.2.1, b: 192.0.2.2 } }
`, "instance P101 reads 2 connections (x, y): an instance on more than one connection arrives with slice 9"},
		"one device twice": {wire("      Running: { from: drive, table: coil, address: 0 }\n") + "  - { name: P102, type: Pump, params: { endpoint: 192.0.2.1 } }\n",
			"instance P101 and instance P102 are both on 192.0.2.1:502 unit 0"},
		"bool from a register": {wire("      Running: { from: drive, table: holding, address: 0, format: int16 }\n"),
			"instance P101: member Running: is BOOL, but holding 0 (format int16) delivers INT"},
		"a scaled reading into an integer": {strings.Replace(wire("      Count: { from: drive, table: holding, address: 0, format: int16, scale: 0.1 }\n"), "Pump", "Counter", -1),
			"member Count: is DINT, but holding 0 delivers REAL"},
		"a command to an input register": {wire("      Cmd.SpeedRef: { to: drive, table: input, address: 0, format: int16 }\n"),
			"is a command (to:), and table input is read-only"},
		"a whole struct": {wire("      Cmd: { from: drive, table: holding, address: 0 }\n"),
			"member Cmd: is a PumpCmd: a Modbus binding is one value"},
		"an unknown address key": {wire("      Running: { from: drive, table: coil, adress: 0 }\n"),
			`"adress" is not a Modbus address key`},
		"one member twice": {strings.Replace(wire("      Levels[01]: { to: drive, table: holding, address: 1, format: int16 }\n      Levels[1]: { memory: initial }\n"), "Pump", "Tank", -1),
			`members "Levels[01]" and "Levels[1]" are one member, Levels[1]`},
		"an event command re-sent every period": {wire("      Cmd.Start: { to: drive, table: coil, address: 10, event: true, rewrite: 5s }\n"),
			"is event: (never sent twice), and rewrite: re-sends it every period"},
		"one device under two spellings": {wire("      Running: { from: drive, table: coil, address: 0 }\n") + "  - { name: P102, type: Pump, params: { endpoint: \"192.0.2.1:502\" } }\n",
			"instance P101 and instance P102 are both on 192.0.2.1:502 unit 0"},
		"two commands on one register": {wire("      Cmd.Start: { to: drive, table: holding, address: 9, format: bit:3 }\n      Cmd.SpeedRef: { to: drive, table: holding, address: 8, format: int32 }\n"),
			"commands Cmd.SpeedRef and Cmd.Start both write holding 9"},
		"wire: a readback": {strings.Replace(wire("      Running: { from: drive, table: coil, address: 0 }\n"), "192.0.2.1 }", "192.0.2.1 }, wire: { Running: { readback: Speed } }", 1),
			"wire: Running may change an address or a source only"},
		"an unsigned register into a signed member of its width": {strings.Replace(wire("      Levels[1]: { from: drive, table: holding, address: 1, format: uint16 }\n"), "Pump", "Tank", -1),
			"member Levels[1]: is INT, but holding 1 delivers UINT"},
		"a signed register into an unsigned member": {strings.Replace(wire("      Raw: { from: drive, table: holding, address: 1, format: int16 }\n"), "Pump", "Tank", -1),
			"member Raw: is UINT, but holding 1 delivers INT"},
		"an address that does not parse": {strings.Replace(wire("      Running: { from: drive, table: coil, address: \"{reg}\" }\n"), "endpoint: null", "endpoint: null, reg: 70000", 1),
			"address 70000 is not 0..65535"},
	} {
		fsys := wiredProject(tc.manifest, "PROGRAM Main\nEND_PROGRAM\n")
		fsys["types.st"].Data = append(fsys["types.st"].Data, []byte("TYPE Counter : STRUCT\n  Count : DINT;\nEND_STRUCT; END_TYPE\nTYPE Tank : STRUCT\n  Levels : ARRAY[1..2] OF INT;\n  Raw : UINT;\nEND_STRUCT; END_TYPE\n")...)
		_, err := Load(fsys, "")
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v\nwant %q", name, err, tc.want)
		}
	}
}

// A param substitutes into a numeric address as its own type, and a
// per-instance wire: override moves one member; the bindings are named by
// the member's path as the TYPE spells it, whatever case the wiring uses.
func TestWiredModbusBindings(t *testing.T) {
	fsys := wiredProject(`name: w
tasks: [{ program: main.st }]
wirings:
  - type: Pump
    params: { endpoint: null, base: 4 }
    sources:
      drive: { driver: modbus, endpoint: "{endpoint}", unit-id: 3 }
    members:
      Running: { from: drive, table: coil, address: 0 }
      Speed:   { from: drive, table: holding, address: "{base}", format: int16, scale: 0.1 }
      Cmd.Start: { to: drive, table: coil, address: 10, event: true }
instances:
  - { name: P101, type: Pump, params: { endpoint: "192.0.2.1:1502" } }
  - { name: P102, type: Pump, params: { endpoint: 192.0.2.2, base: 40 }, wire: { Running: { address: 7 } } }
`, "PROGRAM Main\nEND_PROGRAM\n")
	if _, err := Load(fsys, ""); err != nil {
		t.Fatal(err)
	}
	m, err := ReadManifest(fsys, "")
	if err != nil {
		t.Fatal(err)
	}
	cfgs, err := driverConfigs(m)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfgs) != 1 || cfgs[0].Type != "modbus" {
		t.Fatalf("one Modbus driver is added for the instances: %+v", cfgs)
	}
	mm, err := modbusManifest(nil, cfgs[0])
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, b := range mm.Tags {
		got[b.Name] = fmt.Sprintf("%s %s %d w%v wo%v e%v", b.Source, b.Table, b.Address, b.Writable, b.WriteOnly, b.Event)
	}
	want := map[string]string{
		"P101.Running":   "P101 coil 0 wfalse wofalse efalse",
		"P101.Speed":     "P101 holding 4 wfalse wofalse efalse",
		"P101.Cmd.Start": "P101 coil 10 wtrue wotrue etrue",
		"P102.Running":   "P102 coil 7 wfalse wofalse efalse",
		"P102.Speed":     "P102 holding 40 wfalse wofalse efalse",
		"P102.Cmd.Start": "P102 coil 10 wtrue wotrue etrue",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("bindings\n got %v\nwant %v", got, want)
	}
	if len(mm.Sources) != 2 || mm.Sources[0].Host != "192.0.2.1" || mm.Sources[0].Port != 1502 || mm.Sources[0].UnitID != 3 || mm.Sources[1].ID != "P102" {
		t.Errorf("sources %+v", mm.Sources)
	}
}

// A running wired project has the live I/O screen too, and an edit to its
// wiring is not applied live: the instance's bindings are part of what a
// restart builds, so the candidate is refused, never half-applied with the
// old bindings still running.
func TestWiredProjectLiveIOApplyRefusesAWiringEdit(t *testing.T) {
	srv := slave.NewServer("", nil)
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Stop)
	manifest := `name: w
tasks: [{ program: main.st, scan: 20ms }]
drivers: [{ type: modbus, scan-rate: 20ms }]
` + pumpWiring + `instances:
  - { name: P101, type: Pump, params: { endpoint: "` + srv.Addr() + `" } }
`
	files := wiredProject(manifest, "PROGRAM Main\nEND_PROGRAM\n")
	r := startIORig(t, files, false)

	m, err := ReadManifest(files, "")
	if err != nil {
		t.Fatal(err)
	}
	cfgs, err := driverConfigs(m)
	if err != nil {
		t.Fatal(err)
	}
	if _, named, err := buildDrivers(files, *m); err != nil || len(named) != len(cfgs) || cfgs[0].Name != named[0].Name {
		t.Fatalf("driverConfigs and buildDrivers disagree on a wired project: %v %v %v", cfgs, named, err)
	}

	moved := withFile(files, "nautilus.yaml", strings.Replace(manifest, "address: 4,", "address: 5,", 1))
	v := r.io.Validate(r.io.Stage(moved, "upload", "test"))
	if v.Valid || !slices.Equal(failed(v), []string{"wiring"}) {
		t.Fatalf("a wiring edit must be refused by the wiring check alone (applied by a restart): valid %v, failed %v", v.Valid, failed(v))
	}
	// The same project unchanged has nothing to apply, and the wiring
	// check passes.
	if v := r.io.Validate(r.io.Stage(files, "upload", "test")); slices.Contains(failed(v), "wiring") {
		t.Errorf("an unchanged wiring fails the wiring check: %v", failed(v))
	}
}

// A devices: row and an instance share the project's one Modbus driver: the
// device adapter passes the instance's keys through, so the instance's
// commands and readings still reach it (the runtime would refuse to start
// otherwise), and a -m manifest wires its own instances.
func TestWiredInstanceBesideADeviceRow(t *testing.T) {
	fsys := devicesProject(t, `name: d
tasks: [{ program: main.st }]
devices: [{ name: CONV, profile: abb-acs355-fmbt21, endpoint: 192.0.2.9 }]
`+pumpWiring+`instances:
  - { name: P101, type: Pump, params: { endpoint: 192.0.2.1 } }
`, "PROGRAM Main\nP101.Cmd.Start := CONV.StatusWord.0;\nEND_PROGRAM\n")
	fsys["types.st"] = &fstest.MapFile{Data: []byte(pumpTypes)}
	fsys["field.yaml"] = &fstest.MapFile{Data: []byte("name: f\ntasks: [{ program: main.st }]\n" +
		"devices: [{ name: CONV, profile: abb-acs355-fmbt21, endpoint: 192.0.2.9 }]\n" + pumpWiring +
		"instances:\n  - { name: P101, type: Pump, params: { endpoint: 192.0.2.3 } }\n")}
	for _, manifest := range []string{"", "field.yaml"} {
		proj, err := Load(fsys, manifest)
		if err != nil {
			t.Fatalf("%q: %v", manifest, err)
		}
		if got := proj.Drivers(); len(got) != 1 {
			t.Fatalf("%q: drivers %+v, want the one Modbus driver", manifest, got)
		}
		if _, err := runtime.New(proj.Runtime); err != nil {
			t.Fatalf("%q: the instance's keys do not reach the shared driver: %v", manifest, err)
		}
	}
}

// A member's unit: and desc: reach the tag meta under its path, and the
// project's own tag-meta: wins.
func TestWiredMemberMeta(t *testing.T) {
	m, err := ReadManifest(wiredProject(`name: w
tasks: [{ program: main.st }]
tag-meta:
  P102.Speed: { unit: rpm }
`+strings.Replace(pumpWiring, `unit: "%" }`, `unit: "%", desc: "{desc} speed" }`, 1)+`instances:
  - { name: P101, type: Pump, desc: Lift pump 1, params: { endpoint: 192.0.2.1 } }
  - { name: P102, type: Pump, params: { endpoint: 192.0.2.2 } }
`, "PROGRAM Main\nEND_PROGRAM\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	if got := m.TagMeta["P101.Speed"]; got.Unit != "%" || got.Desc != "Lift pump 1 speed" {
		t.Errorf("P101.Speed meta %+v", got)
	}
	if got := m.TagMeta["P102.Speed"]; got.Unit != "rpm" || got.Desc != "" {
		t.Errorf("the project's own tag-meta must win: %+v", got)
	}
}

// An instance on a device a devices: row (or the driver's manifest) already
// polls is refused: one device, two connections.
func TestWiredInstanceOnADeviceRowsEndpoint(t *testing.T) {
	fsys := devicesProject(t, `name: d
tasks: [{ program: main.st }]
devices: [{ name: CONV, profile: abb-acs355-fmbt21, endpoint: 192.0.2.9, unit-id: 1 }]
`+pumpWiring+`instances:
  - { name: P101, type: Pump, params: { endpoint: "192.0.2.9:502" } }
`, "PROGRAM Main\nEND_PROGRAM\n")
	fsys["types.st"] = &fstest.MapFile{Data: []byte(pumpTypes)}
	_, err := Load(fsys, "")
	if err == nil || !strings.Contains(err.Error(), "instance P101 is on 192.0.2.9:502 unit 1, which the Modbus driver already polls as CONV") {
		t.Errorf("an instance on a device row's device: %v", err)
	}
}

// pumpCtlBlock is the function block of one-struct-model §2.4 and the
// struct-wiring guide: it takes the instance as a VAR_IN_OUT, commands it,
// and reads its from: members. It never assigns Running or Speed.
const pumpCtlBlock = `FUNCTION_BLOCK PumpCtl
  VAR_IN_OUT P : Pump; END_VAR
  VAR_INPUT Demand : BOOL; END_VAR
  P.Cmd.Start := Demand AND NOT P.Running;
  IF Demand THEN
    P.Cmd.SpeedRef := P.SpeedSP;
  END_IF;
END_FUNCTION_BLOCK
`

// The PumpCtl block on a Modbus instance, in sequence (#1458 slice 7): only
// what the block assigned on a call is committed. While there is no demand
// the conditional SpeedRef is not assigned, so the drive's own reference is
// never commanded to a zero; with demand it is sent once, and when demand
// ends it is left where it was.
func TestPumpCtlBlockSequence(t *testing.T) {
	srv := slave.NewServer("", nil)
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Stop)
	drive := srv.Unit(1)
	drive.SetCoil(0, false) // not running
	drive.SetHolding(4, 0)
	drive.SetHolding(20, 77) // the drive's own reference

	files := wiredProject(`name: w
tasks: [{ program: main.st, scan: 20ms }]
drivers: [{ type: modbus, scan-rate: 20ms }]
tags:
  - { name: Demand, role: setpoint, type: BOOL, init: false }
`+pumpWiring+`instances:
  - { name: P101, type: Pump, params: { endpoint: "`+srv.Addr()+`" } }
`, pumpCtlBlock+`PROGRAM Main
VAR Ctl : PumpCtl; END_VAR
  Ctl(P := P101, Demand := Demand);
END_PROGRAM
`)
	proj, err := Load(files, "")
	if err != nil {
		t.Fatal(err)
	}
	rt := startWired(t, proj)
	eventually(t, "the drive answers", func() bool { return rt.Tags().Bool("P101__Online") })

	// No demand: nothing is commanded, the drive's reference is untouched.
	time.Sleep(200 * time.Millisecond)
	if drive.Coil(10) || drive.Holding(20) != 77 {
		t.Fatalf("with no demand: start %v, reference %d (want false, 77)", drive.Coil(10), drive.Holding(20))
	}

	if err := rt.Tags().SetPath("Demand", true); err != nil {
		t.Fatal(err)
	}
	eventually(t, "demand starts the pump and sends its reference", func() bool {
		return drive.Coil(10) && drive.Holding(20) == 500 // SpeedSP 50.0 % at scale 0.1
	})

	// The drive's own reference moves while demand holds; nothing resends
	// the unchanged command, and ending demand leaves the reference alone.
	drive.SetHolding(20, 321)
	if err := rt.Tags().SetPath("Demand", false); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the end of demand stops the pump", func() bool { return !drive.Coil(10) })
	time.Sleep(200 * time.Millisecond)
	if got := drive.Holding(20); got != 321 {
		t.Errorf("the reference the block no longer assigns was commanded to %d (want the drive's own 321)", got)
	}
}
