package runtime

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/joyautomation/nautilus/internal/stproject"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
)

// Options configure a Runtime.
type Options struct {
	Program string // IEC 61131-3 source (ST, or ST header + FBD body)
	// Libraries are ST sources declaring TYPEs, FUNCTIONs, and
	// FUNCTION_BLOCKs the program calls — the unit of logic reuse in
	// IEC 61131-3. They compose ahead of Program exactly the way the
	// editor, LSP, and `nautilus pull` compose a project directory, so
	// online edits round-trip losslessly. The program may be ST or FBD;
	// libraries are ST.
	Libraries []string
	Driver    nio.Driver    // field I/O
	Scan      time.Duration // target scan interval (default 100ms)
	Inputs    []string      // tags read from the driver before each scan
	Outputs   []string      // tags written to the driver after each scan
	Seed      nio.Values    // initial operator/config tag values
	// DtTag, if set, receives the measured scan-to-scan seconds each scan
	// (bind it to your program's dt input, e.g. "ScanDtS").
	DtTag string
	// Meta optionally describes tags for HMIs — descriptions and engineering
	// units for a live tag table. Purely informational; the runtime never
	// reads it. Served by the server package at GET /api/meta.
	Meta map[string]TagMeta
	// Tags declares tags by ROLE — Input/Output/Setpoint/State, each with
	// its seed and HMI meta in one place (see tagdef.go). Merged with the
	// flat fields above, which remain fully supported.
	Tags []TagDef
	// Tasks are additional programs on their own scan rates — IEC
	// 61131-3's resource/task model (a fast interlock task beside a slow
	// reporting task). All tasks share the tag store; scans serialize on
	// one lock so every scan sees a consistent snapshot. The MAIN task
	// (Program/Scan above) owns field I/O and remains the online-edit
	// target; task programs are fixed at composition.
	Tasks []Task
	// Clock, when set, replaces the wall clock as the basis for scan dt and
	// for the IEC timers' NowMs — the two clocks a program can observe. Nil
	// in production; tests inject a virtual one. See clock.go.
	Clock Clock
}

// Task is one additional program in the resource: its source, its own
// libraries, and its scan interval.
type Task struct {
	Name      string        // diagnostics label ("interlock", "reports")
	Program   string        // IEC source (ST, or ST header + FBD body)
	Libraries []string      // composed ahead of Program, like Options.Libraries
	Scan      time.Duration // this task's interval (default 100ms)
	DtTag     string        // optional measured-dt tag, like Options.DtTag
}

// TaskStats is one additional task's health, riding inside ScanStats.
type TaskStats struct {
	Name        string  `json:"name"`
	TargetMs    float64 `json:"targetMs"`
	Count       uint64  `json:"count"`
	LastMs      float64 `json:"lastMs"`
	LogicErrors uint64  `json:"logicErrors"`
	LastError   string  `json:"lastError,omitempty"`
}

// taskRun is a compiled Task plus its live scheduling state.
type taskRun struct {
	name  string
	prog  *Program
	scan  time.Duration
	dtTag string

	mu       sync.Mutex
	lastScan time.Time
	stats    TaskStats
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
	tasks   []*taskRun
	clock   Clock // nil = wall clock; see clock.go

	// scanMu serializes scan execution across the main task and every
	// additional task — a scan always sees a consistent tag snapshot.
	scanMu sync.Mutex

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

	// Additional tasks' health (empty when the resource runs one program).
	Tasks []TaskStats `json:"tasks,omitempty"`
}

// New compiles the program and prepares the runtime.
func New(o Options) (*Runtime, error) {
	o = expandTags(o)
	if len(o.Libraries) > 0 {
		o.Program = stproject.Join(o.Libraries, o.Program)
	}
	prog, err := Compile(o.Program)
	if err != nil {
		return nil, err
	}
	if o.Scan <= 0 {
		o.Scan = 100 * time.Millisecond
	}
	tags := NewTags()
	// Set before any scan or goroutine can observe it, so the store's NowMs
	// and the scan's dt always read the same clock.
	tags.clock = o.Clock
	for k, v := range o.Seed {
		tags.Set(k, v)
	}
	r := &Runtime{
		prog: prog, tags: tags, driver: o.Driver, scan: o.Scan,
		inputs: o.Inputs, outputs: o.Outputs, dtTag: o.DtTag, meta: o.Meta,
		clock: o.Clock,
	}
	// POU names are the programs' identities (online edits route by them),
	// so every program in the resource must carry a distinct one.
	pous := map[string]string{strings.ToLower(prog.POU()): MainTaskName}
	for i, td := range o.Tasks {
		src := td.Program
		if len(td.Libraries) > 0 {
			src = stproject.Join(td.Libraries, src)
		}
		name := td.Name
		if name == "" {
			name = fmt.Sprintf("task%d", i+1)
		}
		if name == MainTaskName {
			return nil, fmt.Errorf("task name %q is reserved for the main task", MainTaskName)
		}
		tprog, err := Compile(src)
		if err != nil {
			return nil, fmt.Errorf("task %s: %w", name, err)
		}
		pou := strings.ToLower(tprog.POU())
		if other, dup := pous[pou]; dup {
			return nil, fmt.Errorf("task %s: PROGRAM %s collides with task %s — POU names identify programs and must be unique", name, tprog.POU(), other)
		}
		pous[pou] = name
		scan := td.Scan
		if scan <= 0 {
			scan = 100 * time.Millisecond
		}
		tr := &taskRun{name: name, prog: tprog, scan: scan, dtTag: td.DtTag}
		tr.stats.Name = name
		tr.stats.TargetMs = scan.Seconds() * 1000
		r.tasks = append(r.tasks, tr)
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

// Program exposes the main task's compiled program (hot-swap, status).
func (r *Runtime) Program() *Program { return r.prog }

// MainTaskName is the reserved name of the main task (Options.Program).
const MainTaskName = "main"

// TaskNames lists the additional tasks, in declaration order.
func (r *Runtime) TaskNames() []string {
	names := make([]string, len(r.tasks))
	for i, tr := range r.tasks {
		names[i] = tr.name
	}
	return names
}

// TaskProgram returns a task's program by task name — "main" (or "") for
// the main task, nil for an unknown name. Task programs hot-swap exactly
// like the main one; the swap applies on that task's next scan.
func (r *Runtime) TaskProgram(name string) *Program {
	if name == "" || name == MainTaskName {
		return r.prog
	}
	for _, tr := range r.tasks {
		if tr.name == name {
			return tr.prog
		}
	}
	return nil
}

// AllLocals merges every program's retained locals into one watch surface
// (HMI watches, editor live-value pills). Tasks first, the main task last,
// so a name collision resolves to the main program's value — the behavior
// single-program controllers always had.
func (r *Runtime) AllLocals() map[string]any {
	out := map[string]any{}
	for _, tr := range r.tasks {
		for k, v := range tr.prog.Locals() {
			out[k] = v
		}
	}
	for k, v := range r.prog.Locals() {
		out[k] = v
	}
	return out
}

// ProgramByPOU finds the program — main or task — whose `PROGRAM <Name>`
// matches, case-insensitively, returning it with its task name. This is
// how online edits route: the POU name is the program's identity. nil for
// no match.
func (r *Runtime) ProgramByPOU(pou string) (*Program, string) {
	if strings.EqualFold(r.prog.POU(), pou) {
		return r.prog, MainTaskName
	}
	for _, tr := range r.tasks {
		if strings.EqualFold(tr.prog.POU(), pou) {
			return tr.prog, tr.name
		}
	}
	return nil, ""
}

// Run drives the scan loops until the context is cancelled: the main task
// (read inputs → execute → write outputs, every Scan interval) plus one
// loop per additional task.
func (r *Runtime) Run(ctx context.Context) {
	for _, tr := range r.tasks {
		go func(tr *taskRun) {
			t := time.NewTicker(tr.scan)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					r.scanTask(tr)
				}
			}
		}(tr)
	}
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

// ScanTask executes one scan of an additional task by name — for tests and
// custom schedulers, the per-task analog of Scan().
func (r *Runtime) ScanTask(name string) error {
	for _, tr := range r.tasks {
		if tr.name == name {
			r.scanTask(tr)
			return nil
		}
	}
	return fmt.Errorf("runtime: no task named %q", name)
}

// scanTask runs one cycle of an additional task: measured dt in, program
// against the shared tag store, stats out. No driver I/O — the main task
// owns the field seam; tasks compute on the store at their own rates.
func (r *Runtime) scanTask(tr *taskRun) {
	t0 := time.Now()
	now := r.now(t0) // dt basis: the injected clock under test, else t0
	tr.mu.Lock()
	dt := tr.scan.Seconds()
	if !tr.lastScan.IsZero() {
		dt = now.Sub(tr.lastScan).Seconds()
	}
	tr.lastScan = now
	tr.mu.Unlock()

	r.scanMu.Lock()
	if tr.dtTag != "" {
		r.tags.SetReal(tr.dtTag, dt)
	}
	err := tr.prog.Run(r.tags)
	r.scanMu.Unlock()

	tr.mu.Lock()
	tr.stats.Count++
	tr.stats.LastMs = time.Since(t0).Seconds() * 1000
	if err != nil {
		tr.stats.LogicErrors++
		tr.stats.LastError = err.Error()
	}
	tr.mu.Unlock()
}

// Scan executes one full cycle: read inputs, run the program, write outputs.
// Run calls it on each tick; call it directly to drive the loop yourself
// (tests, a custom scheduler, or a redundancy standby stepping in sync).
func (r *Runtime) Scan() {
	t0 := time.Now()
	now := r.now(t0) // dt basis: the injected clock under test, else t0
	r.mu.Lock()
	dt := r.scan.Seconds()
	first := r.lastScan.IsZero()
	if !first {
		dt = now.Sub(r.lastScan).Seconds()
	}
	r.lastScan = now
	r.mu.Unlock()

	// The whole cycle excludes other tasks — one consistent tag snapshot.
	r.scanMu.Lock()
	defer r.scanMu.Unlock()

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
	s := r.stats
	s.Recent = append([]float64(nil), r.stats.Recent...)
	s.Periods = append([]float64(nil), r.stats.Periods...)
	s.Histogram = append([]int(nil), r.stats.Histogram...)
	r.mu.Unlock()
	for _, tr := range r.tasks {
		tr.mu.Lock()
		s.Tasks = append(s.Tasks, tr.stats)
		tr.mu.Unlock()
	}
	return s
}

// Meta returns the HMI tag documentation given at construction (may be nil).
func (r *Runtime) Meta() map[string]TagMeta { return r.meta }

// Inputs returns the driver-bound input tag names.
func (r *Runtime) Inputs() []string { return r.inputs }

// Outputs returns the driver-bound output tag names.
func (r *Runtime) Outputs() []string { return r.outputs }
