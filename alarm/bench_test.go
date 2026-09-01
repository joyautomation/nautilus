package alarm

import (
	"fmt"
	"testing"
	"time"
)

// fleetScale builds a definition set and a matching tag map at the size the
// Pomona export implies: ~2 500 definitions across 25 sites, each gated on
// its node's __Online bit, with a tenth of them in alarm — a plausible bad
// day, not an idle plant.
func fleetScale(n int) ([]Def, tags) {
	defs := make([]Def, 0, n)
	tg := tags{}
	members := []string{"LOS", "H", "HH", "L", "LL", "OOR"}
	for i := 0; i < n; i++ {
		site := fmt.Sprintf("RTU%02d", i%25)
		tg[site+"__Online"] = true
		path := fmt.Sprintf("%s_EQ_%03d.%s", site, i/len(members), members[i%len(members)])
		tg[path] = i%10 == 0
		d := def(path, path)
		d.Site = site
		d.Enable = site + "__Online"
		defs = append(defs, d)
	}
	return defs, tg
}

func newFleetEngine(tb testing.TB, n int) (*Engine, *clock, tags) {
	tb.Helper()
	defs, tg := fleetScale(n)
	clk := newClock()
	e, err := New(Options{Defs: defs, Read: tg.read, Now: clk.now, Journal: NewRing(DefaultKeep)})
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { e.Close() })
	return e, clk, tg
}

// BenchmarkEvaluateFleet is the number the brief calls the main risk: one
// pass over the whole definition set, which has to fit inside a scan.
func BenchmarkEvaluateFleet(b *testing.B) {
	e, clk, _ := newFleetEngine(b, 2500)
	e.Evaluate() // settle: the first pass emits every activation
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		clk.advance(500 * time.Millisecond)
		e.Evaluate()
	}
}

// BenchmarkEvaluateFleetFlapping is the worst case: every alarm changing
// state on every pass, so every definition also journals and notifies.
func BenchmarkEvaluateFleetFlapping(b *testing.B) {
	e, clk, tg := newFleetEngine(b, 2500)
	paths := make([]string, 0, len(tg))
	for k := range tg {
		if k[len(k)-1] != 'e' { // skip the __Online bits
			paths = append(paths, k)
		}
	}
	e.Evaluate()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, p := range paths {
			tg[p] = i%2 == 0
		}
		clk.advance(500 * time.Millisecond)
		e.Evaluate()
	}
}

func BenchmarkSummaryFleet(b *testing.B) {
	e, _, _ := newFleetEngine(b, 2500)
	e.Evaluate()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = e.Summary()
	}
}

// TestEvaluateFleetSmoke is a floor, not a target: it reports the measured
// per-pass cost so a regression is visible in test output, and fails only
// at a ceiling so far above the real number that hitting it means something
// is algorithmically wrong. The budget the design cares about — 5 ms per
// Evaluate at 2 500 definitions — belongs in BenchmarkEvaluateFleet, where
// a machine-dependent number cannot make CI flaky.
func TestEvaluateFleetSmoke(t *testing.T) {
	if testing.Short() {
		t.Skip("fleet-scale smoke test")
	}
	const n, passes = 2500, 20
	e, clk, _ := newFleetEngine(t, n)
	e.Evaluate()

	start := time.Now()
	for i := 0; i < passes; i++ {
		clk.advance(500 * time.Millisecond)
		e.Evaluate()
	}
	per := time.Since(start) / passes
	t.Logf("Evaluate over %d definitions: %v per pass (design budget 5ms)", n, per)
	if per > 50*time.Millisecond {
		t.Fatalf("Evaluate took %v per pass over %d definitions — ten times the 5ms budget", per, n)
	}
	if got := e.Summary().Active; got != n/10 {
		t.Fatalf("Summary().Active = %d, want %d", got, n/10)
	}
}

// TestExpandFleetSmoke checks the other half of the scale story: 14 rules
// over a fleet-sized tag list expand quickly and deterministically.
func TestExpandFleetSmoke(t *testing.T) {
	if testing.Short() {
		t.Skip("fleet-scale smoke test")
	}
	var tagList []TagInfo
	for i := 0; i < 400; i++ {
		site := fmt.Sprintf("RTU%02d", i%25)
		tagList = append(tagList, TagInfo{
			Name: fmt.Sprintf("%s_EQ_%03d", site, i), TypeName: "AnalogInput",
			Struct: analogInput, Site: site, Desc: fmt.Sprintf("Equipment %d", i),
		})
	}
	start := time.Now()
	defs, err := Expand(fleetRules(), nil, tagList)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Expand: %d rules × %d tags -> %d definitions in %v",
		len(fleetRules()), len(tagList), len(defs), time.Since(start))
	if len(defs) != 400*6 {
		t.Fatalf("got %d definitions, want %d", len(defs), 400*6)
	}
}

func BenchmarkExpand(b *testing.B) {
	var tagList []TagInfo
	for i := 0; i < 400; i++ {
		tagList = append(tagList, TagInfo{
			Name: fmt.Sprintf("RTU%02d_EQ_%03d", i%25, i), TypeName: "AnalogInput",
			Struct: analogInput, Site: fmt.Sprintf("RTU%02d", i%25),
		})
	}
	rules := fleetRules()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Expand(rules, nil, tagList); err != nil {
			b.Fatal(err)
		}
	}
}
