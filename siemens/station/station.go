// Package station is the manifest form of a Siemens CPU's hardware config
// (target.siemens.station in nautilus.yaml) and the rules it must obey
// (docs/design/siemens-ide.md §3.3). It imports nothing but the standard
// library, so the manifest loader can hold these types without pulling in
// the agent client, and the rules live in one place: siemens/tiad applies
// the same primitives to its wire form.
//
// Problems never stop a manifest loading: an unfinished station is
// normal while someone types it. `naut check` reports them.
package station

import (
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"
)

// Station is a CPU's whole hardware config.
type Station struct {
	CPU       CPU        `yaml:"cpu"`
	WebServer *WebServer `yaml:"web-server"`
	Modules   []Module   `yaml:"modules"`
	Profinet  *Profinet  `yaml:"profinet"`
}

// CPU is the controller. In a repo, Address is a documentation address.
type CPU struct {
	Name         string `yaml:"name"`
	Order        string `yaml:"order"`
	Firmware     string `yaml:"firmware"`
	Address      string `yaml:"address"`
	SubnetMask   string `yaml:"subnet-mask"`
	Router       string `yaml:"router"`
	ProfinetName string `yaml:"profinet-name"`
}

// WebServer turns on the CPU's web server. Passwords are never here.
type WebServer struct {
	Enabled bool      `yaml:"enabled"`
	Users   []WebUser `yaml:"users"`
}

// WebUser is a web server user and its rights.
type WebUser struct {
	Name   string   `yaml:"name"`
	Rights []string `yaml:"rights"`
}

// Module is a local module next to the CPU.
type Module struct {
	Slot        int    `yaml:"slot"`
	Order       string `yaml:"order"`
	Firmware    string `yaml:"firmware"`
	InputStart  *int   `yaml:"input-start"`
	OutputStart *int   `yaml:"output-start"`
}

// Profinet is the CPU's PROFINET IO system.
type Profinet struct {
	UpdateTime string   `yaml:"update-time"`
	Devices    []Device `yaml:"devices"`
}

// Device is one IO device: explicit (gsdml, dap, name, address, modules),
// or a reference to a nautilus PROFINET device's own manifest
// (profinet-manifest + address), which the deploy expands so the PLC's
// config and the device's driver read one file.
type Device struct {
	ProfinetManifest string         `yaml:"profinet-manifest"`
	GSDML            string         `yaml:"gsdml"`
	DAP              string         `yaml:"dap"`
	Name             string         `yaml:"name"`
	Address          string         `yaml:"address"`
	UpdateTime       string         `yaml:"update-time"`
	Modules          []DeviceModule `yaml:"modules"`
}

// IsReference reports whether the device is a profinet-manifest reference.
func (d Device) IsReference() bool { return d.ProfinetManifest != "" }

// DeviceModule places a GSDML module by its module ID.
type DeviceModule struct {
	Slot        int    `yaml:"slot"`
	Subslot     int    `yaml:"subslot"`
	Module      string `yaml:"module"`
	InputStart  *int   `yaml:"input-start"`
	OutputStart *int   `yaml:"output-start"`
}

// Problem is one rule a station breaks. Path is the YAML path below the
// station ("cpu.subnet-mask", "profinet.devices[1].name").
type Problem struct {
	Path    string
	Message string
}

func (p Problem) String() string { return p.Path + ": " + p.Message }

// Problems lists every rule the station breaks, in manifest order.
func (s Station) Problems() []Problem {
	var ps []Problem
	bad := func(p, f string, a ...any) { ps = append(ps, Problem{Path: p, Message: fmt.Sprintf(f, a...)}) }

	c := s.CPU
	for _, f := range []struct{ key, val string }{{"name", c.Name}, {"order", c.Order}, {"firmware", c.Firmware}} {
		if f.val == "" {
			bad("cpu."+f.key, "is empty")
		}
	}
	if !IsIPv4(c.Address) {
		bad("cpu.address", "%q is not an IPv4 address", c.Address)
	}
	if !IsIPv4(c.SubnetMask) {
		bad("cpu.subnet-mask", "%q is not an IPv4 mask", c.SubnetMask)
	}
	if c.Router != "" && !IsIPv4(c.Router) {
		bad("cpu.router", "%q is not an IPv4 address", c.Router)
	}
	if err := CheckStationName(c.ProfinetName); err != nil {
		bad("cpu.profinet-name", "%v", err)
	}

	if w := s.WebServer; w != nil {
		seen := map[string]bool{}
		for i, u := range w.Users {
			at := fmt.Sprintf("web-server.users[%d]", i)
			if u.Name == "" {
				bad(at+".name", "is empty")
			} else if seen[u.Name] {
				bad(at+".name", "%q twice", u.Name)
			}
			seen[u.Name] = true
			for _, r := range u.Rights {
				if !KnownRights[r] {
					bad(at+".rights", "unknown right %q (one of read, write, mode, diagnostics)", r)
				}
			}
		}
	}

	slots := map[int]bool{}
	for i, m := range s.Modules {
		at := fmt.Sprintf("modules[%d]", i)
		if m.Slot < 0 || m.Slot == 1 {
			bad(at+".slot", "%d: slot 1 is the CPU's own; modules go in slot 2 and up", m.Slot)
		} else if slots[m.Slot] {
			bad(at+".slot", "slot %d twice", m.Slot)
		}
		slots[m.Slot] = true
		if m.Order == "" {
			bad(at+".order", "is empty")
		}
		starts(bad, at, m.InputStart, m.OutputStart)
	}

	if p := s.Profinet; p != nil {
		if p.UpdateTime != "" {
			if err := CheckUpdateTime(p.UpdateTime); err != nil {
				bad("profinet.update-time", "%v", err)
			}
		}
		names := map[string]bool{c.ProfinetName: true}
		addrs := map[string]string{c.Address: "the CPU"}
		for i, d := range p.Devices {
			at := fmt.Sprintf("profinet.devices[%d]", i)
			if !IsIPv4(d.Address) {
				bad(at+".address", "%q is not an IPv4 address", d.Address)
			} else if owner, used := addrs[d.Address]; used {
				bad(at+".address", "%s is already %s's", d.Address, owner)
			} else {
				addrs[d.Address] = at
			}
			if d.UpdateTime != "" {
				if err := CheckUpdateTime(d.UpdateTime); err != nil {
					bad(at+".update-time", "%v", err)
				}
			}
			if d.IsReference() {
				if d.GSDML != "" || d.DAP != "" || d.Name != "" || len(d.Modules) > 0 {
					bad(at, "a profinet-manifest reference takes only address and update-time; gsdml, dap, name and modules come from the referenced manifest")
				}
				if c := path.Clean(d.ProfinetManifest); !fs.ValidPath(c) {
					bad(at+".profinet-manifest", "%q is outside the project", d.ProfinetManifest)
				}
				continue
			}
			if d.GSDML == "" {
				bad(at+".gsdml", "is empty")
			}
			if d.DAP == "" {
				bad(at+".dap", "is empty")
			}
			if err := CheckStationName(d.Name); err != nil {
				bad(at+".name", "%v", err)
			} else if names[d.Name] {
				bad(at+".name", "%q is already used in this station", d.Name)
			}
			names[d.Name] = true
			sub := map[[2]int]bool{}
			for j, m := range d.Modules {
				mat := fmt.Sprintf("%s.modules[%d]", at, j)
				if m.Slot < 0 || m.Subslot < 1 {
					bad(mat, "slot %d subslot %d is out of range", m.Slot, m.Subslot)
				}
				k := [2]int{m.Slot, m.Subslot}
				if sub[k] {
					bad(mat, "slot %d subslot %d twice", m.Slot, m.Subslot)
				}
				sub[k] = true
				if m.Module == "" {
					bad(mat+".module", "is empty")
				}
				starts(bad, mat, m.InputStart, m.OutputStart)
			}
		}
	}
	return ps
}

func starts(bad func(string, string, ...any), at string, in, out *int) {
	if in != nil && *in < 0 {
		bad(at+".input-start", "%d is negative", *in)
	}
	if out != nil && *out < 0 {
		bad(at+".output-start", "%d is negative", *out)
	}
}

// KnownRights are the web server rights a user may hold.
var KnownRights = map[string]bool{"read": true, "write": true, "mode": true, "diagnostics": true}

// MinUpdateTime is the shortest PROFINET update time a spec may ask for.
const MinUpdateTime = 16 * time.Millisecond

// CheckUpdateTime parses a duration and applies MinUpdateTime.
func CheckUpdateTime(s string) error {
	d, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("%q is not a duration", s)
	}
	if d < MinUpdateTime {
		return fmt.Errorf("%s is below the %s minimum", d, MinUpdateTime)
	}
	return nil
}

// IsIPv4 reports whether s is a dotted-quad IPv4 address.
func IsIPv4(s string) bool {
	ip := net.ParseIP(s)
	return ip != nil && ip.To4() != nil && !strings.Contains(s, ":")
}

// reservedPort is the first label PROFINET reserves for port names:
// port-xyz or port-xyz-abcde, with digits.
var reservedPort = regexp.MustCompile(`^port-[0-9]{3}(-[0-9]{5})?$`)

// CheckStationName applies the PROFINET name-of-station rules: at most
// 240 characters; DNS-style labels of 1 to 63 lowercase letters, digits
// and hyphens, not starting or ending with a hyphen; not of the form
// n.n.n.n (n = 0-999); no first label port-xyz or port-xyz-abcde.
func CheckStationName(n string) error {
	if n == "" {
		return fmt.Errorf("empty (names are always explicit)")
	}
	if len(n) > 240 {
		return fmt.Errorf("%q is longer than 240 characters", n)
	}
	labels := strings.Split(n, ".")
	for _, label := range labels {
		if label == "" || len(label) > 63 {
			return fmt.Errorf("%q has a label that is empty or longer than 63 characters", n)
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return fmt.Errorf("%q has a label starting or ending with a hyphen", n)
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
				return fmt.Errorf("%q: only lowercase letters, digits, hyphens and dots are allowed", n)
			}
		}
	}
	if ipShaped(labels) {
		return fmt.Errorf("%q has the form n.n.n.n (n = 0-999), which PROFINET forbids", n)
	}
	if reservedPort.MatchString(labels[0]) {
		return fmt.Errorf("%q: a first label of the form port-xyz or port-xyz-abcde is reserved by PROFINET", n)
	}
	return nil
}

func ipShaped(labels []string) bool {
	if len(labels) != 4 {
		return false
	}
	for _, l := range labels {
		if len(l) < 1 || len(l) > 3 {
			return false
		}
		for _, r := range l {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}

// CheckAgentURL requires an http or https URL with a host and no user
// info: the agent's token is never in a manifest, and neither is any other
// credential.
func CheckAgentURL(s string) error {
	u, err := url.Parse(s)
	if err != nil {
		// url's own error quotes the input, which may hold a secret.
		return fmt.Errorf("is not an http(s) URL with a host")
	}
	if u.User != nil {
		// Never echo the user info: it is the secret being refused.
		u.User = nil
		return fmt.Errorf("carries user info (%s with the credentials removed); credentials never go in the manifest (set NAUTILUS_TIAD_TOKEN)", u)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("%q is not an http(s) URL with a host", s)
	}
	return nil
}

var sha256Hex = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

// CheckCertSHA256 requires 64 hex digits, colons allowed between bytes.
func CheckCertSHA256(s string) error {
	if !sha256Hex.MatchString(strings.ReplaceAll(s, ":", "")) {
		return fmt.Errorf("%q is not a SHA-256 in hex", s)
	}
	return nil
}
