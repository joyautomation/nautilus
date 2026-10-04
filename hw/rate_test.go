package hw

import (
	"github.com/joyautomation/nautilus/lang/ir"
	"testing"
	"time"
)

func TestCounterRate(t *testing.T) {
	t0 := time.Unix(1000, 0)
	var c Counter
	if _, ok := c.Observe(1000, 64, t0); ok {
		t.Fatal("first sample must not yield a rate")
	}
	r, ok := c.Observe(3000, 64, t0.Add(10*time.Second))
	if !ok || r != 200 {
		t.Fatalf("rate = %v, %v; want 200", r, ok)
	}
	// 64-bit going backwards is a reset: no rate, but the sample is kept
	// so the next delta is computed from it.
	if _, ok := c.Observe(100, 64, t0.Add(20*time.Second)); ok {
		t.Fatal("a 64-bit step backwards must read as a reset")
	}
	r, ok = c.Observe(600, 64, t0.Add(30*time.Second))
	if !ok || r != 50 {
		t.Fatalf("after reset: rate = %v, %v; want 50", r, ok)
	}
	// Same instant: no rate (no division by zero).
	if _, ok := c.Observe(700, 64, t0.Add(30*time.Second)); ok {
		t.Fatal("zero elapsed must not yield a rate")
	}
}

func TestCounterWrap32(t *testing.T) {
	t0 := time.Unix(1000, 0)
	var c Counter
	c.Observe(1<<32-100, 32, t0)
	r, ok := c.Observe(100, 32, t0.Add(time.Second))
	if !ok || r != 200 {
		t.Fatalf("32-bit wrap: rate = %v, %v; want 200", r, ok)
	}
	c.Reset()
	if _, ok := c.Observe(5, 32, t0.Add(2*time.Second)); ok {
		t.Fatal("after Reset the next sample is a first sample")
	}
}

func TestCounterFloat(t *testing.T) {
	t0 := time.Unix(1000, 0)
	var c Counter
	if _, ok := c.ObserveFloat(10.25, t0); ok {
		t.Fatal("first sample")
	}
	r, ok := c.ObserveFloat(12.75, t0.Add(10*time.Second))
	if !ok || r != 0.25 {
		t.Fatalf("rate = %v %v; the fraction must survive", r, ok)
	}
	if _, ok := c.ObserveFloat(1, t0.Add(20*time.Second)); ok {
		t.Fatal("any decrease is a reset")
	}
	// Through a binding: a RawFloat counter uses the float path, a RawUint
	// the wire-width one.
	b := Binding{Rate: true}
	f := field("X", ir.TypeReal)
	c.Reset()
	b.Apply(f, RawFloatVal(100.5), &c, t0)
	v, ok, _ := b.Apply(f, RawFloatVal(101.5), &c, t0.Add(time.Second))
	if !ok || v.F != 1 {
		t.Fatalf("float rate = %+v %v", v, ok)
	}
}
