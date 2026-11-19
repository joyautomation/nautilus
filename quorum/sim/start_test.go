package sim

import (
	"fmt"
	"testing"
	"time"
)

// The three start states that left a standby refusing a live leader before
// #973: it started during the leader's campaign (its voter in its restart
// wait), it lost a contested cold start (F19), or it restarted under a
// live leader (F2/F3). Each then loses the witness for 10 s (F13). The
// leader must hold the gate without a break throughout (no takeover), with
// every voter up backing it once settled: before the outage A, B and W,
// during it A and B, and after the witness's restart wait all three again.
func TestStartStatesThenWitnessDown(t *testing.T) {
	cases := []struct {
		name   string
		start  [3]time.Duration
		faults []Fault
	}{
		{"late standby", [3]time.Duration{0, 300 * ms, 0}, nil},
		{"contested cold start", [3]time.Duration{0, 0, 0}, nil},
		{"standby restart", [3]time.Duration{0, 300 * ms, 0}, []Fault{{Kind: Crash, At: 4 * sec, For: 100 * ms, Node: B}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for seed := uint64(1); seed <= 20; seed++ {
				o := Options{Seed: seed, Delay: ms, Jitter: ms, Fsync: ms, Until: 40 * sec, Start: c.start,
					Faults: append([]Fault{{Kind: Crash, At: 12 * sec, For: 10 * sec, Node: W}}, c.faults...)}
				rec := Run(o)
				for _, ck := range []Checks{
					{MaxTakeovers: 0, SteadyFrom: 9 * sec, SteadyTo: 12 * sec, SteadyVoters: []Addr{A, B, W}},
					{MaxTakeovers: -1, SteadyFrom: 12 * sec, SteadyTo: 22 * sec, SteadyVoters: []Addr{A, B}},
					{MaxTakeovers: -1, SteadyFrom: 28 * sec, SteadyTo: 40 * sec, SteadyVoters: []Addr{A, B, W}},
				} {
					if vs := Check(rec, ck); len(vs) > 0 {
						t.Errorf("seed %d, window %v–%v: %v\n  repro: %s", seed, ck.SteadyFrom, ck.SteadyTo, vs[0],
							fmt.Sprintf("go test ./quorum/sim -run '^TestStartStatesThenWitnessDown$/^%s$' -v", t.Name()[len("TestStartStatesThenWitnessDown/"):]))
						return
					}
				}
			}
		})
	}
}
