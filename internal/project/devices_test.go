package project

import (
	"context"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/modbus"
	"github.com/joyautomation/nautilus/modbus/slave"
	"github.com/joyautomation/nautilus/opcua"
	"github.com/joyautomation/nautilus/runtime"
)

// acs355 is the built-in ACS355 + FMBT-21 profile, as `naut devices add`
// vendors it.
func acs355(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("../../devprofile/profiles/abb-acs355-fmbt21.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func devicesProject(t *testing.T, manifest, program string) fstest.MapFS {
	return fstest.MapFS{
		"nautilus.yaml":                   {Data: []byte(manifest)},
		"main.st":                         {Data: []byte(program)},
		"profiles/abb-acs355-fmbt21.yaml": {Data: acs355(t)},
		"profiles/abb-drives.st":          {Data: []byte("(* the simulator; not the controller's *)\n")},
	}
}

// A device declared by profile is one struct tag typed by the profile,
// whose members read back the drive, and a command tag per writable point
// that reaches the drive's register: end to end, through the real Modbus
// driver against a Modbus slave standing in for the drive, with a program
// that uses both.
func TestDeviceIsAStructTagWithCommandTags(t *testing.T) {
	srv := slave.NewServer("", nil)
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Stop)
	drive := srv.Unit(1)
	drive.SetHolding(3, 0x0237) // status word: READY TO SWITCH ON …
	drive.SetHolding(103, 125)  // 12.5 A

	fsys := devicesProject(t, `name: d
tasks: [{ program: main.st, scan: 20ms }]
drivers: [{ type: modbus, scan-rate: 20ms }]
devices:
  - { name: CONV, profile: abb-acs355-fmbt21, endpoint: "`+srv.Addr()+`", desc: Conveyor drive }
tags:
  - { name: Ready, role: state, type: BOOL }
  - { name: Amps,  role: state, type: REAL }
  - { name: Cmd,   role: setpoint, type: WORD, init: 0 }
`, `PROGRAM Main
Ready := CONV.StatusWord.0;
Amps := CONV.Current;
CONV_ControlWord := Cmd;
END_PROGRAM
`)
	proj, err := Load(fsys, "")
	if err != nil {
		t.Fatal(err)
	}
	types := map[string]string{}
	roles := map[string]bool{}
	for _, td := range proj.Runtime.Tags {
		types[td.Name] = td.Type
		switch td.Role {
		case runtime.RoleInput:
			roles["in:"+td.Name] = true
		case runtime.RoleOutput:
			roles["out:"+td.Name] = true
		}
	}
	if types["CONV"] != "ABB_ACS355_FMBT21" || !roles["in:CONV"] {
		t.Errorf("CONV: type %q, input %v; want an input of ABB_ACS355_FMBT21", types["CONV"], roles["in:CONV"])
	}
	for _, cmd := range []string{"CONV_ControlWord", "CONV_Reference1", "CONV_Reference2"} {
		if !roles["out:"+cmd] {
			t.Errorf("%s is not an output", cmd)
		}
	}
	if _, ok := types["CONV_StatusWord"]; ok {
		t.Error("a read-only point got a command tag")
	}

	rt, err := runtime.New(proj.Runtime)
	if err != nil {
		t.Fatalf("the project does not compile against the device type: %v", err)
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

	eventually(t, "the struct tag reads back the drive", func() bool {
		v, err := rt.Tags().ReadGlobal("CONV")
		if err != nil || v.Kind != ir.TypeStruct {
			return false
		}
		sw := v.Fld[v.Struct.FieldIndex["StatusWord"]]
		cur := v.Fld[v.Struct.FieldIndex["Current"]]
		return sw.I == 0x0237 && cur.F > 12.49 && cur.F < 12.51
	})
	eventually(t, "the program sees the members", func() bool {
		return rt.Tags().Bool("Ready") && rt.Tags().Real("Amps") > 12.4
	})
	// The command reaches the register. The first push is the driver's
	// start-up baseline (recorded, not sent), so the write is a change
	// made after it.
	for _, cw := range []uint16{0x0476, 0x047F} {
		if err := rt.Tags().SetPath("Cmd", int64(cw)); err != nil {
			t.Fatal(err)
		}
		eventually(t, "a command tag's write reaches the drive", func() bool { return drive.Holding(0) == cw })
	}
	eventually(t, "the read-back member shows what the drive holds", func() bool {
		v, _ := rt.Tags().ReadGlobal("CONV")
		return v.Kind == ir.TypeStruct && v.Fld[v.Struct.FieldIndex["ControlWord"]].I == int64(drive.Holding(0))
	})
}

// A command tag no program writes never reaches the device: not at start,
// and not when a standby whose gate was closed from boot takes over (the
// driver then has no baseline and its first push commands everything it
// is handed, #606). The runtime holds an output nothing has written out
// of every push, which is why naut check does not warn about a device's
// unwritten commands.
func TestUnwrittenCommandNeverReachesTheDevice(t *testing.T) {
	srv := slave.NewServer("", nil)
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Stop)
	drive := srv.Unit(1)
	drive.SetHolding(2, 777) // Reference2: the program never writes it
	fsys := devicesProject(t, `name: d
tasks: [{ program: main.st, scan: 20ms }]
drivers: [{ type: modbus, scan-rate: 20ms }]
devices:
  - { name: CONV, profile: abb-acs355-fmbt21, endpoint: "`+srv.Addr()+`" }
`, "PROGRAM Main\nCONV_ControlWord := 16#0476;\nEND_PROGRAM\n")
	proj, err := Load(fsys, "")
	if err != nil {
		t.Fatal(err)
	}
	var open atomic.Bool // a standby: the gate is closed from boot
	drv := proj.Runtime.Driver
	drv.(interface{ SetWriteGate(func() bool) }).SetWriteGate(open.Load)
	rt, err := runtime.New(proj.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	drv.(interface{ Start(context.Context) }).Start(ctx)
	go func() { rt.Run(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		<-done
		drv.(interface{ Stop() }).Stop()
	})
	eventually(t, "the standby reads the drive", func() bool {
		v, err := rt.Tags().ReadGlobal("CONV")
		return err == nil && v.Kind == ir.TypeStruct
	})
	// Several scans with the gate closed: the runtime has pushed its
	// outputs and the driver has refused them, as a standby's does.
	time.Sleep(200 * time.Millisecond)
	open.Store(true) // takeover
	eventually(t, "the written command lands after the takeover", func() bool { return drive.Holding(0) == 0x0476 })
	time.Sleep(200 * time.Millisecond) // several scans and pushes more
	if got := drive.Holding(2); got != 777 {
		t.Fatalf("Reference2, which no program writes, was commanded to %d on takeover", got)
	}
}

// A project loaded with another manifest (-m field.yaml) gets that
// manifest's device types, not nautilus.yaml's.
func TestDevicesInAnotherManifest(t *testing.T) {
	fsys := devicesProject(t, "name: bench\ntasks: [{ program: main.st }]\n", "PROGRAM Main\nIF CONV.StatusWord.3 THEN CONV_ControlWord := 16#0080; END_IF;\nEND_PROGRAM\n")
	fsys["field.yaml"] = &fstest.MapFile{Data: []byte(`name: field
tasks: [{ program: main.st }]
devices: [{ name: CONV, profile: abb-acs355-fmbt21, endpoint: 10.0.0.9 }]
`)}
	proj, err := Load(fsys, "field.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.New(proj.Runtime); err != nil {
		t.Fatalf("field.yaml's devices do not compile: %v", err)
	}
}

// A project without a Modbus driver gets one for its devices; a row whose
// profile is not vendored says how to vendor it; a derived tag that
// collides with a declared one names both.
func TestDevicesLoadErrors(t *testing.T) {
	ok := devicesProject(t, `name: d
tasks: [{ program: main.st }]
devices: [{ name: CONV, profile: abb-acs355-fmbt21, endpoint: 10.0.0.9 }]
`, "PROGRAM Main\nEND_PROGRAM\n")
	proj, err := Load(ok, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := proj.Drivers(); len(got) != 1 || got[0].Name != "modbus" {
		t.Errorf("drivers %+v, want the one modbus driver added for the devices", got)
	}

	for name, tc := range map[string]struct{ manifest, want string }{
		"not vendored": {`devices: [{ name: CONV, profile: abb-acs580-fmbt21, endpoint: 10.0.0.9 }]`, "naut devices add abb-acs580-fmbt21"},
		"collision": {"devices: [{ name: CONV, profile: abb-acs355-fmbt21, endpoint: 10.0.0.9 }]\ntags: [{ name: CONV_ControlWord, role: output, type: WORD }]",
			`tag "CONV_ControlWord" is declared in both devices: CONV and nautilus.yaml`},
		"two drivers": {"drivers: [{ type: modbus, name: a }, { type: modbus, name: b }]\ndevices: [{ name: CONV, profile: abb-acs355-fmbt21, endpoint: 10.0.0.9 }]",
			"more than one modbus driver"},
		"point typo":        {`devices: [{ name: CONV, profile: abb-acs355-fmbt21, endpoint: 10.0.0.9, points: [StatusWrod] }]`, `"StatusWrod" matches no point`},
		"not an identifier": {`devices: [{ name: 1CONV, profile: abb-acs355-fmbt21, endpoint: 10.0.0.9 }]`, "not an IEC identifier"},
	} {
		fsys := devicesProject(t, "name: d\ntasks: [{ program: main.st }]\n"+tc.manifest+"\n", "PROGRAM Main\nEND_PROGRAM\n")
		_, err := Load(fsys, "")
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
	}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("never: %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The driver carrying devices is the Modbus driver to everything that asks:
// it has the Modbus status row, and the device's struct tag is a comms
// input (it reads its init until the device first answers, then holds).
func TestDeviceDriverIsTheModbusDriver(t *testing.T) {
	fsys := devicesProject(t, `name: d
tasks: [{ program: main.st }]
devices: [{ name: CONV, profile: abb-acs355-fmbt21, endpoint: 10.0.0.9 }]
`, "PROGRAM Main\nEND_PROGRAM\n")
	proj, err := Load(fsys, "")
	if err != nil {
		t.Fatal(err)
	}
	rows := driverStatusFuncs(proj.Runtime.Driver)
	if len(rows) != 1 || rows[0]().Kind != modbusStatus(modbus.Health{}).Kind {
		t.Fatalf("status rows %d, want the one Modbus row", len(rows))
	}
	if !commsDriver(proj.Runtime.Driver) {
		t.Error("the driver carrying devices is not a comms driver")
	}
	for _, td := range proj.Runtime.Tags {
		if td.Name == "CONV" && !td.Comms {
			t.Error("the device's struct tag is not marked as a comms input")
		}
	}
}

// Two devices whose tags collide are named, each by its device.
func TestDeviceTagClashNamesBothDevices(t *testing.T) {
	fsys := devicesProject(t, `name: d
tasks: [{ program: main.st }]
devices:
  - { name: CONV, profile: abb-acs355-fmbt21, endpoint: 10.0.0.9 }
  - { name: CONV_ControlWord, profile: abb-acs355-fmbt21, endpoint: 10.0.0.10 }
`, "PROGRAM Main\nEND_PROGRAM\n")
	_, err := Load(fsys, "")
	if err == nil || !strings.Contains(err.Error(), "declared in both devices: CONV and devices: CONV_ControlWord") {
		t.Errorf("clash: %v", err)
	}
}

// An OPC UA device is an opcua-client driver of its own, named after it
// (an OPC UA client talks to one server): no Modbus driver is added for a
// project without Modbus devices, and the driver is the OPC UA driver to
// everything that asks (its status row, its health, comms).
func TestOPCUADevicesAreDriversOfTheirOwn(t *testing.T) {
	ice3, err := os.ReadFile("../../devprofile/profiles/pepperl-fuchs-ice3.yaml")
	if err != nil {
		t.Fatal(err)
	}
	project := func(manifest string) fstest.MapFS {
		fsys := devicesProject(t, "name: d\ntasks: [{ program: main.st }]\n"+manifest, "PROGRAM Main\nEND_PROGRAM\n")
		fsys["profiles/pepperl-fuchs-ice3.yaml"] = &fstest.MapFile{Data: ice3}
		return fsys
	}
	proj, err := Load(project("devices: [{ name: PF1, profile: pepperl-fuchs-ice3, endpoint: 10.0.0.21 }]\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	if got := proj.Drivers(); len(got) != 1 || got[0].Name != "PF1" {
		t.Fatalf("drivers %+v, want PF1's own", got)
	}
	drv := proj.Runtime.Driver
	rows := driverStatusFuncs(drv)
	if len(rows) != 1 || rows[0]().Kind != opcuaClientStatus(opcua.Health{}).Kind {
		t.Errorf("status rows %d, want the one OPC UA client row", len(rows))
	}
	if driverHealth(drv) == nil {
		t.Error("no health for the device's driver")
	}
	if !commsDriver(drv) {
		t.Error("the device's driver is not a comms driver")
	}
	u, ok := drv.(interface{ Unwrap() nio.Driver })
	if !ok {
		t.Fatalf("%T carries no devices", drv)
	}
	od, ok := u.Unwrap().(*opcua.Driver)
	if !ok || od.Endpoint() != "opc.tcp://10.0.0.21:4840" || od.OnlineTagName() != "PF1__Online" {
		t.Errorf("driver %T", u.Unwrap())
	}
	for _, td := range proj.Runtime.Tags {
		if td.Name == "PF1" && (td.Type != "PEPPERL_FUCHS_ICE3" || !td.Comms) {
			t.Errorf("PF1: %+v", td)
		}
	}

	proj, err = Load(project("devices:\n  - { name: CONV, profile: abb-acs355-fmbt21, endpoint: 10.0.0.9 }\n  - { name: PF1, profile: pepperl-fuchs-ice3, endpoint: 10.0.0.21 }\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, d := range proj.Drivers() {
		names = append(names, d.Name)
	}
	if strings.Join(names, " ") != "modbus PF1" {
		t.Errorf("drivers %v, want the Modbus devices' driver and PF1's", names)
	}

	_, err = Load(project("drivers: [{ type: memory, name: pf1 }]\ndevices: [{ name: PF1, profile: pepperl-fuchs-ice3, endpoint: 10.0.0.21 }]\n"), "")
	if err == nil || !strings.Contains(err.Error(), "the project already has a driver PF1") {
		t.Errorf("a device named as a driver: %v", err)
	}
}

// ports: decodes from nautilus.yaml in #705's shape (a map by port number,
// a typo an error), and is refused while no profile declares IO-Link
// ports, rather than ignored.
func TestDevicePortsInTheManifest(t *testing.T) {
	ifm, err := os.ReadFile("../../devprofile/profiles/ifm-al1590.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct{ ports, want string }{
		"refused": {"{ 2: { name: LEVEL1, iodd: sensor.xml } }", "device MASTER1: ports: profile ifm-al1590 declares no IO-Link ports"},
		"typo":    {"{ 2: { nme: LEVEL1 } }", "field nme not found"},
		"by name": {"{ two: { name: LEVEL1 } }", "cannot unmarshal"},
	} {
		fsys := fstest.MapFS{
			"nautilus.yaml":            {Data: []byte("name: d\ntasks: [{ program: main.st }]\ndevices:\n  - { name: MASTER1, profile: ifm-al1590, endpoint: 10.0.0.20, ports: " + tc.ports + " }\n")},
			"main.st":                  {Data: []byte("PROGRAM Main\nEND_PROGRAM\n")},
			"profiles/ifm-al1590.yaml": {Data: ifm},
			"iodd/sensor.xml":          {Data: []byte("<IODevice/>")},
		}
		if _, err := ReadManifest(fsys, ""); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
	}
}

// opcuaRows is a project with ice3 rows and drivers, as vendored.
func opcuaRows(t *testing.T, manifest string) fstest.MapFS {
	t.Helper()
	ice3, err := os.ReadFile("../../devprofile/profiles/pepperl-fuchs-ice3.yaml")
	if err != nil {
		t.Fatal(err)
	}
	fsys := devicesProject(t, "name: d\ntasks: [{ program: main.st }]\n"+manifest, "PROGRAM Main\nEND_PROGRAM\n")
	fsys["profiles/pepperl-fuchs-ice3.yaml"] = &fstest.MapFile{Data: ice3}
	return fsys
}

// A row's driver: names an opcua-client section that is a template: each
// device that names it gets its own driver with the section's connection
// settings and its own endpoint, and the section is never a driver itself
// (not to Load, not to the live I/O engine).
func TestOPCUADeviceDriverSection(t *testing.T) {
	fsys := opcuaRows(t, `drivers:
  - { name: iolm-secure, type: opcua-client, security: Basic256Sha256, security-mode: SignAndEncrypt, pki-dir: pki/client, username: plc, read-mode: subscribe, scan-rate: 250ms }
devices:
  - { name: PF1, profile: pepperl-fuchs-ice3, endpoint: 10.0.0.21, driver: iolm-secure }
  - { name: PF2, profile: pepperl-fuchs-ice3, endpoint: 10.0.0.22, driver: IOLM-SECURE }
  - { name: PF3, profile: pepperl-fuchs-ice3, endpoint: 10.0.0.23 }
`)
	m, err := ReadManifest(fsys, "")
	if err != nil {
		t.Fatal(err)
	}
	configs, err := withDevices(m.Drivers, m.DeviceSet())
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 3 {
		t.Fatalf("configs %+v, want the three devices' drivers and no template", configs)
	}
	for i, want := range []struct{ name, ep string }{{"PF1", "opc.tcp://10.0.0.21:4840"}, {"PF2", "opc.tcp://10.0.0.22:4840"}} {
		c := configs[i]
		if c.Name != want.name || c.Endpoint != want.ep || c.Security != "Basic256Sha256" || c.SecurityMode != "SignAndEncrypt" ||
			c.PKIDir != "pki/client" || c.Username != "plc" || c.ReadMode != "subscribe" || c.ScanRate != Duration(250*time.Millisecond) || c.opcuaDevice != want.name {
			t.Errorf("%s: %+v, want the section's settings at its own endpoint", want.name, c)
		}
	}
	if c := configs[2]; c.Security != "None" || c.SecurityMode != "None" || c.PKIDir != "" || c.Username != "" {
		t.Errorf("PF3 (no driver:): %+v, want None, anonymous", c)
	}
	units, err := driverConfigs(m)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range units {
		if strings.EqualFold(c.Name, "iolm-secure") {
			t.Error("the live I/O engine sees the template as a driver")
		}
	}
	proj, err := Load(fsys, "")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, d := range proj.Drivers() {
		names = append(names, d.Name)
	}
	if strings.Join(names, " ") != "PF1 PF2 PF3" {
		t.Errorf("drivers %v", names)
	}
}

func TestOPCUADeviceDriverSectionErrors(t *testing.T) {
	row := "devices: [{ name: PF1, profile: pepperl-fuchs-ice3, endpoint: 10.0.0.21, driver: sec }]\n"
	for name, tc := range map[string]struct{ manifest, want string }{
		"unknown":       {row, "driver: sec is not a section of drivers:"},
		"another type":  {"drivers: [{ name: sec, type: modbus }]\n" + row, "driver: sec is a modbus driver"},
		"endpoint":      {"drivers: [{ name: sec, type: opcua-client, endpoint: \"opc.tcp://h:4840\" }]\n" + row, "not endpoint"},
		"manifest":      {"drivers: [{ name: sec, type: opcua-client, manifest: m.yaml }]\n" + row, "not manifest"},
		"scan-classes":  {"drivers: [{ name: sec, type: opcua-client, scan-classes: { fast: 100ms } }]\n" + row, "not scan-classes"},
		"tag-classes":   {"drivers: [{ name: sec, type: opcua-client, tag-classes: { fast: [\"*\"] } }]\n" + row, "not tag-classes"},
		"another key":   {"drivers: [{ name: sec, type: opcua-client, host: h }]\n" + row, "not host"},
		"a modbus row":  {"drivers: [{ name: sec, type: opcua-client }]\ndevices: [{ name: CONV, profile: abb-acs355-fmbt21, endpoint: 10.0.0.9, driver: sec }]\n", "driver: is an OPC UA device's"},
		"a device name": {"drivers: [{ name: PF1, type: opcua-client }]\n" + strings.Replace(row, "driver: sec", "driver: PF1", 1), "the project already has a driver PF1"},
	} {
		if _, err := Load(opcuaRows(t, tc.manifest), ""); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
	}
	// A section no row names is a driver like any other: it needs its endpoint.
	if _, err := Load(opcuaRows(t, "drivers: [{ name: sec, type: opcua-client }]\n"), ""); err == nil || !strings.Contains(err.Error(), "endpoint is required") {
		t.Errorf("an unnamed template: %v", err)
	}
}

// What naut check says of a device's connection: None without a driver:
// section, one line for a section that sets None, the driver's own
// findings for a section that needs a certificate store it lacks, nothing
// about security for a secured section with one (the ice3's stand-in
// namespace is said either way).
func TestOPCUADeviceSecurityWarnings(t *testing.T) {
	for name, tc := range map[string]struct{ section, row, want, not string }{
		"no section":     {"", "", "connects with security None, anonymous", ""},
		"section None":   {"{ name: sec, type: opcua-client, security: None, security-mode: None }", ", driver: sec", "device PF1 connects with security None, as its driver: section sec sets", "anonymous"},
		"strongest":      {"{ name: sec, type: opcua-client }", ", driver: sec", "no pki-dir", "security None"},
		"secured, a pki": {"{ name: sec, type: opcua-client, security: Basic256Sha256, pki-dir: pki }", ", driver: sec", "binds by namespace index", "security"},
	} {
		manifest := "devices: [{ name: PF1, profile: pepperl-fuchs-ice3, endpoint: 10.0.0.21" + tc.row + " }]\n"
		if tc.section != "" {
			manifest = "drivers: [" + tc.section + "]\n" + manifest
		}
		proj, err := Load(opcuaRows(t, manifest), "")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		ws := strings.Join(proj.Runtime.Driver.(interface{ Warnings() []string }).Warnings(), "\n")
		if !strings.Contains(ws, tc.want) {
			t.Errorf("%s: warnings %q, want %q", name, ws, tc.want)
		}
		if tc.not != "" && strings.Contains(ws, tc.not) {
			t.Errorf("%s: warnings %q, want none saying %q", name, ws, tc.not)
		}
	}
}
