package optommp

// driver.go is the io.Driver for a groov EPIC (or groov RIO) chassis over
// OptoMMP. New never dials, so `naut check` and `naut build` pass on a
// laptop. Start connects, sends the power-up clear if one is owed, checks
// the chassis against the manifest, and polls at the scan rate.
//
// A scan is as few requests as the bindings allow, because each costs the
// same ≈ 0.75 ms inside the processor's OptoMMP server whatever its size
// (brief §2): one read of the packed values for the slots that carry
// analog or counter bindings, one of the packed digital data (state,
// quality, latches) for the digital slots — two for an ordinary chassis —
// plus one each for quality codes and scratchpad areas when bound. Outputs
// are written when they change: every digital output in one must-on /
// must-off write that touches only the commanded channels, an analog
// output or a scratchpad element one write each. WriteOutputs wakes the
// loop, so a command does not wait for the next poll.
//
// Quality per tag: NotConnected until first delivered; Stale after the
// socket drops; Bad when the slot is empty or holds another module than
// the manifest says, when a digital channel is absent or its quality bit
// is set, and when an analog value is NaN (the processor reports every
// bad-quality analog reading as NaN); Good otherwise.

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	nio "github.com/joyautomation/nautilus/io"
)

const (
	defaultRetryMin = time.Second
	defaultRetryMax = 30 * time.Second
	// chassisEvery is how often the module types and channel-present mask
	// are re-read: groov modules are hot-swappable.
	chassisEvery = 5 * time.Second
)

// Option configures New.
type Option func(*Driver)

// WithScanRate overrides the manifest's scan rate.
func WithScanRate(r time.Duration) Option { return func(d *Driver) { d.scanRate = r } }

// WithLogger sets the structured logger.
func WithLogger(l *slog.Logger) Option {
	return func(d *Driver) {
		if l != nil {
			d.log = l
		}
	}
}

// WithDialer substitutes the TCP dial — tests hand the driver an
// in-process fake device.
func WithDialer(dial func(ctx context.Context) (net.Conn, error)) Option {
	return func(d *Driver) { d.dial = dial }
}

// span is an inclusive range of slots or indexes; lo < 0 means empty.
type span struct{ lo, hi int }

func (s *span) add(i int) {
	if s.lo < 0 || i < s.lo {
		s.lo = i
	}
	if i > s.hi {
		s.hi = i
	}
}

func (s span) empty() bool { return s.lo < 0 }
func (s span) n() int      { return s.hi - s.lo + 1 }

func newSpan() span { return span{lo: -1, hi: -1} }

// readPlan is which areas a scan reads, from the bindings.
type readPlan struct {
	values, digital, quality span // slots
	ints, floats             span // scratchpad indexes
	bits                     bool
	slots                    []int // slots any channel binding names
}

// Driver polls one processor.
type Driver struct {
	m        Manifest
	scanRate time.Duration
	retryMin time.Duration
	chassis  time.Duration // chassis re-check interval
	log      *slog.Logger
	dial     func(ctx context.Context) (net.Conn, error)
	plan     readPlan
	outputs  map[string]Binding
	expect   map[int]uint32 // slot → module type the manifest names

	mu        sync.Mutex
	in        map[string]any
	quality   map[string]nio.Quality
	commanded map[string]any // last output value accepted
	pending   map[string]any // accepted, not yet written
	slotFault map[int]string // slot → why its tags are Bad
	absent    []byte         // channel-present mask as last read (nil: unknown)
	unit      string
	firmware  string

	gateMu    sync.Mutex
	writeGate func() bool

	connected atomic.Bool
	reads     atomic.Uint64
	writes    atomic.Uint64
	errs      atomic.Uint64
	scanUs    atomic.Int64
	requests  atomic.Int64
	lastErr   atomic.Value // string

	kick   chan struct{}
	cancel context.CancelFunc
	done   chan struct{}
}

// New validates the manifest and builds the driver. It never dials.
func New(m Manifest, opts ...Option) (*Driver, error) {
	m.fillDefaults()
	if err := m.Validate(); err != nil {
		return nil, err
	}
	d := &Driver{
		m:         m,
		scanRate:  time.Duration(m.ScanRate),
		retryMin:  defaultRetryMin,
		chassis:   chassisEvery,
		log:       slog.Default(),
		outputs:   map[string]Binding{},
		expect:    map[int]uint32{},
		in:        map[string]any{},
		quality:   map[string]nio.Quality{},
		commanded: map[string]any{},
		pending:   map[string]any{},
		slotFault: map[int]string{},
		kick:      make(chan struct{}, 1),
	}
	d.lastErr.Store("")
	for _, o := range opts {
		o(d)
	}
	if d.scanRate <= 0 {
		d.scanRate = DefaultScanRate
	}
	if d.dial == nil {
		d.dial = func(ctx context.Context) (net.Conn, error) {
			dl := net.Dialer{Timeout: time.Duration(m.Timeout)}
			return dl.DialContext(ctx, "tcp", net.JoinHostPort(m.Host, fmt.Sprint(m.Port)))
		}
	}
	for _, mod := range m.Modules {
		d.expect[mod.Slot] = uint32(mod.Type)
	}
	p := readPlan{values: newSpan(), digital: newSpan(), quality: newSpan(), ints: newSpan(), floats: newSpan()}
	slots := map[int]bool{}
	for _, b := range m.Tags {
		d.quality[b.Name] = nio.NotConnected
		if b.isOutput() {
			d.outputs[b.Name] = b
		}
		if b.isChannel() {
			slots[b.Slot] = true
		}
		switch b.Point {
		case PointAI, PointAO:
			p.values.add(b.Slot)
		case PointCounter:
			p.values.add(b.Slot)
			p.digital.add(b.Slot) // its quality is the channel's
		case PointDI, PointDO, PointOnLatch, PointOffLatch:
			p.digital.add(b.Slot)
		case PointQuality:
			p.quality.add(b.Slot)
		case PointScratchBit:
			p.bits = true
		case PointScratchInt:
			p.ints.add(b.Index)
		case PointScratchFloat:
			p.floats.add(b.Index)
		}
	}
	for s := range slots {
		p.slots = append(p.slots, s)
	}
	sort.Ints(p.slots)
	d.plan = p
	return d, nil
}

// InputNames / OutputNames are the bound tags by direction.
func (d *Driver) InputNames() []string  { return d.m.InputNames() }
func (d *Driver) OutputNames() []string { return d.m.OutputNames() }

// SetWriteGate installs the write gate: commands leave the driver only
// while gate() is true (redundancy leadership). Reads are unaffected.
func (d *Driver) SetWriteGate(gate func() bool) {
	d.gateMu.Lock()
	d.writeGate = gate
	d.gateMu.Unlock()
}

func (d *Driver) gateOpen() bool {
	d.gateMu.Lock()
	g := d.writeGate
	d.gateMu.Unlock()
	return g == nil || g()
}

// Start connects and runs the poll loop until ctx ends or Stop is called.
func (d *Driver) Start(ctx context.Context) {
	ctx, d.cancel = context.WithCancel(ctx)
	d.done = make(chan struct{})
	go d.run(ctx)
}

// Stop ends the loop and waits for it.
func (d *Driver) Stop() {
	if d.cancel != nil {
		d.cancel()
		<-d.done
	}
}

func (d *Driver) run(ctx context.Context) {
	defer close(d.done)
	backoff := d.retryMin
	for {
		conn, err := d.dial(ctx)
		polled := false
		if err == nil {
			c := NewClient(conn, time.Duration(d.m.Timeout))
			polled, err = d.session(ctx, c)
			c.Close()
		}
		d.connected.Store(false)
		d.mu.Lock()
		for name, q := range d.quality {
			if q != nio.NotConnected {
				d.quality[name] = nio.Stale
			}
		}
		d.mu.Unlock()
		if ctx.Err() != nil {
			return
		}
		if polled {
			backoff = d.retryMin
		}
		if err != nil {
			d.fail(err)
			d.log.Warn("optommp: session ended", "host", d.m.Host, "err", err, "retry", backoff)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, defaultRetryMax)
	}
}

// session is one connection: power-up clear, chassis check, then poll
// until an error. polled reports whether at least one scan completed.
func (d *Driver) session(ctx context.Context, c *Client) (polled bool, err error) {
	if puc, err := c.ReadUint32(addrPUCFlag); err != nil {
		return false, err
	} else if puc != 0 {
		if err := c.Write(addrPowerupClear, []byte{0, 0, 0, 1}); err != nil {
			return false, fmt.Errorf("optommp: power-up clear: %w", err)
		}
		d.log.Info("optommp: sent the power-up clear the processor asked for", "host", d.m.Host)
	}
	unit, fw := "", ""
	if b, err := c.Read(addrUnitDescription, 32); err == nil {
		unit = cString(b)
	} else {
		return false, err
	}
	if b, err := c.Read(addrFirmware, 4); err == nil {
		fw = fmt.Sprintf("%d.%d.%d.%d", b[0], b[1], b[2], b[3])
	}
	d.mu.Lock()
	d.unit, d.firmware = unit, fw
	d.mu.Unlock()
	if err := d.checkChassis(c); err != nil {
		return false, err
	}
	d.connected.Store(true)
	d.log.Info("optommp: connected", "host", d.m.Host, "unit", unit, "firmware", fw,
		"tags", len(d.m.Tags), "scan", d.scanRate)

	tick := time.NewTicker(d.scanRate)
	defer tick.Stop()
	nextChassis := time.Now().Add(d.chassis)
	for {
		if err := d.poll(c); err != nil {
			return polled, err
		}
		polled = true
		if time.Now().After(nextChassis) {
			if err := d.checkChassis(c); err != nil {
				return polled, err
			}
			nextChassis = time.Now().Add(d.chassis)
		}
		for waiting := true; waiting; {
			select {
			case <-ctx.Done():
				return polled, nil
			case <-tick.C:
				waiting = false
			case <-d.kick:
				// A command: write it now, read on the next tick.
				if err := d.flush(c); err != nil {
					return polled, err
				}
			}
		}
	}
}

// checkChassis re-reads each bound slot's module type and the
// channel-present mask, and records a fault for any slot that is empty or
// holds a module the manifest does not name.
func (d *Driver) checkChassis(c *Client) error {
	faults := map[int]string{}
	for _, slot := range d.plan.slots {
		got, err := c.ReadUint32(addrChannelConfig + uint32(slot)*configModuleStride + cfgModuleType)
		if err != nil {
			return err
		}
		want, named := d.expect[slot]
		switch {
		case !named:
			// a manifest without modules: no verdict beyond presence
		case got == 0 && want != 0:
			faults[slot] = fmt.Sprintf("slot %d is empty (expected %s)", slot, ModuleName(want))
		case got != want:
			faults[slot] = fmt.Sprintf("slot %d holds %s, the manifest expects %s — re-run the import", slot, ModuleName(got), ModuleName(want))
		}
	}
	present, err := c.Read(addrPackedPresent, MaxModules*MaxChannels/8)
	if err != nil {
		return err
	}
	d.mu.Lock()
	for slot, why := range faults {
		if d.slotFault[slot] != why {
			d.log.Warn("optommp: chassis does not match the manifest; the slot's tags are Bad", "host", d.m.Host, "fault", why)
		}
	}
	for slot := range d.slotFault {
		if _, still := faults[slot]; !still {
			d.log.Info("optommp: slot matches the manifest again", "host", d.m.Host, "slot", slot)
		}
	}
	d.slotFault = faults
	d.absent = present
	d.mu.Unlock()
	if len(faults) > 0 {
		var msgs []string
		for _, s := range d.plan.slots {
			if f, ok := faults[s]; ok {
				msgs = append(msgs, f)
			}
		}
		d.lastErr.Store(strings.Join(msgs, "; "))
	}
	return nil
}

// poll is one scan: the reads the plan names, decoded into tags, then the
// latches it saw cleared and the pending outputs written.
func (d *Driver) poll(c *Client) error {
	t0 := time.Now()
	reqs := 0
	read := func(s span, base uint32, stride int) ([]byte, error) {
		if s.empty() {
			return nil, nil
		}
		n := s.n() * stride
		reqs += (n + MaxBlock - 1) / MaxBlock
		return c.Read(base+uint32(s.lo*stride), n)
	}
	values, err := read(d.plan.values, addrPackedValues, valuesModuleStride)
	if err != nil {
		return err
	}
	digital, err := read(d.plan.digital, addrDigitalPacked, digitalModuleStride)
	if err != nil {
		return err
	}
	codes, err := read(d.plan.quality, addrQualityCodes, qualityModuleStride)
	if err != nil {
		return err
	}
	ints, err := read(d.plan.ints, addrScratchInts, 4)
	if err != nil {
		return err
	}
	floats, err := read(d.plan.floats, addrScratchFloats, 4)
	if err != nil {
		return err
	}
	var bits uint64
	if d.plan.bits {
		b, err := c.Read(addrScratchBits, 8)
		if err != nil {
			return err
		}
		reqs++
		bits = binary.BigEndian.Uint64(b)
	}

	u32 := func(buf []byte, off int) uint32 { return binary.BigEndian.Uint32(buf[off:]) }
	digWord := func(slot, field int) uint32 {
		return u32(digital, (slot-d.plan.digital.lo)*digitalModuleStride+field)
	}
	type clear struct{ addr uint32 }
	var clears []clear

	d.mu.Lock()
	for _, b := range d.m.Tags {
		var v any
		q := nio.Good
		bit := uint(b.Channel)
		switch b.Point {
		case PointDI, PointDO:
			v = digWord(b.Slot, 0)>>bit&1 == 1
		case PointAI, PointAO:
			f := float64(math.Float32frombits(u32(values, (b.Slot-d.plan.values.lo)*valuesModuleStride+b.Channel*4)))
			v = f
			if math.IsNaN(f) || math.IsInf(f, 0) {
				q = nio.Bad
			}
		case PointCounter:
			v = int64(u32(values, (b.Slot-d.plan.values.lo)*valuesModuleStride+b.Channel*4))
		case PointOnLatch, PointOffLatch:
			field, off := 8, clearOnLatch
			if b.Point == PointOffLatch {
				field, off = 12, clearOffLatch
			}
			set := digWord(b.Slot, field)>>bit&1 == 1
			v = set
			if set {
				clears = append(clears, clear{addrDigitalClear + uint32(b.Slot*clearModuleStride+b.Channel*clearChannelStride+off)})
			}
		case PointQuality:
			v = int64(u32(codes, (b.Slot-d.plan.quality.lo)*qualityModuleStride+b.Channel*4))
		case PointScratchBit:
			v = bits>>uint(b.Index)&1 == 1
		case PointScratchInt:
			v = int64(int32(u32(ints, (b.Index-d.plan.ints.lo)*4)))
		case PointScratchFloat:
			v = float64(math.Float32frombits(u32(floats, (b.Index-d.plan.floats.lo)*4)))
		}
		if b.isChannel() {
			if _, bad := d.slotFault[b.Slot]; bad {
				q = nio.Bad
			}
			switch b.Point {
			case PointDI, PointDO, PointCounter, PointOnLatch, PointOffLatch:
				if digWord(b.Slot, 4)>>bit&1 == 1 {
					q = nio.Bad // the processor's quality bit for the channel
				}
				if d.absent != nil && !packedBit(d.absent, b.Slot*MaxChannels+b.Channel) {
					q = nio.Bad // no such channel on the module in the slot
				}
			}
		}
		d.in[b.Name] = v
		d.quality[b.Name] = q
	}
	d.mu.Unlock()
	d.reads.Add(1)

	for _, cl := range clears {
		if _, err := c.Read(cl.addr, 4); err != nil {
			return err
		}
		reqs++
	}
	n, err := d.flushCount(c)
	reqs += n
	d.requests.Store(int64(reqs))
	d.scanUs.Store(time.Since(t0).Microseconds())
	return err
}

func (d *Driver) flush(c *Client) error {
	_, err := d.flushCount(c)
	return err
}

// flushCount writes every pending output and reports how many requests it
// took. A failed write leaves the rest pending for the next session.
func (d *Driver) flushCount(c *Client) (int, error) {
	d.mu.Lock()
	if len(d.pending) == 0 {
		d.mu.Unlock()
		return 0, nil
	}
	work := d.pending
	d.pending = map[string]any{}
	d.mu.Unlock()

	type pointWrite struct {
		addr  uint32
		data  []byte
		names []string
	}
	var writes []pointWrite
	momo := map[int][2]uint32{} // slot → on, off
	var momoNames []string
	var bitsOn, bitsOff uint64
	var bitNames []string
	var errs []error
	names := make([]string, 0, len(work))
	for name := range work {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		b := d.outputs[name]
		v := work[name]
		switch b.Point {
		case PointDO:
			on, err := toBool(v)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", name, err))
				continue
			}
			mask := momo[b.Slot]
			if on {
				mask[0] |= 1 << uint(b.Channel)
			} else {
				mask[1] |= 1 << uint(b.Channel)
			}
			momo[b.Slot] = mask
			momoNames = append(momoNames, name)
		case PointScratchBit:
			on, err := toBool(v)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", name, err))
				continue
			}
			if on {
				bitsOn |= 1 << uint(b.Index)
			} else {
				bitsOff |= 1 << uint(b.Index)
			}
			bitNames = append(bitNames, name)
		case PointAO, PointScratchFloat:
			f, err := toFloat(v)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", name, err))
				continue
			}
			addr := uint32(addrScratchFloats + b.Index*4)
			if b.Point == PointAO {
				addr = uint32(addrAnalogWrite + b.Slot*pointModuleStride + b.Channel*pointChannelStride)
			}
			data := make([]byte, 4)
			binary.BigEndian.PutUint32(data, math.Float32bits(float32(f)))
			writes = append(writes, pointWrite{addr, data, []string{name}})
		case PointScratchInt:
			i, err := toInt(v)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", name, err))
				continue
			}
			data := make([]byte, 4)
			binary.BigEndian.PutUint32(data, uint32(int32(i)))
			writes = append(writes, pointWrite{uint32(addrScratchInts + b.Index*4), data, []string{name}})
		}
	}
	if len(momo) > 0 {
		// One write spanning every slot with a command; slots between
		// carry zero masks, which leave their channels alone.
		s := newSpan()
		for slot := range momo {
			s.add(slot)
		}
		data := make([]byte, s.n()*momoModuleStride)
		for slot, mask := range momo {
			off := (slot - s.lo) * momoModuleStride
			binary.BigEndian.PutUint32(data[off:], mask[0])
			binary.BigEndian.PutUint32(data[off+4:], mask[1])
		}
		writes = append([]pointWrite{{uint32(addrDigitalMOMO + s.lo*momoModuleStride), data, momoNames}}, writes...)
	}
	if len(bitNames) > 0 {
		data := make([]byte, 16)
		binary.BigEndian.PutUint64(data, bitsOn)
		binary.BigEndian.PutUint64(data[8:], bitsOff)
		writes = append(writes, pointWrite{addrScratchOn, data, bitNames})
	}
	for i, w := range writes {
		if err := c.Write(w.addr, w.data); err != nil {
			// Requeue this write and every one after it, unless a newer
			// command for the same tag arrived meanwhile.
			d.mu.Lock()
			for _, rest := range writes[i:] {
				for _, name := range rest.names {
					if _, newer := d.pending[name]; !newer {
						d.pending[name] = work[name]
					}
				}
			}
			d.mu.Unlock()
			return i, err
		}
		d.writes.Add(1)
	}
	if len(errs) > 0 {
		d.fail(errors.Join(errs...))
	}
	return len(writes), nil
}

func (d *Driver) fail(err error) {
	d.errs.Add(1)
	d.lastErr.Store(err.Error())
}

// ReadInputs is io.Driver: the latest decoded values, never blocking on
// the socket.
func (d *Driver) ReadInputs() (nio.Values, error) {
	out := make(nio.Values, len(d.m.Tags))
	return out, d.ReadInputsInto(out)
}

// ReadInputsInto is io.BatchReader.
func (d *Driver) ReadInputsInto(dst nio.Values) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for k := range dst {
		if _, ok := d.in[k]; !ok {
			delete(dst, k)
		}
	}
	for k, v := range d.in {
		dst[k] = v
	}
	return nil
}

// WriteOutputs is io.Driver. Nautilus owns the outputs it binds: the first
// value handed over for a tag is written, and after that every change.
// Commands are held across a disconnect and written on reconnect. While
// the write gate is closed nothing is accepted.
func (d *Driver) WriteOutputs(vals nio.Values) error {
	if !d.gateOpen() {
		return nil
	}
	queued := false
	d.mu.Lock()
	for name, v := range vals {
		if _, ok := d.outputs[name]; !ok {
			continue
		}
		if prev, ok := d.commanded[name]; ok && sameScalar(prev, v) {
			continue
		}
		d.commanded[name] = v
		d.pending[name] = v
		queued = true
	}
	d.mu.Unlock()
	if queued {
		select {
		case d.kick <- struct{}{}:
		default:
		}
	}
	return nil
}

// Quality is io.QualityReporter.
func (d *Driver) Quality() map[string]nio.Quality {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make(map[string]nio.Quality, len(d.quality))
	for k, q := range d.quality {
		out[k] = q
	}
	return out
}

// Health is the status row's raw material.
type Health struct {
	Host           string
	Port           int
	Unit, Firmware string
	Connected      bool
	Modules        int    // modules the manifest names
	Faults         string // why some slots' tags are Bad, "" when none
	Reads, Writes  uint64
	Errors         uint64
	ScanUs         int64 // the last scan's reads, latch clears and writes
	Requests       int64 // OptoMMP requests in the last scan
	ScanRate       time.Duration
	LastError      string
	Tags, BadTags  int
}

// Health reports the driver's state for /api/drivers.
func (d *Driver) Health() Health {
	d.mu.Lock()
	h := Health{Unit: d.unit, Firmware: d.firmware}
	var faults []string
	for _, s := range d.plan.slots {
		if f, ok := d.slotFault[s]; ok {
			faults = append(faults, f)
		}
	}
	for _, q := range d.quality {
		if q == nio.Bad {
			h.BadTags++
		}
	}
	d.mu.Unlock()
	h.Host, h.Port = d.m.Host, d.m.Port
	h.Connected = d.connected.Load()
	h.Modules = len(d.m.Modules)
	h.Faults = strings.Join(faults, "; ")
	h.Reads, h.Writes, h.Errors = d.reads.Load(), d.writes.Load(), d.errs.Load()
	h.ScanUs, h.Requests = d.scanUs.Load(), d.requests.Load()
	h.ScanRate = d.scanRate
	h.LastError = d.lastErr.Load().(string)
	h.Tags = len(d.m.Tags)
	return h
}

func toBool(v any) (bool, error) {
	switch x := v.(type) {
	case bool:
		return x, nil
	case float64:
		return x != 0, nil
	case int64:
		return x != 0, nil
	case int:
		return x != 0, nil
	}
	return false, fmt.Errorf("want a BOOL, got %T", v)
}

func toFloat(v any) (float64, error) {
	switch x := v.(type) {
	case float64:
		return x, nil
	case int64:
		return float64(x), nil
	case int:
		return float64(x), nil
	case bool:
		if x {
			return 1, nil
		}
		return 0, nil
	}
	return 0, fmt.Errorf("want a number, got %T", v)
}

func toInt(v any) (int64, error) {
	switch x := v.(type) {
	case int64:
		return x, nil
	case int:
		return int64(x), nil
	case float64:
		return int64(math.Round(x)), nil
	case bool:
		if x {
			return 1, nil
		}
		return 0, nil
	}
	return 0, fmt.Errorf("want an integer, got %T", v)
}

// sameScalar compares two handed-over output values for on-change
// suppression; anything but a plain scalar reads as changed.
func sameScalar(a, b any) bool {
	switch x := a.(type) {
	case bool:
		y, ok := b.(bool)
		return ok && x == y
	case float64:
		y, ok := b.(float64)
		return ok && x == y
	case int64:
		y, ok := b.(int64)
		return ok && x == y
	case int:
		y, ok := b.(int)
		return ok && x == y
	}
	return false
}
