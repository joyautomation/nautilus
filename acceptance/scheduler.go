package acceptance

import (
	"fmt"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/runtime"
)

// Scheduler replays a resource's tasks in virtual time: at each step it
// finds the next instant any task comes due, lands the clock exactly on
// it, and scans every task due at that instant — main first, then
// declaration order. Landing exactly on the tick is what makes the
// measured dt exactly the configured period, which is what makes a PI
// loop's trajectory identical on every run.
//
// It deliberately does not use runtime.Run: that gives every task its own
// ticker and lets the OS decide the interleaving. See the package doc for
// what that trades away.
type Scheduler struct {
	rt    *runtime.Runtime
	clk   *VirtualClock
	tasks []*schedTask

	// OnTick, when set, runs after every batch of due scans — the seam an
	// invariant (`always:`) is checked on, and where a failure trace
	// samples the tags it will need to explain itself.
	OnTick func()
}

type schedTask struct {
	name      string
	interval  time.Duration
	next      time.Duration // virtual offset from Epoch when it next comes due
	scans     int
	suspended bool
	main      bool
}

// NewRuntime builds a runtime wired to a fresh virtual clock, plus the
// scheduler that drives it — the one-call form, since forgetting to set
// Options.Clock would silently leave the resource on the wall clock and
// every timing assertion would quietly become untestable again.
func NewRuntime(o runtime.Options) (*runtime.Runtime, *Scheduler, error) {
	clk := NewClock()
	o.Clock = clk
	rt, err := runtime.New(o)
	if err != nil {
		return nil, nil, err
	}
	return rt, NewScheduler(rt, clk), nil
}

// NewScheduler drives an existing runtime that was built with clk as its
// Options.Clock. Prefer NewRuntime unless you need the runtime first.
func NewScheduler(rt *runtime.Runtime, clk *VirtualClock) *Scheduler {
	s := &Scheduler{rt: rt, clk: clk}
	for _, ts := range rt.Schedule() {
		s.tasks = append(s.tasks, &schedTask{
			name:     ts.Name,
			interval: ts.Interval,
			// First due one interval in, the way runtime.Run's tickers
			// fire: at virtual t=0 the resource has not scanned, so a
			// test can assert on the seeded state before anything runs.
			next: ts.Interval,
			main: ts.Name == runtime.MainTaskName,
		})
	}
	return s
}

// Elapsed is virtual time since the resource started.
func (s *Scheduler) Elapsed() time.Duration { return s.clk.Elapsed() }

// ScanCount reports how many scans a task has run ("main" for the main
// task) — the "106 scans" in a result line.
func (s *Scheduler) ScanCount(name string) int {
	if t := s.task(name); t != nil {
		return t.scans
	}
	return 0
}

// Suspend stops the named tasks from scanning. Freezing the task that
// simulates the plant is what turns a closed-loop project into an
// open-loop test: drive the process value directly and nothing fights you
// for it. A suspended task keeps its phase, so Resume does not replay the
// ticks it slept through.
func (s *Scheduler) Suspend(names ...string) error { return s.setSuspended(true, names) }

// Resume undoes Suspend, re-phasing the task to the next tick at or after
// now so virtual time never runs backwards.
func (s *Scheduler) Resume(names ...string) error { return s.setSuspended(false, names) }

func (s *Scheduler) setSuspended(v bool, names []string) error {
	for _, n := range names {
		t := s.task(n)
		if t == nil {
			return fmt.Errorf("acceptance: no task %q in this resource (have %s)", n, s.names())
		}
		if !v && t.suspended {
			t.rephase(s.Elapsed())
		}
		t.suspended = v
	}
	return nil
}

// Advance runs every scan due at or before now+d, then lands virtual time
// exactly on now+d. A partial tick does not execute, so `advance 9.5s` is
// exact rather than rounded, and the elapsed time a timer sees is exactly
// the number the test asked for.
func (s *Scheduler) Advance(d time.Duration) {
	if d <= 0 {
		return
	}
	target := s.Elapsed() + d
	for {
		next, ok := s.nextDue()
		if !ok || next > target {
			break
		}
		s.runDue(next)
	}
	s.clk.Set(Epoch.Add(target))
}

// Scans runs until the main task has completed n more scans, interleaving
// the other tasks at their own rates. This is the honest unit for
// combinational logic — a latch, a comparison, a clamp — where "one scan"
// is the whole question and a duration would only obscure it.
func (s *Scheduler) Scans(n int) error {
	main := s.task(runtime.MainTaskName)
	if main.suspended {
		return fmt.Errorf("acceptance: cannot count main-task scans while main is suspended")
	}
	target := main.scans + n
	for main.scans < target {
		next, ok := s.nextDue()
		if !ok {
			return fmt.Errorf("acceptance: no task can run (every task suspended)")
		}
		s.runDue(next)
	}
	return nil
}

// AdvanceUntil runs up to limit, stopping as soon as pred has held
// continuously for hold. It reports whether that happened and the instant
// it first became true.
//
// This is the assertion shape a settling test actually needs: a loop that
// merely passes through its target on the way to an overshoot has not
// settled, and a bare "after 45 s" assertion cannot tell the difference.
// hold=0 means "the first tick it holds".
func (s *Scheduler) AdvanceUntil(limit, hold time.Duration, pred func() bool) (bool, time.Duration) {
	deadline := s.Elapsed() + limit
	var since time.Duration
	holding := pred()
	if holding {
		since = s.Elapsed()
		if hold == 0 {
			return true, since
		}
	}
	for {
		if holding && s.Elapsed()-since >= hold {
			return true, since
		}
		next, ok := s.nextDue()
		if !ok || next > deadline {
			break
		}
		s.runDue(next)
		if pred() {
			if !holding {
				holding, since = true, s.Elapsed()
			}
		} else {
			holding = false
		}
	}
	s.clk.Set(Epoch.Add(deadline))
	if holding && s.Elapsed()-since >= hold {
		return true, since
	}
	return false, 0
}

// LogicErrors reports the resource's total program errors and the most
// recent message, across main and every task. Any scan that faults fails
// the test that ran it — you never have to ask for that assertion.
func (s *Scheduler) LogicErrors() (uint64, string) {
	st := s.rt.Stats()
	total, last := st.LogicErrors, ""
	for _, t := range st.Tasks {
		total += t.LogicErrors
		if t.LastError != "" {
			last = t.LastError
		}
	}
	return total, last
}

// nextDue is the earliest instant any runnable task comes due.
func (s *Scheduler) nextDue() (time.Duration, bool) {
	var min time.Duration
	found := false
	for _, t := range s.tasks {
		if t.suspended {
			continue
		}
		if !found || t.next < min {
			min, found = t.next, true
		}
	}
	return min, found
}

// runDue lands the clock on at and scans every task due there, in the
// order Schedule reports them: main first, then declaration order. Ties
// are the common case, not the edge case — a 100 ms control task and a
// 100 ms sim task coincide on every single tick.
func (s *Scheduler) runDue(at time.Duration) {
	s.clk.Set(Epoch.Add(at))
	for _, t := range s.tasks {
		if t.suspended || t.next != at {
			continue
		}
		if t.main {
			s.rt.Scan()
		} else {
			_ = s.rt.ScanTask(t.name)
		}
		t.scans++
		t.next = at + t.interval
	}
	if s.OnTick != nil {
		s.OnTick()
	}
}

func (s *Scheduler) task(name string) *schedTask {
	for _, t := range s.tasks {
		if t.name == name {
			return t
		}
	}
	return nil
}

func (s *Scheduler) names() string {
	out := make([]string, len(s.tasks))
	for i, t := range s.tasks {
		out[i] = t.name
	}
	return strings.Join(out, ", ")
}

// rephase skips the ticks a suspended task slept through, keeping its
// original phase relative to Epoch.
func (t *schedTask) rephase(now time.Duration) {
	if t.next >= now || t.interval <= 0 {
		return
	}
	missed := (now-t.next)/t.interval + 1
	t.next += missed * t.interval
}
