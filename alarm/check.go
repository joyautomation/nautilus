package alarm

import (
	"fmt"
	"time"
)

// Checks without effect, for a redundant pair (#710 PR 3b-2c): the tag
// API validates an ack, a shelve or an unshelve against the leader's
// engine, then proposes it to the operator-intent log; every replica
// applies the committed entry. Each answers what the matching call would
// refuse, and changes nothing.

// UnackedIDs are the alarms an "acknowledge everything" would change now:
// what a pair proposes in place of "all", since a standby's engine can't
// know which alarms are unacknowledged.
func (e *Engine) UnackedIDs() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []string
	for i := range e.inst {
		in := &e.inst[i]
		st := in.state
		if st == Shelved {
			st = in.prior
		}
		if st == UnackActive || st == UnackRTN {
			out = append(out, in.def.ID)
		}
	}
	return out
}

// AckableIDs is what Ack(ids) would refuse (an id naming no alarm), and
// the ids among them it would change: a pair proposes only those, so an
// ack of an alarm with nothing to acknowledge is never recorded (#1033
// review).
func (e *Engine) AckableIDs(ids []string) ([]string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []string
	for _, id := range ids {
		i, ok := e.byID[id]
		if !ok {
			return nil, fmt.Errorf("%w %q", ErrNoAlarm, id)
		}
		in := &e.inst[i]
		st := in.state
		if st == Shelved {
			st = in.prior
		}
		if st == UnackActive || st == UnackRTN {
			out = append(out, id)
		}
	}
	return out, nil
}

// AckTarget is one alarm an ack would change, on the occurrence it is on
// now: what a pair's ack entry carries (#715).
type AckTarget struct {
	ID         string `json:"id"`
	Occurrence string `json:"occ,omitempty"`
	ActiveMs   int64  `json:"activeMs,omitempty"`
}

// AckTargets is AckIDsAt without effect, read under one lock: what it
// would refuse (an id naming no alarm; an alarm occ names on another
// occurrence now), and the alarms it would change with the occurrence
// each is on. ids nil means every unacknowledged alarm (occ ignored).
func (e *Engine) AckTargets(ids []string, occ map[string]string) ([]AckTarget, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	var idx []int
	if ids == nil {
		for i := range e.inst {
			idx = append(idx, i)
		}
	} else {
		for _, id := range ids {
			i, ok := e.byID[id]
			if !ok {
				return nil, fmt.Errorf("%w %q", ErrNoAlarm, id)
			}
			if err := e.inst[i].sameOccurrence(occ); err != nil {
				return nil, err
			}
			idx = append(idx, i)
		}
	}
	var out []AckTarget
	for _, i := range idx {
		in := &e.inst[i]
		if st := in.underShelf(); st == UnackActive || st == UnackRTN {
			out = append(out, AckTarget{ID: in.def.ID, Occurrence: in.occ, ActiveMs: in.activeAt.UnixMilli()})
		}
	}
	return out, nil
}

// Occurrence is one alarm's current occurrence as a pair's log keeps it
// for a takeover (#715): its ID, activation and wait.
type Occurrence struct {
	ID         string `json:"id"`
	Occurrence string `json:"occ"`
	ActiveMs   int64  `json:"activeMs"`
	WaitMs     int64  `json:"waitMs,omitempty"`
}

// Occurrences lists the alarms on an occurrence whose condition is
// present (unack-active or ack-active, or shelved over either), by id,
// with each one's definition for a caller that selects among them. An
// alarm in unack-rtn is left out: its condition is gone, so a takeover
// could not continue it anyway.
func (e *Engine) Occurrences() map[string]OccurrenceOf {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := map[string]OccurrenceOf{}
	for i := range e.inst {
		in := &e.inst[i]
		if st := in.underShelf(); in.occ == "" || (st != UnackActive && st != AckActive) {
			continue
		}
		o := Occurrence{ID: in.def.ID, Occurrence: in.occ, ActiveMs: in.activeAt.UnixMilli()}
		if !in.waitAt.IsZero() {
			o.WaitMs = in.waitAt.UnixMilli()
		}
		out[in.def.ID] = OccurrenceOf{Occurrence: o, Def: in.def, Waiting: !in.waitAt.IsZero() || (!in.def.AckRequired && in.state == AckActive)}
	}
	return out
}

// OccurrenceOf is an Occurrence with its alarm's definition, and whether
// it pages anybody now (escalation's wait: unacknowledged, or active
// with ack-required false).
type OccurrenceOf struct {
	Occurrence
	Def     Def
	Waiting bool
}

// AckHeld reports whether alarm id's current occurrence is acknowledged
// (ack-active, or shelved over it), and which occurrence that is.
func (e *Engine) AckHeld(id string) (string, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	i, ok := e.byID[id]
	if !ok {
		return "", false
	}
	in := &e.inst[i]
	return in.occ, in.underShelf() == AckActive
}

// AckHeldIDs are the alarms whose current occurrence is acknowledged:
// ack-active, or shelved over ack-active. A pair's leader reconciles its
// log's alarm view against it.
func (e *Engine) AckHeldIDs() map[string]bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := map[string]bool{}
	for i := range e.inst {
		in := &e.inst[i]
		if in.state == AckActive || (in.state == Shelved && in.prior == AckActive) {
			out[in.def.ID] = true
		}
	}
	return out
}

// CheckShelve is what Shelve(id, until) would refuse.
func (e *Engine) CheckShelve(id string, until time.Time) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	i, ok := e.byID[id]
	if !ok {
		return fmt.Errorf("%w %q", ErrNoAlarm, id)
	}
	if !e.inst[i].def.Shelvable {
		return fmt.Errorf("alarm %q is not shelvable", id)
	}
	if !until.After(e.now()) {
		return fmt.Errorf("alarm %q: shelve until %s is not in the future", id, until.Format(time.RFC3339))
	}
	return nil
}

// CheckUnshelve is what Unshelve(id) would refuse.
func (e *Engine) CheckUnshelve(id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	i, ok := e.byID[id]
	if !ok {
		return fmt.Errorf("%w %q", ErrNoAlarm, id)
	}
	if e.inst[i].shelfUntil.IsZero() {
		return fmt.Errorf("alarm %q is not shelved", id)
	}
	return nil
}
