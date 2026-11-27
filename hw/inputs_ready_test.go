package hw

import (
	"context"
	"errors"
	"testing"
	"time"
)

// InputsReady (io.InputsReadier, #786): false until every enabled source's
// first poll round is over (answered or failed); sticky.

func waitInputsReady(t *testing.T, b *Base, what string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if b.InputsReady() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("InputsReady never became true: %s", what)
}

func TestBaseInputsReadyAfterTheFirstRoundThenSticky(t *testing.T) {
	w := newFakeWire()
	w.set("SW1", DefaultClass, Result{Updates: up("SW1_Port01", true, true, 5)})
	cfg := switchConfig(w)
	release := make(chan struct{})
	cfg.Poll = func(ctx context.Context, source, class string) (Result, error) {
		if class == "slow" { // the second class holds the first round open
			select {
			case <-release:
			case <-ctx.Done():
				return Result{}, ctx.Err()
			}
		}
		return w.poll(ctx, source, class)
	}
	b, err := NewBase(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if b.InputsReady() {
		t.Fatal("ready before Start")
	}
	w.watch(b)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b.Start(ctx)
	defer b.Stop()
	w.waitPoll(t, "SW1/default")
	if b.InputsReady() {
		t.Fatal("ready with one of the source's two classes not yet polled")
	}
	close(release)
	waitInputsReady(t, b, "after both classes polled")
	// Now the device dies: three failed polls take the source to error.
	w.setErr("SW1", DefaultClass, errors.New("refused"))
	w.setErr("SW1", "slow", errors.New("refused"))
	deadline := time.Now().Add(3 * time.Second)
	for b.Health().Sources[0].State != "error" && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if b.Health().Sources[0].State != "error" {
		t.Fatal("the source never went down")
	}
	if !b.InputsReady() {
		t.Fatal("InputsReady went false after the source failed")
	}
}

func TestBaseInputsReadyAfterAFailedFirstRound(t *testing.T) {
	w := newFakeWire()
	w.setErr("SW1", DefaultClass, errors.New("refused"))
	w.setErr("SW1", "slow", errors.New("refused"))
	b, err := NewBase(switchConfig(w))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b.Start(ctx)
	defer b.Stop()
	waitInputsReady(t, b, "after the first round failed")
}

func TestBaseInputsReadyWaitsForEverySource(t *testing.T) {
	w := newFakeWire()
	cfg := switchConfig(w)
	cfg.Sources = append(cfg.Sources, SourceConfig{ID: "SW2", Addr: "sw2:161", Interval: 20 * time.Millisecond, RetryMin: 10 * time.Millisecond, RetryMax: 20 * time.Millisecond})
	cfg.Tags = append(cfg.Tags, portDecl("SW2_Port01", "SW2"))
	release := make(chan struct{})
	cfg.Poll = func(ctx context.Context, source, class string) (Result, error) {
		if source == "SW2" {
			select {
			case <-release:
			case <-ctx.Done():
				return Result{}, ctx.Err()
			}
		}
		return w.poll(ctx, source, class)
	}
	b, err := NewBase(cfg)
	if err != nil {
		t.Fatal(err)
	}
	w.watch(b)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b.Start(ctx)
	defer b.Stop()
	w.waitPoll(t, "SW1/default")
	time.Sleep(30 * time.Millisecond)
	if b.InputsReady() {
		t.Fatal("ready while SW2 has not had its first round")
	}
	close(release)
	waitInputsReady(t, b, "after SW2's first round")
}
