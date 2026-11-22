package tiad

import (
	"fmt"
	"sort"
	"strings"

	"github.com/joyautomation/nautilus/siemens/station"
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
	// ConfigurationProtection is required: "password", "none" or "keep"
	// (siemens-ide.md §3.3). Its password travels in
	// Secrets.ConfigurationPassword.
	ConfigurationProtection string `json:"configurationProtection"`
	// AccessControl is required: "fullAccessUser" or "keep"
	// (siemens-ide.md §3.3). The user's password travels in
	// Secrets.FullAccessPassword.
	AccessControl string `json:"accessControl"`
}

// The configuration protection values.
const (
	ProtectPassword = station.ProtectPassword
	ProtectNone     = station.ProtectNone
	ProtectKeep     = station.ProtectKeep
)

// The access control values.
const (
	AccessFullAccessUser = station.AccessFullAccessUser
	AccessKeep           = station.AccessKeep
)

// WebServer turns on the CPU's web server, which carries the Web API the
// online plane uses, or (Enabled false) turns it off; a station without one
// keeps a kept CPU's web server as found and a new CPU's off. tiad turns it
// on only over access control EnabledWithAccessControl. Passwords travel in
// Secrets, never here.
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
	// ConfigurationPassword protects the CPU's confidential configuration
	// data (cpu.configurationProtection). It is its own secret: never the
	// protection password, never a web user's.
	ConfigurationPassword string `json:"configurationPassword,omitempty"`
	// FullAccessPassword is the password of the CPU's user with the
	// runtime right Full access (cpu.accessControl). It is its own secret:
	// never the protection or configuration password, never a web user's.
	FullAccessPassword string `json:"fullAccessPassword,omitempty"`
}

// CheckFor applies what st needs from the secrets before TIA hears of
// them: when st protects the configuration, a configuration password that
// meets TIA's password rules, since it may be set; when st has a Full
// access user, that user's password, by the same rules (TIA's default
// policy for local users; tiad checks the project's own before any
// write). A password that only unlocks (none, or a download) is never
// checked: it was set under whatever rules TIA had then. The error never
// echoes a password.
func (s Secrets) CheckFor(st Station) error {
	var errs []string
	if st.CPU.ConfigurationProtection == ProtectPassword {
		if s.ConfigurationPassword == "" {
			errs = append(errs, "cpu.configurationProtection is password, and secrets.configurationPassword is empty (it comes from the secret store, never the spec)")
		} else if err := checkTiaPassword(s.ConfigurationPassword); err != nil {
			errs = append(errs, fmt.Sprintf("secrets.configurationPassword %v", err))
		}
	}
	if st.CPU.AccessControl == AccessFullAccessUser {
		if s.FullAccessPassword == "" {
			errs = append(errs, "cpu.accessControl is fullAccessUser, and secrets.fullAccessPassword is empty (it comes from the secret store, never the spec)")
		} else {
			if err := checkTiaPassword(s.FullAccessPassword); err != nil {
				errs = append(errs, fmt.Sprintf("secrets.fullAccessPassword %v", err))
			}
			same := ""
			switch s.FullAccessPassword {
			case s.ConfigurationPassword:
				same = "secrets.configurationPassword"
			case s.Protection:
				same = "secrets.protection"
			default:
				names := make([]string, 0, len(s.WebUsers))
				for n := range s.WebUsers {
					names = append(names, n)
				}
				sort.Strings(names)
				for _, n := range names {
					if s.WebUsers[n] == s.FullAccessPassword {
						same = fmt.Sprintf("secrets.webUsers[%q]", n)
						break
					}
				}
			}
			if same != "" {
				errs = append(errs, "secrets.fullAccessPassword is the same as "+same+": each password is its own secret")
			}
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("%s", strings.Join(errs, "; "))
}

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
	if c.ConfigurationProtection == "" {
		bad("cpu.configurationProtection is empty (one of password, none, keep)")
	} else if err := checkConfigurationProtection(c.ConfigurationProtection); err != nil {
		bad("cpu.configurationProtection: %v", err)
	}
	if c.AccessControl == "" {
		bad("cpu.accessControl is empty (one of fullAccessUser, keep)")
	} else if err := checkAccessControl(c.AccessControl); err != nil {
		bad("cpu.accessControl: %v", err)
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
		// On an ET 200SP and an S7-1500 the CPU itself is slot 1 (an
		// S7-1500 power supply can be slot 0) (verify on the S2a upload).
		if m.Slot < 0 || m.Slot == 1 {
			bad("modules[%d].slot %d: slot 1 is the CPU's own; modules go in slot 2 and up", i, m.Slot)
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

func checkStart(bad func(string, ...any), at string, in, out *int) {
	if in != nil && *in < 0 {
		bad("%s.inputStart %d is negative", at, *in)
	}
	if out != nil && *out < 0 {
		bad("%s.outputStart %d is negative", at, *out)
	}
}

// The rule primitives are siemens/station's, so the manifest and the wire
// form obey one set of rules.
var (
	knownRight       = station.KnownRights
	checkUpdateTime  = station.CheckUpdateTime
	isIPv4           = station.IsIPv4
	checkStationName = station.CheckStationName

	checkConfigurationProtection = station.CheckConfigurationProtection
	checkAccessControl           = station.CheckAccessControl
	checkTiaPassword             = station.CheckTiaPassword
)

// MinUpdateTime is the shortest PROFINET update time a spec may ask for.
const MinUpdateTime = station.MinUpdateTime
