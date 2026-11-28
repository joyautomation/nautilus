package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/retain"
)

// Coordinator decides whether this replica owns the scan loop. leader.Elector
// satisfies it; nil means standalone — always the leader. The runtime polls
// rather than subscribing so the elector needs no callback plumbing, and a
// standby costs one atomic read per tick.
type Coordinator interface {
	IsLeader() bool
}

// Termed is a Coordinator that also says which leadership this is: Term
// changes whenever another replica may have led since this one last did
// (leader.Elector.Term). With it, a replica whose gate closed and opens
// again in the same term resumes instead of taking over (#621): a scan that
// stalled for a couple of seconds pauses the outputs, not the program's
// state. Without it, every opening of the gate is a takeover.
type Termed interface {
	Term() uint64
}

// saveInterval paces the retain saver. Two seconds bounds how much operator
// input a hard kill can lose (an orderly stop saves once more, see Run),
// while keeping a busy HMI session from turning every slider drag into a
// ConfigMap write.
const saveInterval = 2 * time.Second

// SetAlarms registers the alarm engine as retained operator state, so
// acknowledgement and shelf ride along in the store the setpoints already
// use.
//
// A method rather than an Options field for the same reason OnScan is one:
// the engine is built AFTER runtime.New — it reads the tag store, which
// does not exist until then. Call it before Run; the first takeover is
// what restores what the last leader saved.
//
// Nil is legal and is the default: a project with no alarms writes no
// alarms section, and a store written before alarms existed loads
// unchanged (retain.State.Alarms is omitempty).
func (r *Runtime) SetAlarms(a retain.AlarmRetainer) {
	r.alarmMu.Lock()
	r.alarms = a
	r.alarmMu.Unlock()
}

// SyncedAlarmRestorer is an alarm retainer that restores from a
// replicated log of operator intent (a redundant pair, #710 PR 3b-2c):
// takeover tells it whether the log's catch-up succeeded, so an unsynced
// takeover carries no acknowledgement (they re-annunciate instead).
type SyncedAlarmRestorer interface {
	RestoreAlarmsSynced(m map[string]retain.AlarmRetain, synced bool)
}

// alarmRetainer reads the registered engine. Its own mutex, not leadMu:
// loadRetained runs inside takeover, which already holds leadMu.
func (r *Runtime) alarmRetainer() retain.AlarmRetainer {
	r.alarmMu.Lock()
	defer r.alarmMu.Unlock()
	return r.alarms
}

// LoadRetained restores the retain store now, before Run, for a controller
// with no coordinator. Call it after SetAlarms and before the tag API
// serves: otherwise the API answers with the manifest's seeds until the
// first scan's takeover loads the store, and a write that lands in that
// window is answered 204 and then quietly overwritten by the load
// (nautilus#531's startup half). The first takeover then skips its own
// load; frames are still reset and VAR RETAIN locals still restored there.
//
// With a coordinator it does nothing: a replica's takeover must re-read
// the store, because another replica may have led (and saved) since.
func (r *Runtime) LoadRetained() {
	if r.coord != nil || r.retainStore == nil {
		return
	}
	r.leadMu.Lock()
	defer r.leadMu.Unlock()
	if r.leading || r.retainPreloaded {
		return
	}
	if err := r.loadRetained(); err != nil {
		r.retainLoadFailed(err)
	}
	r.retainPreloaded = true
}

// gate is the first thing every scan does. It answers "should this replica
// scan?", and on the standby→leader edge it performs the takeover sequence
// before any logic runs. With no coordinator and no retain store it is a
// single nil check — existing compositions pay nothing.
func (r *Runtime) gate() bool {
	ok, _ := r.gateEdge()
	return ok
}

// gateEdge is gate, plus the count of gate edges (see unfresh) as of this
// answer: the main scan sets fresh only if no edge came after its own gate.
func (r *Runtime) gateEdge() (bool, uint64) {
	if r.coord == nil && r.retainStore == nil {
		if r.started.Load() {
			return true, 0
		}
		// The first scan of a lone controller waits for its drivers'
		// inputs (#786), as a takeover does: no logic, no outputs, and no
		// scan is counted, timed or clocked until it runs.
		r.leadMu.Lock()
		defer r.leadMu.Unlock()
		if !r.started.Load() && !r.inputsReady() {
			return false, 0
		}
		r.started.Store(true)
		return true, 0
	}
	if r.edgeWait.Load() && !mutRuntime("edge_wait_blocks_others") {
		// Another loop's tick holds the takeover edge, waiting a moment
		// for a step it started (waitStep): nothing scans before the edge
		// is taken, so answer that at once rather than wait on leadMu.
		return false, 0
	}
	lead := r.coord == nil || r.coord.IsLeader()
	r.leadMu.Lock()
	defer r.leadMu.Unlock()
	termed, _ := r.coord.(Termed)
	// Every edge below that takes over or resumes (from standby, the same
	// term resumed, or a new term this replica never saw the gate close
	// for) waits for live inputs first (#626): the loop keeps ticking, and
	// nothing below runs until they are, or the wait is over. A steady
	// leader never asks.
	if !lead {
		r.endWait()
		r.ioReset()
		r.resetIntentSync()
		r.flushReset()
		r.handBackReset()
	} else if !r.leading || termed != nil && termed.Term() != r.ledTerm {
		// A takeover (not the same leadership resumed) first re-applies
		// the kept I/O configuration (#706), so the inputs it then waits
		// for come from the applied addresses and the first output goes
		// to them (ioReady). The operator-intent log's catch-up
		// (substitute_intent.go) runs alongside both, asked every tick, so
		// the edge waits for the longer, not the sum.
		resume := !r.leading && termed != nil && r.ledTerm != 0 && termed.Term() == r.ledTerm
		// A pair's takeover first flushes the newest state image into the
		// retain store (#710 S1b), before anything below reads the store:
		// ioReady keeps the store it reads for takeover's own load.
		var term uint64
		if termed != nil {
			term = termed.Term()
		}
		if mutRuntime("stream_flush_after_io_read") {
			// the store read for this edge first, the flush after it
			if !resume && !r.ioReady() {
				r.unfresh()
				return false, r.edges
			}
		}
		if !resume && !r.flushReady(term) {
			r.unfresh()
			return false, r.edges
		}
		// Once the edge has read the store (the flush, the I/O restore's
		// first read): a
		// takeover that would go cold on an unreadable store first asks
		// whether to hand the lead back to the peer (#710 S2c-3), before
		// the waits for live inputs and the log's catch-up, which a
		// hand-back would spend for nothing. (The log's catch-up runs
		// alongside all of these, as before: it reads, it never sends.)
		io := resume || r.ioReady()
		// The hand-back needs the edge's first read of the store only, not
		// the I/O restore's retries of a failed one (about a second).
		hb := resume || r.iokRead() && r.handBackReady(term)
		stored := io && hb
		ready := stored && r.inputsReady()
		synced := resume || r.intentReady()
		// The lease and its term are sampled again before taking over: this
		// tick, or another that waited on leadMu, may have waited for a
		// step meanwhile (#710 d1e, Fable). A lock-free read.
		if r.coord != nil && !mutRuntime("wait_keeps_lead_sample") && (!r.coord.IsLeader() || termed != nil && termed.Term() != term) {
			r.unfresh()
			return false, r.edges
		}
		if !ready || !synced {
			r.unfresh() // nothing scans: the tags are frozen
			return false, r.edges
		}
	}
	switch {
	case lead && !r.leading && termed != nil && r.ledTerm != 0 && termed.Term() == r.ledTerm:
		// The same leadership, resumed: this replica closed its gate (its
		// scans stalled) and opens it again on a lease nobody else held in
		// between. Nothing to reload and nothing stale to discard, so no
		// takeover: frames, timers, SFC steps and retained state carry on.
		// The drivers get the whole output set on this scan, as after any
		// gap in what was sent; an event: leaf takes a new baseline
		// (instance_push.go).
		r.outSent.Store(false)
		r.tags.rebaseEvents()
		// No unfresh: the step-down before this resume cleared fresh and
		// counted an edge, so a main read that began under the leadership
		// before it cannot set fresh either.
		r.leading = true
		r.publishLead()
	case lead && (!r.leading || termed != nil && termed.Term() != r.ledTerm):
		// Leadership from standby, or a new term this replica never saw
		// the gate close for (lost and regained between two scans): another
		// replica may have led meanwhile.
		r.unfresh() // until this leadership's first read
		if !mutRuntime("takeover_keeps_switching") {
			// A switchover of an earlier leadership is over (its resign
			// stood this controller down). Not at the stand-down itself: a
			// gate that closes for a tick and reopens in the same term (a
			// late renewal) resumes the switchover, scans still stopped
			// (Fable M1).
			r.switchEnded()
		}
		r.takeover()
		r.ioReset()
		r.flushReset()
		r.handBackReset()
		r.leading = true
		if termed != nil {
			r.ledTerm = termed.Term()
		}
		r.publishLead()
	case !lead:
		if mutRuntime("resume_ends_switching") {
			r.switchEnded()
		}
		if r.leading {
			// Stepping down: forces belong to the active controller only.
			r.dropForces("lost leadership")
			// A takeover a program never scanned after is spent: a resume
			// in the same term is no takeover (Sonnet, #1350).
			r.clearTakeovers()
		}
		r.leading = false
		r.publishLead()
		r.unfresh()
	}
	return lead, r.edges
}

// unfresh marks the scan-fed tags not current and counts a gate edge, so a
// main scan whose read began before it cannot mark them current when the
// read lands (see Scan). Called under leadMu.
func (r *Runtime) unfresh() {
	r.fresh.Store(false)
	r.edges++
}

// DefaultInputsWait is how long a first scan waits for the drivers'
// inputs when Options.InputsWait is zero.
const DefaultInputsWait = 2 * time.Second

// DefaultTakeoverInputsWait is the earlier name of DefaultInputsWait.
//
// Deprecated: use DefaultInputsWait.
const DefaultTakeoverInputsWait = DefaultInputsWait

// InputsWait is a first scan's wait for the drivers' inputs, as Stats
// reports it while it lasts.
type InputsWait struct {
	// Since is when the wait began.
	Since time.Time `json:"since"`
	// WaitingFor names the drivers whose inputs have not arrived.
	WaitingFor []string `json:"waitingFor,omitempty"`
}

// inputsReady answers, before a first scan, whether the driver's inputs
// have arrived (io.InputsReadier). A first scan is the one at start (#786)
// and the first of every takeover: the takeover resets every frame so
// warm-start logic runs against live field values, and a driver that held
// no session while standing by has none yet (#626); at start nothing has
// been read at all. While it is false, gate reports "do not scan": no
// frame reset, no logic, no outputs, but the scan loop goes on ticking, so
// the replica's scans stay live for the lease. After readyWait it gives up
// and the scan runs on what the driver holds, with a warning naming the
// drivers still waiting; their inputs read NotConnected until they arrive.
// Called under leadMu.
func (r *Runtime) inputsReady() bool {
	if r.ready == nil {
		return true
	}
	now := r.now(time.Now()) // a virtual Clock bounds the wait too (clock.go)
	what := "takeover"
	if r.coord == nil {
		what = "first scan"
	}
	if r.ready.InputsReady() {
		if !r.readySince.IsZero() {
			slog.Info("runtime: inputs live; starting the "+what, "waited", now.Sub(r.readySince).Round(time.Millisecond))
		}
		r.endWait()
		return true
	}
	if r.readySince.IsZero() {
		r.readySince = now
		r.waitSince.Store(now.UnixNano())
		slog.Info("runtime: holding the "+what+" until the drivers' inputs are live", "up_to", r.readyWait, "waiting_for", strings.Join(r.inputsPending(), ", "))
		return false
	}
	if now.Sub(r.readySince) < r.readyWait {
		return false
	}
	slog.Warn("runtime: inputs still not live; starting the "+what+" on the inputs the drivers hold", "waited", now.Sub(r.readySince).Round(time.Millisecond), "waiting_for", strings.Join(r.inputsPending(), ", "))
	r.endWait()
	return true
}

// tasksMayScan answers, for an additional task past its gate, whether the
// main scan has read the inputs since the gate last opened: only the main
// scan reads the drivers, so a task that ran first would scan the seeds
// (#786; Fable). It applies when there is a wait for inputs at all (an
// io.InputsReadier driver): a lone controller holds its tasks until the
// main scan's first read, a pair's new leader until its first read as
// leader (fresh). A task gated this way runs nothing and counts nothing.
func (r *Runtime) tasksMayScan() bool {
	if r.ready == nil {
		return true
	}
	if r.coord != nil {
		return r.fresh.Load()
	}
	return r.mainIn.Load()
}

// endWait clears a first scan's wait. Called under leadMu.
func (r *Runtime) endWait() {
	r.readySince = time.Time{}
	r.waitSince.Store(0)
}

// inputsPending names the drivers whose inputs have not arrived: a set's
// children by name (io.InputsPending), a lone driver by its package.
func (r *Runtime) inputsPending() []string {
	if p, ok := r.driver.(nio.InputsPending); ok {
		return p.InputsPending()
	}
	if r.ready != nil && !r.ready.InputsReady() {
		name := strings.TrimPrefix(fmt.Sprintf("%T", r.driver), "*")
		if i := strings.IndexByte(name, '.'); i > 0 {
			name = name[:i]
		}
		return []string{name}
	}
	return nil
}

// takeover runs once per acquisition of leadership (including process start,
// which is an acquisition from nothing). Three steps, in order:
//
//  1. Re-read the retain store — the OLD leader may have accepted retunes or
//     online edits while this replica idled, and those must win over both the
//     manifest's seeds and anything stale in this process.
//  2. Discard every program's retained frame — the ST warm-start path must
//     run against live field values, not a VM state frozen hours ago.
//  3. Zero the scan clocks — the first dt must be the scan target, not the
//     wall-clock gap since this replica last led, which would slam every
//     integrator in the resource.
//
// Process state is deliberately NOT replicated between replicas: config
// travels through the retain store, process state re-derives from the field.
func (r *Runtime) takeover() {
	// Forces are never carried across a takeover (see force.go): the standby
	// never saw this replica's force table, and a leader that lost and
	// regained the lease must not quietly resume holding values nobody may
	// still be watching. Dropped BEFORE the retained load so a restored
	// value is not captured as a forced tag's "actual".
	//
	// Without a coordinator there is no standby and no lease: this takeover
	// is the process's first scan, and runs once. A force already in the
	// table was made against this very process (its API answers before the
	// first scan) by someone still watching it, so it stays — dropping it
	// would answer the POST with 200 and then quietly undo it.
	//
	// Substitutes (substitute.go) are NOT dropped: they are operator intent
	// meant to run the plant through a bad sensor, and a takeover that put
	// the bad reading back into the logic is the bump they exist to prevent
	// (#707).
	if r.coord != nil {
		// A new leadership has written no command and read no peer value
		// (instance_push.go), its first one included: a write taken before
		// this replica led is not this leadership's. Without a coordinator
		// nothing came before the first scan, and a write the API took
		// before it stands.
		r.tags.newPushLeadership()
		r.dropForces("takeover")
		// READ/WRITE requests the last leadership queued are not this
		// one's to send.
		r.tags.comm.newLeadership()
	}
	// LoadRetained already restored the store before the API answered
	// (standalone only): loading it again here would undo a write made
	// in between (nautilus#531's startup half).
	// This edge's own read of the store may already have failed
	// (ioReady): then what loads below is no store at all.
	bad := r.strm.storeBad.Swap(false) && !mutRuntime("stream_store_bad_forgotten")
	r.strm.loadFailed.Store((r.iok.loadErr || bad) && !mutRuntime("stream_failed_load_streams"))
	if r.retainPreloaded {
		r.retainPreloaded = false
	} else if r.retainStore != nil {
		if err := r.loadRetained(); err != nil {
			r.retainLoadFailed(err)
			// The store failed, but a pair's log still holds the alarm
			// state: restore it, or the engine stays frozen at an earlier
			// leadership and the log never counts it live (#1033 CI).
			r.restorePairAlarms()
		}
	} else {
		// A pair with no retain: block still restores its log's alarm
		// view (#710 PR 3b-2c): the log, not a store, holds the acks.
		r.restorePairAlarms()
	}
	// A substitute's remainder is computed once, from its wall-clock
	// until: the leadership that timed it may have been another replica's,
	// or this one's before a spell as standby (substitute_expiry.go).
	r.retimeSubstitutes()
	r.intentAfterTakeover() // a failed log catch-up: the fault, and a retry
	// A pair's takeover goes on from its standby's copy of the old
	// leader's state when it may (hot.go, #710 S2): the image's frames,
	// by value, timers moved to this clock, and its tags over the retain
	// load. Otherwise it is cold, as below.
	hot := false
	inherit := time.Duration(-1) // the hold a hot takeover inherits (hold.go)
	if r.strm.flush.Load() != nil {
		h, why, had := r.strm.hot, r.strm.hotWhy, r.strm.hadImage
		r.strm.hot, r.strm.hotWhy, r.strm.hadImage = nil, "", false
		if h != nil && h.holding {
			inherit = h.holdAge + h.Age
		}
		hot = r.hotTakeover(h, why, had)
	}
	// The takeover hold (#710 (b2)): started, inherited or ended here,
	// before the first scan latches TAKEOVER_HOLD().
	r.holdOnTakeover(hot, inherit)
	// TAKEOVER_SCAN() and HOT_TAKEOVER() for each program's next scan, and
	// TAKEOVERS(), when this leadership took over from the peer rather
	// than started (#710 (b)): the coordinator knows which.
	defer func() { r.markTakeovers(hot) }()
	if !hot {
		r.prog.ResetFrame()
		for _, tr := range r.tasks {
			tr.prog.ResetFrame()
		}
		// Process state re-derives from the field, except what a program
		// declared VAR RETAIN: it goes back into the fresh frames.
		if err := r.restoreLocals(); err != nil {
			r.noteRetainError(err)
		}
	}
	// This replica has never written to the field, whatever its predecessor
	// left in the store: the first scan as leader pushes the FULL output set
	// rather than trusting a generation stamp the old leader's driver knows
	// nothing about. See Options.AlwaysWriteOutputs. After a spell as
	// standby the push commands: a driver that tracks the write gate
	// (modbus, opcua-client) has seen it closed meanwhile, so it sends every
	// output, even one equal to what it sent when it last led (#600), and
	// even when this is its first push since start, which on a plain start
	// (leader from the first scan, gate never closed) is its baseline
	// instead (#606).
	r.outSent.Store(false)
	r.mu.Lock()
	r.lastScan = time.Time{}
	r.mu.Unlock()
	for _, tr := range r.tasks {
		tr.mu.Lock()
		tr.lastScan = time.Time{}
		tr.mu.Unlock()
	}
}

// dropForces clears the force table, logging what was dropped.
func (r *Runtime) dropForces(why string) {
	if n := r.tags.UnforceAll(); n > 0 {
		slog.Warn("runtime: forces dropped", "count", n, "reason", why)
	}
}

// loadRetained applies the store's state: retained tag values, then any
// online-edited program sources — an edit only when this binary embeds the
// program it was edited from (a restart or failover of the same build); a
// different build was a deploy, and the deploy wins. Tag names outside the retained set are
// ignored — the store is not a back door for writing arbitrary tags. A
// program that no longer compiles (a library moved under it) is reported
// but does not block the rest of the load; the current program keeps
// running, which is Swap's contract.
// restorePairAlarms restores a pair's alarm view from its operator-intent
// log with no retained state to go by, at a takeover that has no store or
// whose store could not be read. Nothing without a pair's log.
func (r *Runtime) restorePairAlarms() {
	if sa, ok := r.alarmRetainer().(SyncedAlarmRestorer); ok && r.coord != nil && r.intentLog() != nil {
		sa.RestoreAlarmsSynced(nil, r.intent.synced)
	}
}

func (r *Runtime) loadRetained() error {
	st, err := r.loadStore()
	if err != nil {
		// Unreadable, not merely a value that no longer fits: this
		// leadership runs on initial values (RetainLoadFailed).
		r.strm.loadFailed.Store(!mutRuntime("stream_failed_load_streams"))
		// The failback state is unknown, not "never set": the keeper must
		// not read it as enabled (#710 d2). A hot takeover that carries the
		// image's retain section hands it over again, known.
		r.failbackLoaded(nil, mutRuntime("failback_unknown_as_known"))
		return err
	}
	return r.applyRetained(st)
}

// applyRetained is loadRetained's application of a state: the store's,
// or a hot takeover's image's when the store could not be read (hot.go).
func (r *Runtime) applyRetained(st retain.State) error { return r.applyRetainedFrom(st, true) }

// applyRetainedCarried is applyRetained of a hot takeover's image over an
// unreadable own store (hot_install.go): the values the image carries,
// not the alarm and substitute steps, which the pair's log owns and the
// takeover already ran (a nil section would clear the substitute load
// fault the failed store read raised).
func (r *Runtime) applyRetainedCarried(st retain.State) error { return r.applyRetainedFrom(st, false) }

func (r *Runtime) applyRetainedFrom(st retain.State, full bool) error {
	allowed := make(map[string]bool, len(r.retainTags))
	for _, n := range r.retainTags {
		allowed[n] = true
	}
	r.params.load(st.ParamBases, st.ParamEdits, st.ParamCommitted)
	r.pushLoaded(st.Push)
	// A takeover whose store already failed this edge (a corrupt file the
	// standby's carry or the edge's I/O read moved aside, so this load
	// reads it empty) does not know the failback state either (Fable on
	// d2): an image carried in later hands it over, known.
	if full && r.strm.loadFailed.Load() && !mutRuntime("failback_unknown_as_known") {
		r.failbackLoaded(nil, false)
	} else {
		r.failbackLoaded(st.Failback, true)
	}
	var errs []error
	for name, v := range st.Tags {
		if !allowed[name] {
			continue
		}
		// A retained STRUCT or ARRAY tag: JSON collapsed it to a plain
		// map[string]any or []any (see retainState), which setAny has no
		// case for — a flat write must never conjure a tag from nothing
		// (see Tags.Set), so a composite there is silently dropped.
		// Restoring configuration is different: the tag already exists,
		// seeded from the manifest (with its StructDef and declared type
		// attached) before takeover ever runs, so it is read-modify-written
		// through ir.SetFieldTyped exactly like an operator's whole-tag
		// write (Tags.SetPath) is. An empty path assigns the whole value: a
		// mapping merges member by member onto a struct, a list element by
		// element onto an array from its declared lower bound — recursively,
		// so an array of structs and an ARRAY[..,..] (an array of arrays)
		// restore the same way. Every leaf is coerced to the kind it
		// already has, so an INT element stays an INT and an enumeration
		// element comes back named.
		//
		// A retained list LONGER than the declared array (the project
		// shrank it) is an error and the tag keeps its seed: dropping the
		// tail would be a guess about which elements still mean what. A
		// shorter one (the array grew) restores its prefix and the new
		// elements keep their seeds.
		switch v.(type) {
		case map[string]any, []any:
			cur, err := r.tags.ReadGlobal(name)
			if err != nil {
				errs = append(errs, fmt.Errorf("retained tag %q: %w", name, err))
				continue
			}
			nv, err := ir.SetFieldTyped(cur, r.tags.TypeOf(name), nil, v, "tag "+name)
			if err != nil {
				errs = append(errs, fmt.Errorf("retained tag %q: %w", name, err))
				continue
			}
			r.tags.setAny(name, nv)
			continue
		}
		// JSON collapses every number to float64. If the tag is currently an
		// integer kind (seeded from the manifest), restore it as one — the
		// store must not silently retype a DINT setpoint into a REAL, nor a
		// TIME into an INT, nor drop an enumeration's member name. The
		// coercion is the same one an element of a retained array gets.
		if f, isNum := v.(float64); isNum {
			if cur, err := r.tags.ReadGlobal(name); err == nil &&
				(cur.Kind == ir.TypeInt || cur.Kind == ir.TypeTime) {
				nv, err := ir.SetFieldTyped(cur, r.tags.TypeOf(name), nil, f, "tag "+name)
				if err != nil {
					errs = append(errs, fmt.Errorf("retained tag %q: %w", name, err))
					continue
				}
				r.tags.setAny(name, nv)
				continue
			}
		}
		// setAny: a restore puts a declared tag's own value back under its
		// own name — it is configuration, not an operator write, so it does
		// not go through Set's member-path guard.
		r.tags.setAny(name, v)
	}
	// Operator alarm state — acknowledgement and shelf — goes back to the
	// engine before the first Evaluate of this leadership term. Doing it
	// here rather than at construction is what makes a standby takeover
	// correct: takeover() re-reads the store on the standby→leader edge, so
	// a failover cannot resurrect hundreds of acked alarms as unacked.
	if a := r.alarmRetainer(); a != nil && full {
		if sa, ok := a.(SyncedAlarmRestorer); ok && r.intentLog() != nil {
			// A pair: its operator-intent log holds the alarm state, and
			// says whether this takeover's catch-up succeeded (#710).
			sa.RestoreAlarmsSynced(st.Alarms, r.intent.synced)
		} else {
			a.RestoreAlarms(st.Alarms)
			// The flood in progress (#1249): a standalone controller's
			// alone. A pair's log carries its own (PairAlarms).
			if fr, ok := a.(retain.AlarmFloodRetainer); ok {
				fr.RestoreFlood(st.AlarmFlood)
			}
		}
	}
	// Earlier discards ride along until a newer one for the same task
	// replaces them, so a save never loses a recoverable edit.
	if len(st.DiscardedPrograms) > 0 {
		r.mu.Lock()
		if r.discardedPrograms == nil {
			r.discardedPrograms = map[string]string{}
		}
		for task, src := range st.DiscardedPrograms {
			if _, newer := r.discardedPrograms[task]; !newer {
				r.discardedPrograms[task] = src
			}
		}
		r.mu.Unlock()
	}
	for task, src := range st.Programs {
		p := r.TaskProgram(task)
		if p == nil {
			errs = append(errs, fmt.Errorf("retained program for unknown task %q", task))
			continue
		}
		edit, base, build := sourceHash(src), st.ProgramBases[task], p.BootHash()
		switch {
		case edit == build:
			// The build already contains the edit (it was pulled into git
			// and shipped): nothing to swap. The program is clean, so the
			// next save leaves the entry out.
			slog.Info("runtime: retained online edit is in this build; clearing it",
				"task", task, "edit", edit)
			continue
		case base != build:
			// A deploy wins, as a download does on a PLC: the binary's
			// program for this task changed since the edit was made, so the
			// edit is dropped and the deployed program runs. The next save
			// leaves the entry out. An entry with no base (a store written
			// before bases were recorded) cannot prove it was made against
			// this build, so it is treated as one that was not.
			r.discardRetainedEdit(task, src, base, build)
			continue
		case p.Hash() == edit:
			continue // already running it (a takeover in the same process)
		}
		if err := p.Swap(src); err != nil {
			errs = append(errs, fmt.Errorf("retained program for task %q: %w", task, err))
		}
	}
	// VAR RETAIN locals are held for takeover, which writes them back after
	// it resets the frames (restoreLocals).
	r.pendingLocals = st.Locals
	// Substitutes (substitute_retain.go): their own block, last.
	if full {
		r.loadSubstitutes(st.Substitutes)
	}
	return errors.Join(errs...)
}

// restoreLocals writes the VAR RETAIN locals loadRetained read back into
// whichever program each task now runs (a reapplied online edit replaced
// the frame), by name, after takeover has reset every frame: a local the
// program no longer declares RETAIN is skipped, one whose type changed
// keeps its initial value and is reported.
func (r *Runtime) restoreLocals() error {
	locals := r.pendingLocals
	r.pendingLocals = nil
	var errs []error
	for task, vals := range locals {
		p := r.TaskProgram(task)
		if p == nil {
			continue // the task is gone; its locals went with it
		}
		errs = append(errs, p.RestoreLocals(vals)...)
	}
	return errors.Join(errs...)
}

// discardRetainedEdit drops a retained online edit because a different
// build was deployed: it keeps the source as the task's discarded program
// (saved under discardedPrograms, never applied) so it can be recovered,
// logs where to find it, and names the task in the diagnostics
// (ScanStats.RetainDiscardedEdits) so the dashboard and /api/state show it.
func (r *Runtime) discardRetainedEdit(task, src, base, build string) {
	reason := "the deployed program for this task changed since the edit was made"
	if base == "" {
		reason = "the edit was saved without its base build, so it may predate this deploy"
	}
	slog.Warn("runtime: retained online edit discarded: a different build was deployed",
		"task", task, "edit", sourceHash(src), "editBase", base, "build", build, "reason", reason,
		"recover", fmt.Sprintf("the edited source is kept in the %s retain store under discardedPrograms[%q]",
			r.retainStore.Kind(), task))
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.discardedPrograms == nil {
		r.discardedPrograms = map[string]string{}
	}
	r.discardedPrograms[task] = src
	for _, t := range r.stats.RetainDiscardedEdits {
		if t == task {
			return
		}
	}
	r.stats.RetainDiscardedEdits = append(r.stats.RetainDiscardedEdits, task)
}

// retainSaver flushes changed state on a fixed cadence, only while leading —
// the ConfigMap store is last-writer-wins, and leadership is what makes that
// safe. Change detection is by comparison against the last written encoding
// rather than dirty flags: writes reach the tag store from operator APIs,
// field drivers, and logic alike, and a comparison catches all of them
// without threading a flag through every path. A failed save keeps the old
// encoding so the next tick retries. On the context's end it returns the
// encoding the store holds, for Run's last save after the final scan.
func (r *Runtime) retainSaver(ctx context.Context) []byte {
	t := time.NewTicker(saveInterval)
	defer t.Stop()
	var lastSaved []byte
	for {
		select {
		case <-ctx.Done():
			return lastSaved
		case <-t.C:
			lastSaved = r.saveRetained(lastSaved)
		case req := <-r.carryRequests():
			err := r.carry(req)
			if err == nil {
				// The store now holds an image's state, not what this
				// replica last saved as leader: its next save as leader
				// writes, whatever it compares equal to.
				lastSaved = nil
			}
			req.reply <- err
		case req := <-r.saveRequests():
			var err error
			lastSaved, err = r.saveRetainedErr(lastSaved)
			if errors.Is(err, retain.ErrConflict) {
				// Another writer changed the store since this replica read
				// it; the store has re-read its version, and the leader's
				// state wins.
				lastSaved, err = r.saveRetainedErr(lastSaved)
			}
			req <- err
		}
	}
}

// saveRetained is one saver tick: skip as standby, skip when nothing
// changed, keep the old encoding on failure so the next tick retries.
// Returns the encoding of what the store now holds.
func (r *Runtime) saveRetained(lastSaved []byte) []byte {
	enc, _ := r.saveRetainedErr(lastSaved)
	return enc
}

// saveRetainedErr is saveRetained, and says why it did not save: as a
// standby (ErrNotLeading), or the store's error (SaveRetained).
func (r *Runtime) saveRetainedErr(lastSaved []byte) ([]byte, error) {
	if r.coord != nil && !r.coord.IsLeader() {
		return lastSaved, ErrNotLeading
	}
	// Leading by the lease is not enough: until this runtime's own takeover
	// has loaded the store (gate, before its next scan), its tags hold seeds
	// and its frames are fresh, and saving them would overwrite what the
	// takeover is about to load.
	//
	// A takeover edge waiting for its flush holds leadMu, and that flush's
	// carry goes through this same saver goroutine: a save parked on the
	// lock would hold the carry behind it for the whole wait. The edge's
	// takeover has not loaded the store yet, so this save is one to skip
	// anyway; the next period saves (#710 d1e, CI on #1359).
	if r.flushWait.Load() && !mutRuntime("save_waits_on_edge") {
		return lastSaved, ErrNotLeading
	}
	r.leadMu.Lock()
	leading, term := r.leading, r.ledTerm
	r.leadMu.Unlock()
	if !leading {
		return lastSaved, ErrNotLeading
	}
	st := r.retainState()
	if r.strm.flush.Load() != nil && term != 0 && !mutRuntime("stream_leader_saves_unmarked") {
		// A pair's leader marks its file with its term (#710 S1b): as a
		// standby later, it takes no image of a leadership before this.
		st.Stream = &retain.StreamPos{Term: term}
	}
	enc, err := json.Marshal(st)
	if err != nil {
		return lastSaved, err
	}
	if string(enc) == string(lastSaved) {
		return lastSaved, nil
	}
	if err := r.retainStore.Save(st); err != nil {
		r.noteRetainError(err)
		return lastSaved, err
	}
	if !mutRuntime("retain_memory_only_forgotten") {
		r.strm.memOnly.Store(false) // the store holds this leadership's values now
	}
	if k := r.ioKeeper(); k != nil {
		k.IOSaved(st.IO) // durable now: its sequence is the store's
	}
	r.substitutesSaved() // a stop or ack that missed its own save is kept now
	return enc, nil
}

// retainState assembles what this controller persists: the retained tags'
// current values, and the source of every program that has drifted from
// what it compiled from (an online edit not yet pulled into git), with the
// hash of the build program it was edited from (see loadRetained). A struct
// (UDT) tag persists as its plain JSON form (see plainNumeric) — the same
// shape GET /api/state already sends an HMI — and an ARRAY tag as a JSON
// list of its elements, lower bound first (an array of structs as a list of
// mappings, a multi-dimensional array as nested lists). loadRetained writes
// both back through ir.SetFieldTyped, so a restart or a failover doesn't
// lose a faceplate's setpoints because they live inside a UDT, or a
// setpoint table because it is an array.
//
// An array or struct with a function-block instance inside it (an ARRAY
// OF TON, a STRUCT with a TON member) is not persisted: an FB is identity,
// not a value, and the load path cannot write one back, so persisting it
// would be a promise loadRetained could not keep. Such a tag is skipped
// and named in the log once (see retainable). The tag model refuses one as
// a manifest tag's type, and as the type a program declares for a manifest
// tag, so no project reaches this; the skip is defense in depth.
//
// Compatibility: arrays add no field and no version to retain.State — they
// are one more JSON shape under Tags. A store written before arrays were
// retained has no entry for them, and each array keeps its seed. A store
// written by this version and read by an older one hands the old
// loadRetained a []any, which its setAny ignores (irValue has no case for
// one): the array keeps its seed there too, and nothing fails.
func (r *Runtime) retainState() retain.State {
	st := retain.State{}
	for _, name := range r.retainTags {
		// The ACTUAL value, not a forced one: forces are not retained
		// (force.go), and persisting a forced setpoint's value would bring
		// the force back after a restart in all but name. For an array with
		// a forced element, that is the element's actual value too: the
		// force table holds the whole tag's actual beside the forced view.
		v, err := r.tags.readActual(name)
		if err != nil {
			continue
		}
		switch v.Kind {
		case ir.TypeArray, ir.TypeStruct:
			if !retainable(v) {
				if _, logged := r.retainSkipped.LoadOrStore(name, true); !logged {
					slog.Warn("runtime: retained tag not saved: it holds a function-block instance, which has no value to persist",
						"tag", name)
				}
				continue
			}
			fallthrough
		case ir.TypeBool, ir.TypeReal, ir.TypeInt, ir.TypeTime, ir.TypeString:
			if st.Tags == nil {
				st.Tags = map[string]any{}
			}
			st.Tags[name] = plainNumeric(v) // an enumeration as its integer: it reloads as a number
		}
	}
	st.ParamBases, st.ParamEdits, st.ParamCommitted = r.params.snapshot(st.Tags)
	record := func(task string, p *Program) {
		if !p.Dirty() {
			return
		}
		if st.Programs == nil {
			st.Programs = map[string]string{}
			st.ProgramBases = map[string]string{}
		}
		st.Programs[task] = p.Source()
		st.ProgramBases[task] = p.BootHash()
	}
	record(MainTaskName, r.prog)
	for _, tr := range r.tasks {
		record(tr.name, tr.prog)
	}
	// VAR RETAIN locals, per task. The live frame: a forced value lives in
	// the tag store, never in a program's own variables.
	locals := func(task string, p *Program) {
		if m := p.RetainedLocals(); len(m) > 0 {
			if st.Locals == nil {
				st.Locals = map[string]map[string]any{}
			}
			st.Locals[task] = m
		}
	}
	locals(MainTaskName, r.prog)
	for _, tr := range r.tasks {
		locals(tr.name, tr.prog)
	}
	r.mu.Lock()
	if len(r.discardedPrograms) > 0 {
		st.DiscardedPrograms = make(map[string]string, len(r.discardedPrograms))
		for task, src := range r.discardedPrograms {
			st.DiscardedPrograms[task] = src
		}
	}
	r.mu.Unlock()
	// Only ack and shelf: active and return-to-normal re-derive from the
	// field on the next scan, and a retained "active" would be a claim
	// about the plant made by a file. The map is omitted when empty, so a
	// project with no alarms — or one whose alarms are all normal — writes
	// exactly what it wrote before this existed.
	if a := r.alarmRetainer(); a != nil {
		if m := a.RetainedAlarms(); len(m) > 0 {
			st.Alarms = m
		}
		if fr, ok := a.(retain.AlarmFloodRetainer); ok {
			st.AlarmFlood = fr.RetainedFlood()
		}
	}
	// The live I/O configuration an apply changed (#706): nil while what
	// runs is the manifest, so a project that never applied writes what it
	// wrote before.
	if k := r.ioKeeper(); k != nil {
		st.IO = k.RetainedIO()
	}
	// Web Push subscriptions, sealed: nothing secret in the store (#722).
	if k := r.pushKeeper(); k != nil {
		st.Push = k.RetainedPush()
	} else {
		// No keeper (push is off here, or has no key): what the store
		// holds is another replica's, and a save must not erase it.
		st.Push = r.pushHeld()
	}
	st.Failback = r.failbackHeld() // a pair's failback state (#710 d2)
	// Substitutes (substitute_retain.go): their own block, last.
	st.Substitutes = r.retainedSubstitutes()
	return st
}

// retainable reports whether an array or struct round-trips through the retain
// store: scalars, and structs and arrays made of them, at any depth. A
// function-block instance (or a value with no kind yet) anywhere inside
// does not — loadRetained could not write it back.
func retainable(v ir.Value) bool {
	switch v.Kind {
	case ir.TypeBool, ir.TypeReal, ir.TypeInt, ir.TypeTime, ir.TypeString:
		return true
	case ir.TypeArray:
		for _, e := range v.Arr {
			if !retainable(e) {
				return false
			}
		}
		return true
	case ir.TypeStruct:
		for _, f := range v.Fld {
			if !retainable(f) {
				return false
			}
		}
		return true
	}
	return false
}

// noteRetainError folds a store failure into the diagnostics the dashboard
// already shows, the same way I/O errors surface.
// retainLoadFailed reports a retain store that could not be loaded: the
// tags start from their initial values, which an operator must hear about
// at once, not find later as a counter. (The file store has already moved
// the unusable file aside, so the first save does not overwrite it.)
func (r *Runtime) retainLoadFailed(err error) {
	slog.Error("runtime: retained values could not be loaded; the tags start from their initial values",
		"store", r.retainStore.Kind(), "error", err)
	r.noteRetainError(err)
	r.substituteLoadFailed()
}

func (r *Runtime) noteRetainError(err error) {
	r.mu.Lock()
	r.stats.RetainErrors++
	r.stats.LastRetainError = err.Error()
	r.mu.Unlock()
}
