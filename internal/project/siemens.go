package project

import (
	"github.com/joyautomation/nautilus/siemens/station"
)

// SiemensTarget is a Siemens S7-1500 CPU as a deploy target
// (docs/design/siemens-ide.md): the CPU runs the program instead of the
// nautilus runtime. The writer turns the project into SCL, tiad (the TIA
// Portal Openness agent) compiles and downloads it, and the PLC Web API
// carries live values.
//
// Loading never fails on the station's content: an unfinished station is
// normal while it is being typed, and failing ReadManifest would drop
// every tag from the editor. Problems reports what `naut check` flags.
type SiemensTarget struct {
	// Agent is the tiad URL. NAUTILUS_TIAD_URL overrides it. The token is
	// NEVER in the manifest: NAUTILUS_TIAD_TOKEN or NAUTILUS_TIAD_TOKEN_FILE.
	Agent string `yaml:"agent"`
	// Group is the block group nautilus owns in the TIA project. Default
	// "nautilus".
	Group string `yaml:"group"`
	// Station is the CPU's whole hardware config. One CPU has exactly one
	// station spec, because a download replaces the whole config.
	Station station.Station `yaml:"station"`
	// Online configures the Web API connection for live values.
	Online *SiemensOnline `yaml:"online"`
}

// SiemensOnline is the Web API connection. The password is never in the
// manifest: NAUTILUS_SIEMENS_PASSWORD.
type SiemensOnline struct {
	// User is the web server user `naut siemens serve` logs in as.
	User string `yaml:"user"`
	// CertSHA256 pins the CPU's certificate (CPUs ship self-signed ones).
	CertSHA256 string `yaml:"cert-sha256"`
}

// DefaultSiemensGroup is the block group nautilus owns by default.
const DefaultSiemensGroup = "nautilus"

// Problems lists what the target section breaks, with YAML paths from
// the manifest root ("target.siemens.station.cpu.subnet-mask").
func (t *TargetConfig) Problems() []station.Problem {
	if t == nil {
		return nil
	}
	var ps []station.Problem
	if t.Logix != nil && t.Siemens != nil {
		ps = append(ps, station.Problem{Path: "target", Message: "one kind at a time, not both logix and siemens"})
	}
	s := t.Siemens
	if s == nil {
		return ps
	}
	if s.Agent != "" {
		if err := station.CheckAgentURL(s.Agent); err != nil {
			ps = append(ps, station.Problem{Path: "target.siemens.agent", Message: err.Error()})
		}
	}
	if o := s.Online; o != nil && o.CertSHA256 != "" {
		if err := station.CheckCertSHA256(o.CertSHA256); err != nil {
			ps = append(ps, station.Problem{Path: "target.siemens.online.cert-sha256", Message: err.Error()})
		}
	}
	for _, p := range s.Station.Problems() {
		p.Path = "target.siemens.station." + p.Path
		ps = append(ps, p)
	}
	return ps
}
