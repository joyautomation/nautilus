//go:build !js

package io

import (
	"errors"
	"fmt"
	"sort"
)

// The takeover hold's refinements (hold.go), fanned out to the children
// that have them. The playground runs no pair, so they stay out of wasm.

// ForgetOutputs tells every child that re-sends on its own to forget what
// it sent (OutputsForgetter).
func (m *Multi) ForgetOutputs() {
	m.set.RLock()
	defer m.set.RUnlock()
	for _, c := range m.children {
		if f, ok := c.Driver.(OutputsForgetter); ok {
			f.ForgetOutputs()
		}
	}
}

// WriteSafe routes each safe value to the child that owns the output. One
// owned by a child that cannot write safe values (no SafeWriter) is not
// sent, is reported undelivered, and is an error naming the driver: load
// refuses such a manifest, so it is a bug when it happens.
func (m *Multi) WriteSafe(vals Values) error {
	m.set.RLock()
	defer m.set.RUnlock()
	perChild := make([]Values, len(m.children))
	var orphans []string
	var errs []error
	for k, v := range vals {
		i, ok := m.outOwner[k]
		if !ok {
			orphans = append(orphans, k)
			errs = append(errs, fmt.Errorf("safe value for %q: no driver owns that output", k))
			continue
		}
		if _, ok := m.children[i].Driver.(SafeWriter); !ok {
			orphans = append(orphans, k)
			errs = append(errs, fmt.Errorf("safe value for %q: driver %q does not write safe values", k, m.children[i].Name))
			continue
		}
		if perChild[i] == nil {
			perChild[i] = Values{}
		}
		perChild[i][k] = v
	}
	for i, c := range m.children {
		sw, ok := c.Driver.(SafeWriter)
		if !ok {
			continue
		}
		// Every SafeWriter child gets the call, an empty set included, so
		// one that held an earlier set stops reporting it.
		sub := perChild[i]
		if sub == nil {
			sub = Values{}
		}
		if err := sw.WriteSafe(sub); err != nil {
			errs = append(errs, fmt.Errorf("driver %q: %w", c.Name, err))
		}
	}
	sort.Strings(orphans)
	m.mu.Lock()
	m.safeOrphans = orphans
	m.mu.Unlock()
	return errors.Join(errs...)
}

// SafeUndelivered merges the children's undelivered safe outputs, with the
// ones no child could take.
func (m *Multi) SafeUndelivered() []string {
	m.set.RLock()
	defer m.set.RUnlock()
	m.mu.Lock()
	out := append([]string(nil), m.safeOrphans...)
	m.mu.Unlock()
	for _, c := range m.children {
		if sw, ok := c.Driver.(SafeWriter); ok {
			out = append(out, sw.SafeUndelivered()...)
		}
	}
	return out
}

var (
	_ OutputsForgetter = (*Multi)(nil)
	_ SafeWriter       = (*Multi)(nil)
)
