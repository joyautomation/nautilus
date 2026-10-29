package io

import (
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
)

// imaged is a child that keeps its own image: reads always succeed, and
// Delivered says whether the field has answered (io.DeliveryReporter).
type imaged struct {
	named
	done *atomic.Bool
}

func (i imaged) Delivered() bool { return i.done.Load() }

// flaky is a plain child whose reads fail while fail is set.
type flaky struct {
	plain
	fail *atomic.Bool
}

func (f flaky) ReadInputs() (Values, error) {
	if f.fail.Load() {
		return nil, errors.New("down")
	}
	return f.plain.ReadInputs()
}

func TestDeliveredWithoutAReporterIsTrue(t *testing.T) {
	if !Delivered(NewMemory()) {
		t.Fatal("a driver without DeliveryReporter is judged by its read alone")
	}
}

// A Multi has delivered when every reporting child has; Pending names the
// children not heard from — a reporting child that has not delivered, or
// any child whose part of the last read failed — for the runtime's
// start-up hold to put in its status and its WARN.
func TestMultiDeliveredAndPending(t *testing.T) {
	var done, fail atomic.Bool
	fail.Store(true)
	a := imaged{named: named{Memory: NewMemory(), ins: []string{"A_In"}}, done: &done}
	b := flaky{plain: plain{mem: NewMemory(), ins: []string{"B_In"}}, fail: &fail}
	c := named{Memory: NewMemory(), ins: []string{"C_In"}}
	m, err := NewMulti(NamedDriver{"rack", a}, NamedDriver{"meter", b}, NamedDriver{"loop", c})
	if err != nil {
		t.Fatal(err)
	}
	if m.Delivered() {
		t.Fatal("Delivered while rack has not")
	}
	if err := m.ReadInputsInto(Values{}); err == nil {
		t.Fatal("meter's read should fail the set's")
	}
	if got := m.Pending(); !reflect.DeepEqual(got, []string{"rack", "meter"}) {
		t.Fatalf("Pending = %v, want [rack meter]", got)
	}

	done.Store(true)
	if !m.Delivered() {
		t.Fatal("every reporting child has delivered; the read covers the rest")
	}
	if got := m.Pending(); !reflect.DeepEqual(got, []string{"meter"}) {
		t.Fatalf("Pending = %v, want [meter]", got)
	}

	fail.Store(false)
	if err := m.ReadInputsInto(Values{}); err != nil {
		t.Fatal(err)
	}
	if got := m.Pending(); len(got) != 0 {
		t.Fatalf("Pending = %v after every child delivered", got)
	}
}
