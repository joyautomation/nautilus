//go:build !js

package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/lang/ir"
)

// IntentView is the substitutes' state as the operator-intent log alone
// makes it (#1455 PR 2b, docs/design/oplog-compaction.md §3): the same
// rules as ApplySubstitute, ApplyUnsubstitute, ApplySuback,
// ApplyLapsedSubstitute and ApplyLapse, over the log's entries only, with
// no tags, no clock but the one it is given, and no events. The log's
// compaction replays it, never the runtime, to judge which substitute
// entries state rests on (each step says what it read and wrote) and to
// check that a replay of the kept entries gives the same state (Digest).
//
// A lapse it lists names its address as the entry spelled it (the runtime
// lists the program's spelling; addresses compare by ir.NameKey).
//
// It takes every committed start as installed: a start the runtime
// refused (an address the program does not have) is kept as though it
// ran, which keeps more, never less. TestIntentViewMatchesRuntime checks
// it against the runtime itself.
type IntentView struct {
	active  map[string]viewSub // by address (ir.NameKey)
	byID    map[uint64]string  // an active entry's address
	reqs    map[string]reqSeen
	lapses  []SubstituteLapse
	applied uint64 // the highest substitute ID applied, as intentState.applied
	subSeq  uint64
	// maybe are starts the view did not apply for their request ID (a
	// retry, or a reuse) that the runtime may have: the view takes every
	// start as installed, but the runtime records a request ID only for a
	// start it installed, so after one it refused, a retry runs there
	// (Fable, 2b-1). Not state of the view's: only kept while their time
	// runs (Live), with what ends them.
	maybe map[uint64]int64 // id: its end
}

// viewSub is one active substitute in an IntentView.
type viewSub struct {
	ReplicatedSubstitute
	acked bool
}

// ViewEffect is what one entry did to an IntentView: the items of its
// state whose value (non-zero, as it stood) the outcome depended on, and
// the items it changed. Items are opaque names: "s/<address>" (the
// address's active entry), "r/<request ID>", "l/<id>" (a lapse listed).
type ViewEffect struct {
	Reads, Writes []string
}

func (f *ViewEffect) read(item string)  { f.Reads = append(f.Reads, item) }
func (f *ViewEffect) write(item string) { f.Writes = append(f.Writes, item) }

// NewIntentView is an empty IntentView.
func NewIntentView() *IntentView {
	return &IntentView{active: map[string]viewSub{}, byID: map[uint64]string{}, reqs: map[string]reqSeen{}, maybe: map[uint64]int64{}}
}

func subItem(key string) string { return "s/" + key }
func reqItem(id string) string  { return "r/" + id }
func lapseItem(id uint64) string {
	return fmt.Sprintf("l/%d", id)
}
func maybeItem(id uint64) string { return fmt.Sprintf("m/%d", id) }

// endMaybe ends a start the runtime may hold (maybe) that an entry names.
func (v *IntentView) endMaybe(f *ViewEffect, id uint64) {
	if _, ok := v.maybe[id]; ok {
		f.read(maybeItem(id))
		delete(v.maybe, id)
		f.write(maybeItem(id))
	}
}

// Start applies a committed substitute start, as ApplySubstitute does.
func (v *IntentView) Start(e ReplicatedSubstitute) (f ViewEffect) {
	v.applied, v.subSeq = max(v.applied, e.ID), max(v.subSeq, e.ID)
	start := e.UntilMs - e.GrantedMs
	for id, seen := range v.reqs {
		if seen.startMs < start-RequestIDWindow.Milliseconds() {
			delete(v.reqs, id)
			f.write(reqItem(id))
		}
	}
	fp := requestPrint(e.Name, e.Value, e.GrantedMs)
	if seen, ok := v.reqs[e.RequestID]; e.RequestID != "" && ok && seen.id != e.ID {
		f.read(reqItem(e.RequestID)) // refused, or a retry: the first stands
		v.maybe[e.ID] = e.UntilMs    // unless the runtime refused the first
		f.write(maybeItem(e.ID))
		return f
	}
	key := ir.NameKey(e.Name)
	if old, ok := v.active[key]; ok {
		delete(v.byID, old.ID) // replaced, as a start on the leader does
	}
	v.active[key] = viewSub{ReplicatedSubstitute: e}
	v.byID[e.ID] = key
	f.write(subItem(key))
	if e.RequestID != "" {
		v.reqs[e.RequestID] = reqSeen{id: e.ID, fp: fp, startMs: start}
		f.write(reqItem(e.RequestID))
	}
	return f
}

// Unsubstitute applies a committed unsubstitute entry naming id.
func (v *IntentView) Unsubstitute(id uint64) (f ViewEffect) {
	v.endMaybe(&f, id)
	if key, ok := v.byID[id]; ok {
		f.read(subItem(key))
		delete(v.active, key)
		delete(v.byID, id)
		f.write(subItem(key))
	}
	return f
}

// Suback applies a committed suback entry naming ids: an active entry that
// waits for an acknowledgement has had one.
func (v *IntentView) Suback(ids []uint64) (f ViewEffect) {
	for _, id := range ids {
		key, ok := v.byID[id]
		if !ok {
			continue
		}
		s := v.active[key]
		f.read(subItem(key))
		if !s.AckOnStart || s.acked {
			continue
		}
		s.acked = true
		v.active[key] = s
		f.write(subItem(key))
	}
	return f
}

// LapsedStart applies a committed start that a lapse names
// (ApplyLapsedSubstitute): recorded, never started.
func (v *IntentView) LapsedStart(e ReplicatedSubstitute) (f ViewEffect) {
	v.applied, v.subSeq = max(v.applied, e.ID), max(v.subSeq, e.ID)
	v.recordLapse(&f, SubstituteLapse{ID: e.ID, Name: e.Name, Value: e.Value, By: e.By, RequestID: e.RequestID, ProposedMs: e.UntilMs - e.GrantedMs})
	return f
}

// Lapse applies a committed lapse naming ids (ApplyLapse): one still
// active ends; its request ID is free again.
func (v *IntentView) Lapse(ids []uint64) (f ViewEffect) {
	for _, id := range ids {
		v.endMaybe(&f, id)
		key, ok := v.byID[id]
		if !ok {
			continue
		}
		s := v.active[key]
		f.read(subItem(key))
		delete(v.active, key)
		delete(v.byID, id)
		f.write(subItem(key))
		for k, seen := range v.reqs {
			if seen.id == id {
				delete(v.reqs, k)
				f.write(reqItem(k))
			}
		}
		v.recordLapse(&f, SubstituteLapse{ID: s.ID, Name: s.Name, Value: s.Value, By: s.By, ProposedMs: s.UntilMs - s.GrantedMs, Ended: true})
	}
	return f
}

// recordLapse is recordLapseLocked.
func (v *IntentView) recordLapse(f *ViewEffect, l SubstituteLapse) {
	for i, old := range v.lapses {
		if old.ID == l.ID {
			f.read(lapseItem(l.ID))
			if l.Ended || !old.Ended {
				v.lapses[i] = l
				f.write(lapseItem(l.ID))
			}
			return
		}
	}
	v.lapses = append(v.lapses, l)
	f.write(lapseItem(l.ID))
	if len(v.lapses) > maxLapses {
		for _, gone := range v.lapses[:len(v.lapses)-maxLapses] {
			f.write(lapseItem(gone.ID))
		}
		v.lapses = v.lapses[len(v.lapses)-maxLapses:]
	}
}

// Base is the log's base hook (oplog.Options.Base): everything up to
// through was delivered, so the high-water marks start there.
func (v *IntentView) Base(through uint64) {
	v.applied, v.subSeq = max(v.applied, through), max(v.subSeq, through)
}

// Live is the items whose value still matters at t: an active entry not
// yet over (one past its end is expired at once by a leader that replays
// it), a request ID inside its window, a lapse listed inside it.
func (v *IntentView) Live(t time.Time) []string {
	var out []string
	ms := t.UnixMilli()
	for key, s := range v.active {
		if s.UntilMs > ms {
			out = append(out, subItem(key))
		}
	}
	for id, seen := range v.reqs {
		if seen.startMs >= ms-RequestIDWindow.Milliseconds() {
			out = append(out, reqItem(id))
		}
	}
	for _, l := range v.lapses {
		if l.ProposedMs >= ms-RequestIDWindow.Milliseconds() {
			out = append(out, lapseItem(l.ID))
		}
	}
	for id, until := range v.maybe {
		if until > ms && !mutRuntime("view_maybe_dropped") {
			out = append(out, maybeItem(id))
		}
	}
	return out
}

// Digest is the view's complete state, normalised at t, with the
// high-water marks at least through (a compacted replay starts them
// there, Base): an entry past its end, a request ID or a listed lapse
// outside its window is left out, as Live leaves them. Two replays that
// give the same Digest give the same substitutes to a runtime that
// replays them at or after t. The text it hashes, too, for a refusal's
// evidence.
func (v *IntentView) Digest(t time.Time, through uint64) (sum, text string) {
	ms := t.UnixMilli()
	var b strings.Builder
	fmt.Fprintf(&b, "applied %d subSeq %d\n", max(v.applied, through), max(v.subSeq, through))
	keys := make([]string, 0, len(v.active))
	for k, s := range v.active {
		if s.UntilMs > ms {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		s := v.active[k]
		val, _ := json.Marshal(s.Value)
		fmt.Fprintf(&b, "sub %s id %d name %q value %s by %q until %d granted %d ackOnStart %t acked %t req %q\n",
			k, s.ID, s.Name, val, s.By, s.UntilMs, s.GrantedMs, s.AckOnStart, s.acked, s.RequestID)
	}
	ids := make([]string, 0, len(v.reqs))
	for id, seen := range v.reqs {
		if seen.startMs >= ms-RequestIDWindow.Milliseconds() {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		seen := v.reqs[id]
		fmt.Fprintf(&b, "req %q id %d fp %q start %d\n", id, seen.id, seen.fp, seen.startMs)
	}
	for _, l := range v.lapses {
		if l.ProposedMs >= ms-RequestIDWindow.Milliseconds() {
			val, _ := json.Marshal(l.Value)
			fmt.Fprintf(&b, "lapse id %d name %q value %s by %q req %q proposed %d ended %t\n", l.ID, l.Name, val, l.By, l.RequestID, l.ProposedMs, l.Ended)
		}
	}
	h := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(h[:]), b.String()
}

// NoteIntentBase is the log's base hook for the runtime (oplog.Options.
// Base, through the server's SubstituteLog): everything up to through was
// delivered, though only its kept entries are replayed, so the retain
// store's gate (applied) and the substitute sequence start there, and a
// stale retained substitute with an ID at or below it is never re-added.
func (r *Runtime) NoteIntentBase(through uint64) {
	r.subMu.Lock()
	defer r.subMu.Unlock()
	r.intent.applied = max(r.intent.applied, through)
	r.subSeq = max(r.subSeq, through)
}
