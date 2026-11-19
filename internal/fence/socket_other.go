//go:build !linux

package fence

// linger0 and abortFD do nothing off Linux: a redundant pair runs on Linux
// only (CLOCK_BOOTTIME), and a Socket's close there is a FIN.
func linger0(uintptr) error { return nil }

func abortFD(uintptr) bool { return false }
