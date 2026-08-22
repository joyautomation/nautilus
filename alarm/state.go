package alarm

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// State is one alarm's ISA-18.2 state.
//
// The two axes are "is the condition present" and "has an operator seen
// it", which is why return-to-normal is not the end of an alarm's life:
// Unack-RTN is the state that keeps a transient on the screen until
// somebody acknowledges it happened.
type State uint8

const (
	Normal      State = iota // condition clear, nothing outstanding
	UnackActive              // condition present, not acknowledged
	AckActive                // condition present, acknowledged
	UnackRTN                 // condition cleared, acknowledgement outstanding
	Shelved                  // operator-silenced until a deadline; always expires
	Suppressed               // not evaluated: enable false, or the tag is absent
)

var stateNames = [...]string{"normal", "unack-active", "ack-active", "unack-rtn", "shelved", "suppressed"}

func (s State) String() string {
	if int(s) < len(stateNames) {
		return stateNames[s]
	}
	return "normal"
}

// ParseState accepts the lowercase wire tokens, case-insensitively.
func ParseState(s string) (State, error) {
	for i, n := range stateNames {
		if strings.EqualFold(s, n) {
			return State(i), nil
		}
	}
	return 0, fmt.Errorf("unknown alarm state %q (want one of %s)",
		s, strings.Join(stateNames[:], ", "))
}

func (s State) MarshalJSON() ([]byte, error) { return json.Marshal(s.String()) }

func (s *State) UnmarshalJSON(b []byte) error {
	var str string
	if err := json.Unmarshal(b, &str); err != nil {
		return err
	}
	v, err := ParseState(str)
	if err != nil {
		return err
	}
	*s = v
	return nil
}

func (s State) MarshalYAML() (any, error) { return s.String(), nil }

func (s *State) UnmarshalYAML(node *yaml.Node) error {
	v, err := ParseState(node.Value)
	if err != nil {
		return fmt.Errorf("line %d: %w", node.Line, err)
	}
	*s = v
	return nil
}

// Annunciating reports whether the state belongs on the active list: the
// condition is present, or an acknowledgement is outstanding, or an
// operator has deliberately parked it. Normal and Suppressed are the two
// states nobody needs to look at.
func (s State) Annunciating() bool {
	switch s {
	case UnackActive, AckActive, UnackRTN, Shelved:
		return true
	}
	return false
}

// Unacked reports whether the state is waiting on an operator.
func (s State) Unacked() bool { return s == UnackActive || s == UnackRTN }

// instance is the engine's per-definition state. One of these exists for
// the life of the engine, in definition order, so Evaluate is a linear walk
// over a slice with no allocation.
type instance struct {
	def Def

	state State
	// prior is the state a shelve interrupted, restored when the shelf
	// expires or an operator unshelves. Shelve is time-boxed by design:
	// there is no permanent shelf, because a permanently silenced alarm is
	// indistinguishable from a broken one.
	prior State

	raw      bool      // the bit as last read
	rawSince time.Time // when raw last changed — the delay's reference point
	cond     bool      // the QUALIFIED condition: raw, held for on/off-delay

	activeAt time.Time
	rtnAt    time.Time
	ackAt    time.Time
	ackBy    string

	shelfUntil time.Time
	shelfBy    string

	count  int    // activations since start, for flood detection
	reason string // why suppressed

	// retainedAck carries an acknowledgement recovered from the retain
	// store across the FIRST activation only. On a restart or a standby
	// takeover the field re-asserts the condition, so the alarm walks
	// Normal → Unack-Active again; without this, a failover would
	// resurrect hundreds of acked alarms as unacked, which is the exact
	// failure mode retaining ack exists to prevent.
	retainedAck   bool
	retainedAckBy string
	retainedAckAt time.Time

	started bool // seen at least one evaluation
}

// qualify folds the raw bit into the qualified condition, applying the
// on-delay to the rising edge and the off-delay to the falling one. It is
// separate from the transition table because delays are about the SIGNAL
// and the table is about the OPERATOR: keeping them apart is what lets a
// 5-minute on-delay be tested by advancing a stopped clock.
func (in *instance) qualify(raw bool, now time.Time) {
	if !in.started || raw != in.raw {
		in.raw, in.rawSince, in.started = raw, now, true
	}
	switch {
	case raw && !in.cond && now.Sub(in.rawSince) >= in.def.OnDelay:
		in.cond = true
	case !raw && in.cond && now.Sub(in.rawSince) >= in.def.OffDelay:
		in.cond = false
	}
}

// step runs the ISA-18.2 transition table for one evaluation and returns
// the event kinds it produced, in order. It never emits while Shelved or
// Suppressed — those are handled by the caller, which owns entering and
// leaving them.
//
//	from         | event                        | to
//	-------------+------------------------------+---------------------------
//	Normal       | cond true (held on-delay)    | Unack-Active  (+active)
//	Unack-Active | cond false (held off-delay)  | Unack-RTN     (+rtn)
//	Ack-Active   | cond false                   | Normal        (+rtn)
//	             |   ... with auto-clear false  | Unack-RTN     (+rtn)
//	Unack-RTN    | cond true again              | Unack-Active  (+active)
//
// ack-required false collapses Unack-Active to Ack-Active and Unack-RTN to
// Normal on arrival — the annunciate-only contacts that have no operator
// workflow at all.
func (in *instance) step(now time.Time) []string {
	var kinds []string
	switch in.state {
	case Normal:
		if in.cond {
			in.activate(now)
			kinds = append(kinds, KindActive)
		}
	case UnackActive:
		if !in.cond {
			in.rtnAt = now
			in.state = in.collapse(UnackRTN)
			kinds = append(kinds, KindRTN)
		}
	case AckActive:
		if !in.cond {
			in.rtnAt = now
			next := Normal
			if !in.def.AutoClear {
				// Latched: the alarm happened, and clearing the field bit
				// does not discharge the operator's obligation to see it.
				next = UnackRTN
			}
			in.state = in.collapse(next)
			kinds = append(kinds, KindRTN)
		}
	case UnackRTN:
		if in.cond {
			in.activate(now)
			kinds = append(kinds, KindActive)
		}
	}
	return kinds
}

func (in *instance) activate(now time.Time) {
	in.activeAt = now
	in.rtnAt = time.Time{}
	in.ackAt = time.Time{}
	in.ackBy = ""
	in.count++
	in.state = in.collapse(UnackActive)
	// A retained acknowledgement applies to the activation that was already
	// outstanding when the process stopped — the first one after restore —
	// and to nothing after it.
	if in.retainedAck {
		in.retainedAck = false
		if in.state == UnackActive {
			in.state = AckActive
			in.ackAt, in.ackBy = in.retainedAckAt, in.retainedAckBy
		}
	}
}

// collapse applies ack-required: without it there is no unacknowledged
// state to sit in, so the alarm arrives already discharged.
func (in *instance) collapse(s State) State {
	if in.def.AckRequired {
		return s
	}
	switch s {
	case UnackActive:
		return AckActive
	case UnackRTN:
		return Normal
	}
	return s
}

// ack discharges the operator's obligation, returning true if it changed
// anything. Acking a shelved alarm acks the state underneath the shelf, so
// unshelving does not re-annunciate something already seen.
func (in *instance) ack(now time.Time, by string) bool {
	target := &in.state
	if in.state == Shelved {
		target = &in.prior
	}
	switch *target {
	case UnackActive:
		*target = AckActive
	case UnackRTN:
		*target = Normal
	default:
		return false
	}
	in.ackAt, in.ackBy = now, by
	return true
}
