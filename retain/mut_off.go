//go:build !quorum_mut

package retain

func mutRetain(string) bool { return false }
