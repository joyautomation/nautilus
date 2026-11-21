package quorum

import (
	"slices"
	"time"
)

// Durable duplicate marks (#1029 3c-3b). A voter keeps, per name it marked
// or an operator cleared, a DupRecord in its state file: a restart no
// longer forgets a mark, the boots it knew when it marked (so HeldBy can
// name the incumbent again), the boots an operator's clear retired, and
// the name's clear generation. Only the operator's clear-duplicate command
// clears a mark.
//
// The mark is told at once (Reply.Marked), as before, written after: a
// crash before the write loses it, which only rule (i) covers then. Each
// reply carries the name's clear generation (Reply.ClearGen): a hold ends
// on the marking boot's unmarked word, or on any boot's whose generation
// is above the one the hold was set under, which only an operator's clear
// raises (Candidate.NoteMark). The mark's write is off the grant path: a
// grant waits only for the grant's own state (need), never for a mark
// write.

// DupRecord is one name's duplicate record in a voter's state file.
type DupRecord struct {
	Name NodeID
	// Marked: this voter marks Name a confirmed duplicate.
	Marked bool
	// Gen is Name's clear generation at this voter: every operator clear
	// sets it above every member's (and every hold's), so an unmarked word
	// from a voter that missed the clear (wiped, or a copy from an older
	// disk) carries a lower one and frees no hold.
	Gen uint64
	// Boots, while Marked: the boots of Name this voter knew when it
	// marked, the first heard first (HeldBy's incumbent).
	Boots []BootSeq
	// Retired: the boots of Name an operator's clear retired here.
	Retired []BootSeq
}

// maxDupRecords bounds a state file's records: a pair has two names, so
// more is strays (a name marked once and long cleared). The unmarked go
// first.
const maxDupRecords = 8

// staleAge: how long before a restart a restored boot reads as last heard:
// never fresh until it sends a new frame (HeldBy names no dead incumbent).
const staleAge = 1000 * time.Second

// syncDups copies the in-memory marks, boots, retired boots and
// generations into the state, as a change of its own: it doesn't hold up
// grants (need), and is written by MarkWrite.
func (v *Voter) syncDups() {
	if mut("marks_volatile") {
		return
	}
	names := map[NodeID]bool{}
	for n, m := range v.marked {
		names[n] = names[n] || m
	}
	for n := range v.retired {
		names[n] = names[n] || false
	}
	for n, g := range v.gens {
		if g != 0 {
			names[n] = names[n] || false
		}
	}
	var recs []DupRecord
	for n := range names {
		r := DupRecord{Name: n, Marked: v.marked[n], Gen: v.gens[n]}
		if r.Marked {
			bs := slices.Clone(v.boots[n])
			slices.SortStableFunc(bs, func(a, b bootSeen) int {
				switch {
				case a.first < b.first:
					return -1
				case a.first > b.first:
					return 1
				}
				return 0
			})
			for _, b := range bs {
				r.Boots = append(r.Boots, BootSeq{Nonce: b.nonce, Seq: b.seq})
			}
		}
		for _, b := range v.retired[n] {
			r.Retired = append(r.Retired, BootSeq{Nonce: b.nonce, Seq: b.seq})
		}
		recs = append(recs, r)
	}
	slices.SortFunc(recs, func(a, b DupRecord) int {
		if a.Marked != b.Marked {
			if a.Marked {
				return -1
			}
			return 1
		}
		switch {
		case a.Name < b.Name:
			return -1
		case a.Name > b.Name:
			return 1
		}
		return 0
	})
	if len(recs) > maxDupRecords {
		recs = recs[:maxDupRecords]
	}
	v.st.Dups = recs // a new slice: a Persist handed out keeps its own
	v.dirty++
	v.dupNeed = v.dirty
}

// restoreDups takes the records a state file held (NewVoter): each marked
// name is marked from the first frame, its boots known but stale until a
// new frame; the retired boots and generations stand.
func (v *Voter) restoreDups(now Instant) {
	stale := now - Instant(staleAge)
	for _, r := range v.st.Dups {
		if r.Gen != 0 {
			if v.gens == nil {
				v.gens = map[NodeID]uint64{}
			}
			v.gens[r.Name] = r.Gen
		}
		if len(r.Retired) > 0 && !mut("retired_not_restored") {
			if v.retired == nil {
				v.retired = map[NodeID][]bootSeen{}
			}
			var rs []bootSeen
			for _, b := range r.Retired {
				rs = append(rs, bootSeen{nonce: b.Nonce, seq: b.Seq, first: stale, last: stale})
			}
			v.retired[r.Name] = rs
		}
		if !r.Marked || mut("mark_not_restored") {
			continue
		}
		if v.marked == nil {
			v.marked = map[NodeID]bool{}
		}
		v.marked[r.Name] = true
		var bs []bootSeen
		for i, b := range r.Boots {
			last := stale
			if mut("restored_boot_fresh") {
				last = now
			}
			bs = append(bs, bootSeen{nonce: b.Nonce, seq: b.Seq, first: stale + Instant(i), last: last})
		}
		if v.boots == nil {
			v.boots = map[NodeID][]bootSeen{}
		}
		v.boots[r.Name] = bs
	}
}

// MarkWrite returns the write the duplicate records are owed (after a
// mark, a clear, or a failed write of them), or nil: none owed, or one
// covering them is already in flight. Handle returns it when it has no
// write of its own, so a driver needs no other call; a driver that
// changes the records outside Handle (the operator's clear) may call it.
func (v *Voter) MarkWrite() *Persist {
	if v.durable >= v.dupNeed || v.issued >= v.dupNeed {
		return nil
	}
	v.issued = v.dirty
	return &Persist{Seq: v.dirty, State: v.st}
}

// MarksOwed reports whether a change to the duplicate records is not yet
// durable (a crash now loses it).
func (v *Voter) MarksOwed() bool { return v.durable < v.dupNeed }

// MutClearGenIgnoresHolds reports the clear_gen_ignores_holds mutation,
// for the simulator's model of the command.
func MutClearGenIgnoresHolds() bool { return mut("clear_gen_ignores_holds") }

// MutClearIgnoresHeld reports the clear_ignores_held mutation, for the
// simulator's model of the command.
func MutClearIgnoresHeld() bool { return mut("clear_ignores_held") }

// DupGen is name's clear generation at this voter (0: never cleared here).
func (v *Voter) DupGen(name NodeID) uint64 { return v.gens[name] }

// setGen sets name's clear generation for an operator's clear: gen, the
// one the command chose above every member's, or above this voter's own
// when the clear names none.
func (v *Voter) setGen(name NodeID, gen uint64) {
	if mut("clear_gen_not_bumped") {
		return
	}
	if v.gens == nil {
		v.gens = map[NodeID]uint64{}
	}
	v.gens[name] = max(gen, v.gens[name]+1)
}

// RetiredReply takes a reply this voter's own candidate got from voter
// name's process boot: when that boot is one an operator's clear retired
// for name, the copy it was runs again (it was paused or hung at the
// clear, and answers votes now). The name is marked again at once, and it
// reports true: the candidate counts no grant of that reply. A reply
// matched to a current request is proof enough that the boot is alive.
func (v *Voter) RetiredReply(now Instant, name NodeID, boot uint64) bool {
	if boot == 0 || name == v.self || mut("retired_reply_counted") {
		return false
	}
	i := slices.IndexFunc(v.retired[name], func(b bootSeen) bool { return b.nonce == boot })
	if i < 0 {
		return false
	}
	if !v.marked[name] {
		v.markRetired(now, name, boot, v.retired[name][i].seq)
	}
	return true
}
