//go:build !quorum_mut

package main

// mutNaut is off in every normal build (mut_on.go).
func mutNaut(string) bool { return false }
