package ir

import (
	"errors"
	"testing"
)

type nopHost struct{}

func (nopHost) ReadGlobal(string) (Value, error) { return Value{}, nil }
func (nopHost) WriteGlobal(string, Value) error  { return nil }
func (nopHost) NowMs() int64                     { return 0 }

// An index read out of bounds keeps its text, is an *IndexError, and
// faulting the same way every scan allocates nothing; another index is a
// new error, and the one handed out before is not changed.
func TestIndexErrorTextAndNoAllocation(t *testing.T) {
	arr := &Type{Kind: TypeArray, Elem: &Type{Kind: TypeInt}, ArrLen: 4}
	prog := &Program{Slots: []VarSlot{{Name: "a", Type: arr}, {Name: "i", Type: &Type{Kind: TypeInt}}, {Name: "y", Type: &Type{Kind: TypeInt}}}}
	prog.Body = []Stmt{&Assign{Target: &SlotRef{Slot: 2, T: arr.Elem}, Value: &IndexRef{Array: &SlotRef{Slot: 0, T: arr}, Index: &SlotRef{Slot: 1, T: arr.Elem}, T: arr.Elem}}}
	f := NewFrame(prog)
	f.Slots[1] = IntVal(7)
	err := Run(prog, f, nopHost{})
	var ie *IndexError
	if !errors.As(err, &ie) || err.Error() != "index 7 out of bounds [0..3]" {
		t.Fatalf("err %v (%T)", err, err)
	}
	if n := testing.AllocsPerRun(100, func() { _ = Run(prog, f, nopHost{}) }); n != 0 {
		t.Errorf("the same fault every run: %v allocs, want 0", n)
	}
	f.Slots[1] = IntVal(-1)
	if err2 := Run(prog, f, nopHost{}); err2.Error() != "index -1 out of bounds [0..3]" || err.Error() != "index 7 out of bounds [0..3]" {
		t.Errorf("another index: %v; the first error now says %v", err2, err)
	}
}
