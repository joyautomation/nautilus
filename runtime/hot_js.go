//go:build js

package runtime

// The playground's wasm runs no redundant pair: every takeover is its
// first start, cold (hot_install.go is the controller's).

func (r *Runtime) hotTakeover(*HotImage, string, bool) bool { return false }
