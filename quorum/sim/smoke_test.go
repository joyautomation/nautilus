package sim

import (
	"testing"
	"time"
)

func TestSmokeNoFaults(t *testing.T) {
	rec := Run(Options{Seed: 1, Delay: time.Millisecond, Fsync: time.Millisecond, Until: 20 * time.Second, Trace: true})
	g := rec.Gates()
	for _, iv := range g {
		t.Log(iv)
	}
	for _, v := range Check(rec, Checks{MaxTakeovers: 0}) {
		t.Error(v)
	}
	if len(g) == 0 {
		for _, l := range rec.Trace[:min(60, len(rec.Trace))] {
			t.Log(l)
		}
		t.Fatal("nobody led")
	}
}
