package runtime

import (
	"strings"
	"sync"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
)

// R22: substitutes on an instance with expr members.
func TestExprReviewSubstitutes(t *testing.T) {
	def := InstanceDef{Members: map[string]MemberDef{
		"Raw":  {Kind: MemberFrom},
		"Sel":  {Kind: MemberFrom},
		"P2.A": {Kind: MemberFrom},
		"P2.B": {Kind: MemberExpr, Expr: "Raw * 2"},
		"SumM": {Kind: MemberExpr, Expr: "P2.A + P2.B"},
		"Cmd":  {Kind: MemberTo},
	}}
	d := &countDriver{}
	r, err := New(Options{
		Program:   "PROGRAM Main\nVAR_EXTERNAL D1 : Dev; Got : INT; END_VAR\nGot := D1.P2.B;\nEND_PROGRAM",
		Libraries: []string{devLib}, Driver: d,
		Tags:          []TagDef{InstanceTag("D1", "Dev", def), State("Got", int64(0))},
		Substitutable: []string{"D1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	d.set("D1.Raw", int64(3))
	d.set("D1.Sel", false)
	d.set("D1.P2.A", int64(1))
	r.Scan()
	wantPath(t, r, "D1.SumM", int64(7))
	for _, addr := range []string{"D1", "D1.P2", "D1.P2.B", "D1.SumM"} {
		var v any = int64(50)
		if addr == "D1.P2" {
			v = map[string]any{"A": 40, "B": 50}
		}
		if addr == "D1" {
			v = map[string]any{"Raw": 9, "SumM": 50}
		}
		s, err := r.Substitute(addr, SubstituteRequest{Value: v, For: time.Hour, By: "op"})
		t.Logf("Substitute(%s): id=%d err=%v", addr, s.ID, err)
		if err != nil {
			continue
		}
		r.Scan()
		r.Scan()
		t.Logf("  D1.P2.A=%v D1.P2.B=%v D1.SumM=%v Got=%v Raw=%v", readPath(t, r, "D1.P2.A"), readPath(t, r, "D1.P2.B"), readPath(t, r, "D1.SumM"), readPath(t, r, "Got"), readPath(t, r, "D1.Raw"))
		b, sum, a := readPath(t, r, "D1.P2.B").(int64), readPath(t, r, "D1.SumM").(int64), readPath(t, r, "D1.P2.A").(int64)
		if got := readPath(t, r, "Got").(int64); got != b {
			t.Errorf("%s: the program read P2.B = %d, the store serves %d", addr, got, b)
		}
		if sum != a+b && addr != "D1" {
			t.Errorf("%s: SumM = %d, but P2.A + P2.B as served = %d", addr, sum, a+b)
		}
		gen := r.Tags().Generation()
		r.Scan()
		if r.Tags().Generation() != gen {
			t.Errorf("%s: generation moves with steady inputs", addr)
		}
		r.Restore(s.ID, "op")
		r.Scan()
		wantPath(t, r, "D1.P2.B", int64(6))
	}
}

// R23: a substitute on an input never delivered, then restored: the twin
// of the unforce case.
func TestExprReviewRestoreBeforeDelivery(t *testing.T) {
	d := &countDriver{}
	r, err := New(Options{
		Program:   "PROGRAM Main\nVAR_EXTERNAL G1 : Gauge; Seen : BOOL; END_VAR\nSeen := G1.Ok;\nEND_PROGRAM",
		Libraries: []string{gaugeLib}, Driver: d,
		Tags:          []TagDef{InstanceTag("G1", "Gauge", gaugeDef()), State("Seen", false)},
		Substitutable: []string{"G1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	s, err := r.Substitute("G1.Raw", SubstituteRequest{Value: int64(4), For: time.Hour, By: "op"})
	if err != nil {
		t.Fatal(err)
	}
	r.Scan()
	wantPath(t, r, "G1.Ok", true)
	r.Restore(s.ID, "op")
	r.Scan()
	r.Scan()
	if q := r.TagQuality("G1.Ok"); q != nio.NotConnected {
		t.Errorf("quality %v", q)
	}
	wantPath(t, r, "G1.Ok", false)
	wantPath(t, r, "Seen", false)
}

// R24: what a forced delivered member's quality is while its driver says Bad.
func TestExprReviewForcedDeliveredQuality(t *testing.T) {
	r, d := newGaugeRuntime(t, "", gaugeDef())
	deliver(d, 1, 0, 1)
	r.Scan()
	d.setQ("G1.Raw", nio.Bad)
	_ = r.Tags().Force("G1.Raw", int64(5))
	r.Scan()
	t.Logf("forced delivered member with a Bad driver: TagQuality(G1.Raw) = %v", r.TagQuality("G1.Raw"))
}

// R25: an edit removes or retypes the FUNCTION an expression calls.
func TestExprReviewEditRemovesFunction(t *testing.T) {
	r, d := newGaugeRuntime(t, "", gaugeDef())
	deliver(d, 3, 0, 1)
	r.Scan()
	src := r.prog.Source()
	a := strings.Index(src, "FUNCTION Double")
	b := strings.Index(src, "END_FUNCTION") + len("END_FUNCTION")
	gone := src[:a] + src[b:]
	retyped := strings.Replace(strings.Replace(src, "FUNCTION Double : INT", "FUNCTION Double : REAL", 1), "Double := x * 2;", "Double := 2.5;", 1)
	for name, s := range map[string]string{"removed": gone, "retyped": retyped} {
		_, err := r.prog.SwapWarm(s)
		if err == nil || !strings.Contains(err.Error(), "expr: member G1.Twice") || !strings.Contains(err.Error(), "fix the function or the expr:") {
			t.Errorf("%s: %v", name, err)
		}
	}
	r.Scan()
	wantPath(t, r, "G1.Twice", int64(6))
	// SwapWarm, Rollback, SwapWarm, Rollback.
	triple := strings.Replace(src, "x * 2", "x * 3", 1)
	for i := 0; i < 2; i++ {
		if _, err := r.prog.SwapWarm(triple); err != nil {
			t.Fatal(err)
		}
		r.Scan()
		wantPath(t, r, "G1.Twice", int64(9))
		if _, err := r.prog.Rollback(); err != nil {
			t.Fatal(err)
		}
		r.Scan()
		wantPath(t, r, "G1.Twice", int64(6))
	}
}

// R26: a second task, installs and reads from other goroutines (-race).
func TestExprReviewRaceWithTaskAndInstalls(t *testing.T) {
	d := &countDriver{}
	r, err := New(Options{
		Program:   "PROGRAM Main\nVAR_EXTERNAL G1 : Gauge; Seen : BOOL; END_VAR\nSeen := G1.Ok;\nEND_PROGRAM",
		Libraries: []string{gaugeLib}, Driver: d,
		Tags:  []TagDef{InstanceTag("G1", "Gauge", gaugeDef()), State("Seen", false), State("AuxSaw", false)},
		Tasks: []Task{{Name: "aux", Program: "PROGRAM Aux\nVAR_EXTERNAL G1 : Gauge; AuxSaw : BOOL; END_VAR\nG1.Note := G1.Note + 1.0; G1.Cmd := NOT G1.Cmd; AuxSaw := G1.Ok;\nEND_PROGRAM", Libraries: []string{gaugeLib}, Scan: time.Hour}},
		Scan:  time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	deliver(d, 1, 0, 1)
	r.Scan()
	src := r.prog.Source()
	triple := strings.Replace(src, "x * 2", "x * 3", 1)
	var wg sync.WaitGroup
	stop := make(chan struct{})
	loop := func(f func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					f()
				}
			}
		}()
	}
	loop(func() { _ = r.ScanTask("aux") })
	loop(func() { _, _ = r.prog.SwapWarm(triple); _, _ = r.prog.Rollback() })
	loop(func() { _ = r.Quality(); _ = r.TagQuality("G1.Both"); _ = r.Stats() })
	loop(func() { _ = r.Tags().Force("G1.Ok", true); r.Tags().Unforce("G1.Ok") })
	var notes float64
	for i := 0; i < 1000; i++ {
		d.set("G1.Raw", int64(i%3))
		d.set("G1.Div", int64(i%2))
		d.set("G1.Idx", int64(i%6))
		r.Scan()
		n := readPath(t, r, "G1.Note").(float64)
		if n < notes {
			t.Fatalf("G1.Note went back from %v to %v: a task's write was lost to the stage's commit", notes, n)
		}
		notes = n
	}
	close(stop)
	wg.Wait()
}
