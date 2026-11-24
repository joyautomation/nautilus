//go:build js

package runtime

// The playground's wasm runs no redundant pair: the system functions read
// no host extension there and answer FALSE/0 (redstate.go is the
// controller's).

type redState struct{}

func (v *scanView) latchRed(bool, bool) {}
func (r *Runtime) markTakeovers(bool)   {}
func (r *Runtime) clearTakeovers()      {}
