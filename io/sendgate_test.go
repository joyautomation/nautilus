package io

import (
	"testing"
	"time"
)

// sendGated is a gated child that also checks a SendGate before each send
// (design §3.4).
type sendGated struct {
	gated
	send *SendGate
}

func (g sendGated) SetSendGate(s SendGate) { *g.send = s }

// A pair's send gate reaches every child that takes one, and a child
// brought in by Replace; the child Replace takes out gets a closed one as
// well as a closed write gate, since a send-gated driver reads its send
// gate, not the plain one.
func TestMultiSendGateReachesChildrenAndReplaceClosesIt(t *testing.T) {
	var gA, gA2 func() bool
	var sA, sA2 SendGate
	var sent []string
	a := sendGated{gated{named: named{Memory: NewMemory(), outs: []string{"A_Out"}}, gate: &gA, sent: &sent}, &sA}
	var gP func() bool
	plain := gated{named: named{Memory: NewMemory(), outs: []string{"P_Out"}}, gate: &gP, sent: &sent}
	m, err := NewMulti(NamedDriver{"a", a}, NamedDriver{"p", plain})
	if err != nil {
		t.Fatal(err)
	}
	m.SetWriteGate(func() bool { return true })
	m.SetSendGate(func() (bool, time.Duration) { return true, time.Second })
	if sA == nil {
		t.Fatal("the send-gated child has no send gate")
	}
	if gP == nil {
		t.Fatal("the plain child lost its write gate")
	}
	a2 := sendGated{gated{named: named{Memory: NewMemory(), outs: []string{"A_Out"}}, gate: &gA2, sent: &sent}, &sA2}
	if _, err := m.Replace("a", a2); err != nil {
		t.Fatal(err)
	}
	if sA2 == nil {
		t.Fatal("the replacement has no send gate")
	}
	if ok, _ := sA(); ok {
		t.Fatal("the replaced child's send gate is still open: it would keep sending")
	}
}
