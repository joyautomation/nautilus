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
// witness's promise to the peer has run, renewed without a gap, for a
// few leases). Raised for a fenced state or a refused lease
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

// peerLeadAfter is how long the witness's promise to the peer must have
// run without a gap before this controller takes it that the peer leads:
// a leader's renewals keep it running, while a campaign that does not
// win holds it about a lease and three quarters at most (its re-asks),
// then lets it lapse for its back-off.
const peerLeadAfter = 3

// noteWitnessRelay records the running promise a witness reply carries
// (its Relay, on probes and campaign replies alike). Under mu.
func (e *Elector) noteWitnessRelay(now quorum.Instant, p quorum.Relay) {
	if p.To != e.o.Peer || p.Left <= 0 {
		e.witPeer = false
		return
	}
	if !e.witPeer || (now > e.witPeerUntil && !mut("leaderless_alarm_relay_gap_unseen")) {
		e.witPeerFrom = now // a new promise, not one renewed without a gap
	}
	e.witPeer, e.witPeerUntil = true, max(e.witPeerUntil, now.Add(min(p.Left, e.o.Lease)))
}

// leaderOutOfReach reports whether the peer leads beyond this
// controller's reach: the witness's promise to the peer has run without
// a gap for peerLeadAfter leases and runs now. Only renewals do that, so
// the peer holds the lease (this controller misses its renewals: a cut
// link, or a one-way one). A duel never does, so neither side of one is
// silenced. Under mu.
func (e *Elector) leaderOutOfReach(now quorum.Instant) bool {
	if !e.witPeer || now >= e.witPeerUntil {
		return false
	}
	if mut("leaderless_alarm_reach_any_relay") {
		return true
	}
	return now.Sub(e.witPeerFrom) >= peerLeadAfter*e.o.Lease
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
