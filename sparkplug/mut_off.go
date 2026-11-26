//go:build !quorum_mut

package sparkplug

func mut(string) bool { return false }
