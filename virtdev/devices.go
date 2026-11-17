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
	profiles map[string]*Profile
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
func LoadSet(dir string) (*Set, error) {
	file := filepath.Join(dir, FileName)
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	return ParseSet(raw, dir)
}

// ParseSet decodes a virtual-devices.yaml whose relative paths resolve against dir.
func ParseSet(data []byte, dir string) (*Set, error) {
	var s Set
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&s); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%s: no devices", FileName)
		}
		return nil, fmt.Errorf("%s: %w", FileName, err)
	}
	s.Dir = dir
	if err := s.resolve(); err != nil {
		return nil, fmt.Errorf("%s: %w", FileName, err)
	}
	return &s, nil
}

func (s *Set) resolve() error {
	if len(s.Devices) == 0 {
		return fmt.Errorf("no devices")
	}
	s.profiles = map[string]*Profile{}
	type wire struct {
		addr string
		unit uint8
	}
	endpoints := map[wire]string{}
	for i := range s.Devices {
		d := &s.Devices[i]
		if !identRE.MatchString(d.Name) {
			return fmt.Errorf("device %q: the name must be an IEC identifier", d.Name)
		}
		k := strings.ToLower(d.Name)
		if _, dup := s.profiles[k]; dup {
			return fmt.Errorf("device %s: declared twice", d.Name)
		}
		if d.Profile == "" {
			return fmt.Errorf("device %s: a profile is required", d.Name)
		}
		p, err := ResolveProfile(s.Dir, d.Profile)
		if err != nil {
			return fmt.Errorf("device %s: %w", d.Name, err)
		}
		s.profiles[k] = p
		for name := range d.Init {
			if _, ok := p.Point(name); !ok && p.Simulator == nil {
				return fmt.Errorf("device %s: init: %s is not a point of %s", d.Name, name, p.ID)
			}
		}
		if p.Protocol == ProtoModbus {
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
func (s *Set) Profile(name string) *Profile { return s.profiles[strings.ToLower(name)] }

// Unit is the instance's Modbus unit id.
func (d Instance) Unit(p *Profile) uint8 {
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
func (d Instance) modbusEndpoint(p *Profile) (string, error) {
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
