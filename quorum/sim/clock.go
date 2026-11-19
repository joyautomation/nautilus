package sim

import (
	"time"

	"github.com/joyautomation/nautilus/quorum"
)

// PPM is a clock rate in parts per million of real (global) time: 1e6 is
// a perfect clock, 0 a stopped one. Rates are integers so a seed replays
// bit for bit on every architecture (no floating point, no fused
// multiply-add differences).
type PPM int64

// Perfect is a clock that keeps global time.
const Perfect PPM = 1_000_000

// segment is one stretch of a node clock: from global time g on, local
// time is l + (t-g)·rate/1e6.
type segment struct {
	g    time.Duration
	l    quorum.Instant
	rate PPM
}

// Clock is a node's clock, piecewise linear in global time. Its whole
// history is kept, so a local instant can be mapped back to global time
// after the run (the oracles work on exact intervals, not samples).
type Clock struct {
	segs []segment
}

// NewClock starts a clock at global time g reading l, running at rate.
func NewClock(g time.Duration, l quorum.Instant, rate PPM) *Clock {
	return &Clock{segs: []segment{{g: g, l: l, rate: rate}}}
}

// Local reads the clock at global time t (t not before the last change).
func (c *Clock) Local(t time.Duration) quorum.Instant {
	s := c.at(t)
	return s.l + quorum.Instant(int64(t-s.g)*int64(s.rate)/int64(Perfect))
}

func (c *Clock) at(t time.Duration) segment {
	for i := len(c.segs) - 1; i > 0; i-- {
		if c.segs[i].g <= t {
			return c.segs[i]
		}
	}
	return c.segs[0]
}

// Rate is the clock's rate now.
func (c *Clock) Rate() PPM { return c.segs[len(c.segs)-1].rate }

// SetRate changes the rate from global time t on. Changes come in time
// order.
func (c *Clock) SetRate(t time.Duration, rate PPM) {
	l := c.Local(t)
	last := &c.segs[len(c.segs)-1]
	if last.g == t {
		last.l, last.rate = l, rate
		return
	}
	c.segs = append(c.segs, segment{g: t, l: l, rate: rate})
}

// Never is a global time later than any run: a local instant the clock
// never reaches maps to it.
const Never = time.Duration(1<<62 - 1)

// Global returns the first global time at which the clock reads at least
// l, searching from global time from on. It is exact: the smallest t with
// Local(t) >= l under the integer rounding Local uses. Never when the
// clock never gets there (stopped for good).
func (c *Clock) Global(l quorum.Instant, from time.Duration) time.Duration {
	if c.Local(from) >= l {
		return from
	}
	for i, s := range c.segs {
		end := Never
		if i+1 < len(c.segs) {
			end = c.segs[i+1].g
		}
		if end <= from {
			continue
		}
		if s.rate <= 0 {
			continue
		}
		// smallest d with floor(d·rate/1e6) >= need: d = ceil(need·1e6/rate)
		need := int64(l - s.l)
		d := (need*int64(Perfect) + int64(s.rate) - 1) / int64(s.rate)
		t := s.g + time.Duration(d)
		t = max(t, s.g, from)
		if t < end {
			return t
		}
	}
	return Never
}
