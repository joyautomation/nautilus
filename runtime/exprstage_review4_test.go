package runtime

import (
	"testing"

	nio "github.com/joyautomation/nautilus/io"
)

const sneakLib = `
FUNCTION Deep : REAL
VAR_INPUT i : INT; END_VAR
VAR a : ARRAY[0..1] OF REAL; END_VAR
Deep := a[i];
END_FUNCTION
`

// R18: a fault inside a FUNCTION, then other units that call functions.
func TestExprReviewFaultInsideFunction(t *testing.T) {
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
	for i := 0; i < 3; i++ {
		r.Scan()
	}
	wantPath(t, r, "G1.Twice", int64(6))
	wantPath(t, r, "G1.Ratio", int64(25))
	if q := r.TagQuality("G1.Pick"); q != nio.Bad {
		t.Errorf("quality %v", q)
	}
	d.set("G1.Idx", int64(1))
	d.set("G1.Raw", int64(4))
	r.Scan()
	wantPath(t, r, "G1.Twice", int64(8))
	if q := r.TagQuality("G1.Pick"); q != nio.Good {
		t.Errorf("quality %v once clear", q)
	}
}

// R19: another instance's expression, spelled in another case, this scan.
func TestExprReviewCrossInstanceSameScan(t *testing.T) {
	a := gaugeDef()
	a.Members["Ok"] = MemberDef{Kind: MemberExpr, Expr: "NOT g2.OK"}
	d := &countDriver{}
	r, err := New(Options{
		Program: "PROGRAM Main\nEND_PROGRAM", Libraries: []string{gaugeLib}, Driver: d,
		Tags: []TagDef{InstanceTag("G1", "Gauge", a), InstanceTag("G2", "Gauge", gaugeDef())},
	})
	if err != nil {
		t.Fatal(err)
	}
	deliver(d, 0, 0, 1)
	d.set("G2.Idx", int64(0))
	d.set("G2.Div", int64(1))
	r.Scan()
	wantPath(t, r, "G1.Ok", false) // G2.Raw not delivered: G2.Ok not computed
	if q := r.TagQuality("G1.Ok"); q != nio.NotConnected {
		t.Errorf("quality %v", q)
	}
	for i := int64(0); i < 4; i++ {
		d.set("G2.Raw", i%2)
		r.Scan()
		wantPath(t, r, "G2.Ok", i%2 == 1)
		wantPath(t, r, "G1.Ok", i%2 == 0)
	}
	if err := r.Tags().Force("G2", map[string]any{"Ok": true}); err != nil {
		t.Logf("whole-instance force: %v", err)
		return
	}
	d.set("G2.Raw", int64(0))
	r.Scan()
	wantPath(t, r, "G2.Ok", true)
	wantPath(t, r, "G1.Ok", false)
	r.Tags().Unforce("G2")
}
