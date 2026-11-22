package main

import (
	"fmt"
	"io/fs"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"

	"github.com/joyautomation/nautilus/checkout"
	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/virtdev"
)

// checkoutEvidence is what a seal asks the controller about its own devices
// (#719 slice 4): a run is signed off only against real field I/O, and the
// run's rehearsal flag is the client's, so the controller works out for itself
// whether anything physical is behind the points. It reads the manifest it
// runs. It refuses when
//
//   - no field driver is configured (the controller runs the in-memory
//     loopback, so nothing was checked),
//   - every configured endpoint is a loopback or served by the project's own
//     virtual devices, or
//   - a point's device is a devices: row whose endpoint is.
//
// What it saw goes in the sealed record. A controller built from a project
// takes the manifest from what it embeds; the virtual devices are fixtures
// that do not ride along, so only the loopback rule applies there.
func checkoutEvidence(fsys fs.FS, manifest, dir string) func(*checkout.Sheet) (checkout.Evidence, string) {
	return func(sh *checkout.Sheet) (checkout.Evidence, string) {
		m, err := project.ReadManifest(fsys, manifest)
		if err != nil {
			return checkout.Evidence{}, "the manifest could not be read to say what the controller is connected to: " + err.Error()
		}
		virtual := virtualListens(dir)
		return deriveEvidence(m, sh, virtual)
	}
}

// virtualListens is the addresses the project's own virtual devices answer on
// (virtual-devices.yaml and the devices: rows), so a driver pointed at one is
// known for a virtual device. Empty when there are none or no project dir.
func virtualListens(dir string) map[string]bool {
	out := map[string]bool{}
	if dir == "" {
		return out
	}
	if _, err := os.Stat(filepath.Join(dir, virtdev.FileName)); err != nil {
		return out
	}
	set, err := virtdev.Compose(dir, nil)
	if err != nil || set == nil {
		return out
	}
	for _, in := range set.Devices {
		// the address a virtual device LISTENS on: its Endpoint is the real
		// device's address (what the controller dials), not a virtual one
		if in.Listen != "" {
			out[hostOf(in.Listen)+"|"+portOf(in.Listen)] = true
		}
	}
	return out
}

func deriveEvidence(m *project.Manifest, sh *checkout.Sheet, virtual map[string]bool) (checkout.Evidence, string) {
	var ev checkout.Evidence
	drivers := append([]project.DriverConfig(nil), m.Drivers...)
	if m.Driver.Type != "" {
		drivers = append(drivers, m.Driver)
	}
	real := 0
	notReal := 0
	for _, d := range drivers {
		name := d.Name
		if name == "" {
			name = d.Type
		}
		ev.Drivers = append(ev.Drivers, checkout.DriverEvidence{Name: name, Kind: d.Type})
		switch strings.ToLower(d.Type) {
		case "", "memory", "loopback":
			continue
		}
		real++
		for _, ep := range []string{d.Host, d.Endpoint, d.Broker} {
			if ep == "" {
				continue
			}
			cls := classify(ep, virtual)
			ev.Devices = append(ev.Devices, checkout.DeviceEvidence{Name: name, Endpoint: ep, Class: cls})
			if cls != "network" {
				notReal++
			}
		}
	}
	byName := map[string]string{}
	for _, d := range m.Devices {
		cls := classify(d.Endpoint, virtual)
		byName[d.Name] = cls
		ev.Devices = append(ev.Devices, checkout.DeviceEvidence{Name: d.Name, Endpoint: d.Endpoint, Class: cls})
	}
	if real == 0 && len(m.Devices) == 0 {
		return ev, "this controller has no field driver configured (it runs the in-memory loopback): nothing physical was checked"
	}
	for _, p := range sh.Points {
		if cls, ok := byName[p.Device]; ok && cls != "network" {
			return ev, fmt.Sprintf("point %s is on device %s, which this controller reaches at a %s address: a checkout against it proves nothing about the plant", p.ID, p.Device, cls)
		}
	}
	// every endpoint the controller dials is a loopback or a virtual device
	total := 0
	bad := 0
	for _, d := range ev.Devices {
		total++
		if d.Class != "network" {
			bad++
		}
	}
	if total > 0 && bad == total {
		return ev, "every device this controller dials is a loopback or a virtual device: a checkout against them proves nothing about the plant"
	}
	return ev, ""
}

// classify says what an endpoint is: loopback (localhost, 127.x, ::1),
// virtual (one the project's own virtual devices answer on) or network.
func classify(endpoint string, virtual map[string]bool) string {
	h, p := hostOf(endpoint), portOf(endpoint)
	if virtual[h+"|"+p] {
		return "virtual"
	}
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return "loopback"
	}
	if a, err := netip.ParseAddr(h); err == nil && (a.IsLoopback() || a.IsUnspecified()) {
		return "loopback"
	}
	return "network"
}

// hostOf and portOf read a host[:port], a scheme://host[:port] or a [v6]:port.
func hostOf(ep string) string {
	if i := strings.Index(ep, "://"); i >= 0 {
		ep = ep[i+3:]
	}
	if i := strings.IndexAny(ep, "/?"); i >= 0 {
		ep = ep[:i]
	}
	if h, _, err := net.SplitHostPort(ep); err == nil {
		return strings.ToLower(h)
	}
	return strings.ToLower(strings.Trim(ep, "[]"))
}

func portOf(ep string) string {
	if i := strings.Index(ep, "://"); i >= 0 {
		ep = ep[i+3:]
	}
	if i := strings.IndexAny(ep, "/?"); i >= 0 {
		ep = ep[:i]
	}
	if _, p, err := net.SplitHostPort(ep); err == nil {
		return p
	}
	return ""
}
