//go:build !quorum_mut

package escalate

func mutEscalate(string) bool { return false }
