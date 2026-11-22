// Package floatip holds a redundant pair's optional floating IP address on
// whichever controller leads (#716, design redundancy.md §8).
//
// The address is added after the write gate has opened (so a controller that
// cannot lead never answers for it) with a gratuitous ARP so that neighbours
// move to it at once, and dropped as soon as the gate closes and before an
// orderly stop hands the lease over. After a kill, hang or pause the kernel
// keeps the address on that box until the unit's ExecStopPost removes it, or
// the restarted process clears it at start; that overlap is client routing
// only, since field writes are fenced by the gate and a fenced controller's
// API refuses writes.
//
// The holder reconciles instead of acting on edges: an Add whose outcome is
// unknown (the kernel may have applied it) is undone when the gate is shut,
// a shut gate keeps removing the address every second, and an open gate adds
// it again and re-announces it every few seconds (so an address removed from
// under the leader, or a neighbour that learned the old box's MAC from a
// late frame, is put right).
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
	if a := p.Addr(); p.Bits() == 0 || a.IsUnspecified() || a.IsLoopback() || a.IsMulticast() || a.IsLinkLocalUnicast() {
		return Config{}, fmt.Errorf("address %q: want a unicast address of the network the interface is on, with its prefix length", address)
	}
	if b := p.Bits(); b < 31 {
		// the network address and the broadcast address are not hosts
		n := p.Masked().Addr().As4()
		a4 := p.Addr().As4()
		host := uint32(a4[0])<<24 | uint32(a4[1])<<16 | uint32(a4[2])<<8 | uint32(a4[3])
		net := uint32(n[0])<<24 | uint32(n[1])<<16 | uint32(n[2])<<8 | uint32(n[3])
		if host == net || host == net|(^uint32(0)>>uint(b)) {
			return Config{}, fmt.Errorf("address %q is the network or the broadcast address of its network: pick a host address", address)
		}
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
	// dirty: an Add failed and may still have been applied, so the address
	// may be on the interface although held is false.
	dirty bool
	// gate is the write gate (Watch sets it): Hold looks again between the
	// add and each announcement and takes the address back if it has shut.
	gate func() bool
	// Log receives what the holder does and why it failed; nil is silent.
	// A failure that repeats is logged once a minute for each kind.
	Log func(msg string, args ...any)

	// Backoff is the wait after a failed add or drop (default 1 s for an
	// add, a tenth of it for a drop); Refresh is how often an open gate adds
	// again and announces (default 5 s); Reconcile is how often a shut gate
	// removes the address again (default 1 s). Tests shorten them.
	Backoff, Refresh, Reconcile time.Duration

	warned  map[string]time.Time
	retryAt time.Time
	lastDel time.Time
	lastAnn time.Time
}

// New is a holder over ops (System on Linux).
func New(c Config, ops Ops) *Holder {
	return &Holder{cfg: c, ops: ops, Backoff: time.Second, Refresh: 5 * time.Second, Reconcile: time.Second, warned: map[string]time.Time{}}
}

// Held reports whether the holder believes it has the address.
func (h *Holder) Held() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.held
}

func (h *Holder) open() bool { return h.gate == nil || h.gate() }

// Hold adds the address and announces it. It is called only once the gate
// is open. A failure leaves it not held, and the next call tries again; an
// add that failed may have been applied, so a later Drop removes it anyway.
func (h *Holder) Hold() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.held {
		return nil
	}
	if err := h.ops.Add(h.cfg); err != nil {
		h.dirty = true
		return fmt.Errorf("floating IP %s on %s: %w", h.cfg.Address, h.cfg.Interface, err)
	}
	h.held, h.dirty = true, false
	// Three announcements a few ms apart: one lost frame must not leave a
	// neighbour's cache pointing at the old controller.
	h.announceLocked(3)
	return nil
}

// announceLocked sends n announcements, looking at the gate before each: a
// controller whose gate shut while it was stalled must not announce after
// the new leader's, so it takes the address back instead.
func (h *Holder) announceLocked(n int) {
	var aerr error
	for i := 0; i < n; i++ {
		if !h.open() {
			if err := h.ops.Del(h.cfg); err != nil {
				h.dirty = true
			}
			h.held = false
			return
		}
		if err := h.ops.Announce(h.cfg); err != nil {
			aerr = err
		}
		if i < n-1 {
			time.Sleep(5 * time.Millisecond)
		}
	}
	h.lastAnn = time.Now()
	if aerr != nil {
		h.warnLocked("announce", "floating IP announced, but a gratuitous ARP failed", aerr)
	}
}

// Drop removes the address. It is called when the gate closes, and before an
// orderly stop releases the lease. A failure keeps it "held" (or "dirty"), so
// the next call tries again.
func (h *Holder) Drop() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lastDel = time.Now()
	if err := h.ops.Del(h.cfg); err != nil {
		return fmt.Errorf("floating IP %s on %s: %w", h.cfg.Address, h.cfg.Interface, err)
	}
	h.held, h.dirty = false, false
	return nil
}

// Watch holds the address while gate() is true and drops it when it is not,
// looking every every. It returns when ctx ends, WITHOUT dropping: the caller
// drops after Watch has returned, before it releases the lease (so no tick
// can add it again behind the drop). A gate that is never true holds nothing,
// which is why a spare or a copy held by a duplicate mark never has the
// address: its gate stays closed.
func (h *Holder) Watch(ctx context.Context, gate func() bool, every time.Duration) {
	h.mu.Lock()
	h.gate = gate
	h.mu.Unlock()
	if !gate() {
		// A kill leaves the address behind until ExecStopPost; so does a
		// crash with no unit. Clear what an earlier process left. A failure
		// is said at once, and the periodic reconcile tries again.
		if err := h.Drop(); err != nil {
			h.warn("drop", "floating IP: could not clear the address at start (does this process hold CAP_NET_ADMIN? the unit needs the capabilities and an ExecStopPost, see the redundancy guide)", err)
		}
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
	now := time.Now()
	h.mu.Lock()
	held, dirty, retryAt, lastDel, lastAnn := h.held, h.dirty, h.retryAt, h.lastDel, h.lastAnn
	h.mu.Unlock()
	switch {
	case open && !held:
		if now.Before(retryAt) {
			return
		}
		if err := h.Hold(); err != nil {
			h.setRetry(now.Add(h.Backoff))
			h.warn("take", "floating IP: could not take the address (does this process hold CAP_NET_ADMIN and CAP_NET_RAW? the unit needs both, see the redundancy guide)", err)
		} else if h.Log != nil && h.Held() {
			h.Log("floating IP: taken, gate open", "address", h.cfg.Address.String(), "interface", h.cfg.Interface)
		}
	case open && held && now.Sub(lastAnn) >= h.Refresh:
		// Idempotent: puts the address back if something removed it, and
		// announces again so a neighbour that learned another MAC is put right.
		h.mu.Lock()
		if err := h.ops.Add(h.cfg); err != nil {
			h.warnLocked("take", "floating IP: could not refresh the address", err)
		} else if h.held {
			h.announceLocked(1)
		}
		h.lastAnn = time.Now()
		h.mu.Unlock()
	case !open && (held || dirty):
		if !held && now.Before(retryAt) {
			return
		}
		if err := h.Drop(); err != nil {
			h.setRetry(now.Add(h.Backoff / 10))
			h.warn("drop", "floating IP: could not drop the address", err)
		} else if h.Log != nil && held {
			h.Log("floating IP: dropped, gate closed", "address", h.cfg.Address.String())
		}
	case !open && now.Sub(lastDel) >= h.Reconcile:
		// Held nothing, as far as it knows: say so to the kernel anyway, so an
		// address left by an add whose outcome was lost cannot stay.
		if err := h.Drop(); err != nil {
			h.warn("drop", "floating IP: could not drop the address", err)
		}
	}
}

func (h *Holder) setRetry(t time.Time) { h.mu.Lock(); h.retryAt = t; h.mu.Unlock() }

func (h *Holder) warn(kind, msg string, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.warnLocked(kind, msg, err)
}

func (h *Holder) warnLocked(kind, msg string, err error) {
	if h.Log == nil || time.Since(h.warned[kind]) < time.Minute {
		return
	}
	h.warned[kind] = time.Now()
	h.Log(msg, "err", err)
}
