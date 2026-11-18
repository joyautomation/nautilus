//go:build !js

package devbind

import (
	"context"
	"strings"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/modbus"
	"github.com/joyautomation/nautilus/modbus/slave"
)

// A delivery that changes nothing costs nothing: the assembler hands back
// the value it delivered last. It runs every scan, for every device.
func TestAssemblerSteadyStateAllocatesNothing(t *testing.T) {
	a := testAssembler(t)
	sw, cur := any(int64(0x237)), any(12.5)
	vals := map[string]any{}
	fill := func() {
		vals["CONV.StatusWord"], vals["CONV.Current"] = sw, cur
		a.take(vals)
	}
	fill()
	if n := testing.AllocsPerRun(200, fill); n != 0 {
		t.Errorf("%v allocations per unchanged delivery, want 0", n)
	}
}

// The wrapped driver's batch read, steady state against a live device:
// no allocation per scan (the driver's own fast path stays fast).
func TestWrappedReadInputsIntoSteadyState(t *testing.T) {
	srv := slave.NewServer("", nil)
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Stop)
	srv.Unit(1).SetHolding(3, 0x237)
	s, err := Resolve(vendored(t, "abb-acs355-fmbt21"), []Device{{Name: "CONV", Profile: "abb-acs355-fmbt21", Endpoint: srv.Addr()}})
	if err != nil {
		t.Fatal(err)
	}
	mm, err := s.MergeModbus(modbus.Manifest{})
	if err != nil {
		t.Fatal(err)
	}
	md, err := modbus.New(mm, modbus.WithScanRate(10*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	w := s.WrapModbus(md)
	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)
	t.Cleanup(func() { cancel(); w.Stop() })
	dst := nio.Values{}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := w.ReadInputsInto(dst); err != nil {
			t.Fatal(err)
		}
		if v, ok := dst["CONV"].(ir.Value); ok && v.Fld[v.Struct.FieldIndex["StatusWord"]].I == 0x237 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("never delivered: %v", dst)
		}
		time.Sleep(10 * time.Millisecond)
	}
	base := nio.Values{}
	_ = md.ReadInputsInto(base)
	inner := testing.AllocsPerRun(100, func() { _ = md.ReadInputsInto(base) })
	t.Logf("the Modbus driver alone: %v allocations per scan", inner)
	if n := testing.AllocsPerRun(100, func() { _ = w.ReadInputsInto(dst) }); n > inner {
		t.Errorf("%v allocations per scan, the driver alone %v: the wrap adds some", n, inner)
	}
	for k := range dst {
		if len(k) > 5 && k[:5] == "CONV." {
			t.Errorf("member %s leaked into the delivery", k)
		}
	}
}

// Each NewAssembly is the adapter's input side afresh: it builds the
// struct from members as the driver's own does, and keeps no state with
// another assembly or with the driver, so one test's values (the
// in-process face makes one per test) never show up in the next.
func TestNewAssemblyIsFresh(t *testing.T) {
	s, err := Resolve(vendored(t, "abb-acs355-fmbt21"), []Device{{Name: "CONV", Profile: "abb-acs355-fmbt21", Endpoint: "10.0.0.11"}})
	if err != nil {
		t.Fatal(err)
	}
	mm, err := s.MergeModbus(modbus.Manifest{})
	if err != nil {
		t.Fatal(err)
	}
	md, err := modbus.New(mm)
	if err != nil {
		t.Fatal(err)
	}
	w := s.WrapModbus(md)

	first := w.NewAssembly()
	vals := nio.Values{"CONV.StatusWord": int64(0x237), "CONV.Actual1": int64(1234)}
	first(vals)
	v, ok := vals["CONV"].(ir.Value)
	if !ok {
		t.Fatalf("no struct delivered: %v", vals)
	}
	if got := v.Fld[v.Struct.FieldIndex["Actual1"]].I; got != 1234 {
		t.Errorf("CONV.Actual1 = %d, want 1234", got)
	}
	if _, ok := vals["CONV.Actual1"]; ok {
		t.Error("a member stayed in the delivery")
	}

	// A second assembly has seen nothing: no member, no struct.
	second := w.NewAssembly()
	empty := nio.Values{}
	second(empty)
	if _, ok := empty["CONV"]; ok {
		t.Errorf("a fresh assembly delivered the first one's values: %v", empty)
	}
	// Nor has the driver's own adapter.
	own := nio.Values{}
	w.assemble(own)
	if _, ok := own["CONV"]; ok {
		t.Errorf("the driver's adapter delivered an assembly's values: %v", own)
	}
}

// The devices' sources and bindings join the project's own manifest; one
// the project already has is an error, not a silent second binding.
func TestMergeModbusCollisions(t *testing.T) {
	s, err := Resolve(vendored(t, "abb-acs355-fmbt21"), []Device{{Name: "CONV", Profile: "abb-acs355-fmbt21", Endpoint: "h"}})
	if err != nil {
		t.Fatal(err)
	}
	ok, err := s.MergeModbus(modbus.Manifest{Sources: []modbus.Source{{ID: "RIO", Host: "h"}}, Tags: []modbus.TagBinding{{Name: "Level", Source: "RIO", Table: "holding"}}})
	if err != nil || len(ok.Sources) != 2 || ok.Tags[0].Name != "Level" {
		t.Fatalf("a manifest beside the devices: %v %+v", err, ok)
	}
	if _, err := s.MergeModbus(modbus.Manifest{Sources: []modbus.Source{{ID: "CONV", Host: "h"}}}); err == nil || !strings.Contains(err.Error(), "already has a source CONV") {
		t.Errorf("a source the device also is: %v", err)
	}
	if _, err := s.MergeModbus(modbus.Manifest{Sources: []modbus.Source{{ID: "RIO", Host: "h"}}, Tags: []modbus.TagBinding{{Name: "CONV_ControlWord", Source: "RIO", Table: "holding"}}}); err == nil || !strings.Contains(err.Error(), "already binds CONV_ControlWord") {
		t.Errorf("a binding a device command also is: %v", err)
	}
}
