//go:build !unix && !windows

package oplog

// diskFree is unknown here (the browser playground has no disk): -1, and
// compaction does not wait for space.
func diskFree(string) (int64, error) { return -1, nil }
