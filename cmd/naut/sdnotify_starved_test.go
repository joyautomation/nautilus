package main

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/leader"
	"github.com/joyautomation/nautilus/runtime"
)

// A task starved between scans (its loop never reaches its next slot; the
// main task scans on) withholds the systemd watchdog heartbeat, as a hung
// scan does, so systemd restarts the controller; released, the heartbeat
// resumes. Wired as naut run wires it: the local gate asks the runtime's
// liveness every tick, and the heartbeat follows its verdicts (#858).
func TestSystemdHeartbeatStopsForAStarvedTask(t *testing.T) {
	if testing.Short() {
		t.Skip("takes a few seconds of real time")
	}
	var starve atomic.Bool
	free := make(chan struct{})
	var once sync.Once
	release := func() { starve.Store(false); once.Do(func() { close(free) }) }
	sleep := func(task string, ctx context.Context, until time.Time) bool {
		if task == "fast" && starve.Load() {
			select {
			case <-free:
			case <-ctx.Done():
				return false
			}
		}
		select {
		case <-time.After(time.Until(until)):
			return ctx.Err() == nil
		case <-ctx.Done():
			return false
		}
	}
	rt, err := runtime.New(runtime.Options{
		Program:   "PROGRAM Main VAR n : DINT; END_VAR n := n + 1; END_PROGRAM",
		Scan:      10 * time.Millisecond,
		Seed:      nio.Values{},
		LoopSleep: sleep,
		Tasks: []runtime.Task{{Name: "fast", Scan: 10 * time.Millisecond,
			Program: "PROGRAM Fast VAR m : DINT; END_VAR m := m + 1; END_PROGRAM"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	sock := newNotifySocket(t)
	t.Setenv("WATCHDOG_USEC", "1000000") // a beat every 500 ms
	t.Setenv("WATCHDOG_PID", "")
	n := &notifier{addr: &net.UnixAddr{Name: sock.path, Net: "unixgram"}}
	watch := &liveWatch{stale: 2 * localGateLease}
	gate := leader.NewLocal("starved", localGateLease)
	gate.SetHealth(watch.wrap(rt.Liveness))

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	t.Cleanup(func() { release(); cancel(); wg.Wait() })
	for _, f := range []func(){
		func() { rt.Run(ctx) },
		func() { gate.Run(ctx) },
		func() { systemdNotify(ctx, n, watch, func() bool { return rt.Stats().Count > 0 }) },
	} {
		wg.Add(1)
		go func() { defer wg.Done(); f() }()
	}

	sock.wait(t, "READY=1", 5*time.Second)
	sock.wait(t, "WATCHDOG=1", 5*time.Second)
	starve.Store(true)
	starved := time.Now()
	quiet := sock.wait(t, "STATUS=scans not live: runtime: scans are not completing: task fast: its next scan is overdue", 8*time.Second)
	t.Logf("heartbeat withheld %v after the task was starved", quiet.Sub(starved).Round(time.Millisecond))
	time.Sleep(1500 * time.Millisecond)
	if c := sock.count("WATCHDOG=1", quiet); c != 0 {
		t.Fatalf("%d heartbeats after the starved task was found", c)
	}

	release()
	back := time.Now()
	for deadline := back.Add(8 * time.Second); sock.count("WATCHDOG=1", back) == 0; time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the heartbeat did not resume once the task ran again")
		}
	}
}
