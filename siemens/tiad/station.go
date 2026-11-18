package tiad

import (
	"fmt"
	"net"
	"strings"
	"time"
)

// Station is the whole hardware config of one CPU, declared
// (siemens-ide.md §3.3). Applying it makes the project match: a device or
// module that is not here is removed. One CPU has exactly one Station,
// because a download replaces the whole hardware config.
type Station struct {
	CPU       CPU        `json:"cpu"`
	WebServer *WebServer `json:"webServer,omitempty"`
	Modules   []Module   `json:"modules,omitempty"`
	Profinet  *Profinet  `json:"profinet,omitempty"`
}

// CPU is the controller itself.
type CPU struct {
	Name         string `json:"name"`     // device name in the project, e.g. "PLC_1"
	Order        string `json:"order"`    // order number, e.g. "6ES7 510-1DK03-0AB0"
	Firmware     string `json:"firmware"` // e.g. "V3.1"
	Address      string `json:"address"`  // IPv4 of the PROFINET interface
	SubnetMask   string `json:"subnetMask"`
	Router       string `json:"router,omitempty"`
	ProfinetName string `json:"profinetName"` // the CPU's own name of station
}

// WebServer turns on the CPU's web server, which carries the Web API the
// online plane uses. Passwords travel in Secrets, never here.
type WebServer struct {
	Enabled bool      `json:"enabled"`
	Users   []WebUser `json:"users,omitempty"`
}

// WebUser is one web server user and its rights: "read", "write", "mode"
// (change the operating mode), "diagnostics".
type WebUser struct {
	Name   string   `json:"name"`
	Rights []string `json:"rights"`
}

// Module is a local module next to the CPU.
type Module struct {
	Slot        int    `json:"slot"`
	Order       string `json:"order"`
	Firmware    string `json:"firmware,omitempty"`
	InputStart  *int   `json:"inputStart,omitempty"` // first %I byte; TIA assigns it when nil
	OutputStart *int   `json:"outputStart,omitempty"`
}

// Profinet is the CPU's PROFINET IO system.
type Profinet struct {
	UpdateTime string   `json:"updateTime,omitempty"` // default for devices, e.g. "16ms"
	Devices    []Device `json:"devices,omitempty"`
}

// Device is one IO device from a GSDML file. Its name is always explicit:
// automatic name generation is off, because the device is found on the
// wire by this name.
type Device struct {
	GSDML      string         `json:"gsdml"` // file name, already installed
	DAP        string         `json:"dap"`   // device access point ID in the GSDML
	Name       string         `json:"name"`  // name of station
	Address    string         `json:"address"`
	UpdateTime string         `json:"updateTime,omitempty"`
	Modules    []DeviceModule `json:"modules,omitempty"`
}

// DeviceModule places a GSDML module, found by its module ID, in a slot.
type DeviceModule struct {
	Slot        int    `json:"slot"`
	Subslot     int    `json:"subslot"`
	Module      string `json:"module"`
	InputStart  *int   `json:"inputStart,omitempty"`
	OutputStart *int   `json:"outputStart,omitempty"`
}

// Secrets are the passwords a call needs. They come from the caller's
// environment, never from a manifest, and tiad neither logs nor stores
// them.
type Secrets struct {
	WebUsers   map[string]string `json:"webUsers,omitempty"`
	Protection string            `json:"protection,omitempty"`
}

// MinUpdateTime is the shortest PROFINET update time a spec may ask for.
const MinUpdateTime = 16 * time.Millisecond

// Validate checks the rules tiad enforces before touching TIA, so a bad
// spec fails at the caller with a precise message rather than deep inside
// a TIA call.
func (s Station) Validate() error {
	var errs []string
	bad := func(f string, a ...any) { errs = append(errs, fmt.Sprintf(f, a...)) }

	c := s.CPU
	if c.Name == "" {
		bad("cpu.name is empty")
	}
	if c.Order == "" {
		bad("cpu.order is empty")
	}
	if c.Firmware == "" {
		bad("cpu.firmware is empty")
	}
	if !isIPv4(c.Address) {
		bad("cpu.address %q is not an IPv4 address", c.Address)
	}
	if !isIPv4(c.SubnetMask) {
		bad("cpu.subnetMask %q is not an IPv4 mask", c.SubnetMask)
	}
	if c.Router != "" && !isIPv4(c.Router) {
		bad("cpu.router %q is not an IPv4 address", c.Router)
	}
	if err := checkStationName(c.ProfinetName); err != nil {
		bad("cpu.profinetName: %v", err)
	}

	if w := s.WebServer; w != nil {
		seen := map[string]bool{}
		for i, u := range w.Users {
			if u.Name == "" {
				bad("webServer.users[%d].name is empty", i)
			}
			if seen[u.Name] {
				bad("webServer.users: %q twice", u.Name)
			}
			seen[u.Name] = true
			for _, r := range u.Rights {
				if !knownRight[r] {
					bad("webServer.users[%d]: unknown right %q (one of read, write, mode, diagnostics)", i, r)
				}
			}
		}
	}

	slots := map[int]bool{}
	for i, m := range s.Modules {
		if m.Slot < 1 {
			bad("modules[%d].slot %d: slots start at 1", i, m.Slot)
		}
		if slots[m.Slot] {
			bad("modules: slot %d twice", m.Slot)
		}
		slots[m.Slot] = true
		if m.Order == "" {
			bad("modules[%d].order is empty", i)
		}
		checkStart(bad, fmt.Sprintf("modules[%d]", i), m.InputStart, m.OutputStart)
	}

	if p := s.Profinet; p != nil {
		if p.UpdateTime != "" {
			if err := checkUpdateTime(p.UpdateTime); err != nil {
				bad("profinet.updateTime: %v", err)
			}
		}
		names := map[string]bool{c.ProfinetName: true}
		addrs := map[string]bool{c.Address: true}
		for i, d := range p.Devices {
			at := fmt.Sprintf("profinet.devices[%d]", i)
			if d.GSDML == "" {
				bad("%s.gsdml is empty", at)
			}
			if d.DAP == "" {
				bad("%s.dap is empty", at)
			}
			if err := checkStationName(d.Name); err != nil {
				bad("%s.name: %v", at, err)
			} else if names[d.Name] {
				bad("%s.name %q is already used in this station", at, d.Name)
			}
			names[d.Name] = true
			if !isIPv4(d.Address) {
				bad("%s.address %q is not an IPv4 address", at, d.Address)
			} else if addrs[d.Address] {
				bad("%s.address %s is already used in this station", at, d.Address)
			}
			addrs[d.Address] = true
			if d.UpdateTime != "" {
				if err := checkUpdateTime(d.UpdateTime); err != nil {
					bad("%s.updateTime: %v", at, err)
				}
			}
			sub := map[[2]int]bool{}
			for j, m := range d.Modules {
				mat := fmt.Sprintf("%s.modules[%d]", at, j)
				if m.Slot < 0 || m.Subslot < 1 {
					bad("%s: slot %d subslot %d is out of range", mat, m.Slot, m.Subslot)
				}
				k := [2]int{m.Slot, m.Subslot}
				if sub[k] {
					bad("%s: slot %d subslot %d twice", mat, m.Slot, m.Subslot)
				}
				sub[k] = true
				if m.Module == "" {
					bad("%s.module is empty", mat)
				}
				checkStart(bad, mat, m.InputStart, m.OutputStart)
			}
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("station: %s", strings.Join(errs, "; "))
	}
	return nil
}

var knownRight = map[string]bool{"read": true, "write": true, "mode": true, "diagnostics": true}

func checkStart(bad func(string, ...any), at string, in, out *int) {
	if in != nil && *in < 0 {
		bad("%s.inputStart %d is negative", at, *in)
	}
	if out != nil && *out < 0 {
		bad("%s.outputStart %d is negative", at, *out)
	}
}

func checkUpdateTime(s string) error {
	d, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("%q is not a duration", s)
	}
	if d < MinUpdateTime {
		return fmt.Errorf("%s is below the %s minimum", d, MinUpdateTime)
	}
	return nil
}

func isIPv4(s string) bool {
	ip := net.ParseIP(s)
	return ip != nil && ip.To4() != nil && !strings.Contains(s, ":")
}

// checkStationName applies the PROFINET name-of-station rules (IEC 61158-6
// DNS-style labels): at most 240 characters; labels of 1 to 63 lowercase
// letters, digits and hyphens, not starting or ending with a hyphen; not
// shaped like an IPv4 address; not starting with "port-NNN".
func checkStationName(n string) error {
	if n == "" {
		return fmt.Errorf("empty (names are always explicit)")
	}
	if len(n) > 240 {
		return fmt.Errorf("%q is longer than 240 characters", n)
	}
	for _, label := range strings.Split(n, ".") {
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
	if net.ParseIP(n) != nil {
		return fmt.Errorf("%q has the form of an IP address", n)
	}
	if strings.HasPrefix(n, "port-") && len(n) >= 8 && isDigits(n[5:8]) {
		return fmt.Errorf("%q starts with port-NNN, which PROFINET reserves", n)
	}
	return nil
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}
