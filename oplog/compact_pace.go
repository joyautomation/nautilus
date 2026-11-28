//go:build !js

package oplog

import (
	goruntime "runtime"
	"time"
)

// The compaction's pacing (#1455): the decision and the file rewrite are
// long CPU jobs beside the scan and the writes. On a small box (one core,
// a Verdin's) a job that runs flat out holds every other goroutine back,
// and a CPU quota throttles the whole process for it: the bench saw a
// 10 ms ticker wake 40-60 ms late. So both run duty-cycled: a chunk of
// work (paceWork, measured by the clock, never by a step count: on a slow
// box a few steps are already a chunk), then a rest as long as keeps the
// job to paceDuty of the time (for the stretch it actually ran: a step
// longer than the chunk rests longer). A year's first decision takes
// about 1/paceDuty as long, which nothing waits on.
//
// A pacer never runs under the locks: a caller that holds them passes a
// nil one (tick on nil does nothing).

// yieldEvery is how often a chunk gives the processor up.
const yieldEvery = 250 * time.Microsecond

// pacer duty-cycles one long job outside the locks.
type pacer struct {
	done  <-chan struct{} // closed: no more rests (a cancelled decision, a closed node)
	work  time.Duration   // a chunk of work
	rest  float64         // the rest per unit of work (0: unpaced)
	now   func() time.Time
	sleep func(done <-chan struct{}, d time.Duration) // tests: the clock's
	from  time.Time                                   // when this chunk began
	yield time.Time                                   // when it last gave the processor up
}

// newPacer is a pacer for lim's chunk and duty, rests cut short when done
// closes (nil: never).
func newPacer(done <-chan struct{}, lim decideLimits) *pacer {
	p := &pacer{done: done, work: lim.paceWork, now: time.Now, sleep: sleepUntil}
	if lim.paceNow != nil {
		p.now = lim.paceNow
	}
	if lim.paceSleep != nil {
		p.sleep = lim.paceSleep
	}
	if lim.paceDuty > 0 && lim.paceDuty < 1 {
		p.rest = (1 - lim.paceDuty) / lim.paceDuty
	}
	if mutOplog("compaction_unpaced") {
		p.rest = 0
	}
	p.from = p.now()
	return p
}

// tick is one step of the job: the processor given up every yieldEvery,
// and a rest when this chunk has run its time. Once done closes it never
// rests again (the job ends at its own next check of its context, or runs
// on to its end, still yielding).
func (p *pacer) tick() {
	if p == nil {
		return
	}
	t := p.now()
	if t.Sub(p.yield) >= yieldEvery {
		// A goroutine woken meanwhile (a scan's) runs at once, not at the
		// chunk's end. Unpaced too: 2b-1's loops yielded as well.
		p.yield = t
		goruntime.Gosched()
	}
	ran := t.Sub(p.from)
	if p.rest <= 0 || ran < p.work {
		return
	}
	select {
	case <-p.done:
		p.rest = 0
		return
	default:
	}
	// A stretch is counted to four chunks at most: the clock also runs
	// while the job waits for the processor, and a long wait is not work.
	p.sleep(p.done, time.Duration(float64(min(ran, 4*p.work))*p.rest))
	p.from = p.now()
}

// Pace is tick as CompactInput.Pace hands it out.
func (p *pacer) Pace() { p.tick() }

func sleepUntil(done <-chan struct{}, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-done:
	case <-t.C:
	}
}
