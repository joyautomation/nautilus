//go:build js

package runtime

// The playground's wasm runs no redundant pair, so no hand-back
// (handback.go is the controller's).

type handBackState struct{}

func (r *Runtime) handBackReady(uint64) bool { return true }
func (r *Runtime) handBackReset()            {}
func (r *Runtime) handBackWhy() string       { return "" }
