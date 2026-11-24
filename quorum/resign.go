package quorum

import "time"

// A planned switchover's RESIGN (#710 d1, vote protocol 9). Without it a
// planned handover is no faster than a failover: the standby waits out
// the promises the voters gave the old leader. With it the leader gives
// its leadership up, and each voter that takes the RESIGN drops its
// promise at once, so the standby campaigns and wins without waiting.
//
//   - The leader first stops leading, in one step with its own voter's
//     promise to it (ResignAt), and only then does RESIGN leave: the
//     dropped promises never back a lease anyone still writes under.
//   - A voter takes it only from the boot it promised (GrantedTo and the
//     grant's boot nonce), for the epoch it knows that boot leads in (its
//     record of the boot's campaign, or the epoch of the boot's last
//     renewal it granted: ledEpoch), while the promise runs. Promised is
//     not compared: a re-term moves it within one leadership.
//   - It then drops the promise, the renewals it saw from that boot
//     (LeaderSeen), and the promises it heard relayed to that boot; for a
//     while it ignores new relays of those (a relay made before another
//     voter took the RESIGN), and for good it refuses renewals of that
//     leadership (a renewal in flight when the RESIGN left).
//   - The RESIGN's mark is the standby's acknowledgement of the final
//     image: the voter keeps it as the leader's mark only when this
//     RESIGN dropped the promise, so a hot takeover is judged on it.
//
// A RESIGN lost on the way changes nothing: the standby waits the lease
// out, as before. Nothing of it is written to disk: a restarted voter
// grants nothing until every promise it may have made has run out.

// resignRec is a leadership that resigned at this voter: the boot and its
// epoch, and until when relays of promises to that boot are ignored.
type resignRec struct {
	nonce, epoch uint64
	quiet        Instant
}

// resignQuiet is how long after taking a RESIGN a voter ignores relayed
// promises to the resigned boot. A promise made before the RESIGN runs at
// most a lease after it, and its relay left its sender at most lease/4
// before it lands: so a lease and a quarter covers every stale relay. The
// quiet ends sooner when this voter grants that boot's campaign again
// (grantedAgain): a switchover back makes the old leader lead within the
// quiet, and the promises relayed to it are then new ones.
func resignQuiet(lease time.Duration) time.Duration { return lease + lease/4 }

// ResignHold is how long a resigned leader holds off campaigning: two
// leases, as a lapsed one (expire). If the standby never wins, the old
// leader leads again, cold, after it.
func ResignHold(lease time.Duration) time.Duration { return 2 * lease }

// handleResign answers a RESIGN (Request.Resign). refuse is handle's
// refusal of r. Called from handle after r's boot is noted.
func (v *Voter) handleResign(now Instant, r Request, refuse []Reply) (*Persist, []Reply) {
	if r.From == v.self || r.Epoch == 0 || r.Nonce == 0 {
		return nil, refuse // its own candidate resigns in process (ResignAt)
	}
	if x, ok := v.resigned[r.From]; ok && x.nonce == r.Nonce && x.epoch == r.Epoch {
		// A duplicate: answered as taken, so its sender stops resending,
		// but nothing is done again (ResignTook stays false): an old copy
		// must not make this controller campaign again (Fable, #1355).
		return nil, []Reply{v.reply(now, r, true)}
	}
	if !v.resignMatches(now, r) {
		return nil, refuse
	}
	v.resignTook = true
	v.promiseUntil = 0
	if v.renewalFrom == r.From && !mut("resign_keeps_renewal_seen") {
		v.renewalFrom, v.renewalUntil = "", 0
	}
	if !mut("resign_keeps_heard") {
		v.dropResigned(now, r.From, r.Nonce, r.Epoch)
	} else {
		v.noteResigned(now, r.From, r.Nonce, r.Epoch)
	}
	v.keepFinalMark(r)
	return nil, []Reply{v.reply(now, r, true)}
}

// resignMatches reports whether r resigns the leadership this voter's
// running promise backs: the same name and boot, and that leadership's
// epoch as this voter knows it (ledEpoch).
func (v *Voter) resignMatches(now Instant, r Request) bool {
	if (now >= v.promiseUntil && !mut("resign_after_promise")) || v.st.GrantedTo != r.From || v.grantNonce != r.Nonce {
		return false
	}
	if seq := v.leadSeqOf(r.From, r.Nonce); seq > r.Seq && !mut("resign_ignores_seq") {
		// The boot sent a campaign or a renewal after this RESIGN: it is
		// of an earlier leadership, though the epoch this voter knows may
		// not show it yet (a later leadership's first renewal, still
		// waiting on its write, has not set the mark).
		return false
	}
	if mut("resign_ignores_epoch") {
		return true
	}
	e := v.ledEpoch(r.From, r.Nonce)
	return e != 0 && e == r.Epoch
}

// ledEpoch is the epoch of the latest leadership this voter knows the
// boot (name, nonce) held: its record of that boot's campaign grant, or
// the epoch its last granted renewal from that boot carried, whichever
// is higher (a boot's epochs only rise). Both, since a voter that
// started after the leader's campaign (a standby restarted under a live
// leader, an upgrade) has granted its renewals but holds no record of
// its campaign. A RESIGN of an earlier leadership of the same boot that
// this voter has seen followed by a later one is refused; one whose later
// leadership this voter has heard nothing of may be taken, which is safe
// (no promise of its rests on that later leadership) and costs the
// standby a needless campaign. The Seq gate carries the safety. 0 when
// it knows none.
func (v *Voter) ledEpoch(name NodeID, nonce uint64) uint64 {
	var e uint64
	for _, rec := range v.st.Epochs {
		if rec.Name == name && rec.Nonce == nonce {
			e = rec.Epoch
		}
	}
	if from, ok := v.syncMarkFrom[name]; ok && from[0] == nonce && !mut("resign_record_only") {
		e = max(e, v.syncMarks[name].Term)
	}
	return e
}

// keepFinalMark keeps a RESIGN's mark as its sender's, as setMark keeps a
// renewal's: its Seq is above every renewal of that boot, so a renewal
// delayed past it never brings back an older word.
func (v *Voter) keepFinalMark(r Request) {
	if v.syncMarks == nil {
		v.syncMarks, v.syncMarkFrom = map[NodeID]Mark{}, map[NodeID][2]uint64{}
	}
	if mut("resign_mark_dropped") {
		return
	}
	v.syncMarks[r.From] = markOf(r)
	v.syncMarkFrom[r.From] = [2]uint64{r.Nonce, r.Seq}
}

// ResignTook reports, once, whether the last request Handle answered was
// a RESIGN this voter took afresh (it dropped a running promise), not a
// duplicate's or a refusal's answer. Only then does a standby whose voter
// it is campaign at once (Elector.serveVote).
func (v *Voter) ResignTook() bool {
	t := v.resignTook || (mut("resign_dup_acts") && v.resignAnswered)
	v.resignTook, v.resignAnswered = false, false
	return t
}

// resignedRenewal reports whether r renews a leadership that resigned
// here: refused, and not seen as a live leader's (LeaderSeen).
func (v *Voter) resignedRenewal(r Request) bool {
	if !r.Renewal || mut("resign_renewal_granted") {
		return false
	}
	x, ok := v.resigned[r.From]
	return ok && x.nonce == r.Nonce && x.epoch == r.Epoch
}

// resignedRelay reports whether a relayed promise to (to, nonce) is one
// made before that boot resigned here, inside the quiet: not heard.
func (v *Voter) resignedRelay(now Instant, to NodeID, nonce uint64) bool {
	if mut("resign_relay_heard") {
		return false
	}
	x, ok := v.resigned[to]
	return ok && x.nonce == nonce && now < x.quiet
}

// Resign gives up this candidate's leadership at now, for a planned
// switchover: it stops leading, drops every open round (a late grant then
// matches nothing; and a candidate that does not lead takes no lease in
// its back-off), and holds off campaigning for hold. It returns the
// RESIGN for the other voters, its Seq above every renewal sent; nil when
// it does not lead. Call it through ResignAt.
func (c *Candidate) resign(now Instant, hold time.Duration) *Request {
	c.expire(now)
	if !c.Leading(now) {
		return nil
	}
	c.drainKeep = c.leaseEnd // until the drain is proven (ResignDrained)
	c.leading, c.leaseEnd, c.reterm = false, now, false
	if !mut("resign_keeps_rounds") {
		// A late grant of a renewal sent before the resign matches no round,
		// so it can never re-lead this boot on promises the voters dropped.
		// The hold refuses it too, but EndResignHold can end the hold early.
		c.rounds = c.rounds[:0]
	}
	c.pending = nil
	if !mut("resign_no_hold") {
		c.backoffUntil = max(c.backoffUntil, now.Add(hold))
		c.resignHold = c.backoffUntil
	}
	c.nextID++
	c.seq++
	return &Request{From: c.cfg.Self, Term: c.term, Nonce: c.cfg.Nonce, ID: c.nextID, Seq: c.seq,
		Gen: c.gen(), Fences: c.fences(), Uncommissioned: c.commissioned != nil && !c.commissioned(),
		Epoch: c.epoch, MarkSeq: c.markSeq, MarkCurrent: c.markCurrent, Resign: true}
}

// ResignAt resigns c's leadership: c stops leading (its gate shuts) and
// drops its open rounds, and holds off campaigning. Its own voter v KEEPS
// its promise to c, and c does not yield it, until ResignDrained: sends
// the lease let start may still be on the wire (#710 d1b), and with that
// promise freed the standby could lead beside them (its own voter and
// this one, when its voter heard none of c's renewals). The mark is the
// standby's acknowledgement of the final image (seq, current). It returns
// the RESIGN to send to the other voters once the drain is proven, or nil
// when c does not lead. Without ResignDrained the promise runs out where
// the lease would have: a failover.
func ResignAt(now Instant, v *Voter, c *Candidate, seq uint64, current bool) *Request {
	if v == nil || c == nil || c.cfg.Self != v.self {
		return nil
	}
	if !c.Leading(now) {
		return nil
	}
	c.SetMark(seq, current)
	r := c.resign(now, ResignHold(c.cfg.Lease))
	if r == nil {
		return nil
	}
	if mut("resign_frees_own_before_drain") {
		ResignDrained(now, v, c, r)
	}
	return r
}

// ResignDrained is the second half of a resign, once the outputs' drain is
// proven and before the RESIGN r leaves: c's own voter v drops its
// promise to c, and the promises it heard relayed to c's boot, as the
// RESIGN makes every voter do, in one call, as YieldTo does: nothing runs
// in between, so the dropped promise never backs a lease.
//
// Only while the lease the resign cut short would still run (drainKeep),
// with c not leading or campaigning: inside it the resign hold keeps c
// from campaigning, so the promise v holds is still the resigned one. A
// call later than that (the resigner stalled after its drain) could
// otherwise free a promise that backs c's next campaign or leadership.
// It reports whether it did; false: the RESIGN must not leave, and the
// lease runs out as in a failover.
func ResignDrained(now Instant, v *Voter, c *Candidate, r *Request) bool {
	if v == nil || c == nil || r == nil || c.cfg.Self != v.self {
		return false
	}
	if !(c.drainKeep != 0 && now < c.drainKeep && !c.leading && c.pending == nil) && !mut("resign_drained_unchecked") {
		return false
	}
	c.drainKeep = 0
	if v.st.GrantedTo == v.self {
		// The standby's higher campaign would make it yield too (YieldTo):
		// dropped here, it never refuses that campaign in between.
		v.promiseUntil = 0
	}
	if !mut("resign_own_keeps_heard") {
		// Its own voter is one the RESIGN reaches too: the promises it
		// heard relayed to this boot (the standby's voter granted its
		// renewals) would refuse the standby's campaign by rule (b) of
		// heardRefuses for up to a lease, and stale relays of them are
		// ignored for the quiet, as at every other voter.
		v.dropResigned(now, v.self, r.Nonce, r.Epoch)
	}
	return true
}

// EndResignHold ends the hold a resign started, at now, so this candidate
// may campaign as soon as nobody leads: its peer handed the lead back
// (#710 S2c-3), and the hold was only there not to fight a standby that
// takes over. A back-off of any other cause (a lapse after the resign)
// stays. It reports whether a hold was ended.
func (c *Candidate) EndResignHold(now Instant) bool {
	if now >= c.resignHold || c.backoffUntil != c.resignHold {
		return false
	}
	c.backoffUntil, c.resignHold = now, 0
	return true
}

// LookNow moves this candidate's next look to now: its own voter just
// took its peer's RESIGN, so a campaign now wins, where the look grid
// would wait up to lease/4 (#710 d1).
func (c *Candidate) LookNow(now Instant) {
	if now < c.nextLook {
		c.nextLook = now
	}
}

// dropResigned drops the promises this voter heard relayed to the boot
// (name, nonce) that resigned epoch, and notes the resign (noteResigned).
func (v *Voter) dropResigned(now Instant, name NodeID, nonce, epoch uint64) {
	keep := v.heard[:0]
	for _, x := range v.heard {
		if x.to == name && x.nonce == nonce {
			continue
		}
		keep = append(keep, x)
	}
	v.heard = keep
	v.noteResigned(now, name, nonce, epoch)
}

// noteResigned notes that the boot (name, nonce) resigned epoch here: its
// renewals of that epoch are refused, and relays of promises to it are
// ignored for the quiet.
func (v *Voter) noteResigned(now Instant, name NodeID, nonce, epoch uint64) {
	if v.resigned == nil {
		v.resigned = map[NodeID]resignRec{}
	}
	v.resigned[name] = resignRec{nonce: nonce, epoch: epoch, quiet: now.Add(resignQuiet(v.lease))}
}

// noteLeadSeq notes r's Seq when it is a campaign or a renewal (not a
// probe, not a RESIGN), before anything decides on it, per boot of its
// name: a RESIGN that arrives after a later request of the same boot is
// of an earlier leadership (resignMatches). Another boot of the name (a
// restart, a clone) keeps its own slot; a name keeps its last few boots.
func (v *Voter) noteLeadSeq(r Request) {
	if r.Term == 0 || r.Nonce == 0 {
		return
	}
	if v.leadSeq == nil {
		v.leadSeq = map[NodeID][]bootSeq{}
	}
	// Kept in the order last heard, so a full list drops the boot heard
	// longest ago, never a live one (Fable re-check, #1355).
	bs := v.leadSeq[r.From]
	seq := r.Seq
	for i := range bs {
		if bs[i].nonce == r.Nonce {
			seq = max(seq, bs[i].seq)
			bs = append(bs[:i:i], bs[i+1:]...)
			break
		}
	}
	if len(bs) >= maxLeadSeqBoots {
		bs = bs[1:]
	}
	v.leadSeq[r.From] = append(bs, bootSeq{nonce: r.Nonce, seq: seq})
}

// maxLeadSeqBoots bounds the boots per name noteLeadSeq keeps: a name's
// live boots are one, two with a clone; older ones only age out.
const maxLeadSeqBoots = 4

// bootSeq is a boot's highest campaign or renewal Seq heard.
type bootSeq struct{ nonce, seq uint64 }

// leadSeqOf is the highest campaign or renewal Seq heard from the boot
// (name, nonce), 0 when none.
func (v *Voter) leadSeqOf(name NodeID, nonce uint64) uint64 {
	for _, b := range v.leadSeq[name] {
		if b.nonce == nonce {
			return b.seq
		}
	}
	return 0
}

// grantedAgain ends the relay quiet of a resigned boot this voter grants
// again, for a later leadership than the one it resigned (epoch: the
// request's own, 0 for a campaign, whose term is above it anyway)
// (resignQuiet).
func (v *Voter) grantedAgain(name NodeID, nonce, epoch uint64) {
	if x, ok := v.resigned[name]; ok && x.nonce == nonce && (epoch == 0 || epoch > x.epoch) && !mut("resign_quiet_outlives_grant") {
		x.quiet = 0
		v.resigned[name] = x
	}
}
