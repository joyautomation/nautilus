package runtime

import (
	"strings"
	"sync"
	"testing"

	nio "github.com/joyautomation/nautilus/io"
)

// R7: an online edit reorders the instance's TYPE.
func TestExprReviewTypeReorderEdit(t *testing.T) {
	r, d := newGaugeRuntime(t, "", gaugeDef())
	deliver(d, 3, 0, 1)
	r.Scan()
	wantPath(t, r, "G1.Twice", int64(6))
	src := r.prog.Source()
	// Swap Twice and Ratio in the TYPE (both INT), and move Cmd before Ok.
	re := strings.Replace(src, "    Ratio : INT;\n    Ok    : BOOL;\n    Both  : BOOL;\n    Twice : INT;\n    Note  : REAL;\n    Cmd   : BOOL;\n",
		"    Twice : INT;\n    Cmd   : BOOL;\n    Both  : BOOL;\n    Ratio : INT;\n    Note  : REAL;\n    Ok    : BOOL;\n", 1)
	if re == src {
		t.Fatal("no change made")
	}
	_, err := r.prog.SwapWarm(re)
	t.Logf("SwapWarm of a reordered TYPE: err=%v", err)
	if err != nil {
		return
	}
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

// R8: an LD main program beside expr members.
func TestExprReviewLDMain(t *testing.T) {
	d := &countDriver{}
	main := gaugeLib + `PROGRAM Main
VAR_EXTERNAL G1 : Gauge; Seen : BOOL; END_VAR
LD
  RUNG a  G1.Ok ( Seen )
END_LD
END_PROGRAM`
	r, err := New(Options{Program: main, Driver: d, Tags: []TagDef{InstanceTag("G1", "Gauge", gaugeDef()), State("Seen", false)}})
	if err != nil {
		t.Fatal(err)
	}
	deliver(d, 3, 0, 1)
	r.Scan()
	wantPath(t, r, "G1.Twice", int64(6))
	wantPath(t, r, "Seen", true)
	if _, err := r.prog.SwapWarm(strings.Replace(main, "x * 2", "x * 3", 1)); err != nil {
		t.Fatal(err)
	}
	r.Scan()
	wantPath(t, r, "G1.Twice", int64(9))
}

// R9: spellings in another case: order, gate, cycle.
func TestExprReviewCase(t *testing.T) {
	def := gaugeDef()
	def.Members["Both"] = MemberDef{Kind: MemberExpr, Expr: "NOT g1.OK"}
	def.Members["Ok"] = MemberDef{Kind: MemberExpr, Expr: "NOT (RAW > 0)"}
	r, d := newGaugeRuntime(t, "", def)
	r.Scan()
	wantPath(t, r, "G1.Both", false)
	wantPath(t, r, "G1.Ok", false)
	d.set("G1.Raw", int64(1))
	r.Scan()
	wantPath(t, r, "G1.Ok", false)
	wantPath(t, r, "G1.Both", true)
	d.set("G1.Raw", int64(0))
	r.Scan()
	wantPath(t, r, "G1.Ok", true)
	wantPath(t, r, "G1.Both", false) // this scan's Ok
	def2 := gaugeDef()
	def2.Members["Ok"] = MemberDef{Kind: MemberExpr, Expr: "g1.BOTH"}
	_, err := New(Options{Program: "PROGRAM Main\nEND_PROGRAM", Libraries: []string{gaugeLib}, Driver: &countDriver{}, Tags: []TagDef{InstanceTag("G1", "Gauge", def2)}})
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Errorf("a cycle in another case: %v", err)
	}
	// the instance declared in another case than the program and force use
	def3 := gaugeDef()
	dd := &countDriver{}
	r3, err := New(Options{Program: "PROGRAM Main\nVAR_EXTERNAL G1 : Gauge; Seen : BOOL; END_VAR\nSeen := G1.Ok;\nEND_PROGRAM", Libraries: []string{gaugeLib}, Driver: dd, Tags: []TagDef{InstanceTag("g1", "Gauge", def3), State("Seen", false)}})
	if err != nil {
		t.Fatal(err)
	}
	dd.set("g1.Raw", int64(0))
	dd.set("g1.Idx", int64(0))
	dd.set("g1.Div", int64(1))
	r3.Scan()
	wantPath(t, r3, "g1.Ok", false)
	if err := r3.Tags().Force("G1.OK", true); err != nil {
		t.Fatal(err)
	}
	r3.Scan()
	r3.Scan()
	wantPath(t, r3, "g1.Ok", true)
	wantPath(t, r3, "Seen", true)
	r3.Tags().Unforce("G1.OK")
	wantPath(t, r3, "g1.Ok", false) // the actual never took the force
}

// R10: a variable index into an array one of whose elements is an expr; a
// nested expr leaf under a memory struct; a program assigning the struct.
func TestExprReviewNestedTargets(t *testing.T) {
	def := InstanceDef{Members: map[string]MemberDef{
		"Raw":    {Kind: MemberFrom},
		"Sel":    {Kind: MemberFrom},
		"Arr[2]": {Kind: MemberExpr, Expr: "Raw + 1"},
		"P2.B":   {Kind: MemberExpr, Expr: "Raw * 2"},
		"SumM":   {Kind: MemberExpr, Expr: "Arr[Raw] + P2.B"},
		"Bump":   {Kind: MemberExpr, Expr: "BumpPt(P2)"},
		"Cmd":    {Kind: MemberTo},
	}}
	r, d := newDevRuntime(t, "D1.P2.A := 5; D1.P2.B := 77; D1.Arr[2] := 55; D1.Arr[1] := 4; Seen := D1.SumM = 7;", def)
	d.set("D1.Raw", int64(2))
	d.set("D1.Sel", false)
	r.Scan()
	wantPath(t, r, "D1.Arr[2]", int64(3))
	wantPath(t, r, "D1.P2.B", int64(4))
	wantPath(t, r, "D1.P2.A", int64(5))
	wantPath(t, r, "D1.SumM", int64(7))
	wantPath(t, r, "Seen", true)
	r.Scan()
	wantPath(t, r, "D1.Bump", int64(105))
	wantPath(t, r, "D1.P2.A", int64(5))
	gen := r.Tags().Generation()
	r.Scan()
	if r.Tags().Generation() != gen {
		t.Error("generation moves in the steady state")
	}
	d.set("D1.Raw", int64(1))
	r.Scan()
	wantPath(t, r, "D1.Arr[2]", int64(2))
	wantPath(t, r, "D1.SumM", int64(6))
	for _, p := range []string{"D1.P2.B", "D1.Arr[2]"} {
		errs := r.Tags().Write([]WriteOp{{Path: p, Value: 9}})
		if errs[0] == nil {
			t.Errorf("an API write to the expr member %s was taken", p)
		}
	}
}

// R11: Quality and Stats from other goroutines while scanning (-race), and
// the two quality paths agree.
func TestExprReviewQualityRaceAndAgreement(t *testing.T) {
	r, d := newGaugeRuntime(t, "Seen := G1.Ok;", gaugeDef())
	deliver(d, 1, 0, 1)
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for g := 0; g < 3; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = r.Quality()
				_ = r.TagQuality("G1.Both")
				_ = r.TagQuality("G1")
				_ = r.Stats()
				_ = r.prog.Check(r.prog.Source())
				_, _ = r.Tags().ReadPath("G1")
				_ = r.Tags().Write([]WriteOp{{Path: "G1.Note", Value: 1.5}})
			}
		}()
	}
	for i := 0; i < 300; i++ {
		d.set("G1.Raw", int64(i%3))
		d.set("G1.Div", int64(i%2))
		d.set("G1.Idx", int64(i%6))
		if i%50 == 0 {
			_ = r.Tags().Force("G1.Ok", true)
		}
		if i%50 == 25 {
			r.Tags().Unforce("G1.Ok")
		}
		r.Scan()
	}
	close(stop)
	wg.Wait()
	agree := func(when string) {
		t.Helper()
		all := r.Quality()
		for _, m := range []string{"G1", "G1.Ok", "G1.Both", "G1.Pick", "G1.Ratio", "G1.Twice", "G1.Raw"} {
			a, ok := all[m]
			if !ok {
				a = nio.Good
			}
			if b := r.TagQuality(m); a != b {
				t.Errorf("%s: Quality()[%s]=%v but TagQuality=%v", when, m, a, b)
			}
		}
	}
	r.Tags().Unforce("G1.Ok")
	deliver(d, 1, 0, 1)
	r.Scan()
	agree("good")
	d.setQ("G1.Raw", nio.Stale)
	agree("stale")
	d.setQ("G1.Raw", nio.Bad)
	agree("bad")
	d.setQ("G1.Raw", nio.Good)
	d.set("G1.Div", int64(0))
	r.Scan()
	agree("fault")
	_ = r.Tags().Force("G1.Ok", true)
	r.Scan()
	agree("forced")
	d.mu.Lock()
	d.fail = true
	d.mu.Unlock()
	r.Scan()
	agree("read failing")
}

// R12: an instance whose Init gives an expr member a value.
func TestExprReviewInitOnExpr(t *testing.T) {
	d := &countDriver{}
	r, err := New(Options{
		Program: "PROGRAM Main\nEND_PROGRAM", Libraries: []string{gaugeLib}, Driver: d,
		Tags: []TagDef{InstanceTag("G1", "Gauge", gaugeDef(), Init(map[string]any{"Ok": true, "Twice": 9}))},
	})
	if err != nil {
		t.Logf("New: %v", err)
		return
	}
	r.Scan()
	wantPath(t, r, "G1.Ok", false)
	wantPath(t, r, "G1.Twice", int64(0))
}
