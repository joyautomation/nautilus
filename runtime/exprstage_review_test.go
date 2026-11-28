package runtime

import (
	"strings"
	"sync"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
)

var _ = ir.CopyValue
var _ = time.Second
var _ sync.Mutex
var _ = nio.Good

const devLib = `
TYPE
  Pt : STRUCT
    A : INT;
    B : INT;
  END_STRUCT;
  Dev : STRUCT
    Raw    : INT;
    Sel    : BOOL;
    P1     : Pt;
    P2     : Pt;
    Mirror : Pt;
    SumM   : INT;
    Arr    : ARRAY[0..2] OF INT;
    ArrC   : ARRAY[0..2] OF INT;
    Bump   : INT;
    Cmd    : BOOL;
    Note   : REAL;
  END_STRUCT;
END_TYPE
FUNCTION PickPt : Pt
VAR_INPUT s : BOOL; a : Pt; b : Pt; END_VAR
IF s THEN PickPt := b; ELSE PickPt := a; END_IF;
END_FUNCTION
FUNCTION BumpArr : INT
VAR_INPUT a : ARRAY[0..2] OF INT; END_VAR
a[0] := a[0] + 100;
BumpArr := a[0];
END_FUNCTION
FUNCTION BumpPt : INT
VAR_INPUT p : Pt; END_VAR
p.A := p.A + 100;
BumpPt := p.A;
END_FUNCTION
`

func devDef() InstanceDef {
	return InstanceDef{Members: map[string]MemberDef{
		"Raw":    {Kind: MemberFrom},
		"Sel":    {Kind: MemberFrom},
		"Mirror": {Kind: MemberExpr, Expr: "PickPt(Sel, P1, P2)"},
		"SumM":   {Kind: MemberExpr, Expr: "Mirror.A + Mirror.B"},
		"ArrC":   {Kind: MemberExpr, Expr: "Arr"},
		"Bump":   {Kind: MemberExpr, Expr: "BumpArr(Arr) + BumpPt(P1)"},
		"Cmd":    {Kind: MemberTo},
	}}
}

func newDevRuntime(t testing.TB, body string, def InstanceDef, extra ...TagDef) (*Runtime, *countDriver) {
	t.Helper()
	d := &countDriver{}
	tags := append([]TagDef{InstanceTag("D1", "Dev", def), State("Seen", false)}, extra...)
	r, err := New(Options{
		Program:   "PROGRAM Main\nVAR_EXTERNAL D1 : Dev; Seen : BOOL; END_VAR\n" + body + "\nEND_PROGRAM",
		Libraries: []string{devLib},
		Driver:    d,
		Tags:      tags,
	})
	if err != nil {
		t.Fatal(err)
	}
	return r, d
}

// R1: SwapWarm, then Swap, then Rollback.
func TestExprReviewRollbackAfterSwap(t *testing.T) {
	r, d := newGaugeRuntime(t, "", gaugeDef())
	deliver(d, 3, 0, 1)
	r.Scan()
	src := r.prog.Source()
	triple := strings.Replace(src, "Double := x * 2;", "Double := x * 3;", 1)
	quad := strings.Replace(src, "Double := x * 2;", "Double := x * 4;", 1)
	if _, err := r.prog.SwapWarm(triple); err != nil {
		t.Fatal(err)
	}
	if err := r.prog.Swap(quad); err != nil {
		t.Fatal(err)
	}
	r.Scan()
	wantPath(t, r, "G1.Twice", int64(12))
	if _, err := r.prog.Rollback(); err != nil {
		t.Logf("rollback refused: %v", err)
		return
	}
	d.set("G1.Raw", int64(5))
	r.Scan()
	t.Logf("source after rollback has x*2: %v", strings.Contains(r.prog.Source(), "x * 2"))
	if r.prog.ex.stage == nil {
		t.Error("after SwapWarm, Swap, Rollback the expression stage is nil: no expr: member is evaluated any more")
	}
	wantPath(t, r, "G1.Twice", int64(10))
}

// R2: struct-typed and array-typed expr members: steady state moves nothing.
func TestExprReviewAggregateIdempotent(t *testing.T) {
	r, d := newDevRuntime(t, "", devDef())
	d.set("D1.Raw", int64(1))
	d.set("D1.Sel", false)
	write(t, r, "D1.P1.A", 1)
	write(t, r, "D1.P1.B", 2)
	write(t, r, "D1.P2.A", 10)
	write(t, r, "D1.P2.B", 20)
	write(t, r, "D1.Arr[0]", 7)
	r.Scan()
	r.Scan()
	wantPath(t, r, "D1.Mirror.A", int64(1))
	wantPath(t, r, "D1.SumM", int64(3))
	wantPath(t, r, "D1.ArrC[0]", int64(7))
	wantPath(t, r, "D1.Bump", int64(208))
	wantPath(t, r, "D1.Arr[0]", int64(7))
	wantPath(t, r, "D1.P1.A", int64(1))
	gen := r.Tags().Generation()
	r.Scan()
	r.Scan()
	if g := r.Tags().Generation(); g != gen {
		t.Errorf("steady inputs: the store generation moved %d -> %d", gen, g)
	}
	// Aggregate members allocate in the VM, which copies them by value (the
	// stage's doc): the stage adds nothing over what the VM costs alone.
	if raceOn {
		return
	}
	s := r.prog.ex.stage
	vm := 0.0
	for i := range r.prog.ex.units {
		vm += testing.AllocsPerRun(50, func() { _ = ir.Run(s.units[i], s.frame, s.host) })
	}
	if n := testing.AllocsPerRun(50, func() { r.runExprs(time.Now()) }); n != vm {
		t.Errorf("steady stage with aggregate members: %v allocs per run, the VM alone %v", n, vm)
	}
}

// R3: aliasing: a struct result that came from one tag, then from another.
func TestExprReviewAggregateAliasing(t *testing.T) {
	r, d := newDevRuntime(t, "", devDef())
	d.set("D1.Raw", int64(1))
	d.set("D1.Sel", false)
	write(t, r, "D1.P1.A", 1)
	write(t, r, "D1.P1.B", 2)
	write(t, r, "D1.P2.A", 10)
	write(t, r, "D1.P2.B", 20)
	write(t, r, "D1.Arr[0]", 7)
	r.Scan()
	held, _ := r.Tags().ReadGlobal("D1")
	heldCopy := ir.CopyValue(held)
	for i := 0; i < 4; i++ {
		d.set("D1.Sel", i%2 == 0)
		write(t, r, "D1.Arr[1]", i+1)
		r.Scan()
		wantPath(t, r, "D1.P1.A", int64(1))
		wantPath(t, r, "D1.P1.B", int64(2))
		wantPath(t, r, "D1.P2.A", int64(10))
		wantPath(t, r, "D1.P2.B", int64(20))
		wantPath(t, r, "D1.Arr[0]", int64(7))
		wantPath(t, r, "D1.ArrC[1]", int64(i+1))
		if i%2 == 0 {
			wantPath(t, r, "D1.Mirror.A", int64(10))
			wantPath(t, r, "D1.SumM", int64(30))
		} else {
			wantPath(t, r, "D1.Mirror.A", int64(1))
			wantPath(t, r, "D1.SumM", int64(3))
		}
		if !sameValue(&held, &heldCopy) {
			t.Fatalf("scan %d: a value a reader held changed under it: %v", i, plain(held))
		}
	}
}

// R4: a force below a struct expr member: the dependent and the program.
func TestExprReviewForceBelowUnit(t *testing.T) {
	r, d := newDevRuntime(t, "Seen := D1.Mirror.A = 99;", devDef())
	d.set("D1.Raw", int64(1))
	d.set("D1.Sel", false)
	write(t, r, "D1.P1.A", 1)
	write(t, r, "D1.P1.B", 2)
	r.Scan()
	wantPath(t, r, "D1.SumM", int64(3))
	if err := r.Tags().Force("D1.Mirror.A", int64(99)); err != nil {
		t.Fatal(err)
	}
	r.Scan()
	wantPath(t, r, "D1.Mirror.A", int64(99))
	wantPath(t, r, "Seen", true)          // the program sees the force
	wantPath(t, r, "D1.SumM", int64(101)) // a dependent must too (rule 4)
	gen := r.Tags().Generation()
	r.Scan()
	if g := r.Tags().Generation(); g != gen {
		t.Errorf("generation moved under a sub-member force with steady inputs")
	}
	r.Tags().Unforce("D1.Mirror.A")
	r.Scan()
	wantPath(t, r, "D1.Mirror.A", int64(1))
	wantPath(t, r, "D1.SumM", int64(3))
}

// R5: force then unforce an input before it is ever delivered.
func TestExprReviewUnforceBeforeDelivery(t *testing.T) {
	r, _ := newGaugeRuntime(t, "Seen := G1.Ok;", gaugeDef())
	if err := r.Tags().Force("G1.Raw", int64(4)); err != nil {
		t.Fatal(err)
	}
	r.Scan()
	wantPath(t, r, "G1.Ok", true)
	r.Tags().Unforce("G1.Raw")
	r.Scan()
	r.Scan()
	t.Logf("quality G1.Ok after unforce = %v; Quality()=%v", r.TagQuality("G1.Ok"), r.Quality())
	wantPath(t, r, "G1.Ok", false) // rule 5: not computed -> FALSE
	wantPath(t, r, "Seen", false)
	wantPath(t, r, "G1.Twice", int64(0))
}

// R5b: force the expr itself before its inputs, then unforce.
func TestExprReviewUnforceExprBeforeDelivery(t *testing.T) {
	r, _ := newGaugeRuntime(t, "", gaugeDef())
	r.Scan()
	if q := r.TagQuality("G1.Ok"); q != nio.NotConnected {
		t.Fatalf("before: %v", q)
	}
	if err := r.Tags().Force("G1.Ok", true); err != nil {
		t.Fatal(err)
	}
	r.Scan()
	r.Tags().Unforce("G1.Ok")
	r.Scan()
	wantPath(t, r, "G1.Ok", false)
	// Ratio reads only Div... use Ok which reads Raw (not delivered)
	t.Logf("TagQuality(G1.Ok)=%v Quality()[G1.Ok]=%v", r.TagQuality("G1.Ok"), r.Quality()["G1.Ok"])
}

// R6: a faulting member, then forced.
func TestExprReviewFaultThenForce(t *testing.T) {
	r, d := newGaugeRuntime(t, "", gaugeDef())
	deliver(d, 1, 0, 0)
	r.Scan()
	if q := r.TagQuality("G1.Ratio"); q != nio.Bad {
		t.Fatalf("faulting: %v", q)
	}
	if err := r.Tags().Force("G1.Ratio", int64(7)); err != nil {
		t.Fatal(err)
	}
	d.set("G1.Div", int64(5))
	r.Scan()
	r.Scan()
	wantPath(t, r, "G1.Ratio", int64(7))
	if q := r.TagQuality("G1.Ratio"); q == nio.Bad {
		t.Errorf("a forced member whose inputs are fine is still Bad from a fault before the force")
	}
	if st := r.Stats().Exprs; len(st.Faulting) != 0 {
		t.Errorf("Stats lists a fault for a member that is forced and not evaluated: %+v", st.Faulting)
	}
}
