//go:build race

package runtime

// raceOn: the race detector's instrumentation allocates, so the allocation
// guards do not hold under it.
const raceOn = true
