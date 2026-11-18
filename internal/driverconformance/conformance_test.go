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
//	C5 the plain write gate naut run installs in every project
//	   (io.SetPlainGate): while it is closed nothing reaches the device;
//	   after it reopens the driver asks for every output
//	   (io.OutputsRequester) and the next push arrives
//	C6 Stop returns within a bound while a request hangs at the device
//	C7 a link that comes back brings the input back to Good
//	C8 the leadership gate a redundant pair installs (SetWriteGate): the
//	   same as C5, for a driver that reads any gate as "one replica of a
//	   redundant set"
//
// C5 and C8 hold for either of two designs, which each subject declares
// (gate): drop the commands pushed while the gate is closed and ask for
// every output when it reopens (dropAndAsk), or hold the latest one and
// send it on reopen (holdAndDeliver), never a superseded one.
//
// A driver that does not keep a contract is listed in expect with the
// issue that tracks the gap. Its row still runs: it must keep failing, and
// once it passes the suite fails until the mark is removed, so a mark
// cannot go stale. A contract the driver's fake cannot exercise is listed
// in unsupported with the reason, and skipped. A contract that cannot
// apply to the driver at all (it has no outputs; it has no device once
// loaded) is listed in byDesign with the reason, and skipped. A missing
// safety behaviour is never byDesign: it is an expect with its issue.
//
// Survey: #861.
package driverconformance

import (
	"fmt"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
)

// link sits between a driver and its fake. A driver on a stream socket
// uses relayLink (newTCPLink, newUnixLink); a driver on another transport
// supplies its own.
type link interface {
	// cut closes every connection and refuses new ones; heal accepts
	// again.
	cut()
	heal()
	// freeze holds traffic without closing anything (a hung device);
	// thaw releases it.
	freeze()
	thaw()
	// refused counts connection attempts refused while cut; held counts
	// requests held while frozen.
	refused() int64
	held() int64
}

// rig is one running driver against its fake. Every field is required
// unless it says otherwise.
type rig struct {
	drv nio.Driver
	// in is an input tag the fake serves; out an output tag it accepts.
	in, out string
	// outVals are two different values for out, in the form the runtime
	// hands WriteOutputs.
	outVals [2]any
	// field reads out back from the fake, in the same form as outVals: the
	// value the device is acting on. A driver whose closed gate still
	// sends frames marked invalid reports the last valid value.
	field func() any
	// sent, optional, is every value of out the device has received, in
	// order. With it the holdAndDeliver check sees every command; without
	// it the check samples field and can miss one that is overwritten
	// within a poll.
	sent func() []any
	// polls counts the driver's completed reads (a Health counter), so a
	// "nothing happened" check waits for real polls instead of a sleep.
	polls func() uint64
	// failRead makes the next reads of in fail on a live link (an
	// exception, a bad status). nil when the fake cannot (see unsupported).
	failRead func()
	link     link
}

// startOpts says how a contract wants the driver started.
type startOpts struct {
	// cut starts the link down.
	cut bool
	// install, when non-nil, is called with the driver before Start, to
	// install a write gate the way naut run would.
	install func(nio.Driver)
}

// subject is one driver in the suite.
type subject struct {
	name string
	// start starts the driver against a fresh fake, calling o.install
	// before the driver's Start, and registers its teardown with
	// stopOnCleanup.
	start func(t tb, o startOpts) *rig
	// expect maps a contract to the issue tracking the driver's gap
	// ("#123").
	expect map[string]string
	// unsupported maps a contract to why the fake cannot exercise it.
	unsupported map[string]string
	// byDesign maps a contract to why it cannot apply to this driver.
	byDesign map[string]string
	// gate is how the driver treats a command pushed while its write gate
	// is closed (C5, C8). Required unless both are skipped.
	gate gateMode
}

// gateMode is a driver's design for commands pushed while its write gate
// is closed. Both keep C5 and C8; the suite checks each by its own rules.
type gateMode int

const (
	gateUnset gateMode = iota
	// dropAndAsk: nothing pushed while closed is kept; when the gate
	// reopens the driver asks for every output (io.OutputsRequester), and
	// the next push goes out. modbus, eip, optommp, plcnext.
	dropAndAsk
	// holdAndDeliver: the driver keeps the latest command pushed while
	// closed and sends it when the gate reopens, with no further push. A
	// command superseded while the gate was closed is never sent: a stale
	// command delivered on reopen is the danger this design carries.
	holdAndDeliver
)

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
	{"C5", "the plain write gate: closed sends nothing, reopened asks for every output", c5},
	{"C6", "Stop returns within a bound while a request hangs at the device", c6},
	{"C7", "a link that comes back brings the input back to Good", c7},
	{"C8", "the leadership gate: closed sends nothing, reopened asks for every output", c8},
}

func TestDriverContracts(t *testing.T) {
	if len(subjects) == 0 {
		t.Fatal("no drivers registered")
	}
	for _, s := range subjects {
		t.Run(s.name, func(t *testing.T) {
			for _, e := range validate(s, contracts) {
				t.Error(e)
			}
			for _, c := range contracts {
				t.Run(c.id, func(t *testing.T) {
					if why, ok := s.unsupported[c.id]; ok {
						t.Skipf("%s: not exercisable through the fake: %s", c.what, why)
					}
					if why, ok := s.byDesign[c.id]; ok {
						t.Skipf("%s: does not apply to this driver: %s", c.what, why)
					}
					failed, msgs := record(t, func(r tb) { c.run(r, s) })
					issue, expected := s.expect[c.id]
					errMsg, logMsg := verdict(c, issue, expected, failed, msgs)
					if logMsg != "" {
						t.Log(logMsg)
					}
					if errMsg != "" {
						t.Error(errMsg)
					}
				})
			}
		})
	}
}

var issueRef = regexp.MustCompile(`^#[0-9]+$`)

// validate checks a subject's marks: every key a known contract, every
// expected failure citing an issue, every unsupported or byDesign contract
// giving a reason, no contract carrying two marks, and a gate mode
// declared unless C5 and C8 are both skipped.
func validate(s subject, cs []contract) []string {
	known := map[string]bool{}
	for _, c := range cs {
		known[c.id] = true
	}
	var errs []string
	for _, k := range sortedKeys(s.expect) {
		if !known[k] {
			errs = append(errs, fmt.Sprintf("expect names unknown contract %q", k))
		}
		if !issueRef.MatchString(s.expect[k]) {
			errs = append(errs, fmt.Sprintf("expect[%s] = %q: an expected failure cites the issue tracking it, as #123", k, s.expect[k]))
		}
	}
	for _, m := range []struct {
		name  string
		marks map[string]string
	}{{"unsupported", s.unsupported}, {"byDesign", s.byDesign}} {
		for _, k := range sortedKeys(m.marks) {
			if !known[k] {
				errs = append(errs, fmt.Sprintf("%s names unknown contract %q", m.name, k))
			}
			if strings.TrimSpace(m.marks[k]) == "" {
				errs = append(errs, fmt.Sprintf("%s[%s] gives no reason", m.name, k))
			}
		}
	}
	for _, c := range cs {
		var on []string
		for _, m := range []struct {
			name  string
			marks map[string]string
		}{{"expect", s.expect}, {"unsupported", s.unsupported}, {"byDesign", s.byDesign}} {
			if _, ok := m.marks[c.id]; ok {
				on = append(on, m.name)
			}
		}
		if len(on) > 1 {
			errs = append(errs, fmt.Sprintf("%s carries more than one mark: %s", c.id, strings.Join(on, ", ")))
		}
	}
	skipped := func(id string) bool {
		_, u := s.unsupported[id]
		_, b := s.byDesign[id]
		return u || b
	}
	if s.gate == gateUnset && !(skipped("C5") && skipped("C8")) {
		errs = append(errs, "no gate mode: declare gate dropAndAsk or holdAndDeliver (C5, C8)")
	}
	return errs
}

func sortedKeys(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// verdict turns one contract's outcome into what the test reports: an
// error (errMsg) or only a log line (logMsg).
func verdict(c contract, issue string, expected, failed bool, msgs []string) (errMsg, logMsg string) {
	switch {
	case expected && failed:
		return "", fmt.Sprintf("expected failure (%s): %s: %s", issue, c.what, strings.Join(msgs, "; "))
	case expected:
		return fmt.Sprintf("%s now passes: remove its expected-failure mark (%s)", c.what, issue), ""
	case failed:
		return fmt.Sprintf("%s:\n%s", c.what, strings.Join(msgs, "\n")), ""
	}
	return "", ""
}

// tb is testing.TB. A contract runs against a recorder, so an expected
// failure can be observed without failing the test; fakes' own helpers
// (which take a testing.TB) report through it too.
type tb = testing.TB

// recorder is a testing.TB whose failures are recorded, not reported, and
// whose cleanups run when the contract ends, before its verdict, so a
// teardown that fails (a Stop that never returns) counts against the
// contract instead of hanging the package. Logs, temp dirs and the rest go
// to the real test.
type recorder struct {
	*testing.T
	mu       sync.Mutex
	failed   bool
	msgs     []string
	cleanups []func()
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

func (r *recorder) Cleanup(f func()) {
	r.mu.Lock()
	r.cleanups = append(r.cleanups, f)
	r.mu.Unlock()
}

// record runs f against a recorder on its own goroutine (Fatalf ends it
// with Goexit), then its cleanups, last registered first, each on its own
// goroutine too, and reports whether anything failed and why.
func record(t *testing.T, f func(tb)) (bool, []string) {
	r := &recorder{T: t}
	run := func(f func()) {
		done := make(chan struct{})
		go func() {
			defer close(done)
			f()
		}()
		<-done
	}
	run(func() { f(r) })
	for {
		r.mu.Lock()
		n := len(r.cleanups)
		if n == 0 {
			r.mu.Unlock()
			break
		}
		c := r.cleanups[n-1]
		r.cleanups = r.cleanups[:n-1]
		r.mu.Unlock()
		run(c)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.failed, r.msgs
}

// stopBound is how long Stop may take, with the device hung or not. Every
// driver's per-request timeout in the suite's rigs is well under it.
const stopBound = 5 * time.Second

// stopOnCleanup is every rig's teardown: cancel the driver's context and
// Stop it, failing (rather than hanging) if Stop does not return.
func stopOnCleanup(t tb, cancel func(), stop func()) {
	t.Cleanup(func() {
		cancel()
		if !returnsWithin(stopBound, stop) {
			t.Errorf("teardown: Stop still running after %s", stopBound)
		}
	})
}

func returnsWithin(d time.Duration, f func()) bool {
	done := make(chan struct{})
	go func() { f(); close(done) }()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
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

func good(r *rig) bool { return delivered(r) && quality(r, r.in) == nio.Good }

// up starts s with its link up and waits for the first delivery.
func up(t tb, s subject, o startOpts) *rig {
	t.Helper()
	r := s.start(t, o)
	wait(t, 5*time.Second, r.in+" delivered and Good", func() bool { return good(r) })
	return r
}

func c1(t tb, s subject) {
	r := s.start(t, startOpts{cut: true})
	if q := quality(r, r.in); q != nio.NotConnected {
		t.Fatalf("%s before any read: quality %s, want NotConnected", r.in, q)
	}
	// Still NotConnected once the driver has tried and failed.
	wait(t, 5*time.Second, "a refused connection attempt", func() bool { return r.link.refused() > 0 })
	if q := quality(r, r.in); q != nio.NotConnected {
		t.Fatalf("%s after a refused connection attempt: quality %s, want NotConnected", r.in, q)
	}
}

func c2(t tb, s subject) {
	up(t, s, startOpts{})
}

func c3(t tb, s subject) {
	r := up(t, s, startOpts{})
	if r.failRead == nil {
		t.Fatalf("the rig has no failRead: list C3 in unsupported with the reason")
	}
	r.failRead()
	wait(t, 5*time.Second, r.in+" Bad after a failed read on a live link", func() bool { return quality(r, r.in) == nio.Bad })
}

func c4(t tb, s subject) {
	r := up(t, s, startOpts{})
	r.link.cut()
	wait(t, 5*time.Second, r.in+" Stale after the link was cut", func() bool { return quality(r, r.in) == nio.Stale })
}

func c5(t tb, s subject) {
	took := 0
	gateFlow(t, s, func(d nio.Driver, gate func() bool) {
		took = nio.SetPlainGate(d, gate)
	}, func() {
		if took == 0 {
			t.Fatalf("the driver is not io.PlainGated: the gate naut run installs in every project does not reach it")
		}
	})
}

func c8(t tb, s subject) {
	took := false
	gateFlow(t, s, func(d nio.Driver, gate func() bool) {
		if g, ok := d.(interface{ SetWriteGate(func() bool) }); ok {
			g.SetWriteGate(gate)
			took = true
		}
	}, func() {
		if !took {
			t.Fatalf("the driver takes no write gate")
		}
	})
}

// gateFlow is C5 and C8: start the driver with a gate installed by
// install, check it took (took), then close the gate, push a change, see
// nothing arrive, and reopen it. Then, by the subject's gate mode: see the
// driver ask for every output and the next push arrive (dropAndAsk), or
// see the held command arrive with no push, and a command superseded
// while the gate was closed never arrive (holdAndDeliver).
func gateFlow(t tb, s subject, install func(nio.Driver, func() bool), took func()) {
	var mu sync.Mutex
	open := true
	gate := func() bool { mu.Lock(); defer mu.Unlock(); return open }
	setGate := func(v bool) { mu.Lock(); open = v; mu.Unlock() }

	r := up(t, s, startOpts{install: func(d nio.Driver) { install(d, gate) }})
	took()
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

	switch s.gate {
	case dropAndAsk:
		setGate(true)
		req, ok := r.drv.(nio.OutputsRequester)
		if !ok {
			t.Fatalf("the gate dropped a command and the driver cannot ask for it again (no io.OutputsRequester)")
		}
		wait(t, 5*time.Second, "OutputsWanted after the gate reopened", req.OutputsWanted)
		push(a)
		wait(t, 5*time.Second, fmt.Sprintf("%s = %v at the device after the gate reopened", r.out, a), func() bool { return r.field() == a })
	case holdAndDeliver:
		setGate(true)
		wait(t, 5*time.Second, fmt.Sprintf("%s = %v, held while the gate was closed, at the device after it reopened (no further push)", r.out, a),
			func() bool { return r.field() == a })
		// Latest wins: close the gate again and push b, then a, which the
		// device already holds. Nothing is owed. b went stale the moment a
		// was pushed, and must never reach the device: not while the gate
		// is closed, not when it reopens.
		setGate(false)
		settle(t, r, 2)
		var mark int
		if r.sent != nil {
			mark = len(r.sent())
		}
		seen := sample(r.field)
		push(b)
		push(a)
		settle(t, r, 3)
		setGate(true)
		settle(t, r, 5)
		got := seen()
		if r.sent != nil {
			got = r.sent()[mark:]
		}
		for _, v := range got {
			if v != a {
				t.Fatalf("%s at the device went through %v; want only %v: %v was pushed while the gate was closed and superseded by %v before it reopened, so it must never be sent",
					r.out, got, a, b, a)
			}
		}
	default:
		t.Fatalf("the subject declares no gate mode")
	}
}

// sample reads field every millisecond until the returned func is called,
// and returns the distinct values it saw, in the order first seen.
func sample(field func() any) (stop func() []any) {
	var mu sync.Mutex
	var seen []any
	note := func() {
		v := field()
		mu.Lock()
		defer mu.Unlock()
		for _, s := range seen {
			if s == v {
				return
			}
		}
		seen = append(seen, v)
	}
	note()
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		for {
			select {
			case <-done:
				return
			case <-time.After(time.Millisecond):
				note()
			}
		}
	}()
	return func() []any {
		close(done)
		<-finished
		note()
		mu.Lock()
		defer mu.Unlock()
		return append([]any(nil), seen...)
	}
}

func c6(t tb, s subject) {
	r := up(t, s, startOpts{})
	stopper, ok := r.drv.(interface{ Stop() })
	if !ok {
		t.Fatalf("the driver has no Stop")
	}
	r.link.freeze()
	// Registered last, so it runs first: the link thaws before the rig's
	// own teardown stops the driver again.
	t.Cleanup(r.link.thaw)
	wait(t, 5*time.Second, "a request held at the frozen device", func() bool { return r.link.held() > 0 })
	if !returnsWithin(stopBound, stopper.Stop) {
		t.Fatalf("Stop still running after %s with a request hung at the device", stopBound)
	}
}

func c7(t tb, s subject) {
	r := up(t, s, startOpts{})
	r.link.cut()
	wait(t, 5*time.Second, r.in+" Stale after the link was cut", func() bool { return quality(r, r.in) == nio.Stale })
	r.link.heal()
	wait(t, 10*time.Second, r.in+" Good again after the link came back", func() bool { return good(r) })
}
