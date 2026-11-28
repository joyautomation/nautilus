package io

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"sort"
	"sync"
)

// BaselineDisarmer is a driver whose first write after Start records the
// outputs rather than sending them (a start-up baseline: the world at t=0
// is not a command), and that can be told not to: a driver swapped in live
// (#706) can then start from the current commands.
type BaselineDisarmer interface{ DisarmBaseline() }

// BaselineHolder is a driver whose start-up baseline decides each output
// against what the device holds (modbus, #1123) and that can be told to keep
// the older, stricter rule instead: the first push is recorded, never sent,
// whatever the device holds. A driver swapped in live under
// Options.HoldOutputsOnSwap is held this way: a held swap commands nothing
// until an output changes.
type BaselineHolder interface{ HoldBaseline() }

// StartReporter says whether Start has run. A driver that has not been
// started cannot be read (Modbus and OPC UA error), so Replace refuses one.
type StartReporter interface{ Started() bool }

// closedGate is the gate a child replaced out of a set gets: whatever it
// still holds queued, it must not write any more.
func closedGate() bool { return false }

// NamedDriver is one member of a multi-driver set: the Driver itself plus
// the name status rows and error messages know it by ("modbus", "eip-2").
type NamedDriver struct {
	Name   string
	Driver Driver
}

// tagOwner is what a driver must add before it can share a scan with
// others: which tags it delivers and which it accepts writes for. Every
// field-bus driver already has this pair (its bindings are its manifest);
// Memory deliberately does not — it owns whatever is written to it, which
// is exactly the property that makes it unroutable next to another driver.
type tagOwner interface {
	InputNames() []string
	OutputNames() []string
}

// Multi is several drivers presented to the runtime as one: a controller
// that reads a Modbus bus AND an MQTT feed on the same scan, without the
// runtime learning anything about either. Reads fan out and merge; writes
// route to the driver whose OutputNames claim them; Quality, Start and
// Stop fan out to the children that implement them.
//
// Ownership is disjoint by construction — NewMulti refuses a tag claimed
// by two children — so a merged read can never have two writers for one
// key and a routed write always has exactly one destination.
type Multi struct {
	// set guards the composition (children, ownership, askers, gates)
	// against Replace (#706): reads, writes, quality and the rest take it
	// shared; Replace takes it alone, so a swap lands between two calls.
	set sync.RWMutex

	children []NamedDriver
	inOwner  map[string]int // input tag name → index into children
	outOwner map[string]int // output tag name → index into children

	// askers are the children that implement OutputsRequester, found once
	// here rather than by a type assertion per child on every scan.
	askers []OutputsRequester

	// mu guards scratch, the per-child delivery maps reused across scans
	// for children that implement BatchReader (their contract says "the
	// same map back each scan"). Reads are serialized by the runtime, but
	// nothing in the Driver contract promises that, so the cheap lock
	// stays.
	mu      sync.Mutex
	scratch []Values
	// safeOrphans are the takeover hold's safe outputs the last WriteSafe
	// could hand no child (multi_hold.go). Under mu.
	safeOrphans []string

	// gates are the write gates installed so far (SetWriteGate, then any
	// SetPlainGate), in order, replayed onto a child Replace brings in.
	gates []func(Driver)

	// unclaimed are the keys a WriteOutputs held that no child takes, each
	// logged the first time (WriteOutputs). Under unclaimedMu.
	unclaimedMu sync.Mutex
	unclaimed   map[string]bool
}

// NewMulti composes drivers into one. Every child must be named, the names
// must differ, and every child must declare its tags (InputNames and
// OutputNames) — routing is impossible without them, which is why Memory,
// whose tag set is whatever you write to it, cannot join a multi set and
// keeps working alone. A tag claimed by two children is an error naming
// both, in the same spirit as a tag declared in two tag files: last-wins
// would rot silently, so it is refused up front.
func NewMulti(children ...NamedDriver) (*Multi, error) {
	if len(children) == 0 {
		return nil, errors.New("multi-driver: at least one driver is required")
	}
	m := &Multi{
		children: append([]NamedDriver(nil), children...),
		inOwner:  map[string]int{},
		outOwner: map[string]int{},
		scratch:  make([]Values, len(children)),
	}
	seen := map[string]int{}
	for i, c := range m.children {
		if c.Driver == nil {
			return nil, fmt.Errorf("multi-driver: driver %q is nil", c.Name)
		}
		if c.Name == "" {
			return nil, fmt.Errorf("multi-driver: the %T at position %d has no name — every driver in a set needs one, so status rows and errors can tell them apart", c.Driver, i+1)
		}
		if j, dup := seen[c.Name]; dup {
			return nil, fmt.Errorf("multi-driver: two drivers are both named %q (positions %d and %d) — give one a distinct name", c.Name, j+1, i+1)
		}
		seen[c.Name] = i
		owner, ok := c.Driver.(tagOwner)
		if !ok {
			return nil, fmt.Errorf("multi-driver: driver %q (%T) does not declare which tags it owns (no InputNames/OutputNames) — reads and writes cannot be routed to it, so it can only run alone", c.Name, c.Driver)
		}
		for _, tag := range owner.InputNames() {
			if j, dup := m.inOwner[tag]; dup {
				return nil, fmt.Errorf("multi-driver: input tag %q is delivered by both driver %q and driver %q — a tag may have exactly one source (rename one binding, or remove the duplicate)", tag, m.children[j].Name, c.Name)
			}
			m.inOwner[tag] = i
		}
		for _, tag := range owner.OutputNames() {
			if j, dup := m.outOwner[tag]; dup {
				return nil, fmt.Errorf("multi-driver: output tag %q is accepted by both driver %q and driver %q — a write must have exactly one destination (rename one binding, or remove the duplicate)", tag, m.children[j].Name, c.Name)
			}
			m.outOwner[tag] = i
		}
		if r, ok := c.Driver.(OutputsRequester); ok {
			m.askers = append(m.askers, r)
		}
	}
	return m, nil
}

// Children returns the composed drivers in configuration order — for status
// reporting, which keeps one row per child rather than blending them.
func (m *Multi) Children() []NamedDriver {
	m.set.RLock()
	defer m.set.RUnlock()
	return append([]NamedDriver(nil), m.children...)
}

// Child returns the composed driver named name.
func (m *Multi) Child(name string) (Driver, bool) {
	m.set.RLock()
	defer m.set.RUnlock()
	for _, c := range m.children {
		if c.Name == name {
			return c.Driver, true
		}
	}
	return nil, false
}

// Gate installs on d the write gates installed on the set so far, as
// Replace will: call it before starting a replacement, so it never runs,
// and never arms a leader's baseline on a standby, ungated.
func (m *Multi) Gate(d Driver) {
	m.set.RLock()
	defer m.set.RUnlock()
	for _, g := range m.gates {
		g(d)
	}
}

// sameDriver reports whether a and b are the same driver value, without
// panicking on a type that cannot be compared.
func sameDriver(a, b Driver) bool {
	ta := reflect.TypeOf(a)
	if ta != reflect.TypeOf(b) || !ta.Comparable() {
		return false
	}
	return a == b
}

// Replace puts d in place of the child named name and returns the child it
// replaced (#706: an I/O configuration applied live). The same checks as
// NewMulti apply: d is not nil and declares its tags, and none of them is
// owned by another child. Routing, the cached OutputsRequesters and the
// write gates installed so far follow the new child, and the old child is
// gated closed, so it writes nothing more while it winds down; the rest of
// the set is untouched. Gate and Start d before (an unstarted d is refused,
// as is the current child itself), and Stop the old child after, outside
// any lock a scan holds: Replace itself only swaps.
func (m *Multi) Replace(name string, d Driver) (Driver, error) {
	if d == nil {
		return nil, fmt.Errorf("multi-driver: the replacement for %q is nil", name)
	}
	if sr, ok := d.(StartReporter); ok && !sr.Started() {
		return nil, fmt.Errorf("multi-driver: the replacement for %q has not been started; start it (gated with Gate) first, or every input of the set reads stale", name)
	}
	owner, ok := d.(tagOwner)
	if !ok {
		return nil, fmt.Errorf("multi-driver: the replacement for %q (%T) does not declare which tags it owns (no InputNames/OutputNames)", name, d)
	}
	m.set.Lock()
	defer m.set.Unlock()
	at := -1
	for i, c := range m.children {
		if c.Name == name {
			at = i
		}
	}
	if at < 0 {
		return nil, fmt.Errorf("multi-driver: no driver named %q to replace", name)
	}
	if sameDriver(m.children[at].Driver, d) {
		return nil, fmt.Errorf("multi-driver: %q is already that driver; replacing it with itself would hand it back as the old one to stop", name)
	}
	in, out := map[string]int{}, map[string]int{}
	for tag, i := range m.inOwner {
		if i != at {
			in[tag] = i
		}
	}
	for tag, i := range m.outOwner {
		if i != at {
			out[tag] = i
		}
	}
	for _, tag := range owner.InputNames() {
		if j, dup := in[tag]; dup {
			return nil, fmt.Errorf("multi-driver: input tag %q of the replacement for %q is delivered by driver %q already", tag, name, m.children[j].Name)
		}
		in[tag] = at
	}
	for _, tag := range owner.OutputNames() {
		if j, dup := out[tag]; dup {
			return nil, fmt.Errorf("multi-driver: output tag %q of the replacement for %q is accepted by driver %q already", tag, name, m.children[j].Name)
		}
		out[tag] = at
	}
	for _, g := range m.gates {
		g(d)
	}
	old := m.children[at].Driver
	// The old child writes nothing more, whatever it holds queued (a write
	// held across a disconnect, a rewrite: keep-alive) until it is stopped.
	if g, ok := old.(interface{ SetWriteGate(func() bool) }); ok {
		g.SetWriteGate(closedGate)
	}
	if g, ok := old.(SendGated); ok {
		g.SetSendGate(ClosedSendGate) // the gate such a driver reads (design §3.4)
	}
	m.children[at].Driver = d
	m.inOwner, m.outOwner = in, out
	m.askers = m.askers[:0]
	for _, c := range m.children {
		if r, ok := c.Driver.(OutputsRequester); ok {
			m.askers = append(m.askers, r)
		}
	}
	m.mu.Lock()
	m.scratch[at] = nil // the old child's delivery map is not the new one's
	m.mu.Unlock()
	return old, nil
}

// InputNames merges the children's input tags; with ownership disjoint the
// union is exact. Sorted, like every child's own list.
func (m *Multi) InputNames() []string {
	m.set.RLock()
	defer m.set.RUnlock()
	out := make([]string, 0, len(m.inOwner))
	for tag := range m.inOwner {
		out = append(out, tag)
	}
	sort.Strings(out)
	return out
}

// OutputNames merges the children's writable tags.
func (m *Multi) OutputNames() []string {
	m.set.RLock()
	defer m.set.RUnlock()
	out := make([]string, 0, len(m.outOwner))
	for tag := range m.outOwner {
		out = append(out, tag)
	}
	sort.Strings(out)
	return out
}

// ReadInputs is the plain-Driver read: a fresh merged map each call.
func (m *Multi) ReadInputs() (Values, error) {
	dst := Values{}
	if err := m.ReadInputsInto(dst); err != nil {
		return nil, err
	}
	return dst, nil
}

// ReadInputsInto fans the read out and merges the deliveries, honouring the
// BatchReader contract on both sides: a child that implements it refills
// its own reused per-child map (never the caller's dst, whose foreign keys
// the child would be obliged to delete); a child that does not falls back
// to ReadInputs. Afterwards dst describes THIS delivery — a key no child
// delivered any more is removed.
//
// A failing child fails the whole read, the same way a single driver's
// failed poll does: the runtime holds last-known values and the error names
// the child, so "which bus?" is never a debugging step. Values other
// children delivered in the same call stay merged into dst but unused —
// per-child partial delivery is a quality story (Stale via Quality()), not
// a partial-read story.
func (m *Multi) ReadInputsInto(dst Values) error {
	m.set.RLock()
	defer m.set.RUnlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	var errs []error
	delivered := make(map[string]struct{}, len(dst))
	for i, c := range m.children {
		if br, ok := c.Driver.(BatchReader); ok {
			if m.scratch[i] == nil {
				m.scratch[i] = Values{}
			}
			if err := br.ReadInputsInto(m.scratch[i]); err != nil {
				errs = append(errs, fmt.Errorf("driver %q: %w", c.Name, err))
				continue
			}
			for k, v := range m.scratch[i] {
				dst[k] = v
				delivered[k] = struct{}{}
			}
			continue
		}
		vals, err := c.Driver.ReadInputs()
		if err != nil {
			errs = append(errs, fmt.Errorf("driver %q: %w", c.Name, err))
			continue
		}
		for k, v := range vals {
			dst[k] = v
			delivered[k] = struct{}{}
		}
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	for k := range dst {
		if _, ok := delivered[k]; !ok {
			delete(dst, k)
		}
	}
	return nil
}

// WriteOutputs routes each value to the child whose OutputNames claim it
// and skips children that own none of this delivery. A value no child
// claims reaches no device: it is not an error (a declared output tag no
// driver binds is legal, and an error here would fail every push, the
// claimed keys' too), but it is never dropped without a word either. The
// first time a key arrives unclaimed it is logged, naming it, and Unclaimed
// lists it from then on. (The runtime refuses at start an instance whose
// to: or both: leaf no driver claims, so a command of a wired instance
// never gets here.) When any child fails its share, the result is a
// *PartialWriteError: the children's errors joined, each naming its child,
// and which keys the other children took.
func (m *Multi) WriteOutputs(vals Values) error {
	m.set.RLock()
	defer m.set.RUnlock()
	perChild := make([]Values, len(m.children))
	for k, v := range vals {
		i, ok := m.outOwner[k]
		if !ok {
			m.noteUnclaimed(k)
			continue
		}
		if perChild[i] == nil {
			perChild[i] = Values{}
		}
		perChild[i][k] = v
	}
	var errs []error
	var took []Values
	for i, sub := range perChild {
		if len(sub) == 0 {
			continue
		}
		if err := m.children[i].Driver.WriteOutputs(sub); err != nil {
			errs = append(errs, fmt.Errorf("driver %q: %w", m.children[i].Name, err))
		} else {
			took = append(took, sub)
		}
	}
	if len(errs) == 0 {
		return nil
	}
	taken := map[string]bool{}
	for _, sub := range took {
		for k := range sub {
			taken[k] = true
		}
	}
	return NewPartialWriteError(errors.Join(errs...), taken)
}

// PartialWriteError is the error of a push that some of a composite
// driver's children failed while the others took their share
// (Multi.WriteOutputs). The runtime counts what was taken as handed over
// (#1497), so an event: command a healthy child took is not handed to it
// again, and only the failing children's commands are retried.
//
// Experimental: the shape may change while struct wiring (#1458) settles.
type PartialWriteError struct {
	err   error
	taken map[string]bool
}

// NewPartialWriteError is a PartialWriteError for err, whose taken keys
// were handed over to a child that took them. A key not in taken counts
// as not handed, so a composite that cannot tell fails safe: its commands
// are retried.
func NewPartialWriteError(err error, taken map[string]bool) *PartialWriteError {
	return &PartialWriteError{err: err, taken: taken}
}

func (e *PartialWriteError) Error() string {
	if e.err == nil {
		return "some children of a composite driver failed their share of a push"
	}
	return e.err.Error()
}

func (e *PartialWriteError) Unwrap() error { return e.err }

// Taken says whether key was in the share of a child that took its write.
// An unknown key, and every key of the zero value, is not taken.
func (e *PartialWriteError) Taken(key string) bool { return e.taken[key] }

// noteUnclaimed logs key the first time a write holds it and no child
// claims it.
func (m *Multi) noteUnclaimed(key string) {
	m.unclaimedMu.Lock()
	defer m.unclaimedMu.Unlock()
	if m.unclaimed[key] {
		return
	}
	if m.unclaimed == nil {
		m.unclaimed = map[string]bool{}
	}
	m.unclaimed[key] = true
	names := make([]string, len(m.children))
	for i, c := range m.children {
		names[i] = c.Name
	}
	slog.Warn("multi-driver: a write reaches no driver: no driver in the set takes this output, so its value goes nowhere",
		"key", key, "drivers", names)
}

// Unclaimed lists, sorted, every key a WriteOutputs held that no child
// claimed (each is logged once, when first seen).
func (m *Multi) Unclaimed() []string {
	m.unclaimedMu.Lock()
	defer m.unclaimedMu.Unlock()
	out := make([]string, 0, len(m.unclaimed))
	for k := range m.unclaimed {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Quality merges the children's reports. Ownership is disjoint, so the
// merge can never see two opinions about one tag; children that report
// nothing contribute nothing, exactly as if they ran alone.
func (m *Multi) Quality() map[string]Quality {
	m.set.RLock()
	defer m.set.RUnlock()
	var out map[string]Quality
	for _, c := range m.children {
		qr, ok := c.Driver.(QualityReporter)
		if !ok {
			continue
		}
		for k, q := range qr.Quality() {
			if out == nil {
				out = map[string]Quality{}
			}
			out[k] = q
		}
	}
	return out
}

// OutputsWanted asks every child that can ask and reports whether any
// did. Every child is asked, not just up to the first yes: each answer is
// a read-and-clear, and the one push the runtime makes hands every child
// its outputs, so it answers them all at once.
func (m *Multi) OutputsWanted() bool {
	m.set.RLock()
	defer m.set.RUnlock()
	wanted := false
	for _, r := range m.askers {
		if r.OutputsWanted() {
			wanted = true
		}
	}
	return wanted
}

// BeginScan tells every child that wants to know a scan is starting
// (ScanBeginner).
func (m *Multi) BeginScan() {
	m.set.RLock()
	defer m.set.RUnlock()
	for _, c := range m.children {
		if b, ok := c.Driver.(ScanBeginner); ok {
			b.BeginScan()
		}
	}
}

// InputsReady is true when every child that can say so is ready: a first
// scan (a start or a takeover) runs on the whole delivery, so it waits
// for the slowest child.
// Every such child is asked (each may look at its write gate). A set with
// no such child is always ready.
func (m *Multi) InputsReady() bool {
	m.set.RLock()
	defer m.set.RUnlock()
	ready := true
	for _, c := range m.children {
		if r, ok := c.Driver.(InputsReadier); ok && !r.InputsReady() {
			ready = false
		}
	}
	return ready
}

// InputsPending names the children whose inputs have not arrived, in set
// order (io.InputsPending).
func (m *Multi) InputsPending() []string {
	m.set.RLock()
	defer m.set.RUnlock()
	var out []string
	for _, c := range m.children {
		if r, ok := c.Driver.(InputsReadier); ok && !r.InputsReady() {
			out = append(out, c.Name)
		}
	}
	return out
}

// Start fans out to every child with its own loop to launch (eip's poll
// loop, a Sparkplug host's MQTT session); a child without Start has
// nothing to start, exactly as `naut run` treats a lone driver.
func (m *Multi) Start(ctx context.Context) {
	m.set.RLock()
	defer m.set.RUnlock()
	for _, c := range m.children {
		if s, ok := c.Driver.(interface{ Start(context.Context) }); ok {
			s.Start(ctx)
		}
	}
}

// SetWriteGate fans the leadership gate out to every child that takes one
// (modbus's re-assert loop, the hw command queues, a Sparkplug host's STATE
// and NCMD/DCMD). `naut run` installs the gate on whatever driver the
// project built; without this fan-out a standby replica's children behind a
// Multi would never see it, and two replicas could both drive a device. A
// child without a gate (eip) is skipped, exactly as it would be running
// alone.
func (m *Multi) SetWriteGate(gate func() bool) {
	install := func(d Driver) {
		if g, ok := d.(interface{ SetWriteGate(func() bool) }); ok {
			g.SetWriteGate(gate)
		}
	}
	m.set.Lock()
	defer m.set.Unlock()
	m.gates = append(m.gates, install)
	for _, c := range m.children {
		install(c.Driver)
	}
}

// SetSendGate fans a pair's send gate out to every child that checks one
// before each send (SendGated), as SetWriteGate fans the plain gate out.
// A child that doesn't keeps the plain gate SetWriteGate gave it.
func (m *Multi) SetSendGate(gate SendGate) {
	install := func(d Driver) {
		if g, ok := d.(SendGated); ok {
			g.SetSendGate(gate)
		}
	}
	m.set.Lock()
	defer m.set.Unlock()
	m.gates = append(m.gates, install)
	for _, c := range m.children {
		install(c.Driver)
	}
}

// PlainGated is a driver whose write gate is only a gate. Open, it acts
// exactly as with no gate; closed, it sends nothing of its own (no
// re-assert, no queued command); reopened, it forgets what it sent and
// asks for every output (OutputsRequester), or, for event commands that
// must never go out twice, takes the next value handed over as a baseline
// (hw.Base, a Redfish reset or an SNMP SET; there a command queued before
// the gate closed waits, and goes out on reopen while younger than its
// source's stale-after). Such a driver can take a gate
// that has nothing to do with redundancy. `naut run` gives it one in
// every project, open while the scans are live, so a hung scan stops its
// keep-alives (#625). A driver that reads "a gate is installed" as "I am
// one replica of a redundant set" (a Sparkplug host's client ID and STATE
// hand-over) is not PlainGated. SetPlainGate calls PlainWriteGate after
// installing the gate, so a driver that does treat a redundancy lease
// differently (optommp's passive standby, #865) can tell the two apart;
// for the rest it is only the marker.
type PlainGated interface {
	SetWriteGate(func() bool)
	PlainWriteGate()
}

// SetPlainGate installs gate on d if it is PlainGated, or on each child
// of a Multi that is, and leaves every other driver ungated. It reports
// how many drivers took the gate.
func SetPlainGate(d Driver, gate func() bool) int {
	if m, ok := d.(*Multi); ok {
		m.set.Lock()
		defer m.set.Unlock()
		// Remembered, so a child Replace brings in gets it too.
		m.gates = append(m.gates, func(c Driver) { SetPlainGate(c, gate) })
		n := 0
		for _, c := range m.children {
			n += SetPlainGate(c.Driver, gate)
		}
		return n
	}
	if g, ok := d.(PlainGated); ok {
		g.SetWriteGate(gate)
		g.PlainWriteGate() // so a driver can tell this gate from a redundancy lease
		return 1
	}
	return 0
}

// Stop tears the children down in reverse start order, so a driver that
// says goodbye on the wire (a Sparkplug host's STATE death certificate)
// does it on a session nothing has pulled down yet.
func (m *Multi) Stop() {
	m.set.RLock()
	defer m.set.RUnlock()
	for i := len(m.children) - 1; i >= 0; i-- {
		if s, ok := m.children[i].Driver.(interface{ Stop() }); ok {
			s.Stop()
		}
	}
}

// Multi carries every optional refinement its children might, so composing
// drivers never costs the runtime a capability a lone driver had.
var (
	_ Driver           = (*Multi)(nil)
	_ BatchReader      = (*Multi)(nil)
	_ QualityReporter  = (*Multi)(nil)
	_ OutputsRequester = (*Multi)(nil)
	_ InputsReadier    = (*Multi)(nil)
	_ InputsPending    = (*Multi)(nil)

	_ interface{ SetWriteGate(func() bool) } = (*Multi)(nil)
	_ SendGated                              = (*Multi)(nil)
)
