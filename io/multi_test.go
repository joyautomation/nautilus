package io

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// named gives a Memory the tag ownership Multi routing requires: a fixed
// input and output set, over the loopback everything else in the repo
// tests with. It embeds *Memory, so it is also a BatchReader and a
// QualityReporter — the fully capable child.
type named struct {
	*Memory
	ins, outs []string
}

func (n named) InputNames() []string  { return append([]string(nil), n.ins...) }
func (n named) OutputNames() []string { return append([]string(nil), n.outs...) }

// plain is the least capable child: a Driver with tag ownership and NOTHING
// else — no ReadInputsInto, no Quality, no Start/Stop — so the fallbacks
// are exercised. It delegates to a Memory without embedding it, which would
// re-expose the optional methods.
type plain struct {
	mem       *Memory
	ins, outs []string
}

func (p plain) ReadInputs() (Values, error) { return p.mem.ReadInputs() }
func (p plain) WriteOutputs(v Values) error { return p.mem.WriteOutputs(v) }
func (p plain) InputNames() []string        { return p.ins }
func (p plain) OutputNames() []string       { return p.outs }

// startStop records lifecycle fanout.
type startStop struct {
	named
	started, stopped *[]string
	name             string
}

func (s startStop) Start(context.Context) { *s.started = append(*s.started, s.name) }
func (s startStop) Stop()                 { *s.stopped = append(*s.stopped, s.name) }

func TestMultiRoutesAndMerges(t *testing.T) {
	a := named{Memory: NewMemory(), ins: []string{"A_In"}, outs: []string{"A_Out"}}
	b := plain{mem: NewMemory(), ins: []string{"B_In"}, outs: []string{"B_Out"}}
	m, err := NewMulti(NamedDriver{"a", a}, NamedDriver{"b", b})
	if err != nil {
		t.Fatal(err)
	}

	// Writes route by ownership; each child sees only its own values.
	if err := m.WriteOutputs(Values{"A_Out": 1.0, "B_Out": 2.0, "Nobody": 3.0}); err != nil {
		t.Fatal(err)
	}
	av, _ := a.ReadInputs()
	bv, _ := b.ReadInputs()
	if !reflect.DeepEqual(av, Values{"A_Out": 1.0}) {
		t.Fatalf("child a holds %v", av)
	}
	if !reflect.DeepEqual(bv, Values{"B_Out": 2.0}) {
		t.Fatalf("child b holds %v", bv)
	}

	// Reads merge both children — a (BatchReader path) and b (ReadInputs
	// fallback) — into one delivery.
	got, err := m.ReadInputs()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, Values{"A_Out": 1.0, "B_Out": 2.0}) {
		t.Fatalf("merged read = %v", got)
	}

	// And the merged names surface for the runtime/device wiring.
	if names := m.InputNames(); !reflect.DeepEqual(names, []string{"A_In", "B_In"}) {
		t.Fatalf("InputNames = %v", names)
	}
	if names := m.OutputNames(); !reflect.DeepEqual(names, []string{"A_Out", "B_Out"}) {
		t.Fatalf("OutputNames = %v", names)
	}
}

func TestMultiReadDescribesThisDelivery(t *testing.T) {
	a := named{Memory: NewMemory(), ins: []string{"X"}, outs: []string{"X"}}
	m, err := NewMulti(NamedDriver{"a", a})
	if err != nil {
		t.Fatal(err)
	}
	dst := Values{"Gone": 9.0} // a key from some previous delivery
	a.Memory.WriteOutputs(Values{"X": 1.0})
	if err := m.ReadInputsInto(dst); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(dst, Values{"X": 1.0}) {
		t.Fatalf("dst = %v — a key no child delivered must be removed", dst)
	}
}

func TestMultiDuplicateTagIsAnError(t *testing.T) {
	a := named{Memory: NewMemory(), ins: []string{"Shared"}, outs: nil}
	b := named{Memory: NewMemory(), ins: []string{"Shared"}, outs: nil}
	_, err := NewMulti(NamedDriver{"alpha", a}, NamedDriver{"beta", b})
	if err == nil {
		t.Fatal("two claims on one input tag must refuse to construct")
	}
	for _, want := range []string{"Shared", "alpha", "beta"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}

	c := named{Memory: NewMemory(), ins: nil, outs: []string{"Cmd"}}
	d := named{Memory: NewMemory(), ins: nil, outs: []string{"Cmd"}}
	_, err = NewMulti(NamedDriver{"c", c}, NamedDriver{"d", d})
	if err == nil || !strings.Contains(err.Error(), "Cmd") {
		t.Fatalf("output dup: err = %v", err)
	}

	// The same tag as one child's input and its own output is the ordinary
	// read-back shape and must NOT error.
	e := named{Memory: NewMemory(), ins: []string{"V"}, outs: []string{"V"}}
	if _, err := NewMulti(NamedDriver{"e", e}); err != nil {
		t.Fatalf("read-back tag within one child: %v", err)
	}
}

func TestMultiRefusesTheUnroutable(t *testing.T) {
	// Memory declares no tag ownership — it owns whatever is written — so
	// it cannot share a scan.
	_, err := NewMulti(NamedDriver{"loop", NewMemory()})
	if err == nil || !strings.Contains(err.Error(), "InputNames") {
		t.Fatalf("err = %v, want a teaching error about tag ownership", err)
	}
	// Unnamed and same-named children are refused too.
	a := named{Memory: NewMemory()}
	if _, err := NewMulti(NamedDriver{"", a}); err == nil {
		t.Fatal("an unnamed child must be an error")
	}
	if _, err := NewMulti(NamedDriver{"x", a}, NamedDriver{"x", a}); err == nil {
		t.Fatal("two children with one name must be an error")
	}
	if _, err := NewMulti(); err == nil {
		t.Fatal("an empty set must be an error")
	}
}

func TestMultiQualityMerges(t *testing.T) {
	a := named{Memory: NewMemory(), ins: []string{"A"}, outs: nil}
	b := named{Memory: NewMemory(), ins: []string{"B"}, outs: nil}
	c := plain{mem: NewMemory(), ins: []string{"C"}, outs: nil} // no QualityReporter
	m, err := NewMulti(NamedDriver{"a", a}, NamedDriver{"b", b}, NamedDriver{"c", c})
	if err != nil {
		t.Fatal(err)
	}
	if q := m.Quality(); q != nil {
		t.Fatalf("all-healthy quality = %v, want nil (non-Good only)", q)
	}
	a.SetQuality("A", Stale)
	b.SetQuality("B", NotConnected)
	want := map[string]Quality{"A": Stale, "B": NotConnected}
	if q := m.Quality(); !reflect.DeepEqual(q, want) {
		t.Fatalf("merged quality = %v, want %v", q, want)
	}
}

func TestMultiReadErrorNamesTheChild(t *testing.T) {
	a := named{Memory: NewMemory(), ins: []string{"A"}, outs: nil}
	bad := failing{ins: []string{"B"}}
	m, err := NewMulti(NamedDriver{"good", a}, NamedDriver{"dark-bus", bad})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.ReadInputs(); err == nil || !strings.Contains(err.Error(), "dark-bus") {
		t.Fatalf("read err = %v, want it to name the failing child", err)
	}
	if err := m.WriteOutputs(Values{"Bcmd": 1.0}); err == nil || !strings.Contains(err.Error(), "dark-bus") {
		t.Fatalf("write err = %v, want it to name the failing child", err)
	}
}

type failing struct{ ins []string }

func (f failing) ReadInputs() (Values, error) { return nil, errors.New("socket ate it") }
func (f failing) WriteOutputs(Values) error   { return errors.New("socket ate it") }
func (f failing) InputNames() []string        { return f.ins }
func (f failing) OutputNames() []string       { return []string{"Bcmd"} }

func TestMultiStartStopFanout(t *testing.T) {
	var started, stopped []string
	child := func(name, in string) startStop {
		return startStop{
			named:   named{Memory: NewMemory(), ins: []string{in}},
			started: &started, stopped: &stopped, name: name,
		}
	}
	m, err := NewMulti(
		NamedDriver{"first", child("first", "F")},
		NamedDriver{"second", child("second", "S")},
		// A child with no Start/Stop is simply skipped.
		NamedDriver{"inert", named{Memory: NewMemory(), ins: []string{"I"}}},
	)
	if err != nil {
		t.Fatal(err)
	}
	m.Start(context.Background())
	m.Stop()
	if !reflect.DeepEqual(started, []string{"first", "second"}) {
		t.Fatalf("started = %v", started)
	}
	if !reflect.DeepEqual(stopped, []string{"second", "first"}) {
		t.Fatalf("stopped = %v — teardown runs in reverse start order", stopped)
	}
}

// gated is a child with a leadership gate the way modbus and the hw drivers
// have one: while the gate is closed a command never leaves the driver.
type gated struct {
	named
	gate *func() bool
	sent *[]string
}

func (g gated) SetWriteGate(gate func() bool) { *g.gate = gate }
func (g gated) WriteOutputs(v Values) error {
	if gf := *g.gate; gf != nil && !gf() {
		return nil
	}
	for k := range v {
		*g.sent = append(*g.sent, k)
	}
	return nil
}

func TestMultiWriteGateReachesChildren(t *testing.T) {
	var sent []string
	var gateA, gateB func() bool
	a := gated{named: named{Memory: NewMemory(), outs: []string{"A_Cmd"}}, gate: &gateA, sent: &sent}
	b := gated{named: named{Memory: NewMemory(), outs: []string{"B_Cmd"}}, gate: &gateB, sent: &sent}
	// A child with no gate is skipped, not an error.
	c := plain{mem: NewMemory(), outs: []string{"C_Cmd"}}
	m, err := NewMulti(NamedDriver{"a", a}, NamedDriver{"b", b}, NamedDriver{"c", c})
	if err != nil {
		t.Fatal(err)
	}

	// `naut run` gates whatever driver the project built; for a multi-driver
	// project that is the Multi, so it must accept the gate...
	g, ok := Driver(m).(interface{ SetWriteGate(func() bool) })
	if !ok {
		t.Fatal("Multi has no SetWriteGate — a follower's children would write")
	}
	leader := false
	g.SetWriteGate(func() bool { return leader })
	if gateA == nil || gateB == nil {
		t.Fatal("the gate did not reach every gated child")
	}

	// ...and a follower's write through it is refused by every child.
	if err := m.WriteOutputs(Values{"A_Cmd": true, "B_Cmd": true}); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 0 {
		t.Fatalf("follower sent %v", sent)
	}
	leader = true
	if err := m.WriteOutputs(Values{"A_Cmd": true, "B_Cmd": true}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(sent)
	if !reflect.DeepEqual(sent, []string{"A_Cmd", "B_Cmd"}) {
		t.Fatalf("leader sent %v", sent)
	}
}

// plainGated is a gated child whose gate is only a gate (PlainGated).
type plainGated struct{ gated }

func (plainGated) PlainWriteGate() {}

// SetPlainGate, the gate a project without redundancy gets (#625), reaches
// only the children whose gate is only a gate: a child that reads any
// gate as "one replica of a redundant set" (optommp, a Sparkplug host) is
// left ungated, as it is without redundancy today.
func TestSetPlainGateReachesOnlyPlainGatedChildren(t *testing.T) {
	var sent []string
	var gateA, gateB func() bool
	a := plainGated{gated{named: named{Memory: NewMemory(), outs: []string{"A_Cmd"}}, gate: &gateA, sent: &sent}}
	b := gated{named: named{Memory: NewMemory(), outs: []string{"B_Cmd"}}, gate: &gateB, sent: &sent}
	m, err := NewMulti(NamedDriver{"a", a}, NamedDriver{"b", b})
	if err != nil {
		t.Fatal(err)
	}
	if n := SetPlainGate(m, func() bool { return false }); n != 1 {
		t.Fatalf("%d children took the gate, want 1", n)
	}
	if gateA == nil || gateB != nil {
		t.Fatalf("plain child gated: %v, replica-minded child gated: %v", gateA != nil, gateB != nil)
	}
	if n := SetPlainGate(b, func() bool { return false }); n != 0 || gateB != nil {
		t.Fatal("a lone driver that is not PlainGated took the gate")
	}
}
