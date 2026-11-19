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
