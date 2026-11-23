package ir_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/joyautomation/nautilus/lang/ir"
)

// imageSrc exercises every shape a frame holds: elementary slots of each
// kind, an enumeration, a STRING, a STRUCT, an ARRAY of STRUCT, a built-in
// timer and counter, a user FUNCTION_BLOCK with a timer inside, and an
// ARRAY OF TON.
const imageSrc = `TYPE Mode : (Idle, Run := 10, Fault); END_TYPE
TYPE Pt : STRUCT x : REAL; y : INT; tag : STRING; END_STRUCT; END_TYPE

FUNCTION_BLOCK Stepper
VAR_INPUT go : BOOL; END_VAR
VAR_OUTPUT step : INT; END_VAR
VAR t : TON; END_VAR
t(IN := go, PT := T#50ms);
IF t.Q THEN step := step + 1; END_IF;
END_FUNCTION_BLOCK

PROGRAM Main
VAR_EXTERNAL Run : BOOL; END_VAR
VAR
    n : INT;
    r : REAL;
    b : BOOL;
    d : TIME;
    s : STRING;
    m : Mode;
    p : Pt;
    pts : ARRAY[1..3] OF Pt;
    tmr : TON;
    cnt : CTU;
    st : Stepper;
    tons : ARRAY[0..1] OF TON;
END_VAR
n := n + 1;
r := r + 0.5;
b := NOT b;
d := d + T#10ms;
s := 'n';
IF n > 2 THEN m := Mode#Run; END_IF;
p.x := r; p.y := n; p.tag := 'pt';
pts[2].y := n * 2;
tmr(IN := Run, PT := T#1s);
cnt(CU := b, R := FALSE, PV := 100);
st(go := Run);
tons[1](IN := Run, PT := T#2s);
END_PROGRAM`

type imgHost struct {
	g   map[string]ir.Value
	now int64
}

func (h *imgHost) ReadGlobal(n string) (ir.Value, error)  { return h.g[n], nil }
func (h *imgHost) WriteGlobal(n string, v ir.Value) error { h.g[n] = v; return nil }
func (h *imgHost) NowMs() int64                           { return h.now }

// runImage runs the program a few scans so every kind of slot holds a value
// other than its initial one.
func runImage(t *testing.T, p *ir.Program, scans int) *ir.Frame {
	t.Helper()
	f := ir.NewFrame(p)
	h := &imgHost{g: map[string]ir.Value{"Run": ir.BoolVal(true)}, now: 1_000_000}
	for range scans {
		h.now += 40
		if err := ir.Run(p, f, h); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

// A frame flattened and applied to a fresh frame of the same program is
// equal to it, slot for slot, nested FB instances and arrays included.
func TestImageLeavesRoundTrip(t *testing.T) {
	p := lowerOne(t, imageSrc)
	f := runImage(t, p, 7)
	leaves := ir.FlattenInto(nil, f)
	if len(leaves) == 0 {
		t.Fatal("no leaves")
	}
	g := ir.NewFrame(p)
	if reflect.DeepEqual(plainFrame(f), plainFrame(g)) {
		t.Fatal("the run left every slot at its initial value: the test proves nothing")
	}
	if err := ir.ApplyLeaves(g, leaves); err != nil {
		t.Fatal(err)
	}
	if a, b := plainFrame(f), plainFrame(g); !reflect.DeepEqual(a, b) {
		t.Fatalf("applied frame differs:\n got %v\nwant %v", b, a)
	}
	if got := ir.FlattenInto(nil, g); !reflect.DeepEqual(got, leaves) {
		t.Fatal("a second flatten differs from the first")
	}
}

// The copy holds no storage of the frame: the frame running on does not
// change a flattened image.
func TestImageLeavesAreACopy(t *testing.T) {
	p := lowerOne(t, imageSrc)
	f := runImage(t, p, 3)
	leaves := ir.FlattenInto(nil, f)
	keep := append([]ir.Leaf(nil), leaves...)
	h := &imgHost{g: map[string]ir.Value{"Run": ir.BoolVal(false)}, now: 2_000_000}
	for range 3 {
		if err := ir.Run(p, f, h); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(leaves, keep) {
		t.Fatal("running the frame changed the flattened image")
	}
}

// Leaves of another count or another kind are refused.
func TestImageLeavesMismatchRefused(t *testing.T) {
	p := lowerOne(t, imageSrc)
	leaves := ir.FlattenInto(nil, runImage(t, p, 2))
	for name, bad := range map[string][]ir.Leaf{
		"short": leaves[:len(leaves)-1],
		"long":  append(append([]ir.Leaf(nil), leaves...), ir.Leaf{Kind: ir.TypeInt, Bits: 1}),
		"kind":  append([]ir.Leaf{{Kind: ir.TypeString, S: "x"}}, leaves[1:]...),
	} {
		if err := ir.ApplyLeaves(ir.NewFrame(p), bad); !errors.Is(err, ir.ErrLeaves) {
			t.Errorf("%s: got %v, want ErrLeaves", name, err)
		}
	}
}

// After the first call, flattening into the same buffer allocates nothing:
// a scan can take it at its boundary.
func TestImageFlattenAllocFree(t *testing.T) {
	p := lowerOne(t, imageSrc)
	f := runImage(t, p, 2)
	buf := ir.FlattenInto(nil, f)
	if n := testing.AllocsPerRun(100, func() { buf = ir.FlattenInto(buf, f) }); n != 0 {
		t.Fatalf("FlattenInto allocates %v per call", n)
	}
}

// The layout follows declarations, not bodies: a body edit keeps it, and a
// new variable, a changed type, a changed array bound, a changed member of
// a STRUCT, an enumeration or an FB's own declarations change it.
func TestImageLayout(t *testing.T) {
	base := lowerOne(t, imageSrc).Layout()
	if base == "" || base != lowerOne(t, imageSrc).Layout() {
		t.Fatalf("layout not stable: %q", base)
	}
	same := map[string][2]string{
		"body only": {"n := n + 1;", "n := n + 2;"},
	}
	for name, ed := range same {
		if got := lowerOne(t, replaceOnce(t, imageSrc, ed[0], ed[1])).Layout(); got != base {
			t.Errorf("%s changed the layout", name)
		}
	}
	differ := map[string][2]string{
		"new var":      {"    n : INT;", "    n : INT;\n    extra : INT;"},
		"int width":    {"y : INT;", "y : DINT;"},
		"fb type":      {"tmr : TON;", "tmr : TOF;"},
		"array bound":  {"ARRAY[1..3] OF Pt", "ARRAY[1..4] OF Pt"},
		"array lo":     {"ARRAY[1..3] OF Pt", "ARRAY[0..2] OF Pt"},
		"struct field": {"tag : STRING;", "tag : STRING; z : BOOL;"},
		"enum member":  {"Fault);", "Fault, Hold);"},
		"fb internal":  {"VAR t : TON; END_VAR", "VAR t : TON; u : TOF; END_VAR"},
		"rename":       {"    b : BOOL;", "    bb : BOOL;"},
		"real width":   {"x : REAL;", "x : LREAL;"},
		"retain":       {"    tons : ARRAY[0..1] OF TON;\nEND_VAR", "END_VAR\nVAR RETAIN tons : ARRAY[0..1] OF TON; END_VAR"},
	}
	for name, ed := range differ {
		src := replaceOnce(t, imageSrc, ed[0], ed[1])
		if name == "rename" {
			src = replaceOnce(t, src, "b := NOT b;", "bb := NOT bb;")
			src = replaceOnce(t, src, "CU := b,", "CU := bb,")
		}
		if got := lowerOne(t, src).Layout(); got == base {
			t.Errorf("%s kept the layout", name)
		}
	}
}

func replaceOnce(t *testing.T, s, old, new string) string {
	t.Helper()
	i := indexOf(s, old)
	if i < 0 {
		t.Fatalf("%q not in source", old)
	}
	return s[:i] + new + s[i+len(old):]
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// plainFrame renders a frame for comparison without the pointers (StructDef,
// FBDef, the FB's cached step frame) that differ between two frames.
func plainFrame(f *ir.Frame) []any {
	out := make([]any, len(f.Slots))
	for i := range f.Slots {
		out[i] = plainV(f.Slots[i])
	}
	return out
}

func plainV(v ir.Value) any {
	switch v.Kind {
	case ir.TypeArray:
		a := make([]any, len(v.Arr))
		for i, e := range v.Arr {
			a[i] = plainV(e)
		}
		return a
	case ir.TypeStruct:
		a := make([]any, len(v.Fld))
		for i, e := range v.Fld {
			a[i] = plainV(e)
		}
		return a
	case ir.TypeFB:
		if v.FB == nil {
			return nil
		}
		a := make([]any, len(v.FB.Slots))
		for i, e := range v.FB.Slots {
			a[i] = plainV(e)
		}
		return a
	}
	return [5]any{v.Kind, v.I, v.F, v.B, v.S}
}
