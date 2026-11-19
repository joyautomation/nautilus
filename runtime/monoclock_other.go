//go:build !linux

package runtime

import "time"

// bootClock is Go's monotonic clock where there is no CLOCK_BOOTTIME
// (macOS, js/wasm): still untouched by a wall-clock step.
func bootClock() func() time.Duration { return goMonoClock() }
