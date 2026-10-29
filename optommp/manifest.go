package optommp

// manifest.go is optommp_manifest.yaml: one processor, the modules it was
// imported with, and the tag bindings the driver polls. `naut optommp
// import` writes it from a live chassis; `naut check` validates it with no
// processor in sight.
//
//	host: 192.168.82.243
//	scan-rate: 20ms
//	unit: GRV-EPIC-PR1
//	modules:
//	  - { slot: 0, part: GRV-IACDCTTL-24, type: 0x50000005, channels: 24 }
//	tags:
//	  - { name: EPIC_M0_DI00, point: di, slot: 0, channel: 0 }
//	  - { name: EPIC_M2_AI00, point: ai, slot: 2, channel: 0, unit: "°C", lo: -270, hi: 150 }
//	  - { name: EPIC_SP_F0, point: scratch-float, index: 0, writable: true }
//
// Three options change how the driver talks to the processor; each writes
// the processor's own configuration, so none is on unless the manifest
// says so:
//
//	custom-area: true   map every word a scan needs into the processor's
//	                    custom data area, so a scan is one read
//	stream: 20ms        the processor pushes that area over UDP every 20 ms
//	                    instead of being polled (implies custom-area)
//	watchdog: 500ms     the I/O unit's communication watchdog: outputs
//	                    with a watchdog value go to it after 500 ms without
//	                    a request; 0s turns it off, absent leaves it alone
//
// One more changes only nautilus's side:
//
//	priority: 20        run the driver on a thread of its own at SCHED_FIFO
//	                    20 (Linux, needs CAP_SYS_NICE); keep it under
//	                    mmpServer's own 30, which every request waits on
//
// A binding's point says what it reads (and, for outputs, writes):
//
//	di, do          channel state, bool (do is written)
//	ai, ao          channel value in engineering units, REAL (ao is written)
//	counter         a digital channel's feature value (counter, totalizer …), DINT
//	on-latch        the channel turned on since the previous scan, bool
//	off-latch       … turned off since the previous scan, bool
//	quality         the processor's quality indicator code for the channel, DINT (0 = good)
//	scratch-bit     scratchpad bit 0..63, bool
//	scratch-int     scratchpad integer 0..1023, DINT
//	scratch-float   scratchpad float 0..1023, REAL
//	online          the driver is exchanging data with the processor now, BOOL
//	outages         times online has gone false since nautilus started, DINT
//	watchdog-trips  times the unit's communication watchdog may have
//	                driven outputs to their watchdog values, DINT
//
// Scratchpad points are the processor's shared registers — the seam to a
// PAC Control strategy, groov View or Node-RED on the same unit — and are
// written when writable: true.
//
// The last three are the driver's own status, not memory-map words (no
// slot, channel or index): a program reads them to hold its outputs after
// an outage until an operator acknowledges it. `naut optommp import`
// writes them as <ID>__Online, <ID>__Outages and <ID>__WatchdogTrips — the
// names every driver with a status uses.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Points.
const (
	PointDI           = "di"
	PointDO           = "do"
	PointAI           = "ai"
	PointAO           = "ao"
	PointCounter      = "counter"
	PointOnLatch      = "on-latch"
	PointOffLatch     = "off-latch"
	PointQuality      = "quality"
	PointScratchBit   = "scratch-bit"
	PointScratchInt   = "scratch-int"
	PointScratchFloat = "scratch-float"

	// The driver's own status (driver.go, "Status for the program").
	PointOnline        = "online"
	PointOutages       = "outages"
	PointWatchdogTrips = "watchdog-trips"
)

// Scratchpad sizes (form 1465, "Scratch Pad—Read/Write").
const (
	ScratchBits   = 64
	ScratchInts   = 1024
	ScratchFloats = 1024
)

// DefaultScanRate is the poll interval when neither the project nor the
// manifest names one. Two OptoMMP requests a scan cost ≈ 1.5 ms on the
// processor (brief §2), so 5 ms is the floor and 100 ms is generous.
const DefaultScanRate = 100 * time.Millisecond

// DefaultTimeout bounds one request.
const DefaultTimeout = 2 * time.Second

// Custom data area and stream limits (form 1465, "Custom Configuration
// Area" and "Streaming Configuration"): 1024 pointers, and a stream
// carries at most 1480 bytes of data.
const (
	MaxCustomWords = 1024
	MaxStreamWords = 1480 / 4
)

// Manifest is one processor's I/O surface.
type Manifest struct {
	// Host is the processor's address; 127.0.0.1 when nautilus runs on it.
	Host string `yaml:"host"`
	// Port is the OptoMMP port (default 2001).
	Port int `yaml:"port"`
	// ScanRate is the poll interval (default 100 ms); the project's
	// driver scan-rate overrides it.
	ScanRate Duration `yaml:"scan-rate"`
	// Timeout bounds one request (default 2 s).
	Timeout Duration `yaml:"timeout"`
	// Unit documents the processor the manifest was imported from.
	Unit string `yaml:"unit"`
	// CustomArea maps the words a scan needs into the processor's custom
	// data area (form 1465, F0D50000), so a scan is one read. The driver
	// owns pointers 0..n-1 while it runs.
	CustomArea bool `yaml:"custom-area"`
	// Stream, when non-zero, has the processor push the custom data area
	// to the driver over UDP at this interval instead of being polled. It
	// implies CustomArea and replaces the scan rate for inputs.
	Stream Duration `yaml:"stream"`
	// StreamPort is the local UDP port the stream is sent to (default:
	// any free port).
	StreamPort int `yaml:"stream-port"`
	// Watchdog sets the I/O unit's communication watchdog: after this long
	// without a request, outputs whose channel has a watchdog value
	// configured go to it. 0s turns it off; nil leaves the unit's setting
	// alone.
	Watchdog *Duration `yaml:"watchdog"`
	// Priority, when non-zero, runs the driver's loop on one locked thread
	// at SCHED_FIFO this priority, its socket calls made blocking on that
	// thread and its scans timed by the kernel's absolute sleep, so no
	// normal-priority thread sits between a reply and the driver. Needs
	// CAP_SYS_NICE; refused, the driver keeps the thread and says so.
	Priority int `yaml:"priority"`
	// Modules is the chassis as imported. The driver checks each slot's
	// module type against it on connect and every few seconds after: a
	// slot that is empty or holds another module makes its tags Bad.
	Modules []ModuleSpec `yaml:"modules"`
	Tags    []Binding    `yaml:"tags"`
}

// ModuleSpec is one slot as imported.
type ModuleSpec struct {
	Slot     int    `yaml:"slot"`
	Part     string `yaml:"part"`
	Type     Hex32  `yaml:"type"`
	Channels int    `yaml:"channels"`
}

// Binding maps one nautilus tag onto a point.
type Binding struct {
	Name     string `yaml:"name"`
	Point    string `yaml:"point"`
	Slot     int    `yaml:"slot"`
	Channel  int    `yaml:"channel"`
	Index    int    `yaml:"index"` // scratchpad element
	Writable bool   `yaml:"writable"`
	// Type is the channel type as imported ("ICTD Temperature Probe",
	// "±160 V"); Unit and Lo/Hi its engineering unit and range. The
	// driver does not use them; the tag file and a reviewer do.
	Type string   `yaml:"type"`
	Unit string   `yaml:"unit"`
	Lo   *float64 `yaml:"lo"`
	Hi   *float64 `yaml:"hi"`
	Desc string   `yaml:"desc"`
}

// Duration is a time.Duration spelled "20ms" in YAML.
type Duration time.Duration

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	v, err := time.ParseDuration(n.Value)
	if err != nil {
		return fmt.Errorf("duration %q: %w", n.Value, err)
	}
	*d = Duration(v)
	return nil
}

// Hex32 is a type code spelled 0x50000005 in YAML.
type Hex32 uint32

func (h *Hex32) UnmarshalYAML(n *yaml.Node) error {
	var v uint64
	if _, err := fmt.Sscan(n.Value, &v); err != nil || v > 0xFFFFFFFF {
		return fmt.Errorf("type code %q: want a 32-bit number like 0x50000005", n.Value)
	}
	*h = Hex32(v)
	return nil
}

func (h Hex32) String() string { return fmt.Sprintf("0x%08X", uint32(h)) }

// ParseManifest decodes an optommp_manifest.yaml (unknown keys are errors)
// and validates it. Defaults are filled in.
func ParseManifest(data []byte) (Manifest, error) {
	var m Manifest
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&m); err != nil {
		if errors.Is(err, io.EOF) {
			return Manifest{}, fmt.Errorf("optommp: manifest is empty")
		}
		return Manifest{}, fmt.Errorf("optommp: manifest: %w", err)
	}
	m.fillDefaults()
	if err := m.Validate(); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

func (m *Manifest) fillDefaults() {
	if m.Port == 0 {
		m.Port = DefaultPort
	}
	if m.ScanRate == 0 {
		m.ScanRate = Duration(DefaultScanRate)
	}
	if m.Timeout == 0 {
		m.Timeout = Duration(DefaultTimeout)
	}
}

// Validate checks the manifest offline; every finding is reported at once.
func (m Manifest) Validate() error {
	var errs []error
	if m.Host == "" {
		errs = append(errs, fmt.Errorf("host is required (127.0.0.1 when nautilus runs on the processor)"))
	}
	if m.Port < 0 || m.Port > 65535 {
		errs = append(errs, fmt.Errorf("port %d", m.Port))
	}
	if m.ScanRate < 0 || (m.ScanRate > 0 && time.Duration(m.ScanRate) < time.Millisecond) {
		errs = append(errs, fmt.Errorf("scan-rate %s (want at least 1ms)", time.Duration(m.ScanRate)))
	}
	if m.Stream < 0 || (m.Stream > 0 && time.Duration(m.Stream) < time.Millisecond) {
		errs = append(errs, fmt.Errorf("stream %s (want at least 1ms, or leave it out to poll)", time.Duration(m.Stream)))
	}
	if m.StreamPort < 0 || m.StreamPort > 65535 {
		errs = append(errs, fmt.Errorf("stream-port %d", m.StreamPort))
	}
	if m.StreamPort != 0 && m.Stream == 0 {
		errs = append(errs, fmt.Errorf("stream-port without stream"))
	}
	if m.Watchdog != nil {
		if w := time.Duration(*m.Watchdog); w < 0 || w%time.Millisecond != 0 {
			errs = append(errs, fmt.Errorf("watchdog %s (want whole milliseconds; 0s turns it off)", w))
		} else if w > 0 && m.Stream == 0 && m.ScanRate > 0 && w < 3*time.Duration(m.ScanRate) {
			errs = append(errs, fmt.Errorf("watchdog %s is under three scans (%s each): it would trip in normal running", w, time.Duration(m.ScanRate)))
		}
	}
	if m.Priority < 0 || m.Priority > 99 {
		errs = append(errs, fmt.Errorf("priority %d (SCHED_FIFO priorities are 1–99; 0 or absent runs normally)", m.Priority))
	}
	if m.CustomArea || m.Stream > 0 {
		n := len(planWords(m.Tags, true))
		if n > MaxCustomWords {
			errs = append(errs, fmt.Errorf("custom-area: the bindings need %d words, the processor has %d pointers", n, MaxCustomWords))
		} else if m.Stream > 0 && n > MaxStreamWords {
			errs = append(errs, fmt.Errorf("stream: the bindings need %d words, a stream carries at most %d — drop quality or latch tags, or poll", n, MaxStreamWords))
		}
	}
	slots := map[int]bool{}
	for _, mod := range m.Modules {
		if mod.Slot < 0 || mod.Slot >= MaxModules {
			errs = append(errs, fmt.Errorf("module slot %d (want 0..%d)", mod.Slot, MaxModules-1))
		}
		if slots[mod.Slot] {
			errs = append(errs, fmt.Errorf("module slot %d listed twice", mod.Slot))
		}
		slots[mod.Slot] = true
	}
	if len(m.Tags) == 0 {
		errs = append(errs, fmt.Errorf("no tag bindings"))
	}
	seen := map[string]bool{}
	for _, b := range m.Tags {
		if b.Name == "" {
			errs = append(errs, fmt.Errorf("a %s binding has no name", b.Point))
			continue
		}
		if seen[b.Name] {
			errs = append(errs, fmt.Errorf("duplicate tag name %q", b.Name))
			continue
		}
		seen[b.Name] = true
		if err := b.validate(); err != nil {
			errs = append(errs, fmt.Errorf("tag %s: %w", b.Name, err))
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("optommp: manifest:\n%w", errors.Join(errs...))
}

func (b Binding) validate() error {
	switch b.Point {
	case PointDI, PointAI, PointCounter, PointOnLatch, PointOffLatch, PointQuality:
		if b.Writable {
			return fmt.Errorf("a %s point is read-only", b.Point)
		}
		return b.channelInRange()
	case PointDO, PointAO:
		return b.channelInRange()
	case PointScratchBit:
		return indexInRange(b.Index, ScratchBits)
	case PointScratchInt:
		return indexInRange(b.Index, ScratchInts)
	case PointScratchFloat:
		return indexInRange(b.Index, ScratchFloats)
	case PointOnline, PointOutages, PointWatchdogTrips:
		if b.Writable {
			return fmt.Errorf("a %s point is read-only", b.Point)
		}
		return nil
	case "":
		return fmt.Errorf("point is required (%s)", strings.Join(allPoints, ", "))
	}
	return fmt.Errorf("point %q (want %s)", b.Point, strings.Join(allPoints, ", "))
}

var allPoints = []string{PointDI, PointDO, PointAI, PointAO, PointCounter, PointOnLatch, PointOffLatch,
	PointQuality, PointScratchBit, PointScratchInt, PointScratchFloat,
	PointOnline, PointOutages, PointWatchdogTrips}

func (b Binding) channelInRange() error {
	if b.Slot < 0 || b.Slot >= MaxModules {
		return fmt.Errorf("slot %d (want 0..%d)", b.Slot, MaxModules-1)
	}
	if b.Channel < 0 || b.Channel >= MaxChannels {
		return fmt.Errorf("channel %d (want 0..%d)", b.Channel, MaxChannels-1)
	}
	return nil
}

func indexInRange(i, n int) error {
	if i < 0 || i >= n {
		return fmt.Errorf("index %d (want 0..%d)", i, n-1)
	}
	return nil
}

// isOutput reports whether the driver writes the binding.
func (b Binding) isOutput() bool {
	switch b.Point {
	case PointDO, PointAO:
		return true
	case PointScratchBit, PointScratchInt, PointScratchFloat:
		return b.Writable
	}
	return false
}

// isChannel reports whether the binding lives on a module channel (and so
// shares that module's verdict when the slot is empty or swapped).
func (b Binding) isChannel() bool {
	switch b.Point {
	case PointScratchBit, PointScratchInt, PointScratchFloat:
		return false
	}
	return !b.isStatus()
}

// isStatus reports whether the binding is the driver's own status rather
// than a word of the memory map.
func (b Binding) isStatus() bool {
	switch b.Point {
	case PointOnline, PointOutages, PointWatchdogTrips:
		return true
	}
	return false
}

// InputNames lists every bound tag the driver delivers, sorted: all of
// them — outputs are read back off the processor, so a tag shows what the
// module is doing, not only what was asked.
func (m Manifest) InputNames() []string {
	out := make([]string, 0, len(m.Tags))
	for _, b := range m.Tags {
		out = append(out, b.Name)
	}
	sort.Strings(out)
	return out
}

// OutputNames lists the bound tags the driver writes, sorted.
func (m Manifest) OutputNames() []string {
	var out []string
	for _, b := range m.Tags {
		if b.isOutput() {
			out = append(out, b.Name)
		}
	}
	sort.Strings(out)
	return out
}
