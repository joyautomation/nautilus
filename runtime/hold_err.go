package runtime

import "errors"

// ErrNotHolding is ReleaseHold's answer when no takeover hold holds
// (hold.go, #710 (b2)).
var ErrNotHolding = errors.New("no takeover hold is holding")
