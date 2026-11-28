package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/joyautomation/nautilus/internal/stproject"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/lang/st"
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
	// LoopSleep, when set, replaces a loop's sleep to its next slot: it is
	// called with the task's name (MainTaskName for the main task) and
	// returns false when ctx ended the wait, as the platform sleep does.
	// Nil in production (the wall clock, at no cost); tests use it to
	// starve one loop between scans (#858).
	LoopSleep func(task string, ctx context.Context, until time.Time) bool
	// Retain persists operator state across power cycles — retained tag
	// values and online-edited program sources. See the retain package for
	// the file and ConfigMap stores. Loaded before the first scan, saved on
	// change every 2s, re-loaded on a redundancy takeover.
	Retain retain.Store
	// RetainTags names the tags Retain persists. Empty means every
	// RoleSetpoint tag from Tags — the operator-writable values are exactly
	// what must survive a restart — plus every tag marked Retain (state the
	// logic owns and must keep, TagDef.Retain). Other state starts from its
	// seed on a restart and is not carried to a new leader (which keeps its
	// own copy); inputs re-read from the field. Ignored when Retain is nil.
	RetainTags []string
	// AlwaysWriteOutputs restores the pre-generation behaviour: call the
	// driver's WriteOutputs on EVERY scan, even when not one output tag
	// moved. By default the runtime pushes output CHANGES — it calls
	// WriteOutputs on the first scan, on any scan where an output tag's
	// value differs from the one last handed over, after a failed write,
	// after a redundancy takeover, and on a scan where the driver asked for
	// them (io.OutputsRequester: it forgot a command) — because that is what
	// every driver in tree already reduces the call to internally (see
	// eip.Driver, which diffs against its own last-written set). Set this
	// for a driver that needs a per-scan refresh in its own right: a
	// watchdog that must be re-armed, or a bus whose outputs decay without
	// a rewrite.
	AlwaysWriteOutputs bool
	// HoldOutputsOnSwap keeps a driver swapped in live (ReplaceDriver,
	// #706) from writing the current outputs until they change: its
	// start-up baseline records them. Off (the default) disarms the new
	// driver's baseline, so it starts from the current commands. A single
	// switch while the choice is open (#706, James's decision 1).
	HoldOutputsOnSwap bool
	// InputsWait bounds how long the first scan waits for a driver that
	// is an io.InputsReadier to report its inputs delivered, before it
	// runs on what the driver holds, with a warning (0: DefaultInputsWait).
	// The first scan is the one at start and the first after every
	// takeover: one wait, one setting (#786, #626).
	InputsWait time.Duration
	// TakeoverInputsWait is the earlier name of InputsWait, kept for
	// callers that set it: used when InputsWait is zero.
	//
	// Deprecated: set InputsWait.
	TakeoverInputsWait time.Duration
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
	// Guard bounds the main task while it runs at Priority (prioguard.go);
	// zero fields take the defaults. Tasks have the same field.
	Guard PriorityGuard
	// ShareCPU pins to CPUs even where another program already runs a
	// real-time thread above Priority, which pre-flight otherwise refuses
	// (preflight_linux.go). Tasks have the same field.
	ShareCPU bool
	// Coordinator gates the scan loop for redundancy: a standby replica
	// (IsLeader false) skips scans entirely — no field I/O, no logic — and
	// performs the takeover sequence (reload retained state, reset program
	// frames, zero the dt clocks) on the edge where it becomes leader.
	// leader.Elector satisfies this; nil means standalone, always leader.
	Coordinator Coordinator
	// Channels are the communication channels READ and WRITE blocks run
	// on, by name (see channels.go). Run closes them when it returns.
	Channels []Channel
	// Substitutable names the inputs a substitute may be started on
	// (#707; Runtime.Substitute refuses any other). Each gets a read-only
	// BOOL companion <name>__Substituted, and a non-empty list adds
	// __SubstitutesUnacked, __Substitute_Expired and __Substitute_Fault: declared here, so the
	// programs bind them like any tag. See substitute_expiry.go.
	Substitutable []string
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
	Guard    PriorityGuard // see Options.Guard
	ShareCPU bool          // see Options.ShareCPU
}

// TaskStats is one additional task's health, riding inside ScanStats.
type TaskStats struct {
	Name        string  `json:"name"`
	TargetMs    float64 `json:"targetMs"`
	Count       uint64  `json:"count"`
	LastMs      float64 `json:"lastMs"`
	LogicErrors uint64  `json:"logicErrors"`
	LastError   string  `json:"lastError,omitempty"`
	// Faulting: the task's last scan raised a logic error (a fault episode
	// is open; it closes on the first clean scan).
	Faulting bool `json:"faulting,omitempty"`
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
	// Demoted: the priority was granted and later withdrawn (the priority
	// guard or the kernel's RLIMIT_RTTIME); Error says why. A refusal at
	// start leaves it false.
	Demoted bool `json:"demoted,omitempty"`
	// Guard is the bound a task at a priority runs under (prioguard.go).
	Guard *PriorityGuard `json:"guard,omitempty"`
}

// taskRun is a compiled Task plus its live scheduling state.
type taskRun struct {
	name     string
	prog     *Program
	scan     time.Duration
	dtTag    string
	cpus     []int
	prio     int
	shareCPU bool          // pin beside a higher-priority real-time thread anyway (preflight_linux.go)
	guard    PriorityGuard // with defaults
	watch    overrunWatch  // the loop's own (prioguard.go)
	tid      atomic.Int64  // the loop's thread once it holds the priority

	mu       sync.Mutex
	lastScan time.Time
	stats    TaskStats
	late     lateTracker // under mu, like stats
	fault    faultTrack  // logic-fault transitions for the journal, under mu
}

// TagMeta is HMI-facing tag documentation: a human description and the
// engineering unit of the value ("°C", "%", "L/s", ...).
type TagMeta struct {
	Desc string `json:"desc,omitempty"`
	Unit string `json:"unit,omitempty"`
	// Min and Max bound what an EXTERNAL write may set on a setpoint
	// (#718): an HMI, the API, Sparkplug or OPC UA through Tags.Write. A
	// program's own writes are never refused or clamped. nil is no bound.
	Min *float64 `json:"min,omitempty"`
	Max *float64 `json:"max,omitempty"`
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
	// instances are the instance tags (instance.go): delivered like inputs,
	// but member by member, and written from outside like any tag whose
	// member kind allows it. Not in inputs, so nothing treats one as a
	// driver input whole.
	instances []string
	dtTag     string
	meta      map[string]TagMeta
	params    paramBook // parameters: manifest inits and drift bases (#718)
	types     map[string]*ir.Type
	tasks     []*taskRun
	clock     Clock // nil = wall clock; see clock.go
	// subMu orders substitute starts and restores (substitute.go); subSeq
	// is the highest entry ID seen.
	subMu  sync.Mutex
	subSeq uint64
	// subRoots is Options.Substitutable by NameKey; mono is the clock
	// substitutes are timed on; subEvents hands their events out. See
	// substitute_expiry.go.
	subRoots  map[string]struct{}
	mono      func() time.Duration
	subEvents subEventQueue
	// subPending are retained substitutes waiting for their input to
	// exist (its first read), with how to derive their remainder; guarded
	// by subMu, counted in subPendingN for the scan. See
	// substitute_retain.go.
	subPending         []retain.SubstituteRetain
	subPendingMode     remainderMode
	subPendingLastSeen time.Time
	subPendingN        atomic.Int32
	// subStarts are API starts reserved and being saved, not yet applied
	// (substitute_durable.go); guarded by subMu.
	subStarts []*pendingStart
	// intent is the operator-intent log's side (substitute_intent.go).
	intent intentState
	// loopSleep is Options.LoopSleep: nil sleeps on the wall clock.
	loopSleep func(task string, ctx context.Context, until time.Time) bool

	retainStore retain.Store
	retainTags  []string
	coord       Coordinator
	cpus        []int // main task thread placement, see Options.CPUs
	prio        int
	shareCPU    bool          // see taskRun.shareCPU
	guard       PriorityGuard // main task, with defaults (prioguard.go)
	watch       overrunWatch  // main loop's own
	tid         atomic.Int64  // main loop's thread once it holds the priority

	// retainSkipped remembers which retained tags retainState has already
	// logged as unretainable (see retainable), so the 2-second saver names
	// each one once rather than on every tick.
	retainSkipped sync.Map

	// discardedPrograms is, per task, the source of the last retained online
	// edit a deploy replaced (see loadRetained), carried in every save as
	// retain.State.DiscardedPrograms so the edit stays recoverable. Under mu.
	discardedPrograms map[string]string

	// alwaysWrite / outGen / outSent implement the output push rule (see
	// Options.AlwaysWriteOutputs): outGen is the tag store's output-write
	// generation at the last SUCCESSFUL WriteOutputs, so a scan that finds
	// the same stamp knows the driver already holds exactly these values.
	// Atomic because takeover() clears outSent from outside mainMu.
	alwaysWrite bool
	holdOnSwap  bool
	outGen      atomic.Uint64
	outSent     atomic.Bool
	pushWatches []*pushWatch // ReplaceDriverObserved's, one per swap (#706); under mainMu
	// asker is the driver as an io.OutputsRequester, nil when it is not
	// one: asked every scan whether it wants the whole output set.
	asker nio.OutputsRequester
	// beginner is the driver as an io.ScanBeginner, told each scan is
	// starting before the inputs are read.
	beginner nio.ScanBeginner

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
	// fresh is whether this replica's scan-fed tags are current (see
	// Runtime.standby): set by the main scan once its input read has come
	// back, cleared by gate on every leadership edge and every scan it
	// refuses. Not a mirror of leading: a takeover sets leading before
	// its first read, a held takeover on a new term leaves leading set,
	// and an additional task's gate can take over before the main task has
	// read anything. Atomic, so Quality never waits on leadMu, which a
	// takeover holds through a retain-store load.
	fresh atomic.Bool
	// edges counts gate edges (unfresh), under leadMu: a main scan sets
	// fresh only if the count is unchanged since its own gate passed, so an
	// additional task's gate that steps down or holds a takeover while the
	// main read is in flight is not overwritten when that read lands.
	edges uint64
	// imageSeq numbers the state images (image.go).
	imageSeq imageSeqs
	// sw is a planned switchover's state (switch.go).
	sw switchState
	// edgeWait: a loop's tick on a takeover edge is waiting for a step
	// it started (waitStep, under leadMu); other ticks do not queue.
	edgeWait atomic.Bool
	// flushWait: the edgeWait is for this edge's own flush (waitStep),
	// whose carry may read the leadership snapshot (carry).
	flushWait atomic.Bool
	// leadSnap is ledTerm<<1 | leading, published under leadMu
	// (publishLead) for LeadingTerm, which takes no lock.
	leadSnap atomic.Uint64
	// imgMu guards the round the state image is in (RequestImage,
	// TakeImage): its number, its phase (0 first copies, 1 the re-copy of
	// the faster tasks, 2 taken) and the task that copied last.
	imgMu      sync.Mutex
	imgRound   uint64
	imgPhase   int
	imgSlowest int
	imgStamps  []uint64
	// carried is the tags a state image carries (imagetags.go), fixed in New.
	carried *carrySet
	// ready is the driver as an io.InputsReadier (nil when it is not one),
	// readyWait how long a first scan waits for it, and readySince when the
	// pending wait began (zero when none is). See inputsReady. readySince
	// is guarded by leadMu; waitSince mirrors it (Unix ns, 0: no wait) for
	// Stats, which does not take leadMu.
	ready      nio.InputsReadier
	readyWait  time.Duration
	readySince time.Time
	waitSince  atomic.Int64
	// started: the first scan of an uncoordinated runtime has passed its
	// wait for inputs (or had none to wait for). See gateEdge. mainIn: the
	// main scan has read the inputs since (see tasksMayScan).
	started atomic.Bool
	mainIn  atomic.Bool
	// ledTerm is the coordinator's term (Termed) at the last takeover, 0
	// before the first or without a Termed coordinator. Under leadMu.
	ledTerm uint64

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

	// procsOnce applies the GOMAXPROCS rule once, at the first Run.
	procsOnce sync.Once

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
	// logicFault / ioFault are the main task's fault transitions for the
	// journal (faultlog.go), under mu like the counters they shadow.
	logicFault faultTrack
	ioFault    faultTrack

	// log receives the fault transitions; nil means slog.Default() at the
	// time of the log call, so a process that installs its handler after
	// New still gets them. Tests set it.
	log *slog.Logger

	// untypedComms names the Comms inputs New could not seed because no
	// type was knowable for them — see expandTags and UntypedCommsInputs.
	untypedComms []string

	// pendingLocals are the VAR RETAIN locals loadRetained read, held for
	// takeover to write back once it has reset the frames.
	pendingLocals map[string]map[string]any

	// retainPreloaded: LoadRetained restored the store before Run, so the
	// first takeover skips its own load. Guarded by leadMu.
	retainPreloaded bool

	// iok is the live I/O configuration kept in the retain store (#706):
	// SetIO, SaveRetained, and the leadership edge's re-apply (iokeep.go).
	iok ioKeep
	// pushk keeps the Web Push subscriptions in the retain store (#722).
	pushk pushKeep
	// failk carries a pair's failback state (failbackkeep.go, #710 d2).
	failk failbackKeep
	// strm is a pair's state stream: the takeover flush and the standby's
	// carries (stream.go).
	strm streamSide
	// hold is a pair's takeover hold (hold.go, #710 (b2)).
	hold holdState

	// live is what Liveness reads: Run's loops' scan edges and lifecycle.
	live liveness

	// exprEvents is the expression stage's scratch for the fault
	// transitions of one main scan (exprstage.go), under mainMu.
	exprEvents []exprEvent
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
	// InputsWait is set while the first scan (at start, or after a
	// takeover) waits for the drivers' inputs (#786): nothing scans meanwhile.
	InputsWait *InputsWait `json:"inputsWait,omitempty"`
	Count      uint64      `json:"count"`
	TargetMs   float64     `json:"targetMs"` // configured scan interval
	LastMs     float64     `json:"lastMs"`   // last full scan execution time
	MinMs      float64     `json:"minMs"`
	MaxMs      float64     `json:"maxMs"`
	AvgMs      float64     `json:"avgMs"` // exponentially weighted average

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
	// Procs is GOMAXPROCS as the loops run, and whether the runtime raised
	// it for a task at a priority on a box with few cores (gomaxprocs.go).
	Procs ProcsStats `json:"gomaxprocs"`

	// Retain-store failures surface here the way I/O failures do: a save
	// that keeps erroring is invisible exactly until the restart that
	// needed it, so the dashboard must show it while it is fixable.
	RetainErrors    uint64 `json:"retainErrors,omitempty"`
	LastRetainError string `json:"lastRetainError,omitempty"`
	// RetainDiscardedEdits names the tasks whose retained online edit was
	// dropped since start because a different build had been deployed over
	// it (see loadRetained). Not an error — a deploy replaces an edit, as a
	// download does on a PLC — but an edit that was never pulled into git is
	// gone, and that must be visible, not just logged.
	RetainDiscardedEdits []string `json:"retainDiscardedEdits,omitempty"`

	// Fault counters: input reads that failed (the scan ran on last-known
	// values) and program scans that errored.
	IOErrors    uint64 `json:"ioErrors"`
	LogicErrors uint64 `json:"logicErrors"`
	LastError   string `json:"lastError,omitempty"` // message of the last main-task fault
	// Faulting: the main task's last scan raised a LOGIC error (its program
	// failed; an I/O fault is IOHealthy). It closes on the first clean scan.
	Faulting bool `json:"faulting,omitempty"`
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

	// Exprs is the main task's expression stage (struct wiring's expr:
	// members), nil when it has none.
	Exprs *ExprStats `json:"exprs,omitempty"`
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
	// Substitute companions are tags the programs bind: declared before
	// anything compiles.
	comps, err := declareSubstituteCompanions(&o)
	if err != nil {
		return nil, err
	}
	scope, err := tagScopeFor(o)
	if err != nil {
		return nil, err
	}
	lopts := scope.LowerOpts()
	lopts.Peers = peersOf(o)
	prog, err := CompileWith(o.Program, lopts)
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
		tprog, err := CompileWith(src, lopts)
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
		tr := &taskRun{name: name, prog: tprog, scan: scan, dtTag: td.DtTag, cpus: td.CPUs, prio: td.Priority, shareCPU: td.ShareCPU, guard: td.Guard.withDefaults()}
		tr.watch.limit = tr.guard.Overruns
		tr.stats.Name = name
		tr.stats.Sched = SchedStats{CPUs: td.CPUs, Priority: td.Priority, Guard: guardStat(td.Priority, tr.guard, 0)}
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
	if err := checkBindings(prog, tasks, o.Tags); err != nil {
		return nil, err
	}
	for _, d := range o.Tags {
		if d.Retain && (d.Role == RoleInput || d.Role == RoleOutput) {
			what := "an input comes back from the field"
			if d.Role == RoleOutput {
				what = "an output comes back from the logic"
			}
			return nil, fmt.Errorf("tag %s: retain is for state the logic owns; %s on the first scan, so it is never retained", d.Name, what)
		}
	}
	var held, untyped []string
	if o, held, untyped, err = expandTags(o, types, unionGlobals(prog, tasks)); err != nil {
		return nil, err
	}

	tags := NewTags()
	for name, m := range o.Meta {
		if m.Min != nil || m.Max != nil {
			if tags.ranges == nil {
				tags.ranges = map[string]TagRange{}
			}
			tags.ranges[ir.NameKey(name)] = TagRange{Name: name, Min: m.Min, Max: m.Max}
		}
	}
	seenCh := map[string]bool{}
	for _, c := range o.Channels {
		if c.Name == "" || c.Channel == nil {
			return nil, fmt.Errorf("channel %q: needs a name and a channel", c.Name)
		}
		if seenCh[c.Name] {
			return nil, fmt.Errorf("channel %q declared twice", c.Name)
		}
		seenCh[c.Name] = true
	}
	if len(o.Channels) > 0 {
		tags.comm = newCommHub(o.Channels)
		if o.Coordinator != nil {
			var term func() uint64
			if t, ok := o.Coordinator.(Termed); ok {
				term = t.Term
			}
			tags.comm.setGate(o.Coordinator.IsLeader, term)
			// A pair's elector also gives the send budget and term in one
			// read (design §3.4): channel writes check that and run fenced.
			if sg, ok := o.Coordinator.(interface {
				SendGate() (bool, time.Duration, uint64)
			}); ok {
				tags.comm.setSendGate(sg.SendGate)
			}
		}
	}
	tags.types = tagTypes(o.Tags, types, unionGlobals(prog, tasks))
	instances, err := tags.registerInstances(o, types)
	if err != nil {
		return nil, err
	}
	if err := tags.instanceKeysClaimed(o.Driver); err != nil {
		return nil, err
	}
	// The expr: members (exprstage.go): the main task evaluates them, from
	// its own source, so every install of it compiles them too.
	ex, err := newExprSet(tags, o.Tags)
	if err != nil {
		return nil, err
	}
	if ex != nil {
		prog.ex, prog.store = ex, tags
		stage, err := prog.compileExprs(prog.source, prog.opts)
		if err != nil {
			return nil, err
		}
		ex.stage = stage
	}
	// Set before any scan or goroutine can observe it, so the store's NowMs
	// and the scan's dt always read the same clock.
	tags.clock = o.Clock
	for k, v := range o.Seed {
		// setAny, not Set: a seed CREATES the tag under exactly the name it
		// was configured with. Set's member-path guard is for operator
		// writes, which may only address a tag that already exists.
		tags.setAny(k, v)
	}
	// A typed output with no init: starts at its type's zero, but the
	// driver is not handed that zero until something writes the tag (see
	// expandTags and Tags.hold).
	tags.hold(held)
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
	retainTags := append([]string(nil), o.RetainTags...)
	if o.Retain != nil && len(o.RetainTags) > 0 {
		// An explicit list replaces the setpoint default, but a tag marked
		// Retain is kept whatever the list says (TagDef.Retain).
		for _, d := range o.Tags {
			if d.Retain && d.Name != "" && !slices.Contains(retainTags, d.Name) {
				retainTags = append(retainTags, d.Name)
			}
		}
	}
	if o.Retain != nil && len(retainTags) == 0 {
		// The default retained set is the operator-writable surface: every
		// RoleSetpoint tag. Unmarked state starts from its seed on a restart,
		// inputs re-read from the field, outputs re-derive from logic —
		// setpoints are what a restart loses.
		for _, d := range o.Tags {
			if (d.Role == RoleSetpoint || d.Retain) && d.Name != "" {
				retainTags = append(retainTags, d.Name)
			}
		}
	}
	r := &Runtime{
		prog: prog, tags: tags, driver: o.Driver, scan: o.Scan,
		inputs: o.Inputs, outputs: o.Outputs, instances: instances, dtTag: o.DtTag, meta: o.Meta,
		clock: o.Clock, loopSleep: o.LoopSleep, types: types, tasks: tasks,
		retainStore: o.Retain, retainTags: retainTags, coord: o.Coordinator,
		alwaysWrite: o.AlwaysWriteOutputs, holdOnSwap: o.HoldOutputsOnSwap, untypedComms: untyped,
	}
	if err := r.ownSubstituteCompanions(o.Substitutable, comps); err != nil {
		return nil, err
	}
	r.initParams(o.Tags)
	for _, np := range r.namedPrograms() {
		np.prog.switching = &tags.switching // online edits refused while switching (switch.go)
	}
	r.mono = monoFor(o.Clock)
	if a, ok := o.Driver.(nio.OutputsRequester); ok {
		r.asker = a
	}
	if b, ok := o.Driver.(nio.ScanBeginner); ok {
		r.beginner = b
	}
	if rd, ok := o.Driver.(nio.InputsReadier); ok {
		r.ready = rd
	}
	r.readyWait = o.InputsWait
	if r.readyWait <= 0 {
		r.readyWait = o.TakeoverInputsWait
	}
	if r.readyWait <= 0 {
		r.readyWait = DefaultInputsWait
	}
	r.started.Store(r.ready == nil)
	r.live.loops = append(r.live.loops, &loopLive{name: MainTaskName, period: o.Scan})
	for _, tr := range tasks {
		r.live.loops = append(r.live.loops, &loopLive{name: tr.name, period: tr.scan})
	}
	// Flag the output tags in the store so a write to one stamps the
	// output generation Scan reads (see Tags.markOutputs).
	tags.markOutputs(o.Outputs)
	// The tags a pair's state image carries: every one the field does not
	// provide (imagetags.go).
	dtTags := []string{o.DtTag}
	for _, tr := range tasks {
		dtTags = append(dtTags, tr.dtTag)
	}
	r.carried = buildCarrySet(tags, r.inputs, r.outputs, dtTags)
	for _, np := range r.namedPrograms() {
		np.prog.mu.Lock()
		np.prog.carry = r.carried
		np.prog.carryIdx = r.carried.idxOf(np.prog.writtenKeys())
		np.prog.mu.Unlock()
	}
	r.stats.TargetMs = o.Scan.Seconds() * 1000
	r.late.thresholdUs = lateThresholdS(o.LateThreshold.Seconds(), o.Scan.Seconds()) * 1e6
	r.cpus, r.prio = o.CPUs, o.Priority
	r.guard = o.Guard.withDefaults()
	r.shareCPU = o.ShareCPU
	r.watch.limit = r.guard.Overruns
	r.stats.Sched = SchedStats{CPUs: o.CPUs, Priority: o.Priority, Guard: guardStat(o.Priority, r.guard, 0)}
	r.stats.IOHealthy = true
	r.stats.Recent = make([]float64, 0, historyLen)
	r.stats.Periods = make([]float64, 0, historyLen)
	r.stats.Histogram = make([]int, histBuckets)
	return r, nil
}

// peersOf describes every program of the resource as a peer (#327): a
// program that names another's instance (`t1.Q`, `A.t1.Q`) is told that
// tasks share only tags, instead of only that t1 is undeclared. A source
// that does not parse is left out; its own compile reports it.
func peersOf(o Options) []st.PeerProgram {
	if len(o.Tasks) == 0 {
		return nil
	}
	var peers []st.PeerProgram
	if p, ok := PeerOf(o.Program, MainTaskName); ok {
		peers = append(peers, p)
	}
	for i, td := range o.Tasks {
		src := td.Program
		if len(td.Libraries) > 0 {
			src = stproject.Join(td.Libraries, src)
		}
		name := td.Name
		if name == "" {
			name = fmt.Sprintf("task%d", i+1)
		}
		if p, ok := PeerOf(src, name); ok {
			peers = append(peers, p)
		}
	}
	return peers
}

// tagScopeFor resolves the tags every program sees without declaring
// them: the declared tags (o.Tags), plus each task's dt-tag, which the
// runtime writes every scan as REAL seconds. Types resolve against the
// TYPE tables of the composed sources, read before anything compiles —
// the programs compile against this scope, so it cannot wait for them.
func tagScopeFor(o Options) (TagScope, error) {
	types := map[string]*ir.Type{}
	var srcErr error // first source that would not parse: why a TYPE may be missing
	add := func(src string) {
		ts, err := sourceTypes(src)
		if err != nil && srcErr == nil {
			srcErr = err
		}
		for n, t := range ts {
			if _, _, dup := ir.Lookup(types, n); !dup {
				types[n] = t
			}
		}
	}
	add(o.Program)
	for _, td := range o.Tasks {
		src := td.Program
		if len(td.Libraries) > 0 {
			src = stproject.Join(td.Libraries, src)
		}
		add(src)
	}
	scope, errs := ResolveTagScope(o.Tags, types)
	if len(errs) > 0 {
		if srcErr != nil {
			// A source that does not parse declares no TYPE, so the tag's
			// type cannot resolve: the parse error is the real problem.
			return TagScope{}, srcErr
		}
		return TagScope{}, errs[0]
	}
	dt := func(name string) {
		if name == "" {
			return
		}
		if _, _, ok := ir.Lookup(scope.Implicit, name); !ok {
			scope.Implicit[name] = ir.RealT
		}
	}
	dt(o.DtTag)
	for _, td := range o.Tasks {
		dt(td.DtTag)
	}
	return scope, nil
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
	// An enumeration is its members, names and values: two tasks declaring
	// Mode as (Idle, Run := 10, Fault) and as (Off, On, Tripped) disagree
	// on what Mode#Run means, and OPC UA serves one EnumDefinition per name.
	if (a.Enum == nil) != (b.Enum == nil) {
		return false
	}
	if a.Enum != nil {
		if !ir.SameName(a.Enum.Name, b.Enum.Name) || len(a.Enum.Members) != len(b.Enum.Members) ||
			a.Enum.Default != b.Enum.Default { // and where a tag of it starts
			return false
		}
		for i, m := range a.Enum.Members {
			if !ir.SameName(m.Name, b.Enum.Members[i].Name) || m.Value != b.Enum.Members[i].Value {
				return false
			}
		}
		return true
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

// EarlyRead is one program reading a global before it has written it in
// the scan: Program is its POU name, Lang its source language ("ld",
// "fbd", "sfc" or "st"), Writes says whether it writes the global itself
// (a seal-in) or leaves that to another task, and ReadFirst is how sure the
// walk is and why the read comes first.
type EarlyRead struct {
	Program string
	Lang    string
	Writes  bool
	ir.ReadFirst
}

// ReadBeforeWrite maps each global some program can read before that same
// program has written it in the scan (ir.Program.ReadBeforeWrite) to the
// programs that do, in task order. Tasks start concurrently, so another
// task's write is no guarantee: only a program's own earlier write is.
func (r *Runtime) ReadBeforeWrite() map[string][]EarlyRead {
	out := map[string][]EarlyRead{}
	progs := []*Program{r.prog}
	for _, tr := range r.tasks {
		progs = append(progs, tr.prog)
	}
	for _, p := range progs {
		written := map[string]bool{}
		for name := range p.GlobalUses().Written {
			written[ir.NameKey(name)] = true
		}
		lang := Language(p.Source())
		for name, rf := range p.ReadBeforeWrite() {
			out[name] = append(out[name], EarlyRead{Program: p.POU(), Lang: lang, Writes: written[ir.NameKey(name)], ReadFirst: rf})
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
//
// Run returns once every loop has finished its last scan. With a Retain
// store it saves once more before returning, under the same leader check as
// every tick, so an orderly stop (SIGTERM, Ctrl+C) keeps what changed since
// the saver's last tick; only a hard kill can lose up to saveInterval
// (nautilus#531).
func (r *Runtime) Run(ctx context.Context) {
	// Before any loop takes its thread: a task at a priority must find a
	// free P when it wakes (gomaxprocs.go).
	r.tuneProcs()
	var saved chan []byte
	if r.retainStore != nil {
		saved = make(chan []byte, 1)
		go func() { saved <- r.retainSaver(ctx) }()
	}
	// Before the first scan: a status driver reads this runtime's health.
	r.bindStatsReaders(r.driver)
	// Liveness watches the loops from here on; see liveness.go.
	r.live.state.Store(runRunning)
	defer r.live.state.Store(runStopped)
	if r.tags.comm != nil {
		defer r.tags.comm.close()
	}
	var guarded []*guarded
	var rtLimit time.Duration // the strictest RTLimit asked for (prioguard.go)
	ask := func(g PriorityGuard) {
		if g.RTLimit > 0 && (rtLimit == 0 || g.RTLimit < rtLimit) {
			rtLimit = g.RTLimit
		}
	}
	if r.prio > 0 {
		guarded = append(guarded, r.guardLoop(r.live.loops[0], r.scan, r.guard, &r.tid))
		ask(r.guard)
	}
	for i, tr := range r.tasks {
		if tr.prio > 0 {
			guarded = append(guarded, r.guardLoop(r.live.loops[i+1], tr.scan, tr.guard, &tr.tid))
			ask(tr.guard)
		}
	}
	requestRTLimit(rtLimit) // before any loop takes its priority
	if len(guarded) > 0 {
		go monitorPriority(ctx, guarded, demoteTid)
	}
	var loops sync.WaitGroup
	for i, tr := range r.tasks {
		loops.Add(1)
		live := r.live.loops[i+1]
		go func(tr *taskRun) {
			defer loops.Done()
			defer live.exited.Store(true)
			runLoop(ctx, tr.scan, r.loopClockFor(tr.name), func(due time.Time) {
				live.begin()
				r.scanTaskAt(tr, due)
				live.end()
			}, func(k uint64) {
				tr.mu.Lock()
				tr.late.missed += k
				tr.mu.Unlock()
			}, func() {
				err := applySched(tr.cpus, tr.prio, tr.shareCPU, func(reason string) {
					tr.mu.Lock()
					tr.stats.Sched.Applied, tr.stats.Sched.Error, tr.stats.Sched.Demoted = false, reason, true
					tr.mu.Unlock()
				})
				if err == nil && tr.prio > 0 {
					tr.tid.Store(int64(currentTid()))
				}
				tr.mu.Lock()
				tr.stats.Sched = schedResult(tr.cpus, tr.prio, err)
				tr.stats.Sched.Guard = guardStat(tr.prio, tr.guard, rtLimitInForce())
				tr.mu.Unlock()
				logSched(tr.name, tr.cpus, tr.prio, err)
			})
		}(tr)
	}
	mainLive := r.live.loops[0]
	runLoop(ctx, r.scan, r.loopClockFor(MainTaskName), func(due time.Time) {
		mainLive.begin()
		r.scanAt(due)
		mainLive.end()
	}, func(k uint64) {
		r.mu.Lock()
		r.late.missed += k
		r.mu.Unlock()
	}, func() {
		err := applySched(r.cpus, r.prio, r.shareCPU, func(reason string) {
			r.mu.Lock()
			r.stats.Sched.Applied, r.stats.Sched.Error, r.stats.Sched.Demoted = false, reason, true
			r.mu.Unlock()
		})
		if err == nil && r.prio > 0 {
			r.tid.Store(int64(currentTid()))
		}
		r.mu.Lock()
		r.stats.Sched = schedResult(r.cpus, r.prio, err)
		r.stats.Sched.Guard = guardStat(r.prio, r.guard, rtLimitInForce())
		r.mu.Unlock()
		logSched(MainTaskName, r.cpus, r.prio, err)
	})
	mainLive.exited.Store(true)
	loops.Wait()
	if saved != nil {
		r.mu.Lock()
		errsBefore := r.stats.RetainErrors
		r.mu.Unlock()
		r.saveRetained(<-saved)
		// A failed tick is retried and shows in the diagnostics; the last
		// save has no next tick and the diagnostics end with the process,
		// so its failure is logged.
		r.mu.Lock()
		failed, why := r.stats.RetainErrors > errsBefore, r.stats.LastRetainError
		r.mu.Unlock()
		if failed {
			slog.Error("runtime: the last retain save before stopping failed; changes since the last saved tick are lost",
				"store", r.retainStore.Kind(), "error", why)
		}
	}
}

// guardLoop is the priority monitor's view of one loop (prioguard.go).
func (r *Runtime) guardLoop(l *loopLive, period time.Duration, g PriorityGuard, tid *atomic.Int64) *guarded {
	return &guarded{live: l, bound: guardBound(period, g.ScanMultiple), tid: tid, held: holdsPriority}
}

// guardStat is the guard shown in a task's stats: only with a priority,
// with the kernel bound in force (rtLimit, 0 before Run: omitted).
func guardStat(prio int, g PriorityGuard, rtLimit time.Duration) *PriorityGuard {
	if prio <= 0 {
		return nil
	}
	g.RTLimitMs = float64(rtLimit) / float64(time.Millisecond)
	return &g
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
func runLoop(ctx context.Context, period time.Duration, clk loopClock, scan func(due time.Time), missed func(uint64), setup func()) {
	defer loopThread()()
	if setup != nil {
		setup() // on the locked thread: affinity and priority stick to it
		defer releaseSched()
	}
	runSchedule(ctx, period, clk, scan, missed)
}

// loopClockFor is a loop's clock: the wall clock, with Options.LoopSleep in
// place of its sleep when set.
func (r *Runtime) loopClockFor(task string) loopClock {
	if r.loopSleep == nil {
		return wallLoopClock{}
	}
	return hookLoopClock{task: task, sleep: r.loopSleep}
}

type hookLoopClock struct {
	task  string
	sleep func(string, context.Context, time.Time) bool
}

func (hookLoopClock) Now() time.Time { return time.Now() }
func (h hookLoopClock) SleepUntil(ctx context.Context, deadline time.Time) bool {
	return h.sleep(h.task, ctx, deadline)
}

// loopClock is the time runSchedule keeps its schedule against: the wall
// clock and the platform sleep in production, a virtual one under test so
// the schedule's arithmetic is checked exactly rather than within a
// shared runner's jitter.
type loopClock interface {
	Now() time.Time
	SleepUntil(ctx context.Context, deadline time.Time) bool
}

type wallLoopClock struct{}

func (wallLoopClock) Now() time.Time { return time.Now() }
func (wallLoopClock) SleepUntil(ctx context.Context, deadline time.Time) bool {
	return sleepUntil(ctx, deadline)
}

// runSchedule is runLoop's schedule on a given clock.
func runSchedule(ctx context.Context, period time.Duration, clk loopClock, scan func(due time.Time), missed func(uint64)) {
	start := clk.Now()
	for n := int64(1); ; n++ {
		next := start.Add(time.Duration(n) * period)
		if behind := clk.Now().Sub(next); behind > 0 {
			if k := int64(behind / period); k > 0 {
				n += k
				next = start.Add(time.Duration(n) * period)
				missed(uint64(k))
			}
		}
		if !clk.SleepUntil(ctx, next) {
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
	if !r.gate() || !r.tasksMayScan() || !r.scanEnter() {
		return
	}
	defer r.scanExit()
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

	t1 := time.Now()
	execS := t1.Sub(t0).Seconds()
	msg := errText(err)
	tr.mu.Lock()
	tr.stats.Count++
	tr.stats.LastMs = execS * 1000
	if !first {
		tr.late.record(lateUs(t0, due, dt, tr.scan), execS > tr.scan.Seconds())
	}
	var guardWhy string
	if tr.prio > 0 && !due.IsZero() {
		guardWhy = tr.watch.observe(t1.Sub(t0), tr.scan)
	}
	if err != nil {
		tr.stats.LogicErrors++
		tr.stats.LastError = msg
	}
	ev := tr.fault.observe(msg, t1)
	tr.stats.Faulting = tr.fault.faulting
	tr.mu.Unlock()
	// After the unlock: Stats() takes tr.mu, and a log sink may be slow;
	// the demotion's callback takes tr.mu too.
	r.logFault(tr.name, "logic", ev)
	if guardWhy != "" && demoteTid(int(tr.tid.Load()), guardWhy) {
		slog.Error("runtime: real-time priority withdrawn", "task", tr.name, "reason", guardWhy)
	}
}

// Scan executes one full cycle: read inputs, run the program, write outputs.
// Run calls it on each tick; call it directly to drive the loop yourself
// (tests, a custom scheduler, or a redundancy standby stepping in sync).
// A standby replica returns immediately — suppression by not scanning at
// all, so a stale replica can never write an output.
//
// The first scan (at start, and the first after a takeover) waits for the
// drivers' inputs (Options.InputsWait): until they arrive, Scan returns
// at once having run, sent and counted nothing.
func (r *Runtime) Scan() { r.scanAt(time.Time{}) }

// scanAt is Scan with the slot the scan was due at (zero when the caller
// is not Run's scheduler), for the lateness sample.
func (r *Runtime) scanAt(due time.Time) {
	ok, edge := r.gateEdge()
	if !ok || !r.scanEnter() {
		return
	}
	defer r.scanExit()
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
	if r.beginner != nil {
		r.beginner.BeginScan() // before the read: what is live now reaches this scan
	}
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
	if r.coord == nil && !r.mainIn.Load() {
		r.mainIn.Store(true) // the tasks may scan now (tasksMayScan)
	}
	// The read is back (or there is no driver to read): from here on the
	// tags are this leadership's, and quality is the drivers' again, unless
	// a gate edge (another task's gate) came after this scan's own gate.
	// One atomic load per scan on a steady leader; leadMu only while fresh
	// is false. A failed read sets it too: the inputs are Stale through
	// readOK, and what the program writes this scan is current, as on a
	// standalone controller.
	if r.coord != nil && !r.fresh.Load() {
		r.leadMu.Lock()
		if r.edges == edge {
			r.fresh.Store(true)
		}
		r.leadMu.Unlock()
	}
	if r.dtTag != "" {
		r.tags.SetReal(r.dtTag, dt)
	}
	// Substitutes expire here: after the read, whether or not it
	// succeeded, and before the logic (substitute_expiry.go).
	r.expireSubstitutes()
	t1 := time.Now()

	// The expr: members, before the program reads them (exprstage.go):
	// part of the logic's time.
	r.runExprs(t1)

	// 2. execute one scan
	logicErr := r.prog.Run(r.tags)
	r.pulseSeen()
	t2 := time.Now()

	// 3. outputs — a push of what CHANGED. The generation stamp over the
	// output tags answers "does the driver already hold exactly this?"
	// without reading a single value, which is what keeps a controller with
	// thousands of output bindings from re-serialising all of them every
	// scan to say nothing. Options.AlwaysWriteOutputs opts back out.
	//
	// A driver that forgot a command asks for the whole set
	// (io.OutputsRequester). It is asked on every scan, even one that
	// pushes anyway: the answer is a read-and-clear, so a request this push
	// answers is not answered again on the next scan.
	//
	// Leadership is asked again here, not only at the gate: a scan that
	// hung in its read (or in the logic) may have lost the lease meanwhile
	// to a standby that is now writing, and its stale outputs must not land
	// after the new leader's (#621). Not sent means not held: the next push
	// is the whole set.
	//
	// While a pair's takeover hold holds (hold.go), nothing is pushed: not
	// sent means not held, so the release's push is the whole set.
	//
	// An instance's to: and both: leaves ride in the same push, under their
	// full paths, by the push rules (instance_push.go). A driver asking for
	// every output is also where an event: leaf takes a new baseline.
	pushes := len(r.outputs) > 0 || r.tags.instPush > 0
	if r.driver != nil && pushes && r.coord != nil && !r.coord.IsLeader() {
		r.outSent.Store(false)
	} else if r.holdScan() {
		r.outSent.Store(false)
	} else if r.driver != nil && pushes {
		gen := r.tags.outputGeneration()
		wanted := r.asker != nil && r.asker.OutputsWanted()
		if r.outBuf == nil {
			r.outBuf = make(nio.Values, len(r.outputs)+r.tags.instPush)
		}
		if wanted {
			r.tags.rebaseEvents()
		}
		instMoved := r.tags.instancePush(r.outBuf)
		if r.alwaysWrite || wanted || !r.outSent.Load() || gen != r.outGen.Load() || instMoved {
			// Compound values (UDTs, arrays) cross the seam as ir.Value so
			// typed drivers keep field names and integer widths; scalars
			// stay plain Go values for simple drivers.
			out := r.outBuf
			r.tags.readMany(r.outputs, out)
			if err := r.driver.WriteOutputs(out); err != nil {
				if ioErr == nil {
					ioErr = err
				}
				// A failed write leaves the driver holding who-knows-what:
				// retry the whole set next scan rather than trusting the stamp.
				r.outSent.Store(false)
				r.holdAfterPush() // a child of a composite may have taken it
			} else {
				r.outGen.Store(gen)
				r.outSent.Store(true)
				if r.tags.instPush > 0 {
					r.tags.instancePushed()
				}
				r.deliverPush(out)
				r.holdAfterPush()
			}
		}
	}
	t3 := time.Now()

	// 4. observers — the store now holds what this scan committed (program
	// ran, outputs written), and no other MAIN scan can start (mainMu is
	// still held). See OnScan's doc comment for the contract.
	r.fireOnScan()

	r.recordScan(t0, t1, t2, t3, dt, first, ioErr, logicErr, lateUs(t0, due, dt, r.scan))
	if r.prio > 0 && !due.IsZero() && !first {
		if why := r.watch.observe(t3.Sub(t0), r.scan); why != "" && demoteTid(int(r.tid.Load()), why) {
			slog.Error("runtime: real-time priority withdrawn", "task", MainTaskName, "reason", why)
		}
	}
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
//
// It also decides the scan's fault transitions for the journal (faultlog.go)
// under the same lock, and writes them after releasing it.
func (r *Runtime) recordScan(t0, t1, t2, t3 time.Time, periodS float64, first bool, ioErr, logicErr error, lateUs float64) {
	ioMsg, logicMsg := errText(ioErr), errText(logicErr)
	r.mu.Lock()
	r.recordScanLocked(t0, t1, t2, t3, periodS, first, ioMsg, logicMsg, lateUs)
	ioEv := r.ioFault.observe(ioMsg, t3)
	logicEv := r.logicFault.observe(logicMsg, t3)
	r.stats.Faulting = r.logicFault.faulting
	r.mu.Unlock()
	r.logFault(MainTaskName, "I/O", ioEv)
	r.logFault(MainTaskName, "logic", logicEv)
}

// recordScanLocked is recordScan's bookkeeping; the caller holds r.mu. The
// errors arrive as their messages ("" for none), built once by the caller.
func (r *Runtime) recordScanLocked(t0, t1, t2, t3 time.Time, periodS float64, first bool, ioMsg, logicMsg string, lateUs float64) {
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

	if ioMsg != "" {
		s.IOErrors++
		s.IOHealthy = false
		s.LastIOError = ioMsg
	} else {
		s.IOHealthy = true
	}
	if logicMsg != "" {
		s.LogicErrors++
		s.LastError = logicMsg
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
	// Copies that are never nil: before the first scan (and on a standby
	// that doesn't scan) the rings are empty, and JSON must say [] — a null
	// here threw in the kit's ScanDiagnostics (found by naut check's render).
	s.Recent = append([]float64{}, r.stats.Recent...)
	s.Periods = append([]float64{}, r.stats.Periods...)
	s.Histogram = append([]int{}, r.stats.Histogram...)
	if len(r.stats.RetainDiscardedEdits) > 0 {
		s.RetainDiscardedEdits = append([]string(nil), r.stats.RetainDiscardedEdits...)
	}
	s.Lateness = r.late.snapshot()
	r.mu.Unlock()
	if ns := r.waitSince.Load(); ns != 0 {
		s.InputsWait = &InputsWait{Since: time.Unix(0, ns), WaitingFor: r.inputsPending()}
	}
	for _, tr := range r.tasks {
		tr.mu.Lock()
		ts := tr.stats
		ts.Lateness = tr.late.snapshot()
		s.Tasks = append(s.Tasks, ts)
		tr.mu.Unlock()
	}
	if ex := r.prog.ex; ex != nil {
		s.Exprs = ex.stats()
	}
	return s
}

// Meta returns the HMI tag documentation given at construction (may be nil).
func (r *Runtime) Meta() map[string]TagMeta { return r.meta }

// Inputs returns the driver-bound input tag names.
func (r *Runtime) Inputs() []string { return r.inputs }

// UntypedCommsInputs names the inputs a communications driver delivers
// (TagDef.Comms) that New could NOT seed, because nothing said what type
// they are: no type:, no program declaring them, no type from the driver's
// binding. Each stays absent until its first delivery, exactly as every
// input used to. No program reads one (a program that did would have
// declared its type), so this is a report, not a fault — `naut check`
// lists them so a project can add the type: and get the seed.
func (r *Runtime) UntypedCommsInputs() []string {
	return append([]string(nil), r.untypedComms...)
}

// Outputs returns the driver-bound output tag names.
func (r *Runtime) Outputs() []string { return r.outputs }
