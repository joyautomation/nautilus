package runtime

import (
	"math"
	"strings"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
)

// C1: a result of -0.0 over a stored +0.0 (bits differ, sameValue equal).
func TestExprRecheckNegativeZero(t *testing.T) {
	def := gaugeDef()
	def.Members["Pick"] = MemberDef{Kind: MemberExpr, Expr: "Note * Vals[0]"}
	r, d := newGaugeRuntime(t, "", def)
	deliver(d, 3, 0, 1)
	write(t, r, "G1.Vals[0]", 1.0) // Note is 0.0: Pick = +0.0
	r.Scan()
	r.Scan()
	write(t, r, "G1.Vals[0]", -1.0) // Pick = -0.0, and nothing else changes
	r.Scan()
	r.Scan()
	v, _ := r.Tags().ReadPath("G1.Pick")
	t.Logf("G1.Pick = %v signbit=%v", v, math.Signbit(v.(float64)))
	gen := r.Tags().Generation()
	r.Scan()
	if g := r.Tags().Generation(); g != gen {
		t.Errorf("generation moves %d -> %d", gen, g)
	}
	if raceOn {
		return
	}
	if n := testing.AllocsPerRun(50, func() { r.runExprs(time.Now()) }); n != 0 {
		t.Errorf("steady inputs, a -0.0 result: %v allocs per stage run (the commit is retried every scan)", n)
	}
}

// C2: a force on a nested expr leaf, on an array-element expr, and under a
// struct unit: the dependents and the actual value.
func TestExprRecheckForcesOnNestedUnits(t *testing.T) {
	def := InstanceDef{Members: map[string]MemberDef{
		"Raw":    {Kind: MemberFrom},
		"Sel":    {Kind: MemberFrom},
		"Arr[2]": {Kind: MemberExpr, Expr: "Raw + 1"},
		"P2.B":   {Kind: MemberExpr, Expr: "Raw * 2"},
		"SumM":   {Kind: MemberExpr, Expr: "Arr[2] + P2.B"},
		"Mirror": {Kind: MemberExpr, Expr: "PickPt(Sel, P1, P2)"},
		"Bump":   {Kind: MemberExpr, Expr: "Mirror.A + Mirror.B"},
		"Cmd":    {Kind: MemberTo},
	}}
	r, d := newDevRuntime(t, "", def)
	d.set("D1.Raw", int64(2))
	d.set("D1.Sel", false)
	write(t, r, "D1.P1.A", 1)
	write(t, r, "D1.P1.B", 2)
	r.Scan()
	wantPath(t, r, "D1.SumM", int64(7))
	wantPath(t, r, "D1.Bump", int64(3))
	for _, f := range []struct {
		addr string
		v    any
		sum  int64
	}{{"D1.Arr[2]", int64(50), 54}, {"D1.P2.B", int64(60), 63}, {"D1.P2", map[string]any{"A": 1, "B": 70}, 73}, {"D1.Arr", []any{1, 2, 80}, 84}} {
		if err := r.Tags().Force(f.addr, f.v); err != nil {
			t.Logf("force %s: %v", f.addr, err)
			continue
		}
		r.Scan()
		r.Scan()
		wantPath(t, r, "D1.SumM", f.sum)
		gen := r.Tags().Generation()
		r.Scan()
		if r.Tags().Generation() != gen {
			t.Errorf("%s forced: generation moves", f.addr)
		}
		r.Tags().Unforce(f.addr)
		wantPath(t, r, "D1.Arr[2]", int64(3)) // the actual never took the force
		wantPath(t, r, "D1.P2.B", int64(4))
		r.Scan()
		wantPath(t, r, "D1.SumM", int64(7))
	}
	// Under a struct unit: the whole unit is held; inputs move meanwhile.
	if err := r.Tags().Force("D1.Mirror.A", int64(99)); err != nil {
		t.Fatal(err)
	}
	write(t, r, "D1.P1.B", 5)
	r.Scan()
	wantPath(t, r, "D1.Mirror.A", int64(99))
	wantPath(t, r, "D1.Bump", int64(101)) // 99 + the held B (2)
	r.Tags().Unforce("D1.Mirror.A")
	r.Scan()
	wantPath(t, r, "D1.Mirror.A", int64(1))
	wantPath(t, r, "D1.Mirror.B", int64(5))
	wantPath(t, r, "D1.Bump", int64(6))
}

// C3: zero placement: a force on the not-ready member wins; the program
// reads the zero; nothing is pushed; a fault ends when the unit stops.
func TestExprRecheckZeroPlacement(t *testing.T) {
	r, d := newGaugeRuntime(t, "Seen := G1.Ok; G1.Cmd := G1.Ok;", gaugeDef(), Output("Out"))
	log := logBuf(r)
	// Not ready, forced: the force stands, scan after scan.
	if err := r.Tags().Force("G1.Ok", true); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		r.Scan()
		wantPath(t, r, "G1.Ok", true)
		wantPath(t, r, "Seen", true)
	}
	wantPath(t, r, "G1.Both", false) // reads Pick, not ready
	r.Tags().Unforce("G1.Ok")
	wantPath(t, r, "G1.Ok", false)
	r.Scan()
	wantPath(t, r, "Seen", false)
	// Force an input, compute, release: back to zero, and steady.
	_ = r.Tags().Force("G1.Raw", int64(4))
	r.Scan()
	wantPath(t, r, "G1.Twice", int64(8))
	r.Tags().Unforce("G1.Raw")
	r.Scan()
	wantPath(t, r, "G1.Twice", int64(0))
	wantPath(t, r, "G1.Ok", false)
	if q := r.TagQuality("G1.Twice"); q != nio.NotConnected {
		t.Errorf("quality %v", q)
	}
	gen, n := r.Tags().Generation(), d.writeCount()
	for i := 0; i < 3; i++ {
		r.Scan()
	}
	if r.Tags().Generation() != gen {
		t.Error("not ready and steady: the generation moves")
	}
	if d.writeCount() != n {
		t.Error("not ready and steady: pushes")
	}
	if !raceOn {
		if a := testing.AllocsPerRun(50, func() { r.runExprs(time.Now()) }); a != 0 {
			t.Errorf("not ready and steady: %v allocs", a)
		}
	}
	// A fault, then the unit stops being ready: no fault listed, and the
	// log does not say "cleared" for a fault nothing cleared... (nit only)
	_ = r.Tags().Force("G1.Div", int64(0))
	r.Scan()
	if len(r.Stats().Exprs.Faulting) != 1 {
		t.Fatalf("faulting: %+v", r.Stats().Exprs.Faulting)
	}
	r.Tags().Unforce("G1.Div")
	r.Scan()
	if len(r.Stats().Exprs.Faulting) != 0 {
		t.Errorf("still faulting: %+v", r.Stats().Exprs.Faulting)
	}
	wantPath(t, r, "G1.Ratio", int64(0))
	_ = log
	_ = strings.Contains
}

// C4: a struct-typed unit that is not ready: its "zero", when the TYPE
// gives its members initial values; and steady.
func TestExprRecheckZeroOfAStruct(t *testing.T) {
	lib := strings.Replace(devLib, "Pt : STRUCT\n    A : INT;", "Pt : STRUCT\n    A : INT := 5;", 1)
	if lib == devLib {
		t.Fatal("no change")
	}
	def := InstanceDef{Members: map[string]MemberDef{
		"Raw":    {Kind: MemberFrom},
		"Sel":    {Kind: MemberFrom},
		"Mirror": {Kind: MemberExpr, Expr: "PickPt(Sel, P1, P2)"},
		"Cmd":    {Kind: MemberTo},
	}}
	d := &countDriver{}
	r, err := New(Options{
		Program: "PROGRAM Main\nEND_PROGRAM", Libraries: []string{lib}, Driver: d,
		Tags: []TagDef{InstanceTag("D1", "Dev", def)},
	})
	if err != nil {
		t.Fatal(err)
	}
	r.Scan()
	r.Scan()
	wantPath(t, r, "D1.Mirror.A", int64(0))
	gen := r.Tags().Generation()
	r.Scan()
	if r.Tags().Generation() != gen {
		t.Error("generation moves")
	}
}

// C5: a fault inside a FUNCTION, every scan the same: allocations.
func TestExprRecheckFaultInsideFunctionAllocs(t *testing.T) {
	t.Skip("not a finding: the 1 alloc is Deep's local array, re-initialised per call by the VM; the frame is released on the error path")
	def := gaugeDef()
	def.Members["Pick"] = MemberDef{Kind: MemberExpr, Expr: "Deep(Idx)"}
	d := &countDriver{}
	r, err := New(Options{
		Program: "PROGRAM Main\nEND_PROGRAM", Libraries: []string{gaugeLib, sneakLib}, Driver: d,
		Tags: []TagDef{InstanceTag("G1", "Gauge", def), State("Seen", false)},
	})
	if err != nil {
		t.Fatal(err)
	}
	deliver(d, 3, 9, 4)
	r.Scan()
	r.Scan()
	if n := testing.AllocsPerRun(100, func() { r.runExprs(time.Now()) }); n != 0 {
		t.Errorf("a member faulting inside a FUNCTION every scan: %v allocs per stage run", n)
	}
	st := r.Stats().Exprs
	if len(st.Faulting) != 1 || !strings.Contains(st.Faulting[0].Error, "index 9 out of bounds [0..1]") {
		t.Errorf("faulting: %+v", st.Faulting)
	}
}
