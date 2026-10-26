//go:build !linux

package plcnext

import "errors"

// The shim and its page exist only on the controller (Linux); `naut check`
// and `naut build` on any OS never open it.
func openShmPage(string) (*shmPage, error) {
	return nil, errors.New("plcnext: shm: the exchange page is Linux-only (it lives on the controller)")
}

func monoNow() int64 { return 0 }
