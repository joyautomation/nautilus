package runtime_test

import (
	"sync/atomic"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/runtime"
)

// readyDriver is a scriptDriver that says whether its inputs are live
// (io.InputsReadier), counting the times it is asked.
type readyDriver struct {
	scriptDriver
	ready atomic.Bool
	asked atomic.Int64
}

func (d *readyDriver) InputsReady() bool { d.asked.Add(1); return d.ready.Load() }

func newReadyDoubler(t *testing.T, drv nio.Driver, wait time.Duration) *runtime.Runtime {
	t.Helper()
	rt, err := runtime.New(runtime.Options{
		Program:            doublerProg,
		Driver:             drv,
		Inputs:             []string{"In"},
		Outputs:            []string{"Out"},
		Seed:               nio.Values{"Out": 0.0},
		Coordinator:        &leaderFlag{lead: true},
		TakeoverInputsWait: wait,
	})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return rt
}

// #626: a takeover waits for a driver that says its inputs are not live:
// no read, no logic, no output while it waits, and the first scan once
// they are live runs the takeover on them.
func TestTakeoverWaitsForLiveInputs(t *testing.T) {
	drv := &readyDriver{scriptDriver: scriptDriver{in: nio.Values{"In": 3.0}}}
	rt := newReadyDoubler(t, drv, time.Hour)
	for range 3 {
		rt.Scan()
	}
	if drv.asked.Load() < 3 || drv.reads != 0 || drv.writeCount() != 0 {
		t.Fatalf("waiting: asked %d, reads %d, writes %d; want asked every scan, nothing read or written",
			drv.asked.Load(), drv.reads, drv.writeCount())
	}
	drv.ready.Store(true)
	rt.Scan()
	if drv.reads != 1 || drv.writeCount() != 1 || drv.lastOut["Out"] != 6.0 {
		t.Fatalf("live: reads %d, writes %d, Out %v; want 1, 1, 6", drv.reads, drv.writeCount(), drv.lastOut["Out"])
	}
	asked := drv.asked.Load()
	rt.Scan()
	if drv.asked.Load() != asked {
		t.Error("asked again after the takeover: only a pending takeover asks")
	}
}

// The wait is bounded: past TakeoverInputsWait the takeover runs on what
// the driver holds.
func TestTakeoverWaitIsBounded(t *testing.T) {
	drv := &readyDriver{scriptDriver: scriptDriver{in: nio.Values{"In": 1.0}}}
	rt := newReadyDoubler(t, drv, 20*time.Millisecond)
	rt.Scan()
	if drv.writeCount() != 0 {
		t.Fatal("wrote before the wait began")
	}
	time.Sleep(30 * time.Millisecond)
	rt.Scan()
	if drv.writeCount() != 1 || drv.lastOut["Out"] != 2.0 {
		t.Fatalf("past the wait: writes %d, Out %v; want 1, 2", drv.writeCount(), drv.lastOut["Out"])
	}
}

// Without a coordinator there is no takeover to hold: a driver that is not
// ready yet is scanned as before.
func TestNoCoordinatorDoesNotWaitForInputs(t *testing.T) {
	drv := &readyDriver{scriptDriver: scriptDriver{in: nio.Values{"In": 1.0}}}
	rt, err := runtime.New(runtime.Options{
		Program: doublerProg, Driver: drv, Inputs: []string{"In"}, Outputs: []string{"Out"},
		Seed: nio.Values{"Out": 0.0},
	})
	if err != nil {
		t.Fatal(err)
	}
	rt.Scan()
	if drv.writeCount() != 1 || drv.asked.Load() != 0 {
		t.Fatalf("writes %d, asked %d; want 1, 0", drv.writeCount(), drv.asked.Load())
	}
}
