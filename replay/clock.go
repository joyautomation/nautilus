package replay

import (
	"math"
	"time"
)

// Clock maps wall time onto recorded time: Pos moves Speed recorded
// seconds per wall second between From and To, wrapping back to From at
// To (a baseline loops forever). Pause holds it; Seek jumps it.
type Clock struct {
	From, To float64 // unix seconds
	Pos      float64
	Speed    float64
	Pause    bool
	Loops    int

	last     time.Time
	lastSeek int64
}

// Advance moves the clock to wall time now and returns the position.
func (c *Clock) Advance(now time.Time) float64 {
	if !c.last.IsZero() && !c.Pause && c.Speed > 0 {
		c.Pos += now.Sub(c.last).Seconds() * c.Speed
	}
	c.last = now
	if span := c.To - c.From; span > 0 && c.Pos >= c.To {
		n := math.Floor((c.Pos - c.From) / span)
		c.Pos -= n * span
		c.Loops += int(n)
	}
	return c.Pos
}

// Seek jumps to t, clamped into [From, To).
func (c *Clock) Seek(t float64) {
	c.Pos = math.Min(math.Max(t, c.From), math.Max(c.From, c.To-1))
}
