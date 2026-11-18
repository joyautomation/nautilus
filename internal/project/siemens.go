package project

import (
	"fmt"
	"strings"

	"github.com/joyautomation/nautilus/siemens/tiad"
)

// SiemensTarget is a Siemens S7-1500 CPU as a deploy target
// (docs/design/siemens-ide.md): the CPU runs the program instead of the
// nautilus runtime. The writer turns the project into SCL, tiad (the TIA
// Portal Openness agent) compiles and downloads it, and the PLC Web API
// carries live values.
type SiemensTarget struct {
	// Agent is the tiad URL. NAUTILUS_TIAD_URL overrides it. The token is
	// NEVER in the manifest: NAUTILUS_TIAD_TOKEN or NAUTILUS_TIAD_TOKEN_FILE.
	Agent string `yaml:"agent"`
	// Group is the block group nautilus owns in the TIA project. Default
	// "nautilus".
	Group string `yaml:"group"`
	// Station is the CPU's whole hardware config. One CPU has exactly one
	// station spec, because a download replaces the whole config.
	Station SiemensStation `yaml:"station"`
	// Online configures the Web API connection for live values.
	Online *SiemensOnline `yaml:"online"`
}

// SiemensStation is the manifest form of tiad.Station.
type SiemensStation struct {
	CPU       SiemensCPU        `yaml:"cpu"`
	WebServer *SiemensWebServer `yaml:"web-server"`
	Modules   []SiemensModule   `yaml:"modules"`
	Profinet  *SiemensProfinet  `yaml:"profinet"`
}

// SiemensCPU is the controller. In a repo, Address is a documentation
// address; the deploy reads the real one from the environment.
type SiemensCPU struct {
	Name         string `yaml:"name"`
	Order        string `yaml:"order"`
	Firmware     string `yaml:"firmware"`
	Address      string `yaml:"address"`
	SubnetMask   string `yaml:"subnet-mask"`
	Router       string `yaml:"router"`
	ProfinetName string `yaml:"profinet-name"`
}

// SiemensWebServer turns on the CPU's web server. Passwords are never in
// the manifest.
type SiemensWebServer struct {
	Enabled bool             `yaml:"enabled"`
	Users   []SiemensWebUser `yaml:"users"`
}

// SiemensWebUser is a web server user and its rights.
type SiemensWebUser struct {
	Name   string   `yaml:"name"`
	Rights []string `yaml:"rights"`
}

// SiemensModule is a local module next to the CPU.
type SiemensModule struct {
	Slot        int    `yaml:"slot"`
	Order       string `yaml:"order"`
	Firmware    string `yaml:"firmware"`
	InputStart  *int   `yaml:"input-start"`
	OutputStart *int   `yaml:"output-start"`
}

// SiemensProfinet is the CPU's PROFINET IO system.
type SiemensProfinet struct {
	UpdateTime string          `yaml:"update-time"`
	Devices    []SiemensDevice `yaml:"devices"`
}

// SiemensDevice is one IO device, given in one of two forms:
//
//   - explicit: gsdml, dap, name, address and modules;
//   - a reference to a nautilus PROFINET device's own manifest
//     (profinet-manifest + address), which the deploy expands into the
//     explicit form, so the PLC's config and the device's driver read one
//     file and cannot drift.
type SiemensDevice struct {
	ProfinetManifest string                `yaml:"profinet-manifest"`
	GSDML            string                `yaml:"gsdml"`
	DAP              string                `yaml:"dap"`
	Name             string                `yaml:"name"`
	Address          string                `yaml:"address"`
	UpdateTime       string                `yaml:"update-time"`
	Modules          []SiemensDeviceModule `yaml:"modules"`
}

// SiemensDeviceModule places a GSDML module, by its module ID.
type SiemensDeviceModule struct {
	Slot        int    `yaml:"slot"`
	Subslot     int    `yaml:"subslot"`
	Module      string `yaml:"module"`
	InputStart  *int   `yaml:"input-start"`
	OutputStart *int   `yaml:"output-start"`
}

// SiemensOnline is the Web API connection. The password is never in the
// manifest.
type SiemensOnline struct {
	// User is the web server user `naut siemens serve` logs in as.
	User string `yaml:"user"`
	// CertSHA256 pins the CPU's certificate (CPUs ship self-signed ones).
	CertSHA256 string `yaml:"cert-sha256"`
}

// DefaultSiemensGroup is the block group nautilus owns by default.
const DefaultSiemensGroup = "nautilus"

// IsReference reports whether the device is a profinet-manifest reference.
func (d SiemensDevice) IsReference() bool { return d.ProfinetManifest != "" }

// ExplicitStation returns the station as tiad takes it, with only the
// explicit devices. Reference devices are added by the deploy once it has
// expanded them.
func (s SiemensStation) ExplicitStation() tiad.Station {
	c := s.CPU
	st := tiad.Station{CPU: tiad.CPU{
		Name: c.Name, Order: c.Order, Firmware: c.Firmware, Address: c.Address,
		SubnetMask: c.SubnetMask, Router: c.Router, ProfinetName: c.ProfinetName,
	}}
	if w := s.WebServer; w != nil {
		st.WebServer = &tiad.WebServer{Enabled: w.Enabled}
		for _, u := range w.Users {
			st.WebServer.Users = append(st.WebServer.Users, tiad.WebUser{Name: u.Name, Rights: u.Rights})
		}
	}
	for _, m := range s.Modules {
		st.Modules = append(st.Modules, tiad.Module{Slot: m.Slot, Order: m.Order, Firmware: m.Firmware, InputStart: m.InputStart, OutputStart: m.OutputStart})
	}
	if p := s.Profinet; p != nil {
		st.Profinet = &tiad.Profinet{UpdateTime: p.UpdateTime}
		for _, d := range p.Devices {
			if d.IsReference() {
				continue
			}
			td := tiad.Device{GSDML: d.GSDML, DAP: d.DAP, Name: d.Name, Address: d.Address, UpdateTime: d.UpdateTime}
			for _, m := range d.Modules {
				td.Modules = append(td.Modules, tiad.DeviceModule{Slot: m.Slot, Subslot: m.Subslot, Module: m.Module, InputStart: m.InputStart, OutputStart: m.OutputStart})
			}
			st.Profinet.Devices = append(st.Profinet.Devices, td)
		}
	}
	return st
}

// validate checks the target section: one kind at a time, and for Siemens
// the station spec's own rules (tiad.Station.Validate) on its explicit
// part, plus the shape of reference devices. What a reference expands to
// is checked again by the deploy.
func (t *TargetConfig) validate() error {
	if t == nil {
		return nil
	}
	if t.Logix != nil && t.Siemens != nil {
		return fmt.Errorf("target: one kind at a time, not both logix and siemens")
	}
	s := t.Siemens
	if s == nil {
		return nil
	}
	var errs []string
	if p := s.Station.Profinet; p != nil {
		for i, d := range p.Devices {
			if !d.IsReference() {
				continue
			}
			at := fmt.Sprintf("profinet.devices[%d]", i)
			if d.GSDML != "" || d.DAP != "" || d.Name != "" || len(d.Modules) > 0 {
				errs = append(errs, at+": a profinet-manifest reference takes only address and update-time; gsdml, dap, name and modules come from the referenced manifest")
			}
			if _, err := projectPath(d.ProfinetManifest); err != nil {
				errs = append(errs, fmt.Sprintf("%s.profinet-manifest: %v", at, err))
			}
			if d.Address == "" {
				errs = append(errs, at+": a reference needs address: (the host's address, which the device manifest does not hold)")
			}
		}
	}
	if err := s.Station.ExplicitStation().Validate(); err != nil {
		errs = append(errs, err.Error())
	}
	if len(errs) > 0 {
		return fmt.Errorf("target.siemens: %s", strings.Join(errs, "; "))
	}
	return nil
}
