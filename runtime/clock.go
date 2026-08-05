package runtime

import "time"

// Clock is the runtime's source of time for the two clocks a PROGRAM can
// actually observe:
//
//  1. the measured scan-to-scan seconds bound to DtTag — every PI loop,
//     integrator, and ramp in the resource integrates against it;
//  2. the millisecond base that IEC timers count from, reached through
//     ir.Host.NowMs — TON, TOF, TP, and every user block built on them.
//
// In production Options.Clock is nil and the runtime reads the wall clock
// directly, at no extra cost. A test supplies a virtual clock instead, so a
// 10 s on-delay elapses in microseconds and two runs of the same test are
// bit-identical. Without it a test that loops Scan() a hundred times
// simulates a few hundred microseconds of process time, which is why an
// alarm delay or a loop's settling time could not be asserted at all.
//
// Nothing else follows this clock. Scan diagnostics (ReadMs, ExecUs,
// WriteMs), Sparkplug timestamps, and the HMI's SSE stamps keep measuring
// real elapsed time, because they describe the machine rather than the
// process — and they stay honest even under a virtual clock. That
// separation is safe by construction: a Clock belongs to one Runtime, not
// to the process, so injecting one cannot reach anything outside it.
type Clock interface {
	Now() time.Time
}

// now returns the basis for scan dt: the injected clock when there is one,
// otherwise the wall reading the caller already took. Passing t0 in keeps
// the default path down to the single time.Now() the scan always made.
func (r *Runtime) now(t0 time.Time) time.Time {
	if r.clock != nil {
		return r.clock.Now()
	}
	return t0
}

// TaskSchedule is one task's identity and interval. Schedule returns them
// for a scheduler written outside this package — the acceptance harness
// replays tick order deterministically instead of using Run's tickers.
type TaskSchedule struct {
	Name     string
	Interval time.Duration
}

// Schedule lists every task in the resource with its scan interval — the
// main task first, then additional tasks in declaration order. That order
// is also the order Run's goroutines would contend for the scan lock in,
// so a scheduler that breaks same-instant ties by it reproduces one of the
// interleavings the runtime can actually produce.
func (r *Runtime) Schedule() []TaskSchedule {
	out := make([]TaskSchedule, 0, len(r.tasks)+1)
	out = append(out, TaskSchedule{Name: MainTaskName, Interval: r.scan})
	for _, tr := range r.tasks {
		out = append(out, TaskSchedule{Name: tr.name, Interval: tr.scan})
	}
	return out
}
