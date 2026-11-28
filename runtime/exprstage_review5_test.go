package runtime

import (
	"testing"
)

// R20: an INT expression into a REAL member: converted, and steady.
func TestExprReviewWidening(t *testing.T) {
	def := gaugeDef()
	def.Members["Pick"] = MemberDef{Kind: MemberExpr, Expr: "Raw"}
	def.Members["Note"] = MemberDef{Kind: MemberExpr, Expr: "1"}
	r, d := newGaugeRuntime(t, "", def)
	deliver(d, 3, 0, 1)
	r.Scan()
	r.Scan()
	wantPath(t, r, "G1.Pick", 3.0)
	wantPath(t, r, "G1.Note", 1.0)
	gen := r.Tags().Generation()
	r.Scan()
	if r.Tags().Generation() != gen {
		t.Error("generation moves in the steady state")
	}
}
