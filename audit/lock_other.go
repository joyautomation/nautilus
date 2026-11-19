//go:build !(linux || darwin || freebsd || netbsd || openbsd || dragonfly)

package audit

// LockName is the file in a journal directory that OpenFile locks.
const LockName = ".audit.lock"

// lockDir does not lock on this platform (Windows, wasm): two processes
// that open one journal directory interleave their chains, which
// `naut audit verify` then reports.
func lockDir(string) (func(), error) { return func() {}, nil }
