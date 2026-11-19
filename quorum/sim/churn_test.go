package sim

import (
	"testing"
	"time"
)

func TestQuickChurn(t *testing.T) {
	for seed := uint64(1); seed <= 10; seed++ {
		o := Options{Seed: seed, Delay: ms, Jitter: ms, Fsync: ms, Until: 60 * sec, Start: [3]time.Duration{0, 300 * ms, 0},
			Faults: []Fault{{Kind: Duplicate, At: 0, From: A, To: W, Both: true, Value: int64(lease)}, {Kind: Duplicate, At: 0, From: A, To: B, Both: true, Value: int64(lease)}}}
		g := Run(o).Gates()
		t.Logf("seed %d: %d intervals, last %v", seed, len(g), g[len(g)-1])
	}
}
