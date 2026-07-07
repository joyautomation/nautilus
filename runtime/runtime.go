package runtime

import (
	"context"
	"sync"
	"time"

	nio "github.com/joyautomation/nautilus/io"
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

	mu       sync.Mutex
	lastScan time.Time
	stats    ScanStats
}

// ScanStats are the loop's live health metrics for an HMI/diagnostics view.
type ScanStats struct {
	Count   uint64  `json:"count"`
	LastMs  float64 `json:"lastMs"`  // scan execution time
	PeriodS float64 `json:"periodS"` // actual interval between scans
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
	return &Runtime{
		prog: prog, tags: tags, driver: o.Driver, scan: o.Scan,
		inputs: o.Inputs, outputs: o.Outputs, dtTag: o.DtTag,
	}, nil
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
	if !r.lastScan.IsZero() {
		dt = t0.Sub(r.lastScan).Seconds()
	}
	r.lastScan = t0
	r.mu.Unlock()

	// 1. inputs
	if r.driver != nil {
		if in, err := r.driver.ReadInputs(); err == nil {
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

	// 2. execute one scan
	_ = r.prog.Run(r.tags)

	// 3. outputs
	if r.driver != nil && len(r.outputs) > 0 {
		out := make(nio.Values, len(r.outputs))
		for _, name := range r.outputs {
			if v, err := r.tags.ReadGlobal(name); err == nil {
				out[name] = plain(v)
			}
		}
		_ = r.driver.WriteOutputs(out)
	}

	r.mu.Lock()
	r.stats.Count++
	r.stats.LastMs = time.Since(t0).Seconds() * 1000
	r.stats.PeriodS = dt
	r.mu.Unlock()
}

// Stats returns the current scan health metrics.
func (r *Runtime) Stats() ScanStats {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stats
}
