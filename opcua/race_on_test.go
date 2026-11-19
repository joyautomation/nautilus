//go:build race

package opcua

// raceOn: the race detector slows sixteen live sessions about fivefold.
const raceOn = true
