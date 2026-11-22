//go:build !quorum_mut

package alarm

// mutAlarm is off in every normal build (mut_on.go).
func mutAlarm(string) bool { return false }
