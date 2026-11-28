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
	elem := func(i int) MemberPath { p, _ := memberPath([]accessor{{arrIdx: i}}); return p }
	one, _ := memberPath([]accessor{{fieldIdx: 0, isField: true}})
	for i := 0; i < maxInOutPaths; i++ {
		s.mark(elem(i), true)
	}
	if s.all {
		t.Fatal("whole before the bound")
	}
	// The same member again, however often, is one record.
	for i := 0; i < 3*maxInOutPaths; i++ {
		s.mark(elem(0), true)
	}
	if s.all || len(s.paths) != maxInOutPaths {
		t.Fatalf("a repeated assignment counted towards the bound: all=%v paths=%d", s.all, len(s.paths))
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

// A recorded whole member covers an assignment inside it, and a bit within
// recorded bits; joining bits within bits is refused.
func TestInOutCovers(t *testing.T) {
	speed, _ := memberPath([]accessor{{fieldIdx: 1, isField: true}})
	inner, _ := memberPath([]accessor{{fieldIdx: 0, isField: true}, {fieldIdx: 1, isField: true}})
	bit3 := speed
	bit3.bits = 1 << 3
	bits := speed
	bits.bits = 0xF
	for _, tc := range []struct {
		a, b MemberPath
		want bool
	}{{speed, speed, true}, {speed, inner, true}, {inner, speed, false}, {bits, bit3, true}, {bit3, bits, false}, {bit3, speed, false}} {
		if got := covers(tc.a, tc.b); got != tc.want {
			t.Errorf("covers(%+v, %+v) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
	if _, ok := bit3.Join(MemberPath{bits: 1 << 2}); ok {
		t.Error("bits within bits joined")
	}
	full := MemberPath{n: MaxMemberDepth - 1}
	if _, ok := full.Join(speed); !ok {
		t.Error("a join to exactly MaxMemberDepth refused")
	}
}
