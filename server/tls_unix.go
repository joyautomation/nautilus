//go:build unix

package server

import (
	"fmt"
	"os"
)

// checkKeyMode refuses a TLS key every user on the box may read. A group
// may (the ssl-cert group convention).
func checkKeyMode(path string) error {
	st, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("tls: %w", err)
	}
	if m := st.Mode().Perm(); m&0o004 != 0 {
		return fmt.Errorf("tls: %s is mode %04o: every user on this machine can read the private key; chmod o-r it", path, m)
	}
	return nil
}
