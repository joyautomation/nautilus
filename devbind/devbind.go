// Package devbind turns a project's devices: rows into what the project
// runs: each device's struct type, its tags, and its driver bindings, from
// the profiles the project vendored (docs/design/device-library.md §3).
//
// A device is one struct tag typed by its profile (CONV : ABB_ACS355_FMBT21)
// whose members read back what the device holds, plus a command tag per
// writable point (CONV_ControlWord) that the program writes: the rule the
// IT drivers and the Sparkplug host follow for writable members. The
// drivers are only ever handed ordinary manifests. Each point is bound
// under an internal member name ("CONV.StatusWord") and a command under
// its tag's name; the adapters in this package assemble the members into
// the struct the runtime stores.
package devbind

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/joyautomation/nautilus/devprofile"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/lang/st"
)

// Device is one row of a project's devices: list.
type Device struct {
	// Name is the device's tag name, an IEC identifier.
	Name string `yaml:"name"`
	// Profile is the id of a profile vendored in profiles/.
	Profile string `yaml:"profile"`
	// Endpoint is where the device answers: a Modbus host[:port], or an
	// OPC UA host[:port] or opc.tcp://host[:port].
	Endpoint string `yaml:"endpoint"`
	// UnitID is the Modbus unit id; nil takes the profile's. An OPC UA
	// device has none.
	UnitID *int `yaml:"unit-id"`
	// Points selects the points to poll and expose, as globs over point
	// names; empty selects all. The struct has the selected points only.
	Points []string `yaml:"points"`
	// Params sets the profile's params:.
	Params map[string]string `yaml:"params"`
	// Desc is one line for the device's tags.
	Desc string `yaml:"desc"`
	// Ports are an IO-Link master's ports, by number: the sensor on each
	// (#705). Nothing consumes them yet, so a row may name only ports its
	// profile declares (devprofile.Profile.IOLinkPorts).
	Ports map[int]Port `yaml:"ports"`
}

// Port is one IO-Link port of a master's row.
type Port struct {
	// Name is the port's device tag, an IEC identifier unique among the
	// project's devices, ports and device types.
	Name string `yaml:"name"`
	// IODD is the sensor's IODD, a file name in the project's iodd/ folder.
	IODD string `yaml:"iodd"`
}

// IODDDir is the folder a project's vendored IODDs live in.
const IODDDir = "iodd"

// Set is a project's devices, resolved against its vendored profiles.
type Set struct {
	devices []*device
	// types are the device types in use, one per name, by name.
	types []devprofile.Struct
}

type device struct {
	Device
	prof *devprofile.Profile
	// points are the selected points, in profile order: the struct's
	// members.
	points []devprofile.Point
}

// Resolve checks rows against the profiles vendored in fsys (profiles/).
func Resolve(fsys fs.FS, rows []Device) (*Set, error) {
	s := &Set{}
	names := map[string]string{}
	profiles := map[string]*devprofile.Profile{}
	type typed struct {
		st  devprofile.Struct
		dev string // the first device of the type
	}
	byType := map[string]*typed{}
	for i, r := range rows {
		where := fmt.Sprintf("devices[%d]", i)
		if r.Name != "" {
			where = "device " + r.Name
		}
		if !devprofile.IsIdent(r.Name) {
			return nil, fmt.Errorf("%s: name %q is not an IEC identifier", where, r.Name)
		}
		if prev, dup := names[strings.ToLower(r.Name)]; dup {
			return nil, fmt.Errorf("device %s is declared twice (as %s and %s)", r.Name, prev, r.Name)
		}
		names[strings.ToLower(r.Name)] = r.Name
		if r.Profile == "" {
			return nil, fmt.Errorf("%s: profile is required", where)
		}
		if !devprofile.IsID(r.Profile) {
			return nil, fmt.Errorf("%s: profile %q is not a profile id (letters, digits, '-', '_', '.')", where, r.Profile)
		}
		p := profiles[r.Profile]
		if p == nil {
			var err error
			p, err = devprofile.ReadProject(fsys, r.Profile)
			if err != nil {
				if errorsIsNotExist(err) {
					return nil, fmt.Errorf("%s: profile %s is not in %s/ (naut devices add %s copies it in)", where, r.Profile, devprofile.ProjectDir, r.Profile)
				}
				return nil, fmt.Errorf("%s: %w", where, err)
			}
			if err := reservedType(p.TypeName()); err != nil {
				return nil, fmt.Errorf("profile %s: %w", p.ID, err)
			}
			profiles[r.Profile] = p
		}
		d := &device{Device: r, prof: p}
		if err := d.check(where); err != nil {
			return nil, err
		}
		if err := d.checkPorts(fsys, where, names); err != nil {
			return nil, err
		}
		st := devprofile.Struct{Name: p.TypeName(), Points: d.points}
		if prev := byType[strings.ToLower(st.Name)]; prev != nil {
			if err := sameMembers(prev.st, st); err != nil {
				return nil, fmt.Errorf("devices %s and %s are both of type %s, %w: select the same points, or give one profile a udt of its own", prev.dev, r.Name, st.Name, err)
			}
		} else {
			byType[strings.ToLower(st.Name)] = &typed{st: st, dev: r.Name}
			s.types = append(s.types, st)
		}
		s.devices = append(s.devices, d)
	}
	for _, st := range s.types {
		if dev, ok := names[strings.ToLower(st.Name)]; ok {
			return nil, fmt.Errorf("device %s has the name of a device type (%s): a tag and a type may not share a name", dev, st.Name)
		}
	}
	sort.Slice(s.types, func(i, j int) bool { return s.types[i].Name < s.types[j].Name })
	return s, nil
}

// reservedType refuses a type name the language already means: an
// elementary type (WORD) or a standard block (TON). Declaring one would
// shadow it, and the errors that follow would not say why.
func reservedType(name string) error {
	if _, ok := ir.FBs[strings.ToUpper(name)]; ok {
		return fmt.Errorf("udt: %s is a standard function block's name", name)
	}
	if _, err := st.ResolveTypeName(name, nil); err == nil {
		return fmt.Errorf("udt: %s is an IEC elementary type's name", name)
	}
	return nil
}

// check validates a row against its profile and selects its points.
func (d *device) check(where string) error {
	p := d.prof
	if d.Endpoint == "" {
		return fmt.Errorf("%s: endpoint is required", where)
	}
	switch p.Protocol {
	case devprofile.ProtoModbus:
		if _, _, err := d.hostPort(); err != nil {
			return fmt.Errorf("%s: %w", where, err)
		}
		if d.UnitID != nil && (*d.UnitID < 0 || *d.UnitID > 255) {
			return fmt.Errorf("%s: unit-id %d is not 0-255", where, *d.UnitID)
		}
	case devprofile.ProtoOPCUA:
		if _, err := p.OPCUAEndpoint(d.Endpoint); err != nil {
			return fmt.Errorf("%s: %w", where, err)
		}
		if d.UnitID != nil {
			return fmt.Errorf("%s: unit-id is a Modbus device's; profile %s speaks OPC UA", where, p.ID)
		}
	default:
		return fmt.Errorf("%s: profile %s speaks %s, which devices: rows do not support", where, p.ID, p.Protocol)
	}
	for k := range d.Params {
		if _, ok := p.Params[k]; !ok {
			return fmt.Errorf("%s: profile %s has no param %q", where, p.ID, k)
		}
	}
	pts, err := p.Select(d.Points)
	if err != nil {
		return fmt.Errorf("%s: %w", where, err)
	}
	d.points = pts
	return nil
}

// checkPorts validates an IO-Link master's ports:, adding each port's
// name to names (lower-cased name -> as written) so it shares the devices'
// one namespace.
func (d *device) checkPorts(fsys fs.FS, where string, names map[string]string) error {
	if len(d.Ports) == 0 {
		return nil
	}
	nums := make([]int, 0, len(d.Ports))
	for k := range d.Ports {
		nums = append(nums, k)
	}
	sort.Ints(nums)
	for _, k := range nums {
		pt := d.Ports[k]
		at := fmt.Sprintf("%s: port %d", where, k)
		if !devprofile.IsIdent(pt.Name) {
			return fmt.Errorf("%s: name %q is not an IEC identifier", at, pt.Name)
		}
		if prev, dup := names[strings.ToLower(pt.Name)]; dup {
			return fmt.Errorf("%s: %s is already the name of %s", at, pt.Name, prev)
		}
		names[strings.ToLower(pt.Name)] = pt.Name
		if pt.IODD == "" {
			continue
		}
		if !devprofile.IsFileName(pt.IODD) {
			return fmt.Errorf("%s: iodd %q must be a file name in %s/", at, pt.IODD, IODDDir)
		}
		if _, err := fs.Stat(fsys, path.Join(IODDDir, pt.IODD)); err != nil {
			return fmt.Errorf("%s: iodd %s is not in %s/", at, pt.IODD, IODDDir)
		}
	}
	// A profile without an iolink: block has no IO-Link ports: its row's
	// ports: is refused, not ignored.
	n := d.prof.IOLinkPorts()
	if n == 0 {
		return fmt.Errorf("%s: ports: profile %s declares no IO-Link ports", where, d.prof.ID)
	}
	if err := portRange(nums, n); err != nil {
		return fmt.Errorf("%s: ports: profile %s %w", where, d.prof.ID, err)
	}
	// Nothing derives a port device yet (#705 PR 4): refuse a valid
	// ports: rather than load a project whose sensors silently do nothing.
	return fmt.Errorf("%s: ports: port devices are not loaded yet (#705); remove ports: until they are", where)
}

// portRange checks a row's port numbers (sorted) against a master's n
// ports, numbered 1 to n.
func portRange(nums []int, n int) error {
	for _, k := range nums {
		if k < 1 || k > n {
			return fmt.Errorf("has ports 1-%d, not %d", n, k)
		}
	}
	return nil
}

// sameMembers reports how two structs sharing a type name differ, nil
// when their members are the same names and types in the same order.
func sameMembers(a, b devprofile.Struct) error {
	if len(a.Points) != len(b.Points) {
		return fmt.Errorf("with %d and %d members", len(a.Points), len(b.Points))
	}
	for i := range a.Points {
		pa, pb := a.Points[i], b.Points[i]
		if !strings.EqualFold(pa.Name, pb.Name) || !strings.EqualFold(pa.Type, pb.Type) {
			return fmt.Errorf("member %d differs (%s %s, %s %s)", i+1, pa.Name, pa.Type, pb.Name, pb.Type)
		}
	}
	return nil
}

// Profiles are the vendored profiles the rows use, by id.
func (s *Set) Profiles() []*devprofile.Profile {
	seen := map[*devprofile.Profile]bool{}
	var out []*devprofile.Profile
	for _, d := range s.devices {
		if !seen[d.prof] {
			seen[d.prof] = true
			out = append(out, d.prof)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Rows are the set's devices: rows, in declaration order.
func (s *Set) Rows() []Device {
	if s == nil {
		return nil
	}
	out := make([]Device, len(s.devices))
	for i, d := range s.devices {
		out[i] = d.Device
	}
	return out
}

// protocol is the set's devices of one protocol, in declaration order.
func (s *Set) protocol(proto string) []*device {
	if s == nil {
		return nil
	}
	var out []*device
	for _, d := range s.devices {
		if d.prof.Protocol == proto {
			out = append(out, d)
		}
	}
	return out
}

// Modbus reports whether the set has a Modbus device, for the project's
// Modbus driver to carry.
func (s *Set) Modbus() bool { return len(s.protocol(devprofile.ProtoModbus)) > 0 }

// OPCUARow is an OPC UA device's driver: the device's name, which its
// opcua-client driver takes, where it answers, and how it connects.
type OPCUARow struct {
	Name string
	// Endpoint is opc.tcp://host:port.
	Endpoint string
	// Security and SecurityMode are the endpoint policy and mode: None,
	// anonymous, what the profiles document their servers offering. A row
	// cannot set its own yet (an opcua-client driver's default, the
	// strongest offered, needs a pki-dir a row has nowhere to name), so
	// the device's driver says so in naut check rather than downgrade a
	// device that offers more without a word.
	Security, SecurityMode string
}

// OPCUARows are the set's OPC UA devices, in declaration order: an OPC UA
// client talks to one server, so each is a driver of its own.
func (s *Set) OPCUARows() []OPCUARow {
	var out []OPCUARow
	for _, d := range s.protocol(devprofile.ProtoOPCUA) {
		ep, _ := d.prof.OPCUAEndpoint(d.Endpoint) // Resolve checked it
		out = append(out, OPCUARow{Name: d.Name, Endpoint: ep, Security: "None", SecurityMode: "None"})
	}
	return out
}

// Len is how many devices the set has.
func (s *Set) Len() int {
	if s == nil {
		return 0
	}
	return len(s.devices)
}

// hostPort splits a Modbus endpoint; the port defaults to the profile's,
// then 502.
func (d *device) hostPort() (string, int, error) {
	port := 502
	if m := d.prof.Modbus; m != nil && m.Port != 0 {
		port = m.Port
	}
	host := d.Endpoint
	if h, p, err := net.SplitHostPort(d.Endpoint); err == nil {
		n, err := strconv.Atoi(p)
		if err != nil || n <= 0 || n > 65535 {
			return "", 0, fmt.Errorf("endpoint %q: bad port", d.Endpoint)
		}
		host, port = h, n
	}
	if host == "" {
		return "", 0, fmt.Errorf("endpoint %q: no host", d.Endpoint)
	}
	return host, port, nil
}

func (d *device) unitID() uint8 {
	if d.UnitID != nil {
		return uint8(*d.UnitID)
	}
	if m := d.prof.Modbus; m != nil {
		return m.UnitID
	}
	return 0
}

// IsCommand reports whether name is one of the devices' command tags.
func (s *Set) IsCommand(name string) bool {
	if s == nil {
		return false
	}
	for _, d := range s.devices {
		for _, pt := range d.points {
			if pt.Writable && strings.EqualFold(CommandTag(d.Name, pt.Name), name) {
				return true
			}
		}
	}
	return false
}

// memberName is the internal binding name a driver delivers a point under.
func memberName(dev, point string) string { return dev + "." + point }

// CommandTag is the output tag a writable point is written through.
func CommandTag(dev, point string) string { return dev + "_" + point }

func errorsIsNotExist(err error) bool { return errors.Is(err, fs.ErrNotExist) }
