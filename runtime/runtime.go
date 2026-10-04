package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
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
	// editor, LSP, and `naut pull` compose a project directory, so
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
	// reporting task). All tasks share the tag store and run concurrently:
	// each scan snapshots its externals in, executes privately, and
	// commits what changed as one unit (see scanview.go), so every scan
	// sees a consistent store and a fast task never waits behind a slow
	// one. The MAIN task (Program/Scan above) owns field I/O and remains
	// the online-edit target; task programs are fixed at composition.
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
	// AlwaysWriteOutputs restores the pre-generation behaviour: call the
	// driver's WriteOutputs on EVERY scan, even when not one output tag
	// moved. By default the runtime pushes output CHANGES — it calls
	// WriteOutputs on the first scan, on any scan where an output tag's
	// value differs from the one last handed over, after a failed write,
	// and after a redundancy takeover — because that is what every driver
	// in tree already reduces the call to internally (see eip.Driver, which
	// diffs against its own last-written set). Set this for a driver that
	// needs a per-scan refresh in its own right: a watchdog that must be
	// re-armed, or a bus whose outputs decay without a rewrite.
	AlwaysWriteOutputs bool
	// LateThreshold is how far past its target a scan may START before it
	// counts as late in Lateness (see ScanStats.Lateness). It applies to
	// the main task and to every Task that does not set its own. Zero
	// means a tenth of the task's scan interval: 10 ms on a 100 ms task,
	// 100 µs on a 1 ms task.
	LateThreshold time.Duration
	// CPUs pins the main task's scan thread to these CPUs (Linux
	// sched_setaffinity); empty leaves it to the scheduler. Priority > 0
	// moves that thread to SCHED_FIFO at that priority (1–99), which needs
	// CAP_SYS_NICE or an rtprio rlimit; a request the OS refuses is logged
	// and reported in ScanStats.Sched, and the task runs normally. Tasks
	// have the same two fields. See docs/design/realtime.md for what the
	// kernel must be told (isolcpus, nohz_full, irqaffinity) before a
	// pinned core is actually quiet.
	CPUs     []int
	Priority int
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
	// LateThreshold overrides Options.LateThreshold for this task.
	LateThreshold time.Duration
	// CPUs and Priority pin this task's thread — see Options.CPUs.
	CPUs     []int
	Priority int
}

// TaskStats is one additional task's health, riding inside ScanStats.
type TaskStats struct {
	Name        string  `json:"name"`
	TargetMs    float64 `json:"targetMs"`
	Count       uint64  `json:"count"`
	LastMs      float64 `json:"lastMs"`
	LogicErrors uint64  `json:"logicErrors"`
	LastError   string  `json:"lastError,omitempty"`
	// Lateness is this task's wake-up timing — see Lateness.
	Lateness Lateness `json:"lateness"`
	// Sched is what was asked of the OS scheduler for this task's thread
	// and whether it was granted.
	Sched SchedStats `json:"sched"`
}

// SchedStats reports a task's thread placement: the CPUs and SCHED_FIFO
// priority configured, whether the OS granted them (Applied), and the
// refusal if not — surfaced here because a controller that was MEANT to be
// pinned and silently is not would be the worst kind of wrong.
type SchedStats struct {
	CPUs     []int  `json:"cpus,omitempty"`
	Priority int    `json:"priority,omitempty"`
	Applied  bool   `json:"applied"`
	Error    string `json:"error,omitempty"`
}

// taskRun is a compiled Task plus its live scheduling state.
type taskRun struct {
	name  string
	prog  *Program
	scan  time.Duration
	dtTag string
	cpus  []int
	prio  int

	mu       sync.Mutex
	lastScan time.Time
	stats    TaskStats
	late     lateTracker // under mu, like stats
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
	cpus        []int // main task thread placement, see Options.CPUs
	prio        int

	// alwaysWrite / outGen / outSent implement the output push rule (see
	// Options.AlwaysWriteOutputs): outGen is the tag store's output-write
	// generation at the last SUCCESSFUL WriteOutputs, so a scan that finds
	// the same stamp knows the driver already holds exactly these values.
	// Atomic because takeover() clears outSent from outside mainMu.
	alwaysWrite bool
	outGen      atomic.Uint64
	outSent     atomic.Bool

	// inBuf is the delivery map an io.BatchReader driver refills each scan
	// instead of allocating one, and outBuf the map the output push is
	// assembled in (drivers copy what they keep; see io.Driver). Touched
	// only from Scan, under mainMu.
	inBuf  nio.Values
	outBuf nio.Values

	// readOK is whether the LAST input read succeeded — the runtime's own
	// contribution to per-tag quality (see Quality). Distinct from
	// ScanStats.IOHealthy, which a failed output WRITE also clears: a write
	// that did not land says nothing about how old the readings are, and
	// calling every input Stale over it would cry wolf on every screen.
	// Atomic because Quality is answered from the server's goroutine.
	// Starts false: before the first scan, nothing has been read.
	readOK atomic.Bool

	// leadMu guards the leadership edge so exactly one scan performs the
	// takeover sequence when this replica becomes leader. See retain.go.
	leadMu  sync.Mutex
	leading bool

	// alarms is the alarm engine, as retained operator state — nil unless
	// SetAlarms registered one. Its own mutex because loadRetained reads it
	// from inside takeover, which already holds leadMu.
	alarmMu sync.Mutex
	alarms  retain.AlarmRetainer

	// mainMu serializes the MAIN task's Scan — its I/O phases and the
	// observers that follow — against a second caller of Scan. Additional
	// tasks do not take it: each Program.Run isolates its own scan
	// (scanview.go), so tasks never wait on one another.
	mainMu sync.Mutex

	// obsMu guards onScan/obsNext independently of mainMu: OnScan may be
	// called (registration or cancel) from any goroutine at any time,
	// including while a scan is in flight. mainMu still serializes the
	// CALLS to registered observers — see fireOnScan.
	obsMu   sync.Mutex
	obsNext uint64
	onScan  []onScanEntry

	mu       sync.Mutex
	lastScan time.Time
	stats    ScanStats
	late     lateTracker // the main task's, under mu like stats
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

	// Lateness is the main task's wake-up timing: late-scan and overrun
	// counters, the worst and percentile lateness, and a log-spaced
	// histogram of it — cumulative since start. The soft-real-time view
	// of the loop; PeriodMs/JitterMs above are the live one.
	Lateness Lateness `json:"lateness"`
	// Sched is the main task's thread placement — see SchedStats.
	Sched SchedStats `json:"sched"`

	// Retain-store failures surface here the way I/O failures do: a save
	// that keeps erroring is invisible exactly until the restart that
	// needed it, so the dashboard must show it while it is fixable.
	RetainErrors    uint64 `json:"retainErrors,omitempty"`
	LastRetainError string `json:"lastRetainError,omitempty"`

	// Fault counters: input reads that failed (the scan ran on last-known
	// values) and program scans that errored.
	IOErrors    uint64 `json:"ioErrors"`
	LogicErrors uint64 `json:"logicErrors"`
	LastError   string `json:"lastError,omitempty"` // message of the last main-task fault
	// DivZero counts divisions (and MODs) by zero evaluated by any task
	// since start. The VM yields 0 and the scan keeps running, so this is
	// the only trace a bad divisor leaves — the Logix S:V of nautilus.
	DivZero     uint64 `json:"divZero"`
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
		tr := &taskRun{name: name, prog: tprog, scan: scan, dtTag: td.DtTag, cpus: td.CPUs, prio: td.Priority}
		tr.stats.Name = name
		tr.stats.Sched = SchedStats{CPUs: td.CPUs, Priority: td.Priority}
		tr.stats.TargetMs = scan.Seconds() * 1000
		lt := td.LateThreshold
		if lt <= 0 {
			lt = o.LateThreshold
		}
		tr.late.thresholdUs = lateThresholdS(lt.Seconds(), scan.Seconds()) * 1e6
		tasks = append(tasks, tr)
	}

	types, err := unionTypes(prog, tasks)
	if err != nil {
		return nil, err
	}
	if o, err = expandTags(o, types, unionGlobals(prog, tasks)); err != nil {
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
		alwaysWrite: o.AlwaysWriteOutputs,
	}
	// Flag the output tags in the store so a write to one stamps the
	// output generation Scan reads (see Tags.markOutputs).
	tags.markOutputs(o.Outputs)
	r.stats.TargetMs = o.Scan.Seconds() * 1000
	r.late.thresholdUs = lateThresholdS(o.LateThreshold.Seconds(), o.Scan.Seconds()) * 1e6
	r.cpus, r.prio = o.CPUs, o.Priority
	r.stats.Sched = SchedStats{CPUs: o.CPUs, Priority: o.Priority}
	r.stats.IOHealthy = true
	r.stats.Recent = make([]float64, 0, historyLen)
	r.stats.Periods = make([]float64, 0, historyLen)
	r.stats.Histogram = make([]int, histBuckets)
	return r, nil
}

// OnScan registers fn to run at the end of every main-task Scan() — after
// the program has executed and, if a driver is bound, after outputs have
// been written to it — so fn observes the tag store with this scan's
// results committed. Registered observers run in registration order,
// synchronously, still holding mainMu: the next main scan cannot start
// while fn runs. The store only ever holds whole, committed scans (see
// scanview.go), so fn never sees one half-done; an additional task may
// commit between two of fn's reads, though — fn that wants one instant
// takes it with Snapshot/SnapshotInto, one lock. fn must NOT block (it
// shares the scan budget: a slow observer is a slow scan) and must NOT
// write field tags (inputs for this cycle already landed in the read
// phase; a write here would be invisible to the program that just ran and
// only take effect next scan, which is not what "post-scan" means). Reads
// are fine and safe — fn runs with mainMu held but NOT t.mu, so
// Tags.ReadPath/ReadGlobal/Snapshot etc. all work without deadlocking.
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
// Called from Scan() with mainMu already held (see the call site) — that is
// the whole contract OnScan documents, so this only needs to snapshot the
// observer list (registration can happen concurrently from any goroutine,
// guarded by obsMu, independent of mainMu) and run it.
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

// unionGlobals merges every program's bound PLC variables (Program.Globals
// is the deep set, so a library block's VAR_EXTERNAL counts) with their
// declared types — what
// expandTags seeds an untyped tag's init against. Two programs declaring one
// tag differently is `naut check`'s report to make; here the last wins,
// as in Runtime.Globals.
func unionGlobals(main *Program, tasks []*taskRun) map[string]*ir.Type {
	out := map[string]*ir.Type{}
	for name, t := range main.Globals() {
		out[name] = t
	}
	for _, tr := range tasks {
		for name, t := range tr.prog.Globals() {
			if prev, dup := out[name]; dup && t != nil && t.Kind == ir.TypeFB && prev != nil && prev.Kind == ir.TypeFB {
				// Tasks run concurrently and an FB instance is shared
				// identity, not a copied value (scanview.go): two tasks
				// stepping the same instance race on its state.
				slog.Warn("runtime: function-block instance bound by more than one task; tasks run concurrently and will race on its state",
					"tag", name, "task", tr.name)
			}
			out[name] = t
		}
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
			runLoop(ctx, tr.scan, func(due time.Time) { r.scanTaskAt(tr, due) }, func(k uint64) {
				tr.mu.Lock()
				tr.late.missed += k
				tr.mu.Unlock()
			}, func() {
				err := applySched(tr.cpus, tr.prio)
				tr.mu.Lock()
				tr.stats.Sched = schedResult(tr.cpus, tr.prio, err)
				tr.mu.Unlock()
				logSched(tr.name, tr.cpus, tr.prio, err)
			})
		}(tr)
	}
	runLoop(ctx, r.scan, r.scanAt, func(k uint64) {
		r.mu.Lock()
		r.late.missed += k
		r.mu.Unlock()
	}, func() {
		err := applySched(r.cpus, r.prio)
		r.mu.Lock()
		r.stats.Sched = schedResult(r.cpus, r.prio, err)
		r.mu.Unlock()
		logSched(MainTaskName, r.cpus, r.prio, err)
	})
}

// schedResult is the SchedStats for a placement request and its outcome.
func schedResult(cpus []int, prio int, err error) SchedStats {
	s := SchedStats{CPUs: cpus, Priority: prio, Applied: err == nil && (len(cpus) > 0 || prio > 0)}
	if err != nil {
		s.Error = err.Error()
	}
	return s
}

// logSched reports a placement request's outcome: nothing when nothing was
// asked, Info when granted, Error when refused — loudly, because a task
// that was meant to be pinned and is not is a configuration that lies.
func logSched(task string, cpus []int, prio int, err error) {
	if len(cpus) == 0 && prio == 0 {
		return
	}
	if err != nil {
		slog.Error("runtime: task scheduling request refused", "task", task, "cpus", cpus, "priority", prio, "error", err)
		return
	}
	slog.Info("runtime: task thread placed", "task", task, "cpus", cpus, "priority", prio)
}

// runLoop calls scan once per period on an ABSOLUTE schedule — slot n is
// due at start + n·period, whatever happened to slot n−1 — until ctx is
// done. A scan that wakes late does not push the next one later: the
// following sleep is just shorter. A loop that falls more than a whole
// period behind (an overrun, a stall) skips the slots it has lost, reports
// them through missed, and resumes on the next future slot rather than
// firing a burst of catch-up scans. scan receives the slot it is running
// for, so its lateness can be measured against the schedule itself. The
// sleep itself is the platform's: sleep_linux.go, sleep_other.go.
//
// This replaces time.Ticker, whose ticks at a 1 ms period were measured
// arriving one wake-up latency after the PREVIOUS tick rather than on the
// schedule: the schedule drifted ~65 µs a tick and 4–6 % of ticks were
// dropped on an idle desktop. See docs/design/realtime.md, finding F1.
func runLoop(ctx context.Context, period time.Duration, scan func(due time.Time), missed func(uint64), setup func()) {
	defer loopThread()()
	if setup != nil {
		setup() // on the locked thread: affinity and priority stick to it
	}
	start := time.Now()
	for n := int64(1); ; n++ {
		next := start.Add(time.Duration(n) * period)
		if behind := -time.Until(next); behind > 0 {
			if k := int64(behind / period); k > 0 {
				n += k
				next = start.Add(time.Duration(n) * period)
				missed(uint64(k))
			}
		}
		if !sleepUntil(ctx, next) {
			return
		}
		scan(next)
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
func (r *Runtime) scanTask(tr *taskRun) { r.scanTaskAt(tr, time.Time{}) }

// scanTaskAt is scanTask with the slot the scan was due at (zero when the
// caller is not Run's scheduler), for the lateness sample.
func (r *Runtime) scanTaskAt(tr *taskRun, due time.Time) {
	if !r.gate() {
		return
	}
	t0 := time.Now()
	now := r.now(t0) // dt basis: the injected clock under test, else t0
	tr.mu.Lock()
	dt := tr.scan.Seconds()
	first := tr.lastScan.IsZero()
	if !first {
		dt = now.Sub(tr.lastScan).Seconds()
	}
	tr.lastScan = now
	tr.mu.Unlock()

	// No resource-wide lock: Program.Run isolates this scan on its own
	// (snapshot in, commit out), so a task never waits for another.
	if tr.dtTag != "" {
		r.tags.SetReal(tr.dtTag, dt)
	}
	err := tr.prog.Run(r.tags)

	execS := time.Since(t0).Seconds()
	tr.mu.Lock()
	tr.stats.Count++
	tr.stats.LastMs = execS * 1000
	if !first {
		tr.late.record(lateUs(t0, due, dt, tr.scan), execS > tr.scan.Seconds())
	}
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
func (r *Runtime) Scan() { r.scanAt(time.Time{}) }

// scanAt is Scan with the slot the scan was due at (zero when the caller
// is not Run's scheduler), for the lateness sample.
func (r *Runtime) scanAt(due time.Time) {
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

	// One main scan at a time (I/O buffers, observers); additional tasks
	// run alongside, each isolated by its own Program.Run.
	r.mainMu.Lock()
	defer r.mainMu.Unlock()

	// 1. inputs — on a read failure the scan runs on last-known values.
	var ioErr error
	if r.driver != nil {
		var in nio.Values
		var err error
		if br, ok := r.driver.(nio.BatchReader); ok {
			// The driver can refill our map: no per-scan allocation of the
			// whole input set. See io.BatchReader.
			if r.inBuf == nil {
				r.inBuf = make(nio.Values, len(r.inputs))
			}
			in, err = r.inBuf, br.ReadInputsInto(r.inBuf)
		} else {
			in, err = r.driver.ReadInputs()
		}
		ioErr = err
		r.readOK.Store(err == nil)
		if err == nil {
			// setMany: the driver delivers whole tags under the names the
			// project bound, including the first delivery of an (unseeded)
			// input, which has no value to modify yet. One lock for the
			// whole delivery, and a re-delivered identical value is not a
			// write at all — see Tags' "Write generations".
			r.tags.setMany(r.inputs, in)
		}
	}
	if r.dtTag != "" {
		r.tags.SetReal(r.dtTag, dt)
	}
	t1 := time.Now()

	// 2. execute one scan
	logicErr := r.prog.Run(r.tags)
	t2 := time.Now()

	// 3. outputs — a push of what CHANGED. The generation stamp over the
	// output tags answers "does the driver already hold exactly this?"
	// without reading a single value, which is what keeps a controller with
	// thousands of output bindings from re-serialising all of them every
	// scan to say nothing. Options.AlwaysWriteOutputs opts back out.
	if r.driver != nil && len(r.outputs) > 0 {
		gen := r.tags.outputGeneration()
		if r.alwaysWrite || !r.outSent.Load() || gen != r.outGen.Load() {
			// Compound values (UDTs, arrays) cross the seam as ir.Value so
			// typed drivers keep field names and integer widths; scalars
			// stay plain Go values for simple drivers.
			if r.outBuf == nil {
				r.outBuf = make(nio.Values, len(r.outputs))
			}
			out := r.outBuf
			r.tags.readMany(r.outputs, out)
			if err := r.driver.WriteOutputs(out); err != nil {
				if ioErr == nil {
					ioErr = err
				}
				// A failed write leaves the driver holding who-knows-what:
				// retry the whole set next scan rather than trusting the stamp.
				r.outSent.Store(false)
			} else {
				r.outGen.Store(gen)
				r.outSent.Store(true)
			}
		}
	}
	t3 := time.Now()

	// 4. observers — the store now holds what this scan committed (program
	// ran, outputs written), and no other MAIN scan can start (mainMu is
	// still held). See OnScan's doc comment for the contract.
	r.fireOnScan()

	r.recordScan(t0, t1, t2, t3, dt, first, ioErr, logicErr, lateUs(t0, due, dt, r.scan))
}

// lateUs is one lateness sample: the scan's start against its slot when
// Run supplied one, else period − target (see Lateness).
func lateUs(t0, due time.Time, periodS float64, target time.Duration) float64 {
	if !due.IsZero() {
		if l := t0.Sub(due); l > 0 {
			return float64(l) / 1e3
		}
		return 0
	}
	return (periodS - target.Seconds()) * 1e6
}

// recordScan folds one cycle's timings into the diagnostics.
func (r *Runtime) recordScan(t0, t1, t2, t3 time.Time, periodS float64, first bool, ioErr, logicErr error, lateUs float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := &r.stats

	scanMs := t3.Sub(t0).Seconds() * 1000
	s.Count++
	s.DivZero = r.tags.DivZeroCount()
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
		r.late.record(lateUs, scanMs > s.TargetMs)
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
		s.LastError = logicErr.Error()
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
	s.Lateness = r.late.snapshot()
	r.mu.Unlock()
	for _, tr := range r.tasks {
		tr.mu.Lock()
		ts := tr.stats
		ts.Lateness = tr.late.snapshot()
		s.Tasks = append(s.Tasks, ts)
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
