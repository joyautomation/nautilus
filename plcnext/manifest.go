// Package plcnext runs the nautilus runtime ON a Phoenix Contact PLCnext
// controller (AXC F 1152/2152/3152, RFC 4072S …) and drives its local
// Axioline I/O. PLCnext firmware exposes its Global Data Space over gRPC on
// a Unix socket (/run/plcnext/grpc.sock), and BusConductor — Phoenix
// Contact's own open component, github.com/PLCnext/BusConductor — configures
// the Axioline bus at run time and presents the whole process image as two
// 512-byte arrays, Arp.Io.AxlC/0.DI4096 and Arp.Io.AxlC/0.DO4096. The
// data-access service cannot see I/O components' ports directly, so the
// project's GDS configuration connects those two onto a pair of
// MainProcess byte-array ports it can — on a bare controller the eCLR
// system variables Arp.Plc.Eclr/PND_S1_INPUTS and PND_S1_OUTPUTS, 512
// bytes each, which the PROFINET device (unused here) would otherwise
// own. This driver polls the one, writes the other, and binds nautilus
// tags to byte and bit offsets inside them; PLCnext Engineer is never
// involved.
//
// What crosses the seam upward is io.Driver; Phoenix Contact's spellings
// (GDS port names, Arp types, BusConductor's ports) stop here. The gRPC and
// protobuf dependencies stay inside this package, the way gosnmp stays
// inside snmp/ and paho inside sparkplug/. Two routes for the bytes
// (`transport`): grpc polls the mirrored arrays at the scan rate over the
// local socket, best-effort; shm exchanges the Axioline frames with
// rt/plcnext-io/nautilus_io, an ACF-started C process that touches them
// through the firmware's ANSI-C API every millisecond, through a
// shared-memory page (shm.go; docs/design/open-controllers.md §3.4).
package plcnext

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Defaults for the manifest's optional keys.
const (
	DefaultSocket    = "/run/plcnext/grpc.sock"
	DefaultInputs    = "Arp.Plc.Eclr/PND_S1_INPUTS"
	DefaultOutputs   = "Arp.Plc.Eclr/PND_S1_OUTPUTS"
	DefaultRunPort   = "Arp.Plc.Eclr/AXIO_DIAG_STATUS_REG_RUN"
	DefaultComponent = "BcComponent1"
	// AxiolineInputs / AxiolineOutputs are BusConductor's own image ports,
	// the start and end of the GDS connectors that mirror them onto
	// DefaultInputs / DefaultOutputs. The driver never names them; the
	// project's gds.config does.
	AxiolineInputs    = "Arp.Io.AxlC/0.DI4096"
	AxiolineOutputs   = "Arp.Io.AxlC/0.DO4096"
	DefaultByteOrder  = "big"
	ByteOrderBig      = "big"
	ByteOrderLittle   = "little"
	DirIn             = "in"
	DirOut            = "out"
	TransportGRPC     = "grpc"
	TransportShm      = "shm"
	defaultTagFileHdr = "plcnext_manifest.yaml"
)

// Manifest is one controller's I/O surface: where the firmware's gRPC
// socket is, which GDS ports carry the process image, whether BusConductor
// has to be nudged into configuring the bus, and the tag bindings.
type Manifest struct {
	// Transport is the bytes' route: "grpc" (default) polls the GDS
	// mirror ports through the data-access service; "shm" exchanges the
	// frames with rt/plcnext-io/nautilus_io through its shared-memory
	// page, at the shim's 1 ms cycle. The bindings mean the same bytes
	// either way; the BusConductor handshake runs over gRPC in both.
	Transport string `yaml:"transport"`
	// Shm is the shim's page (default DefaultShm), for transport shm.
	Shm string `yaml:"shm"`
	// OutputWatchdog is how old this driver's outputs may get in the page
	// before the shim drives the output frame to zero (transport shm; a
	// Go duration, default 1s, "0" holds the last outputs). The driver
	// refreshes the page every scan, so a naut that stops — crash, kill,
	// redeploy — is what trips it.
	OutputWatchdog string `yaml:"output-watchdog"`
	// Socket is the firmware's gRPC Unix socket (default DefaultSocket).
	Socket string `yaml:"socket"`
	// BusConductor names the BusConductor component instance whose ports
	// the driver pulses at Start (CONFIG_REQ, then START_IO_REQ) — the
	// PLM component name, "BcComponent1" in the stock no-Engineer
	// deployment. Empty skips the handshake for a controller whose bus
	// is configured some other way.
	BusConductor string `yaml:"bus-conductor"`
	// Inputs / Outputs are the MainProcess byte-array ports the GDS
	// connectors mirror the process image onto (defaults DefaultInputs /
	// DefaultOutputs). The driver reads and writes them element by
	// element, as the data-access service requires.
	Inputs  string `yaml:"inputs"`
	Outputs string `yaml:"outputs"`
	// RunPort is the controller's own "the local bus is exchanging
	// process data" diagnostic (default DefaultRunPort); it is read with
	// every scan and a stopped bus makes every input Bad. Empty disables
	// the check.
	RunPort string `yaml:"run-port"`
	// ByteOrder is how multi-byte values sit in the arrays: "big"
	// (default; Axioline process data is Motorola order) or "little".
	ByteOrder string `yaml:"byte-order"`
	// Modules documents the bus, slot by slot, so the offsets below can be
	// read against something — the driver does not use it, but `naut
	// plcnext tags` and a reviewer do.
	Modules []Module  `yaml:"modules"`
	Tags    []Binding `yaml:"tags"`
}

// Module is one Axioline device on the local bus, in bus order.
type Module struct {
	Slot int    `yaml:"slot"`
	Part string `yaml:"part"` // "AXL F DI16/4 1F"
	In   int    `yaml:"in"`   // process-data bytes it contributes to the input image
	Out  int    `yaml:"out"`  // … and to the output image
	Desc string `yaml:"desc"`
}

// Binding maps one nautilus tag onto a span of the input or output image.
type Binding struct {
	Name string `yaml:"name"`
	// Dir is "in" (polled from Inputs) or "out" (written to Outputs).
	Dir string `yaml:"dir"`
	// Offset is the byte offset inside the image.
	Offset int `yaml:"offset"`
	// Format is ch:N (digital channel N of the module whose first byte is
	// Offset, numbered as on its terminals: channel 1 is bit 0 of that
	// byte, channel 9 bit 0 of the next), bit:N (bit N of the 16-bit word
	// at Offset in byte-order, N 0..15), or uint8 int8 uint16 int16
	// uint32 int32 float32.
	Format string `yaml:"format"`
	// Scale/Offset-in-engineering-units: engineering = raw*Scale + Bias.
	Scale float64 `yaml:"scale"`
	Bias  float64 `yaml:"bias"`
	Unit  string  `yaml:"unit"`
	Desc  string  `yaml:"desc"`
	// Init seeds an output tag in the tag file.
	Init any `yaml:"init"`
}

// Format is a parsed Binding.Format.
type Format struct {
	Kind string // ch bit uint8 int8 uint16 int16 uint32 int32 float32
	Bit  int    // for bit
	Ch   int    // for ch: the 1-based channel
}

// maxChannel is the highest ch:N a 512-byte image can hold.
const maxChannel = 512 * 8

// Bytes is the span the format occupies in the image, from the binding's
// offset; for ch:N, up to and including the byte that holds the channel.
func (f Format) Bytes() int {
	switch f.Kind {
	case "ch":
		return (f.Ch-1)/8 + 1
	case "uint8", "int8":
		return 1
	case "bit", "uint16", "int16":
		return 2
	default:
		return 4
	}
}

// Digital reports whether the format is one bit (ch:N or bit:N).
func (f Format) Digital() bool { return f.Kind == "ch" || f.Kind == "bit" }

func (f Format) String() string {
	switch f.Kind {
	case "ch":
		return "ch:" + strconv.Itoa(f.Ch)
	case "bit":
		return "bit:" + strconv.Itoa(f.Bit)
	}
	return f.Kind
}

// bitAt is where a digital format's bit sits: its byte, relative to the
// binding's offset, and the bit inside that byte. ch:N counts byte by
// byte, the way an Axioline module numbers its channels, so the byte
// order plays no part; bit:N counts in the 16-bit word at the offset, so
// it does.
func (f Format) bitAt(byteOrder string) (byteIdx, bit int) {
	if f.Kind == "ch" {
		return (f.Ch - 1) / 8, (f.Ch - 1) % 8
	}
	if byteOrder == ByteOrderLittle {
		return f.Bit / 8, f.Bit % 8
	}
	return 1 - f.Bit/8, f.Bit % 8
}

// ParseFormat parses a binding format. ch:N takes leading zeros, so a
// terminal labelled 01 can be written ch:01.
func ParseFormat(s string) (Format, error) {
	if strings.HasPrefix(s, "ch:") {
		n, err := strconv.Atoi(strings.TrimPrefix(s, "ch:"))
		if err != nil || n < 1 || n > maxChannel {
			return Format{}, fmt.Errorf("format %q: want ch:1 or more — the channel number printed on the module, with the module's first byte as the offset (channel 01 is ch:1)", s)
		}
		return Format{Kind: "ch", Ch: n}, nil
	}
	if strings.HasPrefix(s, "bit:") {
		n, err := strconv.Atoi(strings.TrimPrefix(s, "bit:"))
		if err != nil || n < 0 || n > 15 {
			return Format{}, fmt.Errorf("format %q: want bit:0 … bit:15, or ch:N for a module's channel N", s)
		}
		return Format{Kind: "bit", Bit: n}, nil
	}
	switch s {
	case "uint8", "int8", "uint16", "int16", "uint32", "int32", "float32":
		return Format{Kind: s}, nil
	}
	return Format{}, fmt.Errorf("format %q: want ch:N (a digital module's channel N, as printed on it), bit:N, uint8, int8, uint16, int16, uint32, int32 or float32", s)
}

// ParseManifest decodes a plcnext_manifest.yaml with KnownFields semantics
// and validates it. Defaults are filled in.
func ParseManifest(data []byte) (Manifest, error) {
	var m Manifest
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&m); err != nil {
		if errors.Is(err, io.EOF) {
			return Manifest{}, fmt.Errorf("plcnext: manifest is empty")
		}
		return Manifest{}, fmt.Errorf("plcnext: manifest: %w", err)
	}
	m.fillDefaults()
	if err := m.Validate(); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

func (m *Manifest) fillDefaults() {
	if m.Transport == "" {
		m.Transport = TransportGRPC
	}
	if m.Shm == "" {
		m.Shm = DefaultShm
	}
	if m.OutputWatchdog == "" {
		m.OutputWatchdog = DefaultOutputWatchdog.String()
	}
	if m.Socket == "" {
		m.Socket = DefaultSocket
	}
	if m.Inputs == "" {
		m.Inputs = DefaultInputs
	}
	if m.Outputs == "" {
		m.Outputs = DefaultOutputs
	}
	if m.ByteOrder == "" {
		m.ByteOrder = DefaultByteOrder
	}
	switch m.RunPort {
	case "":
		m.RunPort = DefaultRunPort
	case "none":
		m.RunPort = ""
	}
}

// DefaultOutputWatchdog is the shim's grace for stale outputs.
const DefaultOutputWatchdog = time.Second

// watchdog is the validated OutputWatchdog.
func (m Manifest) watchdog() time.Duration {
	wd, _ := time.ParseDuration(m.OutputWatchdog)
	return wd
}

// span is how many bytes of an image the bindings in that direction
// reach: what one scan reads, and what one flush writes. At least one
// byte, so an image with no bindings still polls (the RUN bit rides on
// the same round trip).
func (m Manifest) span(dir string) int {
	n := 1
	for _, b := range m.Tags {
		if b.Dir != dir {
			continue
		}
		if end := b.Offset + b.format().Bytes(); end > n {
			n = end
		}
	}
	return n
}

// Validate checks the bindings offline — the whole of what `naut check`
// needs. Every finding is reported at once.
func (m Manifest) Validate() error {
	var errs []error
	if m.ByteOrder != ByteOrderBig && m.ByteOrder != ByteOrderLittle {
		errs = append(errs, fmt.Errorf("byte-order %q (want big or little)", m.ByteOrder))
	}
	if m.Transport != TransportGRPC && m.Transport != TransportShm {
		errs = append(errs, fmt.Errorf("transport %q (want grpc or shm)", m.Transport))
	}
	if wd, err := time.ParseDuration(m.OutputWatchdog); err != nil || wd < 0 {
		errs = append(errs, fmt.Errorf("output-watchdog %q (want a duration such as 1s, or 0 to hold the last outputs)", m.OutputWatchdog))
	}
	if len(m.Tags) == 0 {
		errs = append(errs, fmt.Errorf("no tag bindings"))
	}
	seen := map[string]bool{}
	for _, b := range m.Tags {
		if b.Name == "" {
			errs = append(errs, fmt.Errorf("a binding at offset %d has no name", b.Offset))
			continue
		}
		if seen[b.Name] {
			errs = append(errs, fmt.Errorf("duplicate tag name %q", b.Name))
			continue
		}
		seen[b.Name] = true
		if b.Dir != DirIn && b.Dir != DirOut {
			errs = append(errs, fmt.Errorf("tag %s: dir %q (want in or out)", b.Name, b.Dir))
		}
		if b.Offset < 0 || b.Offset >= 512 {
			errs = append(errs, fmt.Errorf("tag %s: offset %d outside the 512-byte image", b.Name, b.Offset))
		}
		f, err := ParseFormat(b.Format)
		if err != nil {
			errs = append(errs, fmt.Errorf("tag %s: %w", b.Name, err))
			continue
		}
		if b.Offset+f.Bytes() > 512 {
			errs = append(errs, fmt.Errorf("tag %s: %s at offset %d runs past the 512-byte image", b.Name, f, b.Offset))
		}
		if f.Digital() && (b.Scale != 0 || b.Bias != 0) {
			errs = append(errs, fmt.Errorf("tag %s: scale/bias on a bit", b.Name))
		}
		if err := m.channelInModule(b, f); err != nil {
			errs = append(errs, err)
		}
	}
	for _, mod := range m.Modules {
		if mod.In < 0 || mod.Out < 0 {
			errs = append(errs, fmt.Errorf("module slot %d: negative byte count", mod.Slot))
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("plcnext: manifest:\n%w", errors.Join(errs...))
}

// InputNames / OutputNames list the bound tags by direction, sorted.
func (m Manifest) InputNames() []string  { return m.names(DirIn) }
func (m Manifest) OutputNames() []string { return m.names(DirOut) }

func (m Manifest) names(dir string) []string {
	var out []string
	for _, b := range m.Tags {
		if b.Dir == dir {
			out = append(out, b.Name)
		}
	}
	sort.Strings(out)
	return out
}

// format is the parsed format of a validated binding.
func (b Binding) format() Format {
	f, _ := ParseFormat(b.Format)
	return f
}
