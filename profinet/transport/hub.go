package transport

import (
	"bytes"
	"context"
	"net"
	"slices"
	"sync"
)

// Hub is an in-memory Ethernet segment: every frame a port sends reaches
// every other port it is addressed to. It is what the device and
// controller tests run on, with no socket and no privilege.
//
// A port receives a frame when the destination is its MAC, broadcast, or
// a multicast group it joined, and the EtherType is one it was opened
// for. A full receive queue drops the frame, as a NIC would.
type Hub struct {
	mu    sync.Mutex
	ports []*hubPort
	tap   func(Frame)
}

// NewHub returns an empty segment.
func NewHub() *Hub { return &Hub{} }

// hubQueue is each port's receive queue depth.
const hubQueue = 4096

// Port attaches a station with address mac.
func (h *Hub) Port(mac net.HardwareAddr, opt Options) Port {
	p := &hubPort{
		hub:   h,
		mac:   append(net.HardwareAddr(nil), mac...),
		types: append([]uint16(nil), opt.EtherTypes...),
		rx:    make(chan Frame, hubQueue),
		done:  make(chan struct{}),
	}
	for _, g := range opt.Multicast {
		p.groups = append(p.groups, append(net.HardwareAddr(nil), g...))
	}
	h.mu.Lock()
	h.ports = append(h.ports, p)
	h.mu.Unlock()
	return p
}

// Tap installs f to see every frame sent on the segment, before
// delivery (nil removes it). Tests use it to assert on the wire.
func (h *Hub) Tap(f func(Frame)) {
	h.mu.Lock()
	h.tap = f
	h.mu.Unlock()
}

// Cut disconnects (true) or reconnects (false) the station with address
// mac: while cut, nothing it sends leaves and nothing reaches it, as
// with a pulled cable.
func (h *Hub) Cut(mac net.HardwareAddr, cut bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, p := range h.ports {
		if bytes.Equal(p.mac, mac) {
			p.cut = cut
		}
	}
}

func (h *Hub) deliver(from *hubPort, f Frame) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if from.closed {
		return ErrClosed
	}
	if h.tap != nil {
		h.tap(f)
	}
	if from.cut {
		return nil // the frame leaves the NIC and goes nowhere
	}
	for _, p := range h.ports {
		if p == from || p.cut || p.closed || !p.wants(f) {
			continue
		}
		g := Frame{
			Dst:       append(net.HardwareAddr(nil), f.Dst...),
			Src:       append(net.HardwareAddr(nil), f.Src...),
			EtherType: f.EtherType,
			Payload:   append([]byte(nil), f.Payload...),
		}
		select {
		case p.rx <- g:
		default: // queue full: dropped
		}
	}
	return nil
}

type hubPort struct {
	hub    *Hub
	mac    net.HardwareAddr
	types  []uint16
	groups []net.HardwareAddr
	rx     chan Frame
	done   chan struct{}
	// cut and closed are guarded by hub.mu.
	cut, closed bool
}

func (p *hubPort) wants(f Frame) bool {
	if !slices.Contains(p.types, f.EtherType) {
		return false
	}
	return addressedTo(f.Dst, p.mac, p.groups)
}

func (p *hubPort) MAC() net.HardwareAddr { return p.mac }

func (p *hubPort) Recv(ctx context.Context) (Frame, error) {
	select {
	case f := <-p.rx:
		return f, nil
	case <-p.done:
		return Frame{}, ErrClosed
	case <-ctx.Done():
		return Frame{}, ctx.Err()
	}
}

func (p *hubPort) Send(dst net.HardwareAddr, etherType uint16, payload []byte) error {
	return p.hub.deliver(p, Frame{Dst: dst, Src: p.mac, EtherType: etherType, Payload: padded(payload)})
}

func (p *hubPort) Close() error {
	p.hub.mu.Lock()
	defer p.hub.mu.Unlock()
	if !p.closed {
		p.closed = true
		close(p.done)
	}
	return nil
}
