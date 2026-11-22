//go:build race

package node

// raceOn: the race detector slows the loopback tests' processes several
// fold, so a renewal can come late on a loaded runner (#1120). Only the
// tests' timing margins use it, never the protocol's.
const raceOn = true
