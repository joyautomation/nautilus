package quorum

import (
	"slices"
	"time"
)

// The bad-retain hand-back (#710 S2c-3): a controller that wins a lease,
// goes cold, and finds its own retain store unreadable may hand the lead
// back to its peer instead of running the plant on initial values, when
// the peer's retain file is new enough. "New enough" is judged against
// the voters' epoch records, never against what this controller
// remembers: after a restart with a corrupt file it remembers nothing.

// handBackEvery is how many leases must pass after a hand-back before
// this controller hands back again (HandBackHeld), at least
// handBackEveryMin: a controller whose store stays unreadable and that
// keeps coming back to the lead (its peer crash-looping, or its own
// process) hands back once, then runs on initial values.
const (
	handBackEvery    = 20
	handBackEveryMin = time.Minute
)

// HandBackEvery is the guard's window at lease.
func HandBackEvery(lease time.Duration) time.Duration {
	return max(handBackEvery*lease, handBackEveryMin)
}

// HandBackHeld reports whether a hand-back at lastMs (Unix milliseconds,
// 0 for none) still holds the next one back at nowMs. The wall clock is
// the only one that outlives a restart. A lastMs in the future (the clock
// went back since) is ignored rather than trusted, so a backward jump
// never holds the guard past its window; a forward jump may end it early.
func HandBackHeld(lastMs, nowMs int64, lease time.Duration) bool {
	if lastMs == 0 || mut("handback_guard_off") {
		return false
	}
	if lastMs > nowMs && !mut("handback_future_held") {
		return false
	}
	return time.Duration(nowMs-lastMs)*time.Millisecond < HandBackEvery(lease)
}

// HandedBackMs is when this voter's controller last handed back
// (VoterState.HandedBackMs), 0 for never.
func (v *Voter) HandedBackMs() int64 { return v.st.HandedBackMs }

// NoteHandBack records a hand-back at atMs and returns the write that
// makes it durable. The driver hands back only once the write is.
func (v *Voter) NoteHandBack(atMs int64) *Persist {
	v.st.HandedBackMs = atMs
	return v.change()
}

// UndoHandBack puts back the hand-back time NoteHandBack replaced, in
// memory, when the hand-back did not happen after all (its write failed,
// or the lease ended first): an armed guard would refuse the next one,
// and the peer's question, for a hand-back that never was (Fable, S3).
// The next write carries it to disk.
func (v *Voter) UndoHandBack(prevMs int64) { v.st.HandedBackMs = prevMs }

// Hand-back refusals (HotCheck.HandBackFloor).
const (
	HandBackRecordsUnknown = "records-unknown" // fewer than two winning replies know every grant after the newest leadership
	HandBackNotLeading     = "not-leading"
)

// HandBackFloor is the newest leadership before this one that the
// winning round's voters know of: a peer's retain file at an earlier
// epoch than it misses that leadership's values, and is not handed the
// lead. It counts the peer's records (every boot), this controller's
// records of its EARLIER boots (a reply's own-name record, Voter.recs),
// and this boot's own leaderships. ok is false, with a reason, when the
// voters can't say: fewer than two winning replies cover every grant
// above it (a voter that lost its state may be the one that saw a later
// leadership), a voter reports this controller's current epoch as an
// earlier boot's (an older build's reply), or a covering reply names a
// third controller above it (its peer record is then not in the reply).
//
// Every leadership was granted by two of the three voters, and two
// covering winning replies share one of them: a leadership above the
// floor would be in its records. A record is a grant, not a leadership:
// a campaign one voter granted (its own controller's, in a split vote)
// raises one record only. So when all three voters answered and cover,
// the floor is each name's SECOND-highest record: a leadership above it
// would need two voters' records above it (Fable, S1).
func (h HotCheck) HandBackFloor() (floor uint64, ok bool, why string) {
	if h.Epoch == 0 {
		return 0, false, HandBackNotLeading
	}
	var peer, own []uint64
	for _, w := range h.Won {
		r, _ := w.Recs.Of(h.Peer)
		peer = append(peer, r.Epoch)
		var o uint64
		if r, ok := w.Recs.Of(h.Self); ok && !mut("handback_own_record_ignored") {
			o = r.Epoch
		}
		own = append(own, o)
	}
	floor = max(h.LedBefore, nth(peer, 0), nth(own, 0))
	if len(h.Won) == 3 && !mut("handback_floor_max_only") {
		second := max(h.LedBefore, nth(peer, 1), nth(own, 1))
		all := true
		for _, w := range h.Won {
			all = all && Covers(h.recordsFrom(w), second)
		}
		if all {
			floor = second
		}
	}
	if (nth(own, 0) >= h.Epoch || floor >= h.Epoch) && !mut("handback_own_record_ignored") {
		return 0, false, HandBackRecordsUnknown
	}
	covering := 0
	for _, w := range h.Won {
		cov := Covers(h.recordsFrom(w), floor)
		if cov || mut("handback_unknown_counts") {
			covering++
		}
		for _, r := range w.Recs {
			if cov && r.Name != "" && r.Name != h.Self && r.Name != h.Peer && r.Epoch > floor {
				return 0, false, HandBackRecordsUnknown
			}
		}
	}
	if covering < 2 {
		return 0, false, HandBackRecordsUnknown
	}
	return floor, true, ""
}

// nth is the i-th highest of vs (0 the highest), 0 when there is none.
func nth(vs []uint64, i int) uint64 {
	s := slices.Clone(vs)
	slices.Sort(s)
	if i >= len(s) {
		return 0
	}
	return s[len(s)-1-i]
}
