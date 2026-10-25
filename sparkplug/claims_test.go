// claims_test.go: tests written against the claims of the Sparkplug edge-node
// guide (docs/claims/sparkplug.yaml) that earlier tests only brushed past: the
// birth lists the whole tag store, devices and the node split the metrics,
// one dead device leaves the others born, the primary host defers the birth,
// store-and-forward is a bounded ring with a rate-limited drain, a zero
// deadband publishes any change, and runtime core packages never import MQTT
// or protobuf.

package sparkplug

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/runtime"
	"github.com/joyautomation/nautilus/sparkplug/spb"
)

// ── SPB-001: the birth lists the whole tag store ─────────────────────────

const wholeStoreProgramST = `
PROGRAM Main
VAR_EXTERNAL
	Running : BOOL;
	Count   : DINT;
	Level   : REAL;
	Label   : STRING;
	Motor1  : Motor;
END_VAR
END_PROGRAM
`

func TestBirthListsTheWholeTagStoreWithTypesAndValues(t *testing.T) {
	rt, err := runtime.New(runtime.Options{
		Program:   wholeStoreProgramST,
		Libraries: []string{propsTypesST},
		Driver:    nio.NewMemory(),
		Scan:      50 * time.Millisecond,
		Tags: []runtime.TagDef{
			runtime.State("Running", true),
			runtime.State("Count", int64(42)),
			runtime.State("Level", 3.5),
			runtime.State("Label", "Pump 1"),
			runtime.Typed("Motor1", runtime.RoleState, "Motor"),
		},
		Meta: map[string]runtime.TagMeta{
			"Motor1.Speed": {Unit: "rpm", Desc: "Pump 1 speed"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Distinctive current values: the birth must carry these, not the inits.
	rt.Tags().Set("Level", ir.RealVal(7.25))
	rt.Tags().Set("Count", ir.IntVal(99))

	n, err := New(rt, Config{GroupID: "Plant", EdgeNode: "Edge1"})
	if err != nil {
		t.Fatal(err)
	}
	fc := &fakeClient{open: true}
	n.cli = fc
	if err := n.birth(); err != nil {
		t.Fatal(err)
	}
	p := decodeLast(t, fc, "NBIRTH")
	byName := map[string]Metric{}
	for _, m := range p.Metrics {
		byName[m.Name] = m
	}

	// Every tag in the store is a metric of the birth.
	snap := rt.Tags().Snapshot()
	for name := range snap {
		if _, ok := byName[name]; !ok {
			t.Errorf("tag %s is in the store but not in the NBIRTH (%v)", name, metricNames(p.Metrics))
		}
	}
	for _, c := range []struct {
		name string
		typ  spb.DataType
		val  any
	}{
		{"Running", spb.DataType_Boolean, true},
		{"Count", spb.DataType_Int64, int64(99)},
		{"Level", spb.DataType_Double, 7.25},
		{"Label", spb.DataType_String, "Pump 1"},
	} {
		m, ok := byName[c.name]
		if !ok {
			continue
		}
		if m.Datatype != c.typ || m.Value != c.val {
			t.Errorf("%s: datatype %v value %v (%T), want %v / %v", c.name, m.Datatype, m.Value, m.Value, c.typ, c.val)
		}
	}
	tmpl, ok := byName["Motor1"].Value.(*Template)
	if !ok || tmpl == nil {
		t.Fatalf("Motor1 is not a template instance: %+v", byName["Motor1"])
	}
	var speed Metric
	for _, m := range tmpl.Metrics {
		if m.Name == "Speed" {
			speed = m
		}
	}
	if d := speed.PropertyString(PropDocumentation); d != "Pump 1 speed" {
		t.Errorf("Motor1.Speed documentation = %q, want the member's desc: (%+v)", d, speed.Properties)
	}
	if u := speed.PropertyString(PropEngUnit); u != "rpm" {
		t.Errorf("Motor1.Speed engUnit = %q, want rpm", u)
	}
}

// ── SPB-015 / SPB-016: devices ───────────────────────────────────────────

func newTwoDeviceNode(t *testing.T, healthA, healthB *bool) (*Node, *fakeClient, *runtime.Runtime) {
	t.Helper()
	rt, err := runtime.New(runtime.Options{
		Program: "PROGRAM P\nVAR_EXTERNAL\n  A_FIT_001 : REAL;\n  A__Online : BOOL;\n  B_FIT_001 : REAL;\n  B__Online : BOOL;\n  Calc : REAL;\n  Cmd : REAL;\nEND_VAR\nEND_PROGRAM",
		Driver:  nio.NewMemory(),
		Scan:    50 * time.Millisecond,
		Tags: []runtime.TagDef{
			runtime.Input("A_FIT_001"), runtime.Input("A__Online"),
			runtime.Input("B_FIT_001"), runtime.Input("B__Online"),
			runtime.State("Calc", 1.0),
			runtime.Output("Cmd"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []struct {
		n string
		v ir.Value
	}{
		{"A_FIT_001", ir.RealVal(1)}, {"A__Online", ir.BoolVal(true)},
		{"B_FIT_001", ir.RealVal(2)}, {"B__Online", ir.BoolVal(true)},
		{"Cmd", ir.RealVal(0)},
	} {
		if err := rt.Tags().WriteGlobal(v.n, v.v); err != nil {
			t.Fatal(err)
		}
	}
	n, err := New(rt, Config{GroupID: "Plant", EdgeNode: "Edge1"},
		WithDevice(Device{ID: "A", Tags: []string{"A_FIT_001", "A__Online"}, Health: func() bool { return *healthA }}),
		WithDevice(Device{ID: "B", Tags: []string{"B_FIT_001", "B__Online"}, Health: func() bool { return *healthB }}))
	if err != nil {
		t.Fatal(err)
	}
	fc := &fakeClient{open: true}
	n.cli = fc
	if err := n.birth(); err != nil {
		t.Fatal(err)
	}
	return n, fc, rt
}

// devicePubs returns the decoded metric names of the publishes of one message
// type addressed to one device (".../<TYPE>/Edge1/<device>").
func devicePubs(t *testing.T, fc *fakeClient, msgType, device string) [][]string {
	t.Helper()
	var out [][]string
	for _, p := range fc.published("/" + msgType + "/") {
		if !strings.HasSuffix(p.topic, "/"+msgType+"/Edge1/"+device) {
			continue
		}
		pl, err := DecodePayload(p.payload)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, metricNames(pl.Metrics))
	}
	return out
}

func TestMetricsSplitBetweenDevicesAndTheNode(t *testing.T) {
	up := true
	n, fc, _ := newTwoDeviceNode(t, &up, &up)
	tickWithin(t, n, 2*time.Second)

	nbirth := metricNames(decodeLast(t, fc, "NBIRTH").Metrics)
	for _, want := range []string{"Calc", "Cmd"} {
		found := false
		for _, m := range nbirth {
			found = found || m == want
		}
		if !found {
			t.Errorf("node-level tag %s is not in the NBIRTH: %v", want, nbirth)
		}
	}
	for _, m := range nbirth {
		if strings.HasPrefix(m, "A_") || strings.HasPrefix(m, "A__") || strings.HasPrefix(m, "B_") || strings.HasPrefix(m, "B__") {
			t.Errorf("device tag %s rides the NBIRTH; it belongs to its device", m)
		}
	}
	if got := devicePubs(t, fc, "DBIRTH", "A"); len(got) != 1 || strings.Join(got[0], ",") != "A_FIT_001,A__Online" {
		t.Errorf("device A DBIRTH = %v, want its own input and its __Online companion", got)
	}
	if got := devicePubs(t, fc, "DBIRTH", "B"); len(got) != 1 || strings.Join(got[0], ",") != "B_FIT_001,B__Online" {
		t.Errorf("device B DBIRTH = %v, want its own input and its __Online companion", got)
	}
}

func TestOneDeviceDyingLeavesTheOthersBorn(t *testing.T) {
	upA, upB := true, true
	n, fc, rt := newTwoDeviceNode(t, &upA, &upB)
	tickWithin(t, n, 2*time.Second)
	if a, b := len(devicePubs(t, fc, "DBIRTH", "A")), len(devicePubs(t, fc, "DBIRTH", "B")); a != 1 || b != 1 {
		t.Fatalf("DBIRTH A=%d B=%d, want both born once", a, b)
	}

	// Link A drops: A dies, B is untouched.
	upA = false
	tickWithin(t, n, 2*time.Second)
	if got := len(devicePubs(t, fc, "DDEATH", "A")); got != 1 {
		t.Fatalf("%d DDEATH for A, want 1", got)
	}
	if got := len(devicePubs(t, fc, "DDEATH", "B")); got != 0 {
		t.Fatalf("%d DDEATH for B though only A's link dropped", got)
	}
	// B still reports; A's values stop instead of going stale on the wire.
	rt.Tags().Set("B_FIT_001", ir.RealVal(5))
	rt.Tags().Set("A_FIT_001", ir.RealVal(9))
	tickWithin(t, n, 2*time.Second)
	if got := devicePubs(t, fc, "DDATA", "B"); len(got) != 1 || strings.Join(got[0], ",") != "B_FIT_001" {
		t.Errorf("device B DDATA = %v, want its change to keep flowing", got)
	}
	if got := devicePubs(t, fc, "DDATA", "A"); len(got) != 0 {
		t.Errorf("dead device A published DDATA %v", got)
	}
	if got := len(fc.published("NBIRTH")); got != 1 {
		t.Errorf("%d NBIRTH, want 1: one device dying must not rebirth the node", got)
	}
	if got := len(devicePubs(t, fc, "DBIRTH", "B")); got != 1 {
		t.Errorf("device B was born %d times, want once", got)
	}

	// The link comes back: only A is born again.
	upA = true
	tickWithin(t, n, 2*time.Second)
	if a, b := len(devicePubs(t, fc, "DBIRTH", "A")), len(devicePubs(t, fc, "DBIRTH", "B")); a != 2 || b != 1 {
		t.Errorf("after recovery DBIRTH A=%d B=%d, want 2 and 1", a, b)
	}
}

// ── SPB-017 / SPB-018: primary host ──────────────────────────────────────

// subClient is a fake client that also accepts subscriptions, so the node's
// onConnect can run synchronously.
type subClient struct {
	*fakeClient
	subs []string
}

func (c *subClient) Subscribe(topic string, _ byte, _ mqtt.MessageHandler) mqtt.Token {
	c.subs = append(c.subs, topic)
	return doneToken{}
}

type fakeMsg struct {
	topic   string
	payload []byte
}

func (m fakeMsg) Duplicate() bool   { return false }
func (m fakeMsg) Qos() byte         { return 1 }
func (m fakeMsg) Retained() bool    { return true }
func (m fakeMsg) Topic() string     { return m.topic }
func (m fakeMsg) MessageID() uint16 { return 0 }
func (m fakeMsg) Payload() []byte   { return m.payload }
func (m fakeMsg) Ack()              {}

func newPrimaryHostNode(t *testing.T, opts ...Option) (*Node, *subClient, *runtime.Runtime) {
	t.Helper()
	rt, err := runtime.New(runtime.Options{
		Program: silentLinkProgramST,
		Driver:  nio.NewMemory(),
		Scan:    50 * time.Millisecond,
		Tags: []runtime.TagDef{
			runtime.Setpoint("LevelSP", 10.0),
			runtime.State("LevelFt", 10.0),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	n, err := New(rt, Config{GroupID: "Plant", EdgeNode: "Edge1", PrimaryHostID: "ScadaHost"}, opts...)
	if err != nil {
		t.Fatal(err)
	}
	sc := &subClient{fakeClient: &fakeClient{open: true}}
	n.cli = sc
	return n, sc, rt
}

func TestPrimaryHostDefersTheBirthUntilOnline(t *testing.T) {
	for _, tc := range []struct {
		name, topic string
		online      []byte
	}{
		{"2.x plain ONLINE", "STATE/ScadaHost", []byte("ONLINE")},
		{"3.0 JSON", "spBv1.0/STATE/ScadaHost", []byte(`{"online":true,"timestamp":1000}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, sc, _ := newPrimaryHostNode(t)
			n.onConnect(sc)
			if got := len(sc.published("NBIRTH")); got != 0 {
				t.Fatalf("%d NBIRTH on connect with the primary host not heard from, want 0", got)
			}
			if n.Status().Born {
				t.Fatal("node reports born before its primary host is online")
			}
			// OFFLINE does not birth it either.
			n.handleState(sc, fakeMsg{tc.topic, []byte("OFFLINE")})
			if got := len(sc.published("NBIRTH")); got != 0 {
				t.Fatalf("%d NBIRTH after an OFFLINE STATE, want 0", got)
			}
			n.handleState(sc, fakeMsg{tc.topic, tc.online})
			if got := len(sc.published("NBIRTH")); got != 1 {
				t.Fatalf("%d NBIRTH once the host is ONLINE, want 1", got)
			}
			if !n.Status().Born {
				t.Error("node is not born after the host came online")
			}
		})
	}
}

func TestNoPrimaryHostBirthsOnConnect(t *testing.T) {
	n, sc, _ := newPrimaryHostNode(t)
	n.cfg.PrimaryHostID = ""
	n.onConnect(sc)
	if got := len(sc.published("NBIRTH")); got != 1 {
		t.Fatalf("%d NBIRTH on connect with no primary host, want 1", got)
	}
}

func TestStoreForwardBuffersWhileThePrimaryHostIsOffline(t *testing.T) {
	n, sc, rt := newPrimaryHostNode(t, WithStoreForward(10))
	n.onConnect(sc)
	n.handleState(sc, fakeMsg{"STATE/ScadaHost", []byte("ONLINE")})
	if len(sc.published("NBIRTH")) != 1 {
		t.Fatal("node did not birth")
	}

	// The host goes offline while the broker stays up.
	n.handleState(sc, fakeMsg{"STATE/ScadaHost", []byte("OFFLINE")})
	for i, v := range []float64{55, 56, 57} {
		rt.Tags().Set("LevelSP", ir.RealVal(v))
		tickWithin(t, n, 2*time.Second)
		if got := n.sf.len(); got != i+1 {
			t.Fatalf("after change %d the buffer holds %d, want %d", i+1, got, i+1)
		}
	}
	if got := len(sc.published("NDATA")); got != 0 {
		t.Fatalf("%d NDATA published while the primary host was offline, want 0", got)
	}
	// By default (HostOfflineBuffer) the node keeps its session: no NDEATH,
	// still born. (HostOfflineDisconnect ends it instead, as Sparkplug 3.0
	// has it; see hostoffline_test.go. A Disconnect would panic through the
	// fake client's nil embedded interface.)
	if got := len(sc.published("NDEATH")); got != 0 {
		t.Fatalf("%d NDEATH published when the primary host went offline, want 0", got)
	}
	if !n.Status().Born {
		t.Fatal("the node stopped being born when the primary host went offline")
	}

	// The host returns: the next tick replays all three, marked historical.
	n.handleState(sc, fakeMsg{"STATE/ScadaHost", []byte("ONLINE")})
	tickWithin(t, n, 2*time.Second)
	got := sc.published("NDATA")
	if len(got) != 3 {
		t.Fatalf("%d NDATA after the host returned, want the 3 buffered", len(got))
	}
	for i, p := range got {
		if _, hist := decodeSeq(t, p); !hist {
			t.Errorf("replayed record %d is not marked historical", i)
		}
	}
	if n.sf.len() != 0 {
		t.Errorf("%d records still buffered after the replay", n.sf.len())
	}
}

// Without store-and-forward, data from while the primary host is offline is
// not sent at all: nothing is published and nothing is kept for later.
func TestPrimaryHostOfflineWithoutStoreForwardSendsNothing(t *testing.T) {
	n, sc, rt := newPrimaryHostNode(t)
	n.onConnect(sc)
	n.handleState(sc, fakeMsg{"STATE/ScadaHost", []byte("ONLINE")})
	n.handleState(sc, fakeMsg{"STATE/ScadaHost", []byte("OFFLINE")})
	for _, v := range []float64{55, 56, 57} {
		rt.Tags().Set("LevelSP", ir.RealVal(v))
		tickWithin(t, n, 2*time.Second)
	}
	if got := len(sc.published("NDATA")); got != 0 {
		t.Fatalf("%d NDATA published while the primary host was offline, want 0", got)
	}
	if got := len(sc.published("NDEATH")); got != 0 {
		t.Fatalf("%d NDEATH published when the primary host went offline, want 0", got)
	}
	if n.sf != nil {
		t.Fatal("a node without store-forward has a buffer")
	}
	n.handleState(sc, fakeMsg{"STATE/ScadaHost", []byte("ONLINE")})
	tickWithin(t, n, 2*time.Second)
	for _, p := range sc.published("NDATA") {
		if _, hist := decodeSeq(t, p); hist {
			t.Fatal("a node without store-forward replayed historical data")
		}
	}
}

// ── SPB-019: the ring and the rate-limited drain, through the node ───────

func TestStoreForwardRingDropsOldestAndDrainsInBatches(t *testing.T) {
	const ring = 120
	n, fc, rt := newSilentLinkNode(t, WithStoreForward(ring))
	n.connectionLost(fc, os.ErrClosed)
	fc.set(false, false)

	// 130 changes during the outage into a ring of 120: the first 10 are lost.
	for i := 1; i <= 130; i++ {
		rt.Tags().Set("LevelSP", ir.RealVal(float64(100+i)))
		tickWithin(t, n, 2*time.Second)
	}
	if got := n.sf.len(); got != ring {
		t.Fatalf("buffer holds %d records, want the ring's %d", got, ring)
	}

	fc.set(true, false)
	if err := n.birth(); err != nil {
		t.Fatal(err)
	}
	// Each tick drains a bounded batch, not the whole backlog.
	var perTick []int
	prev := 0
	for len(fc.published("NDATA")) < ring {
		tickWithin(t, n, 2*time.Second)
		cur := len(fc.published("NDATA"))
		if cur == prev {
			t.Fatalf("tick delivered nothing with %d still buffered", n.sf.len())
		}
		perTick = append(perTick, cur-prev)
		prev = cur
		if len(perTick) > 10 {
			t.Fatal("drain never finished")
		}
	}
	if len(perTick) < 2 {
		t.Fatalf("the whole %d-record backlog drained in %v ticks; the drain is not rate-limited", ring, perTick)
	}
	for i, c := range perTick[:len(perTick)-1] {
		if c != perTick[0] {
			t.Errorf("tick %d drained %d, want the steady batch of %d (%v)", i, c, perTick[0], perTick)
		}
	}
	// Oldest dropped: the first replayed value is the 11th change, in order.
	first, err := DecodePayload(fc.published("NDATA")[0].payload)
	if err != nil {
		t.Fatal(err)
	}
	if v := first.Metrics[0].Value; v != 111.0 {
		t.Errorf("first replayed LevelSP = %v, want 111 (the 10 oldest were dropped)", v)
	}
	last, _ := DecodePayload(fc.published("NDATA")[ring-1].payload)
	if v := last.Metrics[0].Value; v != 230.0 {
		t.Errorf("last replayed LevelSP = %v, want 230 (the newest survives)", v)
	}
}

// ── SPB-010: a zero deadband publishes any change ────────────────────────

func TestRBEZeroDeadbandPublishesAnyChange(t *testing.T) {
	r := RBE{} // deadband 0, no intervals
	st := &rbeState{}
	t0 := time.Unix(0, 0)
	if !r.shouldPublish(st, ir.RealVal(10), t0) {
		t.Fatal("first value must publish")
	}
	st.record(ir.RealVal(10), 0, t0)
	if !r.shouldPublish(st, ir.RealVal(10.000001), t0.Add(time.Millisecond)) {
		t.Error("a zero deadband must publish a tiny numeric change")
	}
	st.record(ir.RealVal(10.000001), 0, t0)
	if r.shouldPublish(st, ir.RealVal(10.000001), t0.Add(2*time.Millisecond)) {
		t.Error("an unchanged value must stay quiet")
	}
	// Contrast: any non-zero deadband swallows the same change.
	wide := RBE{Deadband: 0.001}
	st2 := &rbeState{}
	wide.shouldPublish(st2, ir.RealVal(10), t0)
	st2.record(ir.RealVal(10), 0, t0)
	if wide.shouldPublish(st2, ir.RealVal(10.000001), t0.Add(time.Millisecond)) {
		t.Error("a 0.001 deadband must swallow a 1e-6 change")
	}
}

// ── SPB-024: MQTT and protobuf live only in the sparkplug package ────────

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test directory")
		}
		dir = parent
	}
}

func isWireLib(path string) bool {
	return strings.HasPrefix(path, "github.com/eclipse/paho") ||
		strings.HasPrefix(path, "google.golang.org/protobuf") ||
		strings.HasPrefix(path, "github.com/golang/protobuf")
}

func TestMQTTAndProtobufLiveOnlyInTheSparkplugPackage(t *testing.T) {
	root := moduleRoot(t)
	fset := token.NewFileSet()
	inSparkplug := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", "vendor", ".git", ".claude":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return nil // not this test's business
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		for _, imp := range f.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			if !isWireLib(p) {
				continue
			}
			if strings.HasPrefix(rel, "sparkplug/") {
				inSparkplug++
				continue
			}
			// The PLCnext driver's generated stubs for the firmware's
			// gRPC data-access service carry protobuf, not MQTT.
			if strings.HasPrefix(rel, "plcnext/gen/") && !strings.HasPrefix(p, "github.com/eclipse/paho") {
				continue
			}
			t.Errorf("%s imports %s; MQTT and protobuf belong to the sparkplug package", rel, p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if inSparkplug == 0 {
		t.Fatal("found no MQTT or protobuf import under sparkplug/: the scan is not looking at the right tree")
	}
}

// The runtime core (the scan loop, the language, the I/O seam) must not
// reach MQTT or protobuf through any import chain.
func TestRuntimeCoreDoesNotDependOnMQTTOrProtobuf(t *testing.T) {
	cmd := exec.Command("go", "list", "-deps", "./runtime", "./io", "./lang/ir", "./lang/st")
	cmd.Dir = moduleRoot(t)
	out, err := cmd.Output()
	if err != nil {
		t.Skipf("go list unavailable: %v", err)
	}
	deps := strings.Fields(string(out))
	if len(deps) < 20 {
		t.Fatalf("go list returned only %d packages: %v", len(deps), deps)
	}
	for _, d := range deps {
		if isWireLib(d) {
			t.Errorf("the runtime core depends on %s", d)
		}
	}
}

// SPB-024: the runtime core's one dependency outside the Go standard
// library is gopkg.in/yaml.v3; everything else it imports is this module.
func TestRuntimeCoreDependsOnlyOnStdlibAndYAML(t *testing.T) {
	cmd := exec.Command("go", "list", "-deps", "-f", "{{if not .Standard}}{{.ImportPath}}{{end}}",
		"./runtime", "./io", "./lang/ir", "./lang/st")
	cmd.Dir = moduleRoot(t)
	out, err := cmd.Output()
	if err != nil {
		t.Skipf("go list unavailable: %v", err)
	}
	var yaml bool
	for _, d := range strings.Fields(string(out)) {
		switch {
		case strings.HasPrefix(d, "github.com/joyautomation/nautilus/"):
		case d == "gopkg.in/yaml.v3":
			yaml = true
		default:
			t.Errorf("the runtime core depends on %s, outside the standard library and gopkg.in/yaml.v3", d)
		}
	}
	if !yaml {
		t.Error("gopkg.in/yaml.v3 is no longer a runtime dependency: the page can say stdlib-only again")
	}
}
