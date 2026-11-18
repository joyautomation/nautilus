// Package driverconformance runs one suite of contracts against every field
// driver in the tree, each through its public fake, so a new driver (or a
// contract added later) cannot quietly skip one. It holds no non-test code:
// like internal/contextmap, it is a check, not a library.
//
// The contracts are the ones io/io.go and io/multi.go describe and that
// every driver is expected to keep:
//
//	C1 an input never read reports NotConnected
//	C2 after a good read the input is delivered and Good
//	C3 a read that fails on a live link reports Bad
//	C4 a lost link turns a delivered input Stale
//	C5 while the write gate is closed nothing reaches the device; after it
//	   reopens the driver asks for every output (io.OutputsRequester) and
//	   the next push arrives
//	C6 Stop returns within a bound while the device hangs
//
// A driver that does not keep a contract is listed with the issue that
// tracks the gap (expect). Its row still runs: it must keep failing, and
// once it passes the suite fails until the mark is removed, so a mark
// cannot go stale. A contract the driver's fake cannot exercise is listed
// in unsupported with the reason, and skipped.
//
// Survey: #861.
package driverconformance

import (
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
)

// rig is one running driver against its fake, behind a link the suite
// controls. Every field is required unless it says otherwise.
type rig struct {
	drv nio.Driver
	// in is an input tag the fake serves; out an output tag it accepts.
	in, out string
	// outVals are two different values for out, in the form the runtime
	// hands WriteOutputs.
	outVals [2]any
	// field reads out back from the fake, in the same form as outVals.
	field func() any
	// polls counts the driver's completed reads (a Health counter), so a
	// "nothing happened" check waits for real polls instead of a sleep.
	polls func() uint64
	// failRead makes the next reads of in fail on a live link (an
	// exception, a bad status). nil when the fake cannot (see unsupported).
	failRead func()
	link     *link
}

// subject is one driver in the suite.
type subject struct {
	name string
	// start starts the driver against a fresh fake. The link starts up
	// unless cut is true; gate, when non-nil, is installed before Start.
	start func(t tb, cut bool, gate func() bool) *rig
	// expect maps a contract to the issue tracking the driver's gap.
	expect map[string]string
	// unsupported maps a contract to why the fake cannot exercise it.
	unsupported map[string]string
}

var subjects []subject

func register(s subject) { subjects = append(subjects, s) }

type contract struct {
	id, what string
	run      func(t tb, s subject)
}

var contracts = []contract{
	{"C1", "an input never read reports NotConnected", c1},
	{"C2", "a read input is delivered and Good", c2},
	{"C3", "a read that fails on a live link reports Bad", c3},
	{"C4", "a lost link turns a delivered input Stale", c4},
	{"C5", "a closed write gate sends nothing; a reopened one asks for every output", c5},
	{"C6", "Stop returns within a bound while the device hangs", c6},
}

func TestDriverContracts(t *testing.T) {
	if len(subjects) == 0 {
		t.Fatal("no drivers registered")
	}
	for _, s := range subjects {
		t.Run(s.name, func(t *testing.T) {
			for k := range s.expect {
				if !knownContract(k) {
					t.Errorf("expect names unknown contract %q", k)
				}
			}
			for _, c := range contracts {
				t.Run(c.id, func(t *testing.T) {
					if why, ok := s.unsupported[c.id]; ok {
						t.Skipf("%s: not exercisable through the fake: %s", c.what, why)
					}
					failed, msgs := record(t, func(r tb) { c.run(r, s) })
					issue, expected := s.expect[c.id]
					switch {
					case expected && failed:
						t.Logf("expected failure (%s): %s: %s", issue, c.what, strings.Join(msgs, "; "))
					case expected:
						t.Errorf("%s now passes: remove its expected-failure mark (%s)", c.what, issue)
					case failed:
						t.Errorf("%s:\n%s", c.what, strings.Join(msgs, "\n"))
					}
				})
			}
		})
	}
}

func knownContract(id string) bool {
	for _, c := range contracts {
		if c.id == id {
			return true
		}
	}
	return false
}

// tb is testing.TB. A contract runs against a recorder, so an expected
// failure can be observed without failing the test; fakes' own helpers
// (which take a testing.TB) report through it too.
type tb = testing.TB

// recorder is a testing.TB whose failures are recorded, not reported.
// Logs, cleanups, temp dirs and the rest go to the real test.
type recorder struct {
	*testing.T
	mu     sync.Mutex
	failed bool
	msgs   []string
}

func (r *recorder) fail(msg string) {
	r.mu.Lock()
	r.failed = true
	if msg != "" {
		r.msgs = append(r.msgs, msg)
	}
	r.mu.Unlock()
}

func (r *recorder) Errorf(format string, args ...any) { r.fail(fmt.Sprintf(format, args...)) }
func (r *recorder) Error(args ...any)                 { r.fail(fmt.Sprint(args...)) }
func (r *recorder) Fail()                             { r.fail("") }
func (r *recorder) FailNow()                          { r.fail(""); runtime.Goexit() }
func (r *recorder) Fatalf(format string, args ...any) { r.Errorf(format, args...); runtime.Goexit() }
func (r *recorder) Fatal(args ...any)                 { r.Error(args...); runtime.Goexit() }
func (r *recorder) Failed() bool                      { r.mu.Lock(); defer r.mu.Unlock(); return r.failed }

// record runs f against a recorder on its own goroutine (Fatalf ends it
// with Goexit) and reports whether it failed and why. Cleanups registered
// through the recorder run with t's.
func record(t *testing.T, f func(tb)) (bool, []string) {
	r := &recorder{T: t}
	done := make(chan struct{})
	go func() {
		defer close(done)
		f(r)
	}()
	<-done
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.failed, r.msgs
}

// wait polls cond until it holds or d passes. It is the suite's only way
// to wait for something to happen.
func wait(t tb, d time.Duration, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(d); ; time.Sleep(2 * time.Millisecond) {
		if cond() {
			return
		}
		if time.Now().After(end) {
			t.Fatalf("after %s: %s", d, what)
		}
	}
}

// settle waits for n more completed polls: long enough for anything a
// driver was going to send to have gone out.
func settle(t tb, r *rig, n uint64) {
	t.Helper()
	p := r.polls()
	wait(t, 5*time.Second, fmt.Sprintf("%d more polls", n), func() bool { return r.polls() >= p+n })
}

func quality(r *rig, name string) nio.Quality {
	q, ok := r.drv.(nio.QualityReporter)
	if !ok {
		return nio.Good
	}
	if v, ok := q.Quality()[name]; ok {
		return v
	}
	return nio.Good
}

func delivered(r *rig) bool {
	v, err := r.drv.ReadInputs()
	if err != nil {
		return false
	}
	_, ok := v[r.in]
	return ok
}

// up starts s with its link up and waits for the first delivery.
func up(t tb, s subject, gate func() bool) *rig {
	t.Helper()
	r := s.start(t, false, gate)
	wait(t, 5*time.Second, r.in+" delivered", func() bool { return delivered(r) && quality(r, r.in) == nio.Good })
	return r
}

func c1(t tb, s subject) {
	r := s.start(t, true, nil)
	if q := quality(r, r.in); q != nio.NotConnected {
		t.Fatalf("%s before any read: quality %s, want NotConnected", r.in, q)
	}
	// Still NotConnected once the driver has tried and failed.
	wait(t, 2*time.Second, "a refused dial", func() bool { return r.link.refused() > 0 })
	if q := quality(r, r.in); q != nio.NotConnected {
		t.Fatalf("%s after a refused dial: quality %s, want NotConnected", r.in, q)
	}
}

func c2(t tb, s subject) {
	up(t, s, nil)
}

func c3(t tb, s subject) {
	r := up(t, s, nil)
	if r.failRead == nil {
		t.Fatalf("the rig has no failRead: list C3 in unsupported with the reason")
	}
	r.failRead()
	wait(t, 5*time.Second, r.in+" Bad after a failed read on a live link", func() bool { return quality(r, r.in) == nio.Bad })
}

func c4(t tb, s subject) {
	r := up(t, s, nil)
	r.link.cut()
	wait(t, 5*time.Second, r.in+" Stale after the link was cut", func() bool { return quality(r, r.in) == nio.Stale })
}

func c5(t tb, s subject) {
	var mu sync.Mutex
	open := true
	gate := func() bool { mu.Lock(); defer mu.Unlock(); return open }
	setGate := func(v bool) { mu.Lock(); open = v; mu.Unlock() }

	r := up(t, s, gate)
	if _, ok := r.drv.(interface{ SetWriteGate(func() bool) }); !ok {
		t.Fatalf("the driver takes no write gate")
	}
	push := func(v any) {
		t.Helper()
		if err := r.drv.WriteOutputs(nio.Values{r.out: v}); err != nil {
			t.Fatalf("WriteOutputs(%v): %v", v, err)
		}
	}
	a, b := r.outVals[0], r.outVals[1]
	// The first push is what the runtime's first scan hands over; a driver
	// may take it as a baseline and send nothing. A change is a command.
	push(a)
	push(b)
	wait(t, 5*time.Second, fmt.Sprintf("%s = %v at the device with the gate open", r.out, b), func() bool { return r.field() == b })

	setGate(false)
	settle(t, r, 2) // the driver has looked at the closed gate
	push(a)
	settle(t, r, 3)
	if got := r.field(); got != b {
		t.Fatalf("%s reached the device as %v while the write gate was closed", r.out, got)
	}

	setGate(true)
	req, ok := r.drv.(nio.OutputsRequester)
	if !ok {
		t.Fatalf("the gate dropped a command and the driver cannot ask for it again (no io.OutputsRequester)")
	}
	wait(t, 5*time.Second, "OutputsWanted after the gate reopened", req.OutputsWanted)
	push(a)
	wait(t, 5*time.Second, fmt.Sprintf("%s = %v at the device after the gate reopened", r.out, a), func() bool { return r.field() == a })
}

// stopBound is how long Stop may take with the device hung. Every driver's
// per-request timeout in the suite's rigs is well under it.
const stopBound = 5 * time.Second

func c6(t tb, s subject) {
	r := up(t, s, nil)
	stopper, ok := r.drv.(interface{ Stop() })
	if !ok {
		t.Fatalf("the driver has no Stop")
	}
	r.link.freeze()
	done := make(chan struct{})
	go func() { stopper.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(stopBound):
		t.Fatalf("Stop still running after %s with the device hung", stopBound)
	}
}
