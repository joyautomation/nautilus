package quorum

import "slices"

// The hot takeover's check against the voters (#710 S2c-1): a standby that
// wins a lease may go on from its image of the old leader's state only if
// no leadership came after that image, and the image is as fresh as the
// old leader last said its standby was. The voters' epoch records and
// marks (epochs.go) say so; this reads them from the replies of the round
// that won, never from an earlier round, whose refusals can predate the
// leadership in between.

// WonReply is one voter's reply in a winning campaign round, as far as a
// hot takeover reads it.
type WonReply struct {
	From        NodeID
	RecordsFrom uint64
	Recs        Recs
}

// HotCheck is what a candidate that just won a lease knows for the check:
// its name and its peer's, the epoch it now leads in, its winning round's
// replies, and the epochs this boot campaigned in and led in.
type HotCheck struct {
	Self, Peer NodeID
	Epoch      uint64
	Won        []WonReply
	Campaigns  []uint64
	LedBefore  uint64
}

// maxCampaigns bounds the campaign terms a candidate keeps (HotCheck).
const maxCampaigns = 64

// HotCheck is the check's input for the lease this candidate holds now
// (zero when it leads none).
func (c *Candidate) HotCheck() HotCheck {
	if !c.leading {
		return HotCheck{}
	}
	return HotCheck{Self: c.cfg.Self, Peer: c.cfg.Peer, Epoch: c.epoch, Won: slices.Clone(c.won),
		Campaigns: slices.Clone(c.campaigns), LedBefore: c.ledBefore}
}

// wonFrom is the winning round's replies, in voter order.
func (c *Candidate) wonFrom(rd *round) []WonReply {
	src := []*round{rd}
	if mut("mark_from_losing_round") {
		src = c.rounds // every round still kept, earlier campaigns' too
	}
	by := map[NodeID]WonReply{}
	for _, x := range src {
		for from, w := range x.recs {
			by[from] = w
		}
	}
	var out []WonReply
	for _, w := range by {
		out = append(out, w)
	}
	slices.SortFunc(out, func(a, b WonReply) int {
		switch {
		case a.From < b.From:
			return -1
		case a.From > b.From:
			return 1
		}
		return 0
	})
	return out
}

// Cold reasons (HotCheck.Verdict). Each names what the voters said.
const (
	ColdRecordsUnknown = "records-unknown"  // fewer than two of the winning replies know every grant since the image's epoch
	ColdLaterLeader    = "later-leadership" // a voter granted the old leader an epoch after the image's
	ColdImageUnseen    = "image-epoch-unrecorded"
	ColdOwnLeadership  = "own-leadership" // this controller led after the image's epoch
	ColdNoMark         = "no-mark"        // no voter knows how far the standby was in sync
	ColdMarkOtherEpoch = "mark-other-epoch"
	ColdNotCurrent     = "standby-not-current" // the old leader's last word: its standby was behind
	ColdBehindMark     = "behind-mark"         // the image is older than the one the standby acknowledged
	ColdNotLeading     = "not-leading"         // the check ran with no lease held
)

// Verdict says whether a takeover may go on hot from an image of the
// peer's state at (imgEpoch, imgSeq), and if not, why (a Cold reason).
//
//   - The winning replies whose records cover every grant above the
//     image's epoch (Covers) must be a majority, and at least two of them
//     must hold a mark of the peer: a voter that lost
//     its state (a rebuilt witness) may be the only one that saw a
//     leadership after the image.
//   - Among them, the highest epoch granted to the peer must be the
//     image's: any above it is a later leadership the image predates.
//   - This boot led in no epoch after the image's but the current one, and
//     no reply records one for this name that this boot did not campaign
//     in (a previous boot's leadership).
//   - The newest mark (by epoch, then sequence) the peer's renewals left
//     at those voters must be of the image's epoch and current, and the
//     image at or past it. Of two marks at one (epoch, sequence), the one
//     not current wins: for one acknowledgement, current only ever turns
//     false (the standby stalled), so it is the later word.
func (h HotCheck) Verdict(imgEpoch, imgSeq uint64) (hot bool, why string) {
	// Coverage decides only who counts toward the majority. Evidence
	// against hot (a later epoch, a mark not current) counts from every
	// winning reply, covering or not (Fable, #1345 S2).
	covering, marked := 0, 0
	var peerMax uint64
	var mark Mark
	for _, w := range h.Won {
		cov := Covers(w.RecordsFrom, imgEpoch) || mut("unknown_counts_toward_majority")
		if cov {
			covering++
		}
		if r, ok := w.Recs.Of(h.Peer); ok && (cov || !mut("evidence_from_covering_only")) {
			peerMax = max(peerMax, r.Epoch)
			if m := r.Mark; m.Known {
				if cov {
					marked++
				}
				if newerMark(m, mark) {
					mark = m
				}
			}
		}
		if r, ok := w.Recs.Of(h.Self); ok && r.Epoch > imgEpoch && r.Epoch != h.Epoch &&
			!slices.Contains(h.Campaigns, r.Epoch) && !mut("own_record_ignored") {
			return false, ColdOwnLeadership
		}
	}
	if covering < 2 {
		return false, ColdRecordsUnknown
	}
	if h.LedBefore > imgEpoch && !mut("own_record_ignored") {
		return false, ColdOwnLeadership
	}
	switch {
	case peerMax > imgEpoch && !mut("later_leader_ignored"):
		return false, ColdLaterLeader
	case peerMax < imgEpoch && !mut("later_leader_ignored"):
		return false, ColdImageUnseen
	case marked < 2 && !mut("one_mark_enough"):
		// A voter that restarted has no mark: one other voter's word may
		// be stale (Fable, #1345 M2). The old leader's last lease rested
		// on a renewal a majority granted, so two covering voters with a
		// mark always include one that heard it.
		return false, ColdNoMark
	case !mark.Known:
		return false, ColdNoMark
	case mark.Term != imgEpoch:
		return false, ColdMarkOtherEpoch
	case !mark.Current:
		return false, ColdNotCurrent
	case imgSeq < mark.Scan:
		return false, ColdBehindMark
	}
	return true, ""
}

// newerMark reports whether m is a later word than cur: a higher (epoch,
// sequence), or the same one no longer current.
func newerMark(m, cur Mark) bool {
	switch {
	case !cur.Known:
		return true
	case m.Term != cur.Term:
		return m.Term > cur.Term
	case m.Scan != cur.Scan:
		return m.Scan > cur.Scan
	}
	return cur.Current && !m.Current && !mut("mark_tie_current_wins")
}
