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

// CheckAck is what Ack(ids) would refuse (an id naming no alarm), and how
// many alarms it would change.
func (e *Engine) CheckAck(ids []string) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := 0
	for _, id := range ids {
		i, ok := e.byID[id]
		if !ok {
			return 0, fmt.Errorf("%w %q", ErrNoAlarm, id)
		}
		in := &e.inst[i]
		st := in.state
		if st == Shelved {
			st = in.prior
		}
		if st == UnackActive || st == UnackRTN {
			n++
		}
	}
	return n, nil
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
