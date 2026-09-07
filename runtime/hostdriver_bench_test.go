package runtime_test

import (
	"fmt"
	"sync"
	"testing"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/runtime"
)

// The central-host shape: one controller whose driver is a Sparkplug HOST
// application aggregating a whole site — 10,000 input bindings it snapshots
// every scan and 6,000 output bindings the runtime hands back every scan,
// none of which an operator has touched.
//
// hostLike reproduces exactly the two costs that shape pays, and nothing
// else: ReadInputs copies its whole value map (sparkplug/host state.go's
// snapshot()), and WriteOutputs canonicalises EVERY output it is handed —
// coerce to the bound member's type, then compare against the last accounted
// value — before deciding almost every scan that nothing was commanded
// (sparkplug/host driver.go's WriteOutputs).

const (
	benchHostInputs  = 10000
	benchHostOutputs = 6000
)

type hostLike struct {
	mu      sync.Mutex
	vals    nio.Values
	lastOut map[string]ir.Value
	leaf    map[string]*ir.Type // member bindings, as the host manifest has
	reads   int
	writes  int
}

func newHostLike() *hostLike {
	h := &hostLike{
		vals:    make(nio.Values, benchHostInputs),
		lastOut: make(map[string]ir.Value, benchHostOutputs),
		leaf:    make(map[string]*ir.Type, benchHostOutputs),
	}
	realT := &ir.Type{Kind: ir.TypeReal}
	for i := 0; i < benchHostInputs; i++ {
		h.vals[fmt.Sprintf("HI%d", i)] = float64(i)
	}
	for i := 0; i < benchHostOutputs; i++ {
		h.leaf[fmt.Sprintf("HO%d", i)] = realT
	}
	return h
}

func (h *hostLike) ReadInputs() (nio.Values, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.reads++
	out := make(nio.Values, len(h.vals))
	for k, v := range h.vals {
		out[k] = v
	}
	return out, nil
}

// WriteOutputs is the canonWrite pass: every output, every call.
func (h *hostLike) WriteOutputs(vals nio.Values) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.writes++
	for name, v := range vals {
		leaf, bound := h.leaf[name]
		if !bound {
			continue
		}
		iv, ok := irValueOfBench(v)
		if !ok {
			continue
		}
		cv := ir.CoerceValue(iv, leaf)
		if prev, seen := h.lastOut[name]; seen && prev.Kind == cv.Kind && prev.F == cv.F {
			continue // unchanged since the last scan we accounted for
		}
		h.lastOut[name] = cv
	}
	return nil
}

// hostLikeBatch is the same driver with io.BatchReader implemented — the
// one-line change that stops a 10,000-binding snapshot allocating a fresh
// map every scan.
type hostLikeBatch struct{ *hostLike }

func (h hostLikeBatch) ReadInputsInto(dst nio.Values) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.reads++
	for k, v := range h.vals {
		dst[k] = v
	}
	if len(dst) != len(h.vals) {
		for k := range dst {
			if _, ok := h.vals[k]; !ok {
				delete(dst, k)
			}
		}
	}
	return nil
}

func irValueOfBench(v any) (ir.Value, bool) {
	switch x := v.(type) {
	case bool:
		return ir.BoolVal(x), true
	case float64:
		return ir.RealVal(x), true
	case int64:
		return ir.IntVal(x), true
	case string:
		return ir.StringVal(x), true
	case ir.Value:
		return x, true
	}
	return ir.Value{}, false
}

const hostBenchProg = `PROGRAM Main
VAR_EXTERNAL
    HI0 : REAL;
    HI1 : REAL;
    HO0 : REAL;
END_VAR
HO0 := HI0 + HI1;
END_PROGRAM
`

func hostRuntime(tb testing.TB, drv nio.Driver, alwaysWrite bool) *runtime.Runtime {
	tb.Helper()
	defs := make([]runtime.TagDef, 0, benchHostInputs+benchHostOutputs)
	for i := 0; i < benchHostInputs; i++ {
		defs = append(defs, runtime.Input(fmt.Sprintf("HI%d", i)))
	}
	for i := 0; i < benchHostOutputs; i++ {
		defs = append(defs, runtime.Output(fmt.Sprintf("HO%d", i)))
	}
	rt, err := runtime.New(runtime.Options{
		Program:            hostBenchProg,
		Driver:             drv,
		Tags:               defs,
		AlwaysWriteOutputs: alwaysWrite,
	})
	if err != nil {
		tb.Fatalf("compile: %v", err)
	}
	rt.Scan() // first delivery, first (baseline) write
	rt.Scan()
	return rt
}

// BenchmarkHostScanAlwaysWrite is the old contract: WriteOutputs on every
// scan, so the driver canonicalises all 6,000 outputs to conclude that none
// of them is a command.
func BenchmarkHostScanAlwaysWrite(b *testing.B) {
	rt := hostRuntime(b, newHostLike(), true)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rt.Scan()
	}
}

// BenchmarkHostScanOnChange is the default contract: the generation stamp
// over the output tags says nothing moved, so the driver is not called.
func BenchmarkHostScanOnChange(b *testing.B) {
	rt := hostRuntime(b, newHostLike(), false)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rt.Scan()
	}
}

// BenchmarkHostScanOnChangeBatchRead adds io.BatchReader on the input side:
// the 10,000-binding delivery lands in the runtime's own map.
func BenchmarkHostScanOnChangeBatchRead(b *testing.B) {
	rt := hostRuntime(b, hostLikeBatch{newHostLike()}, false)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rt.Scan()
	}
}

// BenchmarkHostScanOutputChanging keeps one output genuinely moving every
// scan, so the driver IS called every scan — the guard must not cost more
// than it saves when there is always something to send.
func BenchmarkHostScanOutputChanging(b *testing.B) {
	drv := hostLikeBatch{newHostLike()}
	rt := hostRuntime(b, drv, false)
	tags := rt.Tags()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tags.SetReal("HO1", float64(i))
		rt.Scan()
	}
}

// TestHostWriteOutputsSkippedWhenUnchanged pins the contract the benchmarks
// above lean on: the driver sees the first scan and then nothing until an
// output actually moves — and sees it again the moment one does.
func TestHostWriteOutputsSkippedWhenUnchanged(t *testing.T) {
	h := newHostLike()
	rt := hostRuntime(t, h, false)
	h.mu.Lock()
	base := h.writes
	h.mu.Unlock()
	for i := 0; i < 5; i++ {
		rt.Scan()
	}
	h.mu.Lock()
	quiet := h.writes
	h.mu.Unlock()
	if quiet != base {
		t.Errorf("WriteOutputs called %d times with nothing changed, want 0", quiet-base)
	}
	rt.Tags().SetReal("HO5", 42)
	rt.Scan()
	h.mu.Lock()
	after := h.writes
	h.mu.Unlock()
	if after != quiet+1 {
		t.Errorf("WriteOutputs called %d times after an output moved, want 1", after-quiet)
	}
	if got := h.lastOut["HO5"].F; got != 42 {
		t.Errorf("driver holds HO5 = %v, want 42", got)
	}
}

// TestAlwaysWriteOutputs is the escape hatch: a driver that needs a per-scan
// call keeps getting one.
func TestAlwaysWriteOutputs(t *testing.T) {
	h := newHostLike()
	rt := hostRuntime(t, h, true)
	h.mu.Lock()
	base := h.writes
	h.mu.Unlock()
	for i := 0; i < 5; i++ {
		rt.Scan()
	}
	h.mu.Lock()
	got := h.writes - base
	h.mu.Unlock()
	if got != 5 {
		t.Errorf("WriteOutputs called %d times over 5 scans, want 5", got)
	}
}
