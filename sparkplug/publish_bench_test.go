package sparkplug

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/runtime"
)

// One publish tick of a real edge node: 550 published metrics, 300 of them
// UDT templates, 5% of them actually moving. Everything above the MQTT seam
// — Snapshot, class matching, RBE (including the struct deep-compare), and
// the ir.Value → Metric mapping — is in publishPassLocked, so this measures
// the whole cost the broker never sees.

const (
	benchTemplates = 300
	benchTmplFlds  = 10
	benchScalars   = 250
	benchChurn     = 28 // ≈5% of 550
)

func benchLib() string {
	var b strings.Builder
	b.WriteString("TYPE\n  Blk : STRUCT\n")
	for i := 0; i < benchTmplFlds; i++ {
		switch i % 3 {
		case 0:
			fmt.Fprintf(&b, "    F%d : REAL;\n", i)
		case 1:
			fmt.Fprintf(&b, "    F%d : BOOL;\n", i)
		default:
			fmt.Fprintf(&b, "    F%d : INT;\n", i)
		}
	}
	b.WriteString("  END_STRUCT;\nEND_TYPE\n")
	return b.String()
}

const benchPubProg = `PROGRAM Main
VAR_EXTERNAL
    S0 : Blk;
END_VAR
S0.F1 := S0.F1;
END_PROGRAM
`

// benchNode builds a born node over a 550-metric store, its RBE baseline
// already primed, with the publish classes a site actually configures
// (globbed assignments, a deadband class and an every-change class).
func benchNode(tb testing.TB) (*Node, *runtime.Runtime, []string) {
	tb.Helper()
	defs := []runtime.TagDef{}
	var names []string
	for i := 0; i < benchTemplates; i++ {
		n := fmt.Sprintf("S%d", i)
		names = append(names, n)
		defs = append(defs, runtime.Typed(n, runtime.RoleState, "Blk"))
	}
	for i := 0; i < benchScalars; i++ {
		n := fmt.Sprintf("N%d", i)
		names = append(names, n)
		defs = append(defs, runtime.Setpoint(n, float64(i)))
	}
	rt, err := runtime.New(runtime.Options{
		Program:   benchPubProg,
		Libraries: []string{benchLib()},
		Tags:      defs,
	})
	if err != nil {
		tb.Fatalf("compile: %v", err)
	}
	n, err := New(rt, Config{GroupID: "g", EdgeNode: "e"},
		WithPublishClass("fast", RBE{}),
		WithPublishClass("slow", RBE{Deadband: 0.5, MaxInterval: time.Hour}),
		WithMetricClass("fast", "N*"),
		WithMetricClass("slow", "S1*", "S2*"),
	)
	if err != nil {
		tb.Fatalf("node: %v", err)
	}
	n.born = true
	for _, nm := range names {
		n.known[nm] = true
	}
	// Prime the RBE baseline the way a birth does, so the first timed pass
	// is a steady-state pass and not 550 first-value publishes.
	n.mu.Lock()
	n.publishPassLocked(time.Now())
	n.mu.Unlock()
	return n, rt, names
}

// BenchmarkPublishPassSteady: nothing changed since the last tick. This is
// the overwhelmingly common case at any publish rate faster than the plant.
func BenchmarkPublishPassSteady(b *testing.B) {
	n, _, _ := benchNode(b)
	now := time.Now()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		n.mu.Lock()
		n.publishPassLocked(now)
		n.mu.Unlock()
	}
}

// BenchmarkPublishPassChurn: 5% of the metrics move between ticks — the
// steady state of a live plant.
func BenchmarkPublishPassChurn(b *testing.B) {
	n, rt, names := benchNode(b)
	tags := rt.Tags()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		for k := 0; k < benchChurn; k++ {
			nm := names[(i*benchChurn+k)%len(names)]
			if strings.HasPrefix(nm, "S") {
				_ = tags.SetPath(nm+".F0", float64(i+k))
			} else {
				tags.SetReal(nm, float64(i+k))
			}
		}
		b.StartTimer()
		n.mu.Lock()
		n.publishPassLocked(time.Now())
		n.mu.Unlock()
	}
}

// BenchmarkRBEStructCompare isolates the struct deep-compare: 300 templates
// of 10 members, none of them changed.
func BenchmarkRBEStructCompare(b *testing.B) {
	sd := &ir.StructDef{Name: "Blk", FieldIndex: map[string]int{}}
	fld := make([]ir.Value, benchTmplFlds)
	for i := 0; i < benchTmplFlds; i++ {
		sd.FieldIndex[fmt.Sprintf("F%d", i)] = i
		sd.Fields = append(sd.Fields, ir.StructField{Name: fmt.Sprintf("F%d", i)})
		fld[i] = ir.RealVal(float64(i))
	}
	v := ir.Value{Kind: ir.TypeStruct, Struct: sd, Fld: fld}
	r := RBE{Deadband: 0.5}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for k := 0; k < benchTemplates; k++ {
			if r.memberChanged(&v, &v) {
				b.Fatal("unchanged struct reported changed")
			}
		}
	}
}
