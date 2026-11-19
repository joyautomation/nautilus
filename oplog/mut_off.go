//go:build !quorum_mut

package oplog

// mutOplog is off in every normal build (mut_on.go).
func mutOplog(string) bool { return false }
