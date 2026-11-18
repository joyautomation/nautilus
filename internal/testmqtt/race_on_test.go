//go:build race

package testmqtt

// raceEnabled: built with -race (TestCloseUnderClientChurn runs longer).
const raceEnabled = true
