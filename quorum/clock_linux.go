//go:build linux

package quorum

import "golang.org/x/sys/unix"

// Now reads CLOCK_BOOTTIME: monotonic, and unlike CLOCK_MONOTONIC it keeps
// counting while the host is suspended, so a lease never outlives a
// voter's promise across a suspend (design §3.1, clocks).
func Now() Instant {
	var ts unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_BOOTTIME, &ts); err != nil {
		panic("quorum: CLOCK_BOOTTIME: " + err.Error())
	}
	return Instant(ts.Nano())
}

// BootClock reports whether Now counts through a host suspend. Controllers
// under redundancy: require it (naut check refuses otherwise).
const BootClock = true
