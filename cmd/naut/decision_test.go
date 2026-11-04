package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

type fakeDecider chan struct{}

func (f fakeDecider) Decided() <-chan struct{} { return f }

// awaitDecision waits for the elector's first decision, no longer than its
// timeout (an API server that never answers starts the drivers anyway, as
// a standby's), and no longer than the run's context: a stop during
// startup ends the wait at once.
func TestAwaitDecisionWaitsForTheFirstDecision(t *testing.T) {
	ctx := context.Background()
	d := make(fakeDecider)
	go func() {
		time.Sleep(50 * time.Millisecond)
		close(d)
	}()
	start := time.Now()
	if !awaitDecision(ctx, d, 5*time.Second) {
		t.Fatal("the decision came in time, yet awaitDecision timed out")
	}
	if took := time.Since(start); took < 50*time.Millisecond {
		t.Fatalf("awaitDecision returned after %v, before the decision", took)
	}

	start = time.Now()
	if awaitDecision(ctx, make(fakeDecider), 30*time.Millisecond) {
		t.Fatal("no decision, yet awaitDecision reported one")
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("awaitDecision waited %v past its 30ms timeout", took)
	}
}

func TestAwaitDecisionEndsOnAStop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel() // SIGTERM during startup
	}()
	start := time.Now()
	if awaitDecision(ctx, make(fakeDecider), 5*time.Second) {
		t.Fatal("no decision, yet awaitDecision reported one")
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("a stop during startup waited %v for the 5 s timeout", took)
	}
}

// startDriver records when Start ran relative to the decision.
type startDriver struct {
	dec           fakeDecider
	started       atomic.Bool
	beforeDecided atomic.Bool
	stopped       atomic.Bool
}

func (s *startDriver) Start(context.Context) {
	select {
	case <-s.dec:
	default:
		s.beforeDecided.Store(true)
	}
	s.started.Store(true)
}
func (s *startDriver) Stop() { s.stopped.Store(true) }

// runProject starts the drivers only after the elector's first decision
// (#606: a replica that leads from its first scan keeps its drivers'
// start-up baseline). Without redundancy they start at once, and a stop
// during the wait starts nothing.
func TestDriversStartAfterTheFirstDecision(t *testing.T) {
	t.Run("waits for the decision", func(t *testing.T) {
		drv := &startDriver{dec: make(fakeDecider)}
		go func() {
			time.Sleep(50 * time.Millisecond)
			close(drv.dec)
		}()
		stop := startDriverWhenDecided(context.Background(), drv.dec, 5*time.Second, drv)
		if !drv.started.Load() || drv.beforeDecided.Load() {
			t.Fatalf("started = %v, before the decision = %v: the driver must start after it", drv.started.Load(), drv.beforeDecided.Load())
		}
		if stop == nil {
			t.Fatal("a driver with Stop must be handed back for teardown")
		}
		stop()
		if !drv.stopped.Load() {
			t.Fatal("the teardown did not stop the driver")
		}
	})
	t.Run("no redundancy", func(t *testing.T) {
		drv := &startDriver{dec: make(fakeDecider)}
		startDriverWhenDecided(context.Background(), nil, 5*time.Second, drv)
		if !drv.started.Load() {
			t.Fatal("without an elector the driver must start at once")
		}
	})
	t.Run("stopped during the wait", func(t *testing.T) {
		drv := &startDriver{dec: make(fakeDecider)}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if stop := startDriverWhenDecided(ctx, drv.dec, 5*time.Second, drv); stop != nil || drv.started.Load() {
			t.Fatal("a stop during startup started the driver")
		}
	})
}
