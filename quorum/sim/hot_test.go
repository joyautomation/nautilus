package sim

import (
	"testing"
	"time"

	"github.com/joyautomation/nautilus/quorum"
)

// The hot takeover's check against the voters (#710 S2c-1). A standby's
// image here is the epoch of the last leader renewal its voter heard
// (World.gate); the hot oracle (checkHot) fails any hot verdict that
// skips an epoch someone led in between.

// hotOf is node n's takeovers that judged an image.
func hotOf(rec *Record, n Addr) []HotTakeover {
	var out []HotTakeover
	for _, h := range rec.Takeovers {
		if h.Node == n {
			out = append(out, h)
		}
	}
	return out
}

// A plain kill: B followed A's renewals to the end, so the voters' records
// and marks let it go on hot. grant_keeps_record turns it red (cold: no
// voter vouches for the image's epoch).
func TestTargetedHotAfterAKill(t *testing.T) {
	rec := Run(Options{Seed: 3, Delay: ms, Fsync: ms, Until: 30 * sec,
		Start:  [3]time.Duration{0, 4 * sec, 0},
		Faults: []Fault{{Kind: Crash, At: 15 * sec, Node: A}}})
	clean(t, rec, Checks{MaxTakeovers: -1})
	hs := hotOf(rec, B)
	if len(hs) != 1 || !hs[0].Hot {
		t.Fatalf("B's takeover after A's kill: %+v; want one, hot", hs)
	}
}

// Ruling B: A leads, B follows, then B is paused and cut from A, so its
// image stays at A's first epoch. A's lease lapses on a cut from W and A
// leads again in a later epoch with W. A dies; B resumes and takes over
// with W, whose record shows A's later epoch: cold, for that reason. (The
// marks would go cold too: A's last renewals carried its later epoch.)
// later_leader_ignored turns it red.
func TestTargetedLaterLeadershipGoesCold(t *testing.T) {
	rec := Run(Options{Seed: 3, Delay: ms, Fsync: ms, Until: 45 * sec,
		Start: [3]time.Duration{0, 4 * sec, 0},
		Faults: []Fault{
			{Kind: Pause, At: 11 * sec, For: 11 * sec, Node: B},
			{Kind: Cut, At: 11 * sec, From: A, To: B, Both: true},
			{Kind: Cut, At: 12 * sec, For: 3 * sec, From: A, To: W, Both: true},
			{Kind: Crash, At: 20 * sec, Node: A},
		}})
	clean(t, rec, Checks{MaxTakeovers: -1})
	var aEpochs []uint64
	for _, l := range rec.LeadStarts {
		if l.Node == A {
			aEpochs = append(aEpochs, l.Epoch)
		}
	}
	if len(aEpochs) < 2 {
		t.Fatalf("A led in %v: the case needs a second lease of A's", aEpochs)
	}
	hs := hotOf(rec, B)
	if len(hs) != 1 || hs[0].Hot || hs[0].Why != quorum.ColdLaterLeader || hs[0].Image != aEpochs[0] {
		t.Fatalf("B's takeover: %+v; want one, cold (%s), from an image of epoch %d (A led in %v)", hs, quorum.ColdLaterLeader, aEpochs[0], aEpochs)
	}
}
