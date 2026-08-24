// quality_test.go exercises Quality() across the Sparkplug lifecycle. It
// reuses state_test.go's fixtures (testManifest, newTestDriver, nbirth/data/
// ndeath, the metric-value helpers) — no broker, no MQTT, just
// handleMessage and Quality().

package host

import (
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/sparkplug"
	"github.com/joyautomation/nautilus/sparkplug/spb"
)

// dataBindingNames is every tag testManifest() binds as a DATA (read-only)
// metric — the only bindings Quality() ever has an opinion about.
func dataBindingNames() []string {
	return []string{"W6_Well_Level", "W6_Site", "W6_Pump1", "W6_Skid1", "W6_PLC1_Pump_Run"}
}

// fullBirth births W6 and PLC1 with every manifest metric present, so a
// fresh driver reaches "every data binding delivered and online".
func fullBirth(t *testing.T, d *Driver) {
	t.Helper()
	d.handleMessage("spBv1.0/G/NBIRTH/W6", enc(t, nbirth(1,
		dbl("Well/Level", 12.5),
		str("Site", "W6"),
		motorInstance("Pump1", boolean("Run", true), dbl("Speed", 58.25), str("Label", "M1")),
		sparkplug.Metric{Name: "Skid1", Datatype: spb.DataType_Template,
			Value: &sparkplug.Template{TemplateRef: "Skid", Metrics: []sparkplug.Metric{
				{Name: "Hours", Datatype: spb.DataType_Int64, Value: int64(1)},
				motorInstance("Drive", boolean("Run", true), dbl("Speed", 1), str("Label", "D1")),
			}}},
	)))
	d.handleMessage("spBv1.0/G/DBIRTH/W6/PLC1", enc(t, data(1, boolean("Pump/Run", true))))
}

// TestQualityPreBirthNotConnected — before any traffic, every data binding
// has never delivered a value: NotConnected. Writable bindings (plain and
// member) and the synthesized companions never appear at all — they are not
// in d.inputs, so Quality() has nothing to say about them either way.
func TestQualityPreBirthNotConnected(t *testing.T) {
	d, _ := newTestDriver(t, testManifest(), Config{})

	q := d.Quality()
	if len(q) != len(dataBindingNames()) {
		t.Fatalf("Quality() = %v, want NotConnected for exactly %v", q, dataBindingNames())
	}
	for _, name := range dataBindingNames() {
		if q[name] != nio.NotConnected {
			t.Errorf("%s = %v, want NotConnected", name, q[name])
		}
	}
	for _, name := range []string{
		"W6_PLC1_Pump_SpeedSP", "W6_Pump1_Speed", "W6_Skid1_Drive_Run", "W6_Skid1_Drive_Speed",
		"W6__Online", "W6__LastBirthMs", "W6_PLC1__Online", "W6__Rebirth",
	} {
		if _, ok := q[name]; ok {
			t.Errorf("%s must never appear in Quality(): outputs and companions are always Good", name)
		}
	}
}

// TestQualityBirthEmptyMap — once every manifest metric has been birthed and
// the node/device are online, every data binding is Good: the map is empty.
func TestQualityBirthEmptyMap(t *testing.T) {
	d, _ := newTestDriver(t, testManifest(), Config{})
	fullBirth(t, d)

	if q := d.Quality(); len(q) != 0 {
		t.Fatalf("Quality() after a full birth = %v, want empty", q)
	}
}

// TestQualityNDeathStaleScopedToNode — an NDEATH marks Stale only the
// bindings of the node that died, leaving a sibling node's bindings Good.
func TestQualityNDeathStaleScopedToNode(t *testing.T) {
	m := Manifest{
		Group: "G",
		Nodes: []Node{{EdgeNode: "W6", Prefix: "W6"}, {EdgeNode: "W7", Prefix: "W7"}},
		Tags: []Binding{
			{Name: "W6_Well_Level", Node: "W6", Metric: "Well/Level", Type: "Double"},
			{Name: "W7_Well_Level", Node: "W7", Metric: "Well/Level", Type: "Double"},
		},
	}
	d, _ := newTestDriver(t, m, Config{})
	d.handleMessage("spBv1.0/G/NBIRTH/W6", enc(t, nbirth(1, dbl("Well/Level", 1))))
	d.handleMessage("spBv1.0/G/NBIRTH/W7", enc(t, nbirth(1, dbl("Well/Level", 2))))
	if q := d.Quality(); len(q) != 0 {
		t.Fatalf("Quality() after both births = %v, want empty", q)
	}

	d.handleMessage("spBv1.0/G/NDEATH/W6", enc(t, ndeath(1)))

	q := d.Quality()
	if len(q) != 1 || q["W6_Well_Level"] != nio.Stale {
		t.Fatalf("Quality() after W6's NDEATH = %v, want {W6_Well_Level: Stale}", q)
	}
	if _, ok := q["W7_Well_Level"]; ok {
		t.Errorf("W7's binding must stay Good: W6's death must not leak, got %v", q)
	}
}

// TestQualityDeviceDeathStaleJustThatDevice — a DDEATH on one device is
// Stale for that device's own bindings while the node itself (and its
// node-level bindings) stay Good.
func TestQualityDeviceDeathStaleJustThatDevice(t *testing.T) {
	d, _ := newTestDriver(t, testManifest(), Config{})
	fullBirth(t, d)

	d.handleMessage("spBv1.0/G/DDEATH/W6/PLC1", enc(t, data(2)))

	q := d.Quality()
	if len(q) != 1 || q["W6_PLC1_Pump_Run"] != nio.Stale {
		t.Fatalf("Quality() after PLC1's DDEATH = %v, want {W6_PLC1_Pump_Run: Stale}", q)
	}
}

// TestQualityStaleSweepMarksStale — sweepStale flags a silent node, and
// Quality() reflects it for every binding under that node, without
// disturbing its values.
func TestQualityStaleSweepMarksStale(t *testing.T) {
	d, _ := newTestDriver(t, testManifest(), Config{StaleAfter: time.Minute})
	fullBirth(t, d)

	d.sweepStale(time.Now())
	if q := d.Quality(); len(q) != 0 {
		t.Fatalf("Quality() right after birth = %v, want empty (not stale yet)", q)
	}

	d.sweepStale(time.Now().Add(2 * time.Minute))
	q := d.Quality()
	if len(q) != len(dataBindingNames()) {
		t.Fatalf("Quality() after a stale sweep = %v, want Stale for every data binding", q)
	}
	for _, name := range dataBindingNames() {
		if q[name] != nio.Stale {
			t.Errorf("%s = %v, want Stale", name, q[name])
		}
	}
	if v := d.snapshot()["W6_Well_Level"]; v != 12.5 {
		t.Errorf("staleness must not disturb the value: %#v", v)
	}

	// Any traffic from the node clears staleness for the whole node (staleness
	// is a per-node flag, not per-metric — see state.go's handleMessage).
	d.handleMessage("spBv1.0/G/NDATA/W6", enc(t, data(1, dbl("Well/Level", 8))))
	if q := d.Quality(); len(q) != 0 {
		t.Fatalf("Quality() after traffic clears staleness = %v, want empty", q)
	}
}

// TestQualityMissingMetricFromBirth — a manifest-bound metric a birth simply
// never carries reads the same as "never birthed": NotConnected, and only
// for that one binding. This is the "manifest metric missing from birth"
// case: the driver cannot tell "hasn't happened yet" from "will never
// happen", so it reports the honest thing — nothing to show — rather than
// inventing a Bad it cannot justify.
func TestQualityMissingMetricFromBirth(t *testing.T) {
	d, _ := newTestDriver(t, testManifest(), Config{})
	// Birth W6 without ever mentioning "Site" (W6_Site's metric).
	d.handleMessage("spBv1.0/G/NBIRTH/W6", enc(t, nbirth(1,
		dbl("Well/Level", 1),
		motorInstance("Pump1", boolean("Run", true), dbl("Speed", 1), str("Label", "M")),
		sparkplug.Metric{Name: "Skid1", Datatype: spb.DataType_Template,
			Value: &sparkplug.Template{TemplateRef: "Skid", Metrics: []sparkplug.Metric{
				{Name: "Hours", Datatype: spb.DataType_Int64, Value: int64(1)},
				motorInstance("Drive", boolean("Run", true), dbl("Speed", 1), str("Label", "D")),
			}}},
	)))
	d.handleMessage("spBv1.0/G/DBIRTH/W6/PLC1", enc(t, data(1, boolean("Pump/Run", true))))

	q := d.Quality()
	if len(q) != 1 || q["W6_Site"] != nio.NotConnected {
		t.Fatalf("Quality() with Site missing from the birth = %v, want {W6_Site: NotConnected}", q)
	}
}
