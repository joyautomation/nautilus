//go:build !linux

package quorum

import "time"

var epoch = time.Now()

// Now reads Go's monotonic clock, which may stop while the host is
// suspended. That is safe only for the witness: a stopped witness clock
// grants later, never earlier (design §3.1). Controllers require
// BootClock.
func Now() Instant { return Instant(time.Since(epoch)) }

// BootClock reports whether Now counts through a host suspend.
const BootClock = false
