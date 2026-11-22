package main

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"

	"github.com/joyautomation/nautilus/checkout"
	"github.com/joyautomation/nautilus/internal/project"
)

// checkoutEvidence is what a run and a seal ask the controller about its own
// devices (#719 slice 4): a run is signed off only against real field I/O, and
// the run's rehearsal flag is the client's, so the controller works out for
// itself whether anything physical is behind the points. It reads the manifest
// it runs, at the start of a run (a run begun against what it refuses can
// never be sealed) and again at the seal (the devices must be the same). It
// fails closed: it refuses when
//
//   - no field driver is configured (the in-memory loopback: nothing was
//     checked),
//   - ANY endpoint it can read is a loopback, an address of this box itself, or
//     one of the project's own virtual devices (one simulator among real
//     devices is enough),
//   - ANY driver's endpoint cannot be read (a driver that keeps its hosts in a
//     manifest file, an empty endpoint), or
//   - a point's device is a devices: row at such an address,
//
// unless the manifest attests checkout: real-io, for a controller that reaches
// real devices through a local gateway or a driver whose hosts it cannot read;
// the evidence then says so. What it saw goes in the run and the sealed record.
func checkoutEvidence(fsys fs.FS, manifest, dir string, attested bool) func(*checkout.Sheet) (checkout.Evidence, string) {
	return func(sh *checkout.Sheet) (checkout.Evidence, string) {
		m, err := project.ReadManifest(fsys, manifest)
		if err != nil {
			return checkout.Evidence{}, "the manifest could not be read to say what the controller is connected to: " + err.Error()
		}
		virtual, verr := virtualListens(dir, manifest)
		if verr != nil {
			return checkout.Evidence{}, "the project's virtual devices could not be read, so it cannot be told whether the devices are real: " + verr.Error()
		}
		return deriveEvidence(m, sh, virtual, ownAddrs(), attested)
	}
}

// virtualListens is the addresses the project's own virtual devices answer on
// (virtual-devices.yaml and the devices: rows), as host|port. Empty when there
// are none; an error when they exist but cannot be read.
func virtualListens(dir, manifest string) (map[string]bool, error) {
	out := map[string]bool{}
	if dir == "" {
		return out, nil
	}
	set, err := loadVirtualDevices(dir, manifest)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return out, nil
		}
		return nil, err
	}
	if set == nil {
		return out, nil
	}
	for _, in := range set.Devices {
		// the address a virtual device LISTENS on: its Endpoint is the real
		// device's address (what the controller dials), not a virtual one
		if in.Listen != "" {
			out[hostOf(in.Listen)+"|"+portOf(in.Listen)] = true
		}
	}
	return out, nil
}

// ownAddrs is the addresses of this box's interfaces: a driver dialling one is
// dialling the box itself.
func ownAddrs() map[netip.Addr]bool {
	out := map[netip.Addr]bool{}
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if p, err := netip.ParsePrefix(a.String()); err == nil {
			out[p.Addr().Unmap()] = true
		}
	}
	return out
}

func deriveEvidence(m *project.Manifest, sh *checkout.Sheet, virtual map[string]bool, own map[netip.Addr]bool, attested bool) (checkout.Evidence, string) {
	ev := checkout.Evidence{Attested: attested}
	drivers := append([]project.DriverConfig(nil), m.Drivers...)
	if m.Driver.Type != "" {
		drivers = append(drivers, m.Driver)
	}
	var problems []string
	field := 0
	for _, d := range drivers {
		name := d.Name
		if name == "" {
			name = d.Type
		}
		ev.Drivers = append(ev.Drivers, checkout.DriverEvidence{Name: name, Kind: d.Type})
		switch strings.ToLower(d.Type) {
		case "", "memory", "loopback", "runtime":
			continue // no field I/O behind these
		}
		field++
		read := 0
		for _, ep := range []string{d.Host, d.Endpoint, d.Broker} {
			if ep == "" {
				continue
			}
			read++
			cls := classify(ep, virtual, own)
			ev.Devices = append(ev.Devices, checkout.DeviceEvidence{Name: name, Endpoint: ep, Class: cls})
			if cls != "network" {
				problems = append(problems, fmt.Sprintf("driver %s connects to a %s address (%s)", name, cls, ep))
			}
		}
		if read == 0 {
			ev.Devices = append(ev.Devices, checkout.DeviceEvidence{Name: name, Class: "unknown"})
			problems = append(problems, fmt.Sprintf("the controller cannot read where driver %s connects (its hosts are in a manifest file)", name))
		}
	}
	byName := map[string]string{}
	for _, d := range m.Devices {
		cls := classify(d.Endpoint, virtual, own)
		if strings.TrimSpace(d.Endpoint) == "" {
			cls = "unknown"
		}
		byName[d.Name] = cls
		ev.Devices = append(ev.Devices, checkout.DeviceEvidence{Name: d.Name, Endpoint: d.Endpoint, Class: cls})
		if cls != "network" {
			problems = append(problems, fmt.Sprintf("device %s is at a %s address (%q)", d.Name, cls, d.Endpoint))
		}
	}
	if field == 0 && len(m.Devices) == 0 {
		return ev, "this controller has no field driver configured (it runs the in-memory loopback): nothing physical was checked"
	}
	for _, p := range sh.Points {
		if cls, ok := byName[p.Device]; ok && cls != "network" {
			problems = append(problems, fmt.Sprintf("point %s is on device %s, which this controller reaches at a %s address", p.ID, p.Device, cls))
		}
	}
	if len(problems) > 0 && !attested {
		return ev, strings.Join(problems, "; ") + ": a checkout against these proves nothing about the plant. If this controller does reach real field devices this way (a local gateway, a driver whose hosts are in a manifest file), say so in the manifest with checkout: {real-io: true}"
	}
	return ev, ""
}

// classify says what an endpoint is: loopback (localhost, 127.x, ::1, an
// unspecified address, or an address of this box), virtual (a port one of the
// project's own virtual devices answers on, at a host that is this box) or
// network. An endpoint with no host is unknown.
func classify(endpoint string, virtual map[string]bool, own map[netip.Addr]bool) string {
	h, p := hostOf(endpoint), portOf(endpoint)
	if h == "" {
		return "unknown"
	}
	self := h == "localhost" || strings.HasSuffix(h, ".localhost")
	if a, ok := parseHostAddr(h); ok {
		a = a.Unmap()
		self = self || a.IsLoopback() || a.IsUnspecified() || own[a]
	}
	if virtual[h+"|"+p] || (self && p != "" && virtualPort(virtual, p)) {
		return "virtual"
	}
	if self {
		return "loopback"
	}
	return "network"
}

func virtualPort(virtual map[string]bool, port string) bool {
	for k := range virtual {
		if strings.HasSuffix(k, "|"+port) {
			return true
		}
	}
	return false
}

// parseHostAddr reads an IP in any spelling a resolver accepts: dotted, IPv6,
// and the short IPv4 forms (127.1, 0x7f.1, a single decimal number).
func parseHostAddr(h string) (netip.Addr, bool) {
	if a, err := netip.ParseAddr(h); err == nil {
		return a, true
	}
	parts := strings.Split(h, ".")
	if len(parts) < 1 || len(parts) > 4 {
		return netip.Addr{}, false
	}
	var nums []uint64
	for _, s := range parts {
		n, err := strconv.ParseUint(s, 0, 32)
		if err != nil {
			return netip.Addr{}, false
		}
		nums = append(nums, n)
	}
	var v uint64
	for i, n := range nums {
		if i < len(nums)-1 {
			if n > 255 {
				return netip.Addr{}, false
			}
			v |= n << (8 * (3 - uint(i)))
		} else {
			if n >= 1<<(8*(4-uint(i))) {
				return netip.Addr{}, false
			}
			v |= n
		}
	}
	return netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}), true
}

// hostOf and portOf read a host[:port], a scheme://host[:port] or a [v6]:port.
// The host is lower case with no trailing dot.
func hostOf(ep string) string {
	ep = strings.TrimSpace(ep)
	if i := strings.Index(ep, "://"); i >= 0 {
		ep = ep[i+3:]
	}
	if i := strings.IndexAny(ep, "/?"); i >= 0 {
		ep = ep[:i]
	}
	if i := strings.LastIndex(ep, "@"); i >= 0 {
		ep = ep[i+1:]
	}
	h := ep
	if hh, _, err := net.SplitHostPort(ep); err == nil {
		h = hh
	}
	return strings.TrimRight(strings.ToLower(strings.Trim(h, "[]")), ".")
}

func portOf(ep string) string {
	ep = strings.TrimSpace(ep)
	if i := strings.Index(ep, "://"); i >= 0 {
		ep = ep[i+3:]
	}
	if i := strings.IndexAny(ep, "/?"); i >= 0 {
		ep = ep[:i]
	}
	if i := strings.LastIndex(ep, "@"); i >= 0 {
		ep = ep[i+1:]
	}
	if _, p, err := net.SplitHostPort(ep); err == nil {
		return p
	}
	return ""
}
