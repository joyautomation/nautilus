//go:build quorumtest

package fence

import (
	"sync/atomic"
	"time"
)

// Pause sets how long the next sends stall at p (test plan §3.4): the
// three points between the gate check and the send system call. Zero
// clears it. Only a quorumtest build has it.
func Pause(p Point, d time.Duration) { pauses[p].Store(int64(d)) }

var pauses [3]atomic.Int64

func pause(p Point) {
	if d := pauses[p].Load(); d > 0 {
		time.Sleep(time.Duration(d))
	}
}

// BeforeSend stalls at RightBeforeSend: after the last look at the
// deadline, right before the send system call.
func BeforeSend() { pause(RightBeforeSend) }
