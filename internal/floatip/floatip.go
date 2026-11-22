// Package floatip holds a redundant pair's optional floating IP address on
// whichever controller leads (#716, design redundancy.md §8).
//
// The address is added after the write gate has opened (so a controller that
// cannot lead never answers for it) with a gratuitous ARP so that neighbours
// move to it at once, and dropped as soon as the gate closes and before an
// orderly stop hands the lease over. After a kill, hang or pause the kernel
// keeps the address until systemd's ExecStopPost (or the watchdog's kill)
// removes it; that overlap is client routing only, since field writes are
// fenced by the gate and a fenced controller's API refuses writes.
//
// The package knows nothing about elections: Watch is given the gate as a
// func. The kernel side (netlink, a raw socket) is behind Ops, so tests run
// without privileges.
package floatip

import (
	"context"
	"fmt"
	"net/netip"
	"sync"
	"time"
)

// Config is redundancy.floating-ip.
type Config struct {
	// Address is the address with its prefix length, "10.0.0.50/24".
	Address netip.Prefix
	// Interface is the network interface that carries it ("eth0").
	Interface string
}

// Parse is Config from the manifest's two strings.
func Parse(address, iface string) (Config, error) {
	p, err := netip.ParsePrefix(address)
	if err != nil {
		return Config{}, fmt.Errorf("address %q: want an IPv4 address with its prefix length, e.g. 10.0.0.50/24", address)
	}
	if !p.Addr().Is4() {
		return Config{}, fmt.Errorf("address %q: only IPv4 is supported (the takeover announces itself with a gratuitous ARP)", address)
	}
	if iface == "" || len(iface) > 15 {
		return Config{}, fmt.Errorf("interface %q: want a network interface name (at most 15 characters)", iface)
	}
	return Config{Address: p, Interface: iface}, nil
}

// Ops is what the kernel does for the address.
type Ops interface {
	// Add puts the address on the interface; nil if it is already there.
	Add(c Config) error
	// Del removes it; nil if it is not there.
	Del(c Config) error
	// Announce sends a gratuitous ARP for it.
	Announce(c Config) error
}

// Holder keeps the address on this controller while told to.
type Holder struct {
	cfg Config
	ops Ops

	mu   sync.Mutex
	held bool
	// Log receives what the holder does and why it failed; nil is silent.
	// A failure that repeats on every tick is logged once a minute.
	Log func(msg string, args ...any)
	// warned is when the last failure was logged.
	warned time.Time
}

// New is a holder over ops (System on Linux).
func New(c Config, ops Ops) *Holder { return &Holder{cfg: c, ops: ops} }

// Held reports whether the holder believes it has the address.
func (h *Holder) Held() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.held
}

// Hold adds the address and announces it. It is called only once the gate
// is open. A failure leaves it not held, and the next call tries again.
func (h *Holder) Hold() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.held {
		return nil
	}
	if err := h.ops.Add(h.cfg); err != nil {
		return fmt.Errorf("floating IP %s on %s: %w", h.cfg.Address, h.cfg.Interface, err)
	}
	h.held = true
	// Three announcements a few ms apart: one lost frame must not leave a
	// neighbour's cache pointing at the old controller.
	var aerr error
	for i := 0; i < 3; i++ {
		if err := h.ops.Announce(h.cfg); err != nil {
			aerr = err
		}
		if i < 2 {
			time.Sleep(5 * time.Millisecond)
		}
	}
	if aerr != nil && h.Log != nil {
		h.Log("floating IP announced, but a gratuitous ARP failed", "err", aerr)
	}
	return nil
}

// Drop removes the address. It is called when the gate closes, and before an
// orderly stop releases the lease. A failure keeps it "held", so the next
// call tries again.
func (h *Holder) Drop() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.ops.Del(h.cfg); err != nil {
		return fmt.Errorf("floating IP %s on %s: %w", h.cfg.Address, h.cfg.Interface, err)
	}
	h.held = false
	return nil
}

// Watch holds the address while gate() is true and drops it when it is not,
// looking every every. It returns when ctx ends, WITHOUT dropping: the caller
// drops after Watch has returned, before it releases the lease (so no tick
// can add it again behind the drop). A gate that is never true holds nothing,
// which is why a spare or a copy held by a duplicate mark never has the
// address: its gate stays closed.
func (h *Holder) Watch(ctx context.Context, gate func() bool, every time.Duration) {
	if !gate() {
		// A kill leaves the address behind until ExecStopPost; so does a
		// crash with no unit. Clear what an earlier process left.
		_ = h.Drop()
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		h.step(gate())
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (h *Holder) step(open bool) {
	switch {
	case open && !h.Held():
		if err := h.Hold(); err != nil {
			h.warn("floating IP: could not take the address (does this process hold CAP_NET_ADMIN and CAP_NET_RAW?)", err)
		} else if h.Log != nil {
			h.Log("floating IP: taken, gate open", "address", h.cfg.Address.String(), "interface", h.cfg.Interface)
		}
	case !open && h.Held():
		if err := h.Drop(); err != nil {
			h.warn("floating IP: could not drop the address", err)
		} else if h.Log != nil {
			h.Log("floating IP: dropped, gate closed", "address", h.cfg.Address.String())
		}
	}
}

func (h *Holder) warn(msg string, err error) {
	if h.Log == nil || time.Since(h.warned) < time.Minute {
		return
	}
	h.warned = time.Now()
	h.Log(msg, "err", err)
}
