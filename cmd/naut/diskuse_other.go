//go:build !unix && !windows

package main

import "errors"

// diskUsed cannot tell on this platform.
func diskUsed(string) (float64, error) {
	return 0, errors.New("disk use is not known on this platform")
}
