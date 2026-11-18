//go:build race

package host

// raceEnabled: built with -race (TestCloseBrokerUnderClientChurn runs longer).
const raceEnabled = true
