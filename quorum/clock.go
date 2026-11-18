package quorum

import "time"

// Instant is a reading of a node's own clock: the time since an arbitrary
// epoch. Instants from different nodes are never compared.
type Instant time.Duration

// Add returns i + d.
func (i Instant) Add(d time.Duration) Instant { return i + Instant(d) }

// Sub returns i - j.
func (i Instant) Sub(j Instant) time.Duration { return time.Duration(i - j) }
