//go:build !quorum_mut

package server

// mutServer is off in every normal build (mut_on.go).
func mutServer(string) bool { return false }
