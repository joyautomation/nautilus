package ir

import "testing"

// Join appends steps, keeps bits at the leaf, and refuses a step below a
// bit or a path past MaxMemberDepth.
func TestMemberPathJoin(t *testing.T) {
	base, _ := memberPath([]accessor{{fieldIdx: 1, isField: true}})
	sub, _ := memberPath([]accessor{{arrIdx: 3}, {fieldIdx: 2, isField: true}}) // leaf first: .2 then [3]
	sub.bits = 1 << 4
	j, ok := base.Join(sub)
	if !ok || j.Len() != 3 || j.Bits() != 1<<4 {
		t.Fatalf("Join = %+v, %v", j, ok)
	}
	for i, want := range []struct {
		idx   int
		field bool
	}{{1, true}, {2, true}, {3, false}} {
		if idx, f := j.Step(i); idx != want.idx || f != want.field {
			t.Errorf("step %d = %d,%v, want %d,%v", i, idx, f, want.idx, want.field)
		}
	}
	bit := MemberPath{bits: 1}
	if _, ok := bit.Join(base); ok {
		t.Error("a step below a bit joined")
	}
	deep := MemberPath{n: MaxMemberDepth}
	if _, ok := deep.Join(base); ok {
		t.Error("a path past MaxMemberDepth joined")
	}
}

// The record fails towards whole: past maxInOutPaths, or on a path it
// cannot describe, the pin is taken whole.
func TestInOutRecordFailsTowardsWhole(t *testing.T) {
	var s inOutSlot
	one, _ := memberPath([]accessor{{fieldIdx: 0, isField: true}})
	for i := 0; i < maxInOutPaths; i++ {
		s.mark(one, true)
	}
	if s.all {
		t.Fatal("whole before the bound")
	}
	s.mark(one, true)
	if !s.all || len(s.paths) != 0 {
		t.Errorf("past the bound: all=%v paths=%d, want whole", s.all, len(s.paths))
	}
	var u inOutSlot
	u.mark(MemberPath{}, false)
	if !u.all {
		t.Error("an undescribable store did not take the pin whole")
	}
	var w inOutSlot
	w.mark(MemberPath{}, true) // the whole slot assigned
	if !w.all {
		t.Error("assigning the whole slot did not take it whole")
	}
}
