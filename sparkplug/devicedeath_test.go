package sparkplug

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/runtime"
)

// #616 (from #864's review): Config.DeviceDeathDelay holds a device's
// DDEATH until it has been unhealthy that long, and a recovery inside it
// sends nothing.

// deathRig is a node with one device (RTU53, one tag already read) whose
// health the test sets, born once.
func deathRig(t *testing.T, delay time.Duration, opts ...Option) (*Node, *fakeClient, *atomic.Bool) {
	t.Helper()
	rt, err := runtime.New(runtime.Options{
		Program: "PROGRAM P\nVAR_EXTERNAL\n  Level : REAL;\nEND_VAR\nEND_PROGRAM",
		Driver:  nio.NewMemory(),
		Scan:    50 * time.Millisecond,
		Tags:    []runtime.TagDef{runtime.Input("Level")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.Tags().WriteGlobal("Level", ir.RealVal(4.5)); err != nil {
		t.Fatal(err)
	}
	healthy := &atomic.Bool{}
	healthy.Store(true)
	n, err := New(rt, Config{GroupID: "g", EdgeNode: "e", DeviceDeathDelay: delay},
		append([]Option{WithDevice(Device{ID: "RTU53", Tags: []string{"Level"}, Health: healthy.Load})}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	fc := &fakeClient{open: true}
	n.cli = fc
	if err := n.birth(); err != nil {
		t.Fatal(err)
	}
	if got := len(fc.published("DBIRTH")); got != 1 {
		t.Fatalf("setup: %d DBIRTH, want 1", got)
	}
	return n, fc, healthy
}

// lifecycle is the device's DBIRTH/DDEATH messages in order, as a string.
func lifecycle(fc *fakeClient) string {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	var out []string
	for _, p := range fc.pubs {
		for _, k := range []string{"DBIRTH", "DDEATH"} {
			if strings.Contains(p.topic, "/"+k+"/") {
				out = append(out, k)
			}
		}
	}
	return strings.Join(out, " ")
}

// Delay 0, the default, is today's behaviour: DDEATH on the first
// unhealthy tick, DBIRTH on the first healthy one, every blip both.
func TestDeviceDeathDelayZeroIsImmediate(t *testing.T) {
	n, fc, healthy := deathRig(t, 0)
	healthy.Store(false)
	tickWithin(t, n, 2*time.Second)
	if got := lifecycle(fc); got != "DBIRTH DDEATH" {
		t.Fatalf("after one unhealthy tick: %q, want DDEATH at once", got)
	}
	healthy.Store(true)
	tickWithin(t, n, 2*time.Second)
	healthy.Store(false)
	tickWithin(t, n, 2*time.Second)
	if got := lifecycle(fc); got != "DBIRTH DDEATH DBIRTH DDEATH" {
		t.Fatalf("a blip with no delay: %q, want a DDEATH and DBIRTH for each change", got)
	}
}

// A blip shorter than the delay sends nothing: no DDEATH, no DBIRTH.
func TestDeviceDeathDelayHidesABlip(t *testing.T) {
	const delay = 400 * time.Millisecond
	n, fc, healthy := deathRig(t, delay)
	healthy.Store(false)
	tickWithin(t, n, 2*time.Second)
	time.Sleep(delay / 4)
	tickWithin(t, n, 2*time.Second)
	healthy.Store(true)
	tickWithin(t, n, 2*time.Second)
	// And again, so a recovery really cleared the pending death: the
	// second blip starts its own window.
	time.Sleep(delay / 2)
	healthy.Store(false)
	tickWithin(t, n, 2*time.Second)
	time.Sleep(delay * 3 / 4)
	tickWithin(t, n, 2*time.Second)
	healthy.Store(true)
	tickWithin(t, n, 2*time.Second)
	if got := lifecycle(fc); got != "DBIRTH" {
		t.Fatalf("blips inside the delay: %q, want no DDEATH and no new DBIRTH", got)
	}
	if !n.devHealth["RTU53"] {
		t.Error("the device is not live after a hidden blip")
	}
}

// An outage longer than the delay: one DDEATH, not before the delay and
// on the first tick after it, then one DBIRTH on recovery.
func TestDeviceDeathDelayThenDeath(t *testing.T) {
	const delay = 300 * time.Millisecond
	n, fc, healthy := deathRig(t, delay)
	healthy.Store(false)
	down := time.Now()
	for time.Since(down) < delay-50*time.Millisecond {
		tickWithin(t, n, 2*time.Second)
		if got := lifecycle(fc); got != "DBIRTH" {
			t.Fatalf("at +%s, inside the delay: %q, want no DDEATH yet", time.Since(down).Round(time.Millisecond), got)
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(time.Until(down.Add(delay + 10*time.Millisecond)))
	tickWithin(t, n, 2*time.Second)
	if got := lifecycle(fc); got != "DBIRTH DDEATH" {
		t.Fatalf("on the first tick past the delay: %q, want one DDEATH", got)
	}
	tickWithin(t, n, 2*time.Second)
	if got := lifecycle(fc); got != "DBIRTH DDEATH" {
		t.Fatalf("a dead device died again: %q", got)
	}
	healthy.Store(true)
	tickWithin(t, n, 2*time.Second)
	if got := lifecycle(fc); got != "DBIRTH DDEATH DBIRTH" {
		t.Fatalf("on recovery: %q, want a DBIRTH at once (births are not delayed)", got)
	}
}

// A rebirth while a death is pending decides afresh: the still-unhealthy
// device is left out of the births, and its pending death is dropped (the
// NBIRTH re-announces everything).
func TestDeviceDeathDelayClearedByARebirth(t *testing.T) {
	n, fc, healthy := deathRig(t, time.Hour)
	healthy.Store(false)
	tickWithin(t, n, 2*time.Second)
	if _, pending := n.devDownSince["RTU53"]; !pending {
		t.Fatal("setup: no pending death")
	}
	if err := n.birth(); err != nil {
		t.Fatal(err)
	}
	if _, pending := n.devDownSince["RTU53"]; pending {
		t.Error("a rebirth kept the pending death")
	}
	if got := lifecycle(fc); got != "DBIRTH" {
		t.Errorf("the rebirth with the device unhealthy: %q, want no second DBIRTH", got)
	}
}

// Nothing goes out for a device whose death is held: not a max-interval
// heartbeat with a fresh timestamp, not a value written to the store, so
// a lost device never looks current. A change made meanwhile goes out once
// it recovers, and Status says the device is held, not plainly online.
func TestDeviceDeathDelaySendsNothingWhileHeld(t *testing.T) {
	n, fc, healthy := deathRig(t, time.Hour, WithDefaultRBE(RBE{MaxInterval: 20 * time.Millisecond}))
	ddata := func() int { return len(fc.published("DDATA")) }
	// Live: the heartbeat runs.
	time.Sleep(30 * time.Millisecond)
	tickWithin(t, n, 2*time.Second)
	if ddata() == 0 {
		t.Fatal("setup: no heartbeat DDATA from the live device")
	}
	healthy.Store(false)
	tickWithin(t, n, 2*time.Second) // the hold starts
	sent := ddata()
	for range 4 {
		time.Sleep(30 * time.Millisecond) // past the heartbeat interval each time
		tickWithin(t, n, 2*time.Second)
	}
	if err := n.rt.Tags().WriteGlobal("Level", ir.RealVal(7.5)); err != nil {
		t.Fatal(err)
	}
	tickWithin(t, n, 2*time.Second)
	if got := ddata(); got != sent {
		t.Fatalf("%d DDATA while the death was held (heartbeats or the write), want none", got-sent)
	}
	st := n.Status()
	if len(st.Devices) != 1 || !st.Devices[0].Online || !st.Devices[0].DeathHeld {
		t.Errorf("Status devices %+v, want RTU53 online with its death held", st.Devices)
	}
	healthy.Store(true)
	tickWithin(t, n, 2*time.Second)
	if got := lifecycle(fc); got != "DBIRTH" {
		t.Fatalf("after the recovery: %q, want no DDEATH and no new DBIRTH", got)
	}
	var last fakePub
	for _, p := range fc.published("DDATA") {
		last = p
	}
	p, err := DecodePayload(last.payload)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range p.Metrics {
		if m.Name == "Level" && m.Value == 7.5 {
			found = true
		}
	}
	if !found {
		t.Errorf("after the recovery the last DDATA is %+v, want the 7.5 written meanwhile", p.Metrics)
	}
	if n.Status().Devices[0].DeathHeld {
		t.Error("still held after the recovery")
	}
}

// The NCMD Node Control/Rebirth path, with a death pending: the rebirth
// leaves the still-unhealthy device out of its births and drops the
// pending death, and the device births on the tick after it recovers.
func TestDeviceDeathDelayAndAnNCMDRebirth(t *testing.T) {
	n, fc, healthy := deathRig(t, time.Hour)
	healthy.Store(false)
	tickWithin(t, n, 2*time.Second)
	n.applyCommand("", Payload{Metrics: []Metric{{Name: "Node Control/Rebirth", Value: true}}})
	deadline := time.Now().Add(5 * time.Second)
	for len(fc.published("NBIRTH")) < 2 {
		if time.Now().After(deadline) {
			t.Fatal("the NCMD rebirth never published its NBIRTH")
		}
		time.Sleep(time.Millisecond)
	}
	n.mu.Lock()
	_, pending := n.devDownSince["RTU53"]
	live := n.devHealth["RTU53"]
	n.mu.Unlock()
	if pending || live {
		t.Fatalf("after the rebirth: pending %v, live %v; want the death dropped and the device left out", pending, live)
	}
	if got := lifecycle(fc); got != "DBIRTH" {
		t.Fatalf("the rebirth with the device unhealthy: %q, want no second DBIRTH", got)
	}
	healthy.Store(true)
	tickWithin(t, n, 2*time.Second)
	if got := lifecycle(fc); got != "DBIRTH DBIRTH" {
		t.Fatalf("on recovery after the rebirth: %q, want its DBIRTH", got)
	}
}
