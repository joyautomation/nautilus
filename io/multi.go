package io

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
)

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
	children []NamedDriver
	inOwner  map[string]int // input tag name → index into children
	outOwner map[string]int // output tag name → index into children

	// mu guards scratch, the per-child delivery maps reused across scans
	// for children that implement BatchReader (their contract says "the
	// same map back each scan"). Reads are serialized by the runtime, but
	// nothing in the Driver contract promises that, so the cheap lock
	// stays.
	mu      sync.Mutex
	scratch []Values
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
	}
	return m, nil
}

// Children returns the composed drivers in configuration order — for status
// reporting, which keeps one row per child rather than blending them.
func (m *Multi) Children() []NamedDriver {
	return append([]NamedDriver(nil), m.children...)
}

// InputNames merges the children's input tags; with ownership disjoint the
// union is exact. Sorted, like every child's own list.
func (m *Multi) InputNames() []string {
	out := make([]string, 0, len(m.inOwner))
	for tag := range m.inOwner {
		out = append(out, tag)
	}
	sort.Strings(out)
	return out
}

// OutputNames merges the children's writable tags.
func (m *Multi) OutputNames() []string {
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
// claims is dropped silently — the same tolerance a single driver extends
// to a tag it has no binding for. Errors from several children are joined,
// each naming its child.
func (m *Multi) WriteOutputs(vals Values) error {
	perChild := make([]Values, len(m.children))
	for k, v := range vals {
		i, ok := m.outOwner[k]
		if !ok {
			continue
		}
		if perChild[i] == nil {
			perChild[i] = Values{}
		}
		perChild[i][k] = v
	}
	var errs []error
	for i, sub := range perChild {
		if len(sub) == 0 {
			continue
		}
		if err := m.children[i].Driver.WriteOutputs(sub); err != nil {
			errs = append(errs, fmt.Errorf("driver %q: %w", m.children[i].Name, err))
		}
	}
	return errors.Join(errs...)
}

// Quality merges the children's reports. Ownership is disjoint, so the
// merge can never see two opinions about one tag; children that report
// nothing contribute nothing, exactly as if they ran alone.
func (m *Multi) Quality() map[string]Quality {
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

// Start fans out to every child with its own loop to launch (eip's poll
// loop, a Sparkplug host's MQTT session); a child without Start has
// nothing to start, exactly as `naut run` treats a lone driver.
func (m *Multi) Start(ctx context.Context) {
	for _, c := range m.children {
		if s, ok := c.Driver.(interface{ Start(context.Context) }); ok {
			s.Start(ctx)
		}
	}
}

// Stop tears the children down in reverse start order, so a driver that
// says goodbye on the wire (a Sparkplug host's STATE death certificate)
// does it on a session nothing has pulled down yet.
func (m *Multi) Stop() {
	for i := len(m.children) - 1; i >= 0; i-- {
		if s, ok := m.children[i].Driver.(interface{ Stop() }); ok {
			s.Stop()
		}
	}
}

// Multi carries every optional refinement its children might, so composing
// drivers never costs the runtime a capability a lone driver had.
var (
	_ Driver          = (*Multi)(nil)
	_ BatchReader     = (*Multi)(nil)
	_ QualityReporter = (*Multi)(nil)
)
