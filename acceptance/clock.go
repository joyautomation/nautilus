// Package acceptance drives a nautilus resource in virtual time, so tests
// can assert the things a wall clock makes untestable: an on-delay's full
// PT, a loop's settling time, a rate limit, an interlock's debounce.
//
// The runtime's scan dt and its IEC timers both read runtime.Clock. Give a
// resource a VirtualClock and a Scheduler, and a ten-second alarm delay
// elapses in microseconds, exactly, every time — the same tick order on
// every run and on every machine.
//
// The trade this makes is deliberate and worth stating: replaying one
// fixed interleaving is what makes a test reproducible, and it is also why
// these tests can never prove the ABSENCE of a race between tasks. Real
// scheduling stays non-deterministic (runtime.Run gives every task its own
// ticker, serialized only by the scan lock). This harness reproduces one
// interleaving the runtime can produce, not all of them.
package acceptance

import (
	"sync"
	"time"
)

// Epoch is where virtual time starts. Two things make the specific value
// matter more than it looks:
//
//   - It must not be the Unix epoch. The standard timers store "NowMs at
//     the rising edge" in an internal slot and treat ZERO as "idle"
//     (lang/ir/builtins_fb.go). Start virtual time at 0 ms and a TON that
//     starts on the very first scan would re-arm itself forever, so the
//     delay under test could never elapse.
//   - It is fixed, not time.Now(), so NowMs is identical on every run.
//     Logic that formats or compares a timestamp stays reproducible.
var Epoch = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

// VirtualClock is a runtime.Clock that only moves when the harness moves
// it. Safe for concurrent use, because the runtime reads it from whichever
// goroutine is scanning.
type VirtualClock struct {
	mu  sync.RWMutex
	now time.Time
}

// NewClock returns a clock stopped at Epoch.
func NewClock() *VirtualClock { return &VirtualClock{now: Epoch} }

// Now implements runtime.Clock.
func (c *VirtualClock) Now() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.now
}

// Set moves virtual time to an absolute instant. The Scheduler uses it to
// land exactly on each tick, which is what makes dt exactly the configured
// period instead of merely close to it.
func (c *VirtualClock) Set(t time.Time) {
	c.mu.Lock()
	c.now = t
	c.mu.Unlock()
}

// Advance moves virtual time forward by d without running any scans. Use
// Scheduler.Advance to move time AND scan the resource; this is the lower
// level, for tests that drive scans by hand.
func (c *VirtualClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// Elapsed reports virtual time since Epoch — the number that means
// something in a failure message.
func (c *VirtualClock) Elapsed() time.Duration { return c.Now().Sub(Epoch) }
