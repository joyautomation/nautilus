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
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/devprofile"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/lang/st"
	"gopkg.in/yaml.v3"
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
	// Driver names the drivers: section the device's driver is made from.
	// For an OPC UA device it is an opcua-client section with no endpoint
	// or manifest (a template: the row gives the endpoint, the profile the
	// manifest) whose security, credentials and pki-dir the device's own
	// driver takes. "" connects with security None, anonymous. Not yet on
	// a Modbus device, which joins the project's one Modbus driver.
	Driver string `yaml:"driver"`
	// Validate is how a live configuration change checks the device
	// (#706): "live" probes it on a connection of its own, "static" checks
	// the configuration only; "" leaves it to the driver, then the
	// profile's default (see Set.Validate).
	Validate string `yaml:"validate"`
	// Ports are an IO-Link master's ports, by number: the sensor on each
	// (#705). Nothing consumes them yet, so a row may name only ports its
	// profile declares (devprofile.Profile.IOLinkPorts).
	Ports map[int]Port `yaml:"ports"`
	// OfflineAlarm tunes or turns off the comms-loss alarm the device
	// raises while it does not answer (<Device>__Offline): nil keeps the
	// default (high priority, 5 s on-delay); a mapping sets priority and
	// on-delay; false turns it off, which `naut check` warns about, since the
	// device's other alarms are suppressed meanwhile.
	OfflineAlarm *OfflineAlarm `yaml:"offline-alarm"`
}

// OfflineAlarm is a row's offline-alarm: false, true, or {priority,
// on-delay}.
type OfflineAlarm struct {
	// Off is set by `offline-alarm: false`.
	Off bool
	// Priority is one of devprofile.AlarmPriorities; "" keeps the default.
	Priority string
	// OnDelay is a duration like "30s"; "" keeps the default.
	OnDelay string
}

// UnmarshalYAML reads false, true or a mapping of priority and on-delay; any
// other key or value is an error, so a typo cannot turn the alarm off.
func (o *OfflineAlarm) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		var b bool
		if err := node.Decode(&b); err != nil {
			return fmt.Errorf("line %d: offline-alarm: want false, true or {priority, on-delay}, not %q", node.Line, node.Value)
		}
		*o = OfflineAlarm{Off: !b}
		return nil
	}
	var w struct {
		Priority string `yaml:"priority"`
		OnDelay  string `yaml:"on-delay"`
	}
	if err := strictNode(node, &w); err != nil {
		return fmt.Errorf("offline-alarm: %w", err)
	}
	*o = OfflineAlarm{Priority: w.Priority, OnDelay: w.OnDelay}
	return nil
}

// strictNode decodes node into v, refusing keys v has no field for.
func strictNode(node *yaml.Node, v any) error {
	raw, err := yaml.Marshal(node)
	if err != nil {
		return err
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	return dec.Decode(v)
}

// checkOfflineAlarm validates the row's offline-alarm.
func (d *device) checkOfflineAlarm(where string) error {
	o := d.OfflineAlarm
	if o == nil {
		return nil
	}
	if o.Priority != "" {
		ok := false
		for _, n := range devprofile.AlarmPriorities {
			ok = ok || strings.EqualFold(n, o.Priority)
		}
		if !ok {
			return fmt.Errorf("%s: offline-alarm priority %q (want one of %s)", where, o.Priority, strings.Join(devprofile.AlarmPriorities, ", "))
		}
	}
	if o.OnDelay != "" {
		if dur, err := time.ParseDuration(o.OnDelay); err != nil || dur < 0 {
			return fmt.Errorf("%s: offline-alarm on-delay %q is not a duration like 30s", where, o.OnDelay)
		}
	}
	return nil
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
	// derived are the BOOL members computed from points (PR 5a), after them.
	derived []devprofile.Derived
	// ports are an IO-Link master's port devices (loadPorts).
	ports []*portDev
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
		if err := d.checkValidate(where); err != nil {
			return nil, err
		}
		if err := d.checkOfflineAlarm(where); err != nil {
			return nil, err
		}
		structs := []struct {
			st  devprofile.Struct
			dev string
			why string
		}{{devprofile.Struct{Name: p.TypeName(), Points: d.points, Derived: d.derived}, r.Name, "select the same points, or give one profile a udt of its own"}}
		for _, pd := range d.ports {
			structs = append(structs, struct {
				st  devprofile.Struct
				dev string
				why string
			}{pd.structType(), pd.Name, "two IODDs derive the same type name: rename one file"})
		}
		for _, x := range structs {
			if err := reservedType(x.st.Name); err != nil && x.dev != r.Name {
				return nil, fmt.Errorf("port device %s: %w", x.dev, err)
			}
			if prev := byType[strings.ToLower(x.st.Name)]; prev != nil {
				if err := sameMembers(prev.st, x.st); err != nil {
					return nil, fmt.Errorf("devices %s and %s are both of type %s, %w: %s", prev.dev, x.dev, x.st.Name, err, x.why)
				}
			} else {
				byType[strings.ToLower(x.st.Name)] = &typed{st: x.st, dev: x.dev}
				s.types = append(s.types, x.st)
			}
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
		if d.Driver != "" {
			return fmt.Errorf("%s: driver: is an OPC UA device's (the section its connection is made from); a Modbus device joins the project's Modbus driver", where)
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
	sel, err := p.SelectAll(d.Points)
	if err != nil {
		return fmt.Errorf("%s: %w", where, err)
	}
	d.points, d.derived = sel.Points, sel.Derived
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
	return d.loadPorts(fsys, where)
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

// Validation modes a row's validate: may name.
const (
	ValidateLive   = "live"
	ValidateStatic = "static"
)

// checkValidate refuses a validate: that is neither mode, and a live
// check of a device whose profile documents one connection: the probe's
// would be the device's only one, and that one is the controller's.
func (d *device) checkValidate(where string) error {
	switch d.Validate {
	case "", ValidateStatic:
		return nil
	case ValidateLive:
		if n := d.maxConnections(); n == 1 {
			return fmt.Errorf("%s: validate: live, but profile %s documents max-connections 1 (%s): a live check's connection would be the controller's; use validate: static", where, d.prof.ID, d.prof.Modbus.MaxConnectionsDoc)
		}
		return nil
	}
	return fmt.Errorf("%s: validate: %q is not live or static", where, d.Validate)
}

// maxConnections is the connection limit the device's profile documents;
// 0 when it does not.
func (d *device) maxConnections() int {
	if m := d.prof.Modbus; m != nil && d.prof.Protocol == devprofile.ProtoModbus {
		return m.MaxConnections
	}
	return 0
}

// Validate is what decides how a live configuration change checks device
// name (#706), as facts for the caller to compose, row then driver then
// default: row is the row's own validate: ("" when it sets none); def is
// the default, static when the profile documents max-connections of 2 or
// fewer, else live; why says so in words. ok is false for a name that is
// not a device of the set.
func (s *Set) Validate(name string) (row, def, why string, ok bool) {
	if s == nil {
		return "", "", "", false
	}
	for _, d := range s.devices {
		if !strings.EqualFold(d.Name, name) {
			continue
		}
		if n := d.maxConnections(); n > 0 && n <= 2 {
			return d.Validate, ValidateStatic, fmt.Sprintf("profile %s documents max-connections %d (%s)", d.prof.ID, n, d.prof.Modbus.MaxConnectionsDoc), true
		} else if n > 0 {
			return d.Validate, ValidateLive, fmt.Sprintf("profile %s documents max-connections %d, room for a check's own", d.prof.ID, n), true
		}
		return d.Validate, ValidateLive, fmt.Sprintf("profile %s does not document a connection limit", d.prof.ID), true
	}
	return "", "", "", false
}

// sameMembers reports how two structs sharing a type name differ, nil
// when their members are the same names and types in the same order.
func sameMembers(a, b devprofile.Struct) error {
	if len(a.Points)+len(a.Derived) != len(b.Points)+len(b.Derived) {
		return fmt.Errorf("with %d and %d members", len(a.Points)+len(a.Derived), len(b.Points)+len(b.Derived))
	}
	if len(a.Derived) != len(b.Derived) {
		return fmt.Errorf("with %d and %d derived members", len(a.Derived), len(b.Derived))
	}
	for i := range a.Derived {
		if !a.Derived[i].Same(b.Derived[i]) {
			return fmt.Errorf("derived member %d differs (%s, %s: another source or test)", i+1, a.Derived[i].Name, b.Derived[i].Name)
		}
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
	// Driver is the row's driver: section, whose connection settings the
	// device's driver takes; "" when the row names none.
	Driver string
	// Security and SecurityMode are the endpoint policy and mode of a row
	// that names no driver: section: None, anonymous, what the profiles
	// document their servers offering (an opcua-client driver's default,
	// the strongest offered, needs a pki-dir only a section can name), and
	// the device's driver says so in naut check rather than downgrade a
	// device that offers more without a word. "" when Driver is set: the
	// section's are the device's.
	Security, SecurityMode string
}

// OPCUARows are the set's OPC UA devices, in declaration order: an OPC UA
// client talks to one server, so each is a driver of its own.
func (s *Set) OPCUARows() []OPCUARow {
	var out []OPCUARow
	for _, d := range s.protocol(devprofile.ProtoOPCUA) {
		ep, _ := d.prof.OPCUAEndpoint(d.Endpoint) // Resolve checked it
		r := OPCUARow{Name: d.Name, Endpoint: ep, Driver: d.Driver}
		if d.Driver == "" {
			r.Security, r.SecurityMode = "None", "None"
		}
		out = append(out, r)
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

// DevicePoints are one device's selected points, as its struct has them.
type DevicePoints struct {
	Device string
	// Title is the profile's one line for a person.
	Title string
	// Desc is the row's own line, "" when it gave none.
	Desc   string
	Points []devprofile.Point
}

// DevicePoints is each device with the points it exposes, in declaration
// order: what a screen is generated from (#714). A point the master owns
// (its process data out) is not writable here: it has no command tag.
func (s *Set) DevicePoints() []DevicePoints {
	if s == nil {
		return nil
	}
	out := make([]DevicePoints, 0, len(s.devices))
	for _, d := range s.devices {
		pts := append([]devprofile.Point(nil), d.points...)
		for i := range pts {
			// the master's own process data out has no command tag: it is
			// shown, not offered to write
			if pts[i].Writable && d.owned(pts[i].Name) {
				pts[i].Writable = false
			}
		}
		out = append(out, DevicePoints{Device: d.Name, Title: d.prof.Title, Desc: d.Desc, Points: pts})
	}
	return out
}

// DerivedRow is one derived member of a device: what it reads and how.
type DerivedRow struct {
	Device, Name, From string
	// Test is "bit 3" or "== 39".
	Test string
}

// Derived lists each device's derived members, in declaration order.
func (s *Set) Derived() []DerivedRow {
	if s == nil {
		return nil
	}
	var out []DerivedRow
	for _, d := range s.devices {
		for _, dp := range d.derived {
			var test string
			if dp.Bit != nil {
				test = fmt.Sprintf("bit %d", *dp.Bit)
			} else if dp.Eq != nil {
				test = fmt.Sprintf("== %d", *dp.Eq)
			}
			out = append(out, DerivedRow{Device: d.Name, Name: dp.Name, From: dp.From, Test: test})
		}
	}
	return out
}

// AlarmRow is a default alarm of a device type (a profile's alarms: entry),
// once per type that a row selects the point of. Priority and OnDelay are
// "" when the profile left them out.
type AlarmRow struct {
	Type, Member, Name, Priority, OnDelay, Class, Notes string
}

// AlarmRows are the default alarms the rows' profiles declare: one per
// (device type, point) for which some row has the point as a struct
// member, so a row that does not select the point has no alarm on it.
func (s *Set) AlarmRows() []AlarmRow {
	if s == nil {
		return nil
	}
	var out []AlarmRow
	seen := map[string]bool{}
	for _, d := range s.devices {
		for _, a := range d.prof.Alarms {
			member, ok := d.member(a.Point)
			if !ok {
				continue
			}
			k := strings.ToLower(d.prof.TypeName() + "." + member)
			if seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, AlarmRow{
				Type: d.prof.TypeName(), Member: member, Name: a.Name, Priority: a.Priority,
				OnDelay: a.OnDelay, Class: a.Class, Notes: a.Notes,
			})
		}
	}
	return out
}

// member is the struct member called name (a selected BOOL point or derived
// point), as the struct spells it.
func (d *device) member(name string) (string, bool) {
	for _, pt := range d.points {
		if strings.EqualFold(pt.Name, name) {
			return pt.Name, strings.EqualFold(pt.Type, "BOOL")
		}
	}
	for _, dp := range d.derived {
		if strings.EqualFold(dp.Name, name) {
			return dp.Name, true
		}
	}
	return "", false
}
