package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/joyautomation/nautilus/internal/stproject"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/retain"
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
	// Retain persists operator state across power cycles — retained tag
	// values and online-edited program sources. See the retain package for
	// the file and ConfigMap stores. Loaded before the first scan, saved on
	// change every 2s, re-loaded on a redundancy takeover.
	Retain retain.Store
	// RetainTags names the tags Retain persists. Empty means every
	// RoleSetpoint tag from Tags — the operator-writable values are exactly
	// what must survive a restart, while state/inputs re-derive from the
	// field. Ignored when Retain is nil.
	RetainTags []string
	// Coordinator gates the scan loop for redundancy: a standby replica
	// (IsLeader false) skips scans entirely — no field I/O, no logic — and
	// performs the takeover sequence (reload retained state, reset program
	// frames, zero the dt clocks) on the edge where it becomes leader.
	// leader.Elector satisfies this; nil means standalone, always leader.
	Coordinator Coordinator
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
	types   map[string]*ir.Type
	tasks   []*taskRun
	clock   Clock // nil = wall clock; see clock.go

	retainStore retain.Store
	retainTags  []string
	coord       Coordinator

	// leadMu guards the leadership edge so exactly one scan performs the
	// takeover sequence when this replica becomes leader. See retain.go.
	leadMu  sync.Mutex
	leading bool

	// alarms is the alarm engine, as retained operator state — nil unless
	// SetAlarms registered one. Its own mutex because loadRetained reads it
	// from inside takeover, which already holds leadMu.
	alarmMu sync.Mutex
	alarms  retain.AlarmRetainer

	// scanMu serializes scan execution across the main task and every
	// additional task — a scan always sees a consistent tag snapshot.
	scanMu sync.Mutex

	// obsMu guards onScan/obsNext independently of scanMu: OnScan may be
	// called (registration or cancel) from any goroutine at any time,
	// including while a scan is in flight. scanMu still serializes the
	// CALLS to registered observers — see fireOnScan.
	obsMu   sync.Mutex
	obsNext uint64
	onScan  []onScanEntry

	mu       sync.Mutex
	lastScan time.Time
	stats    ScanStats
}

// onScanEntry is one registered OnScan observer, identified by an id so
// cancel can remove exactly this registration without aliasing another
// caller's identical func value.
type onScanEntry struct {
	id uint64
	fn func(*Tags)
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

	// Retain-store failures surface here the way I/O failures do: a save
	// that keeps erroring is invisible exactly until the restart that
	// needed it, so the dashboard must show it while it is fixable.
	RetainErrors    uint64 `json:"retainErrors,omitempty"`
	LastRetainError string `json:"lastRetainError,omitempty"`

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

// New compiles the programs and prepares the runtime.
//
// Order matters here. A TagDef may name a UDT (`Type: "Motor"`), and the only
// place that name has a meaning is the compiled programs' TYPE table — the
// same declarations the logic uses, so the tag store and the programs cannot
// disagree about a Motor's shape. So every program compiles FIRST, the type
// tables union, and only then are tags expanded and seeded.
func New(o Options) (*Runtime, error) {
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

	// POU names are the programs' identities (online edits route by them),
	// so every program in the resource must carry a distinct one.
	var tasks []*taskRun
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
		tasks = append(tasks, tr)
	}

	types, err := unionTypes(prog, tasks)
	if err != nil {
		return nil, err
	}
	if o, err = expandTags(o, types); err != nil {
		return nil, err
	}

	tags := NewTags()
	// Set before any scan or goroutine can observe it, so the store's NowMs
	// and the scan's dt always read the same clock.
	tags.clock = o.Clock
	for k, v := range o.Seed {
		// setAny, not Set: a seed CREATES the tag under exactly the name it
		// was configured with. Set's member-path guard is for operator
		// writes, which may only address a tag that already exists.
		tags.setAny(k, v)
	}
	// A dt-tag is otherwise unseeded: the scan loop only writes it AFTER a
	// scan completes (see step() below), so a snapshot taken between New()
	// and the first scan is missing it — exactly the gap a Sparkplug birth
	// reads from. Left that way, a node births with N-1 metrics and rebirths
	// a scan later once the tag exists: one spurious NBIRTH per restart per
	// task with a dt-tag. Seed it here at REAL 0.0 like any other `state`
	// tag, unless it was already seeded explicitly (a declared tag with this
	// name wins, so an operator-visible init: still applies).
	seedDtTag := func(name string) {
		if name == "" {
			return
		}
		if _, ok := tags.vals[name]; ok {
			return
		}
		tags.SetReal(name, 0.0)
	}
	seedDtTag(o.DtTag)
	for _, tr := range tasks {
		seedDtTag(tr.dtTag)
	}
	retainTags := o.RetainTags
	if o.Retain != nil && len(retainTags) == 0 {
		// The default retained set is the operator-writable surface: every
		// RoleSetpoint tag. State re-seeds, inputs re-read from the field,
		// outputs re-derive from logic — setpoints are what a restart loses.
		for _, d := range o.Tags {
			if d.Role == RoleSetpoint && d.Name != "" {
				retainTags = append(retainTags, d.Name)
			}
		}
	}
	r := &Runtime{
		prog: prog, tags: tags, driver: o.Driver, scan: o.Scan,
		inputs: o.Inputs, outputs: o.Outputs, dtTag: o.DtTag, meta: o.Meta,
		clock: o.Clock, types: types, tasks: tasks,
		retainStore: o.Retain, retainTags: retainTags, coord: o.Coordinator,
	}
	r.stats.TargetMs = o.Scan.Seconds() * 1000
	r.stats.IOHealthy = true
	r.stats.Recent = make([]float64, 0, historyLen)
	r.stats.Periods = make([]float64, 0, historyLen)
	r.stats.Histogram = make([]int, histBuckets)
	return r, nil
}

// OnScan registers fn to run at the end of every main-task Scan() — after
// the program has executed and, if a driver is bound, after outputs have
// been written to it — so fn observes the tag store exactly as this scan
// left it. Registered observers run in registration order, synchronously,
// still holding scanMu: nothing else can be mid-scan while fn runs, which
// is what lets fn read the store through Tags without racing the next
// scan. fn must NOT block (it shares the scan budget: a slow observer is a
// slow scan) and must NOT write field tags (inputs for this cycle already
// landed in the read phase; a write here would be invisible to the program
// that just ran and only take effect next scan, which is not what "post-
// scan" means). Reads are fine and safe — fn runs with scanMu held but NOT
// t.mu, so Tags.ReadPath/ReadGlobal/Snapshot etc. all work without
// deadlocking.
//
// A panicking observer is recovered and logged rather than propagated: one
// misbehaving observer (a bug in an alarm engine, say) must not fault the
// scan loop or block the remaining observers.
//
// Only the main task fires OnScan — additional Tasks share the tag store
// but not the field I/O phase this hook is defined relative to. A standby
// replica never scans at all (Scan returns immediately, see gate), so it
// never fires OnScan either — correct by construction, since a standby has
// nothing new to observe.
//
// cancel unregisters fn; calling it more than once is a no-op. Cost with
// nobody registered is one slice-length check per scan.
func (r *Runtime) OnScan(fn func(*Tags)) (cancel func()) {
	r.obsMu.Lock()
	id := r.obsNext
	r.obsNext++
	r.onScan = append(r.onScan, onScanEntry{id: id, fn: fn})
	r.obsMu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			r.obsMu.Lock()
			for i, e := range r.onScan {
				if e.id == id {
					r.onScan = append(r.onScan[:i], r.onScan[i+1:]...)
					break
				}
			}
			r.obsMu.Unlock()
		})
	}
}

// fireOnScan runs every registered OnScan observer, in registration order.
// Called from Scan() with scanMu already held (see the call site) — that is
// the whole contract OnScan documents, so this only needs to snapshot the
// observer list (registration can happen concurrently from any goroutine,
// guarded by obsMu, independent of scanMu) and run it.
func (r *Runtime) fireOnScan() {
	r.obsMu.Lock()
	if len(r.onScan) == 0 {
		r.obsMu.Unlock()
		return
	}
	obs := make([]onScanEntry, len(r.onScan))
	copy(obs, r.onScan)
	r.obsMu.Unlock()

	for _, e := range obs {
		r.runOnScan(e.fn)
	}
}

// runOnScan calls one observer with its panic recovered, so a bug in one
// observer cannot fault the scan loop or stop the observers after it.
func (r *Runtime) runOnScan(fn func(*Tags)) {
	defer func() {
		if rec := recover(); rec != nil {
			slog.Default().Error("runtime: OnScan observer panicked",
				"panic", rec)
		}
	}()
	fn(r.tags)
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

// Globals reports every PLC variable the resource's programs bind, unioned
// across the main task and every additional task, with the type each was
// declared as. It is available the moment New returns — before any scan —
// which is what tooling needs.
func (r *Runtime) Globals() map[string]*ir.Type {
	out := r.prog.Globals()
	if out == nil {
		out = map[string]*ir.Type{}
	}
	for _, tr := range r.tasks {
		for name, t := range tr.prog.Globals() {
			out[name] = t
		}
	}
	return out
}

// Types reports every TYPE the resource's programs declare, unioned across
// tasks. Available the moment New returns.
func (r *Runtime) Types() map[string]*ir.Type {
	out := make(map[string]*ir.Type, len(r.types))
	for name, t := range r.types {
		out[name] = t
	}
	return out
}

// unionTypes merges every program's TYPE table. Tasks share a project's
// library files, so they normally agree; two tasks declaring DIFFERENT types
// under one name is an error for the same reason a POU collision is — a tag
// declared `type: Motor` would otherwise mean whichever Motor compiled last.
func unionTypes(main *Program, tasks []*taskRun) (map[string]*ir.Type, error) {
	out := map[string]*ir.Type{}
	from := map[string]string{}
	merge := func(src string, types map[string]*ir.Type) error {
		for name, t := range types {
			prev, seen := out[name]
			if seen && !sameType(prev, t) {
				return fmt.Errorf("TYPE %s is declared differently in %s and %s — "+
					"a tag naming it could not say which one it meant", name, from[name], src)
			}
			out[name], from[name] = t, src
		}
		return nil
	}
	if err := merge(MainTaskName, main.Types()); err != nil {
		return nil, err
	}
	for _, tr := range tasks {
		if err := merge(tr.name, tr.prog.Types()); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// sameType compares two resolved types structurally — the programs are
// compiled separately, so identical declarations produce distinct pointers.
func sameType(a, b *ir.Type) bool {
	switch {
	case a == b:
		return true
	case a == nil || b == nil || a.Kind != b.Kind:
		return false
	}
	switch a.Kind {
	case ir.TypeStruct:
		if a.Struct == nil || b.Struct == nil {
			return a.Struct == b.Struct
		}
		if a.Struct.Name != b.Struct.Name || len(a.Struct.Fields) != len(b.Struct.Fields) {
			return false
		}
		for i := range a.Struct.Fields {
			if a.Struct.Fields[i].Name != b.Struct.Fields[i].Name ||
				!sameType(a.Struct.Fields[i].Type, b.Struct.Fields[i].Type) {
				return false
			}
		}
		return true
	case ir.TypeArray:
		return a.ArrLen == b.ArrLen && a.ArrLoBound == b.ArrLoBound && sameType(a.Elem, b.Elem)
	default:
		return true
	}
}

// GlobalUses reports how the resource's programs use their globals, unioned
// across every task: a tag read by any task is read, written by any task is
// written. Like Globals, it is available the moment New returns.
func (r *Runtime) GlobalUses() ir.GlobalUse {
	out := r.prog.GlobalUses()
	if out.Read == nil {
		out.Read = map[string]bool{}
	}
	if out.Written == nil {
		out.Written = map[string]bool{}
	}
	for _, tr := range r.tasks {
		u := tr.prog.GlobalUses()
		for name := range u.Read {
			out.Read[name] = true
		}
		for name := range u.Written {
			out.Written[name] = true
		}
	}
	return out
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
// loop per additional task. With a Coordinator, a standby's tickers still
// fire but every scan gates out before touching I/O or logic; with a Retain
// store, a saver goroutine flushes changed state alongside the loops.
func (r *Runtime) Run(ctx context.Context) {
	if r.retainStore != nil {
		go r.retainSaver(ctx)
	}
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
	if !r.gate() {
		return
	}
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
// A standby replica returns immediately — suppression by not scanning at
// all, so a stale replica can never write an output.
func (r *Runtime) Scan() {
	if !r.gate() {
		return
	}
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
					// setAny: the driver delivers whole tags under the names
					// the project bound, including the first delivery of an
					// (unseeded) input, which has no value to modify yet.
					r.tags.setAny(name, v)
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

	// 4. observers — the store now holds exactly what this scan left it
	// (program ran, outputs written), and nothing else can be scanning
	// (scanMu is still held). See OnScan's doc comment for the contract.
	r.fireOnScan()

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
