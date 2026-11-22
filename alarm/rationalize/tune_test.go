package rationalize

import (
	"testing"
	"time"

	"github.com/joyautomation/nautilus/alarm"
)

// bounces makes n activate/return cycles for alarm "A": each condition is
// true for `hold`, then false for `gap`, as the journal records them with
// the delays `cur` (an activation lands `cur.On` after the raw rise, a
// return `cur.Off` after the raw fall).
func bounces(n int, hold, gap time.Duration, cur Delays) []alarm.Event {
	var ev []alarm.Event
	t := int64(1_000_000)
	for i := 0; i < n; i++ {
		ev = append(ev, alarm.Event{TS: t + cur.On.Milliseconds(), ID: "A", Kind: alarm.KindActive})
		t += hold.Milliseconds()
		ev = append(ev, alarm.Event{TS: t + cur.Off.Milliseconds(), ID: "A", Kind: alarm.KindRTN})
		t += gap.Milliseconds()
	}
	return ev
}

func TestSuggestAnOnDelayForShortBounces(t *testing.T) {
	// 7 bounces of 3 s with 4 s between: 7 activations in 60 s
	s, ok := Suggest(bounces(7, 3*time.Second, 4*time.Second, Delays{}), Delays{}, time.Minute, 3)
	if !ok {
		t.Fatal("no suggestion")
	}
	if time.Duration(s.On) != 5*time.Second || s.Off != 0 {
		t.Errorf("suggested on %s off %s, want the smallest ladder on-delay that filters 3 s bounces: 5s and no off-delay", s.On, s.Off)
	}
	if s.Before != 7 || s.After != 0 || s.Bounces != 7 {
		t.Errorf("before %d after %d of %d", s.Before, s.After, s.Bounces)
	}
}

func TestSuggestAnOffDelayWhenTheConditionHoldsBetweenShortDropOuts(t *testing.T) {
	// 10 s true, 3 s false: an on-delay would need 15 s; an off-delay of 5 s keeps one activation
	s, ok := Suggest(bounces(7, 10*time.Second, 3*time.Second, Delays{}), Delays{}, time.Minute, 3)
	if !ok {
		t.Fatal("no suggestion")
	}
	if s.On != 0 || time.Duration(s.Off) != 5*time.Second {
		t.Errorf("suggested on %s off %s, want off-delay 5s", s.On, s.Off)
	}
	if s.After != 1 {
		t.Errorf("after %d, want the 7 bounces to become 1", s.After)
	}
}

func TestSuggestOnlyLengthensAndTakesTheCurrentDelaysOutOfTheRawEdges(t *testing.T) {
	cur := Delays{On: 2 * time.Second}
	// activations land 2 s after the raw rise: 4 s raw bounces, 3 s gaps
	s, ok := Suggest(bounces(7, 4*time.Second, 3*time.Second, cur), cur, time.Minute, 3)
	if !ok {
		t.Fatal("no suggestion")
	}
	if time.Duration(s.On) != 5*time.Second {
		t.Errorf("on %s: 4 s raw bounces need more than 2 s, and 5 s is the next step above the current 2 s", s.On)
	}
	if time.Duration(s.On) <= cur.On {
		t.Error("a suggestion never shortens")
	}
}

func TestSuggestNothingWhenItDoesNotChatterOrNothingHelps(t *testing.T) {
	if _, ok := Suggest(bounces(2, 3*time.Second, 4*time.Second, Delays{}), Delays{}, time.Minute, 3); ok {
		t.Error("two bounces do not chatter")
	}
	// 100 s true, 1 s false: every delay on the ladder (max 60 s) still leaves
	// the alarm active through each 1 s drop-out? an off-delay of 1 s merges them: so this one helps
	if s, ok := Suggest(bounces(5, 100*time.Second, time.Second, Delays{}), Delays{}, 5*time.Minute, 3); !ok || time.Duration(s.Off) != 2*time.Second {
		t.Errorf("a 1 s drop-out is bridged by the next off-delay step above it: %+v %v", s, ok)
	}
	// long true, long false, fast cycle in a short window: 20 s true, 20 s false inside a 100 s window, count 3:
	// no on-delay under 20 s... 30 s on-delay filters them; so it helps. A bounce of 120 s true / 120 s false cannot be fixed by the ladder.
	if _, ok := Suggest(bounces(4, 120*time.Second, 120*time.Second, Delays{}), Delays{}, 10*time.Minute, 3); ok {
		t.Error("bounces longer than the whole ladder are not a delay's problem")
	}
}

func TestSuggestLeavesOutShelvedAndSuppressedSpansAndStartsAfterTheLastTuning(t *testing.T) {
	ev := bounces(7, 3*time.Second, 4*time.Second, Delays{})
	// everything is inside a shelve: no history to judge
	shelved := append([]alarm.Event{{TS: 1, ID: "A", Kind: alarm.KindShelve}}, ev...)
	if _, ok := Suggest(shelved, Delays{}, time.Minute, 3); ok {
		t.Error("activations while shelved are not judged")
	}
	// a tuned row after the bounces: nothing before it counts
	tuned := append(append([]alarm.Event{}, ev...), alarm.Event{TS: ev[len(ev)-1].TS + 1, ID: "A", Kind: KindTuned})
	if _, ok := Suggest(tuned, Delays{}, time.Minute, 3); ok {
		t.Error("history before a tuning was made under other delays")
	}
	// the same after the tuned row does count
	after := append([]alarm.Event{{TS: 1, ID: "A", Kind: KindTuned}}, ev...)
	if _, ok := Suggest(after, Delays{}, time.Minute, 3); !ok {
		t.Error("history after the tuning is judged")
	}
	// shelved, then unshelved: the later bounces count
	back := append([]alarm.Event{{TS: 1, ID: "A", Kind: alarm.KindShelve}, {TS: 2, ID: "A", Kind: alarm.KindUnshelve}}, ev...)
	if _, ok := Suggest(back, Delays{}, time.Minute, 3); !ok {
		t.Error("after an unshelve the history counts again")
	}
}

// The engine's on-delay is inclusive (a condition held exactly the delay
// qualifies), so 5 s bounces are not filtered by a 5 s on-delay.
func TestSuggestOnDelayIsInclusiveLikeTheEngine(t *testing.T) {
	s, ok := Suggest(bounces(7, 5*time.Second, 4*time.Second, Delays{}), Delays{}, time.Minute, 3)
	if !ok {
		t.Fatal("no suggestion")
	}
	if s.On != 0 || time.Duration(s.Off) != 5*time.Second {
		t.Errorf("on %s off %s: a 5 s on-delay would still activate on 5 s bounces; the 4 s drop-outs are bridged by a 5 s off-delay", s.On, s.Off)
	}
}
