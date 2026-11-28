package project

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	uaid "github.com/gopcua/opcua/id"
	uasrv "github.com/gopcua/opcua/server"
	"github.com/gopcua/opcua/ua"

	"github.com/joyautomation/nautilus/opcua"
	"github.com/joyautomation/nautilus/runtime"
)

const uaDriveNS = "urn:test:drive"

// uaDrive is an OPC UA server standing in for drives: per pump, Running and
// Speed (read) and Start and SpeedRef (written), under <pump>/<name> in
// namespace uaDriveNS. A test reads and writes it through a session of its
// own (gopcua's nodes are only safe through the server's services).
type uaDrive struct {
	srv  *uasrv.Server
	url  string
	port int
	ns   uint16
	sess *opcua.Session
}

func freeUAPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

func startUADrive(t *testing.T, port int, pumps ...string) *uaDrive {
	t.Helper()
	srv := uasrv.New(uasrv.EnableSecurity("None", ua.MessageSecurityModeNone), uasrv.EnableAuthMode(ua.UserTokenTypeAnonymous), uasrv.EndPoint("127.0.0.1", port))
	ns := uasrv.NewNodeNameSpace(srv, uaDriveNS)
	const rd, rw = uint8(ua.AccessLevelTypeCurrentRead), uint8(ua.AccessLevelTypeCurrentRead | ua.AccessLevelTypeCurrentWrite)
	for _, p := range pumps {
		root := ns.AddNode(uasrv.NewFolderNode(ua.NewStringNodeID(ns.ID(), p), p))
		srv.Node(uasrv.ObjectsFolder).AddRef(root, uaid.Organizes, true)
		add := func(name string, v any, typ uint32, access uint8) {
			n := ns.AddNewVariableStringNode(p+"/"+name, v)
			_ = n.SetAttribute(ua.AttributeIDDataType, uasrv.DataValueFromValue(ua.NewNumericNodeID(0, typ)))
			_ = n.SetAttribute(ua.AttributeIDAccessLevel, uasrv.DataValueFromValue(access))
			root.AddRef(n, uaid.HasComponent, true)
		}
		add("Running", true, uaid.Boolean, rd)
		add("Speed", 50.0, uaid.Double, rd)
		add("Start", false, uaid.Boolean, rw)
		add("SpeedRef", 0.0, uaid.Double, rw)
	}
	if err := srv.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	d := &uaDrive{srv: srv, url: fmt.Sprintf("opc.tcp://127.0.0.1:%d", port), port: port, ns: ns.ID()}
	t.Cleanup(d.close)
	return d
}

func (d *uaDrive) close() {
	if d.sess != nil {
		d.sess.Close()
		d.sess = nil
	}
	_ = d.srv.Close()
}

func (d *uaDrive) node(name string) *ua.NodeID { return ua.NewStringNodeID(d.ns, name) }

func (d *uaDrive) session(t *testing.T) *opcua.Session {
	t.Helper()
	if d.sess == nil {
		s, err := opcua.Dial(context.Background(), d.url, opcua.Connect{Timeout: 2 * time.Second, SessionName: "fixture"})
		if err != nil {
			t.Fatalf("fixture session: %v", err)
		}
		d.sess = s
	}
	return d.sess
}

func (d *uaDrive) get(t *testing.T, name string) any {
	t.Helper()
	resp, err := d.session(t).Client.Read(context.Background(), &ua.ReadRequest{NodesToRead: []*ua.ReadValueID{{NodeID: d.node(name), AttributeID: ua.AttributeIDValue}}})
	if err != nil || len(resp.Results) != 1 || resp.Results[0].Value == nil {
		t.Fatalf("fixture read %s: %v", name, err)
	}
	return resp.Results[0].Value.Value()
}

func (d *uaDrive) set(t *testing.T, name string, v any) {
	t.Helper()
	variant, err := ua.NewVariant(v)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := d.session(t).Client.Write(context.Background(), &ua.WriteRequest{NodesToWrite: []*ua.WriteValue{{
		NodeID: d.node(name), AttributeID: ua.AttributeIDValue,
		Value: &ua.DataValue{EncodingMask: ua.DataValueValue, Value: variant},
	}}})
	if err != nil || len(resp.Results) != 1 || resp.Results[0] != ua.StatusOK {
		t.Fatalf("fixture write %s: %v %v", name, err, resp)
	}
}

// uaPumpWiring wires Pump on OPC UA: source drive gives the server per
// instance ({endpoint}); its nodes are under {name}/.
const uaPumpWiring = `wirings:
  - type: Pump
    params: { endpoint: null }
    sources:
      drive: { driver: opcua-client, endpoint: "{endpoint}" }
    members:
      Running:      { from: drive, node: "ns=1;s={name}/Running", namespace: "` + uaDriveNS + `" }
      Speed:        { from: drive, node: "ns=1;s={name}/Speed", namespace: "` + uaDriveNS + `", type: Double, unit: "%" }
      Cmd.Start:    { to: drive, node: "ns=1;s={name}/Start", namespace: "` + uaDriveNS + `", event: true }
      Cmd.SpeedRef: { to: drive, node: "ns=1;s={name}/SpeedRef", namespace: "` + uaDriveNS + `", type: Double, readback: Speed }
`

func uaPumpProject(t *testing.T, endpoint string) *Project {
	t.Helper()
	proj, err := Load(wiredProject(`name: w
tasks: [{ program: main.st, scan: 20ms }]
tags:
  - { name: Demand, role: setpoint, type: BOOL, init: false }
`+uaPumpWiring+`instances:
  - { name: P101, type: Pump, desc: Lift pump 1, params: { endpoint: "`+endpoint+`" } }
`, "PROGRAM Main\nP101.Cmd.Start := Demand;\nEND_PROGRAM\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	return proj
}

// An OPC UA instance end to end (#1458 slice 9a-1), through the real OPC
// UA client driver against a server standing in for the drive: its from:
// members read the server into the one struct tag, its companion says the
// server answers, a program's command reaches its node, an operator's write
// to a to: member reaches its node, and a write to a from: member is
// refused. The connection is the instance's own driver, named after it.
func TestOPCUAInstanceEndToEnd(t *testing.T) {
	drive := startUADrive(t, freeUAPort(t), "P101")
	proj := uaPumpProject(t, drive.url)
	if ws := proj.WiringWarnings(); len(ws) != 1 || !strings.Contains(ws[0], "instance P101: source drive connects with security None, anonymous: it names no drivers: opcua-client section") {
		t.Errorf("warnings = %q, want the security None one", ws)
	}
	rt := startWired(t, proj)
	eventually(t, "the from: members read the server", func() bool {
		run, _ := rt.Tags().ReadPath("P101.Running")
		spd, _ := rt.Tags().ReadPath("P101.Speed")
		return run == true && spd == 50.0
	})
	eventually(t, "the companion says the server answers", func() bool { return rt.Tags().Bool("P101__Online") })

	if err := rt.Tags().SetPath("Demand", true); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the program's command reaches its node", func() bool { return drive.get(t, "P101/Start") == true })
	if err := rt.Tags().Write([]runtime.WriteOp{{Path: "P101.Cmd.SpeedRef", Value: 40.0}})[0]; err != nil {
		t.Fatalf("an operator's write to a to: member: %v", err)
	}
	eventually(t, "the operator's command reaches its node", func() bool { return drive.get(t, "P101/SpeedRef") == 40.0 })
	if err := rt.Tags().Write([]runtime.WriteOp{{Path: "P101.Speed", Value: 1.0}})[0]; err == nil {
		t.Error("a write to a from: member must be refused")
	}
}

// A server restart with its state reset (design §2.3, #1458 slice 9 write
// path): the level command is re-asserted on the new session, the event
// command the program still holds is not sent again, and the event's next
// change goes out.
func TestOPCUAInstanceServerRestart(t *testing.T) {
	port := freeUAPort(t)
	drive := startUADrive(t, port, "P101")
	proj := uaPumpProject(t, drive.url)
	rt := startWired(t, proj)
	eventually(t, "online", func() bool { return rt.Tags().Bool("P101__Online") })
	if err := rt.Tags().SetPath("Demand", true); err != nil {
		t.Fatal(err)
	}
	if err := rt.Tags().Write([]runtime.WriteOp{{Path: "P101.Cmd.SpeedRef", Value: 40.0}})[0]; err != nil {
		t.Fatal(err)
	}
	eventually(t, "both commands reach the server", func() bool {
		return drive.get(t, "P101/Start") == true && drive.get(t, "P101/SpeedRef") == 40.0
	})

	drive.close()
	eventually(t, "the server is gone", func() bool { return !rt.Tags().Bool("P101__Online") })
	again := startUADrive(t, port, "P101") // fresh state: Start FALSE, SpeedRef 0
	eventually(t, "the server answers again", func() bool { return rt.Tags().Bool("P101__Online") })
	eventually(t, "the level command is re-asserted", func() bool { return again.get(t, "P101/SpeedRef") == 40.0 })
	time.Sleep(300 * time.Millisecond)
	if again.get(t, "P101/Start") == true {
		t.Fatal("the event command the program holds was sent again on the new session")
	}
	if err := rt.Tags().SetPath("Demand", false); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if err := rt.Tags().SetPath("Demand", true); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the event's next change goes out", func() bool { return again.get(t, "P101/Start") == true })
}

// standbyLead is a coordinator that is never the leader.
type standbyLead struct{}

func (standbyLead) IsLeader() bool { return false }

// flipLead is a coordinator a test makes the leader.
type flipLead struct{ lead atomic.Bool }

func (l *flipLead) IsLeader() bool { return l.lead.Load() }

// A standby sends nothing to an instance's own OPC UA driver, and neither
// does a leader whose write gate is shut: the driver Load makes for the
// source is the project's driver, so it gets the gate every driver gets
// (naut run installs it on Runtime.Driver). Each case alone sends nothing.
func TestOPCUAInstanceStandbySendsNothing(t *testing.T) {
	for _, tc := range []struct {
		name          string
		standby, shut bool
	}{
		{"standby", true, false},
		{"gate shut", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			drive := startUADrive(t, freeUAPort(t), "P101")
			drive.set(t, "P101/SpeedRef", 7.0)
			proj := uaPumpProject(t, drive.url)
			drv, ok := proj.Runtime.Driver.(*opcua.Driver)
			if !ok {
				t.Fatalf("the instance's driver is %T, want its own *opcua.Driver", proj.Runtime.Driver)
			}
			lead := &flipLead{}
			lead.lead.Store(!tc.standby)
			proj.Runtime.Coordinator = lead
			var open atomic.Bool
			open.Store(!tc.shut)
			drv.SetWriteGate(open.Load)
			rt := startWired(t, proj)
			// The session must be up, or "nothing was sent" would prove
			// nothing (a standby reads nothing into its tags).
			eventually(t, "the session is up", func() bool { return drv.Health().State == "connected" })
			time.Sleep(100 * time.Millisecond)
			if err := rt.Tags().SetPath("Demand", true); err != nil {
				t.Fatal(err)
			}
			_ = rt.Tags().Write([]runtime.WriteOp{{Path: "P101.Cmd.SpeedRef", Value: 40.0}})
			time.Sleep(400 * time.Millisecond)
			if got := drive.get(t, "P101/Start"); got != false {
				t.Errorf("Start was commanded: %v", got)
			}
			if got := drive.get(t, "P101/SpeedRef"); got != 7.0 {
				t.Errorf("SpeedRef was commanded: %v", got)
			}
			// The path works, only the guard held it: lift it and a level
			// command goes out (Start is an event, whose first value after
			// a reopen or a new leadership is a baseline). A gate that
			// reopens re-asserts the held SpeedRef; a new leader sends an
			// operator's next write.
			lead.lead.Store(true)
			open.Store(true)
			want := 40.0
			if tc.standby {
				want = 41.0 // written (again) once the new leadership has begun
			}
			eventually(t, "a level command once the guard lifts", func() bool {
				if tc.standby {
					_ = rt.Tags().Write([]runtime.WriteOp{{Path: "P101.Cmd.SpeedRef", Value: want}})
				}
				return drive.get(t, "P101/SpeedRef") == want
			})
		})
	}
}

// A source with no endpoint shares the session of the drivers: entry it
// names: two instances' bindings in its one manifest, read and commanded
// over it, and its companion (<driver>__Online) declared once.
func TestOPCUAInstancesShareASession(t *testing.T) {
	drive := startUADrive(t, freeUAPort(t), "P101", "P102")
	proj, err := Load(wiredProject(`name: w
tasks: [{ program: main.st, scan: 20ms }]
drivers:
  - { name: plant-ua, type: opcua-client, endpoint: "`+drive.url+`", security: None, security-mode: None }
wirings:
  - type: Pump
    sources:
      drive: { driver: plant-ua }
    members:
      Running:   { from: drive, node: "ns=1;s={name}/Running", namespace: "`+uaDriveNS+`" }
      Cmd.Start: { to: drive, node: "ns=1;s={name}/Start", namespace: "`+uaDriveNS+`" }
instances:
  - { name: P101, type: Pump }
  - { name: P102, type: Pump }
`, "PROGRAM Main\nP102.Cmd.Start := TRUE;\nEND_PROGRAM\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, tg := range proj.Runtime.Tags {
		if strings.HasSuffix(tg.Name, "__Online") {
			n++
			if tg.Name != "plant_ua__Online" {
				t.Errorf("companion %s, want only the shared driver's plant_ua__Online", tg.Name)
			}
		}
	}
	if n != 1 {
		t.Errorf("%d companions declared, want the shared one once", n)
	}
	rt := startWired(t, proj)
	eventually(t, "both instances read over the one session", func() bool {
		a, _ := rt.Tags().ReadPath("P101.Running")
		b, _ := rt.Tags().ReadPath("P102.Running")
		return a == true && b == true && rt.Tags().Bool("plant_ua__Online")
	})
	eventually(t, "P102's command reaches its node", func() bool { return drive.get(t, "P102/Start") == true })
	if drive.get(t, "P101/Start") != false {
		t.Error("P101's node was commanded")
	}
}

// What an OPC UA source refuses at load, each naming its fix.
func TestOPCUAWiredLoadRefusals(t *testing.T) {
	base := "name: w\ntasks: [{ program: main.st }]\n"
	wire := func(drivers, source, members string) string {
		return base + drivers + `wirings:
  - type: Pump
    params: { endpoint: "opc.tcp://192.0.2.1:4840" }
    sources:
      drive: ` + source + `
    members:
` + members + "instances:\n  - { name: P101, type: Pump }\n"
	}
	own := `{ driver: opcua-client, endpoint: "{endpoint}" }`
	for name, tc := range map[string]struct{ manifest, want string }{
		"a command by namespace index alone": {wire("", own, "      Cmd.Start: { to: drive, node: \"ns=2;s=P/Start\" }\n"),
			"member Cmd.Start: is a command (to:) addressed by namespace index 2 alone: a server that renumbers its namespaces would take it at another node; add namespace: <uri>"},
		"no node":      {wire("", own, "      Running: { from: drive }\n"), "member Running: needs node:"},
		"a Modbus key": {wire("", own, "      Running: { from: drive, node: \"i=1\", table: coil }\n"), `"table" is not an OPC UA address key`},
		"a session key on the source": {wire("", `{ driver: opcua-client, endpoint: "{endpoint}", security: None }`, "      Running: { from: drive, node: \"i=1\" }\n"),
			`source drive: "security" is not an OPC UA source key`},
		"two commands on one node": {wire("", own, "      Cmd.Start: { to: drive, node: \"i=5\" }\n      Running: { from: drive, node: \"i=6\" }\n      SpeedSP: { to: drive, node: \"i=5\", type: Double }\n"),
			"commands Cmd.Start and SpeedSP both write node i=5"},
		"a BOOL on a Double":       {wire("", own, "      Running: { from: drive, node: \"i=1\", type: Double }\n"), "member Running: is BOOL, and its node is Double"},
		"an integer from a Double": {strings.Replace(wire("", own, "      Count: { from: drive, node: \"i=1\", type: Double }\n"), "Pump", "Counter", -1), "is DINT, and its node is Double"},
		"a numeric event command":  {wire("", own, "      Cmd.SpeedRef: { to: drive, node: \"i=1\", namespace: x, type: Double, event: true }\n"), "an event command is a BOOL"},
		"a shared source with no entry": {wire("", `{ driver: opcua-client }`, "      Running: { from: drive, node: \"i=1\" }\n"),
			"source drive gives no endpoint, so it shares the session of the drivers: entry it names, and the project has no opcua-client entry"},
		"a template with a manifest": {wire("drivers:\n  - { name: ua, type: opcua-client, endpoint: \"opc.tcp://192.0.2.9:4840\", manifest: m.yaml }\n", `{ driver: ua, endpoint: "{endpoint}" }`, "      Running: { from: drive, node: \"i=1\" }\n"),
			"driver: ua is the section the instance's own session is made from, which may set only security"},
		"an empty namespace": {wire("", own, "      Cmd.Start: { to: drive, node: \"ns=2;s=P/Start\", namespace: \"\" }\n"), "member Cmd.Start: namespace: is empty"},
		"a template that is also a shared session": {base + "drivers:\n  - { name: ua, type: opcua-client, endpoint: \"opc.tcp://192.0.2.9:4840\" }\n" + `wirings:
  - type: Pump
    params: { endpoint: "opc.tcp://192.0.2.1:4840" }
    sources:
      own: { driver: ua, endpoint: "{endpoint}" }
    members:
      Running: { from: own, node: "i=1" }
  - type: Counter
    sources:
      sh: { driver: ua }
    members:
      Count: { from: sh, node: "i=2", type: Int32 }
instances:
  - { name: P101, type: Pump }
  - { name: C1, type: Counter }
`, "driver: ua is the section the instance's own session is made from, which may set only security, security-mode, pki-dir, username, read-mode, scan-rate and reassert-on-reconnect, not endpoint"},
		"the type with two entries": {wire("drivers:\n  - { name: a, type: opcua-client, endpoint: \"opc.tcp://192.0.2.9:4840\" }\n  - { name: b, type: opcua-client, endpoint: \"opc.tcp://192.0.2.8:4840\" }\n", own, "      Running: { from: drive, node: \"i=1\" }\n"),
			"driver: opcua-client, and the project has more than one opcua-client driver: name the drivers: entry instead"},
		"a companion declared as something else": {strings.Replace(wire("drivers:\n  - { name: ua, type: opcua-client, endpoint: \"opc.tcp://192.0.2.9:4840\" }\n", `{ driver: ua }`, "      Running: { from: drive, node: \"i=1\" }\n"), "drivers:", "tags:\n  - { name: ua__Online, role: state, type: DINT }\ndrivers:", 1),
			"its shared OPC UA session's companion ua__Online is declared as a state DINT tag"},
		"a driver named like the instance": {wire("drivers:\n  - { name: P101, type: modbus }\n", own, "      Running: { from: drive, node: \"i=1\" }\n"),
			"the project already has a driver P101"},
	} {
		t.Run(name, func(t *testing.T) {
			fsys := wiredProject(tc.manifest, "PROGRAM Main\nEND_PROGRAM\n")
			fsys["types.st"].Data = append(fsys["types.st"].Data, []byte("TYPE Counter : STRUCT Count : DINT; END_STRUCT; END_TYPE\n")...)
			_, err := Load(fsys, "")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Load = %v\nwant it to say %q", err, tc.want)
			}
		})
	}
}

// naut check's warnings: a from: node read by namespace index alone, and a
// session made from a section that sets security None.
func TestOPCUAWiredWarnings(t *testing.T) {
	proj, err := Load(wiredProject(`name: w
tasks: [{ program: main.st }]
drivers:
  - { name: ua, type: opcua-client, security: None }
wirings:
  - type: Pump
    sources:
      drive: { driver: ua, endpoint: "opc.tcp://192.0.2.1:4840" }
    members:
      Running: { from: drive, node: "ns=3;s=P/Running" }
instances:
  - { name: P101, type: Pump }
`, "PROGRAM Main\nEND_PROGRAM\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(proj.WiringWarnings(), "\n")
	for _, want := range []string{
		"instance P101: member Running is read by namespace index 3 alone: a server that renumbers its namespaces would be misread; add namespace: <uri>",
		"instance P101: source drive connects with security None, as its drivers: section ua sets",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("warnings:\n%s\nwant %q", got, want)
		}
	}
}

// An event command assigned while the server is unreachable is not
// delivered late when it answers again: the instance's own driver drops
// it (opcua TagBinding.Event, which the wiring sets). Its next change goes
// out.
func TestOPCUAInstanceEventNotDeliveredLate(t *testing.T) {
	port := freeUAPort(t)
	drive := startUADrive(t, port, "P101")
	proj := uaPumpProject(t, drive.url)
	rt := startWired(t, proj)
	eventually(t, "online", func() bool { return rt.Tags().Bool("P101__Online") })
	drive.close()
	eventually(t, "the server is gone", func() bool { return !rt.Tags().Bool("P101__Online") })
	if err := rt.Tags().SetPath("Demand", true); err != nil {
		t.Fatal(err)
	}
	drv := proj.Runtime.Driver.(*opcua.Driver)
	eventually(t, "the press is queued while the server is gone", func() bool { return drv.Health().QueuedWrites == 1 })
	again := startUADrive(t, port, "P101")
	eventually(t, "the server answers again", func() bool { return rt.Tags().Bool("P101__Online") })
	time.Sleep(300 * time.Millisecond)
	if again.get(t, "P101/Start") == true {
		t.Fatal("Cmd.Start, assigned TRUE while the server was gone, went out late on the reconnect")
	}
	if got := drv.Health().DroppedEvents; got != 1 {
		t.Errorf("DroppedEvents = %d, want 1", got)
	}
	if err := rt.Tags().SetPath("Demand", false); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if err := rt.Tags().SetPath("Demand", true); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the command's next change goes out", func() bool { return again.get(t, "P101/Start") == true })
}

// The drivers and manifests Load derives: a per-instance source with no
// section is its own None session named after the instance, its commands
// writable and never polled (the event one marked), and a shared source
// naming the type binds into the project's one opcua-client entry, whatever
// that entry is named.
func TestWiredOPCUABindings(t *testing.T) {
	cfgs := func(manifest string) []DriverConfig {
		t.Helper()
		fsys := wiredProject(manifest, "PROGRAM Main\nEND_PROGRAM\n")
		if _, err := Load(fsys, ""); err != nil {
			t.Fatal(err)
		}
		m, err := ReadManifest(fsys, "")
		if err != nil {
			t.Fatal(err)
		}
		c, err := driverConfigs(m)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	own := cfgs("name: w\ntasks: [{ program: main.st }]\n" + uaPumpWiring + "instances:\n  - { name: P101, type: Pump, params: { endpoint: \"opc.tcp://192.0.2.1:4840\" } }\n")
	if len(own) != 1 || own[0].Name != "P101" || own[0].Endpoint != "opc.tcp://192.0.2.1:4840" || own[0].Security != "None" || own[0].SecurityMode != "None" {
		t.Fatalf("the instance's own driver: %+v", own)
	}
	om, err := opcuaManifest(nil, own[0])
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, b := range om.Tags {
		got[b.Name] = fmt.Sprintf("%s %s w%v %q e%v", b.NodeID, b.Type, b.Writable, b.ScanClass, b.Event)
	}
	want := map[string]string{
		"P101.Running":      `ns=1;s=P101/Running Boolean wfalse "" efalse`,
		"P101.Speed":        `ns=1;s=P101/Speed Double wfalse "" efalse`,
		"P101.Cmd.Start":    `ns=1;s=P101/Start Boolean wtrue "none" etrue`,
		"P101.Cmd.SpeedRef": `ns=1;s=P101/SpeedRef Double wtrue "none" efalse`,
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s: %s, want %s", k, got[k], w)
		}
	}

	shared := cfgs(`name: w
tasks: [{ program: main.st }]
drivers:
  - { name: plant-ua, type: opcua-client, endpoint: "opc.tcp://192.0.2.9:4840" }
wirings:
  - type: Pump
    sources:
      drive: { driver: opcua-client }
    members:
      Running: { from: drive, node: "ns=1;s={name}/Running", namespace: "` + uaDriveNS + `" }
instances:
  - { name: P101, type: Pump }
  - { name: P102, type: Pump }
`)
	if len(shared) != 1 {
		t.Fatalf("a shared session is the one entry: %+v", shared)
	}
	sm, err := opcuaManifest(nil, shared[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(sm.Tags) != 2 || sm.Tags[0].Name != "P101.Running" || sm.Tags[1].Name != "P102.Running" {
		t.Errorf("the shared entry's manifest: %+v", sm.Tags)
	}
}

// One drivers: section may be the template of devices: rows and of
// instances' own sessions alike: the instance's session gets the section's
// security, pki-dir and username, and naut check has no None warning to
// give. By name or by the type (the project's one opcua-client section).
func TestWiredOPCUATemplateSecurity(t *testing.T) {
	ice3, err := os.ReadFile("../../devprofile/profiles/pepperl-fuchs-ice3.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, drv := range []string{"sec", "opcua-client"} {
		t.Run(drv, func(t *testing.T) {
			fsys := wiredProject(`name: w
tasks: [{ program: main.st }]
drivers:
  - { name: sec, type: opcua-client, security: Basic256Sha256, security-mode: SignAndEncrypt, pki-dir: pki, username: op }
devices:
  - { name: PF1, profile: pepperl-fuchs-ice3, endpoint: "opc.tcp://192.0.2.5:4840", points: [Model], driver: sec, validate: static }
wirings:
  - type: Pump
    sources:
      drive: { driver: `+drv+`, endpoint: "opc.tcp://192.0.2.1:4840" }
    members:
      Running: { from: drive, node: "ns=1;s=P/Running", namespace: "urn:x" }
instances:
  - { name: P101, type: Pump }
`, "PROGRAM Main\nEND_PROGRAM\n")
			fsys["profiles/pepperl-fuchs-ice3.yaml"] = &fstest.MapFile{Data: ice3}
			proj, err := Load(fsys, "")
			if err != nil {
				t.Fatal(err)
			}
			for _, w := range proj.WiringWarnings() {
				if strings.Contains(w, "security None") {
					t.Errorf("a None warning for a secured session: %s", w)
				}
			}
			m, err := ReadManifest(fsys, "")
			if err != nil {
				t.Fatal(err)
			}
			cfgs, err := driverConfigs(m)
			if err != nil {
				t.Fatal(err)
			}
			for _, c := range cfgs {
				if c.Name != "P101" {
					continue
				}
				if c.Security != "Basic256Sha256" || c.SecurityMode != "SignAndEncrypt" || c.PKIDir != "pki" || c.Username != "op" || c.wiredFrom != "sec" {
					t.Fatalf("the instance's session lost its template's security: %+v", c)
				}
				return
			}
			t.Fatalf("no driver P101 in %+v", cfgs)
		})
	}
}
