//go:build js

package runtime

// The playground's wasm runs no redundant pair, so no switchover
// (switch.go is the controller's).

type switchState struct{}

func (r *Runtime) Switching() uint64 { return 0 }
func (r *Runtime) switchEnded()      {}
func (r *Runtime) scanEnter() bool   { return true }
func (r *Runtime) scanExit()         {}
