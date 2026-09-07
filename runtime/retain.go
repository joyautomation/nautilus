package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// saveInterval paces the retain saver. Two seconds bounds how much operator
// input a hard kill can lose, while keeping a busy HMI session from turning
// every slider drag into a ConfigMap write.
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
	switch {
	case lead && !r.leading:
		r.takeover()
		r.leading = true
	case !lead:
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
	if r.retainStore != nil {
		if err := r.loadRetained(); err != nil {
			r.noteRetainError(err)
		}
	}
	r.prog.ResetFrame()
	for _, tr := range r.tasks {
		tr.prog.ResetFrame()
	}
	// This replica has never written to the field, whatever its predecessor
	// left in the store: the first scan as leader pushes the FULL output set
	// rather than trusting a generation stamp the old leader's driver knows
	// nothing about. See Options.AlwaysWriteOutputs.
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

// loadRetained applies the store's state: retained tag values, then any
// online-edited program sources. Tag names outside the retained set are
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
		// A retained STRUCT tag: JSON collapsed it to a plain
		// map[string]any (see retainState/plain), which setAny has no case
		// for — a flat write must never conjure a tag from nothing (see
		// Tags.Set), so a map there is silently dropped. Restoring
		// configuration is different: the tag already exists, seeded from
		// the manifest (with its StructDef attached) before takeover ever
		// runs, so it is read-modify-written member by member through
		// ir.SetField exactly like a struct's own operator writes
		// (Tags.SetPath) are — an empty path assigns the whole value, which
		// for a struct target means the same partial merge SetPath does.
		if m, isMap := v.(map[string]any); isMap {
			cur, err := r.tags.ReadGlobal(name)
			if err != nil {
				errs = append(errs, fmt.Errorf("retained tag %q: %w", name, err))
				continue
			}
			nv, err := ir.SetField(cur, nil, m, "tag "+name)
			if err != nil {
				errs = append(errs, fmt.Errorf("retained tag %q: %w", name, err))
				continue
			}
			r.tags.setAny(name, nv)
			continue
		}
		// JSON collapses every number to float64. If the tag is currently an
		// integer kind (seeded from the manifest), restore it as one — the
		// store must not silently retype a DINT setpoint into a REAL.
		if f, isNum := v.(float64); isNum {
			if cur, err := r.tags.ReadGlobal(name); err == nil &&
				(cur.Kind == ir.TypeInt || cur.Kind == ir.TypeTime) {
				r.tags.setAny(name, int64(f))
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
	for task, src := range st.Programs {
		p := r.TaskProgram(task)
		if p == nil {
			errs = append(errs, fmt.Errorf("retained program for unknown task %q", task))
			continue
		}
		if p.Hash() == sourceHash(src) {
			continue
		}
		if err := p.Swap(src); err != nil {
			errs = append(errs, fmt.Errorf("retained program for task %q: %w", task, err))
		}
	}
	return errors.Join(errs...)
}

// retainSaver flushes changed state on a fixed cadence, only while leading —
// the ConfigMap store is last-writer-wins, and leadership is what makes that
// safe. Change detection is by comparison against the last written encoding
// rather than dirty flags: writes reach the tag store from operator APIs,
// field drivers, and logic alike, and a comparison catches all of them
// without threading a flag through every path. A failed save keeps the old
// encoding so the next tick retries.
func (r *Runtime) retainSaver(ctx context.Context) {
	t := time.NewTicker(saveInterval)
	defer t.Stop()
	var lastSaved []byte
	for {
		select {
		case <-ctx.Done():
			return
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
// what it compiled from (an online edit not yet pulled into git). A struct
// (UDT) tag persists as its plain JSON form (see plain) — the same shape
// GET /api/state already sends an HMI — and loadRetained merges it back
// member by member through ir.SetField, so a restart doesn't lose a
// faceplate's setpoints just because they live inside a UDT. Arrays are
// still skipped: there is no settled path-addressed way to write one back
// through the tag store (see ir.SetField's TypeArray case), so persisting
// one would be a lie loadRetained could not make good on.
func (r *Runtime) retainState() retain.State {
	st := retain.State{}
	for _, name := range r.retainTags {
		v, err := r.tags.ReadGlobal(name)
		if err != nil {
			continue
		}
		switch v.Kind {
		case ir.TypeBool, ir.TypeReal, ir.TypeInt, ir.TypeTime, ir.TypeString, ir.TypeStruct:
			if st.Tags == nil {
				st.Tags = map[string]any{}
			}
			st.Tags[name] = plain(v)
		}
	}
	record := func(task string, p *Program) {
		if !p.Dirty() {
			return
		}
		if st.Programs == nil {
			st.Programs = map[string]string{}
		}
		st.Programs[task] = p.Source()
	}
	record(MainTaskName, r.prog)
	for _, tr := range r.tasks {
		record(tr.name, tr.prog)
	}
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

// noteRetainError folds a store failure into the diagnostics the dashboard
// already shows, the same way I/O errors surface.
func (r *Runtime) noteRetainError(err error) {
	r.mu.Lock()
	r.stats.RetainErrors++
	r.stats.LastRetainError = err.Error()
	r.mu.Unlock()
}
