//go:build !quorum_mut

package peertls

func mut(string) bool { return false }
