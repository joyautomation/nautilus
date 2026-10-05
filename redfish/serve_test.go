package redfish

// serve_test.go pins the manifest read backwards (Feeds, Feed.Serve) and
// the stand-in that serves a plant through it (Plant): what the plant
// writes is what the driver reads, through the same manifest.

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/hw"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/redfish/mockup"
)

// read is the driver's decode, without a driver: the member's value from
// a body, or false when the body does not carry it.
func read(t *testing.T, f Feed, doc map[string]any) (ir.Value, bool) {
	t.Helper()
	vals, err := f.Path.Eval(doc)
	if err != nil {
		t.Fatalf("%s.%s: %v", f.Tag, f.Member, err)
	}
	if f.Binding.Exists != nil {
		return ir.BoolVal((len(vals) > 0) == *f.Binding.Exists), true
	}
	if len(vals) == 0 {
		return ir.Value{}, false
	}
	var raw hw.Raw
	var ok bool
	if f.Binding.Agg != "" {
		raw, ok = aggregate(vals, f.Binding.Agg)
	} else {
		raw, ok = toRaw(vals[0])
	}
	if !ok {
		return ir.Value{}, false
	}
	v, ok, err := f.Binding.Binding.Apply(f.Field, raw, nil, time.Now())
	if err != nil || !ok {
		return ir.Value{}, false
	}
	return v, true
}

func same(a, b ir.Value) bool {
	return a.Kind == b.Kind && a.B == b.B && a.I == b.I && a.F == b.F && a.S == b.S
}

// For every member of the subsystem fixture: the value the driver reads,
// served back, leaves the body as recorded; every BOOL flipped reads
// flipped; every number moved reads moved.
func TestServeInvertsEveryBinding(t *testing.T) {
	tree, err := mockup.LoadDir("testdata/subsystem-1u")
	if err != nil {
		t.Fatal(err)
	}
	m := manifestFor(t, subsystemManifest, "http://127.0.0.1:1", "")
	feeds, err := m.Feeds("NODE1")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	defer func() {
		// Every value member of the fixture's manifest is read, served and moved.
		if checked < 18 {
			t.Errorf("only %d members exercised", checked)
		}
	}()
	if last := feeds[len(feeds)-1]; last.Binding.Exists == nil {
		t.Fatal("exists: members must come after the value members")
	}
	for _, f := range feeds {
		doc, err := tree.Get(f.Resource)
		if err != nil {
			t.Fatalf("%s: %v", f.Resource, err)
		}
		v, ok := read(t, f, doc)
		if !ok {
			continue
		}
		before, _ := json.Marshal(doc)
		if err := f.Serve(doc, v); err != nil {
			t.Fatalf("%s.%s: %v", f.Tag, f.Member, err)
		}
		if after, _ := json.Marshal(doc); string(after) != string(before) {
			t.Errorf("%s.%s: serving the recorded %v rewrote the body", f.Tag, f.Member, v)
		}
		var want ir.Value
		switch v.Kind {
		case ir.TypeBool:
			want = ir.BoolVal(!v.B)
		case ir.TypeReal:
			want = ir.RealVal(v.F + 7)
		case ir.TypeInt:
			if f.Binding.Map != nil {
				want = ir.IntVal((v.I + 1) % 3) // Health: 0 → 1 → 2 → 0
			} else {
				want = ir.IntVal(v.I + 7)
			}
		case ir.TypeString:
			want = ir.StringVal(v.S + "-sim")
		}
		if f.Binding.Exists != nil && !want.B == *f.Binding.Exists {
			continue // "there again" is the value member's job, not exists:'s
		}
		if err := f.Serve(doc, want); err != nil {
			t.Fatalf("%s.%s = %v: %v", f.Tag, f.Member, want, err)
		}
		checked++
		if got, ok := read(t, f, doc); !ok || !same(got, want) {
			t.Errorf("%s.%s: served %v, reads %v (%v)", f.Tag, f.Member, want, got, ok)
		}
	}
}

// The end to end: the plant writes a fan stopped and faulted, a PSU off
// line, a CPU running hot and then its sensor gone, the server off; the
// real driver, polling the stand-in through the SAME manifest, reads
// exactly that — and the BMC going dark and coming back.
func TestPlantDrivesTheDriver(t *testing.T) {
	t.Setenv("RF_DRIVER_PASSWORD", driverPassword)
	srv := standIn(t, "subsystem-1u")
	m := manifestFor(t, subsystemManifest, srv.URL(), "")
	p, err := NewPlant(srv, m, "NODE1")
	if err != nil {
		t.Fatal(err)
	}
	p.Log = quiet()
	if p.Online != "NODE1" {
		t.Fatalf("Online tag %q", p.Online)
	}
	for _, f := range p.Feeds {
		if f.Tag == "NODE1" && f.Member == "Online" {
			t.Fatal("the root's Online is the dark switch, not a fed member")
		}
	}
	d, err := New(m, WithLogger(quiet()))
	if err != nil {
		t.Fatal(err)
	}
	startDriver(t, d)
	waitFor(t, "first poll", func() bool { return online(d) })

	plant := map[string]any{
		"NODE1":           map[string]any{"Online": true, "PowerOn": false, "Health": 2.0, "MaxTempC": 96.0},
		"NODE1_Fan1":      map[string]any{"RPM": 0.0, "Fault": true, "Present": true},
		"NODE1_PSU1":      map[string]any{"InputOk": false, "InputV": 0.0},
		"NODE1_Temp_CPU1": map[string]any{"Value": 96.0, "Fault": false},
	}
	if n := p.Apply(plant); n != 10 {
		t.Fatalf("Apply served %d members, want 10", n)
	}
	checks := []struct {
		tag, m string
		want   ir.Value
	}{
		{"NODE1", "PowerOn", ir.BoolVal(false)},
		{"NODE1", "Fault", ir.BoolVal(true)},
		{"NODE1", "MaxTempC", ir.RealVal(96)},
		{"NODE1_Fan1", "RPM", ir.RealVal(0)},
		{"NODE1_Fan1", "Fault", ir.BoolVal(true)},
		{"NODE1_PSU1", "InputOk", ir.BoolVal(false)},
		{"NODE1_PSU1", "CapacityW", ir.RealVal(800)}, // not written: still recorded
		{"NODE1_Temp_CPU1", "Value", ir.RealVal(96)},
		{"NODE1_Temp_CPU1", "High", ir.BoolVal(true)},
	}
	waitFor(t, "the plant's values", func() bool {
		vals, _ := d.ReadInputs()
		for _, c := range checks {
			if !same(member(t, vals, c.tag, c.m), c.want) {
				return false
			}
		}
		return true
	})

	// The sensor goes: its Reading is removed, the driver reads Fault.
	plant["NODE1_Temp_CPU1"] = map[string]any{"Value": 96.0, "Fault": true}
	p.Apply(plant)
	waitFor(t, "sensor fault", func() bool {
		vals, _ := d.ReadInputs()
		return member(t, vals, "NODE1_Temp_CPU1", "Fault").B
	})

	// Dark, and back.
	plant["NODE1"].(map[string]any)["Online"] = false
	p.Apply(plant)
	waitFor(t, "NODE1 offline", func() bool { return !online(d) })
	plant["NODE1"].(map[string]any)["Online"] = true
	p.Apply(plant)
	waitFor(t, "NODE1 back", func() bool { return online(d) })
}
