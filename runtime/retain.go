package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

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

// alarmRetainer reads the registered engine. Its own mutex, not leadMu:
// loadRetained runs inside takeover, which already holds leadMu.
func (r *Runtime) alarmRetainer() retain.AlarmRetainer {
	r.alarmMu.Lock()
	defer r.alarmMu.Unlock()
	return r.alarms
}

// gate is the first thing every scan does. It answers "should this replica
// scan?", and on the standby→leader edge it performs the takeover sequence
// before any logic runs. With no coordinator and no retain store it is a
// single nil check — existing compositions pay nothing.
func (r *Runtime) gate() bool {
	if r.coord == nil && r.retainStore == nil {
		return true
	}
	lead := r.coord == nil || r.coord.IsLeader()
	r.leadMu.Lock()
	defer r.leadMu.Unlock()
	termed, _ := r.coord.(Termed)
	switch {
	case lead && !r.leading && termed != nil && r.ledTerm != 0 && termed.Term() == r.ledTerm:
		// The same leadership, resumed: this replica closed its gate (its
		// scans stalled) and opens it again on a lease nobody else held in
		// between. Nothing to reload and nothing stale to discard, so no
		// takeover: frames, timers, SFC steps and retained state carry on.
		// The drivers get the whole output set on this scan, as after any
		// gap in what was sent.
		r.outSent.Store(false)
		r.leading = true
	case lead && (!r.leading || termed != nil && termed.Term() != r.ledTerm):
		// Leadership from standby, or a new term this replica never saw
		// the gate close for (lost and regained between two scans): another
		// replica may have led meanwhile.
		r.takeover()
		r.leading = true
		if termed != nil {
			r.ledTerm = termed.Term()
		}
	case !lead:
		if r.leading {
			// Stepping down: forces belong to the active controller only.
			r.dropForces("lost leadership")
		}
		r.leading = false
	}
	return lead
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
	if r.coord != nil {
		r.dropForces("takeover")
	}
	if r.retainStore != nil {
		if err := r.loadRetained(); err != nil {
			r.noteRetainError(err)
		}
	}
	r.prog.ResetFrame()
	for _, tr := range r.tasks {
		tr.prog.ResetFrame()
	}
	// Process state re-derives from the field, except what a program
	// declared VAR RETAIN: it goes back into the fresh frames.
	if err := r.restoreLocals(); err != nil {
		r.noteRetainError(err)
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
func (r *Runtime) loadRetained() error {
	st, err := r.retainStore.Load()
	if err != nil {
		return err
	}
	allowed := make(map[string]bool, len(r.retainTags))
	for _, n := range r.retainTags {
		allowed[n] = true
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
	if a := r.alarmRetainer(); a != nil {
		a.RestoreAlarms(st.Alarms)
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
		}
	}
}

// saveRetained is one saver tick: skip as standby, skip when nothing
// changed, keep the old encoding on failure so the next tick retries.
// Returns the encoding of what the store now holds.
func (r *Runtime) saveRetained(lastSaved []byte) []byte {
	if r.coord != nil && !r.coord.IsLeader() {
		return lastSaved
	}
	// Leading by the lease is not enough: until this runtime's own takeover
	// has loaded the store (gate, before its next scan), its tags hold seeds
	// and its frames are fresh, and saving them would overwrite what the
	// takeover is about to load.
	r.leadMu.Lock()
	leading := r.leading
	r.leadMu.Unlock()
	if !leading {
		return lastSaved
	}
	st := r.retainState()
	enc, err := json.Marshal(st)
	if err != nil || string(enc) == string(lastSaved) {
		return lastSaved
	}
	if err := r.retainStore.Save(st); err != nil {
		r.noteRetainError(err)
		return lastSaved
	}
	return enc
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
	}
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
func (r *Runtime) noteRetainError(err error) {
	r.mu.Lock()
	r.stats.RetainErrors++
	r.stats.LastRetainError = err.Error()
	r.mu.Unlock()
}
