package project

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/joyautomation/nautilus/eip"
	"github.com/joyautomation/nautilus/runtime"
	sphost "github.com/joyautomation/nautilus/sparkplug/host"
)

func eipHealthLike(connected bool, lastErr string, polls, pollErrors uint64) eip.Health {
	return eip.Health{
		Connected: connected, LastError: lastErr, Host: "plc", Slot: 0,
		Tags: 5, Polls: polls, PollErrors: pollErrors,
	}
}

func fsys(manifest string) fstest.MapFS {
	return fstest.MapFS{
		"nautilus.yaml": &fstest.MapFile{Data: []byte(manifest)},
		"program.fbd": &fstest.MapFile{Data: []byte(`PROGRAM Main
VAR_EXTERNAL Sensor : REAL; Setpoint : REAL; Actuator : REAL; END_VAR
FBD
  w_err = SUB(Setpoint, Sensor)
  Actuator := LIMIT(0.0, w_err, 100.0)
END_FBD
END_PROGRAM`)},
		"interlocks.ld": &fstest.MapFile{Data: []byte(`PROGRAM Interlocks
VAR_EXTERNAL Sensor : REAL; Setpoint : REAL; Alarm : BOOL; END_VAR
LD
  RUNG high
    GT(Sensor, Setpoint) ( Alarm )
END_LD
END_PROGRAM`)},
		"util.st": &fstest.MapFile{Data: []byte(`FUNCTION Doubled : REAL
VAR_INPUT X : REAL; END_VAR
Doubled := X * 2.0;
END_FUNCTION
`)},
	}
}

const manifest = `
name: test-plant
server:
  addr: "localhost:9911"
  online-edits: true
tasks:
  - program: program.fbd
    scan: 50ms
    dt-tag: ScanDtS
  - name: interlocks
    program: interlocks.ld
    scan: 200ms
tags:
  - { name: Sensor,   role: input, unit: "°C", desc: "the sensor" }
  - { name: Setpoint, role: setpoint, init: 50.0 }
  - { name: Actuator, role: output }
  - { name: Alarm,    role: output, init: false }
driver:
  type: memory
`

func TestLoad(t *testing.T) {
	p, err := Load(fsys(manifest), "")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "test-plant" || p.Addr != "localhost:9911" {
		t.Fatalf("name/addr: %q %q", p.Name, p.Addr)
	}
	if !p.Server.OnlineEdits {
		t.Fatal("online-edits must carry")
	}
	if p.Runtime.Scan != 50*time.Millisecond || p.Runtime.DtTag != "ScanDtS" {
		t.Fatalf("main task scan/dt: %v %q", p.Runtime.Scan, p.Runtime.DtTag)
	}
	if len(p.Runtime.Tasks) != 1 || p.Runtime.Tasks[0].Name != "interlocks" ||
		p.Runtime.Tasks[0].Scan != 200*time.Millisecond {
		t.Fatalf("tasks: %+v", p.Runtime.Tasks)
	}
	if len(p.Runtime.Libraries) != 1 || !strings.Contains(p.Runtime.Libraries[0], "FUNCTION Doubled") {
		t.Fatalf("libraries: %v", p.Runtime.Libraries)
	}
	if len(p.Runtime.Tags) != 4 {
		t.Fatalf("tags: %+v", p.Runtime.Tags)
	}

	// And the loaded project actually compiles and scans.
	rt, err := runtime.New(p.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	rt.Tags().SetReal("Sensor", 20.0)
	rt.Scan()
	if v := rt.Tags().Real("Actuator"); v != 30.0 {
		t.Fatalf("main task: Actuator = %v, want 30", v)
	}
	rt.Tags().SetReal("Sensor", 80.0)
	if err := rt.ScanTask("interlocks"); err != nil {
		t.Fatal(err)
	}
	if !rt.Tags().Bool("Alarm") {
		t.Fatal("interlocks task must trip the alarm above setpoint")
	}
}

func TestLoadErrors(t *testing.T) {
	cases := []struct {
		name, manifest, want string
	}{
		{"no tasks", "name: x\n", "at least one task"},
		{"typo key", "name: x\nserver:\n  onlin-edits: true\ntasks:\n  - program: program.fbd\n", "onlin-edits"},
		{"bad role", "tasks:\n  - program: program.fbd\ntags:\n  - { name: X, role: writable, init: 1.0 }\n", "role must be"},
		{"setpoint without init", "tasks:\n  - program: program.fbd\ntags:\n  - { name: S, role: setpoint }\n", "needs init"},
		{"missing program", "tasks:\n  - program: nope.ld\n", "nope.ld"},
		{"unknown driver", "tasks:\n  - program: program.fbd\ndriver: { type: modbus }\n", "Go tier"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(fsys(tc.manifest), "")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}

// yaml integers land as float64 for numeric seeds — an init of 50 and 50.0
// behave identically (the tag store is REAL-typed).
func TestInitNormalization(t *testing.T) {
	m := strings.Replace(manifest, "init: 50.0", "init: 50", 1)
	p, err := Load(fsys(m), "")
	if err != nil {
		t.Fatal(err)
	}
	rt, err := runtime.New(p.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	if v := rt.Tags().Real("Setpoint"); v != 50.0 {
		t.Fatalf("Setpoint = %v", v)
	}
}

func TestSparkplugConfig(t *testing.T) {
	m := manifest + `
sparkplug:
  broker: "tcp://localhost:1883"
  group-id: Joy
  device: plc
  store-forward: 5000
  default-class: { deadband: 0.5, max-interval: 30s }
  classes:
    fast: { deadband: 0.1, max-interval: 5s }
    alarms: { every-change: true }
  metric-classes:
    fast: ["Sensor"]
    alarms: ["Alarm", "*Alm"]
`
	p, err := Load(fsys(m), "")
	if err != nil {
		t.Fatal(err)
	}
	rt, err := runtime.New(p.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	node, err := p.Sparkplug(rt)
	if err != nil || node == nil {
		t.Fatalf("node: %v, %v", node, err)
	}

	// No section → no node; missing group-id → a readable error.
	p2, _ := Load(fsys(manifest), "")
	if n, err := p2.Sparkplug(rt); n != nil || err != nil {
		t.Fatalf("no-section must be (nil, nil): %v %v", n, err)
	}
	p3, err := Load(fsys(manifest+"\nsparkplug:\n  broker: tcp://x:1883\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p3.Sparkplug(rt); err == nil {
		t.Fatal("missing group-id must error")
	}
}

func TestEIPStatusStates(t *testing.T) {
	// Connected → connected; connected-but-failing → degraded; error state.
	connected := eipStatus(eipHealthLike(true, "", 5, 0))
	if connected.State != "connected" || connected.Kind != "ethernet-ip" {
		t.Fatalf("connected: %+v", connected)
	}
	degraded := eipStatus(eipHealthLike(true, "", 0, 3))
	if degraded.State != "degraded" {
		t.Fatalf("failing polls must be degraded: %+v", degraded)
	}
	errored := eipStatus(eipHealthLike(false, "connection refused", 0, 0))
	if errored.State != "error" || errored.LastError == "" {
		t.Fatalf("error: %+v", errored)
	}
	connecting := eipStatus(eipHealthLike(false, "", 0, 0))
	if connecting.State != "connecting" {
		t.Fatalf("connecting: %+v", connecting)
	}
}

// hostStatusLike is one host snapshot: connected, with `nodes` sites of
// which `online` have birthed and are up.
func hostStatusLike(connected bool, nodes, online int) sphost.Status {
	st := sphost.Status{
		Broker: "tcp://mqtt:1883", HostID: "central", Groups: []string{"PomonaWRD"},
		Connected: connected, Msgs: 42,
	}
	for i := range nodes {
		n := sphost.NodeStatus{
			Group: "PomonaWRD", EdgeNode: fmt.Sprintf("W%d", i+1),
			Online: i < online, BirthMs: time.Now().UnixMilli(), Metrics: 12,
		}
		st.Nodes = append(st.Nodes, n)
	}
	return st
}

func TestHostStatusStates(t *testing.T) {
	// Broker down → error, whatever the node rows say.
	errored := hostStatusLike(false, 2, 2)
	errored.LastError = "connection refused"
	if s := hostStatus(errored); s.State != "error" || s.Kind != "sparkplug-host" || s.LastError == "" {
		t.Fatalf("broker down: %+v", s)
	}

	// Connected but nothing has birthed → connecting. Births are not
	// retained, so this is the normal first second of a host's life.
	connecting := hostStatusLike(true, 2, 0)
	for i := range connecting.Nodes {
		connecting.Nodes[i].BirthMs = 0
	}
	if s := hostStatus(connecting); s.State != "connecting" {
		t.Fatalf("no births yet: %+v", s)
	}

	// A dark site degrades the whole driver — that is the alarm the HMI's
	// comms page exists to raise.
	if s := hostStatus(hostStatusLike(true, 3, 2)); s.State != "degraded" {
		t.Fatalf("one site offline must be degraded: %+v", s)
	}

	// Strict discovery seeing an unbound metric degrades it too, even with
	// every site up: the manifest and the wire disagree.
	strict := hostStatusLike(true, 2, 2)
	strict.Degraded, strict.Unknown = true, 3
	if s := hostStatus(strict); s.State != "degraded" {
		t.Fatalf("strict discovery: %+v", s)
	}

	all := hostStatus(hostStatusLike(true, 2, 2))
	if all.State != "connected" {
		t.Fatalf("all sites up: %+v", all)
	}
	if all.Detail != "tcp://mqtt:1883 · PomonaWRD" {
		t.Fatalf("detail = %q", all.Detail)
	}
	if all.Name != "central" {
		t.Fatalf("name = %q, want the host id", all.Name)
	}
	var sites string
	for _, m := range all.Metrics {
		if m.Label == "sites online" {
			sites = m.Text
		}
	}
	if sites != "2 / 2" {
		t.Fatalf("sites online = %q", sites)
	}
}

// Every edge node is a Devices row, and its devices are sub-rows flattened
// as "<edge>/<device>" — which is what makes 60 sites' comms status render
// with no HMI change at all.
func TestHostStatusDeviceRows(t *testing.T) {
	st := hostStatusLike(true, 2, 1)
	st.Nodes[0].Devices = []sphost.DeviceStatus{{ID: "PLC1", Online: true, Metrics: 5}}
	s := hostStatus(st)
	var ids []string
	for _, d := range s.Devices {
		ids = append(ids, d.ID)
	}
	want := []string{"W1", "W1/PLC1", "W2"}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("device rows = %v, want %v", ids, want)
	}
	if !strings.HasPrefix(s.Devices[0].Detail, "12 tags · born ") {
		t.Fatalf("online site detail = %q", s.Devices[0].Detail)
	}
	if s.Devices[2].Detail != "offline" {
		t.Fatalf("offline site detail = %q", s.Devices[2].Detail)
	}
	if _, ok := s.Extra["nodes"]; !ok {
		t.Fatal("extra.nodes must carry the full node rows for a richer page")
	}
}

// The sparkplug-host driver builds from the manifest tier with no broker
// anywhere — the guarantee `nautilus check` and `nautilus build` rest on —
// and DriverStatus picks it up so /api/drivers reports the sites.
func TestLoadSparkplugHostDriver(t *testing.T) {
	files := fsys(strings.Replace(manifest, "driver:\n  type: memory\n", "", 1) + `
driver:
  type: sparkplug-host
  broker: "tcp://mqtt.invalid:1883"
  group-id: PomonaWRD
  host-id: pomona-central
  manifest: sparkplug_manifest.yaml
  primary: true
  state-form: both
  reorder-timeout: 5s
  stale-after: 2m
  on-unknown: log
  rebirth-on-start: false
`)
	files["sparkplug_manifest.yaml"] = &fstest.MapFile{Data: []byte(`group: PomonaWRD
nodes:
    - edgenode: W6
tags:
    - { name: W6_Well_Level, node: W6, device: "", metric: Well/Level, type: Double, arraylen: 0, writable: false }
`)}
	p, err := Load(files, "")
	if err != nil {
		t.Fatalf("a sparkplug-host project must load with no broker: %v", err)
	}
	drv, ok := p.Runtime.Driver.(*sphost.Driver)
	if !ok {
		t.Fatalf("driver = %T, want *host.Driver", p.Runtime.Driver)
	}
	// The manifest's binding plus the synthesized companions.
	if got := drv.InputNames(); !reflect.DeepEqual(got, []string{"W6_Well_Level", "W6__LastBirthMs", "W6__Online"}) {
		t.Fatalf("InputNames = %v", got)
	}
	if got := drv.OutputNames(); !reflect.DeepEqual(got, []string{"W6__Rebirth"}) {
		t.Fatalf("OutputNames = %v", got)
	}
	if fn := p.DriverStatus(nil); fn == nil {
		t.Fatal("DriverStatus must report a sparkplug-host driver")
	} else if sts := fn(); len(sts) != 1 || sts[0].Kind != "sparkplug-host" {
		t.Fatalf("DriverStatus = %+v", sts)
	}
}

func TestLoadSparkplugHostErrors(t *testing.T) {
	base := `tasks:
  - program: program.fbd
driver:
  type: sparkplug-host
`
	for _, tc := range []struct{ name, extra, want string }{
		{"no broker", "  host-id: h\n  manifest: m.yaml\n  group-id: G\n", "broker is required"},
		{"no host-id", "  broker: tcp://b:1883\n  manifest: m.yaml\n  group-id: G\n", "host-id is required"},
		{"no manifest", "  broker: tcp://b:1883\n  host-id: h\n  group-id: G\n", "manifest"},
		{"no group", "  broker: tcp://b:1883\n  host-id: h\n  manifest: m.yaml\n", "group-id"},
		{"unknown on-unknown", "  broker: tcp://b:1883\n  host-id: h\n  manifest: m.yaml\n  group-id: G\n  on-unknown: shout\n", "unknown discovery mode"},
		{"unknown state-form", "  broker: tcp://b:1883\n  host-id: h\n  manifest: m.yaml\n  group-id: G\n  state-form: 4.0\n", "unknown state-form"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := fsys(base + tc.extra)
			files["m.yaml"] = &fstest.MapFile{Data: []byte("group: G\n")}
			_, err := Load(files, "")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}
