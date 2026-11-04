package main

import (
	"testing"
	"time"
)

type fakeDecider chan struct{}

func (f fakeDecider) Decided() <-chan struct{} { return f }

// runProject starts the drivers only after the elector's first decision
// (#606: a replica that leads from its first scan keeps its drivers'
// start-up baseline). awaitDecision waits for it, and no longer than its
// timeout: an API server that never answers starts the drivers anyway, as
// a standby's.
func TestAwaitDecisionWaitsForTheFirstDecision(t *testing.T) {
	d := make(fakeDecider)
	go func() {
		time.Sleep(50 * time.Millisecond)
		close(d)
	}()
	start := time.Now()
	if !awaitDecision(d, 5*time.Second) {
		t.Fatal("the decision came in time, yet awaitDecision timed out")
	}
	if took := time.Since(start); took < 50*time.Millisecond {
		t.Fatalf("awaitDecision returned after %v, before the decision", took)
	}

	start = time.Now()
	if awaitDecision(make(fakeDecider), 30*time.Millisecond) {
		t.Fatal("no decision, yet awaitDecision reported one")
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("awaitDecision waited %v past its 30ms timeout", took)
	}
}
