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
// Writing on change assumes the unit holds what it was last told, and two
// things break that: a reconnect (the processor may have restarted, or
// been without nautilus long enough for anything to happen), and the
// communication watchdog, which drives outputs to their watchdog values
// after a quiet spell and leaves them there. So every commanded output is
// rewritten at the start of each session, and every digital and analog
// output whenever the client's bound on the quiet spell (wire.go)
// exceeds the unit's watchdog time — a stalled process, a network drop
// TCP rode out, a scan that overran.
//
// With custom-area the scan is one read of the processor's custom data
// area, its pointers naming every word the bindings need; with stream the
// processor pushes that area over UDP and the driver sends only writes,
// latch clears and the chassis check (stream.go). Writes go over TCP in
// every mode.
//
// Quality per tag: NotConnected until first delivered; Stale after the
// socket drops; Bad when the slot is empty or holds another module than
// the manifest says, when a digital channel is absent or its quality bit
// is set, when an analog value is NaN (the processor reports every
// bad-quality analog reading as NaN), and when a digital output's state
// has disagreed with its command for longer than a write takes to show
// (readbackSettle); Good otherwise. An output tag's value is the program's
// command — the runtime takes only inputs from a driver — so that last
// case is how a module doing something else reaches the screen.
//
// Status for the program: the online, outages and watchdog-trips points
// are the driver's own state, delivered as inputs from New on and always
// Good, so a program can hold its outputs after an outage until an
// operator acknowledges it (recovery itself stays automatic). online is
// true while a session is up and, when streaming, the stream is not late;
// outages counts its falls. watchdog-trips counts the times the unit's
// watchdog (when on) may have fired: the rising edge of rearm's verdict
// within a session, and a reconnect whose gap since the last request of
// the session before outran the watchdog. A watchdog that fired while
// nautilus itself was not running cannot be seen — a program covers that
// with FIRST_SCAN().

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
	// readbackSettle is how long after a write a digital output's state
	// may still disagree with it before the tag goes Bad (at least three
	// scans or stream intervals): long enough for the module to switch and
	// for a datagram already in flight to be superseded.
	readbackSettle = 250 * time.Millisecond
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

// scanPlan is the words a scan needs (plan.go) and how each binding
// decodes from them.
type scanPlan struct {
	words []uint32  // addresses, sorted, unique
	refs  []tagRef  // per binding, in manifest order
	areas []area    // packed-mode reads
	src   []wordSrc // per word: its packed-mode area and offset
	slots []int     // slots any channel binding names
}

// tagRef is a binding's words as indexes into the scan's word image; qual
// is -1 when the binding has no quality word.
type tagRef struct {
	val, qual int
	bit       uint
}

// Driver polls one processor.
type Driver struct {
	m        Manifest
	scanRate time.Duration
	retryMin time.Duration
	chassis  time.Duration // chassis re-check interval
	log      *slog.Logger
	dial     func(ctx context.Context) (net.Conn, error)
	plan     scanPlan
	custom   bool // fetch through the custom data area
	outputs  map[string]Binding
	expect   map[int]uint32 // slot → module type the manifest names
	tags     []Binding      // the memory-map bindings, in manifest order
	status   []Binding      // the driver's own status points

	// Owned by the loop: when the last session's last request went out,
	// and how long before the new session's first one that was.
	lastSent     time.Time
	reconnectGap time.Duration

	mu             sync.Mutex
	in             map[string]any
	quality        map[string]nio.Quality
	commanded      map[string]any       // last output value accepted
	pending        map[string]any       // accepted, not yet written
	wrote          map[string]time.Time // when each output was last written
	disagree       map[string]bool      // digital outputs whose state is not their command
	quietTrip      bool                 // the last quiet check outran the watchdog
	slotFault      map[int]string       // slot → why its tags are Bad
	absent         []byte               // channel-present mask as last read (nil: unknown)
	unit           string
	firmware       string
	watchdogMs     int64  // the unit's watchdog as last read; -1 before
	armed, armable int    // bound outputs with a watchdog value enabled / all
	streamOrig     []byte // streaming configuration as first found
	customOrig     []byte // the custom area's pointers as first found
	streamFrom     net.IP // the processor, the only sender a stream is taken from

	gateMu    sync.Mutex
	writeGate func() bool

	connected atomic.Bool
	online    atomic.Bool   // the status point: a session up, inputs fresh
	outages   atomic.Uint64 // times online fell
	trips     atomic.Uint64 // times the unit's watchdog may have fired
	reads     atomic.Uint64
	writes    atomic.Uint64
	errs      atomic.Uint64
	scanUs    atomic.Int64
	requests  atomic.Int64
	late      atomic.Uint64 // times the stream went quiet long enough to go Stale
	gapMaxUs  atomic.Int64  // longest gap between datagrams
	lastErr   atomic.Value  // string
	schedErr  atomic.Value  // string: why priority: was refused, "" when applied
	scanT     timing        // each scan's (or datagram's) handling time
	wakeT     timing        // each polled scan's start after its slot

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
		m:          m,
		scanRate:   time.Duration(m.ScanRate),
		retryMin:   defaultRetryMin,
		chassis:    chassisEvery,
		log:        slog.Default(),
		outputs:    map[string]Binding{},
		expect:     map[int]uint32{},
		in:         map[string]any{},
		quality:    map[string]nio.Quality{},
		commanded:  map[string]any{},
		pending:    map[string]any{},
		wrote:      map[string]time.Time{},
		disagree:   map[string]bool{},
		slotFault:  map[int]string{},
		watchdogMs: -1,
		kick:       make(chan struct{}, 1),
	}
	d.lastErr.Store("")
	d.schedErr.Store("")
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
	for _, b := range m.Tags {
		if b.isStatus() {
			d.status = append(d.status, b)
		} else {
			d.tags = append(d.tags, b)
		}
	}
	custom := m.CustomArea || m.Stream > 0
	words := planWords(d.tags, custom)
	index := make(map[uint32]int, len(words))
	for i, w := range words {
		index[w] = i
	}
	p := scanPlan{words: words}
	p.areas, p.src = packedAreas(words)
	slots := map[int]bool{}
	for _, b := range d.tags {
		d.quality[b.Name] = nio.NotConnected
		if b.isOutput() {
			d.outputs[b.Name] = b
		}
		if b.isChannel() {
			slots[b.Slot] = true
		}
		w := wordsFor(b, custom)
		r := tagRef{val: index[w.val], qual: -1, bit: w.bit}
		if w.qual != 0 {
			r.qual = index[w.qual]
		}
		p.refs = append(p.refs, r)
	}
	for s := range slots {
		p.slots = append(p.slots, s)
	}
	sort.Ints(p.slots)
	d.custom = custom
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
	if p := d.m.Priority; p > 0 {
		// The whole loop, every session, on this one thread (rt_linux.go).
		if err := rtThread(p); err != nil {
			d.schedErr.Store(err.Error())
			d.log.Warn("optommp: priority refused", "host", d.m.Host, "err", err)
		} else {
			d.log.Info("optommp: driver thread at SCHED_FIFO", "host", d.m.Host, "priority", p)
		}
	}
	backoff := d.retryMin
	for {
		conn, err := d.dial(ctx)
		polled := false
		if err == nil {
			if d.m.Priority > 0 {
				conn = blockingConn(conn)
			}
			d.reconnectGap = 0
			if !d.lastSent.IsZero() {
				d.reconnectGap = time.Since(d.lastSent)
			}
			c := NewClient(conn, time.Duration(d.m.Timeout))
			polled, err = d.session(ctx, c)
			c.Close()
			if !c.sent.IsZero() {
				d.lastSent = c.sent
			}
		}
		d.connected.Store(false)
		d.markStale()
		if ctx.Err() != nil {
			d.online.Store(false) // a stop, not an outage
			return
		}
		d.setOnline(false)
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

// session is one connection: power-up clear, chassis check, the
// configuration the manifest asks for, then poll (or receive the stream)
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
	uc, err := d.configure(c)
	if err != nil {
		return false, err
	}
	d.mu.Lock()
	wd := time.Duration(d.watchdogMs) * time.Millisecond
	d.mu.Unlock()
	if wd > 0 && d.reconnectGap > wd {
		// Nothing reached the unit between the last session's last request
		// and this one's first: longer than its watchdog.
		d.trips.Add(1)
		d.log.Warn("optommp: reconnected after longer than the unit's watchdog; it may have applied watchdog values",
			"host", d.m.Host, "gap", d.reconnectGap.Round(time.Millisecond), "watchdog", wd)
	}
	if d.gateOpen() {
		// The unit may have restarted, or its watchdog fired, since the
		// last session: what it holds is not known to be what was written.
		d.mu.Lock()
		n := d.requeueLocked(false)
		d.mu.Unlock()
		if n > 0 {
			d.log.Info("optommp: rewriting every commanded output on reconnect", "host", d.m.Host, "outputs", n)
		}
	}
	defer func() {
		if ctx.Err() != nil {
			d.restore(c) // a clean stop: leave the unit as it was found
		}
	}()
	d.connected.Store(true)
	d.setOnline(true)
	d.log.Info("optommp: connected", "host", d.m.Host, "unit", unit, "firmware", fw,
		"tags", len(d.tags), "mode", d.mode())
	if uc != nil {
		return d.streamLoop(ctx, c, uc)
	}

	// Scan k is due at start + k·scan-rate; how late each one starts is
	// the wake-up lateness on the status row. With priority: the driver
	// sleeps to the slot in the kernel; without, a ticker wakes it.
	start := time.Now()
	slot := start
	var tick *time.Ticker
	if d.m.Priority == 0 {
		tick = time.NewTicker(d.scanRate)
		defer tick.Stop()
	}
	nextChassis := start.Add(d.chassis)
	for {
		d.wakeT.add(time.Since(slot).Microseconds())
		if err := d.poll(c); err != nil {
			return polled, err
		}
		polled = true
		if time.Now().After(nextChassis) {
			if err := d.checkChassis(c); err != nil {
				return polled, err
			}
			if d.custom {
				if err := d.verifyCustom(c); err != nil {
					return polled, err
				}
			}
			nextChassis = time.Now().Add(d.chassis)
		}
		if tick == nil {
			// The next slot still ahead: a scan that overran skips the
			// slots it missed rather than running them back to back.
			for slot = slot.Add(d.scanRate); !time.Now().Before(slot); slot = slot.Add(d.scanRate) {
			}
			if ok, err := d.sleepTo(ctx, c, slot); !ok {
				return polled, err
			}
			continue
		}
		for waiting := true; waiting; {
			select {
			case <-ctx.Done():
				return polled, nil
			case <-tick.C:
				waiting = false
				slot = start.Add(time.Since(start) / d.scanRate * d.scanRate)
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

// poll is one polled scan: the words fetched, decoded into tags, then the
// latches it saw cleared and the pending outputs written.
func (d *Driver) poll(c *Client) error {
	t0 := time.Now()
	img, reqs, err := d.fetch(c)
	if err != nil {
		return err
	}
	n, err := d.deliver(c, img)
	d.requests.Store(int64(reqs + n))
	us := time.Since(t0).Microseconds()
	d.scanUs.Store(us)
	d.scanT.add(us)
	return err
}

// sleepTo waits on the driver's own thread until deadline, a millisecond
// at a time in the kernel, writing any command that arrives meanwhile.
// ok is false when ctx ended the wait or a write failed (err).
func (d *Driver) sleepTo(ctx context.Context, c *Client, deadline time.Time) (ok bool, err error) {
	for {
		rem := time.Until(deadline)
		if rem <= 0 {
			return true, nil
		}
		if ctx.Err() != nil {
			return false, nil
		}
		select {
		case <-d.kick:
			if err := d.flush(c); err != nil {
				return false, err
			}
			continue
		default:
		}
		nap(min(rem, time.Millisecond))
	}
}

// fetch reads the scan's words: the packed areas one block read each, or
// the custom data area in one.
func (d *Driver) fetch(c *Client) (img []uint32, reqs int, err error) {
	img = make([]uint32, len(d.plan.words))
	if d.custom {
		n := len(img) * 4
		b, err := c.Read(addrCustomData, n)
		if err != nil {
			return nil, 0, err
		}
		for i := range img {
			img[i] = binary.BigEndian.Uint32(b[i*4:])
		}
		return img, (n + MaxBlock - 1) / MaxBlock, nil
	}
	bufs := make([][]byte, len(d.plan.areas))
	for k, a := range d.plan.areas {
		if bufs[k], err = c.Read(a.base, a.n); err != nil {
			return nil, 0, err
		}
		reqs += (a.n + MaxBlock - 1) / MaxBlock
	}
	for i, s := range d.plan.src {
		img[i] = binary.BigEndian.Uint32(bufs[s.area][s.off:])
	}
	return img, reqs, nil
}

// deliver decodes one word image — polled or streamed — into values and
// quality, clears the latches it reported, and writes pending outputs. It
// returns the requests that took.
func (d *Driver) deliver(c *Client, img []uint32) (int, error) {
	var clears []uint32
	// A replica that is not writing has no command to hold the unit to.
	checkOut := d.gateOpen()
	now := time.Now()
	type turn struct {
		name     string
		off, set bool
	}
	var turned []turn // digital outputs that began or stopped disagreeing
	d.mu.Lock()
	for i, b := range d.tags {
		r := d.plan.refs[i]
		w := img[r.val]
		set := w>>r.bit&1 == 1
		var v any
		q := nio.Good
		switch b.Point {
		case PointDI, PointDO, PointScratchBit:
			v = set
		case PointAI, PointAO:
			f := float64(math.Float32frombits(w))
			v = f
			if math.IsNaN(f) || math.IsInf(f, 0) {
				q = nio.Bad
			}
		case PointCounter, PointQuality:
			v = int64(w)
		case PointOnLatch, PointOffLatch:
			v = set
			if set {
				off := clearOnLatch
				if b.Point == PointOffLatch {
					off = clearOffLatch
				}
				clears = append(clears, addrDigitalClear+uint32(b.Slot*clearModuleStride+b.Channel*clearChannelStride+off))
			}
		case PointScratchInt:
			v = int64(int32(w))
		case PointScratchFloat:
			v = float64(math.Float32frombits(w))
		}
		if b.isChannel() {
			if _, bad := d.slotFault[b.Slot]; bad {
				q = nio.Bad
			}
			if r.qual >= 0 {
				if img[r.qual]>>r.bit&1 == 1 {
					q = nio.Bad // the processor's quality bit for the channel
				}
				if d.absent != nil && !packedBit(d.absent, b.Slot*MaxChannels+b.Channel) {
					q = nio.Bad // no such channel on the module in the slot
				}
			}
		}
		if b.Point == PointDO && checkOut {
			off := d.disagreesLocked(b.Name, set, now)
			if off != d.disagree[b.Name] {
				d.disagree[b.Name] = off
				turned = append(turned, turn{b.Name, off, set})
			}
			if off {
				q = nio.Bad
			}
		}
		d.in[b.Name] = v
		d.quality[b.Name] = q
	}
	d.mu.Unlock()
	d.reads.Add(1)
	for _, t := range turned {
		if t.off {
			d.log.Warn("optommp: an output is not in its commanded state; its tag is Bad until it is",
				"host", d.m.Host, "tag", t.name, "state", t.set)
		} else {
			d.log.Info("optommp: an output is in its commanded state again", "host", d.m.Host, "tag", t.name)
		}
	}

	reqs := 0
	for _, addr := range clears {
		if _, err := c.Read(addr, 4); err != nil {
			return reqs, err
		}
		reqs++
	}
	n, err := d.flushCount(c)
	return reqs + n, err
}

func (d *Driver) flush(c *Client) error {
	_, err := d.flushCount(c)
	return err
}

// flushCount writes every pending output and reports how many requests it
// took. A failed write leaves the rest pending for the next session.
func (d *Driver) flushCount(c *Client) (int, error) {
	d.rearm(c)
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
			addr := addrScratchFloats + uint32(b.Index*4)
			if b.Point == PointAO {
				addr = addrAnalogWrite + uint32(b.Slot*pointModuleStride+b.Channel*pointChannelStride)
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
			writes = append(writes, pointWrite{addrScratchInts + uint32(b.Index*4), data, []string{name}})
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
		writes = append([]pointWrite{{addrDigitalMOMO + uint32(s.lo*momoModuleStride), data, momoNames}}, writes...)
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
		now := time.Now()
		d.mu.Lock()
		for _, name := range w.names {
			d.wrote[name] = now
		}
		d.mu.Unlock()
	}
	if len(errs) > 0 {
		d.fail(errors.Join(errs...))
	}
	return len(writes), nil
}

// rearm queues every digital and analog output the driver commands for
// rewriting when the unit may have gone longer than its watchdog time
// without a request (Client.quiet), so an output the watchdog moved goes
// back to its command instead of waiting for the command to change. It
// logs when that starts, not on every scan it lasts.
func (d *Driver) rearm(c *Client) {
	quiet := c.quiet()
	d.mu.Lock()
	wd := time.Duration(d.watchdogMs) * time.Millisecond
	tripped := wd > 0 && quiet > wd
	was := d.quietTrip
	d.quietTrip = tripped
	d.mu.Unlock()
	if tripped && !was {
		d.trips.Add(1)
	}
	if !tripped || !d.gateOpen() {
		return
	}
	d.mu.Lock()
	n := d.requeueLocked(true)
	d.mu.Unlock()
	if !was {
		d.log.Warn("optommp: the unit may have gone longer than its watchdog without a request; rewriting every output in case it applied watchdog values",
			"host", d.m.Host, "quiet", quiet.Round(time.Millisecond), "watchdog", wd, "outputs", n)
	}
}

// requeueLocked queues every commanded output (watchdog: only the digital
// and analog outputs, the points a watchdog moves) at its current command,
// so the next flush writes it whether or not it changed. It returns how
// many were not already queued.
func (d *Driver) requeueLocked(watchdog bool) int {
	n := 0
	for name, v := range d.commanded {
		if p := d.outputs[name].Point; watchdog && p != PointDO && p != PointAO {
			continue
		}
		if _, queued := d.pending[name]; !queued {
			n++
		}
		d.pending[name] = v
	}
	return n
}

// disagreesLocked reports whether a digital output's state as read (set)
// is not its command, counting only a command written at least
// readbackSettle (or three intervals) before now and not since replaced.
func (d *Driver) disagreesLocked(name string, set bool, now time.Time) bool {
	cmd, ok := d.commanded[name]
	if !ok {
		return false
	}
	if _, queued := d.pending[name]; queued {
		return false
	}
	every := d.scanRate
	if d.m.Stream > 0 {
		every = time.Duration(d.m.Stream)
	}
	if now.Sub(d.wrote[name]) < max(readbackSettle, 3*every) {
		return false
	}
	want, err := toBool(cmd)
	return err == nil && want != set
}

func (d *Driver) fail(err error) {
	d.errs.Add(1)
	d.lastErr.Store(err.Error())
}

// setOnline records whether the driver is exchanging data with the unit;
// a fall is an outage.
func (d *Driver) setOnline(up bool) {
	if was := d.online.Swap(up); was && !up {
		d.outages.Add(1)
	}
}

// statusInto delivers the status points.
func (d *Driver) statusInto(dst nio.Values) {
	for _, b := range d.status {
		switch b.Point {
		case PointOnline:
			dst[b.Name] = d.online.Load()
		case PointOutages:
			dst[b.Name] = int64(d.outages.Load())
		case PointWatchdogTrips:
			dst[b.Name] = int64(d.trips.Load())
		}
	}
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
			delete(dst, k) // a status point is put back below
		}
	}
	for k, v := range d.in {
		dst[k] = v
	}
	d.statusInto(dst)
	return nil
}

// WriteOutputs is io.Driver. Nautilus owns the outputs it binds: the first
// value handed over for a tag is written, and after that every change —
// plus every command again on reconnect, and the digital and analog ones
// after the unit may have applied watchdog values (rearm). Commands are
// held across a disconnect. While the write gate is closed nothing is
// accepted, and nothing is rewritten.
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
	// Mode is how inputs arrive: "poll", "custom area" (polled, one
	// read) or "stream".
	Mode     string
	Stream   time.Duration // the stream interval, 0 when polling
	Late     uint64        // times the stream went quiet long enough to go Stale
	GapMaxUs int64         // longest gap between datagrams since start
	// WatchdogMs is the unit's communication watchdog as last read (0 =
	// off, -1 = not yet read); Armed of Armable bound outputs have a
	// watchdog value enabled.
	WatchdogMs     int64
	Armed, Armable int
	// ScanP99Us / ScanMaxUs are scan time over every scan (or datagram)
	// since start; WakeP99Us / WakeMaxUs how late each polled scan started
	// after its slot. A p99 is the upper edge of its 1-2-5 bucket.
	ScanP99Us, ScanMaxUs int64
	WakeP99Us, WakeMaxUs int64
	// Priority is the manifest's SCHED_FIFO priority (0: normal);
	// SchedError why the OS refused it, "" when applied.
	Priority   int
	SchedError string
	// Online, Outages and WatchdogTrips are what the status points
	// deliver (whether or not the manifest binds them).
	Online                 bool
	Outages, WatchdogTrips uint64
}

func (d *Driver) mode() string {
	switch {
	case d.m.Stream > 0:
		return "stream"
	case d.custom:
		return "custom area"
	}
	return "poll"
}

// Health reports the driver's state for /api/drivers.
func (d *Driver) Health() Health {
	d.mu.Lock()
	h := Health{Unit: d.unit, Firmware: d.firmware, WatchdogMs: d.watchdogMs, Armed: d.armed, Armable: d.armable}
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
	h.Online, h.Outages, h.WatchdogTrips = d.online.Load(), d.outages.Load(), d.trips.Load()
	h.Modules = len(d.m.Modules)
	h.Faults = strings.Join(faults, "; ")
	h.Reads, h.Writes, h.Errors = d.reads.Load(), d.writes.Load(), d.errs.Load()
	h.ScanUs, h.Requests = d.scanUs.Load(), d.requests.Load()
	h.ScanRate = d.scanRate
	h.LastError = d.lastErr.Load().(string)
	h.Tags = len(d.tags)
	h.Mode, h.Stream = d.mode(), time.Duration(d.m.Stream)
	h.Late, h.GapMaxUs = d.late.Load(), d.gapMaxUs.Load()
	h.ScanP99Us, h.ScanMaxUs = d.scanT.stats()
	h.WakeP99Us, h.WakeMaxUs = d.wakeT.stats()
	h.Priority, h.SchedError = d.m.Priority, d.schedErr.Load().(string)
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
