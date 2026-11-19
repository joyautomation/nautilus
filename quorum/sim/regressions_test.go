package sim

import (
	"testing"
	"time"
)

// Named regression cases: seeded schedules the runner found, shrunk and
// committed (test plan §3.2), each judged by every oracle (checksFor).

// The two counted voters livelock (the yield): seeds 275 and 1653 of the
// default 2,000, shrunk. Seed 275: the witness is wiped (its new
// incarnation isn't counted before the handshake PR) and A is down 18 s;
// when A is back, A and B each promise themselves and refuse the other.
// Seed 1653: both controllers lose their files, so neither counts the
// witness. Both fail I6 (no leader after the heal) without
// Voter.WouldYield/Candidate.Yield. Seeds 540 and 1068 failed the same
// way first, and pass even without the yield since campaigns observe the
// peer's terms (Candidate.Observe) and retries move off the look grid.
var livelockCases = map[string]Options{
	"seed275": {Seed: 275, Delay: 2985580, Jitter: 1754305, Fsync: 381118, InPlace: true,
		Rates: [nNodes]PPM{997368, 1002584, 1002386}, Start: [3]time.Duration{3759068977, 1372222162, 3882453785}, Until: 58413300107,
		Faults: []Fault{
			{Kind: Crash, At: 21445059081, For: 17968241026, Node: A},
			{Kind: Wipe, At: 10764040395, For: 7199531444, Node: W},
		}},
	"seed1653": {Seed: 1653, Delay: 2919491, Jitter: 1674237, Fsync: 4400320, InPlace: true,
		Rates: [nNodes]PPM{996934, 999597, 998332}, Start: [3]time.Duration{1873736067, 648631022, 1286627556}, Until: 45590035827,
		Faults: []Fault{
			{Kind: Wipe, At: 11619435332, For: 311686393, Node: A},
			{Kind: Wipe, At: 6506519627, For: 17361366765, Node: B},
		}},
}

func TestRegressionTwoVoterLivelock(t *testing.T) {
	for name, o := range livelockCases {
		t.Run(name, func(t *testing.T) {
			for _, v := range Check(Run(o), checksFor(o)) {
				t.Error(v)
			}
		})
	}
}

// #976, a known gap: two controllers that both lose their state files
// form a majority without the witness and grant terms below one already
// led, so the fencing token goes backwards (I2: "B wrote term 2 after term
// 5"). Seeds 208 and 1458 of the default 2,000, shrunk. The generator now
// draws at most one lost controller file per schedule; these two must
// keep failing until the handshake PR re-commissions a controller with an
// unreadable file (term floor + accepted incarnation). When this test
// fails because the gap is gone, make the cases a required pass.
func TestKnownGap976TwoLostControllerFiles(t *testing.T) {
	for name, o := range map[string]Options{
		"seed208": {Seed: 208, Delay: 530818, Jitter: 175320, Fsync: 4337184, InPlace: true,
			Rates: [nNodes]PPM{997866, 1004508, 999726}, Start: [3]time.Duration{581011620, 1369048054, 3402530163}, Until: 41517713503,
			Faults: []Fault{
				{Kind: Wipe, At: 14052161408, For: 6057402149, Node: A},
				{Kind: Wipe, At: 17222489667, For: 5295223836, Node: B},
			}},
		"seed1458": {Seed: 1458, Delay: 220113, Jitter: 34053, Fsync: 3982614, InPlace: false,
			Rates: [nNodes]PPM{1000797, 997917, 1000919}, Start: [3]time.Duration{1080345728, 2642599916, 602681865}, Until: 57840622381,
			Faults: []Fault{
				{Kind: Wipe, At: 14547006072, For: 6742578343, Node: B},
				{Kind: Wipe, At: 20570756019, For: 3834675396, Node: A},
			}},
	} {
		t.Run(name, func(t *testing.T) {
			regressed := false
			for _, v := range Check(Run(o), checksFor(o)) {
				switch v.Oracle {
				case "I2", "I4":
					regressed = true
				default:
					t.Errorf("an oracle besides the known gap failed: %v", v)
				}
			}
			if !regressed {
				t.Fatal("#976 no longer reproduces: if the handshake PR closed it, make these cases a required pass")
			}
		})
	}
}
