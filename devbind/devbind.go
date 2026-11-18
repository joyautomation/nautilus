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
	// names; empty selects all. The struct type keeps every point.
	Points []string `yaml:"points"`
	// Params sets the profile's params:.
	Params map[string]string `yaml:"params"`
	// Desc is one line for the device's tags.
	Desc string `yaml:"desc"`
}

// Set is a project's devices, resolved against its vendored profiles.
type Set struct {
	devices []*device
	// types are the profiles in use, one per type name, by name.
	types []*devprofile.Profile
}

type device struct {
	Device
	prof *devprofile.Profile
	// points are the selected points, in profile order.
	points []devprofile.Point
	sel    map[string]bool
}

// Resolve checks rows against the profiles vendored in fsys (profiles/).
func Resolve(fsys fs.FS, rows []Device) (*Set, error) {
	s := &Set{}
	names := map[string]string{}
	profiles := map[string]*devprofile.Profile{}
	byType := map[string]*devprofile.Profile{}
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
			profiles[r.Profile] = p
		}
		if p.Protocol != devprofile.ProtoModbus {
			return nil, fmt.Errorf("%s: profile %s speaks %s; only Modbus devices are supported so far", where, p.ID, p.Protocol)
		}
		if prev := byType[p.TypeName()]; prev != nil && prev != p {
			if err := sameMembers(prev, p); err != nil {
				return nil, fmt.Errorf("profiles %s and %s both declare type %s, %w", prev.ID, p.ID, p.TypeName(), err)
			}
		} else if prev == nil {
			byType[p.TypeName()] = p
			s.types = append(s.types, p)
		}
		d := &device{Device: r, prof: p, sel: map[string]bool{}}
		if err := d.check(where); err != nil {
			return nil, err
		}
		s.devices = append(s.devices, d)
	}
	sort.Slice(s.types, func(i, j int) bool { return s.types[i].TypeName() < s.types[j].TypeName() })
	return s, nil
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
	for _, g := range d.Points {
		if _, err := path.Match(strings.ToLower(g), ""); err != nil {
			return fmt.Errorf("%s: points: %q: %w", where, g, err)
		}
	}
	matched := make([]bool, len(d.Points))
	for _, pt := range p.Points {
		take := len(d.Points) == 0
		for i, g := range d.Points {
			if ok, _ := path.Match(strings.ToLower(g), strings.ToLower(pt.Name)); ok {
				take, matched[i] = true, true
			}
		}
		if take {
			d.points = append(d.points, pt)
			d.sel[pt.Name] = true
		}
	}
	for i, ok := range matched {
		if !ok {
			return fmt.Errorf("%s: points: %q matches no point of %s (naut devices show %s lists them)", where, d.Points[i], p.ID, p.ID)
		}
	}
	return nil
}

// sameMembers reports how two profiles sharing a type name differ, nil
// when their points are the same names and types in the same order.
func sameMembers(a, b *devprofile.Profile) error {
	if len(a.Points) != len(b.Points) {
		return fmt.Errorf("with %d and %d points", len(a.Points), len(b.Points))
	}
	for i := range a.Points {
		pa, pb := a.Points[i], b.Points[i]
		if !strings.EqualFold(pa.Name, pb.Name) || !strings.EqualFold(pa.Type, pb.Type) {
			return fmt.Errorf("point %d differs (%s %s, %s %s)", i+1, pa.Name, pa.Type, pb.Name, pb.Type)
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
