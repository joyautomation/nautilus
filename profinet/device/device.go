// Package device is a PROFINET IO device (RT_CLASS_1, one AR): DCP and its
// responder, LLDP, the AR over DCE/RPC, cyclic IO data and alarm
// acknowledgement. The nautilus driver (profinet.DeviceDriver) puts tags
// in and takes tags out; this package knows only bytes per subslot.
//
// Ported from tentacle-next's internal/profinet, with the rules that
// source lacked (docs/design/profinet.md §4.4):
//
//   - the controller's DataStatus and IOPS are read on every frame and
//     reported (Snapshot), so a PLC in STOP is not mistaken for live
//     commands;
//   - the output watchdog aborts the AR when the controller goes quiet;
//   - the expected configuration is compared against the layout and a
//     ModuleDiffBlock reports what differs;
//   - one AR at a time: a Connect from another controller while one is
//     running is refused (the same controller reconnecting replaces it);
//   - DCP Set IP is never applied to the host. A Set that names the
//     address the interface already has is accepted; any other is refused
//     and counted.
package device

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/joyautomation/nautilus/profinet/transport"
	"github.com/joyautomation/nautilus/profinet/wire"
)

// The device access point (slot 0) every nautilus device presents. The
// GSDML (profinet/gsdml) and the identification records must agree on
// these, or the engineering tool reports a different device type.
const (
	DAPModuleIdent       uint32 = 0x00010000
	DAPSubmoduleIdent    uint32 = 0x00000001
	InterfaceSubmodIdent uint32 = 0x00008000
	PortSubmodIdent      uint32 = 0x00008001
	InterfaceSubslot     uint16 = 0x8000
	PortSubslot          uint16 = 0x8001
)

// Addr names one subslot.
type Addr struct{ Slot, Subslot uint16 }

func (a Addr) String() string { return fmt.Sprintf("%d.%d", a.Slot, a.Subslot) }

// Subslot is one submodule of the layout. Input and output are seen from
// the controller, as the GSDML and the engineering tool see them: Input
// is data this device provides, Output is data the controller commands.
type Subslot struct {
	Addr
	ModuleIdent    uint32
	SubmoduleIdent uint32
	InputLen       uint16
	OutputLen      uint16
}

// Identity is what the device says about itself in DCP, I&M0 and RPC.
type Identity struct {
	Station     string // NameOfStation
	VendorName  string // DCP "type of station"
	VendorID    uint16
	DeviceID    uint16
	OrderNumber string // I&M0 OrderID; must equal the GSDML's OrderNumber
	Serial      string
	HWRevision  uint16
	SWRevision  [3]byte
}

// Config is everything New needs. Port and RPC are owned by the device
// from New on and closed by Run when it returns.
type Config struct {
	Identity
	Subslots []Subslot // user slots (≥ 1); slot 0 is the DAP and implicit

	Port transport.Port // raw Ethernet: DCP, RT, RTA, LLDP
	RPC  net.PacketConn // UDP for DCE/RPC (34964 in the field)

	// ControllerCMPort is the controller's DCE/RPC port, where the device
	// sends its CControl(ApplicationReady). 0 means 34964.
	ControllerCMPort int
	// InterfaceIP reports the address the device's interface holds (nil
	// when it has none). A DCP Set IP is accepted only when it names this
	// address. nil means none.
	InterfaceIP func() (ip, mask net.IP)
	// MinWatchdog is the shortest output watchdog the device applies,
	// however small the controller's cycle × factor: a non-real-time host
	// stalls for longer than 3 × 1 ms now and then. 0 means 100 ms.
	MinWatchdog time.Duration
	// LLDPEvery is the LLDP announcement period; 0 means 5 s.
	LLDPEvery time.Duration
	Log       *slog.Logger
}

// Device is one running IO device. Use New, then Run.
type Device struct {
	cfg  Config
	log  *slog.Logger
	subs map[Addr]*Subslot // every subslot incl. the DAP's

	mu sync.Mutex
	// The device's process image: inputs set by the driver, outputs as
	// last received.
	in       map[Addr][]byte
	inValid  bool
	out      map[Addr][]byte
	outGood  map[Addr]bool
	ar       *ar // the current AR, nil when none
	station  string
	ipSet    bool // DCP: an IP suite has been set (and matches the interface)
	snapInfo snapInfo

	counters counters
	boot     uint32
	started  atomic.Bool
	runCtx   context.Context // Run's context; parent of each AR's cyclic loop
}

type snapInfo struct {
	state      State
	since      time.Time
	controller string
	ctrlMAC    net.HardwareAddr
	dataStatus uint8
	fresh      bool // an output frame arrived in this AR
	lastEvent  string
	diff       []wire.DiffModule
}

type counters struct {
	ars, aborts, refusedARs, setIPRefused, nameSets, alarms, outputFrames uint64
}

// State is where the AR is.
type State int

const (
	// StateIdle: no AR.
	StateIdle State = iota
	// StateStartup: Connect accepted, parameterisation and PrmEnd under way.
	StateStartup
	// StateData: cyclic exchange running.
	StateData
)

func (s State) String() string {
	switch s {
	case StateStartup:
		return "startup"
	case StateData:
		return "data"
	}
	return "idle"
}

// New validates cfg and builds a device. It does not touch the network.
func New(cfg Config) (*Device, error) {
	if cfg.Port == nil || cfg.RPC == nil {
		return nil, errors.New("profinet device: Port and RPC are required")
	}
	if cfg.Station == "" {
		return nil, errors.New("profinet device: station name is required")
	}
	if len(cfg.Subslots) == 0 {
		return nil, errors.New("profinet device: no subslots")
	}
	if cfg.ControllerCMPort == 0 {
		cfg.ControllerCMPort = wire.PNIOCMPort
	}
	if cfg.MinWatchdog == 0 {
		cfg.MinWatchdog = 100 * time.Millisecond
	}
	if cfg.LLDPEvery == 0 {
		cfg.LLDPEvery = 5 * time.Second
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	d := &Device{
		cfg:     cfg,
		log:     cfg.Log,
		subs:    map[Addr]*Subslot{},
		in:      map[Addr][]byte{},
		out:     map[Addr][]byte{},
		outGood: map[Addr]bool{},
		station: cfg.Station,
		inValid: true,
		boot:    uint32(time.Now().Unix()),
	}
	for _, s := range dapSubslots() {
		d.subs[s.Addr] = &s
	}
	for i := range cfg.Subslots {
		s := cfg.Subslots[i]
		if s.Slot == 0 {
			return nil, fmt.Errorf("profinet device: subslot %s: slot 0 is the access point", s.Addr)
		}
		if _, dup := d.subs[s.Addr]; dup {
			return nil, fmt.Errorf("profinet device: subslot %s twice", s.Addr)
		}
		d.subs[s.Addr] = &s
		d.in[s.Addr] = make([]byte, s.InputLen)
		d.out[s.Addr] = make([]byte, s.OutputLen)
	}
	d.snapInfo.since = time.Now()
	return d, nil
}

func dapSubslots() []Subslot {
	return []Subslot{
		{Addr: Addr{0, 1}, ModuleIdent: DAPModuleIdent, SubmoduleIdent: DAPSubmoduleIdent},
		{Addr: Addr{0, InterfaceSubslot}, ModuleIdent: DAPModuleIdent, SubmoduleIdent: InterfaceSubmodIdent},
		{Addr: Addr{0, PortSubslot}, ModuleIdent: DAPModuleIdent, SubmoduleIdent: PortSubmodIdent},
	}
}

// SetInput sets the data the device provides for one subslot (copied;
// shorter data is zero-filled, longer is cut).
func (d *Device) SetInput(a Addr, data []byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	buf, ok := d.in[a]
	if !ok {
		return
	}
	n := copy(buf, data)
	clear(buf[n:])
}

// SetInputsValid says whether the inputs are worth believing: false (a
// standby, a hung scan) sends IOPS Bad on every input subslot, so the
// controller sees invalid data rather than frozen-good values.
func (d *Device) SetInputsValid(ok bool) {
	d.mu.Lock()
	d.inValid = ok
	d.mu.Unlock()
}

// Snapshot is the device's state for the driver and its status row.
type Snapshot struct {
	State      State
	Since      time.Time
	Station    string // current NameOfStation (a DCP Set may change it)
	Controller string // the AR's controller station name
	CtrlMAC    net.HardwareAddr
	// Run is true while the controller's frames say provider run, primary
	// and valid (wire.MeaningOf == StatusRun); Fresh is true once an
	// output frame has arrived in this AR.
	Meaning    wire.DataStatusMeaning
	Fresh      bool
	Outputs    map[Addr][]byte // last received, per output subslot
	OutputGood map[Addr]bool   // the controller's IOPS for it (and the subslot matches the layout)
	Diff       []wire.DiffModule
	LastEvent  string

	ARs, Aborts, RefusedARs, SetIPRefused, NameSets, Alarms, OutputFrames uint64
}

// Snapshot copies the current state.
func (d *Device) Snapshot() Snapshot {
	d.mu.Lock()
	defer d.mu.Unlock()
	s := Snapshot{
		State:        d.snapInfo.state,
		Since:        d.snapInfo.since,
		Station:      d.station,
		Controller:   d.snapInfo.controller,
		CtrlMAC:      d.snapInfo.ctrlMAC,
		Meaning:      wire.MeaningOf(d.snapInfo.dataStatus),
		Fresh:        d.snapInfo.fresh,
		Outputs:      make(map[Addr][]byte, len(d.out)),
		OutputGood:   make(map[Addr]bool, len(d.outGood)),
		Diff:         d.snapInfo.diff,
		LastEvent:    d.snapInfo.lastEvent,
		ARs:          d.counters.ars,
		Aborts:       d.counters.aborts,
		RefusedARs:   d.counters.refusedARs,
		SetIPRefused: d.counters.setIPRefused,
		NameSets:     d.counters.nameSets,
		Alarms:       d.counters.alarms,
		OutputFrames: d.counters.outputFrames,
	}
	if !s.Fresh {
		s.Meaning = wire.StatusInvalid
	}
	for a, b := range d.out {
		s.Outputs[a] = append([]byte(nil), b...)
	}
	for a, g := range d.outGood {
		s.OutputGood[a] = g
	}
	return s
}

// setState records a state change (callers hold mu).
func (d *Device) setState(st State, event string) {
	if st != d.snapInfo.state {
		d.snapInfo.state = st
		d.snapInfo.since = time.Now()
	}
	if event != "" {
		d.snapInfo.lastEvent = event
		d.log.Info("profinet device: " + event)
	}
}

// Run serves DCP, RPC, LLDP and the AR until ctx ends. It closes Port and
// RPC on return. It can be called once.
func (d *Device) Run(ctx context.Context) error {
	if !d.started.CompareAndSwap(false, true) {
		return errors.New("profinet device: Run called twice")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	d.mu.Lock()
	d.runCtx = ctx
	d.mu.Unlock()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); d.rpcLoop(ctx) }()
	go func() { defer wg.Done(); d.lldpLoop(ctx) }()
	err := d.frameLoop(ctx)
	cancel()
	d.cfg.Port.Close()
	d.cfg.RPC.Close()
	wg.Wait()
	d.mu.Lock()
	if d.ar != nil {
		d.endAR(d.ar, "stopped")
	}
	d.mu.Unlock()
	if ctx.Err() != nil {
		return nil
	}
	return err
}

func (d *Device) frameLoop(ctx context.Context) error {
	for {
		f, err := d.cfg.Port.Recv(ctx)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, transport.ErrClosed) {
				return nil
			}
			return err
		}
		if f.EtherType != wire.EtherTypePNIO || len(f.Payload) < 2 {
			continue
		}
		id := binary.BigEndian.Uint16(f.Payload)
		switch {
		case id >= wire.FrameIDDCPHello && id <= wire.FrameIDDCPIdentResp:
			d.handleDCP(f)
		case wire.IsRTCyclicFrame(id):
			d.handleOutputFrame(f)
		case wire.IsAlarmFrame(id):
			d.handleAlarm(f)
		}
	}
}

func (d *Device) lldpLoop(ctx context.Context) {
	t := time.NewTicker(d.cfg.LLDPEvery)
	defer t.Stop()
	for {
		d.mu.Lock()
		info := wire.LLDPInfo{
			ChassisMAC:  d.cfg.Port.MAC(),
			StationName: d.station,
			SysDesc:     "nautilus PROFINET IO device",
		}
		if d.ipSet && d.cfg.InterfaceIP != nil {
			info.IP, _ = d.cfg.InterfaceIP()
		}
		d.mu.Unlock()
		if err := d.cfg.Port.Send(wire.LLDPMulticastMAC, wire.EtherTypeLLDP, wire.BuildLLDP(info)); err != nil && ctx.Err() == nil {
			d.log.Debug("profinet device: LLDP send", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
