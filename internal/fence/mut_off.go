//go:build !quorum_mut

package fence

// mut is off in every normal build (mut_on.go).
func mut(string) bool { return false }
