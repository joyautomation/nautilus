package profinet

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/profinet/device"
	"github.com/joyautomation/nautilus/profinet/transport"
	"github.com/joyautomation/nautilus/profinet/wire"
)

// DeviceDriver is nautilus as a PROFINET IO device (`type:
// profinet-device`). The PLC's outputs are its input tags, the PLC's
// inputs its output tags. New never opens a socket; Start does, and keeps
// retrying with the reason on the status row (a missing CAP_NET_RAW
// above all), so a project loads and checks anywhere.
//
// Quality of an input tag (data from the PLC): NotConnected until the
// first good value; Good while an AR exchanges data, the PLC is in run
// and its IOPS for the subslot is good; Bad while the PLC is in stop, or
// its IOPS (or the module layout) for the subslot is bad, or the value
// does not fit the tag; Stale once the AR ends. The last good value is
// held through all of them.
//
// The write gate (plain: the live-scan lease, or leadership) decides
// whether the PLC may believe our data: closed, every input subslot to
// the PLC goes IOPS Bad. Reopened, the driver forgets what it sent and
// asks for every output; the data turns valid again with the push that
// answers. The same holds from start-up: nothing is valid before the
// program's first push. (The runtime hands a driver every output on each
// push, and Load refuses a manifest binding to the PLC that the project
// does not declare, so one push is the whole image.)
//
// One replica of a redundant set (WithDeviceReplica, which Load sets under
// `redundancy:`) reads the gate as leadership instead: the device runs only
// while this replica leads. A standby holds no socket, so it answers no DCP
// and sends no LLDP; two replicas never claim one station name. On
// takeover the PLC's AR to the old leader times out, and the PLC finds the
// name again at the new leader's MAC and connects.
type DeviceDriver struct {
	m       DeviceManifest
	log     *slog.Logger
	online  string
	open    DeviceOpener
	minWD   time.Duration
	cmPort  int
	inBind  []Binding // PLC → nautilus
	outBind map[string]Binding
	retry   [2]time.Duration
	replica bool

	mu        sync.Mutex
	stopDev   context.CancelFunc // ends the running device (a replica losing the lead)
	dev       *device.Device
	img       map[device.Addr][]byte // nautilus outputs, per subslot
	last      map[string]any
	q         map[string]nio.Quality
	pushed    bool // a push since the gate last (re)opened
	valid     bool
	lastErr   string
	state     string
	since     time.Time
	encodeErr string

	gateMu   sync.Mutex
	gate     func() bool
	gateShut atomic.Bool
	wanted   atomic.Bool

	cancel context.CancelFunc
	done   chan struct{}
}

// DeviceOpener opens the raw Ethernet port and the DCE/RPC socket on an
// interface, and says which addresses the interface holds. Tests replace
// it with an in-memory hub.
type DeviceOpener func(iface string) (transport.Port, net.PacketConn, func() []net.IP, error)

// DeviceOption configures NewDevice.
type DeviceOption func(*DeviceDriver)

// WithDeviceLogger sets the logger.
func WithDeviceLogger(l *slog.Logger) DeviceOption {
	return func(d *DeviceDriver) {
		if l != nil {
			d.log = l
		}
	}
}

// WithDeviceOnlineTag names the BOOL input that is true while an AR
// exchanges data with the PLC ("" for none).
func WithDeviceOnlineTag(name string) DeviceOption { return func(d *DeviceDriver) { d.online = name } }

// WithDeviceOpener replaces the raw-socket opener (tests).
func WithDeviceOpener(o DeviceOpener) DeviceOption { return func(d *DeviceDriver) { d.open = o } }

// WithDeviceMinWatchdog sets the device's shortest output watchdog (tests).
func WithDeviceMinWatchdog(w time.Duration) DeviceOption {
	return func(d *DeviceDriver) { d.minWD = w }
}

// WithDeviceControllerPort sets the UDP port the device sends its
// CControl to (tests; the field uses 34964).
func WithDeviceControllerPort(p int) DeviceOption { return func(d *DeviceDriver) { d.cmPort = p } }

// WithDeviceRetry sets the reopen backoff (tests).
func WithDeviceRetry(lo, hi time.Duration) DeviceOption {
	return func(d *DeviceDriver) { d.retry = [2]time.Duration{lo, hi} }
}

// WithDeviceReplica makes the driver one replica of a redundant set: the
// gate is leadership, and a standby says nothing on the network.
func WithDeviceReplica() DeviceOption { return func(d *DeviceDriver) { d.replica = true } }

// Replica reports whether the driver is one replica of a redundant set.
func (d *DeviceDriver) Replica() bool { return d.replica }

// NewDevice validates the manifest and builds the driver. It does not
// touch the network.
func NewDevice(m DeviceManifest, opts ...DeviceOption) (*DeviceDriver, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	d := &DeviceDriver{
		m: m, log: slog.New(slog.DiscardHandler), open: OpenInterface,
		outBind: map[string]Binding{}, retry: [2]time.Duration{time.Second, 30 * time.Second},
		img: map[device.Addr][]byte{}, last: map[string]any{}, q: map[string]nio.Quality{},
		state: "starting", since: time.Now(),
	}
	for _, o := range opts {
		o(d)
	}
	for _, s := range m.Subslots() {
		d.img[s.Addr] = make([]byte, s.InputLen)
	}
	for _, b := range m.Tags {
		if b.ToPLC() {
			d.outBind[b.Name] = b
		} else {
			d.inBind = append(d.inBind, b)
			d.q[b.Name] = nio.NotConnected
		}
	}
	sort.Slice(d.inBind, func(i, j int) bool { return d.inBind[i].Name < d.inBind[j].Name })
	if d.online != "" {
		if _, clash := d.outBind[d.online]; clash || containsBinding(d.inBind, d.online) {
			return nil, fmt.Errorf("profinet-device: tag %q is also the driver's online tag", d.online)
		}
	}
	return d, nil
}

func containsBinding(bs []Binding, name string) bool {
	for _, b := range bs {
		if b.Name == name {
			return true
		}
	}
	return false
}

// Apply sets options after New and before Start (a test that loads a
// project and points its driver at an in-memory network).
func (d *DeviceDriver) Apply(opts ...DeviceOption) {
	for _, o := range opts {
		o(d)
	}
}

// OpenInterface is the real opener: AF_PACKET on iface (DCP, RT, RTA and
// LLDP), UDP 34964 for DCE/RPC, and the interface's IPv4 addresses.
func OpenInterface(iface string) (transport.Port, net.PacketConn, func() []net.IP, error) {
	port, err := transport.Open(iface, transport.Options{
		EtherTypes: []uint16{wire.EtherTypePNIO},
		Multicast:  []net.HardwareAddr{wire.DCPMulticastAddr},
	})
	if err != nil {
		return nil, nil, nil, err
	}
	rpc, err := net.ListenPacket("udp4", fmt.Sprintf(":%d", wire.PNIOCMPort))
	if err != nil {
		port.Close()
		return nil, nil, nil, fmt.Errorf("DCE/RPC on UDP %d: %w", wire.PNIOCMPort, err)
	}
	addrs := func() []net.IP {
		ifi, err := net.InterfaceByName(iface)
		if err != nil {
			return nil
		}
		as, _ := ifi.Addrs()
		var out []net.IP
		for _, a := range as {
			if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil {
				out = append(out, n.IP.To4())
			}
		}
		return out
	}
	return port, rpc, addrs, nil
}

// InputNames and OutputNames are the bound tags by nautilus direction
// (the online tag is an input too).
func (d *DeviceDriver) InputNames() []string {
	names := d.m.InputNames()
	if d.online != "" {
		names = append(names, d.online)
		sort.Strings(names)
	}
	return names
}

func (d *DeviceDriver) OutputNames() []string { return d.m.OutputNames() }

// Start opens the interface and serves the device until ctx ends or Stop.
func (d *DeviceDriver) Start(ctx context.Context) {
	ctx, d.cancel = context.WithCancel(ctx)
	d.done = make(chan struct{})
	go d.run(ctx)
	go d.watchGate(ctx)
}

// Stop ends the device and waits for it.
func (d *DeviceDriver) Stop() {
	if d.cancel == nil {
		return
	}
	d.cancel()
	<-d.done
}

func (d *DeviceDriver) run(ctx context.Context) {
	defer close(d.done)
	wait := d.retry[0]
	for ctx.Err() == nil {
		if d.replica && !d.leading() {
			d.standby()
			select {
			case <-ctx.Done():
				return
			case <-time.After(gateLook):
			}
			wait = d.retry[0]
			continue
		}
		port, rpc, addrs, err := d.open(d.m.Interface)
		if err != nil {
			d.setState("error", err.Error())
			d.log.Warn("profinet-device: open", "interface", d.m.Interface, "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			wait = min(wait*2, d.retry[1])
			continue
		}
		dev, err := device.New(device.Config{
			Identity: d.m.Identity(), Subslots: d.m.Subslots(), Port: port, RPC: rpc,
			InterfaceAddrs: addrs, MinWatchdog: d.minWD, ControllerCMPort: d.cmPort, Log: d.log,
		})
		if err != nil { // the manifest was validated; this is a bug
			port.Close()
			rpc.Close()
			d.setState("error", err.Error())
			return
		}
		devCtx, stop := context.WithCancel(ctx)
		d.mu.Lock()
		for a, b := range d.img {
			dev.SetInput(a, b)
		}
		dev.SetInputsValid(d.valid)
		d.dev = dev
		d.stopDev = stop
		d.lastErr = ""
		d.mu.Unlock()
		d.setState("running", "")
		start := time.Now()
		// A replica that lost the lead between the look above and now
		// stops at once: gateOpen cancels only a device it can see.
		if d.replica && d.gateShut.Load() {
			stop()
		}
		err = dev.Run(devCtx)
		stop()
		d.mu.Lock()
		d.stopDev = nil
		d.mu.Unlock()
		if time.Since(start) > d.retry[1] {
			wait = d.retry[0] // it ran a good while: start the backoff over
		}
		if ctx.Err() != nil {
			return
		}
		if d.replica && devCtx.Err() != nil {
			d.log.Info("profinet-device: no longer the leader; the device is silent")
			continue // the loop's top parks it as a standby
		}
		d.setState("error", fmt.Sprintf("device stopped: %v", err))
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = min(wait*2, d.retry[1])
	}
}

// gateLook is how often the gate is looked at between scans, and how
// often a standby replica checks whether it leads.
const gateLook = 200 * time.Millisecond

// leading reports whether a replica holds the lead. Without a gate it
// does not: `naut run` installs leadership before Start, and a replica
// must never answer for the station on a guess.
func (d *DeviceDriver) leading() bool {
	d.gateMu.Lock()
	g := d.gate
	d.gateMu.Unlock()
	return g != nil && d.gateOpen()
}

// standby parks a replica: no device, so inputs read Stale, and the
// status row says why it is silent.
func (d *DeviceDriver) standby() {
	d.mu.Lock()
	d.dev = nil
	d.mu.Unlock()
	d.setState("standby", "")
}

func (d *DeviceDriver) setState(state, lastErr string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if state != d.state {
		d.state, d.since = state, time.Now()
	}
	if lastErr != "" {
		d.lastErr = lastErr
	}
}

// ReadInputs is io.Driver's read.
func (d *DeviceDriver) ReadInputs() (nio.Values, error) {
	v := nio.Values{}
	return v, d.ReadInputsInto(v)
}

// ReadInputsInto delivers the PLC's outputs as of its last frame. It never
// blocks on the network.
func (d *DeviceDriver) ReadInputsInto(dst nio.Values) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	var snap device.Snapshot
	if d.dev != nil {
		snap = d.dev.Snapshot()
	}
	live := d.dev != nil && snap.State == device.StateData && snap.Fresh
	for _, b := range d.inBind {
		a := b.addr()
		var q nio.Quality
		switch {
		case !live:
			q = nio.Stale
		case snap.Meaning != wire.StatusRun || !snap.OutputGood[a]:
			q = nio.Bad
		default:
			v, err := wire.Decode(snap.Outputs[a], b.format, int(b.Offset), b.bit())
			if err != nil {
				q = nio.Bad
				break
			}
			d.last[b.Name] = v
			q = nio.Good
		}
		v, ever := d.last[b.Name]
		if !ever {
			q = nio.NotConnected
			delete(dst, b.Name)
		} else {
			dst[b.Name] = v
		}
		if q == nio.Good {
			delete(d.q, b.Name)
		} else {
			d.q[b.Name] = q
		}
	}
	if d.online != "" {
		dst[d.online] = live
	}
	return nil
}

// Quality is the driver's io.QualityReporter (non-Good entries only).
func (d *DeviceDriver) Quality() map[string]nio.Quality {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.q) == 0 {
		return nil
	}
	out := make(map[string]nio.Quality, len(d.q))
	for k, v := range d.q {
		out[k] = v
	}
	return out
}

// WriteOutputs puts the program's values into the data the PLC reads.
func (d *DeviceDriver) WriteOutputs(v nio.Values) error {
	open := d.gateOpen()
	d.mu.Lock()
	defer d.mu.Unlock()
	touched := map[device.Addr]bool{}
	var errs []error
	for name, val := range v {
		b, ok := d.outBind[name]
		if !ok {
			continue
		}
		if err := wire.Encode(d.img[b.addr()], b.format, int(b.Offset), b.bit(), val); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			continue
		}
		touched[b.addr()] = true
	}
	if d.dev != nil {
		for a := range touched {
			d.dev.SetInput(a, d.img[a])
		}
	}
	// The runtime hands over every output on each push, so one push with
	// the gate open is the program's whole image. A value that did not
	// fit leaves its bytes as they were; the push still counts.
	if open && d.gateOpenLocked() {
		d.pushed = true
	}
	d.updateValid()
	if err := errors.Join(errs...); err != nil {
		d.encodeErr = err.Error()
		return err
	}
	return nil
}

// gateOpenLocked reports the gate as the last look found it. Checked
// under d.mu, so a close seen by a look between WriteOutputs' own look
// and taking the lock is not undone.
func (d *DeviceDriver) gateOpenLocked() bool { return !d.gateShut.Load() }

// updateValid sets the data the PLC reads valid only with the gate open
// and a push since it (re)opened (d.mu held).
func (d *DeviceDriver) updateValid() {
	valid := d.pushed && d.gateOpenLocked()
	if valid != d.valid {
		d.valid = valid
		if d.dev != nil {
			d.dev.SetInputsValid(valid)
		}
	}
}

// SetWriteGate installs the gate. The device driver's gate is a plain
// gate (io.PlainGated): it only says whether the PLC may believe our
// data, so `naut run` may give it the live-scan lease.
func (d *DeviceDriver) SetWriteGate(gate func() bool) {
	d.gateMu.Lock()
	d.gate = gate
	d.gateMu.Unlock()
}

// PlainWriteGate marks the gate as plain (see io.PlainGated).
func (d *DeviceDriver) PlainWriteGate() {}

// gateOpen looks at the gate. A look that finds it closed makes the data
// invalid at once; the first look that finds it open again forgets what
// was pushed and asks for every output.
func (d *DeviceDriver) gateOpen() bool {
	d.gateMu.Lock()
	g := d.gate
	d.gateMu.Unlock()
	if g == nil {
		return true
	}
	open := g()
	if shut := d.gateShut.Load(); shut == open {
		d.mu.Lock()
		if !open {
			d.gateShut.Store(true)
			d.pushed = false
			d.updateValid()
			if d.replica {
				if d.stopDev != nil {
					d.stopDev() // a standby holds no socket
				}
			} else {
				d.log.Info("profinet-device: write gate closed; the PLC sees our data as invalid")
			}
		} else {
			d.gateShut.Store(false)
			d.wanted.Store(true)
			d.log.Info("profinet-device: write gate open again; valid after the next full push")
		}
		d.mu.Unlock()
	}
	return open
}

// watchGate looks at the gate between scans, so a hung scan (which calls
// nothing) still turns the data invalid.
func (d *DeviceDriver) watchGate(ctx context.Context) {
	t := time.NewTicker(gateLook)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			d.gateOpen()
		}
	}
}

// OutputsWanted is the driver's io.OutputsRequester.
func (d *DeviceDriver) OutputsWanted() bool { return d.wanted.Load() && d.wanted.Swap(false) }

var (
	_ nio.BatchReader      = (*DeviceDriver)(nil)
	_ nio.QualityReporter  = (*DeviceDriver)(nil)
	_ nio.OutputsRequester = (*DeviceDriver)(nil)
	_ nio.PlainGated       = (*DeviceDriver)(nil)
)

// DeviceHealth is the status row's content.
type DeviceHealth struct {
	Interface, Station string
	State              string // starting | error | running | standby
	AR                 string // idle | startup | data
	Controller         string
	ControllerState    string // run | stop | invalid
	InputsValid        bool
	SinceMs            int64
	LastError          string
	LastEvent          string
	Diff               int // modules that differ from the layout
	Tags, BadTags      int
	ARs, Aborts        uint64
	RefusedARs         uint64
	SetIPRefused       uint64
	NameSets, Alarms   uint64
	OutputFrames       uint64
}

// Health reports the device for the status row.
func (d *DeviceDriver) Health() DeviceHealth {
	d.mu.Lock()
	defer d.mu.Unlock()
	h := DeviceHealth{
		Interface: d.m.Interface, Station: d.m.Station, State: d.state, AR: "idle",
		InputsValid: d.valid, SinceMs: d.since.UnixMilli(), LastError: d.lastErr,
		Tags: len(d.m.Tags), BadTags: len(d.q),
	}
	if d.encodeErr != "" && h.LastError == "" {
		h.LastError = d.encodeErr
	}
	if d.dev == nil {
		return h
	}
	s := d.dev.Snapshot()
	h.AR, h.Controller, h.Station, h.LastEvent = s.State.String(), s.Controller, s.Station, s.LastEvent
	h.SinceMs = s.Since.UnixMilli()
	switch s.Meaning {
	case wire.StatusRun:
		h.ControllerState = "run"
	case wire.StatusStop:
		h.ControllerState = "stop"
	default:
		h.ControllerState = "invalid"
	}
	h.Diff, h.ARs, h.Aborts, h.RefusedARs = len(s.Diff), s.ARs, s.Aborts, s.RefusedARs
	h.SetIPRefused, h.NameSets, h.Alarms, h.OutputFrames = s.SetIPRefused, s.NameSets, s.Alarms, s.OutputFrames
	return h
}
