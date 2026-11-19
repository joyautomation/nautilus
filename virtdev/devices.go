package virtdev

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/devprofile"
	"gopkg.in/yaml.v3"
)

// FileName is the project file that lists a project's virtual devices.
const FileName = "virtual-devices.yaml"

// Set is a project's virtual devices and its process model: virtual-devices.yaml.
type Set struct {
	Devices []Instance `yaml:"devices"`
	// Model is the process model: what the devices are connected to.
	Model *Model `yaml:"model"`
	// Scan is how often the simulators run; 0 means 50ms.
	Scan Duration `yaml:"scan"`

	// Dir is the project directory virtual-devices.yaml was read from: profile
	// paths and the model program resolve against it.
	Dir string `yaml:"-"`
	// profiles holds each instance's resolved profile, by instance name.
	profiles map[string]*devprofile.Profile
	// declared names (lower case) the instances that come from the
	// project's devices: rows (Compose).
	declared map[string]bool
}

// Instance is one virtual device. The entry's shape is the one a project
// will declare its real devices with once profiles are part of
// nautilus.yaml (#717): a name, a profile, the address the project's
// driver dials. What only a simulation needs (listen, init) sits beside it.
type Instance struct {
	// Name is the device's IEC identifier; its points are plant tags
	// <Name>_<Point>.
	Name string `yaml:"name"`
	// Profile is a built-in profile id or a path to a project's own.
	Profile string `yaml:"profile"`
	// Endpoint is the address the project's driver dials for the real
	// device ("192.168.10.21:502"): the in-process face answers there, and
	// `naut run --devices` redirects that address to the wire face.
	Endpoint string `yaml:"endpoint"`
	// UnitID overrides the profile's Modbus unit id.
	UnitID *uint8 `yaml:"unit-id"`
	// Listen is where the wire face listens ("127.0.0.1:15020"); empty
	// means an ephemeral loopback port.
	Listen string `yaml:"listen"`
	// Init overrides points' power-up values, and seeds simulator hooks.
	Init map[string]any `yaml:"init"`
	// Params sets the profile's params ({location} in an ifm master's
	// node ids).
	Params map[string]string `yaml:"params"`
	// OPCUA is an OPC UA device's simulation-only server settings.
	OPCUA *InstanceOPCUA `yaml:"opcua"`
	// Ports are an IO-Link master's ports that have a device on them, by
	// number: the master answers ISDU for each from its IODD (isdu.go).
	Ports map[int]InstancePort `yaml:"ports"`
}

// InstancePort is the device a virtual IO-Link master simulates on a port.
type InstancePort struct {
	// IODD is the device's IODD, a file name in the project's iodd/ folder.
	IODD string `yaml:"iodd"`
}

// InstanceOPCUA is how an OPC UA virtual device's wire face serves.
type InstanceOPCUA struct {
	// PKI is a certificate store (relative to the project) with which the
	// wire face also offers the secure endpoints, SignAndEncrypt, to a
	// trusted client certificate, as a device configured secure does: a
	// project's row that connects through a secured driver: section is
	// tested against it. Empty serves None alone, as the profiles document.
	PKI string `yaml:"pki"`
}

// Model is the process model: an ST PROGRAM, run as its own task in the
// plant, that reads what the devices command (a drive's speed, a relay)
// and writes what they sense (a level, a plug switch).
type Model struct {
	Program string   `yaml:"program"`
	Scan    Duration `yaml:"scan"`
}

// Duration parses "100ms" / "2s".
type Duration time.Duration

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	v, err := time.ParseDuration(n.Value)
	if err != nil {
		return fmt.Errorf("line %d: %w", n.Line, err)
	}
	*d = Duration(v)
	return nil
}

// LoadSet reads virtual-devices.yaml from dir and resolves every profile.
func LoadSet(dir string) (*Set, error) { return Compose(dir, nil) }

// ManifestName is the project manifest that declares a project's real
// devices (devices:); virtdev never reads it, the CLI hands its rows over.
const ManifestName = "nautilus.yaml"

// Compose builds a project's virtual devices: one per device the project
// declares in nautilus.yaml's devices: (declared, handed over by the CLI:
// virtual devices import no manifest), plus virtual-devices.yaml's own
// when the file is there.
//
// A declared device simulates the profile the project vendored
// (profiles/<id>.yaml, the pin), whatever its row's points: select: the
// virtual device is the whole device. Its Profile is the profile id. An
// entry in virtual-devices.yaml that names a declared device adds only
// what a simulation needs (listen, init); every other entry is a virtual
// device of its own, as before.
//
// With neither declared devices nor the file, the error is the file's
// os.ErrNotExist.
func Compose(dir string, declared []Instance) (*Set, error) {
	s := &Set{}
	raw, err := os.ReadFile(filepath.Join(dir, FileName))
	switch {
	case err == nil:
		if s, err = decodeSet(raw, len(declared) > 0); err != nil {
			return nil, fmt.Errorf("%s: %w", FileName, err)
		}
	case errors.Is(err, os.ErrNotExist) && len(declared) > 0:
	default:
		return nil, err
	}
	s.Dir = dir
	if err := s.overlay(declared); err != nil {
		return nil, err
	}
	if err := s.resolve(); err != nil {
		if len(declared) > 0 {
			return nil, fmt.Errorf("virtual devices: %w", err)
		}
		return nil, fmt.Errorf("%s: %w", FileName, err)
	}
	return s, nil
}

// overlay puts the declared devices first, each with what its entry in
// virtual-devices.yaml (if any) adds.
func (s *Set) overlay(declared []Instance) error {
	if len(declared) == 0 {
		return nil
	}
	s.declared = map[string]bool{}
	devs := make([]Instance, len(declared))
	at := map[string]int{}
	for i, d := range declared {
		if !devprofile.IsID(d.Profile) {
			return fmt.Errorf("%s: device %s: profile %q is not a profile id", ManifestName, d.Name, d.Profile)
		}
		d.Profile = devprofile.ProjectFile(d.Profile)
		d.Listen, d.Init, d.OPCUA = "", nil, nil
		devs[i] = d
		at[strings.ToLower(d.Name)] = i
		s.declared[strings.ToLower(d.Name)] = true
	}
	added := map[string]bool{}
	var own []Instance
	for _, e := range s.Devices {
		k := strings.ToLower(e.Name)
		i, ok := at[k]
		if !ok {
			own = append(own, e)
			continue
		}
		if e.Profile != "" || e.Endpoint != "" || e.UnitID != nil || e.Params != nil {
			return fmt.Errorf("%s: device %s is declared in %s's devices:, which says what it is and where; here it may set only listen, init and opcua", FileName, e.Name, ManifestName)
		}
		if added[k] {
			return fmt.Errorf("%s: device %s: listed twice", FileName, e.Name)
		}
		added[k] = true
		devs[i].Listen, devs[i].Init, devs[i].OPCUA = e.Listen, e.Init, e.OPCUA
	}
	s.Devices = append(devs, own...)
	return nil
}

// ParseSet decodes a virtual-devices.yaml whose relative paths resolve against dir.
func ParseSet(data []byte, dir string) (*Set, error) {
	s, err := decodeSet(data, false)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", FileName, err)
	}
	s.Dir = dir
	if err := s.resolve(); err != nil {
		return nil, fmt.Errorf("%s: %w", FileName, err)
	}
	return s, nil
}

// decodeSet decodes virtual-devices.yaml; an empty file is one with no
// entries, an error unless emptyOK (the project declares devices).
func decodeSet(data []byte, emptyOK bool) (*Set, error) {
	var s Set
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&s); err != nil {
		if errors.Is(err, io.EOF) {
			if emptyOK {
				return &s, nil
			}
			return nil, fmt.Errorf("no devices")
		}
		return nil, err
	}
	return &s, nil
}

func (s *Set) resolve() error {
	if len(s.Devices) == 0 {
		return fmt.Errorf("no devices")
	}
	s.profiles = map[string]*devprofile.Profile{}
	type wire struct {
		addr string
		unit uint8
	}
	endpoints := map[wire]string{}
	for i := range s.Devices {
		d := &s.Devices[i]
		if !devprofile.IsIdent(d.Name) {
			return fmt.Errorf("device %q: the name must be an IEC identifier", d.Name)
		}
		k := strings.ToLower(d.Name)
		if _, dup := s.profiles[k]; dup {
			return fmt.Errorf("device %s: declared twice", d.Name)
		}
		if d.Profile == "" {
			return fmt.Errorf("device %s: a profile is required", d.Name)
		}
		p, err := devprofile.ResolveProfile(s.Dir, d.Profile)
		if err != nil {
			return fmt.Errorf("device %s: %w", d.Name, err)
		}
		s.profiles[k] = p
		for k := range d.Params {
			if _, ok := p.Params[k]; !ok {
				return fmt.Errorf("device %s: params: %s is not a param of %s", d.Name, k, p.ID)
			}
		}
		for name := range d.Init {
			if _, ok := p.Point(name); !ok && p.Simulator == nil {
				return fmt.Errorf("device %s: init: %s is not a point of %s", d.Name, name, p.ID)
			}
		}
		if d.OPCUA != nil && p.Protocol != devprofile.ProtoOPCUA {
			return fmt.Errorf("device %s: opcua: is an OPC UA device's; profile %s speaks %s", d.Name, p.ID, p.Protocol)
		}
		if len(d.Ports) > 0 && p.Protocol != devprofile.ProtoOPCUA {
			return fmt.Errorf("device %s: ports: only an OPC UA master has port devices; profile %s speaks %s", d.Name, p.ID, p.Protocol)
		}
		if p.Protocol == devprofile.ProtoOPCUA {
			ep, err := opcuaEndpoint(*d, p)
			if err != nil {
				return fmt.Errorf("device %s: %w", d.Name, err)
			}
			if ep != "" {
				w := wire{ep, 0}
				if prev, ok := endpoints[w]; ok {
					return fmt.Errorf("devices %s and %s both answer at %s", prev, d.Name, ep)
				}
				endpoints[w] = d.Name
			}
		}
		if p.Protocol == devprofile.ProtoModbus {
			ep, err := d.modbusEndpoint(p)
			if err != nil {
				return fmt.Errorf("device %s: %w", d.Name, err)
			}
			if ep != "" {
				w := wire{ep, d.Unit(p)}
				if prev, ok := endpoints[w]; ok {
					return fmt.Errorf("devices %s and %s both answer at %s unit %d", prev, d.Name, ep, w.unit)
				}
				endpoints[w] = d.Name
			}
		}
	}
	return nil
}

// Profile returns an instance's resolved profile.
func (s *Set) Profile(name string) *devprofile.Profile { return s.profiles[strings.ToLower(name)] }

// Unit is the instance's Modbus unit id.
func (d Instance) Unit(p *devprofile.Profile) uint8 {
	if d.UnitID != nil {
		return *d.UnitID
	}
	if p.Modbus != nil {
		return p.Modbus.UnitID
	}
	return 0
}

// modbusEndpoint normalises Endpoint to host:port, applying the profile's
// port. "" when the instance names none (it is then reachable only on its
// wire face).
func (d Instance) modbusEndpoint(p *devprofile.Profile) (string, error) {
	if d.Endpoint == "" {
		return "", nil
	}
	host, port, err := net.SplitHostPort(d.Endpoint)
	if err != nil {
		// A bare host takes the profile's port.
		host, port = d.Endpoint, ""
	}
	if port == "" {
		n := 502
		if p.Modbus != nil && p.Modbus.Port != 0 {
			n = p.Modbus.Port
		}
		port = strconv.Itoa(n)
	}
	if host == "" {
		return "", fmt.Errorf("endpoint %q: no host", d.Endpoint)
	}
	return net.JoinHostPort(host, port), nil
}

// Files are every project file the virtual devices are made of:
// virtual-devices.yaml, the process model, and the project's own profiles
// with their simulators. They are test fixtures, so `naut build` leaves
// them out of a controller, as it leaves out the *_test.yaml suites.
func (s *Set) Files() []string {
	out := []string{filepath.Join(s.Dir, FileName)}
	out = append(out, s.SourceFiles()...)
	// A profile a declared device uses is the project's, not a fixture:
	// the controller derives the device from it at load.
	seen := map[string]bool{}
	for _, d := range s.Devices {
		if p := s.Profile(d.Name); p != nil && s.declared[strings.ToLower(d.Name)] {
			seen[p.File()] = true
		}
	}
	for _, d := range s.Devices {
		if p := s.Profile(d.Name); p != nil && p.File() != "" && !seen[p.File()] {
			seen[p.File()] = true
			out = append(out, p.File())
		}
	}
	return out
}

// SourceFiles are the ST files the set compiles into its plant that live in
// the project: the process model and any project-local simulator. They are
// the plant's, not the controller's, so a check of the controller's
// programs skips them and compiles the plant instead (NewPlant).
func (s *Set) SourceFiles() []string {
	var out []string
	if s.Model != nil && s.Model.Program != "" {
		out = append(out, filepath.Join(s.Dir, s.Model.Program))
	}
	seen := map[string]bool{}
	for _, d := range s.Devices {
		p := s.Profile(d.Name)
		if p == nil || p.Dir() == "" || p.Simulator == nil {
			continue
		}
		f := filepath.Join(p.Dir(), p.Simulator.Source)
		if !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	return out
}
