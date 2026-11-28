package runtime

import (
	"strings"
	"testing"

	nio "github.com/joyautomation/nautilus/io"
)

// R13: a TYPE edit (reorder) with no VAR_EXTERNAL naming the instance.
func TestExprReviewTypeReorderEditImplicit(t *testing.T) {
	d := &countDriver{}
	r, err := New(Options{Program: "PROGRAM Main\nEND_PROGRAM", Libraries: []string{gaugeLib}, Driver: d, Tags: []TagDef{InstanceTag("G1", "Gauge", gaugeDef())}})
	if err != nil {
		t.Fatal(err)
	}
	deliver(d, 3, 0, 1)
	r.Scan()
	src := r.prog.Source()
	re := strings.Replace(src, "    Ratio : INT;\n    Ok    : BOOL;\n    Both  : BOOL;\n    Twice : INT;\n    Note  : REAL;\n    Cmd   : BOOL;\n",
		"    Twice : INT;\n    Cmd   : BOOL;\n    Both  : BOOL;\n    Ratio : INT;\n    Note  : REAL;\n    Ok    : BOOL;\n", 1)
	if re == src {
		t.Fatal("no change")
	}
	_, err = r.prog.SwapWarm(re)
	t.Logf("SwapWarm: %v", err)
	d.set("G1.Raw", int64(0))
	d.set("G1.Div", int64(4))
	r.Scan()
	r.Scan()
	t.Logf("G1 = %v", readPath(t, r, "G1"))
	wantPath(t, r, "G1.Twice", int64(0))
	wantPath(t, r, "G1.Ratio", int64(25))
	wantPath(t, r, "G1.Ok", false)
	wantPath(t, r, "G1.Cmd", false)
}

// R14: the TYPE gives an expr member an initial value.
func TestExprReviewTypeInitialOnExpr(t *testing.T) {
	lib := strings.Replace(gaugeLib, "Ok    : BOOL;", "Ok    : BOOL := TRUE;", 1)
	d := &countDriver{}
	r, err := New(Options{
		Program: "PROGRAM Main\nVAR_EXTERNAL Seen : BOOL; END_VAR\nSeen := G1.Ok;\nEND_PROGRAM", Libraries: []string{lib}, Driver: d,
		Tags: []TagDef{InstanceTag("G1", "Gauge", gaugeDef()), State("Seen", false)},
	})
	if err != nil {
		t.Fatal(err)
	}
	r.Scan() // nothing delivered: Ok ("Raw > 0") is not computed
	if q := r.TagQuality("G1.Ok"); q != nio.NotConnected {
		t.Errorf("quality %v", q)
	}
	wantPath(t, r, "G1.Ok", false)
	wantPath(t, r, "Seen", false)
}

// R15: an expression changing every scan beside a to: member the program
// holds: nothing more is pushed.
func TestExprReviewNoPushWithWrittenCommand(t *testing.T) {
	r, d := newGaugeRuntime(t, "G1.Cmd := TRUE;", gaugeDef(), Output("Out"))
	deliver(d, 1, 0, 1)
	r.Scan()
	r.Scan()
	r.Scan()
	n := d.writeCount()
	for i := int64(2); i < 8; i++ {
		d.set("G1.Raw", i)
		r.Scan()
	}
	if got := d.writeCount(); got != n {
		t.Errorf("%d pushes while only an expression changed", got-n)
	}
}

// R16: the hot path: a standby does not evaluate and is Stale; the image
// brings no expression value; the new leader computes on its first scan.
func TestExprReviewHotTakeover(t *testing.T) {
	def := InstanceDef{Members: map[string]MemberDef{
		"Ok":    {Kind: MemberExpr, Expr: "Note > 1.0"},
		"Twice": {Kind: MemberExpr, Expr: "Double(Idx)"},
		"Cmd":   {Kind: MemberTo},
	}}
	r := newHotRig(t, hotMain, hotMain, func(o *Options) {
		o.Libraries = append(o.Libraries, gaugeLib)
		o.Tags = append(o.Tags, InstanceTag("G1", "Gauge", def))
	})
	write(t, r.lead, "G1.Note", 2.5)
	write(t, r.lead, "G1.Idx", 4)
	scanBoth(t, r.lead, r.lc, 1)
	wantPath(t, r.lead, "G1.Ok", true)
	wantPath(t, r.lead, "G1.Twice", int64(8))
	r.stream(t, 2)
	r.stby.Scan()
	wantPath(t, r.stby, "G1.Ok", false)
	if q := r.stby.TagQuality("G1.Ok"); q != nio.Stale {
		t.Errorf("standby TagQuality(G1.Ok) = %v, want Stale", q)
	}
	if q := r.stby.Quality()["G1.Ok"]; q != nio.Stale {
		t.Errorf("standby Quality()[G1.Ok] = %v, want Stale", q)
	}
	if q := r.stby.TagQuality("G1"); q != nio.Stale {
		t.Errorf("standby TagQuality(G1) = %v, want Stale", q)
	}
	if q := r.stby.Quality()["G1"]; q != nio.Stale {
		t.Errorf("standby Quality()[G1] = %v, want Stale", q)
	}
	rep := r.takeOver(t)
	t.Logf("takeover: %+v", rep)
	wantPath(t, r.stby, "G1.Note", 2.5)
	wantPath(t, r.stby, "G1.Ok", true)
	wantPath(t, r.stby, "G1.Twice", int64(8))
	if q := r.stby.TagQuality("G1.Ok"); q != nio.Good {
		t.Errorf("new leader TagQuality(G1.Ok) = %v", q)
	}
}
