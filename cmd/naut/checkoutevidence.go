package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/netip"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

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
func checkoutEvidence(fsys fs.FS, manifest, dir string, attested []string) func(*checkout.Sheet) (checkout.Evidence, string) {
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

func deriveEvidence(m *project.Manifest, sh *checkout.Sheet, virtual map[string]bool, own map[netip.Addr]bool, attested []string) (checkout.Evidence, string) {
	ev := checkout.Evidence{}
	ok := map[string]bool{}
	for _, n := range attested {
		ok[strings.ToLower(n)] = true
	}
	used := map[string]bool{}
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
		case "", "memory", "loopback", "runtime", "replay":
			continue // no field I/O behind these (a replay plays recorded data)
		}
		field++
		read := 0
		for _, raw := range []string{d.Host, d.Endpoint, d.Broker} {
			for _, ep := range splitEndpoints(raw) {
				read++
				cls := classify(ep, virtual, own)
				shown := redact(ep)
				ev.Devices = append(ev.Devices, checkout.DeviceEvidence{Name: name, Endpoint: shown, Class: cls})
				if cls != "network" && !ok[strings.ToLower(name)] {
					problems = append(problems, fmt.Sprintf("driver %s connects to a %s address (%s)", name, cls, shown))
				} else if cls != "network" {
					used[strings.ToLower(name)] = true
				}
			}
		}
		if read == 0 {
			ev.Devices = append(ev.Devices, checkout.DeviceEvidence{Name: name, Class: "unknown"})
			if !ok[strings.ToLower(name)] {
				problems = append(problems, fmt.Sprintf("the controller cannot read where driver %s connects (its hosts are in a manifest file)", name))
			} else {
				used[strings.ToLower(name)] = true
			}
		}
	}
	byName := map[string]string{}
	for _, d := range m.Devices {
		cls := "unknown"
		eps := splitEndpoints(d.Endpoint)
		for i, ep := range eps {
			c := classify(ep, virtual, own)
			if i == 0 || c != "network" {
				cls = c
			}
		}
		byName[d.Name] = cls
		ev.Devices = append(ev.Devices, checkout.DeviceEvidence{Name: d.Name, Endpoint: redact(d.Endpoint), Class: cls})
		if cls != "network" && !ok[strings.ToLower(d.Name)] {
			problems = append(problems, fmt.Sprintf("device %s is at a %s address (%q)", d.Name, cls, redact(d.Endpoint)))
		} else if cls != "network" {
			used[strings.ToLower(d.Name)] = true
		}
	}
	if field == 0 && len(m.Devices) == 0 {
		return ev, "this controller has no field driver configured (it runs the in-memory loopback or replays recorded data): nothing physical was checked"
	}
	// the points on devices at such addresses, by device, so a large sheet gives
	// a short reason
	onBad := map[string][]string{}
	var badDevs []string
	for _, p := range sh.Points {
		if cls, found := byName[p.Device]; found && cls != "network" && !ok[strings.ToLower(p.Device)] {
			if len(onBad[p.Device]) == 0 {
				badDevs = append(badDevs, p.Device)
			}
			onBad[p.Device] = append(onBad[p.Device], p.ID)
		}
	}
	for _, dv := range badDevs {
		ids := onBad[dv]
		list := strings.Join(ids[:min(len(ids), 3)], ", ")
		if len(ids) > 3 {
			list += fmt.Sprintf(" and %d more", len(ids)-3)
		}
		problems = append(problems, fmt.Sprintf("%d point(s) (%s) are on device %s, which this controller reaches at a %s address", len(ids), list, dv, byName[dv]))
	}
	for n := range ok {
		if !used[n] {
			continue
		}
		ev.Attested = append(ev.Attested, n)
	}
	sort.Strings(ev.Attested)
	if len(problems) > 0 {
		if len(problems) > 6 {
			problems = append(problems[:6], fmt.Sprintf("and %d more", len(problems)-6))
		}
		return ev, strings.Join(problems, "; ") + ": a checkout against these proves nothing about the plant. If a driver or device really does reach field I/O this way (a local gateway, a driver whose hosts are in a manifest file), name it in the manifest: checkout: {real-io: [name]}"
	}
	return ev, ""
}

// splitEndpoints reads an endpoint field that may list several (a comma or
// space separated list).
func splitEndpoints(raw string) []string {
	var out []string
	for _, p := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' || r == ';' }) {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// redact removes a user name and password from an endpoint (a broker URL
// carries them): the evidence goes in the run, the sealed record and the page.
func redact(ep string) string {
	i := strings.Index(ep, "://")
	rest, scheme := ep, ""
	if i >= 0 {
		scheme, rest = ep[:i+3], ep[i+3:]
	}
	end := strings.IndexAny(rest, "/?")
	if end < 0 {
		end = len(rest)
	}
	if at := strings.LastIndex(rest[:end], "@"); at >= 0 {
		rest = rest[at+1:]
	}
	return scheme + rest
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
	zone := ""
	if i := strings.Index(h, "%"); i >= 0 {
		h, zone = h[:i], h[i+1:] // an IPv6 zone: fe80::1%lo
	}
	self := zone == "lo" || zone == "lo0" || h == "localhost" || strings.HasSuffix(h, ".localhost") || h == "localhost.localdomain" || h == "ip6-localhost" || h == "ip6-loopback"
	if a, ok := parseHostAddr(h); ok {
		a = a.Unmap()
		self = self || a.IsLoopback() || a.IsUnspecified() || own[a]
	} else if !self {
		// a name: this box's own name, or one that resolves to this box (a
		// name in /etc/hosts). A name that does not resolve is unknown: nothing
		// says where the driver would connect.
		if hn, err := os.Hostname(); err == nil && strings.EqualFold(strings.TrimSuffix(hn, "."), h) {
			self = true
		} else if ips, err := lookupHost(h); err != nil || len(ips) == 0 {
			return "unknown"
		} else {
			for _, ip := range ips {
				if a, ok := netip.AddrFromSlice(ip); ok {
					a = a.Unmap()
					if a.IsLoopback() || a.IsUnspecified() || own[a] {
						self = true
					}
				}
			}
		}
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

// lookupHost resolves a name for classify: the system resolver, a second at
// most (a test replaces it).
var lookupHost = func(h string) ([]net.IP, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, h)
	var out []net.IP
	for _, a := range addrs {
		out = append(out, a.IP)
	}
	return out, err
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
