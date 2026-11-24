package host

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/internal/fence"
	"github.com/joyautomation/nautilus/sparkplug"
)

// countingPub notes, at each publish, how many sends a planned
// switchover's drain would wait for; with late set, it says a message has
// left only when the test lets it (a paho publish that timed out).
type countingPub struct {
	mu     sync.Mutex
	during []int
	late   chan struct{}
}

func (p *countingPub) publish(string, byte, bool, []byte) error {
	p.mu.Lock()
	p.during = append(p.during, fence.InFlight())
	p.mu.Unlock()
	return nil
}

func (p *countingPub) publishEnded(topic string, qos byte, retain bool, payload []byte, ended func()) error {
	err := p.publish(topic, qos, retain, payload)
	if p.late == nil {
		ended()
		return err
	}
	go func() { <-p.late; ended() }()
	return err
}

// #710 d1b: an NCMD is counted for a planned switchover's drain from
// BEFORE the host reads its gate, so a command that read the gate open
// just before it closed holds the drain until it has left. The gate here
// closes as it is read open, and the read stalls: counted after the read
// (host_enter_after_gate), the drain returns while the command is still
// to leave.
func TestHostCommandReadingTheGateOpenHoldsTheDrain(t *testing.T) {
	d := &Driver{cfg: Config{Primary: true}}
	pub := &countingPub{}
	d.pub = pub
	var shut atomic.Bool
	reading, release := make(chan struct{}), make(chan struct{})
	d.SetWriteGate(func() bool {
		if shut.Swap(true) {
			return false
		}
		close(reading)
		<-release
		return true
	})
	published := make(chan error, 1)
	go func() {
		published <- d.publishCommand("spBv1.0/G/NCMD/E", []sparkplug.Metric{dbl("Speed", 1)})
	}()
	<-reading
	drained := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		drained <- fence.Drain(ctx, 0)
	}()
	select {
	case err := <-drained:
		t.Fatalf("the drain returned (%v) while a command that read the gate open had not left", err)
	case <-time.After(150 * time.Millisecond):
	}
	close(release)
	if err := <-published; err != nil {
		t.Fatal(err)
	}
	if err := <-drained; err != nil {
		t.Fatal(err)
	}
	drainSettles(t, "after the command left")
}

// drainSettles waits for nothing to be counted in flight: exact counts
// would catch a send of a driver an earlier test left running.
func drainSettles(t *testing.T, what string) {
	t.Helper()
	for end := time.Now().Add(2 * time.Second); fence.InFlight() != 0; time.Sleep(time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("%s: %d still counted in flight; a drain would never end", what, fence.InFlight())
		}
	}
}

// A command whose publish returned before the message left (paho's wait
// timed out: it may still go out from paho's queue) stays counted until it
// has left, and a closed gate's refusal leaves nothing counted.
func TestHostCommandIsCountedUntilItHasLeft(t *testing.T) {
	d := &Driver{cfg: Config{Primary: true}}
	pub := &countingPub{late: make(chan struct{})}
	d.pub = pub
	var open atomic.Bool
	open.Store(true)
	d.SetWriteGate(open.Load)
	_ = d.publishCommand("spBv1.0/G/NCMD/E", []sparkplug.Metric{dbl("Speed", 1)})
	if pub.during[0] < 1 {
		t.Fatal("the publish ran with nothing in flight")
	}
	if n := fence.InFlight(); n < 1 {
		t.Fatal("nothing in flight after a publish that has not left; want it still counted")
	}
	close(pub.late)
	drainSettles(t, "after the message left")
	open.Store(false)
	if err := d.publishCommand("spBv1.0/G/NCMD/E", []sparkplug.Metric{dbl("Speed", 2)}); err == nil {
		t.Fatal("a closed gate: the command was not refused")
	}
	drainSettles(t, "after a closed gate's refusal")
}
