package project

import (
	"fmt"
	"time"
)

// FailbackConfig is redundancy.failback (#710 d2, design §4.8): when the
// preferred controller has been ready to take the lead (a switchover's
// checks) for After without a break, the leader runs a planned switchover
// to it. Off when absent. Pair only.
type FailbackConfig struct {
	// Prefer is the controller that should lead (a name under peers:).
	Prefer string `yaml:"prefer"`
	// After is how long Prefer must be ready, without a break, before the
	// failback (default 5m, at least 10 leases).
	After *Duration `yaml:"after"`
	// Max is how many failbacks in a row, each followed by a failover
	// within After, stop it (default 3): it suspends, with an alarm.
	Max int `yaml:"max"`
}

// DefaultFailbackAfter and DefaultFailbackMax are redundancy.failback's
// defaults.
const (
	DefaultFailbackAfter = 5 * time.Minute
	DefaultFailbackMax   = 3
)

// AfterOr is After, or its default.
func (f *FailbackConfig) AfterOr() time.Duration {
	if f == nil || f.After == nil {
		return DefaultFailbackAfter
	}
	return time.Duration(*f.After)
}

// MaxOr is Max, or its default.
func (f *FailbackConfig) MaxOr() int {
	if f == nil || f.Max == 0 {
		return DefaultFailbackMax
	}
	return f.Max
}

// checkFailbackConfig checks redundancy.failback against its pair.
func checkFailbackConfig(r *RedundancyConfig) error {
	f := r.Failback
	if f == nil {
		return nil
	}
	if !r.Pair() {
		return fmt.Errorf("redundancy.failback is for a redundant pair (peers:): there is no other controller to fail back to")
	}
	if _, ok := r.Peers[f.Prefer]; !ok {
		return fmt.Errorf("redundancy.failback.prefer %q: want one of the pair's controllers (a name under peers:)", f.Prefer)
	}
	if min := 10 * r.LeaseTime(); f.AfterOr() < min {
		return fmt.Errorf("redundancy.failback.after %s: want at least 10 leases (%s): a failback sooner than that would fail back into a flapping link or box", f.AfterOr(), min)
	}
	if f.Max < 0 {
		return fmt.Errorf("redundancy.failback.max %d: want 1 or more (how many failbacks in a row, each followed by a failover, stop it)", f.Max)
	}
	return nil
}
