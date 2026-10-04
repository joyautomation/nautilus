//go:build !linux

package runtime

import (
	"context"
	"time"
)

// Off Linux the loop sleeps on a Go timer. Precision is whatever the Go
// runtime's poller gives on that OS (on Linux it was a millisecond, which
// is why sleep_linux.go exists); the schedule is still absolute, so a
// late wake never pushes the next one later. Measure before promising
// anything on these platforms.

func loopThread() func() { return func() {} }

// sleepUntil blocks until deadline or until ctx is done. Returns false
// when ctx ended it.
func sleepUntil(ctx context.Context, deadline time.Time) bool {
	wait := time.Until(deadline)
	if wait <= 0 {
		return ctx.Err() == nil
	}
	tm := time.NewTimer(wait)
	defer tm.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-tm.C:
		return true
	}
}
