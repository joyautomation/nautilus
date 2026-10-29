package plcnext

// driver.go is the io.Driver: New never dials (so `naut check` and `naut
// build` pass on a laptop), Start connects to the firmware's socket, runs
// the BusConductor handshake, and polls the input image at the scan rate;
// WriteOutputs merges into a kept output image the loop flushes as one
// span. On transport grpc each scan is two round trips at most: one Read
// of the input elements plus the bus RUN bit, one Write of the output
// span if anything changed. On transport shm each scan is two copies in
// and out of the shim's page (shm.go), the output one every scan so the
// shim's watchdog sees this process alive.
//
// Status for the program: bindings with a status deliver the driver's own
// state as inputs, from New on and always Good, so a program can hold its
// outputs after an outage until an operator acknowledges it (recovery
// itself stays automatic). online is true while the inputs are Good — a
// session up, the image fresh, the bus running; outages counts its falls.
// watchdog-trips counts the times the output frame went to zero without
// this driver choosing it, as far as it can see: a write that came longer
// than the shim's watchdog after the one before (this process stalled),
// the shim's watchdog bit rising in the page (a naut that was killed and
// restarted sees it on its first read), and the shim restarting (the
// runtime under it did) — one count per episode. transport grpc has no
// output watchdog, so its count stays 0.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	nio "github.com/joyautomation/nautilus/io"
)

const (
	defaultScanRate   = 100 * time.Millisecond
	defaultCallTO     = 2 * time.Second
	configureWait     = 20 * time.Second
	defaultRetryMin   = time.Second
	defaultRetryMax   = 30 * time.Second
	busConductorPorts = "/BusConductor."
	// edgeHold is how long a request flag is held low before it is raised:
	// BusConductor's program samples its ports from a 100 ms cyclic task
	// and acts on a RISING EDGE, so a flag raised and dropped inside one
	// cycle is never seen. Three cycles low, then high and left high.
	edgeHold = 300 * time.Millisecond
)

// Option configures New.
type Option func(*Driver)

// WithScanRate sets how often the input image is polled (default 100 ms).
func WithScanRate(r time.Duration) Option { return func(d *Driver) { d.scanRate = r } }

// WithLogger sets the structured logger.
func WithLogger(l *slog.Logger) Option {
	return func(d *Driver) {
		if l != nil {
			d.log = l
		}
	}
}

// WithDialer substitutes the GDS connection — tests hand the driver an
// in-process fake.
func WithDialer(dial func(ctx context.Context) (dataAccess, error)) Option {
	return func(d *Driver) { d.dial = dial }
}

// Driver polls one PLCnext controller's process image.
type Driver struct {
	m        Manifest
	scanRate time.Duration
	log      *slog.Logger
	dial     func(ctx context.Context) (dataAccess, error)
	inLen    int // bytes of the input image the bindings reach
	outLen   int // … and of the output image

	mu       sync.Mutex
	in       map[string]any // latest decoded inputs
	quality  nio.Quality    // of every input, as one source
	out      []byte         // the output image as last commanded
	outDirty bool
	why      string    // why the inputs are degraded, "" while they are not
	cause    string    // … its stable name (reading.cause)
	shim     ShimState // transport shm: the page as last read
	shimSeen bool
	inAgeUs  int64 // … and how old its input frame was then
	wdSeen   bool  // the shim's watchdog bit as last read (a trip counts once)

	online  atomic.Bool   // the status: inputs Good
	outages atomic.Uint64 // times online fell
	trips   atomic.Uint64 // times the output frame went to zero without us

	connected  atomic.Bool
	configured atomic.Bool
	running    atomic.Bool
	delivered  atomic.Bool // a Good image has been decoded once (Delivered)
	modules    atomic.Int64
	reads      atomic.Uint64
	writes     atomic.Uint64
	errs       atomic.Uint64
	held       atomic.Uint64 // shm reads that kept the previous copy (reading.held)
	lastErr    atomic.Value  // string
	rttUs      atomic.Int64

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
		m:        m,
		scanRate: defaultScanRate,
		log:      slog.Default(),
		inLen:    m.span(DirIn),
		outLen:   m.span(DirOut),
		in:       map[string]any{},
		quality:  nio.NotConnected,
	}
	d.out = make([]byte, d.outLen)
	d.lastErr.Store("")
	for _, o := range opts {
		o(d)
	}
	if d.dial == nil {
		d.dial = func(ctx context.Context) (dataAccess, error) {
			return Dial(ctx, SocketTarget(m.Socket))
		}
	}
	return d, nil
}

// InputNames / OutputNames are the bound tags by direction.
func (d *Driver) InputNames() []string  { return d.m.InputNames() }
func (d *Driver) OutputNames() []string { return d.m.OutputNames() }

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
	backoff := defaultRetryMin
	for {
		err := d.session(ctx)
		if d.connected.Load() {
			backoff = defaultRetryMin // a session that ran retries promptly, whatever came before it
		}
		d.connected.Store(false)
		d.mu.Lock()
		if d.quality == nio.Good || d.quality == nio.Bad {
			d.quality = nio.Stale
		}
		if errors.Is(err, errShimRestarted) {
			// The runtime under the shim restarted: the frame was zero
			// meanwhile. The watchdog bit a restarted shim may show for
			// outputs this driver has not refreshed yet is the same trip.
			d.trips.Add(1)
			d.wdSeen = true
		}
		d.mu.Unlock()
		if ctx.Err() != nil {
			d.online.Store(false) // a stop, not an outage
			return
		}
		d.setOnline(false)
		if err != nil {
			d.fail(err)
			d.log.Warn("plcnext: session ended", "err", err, "retry", backoff)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, defaultRetryMax)
	}
}

// session is one connection: handshake, then poll until an error. The
// handshake is gRPC on either transport (BusConductor's ports are GDS
// ports); a shm transport with no BusConductor never dials at all.
func (d *Driver) session(ctx context.Context) error {
	st := BusState{Configured: true, Running: true}
	var im image
	if d.m.Transport == TransportGRPC || d.m.BusConductor != "" {
		c, err := d.dial(ctx)
		if err != nil {
			return err
		}
		defer c.Close()
		if st, err = conductBus(ctx, c, d.m.BusConductor, d.m.RunPort); err != nil {
			return err
		}
		im = grpcImage{c: c, m: &d.m}
	}
	if d.m.Transport == TransportShm {
		p, err := openShmPage(d.m.Shm)
		if err != nil {
			return err
		}
		defer p.Close()
		p.gen = p.u64(shmOffOutGen) // generations keep climbing across a naut restart
		im = &shmImage{p: p, watchdog: d.m.watchdog()}
	}
	d.configured.Store(st.Configured)
	d.running.Store(st.Running)
	d.modules.Store(int64(st.Modules))
	d.connected.Store(true)
	route := d.m.Socket
	if d.m.Transport == TransportShm {
		route = d.m.Shm
	}
	d.log.Info("plcnext: connected", "transport", d.m.Transport, "via", route, "inputs", d.m.Inputs, "outputs", d.m.Outputs,
		"in_bytes", d.inLen, "out_bytes", d.outLen, "modules", st.Modules, "bus_running", st.Running)

	tick := time.NewTicker(d.scanRate)
	defer tick.Stop()
	for {
		if err := d.poll(ctx, im); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}

// BusState is what the handshake found.
type BusState struct {
	Configured bool
	Running    bool
	Modules    int
}

// conductBus brings the local bus up through BusConductor, idempotently:
// a bus already configured is left alone, a bus already running is left
// alone, so a driver reconnect never restarts I/O that is fine. Each
// request flag is given a held rising edge (edgeHold). runPort, when
// set, is the controller's own AXIO RUN diagnostic — the truth about
// whether process data is being exchanged, which CONFIGURED is not.
func conductBus(ctx context.Context, c dataAccess, component, runPort string) (BusState, error) {
	var st BusState
	call := func(f func(context.Context) error) error {
		cctx, cancel := withTimeout(ctx, defaultCallTO)
		defer cancel()
		return f(cctx)
	}
	readBool := func(port string) (bool, error) {
		var v bool
		err := call(func(cx context.Context) error {
			var err error
			v, err = c.ReadBool(cx, port)
			return err
		})
		return v, err
	}
	writeBool := func(port string, v bool) error {
		return call(func(cx context.Context) error { return c.WriteBool(cx, port, v) })
	}
	wait := func(d time.Duration) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d):
			return nil
		}
	}
	edge := func(port string) error {
		if err := writeBool(port, false); err != nil {
			return err
		}
		if err := wait(edgeHold); err != nil {
			return err
		}
		return writeBool(port, true)
	}
	until := func(port string, what string) error {
		deadline := time.Now().Add(configureWait)
		for {
			ok, err := readBool(port)
			if err != nil {
				return err
			}
			if ok {
				return nil
			}
			if time.Now().After(deadline) {
				return errors.New("plcnext: " + what + " within " + configureWait.String())
			}
			if err := wait(250 * time.Millisecond); err != nil {
				return err
			}
		}
	}

	if component == "" {
		st.Configured = true
	} else {
		p := func(name string) string { return component + busConductorPorts + name }
		configured, err := readBool(p("CONFIGURED"))
		if err != nil {
			return st, err
		}
		if !configured {
			if err := writeBool(p("CONFIG_MUST_MATCH"), false); err != nil {
				return st, err
			}
			if err := edge(p("CONFIG_REQ")); err != nil {
				return st, err
			}
			if err := until(p("CONFIGURED"), "BusConductor did not report CONFIGURED"); err != nil {
				return st, err
			}
		}
		st.Configured = true
		_ = call(func(cx context.Context) error {
			n, err := c.ReadNumber(cx, p("NUM_MODULES"))
			st.Modules = int(n)
			return err
		})
		running := false
		if runPort != "" {
			if running, err = readBool(runPort); err != nil {
				return st, err
			}
		}
		if !running {
			if err := edge(p("START_IO_REQ")); err != nil {
				return st, err
			}
			if runPort != "" {
				if err := until(runPort, "the Axioline bus did not report RUN"); err != nil {
					return st, err
				}
			} else if err := wait(edgeHold); err != nil {
				return st, err
			}
		}
	}
	st.Running = true
	if runPort != "" {
		running, err := readBool(runPort)
		if err != nil {
			return st, err
		}
		st.Running = running
	}
	return st, nil
}

// image is one scan's route for the bytes: the GDS mirror ports over
// gRPC, or the shim's page.
type image interface {
	// read returns the input span and what it is worth. An error ends the
	// session; a reading with no bytes keeps the last inputs at its quality.
	read(ctx context.Context, n int) (reading, error)
	// write publishes the output image. everyScan routes are written every
	// scan (the write is the shim's watchdog heartbeat); the others only
	// when an output changed.
	write(ctx context.Context, out []byte) error
	everyScan() bool
}

type reading struct {
	img     []byte
	q       nio.Quality // of every input, as one source
	running bool        // the bus exchanges process data
	why     string      // when the reading is degraded: what is wrong, now
	cause   string      // … and a stable name for it, so a change is logged once, not every scan
	held    bool        // the shim was mid-write every try: this is the previous copy
}

// grpcImage is the data-access route: one Read of the input elements plus
// the RUN bit, one Write of the output span when it changed.
type grpcImage struct {
	c dataAccess
	m *Manifest
}

func (g grpcImage) read(ctx context.Context, n int) (reading, error) {
	var extra []string
	if g.m.RunPort != "" {
		extra = []string{g.m.RunPort}
	}
	cctx, cancel := withTimeout(ctx, defaultCallTO)
	img, flags, err := g.c.ReadBytes(cctx, g.m.Inputs, n, extra...)
	cancel()
	if err != nil {
		return reading{}, err
	}
	r := reading{img: img, q: nio.Good, running: true}
	if len(flags) > 0 {
		r.running = flags[0]
	}
	// The image is being refreshed, but a bus that is not exchanging
	// process data is delivering stale bytes with a live socket: Bad, the
	// reading, not the link.
	if !r.running {
		r.q = nio.Bad
	}
	return r, nil
}

func (g grpcImage) write(ctx context.Context, out []byte) error {
	cctx, cancel := withTimeout(ctx, defaultCallTO)
	defer cancel()
	return g.c.WriteBytes(cctx, g.m.Outputs, 0, out)
}

func (grpcImage) everyScan() bool { return false }

// errShimRestarted ends a session whose shim restarted under it.
var errShimRestarted = errors.New("plcnext: shm: the shim restarted")

// shmImage is the shim's page: a copy, never a wait. A shim that stalls
// makes the inputs Stale until it cycles again, on the same mapping. A
// shim that RESTARTED ends the session: the ACF restarts it with the
// plcnext runtime, and a restarted runtime has a bus that needs
// BusConductor's handshake again — which the gRPC transport gets from its
// socket dropping.
type shmImage struct {
	p        *shmPage
	watchdog time.Duration
	st       ShimState
	started  int64 // the shim's start stamp this session first saw
	wroteNs  int64 // this session's last write to the page
	// late: the last write came longer than the watchdog after the one
	// before it, so the shim (cycling meanwhile) drove the frame to zero.
	late time.Duration
}

func (s *shmImage) read(_ context.Context, n int) (reading, error) {
	held := false
	if err := s.p.read(&s.st); err != nil {
		// Mid-write every try: keep the last copy for this scan. Its stamp
		// still ages, so a shim that died mid-write turns Stale below.
		if !errors.Is(err, errShmTorn) || s.st.StampNs == 0 {
			return reading{q: nio.Stale, why: err.Error(), cause: "page"}, nil
		}
		held = true
	}
	switch {
	case s.started == 0:
		s.started = s.st.StartedNs
	case s.st.StartedNs != s.started && s.st.StartedNs != 0:
		return reading{}, fmt.Errorf("%w (now pid %d); handshaking again", errShimRestarted, s.st.PID)
	}
	now := monoNow()
	if age := time.Duration(now - s.st.StampNs); age > shmStaleAfter {
		return reading{q: nio.Stale, why: fmt.Sprintf("plcnext: shm: the shim (pid %d) has not cycled for %v", s.st.PID, age.Round(time.Millisecond)), cause: "stalled"}, nil
	}
	if !s.st.PLC() {
		return reading{q: nio.Stale, why: "plcnext: shm: the PLC domain is not started; the shim is not touching the frames", cause: "plc"}, nil
	}
	if age := time.Duration(now - s.st.FrameNs); age > shmStaleAfter {
		return reading{q: nio.Stale, why: fmt.Sprintf("plcnext: shm: the input frame is %v old", age.Round(time.Millisecond)), cause: "frame"}, nil
	}
	r := reading{img: s.st.Inputs[:n], q: nio.Good, running: true, held: held}
	if s.st.RunKnown() && !s.st.Running() {
		r.q, r.running = nio.Bad, false
	}
	return r, nil
}

func (s *shmImage) write(_ context.Context, out []byte) error {
	now := monoNow()
	s.late = 0
	if gap := time.Duration(now - s.wroteNs); s.wroteNs != 0 && s.watchdog > 0 && gap > s.watchdog {
		s.late = gap
	}
	s.p.write(out, now, s.watchdog)
	s.wroteNs = now
	return nil
}

func (*shmImage) everyScan() bool { return true }

// poll reads the input span (and the RUN bit) once and flushes the output
// span — when it changed, or every scan on a route that wants that.
func (d *Driver) poll(ctx context.Context, im image) error {
	t0 := time.Now()
	r, err := im.read(ctx, d.inLen)
	if err != nil {
		return err
	}
	d.rttUs.Store(time.Since(t0).Microseconds())
	d.reads.Add(1)
	if r.held {
		d.held.Add(1)
	}
	d.running.Store(r.running)

	d.mu.Lock()
	if r.img != nil {
		for _, b := range d.m.Tags {
			if b.Dir != DirIn {
				continue
			}
			v, err := decode(b, r.img, d.m.ByteOrder)
			if err != nil {
				d.mu.Unlock()
				return err
			}
			d.in[b.Name] = v
		}
		if r.q == nio.Good {
			d.delivered.Store(true)
		}
	}
	d.quality = r.q
	d.setOnline(r.q == nio.Good)
	if r.cause != d.cause {
		if r.cause != "" {
			d.fail(errors.New(r.why))
			d.log.Warn("plcnext: inputs degraded", "quality", r.q, "why", r.why)
		} else {
			d.log.Info("plcnext: inputs fresh again")
		}
		d.cause = r.cause
	}
	d.why = r.why
	if s, ok := im.(*shmImage); ok {
		d.shim = s.st
		d.shimSeen = true
		if s.st.StampNs != 0 {
			d.inAgeUs = (monoNow() - s.st.FrameNs) / 1000
		}
		if wd := s.st.Watchdog(); wd != d.wdSeen {
			d.wdSeen = wd
			if wd {
				d.trips.Add(1)
				d.log.Warn("plcnext: the shim drove the output frame to zero: this driver's outputs were older than its watchdog",
					"watchdog_ms", s.st.WatchdogMs)
			}
		}
	}
	var flush []byte
	if d.outDirty || im.everyScan() {
		flush = append([]byte(nil), d.out...)
		d.outDirty = false
	}
	d.mu.Unlock()

	if flush != nil {
		if err := im.write(ctx, flush); err != nil {
			d.mu.Lock()
			d.outDirty = true // retry on the next poll of the next session
			d.mu.Unlock()
			return err
		}
		d.writes.Add(1)
		if s, ok := im.(*shmImage); ok && s.late > 0 {
			// This driver stalled between writes (a read taken before
			// the stall shows no watchdog bit, so this is how it learns).
			d.mu.Lock()
			if !d.wdSeen {
				d.trips.Add(1)
				d.wdSeen = true // the bit a later read may still show is this trip
				d.log.Warn("plcnext: this driver's outputs went longer than the shim's watchdog between writes; it drove the frame to zero meanwhile",
					"gap", s.late.Round(time.Millisecond), "watchdog", s.watchdog)
			}
			d.mu.Unlock()
		}
	}
	return nil
}

func (d *Driver) fail(err error) {
	d.errs.Add(1)
	d.lastErr.Store(err.Error())
}

// setOnline records whether the inputs are Good; a fall is an outage.
func (d *Driver) setOnline(up bool) {
	if was := d.online.Swap(up); was && !up {
		d.outages.Add(1)
	}
}

// statusInto delivers the status bindings.
func (d *Driver) statusInto(dst nio.Values) {
	for _, b := range d.m.Tags {
		switch b.Status {
		case StatusOnline:
			dst[b.Name] = d.online.Load()
		case StatusOutages:
			dst[b.Name] = int64(d.outages.Load())
		case StatusWatchdogTrips:
			dst[b.Name] = int64(d.trips.Load())
		}
	}
}

// ReadInputs is io.Driver: the latest decoded image, never blocking on
// the socket.
func (d *Driver) ReadInputs() (nio.Values, error) {
	out := nio.Values{} // sized under the lock, by ReadInputsInto
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
	d.statusInto(dst)
	return nil
}

// WriteOutputs is io.Driver: merge the changed outputs into the image.
func (d *Driver) WriteOutputs(vals nio.Values) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	var errs []error
	for _, b := range d.m.Tags {
		if b.Dir != DirOut {
			continue
		}
		v, ok := vals[b.Name]
		if !ok {
			continue
		}
		if err := encode(b, v, d.out, d.m.ByteOrder); err != nil {
			errs = append(errs, err)
			continue
		}
		d.outDirty = true
	}
	return errors.Join(errs...)
}

// Delivered is io.DeliveryReporter: true once an input image from a
// running bus has been decoded. A frame the shim is not refreshing, a PLC
// that is not started, or a bus that is not exchanging process data (Bad
// bytes on a live socket) is not a delivery, so the runtime's start-up hold
// keeps waiting through a controller's boot.
func (d *Driver) Delivered() bool { return d.delivered.Load() }

// Quality is io.QualityReporter: one source, so one quality for every
// input — Good while the image is being refreshed and the bus runs, Bad
// while the bus is stopped, Stale after the socket drops, NotConnected
// before the first read.
func (d *Driver) Quality() map[string]nio.Quality {
	d.mu.Lock()
	q := d.quality
	d.mu.Unlock()
	out := map[string]nio.Quality{}
	for _, b := range d.m.Tags {
		if b.Dir == DirIn {
			out[b.Name] = q
		}
	}
	return out
}

// Health is the status row's raw material.
type Health struct {
	Transport, Shm                 string
	Socket, Inputs, Outputs        string
	Connected, Configured, Running bool
	Modules                        int
	InBytes, OutBytes              int
	Reads, Writes, Errors          uint64
	RTTus                          int64
	LastError                      string
	// Degraded is why the inputs are not Good right now beyond a stopped
	// bus (a stalled shim, a PLC that is not started), "" when they are.
	Degraded string
	Tags     int
	// Shim is the in-process tier as last seen through its page
	// (transport shm, once read); nil otherwise.
	Shim *ShimHealth
	// Online, Outages and WatchdogTrips are what the status bindings
	// deliver (whether or not the manifest has any).
	Online                 bool
	Outages, WatchdogTrips uint64
}

// ShimHealth is rt/plcnext-io/nautilus_io's cycle as it reports it.
type ShimHealth struct {
	PID                      uint32
	PeriodUs, LateUs         uint32 // its cycle, and the lateness threshold Late counts against
	WorstUs, LastUs          uint32
	Cycles, Late, Torn       uint64
	FIFO, Watchdog, RunKnown bool
	InputAgeUs               int64  // how old the input frame was when this driver read it
	Written, Applied         uint64 // output generations: this driver's last, and the one in the bus frame
	Held                     uint64 // this driver's reads that kept the previous copy: the shim was mid-write every try
}

// Health reports the driver's state for /api/drivers.
func (d *Driver) Health() Health {
	h := Health{
		Transport: d.m.Transport, Socket: d.m.Socket, Inputs: d.m.Inputs, Outputs: d.m.Outputs,
		Connected: d.connected.Load(), Configured: d.configured.Load(), Running: d.running.Load(),
		Modules: int(d.modules.Load()), InBytes: d.inLen, OutBytes: d.outLen,
		Reads: d.reads.Load(), Writes: d.writes.Load(), Errors: d.errs.Load(),
		RTTus: d.rttUs.Load(), LastError: d.lastErr.Load().(string), Tags: len(d.m.Tags),
		Online: d.online.Load(), Outages: d.outages.Load(), WatchdogTrips: d.trips.Load(),
	}
	if d.m.Transport == TransportShm {
		h.Shm = d.m.Shm
	}
	d.mu.Lock()
	h.Degraded = d.why
	if d.shimSeen {
		st := d.shim
		h.Shim = &ShimHealth{
			PID: st.PID, PeriodUs: st.PeriodNs / 1000, LateUs: st.LateUs, WorstUs: st.WorstUs, LastUs: st.LastUs,
			Cycles: st.Cycles, Late: st.Late, Torn: st.Torn,
			FIFO: st.FIFO(), Watchdog: st.Watchdog(), RunKnown: st.RunKnown(),
			InputAgeUs: d.inAgeUs, Written: st.OutGen, Applied: st.Applied, Held: d.held.Load(),
		}
	}
	d.mu.Unlock()
	return h
}

// Handshake runs the BusConductor handshake over a real client, for
// `naut plcnext browse`.
func Handshake(ctx context.Context, c *Client, component, runPort string) (BusState, error) {
	return conductBus(ctx, c, component, runPort)
}
