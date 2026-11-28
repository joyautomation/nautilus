//go:build !js

package server

import (
	"context"
	"encoding/json"
	"fmt"
	goruntime "runtime"
	"slices"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/oplog"
	"github.com/joyautomation/nautilus/runtime"
)

// The keep decision for compacting the operator-intent log (#1455 PR 2b,
// docs/design/oplog-compaction.md §2-§3): which committed entries up to
// Through any state still rests on. The log asks it (oplog.Options.
// Compact); it never touches the live consumers.
//
// # Provenance
//
// Each kind's model (keepModel) replays the log and says, for every entry,
// which items of its state the entry's outcome depended on (reads: only
// items whose value was not empty then) and which items it changed
// (writes). An item is a piece of state small enough to be overwritten
// whole: an alarm's ack and occurrence, its shelf, a substitute address's
// active entry, a request ID, a session. Then:
//
//   - kept: the last writer of every item still live at T (an open
//     occurrence, a shelf in force, a request ID inside its window...),
//     every note and checkout entry, and every entry of a kind no model
//     claims (a newer build's);
//   - closure, repeated until nothing changes: a kept entry keeps the last
//     writer of each item it read (its outcome is the same only from the
//     same value), every later writer of each item it wrote (what undoes
//     any part of it), and every KindLapse naming it. The tail (after
//     Through), which is never dropped, seeds it too.
//
// An item no kept entry writes is empty in a replay of the kept entries,
// and an empty item needs no writer: its readers depended on nothing.
//
// # The check before proposing
//
// The keep set is then checked against the real consumers (scratch ones,
// in view mode, clock pinned to T): the alarms' AlarmLog.Apply into a view
// with no engine, the session store, the substitutes' runtime.IntentView.
// A replay of every committed entry and a replay of the kept entries plus
// the tail must give the same Digest for each, or nothing is proposed
// (oplog.ErrCompactRefused, naming the consumer and the first difference).
// A wrong model costs a refused compaction, never lost state.

// keepModel is one consumer's provenance model (see above).
type keepModel interface {
	// step replays e: what its outcome read and what it changed.
	step(e oplog.Entry) (reads, writes []string)
	// live is the items whose value still matters at t.
	live(t time.Time) []string
}

// checker is one scratch consumer for the check before proposing.
type checker interface {
	name() string
	apply(e oplog.Entry)
	// base is the log's base hook (oplog.Options.Base), for a compacted
	// replay.
	base(through uint64)
	// digest is the complete state, normalised at t: its hash and the text
	// hashed (the evidence).
	digest(t time.Time, through uint64) (sum, text string)
}

// claimed are the kinds a model of this build reads; any other is kept
// whole (a newer build's kind: its state is unknown here). An explicit
// list, never "whatever the appliers take": the pair's applier ends in a
// catch-all.
var claimed = map[string]bool{
	oplog.KindAck: true, oplog.KindShelve: true, oplog.KindUnshelve: true, oplog.KindAckClear: true,
	oplog.KindOccurrenceStart: true, oplog.KindOccurrenceEnd: true, oplog.KindAlarmDetect: true,
	oplog.KindOutOfService: true, oplog.KindInService: true,
	oplog.KindSubstitute: true, oplog.KindUnsubstitute: true, oplog.KindSuback: true,
	oplog.KindSession: true, oplog.KindLapse: true,
}

// alwaysKept are the kinds kept whatever the models say: notes (every
// revision; the witness keeps their text) and the checkout chain (it
// counts checkout entries, and seals bind their predecessors).
var alwaysKept = map[string]bool{oplog.KindNote: true, oplog.KindCheckout: true}

// Keeper is the pair's keep decision: oplog.Options.Compact is its Decide.
type Keeper struct {
	// models and checkers build fresh scratch state for one decision (tests
	// swap them).
	models   func(t time.Time) []keepModel
	checkers func(t time.Time) []checker
}

// NewKeeper is the pair's keep decision over its consumers' rules.
func NewKeeper() *Keeper {
	return &Keeper{models: defaultModels, checkers: defaultCheckers}
}

func defaultModels(t time.Time) []keepModel {
	return []keepModel{newAlarmModel(), &subModel{v: runtime.NewIntentView()}, newSessionModel(t)}
}

func defaultCheckers(t time.Time) []checker {
	return []checker{newAlarmCheck(), &subCheck{v: runtime.NewIntentView()}, newSessionCheck(t)}
}

// Decide is oplog.Options.Compact.
func (k *Keeper) Decide(ctx context.Context, in oplog.CompactInput) ([]uint64, error) {
	keep, err := decideKeep(ctx, in, k.models(in.T))
	if err != nil {
		return nil, err
	}
	if err := k.check(ctx, in, keep); err != nil {
		return nil, err
	}
	return keep, nil
}

// decideKeep is the provenance decision (see above).
func decideKeep(ctx context.Context, in oplog.CompactInput, models []keepModel) ([]uint64, error) {
	var (
		seqs     []uint64
		kinds    []string
		depOff   = []int32{0}
		deps     []int32 // flat: entry i's read dependencies (entry indexes)
		wrOff    = []int32{0}
		wrs      []int32 // flat: entry i's written items (item ids)
		items    = map[string]int32{}
		writers  [][]int32 // by item: the entries that wrote it, in order
		last     []int32   // by item: its last writer so far (-1: none)
		lapsesOf = map[uint64][]int32{}
		seeds    []int32
	)
	id := func(model int, it string) int32 {
		it = fmt.Sprintf("%d:%s", model, it) // each model's items are its own
		if v, ok := items[it]; ok {
			return v
		}
		v := int32(len(writers))
		items[it] = v
		writers = append(writers, nil)
		last = append(last, -1)
		return v
	}
	var stop error
	in.Entries(func(e oplog.Entry) bool {
		if len(seqs)%32 == 0 {
			if ctx.Err() != nil {
				stop = ctx.Err()
				return false
			}
			goruntime.Gosched()
		}
		i := int32(len(seqs))
		seqs, kinds = append(seqs, e.Seq), append(kinds, e.Kind)
		switch {
		case e.Seq > in.Through, alwaysKept[e.Kind], !claimed[e.Kind] && !mutServer("keep_drops_unclaimed"):
			seeds = append(seeds, i)
		}
		if e.Kind == oplog.KindLapse {
			for _, r := range e.Refs {
				lapsesOf[r] = append(lapsesOf[r], i)
			}
		}
		for mi, m := range models {
			rs, ws := m.step(e)
			for _, r := range rs {
				if w := last[id(mi, r)]; w >= 0 && !mutServer("keep_reads_ignored") {
					deps = append(deps, w)
				}
			}
			for _, w := range ws {
				it := id(mi, w)
				writers[it] = append(writers[it], i)
				last[it] = i
				wrs = append(wrs, it)
			}
		}
		depOff, wrOff = append(depOff, int32(len(deps))), append(wrOff, int32(len(wrs)))
		return true
	})
	if stop != nil {
		return nil, stop
	}
	for mi, m := range models {
		for _, it := range m.live(in.T) {
			if v, ok := items[fmt.Sprintf("%d:%s", mi, it)]; ok && last[v] >= 0 {
				seeds = append(seeds, last[v])
			}
		}
	}
	kept := make([]bool, len(seqs))
	from := make([]int, len(writers)) // by item: writers[from:] are all kept
	for i := range from {
		from[i] = len(writers[i])
	}
	var work []int32
	add := func(i int32) {
		if !kept[i] {
			kept[i] = true
			work = append(work, i)
		}
	}
	for _, s := range seeds {
		add(s)
	}
	for n := 0; len(work) > 0; n++ {
		if n%32 == 0 {
			goruntime.Gosched() // see oplog's yielder: CPU beside the scan
		}
		i := work[len(work)-1]
		work = work[:len(work)-1]
		for _, d := range deps[depOff[i]:depOff[i+1]] {
			add(d)
		}
		if !mutServer("keep_no_forward_closure") {
			for _, it := range wrs[wrOff[i]:wrOff[i+1]] {
				// every later writer of it: writers[it][from[it]:] are kept
				// already, so each is added once.
				ws := writers[it]
				p, _ := slices.BinarySearch(ws, i)
				for q := p + 1; q < from[it]; q++ {
					add(ws[q])
				}
				from[it] = min(from[it], p+1)
			}
		}
		if !mutServer("keep_lapse_split") {
			for _, l := range lapsesOf[seqs[i]] {
				add(l)
			}
		}
	}
	var keep []uint64
	for i, s := range seqs {
		if kept[i] && s <= in.Through && s > 0 {
			keep = append(keep, s)
		}
	}
	return keep, nil
}

// check is the check before proposing (see above).
func (k *Keeper) check(ctx context.Context, in oplog.CompactInput, keep []uint64) error {
	full, part := k.checkers(in.T), k.checkers(in.T)
	kept := map[uint64]bool{}
	for _, s := range keep {
		kept[s] = true
	}
	for _, c := range part {
		c.base(in.Through)
	}
	n := 0
	var stop error
	in.Entries(func(e oplog.Entry) bool {
		if n++; n%32 == 0 {
			if ctx.Err() != nil {
				stop = ctx.Err()
				return false
			}
			goruntime.Gosched()
		}
		for _, c := range full {
			c.apply(e)
		}
		if e.Seq > in.Through || kept[e.Seq] {
			for _, c := range part {
				c.apply(e)
			}
		}
		return true
	})
	if stop != nil {
		return stop
	}
	for i, c := range full {
		a, at := c.digest(in.T, in.Through)
		b, bt := part[i].digest(in.T, in.Through)
		if a != b || mutServer("keep_check_refuses") {
			return fmt.Errorf("%w: %s: a replay of the %d kept entries and the tail gives state %.12s, the whole log's %.12s (first difference: %s)",
				oplog.ErrCompactRefused, c.name(), len(keep), b, a, firstDiff(at, bt))
		}
	}
	return nil
}

// firstDiff is the first line where two digests' texts differ.
func firstDiff(a, b string) string {
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	for i := 0; i < max(len(al), len(bl)); i++ {
		var x, y string
		if i < len(al) {
			x = al[i]
		}
		if i < len(bl) {
			y = bl[i]
		}
		if x != y {
			return fmt.Sprintf("whole log %q, kept %q", truncate(x, 200), truncate(y, 200))
		}
	}
	return "none in the text"
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// replicated is a substitute start entry as the runtime takes it.
func replicated(e oplog.Entry) runtime.ReplicatedSubstitute {
	var v any
	if len(e.Value) > 0 {
		_ = json.Unmarshal(e.Value, &v)
	}
	return runtime.ReplicatedSubstitute{ID: e.Seq, Name: e.Tag, Value: v, By: e.By,
		UntilMs: e.Until, GrantedMs: e.GrantedMs, AckOnStart: e.AckOnStart, RequestID: e.Req}
}

// stepView applies e to an IntentView as SubstituteLog.Apply hands it to
// the runtime.
func stepView(v *runtime.IntentView, e oplog.Entry) runtime.ViewEffect {
	switch e.Kind {
	case oplog.KindSubstitute:
		if len(e.Value) > 0 && !json.Valid(e.Value) {
			return runtime.ViewEffect{} // SubstituteLog.Apply refuses it
		}
		if e.Lapsed {
			return v.LapsedStart(replicated(e))
		}
		return v.Start(replicated(e))
	case oplog.KindUnsubstitute:
		return v.Unsubstitute(e.Ref)
	case oplog.KindSuback:
		return v.Suback(e.Refs)
	case oplog.KindLapse:
		return v.Lapse(e.Refs)
	}
	return runtime.ViewEffect{}
}

// subModel is the substitutes' provenance model.
type subModel struct{ v *runtime.IntentView }

func (m *subModel) step(e oplog.Entry) ([]string, []string) {
	f := stepView(m.v, e)
	return f.Reads, f.Writes
}

func (m *subModel) live(t time.Time) []string { return m.v.Live(t) }

// subCheck is the substitutes' scratch consumer.
type subCheck struct{ v *runtime.IntentView }

func (c *subCheck) name() string        { return "substitutes" }
func (c *subCheck) apply(e oplog.Entry) { stepView(c.v, e) }
func (c *subCheck) base(through uint64) { c.v.Base(through) }
func (c *subCheck) digest(t time.Time, through uint64) (string, string) {
	return c.v.Digest(t, through)
}

// Base is the log's base hook for substitutes (oplog.Options.Base): the
// runtime's high-water marks start at the base's Through.
func (l *SubstituteLog) Base(through uint64) { l.rt.NoteIntentBase(through) }
