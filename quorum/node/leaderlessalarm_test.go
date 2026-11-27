package node

import (
	"context"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/quorum"
)

// The no-leader alarm (#1436): who raises it, paged once. All on the fake
// clock.

// leaderless moves l past its restart hold and leaderlessAfter leases, and
// looks until its no-leader report stands.
func (l *lelector) leaderless(t *testing.T) {
	t.Helper()
	l.pastHold(leaderlessAfter)
	l.clk.add(l.o.Lease / 8)
	waitFor(t, 2*time.Second, func() bool { l.e.look(); return l.e.Status().Leaderless != nil })
}

// raises looks once and reports whether l raises the alarm.
func (l *lelector) raises() bool {
	l.e.look()
	r, _ := l.e.LeaderlessAlarm()
	return r
}

// peerAnswers records an answer from the peer's voter now, as a probe's
// would.
func (l *lelector) peerAnswers() {
	l.e.mu.Lock()
	if l.e.answers == nil {
		l.e.answers = map[quorum.NodeID]quorum.Instant{}
	}
	l.e.answers[l.o.Peer] = l.e.now()
	l.e.mu.Unlock()
}

// The first-named controller raises the alarm while it sees no leader,
// and stops once a leader is seen; each spell with no leader is its own
// episode, so a page queued in one never leaves in the next
// (leaderless_alarm_one_episode turns this red).
func TestLeaderlessAlarmFirstRaises(t *testing.T) {
	l := newLelector(t, wayCut, wayCut, true, nil)
	l.clk.add(leaderlessAfter * l.o.Lease / 2)
	if l.raises() {
		t.Fatal("raised before the no-leader report")
	}
	l.leaderless(t)
	if !l.raises() {
		t.Fatalf("a, first, sees no leader and raises nothing: %+v", l.e.Status().Leaderless)
	}
	if ll := l.e.Status().Leaderless; ll == nil || !ll.Raises {
		t.Fatalf("status: %+v, want Raises", ll)
	}
	_, ep1 := l.e.LeaderlessAlarm()
	// b leads: a's voter grants it.
	l.e.running.Store(true)
	l.e.mu.Lock()
	term := l.e.v.State().Promised + 1
	l.e.mu.Unlock()
	if code, rep := vote(t, l.e.Handler(), l.o.Lease, "", quorum.Request{From: "b", Term: term, Nonce: 7, ID: 1}); !rep.Granted {
		t.Fatalf("b's campaign: %d %+v", code, rep)
	}
	if l.raises() {
		t.Fatal("still raised with b leading")
	}
	// b's lease lapses and none follows: a new spell.
	l.clk.add(2 * l.o.Lease)
	l.e.look()
	l.clk.add(leaderlessAfter*l.o.Lease + l.o.Lease/8)
	waitFor(t, 2*time.Second, func() bool { return l.raises() })
	if _, ep2 := l.e.LeaderlessAlarm(); ep2 == ep1 {
		t.Fatalf("a second spell with no leader kept episode %d", ep1)
	}
}

// The second-named controller pages only without the first's word: it
// waits a short grace for it (asking a hello at once), keeps quiet while
// the word is fresh and the first still answers, and raises when the word
// goes stale, when a hello withdraws it (no answer, or a's sends fail),
// or when no word comes. Once
// raised it keeps the alarm until a leader is seen, even if the word comes
// back. leaderless_alarm_no_grace, _second_never_defers, _stale_word and
// _unlatched each turn a case red.
func TestLeaderlessAlarmSecondPagesWithoutTheFirstsWord(t *testing.T) {
	second := func(t *testing.T) *lelector {
		l := newLelectorAs(t, "b", "a", wayCut, wayHeld, true, nil, nil)
		l.leaderless(t)
		return l
	}
	t.Run("no word: raises after the grace", func(t *testing.T) {
		l := second(t)
		if l.raises() {
			t.Fatal("b raised at once, before the first's word could come")
		}
		select {
		case <-l.e.helloNow:
		default:
			t.Fatal("b did not ask its peer for a hello in the grace")
		}
		l.clk.add(raiseGrace(l.o.Lease))
		if !l.raises() {
			t.Fatal("b raised nothing with no word from a past the grace")
		}
	})
	t.Run("fresh word: quiet, then stale: raises and keeps it", func(t *testing.T) {
		l := second(t)
		l.e.heardRaise(true)
		l.clk.add(raiseGrace(l.o.Lease))
		if l.raises() {
			t.Fatal("b raised with a's fresh word that a pages it")
		}
		l.clk.add(l.e.cfgStale())
		if !l.raises() {
			t.Fatal("b deferred to a's word long gone stale")
		}
		l.e.heardRaise(true)
		if !l.raises() {
			t.Fatal("b dropped the alarm when a's word came back: a flapping link pages again at each hand-off")
		}
	})
	t.Run("the word withdrawn at a hello: raises", func(t *testing.T) {
		l := second(t)
		l.e.heardRaise(true)
		l.e.heardRaise(false) // a hello with no answer, or a's sends fail
		l.clk.add(raiseGrace(l.o.Lease))
		if !l.raises() {
			t.Fatal("b kept quiet on a word its peer withdrew")
		}
	})
}

// Not raised by a controller being set up (uncommissioned): a pair that
// never led has not stopped (leaderless_alarm_uncommissioned turns this
// red). A spare reports nothing at all (TestLeaderlessCause).
func TestLeaderlessAlarmNotWhileUncommissioned(t *testing.T) {
	l := newLelector(t, wayCut, wayCut, false, nil)
	l.leaderless(t)
	for range 5 {
		if l.raises() {
			t.Fatal("an uncommissioned controller raised the alarm")
		}
	}
}

// A controller cut off from a peer that leads (no answer from the peer,
// the witness refusing for a promise it holds) raises nothing; in a duel
// the witness refuses too, but the peer answers, and the alarm is raised
// (leaderless_alarm_reach_ignores_peer turns the duel red).
func TestLeaderlessAlarmNotWhenThePeerLeadsOutOfReach(t *testing.T) {
	witnessRefused := func(l *lelector) func() bool {
		return func() bool {
			l.e.look()
			l.e.mu.Lock()
			defer l.e.mu.Unlock()
			w, ok := l.e.word(l.e.o.Witness)
			return ok && l.e.now().Sub(w.At) < l.o.Lease && w.Counted && !w.Granted && w.Promised >= w.Term
		}
	}
	// The witness's word comes from the campaigns before the report
	// starts, as it does on a real clock.
	refusedThenLeaderless := func(t *testing.T, l *lelector) {
		l.pastHold(leaderlessAfter - 1)
		waitFor(t, 2*time.Second, witnessRefused(l))
		l.clk.add(l.o.Lease + l.o.Lease/8)
		l.e.look()
		if l.e.Status().Leaderless == nil {
			t.Fatal("no no-leader report")
		}
	}
	t.Run("peer out of reach", func(t *testing.T) {
		l := newLelector(t, wayCut, wayRefuse, true, nil)
		refusedThenLeaderless(t, l)
		if l.raises() {
			t.Fatal("a raised the alarm cut off from a peer the witness says leads")
		}
	})
	t.Run("duel", func(t *testing.T) {
		l := newLelector(t, wayRefuse, wayRefuse, true, nil)
		refusedThenLeaderless(t, l)
		l.peerAnswers()
		if !l.raises() {
			t.Fatal("a raised nothing in a duel: the peer answers, so it does not lead out of reach")
		}
	})
}

// The first's word rides its hello answer and reaches the second only
// from a verified answer: "1" while it raises the alarm and its sends
// work, nothing otherwise, and nothing from a build before it (so the
// second pages too). A word changed on the way fails the answer
// (leaderless_word_unbound turns that red).
func TestLeaderlessWordRidesTheHello(t *testing.T) {
	sends := true
	a, err := New(func() Options {
		o := testOptions(t, "a", "b")
		o.LeaderlessWord = func() bool { return sends }
		return o
	}())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	b := newLelectorAs(t, "b", "a", wayCut, wayCut, true, nil, nil)
	b.voters.mu.Lock()
	b.voters.hello = a.Handler()
	b.voters.mu.Unlock()
	word := func() bool {
		b.e.checkToken(context.Background())
		b.e.mu.Lock()
		defer b.e.mu.Unlock()
		return b.e.peerRaises
	}
	if word() {
		t.Fatal("a raises nothing, and b heard a word")
	}
	a.llRaise.Store(true)
	if !word() {
		t.Fatal("a raises the alarm and its sends work, and b heard no word")
	}
	sends = false
	if word() {
		t.Fatal("a's sends fail, and b heard a word")
	}
	sends = true
	a.o.LeaderlessWord = nil // no alarms: section, or a build before it
	if word() {
		t.Fatal("a pages nobody, and b heard a word")
	}

	// The word is bound by the answer's MAC.
	const c = "challenge-0123456789"
	sent := peerInfo{Config: "c", Nonce: 7, Raises: "1"}
	h := helloAnswer{Node: "a", Config: sent.Config, Nonce: "7", Raises: "1", InfoMAC: infoMAC(testToken, c, "b", "a", sent)}
	if pi, ok := b.e.answerVerified(c, h); !ok || pi.Raises != "1" {
		t.Fatalf("the answer as sent: verified %v, word %q", ok, pi.Raises)
	}
	old := peerInfo{Config: "c", Nonce: 7}
	h = helloAnswer{Node: "a", Config: "c", Nonce: "7", InfoMAC: infoMAC(testToken, c, "b", "a", old)}
	h.Raises = "1"
	if _, ok := b.e.answerVerified(c, h); ok {
		t.Fatal("a word added on the way still verifies: anyone between the controllers could silence the second's page")
	}
	h.Raises = ""
	if pi, ok := b.e.answerVerified(c, h); !ok || pi.Raises != "" {
		t.Fatalf("an answer without the word (a build before it): verified %v, word %q", ok, pi.Raises)
	}
}
