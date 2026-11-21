package escalate

import (
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/alarm"
)

// civil is a calendar date with no zone. Rotations count civil days, so a
// daylight-saving change never moves a handover off its wall-clock time.
type civil struct {
	y int
	m time.Month
	d int
}

// days is the number of calendar days from a to b (negative if b is
// earlier). Noon UTC keeps the subtraction clear of any zone.
func (a civil) days(b civil) int {
	ta := time.Date(a.y, a.m, a.d, 12, 0, 0, 0, time.UTC)
	tb := time.Date(b.y, b.m, b.d, 12, 0, 0, 0, time.UTC)
	return int(tb.Sub(ta).Round(24*time.Hour) / (24 * time.Hour))
}

func civilOf(t time.Time) civil { return civil{t.Year(), t.Month(), t.Day()} }

// clock parses HH:MM.
func clock(s string) (time.Duration, error) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		return 0, fmt.Errorf("%q is not a time of day (want HH:MM, 24-hour)", s)
	}
	return time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute, nil
}

var dayNames = map[string]time.Weekday{
	"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday,
	"thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday,
}

// window is a parsed Window.
type window struct {
	days     [7]bool
	from, to time.Duration
}

func (w Window) parse(at string) (window, error) {
	var out window
	if len(w.Days) == 0 {
		for i := range out.days {
			out.days[i] = true
		}
	}
	for j, d := range w.Days {
		lo, hi, isRange := strings.Cut(strings.ToLower(strings.TrimSpace(d)), "-")
		a, ok := dayNames[lo]
		if !ok {
			return out, fmt.Errorf("%s.days[%d]: %q is not a day (want mon … sun, or a range like mon-fri)", at, j, d)
		}
		b := a
		if isRange {
			if b, ok = dayNames[hi]; !ok {
				return out, fmt.Errorf("%s.days[%d]: %q is not a day range (want e.g. mon-fri)", at, j, d)
			}
		}
		for x := a; ; x = (x + 1) % 7 {
			out.days[x] = true
			if x == b {
				break
			}
		}
	}
	var err error
	if w.From == "" || w.To == "" {
		return out, fmt.Errorf("%s: a window needs from and to (HH:MM)", at)
	}
	if out.from, err = clock(w.From); err != nil {
		return out, fmt.Errorf("%s.from: %w", at, err)
	}
	if out.to, err = clock(w.To); err != nil {
		return out, fmt.Errorf("%s.to: %w", at, err)
	}
	return out, nil
}

// at is the instant of time-of-day tod on civil day c in loc. A time a
// daylight-saving change skips lands at the end of the gap (time.Date
// would put it an hour early, before the gap); one it repeats is one of
// its two instants, as time.Date chooses.
func at(c civil, tod time.Duration, loc *time.Location) time.Time {
	h, m := int(tod/time.Hour), int(tod%time.Hour/time.Minute)
	t := time.Date(c.y, c.m, c.d, h, m, 0, 0, loc)
	if t.Hour() != h || t.Minute() != m {
		if _, end := t.ZoneBounds(); !end.IsZero() {
			return end // the moment the clocks jumped
		}
	}
	return t
}

func addDays(c civil, n int) civil {
	return civilOf(time.Date(c.y, c.m, c.d+n, 12, 0, 0, 0, time.UTC))
}

// contains reports whether instant t falls in the window, read in loc. It
// compares instants, not clock readings, so the hour a daylight-saving
// change repeats belongs to one side of a boundary, not both. A window past
// midnight belongs to the day it starts on; from == to is 24 hours from
// from.
func (w window) contains(t time.Time, loc *time.Location) bool {
	day := civilOf(t.In(loc))
	for _, sd := range []civil{addDays(day, -1), day} {
		if !w.days[time.Date(sd.y, sd.m, sd.d, 12, 0, 0, 0, time.UTC).Weekday()] {
			continue
		}
		start := at(sd, w.from, loc)
		ed := sd
		if w.to <= w.from {
			ed = addDays(sd, 1)
		}
		if !t.Before(start) && t.Before(at(ed, w.to, loc)) {
			return true
		}
	}
	return false
}

// OnCall returns who is on call on roster r at t: every member of a plain
// roster, the member whose turn it is on a rotating one, or the members of
// every shift covering t. loc is the roster's zone (Oncall.Location).
func (r *Roster) OnCall(t time.Time, loc *time.Location) []string {
	lt := t.In(loc)
	switch {
	case len(r.shifts) > 0:
		var out []string
		for i, w := range r.shifts {
			if w.contains(t, loc) {
				for _, m := range r.Shifts[i].Members {
					out = appendNew(out, m)
				}
			}
		}
		return out
	case r.Rotate == "":
		return append([]string(nil), r.Members...)
	}
	// A turn begins at the handover: before it, the instant belongs to the
	// previous day's turn.
	day := civilOf(lt)
	if t.Before(at(day, r.handover, loc)) {
		day = addDays(day, -1)
	}
	n := r.start.days(day)
	if r.Rotate == "weekly" {
		n = floorDiv(n, 7)
	}
	i := ((n % len(r.Members)) + len(r.Members)) % len(r.Members)
	return []string{r.Members[i]}
}

func floorDiv(a, b int) int {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

func appendNew(s []string, v string) []string {
	for _, x := range s {
		if x == v {
			return s
		}
	}
	return append(s, v)
}

// pathMatch is the alarm package's glob: case-insensitive path.Match.
func pathMatch(pattern, s string) (bool, error) {
	if strings.EqualFold(pattern, s) {
		return true, nil
	}
	return path.Match(strings.ToUpper(pattern), strings.ToUpper(s))
}

func globOK(pattern, s string) bool {
	if pattern == "" {
		return true
	}
	ok, err := pathMatch(pattern, s)
	return err == nil && ok
}

// Covers reports whether the policy's match covers alarm d. The `when:`
// window is not part of it: Select reads that at the escalation's start.
func (m *Match) Covers(d alarm.Def) bool {
	if len(m.prio) > 0 {
		hit := false
		for _, p := range m.prio {
			if p == d.Priority {
				hit = true
				break
			}
		}
		if !hit {
			return false
		}
	}
	return globOK(m.ID, d.ID) && globOK(m.Site, d.Site) && globOK(m.Area, d.Area) && globOK(m.Class, d.Class)
}

// Select returns the first policy (in manifest order) that covers alarm d
// and whose `when:` window covers since, the moment the alarm began
// waiting; nil if none. The choice is fixed for that escalation: a
// day-shift policy keeps running past the end of the day shift.
func (c *Config) Select(d alarm.Def, since time.Time) *Policy {
	loc := c.Oncall.Location()
	for i := range c.Policies {
		p := &c.Policies[i]
		if !p.Match.Covers(d) {
			continue
		}
		if p.when != nil && !p.when.contains(since, loc) {
			continue
		}
		return p
	}
	return nil
}

// Step is one send of a policy: a level (0-based) and its repeat (0 is the
// level's own send), due At after the escalation began.
type Step struct {
	Level  int
	Repeat int
	At     time.Duration
}

func buildSteps(p *Policy) []Step {
	out := make([]Step, 0, len(p.Levels)+p.Repeat.Times)
	for i, l := range p.Levels {
		out = append(out, Step{Level: i, At: time.Duration(l.After)})
	}
	last := len(p.Levels) - 1
	base := time.Duration(p.Levels[last].After)
	for j := 1; j <= p.Repeat.Times; j++ {
		out = append(out, Step{Level: last, Repeat: j, At: base + time.Duration(j)*time.Duration(p.Repeat.Every)})
	}
	return out
}

// Steps is every send the policy makes, in order of At.
func (p *Policy) Steps() []Step { return p.steps }

// Escalating reports whether an alarm in state s is still waiting on a
// person under stop rule stop. Only an unacknowledged alarm escalates;
// ack-or-rtn also lets a return to normal end it. Shelved and suppressed
// alarms never escalate: an operator silenced the first, the design the
// second.
func Escalating(s alarm.State, stop string) bool {
	switch s {
	case alarm.UnackActive:
		return true
	case alarm.UnackRTN:
		return stop != StopAckOrRTN
	}
	return false
}
