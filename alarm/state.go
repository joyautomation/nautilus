package alarm

import (
	"crypto/rand"
	"encoding/hex"
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
	// lent is the name Rename gave the alarm, read in place of the
	// definition's own wherever the alarm is shown (displayName); "" while
	// it has none. The definition itself is never changed.
	lent string

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
	// suppKind is the kind of suppression (SuppressOutOfService, …) while
	// suppressed; suppBy who suppressed it, for an operator's action.
	suppKind, suppBy string
	// eqKey is the NameKey of def.Equipment (equipment names compare as
	// tag names do); swReason the designed suppression's reason and
	// subAddr the substitute it was last suppressed for, kept so a scan
	// that finds the same suppression builds no string.
	eqKey, swReason, subAddr string

	// warn says why the alarm is shown although a suppression may have
	// been meant: its suppress-when could not be read (warnMissing,
	// warnNotBool). A code, not text, so a scan allocates nothing.
	warn uint8

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

	// occ names the current occurrence (#715): a random ID drawn when the
	// alarm activates out of Normal, kept until it next does. A flap
	// between unack-active and unack-rtn is the same occurrence (it is one
	// wait on a person, discharged by one ack); a return to normal ends
	// it. An ack may name the occurrence it is for, and is refused by any
	// other (Engine.AckIDsAt).
	occ string

	// carry is an occurrence recovered from the retain store or a pair's
	// log at a restore (RestoreAlarms), held until the alarm's condition
	// is read: true continues it (its ID, activation, wait and, unless a
	// false reading came first, its ack), anything else ends it. See
	// Engine.judgeCarry.
	carry *carried

	// judged and adoptable: since the last restore (a fresh engine, or
	// ResetOperatorState), judged turns true at the first evaluation that
	// reads the condition, and adoptable says the condition read true
	// there and at every reading since, with no carried occurrence: the
	// alarm has stood throughout, so it may be the occurrence a committed
	// ack names (Engine.AckCommitted), which a takeover that could not
	// read the log did not continue. Cleared by a false reading, an end
	// (normal, suppressed) and an adoption.
	judged, adoptable bool

	// waitAt is when the alarm began waiting on a person: set as it
	// enters an unacknowledged state from any other, kept while it moves
	// between unack-active and unack-rtn (a latched alarm that flaps is
	// still the same wait), cleared by an ack, a shelf or a suppression.
	// Escalation (#715) runs from it. Maintained by emit.
	waitAt time.Time

	// seen is what came back as the alarm's last activation was seen
	// (Record.SeenWhen); "" for an activation seen as it happened.
	seen string
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
	if in.state == Normal || in.occ == "" || mutAlarm("occurrence_per_activation") {
		in.occ = newOccurrence()
	}
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

// displayName is what the alarm reads as: the name Rename lent it, else
// its definition's.
func (in *instance) displayName() string {
	if in.lent != "" {
		return in.lent
	}
	return in.def.Name
}

// carried is an occurrence held at a restore until the condition is read
// (instance.carry).
type carried struct {
	occ      string
	activeAt time.Time
	waitAt   time.Time
	acked    bool
	ackBy    string
	ackAt    time.Time
	// sawFalse: a reading of false came first, inside the grace. The
	// occurrence may still continue (the reading may be a driver's
	// default before its first read), but never acknowledged: a new
	// activation inside the grace must not arrive pre-acknowledged.
	sawFalse bool
	// falseUntil is the end of the grace for a false reading (Options.
	// CarryGrace); holdUntil the end of the wait for a readable
	// condition (Options.CarryHold).
	falseUntil time.Time
	holdUntil  time.Time
}

// newOccurrence draws an occurrence ID: 16 lowercase hex characters,
// random, so an ID is never reused after a restart (a counter would be).
func newOccurrence() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
