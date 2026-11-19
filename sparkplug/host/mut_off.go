//go:build !quorum_mut

package host

// mut is off in every normal build (mut_on.go).
func mut(string) bool { return false }
