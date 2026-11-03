package host

import (
	"strings"
	"testing"
)

// outagesOf reads one __Outages companion from the snapshot ReadInputs
// serves, failing if it is absent.
func outagesOf(t *testing.T, d *Driver, name string) any {
	t.Helper()
	return mustValue(t, d.snapshot(), name)
}

// <site>__Outages counts the times the node's __Online has gone false: 0
// before the first birth and after it, then one more per matching NDEATH.
// A stale NDEATH and a rebirth of a live node are not outages.
func TestOutagesNodeCountsEachDeath(t *testing.T) {
	d, _ := newTestDriver(t, testManifest(), Config{})
	if got := outagesOf(t, d, "W6__Outages"); got != int64(0) {
		t.Fatalf("before any birth W6__Outages = %#v, want int64(0)", got)
	}

	d.handleMessage("spBv1.0/G/NBIRTH/W6", enc(t, nbirth(1)))
	if got := outagesOf(t, d, "W6__Outages"); got != int64(0) {
		t.Fatalf("the first birth is not an outage: W6__Outages = %#v, want int64(0)", got)
	}

	for bd, want := int64(1), int64(1); want <= 2; bd, want = bd+1, want+1 {
		d.handleMessage("spBv1.0/G/NDEATH/W6", enc(t, ndeath(bd)))
		if d.snapshot()["W6__Online"] != false {
			t.Fatal("W6__Online did not go false on NDEATH")
		}
		if got := outagesOf(t, d, "W6__Outages"); got != want {
			t.Fatalf("after death %d W6__Outages = %#v, want int64(%d)", want, got, want)
		}
		d.handleMessage("spBv1.0/G/NBIRTH/W6", enc(t, nbirth(bd+1)))
		if got := outagesOf(t, d, "W6__Outages"); got != want {
			t.Fatalf("a rebirth changed W6__Outages to %#v, want int64(%d)", got, want)
		}
	}

	// The node is live at bdSeq 3. A late will from bdSeq 2 is ignored, and
	// a rebirth keeps the node online, so neither counts.
	d.handleMessage("spBv1.0/G/NDEATH/W6", enc(t, ndeath(2)))
	d.handleMessage("spBv1.0/G/NBIRTH/W6", enc(t, nbirth(4)))
	if got := outagesOf(t, d, "W6__Outages"); got != int64(2) {
		t.Fatalf("a stale NDEATH or a live rebirth counted: W6__Outages = %#v, want int64(2)", got)
	}
}

// <site>_<device>__Outages counts the device's falls: a DDEATH, its node's
// NDEATH, and a node rebirth that darks it until its DBIRTH. A device that
// is already offline does not count again: not for a second DDEATH, a
// node death or a rebirth.
func TestOutagesDeviceCountsEachFall(t *testing.T) {
	d, _ := newTestDriver(t, testManifest(), Config{})
	if got := outagesOf(t, d, "W6_PLC1__Outages"); got != int64(0) {
		t.Fatalf("before any birth W6_PLC1__Outages = %#v, want int64(0)", got)
	}
	d.handleMessage("spBv1.0/G/NBIRTH/W6", enc(t, nbirth(1)))
	d.handleMessage("spBv1.0/G/DBIRTH/W6/PLC1", enc(t, data(1, boolean("Pump/Run", false))))
	if got := outagesOf(t, d, "W6_PLC1__Outages"); got != int64(0) {
		t.Fatalf("the first DBIRTH is not an outage: %#v", got)
	}

	steps := []struct {
		topic   string
		payload []byte
		device  int64
		node    int64
	}{
		{"spBv1.0/G/DDEATH/W6/PLC1", enc(t, data(2)), 1, 0},
		{"spBv1.0/G/DBIRTH/W6/PLC1", enc(t, data(3, boolean("Pump/Run", true))), 1, 0},
		{"spBv1.0/G/NDEATH/W6", enc(t, ndeath(1)), 2, 1},
		{"spBv1.0/G/NBIRTH/W6", enc(t, nbirth(2)), 2, 1}, // already dark: no second count
		{"spBv1.0/G/DBIRTH/W6/PLC1", enc(t, data(1, boolean("Pump/Run", true))), 2, 1},
		{"spBv1.0/G/NBIRTH/W6", enc(t, nbirth(3)), 3, 1}, // a rebirth darks a live device
		{"spBv1.0/G/DBIRTH/W6/PLC1", enc(t, data(1, boolean("Pump/Run", true))), 3, 1},
		{"spBv1.0/G/DDEATH/W6/PLC1", enc(t, data(2)), 4, 1},
		{"spBv1.0/G/DDEATH/W6/PLC1", enc(t, data(3)), 4, 1}, // a repeated DDEATH counts once
		{"spBv1.0/G/NDEATH/W6", enc(t, ndeath(3)), 4, 2},    // the device was already dark
	}
	for _, s := range steps {
		d.handleMessage(s.topic, s.payload)
		if got := outagesOf(t, d, "W6_PLC1__Outages"); got != s.device {
			t.Fatalf("after %s W6_PLC1__Outages = %#v, want int64(%d)", s.topic, got, s.device)
		}
		if got := outagesOf(t, d, "W6__Outages"); got != s.node {
			t.Fatalf("after %s W6__Outages = %#v, want int64(%d)", s.topic, got, s.node)
		}
	}
}

// The counters are declared like the other companions: in InputNames, as
// DINT, and under the names a manifest gives them with outagestag.
func TestOutagesDeclaredAsDINTUnderTheManifestName(t *testing.T) {
	m := mustParse(t, `
nodes:
    - edgenode: W6
      outagestag: Well6_Drops
      devices: [{ device: PLC1, outagestag: Well6_PLC_Drops }]
tags: []
`)
	d, _ := newTestDriver(t, m, Config{})
	names := map[string]bool{}
	for _, n := range d.InputNames() {
		names[n] = true
	}
	for _, name := range []string{"Well6_Drops", "Well6_PLC_Drops"} {
		if !names[name] {
			t.Errorf("InputNames() = %v, want %s in it", d.InputNames(), name)
		}
		if got := d.InputType(name); got != "DINT" {
			t.Errorf("InputType(%s) = %q, want DINT", name, got)
		}
	}
	if names["W6__Outages"] || names["W6_PLC1__Outages"] {
		t.Errorf("InputNames() = %v: an explicit outagestag replaces the default name", d.InputNames())
	}

	d.handleMessage("spBv1.0/G/NBIRTH/W6", enc(t, nbirth(1)))
	d.handleMessage("spBv1.0/G/DBIRTH/W6/PLC1", enc(t, data(1)))
	d.handleMessage("spBv1.0/G/NDEATH/W6", enc(t, ndeath(1)))
	if got := outagesOf(t, d, "Well6_Drops"); got != int64(1) {
		t.Errorf("Well6_Drops = %#v, want int64(1)", got)
	}
	if got := outagesOf(t, d, "Well6_PLC_Drops"); got != int64(1) {
		t.Errorf("Well6_PLC_Drops = %#v, want int64(1)", got)
	}
}

// A binding may not take a node's or a device's __Outages name.
func TestOutagesCompanionCollidesWithABinding(t *testing.T) {
	for want, src := range map[string]string{
		`node "W6" outagestag is "W6__Outages"`: `
nodes: [{edgenode: W6}]
tags: [{ name: W6__Outages, node: W6, metric: Outages, type: Int32 }]
`,
		`device "PLC1" outagestag is "W6_PLC1__Outages"`: `
nodes: [{edgenode: W6, devices: [{device: PLC1}]}]
tags: [{ name: W6_PLC1__Outages, node: W6, metric: Outages, type: Int32 }]
`,
	} {
		_, err := ParseManifest([]byte(src))
		if err == nil {
			err = mustParse(t, src).Validate()
		}
		if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "which a binding already declares") {
			t.Errorf("err = %v, want the collision %s", err, want)
		}
	}
}
