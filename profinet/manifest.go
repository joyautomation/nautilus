// Package profinet is nautilus's PROFINET IO: the device driver
// (`type: profinet-device`, nautilus as an IO device to a PLC) and, later,
// the controller driver. The protocol lives in profinet/wire,
// profinet/transport, profinet/device and profinet/gsdml; this package is
// the manifest, the tags, quality and the write gate (docs/design/profinet.md).
package profinet

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/joyautomation/nautilus/profinet/device"
	"github.com/joyautomation/nautilus/profinet/wire"
)

// Defaults for a device manifest.
const (
	DefaultVendorName  = "nautilus"
	DefaultOrderNumber = "NAUTILUS-PN"
	DefaultMinCycle    = 8 * time.Millisecond
	// MaxIOLength is the most IO data (bytes, all subslots) per direction
	// the generated GSDML declares (MaxInputLength / MaxOutputLength).
	MaxIOLength = 244
)

// DeviceManifest is profinet_device.yaml: nautilus as a PROFINET IO
// device. Directions (inputs, outputs, dir) are the PLC's, as the GSDML
// and the engineering tool show them: an input is data nautilus provides,
// an output is data the PLC commands.
type DeviceManifest struct {
	Interface   string     `yaml:"interface"`
	Station     string     `yaml:"station"`
	VendorName  string     `yaml:"vendor-name"`
	VendorID    Hex16      `yaml:"vendor-id"`
	DeviceID    Hex16      `yaml:"device-id"`
	OrderNumber string     `yaml:"order-number"`
	MinCycle    Duration   `yaml:"min-cycle"`
	Slots       []SlotSpec `yaml:"slots"`
	Tags        []Binding  `yaml:"tags"`
}

// SlotSpec is one module.
type SlotSpec struct {
	Slot        uint16        `yaml:"slot"`
	ModuleIdent Hex32         `yaml:"module-ident"`
	Subslots    []SubslotSpec `yaml:"subslots"`
}

// SubslotSpec is one submodule and its IO data lengths in bytes.
type SubslotSpec struct {
	Subslot        uint16 `yaml:"subslot"`
	SubmoduleIdent Hex32  `yaml:"submodule-ident"`
	Inputs         uint16 `yaml:"inputs"`
	Outputs        uint16 `yaml:"outputs"`
}

// Binding is one tag at a place in a subslot's IO data.
type Binding struct {
	Name    string `yaml:"name"`
	Slot    uint16 `yaml:"slot"`
	Subslot uint16 `yaml:"subslot"`
	Dir     string `yaml:"dir"` // input (nautilus → PLC) | output (PLC → nautilus)
	Offset  uint16 `yaml:"offset"`
	Bit     *uint8 `yaml:"bit"` // bool only, 0–7
	Format  string `yaml:"format"`

	format wire.Format
}

// ToPLC reports whether the tag is data nautilus provides (a nautilus
// output, the PLC's input).
func (b Binding) ToPLC() bool { return b.Dir == "input" }

func (b Binding) addr() device.Addr { return device.Addr{Slot: b.Slot, Subslot: b.Subslot} }

func (b Binding) bit() uint8 {
	if b.Bit == nil {
		return 0
	}
	return *b.Bit
}

// Duration is a time.Duration spelled "8ms" in YAML.
type Duration time.Duration

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	v, err := time.ParseDuration(n.Value)
	if err != nil {
		return fmt.Errorf("duration %q: %w", n.Value, err)
	}
	*d = Duration(v)
	return nil
}

// Hex32 is an ident number spelled 0x00000101 in YAML.
type Hex32 uint32

func (h *Hex32) UnmarshalYAML(n *yaml.Node) error {
	var v uint64
	if _, err := fmt.Sscan(n.Value, &v); err != nil || v > 0xFFFFFFFF {
		return fmt.Errorf("ident %q: want a 32-bit number like 0x00000101", n.Value)
	}
	*h = Hex32(v)
	return nil
}

// Hex16 is a vendor or device ID spelled 0x002A in YAML.
type Hex16 uint16

func (h *Hex16) UnmarshalYAML(n *yaml.Node) error {
	var v uint64
	if _, err := fmt.Sscan(n.Value, &v); err != nil || v > 0xFFFF {
		return fmt.Errorf("id %q: want a 16-bit number like 0x002A", n.Value)
	}
	*h = Hex16(v)
	return nil
}

// ParseDeviceManifest decodes a profinet_device.yaml (unknown keys are
// errors), fills defaults and validates it.
func ParseDeviceManifest(data []byte) (DeviceManifest, error) {
	var m DeviceManifest
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&m); err != nil {
		if errors.Is(err, io.EOF) {
			return DeviceManifest{}, fmt.Errorf("profinet-device: manifest is empty")
		}
		return DeviceManifest{}, fmt.Errorf("profinet-device: manifest: %w", err)
	}
	if m.VendorName == "" {
		m.VendorName = DefaultVendorName
	}
	if m.OrderNumber == "" {
		m.OrderNumber = DefaultOrderNumber
	}
	if m.MinCycle == 0 {
		m.MinCycle = Duration(DefaultMinCycle)
	}
	if err := m.Validate(); err != nil {
		return DeviceManifest{}, err
	}
	return m, nil
}

// stationName is a PROFINET NameOfStation: DNS-like labels of lowercase
// letters, digits and hyphens, at most 240 characters.
var stationName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$`)

// Validate checks the manifest offline; every finding is reported at
// once. It also resolves each binding's format.
func (m *DeviceManifest) Validate() error {
	var errs []error
	bad := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	if m.Interface == "" {
		bad("interface is required (the NIC on the PROFINET network)")
	}
	if len(m.Station) > 240 || !stationName.MatchString(m.Station) || strings.HasPrefix(m.Station, "port-") {
		bad("station %q: want lowercase letters, digits and hyphens in dot-separated labels (and not port-…)", m.Station)
	}
	if m.VendorID == 0 || m.DeviceID == 0 {
		bad("vendor-id and device-id are required (what the GSDML and DCP say this device is)")
	}
	if len(m.OrderNumber) > 20 {
		bad("order-number %q: at most 20 characters (I&M0 OrderID)", m.OrderNumber)
	}
	if mc := time.Duration(m.MinCycle); mc < time.Millisecond || mc > 512*time.Millisecond {
		bad("min-cycle %s: want 1ms to 512ms", mc)
	}

	subs := map[device.Addr]SubslotSpec{}
	slots := map[uint16]bool{}
	var inTotal, outTotal int
	for _, s := range m.Slots {
		if s.Slot == 0 {
			bad("slot 0 is the device access point; user modules start at 1")
		}
		if slots[s.Slot] {
			bad("slot %d twice", s.Slot)
		}
		slots[s.Slot] = true
		if len(s.Subslots) == 0 {
			bad("slot %d has no subslots", s.Slot)
		}
		for _, ss := range s.Subslots {
			a := device.Addr{Slot: s.Slot, Subslot: ss.Subslot}
			if ss.Subslot == 0 || ss.Subslot >= 0x8000 {
				bad("slot %d subslot %d: want 1–32767", s.Slot, ss.Subslot)
			}
			if _, dup := subs[a]; dup {
				bad("subslot %s twice", a)
			}
			subs[a] = ss
			inTotal += int(ss.Inputs)
			outTotal += int(ss.Outputs)
		}
	}
	if len(subs) == 0 {
		bad("no slots: a device needs at least one subslot")
	}
	if len(subs) > device.MaxSubslots {
		bad("%d subslots: at most %d in this version", len(subs), device.MaxSubslots)
	}
	if inTotal > MaxIOLength || outTotal > MaxIOLength {
		bad("IO data %d bytes in, %d out: at most %d each way", inTotal, outTotal, MaxIOLength)
	}

	names := map[string]bool{}
	// used marks the bits each binding occupies, per subslot and direction.
	used := map[string][]uint8{}
	for i := range m.Tags {
		b := &m.Tags[i]
		where := fmt.Sprintf("tag %q", b.Name)
		if b.Name == "" {
			bad("tags[%d]: name is required", i)
			continue
		}
		if names[b.Name] {
			bad("%s twice", where)
		}
		names[b.Name] = true
		f, err := wire.ParseFormat(b.Format)
		if err != nil {
			bad("%s: %v", where, err)
			continue
		}
		b.format = f
		if b.Dir != "input" && b.Dir != "output" {
			bad("%s: dir %q: want input (nautilus → PLC) or output (PLC → nautilus)", where, b.Dir)
			continue
		}
		ss, ok := subs[b.addr()]
		if !ok {
			bad("%s: no subslot %s in slots", where, b.addr())
			continue
		}
		size := ss.Outputs
		if b.ToPLC() {
			size = ss.Inputs
		}
		if int(b.Offset)+f.Size() > int(size) {
			bad("%s: %s at byte %d does not fit the subslot's %d %s bytes", where, f, b.Offset, size, b.Dir)
			continue
		}
		if f == wire.FormatBool {
			if b.Bit == nil || *b.Bit > 7 {
				bad("%s: bool needs bit: 0–7", where)
				continue
			}
		} else if b.Bit != nil {
			bad("%s: bit: is for bool only", where)
			continue
		}
		key := b.addr().String() + "/" + b.Dir
		if used[key] == nil {
			used[key] = make([]uint8, size)
		}
		var mask uint8 = 0xFF
		if f == wire.FormatBool {
			mask = 1 << b.bit()
		}
		for o := int(b.Offset); o < int(b.Offset)+f.Size(); o++ {
			if used[key][o]&mask != 0 {
				bad("%s overlaps another %s tag in subslot %s at byte %d", where, b.Dir, b.addr(), o)
				break
			}
			used[key][o] |= mask
		}
	}
	return errors.Join(errs...)
}

// Subslots is the layout in device form, sorted by slot and subslot.
func (m DeviceManifest) Subslots() []device.Subslot {
	var out []device.Subslot
	for _, s := range m.Slots {
		for _, ss := range s.Subslots {
			out = append(out, device.Subslot{
				Addr:        device.Addr{Slot: s.Slot, Subslot: ss.Subslot},
				ModuleIdent: uint32(s.ModuleIdent), SubmoduleIdent: uint32(ss.SubmoduleIdent),
				InputLen: ss.Inputs, OutputLen: ss.Outputs,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Slot != out[j].Slot {
			return out[i].Slot < out[j].Slot
		}
		return out[i].Subslot < out[j].Subslot
	})
	return out
}

// Identity is the device's identity in device form.
func (m DeviceManifest) Identity() device.Identity {
	return device.Identity{
		Station: m.Station, VendorName: m.VendorName, VendorID: uint16(m.VendorID), DeviceID: uint16(m.DeviceID),
		OrderNumber: m.OrderNumber, HWRevision: 1, SWRevision: [3]byte{1, 0, 0},
	}
}

// InputNames are the nautilus input tags: data the PLC commands.
func (m DeviceManifest) InputNames() []string { return m.names(false) }

// OutputNames are the nautilus output tags: data nautilus provides.
func (m DeviceManifest) OutputNames() []string { return m.names(true) }

func (m DeviceManifest) names(toPLC bool) []string {
	var out []string
	for _, b := range m.Tags {
		if b.ToPLC() == toPLC {
			out = append(out, b.Name)
		}
	}
	sort.Strings(out)
	return out
}
