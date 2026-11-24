//go:build !js

package io

import (
	"slices"
	"strings"
	"testing"
)

// The takeover hold's refinements through a composite (#710 (b2),
// multi_hold.go).

// holdChild owns In/outs, and records the hold's calls.
type holdChild struct {
	Memory
	outs    []string
	forgets int
	safe    []Values
	und     []string
}

func (c *holdChild) InputNames() []string  { return nil }
func (c *holdChild) OutputNames() []string { return c.outs }
func (c *holdChild) ForgetOutputs()        { c.forgets++ }

// safeChild can write safe values too.
type safeChild struct{ holdChild }

func (c *safeChild) WriteSafe(v Values) error {
	c.safe = append(c.safe, v)
	c.und = nil
	for k := range v {
		c.und = append(c.und, k)
	}
	return nil
}
func (c *safeChild) SafeUndelivered() []string { return c.und }

// ForgetOutputs reaches every child that forgets; WriteSafe routes each
// value to its output's child and gives every SafeWriter child the call
// (an empty set to one that owns none, so it drops an earlier set); a
// value for a child that cannot write it, or for no child, is an error
// and stays undelivered.
func TestMultiHoldFanOut(t *testing.T) {
	a := &safeChild{holdChild{Memory: Memory{v: Values{}}, outs: []string{"A1", "A2"}}}
	b := &safeChild{holdChild{Memory: Memory{v: Values{}}, outs: []string{"B1"}}}
	c := &holdChild{Memory: Memory{v: Values{}}, outs: []string{"C1"}}
	m, err := NewMulti(NamedDriver{"a", a}, NamedDriver{"b", b}, NamedDriver{"c", c})
	if err != nil {
		t.Fatal(err)
	}
	m.ForgetOutputs()
	if a.forgets != 1 || b.forgets != 1 || c.forgets != 1 {
		t.Fatalf("forgets a %d b %d c %d, want 1 each", a.forgets, b.forgets, c.forgets)
	}
	if err := m.WriteSafe(Values{"A1": 0.0}); err != nil {
		t.Fatal(err)
	}
	if len(a.safe) != 1 || len(a.safe[0]) != 1 || a.safe[0]["A1"] != 0.0 {
		t.Fatalf("a got %v, want A1 only", a.safe)
	}
	if len(b.safe) != 1 || len(b.safe[0]) != 0 {
		t.Fatalf("b got %v, want one empty set", b.safe)
	}
	if got := m.SafeUndelivered(); !slices.Equal(got, []string{"A1"}) {
		t.Fatalf("undelivered %v, want [A1]", got)
	}
	err = m.WriteSafe(Values{"C1": true, "Nope": 1.0})
	if err == nil || !strings.Contains(err.Error(), `driver "c" does not write safe values`) || !strings.Contains(err.Error(), "no driver owns") {
		t.Fatalf("WriteSafe to c and to no one: %v", err)
	}
	if got := m.SafeUndelivered(); !slices.Equal(got, []string{"C1", "Nope"}) {
		t.Fatalf("undelivered %v, want the two that went nowhere", got)
	}
}
