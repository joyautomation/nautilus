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
	}
	return fmt.Sprintf("State(%d)", int(s))
}

// Fault is true in the states that raise the identity fault (the alarm).
func (s State) Fault() bool { return s == NoDevice || s == WrongDevice }

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
//   - nothing is sent until a program has written an item;
//   - while the identity is not Ok (or Unchecked), nothing is sent and
//     Valid is false: not zeros either, since "off" is not always safe;
//   - writes made while it is not Ok are refused, not kept: when the right
//     device is back, the image is NOT resent from what was written before
//     or during the break; a program must write again, so a command is
//     never delivered to a device it was not written for.
type OutGate struct {
	out     *iodd.Image
	state   State
	values  []any
	written bool
}

// NewOutGate is a gate for a port device whose process data out is out,
// starting in state s (the port has not been checked yet: pass the first
// Check, or NoDevice to hold everything until it has).
func NewOutGate(out *iodd.Image, s State) *OutGate {
	return &OutGate{out: out, state: s, values: make([]any, len(out.Items))}
}

// SetState records the identity check. Leaving a driving state forgets
// every value written, so the next drive needs fresh writes.
func (g *OutGate) SetState(s State) {
	if g.state.Drives() && !s.Drives() {
		g.forget()
	}
	g.state = s
}

func (g *OutGate) forget() {
	clear(g.values)
	g.written = false
}

// Write records item i's value. It is refused when the identity does not
// allow driving the port, and when the value does not fit its item (the
// image is encoded here, so a value that cannot be sent is an error on the
// write, naming the item, not on a later push).
func (g *OutGate) Write(i int, v any) error {
	if i < 0 || i >= len(g.values) {
		return fmt.Errorf("process data out has no item %d", i)
	}
	if !g.state.Drives() {
		return fmt.Errorf("%s: the device on the port is not the one expected (%s): not sent", g.out.Items[i].Name, g.state)
	}
	prev := g.values[i]
	g.values[i] = v
	if _, err := g.out.Encode(g.values); err != nil {
		g.values[i] = prev
		return err
	}
	g.written = true
	return nil
}

// Plan is what to send now.
func (g *OutGate) Plan() (OutPlan, error) {
	// SetState has already forgotten what was written when the device went
	// wrong, so this check is not what stops a send today; it is the second
	// lock on a rule that must hold if SetState ever changes.
	if !g.state.Drives() {
		return OutPlan{}, nil
	}
	if !g.written {
		// Right device, nothing written yet: valid only once a program has
		// meant to drive it.
		return OutPlan{}, nil
	}
	img, err := g.out.Encode(g.values)
	if err != nil {
		return OutPlan{}, err
	}
	return OutPlan{Valid: true, Image: img}, nil
}
