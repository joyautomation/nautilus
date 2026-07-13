package runtime

import (
	"context"
	"sync"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
)

// Options configure a Runtime.
type Options struct {
	Program string        // IEC 61131-3 source
	Driver  nio.Driver    // field I/O
	Scan    time.Duration // target scan interval (default 100ms)
	Inputs  []string      // tags read from the driver before each scan
	Outputs []string      // tags written to the driver after each scan
	Seed    nio.Values    // initial operator/config tag values
	// DtTag, if set, receives the measured scan-to-scan seconds each scan
	// (bind it to your program's dt input, e.g. "ScanDtS").
	DtTag string
	// Meta optionally describes tags for HMIs — descriptions and engineering
	// units for a live tag table. Purely informational; the runtime never
	// reads it. Served by the server package at GET /api/meta.
	Meta map[string]TagMeta
}

// TagMeta is HMI-facing tag documentation: a human description and the
// engineering unit of the value ("°C", "%", "L/s", ...).
type TagMeta struct {
	Desc string `json:"desc,omitempty"`
	Unit string `json:"unit,omitempty"`
}

// Runtime hosts a Program on a scan loop, binding a Driver's I/O through the
// tag store. One Runtime is one controller.
type Runtime struct {
	prog    *Program
	tags    *Tags
	driver  nio.Driver
	scan    time.Duration
	inputs  []string
	outputs []string
	dtTag   string
	meta    map[string]TagMeta

	mu       sync.Mutex
	lastScan time.Time
	stats    ScanStats
}

// Scan-history sizing for the diagnostics view: enough samples to see a
// pattern, small enough to ship in every frame.
const (
	historyLen   = 180 // recent scan/period samples kept
	histBuckets  = 15  // scan-time distribution buckets
	histBucketMs = 2.0 // 0–2, 2–4, … 28+ ms
)

// ScanStats are the loop's live health metrics for an HMI/diagnostics view —
// the numbers behind a PLC-style "runtime diagnostics" page. Cyclic scan:
// read inputs → execute logic → write outputs; the phase timings show where
// the scan budget actually goes (usually I/O on the wire, not logic).
type ScanStats struct {
	Count    uint64  `json:"count"`
	TargetMs float64 `json:"targetMs"` // configured scan interval
	LastMs   float64 `json:"lastMs"`   // last full scan execution time
	MinMs    float64 `json:"minMs"`
	MaxMs    float64 `json:"maxMs"`
	AvgMs    float64 `json:"avgMs"` // exponentially weighted average

	// Last-scan phase breakdown. Logic executes in microseconds; I/O is
	// milliseconds — different units so both stay readable.
	ReadMs  float64 `json:"readMs"`
	ExecUs  float64 `json:"execUs"`
	WriteMs float64 `json:"writeMs"`

	PeriodMs float64 `json:"periodMs"` // actual interval between scans
	JitterMs float64 `json:"jitterMs"` // EWMA of |period − target|

	// Fault counters: input reads that failed (the scan ran on last-known
	// values) and program scans that errored.
	IOErrors    uint64 `json:"ioErrors"`
	LogicErrors uint64 `json:"logicErrors"`
	IOHealthy   bool   `json:"ioHealthy"`
	LastIOError string `json:"lastIOError,omitempty"`

	Recent    []float64 `json:"recent"`    // last 180 scan times, ms
	Periods   []float64 `json:"periods"`   // last 180 actual periods, ms
	Histogram []int     `json:"histogram"` // 2 ms buckets of scan time
}

// New compiles the program and prepares the runtime.
func New(o Options) (*Runtime, error) {
	prog, err := Compile(o.Program)
	if err != nil {
		return nil, err
	}
	if o.Scan <= 0 {
		o.Scan = 100 * time.Millisecond
	}
	tags := NewTags()
	for k, v := range o.Seed {
		tags.Set(k, v)
	}
	r := &Runtime{
		prog: prog, tags: tags, driver: o.Driver, scan: o.Scan,
		inputs: o.Inputs, outputs: o.Outputs, dtTag: o.DtTag, meta: o.Meta,
	}
	r.stats.TargetMs = o.Scan.Seconds() * 1000
	r.stats.IOHealthy = true
	r.stats.Recent = make([]float64, 0, historyLen)
	r.stats.Periods = make([]float64, 0, historyLen)
	r.stats.Histogram = make([]int, histBuckets)
	return r, nil
}

// Tags exposes the tag store for operator writes and HMI reads.
func (r *Runtime) Tags() *Tags { return r.tags }

// Program exposes the compiled program (hot-swap, status).
func (r *Runtime) Program() *Program { return r.prog }

// Run drives the scan loop until the context is cancelled: read inputs,
// execute the program, write outputs — every Scan interval.
func (r *Runtime) Run(ctx context.Context) {
	t := time.NewTicker(r.scan)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.Scan()
		}
	}
}

// Scan executes one full cycle: read inputs, run the program, write outputs.
// Run calls it on each tick; call it directly to drive the loop yourself
// (tests, a custom scheduler, or a redundancy standby stepping in sync).
func (r *Runtime) Scan() {
	t0 := time.Now()
	r.mu.Lock()
	dt := r.scan.Seconds()
	first := r.lastScan.IsZero()
	if !first {
		dt = t0.Sub(r.lastScan).Seconds()
	}
	r.lastScan = t0
	r.mu.Unlock()

	// 1. inputs — on a read failure the scan runs on last-known values.
	var ioErr error
	if r.driver != nil {
		in, err := r.driver.ReadInputs()
		ioErr = err
		if err == nil {
			for _, name := range r.inputs {
				if v, ok := in[name]; ok {
					r.tags.Set(name, v)
				}
			}
		}
	}
	if r.dtTag != "" {
		r.tags.SetReal(r.dtTag, dt)
	}
	t1 := time.Now()

	// 2. execute one scan
	logicErr := r.prog.Run(r.tags)
	t2 := time.Now()

	// 3. outputs
	if r.driver != nil && len(r.outputs) > 0 {
		out := make(nio.Values, len(r.outputs))
		for _, name := range r.outputs {
			if v, err := r.tags.ReadGlobal(name); err == nil {
				// Compound values (UDTs, arrays) cross the seam as ir.Value so
				// typed drivers keep field names and integer widths; scalars
				// stay plain Go values for simple drivers.
				if v.Kind == ir.TypeStruct || v.Kind == ir.TypeArray {
					out[name] = v
				} else {
					out[name] = plain(v)
				}
			}
		}
		if err := r.driver.WriteOutputs(out); err != nil && ioErr == nil {
			ioErr = err
		}
	}
	t3 := time.Now()

	r.recordScan(t0, t1, t2, t3, dt, first, ioErr, logicErr)
}

// recordScan folds one cycle's timings into the diagnostics.
func (r *Runtime) recordScan(t0, t1, t2, t3 time.Time, periodS float64, first bool, ioErr, logicErr error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := &r.stats

	scanMs := t3.Sub(t0).Seconds() * 1000
	s.Count++
	s.LastMs = scanMs
	s.ReadMs = t1.Sub(t0).Seconds() * 1000
	s.ExecUs = t2.Sub(t1).Seconds() * 1e6
	s.WriteMs = t3.Sub(t2).Seconds() * 1000
	if s.MinMs == 0 || scanMs < s.MinMs {
		s.MinMs = scanMs
	}
	if scanMs > s.MaxMs {
		s.MaxMs = scanMs
	}
	if s.AvgMs == 0 {
		s.AvgMs = scanMs
	} else {
		s.AvgMs = s.AvgMs*0.98 + scanMs*0.02
	}
	if !first {
		periodMs := periodS * 1000
		s.PeriodMs = periodMs
		j := periodMs - s.TargetMs
		if j < 0 {
			j = -j
		}
		if s.JitterMs == 0 {
			s.JitterMs = j
		} else {
			s.JitterMs = s.JitterMs*0.95 + j*0.05
		}
		s.Periods = pushSample(s.Periods, periodMs)
	}
	s.Recent = pushSample(s.Recent, scanMs)
	b := int(scanMs / histBucketMs)
	if b >= histBuckets {
		b = histBuckets - 1
	}
	s.Histogram[b]++

	if ioErr != nil {
		s.IOErrors++
		s.IOHealthy = false
		s.LastIOError = ioErr.Error()
	} else {
		s.IOHealthy = true
	}
	if logicErr != nil {
		s.LogicErrors++
	}
}

// pushSample appends to a fixed-length ring kept as a slice: cheap, and the
// JSON encoding stays a plain ordered array.
func pushSample(s []float64, v float64) []float64 {
	if len(s) >= historyLen {
		copy(s, s[1:])
		s = s[:historyLen-1]
	}
	return append(s, v)
}

// Stats returns a copy of the current scan health metrics.
func (r *Runtime) Stats() ScanStats {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.stats
	s.Recent = append([]float64(nil), r.stats.Recent...)
	s.Periods = append([]float64(nil), r.stats.Periods...)
	s.Histogram = append([]int(nil), r.stats.Histogram...)
	return s
}

// Meta returns the HMI tag documentation given at construction (may be nil).
func (r *Runtime) Meta() map[string]TagMeta { return r.meta }

// Inputs returns the driver-bound input tag names.
func (r *Runtime) Inputs() []string { return r.inputs }

// Outputs returns the driver-bound output tag names.
func (r *Runtime) Outputs() []string { return r.outputs }
