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
	// Fence: the lowest adoption generation of Name's state this voter
	// takes (Voter.Fence, #1029 3c-3c-1b); 0: none. Only a file
	// with a fence is written in format 8.
	Fence uint64
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
	for n := range v.fences {
		names[n] = names[n] || false
	}
	var recs []DupRecord
	for n := range names {
		r := DupRecord{Name: n, Marked: v.marked[n], Gen: v.gens[n], Fence: v.fences[n]}
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
		if (a.Fence != 0) != (b.Fence != 0) {
			if a.Fence != 0 {
				return -1 // a fence is never a stray: kept before plain records
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
		if r.Fence != 0 && !mut("fence_volatile") {
			if v.fences == nil {
				v.fences = map[NodeID]uint64{}
			}
			v.fences[r.Name] = r.Fence
		}
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

// Fence fences name at this voter for an operator's recommission
// --from-spare (#1029 3c-3c-1b): from now on it refuses every request from
// a state of name at an adoption generation below the fence, and its
// candidate counts no reply from one (Fenced). The fence is the lowest
// generation it takes: carried, the value the command chose, raised on a
// controller to two above its own Gen and PendingGen, and above any fence
// it had on name. No state of the name the voters held reaches it:
//   - a commissioned one adopted only after this peer did, or as the
//     receiver of this peer's proposal (its Ack lost, the proposal still
//     pending here): at most max(Gen, PendingGen);
//   - an uncommissioned one (a lost file) may hold a durable proposal of
//     its own, at most this peer's max(Gen, PendingGen) + 1 when it was
//     made (#1164 review, B3); or, a replacement of an earlier
//     --from-spare that died re-adopting, at most that earlier fence.
//
// Those only rise. The replacement learns the fence (Adopt.Fence,
// Reply.Fence) and re-adopts at it or above. A fence only rises. It
// returns the fence now set; the write is owed (MarkWrite).
func (v *Voter) Fence(name NodeID, carried uint64) uint64 {
	if name == v.self || (mut("fence_witness_unset") && !v.commission) {
		return v.fences[name]
	}
	above := uint64(2)
	if mut("fence_plus_one") {
		above = 1
	}
	if v.commission && !mut("fence_takes_carried") {
		// A controller sets its own fence: the command carries it 0, and
		// one carried in, a value it can't check, would raise the name's
		// fence for good (#1164 re-check, S6).
		carried = 0
	}
	f := max(carried, v.st.Gen+above)
	if !mut("fence_peer_own_gen_only") {
		f = max(f, v.st.PendingGen+above)
	}
	if prev := v.fences[name]; prev != 0 && !mut("fence_ignores_previous") {
		f = max(f, prev+1)
	}
	v.setFence(name, f)
	return v.fences[name]
}

// setFence raises name's fence to f, owing the write.
func (v *Voter) setFence(name NodeID, f uint64) {
	if v.fences == nil {
		v.fences = map[NodeID]uint64{}
	}
	if f <= v.fences[name] {
		return
	}
	v.fences[name] = f
	v.syncDups()
}

// Fences are this voter's fences, as its frames carry them (Fences).
func (v *Voter) Fences() Fences {
	var out Fences
	i := 0
	names := make([]NodeID, 0, len(v.fences))
	for n := range v.fences {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		if i == len(out) {
			break
		}
		out[i] = FenceRec{Name: n, Fence: v.fences[n]}
		i++
	}
	return out
}

// LearnFences takes the fences a frame from another member carried
// (Request.Fences, Reply.Fences), the driver having dropped a fenced
// sender's frame whole, and installs those this voter may learn: only a
// voter that lost its state learns (naut-lead's ruling on #1164), so that
// a fence the operator's command set survives one member's file loss (the
// peer wiped after it, #1164 review B1/B2; the witness rebuilt, then the
// peer) while a command that fenced the peer but not the witness stays
// incomplete (ruling C). A controller learns while uncommissioned (a lost,
// wiped or moved-aside file), before it can adopt: the driver calls this
// first, so the reply that makes the witness heard installs the fence
// before the peer's State is taken. The witness learns a name's fence
// only while it keeps no record of that name at all: a rebuilt witness
// (or one restored from a backup) never saw the command, and a crash
// before the learned fence is written leaves it learning still; a witness
// the command reached keeps the name's clear record (an incomplete fence
// does not spread to it). It reports whether it learnt one; the write is
// owed (MarkWrite).
//
// names are the names it may learn a fence on: a controller its peer's,
// the witness the pair's two controllers' (#1164 re-check, S5).
func (v *Voter) LearnFences(fs Fences, names ...NodeID) bool {
	if mut("fence_not_learned") {
		return false
	}
	learnt := false
	for _, r := range fs {
		if r.Fence == 0 || r.Name == v.self || r.Fence <= v.fences[r.Name] {
			continue
		}
		if !slices.Contains(names, r.Name) && !mut("fence_learns_any_name") {
			continue
		}
		switch {
		case v.commission && v.st.Incarnation == 0:
		case v.isWitness && !v.hasRecord(r.Name):
		default:
			continue
		}
		v.setFence(r.Name, r.Fence)
		learnt = true
	}
	return learnt
}

// hasRecord reports whether this voter keeps any duplicate record of
// name: a mark, a clear generation, retired boots or a fence.
func (v *Voter) hasRecord(name NodeID) bool {
	return v.marked[name] || v.gens[name] != 0 || len(v.retired[name]) > 0 || v.fences[name] != 0
}

// Fenced reports whether gen, the committed adoption generation a frame of
// name carries, is below name's fence here: the state of a controller an
// operator replaced (Fence).
func (v *Voter) Fenced(name NodeID, gen uint64) bool {
	f := v.fences[name]
	return f != 0 && gen < f
}

// FencedRequest reports whether r comes from a fenced state (Fenced): a
// driver takes nothing from it, no relayed promise, no term, no yield, no
// word that the peer is up, and hands it to Handle only for the refusal
// (#1029 3c-3c-1b).
func (v *Voter) FencedRequest(r Request) bool {
	return v.Fenced(r.From, r.Gen) && !mut("fence_requests_off")
}

// FencedReply reports whether r comes from a fenced state's voter: a
// driver drops it whole, its relayed promise too (#1029 3c-3c-1b).
func (v *Voter) FencedReply(r Reply) bool {
	return r.From != v.self && v.Fenced(r.From, r.Gen) && !mut("fence_replies_off")
}

// FenceOf is name's fence at this voter: the lowest generation it takes
// from name's state (0: no fence).
func (v *Voter) FenceOf(name NodeID) uint64 { return v.fences[name] }

// MaxFence is the highest fence this voter keeps (0: none): a commission
// or recommission proposes at or above it, so the pair it commissions is
// never fenced (#1029 3c-3c-1b).
func (v *Voter) MaxFence() uint64 {
	var f uint64
	for _, g := range v.fences {
		f = max(f, g)
	}
	return f
}
