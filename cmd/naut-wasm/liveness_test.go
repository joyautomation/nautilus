//go:build js && wasm

package main

import (
	"context"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/runtime"
)

// In a browser tab a loop that misses its slots is a throttled background
// tab, not a starved task: Liveness does not flag it (#858), or the
// playground's gate would flap and log errors whenever the tab is hidden.
func TestPlaygroundLivenessIgnoresMissedSlots(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	starved := make(chan struct{}, 1)
	rt, err := runtime.New(runtime.Options{
		Program: "PROGRAM Main VAR n : DINT; END_VAR n := n + 1; END_PROGRAM",
		Scan:    10 * time.Millisecond,
		LoopSleep: func(task string, ctx context.Context, until time.Time) bool {
			select { // every slot missed, as in a throttled tab
			case starved <- struct{}{}:
			default:
			}
			select {
			case <-block:
				return false
			case <-ctx.Done():
				return false
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go rt.Run(ctx)
	<-starved
	t0 := time.Now()
	rt.Liveness(t0, 2*time.Second) // first look
	if err := rt.Liveness(t0.Add(time.Hour), 2*time.Second); err != nil {
		t.Fatalf("a loop missing its slots in a browser tab reads as %v", err)
	}
}
