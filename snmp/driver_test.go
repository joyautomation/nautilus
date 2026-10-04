package snmp_test

// driver_test.go runs the real Driver — gosnmp client, hw.Base loop — against
// the in-repo agent on 127.0.0.1:0, serving the committed fixture walks
// through the manifests the importer generates from them. What it pins is
// brief §9.2: tags land typed; a counter becomes the right rate; the agent
// going away turns tags Stale with values held and never faults a read;
// coming back recovers; one vanished row leaves its siblings Good; Enable
// parks; a command reaches the wire once, after the baseline; and a wrong
// community reads as one.

import (
	"context"
	"io"
	"log/slog"
	"math"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/hw"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/snmp"
	"github.com/joyautomation/nautilus/snmp/agent"
	"github.com/joyautomation/nautilus/snmp/codegen"
	"github.com/joyautomation/nautilus/snmp/walk"
)

func loadWalk(t *testing.T, name string) walk.Walk {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	w, err := walk.ParseBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

// rig is one agent serving a fixture and the manifest imported from it,
// pointed at the agent with test-speed timing.
type rig struct {
	agent *agent.Agent
	m     snmp.Manifest
}

func newRig(t *testing.T, fixture, tag string) *rig {
	t.Helper()
	w := loadWalk(t, fixture)
	a := agent.New(w, "public")
	a.SetLogger(quiet())
	if err := a.Start("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Stop)
	host, port := splitAddr(t, a.Addr())
	out, err := codegen.Generate(w, codegen.Options{Tag: tag, Host: host, Port: port})
	if err != nil {
		t.Fatal(err)
	}
	m := out.Manifest
	zero := 0
	m.Sources[0].Timeout = 150 * time.Millisecond
	m.Sources[0].Retries = &zero
	m.Sources[0].Interval = 50 * time.Millisecond
	m.Sources[0].StaleAfter = 400 * time.Millisecond
	t.Setenv(m.Sources[0].CommunityEnv, "public")
	return &rig{agent: a, m: m}
}

func splitAddr(t *testing.T, addr string) (string, int) {
	t.Helper()
	i := strings.LastIndexByte(addr, ':')
	var port int
	for _, c := range addr[i+1:] {
		port = port*10 + int(c-'0')
	}
	return addr[:i], port
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func (r *rig) start(t *testing.T, opts ...snmp.Option) *snmp.Driver {
	t.Helper()
	d, err := snmp.New(r.m, append([]snmp.Option{snmp.WithLogger(quiet())}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	d.Start(ctx)
	t.Cleanup(func() { cancel(); d.Stop() })
	return d
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func read(t *testing.T, d *snmp.Driver) nio.Values {
	t.Helper()
	v, err := d.ReadInputs()
	if err != nil {
		t.Fatalf("ReadInputs must never error: %v", err)
	}
	return v
}

func member(t *testing.T, vals nio.Values, tag, m string) ir.Value {
	t.Helper()
	v, ok := vals[tag].(ir.Value)
	if !ok {
		t.Fatalf("%s not delivered (%T)", tag, vals[tag])
	}
	i, ok := v.Struct.FieldIndex[m]
	if !ok {
		t.Fatalf("%s has no member %s", tag, m)
	}
	return v.Fld[i]
}

func online(d *snmp.Driver, id string) bool {
	v, _ := d.ReadInputs()
	return v[id+"__Online"] == true
}

func lastPoll(d *snmp.Driver, id string) int64 {
	v, _ := d.ReadInputs()
	n, _ := v[id+"__LastPollMs"].(int64)
	return n
}

// waitPolls waits until n more complete polls have landed.
func waitPolls(t *testing.T, d *snmp.Driver, id string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		before := lastPoll(d, id)
		waitFor(t, "a poll", func() bool { return lastPoll(d, id) > before })
	}
}

func TestDriverDeliversTypedTags(t *testing.T) {
	r := newRig(t, "switch.snmpwalk", "SW1")
	d := r.start(t)
	waitFor(t, "SW1 online", func() bool { return online(d, "SW1") })
	waitPolls(t, d, "SW1", 1)
	vals := read(t, d)

	p1 := vals["SW1_Port01"].(ir.Value)
	if p1.Struct != hw.StructDef("SwitchPort") {
		t.Fatal("a port must carry THE shared SwitchPort StructDef")
	}
	if vals["SW1"].(ir.Value).Struct != hw.StructDef("Switch") || vals["SW1_Temp_CPU"].(ir.Value).Struct != hw.StructDef("TempSensor") {
		t.Fatal("root/sensor StructDefs")
	}
	checks := []struct {
		tag, member string
		want        any
	}{
		{"SW1", "Name", "sw1-lab"},
		{"SW1", "Model", "SYN-S28-4X"},
		{"SW1", "Serial", "SN-TEST-0001"},
		{"SW1", "UptimeS", int64(1234568)}, // 123456789 ticks × 0.01, rounded
		{"SW1", "PortsTotal", int64(28)},
		{"SW1", "TempC", 52.0},
		{"SW1_Port01", "Index", int64(1)},
		{"SW1_Port01", "Name", "Gi0/1"},
		{"SW1_Port01", "Alias", "uplink-core"},
		{"SW1_Port01", "AdminUp", true},
		{"SW1_Port01", "OperUp", true},
		{"SW1_Port01", "Down", false},
		{"SW1_Port01", "SpeedMbps", 1000.0},
		{"SW1_Port01", "InDiscards", int64(5)},
		{"SW1_Port03", "InErrors", int64(17)},
		{"SW1_Port04", "PoeOn", true},
		{"SW1_Port05", "PoeOn", false},
		{"SW1_Port07", "Down", true},  // admin up, no link: the alarm member
		{"SW1_Port20", "Down", false}, // admin down is not "down"
		{"SW1_Port07", "InPct", 0.0},  // unknown speed: 0%, not NaN
		{"SW1_Port25", "Name", "Te0/25"},
		{"SW1_Port25", "SpeedMbps", 10000.0},
		{"SW1_Temp_Inlet_Air", "Value", 28.4},
		{"SW1_Temp_Inlet_Air", "Name", "Inlet Air"},
		{"SW1_Temp_Inlet_Air", "Fault", false},
	}
	for _, c := range checks {
		got := member(t, vals, c.tag, c.member)
		var ok bool
		switch w := c.want.(type) {
		case string:
			ok = got.Kind == ir.TypeString && got.S == w
		case bool:
			ok = got.Kind == ir.TypeBool && got.B == w
		case int64:
			ok = got.Kind == ir.TypeInt && got.I == w
		case float64:
			ok = got.Kind == ir.TypeReal && math.Abs(got.F-w) < 1e-9
		}
		if !ok {
			t.Errorf("%s.%s = %+v, want %v", c.tag, c.member, got, c.want)
		}
	}
	if _, ok := vals["SW1_Port1001"]; ok {
		t.Error("the VLAN interface must not be a port")
	}
	if q := d.Quality(); len(q) != 0 {
		t.Errorf("quality after clean polls = %v", q)
	}
	h := d.Health()
	if h.Kind != "snmp" || h.Sources[0].State != "connected" || !h.Sources[0].Fresh || h.Sources[0].Tags != 31 {
		t.Errorf("health = %+v", h)
	}
	// 11 ifTable/ifXTable column walks of two PDUs each (rows 1..28 of 30,
	// max-repetitions 20, each walk stopping at its last bound row), one PDU
	// for the 8 PoE rows, and one Get for the scalars and single rows.
	if n := d.Health().Sources[0].Requests; n != 24 {
		t.Errorf("requests per poll = %d\n%s", n, d.Plan())
	}
}

// A counter that moves at a known rate by the wall clock reads back as that
// rate × 8 in bit/s, whatever the poll timing; the first poll delivers none.
func TestDriverCounterRate(t *testing.T) {
	r := newRig(t, "switch.snmpwalk", "SW1")
	const octetsPerSec = 125000.0 // 1 Mb/s
	if err := r.agent.Ramp("1.3.6.1.2.1.31.1.1.1.6.1", octetsPerSec); err != nil {
		t.Fatal(err)
	}
	if err := r.agent.Ramp("1.3.6.1.2.1.2.2.1.14.3", 400); err != nil { // ifInErrors: a storm
		t.Fatal(err)
	}
	d := r.start(t)
	waitFor(t, "SW1 online", func() bool { return online(d, "SW1") })
	vals := read(t, d)
	if v := member(t, vals, "SW1_Port01", "InBps"); v.F != 0 {
		// Rate members read 0 for exactly one interval (brief §5).
		t.Logf("InBps after the first poll = %v", v.F)
	}
	waitPolls(t, d, "SW1", 4)
	vals = read(t, d)
	in := member(t, vals, "SW1_Port01", "InBps").F
	if math.Abs(in-8*octetsPerSec)/(8*octetsPerSec) > 0.15 {
		t.Errorf("InBps = %v, want ≈ %v", in, 8*octetsPerSec)
	}
	pct := member(t, vals, "SW1_Port01", "InPct").F
	if math.Abs(pct-0.1) > 0.015 {
		t.Errorf("InPct = %v, want ≈ 0.1 (1 Mb/s of 1000)", pct)
	}
	er := member(t, vals, "SW1_Port03", "ErrorRate").F
	if math.Abs(er-400) > 60 {
		t.Errorf("ErrorRate = %v, want ≈ 400", er)
	}
	if v := member(t, vals, "SW1_Port02", "InBps").F; v != 0 {
		t.Errorf("a still counter rates %v", v)
	}
}

func TestDriverOutageAndRecovery(t *testing.T) {
	r := newRig(t, "switch.snmpwalk", "SW1")
	d := r.start(t)
	waitFor(t, "SW1 online", func() bool { return online(d, "SW1") })
	addr := r.agent.Addr()

	r.agent.Stop()
	waitFor(t, "SW1 offline", func() bool { return !online(d, "SW1") })
	waitFor(t, "Stale", func() bool { return d.Quality()["SW1_Port01"] == nio.Stale })
	vals := read(t, d)
	if member(t, vals, "SW1_Port01", "Name").S != "Gi0/1" || member(t, vals, "SW1", "Name").S != "sw1-lab" {
		t.Error("values must hold through an outage")
	}
	waitFor(t, "error state", func() bool { return d.Health().Sources[0].State == "error" })
	if e := d.Health().Sources[0].LastError; e == "" {
		t.Error("an outage must leave its reason on the health row")
	}

	if err := r.agent.Start(addr); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "SW1 back online", func() bool { return online(d, "SW1") })
	waitFor(t, "Good again", func() bool { return len(d.Quality()) == 0 })
	if d.Health().Sources[0].State != "connected" {
		t.Errorf("state = %s", d.Health().Sources[0].State)
	}
}

// A row that vanishes (a port removed from a stack, a sensor unplugged) is
// that tag Bad; its siblings stay Good and the source stays online.
func TestDriverOneBadTag(t *testing.T) {
	r := newRig(t, "switch.snmpwalk", "SW1")
	d := r.start(t)
	waitFor(t, "SW1 online", func() bool { return online(d, "SW1") })
	r.agent.Remove("1.3.6.1.2.1.2.2.1.8.5")
	waitFor(t, "Port05 Bad", func() bool { return d.Quality()["SW1_Port05"] == nio.Bad })
	q := d.Quality()
	if len(q) != 1 {
		t.Errorf("only Port05 should be non-Good: %v", q)
	}
	if !online(d, "SW1") || d.Health().Sources[0].BadTags != 1 {
		t.Errorf("health = %+v", d.Health().Sources[0])
	}
	r.agent.SetValue(walk.Varbind{OID: "1.3.6.1.2.1.2.2.1.8.5", Type: walk.Integer, Int: 1})
	waitFor(t, "Port05 Good", func() bool { return len(d.Quality()) == 0 })
}

// Enable false parks the source; the source starts parked until its enable
// tag is written, and wakes when it goes true.
func TestDriverEnableParks(t *testing.T) {
	r := newRig(t, "ups.snmpwalk", "UPS1")
	r.m.Sources[0].Enable = "CFG_PollUPS1"
	d := r.start(t)
	if names := d.OutputNames(); len(names) != 1 || names[0] != "CFG_PollUPS1" {
		t.Fatalf("OutputNames = %v", names)
	}
	waitFor(t, "parked", func() bool { return d.Health().Sources[0].State == "parked" })
	if q := d.Quality(); q["UPS1"] != nio.NotConnected {
		t.Errorf("never-polled quality = %v", q)
	}
	if err := d.WriteOutputs(nio.Values{"CFG_PollUPS1": true}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "UPS1 online", func() bool { return online(d, "UPS1") })
	vals := read(t, d)
	if member(t, vals, "UPS1", "InputV").F != 121 || member(t, vals, "UPS1", "LoadPct").F != 23 || member(t, vals, "UPS1", "OnBattery").B {
		t.Errorf("UPS1 = %+v", vals["UPS1"])
	}
	before := r.agent.Requests()["get"]
	if err := d.WriteOutputs(nio.Values{"CFG_PollUPS1": false}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "parked again", func() bool { return d.Health().Sources[0].State == "parked" })
	time.Sleep(200 * time.Millisecond)
	after := r.agent.Requests()["get"]
	time.Sleep(200 * time.Millisecond)
	if r.agent.Requests()["get"] != after || after < before {
		t.Error("a parked source must not poll")
	}
	// The UPS goes on battery while nobody is looking; waking shows it.
	r.agent.SetValue(walk.Varbind{OID: "1.3.6.1.2.1.33.1.4.1.0", Type: walk.Integer, Int: 5})
	if err := d.WriteOutputs(nio.Values{"CFG_PollUPS1": true}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "OnBattery", func() bool {
		v, _ := d.ReadInputs()
		u, ok := v["UPS1"].(ir.Value)
		return ok && u.Fld[u.Struct.FieldIndex["OnBattery"]].B
	})
}

// A command: the first value is a baseline and never written; a change is
// SET exactly once; the device's read-back becomes the member's value.
func TestDriverCommandWrite(t *testing.T) {
	r := newRig(t, "pdu.snmpwalk", "PDU1")
	const cmdOID = "1.3.6.1.4.1.3808.1.1.3.3.3.1.1.4.5"
	r.m.Writes = []snmp.Write{{Name: "PDU1_Outlet05_Cmd", Tag: "PDU1_Outlet05", Member: "On", OID: cmdOID, Set: map[string]int64{"true": 1, "false": 2}}}
	d := r.start(t)
	waitFor(t, "PDU1 online", func() bool { return online(d, "PDU1") })
	if member(t, read(t, d), "PDU1_Outlet05", "On").B {
		t.Fatal("outlet 5 starts off in the fixture")
	}

	// Baseline: the runtime's first snapshot describes the world — even a
	// true here must not switch the outlet at boot.
	if err := d.WriteOutputs(nio.Values{"PDU1_Outlet05_Cmd": true}); err != nil {
		t.Fatal(err)
	}
	waitPolls(t, d, "PDU1", 3)
	if n := len(r.agent.Sets()); n != 0 {
		t.Fatalf("the baseline was written (%d sets)", n)
	}
	// A change goes out once.
	if err := d.WriteOutputs(nio.Values{"PDU1_Outlet05_Cmd": false}); err != nil {
		t.Fatal(err)
	}
	if err := d.WriteOutputs(nio.Values{"PDU1_Outlet05_Cmd": true}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the SET", func() bool { return len(r.agent.Sets()) >= 1 })
	waitFor(t, "read-back", func() bool { return member(t, read(t, d), "PDU1_Outlet05", "On").B })
	if err := d.WriteOutputs(nio.Values{"PDU1_Outlet05_Cmd": true}); err != nil {
		t.Fatal(err)
	}
	waitPolls(t, d, "PDU1", 3)
	sets := r.agent.Sets()
	// false then true were coalesced into the last value per tag before the
	// loop flushed, or both went out; either way the wire ends at immediateOn
	// and an unchanged value is never repeated.
	last := sets[len(sets)-1]
	if last.OID != cmdOID || last.Int != 1 || len(sets) > 2 {
		t.Errorf("sets = %+v", sets)
	}
	if d.Health().Writes != uint64(len(sets)) {
		t.Errorf("health writes = %d, sets = %d", d.Health().Writes, len(sets))
	}
}

// v2c agents silently drop a wrong community: the source never answers,
// errors, and says why in words a tech can act on.
func TestDriverWrongCommunity(t *testing.T) {
	r := newRig(t, "ups.snmpwalk", "UPS1")
	t.Setenv(r.m.Sources[0].CommunityEnv, "not-the-community")
	d := r.start(t)
	waitFor(t, "error state", func() bool { return d.Health().Sources[0].State == "error" })
	e := d.Health().Sources[0].LastError
	if !strings.Contains(e, "wrong community") || strings.Contains(e, "not-the-community") {
		t.Errorf("LastError = %q (must name the likely cause and never the value)", e)
	}
	if q := d.Quality(); q["UPS1"] != nio.NotConnected {
		t.Errorf("quality = %v", q)
	}
	if online(d, "UPS1") {
		t.Error("online with the wrong community")
	}
}

// An unset credential is a connect-time error naming the variable.
func TestDriverUnsetCredential(t *testing.T) {
	r := newRig(t, "ups.snmpwalk", "UPS1")
	os.Unsetenv(r.m.Sources[0].CommunityEnv)
	d := r.start(t)
	waitFor(t, "error state", func() bool { return d.Health().Sources[0].State == "error" })
	if e := d.Health().Sources[0].LastError; !strings.Contains(e, "$SNMP_UPS1_COMMUNITY is not set") {
		t.Errorf("LastError = %q", e)
	}
}

// A PortList whose octets happen to be printable (0x31 0x32 → "12") is still
// a bitmap: read as bridge ports 3,4,8,11,12,15, not as the hex "12".
// Through the importer, the agent and the real driver, then served back.
func TestPrintablePortList(t *testing.T) {
	w := loadWalk(t, "switch.snmpwalk")
	var ifx []int64
	for _, vb := range w {
		if strings.HasPrefix(vb.OID, "1.3.6.1.2.1.2.2.1.1.") && len(ifx) < 6 {
			ifx = append(ifx, vb.Int)
		}
	}
	bitmap := []byte{0x31, 0x32, 0, 0}
	for i, bp := range []int{3, 4, 8, 11, 12, 15} {
		w = append(w, walk.Varbind{OID: "1.3.6.1.2.1.17.1.4.1.2." + strconv.Itoa(bp), Type: walk.Integer, Int: ifx[i]})
	}
	w = append(w,
		walk.Varbind{OID: "1.3.6.1.2.1.17.7.1.4.3.1.1.20", Type: walk.OctetString, Bytes: []byte("host-mgmt")},
		walk.Varbind{OID: "1.3.6.1.2.1.17.7.1.4.3.1.2.20", Type: walk.OctetString, Bytes: bitmap},
		walk.Varbind{OID: "1.3.6.1.2.1.17.7.1.4.3.1.4.20", Type: walk.OctetString, Bytes: make([]byte, 4)},
	)
	w.Sort()
	a := agent.New(w, "public")
	a.SetLogger(quiet())
	if err := a.Start("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Stop)
	host, port := splitAddr(t, a.Addr())
	out, err := codegen.Generate(w, codegen.Options{Tag: "SW1", Host: host, Port: port})
	if err != nil {
		t.Fatal(err)
	}
	m := out.Manifest
	m.Sources[0].Interval = 50 * time.Millisecond
	t.Setenv(m.Sources[0].CommunityEnv, "public")
	d := (&rig{agent: a, m: m}).start(t)
	waitFor(t, "SW1_Vlan20", func() bool { _, ok := read(t, d)["SW1_Vlan20"].(ir.Value); return ok })
	pos := func(n int) string { return strconv.Itoa(n) }
	want := strings.Join([]string{pos(1), pos(2), pos(3), pos(4), pos(5), pos(6)}, ",")
	if got := member(t, read(t, d), "SW1_Vlan20", "Ports").S; got != want {
		t.Fatalf("Ports = %q, want %q (the octets read as text)", got, want)
	}
	// Served back: the same octets, never the text "12".
	feeds, err := m.Feeds("SW1")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range feeds {
		if f.Tag == "SW1_Vlan20" && f.Member == "Ports" {
			cur, _ := w.Get(f.OID)
			vb, err := f.Serve(ir.StringVal(want), cur)
			if err != nil || string(vb.Bytes) != string(bitmap) {
				t.Fatalf("served %x %v, want %x", vb.Bytes, err, bitmap)
			}
			return
		}
	}
	t.Fatal("no feed for SW1_Vlan20.Ports")
}
