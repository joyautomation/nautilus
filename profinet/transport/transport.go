// Package transport moves raw Ethernet frames for the PROFINET device and
// controller. A Port is one station's attachment to a segment: on Linux an
// AF_PACKET socket bound to an interface (Open), in tests a port on an
// in-memory Hub. Everything above this package sees only Port, so the
// device and controller run against each other in-process with no
// privilege, and on a NIC with CAP_NET_RAW.
package transport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
)

// Frame is one received Ethernet frame. Payload is what follows the
// EtherType (an 802.1Q tag, if the frame carried one, is already
// removed). The slices are the receiver's to keep.
type Frame struct {
	Dst, Src  net.HardwareAddr
	EtherType uint16
	Payload   []byte
}

// Port is a station's attachment to an Ethernet segment.
type Port interface {
	// MAC is this station's address: the source of every frame it sends.
	MAC() net.HardwareAddr
	// Recv blocks for the next frame addressed to this station (its MAC,
	// a multicast group it joined, or broadcast) with one of the
	// EtherTypes it was opened for. It returns ctx.Err() when ctx ends
	// and ErrClosed after Close.
	Recv(ctx context.Context) (Frame, error)
	// Send transmits payload to dst with the given EtherType. Short
	// frames are padded to the Ethernet minimum.
	Send(dst net.HardwareAddr, etherType uint16, payload []byte) error
	// Close releases the port; a blocked Recv returns ErrClosed.
	Close() error
}

// ErrClosed is returned by a Port after Close.
var ErrClosed = errors.New("profinet transport: port closed")

// ErrUnsupported is returned by Open where raw Ethernet is not available
// (every OS but Linux).
var ErrUnsupported = errors.New("profinet transport: raw Ethernet needs Linux (AF_PACKET)")

// PermissionError is Open's error when the process may not open a raw
// socket. Its message says how to grant the capability, because that
// text is what lands on the driver's status row.
type PermissionError struct {
	Interface string
	Err       error
}

func (e *PermissionError) Error() string {
	return fmt.Sprintf("raw Ethernet on %s needs CAP_NET_RAW (setcap cap_net_raw+ep on the naut binary, or AmbientCapabilities=CAP_NET_RAW in its systemd unit): %v", e.Interface, e.Err)
}

func (e *PermissionError) Unwrap() error { return e.Err }

// Options configure Open.
type Options struct {
	// EtherTypes are the frame types the port receives; at least one.
	EtherTypes []uint16
	// Multicast are the groups the port joins (PROFINET DCP's
	// 01:0E:CF:00:00:00, LLDP's 01:80:C2:00:00:0E). Joining is how a
	// port on a macvlan or a bridge sees them without promiscuous mode.
	Multicast []net.HardwareAddr
}

// minPayload is the shortest Ethernet payload (60-byte frame without FCS
// minus the 14-byte header).
const minPayload = 46

func padded(payload []byte) []byte {
	if len(payload) >= minPayload {
		return payload
	}
	p := make([]byte, minPayload)
	copy(p, payload)
	return p
}

var broadcast = net.HardwareAddr{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}

// addressedTo reports whether a frame to dst is for a station with
// address mac that joined groups.
func addressedTo(dst, mac net.HardwareAddr, groups []net.HardwareAddr) bool {
	if bytes.Equal(dst, mac) || bytes.Equal(dst, broadcast) {
		return true
	}
	for _, g := range groups {
		if bytes.Equal(dst, g) {
			return true
		}
	}
	return false
}
