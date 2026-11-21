//go:build !quorum_mut

package runtime

func mutRuntime(string) bool { return false }
