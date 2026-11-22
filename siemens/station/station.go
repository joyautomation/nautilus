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
	"unicode/utf16"
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
	// ConfigurationProtection is the FW 3.1 protection of the CPU's
	// confidential configuration data: password (the default, its
	// password from the secret store), none, or keep (leave the
	// project's setting as found, for an adopted station). Empty means
	// the default.
	ConfigurationProtection string `yaml:"configuration-protection"`
	// AccessControl is the CPU's PLC access control (users and roles):
	// full-access-user (the default: access control on, one user
	// "nautilus" with the runtime right Full access, its password from the
	// secret store) or keep (leave the project's access control and users
	// as found, for an adopted station). Empty means the default.
	AccessControl string `yaml:"access-control"`
}

// WebServer turns on the CPU's web server, or (enabled false) turns it off;
// left out, a kept CPU's stays as found. tiad turns it on only over access
// control on. Passwords are never here.
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
	if c.ConfigurationProtection != "" {
		if err := CheckConfigurationProtection(c.ConfigurationProtection); err != nil {
			bad("cpu.configuration-protection", "%v", err)
		}
	}
	if c.AccessControl != "" {
		if _, err := WireAccessControl(c.AccessControl); err != nil {
			bad("cpu.access-control", "%v", err)
		}
	}

	if w := s.WebServer; w != nil {
		if !w.Enabled && len(w.Users) > 0 {
			bad("web-server.users", "is set and web-server.enabled is false: a web server that is off has no users")
		}
		// TIA's user names may ignore case: so do duplicates.
		seen := map[string]bool{}
		for i, u := range w.Users {
			at := fmt.Sprintf("web-server.users[%d]", i)
			if u.Name == "" {
				bad(at+".name", "is empty")
			} else if err := CheckWebUserName(u.Name); err != nil {
				bad(at+".name", "%v", err)
			} else if seen[strings.ToLower(u.Name)] {
				bad(at+".name", "%q twice (names are compared ignoring case)", u.Name)
			}
			seen[strings.ToLower(u.Name)] = true
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

// MaxWebUserName is the longest web user name.
const MaxWebUserName = 32

// CheckWebUserName applies the web user name rule: 1 to 32 ASCII letters,
// digits, '_', '-' and '.', starting with a letter (narrower than TIA's,
// so TIA never refuses one part-way through an apply), and never a name
// tiad or TIA keeps for itself, in any case: "nautilus" (tiad's Full
// access user) or "Anonymous" (TIA's anonymous user).
func CheckWebUserName(n string) error {
	letter := func(r byte) bool { return r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' }
	ok := len(n) >= 1 && len(n) <= MaxWebUserName && letter(n[0])
	for i := 0; ok && i < len(n); i++ {
		r := n[i]
		ok = letter(r) || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.'
	}
	if !ok {
		return fmt.Errorf("%q: a web user name is 1 to 32 characters: letters, digits, '_', '-' and '.', starting with a letter", n)
	}
	if strings.EqualFold(n, "nautilus") || strings.EqualFold(n, "Anonymous") {
		return fmt.Errorf(`%q is reserved (tiad's Full access user is "nautilus", TIA's anonymous user is "Anonymous")`, n)
	}
	return nil
}

// KnownRights are the web server rights a user may hold.
var KnownRights = map[string]bool{"read": true, "write": true, "mode": true, "diagnostics": true}

// The FW 3.1 configuration protection a station asks for
// (siemens-ide.md §3.3).
const (
	// ProtectPassword protects the CPU's confidential configuration data
	// with a password from the secret store. A new station's default.
	ProtectPassword = "password"
	// ProtectNone turns the protection off: an explicit choice.
	ProtectNone = "none"
	// ProtectKeep leaves the project's setting as it was found.
	ProtectKeep = "keep"
)

// DefaultConfigurationProtection is what a manifest that names none gets.
const DefaultConfigurationProtection = ProtectPassword

// CheckConfigurationProtection accepts password, none and keep.
func CheckConfigurationProtection(v string) error {
	switch v {
	case ProtectPassword, ProtectNone, ProtectKeep:
		return nil
	}
	return fmt.Errorf("%q is not one of password, none, keep", v)
}

// The access control values on the wire (tiad's cpu.accessControl).
const (
	// AccessFullAccessUser turns PLC access control on with one user,
	// "nautilus", that has the runtime right Full access; its password
	// comes from the secret store.
	AccessFullAccessUser = "fullAccessUser"
	// AccessKeep leaves the project's access control and users as found.
	AccessKeep = "keep"
)

// DefaultAccessControl is what a manifest that names none gets: a station
// naut makes has access control on (#789, decision of 2026-11-20).
const DefaultAccessControl = AccessFullAccessUser

// CheckAccessControl accepts the wire values fullAccessUser and keep.
func CheckAccessControl(v string) error {
	switch v {
	case AccessFullAccessUser, AccessKeep:
		return nil
	}
	return fmt.Errorf("%q is not one of fullAccessUser, keep", v)
}

// WireAccessControl turns the manifest's access-control (full-access-user
// or keep; empty is the default) into the wire value.
func WireAccessControl(v string) (string, error) {
	switch v {
	case "":
		return DefaultAccessControl, nil
	case "full-access-user":
		return AccessFullAccessUser, nil
	case "keep":
		return AccessKeep, nil
	}
	return "", fmt.Errorf("%q is not one of full-access-user, keep", v)
}

// TIA's password rules (TIA Portal V21, as it words them when it refuses
// one). Lengths count UTF-16 code units, as .NET does; the classes are
// ASCII, which can only be stricter than TIA.
const (
	MinTiaPassword = 10
	MaxTiaPassword = 120
)

// CheckTiaPassword applies TIA's password rules: 10 to 120 characters,
// at least one digit and one upper-case and one lower-case letter. The
// error names every rule broken and NEVER echoes the password.
func CheckTiaPassword(pw string) error {
	var broken []string
	n := len(utf16.Encode([]rune(pw)))
	if n < MinTiaPassword {
		broken = append(broken, "shorter than 10 characters")
	} else if n > MaxTiaPassword {
		broken = append(broken, "longer than 120 characters")
	}
	var digit, upper, lower bool
	for _, r := range pw {
		digit = digit || r >= '0' && r <= '9'
		upper = upper || r >= 'A' && r <= 'Z'
		lower = lower || r >= 'a' && r <= 'z'
	}
	if !digit {
		broken = append(broken, "no digit")
	}
	if !upper {
		broken = append(broken, "no upper-case letter (A-Z)")
	}
	if !lower {
		broken = append(broken, "no lower-case letter (a-z)")
	}
	if len(broken) == 0 {
		return nil
	}
	return fmt.Errorf("breaks TIA's password rules (10 to 120 characters, at least one digit, one upper-case and one lower-case letter): %s", strings.Join(broken, ", "))
}

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

// CheckAgentURL requires an http or https URL with a host, and nothing
// that could carry a credential: no user info, no query, no fragment. The
// agent's token is never in a manifest, and neither is any other secret.
//
// The error NEVER echoes the value: a mistyped URL can carry a password in
// a place the parser does not recognise as one (admin:s3cret@host without
// a scheme, http//u:s3cret@h). At most it names scheme://host of a URL that
// parsed cleanly.
func CheckAgentURL(s string) error {
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Opaque != "" {
		return fmt.Errorf("is not an http(s) URL with a host (e.g. http://127.0.0.1:8190)")
	}
	safe := u.Scheme + "://" + u.Host
	if u.User != nil {
		return fmt.Errorf("carries user info (%s…); credentials never go in the manifest (set NAUTILUS_TIAD_TOKEN)", safe)
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return fmt.Errorf("has a query or fragment (%s…); the agent URL takes neither, and credentials never go in the manifest (set NAUTILUS_TIAD_TOKEN)", safe)
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
