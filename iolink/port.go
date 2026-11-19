package iolink

import (
	"fmt"

	"github.com/joyautomation/nautilus/iolink/iodd"
)

// State is what the identity check says about a port (docs/design/
// iolink.md §6).
type State int

// The identity states, in the order of the <Port device>__Identity tag.
const (
	// Ok: the master reports the ids the IODD declares.
	Ok State = iota
	// NoDevice: the port reports no device (ids 0).
	NoDevice
	// WrongDevice: the ids differ from the IODD's.
	WrongDevice
	// Unchecked: the master profile has no id points, so nothing can be
	// compared.
	Unchecked
	// AwaitingRearm is not a result of Check: the identity is Ok (or
	// Unchecked) but a port with process data out has not been re-armed
	// since its device went wrong (or since a first check that was not
	// Ok), so it sends nothing. It is what the identity tag reads then, and it is a fault
	// (the alarm stays active) until the pulse on __Rearm.
	AwaitingRearm
)

func (s State) String() string {
	switch s {
	case Ok:
		return "Ok"
	case NoDevice:
		return "NoDevice"
	case WrongDevice:
		return "WrongDevice"
	case Unchecked:
		return "Unchecked"
	case AwaitingRearm:
		return "AwaitingRearm"
	}
	return fmt.Sprintf("State(%d)", int(s))
}

// Fault is true in the states that raise the identity fault (the alarm).
func (s State) Fault() bool { return s == NoDevice || s == WrongDevice || s == AwaitingRearm }

// Drives is true in the states in which the port's outputs may be driven:
// the right device is known to be there, or nothing can be known.
func (s State) Drives() bool { return s == Ok || s == Unchecked }

// Check compares the (vendor id, device id) a master reports for a port
// with the IODD's. haveIDs is false when the master profile names no id
// points.
func Check(d *iodd.IODD, haveIDs bool, vendor, device uint32) State {
	switch {
	case !haveIDs:
		return Unchecked
	case vendor == 0 && device == 0:
		return NoDevice
	case d.Identity.Matches(vendor, device):
		return Ok
	}
	return WrongDevice
}

// OutPlan is what a port's outputs should be right now.
type OutPlan struct {
	// Valid is the value for the master's PD-out-valid point (the P+F's
	// PDO Valid), where the profile has one: false whenever the device on
	// the port is not known to be the right one, so the master stops
	// treating its image as valid and the device falls back to its own
	// fail-safe values.
	Valid bool
	// Image is the process data out to write to the master, nil when it
	// must not be written (Valid is false, or nothing has been written).
	Image []byte
}

// OutGate decides what is sent to a port's process data out. Its rules,
// each of them there because a master keeps the last PD-out image it was
// given and sends it to whatever device is on the port:
//
//   - nothing is sent, and Valid is false, until the port has been
//     ARMED. The FIRST identity check arms it when it is Ok (or
//     Unchecked): at a power-up or a takeover (a new leader builds a new
//     gate) an Ok identity is the device the program was written for, so
//     neither needs a pulse. After that only an explicit Rearm arms it (a
//     pulse on the port's __Rearm tag, journalled like any write): the
//     normal industrial "confirm after a device swap";
//   - a port whose first check is Wrong or NoDevice is not armed, and stays
//     so when the right device appears, until Rearm;
//   - while the identity is not Ok (or Unchecked), nothing is sent, Valid
//     is false, and the port is disarmed: not zeros either, since "off" is
//     not always safe. When the right device is back the port stays quiet
//     (the identity reads AwaitingRearm) until Rearm;
//   - Rearm while the identity is not Ok is refused;
//   - an armed port sends the program's CURRENT value of every item, so
//     nothing a program wrote before or during the break is replayed: the
//     values sent are the ones it holds when the operator confirms.
//
// Unchecked (the master profile has no id points) drives like Ok: there is
// nothing to compare, so a wrong device cannot be told from the right one
// here. naut check says so for such a master, and the pd-out-valid rule
// cannot protect it. A port with no process data out has no gate.
type OutGate struct {
	out    *iodd.Image
	state  State
	values []any // the program's current value per item; nil is zero
	armed  bool
	// checked is set by the first SetState: the check that may arm.
	checked bool
}

// NewOutGate is a gate for a port device whose process data out is out,
// starting in state s (the port has not been checked yet: pass NoDevice to
// hold everything until it has) and not armed; the first SetState decides.
func NewOutGate(out *iodd.Image, s State) *OutGate {
	return &OutGate{out: out, state: s, values: make([]any, len(out.Items))}
}

// SetState records the identity check. The first one arms the port when it
// is Ok or Unchecked; leaving a driving state disarms it.
func (g *OutGate) SetState(s State) {
	first := !g.checked
	g.checked = true
	if g.state.Drives() && !s.Drives() {
		g.armed = false
	}
	g.state = s
	if first && s.Drives() {
		g.armed = true
	}
}

// Drives reports whether the identity now allows the port to be driven.
func (g *OutGate) Drives() bool { return g.state.Drives() }

// Armed reports whether the port has been armed since the identity last
// stopped allowing it.
func (g *OutGate) Armed() bool { return g.armed }

// Rearm arms the port. It is refused while the identity does not allow
// driving it, naming the state.
func (g *OutGate) Rearm() error {
	if !g.state.Drives() {
		return fmt.Errorf("refused: the device on the port is not the one expected (%s)", g.state)
	}
	g.armed = true
	return nil
}

// Set records item i's current value, whether the port is armed or not (an
// armed port sends it; a disarmed one holds it for the next Rearm). A value
// that does not fit its item is refused naming the item, and the last good
// value stays.
func (g *OutGate) Set(i int, v any) error {
	if i < 0 || i >= len(g.values) {
		return fmt.Errorf("process data out has no item %d", i)
	}
	prev := g.values[i]
	g.values[i] = v
	if _, err := g.out.Encode(g.values); err != nil {
		g.values[i] = prev
		return err
	}
	return nil
}

// Plan is what to send now.
func (g *OutGate) Plan() (OutPlan, error) {
	// SetState has already disarmed the port when the device went wrong, so
	// the state check is not what stops a send today; it is the second lock.
	if !g.state.Drives() || !g.armed {
		return OutPlan{}, nil
	}
	img, err := g.out.Encode(g.values)
	if err != nil {
		return OutPlan{}, err
	}
	return OutPlan{Valid: true, Image: img}, nil
}
