package quorum

// ImageTermOK is the standby's term check on a state image of its pair
// (#710 S1b, design §4.1): whether an image stamped with the leader's
// fencing term may be applied and persisted here, now. It holds when all
// three do:
//
//  1. this voter sees peer holding a live lease (LeaderSeen): the image is
//     from the controller that leads, as far as this one knows;
//  2. term is above the last term this controller itself led (ownLed) and
//     at least the newest term it already took an image of (seen: its
//     shadow's, or the one its retain file records). Every new leadership
//     is a term above every earlier one (a majority granted each, and two
//     majorities share a voter whose promises only rise), so an image of
//     a term at or below one this controller led is from a leadership
//     before its own, and one below seen is older than what it holds;
//  3. term is at most the highest term peer has asked this voter at
//     (Voter.Asked): a leader's fencing term is the term it campaigned at,
//     and every renewal asks at that term or above (a re-term only moves
//     it up). A term the peer never asked at is not one it leads.
//
// It is a bound, not an equality: the image carries the fencing term, and
// the vote term this voter sees moves above it on a re-term (a renewal a
// voter refused once). why is "" when ok, else what failed.
func ImageTermOK(v *Voter, now Instant, peer NodeID, term, ownLed, seen uint64) (ok bool, why string) {
	from, until := v.LeaderSeen()
	if (from != peer || now >= until) && !mut("stream_leader_unseen") {
		return false, "this controller's voter sees no live lease of the peer"
	}
	if term <= ownLed && !mut("stream_floor_unchecked") {
		return false, "the image's term is not above the last term this controller led"
	}
	if term < seen && !mut("stream_floor_unchecked") {
		return false, "the image's term is below one this controller already took an image of"
	}
	if term > v.Asked(peer) && !mut("stream_term_unbounded") {
		return false, "the image's term is above every term the peer asked this voter at"
	}
	return true, ""
}
