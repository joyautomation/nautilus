//go:build race

package sim

// raceOn: the race detector slows every simulated run several fold. The
// race job is there for data races, not for seed counts: the plain test
// job runs the default counts and the nightly far more (spareSeeds).
const raceOn = true
