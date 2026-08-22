// state_test.go drives the whole state machine with no broker and no MQTT
// client: handleMessage takes a topic and bytes, so a test is nothing but
// sparkplug.Payload{...}.Encode() and assertions on the snapshot.
//
// OWNED BY B2 (state.go).

package host

import (
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/sparkplug"
	"github.com/joyautomation/nautilus/sparkplug/spb"
)

// ── fixtures ─────────────────────────────────────────────────────────────

// testManifest is one group, one edge node with one device, a Motor template
// and a Skid that nests it — enough to exercise every path.
func testManifest() Manifest {
	return Manifest{
		Group: "G",
		Types: []TypeDef{
			{Name: "Motor", Fields: []FieldDef{
				{Name: "Run", Type: "Boolean"},
				{Name: "Speed", Type: "Double"},
				{Name: "Label", Type: "String"},
			}},
			{Name: "Skid", Fields: []FieldDef{
				{Name: "Hours", Type: "Int64"},
				{Name: "Drive", Type: "Motor"},
			}},
		},
		Nodes: []Node{{
			EdgeNode: "W6",
			Prefix:   "W6",
			Devices:  []Device{{Device: "PLC1"}},
		}},
		Tags: []Binding{
			{Name: "W6_Well_Level", Node: "W6", Metric: "Well/Level", Type: "Double"},
			{Name: "W6_Site", Node: "W6", Metric: "Site", Type: "String"},
			{Name: "W6_Pump1", Node: "W6", Metric: "Pump1", Type: "Motor"},
			{Name: "W6_Skid1", Node: "W6", Metric: "Skid1", Type: "Skid"},
			{Name: "W6_PLC1_Pump_Run", Node: "W6", Device: "PLC1", Metric: "Pump/Run", Type: "Boolean"},
			{Name: "W6_PLC1_Pump_SpeedSP", Node: "W6", Device: "PLC1", Metric: "Pump/SpeedSP",
				Type: "Double", Writable: true, Init: 0.0},
		},
	}
}

// rebirthLog records what the state machine asked the transport to do.
type rebirthLog struct {
	ch chan nodeKey
}

func newRebirthLog() *rebirthLog { return &rebirthLog{ch: make(chan nodeKey, 32)} }

func (r *rebirthLog) hook(group, edge string) {
	select {
	case r.ch <- nodeKey{Group: group, EdgeNode: edge}:
	default:
	}
}

// wait returns the next rebirth request, or fails after d.
func (r *rebirthLog) wait(t *testing.T, d time.Duration) nodeKey {
	t.Helper()
	select {
	case nk := <-r.ch:
		return nk
	case <-time.After(d):
		t.Fatalf("no rebirth request within %s", d)
		return nodeKey{}
	}
}

func (r *rebirthLog) none(t *testing.T, d time.Duration) {
	t.Helper()
	select {
	case nk := <-r.ch:
		t.Fatalf("unexpected rebirth request for %+v", nk)
	case <-time.After(d):
	}
}

// newTestDriver builds a Driver straight from a manifest — no New, no broker,
// no goroutines — with the manifest indexes B1's buildIndexes fills and the
// synthesized companion tags initValues seeds.
func newTestDriver(t *testing.T, m Manifest, cfg Config) (*Driver, *rebirthLog) {
	t.Helper()
	d := &Driver{cfg: cfg, manifest: m, discovery: DiscoveryLog}
	if err := d.buildIndexes(); err != nil {
		t.Fatalf("buildIndexes: %v", err)
	}
	d.initValues()
	rl := newRebirthLog()
	d.onRebirthNeeded = rl.hook
	return d, rl
}

func enc(t *testing.T, p sparkplug.Payload) []byte {
	t.Helper()
	b, err := p.Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return b
}

func dbl(name string, v float64) sparkplug.Metric {
	return sparkplug.Metric{Name: name, Datatype: spb.DataType_Double, Value: v}
}

func boolean(name string, v bool) sparkplug.Metric {
	return sparkplug.Metric{Name: name, Datatype: spb.DataType_Boolean, Value: v}
}

func str(name, v string) sparkplug.Metric {
	return sparkplug.Metric{Name: name, Datatype: spb.DataType_String, Value: v}
}

func bdSeq(v int64) sparkplug.Metric {
	return sparkplug.Metric{Name: "bdSeq", Datatype: spb.DataType_Int64, Value: v}
}

// nbirth is the shape nautilus's own edge publishes: bdSeq, Node
// Control/Rebirth, then the data metrics, full names and no aliases.
func nbirth(bd int64, ms ...sparkplug.Metric) sparkplug.Payload {
	head := []sparkplug.Metric{bdSeq(bd), boolean(RebirthMetric, false)}
	return sparkplug.Payload{Timestamp: 1_000, Seq: 0, Metrics: append(head, ms...)}
}

func ndeath(bd int64) sparkplug.Payload {
	return sparkplug.Payload{Timestamp: 2_000, OmitSeq: true, Metrics: []sparkplug.Metric{bdSeq(bd)}}
}

func data(seq uint64, ms ...sparkplug.Metric) sparkplug.Payload {
	return sparkplug.Payload{Timestamp: 3_000, Seq: seq, Metrics: ms}
}

func mustValue(t *testing.T, vals map[string]any, name string) any {
	t.Helper()
	v, ok := vals[name]
	if !ok {
		t.Fatalf("tag %q absent from the snapshot (have %v)", name, sortedKeys(vals))
	}
	return v
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ── births and value application ─────────────────────────────────────────

// TestStateNBirthAppliesValues — an NBIRTH lands its metrics as plain Go
// scalars (the shape eip's ReadInputs hands the runtime) and flips the
// synthesized companion tags.
func TestStateNBirthAppliesValues(t *testing.T) {
	d, rl := newTestDriver(t, testManifest(), Config{})

	// Before any traffic the companions exist and read false/0, while data
	// metrics are absent so a scan reading them faults — by design.
	pre := d.snapshot()
	if pre["W6__Online"] != false || pre["W6__LastBirthMs"] != int64(0) || pre["W6_PLC1__Online"] != false {
		t.Fatalf("initValues seeded %v", pre)
	}
	if _, ok := pre["W6_Well_Level"]; ok {
		t.Fatal("an unseen data metric must stay absent from the snapshot")
	}

	d.handleMessage("spBv1.0/G/NBIRTH/W6", enc(t, nbirth(3, dbl("Well/Level", 12.5), str("Site", "W6"))))

	got := d.snapshot()
	if v := mustValue(t, got, "W6_Well_Level"); v != 12.5 {
		t.Errorf("W6_Well_Level = %#v, want 12.5 (float64)", v)
	}
	if v := mustValue(t, got, "W6_Site"); v != "W6" {
		t.Errorf("W6_Site = %#v, want \"W6\"", v)
	}
	if got["W6__Online"] != true {
		t.Errorf("W6__Online = %#v, want true", got["W6__Online"])
	}
	if got["W6__LastBirthMs"] != int64(1000) {
		t.Errorf("W6__LastBirthMs = %#v, want 1000", got["W6__LastBirthMs"])
	}
	// bdSeq and Node Control/Rebirth are plumbing, never tags.
	for _, n := range []string{"bdSeq", "W6_bdSeq", "W6_Node_Control_Rebirth"} {
		if _, ok := got[n]; ok {
			t.Errorf("protocol metric leaked as tag %q", n)
		}
	}
	ns := d.nodeStatuses()
	if len(ns) != 1 || !ns[0].Online || ns[0].BdSeq != 3 || ns[0].Metrics != 2 {
		t.Fatalf("node status = %+v", ns)
	}
	rl.none(t, 20*time.Millisecond)
}

// TestStateAliasOnlyNData — an edge that aliases (Ignition, tentacle) sends
// name+alias in the birth and the bare alias in data; both must resolve.
func TestStateAliasOnlyNData(t *testing.T) {
	d, _ := newTestDriver(t, testManifest(), Config{})

	lvl := dbl("Well/Level", 1)
	lvl.Alias = 7
	d.handleMessage("spBv1.0/G/NBIRTH/W6", enc(t, nbirth(1, lvl)))

	d.handleMessage("spBv1.0/G/NDATA/W6", enc(t, data(1,
		sparkplug.Metric{Alias: 7, Datatype: spb.DataType_Double, Value: 42.0})))

	if v := mustValue(t, d.snapshot(), "W6_Well_Level"); v != 42.0 {
		t.Fatalf("W6_Well_Level = %#v, want 42", v)
	}
}

// TestStateDeviceLifecycle — DBIRTH brings a device online and consumes a
// seq, DDATA updates it, DDEATH takes it dark without touching its values.
func TestStateDeviceLifecycle(t *testing.T) {
	d, _ := newTestDriver(t, testManifest(), Config{})
	d.handleMessage("spBv1.0/G/NBIRTH/W6", enc(t, nbirth(1)))

	d.handleMessage("spBv1.0/G/DBIRTH/W6/PLC1", enc(t, data(1, boolean("Pump/Run", false))))
	if d.snapshot()["W6_PLC1__Online"] != true {
		t.Fatal("W6_PLC1__Online did not go true on DBIRTH")
	}

	d.handleMessage("spBv1.0/G/DDATA/W6/PLC1", enc(t, data(2, boolean("Pump/Run", true))))
	if v := mustValue(t, d.snapshot(), "W6_PLC1_Pump_Run"); v != true {
		t.Fatalf("W6_PLC1_Pump_Run = %#v, want true", v)
	}

	d.handleMessage("spBv1.0/G/DDEATH/W6/PLC1", enc(t, data(3)))
	got := d.snapshot()
	if got["W6_PLC1__Online"] != false {
		t.Error("W6_PLC1__Online did not go false on DDEATH")
	}
	if got["W6_PLC1_Pump_Run"] != true {
		t.Error("DDEATH must keep the last value, not zero it")
	}
	if got["W6__Online"] != true {
		t.Error("a device death must not take the node offline")
	}

	st := d.nodeStatuses()
	if len(st) != 1 || len(st[0].Devices) != 1 || st[0].Devices[0].ID != "PLC1" || st[0].Devices[0].Online {
		t.Fatalf("device status = %+v", st)
	}
}

// ── sequence tracking ────────────────────────────────────────────────────

// TestStateSeqGapRequestsRebirth — a hole the reorder window never fills
// drops the buffer, counts a gap, and asks the node to birth again.
func TestStateSeqGapRequestsRebirth(t *testing.T) {
	d, rl := newTestDriver(t, testManifest(), Config{ReorderTimeout: 20 * time.Millisecond})
	d.handleMessage("spBv1.0/G/NBIRTH/W6", enc(t, nbirth(1, dbl("Well/Level", 1))))

	// seq 1 never arrives.
	d.handleMessage("spBv1.0/G/NDATA/W6", enc(t, data(2, dbl("Well/Level", 99))))
	if v := d.snapshot()["W6_Well_Level"]; v != 1.0 {
		t.Fatalf("out-of-order data was applied early: %#v", v)
	}

	if nk := rl.wait(t, time.Second); nk != (nodeKey{Group: "G", EdgeNode: "W6"}) {
		t.Fatalf("rebirth requested for %+v", nk)
	}
	d.mu.Lock()
	gaps, rebirths, primed := d.stats.SeqGaps, d.stats.Rebirths, d.nodes[nodeKey{"G", "W6"}].seqPrimed
	d.mu.Unlock()
	if gaps != 1 || rebirths != 1 {
		t.Errorf("SeqGaps=%d Rebirths=%d, want 1/1", gaps, rebirths)
	}
	if primed {
		t.Error("a dropped buffer must unprime the sequence until the next NBIRTH")
	}
	if v := d.snapshot()["W6_Well_Level"]; v != 1.0 {
		t.Errorf("the dropped buffer was applied anyway: %#v", v)
	}
}

// TestStateOutOfOrderFillInDrains — the missing message arriving inside the
// window drains the buffer in order and cancels the timer, no rebirth.
func TestStateOutOfOrderFillInDrains(t *testing.T) {
	d, rl := newTestDriver(t, testManifest(), Config{ReorderTimeout: 500 * time.Millisecond})
	d.handleMessage("spBv1.0/G/NBIRTH/W6", enc(t, nbirth(1, dbl("Well/Level", 0), str("Site", "a"))))

	d.handleMessage("spBv1.0/G/NDATA/W6", enc(t, data(3, str("Site", "third"))))
	d.handleMessage("spBv1.0/G/NDATA/W6", enc(t, data(2, str("Site", "second"))))
	d.handleMessage("spBv1.0/G/NDATA/W6", enc(t, data(1, str("Site", "first"))))

	got := d.snapshot()
	if got["W6_Site"] != "third" {
		t.Fatalf("W6_Site = %#v; the buffer did not drain in sequence order", got["W6_Site"])
	}
	d.mu.Lock()
	ns := d.nodes[nodeKey{"G", "W6"}]
	seq, pending, timer := ns.seq, len(ns.pending), ns.gapTimer
	gaps := d.stats.SeqGaps
	d.mu.Unlock()
	if seq != 3 || pending != 0 || timer != nil {
		t.Errorf("seq=%d pending=%d timer=%v, want 3/0/nil", seq, pending, timer)
	}
	if gaps != 0 {
		t.Errorf("SeqGaps = %d, want 0", gaps)
	}
	rl.none(t, 100*time.Millisecond)
}

// TestStateDuplicateSeqIgnored — a QoS-1 redelivery of the message we just
// took must not be reapplied nor read as a gap.
func TestStateDuplicateSeqIgnored(t *testing.T) {
	d, rl := newTestDriver(t, testManifest(), Config{ReorderTimeout: 20 * time.Millisecond})
	d.handleMessage("spBv1.0/G/NBIRTH/W6", enc(t, nbirth(1, dbl("Well/Level", 0))))
	d.handleMessage("spBv1.0/G/NDATA/W6", enc(t, data(1, dbl("Well/Level", 5))))
	d.handleMessage("spBv1.0/G/NDATA/W6", enc(t, data(1, dbl("Well/Level", 9))))

	if v := d.snapshot()["W6_Well_Level"]; v != 5.0 {
		t.Fatalf("W6_Well_Level = %#v, want 5 (the duplicate must be dropped)", v)
	}
	rl.none(t, 80*time.Millisecond)
}

// TestStateDataBeforeBirthAsksOnce — a host that starts mid-stream sees data
// for a node it has no birth for. Retained births are forbidden by the spec,
// so the only cure is to ask — exactly once, not once per message.
func TestStateDataBeforeBirthAsksOnce(t *testing.T) {
	d, rl := newTestDriver(t, testManifest(), Config{})
	for i := uint64(1); i <= 5; i++ {
		d.handleMessage("spBv1.0/G/NDATA/W6", enc(t, data(i, dbl("Well/Level", float64(i)))))
	}
	rl.wait(t, time.Second)
	rl.none(t, 20*time.Millisecond)

	d.mu.Lock()
	rebirths := d.stats.Rebirths
	d.mu.Unlock()
	if rebirths != 1 {
		t.Errorf("Rebirths = %d, want 1 (debounced to one per birth cycle)", rebirths)
	}
	if _, ok := d.snapshot()["W6_Well_Level"]; ok {
		t.Error("data before a birth must not be applied")
	}
}

// ── deaths ───────────────────────────────────────────────────────────────

// TestStateStaleNDeathIgnored — a will from a prior session carries the old
// bdSeq and must not take a live node down.
func TestStateStaleNDeathIgnored(t *testing.T) {
	d, _ := newTestDriver(t, testManifest(), Config{})
	d.handleMessage("spBv1.0/G/NBIRTH/W6", enc(t, nbirth(4, dbl("Well/Level", 7))))

	d.handleMessage("spBv1.0/G/NDEATH/W6", enc(t, ndeath(3)))
	if d.snapshot()["W6__Online"] != true {
		t.Fatal("a stale-bdSeq NDEATH took the node offline")
	}

	// An NDEATH with no bdSeq at all cannot be matched, so it is ignored too.
	d.handleMessage("spBv1.0/G/NDEATH/W6", enc(t, sparkplug.Payload{OmitSeq: true}))
	if d.snapshot()["W6__Online"] != true {
		t.Fatal("an NDEATH without bdSeq took the node offline")
	}
}

// TestStateNDeathKeepsValues — the matching death takes the node and every
// device offline and keeps every value. Sparkplug's last value *is* the
// value; quality rides on the __Online companions.
func TestStateNDeathKeepsValues(t *testing.T) {
	d, _ := newTestDriver(t, testManifest(), Config{})
	d.handleMessage("spBv1.0/G/NBIRTH/W6", enc(t, nbirth(4, dbl("Well/Level", 7))))
	d.handleMessage("spBv1.0/G/DBIRTH/W6/PLC1", enc(t, data(1, boolean("Pump/Run", true))))

	d.handleMessage("spBv1.0/G/NDEATH/W6", enc(t, ndeath(4)))

	got := d.snapshot()
	if got["W6__Online"] != false {
		t.Error("W6__Online did not go false")
	}
	if got["W6_PLC1__Online"] != false {
		t.Error("a node death must take its devices offline too")
	}
	if got["W6_Well_Level"] != 7.0 || got["W6_PLC1_Pump_Run"] != true {
		t.Errorf("values were not retained across the death: %v", got)
	}
	if got["W6__LastBirthMs"] != int64(1000) {
		t.Errorf("W6__LastBirthMs = %#v, want the last birth kept", got["W6__LastBirthMs"])
	}
	if st := d.nodeStatuses(); st[0].Online || st[0].Devices[0].Online {
		t.Fatalf("status still online: %+v", st)
	}
}

// TestStateRebirthResetsDevices — an NBIRTH invalidates every device until
// its own DBIRTH arrives, and re-primes the sequence at 0.
func TestStateRebirthResetsDevices(t *testing.T) {
	d, _ := newTestDriver(t, testManifest(), Config{})
	d.handleMessage("spBv1.0/G/NBIRTH/W6", enc(t, nbirth(1)))
	d.handleMessage("spBv1.0/G/DBIRTH/W6/PLC1", enc(t, data(1, boolean("Pump/Run", true))))

	d.handleMessage("spBv1.0/G/NBIRTH/W6", enc(t, nbirth(2)))
	got := d.snapshot()
	if got["W6_PLC1__Online"] != false {
		t.Error("a rebirth must dark every device until its DBIRTH")
	}
	if got["W6_PLC1_Pump_Run"] != true {
		t.Error("a rebirth must not zero device values")
	}
	d.mu.Lock()
	ns := d.nodes[nodeKey{"G", "W6"}]
	seq, bd := ns.seq, ns.bdSeq
	d.mu.Unlock()
	if seq != 0 || bd != 2 {
		t.Fatalf("seq=%d bdSeq=%d, want 0/2", seq, bd)
	}
}

// ── templates ────────────────────────────────────────────────────────────

func motorInstance(name string, ms ...sparkplug.Metric) sparkplug.Metric {
	return sparkplug.Metric{Name: name, Datatype: spb.DataType_Template,
		Value: &sparkplug.Template{TemplateRef: "Motor", Metrics: ms}}
}

// TestStateTemplateAssemblyAndPartialUpdate — a Template metric becomes an
// ir.Value shaped by the manifest type, and a partial update keeps the
// members it does not mention.
func TestStateTemplateAssemblyAndPartialUpdate(t *testing.T) {
	d, _ := newTestDriver(t, testManifest(), Config{})
	d.handleMessage("spBv1.0/G/NBIRTH/W6", enc(t, nbirth(1,
		motorInstance("Pump1", boolean("Run", true), dbl("Speed", 58.25), str("Label", "M1")))))

	v, ok := mustValue(t, d.snapshot(), "W6_Pump1").(ir.Value)
	if !ok || v.Kind != ir.TypeStruct {
		t.Fatalf("W6_Pump1 = %#v, want an ir.Value struct", d.snapshot()["W6_Pump1"])
	}
	if v.Struct == nil || v.Struct.Name != "Motor" {
		t.Fatalf("struct def = %+v, want Motor", v.Struct)
	}
	want := []any{true, 58.25, "M1"}
	for i, w := range want {
		if got := plainOrIR(v.Fld[i]); got != w {
			t.Errorf("Motor.%s = %#v, want %#v", v.Struct.Fields[i].Name, got, w)
		}
	}

	// A DATA carrying one member must not zero the other two.
	d.handleMessage("spBv1.0/G/NDATA/W6", enc(t, data(1, motorInstance("Pump1", dbl("Speed", 60)))))
	v = mustValue(t, d.snapshot(), "W6_Pump1").(ir.Value)
	if v.Fld[0].B != true || v.Fld[1].F != 60 || v.Fld[2].S != "M1" {
		t.Fatalf("partial update clobbered members: %+v", v.Fld)
	}
}

// TestStateNestedTemplateFallsBackToManifest is the nested-template gotcha:
// an edge that emits a nested-struct definition member as a *null* Template
// with no templateRef gives StructDefsFromTemplates nothing to resolve, and
// it rightly fails. (nautilus's own edge did exactly this until templates.go
// started emitting the TemplateRef; other stacks still do.) The manifest
// already knows the shape, so the birth must still assemble, from d.defs.
func TestStateNestedTemplateFallsBackToManifest(t *testing.T) {
	d, _ := newTestDriver(t, testManifest(), Config{})

	// The pre-fix definitionTemplate shape: nested member, no templateRef.
	motorDef := sparkplug.Metric{Name: "Motor", Datatype: spb.DataType_Template,
		Value: &sparkplug.Template{IsDefinition: true, Metrics: []sparkplug.Metric{
			{Name: "Run", Datatype: spb.DataType_Boolean, IsNull: true},
			{Name: "Speed", Datatype: spb.DataType_Double, IsNull: true},
			{Name: "Label", Datatype: spb.DataType_String, IsNull: true},
		}}}
	skidDef := sparkplug.Metric{Name: "Skid", Datatype: spb.DataType_Template,
		Value: &sparkplug.Template{IsDefinition: true, Metrics: []sparkplug.Metric{
			{Name: "Hours", Datatype: spb.DataType_Int64, IsNull: true},
			// No templateRef, no value — the gotcha.
			{Name: "Drive", Datatype: spb.DataType_Template, IsNull: true},
		}}}
	if _, err := sparkplug.StructDefsFromTemplates([]sparkplug.Metric{motorDef, skidDef}); err == nil {
		t.Fatal("fixture drifted: this definition shape is supposed to be unharvestable")
	}

	skid := sparkplug.Metric{Name: "Skid1", Datatype: spb.DataType_Template,
		Value: &sparkplug.Template{TemplateRef: "Skid", Metrics: []sparkplug.Metric{
			{Name: "Hours", Datatype: spb.DataType_Int64, Value: int64(120)},
			motorInstance("Drive", dbl("Speed", 30)),
		}}}
	d.handleMessage("spBv1.0/G/NBIRTH/W6", enc(t, nbirth(1, motorDef, skidDef, skid)))

	v, ok := mustValue(t, d.snapshot(), "W6_Skid1").(ir.Value)
	if !ok || v.Kind != ir.TypeStruct {
		t.Fatalf("W6_Skid1 = %#v", d.snapshot()["W6_Skid1"])
	}
	if v.Fld[0].I != 120 || v.Fld[1].Fld[1].F != 30 {
		t.Fatalf("Skid did not assemble from the manifest type: %+v", v.Fld)
	}
	// Definition metrics are not data and must not be counted or bound.
	if st := d.nodeStatuses(); st[0].Metrics != 1 {
		t.Errorf("Metrics = %d, want 1 (definitions do not count)", st[0].Metrics)
	}
	if len(d.Discovered()) != 0 {
		t.Errorf("definition metrics leaked into discovery: %+v", d.Discovered())
	}
}

// ── discovery ────────────────────────────────────────────────────────────

// TestStateUnknownMetricDiscovery — a metric absent from the manifest is
// counted, never dropped silently, and rendered as the exact YAML line to add.
func TestStateUnknownMetricDiscovery(t *testing.T) {
	d, _ := newTestDriver(t, testManifest(), Config{})
	d.handleMessage("spBv1.0/G/NBIRTH/W6", enc(t, nbirth(1, dbl("Well/Flow", 3))))
	d.handleMessage("spBv1.0/G/NDATA/W6", enc(t, data(1, dbl("Well/Flow", 4))))
	d.handleMessage("spBv1.0/G/DBIRTH/W6/PLC1", enc(t, data(2, boolean("Pump/Aux", true))))

	got := d.Discovered()
	if len(got) != 2 {
		t.Fatalf("Discovered() = %+v, want 2 entries", got)
	}
	// Sorted node → device → metric, so node-level metrics lead each site.
	want := []Discovered{
		{Group: "G", EdgeNode: "W6", Device: "", Metric: "Well/Flow", Datatype: "Double", Count: 2,
			YAML: `- { name: W6_Well_Flow, node: W6, device: "", metric: Well/Flow, type: Double, arraylen: 0, writable: false }`},
		{Group: "G", EdgeNode: "W6", Device: "PLC1", Metric: "Pump/Aux", Datatype: "Boolean", Count: 1,
			YAML: `- { name: W6_PLC1_Pump_Aux, node: W6, device: "PLC1", metric: Pump/Aux, type: Boolean, arraylen: 0, writable: false }`},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Discovered():\n got %+v\nwant %+v", got, want)
	}
	if d.unknownCount() != 2 {
		t.Errorf("unknownCount = %d, want 2", d.unknownCount())
	}
	if d.isDegraded() {
		t.Error("the default log policy must not degrade the driver")
	}
}

// TestStateDiscoveryPolicies — "ignore" counts silently, "strict" degrades.
func TestStateDiscoveryPolicies(t *testing.T) {
	for _, c := range []struct {
		mode     string
		degraded bool
	}{
		{DiscoveryLog, false},
		{DiscoveryIgnore, false},
		{DiscoveryStrict, true},
	} {
		t.Run(c.mode, func(t *testing.T) {
			d, _ := newTestDriver(t, testManifest(), Config{})
			d.discovery = c.mode
			d.handleMessage("spBv1.0/G/NBIRTH/W6", enc(t, nbirth(1, dbl("Well/Flow", 3))))
			if d.unknownCount() != 1 {
				t.Errorf("unknownCount = %d, want 1 — nothing is ever dropped silently", d.unknownCount())
			}
			if d.isDegraded() != c.degraded {
				t.Errorf("degraded = %v, want %v", d.isDegraded(), c.degraded)
			}
		})
	}
}

// ── filtering and robustness ─────────────────────────────────────────────

// TestStateGroupFilter — a shared broker carries other people's groups; with
// GroupIDs set, only ours is consumed.
func TestStateGroupFilter(t *testing.T) {
	d, _ := newTestDriver(t, testManifest(), Config{GroupIDs: []string{"G"}})
	d.handleMessage("spBv1.0/OTHER/NBIRTH/W6", enc(t, nbirth(1, dbl("Well/Level", 99))))

	if _, ok := d.snapshot()["W6_Well_Level"]; ok {
		t.Fatal("a foreign group's birth was consumed")
	}
	d.mu.Lock()
	msgs, nodes := d.stats.Msgs, len(d.nodes)
	d.mu.Unlock()
	if msgs != 0 || nodes != 0 {
		t.Fatalf("msgs=%d nodes=%d, want 0/0", msgs, nodes)
	}

	// Empty GroupIDs means every group on the broker.
	all, _ := newTestDriver(t, testManifest(), Config{})
	all.handleMessage("spBv1.0/OTHER/NBIRTH/W6", enc(t, nbirth(1, dbl("Well/Level", 99))))
	if all.snapshot()["W6_Well_Level"] != 99.0 {
		t.Fatal("empty GroupIDs must consume every group")
	}
}

// TestStateMalformedInputIgnored — junk topics, our own STATE echo and
// undecodable bytes must be dropped without touching any state.
func TestStateMalformedInputIgnored(t *testing.T) {
	d, rl := newTestDriver(t, testManifest(), Config{})
	good := enc(t, nbirth(1, dbl("Well/Level", 1)))

	for _, topic := range []string{
		"",
		"spBv1.0",
		"spBv1.0/G",
		"spBv1.0/G/NBIRTH",         // no edge node
		"spBv1.0/G//NBIRTH/W6",     // empty group segment
		"spBv1.0/G/NBIRTH/W6/PLC1", // node message with a device segment
		"spBv1.0/G/DDATA/W6",       // device message without one
		"spBv1.0/G/NBIRTH/W6/PLC1/EXTRA",
		"spBv1.0/STATE/pomona-central", // our own certificate, echoed back
		"STATE/pomona-central",         // the 2.x form
		"spAv2.0/G/NBIRTH/W6",
		"random/nonsense",
	} {
		d.handleMessage(topic, good)
	}
	// Undecodable bytes on a topic we do consume.
	d.handleMessage("spBv1.0/G/NBIRTH/W6", []byte{0xff, 0xff, 0xff, 0xff, 0xff})

	d.mu.Lock()
	nodes, msgs := len(d.nodes), d.stats.Msgs
	d.mu.Unlock()
	if nodes != 0 || msgs != 0 {
		t.Fatalf("malformed traffic created state: nodes=%d msgs=%d", nodes, msgs)
	}
	if snap := d.snapshot(); snap["W6__Online"] != false {
		t.Fatalf("malformed traffic disturbed the snapshot: %v", snap)
	}
	rl.none(t, 20*time.Millisecond)

	// The same payload on the right topic still works.
	d.handleMessage("spBv1.0/G/NBIRTH/W6", good)
	if d.snapshot()["W6_Well_Level"] != 1.0 {
		t.Fatal("a well-formed message after the junk was not applied")
	}
}

// ── staleness ────────────────────────────────────────────────────────────

// TestStateSweepStale — silence longer than StaleAfter marks a node stale and
// asks it to birth again; the values and __Online are left alone, because
// staleness is a separate axis from the death certificate.
func TestStateSweepStale(t *testing.T) {
	d, rl := newTestDriver(t, testManifest(), Config{StaleAfter: time.Minute})
	d.handleMessage("spBv1.0/G/NBIRTH/W6", enc(t, nbirth(1, dbl("Well/Level", 7))))

	d.sweepStale(time.Now())
	if d.nodeStatuses()[0].Stale {
		t.Fatal("a node that just spoke is not stale")
	}
	rl.none(t, 20*time.Millisecond)

	d.sweepStale(time.Now().Add(2 * time.Minute))
	st := d.nodeStatuses()
	if !st[0].Stale {
		t.Fatal("a silent node was not marked stale")
	}
	if !st[0].Online || d.snapshot()["W6_Well_Level"] != 7.0 {
		t.Error("staleness must not clear __Online or the values")
	}
	rl.wait(t, time.Second)

	// Debounced: a second sweep does not re-ask.
	d.sweepStale(time.Now().Add(3 * time.Minute))
	rl.none(t, 20*time.Millisecond)

	// Traffic clears it.
	d.handleMessage("spBv1.0/G/NDATA/W6", enc(t, data(1, dbl("Well/Level", 8))))
	if d.nodeStatuses()[0].Stale {
		t.Fatal("traffic did not clear the stale flag")
	}

	// StaleAfter == 0 turns the whole thing off.
	off, _ := newTestDriver(t, testManifest(), Config{})
	off.handleMessage("spBv1.0/G/NBIRTH/W6", enc(t, nbirth(1)))
	off.sweepStale(time.Now().Add(24 * time.Hour))
	if off.nodeStatuses()[0].Stale {
		t.Fatal("StaleAfter 0 must disable the sweep")
	}
}

// ── snapshot and status ──────────────────────────────────────────────────

// TestStateSnapshotIsACopy — ReadInputs must not hand the runtime a map the
// message goroutine keeps writing into.
func TestStateSnapshotIsACopy(t *testing.T) {
	d, _ := newTestDriver(t, testManifest(), Config{})
	d.handleMessage("spBv1.0/G/NBIRTH/W6", enc(t, nbirth(1, dbl("Well/Level", 1))))

	snap := d.snapshot()
	snap["W6_Well_Level"] = 999.0
	d.handleMessage("spBv1.0/G/NDATA/W6", enc(t, data(1, dbl("Well/Level", 2))))

	if snap["W6_Well_Level"] != 999.0 {
		t.Fatal("the snapshot aliases the driver's map")
	}
	if d.snapshot()["W6_Well_Level"] != 2.0 {
		t.Fatal("mutating a snapshot leaked back into the driver")
	}
}

// TestStateNodeStatusesSorted — /api/drivers renders these rows, so the order
// must be stable regardless of map iteration.
func TestStateNodeStatusesSorted(t *testing.T) {
	m := testManifest()
	m.Nodes = append(m.Nodes, Node{EdgeNode: "W2", Prefix: "W2"}, Node{EdgeNode: "W9", Prefix: "W9"})
	d, _ := newTestDriver(t, m, Config{})
	for _, edge := range []string{"W9", "W6", "W2"} {
		d.handleMessage("spBv1.0/G/NBIRTH/"+edge, enc(t, nbirth(1)))
	}
	d.handleMessage("spBv1.0/A/NBIRTH/W6", enc(t, nbirth(1)))

	var got []string
	for _, st := range d.nodeStatuses() {
		got = append(got, st.Group+"/"+st.EdgeNode)
	}
	want := []string{"A/W6", "G/W2", "G/W6", "G/W9"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("nodeStatuses order = %v, want %v", got, want)
	}
}
