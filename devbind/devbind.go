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
	// Endpoint is where the device answers: a Modbus host[:port].
	Endpoint string `yaml:"endpoint"`
	// UnitID is the Modbus unit id; nil takes the profile's.
	UnitID *int `yaml:"unit-id"`
	// Points selects the points to poll and expose, as globs over point
	// names; empty selects all. The struct has the selected points only.
	Points []string `yaml:"points"`
	// Params sets the profile's params:.
	Params map[string]string `yaml:"params"`
	// Desc is one line for the device's tags.
	Desc string `yaml:"desc"`
}

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
		if p.Protocol != devprofile.ProtoModbus {
			return nil, fmt.Errorf("%s: profile %s speaks %s; only Modbus devices are supported so far", where, p.ID, p.Protocol)
		}
		d := &device{Device: r, prof: p}
		if err := d.check(where); err != nil {
			return nil, err
		}
		st := devprofile.Struct{Name: p.TypeName(), Points: d.points}
		if prev := byType[strings.ToLower(st.Name)]; prev != nil {
			if err := sameMembers(prev.st, st); err != nil {
				return nil, fmt.Errorf("devices %s and %s are both of type %s, %w: select the same points, or give one profile its own udt:", prev.dev, r.Name, st.Name, err)
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
	if _, _, err := d.hostPort(); err != nil {
		return fmt.Errorf("%s: %w", where, err)
	}
	if d.UnitID != nil && (*d.UnitID < 0 || *d.UnitID > 255) {
		return fmt.Errorf("%s: unit-id %d is not 0-255", where, *d.UnitID)
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
