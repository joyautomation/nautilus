package snmp_test

// serve_test.go pins the manifest read backwards (Feeds, Feed.Serve,
// Feed.Rate) and the stand-in that serves a plant through it
// (agent.Plant): what the plant writes is what the driver reads, through
// the manifest the importer generated — no second mapping anywhere.

import (
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/hw"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/snmp"
	"github.com/joyautomation/nautilus/snmp/agent"
	"github.com/joyautomation/nautilus/snmp/walk"
)

func TestFeedsCoverTheManifestOnce(t *testing.T) {
	r := newRig(t, "switch.snmpwalk", "SW1")
	feeds, err := r.m.Feeds("SW1")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, f := range feeds {
		if seen[f.OID] {
			t.Fatalf("%s fed twice", f.OID)
		}
		seen[f.OID] = true
	}
	// Every non-static binding is either a feed or shadowed by one.
	fed := map[string]bool{}
	for _, f := range feeds {
		fed[f.Tag+"."+f.Member] = true
		for _, s := range f.Shadowed {
			fed[s] = true
		}
	}
	for _, tag := range r.m.Tags {
		for name, mb := range tag.Members {
			if !mb.IsStatic() && !fed[tag.Name+"."+name] {
				t.Errorf("%s.%s (oid %s) has no feed", tag.Name, name, mb.OID)
			}
		}
	}
	// ErrorRate and InErrors share ifInErrors: the rate member drives the
	// counter, the absolute one reads it.
	for _, f := range feeds {
		if f.Tag == "SW1_Port01" && f.Member == "ErrorRate" {
			if !reflect.DeepEqual(f.Shadowed, []string{"SW1_Port01.InErrors"}) {
				t.Fatalf("ErrorRate shadows %v", f.Shadowed)
			}
		}
	}
	if got := r.m.OnlineTag("SW1"); got != "SW1" {
		t.Fatalf("OnlineTag = %q", got)
	}
	if got := r.m.TagPatterns("SW1"); !reflect.DeepEqual(got, []string{"SW1", "SW1_*"}) {
		t.Fatalf("TagPatterns = %v", got)
	}
	if _, err := r.m.Feeds("SW9"); err == nil {
		t.Fatal("an unknown source must be an error")
	}
}

// For every OID of a real imported switch: the value the driver reads from
// the recording, served back, is the recording (identity); and every BOOL
// member flipped, served, reads flipped.
func TestServeInvertsEveryBinding(t *testing.T) {
	w := loadWalk(t, "switch.snmpwalk")
	r := newRig(t, "switch.snmpwalk", "SW1")
	feeds, err := r.m.Feeds("SW1")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for _, f := range feeds {
		cur, ok := w.Get(f.OID)
		if !ok {
			continue // a row the fixture lacks: the driver marks it Bad
		}
		raw, ok, err := snmp.RawOf(cur)
		if err != nil || !ok {
			continue
		}
		if f.Binding.Rate {
			v := ir.RealVal(8e6)
			perSec, err := f.Rate(v)
			if err != nil {
				t.Fatalf("%s.%s: %v", f.Tag, f.Member, err)
			}
			var c hw.Counter
			_, _, _ = f.Binding.Apply(f.Field, hw.RawUintVal(1000), &c, now)
			got, _, _ := f.Binding.Apply(f.Field, hw.RawUintVal(1000+uint64(perSec*10)), &c, now.Add(10*time.Second))
			if math.Abs(got.F-8e6) > 1 {
				t.Errorf("%s.%s: rate reads back %v", f.Tag, f.Member, got.F)
			}
			continue
		}
		v, ok, err := f.Binding.Apply(f.Field, raw, nil, now)
		if err != nil || !ok {
			t.Fatalf("%s.%s: the recording does not read: %v", f.Tag, f.Member, err)
		}
		vb, err := f.Serve(v, cur)
		if err != nil {
			t.Fatalf("%s.%s: %v", f.Tag, f.Member, err)
		}
		if vb.ValueString() != cur.ValueString() || vb.Type != cur.Type {
			t.Errorf("%s.%s: recorded %s served back as %s", f.Tag, f.Member, cur.ValueString(), vb.ValueString())
		}
		if v.Kind == ir.TypeBool {
			flip := ir.BoolVal(!v.B)
			vb, err := f.Serve(flip, cur)
			if err != nil {
				t.Fatalf("%s.%s flipped: %v", f.Tag, f.Member, err)
			}
			raw2, _, _ := snmp.RawOf(vb)
			got, _, err := f.Binding.Apply(f.Field, raw2, nil, now)
			if err != nil || got.B != flip.B {
				t.Errorf("%s.%s: served %s for %v, reads %v (%v)", f.Tag, f.Member, vb.ValueString(), flip.B, got.B, err)
			}
		}
	}
}

func TestVarbindOf(t *testing.T) {
	mac := walk.Varbind{OID: "1.3.6.1.2.1.2.2.1.6.1", Type: walk.OctetString, Bytes: []byte{0, 0x1a, 0x2b, 0x3c, 0x4d, 0x5e}}
	vb, err := snmp.VarbindOf(mac, hw.RawStringVal("00:1A:2B:3C:4D:5F"))
	if err != nil || string(vb.Bytes) != string([]byte{0, 0x1a, 0x2b, 0x3c, 0x4d, 0x5f}) {
		t.Fatalf("MAC: %x %v", vb.Bytes, err)
	}
	c32 := walk.Varbind{OID: "1.3.6.1.2.1.2.2.1.14.1", Type: walk.Counter32}
	if vb, _ := snmp.VarbindOf(c32, hw.RawUintVal(1<<32+5)); vb.Uint != 5 {
		t.Fatalf("Counter32 wraps: %d", vb.Uint)
	}
	if _, err := snmp.VarbindOf(walk.Varbind{OID: "1.3.6.1.2.1.1.3.0", Type: walk.TimeTicks}, hw.RawStringVal("x")); err == nil {
		t.Fatal("a string into TimeTicks must be an error")
	}
}

// The end to end of step 1: the plant writes a port down, a rate and the
// switch dark; the real driver, polling the stand-in through the SAME
// manifest, reads exactly that — and the rest keeps reading the recording.
func TestPlantDrivesTheDriver(t *testing.T) {
	r := newRig(t, "switch.snmpwalk", "SW1")
	p, err := agent.NewPlant(r.agent, r.m, "SW1")
	if err != nil {
		t.Fatal(err)
	}
	p.Log = quiet()
	d := r.start(t)
	waitFor(t, "SW1 online", func() bool { return online(d, "SW1") })
	waitPolls(t, d, "SW1", 1)
	before := read(t, d)
	if !member(t, before, "SW1_Port01", "OperUp").B || !member(t, before, "SW1_Port01", "AdminUp").B {
		t.Fatal("fixture: SW1_Port01 should be up")
	}
	name := member(t, before, "SW1_Port02", "Name").S

	// Only the members the plant has: everything else stays recorded.
	plant := map[string]any{
		"SW1_Port01": map[string]any{"OperUp": false, "InBps": 8e6, "OutBps": 0.0},
		"SW1":        map[string]any{"Online": true, "UptimeS": 42.0},
		"Unrelated":  3.0,
	}
	if n := p.Apply(plant); n != 4 {
		t.Fatalf("Apply served %d OIDs, want 4", n)
	}
	waitFor(t, "SW1_Port01 down", func() bool {
		v := read(t, d)
		return !member(t, v, "SW1_Port01", "OperUp").B && member(t, v, "SW1_Port01", "Down").B
	})
	waitPolls(t, d, "SW1", 3)
	v := read(t, d)
	if got := member(t, v, "SW1_Port01", "InBps").F; math.Abs(got-8e6) > 8e6*0.05 {
		t.Errorf("InBps = %v, want ≈8e6", got)
	}
	if got := member(t, v, "SW1_Port01", "OutBps").F; got != 0 {
		t.Errorf("OutBps = %v, want 0", got)
	}
	if got := member(t, v, "SW1", "UptimeS").I; got != 42 {
		t.Errorf("UptimeS = %v", got)
	}
	if !member(t, v, "SW1_Port01", "AdminUp").B || member(t, v, "SW1_Port02", "Name").S != name {
		t.Error("members the plant does not write must keep reading the recording")
	}

	// Back up.
	plant["SW1_Port01"].(map[string]any)["OperUp"] = true
	p.Apply(plant)
	waitFor(t, "SW1_Port01 up", func() bool { return member(t, read(t, d), "SW1_Port01", "OperUp").B })

	// The switch goes dark and comes back.
	plant["SW1"].(map[string]any)["Online"] = false
	p.Apply(plant)
	waitFor(t, "SW1 offline", func() bool { return !online(d, "SW1") })
	plant["SW1"].(map[string]any)["Online"] = true
	p.Apply(plant)
	waitFor(t, "SW1 back", func() bool { return online(d, "SW1") })
}

// A rate steered second by second never steps the counter backwards.
func TestPlantRateIsMonotonic(t *testing.T) {
	r := newRig(t, "switch.snmpwalk", "SW1")
	p, _ := agent.NewPlant(r.agent, r.m, "SW1")
	p.Log = quiet()
	var oid string
	for _, f := range p.Feeds {
		if f.Tag == "SW1_Port01" && f.Member == "InBps" {
			oid = f.OID
		}
	}
	var last uint64
	for i, bps := range []float64{8e9, 0, 8e9, 1e3} {
		p.Apply(map[string]any{"SW1_Port01": map[string]any{"InBps": bps}})
		time.Sleep(20 * time.Millisecond)
		v, _ := r.agent.Value(oid)
		if v.Uint < last {
			t.Fatalf("step %d: counter went %d → %d", i, last, v.Uint)
		}
		last = v.Uint
	}
}
