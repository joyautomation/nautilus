//go:build !quorum_mut

package node

// mut reports whether a mutation is switched on: never in a normal build.
// See mut_on.go.
func mut(string) bool { return false }

// coreMutation reports whether a mutation of package quorum (not of this
// driver) is switched on: never in a normal build.
func coreMutation() bool { return false }

// guessPeerAPI is a mutation's (mut_on.go): never called in a normal build.
func guessPeerAPI(string, string) string { return "" }
