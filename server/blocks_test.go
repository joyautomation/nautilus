package server

// The non-tag blocks' change gate, from the outside: a delta stream must
// carry the driver status, the scan diagnostics and the alarm counts when
// they have something new to say and NOT when they don't — while a plain
// stream, /api/state and every full frame still carry all of them.
//
// The failure this guards against is asymmetric. Sending a block too often
// costs bytes (the 4.35 MB/min floor this whole change exists to remove);
// sending it too rarely leaves an operator looking at a driver card that
// says "connected" about a node that dropped. So every test here that
// asserts an absence is paired with one that asserts the change lands.

import (
	"fmt"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/alarm"
)

// driverProbe is a settable driver-status provider: the test moves the
// plant, then ticks, exactly as a driver would between broadcasts.
type driverProbe struct{ st []DriverStatus }

func (p *driverProbe) fn() []DriverStatus { return p.st }

func oneDriver(state, msg string, online bool, msgs float64) []DriverStatus {
	return []DriverStatus{{
		Kind: "sparkplug", Name: "Plant/Edge1", Detail: "tcp://broker:1883",
		State: state, Message: msg, SinceMs: 1_700_000_000_000,
		Metrics: []DriverMetric{
			{Label: "messages", Value: msgs, Volatile: true},
			{Label: "bdSeq", Value: 3},
			{Label: "last publish", AtMs: 1_700_000_009_000, Text: "0.4s"},
		},
		Devices: []DriverDevice{{ID: "WEL15", Online: online, Detail: "812 tags"}},
		Extra:   map[string]any{"born": true, "primaryHost": "SCADA"},
	}}
}

// blockFixture is a delta stream over a controller with all three blocks.
type blockFixture struct {
	*streamFixture
	drv  *driverProbe
	cond map[string]any
	eng  *alarm.Engine
}

func openBlockStream(t *testing.T, o Options) *blockFixture {
	t.Helper()
	drv := &driverProbe{st: oneDriver("connected", "Publishing · bdSeq 3", true, 100)}
	cond := map[string]any{"HiTempAlm": false, "LoFlowAlm": false}
	eng := newTestAlarms(t, cond)
	eng.Evaluate()
	o.Drivers = drv.fn
	o.Alarms = eng
	f := openStream(t, newTestRuntime(t), "?delta=1&blocks=delta", o)
	return &blockFixture{streamFixture: f, drv: drv, cond: cond, eng: eng}
}

// carried names the non-tag blocks present on a frame, for a readable
// failure message.
func carried(f Frame) string {
	s := ""
	for _, b := range []struct {
		name string
		on   bool
	}{{"scan", f.Scan != nil}, {"drivers", f.Drivers != nil}, {"alarms", f.Alarms != nil}} {
		if b.on {
			if s != "" {
				s += "+"
			}
			s += b.name
		}
	}
	if s == "" {
		return "none"
	}
	return s
}

func wantBlocks(t *testing.T, what string, f Frame, scan, drivers, alarms bool) {
	t.Helper()
	if (f.Scan != nil) != scan || (f.Drivers != nil) != drivers || (f.Alarms != nil) != alarms {
		t.Fatalf("%s carried %s, want scan=%v drivers=%v alarms=%v",
			what, carried(f), scan, drivers, alarms)
	}
}

func TestDeltaStreamGatesNonTagBlocks(t *testing.T) {
	// A diagnostics cadence longer than the test, so the scan block moves
	// only for the reasons under test here (a full frame), and no resync.
	f := openBlockStream(t, Options{DiagnosticsInterval: time.Hour, ResyncInterval: -1})

	first := f.next()
	if !first.Full {
		t.Fatalf("first frame not full")
	}
	wantBlocks(t, "the first frame", first, true, true, true)

	// The connect repeat: a client is credited with nothing it was not
	// SENT by the broadcast loop, and the first frame came from the HTTP
	// handler — so the first broadcast re-offers all three. A superset,
	// once per connection, and the alternative is racing the revisions.
	f.tick()
	wantBlocks(t, "the first broadcast", f.next(), true, true, true)

	// Steady state: nothing moved, nothing sent. This is the whole point.
	for i := 0; i < 3; i++ {
		f.tick()
		fr := f.next()
		wantBlocks(t, "a quiet frame", fr, false, false, false)
		if fr.Full {
			t.Fatal("a quiet frame claimed to be full")
		}
	}

	// A free-running counter is not a change: the message count climbing
	// must not put 13 kB of driver status on the wire.
	f.drv.st = oneDriver("connected", "Publishing · bdSeq 3", true, 100_000)
	f.tick()
	wantBlocks(t, "a frame after the message counter moved", f.next(), false, false, false)

	// A device dropping IS a change.
	f.drv.st = oneDriver("degraded", "Device offline", false, 100_001)
	f.tick()
	d := f.next()
	wantBlocks(t, "a frame after a device dropped", d, false, true, false)
	if d.Drivers[0].State != "degraded" {
		t.Errorf("driver state = %q, want degraded", d.Drivers[0].State)
	}
	if d.Drivers[0].AsOfMs == 0 {
		t.Error("driver status has no asOfMs — a client cannot date its ages")
	}
	// …once. The next frame is quiet again.
	f.tick()
	wantBlocks(t, "the frame after the change", f.next(), false, false, false)

	// An alarm going active bumps the engine's Rev, and only then.
	f.cond["HiTempAlm"] = true
	f.eng.Evaluate()
	f.tick()
	a := f.next()
	wantBlocks(t, "a frame after an alarm went active", a, false, false, true)
	if a.Alarms.Active != 1 {
		t.Errorf("alarms.active = %d, want 1", a.Alarms.Active)
	}
	f.tick()
	wantBlocks(t, "the frame after the alarm", f.next(), false, false, false)
}

func TestDeltaResyncCarriesEveryBlock(t *testing.T) {
	// Resync on every tick: a full frame is a state a client can be rebuilt
	// from, so it carries all three blocks however recently they were sent.
	f := openBlockStream(t, Options{DiagnosticsInterval: time.Hour, ResyncInterval: time.Nanosecond})
	f.next() // the handler's first frame

	for i := 0; i < 2; i++ {
		f.tick()
		fr := f.next()
		if !fr.Full {
			t.Fatalf("frame %d is not a resync", i)
		}
		wantBlocks(t, "a resync frame", fr, true, true, true)
	}
}

func TestScanBlockRidesItsCadence(t *testing.T) {
	f := openBlockStream(t, Options{DiagnosticsInterval: 60 * time.Millisecond, ResyncInterval: -1})
	f.next()
	f.tick()
	f.next() // the connect repeat

	// Inside the cadence: the block stays off the wire.
	f.tick()
	wantBlocks(t, "a frame inside the diagnostics cadence", f.next(), false, false, false)

	// Past it: back on, once.
	time.Sleep(70 * time.Millisecond)
	f.tick()
	fr := f.next()
	wantBlocks(t, "a frame past the diagnostics cadence", fr, true, false, false)
	if fr.Scan.TargetMs == 0 {
		t.Error("scan block arrived empty")
	}
	f.tick()
	wantBlocks(t, "the frame after the cadence", f.next(), false, false, false)
}

// A negative DiagnosticsInterval is the escape hatch back to the old
// behaviour: the scan block on every frame.
func TestScanBlockOnEveryFrameWhenDisabled(t *testing.T) {
	f := openBlockStream(t, Options{DiagnosticsInterval: -1, ResyncInterval: -1})
	f.next()
	for i := 0; i < 3; i++ {
		f.tick()
		fr := f.next()
		if fr.Scan == nil {
			t.Fatalf("frame %d has no scan block with the cadence disabled", i)
		}
	}
}

// The compatibility guarantee that matters most: a DELTA client that did
// not ask for `?blocks=delta` — every HMI kit built before the gate existed
// — still gets all three blocks on every frame. Getting this wrong looks
// like a plant going quiet (a blank driver panel, an alarm banner that
// clears itself), which is exactly the failure the opt-in exists for.
func TestDeltaStreamWithoutOptInCarriesEveryBlock(t *testing.T) {
	drv := &driverProbe{st: oneDriver("connected", "Publishing", true, 1)}
	cond := map[string]any{"HiTempAlm": false, "LoFlowAlm": false}
	eng := newTestAlarms(t, cond)
	eng.Evaluate()
	f := openStream(t, newTestRuntime(t), "?delta=1", Options{
		Drivers: drv.fn, Alarms: eng, DiagnosticsInterval: time.Hour, ResyncInterval: -1,
	})
	f.next()
	for i := 0; i < 4; i++ {
		f.tick()
		fr := f.next()
		wantBlocks(t, "an un-opted-in delta frame", fr, true, true, true)
		if fr.Seq == 0 {
			t.Fatal("frame lost its delta markers")
		}
	}
}

// The compatibility guarantee: a client that asked for nothing gets every
// block on every frame, exactly as it always did.
func TestPlainStreamCarriesEveryBlockEveryFrame(t *testing.T) {
	drv := &driverProbe{st: oneDriver("connected", "Publishing", true, 1)}
	cond := map[string]any{"HiTempAlm": false, "LoFlowAlm": false}
	eng := newTestAlarms(t, cond)
	eng.Evaluate()
	f := openStream(t, newTestRuntime(t), "", Options{
		Drivers: drv.fn, Alarms: eng, DiagnosticsInterval: time.Hour,
	})
	f.next()
	for i := 0; i < 3; i++ {
		f.tick()
		fr := f.next()
		wantBlocks(t, "a plain frame", fr, true, true, true)
		if fr.Seq != 0 || fr.Full {
			t.Fatalf("plain frame carries delta markers: seq=%d full=%v", fr.Seq, fr.Full)
		}
	}
}

// ── the hash itself ───────────────────────────────────────────────────────

func TestHashDriversIgnoresWhatFreeRuns(t *testing.T) {
	// Two consecutive builds of a status where nothing an operator would
	// call a change happened: the counters ticked, the ages advanced, the
	// server's own stamp moved. The hash must not notice — this is the
	// property the entire gate rests on.
	a := oneDriver("connected", "Publishing · bdSeq 3", true, 100)
	b := oneDriver("connected", "Publishing · bdSeq 3", true, 4_000_000)
	a[0].AsOfMs = 1_700_000_010_000
	b[0].AsOfMs = 1_700_000_011_000
	b[0].Metrics[2].AtMs = 1_700_000_010_500
	b[0].Metrics[2].Text = "1.4s" // the same age, re-rendered
	if hashDrivers(a) != hashDrivers(b) {
		t.Fatal("a free-running counter, an age and the observation stamp changed the hash")
	}

	// And the things that ARE changes, one at a time.
	for _, tc := range []struct {
		name string
		fn   func(d *DriverStatus)
	}{
		{"state", func(d *DriverStatus) { d.State = "error" }},
		{"message", func(d *DriverStatus) { d.Message = "Broker unreachable" }},
		{"lastError", func(d *DriverStatus) { d.LastError = "i/o timeout" }},
		{"sinceMs", func(d *DriverStatus) { d.SinceMs = 42 }},
		{"a device going offline", func(d *DriverStatus) { d.Devices[0].Online = false }},
		{"a device's detail", func(d *DriverStatus) { d.Devices[0].Detail = "0 tags" }},
		{"a device appearing", func(d *DriverStatus) {
			d.Devices = append(d.Devices, DriverDevice{ID: "WEL16", Online: true})
		}},
		{"a non-volatile metric", func(d *DriverStatus) { d.Metrics[1].Value = 4 }},
		{"extra", func(d *DriverStatus) { d.Extra["born"] = false }},
		{"a metric's label", func(d *DriverStatus) { d.Metrics[0].Label = "msgs" }},
	} {
		c := oneDriver("connected", "Publishing · bdSeq 3", true, 100)
		tc.fn(&c[0])
		if hashDrivers(c) == hashDrivers(a) {
			t.Errorf("%s did not change the hash", tc.name)
		}
	}
}

func TestHashDriversHonoursVolatileExtra(t *testing.T) {
	base := oneDriver("connected", "Publishing", true, 1)
	base[0].VolatileExtra = []string{"nodes"}
	base[0].Extra["nodes"] = []any{map[string]any{"id": "A", "msgs": 1.0}}

	moved := oneDriver("connected", "Publishing", true, 1)
	moved[0].VolatileExtra = []string{"nodes"}
	moved[0].Extra["nodes"] = []any{map[string]any{"id": "A", "msgs": 9999.0}}
	if hashDrivers(base) != hashDrivers(moved) {
		t.Error("a volatile extra key changed the hash")
	}

	// A key NOT declared volatile still counts, and so does a volatile key
	// whose siblings moved.
	other := oneDriver("connected", "Publishing", true, 1)
	other[0].VolatileExtra = []string{"nodes"}
	other[0].Extra["nodes"] = []any{map[string]any{"id": "A", "msgs": 1.0}}
	other[0].Extra["primaryHost"] = "SCADA2"
	if hashDrivers(base) == hashDrivers(other) {
		t.Error("a non-volatile extra key did not change the hash")
	}
}

// An empty status list and a nil one are the same block — a controller
// with no drivers must not flip the revision on every tick.
func TestHashDriversStableWhenEmpty(t *testing.T) {
	if hashDrivers(nil) != hashDrivers([]DriverStatus{}) {
		t.Error("nil and empty driver lists hash differently")
	}
}

// ── the Pomona regression ─────────────────────────────────────────────────
//
// The first live deploy of the block gate did not hold: on the WRD host the
// driver status rode every frame anyway (3.0 MB/min to a client subscribed
// to no tags), because the churn was NESTED. Each element of Extra["nodes"]
// carried a last-message stamp and a Sparkplug sequence number that step on
// every message, one level below anything a top-level exclusion could
// reach. These are that shape, held still.

// hostLike is a Sparkplug-host-shaped status: one driver in front of n edge
// nodes, with the per-node roster in Extra that made the real block 13 kB.
// tick advances only the things that move on their own.
func hostLike(n int, tick int64) []DriverStatus {
	nodes := make([]any, 0, n)
	devs := make([]DriverDevice, 0, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("RTU%02d", i)
		devs = append(devs, DriverDevice{ID: id, Online: true, Detail: fmt.Sprintf("%d tags", 100+i)})
		nodes = append(nodes, map[string]any{
			"edgeNode": id, "group": "WRD", "online": true, "stale": false,
			"metrics": float64(100 + i), "bdSeq": float64(i % 7),
			"birthMs": float64(1_700_000_000_000),
			// The two free-runners, exactly as the host reported them.
			"lastMsgMs": float64(1_700_000_000_000 + tick*250),
			"seq":       float64(150 + tick),
		})
	}
	return []DriverStatus{{
		Kind: "sparkplug-host", Name: "pomona-central",
		Detail: "tcp://mqtt:1883 · PomonaWRD", State: "connected",
		Message: fmt.Sprintf("Consuming %d sites", n),
		SinceMs: 1_700_000_000_000, AsOfMs: 1_700_000_000_000 + tick*250,
		Metrics: []DriverMetric{
			{Label: "sites online", Value: float64(n), Text: fmt.Sprintf("%d / %d", n, n)},
			{Label: "sites stale", Value: 0},
			{Label: "messages", Value: float64(41149 + tick*25), Volatile: true},
			{Label: "rebirths", Value: 3},
			{Label: "last message", AtMs: 1_700_000_000_000 + tick*250, Text: "0.2s"},
		},
		Devices:       devs,
		Extra:         map[string]any{"nodes": nodes, "unknown": float64(0)},
		VolatileExtra: []string{"nodes.*.lastMsgMs", "nodes.*.seq"},
	}}
}

func TestHashDriversIgnoresNestedChurn(t *testing.T) {
	a, b := hostLike(55, 0), hostLike(55, 1)
	if hashDrivers(a) != hashDrivers(b) {
		t.Fatal("a quarter-second of ordinary Sparkplug traffic changed the hash — " +
			"the block would ride every frame, which is the bug this test exists for")
	}

	// The roster is still watched, which is the whole point of excluding the
	// two fields rather than the whole "nodes" key.
	for _, tc := range []struct {
		name string
		fn   func(d *DriverStatus)
	}{
		{"a site going offline", func(d *DriverStatus) {
			d.Extra["nodes"].([]any)[7].(map[string]any)["online"] = false
		}},
		{"a site going stale", func(d *DriverStatus) {
			d.Extra["nodes"].([]any)[7].(map[string]any)["stale"] = true
		}},
		{"a site's tag count", func(d *DriverStatus) {
			d.Extra["nodes"].([]any)[7].(map[string]any)["metrics"] = float64(1)
		}},
		{"a rebirth stamp", func(d *DriverStatus) {
			d.Extra["nodes"].([]any)[7].(map[string]any)["birthMs"] = float64(1_700_000_009_000)
		}},
		{"a site disappearing", func(d *DriverStatus) {
			d.Extra["nodes"] = d.Extra["nodes"].([]any)[:54]
		}},
		{"a device row going offline", func(d *DriverStatus) { d.Devices[7].Online = false }},
		{"a device row's detail", func(d *DriverStatus) { d.Devices[7].Detail = "100 tags · stale" }},
		{"an unmanifested metric", func(d *DriverStatus) { d.Extra["unknown"] = float64(1) }},
	} {
		c := hostLike(55, 0)
		tc.fn(&c[0])
		if hashDrivers(c) == hashDrivers(a) {
			t.Errorf("%s did not change the hash", tc.name)
		}
	}
}

// A path may also name a plain nested key, and a list is transparent to it —
// "nodes.lastMsgMs" is the spelling someone will reach for first.
func TestVolatileExtraPathForms(t *testing.T) {
	a, b := hostLike(3, 0), hostLike(3, 1)
	for _, form := range [][]string{
		{"nodes.*.lastMsgMs", "nodes.*.seq"},
		{"nodes.lastMsgMs", "nodes.seq"},
	} {
		a[0].VolatileExtra, b[0].VolatileExtra = form, form
		if hashDrivers(a) != hashDrivers(b) {
			t.Errorf("path form %v did not exclude the nested churn", form)
		}
	}

	// A bad path excludes nothing and breaks nothing: the block simply goes
	// out more often, which is the harmless direction.
	a[0].VolatileExtra, b[0].VolatileExtra = []string{"nope.*.gone"}, []string{"nope.*.gone"}
	if hashDrivers(a) == hashDrivers(b) {
		t.Error("an unmatched path silently excluded something")
	}
}
