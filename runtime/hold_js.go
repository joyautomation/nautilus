//go:build js

package runtime

import "time"

// The playground's wasm runs no redundant pair, so no takeover hold
// (hold.go is the controller's).

type holdState struct{}

func (r *Runtime) holdOnTakeover(bool, time.Duration) {}
func (r *Runtime) holdScan() bool                     { return false }
func (r *Runtime) holdAge() time.Duration             { return -1 }
func (r *Runtime) holdDriverReplaced()                {}
func (r *Runtime) holdAfterPush()                     {}
