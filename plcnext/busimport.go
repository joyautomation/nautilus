package plcnext

// busimport.go is `naut plcnext import`: a manifest's module list, offsets
// and tags generated from the local bus's device list, so the offsets
// stop being typed by hand.
//
// What the bus will say about itself from where naut sits is only how
// many modules there are: BusConductor's GDS port is CONFIG_REQ,
// CONFIG_MUST_MATCH, START_IO_REQ, CONFIGURED and NUM_MODULES, nothing
// else, and the Axioline master's own ReadConfiguration and AxioControl
// RPCs answer Unimplemented on firmware 2023.0 (open-controllers.md
// §3.1). BusConductor reads every device's type internally and does not
// publish it. So the device types come from a file the user writes in
// bus order — off the modules' data sheets, a TIC file's DeviceType, or
// an earlier engineering tool — and the live bus checks the count.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Module kinds the import knows how to bind.
const (
	KindDigitalIn  = "di"
	KindDigitalOut = "do"
	KindAnalogIn   = "ai"
	KindNone       = "none" // no process data
)

// KnownModule is one Axioline device type the import can lay out and bind.
type KnownModule struct {
	Type     uint32 // the device type, as a TIC file's DeviceType or the data sheet's
	Part     string // the part on the bench that reported it
	Kind     string
	Channels int
	Bytes    int    // its frame length, which it takes in BOTH images (generic mode)
	What     string // "16 digital outputs"
}

// knownModules is the table, from the bench AXC F 2152's bus (four Smart
// Elements, layout confirmed with eyes on 2026-10-27). A device type
// names a process-data shape, not a part: another module that reports
// the same type gets the same layout and binding under this part's name.
var knownModules = []KnownModule{
	{Type: 0x00400002, Part: "AXL SE DO16/1", Kind: KindDigitalOut, Channels: 16, Bytes: 2, What: "16 digital outputs"},
	{Type: 0x00800002, Part: "AXL SE DI16/1", Kind: KindDigitalIn, Channels: 16, Bytes: 2, What: "16 digital inputs"},
	{Type: 0x00000100, Part: "(no process data)", Kind: KindNone, What: "no process data"},
	{Type: 0x00200008, Part: "AXL SE AI4 U", Kind: KindAnalogIn, Channels: 4, Bytes: 8, What: "4 analog voltage inputs"},
}

// KnownModules is the import's table, for listing.
func KnownModules() []KnownModule { return append([]KnownModule(nil), knownModules...) }

func knownByType(t uint32) (KnownModule, bool) {
	for _, k := range knownModules {
		if k.Type == t {
			return k, true
		}
	}
	return KnownModule{}, false
}

func knownByPart(p string) (KnownModule, bool) {
	for _, k := range knownModules {
		if k.Kind != KindNone && strings.EqualFold(k.Part, strings.TrimSpace(p)) {
			return k, true
		}
	}
	return KnownModule{}, false
}

// DeviceType is an Axioline device type or id. It is always written in
// hexadecimal — 0x00400002, "0x00400002" or "00400002" as a TIC file
// has it — because YAML would read an unquoted 00400002 as decimal.
type DeviceType uint32

// UnmarshalYAML reads the scalar's text as hexadecimal.
func (d *DeviceType) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.ScalarNode {
		return fmt.Errorf("line %d: a device type is one hexadecimal number", n.Line)
	}
	s := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(n.Value), "0x"), "0X")
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return fmt.Errorf("line %d: device type %q: want hexadecimal, such as 0x00400002", n.Line, n.Value)
	}
	*d = DeviceType(v)
	return nil
}

func (d DeviceType) String() string { return fmt.Sprintf("0x%08X", uint32(d)) }

// DeviceList is plcnext_devices.yaml: the local bus's devices, in bus
// order (slot 1 first, next to the controller).
type DeviceList struct {
	Devices []Device `yaml:"devices"`
}

// Device is one slot of the local bus.
type Device struct {
	// Slot is the device's position, 1 first; optional, and when given it
	// must be its place in the list, so a skipped line is caught.
	Slot int `yaml:"slot"`
	// Type is the Axioline device type (the import's table is keyed on it).
	Type DeviceType `yaml:"type"`
	// ID is the device id beside the type in a TIC file, kept for the
	// record only.
	ID DeviceType `yaml:"id"`
	// Part names the module: with no type, a part from the table; with a
	// type, the name to use instead of the table's.
	Part string `yaml:"part"`
	// Length is the frame length in bits (a TIC file's DeviceLength), needed
	// only for a device the table does not know.
	Length *int `yaml:"length"`
	// Desc leads the module's desc ("K2.1", the label on the panel).
	Desc string `yaml:"desc"`
}

// ParseDeviceList decodes a plcnext_devices.yaml.
func ParseDeviceList(data []byte) (DeviceList, error) {
	var l DeviceList
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&l); err != nil {
		if errors.Is(err, io.EOF) {
			return DeviceList{}, fmt.Errorf("plcnext: device list is empty")
		}
		return DeviceList{}, fmt.Errorf("plcnext: device list: %w", err)
	}
	if len(l.Devices) == 0 {
		return DeviceList{}, fmt.Errorf("plcnext: device list: no devices")
	}
	return l, nil
}

// CheckCount compares the list with the number of modules the bus
// reports (BusConductor's NUM_MODULES).
func (l DeviceList) CheckCount(onBus int) error {
	if onBus == len(l.Devices) {
		return nil
	}
	return fmt.Errorf("the bus reports %d module(s) (BusConductor NUM_MODULES) and the device list has %d: a device is missing from the list, or the list is not this bus", onBus, len(l.Devices))
}

// ImportOptions shape the generated manifest.
type ImportOptions struct {
	// BusConductor is the component to nudge (default DefaultComponent;
	// "none" leaves the handshake out).
	BusConductor string
	// Transport is written when not the default grpc.
	Transport string
	// ID prefixes the driver's status tags, <ID>__Online, <ID>__Outages
	// and <ID>__WatchdogTrips (default DefaultImportID).
	ID string
}

// DefaultImportID is the status tags' prefix when the import is given none.
const DefaultImportID = "AXC"

// ImportedModule is one module of the generated manifest, where it sits
// and whether the table knew it.
type ImportedModule struct {
	Module
	Device Device
	Offset int  // its first byte, the same in both images
	Known  bool // false: a placeholder, its channels left to the user
	known  KnownModule
}

// Imported is what the import generates: the manifest, and the modules
// behind it in bus order.
type Imported struct {
	Manifest Manifest
	Modules  []ImportedModule
}

// Import lays the device list out as BusConductor's generic mode does —
// every module its frame length in both images, in bus order — and binds
// every channel of every module the table knows. Every problem is
// reported at once.
func Import(l DeviceList, opts ImportOptions) (Imported, error) {
	var errs []error
	var out Imported
	off := 0
	kindCount := map[string]int{}
	for i, d := range l.Devices {
		slot := i + 1
		if d.Slot != 0 && d.Slot != slot {
			errs = append(errs, fmt.Errorf("device %d says slot %d: the list is in bus order, so it is slot %d (a device missing above it?)", slot, d.Slot, slot))
		}
		k, known := KnownModule{}, false
		switch {
		case d.Type != 0:
			k, known = knownByType(uint32(d.Type))
		case d.Part != "":
			k, known = knownByPart(d.Part)
		}
		im := ImportedModule{Device: d, Offset: off, Known: known, known: k}
		im.Slot = slot
		if known {
			im.Part, im.In, im.Out = k.Part, k.Bytes, k.Bytes
			if d.Type != 0 && d.Part != "" { // the list names the part; the type found the layout
				im.Part = d.Part
			}
			im.Desc = moduleDesc(d, k.What, k.Type)
			kindCount[k.Kind]++
		} else {
			what := d.Type.String()
			if d.Type == 0 {
				what = fmt.Sprintf("part %q", d.Part)
			}
			switch {
			case d.Type == 0 && d.Part == "":
				errs = append(errs, fmt.Errorf("slot %d: no type and no part", slot))
				continue
			case d.Length == nil:
				errs = append(errs, fmt.Errorf("slot %d: %s is not in the import's table; give its frame length in bits (length:, a TIC file's DeviceLength or the data sheet's process data width) so the modules after it land right", slot, what))
				continue
			case *d.Length < 0 || *d.Length%8 != 0:
				errs = append(errs, fmt.Errorf("slot %d: length %d bits is not a whole number of bytes", slot, *d.Length))
				continue
			}
			n := *d.Length / 8
			im.In, im.Out = n, n
			im.Part = d.Part
			if im.Part == "" {
				im.Part = "unknown " + d.Type.String()
			}
			im.Desc = moduleDesc(d, fmt.Sprintf("%d bits, not in naut's table: bind it by hand", *d.Length), uint32(d.Type))
		}
		off += im.In
		out.Modules = append(out.Modules, im)
	}
	if off > 512 {
		errs = append(errs, fmt.Errorf("the modules take %d bytes, more than the 512-byte image", off))
	}
	if len(errs) > 0 {
		return Imported{}, fmt.Errorf("plcnext: import:\n%w", errors.Join(errs...))
	}

	m := Manifest{Transport: opts.Transport, BusConductor: opts.BusConductor}
	if m.Transport == TransportGRPC {
		m.Transport = ""
	}
	switch m.BusConductor {
	case "":
		m.BusConductor = DefaultComponent
	case "none":
		m.BusConductor = ""
	}
	for _, im := range out.Modules {
		m.Modules = append(m.Modules, im.Module)
	}
	// Inputs first, then outputs, each in bus order.
	for _, dir := range []string{DirIn, DirOut} {
		for _, im := range out.Modules {
			if im.Known {
				m.Tags = append(m.Tags, channelTags(im, dir, kindCount[im.known.Kind] > 1)...)
			}
		}
	}
	// The driver's own status, bound like the optommp import binds it, so
	// a program can hold its outputs after an outage until acknowledged.
	id := opts.ID
	if id == "" {
		id = DefaultImportID
	}
	for _, st := range []struct{ suffix, status string }{
		{"__Online", StatusOnline}, {"__Outages", StatusOutages}, {"__WatchdogTrips", StatusWatchdogTrips},
	} {
		m.Tags = append(m.Tags, Binding{Name: id + st.suffix, Status: st.status})
	}
	out.Manifest = m
	return out, nil
}

func moduleDesc(d Device, what string, typ uint32) string {
	s := what
	if typ != 0 {
		s += fmt.Sprintf(" (device 0x%08X", typ)
		if d.ID != 0 {
			s += ", id " + d.ID.String()
		}
		s += ")"
	}
	if d.Desc != "" {
		s = d.Desc + ", " + s
	}
	return s
}

// channelTags binds every channel of one known module in one direction.
// Names are DI_00, DO_00, AI_00 (zero-based, the bench's convention) when
// the bus has one module of the kind, S<slot>_DI_00 when it has more.
func channelTags(im ImportedModule, dir string, qualify bool) []Binding {
	k := im.known
	prefix := map[string]string{KindDigitalIn: "DI", KindDigitalOut: "DO", KindAnalogIn: "AI"}[k.Kind]
	if qualify {
		prefix = fmt.Sprintf("S%d_%s", im.Slot, prefix)
	}
	var tags []Binding
	switch {
	case k.Kind == KindDigitalIn && dir == DirIn, k.Kind == KindDigitalOut && dir == DirOut:
		what := "input"
		if dir == DirOut {
			what = "output"
		}
		for ch := 1; ch <= k.Channels; ch++ {
			b := Binding{
				Name: fmt.Sprintf("%s_%02d", prefix, ch-1), Dir: dir, Offset: im.Offset,
				Format: "ch:" + strconv.Itoa(ch), Desc: fmt.Sprintf("slot %d %s %02d", im.Slot, what, ch),
			}
			if dir == DirOut {
				b.Init = false
			}
			tags = append(tags, b)
		}
	case k.Kind == KindAnalogIn && dir == DirIn:
		for ch := 0; ch < k.Channels; ch++ {
			tags = append(tags, Binding{
				Name: fmt.Sprintf("%s_%02d", prefix, ch), Dir: DirIn, Offset: im.Offset + 2*ch,
				Format: "int16", Desc: fmt.Sprintf("slot %d analog input %d, raw counts", im.Slot, ch),
			})
		}
	}
	return tags
}

// hasChannels reports whether any binding reaches the image (the status
// bindings the import always writes do not).
func hasChannels(m Manifest) bool {
	for _, t := range m.Tags {
		if t.Status == "" {
			return true
		}
	}
	return false
}

// ImportedYAML renders the generated manifest: the modules in bus order,
// a placeholder (commented) for every module the table does not know, and
// the bindings in ch:N for the digital channels.
func ImportedYAML(imp Imported, command string) []byte {
	m := imp.Manifest
	var b strings.Builder
	fmt.Fprintf(&b, "# Generated by `%s`\n", command)
	b.WriteString("# from the device list, in bus order. In BusConductor's generic mode every\n")
	b.WriteString("# module takes its frame length in BOTH images, so a module's offset is the\n")
	b.WriteString("# sum of the lengths before it, the same in the input and the output image.\n")
	b.WriteString("# Digital channels are ch:N, the number on the module's terminals. A device\n")
	b.WriteString("# type gives the layout, not the part: check one output LED and one input\n")
	b.WriteString("# with eyes before trusting it. `naut check` validates this offline.\n")
	if m.Transport != "" {
		fmt.Fprintf(&b, "transport: %s\n", m.Transport)
	}
	fmt.Fprintf(&b, "bus-conductor: %s\n", quoteYAML(m.BusConductor))
	b.WriteString("modules:\n")
	for _, im := range imp.Modules {
		if !im.Known {
			fmt.Fprintf(&b, "  # slot %d is not in naut's table: its %d byte(s) are at offset %d in both images.\n", im.Slot, im.In, im.Offset)
		}
		fmt.Fprintf(&b, "  - { slot: %d, part: %s", im.Slot, quoteYAML(im.Part))
		if im.In != 0 || im.Out != 0 {
			fmt.Fprintf(&b, ", in: %d, out: %d", im.In, im.Out)
		}
		fmt.Fprintf(&b, ", desc: %s }\n", quoteYAML(im.Desc))
	}
	b.WriteString("tags:\n")
	if !hasChannels(m) {
		b.WriteString("  # none yet: bind the modules below by hand, then `naut check`\n")
	}
	for _, t := range m.Tags {
		if t.Status != "" {
			fmt.Fprintf(&b, "  - { name: %s, status: %s }\n", t.Name, t.Status)
			continue
		}
		fmt.Fprintf(&b, "  - { name: %s, dir: %s, offset: %d, format: %s", t.Name, t.Dir, t.Offset, quoteYAML(t.Format))
		if t.Init != nil {
			fmt.Fprintf(&b, ", init: %v", t.Init)
		}
		fmt.Fprintf(&b, ", desc: %s }\n", quoteYAML(t.Desc))
	}
	for _, im := range imp.Modules {
		if im.Known || im.In == 0 {
			continue
		}
		fmt.Fprintf(&b, "  # slot %d (%s): %d byte(s) at offset %d — bind its channels by hand, for example\n", im.Slot, im.Part, im.In, im.Offset)
		fmt.Fprintf(&b, "  # - { name: S%d_IN_00, dir: in, offset: %d, format: \"ch:1\", desc: \"slot %d input 01\" }\n", im.Slot, im.Offset, im.Slot)
		fmt.Fprintf(&b, "  # - { name: S%d_OUT_00, dir: out, offset: %d, format: \"ch:1\", init: false, desc: \"slot %d output 01\" }\n", im.Slot, im.Offset, im.Slot)
		fmt.Fprintf(&b, "  # - { name: S%d_AI_00, dir: in, offset: %d, format: int16, desc: \"slot %d analog input 0\" }\n", im.Slot, im.Offset, im.Slot)
	}
	return []byte(b.String())
}

func quoteYAML(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return `"` + r.Replace(s) + `"`
}

// BusModules reads how many modules BusConductor found, without touching
// its requests (CONFIGURED, then NUM_MODULES): an import never starts or
// reconfigures a bus. A bus not yet configured is an error that says so.
func BusModules(ctx context.Context, c *Client, component string) (int, error) {
	if component == "" {
		component = DefaultComponent
	}
	p := func(name string) string { return component + busConductorPorts + name }
	cctx, cancel := withTimeout(ctx, defaultCallTO)
	defer cancel()
	configured, err := c.ReadBool(cctx, p("CONFIGURED"))
	if err != nil {
		return 0, err
	}
	if !configured {
		return 0, fmt.Errorf("plcnext: %s has not configured the bus, so NUM_MODULES means nothing yet: run `naut plcnext browse` (it brings the bus up) or start the project first", component)
	}
	n, err := c.ReadNumber(cctx, p("NUM_MODULES"))
	if err != nil {
		return 0, err
	}
	return int(n), nil
}
