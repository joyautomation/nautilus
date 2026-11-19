//go:build !quorum_mut

package quorum

// mut reports whether a mutation is switched on. In every normal build it
// is the constant false, so the checks below compile away. See mut_on.go.
func mut(string) bool { return false }
