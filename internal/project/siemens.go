package project

import (
	"errors"
	"fmt"

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
	// BlockPrefix goes in front of every block, DB and tag table nautilus
	// generates ("naut_Main", "naut_Tags"), so a generated block never
	// collides with TIA's own ("Main" [OB1]) or a customer's. Default
	// "naut_". An explicit empty prefix is refused by check for now.
	BlockPrefix *string `yaml:"block-prefix"`
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

// DefaultSiemensBlockPrefix is the block-name prefix when block-prefix is unset.
const DefaultSiemensBlockPrefix = "naut_"

// Prefix is the block-name prefix in effect: block-prefix, or the default
// when it is unset (or t is nil). An explicit empty prefix is returned
// as is; Problems refuses it.
func (t *SiemensTarget) Prefix() string {
	if t == nil || t.BlockPrefix == nil {
		return DefaultSiemensBlockPrefix
	}
	return *t.BlockPrefix
}

// CheckBlockPrefix says why a block-name prefix cannot be used: it must
// be non-empty and begin an SCL identifier (a letter or underscore, then
// letters, digits and underscores).
func CheckBlockPrefix(p string) error {
	if p == "" {
		return errors.New("empty: a generated block named like the program (\"Main\") collides with TIA's own OB1; leave block-prefix out for the default " + DefaultSiemensBlockPrefix)
	}
	for i, r := range p {
		letter := r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
		if !letter && !(i > 0 && r >= '0' && r <= '9') {
			return fmt.Errorf("%q is not a valid SCL identifier start: use letters, digits and _, not starting with a digit", p)
		}
	}
	return nil
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
	if s.BlockPrefix != nil {
		if err := CheckBlockPrefix(*s.BlockPrefix); err != nil {
			ps = append(ps, station.Problem{Path: "target.siemens.block-prefix", Message: err.Error()})
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
