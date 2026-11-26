//go:build !unix && !js

package camera

// diskFree says nothing on a platform without statfs: the byte and count bounds
// still hold.
func diskFree(dir string) (free, total uint64, ok bool) { return 0, 0, false }
