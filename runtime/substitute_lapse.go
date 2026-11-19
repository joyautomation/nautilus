package runtime

import (
	"log/slog"
	"time"
)

// Lapsed substitute starts (#710, the #1030 review).
//
// A start proposed while the operator-intent log had no majority is
// stored on its proposer alone and answered "outcome unknown". If that
// proposer dies and comes back much later, the new leader's catch-up
// adopts the start, and committing it as usual would start the
// substitute then, on every replica, long after the operator stopped
// waiting for it. Instead, a new leader names such starts, older than
// SubstituteStartCommitBound by its own clock, in the log entry that
// commits them (oplog.KindLapse), and every replica, deciding by that
// entry alone, applies them as lapsed:
//
//   - A start that never ran here (ApplyLapsedSubstitute) is recorded as
//     lapsed, never started: listed by SubstituteLapses (GET
//     /api/substitutes "lapsed"), journalled once by the leader that
//     named it, and no alarm. Its request ID is not taken, so a retry
//     with it starts afresh.
//   - A start that had already started here (ApplyLapse finds it active:
//     its commit was known only outside the majority the new leader
//     heard, and it may have been answered 200) is ended, and that is
//     loud: a stop with reason "lapsed", journalled as "substitute ended:
//     lapsed at takeover (its commit could not be confirmed)", and listed.
//
// A stop, an acknowledgement or an expiry naming a lapsed ID ends
// nothing: the entry is not active.

// SubstituteStartCommitBound is how old a leftover substitute start may
// be, by the committing leader's clock, and still be carried out. It
// covers SubstituteSkew (30 s) between the proposer's clock and the new
// leader's, and a takeover's catch-up.
const SubstituteStartCommitBound = 60 * time.Second

// SubstituteReasonLapsed ends a substitute that a lapse named after it had
// started here.
const SubstituteReasonLapsed = "lapsed"

// SubstituteEventLapse records a start that lapsed before it ever ran
// here (Entry carries what it would have started).
const SubstituteEventLapse = "lapse"

// SubstituteLapse is one lapsed start, as GET /api/substitutes lists it.
type SubstituteLapse struct {
	ID        uint64 `json:"id"`
	Name      string `json:"name"`
	Value     any    `json:"value"`
	By        string `json:"by"`
	RequestID string `json:"requestId,omitempty"`
	// ProposedMs is when the start was proposed, on its proposer's clock.
	ProposedMs int64 `json:"proposedMs"`
	// AtMs is when this replica applied the lapse.
	AtMs int64 `json:"atMs"`
	// Ended: the start had begun here, and the lapse ended it.
	Ended bool `json:"ended,omitempty"`
	// journalled: the lapse's author has journalled it. A lapse that
	// missed its majority is followed by another naming the same start
	// (#1032 review); it is journalled once.
	journalled bool
}

// maxLapses bounds the lapses kept for the listing.
const maxLapses = 32

// ApplyLapsedSubstitute applies a committed start that a lapse names, on a
// replica where it has not started: recorded, never started.
func (r *Runtime) ApplyLapsedSubstitute(e ReplicatedSubstitute) {
	r.dropExpiry(e.ID)
	r.subMu.Lock()
	defer r.subMu.Unlock()
	if e.ID > r.intent.applied {
		r.intent.applied = e.ID // a later catch-up's retain fallback won't re-add it
	}
	if e.ID > r.subSeq {
		r.subSeq = e.ID
	}
	delete(r.intent.unconfirmed, e.ID)
	at := r.wallNow().UnixMilli()
	var evs []SubstituteEvent
	// A copy from this replica's retain store (a catch-up-failed takeover
	// added it) ends: the log says it never began.
	r.tags.mu.Lock()
	s, held := r.tags.unsubstituteLocked(e.ID)
	r.tags.mu.Unlock()
	if !held {
		s, held = r.pendingRestore(e.ID)
	}
	if held {
		evs = append(evs, SubstituteEvent{Kind: SubstituteEventStop, Reason: SubstituteReasonLapsed, Entry: s, By: "system", AtMs: at, Replicated: true})
		evs = append(evs, faultCleared(s, at)...)
	}
	// Ended when a copy was running here: that is loud (ApplyLapse
	// journals it), not a quiet "never started".
	r.recordLapseLocked(SubstituteLapse{ID: e.ID, Name: e.Name, Value: e.Value, By: e.By, RequestID: e.RequestID,
		ProposedMs: e.UntilMs - e.GrantedMs, AtMs: at, Ended: held})
	slog.Info("runtime: a substitute start lapsed: its outcome was unknown and it was committed too late; not started",
		"id", e.ID, "tag", e.Name, "requestId", e.RequestID)
	r.publishSub(evs)
}

// ApplyLapse applies a committed lapse (oplog.KindLapse) naming ids. One
// still active here started before the lapse reached this replica: it
// ends, loudly. One recorded as lapsed (ApplyLapsedSubstitute) is
// journalled when journal is set (the leader that named it). by is the
// lapse's author ("system").
func (r *Runtime) ApplyLapse(ids []uint64, by string, journal bool) {
	for _, id := range ids {
		r.applyLapse(id, by, journal)
	}
}

func (r *Runtime) applyLapse(id uint64, by string, journal bool) {
	r.dropExpiry(id)
	r.subMu.Lock()
	defer r.subMu.Unlock()
	at := r.wallNow().UnixMilli()
	r.tags.mu.Lock()
	s, ok := r.tags.unsubstituteLocked(id)
	r.tags.mu.Unlock()
	if !ok {
		s, ok = r.pendingRestore(id)
	}
	if ok {
		for k, seen := range r.intent.reqs {
			if seen.id == id {
				delete(r.intent.reqs, k) // never started, as far as the log goes: a retry starts
			}
		}
		r.recordLapseLocked(SubstituteLapse{ID: s.ID, Name: s.Name, Value: s.Value, By: s.By, ProposedMs: s.StartMs, AtMs: at, Ended: true, journalled: journal})
		slog.Warn("runtime: substitute ended: lapsed at takeover (its commit could not be confirmed)", "id", id, "tag", s.Name)
		evs := []SubstituteEvent{{Kind: SubstituteEventStop, Reason: SubstituteReasonLapsed, Entry: s, By: by, AtMs: at, Replicated: !journal}}
		r.publishSub(append(evs, faultCleared(s, at)...))
		return
	}
	if !journal {
		return
	}
	for i, l := range r.intent.lapses {
		if l.ID != id || l.journalled {
			continue
		}
		r.intent.lapses[i].journalled = true
		kind := SubstituteEventLapse // never ran here: journalled quietly
		if l.Ended {
			kind = SubstituteEventStop // a copy ran here and ApplyLapsedSubstitute ended it: loud
		}
		r.publishSub([]SubstituteEvent{{Kind: kind, Reason: SubstituteReasonLapsed,
			Entry: Substitution{ID: l.ID, Name: l.Name, Value: l.Value, By: l.By, StartMs: l.ProposedMs, RequestID: l.RequestID}, By: by, AtMs: at}})
		return
	}
}

// recordLapseLocked keeps l for SubstituteLapses. Caller holds subMu.
func (r *Runtime) recordLapseLocked(l SubstituteLapse) {
	for i, old := range r.intent.lapses {
		if old.ID == l.ID {
			if l.Ended || !old.Ended {
				l.journalled = l.journalled || old.journalled
				r.intent.lapses[i] = l
			}
			return
		}
	}
	r.intent.lapses = append(r.intent.lapses, l)
	if len(r.intent.lapses) > maxLapses {
		r.intent.lapses = r.intent.lapses[len(r.intent.lapses)-maxLapses:]
	}
}

// SubstituteLapses lists the recent lapsed starts, oldest first.
func (r *Runtime) SubstituteLapses() []SubstituteLapse {
	r.subMu.Lock()
	defer r.subMu.Unlock()
	return append([]SubstituteLapse(nil), r.intent.lapses...)
}
