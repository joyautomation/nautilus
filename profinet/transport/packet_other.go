//go:build !linux

package transport

// Open is Linux-only: raw Ethernet goes through AF_PACKET. Elsewhere the
// drivers still load, check and build; they report ErrUnsupported on
// their status row when started.
func Open(ifname string, opt Options) (Port, error) { return nil, ErrUnsupported }
