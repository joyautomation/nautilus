package rationalize

import (
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/joyautomation/nautilus/alarm"
)

// KindTuned is the journal kind of a row written when a tuning overlay's
// delays were applied to an alarm at start (#1249): the history before it
// was produced under other delays, so a suggestion's window starts after
// the last one.
const KindTuned = "tuned"

// TuneLadder is the delays a suggestion picks from, smallest first.
var TuneLadder = []time.Duration{
	time.Second, 2 * time.Second, 5 * time.Second, 10 * time.Second,
	15 * time.Second, 30 * time.Second, 60 * time.Second,
}

// Delays are an alarm's on-delay and off-delay.
type Delays struct{ On, Off time.Duration }

// Suggestion is a pair of delays that would have turned a chattering
// alarm's recent history into fewer than count activations per window.
type Suggestion struct {
	ID string `json:"id"`
	// Current are the delays the alarm runs with; the suggestion only ever
	// lengthens them (a shorter delay cannot be judged from the journal).
	Current Delays `json:"-"`
	On      Millis `json:"onDelayMs"`
	Off     Millis `json:"offDelayMs"`
	// Before and After are the most activations inside one window, in the
	// history as it was and as the suggestion would have made it.
	Before int `json:"before"`
	After  int `json:"after"`
	// Bounces is the activations in the history examined.
	Bounces int `json:"bounces"`
	// Since is where the history examined begins: the last tuning, or the
	// first row given (epoch ms).
	Since int64 `json:"sinceMs"`
	// Why is the suggestion in words, ready for a person or a tuning row.
	Why string `json:"why"`
}

// Millis is a duration that is written as whole milliseconds.
type Millis time.Duration

// String is the usual duration text ("5s").
func (m Millis) String() string { return time.Duration(m).String() }

// MarshalJSON writes the milliseconds.
func (m Millis) MarshalJSON() ([]byte, error) {
	return []byte(strconv.FormatInt(time.Duration(m).Milliseconds(), 10)), nil
}

// span is the condition's estimated true stretch behind one activation.
type span struct{ start, end int64 } // end is 0 while the alarm has not returned

// Suggest replays one alarm's journal rows (oldest first, all of this
// alarm's kinds) with longer delays and returns the smallest pair from
// TuneLadder (judged by on plus off) under which no window of the history
// holds count or more activations. ok is false when the history does not
// chatter, or no value on the ladder would have helped.
//
// What the journal can and cannot tell: it records the activation (after the
// on-delay) and the return (after the off-delay), never a bounce the
// current delays already filtered, so only LONGER delays can be judged. The
// raw rise is taken as the activation minus the on-delay and the raw fall as
// the return minus the off-delay, which is a bound, not the signal itself
// (drop-outs shorter than the off-delay are hidden). Spans while the alarm
// was shelved or suppressed are left out, and the history starts after the
// last tuned row.
func Suggest(events []alarm.Event, cur Delays, window time.Duration, count int) (Suggestion, bool) {
	var since int64
	for _, e := range events {
		if e.Kind == KindTuned {
			since = e.TS
		}
	}
	w := window.Milliseconds()
	var spans []span
	var cutFrom int64 // start of a shelved/suppressed stretch, 0 when none
	cut := func(e alarm.Event) bool { return cutFrom != 0 }
	id := ""
	for _, e := range events {
		if e.TS < since {
			continue
		}
		if id == "" {
			id = e.ID
		}
		switch e.Kind {
		case alarm.KindShelve, alarm.KindSuppress:
			if cutFrom == 0 {
				cutFrom = e.TS
			}
			// the activation under way is not judged
			if n := len(spans); n > 0 && spans[n-1].end == 0 {
				spans = spans[:n-1]
			}
		case alarm.KindUnshelve, alarm.KindUnsuppress:
			cutFrom = 0
		case alarm.KindActive:
			if cut(e) {
				continue
			}
			spans = append(spans, span{start: e.TS - cur.On.Milliseconds()})
		case alarm.KindRTN:
			if n := len(spans); n > 0 && spans[n-1].end == 0 {
				spans[n-1].end = e.TS - cur.Off.Milliseconds()
			}
		}
	}
	if len(spans) < count {
		return Suggestion{}, false
	}
	before := peak(activationTimes(spans, cur), w)
	if before < count {
		return Suggestion{}, false
	}
	ons := ladderFrom(cur.On)
	offs := ladderFrom(cur.Off)
	type cand struct{ on, off time.Duration }
	var cands []cand
	for _, on := range ons {
		for _, off := range offs {
			cands = append(cands, cand{on, off})
		}
	}
	sort.SliceStable(cands, func(i, j int) bool {
		a, b := cands[i], cands[j]
		if a.on+a.off != b.on+b.off {
			return a.on+a.off < b.on+b.off
		}
		return a.on > b.on // the same total: an on-delay stops the spurious activation, an off-delay holds the alarm longer
	})
	for _, c := range cands {
		if c.on == cur.On && c.off == cur.Off {
			continue
		}
		ts := activationTimes(spans, Delays{c.on, c.off})
		if after := peak(ts, w); after < count {
			s := Suggestion{ID: id, Current: cur, On: Millis(c.on), Off: Millis(c.off), Before: before, After: after, Bounces: len(spans), Since: since}
			s.Why = why(s, window)
			return s, true
		}
	}
	return Suggestion{}, false
}

// ladderFrom is the current delay (kept as it is) and every ladder step
// longer than it.
func ladderFrom(cur time.Duration) []time.Duration {
	out := []time.Duration{cur}
	for _, d := range TuneLadder {
		if d > cur {
			out = append(out, d)
		}
	}
	return out
}

// activationTimes simulates the condition's spans through an on-delay and
// an off-delay: a span of at least `on` in the normal state activates after
// it, and the alarm returns after the condition has been false for `off`.
// The spans' own raw edges already have the current delays taken out, so
// replaying the current delays gives back the history as it was.
func activationTimes(spans []span, d Delays) []int64 {
	on, off := d.On.Milliseconds(), d.Off.Milliseconds()
	var out []int64
	active := false
	var lastEnd int64
	for _, s := range spans {
		length := int64(1 << 60)
		if s.end != 0 {
			length = s.end - s.start
		}
		if active {
			// a gap shorter than the off-delay never returned the alarm
			if s.start-lastEnd < off {
				lastEnd = s.end
				if s.end == 0 {
					lastEnd = 1 << 60
				}
				continue
			}
			active = false
		}
		if length >= on {
			out = append(out, s.start+on)
			active = true
			lastEnd = s.end
			if s.end == 0 {
				lastEnd = 1 << 60
			}
		}
	}
	return out
}

// peak is the most timestamps within one window (inclusive).
func peak(ts []int64, w int64) int {
	best, k := 0, 0
	for i := range ts {
		if k < i {
			k = i
		}
		for k+1 < len(ts) && ts[k+1]-ts[i] <= w {
			k++
		}
		best = max(best, k-i+1)
	}
	return best
}

func why(s Suggestion, window time.Duration) string {
	parts := ""
	if time.Duration(s.On) != s.Current.On {
		parts = fmt.Sprintf("on-delay %s", time.Duration(s.On))
	}
	if time.Duration(s.Off) != s.Current.Off {
		if parts != "" {
			parts += " and "
		}
		parts += fmt.Sprintf("off-delay %s", time.Duration(s.Off))
	}
	return fmt.Sprintf("%s: %d activations in %s would have been %d (of %d bounces examined; only longer delays are judged)",
		parts, s.Before, windowText(window), s.After, s.Bounces)
}

// windowText is "60 s" or "10 min", the way the alarm rows write a window.
func windowText(d time.Duration) string {
	if d >= time.Minute && d%time.Minute == 0 {
		return fmt.Sprintf("%d min", int(d/time.Minute))
	}
	return fmt.Sprintf("%d s", int(d/time.Second))
}
