package node

import (
	"time"

	"github.com/joyautomation/nautilus/quorum"
)

// The no-leader alarm (#1436). A pair with no leader runs no outputs and
// scans nothing, so its alarm engine never evaluates: none of the
// manifest's alarms is raised, and nobody is paged. This one alarm is the
// exception. A controller raises it from its own no-leader report
// (noteLeaderless), on an alarm engine of its own that does not wait for
// a scan (cmd/naut), and pages through it whatever its role.
//
// Paged once: the controller whose name sorts first raises it while it
// sees no leader. The other raises it too unless it holds fresh word,
// from its peer's hello answer, that the first raises it and its sends
// work. Anything short of that word (a first that is down, cut off, a
// spare, in its restart wait, on a build before this, or whose sends fail)
// and the second raises too: a split pages twice, never zero. Once a
// controller raises it, it keeps it until a leader is seen, so a link
// that comes and goes does not hand it back and forth, each hand-off a
// new page.
//
// Not raised: by a controller that is not commissioned (a pair being set
// up), by a spare, or by a controller cut off from a peer that leads (the
// peer has not answered it, and the witness refuses it for a promise
// of its own: the peer's). Raised for a fenced state or a refused lease
// or version: those pairs can't lead either. A rolling upgrade across a
// vote protocol version relies on the release rule (design §2: a version
// reads its predecessor's frames); until that holds, the new standby
// sees no leader and raises it.

// raiseGrace is how long the second controller waits, at the start of a
// no-leader spell, for the first's word before raising the alarm itself:
// a few hellos, asked at once (hellosAgain).
func raiseGrace(lease time.Duration) time.Duration { return 4 * helloRetry(lease) }

// LeaderlessAlarm reports whether this controller raises the no-leader
// alarm now, and the spell it belongs to: episode moves on at the start
// of each spell with no leader, so a page queued in one never leaves in
// the next. Lock-free: an alarm engine's condition and a dispatcher's
// gate read it.
func (e *Elector) LeaderlessAlarm() (raise bool, episode uint64) {
	return e.llRaise.Load(), e.llEpisode.Load()
}

// raisesWord is what this controller's hello answer says of the alarm:
// "1" while it raises it and its sends work (Options.LeaderlessWord),
// "" otherwise, as a build before it says.
func (e *Elector) raisesWord() string {
	if e.o.LeaderlessWord == nil || !e.llRaise.Load() || !e.o.LeaderlessWord() {
		return ""
	}
	return "1"
}

// heardRaise records the word of the peer's last hello answer: raises
// only from an answer that verified, false from any other (and from a
// hello with no answer). Takes mu.
func (e *Elector) heardRaise(raises bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.peerRaises = raises
	if raises {
		e.peerRaiseAt = e.now()
	}
}

// peerRaisesNow reports whether the peer's word that it raises the alarm
// stands: its last hello answer said so, and that was recent (cfgStale).
// A hello that finds no answer, or one that does not verify, withdraws the
// word at once (heardRaise), so a first that stops or is cut off loses it
// within a hello interval. The voter's answers are no measure: a second
// that does not campaign (released, unhealthy) asks the peer's voter
// nothing. Under mu.
func (e *Elector) peerRaisesNow(now quorum.Instant) bool {
	if !e.peerRaises {
		return false
	}
	return now.Sub(e.peerRaiseAt) < e.cfgStale() || mut("leaderless_alarm_stale_word")
}

// leaderOutOfReach reports whether the peer most likely leads beyond
// this controller's reach: it has not answered this controller for a
// while, and the witness refuses this controller's campaigns for a
// promise it holds (a term at or above the one asked), which only the
// peer can hold. A duel has the witness's refusal too, but there the
// peer answers. Under mu.
func (e *Elector) leaderOutOfReach(now quorum.Instant) bool {
	recent := wordFresh * e.o.Lease
	if e.answeredWithin(e.o.Peer, now, recent) && !mut("leaderless_alarm_reach_ignores_peer") {
		return false
	}
	w, ok := e.word(e.o.Witness)
	return ok && now.Sub(w.At) < recent && w.Counted && !w.Granted && w.Promised >= w.Term
}

// noteRaise moves the alarm at a look with a no-leader report standing;
// start says the report has just begun. Under mu.
func (e *Elector) noteRaise(now quorum.Instant, start bool) {
	if start && !mut("leaderless_alarm_one_episode") {
		e.llEpisode.Add(1)
	}
	if start {
		e.llStart, e.llRaising = now, false
	}
	raise := e.raiseNow(now)
	if raise {
		e.llRaising = true
	}
	e.llRaise.Store(raise)
	e.ll.Raises = raise
}

// raiseNow is noteRaise's verdict. Under mu.
func (e *Elector) raiseNow(now quorum.Instant) bool {
	if e.llRaising && !mut("leaderless_alarm_unlatched") {
		return true
	}
	if (e.boxPending || e.v.State().Incarnation == 0) && !mut("leaderless_alarm_uncommissioned") {
		return false // a pair being set up, not one that stopped
	}
	if e.leaderOutOfReach(now) {
		return false
	}
	if e.o.Self < e.o.Peer {
		return true
	}
	if mut("leaderless_alarm_second_never_defers") {
		return true
	}
	if e.peerRaisesNow(now) {
		return false
	}
	if now.Sub(e.llStart) < raiseGrace(e.o.Lease) && !mut("leaderless_alarm_no_grace") {
		e.hellosAgain() // the first's word, if it has one
		return false
	}
	return true
}

// endRaise ends the alarm: a leader is seen, or the report ended. Under
// mu.
func (e *Elector) endRaise() {
	e.llRaising = false
	e.llRaise.Store(false)
}
