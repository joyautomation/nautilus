package acceptance

import (
	"fmt"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/runtime"
)

// Live runs the same *_test.yaml scenarios against a resource that runs
// SOMEWHERE ELSE — a Logix controller the program was deployed to,
// observed through a runtime whose tag store mirrors the controller over
// EtherNet/IP (logix/facade). The unit tests prove the program; these
// prove the download: the controller, acting as the runtime, passes the
// same scenarios (docs/design/logix-authoring.md §5.5).
//
// What changes when the clock is real:
//
//   - `given` writes go to the controller and the step waits until the
//     mirror reads them back, so time never starts before the input lands.
//   - `advance: d` is d of wall time, then one poll, so the expectation
//     sees the controller at least d after the write.
//   - `scans: n` is n controller task periods plus one poll. A continuous
//     task has no period, so it counts as one poll each.
//   - `until` / `hold` / `always` are evaluated on every poll.
//   - a timing assertion is only as sharp as one poll plus one scan, so a
//     `near`/`tol` on a timer value should allow for both.
//   - `suspend` is a no-op: the tasks it names are nautilus tasks, and
//     none of them runs on the controller. Alarm verbs are not available.
type Live struct {
	// Runtime holds the mirror store the controller's values are polled
	// into; the scenario reads from it.
	Runtime *runtime.Runtime
	// Write sends one value to the controller.
	Write func(name string, value any) error
	// Poll is the mirror's refresh period; Scan the controller task's
	// period (0 for a continuous task).
	Poll, Scan time.Duration
	// Resolve maps a scenario's tag name to the mirror's. A Logix
	// program-scope tag is served as <Program>_<Tag>; nil means names are
	// used as written.
	Resolve func(name string) string
	// Libraries are the project's library sources, for ST expressions.
	Libraries []string
	// Heartbeat names a mirror tag the controller increments once per
	// task scan (the writer's side code). With it, `scans: n` waits for
	// exactly n counts; without it, n task periods of wall time.
	Heartbeat string
}

// RunSuiteLive runs every test in the suite against the live resource.
func RunSuiteLive(s *Suite, live Live, o ...Option) ([]Result, error) {
	if live.Runtime == nil || live.Write == nil {
		return nil, fmt.Errorf("live: a runtime mirror and a write function are required")
	}
	if live.Poll <= 0 {
		live.Poll = 250 * time.Millisecond
	}
	if live.Resolve == nil {
		live.Resolve = func(n string) string { return n }
	}
	var out []Result
	for _, t := range s.Tests {
		r, err := runTestLive(s, t, live)
		if err != nil {
			return out, err
		}
		out = append(out, r)
	}
	return out, nil
}

type liveRun struct {
	*testRun
	live  Live
	start time.Time
	scans int // controller scans spent on the heartbeat, when there is one
}

func (r *liveRun) elapsed() time.Duration { return time.Since(r.start) }

func runTestLive(s *Suite, t *Test, live Live) (Result, error) {
	res := Result{Suite: s.Path, Name: t.Name, Line: t.Line}
	known := map[string]bool{}
	for name := range live.Runtime.Tags().Snapshot() {
		known[name] = true
	}
	r := &liveRun{
		testRun: &testRun{
			rt:    live.Runtime,
			libs:  live.Libraries,
			tol:   firstNonZero(deref(t.Tolerance), s.Tolerance),
			known: known,
			meta:  live.Runtime.Meta(),
			preds: map[string]*predicate{},
		},
		live:  live,
		start: time.Now(),
	}
	if err := r.applyLive(t.Given); err != nil {
		return res, fmt.Errorf("%s:%d: test %q: %w", s.Path, t.Line, t.Name, err)
	}
	steps, err := t.steps()
	if err != nil {
		return res, err
	}
	for i, st := range steps {
		fail, err := r.runStepLive(st)
		if err != nil {
			return res, fmt.Errorf("%s:%d: test %q: %w", s.Path, st.Line, t.Name, err)
		}
		if fail != nil {
			fail.Step = i + 1
			fail.Line = st.Line
			fail.AtMs = float64(fail.At) / float64(time.Millisecond)
			res.Failure = fail
			res.Elapsed, res.Scans = r.elapsed(), r.scans
			return res, nil
		}
	}
	res.Passed = true
	res.Elapsed, res.Scans = r.elapsed(), r.scans
	return res, nil
}

// applyLive writes every given value to the controller, then waits for
// the mirror to read each one back.
func (r *liveRun) applyLive(given map[string]any) error {
	if len(given) == 0 {
		return nil
	}
	want := map[string]any{}
	for _, name := range sortedKeys(given) {
		m := r.live.Resolve(name)
		if !r.known[m] {
			return fmt.Errorf("given: no tag %q on the controller (looked for %q)", name, m)
		}
		if err := r.live.Write(m, given[name]); err != nil {
			return fmt.Errorf("given: %s: %w", name, err)
		}
		want[m] = given[name]
	}
	deadline := time.Now().Add(10*r.live.Poll + 2*time.Second)
	for {
		pending := ""
		for m, v := range want {
			got, err := r.rt.Tags().ReadGlobal(m)
			if err != nil || !sameValue(got, v) {
				pending = m
				break
			}
		}
		if pending == "" {
			return nil
		}
		if time.Now().After(deadline) {
			got, _ := r.rt.Tags().ReadGlobal(pending)
			return fmt.Errorf("given: %s was written as %v but the controller still reads %s", pending, want[pending], show(got))
		}
		time.Sleep(r.live.Poll / 4)
	}
}

// sameValue compares a mirror value with the raw value a test wrote.
func sameValue(got ir.Value, want any) bool {
	switch w := want.(type) {
	case bool:
		return got.Kind == ir.TypeBool && got.B == w
	case string:
		return got.Kind == ir.TypeString && got.S == w
	}
	f, ok := toFloat(want)
	if !ok {
		return false
	}
	g := numOf(got)
	if f == 0 {
		return g == 0
	}
	return abs(g-f) <= abs(f)*1e-6
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

// wait spends wall time in poll ticks, running fn after each; fn returns
// true to stop early.
func (r *liveRun) wait(d time.Duration, fn func() bool) {
	end := time.Now().Add(d)
	for {
		remaining := time.Until(end)
		if remaining <= 0 {
			return
		}
		step := r.live.Poll
		if remaining < step {
			step = remaining
		}
		time.Sleep(step)
		if fn != nil && fn() {
			return
		}
	}
}

func (r *liveRun) runStepLive(st *Step) (*Failure, error) {
	if st.Ack != nil || st.Shelve != nil || st.Unshelve != nil || st.Alarms != nil {
		return nil, fmt.Errorf("alarm steps are not available against a controller")
	}
	if err := r.applyLive(st.Given); err != nil {
		return nil, err
	}
	st = r.resolveStep(st)
	tr := r.newTracer(st)
	var alwaysFail *Failure
	var alwaysErr error
	tick := func() bool {
		tr.sample(r.elapsed())
		if st.Always == nil || alwaysFail != nil || alwaysErr != nil {
			return alwaysFail != nil || alwaysErr != nil
		}
		ok, detail, err := r.check(st.Always)
		if err != nil {
			alwaysErr = err
			return true
		}
		if !ok {
			alwaysFail = &Failure{At: r.elapsed(), Reason: "invariant broke", Detail: detail}
			return true
		}
		return false
	}
	tr.sample(r.elapsed())

	switch {
	case st.Until != nil:
		var perr error
		held := false
		var heldSince time.Time
		r.wait(st.Until.get()+st.Hold.get(), func() bool {
			if tick() {
				return true
			}
			ok, _, err := r.check(st.Expect)
			if err != nil {
				perr = err
				return true
			}
			if !ok {
				heldSince = time.Time{}
				return false
			}
			if heldSince.IsZero() {
				heldSince = time.Now()
			}
			if time.Since(heldSince) >= st.Hold.get() {
				held = true
				return true
			}
			return false
		})
		if perr != nil {
			return nil, perr
		}
		if alwaysErr != nil {
			return nil, alwaysErr
		}
		if alwaysFail != nil {
			alwaysFail.Trace = tr.result()
			return alwaysFail, nil
		}
		if !held {
			_, detail, err := r.check(st.Expect)
			if err != nil {
				return nil, err
			}
			reason := fmt.Sprintf("never held within %s", st.Until.get())
			if st.Hold.get() > 0 {
				reason = fmt.Sprintf("never held for %s within %s", st.Hold.get(), st.Until.get())
			}
			return &Failure{At: r.elapsed(), Reason: reason, Detail: detail, Trace: tr.result()}, nil
		}
		return nil, nil
	case st.Advance != nil:
		r.wait(st.Advance.get()+r.live.Poll, tick)
	case st.Scans != nil:
		if r.live.Heartbeat != "" {
			if err := r.waitScans(*st.Scans, tick); err != nil {
				return nil, err
			}
			break
		}
		per := r.live.Scan
		if per <= 0 {
			per = r.live.Poll
		}
		r.wait(time.Duration(*st.Scans)*per+r.live.Poll, tick)
	}
	if alwaysErr != nil {
		return nil, alwaysErr
	}
	if alwaysFail != nil {
		alwaysFail.Trace = tr.result()
		return alwaysFail, nil
	}
	if st.Expect != nil {
		ok, detail, err := r.check(st.Expect)
		if err != nil {
			return nil, err
		}
		if !ok {
			return &Failure{At: r.elapsed(), Reason: "expectation failed", Detail: detail, Trace: tr.result()}, nil
		}
	}
	return nil, nil
}

// heartbeat reads the scan counter.
func (r *liveRun) heartbeat() (int64, error) {
	v, err := r.rt.Tags().ReadGlobal(r.live.Heartbeat)
	if err != nil {
		return 0, fmt.Errorf("heartbeat %s: %w", r.live.Heartbeat, err)
	}
	return int64(numOf(v)), nil
}

// waitScans spends exactly n controller scans: it waits until the
// heartbeat has advanced by n from where it stood, then one more poll so
// the rest of the mirror is at least as fresh as the count. A counter
// that does not move within a generous window means the task is not
// scanning — a faulted or Program-mode controller — and that is an error,
// not a timeout to wait out.
func (r *liveRun) waitScans(n int, tick func() bool) error {
	start, err := r.heartbeat()
	if err != nil {
		return err
	}
	per := r.live.Scan
	if per <= 0 {
		per = r.live.Poll
	}
	budget := time.Duration(n)*per + 10*r.live.Poll + 2*time.Second
	deadline := time.Now().Add(budget)
	r.scans = 0
	for {
		now, err := r.heartbeat()
		if err != nil {
			return err
		}
		if d := now - start; d < 0 || d >= int64(n) {
			r.scans += int(d)
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the controller's scan counter %s did not advance %d in %s (is the controller in Run?)", r.live.Heartbeat, n, budget)
		}
		time.Sleep(r.live.Poll / 4)
		if tick() {
			return nil
		}
	}
	r.wait(r.live.Poll, tick)
	return nil
}

// resolveStep rewrites the expectation's tag names to the mirror's, so the
// shared check() reads the right store entry. Expressions are left as
// written: they compile against the mirror's own names.
func (r *liveRun) resolveStep(st *Step) *Step {
	cp := *st
	cp.Expect = resolveExpect(st.Expect, r.live.Resolve)
	cp.Always = resolveExpect(st.Always, r.live.Resolve)
	return &cp
}

func resolveExpect(e *Expect, resolve func(string) string) *Expect {
	if e == nil {
		return nil
	}
	cp := *e
	cp.Terms = make([]Term, len(e.Terms))
	for i, t := range e.Terms {
		cp.Terms[i] = t
		if t.Tag != "" {
			cp.Terms[i].Tag = resolve(t.Tag)
		}
	}
	return &cp
}

// ResolveLogix is the Resolve for a facade over a Logix controller: a
// name the mirror holds is used as is; otherwise <Program>_<name>.
func ResolveLogix(rt *runtime.Runtime, program string) func(string) string {
	return func(name string) string {
		if _, err := rt.Tags().ReadGlobal(name); err == nil {
			return name
		}
		if program != "" && !strings.Contains(name, ".") {
			return program + "_" + name
		}
		return name
	}
}
