package plcnext

// driver.go is the io.Driver: New never dials (so `naut check` and `naut
// build` pass on a laptop), Start connects to the firmware's socket, runs
// the BusConductor handshake, and polls the input image at the scan rate;
// WriteOutputs merges into a kept output image the loop flushes whole.

import (
	"context"
	"errors"
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

	mu       sync.Mutex
	in       map[string]any // latest decoded inputs
	quality  nio.Quality    // of every input, as one source
	out      []byte         // the output image as last commanded
	outDirty bool
	outSize  int

	connected  atomic.Bool
	configured atomic.Bool
	modules    atomic.Int64
	reads      atomic.Uint64
	writes     atomic.Uint64
	errs       atomic.Uint64
	lastErr    atomic.Value // string
	inLen      atomic.Int64
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
		in:       map[string]any{},
		quality:  nio.NotConnected,
		out:      make([]byte, 512),
		outSize:  512,
	}
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
		c, err := d.dial(ctx)
		if err == nil {
			err = d.session(ctx, c)
			c.Close()
		}
		d.connected.Store(false)
		d.mu.Lock()
		if d.quality == nio.Good {
			d.quality = nio.Stale
		}
		d.mu.Unlock()
		if ctx.Err() != nil {
			return
		}
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

// session is one connection: handshake, then poll until an error.
func (d *Driver) session(ctx context.Context, c dataAccess) error {
	if err := d.handshake(ctx, c); err != nil {
		return err
	}
	// Size the output image from the controller's own, so a write never
	// changes the port's length.
	cctx, cancel := withTimeout(ctx, defaultCallTO)
	outImg, err := c.ReadBytes(cctx, d.m.Outputs)
	cancel()
	if err != nil {
		return err
	}
	d.mu.Lock()
	if len(outImg) != d.outSize {
		d.outSize = len(outImg)
		img := make([]byte, d.outSize)
		copy(img, d.out)
		d.out = img
	}
	d.mu.Unlock()
	d.connected.Store(true)
	d.log.Info("plcnext: connected", "socket", d.m.Socket, "inputs", d.m.Inputs, "outputs", d.m.Outputs, "out_bytes", len(outImg))

	tick := time.NewTicker(d.scanRate)
	defer tick.Stop()
	for {
		if err := d.poll(ctx, c); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}

// handshake pulses BusConductor: configure the bus as found, wait for
// CONFIGURED, start I/O exchange. Its request ports are edge-triggered,
// so they are set back to false afterwards.
func (d *Driver) handshake(ctx context.Context, c dataAccess) error {
	if d.m.BusConductor == "" {
		d.configured.Store(true)
		return nil
	}
	p := func(name string) string { return d.m.BusConductor + busConductorPorts + name }
	call := func(f func(context.Context) error) error {
		cctx, cancel := withTimeout(ctx, defaultCallTO)
		defer cancel()
		return f(cctx)
	}
	if err := call(func(cx context.Context) error { return c.WriteBool(cx, p("CONFIG_MUST_MATCH"), false) }); err != nil {
		return err
	}
	if err := call(func(cx context.Context) error { return c.WriteBool(cx, p("CONFIG_REQ"), true) }); err != nil {
		return err
	}
	deadline := time.Now().Add(configureWait)
	for {
		var ok bool
		if err := call(func(cx context.Context) error {
			var err error
			ok, err = c.ReadBool(cx, p("CONFIGURED"))
			return err
		}); err != nil {
			return err
		}
		if ok {
			break
		}
		if time.Now().After(deadline) {
			return errors.New("plcnext: BusConductor did not report CONFIGURED within " + configureWait.String())
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	_ = call(func(cx context.Context) error { return c.WriteBool(cx, p("CONFIG_REQ"), false) })
	var n float64
	if err := call(func(cx context.Context) error {
		var err error
		n, err = c.ReadNumber(cx, p("NUM_MODULES"))
		return err
	}); err == nil {
		d.modules.Store(int64(n))
	}
	if err := call(func(cx context.Context) error { return c.WriteBool(cx, p("START_IO_REQ"), true) }); err != nil {
		return err
	}
	_ = call(func(cx context.Context) error { return c.WriteBool(cx, p("START_IO_REQ"), false) })
	d.configured.Store(true)
	d.log.Info("plcnext: BusConductor configured the local bus", "modules", int(n))
	return nil
}

// poll reads the input image once and flushes a dirty output image.
func (d *Driver) poll(ctx context.Context, c dataAccess) error {
	t0 := time.Now()
	cctx, cancel := withTimeout(ctx, defaultCallTO)
	img, err := c.ReadBytes(cctx, d.m.Inputs)
	cancel()
	if err != nil {
		return err
	}
	d.rttUs.Store(time.Since(t0).Microseconds())
	d.reads.Add(1)
	d.inLen.Store(int64(len(img)))

	d.mu.Lock()
	for _, b := range d.m.Tags {
		if b.Dir != DirIn {
			continue
		}
		v, err := decode(b, img, d.m.ByteOrder)
		if err != nil {
			d.mu.Unlock()
			return err
		}
		d.in[b.Name] = v
	}
	d.quality = nio.Good
	var flush []byte
	if d.outDirty {
		flush = append([]byte(nil), d.out...)
		d.outDirty = false
	}
	d.mu.Unlock()

	if flush != nil {
		cctx, cancel := withTimeout(ctx, defaultCallTO)
		err := c.WriteBytes(cctx, d.m.Outputs, flush)
		cancel()
		if err != nil {
			d.mu.Lock()
			d.outDirty = true // retry on the next poll of the next session
			d.mu.Unlock()
			return err
		}
		d.writes.Add(1)
	}
	return nil
}

func (d *Driver) fail(err error) {
	d.errs.Add(1)
	d.lastErr.Store(err.Error())
}

// ReadInputs is io.Driver: the latest decoded image, never blocking on
// the socket.
func (d *Driver) ReadInputs() (nio.Values, error) {
	out := make(nio.Values, len(d.in))
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

// Quality is io.QualityReporter: one source, so one quality for every
// input — Good while the image is being refreshed, Stale after the socket
// drops, NotConnected before the first read.
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
	Socket, Inputs, Outputs string
	Connected, Configured   bool
	Modules                 int
	InBytes, OutBytes       int
	Reads, Writes, Errors   uint64
	RTTus                   int64
	LastError               string
	Tags                    int
}

// Health reports the driver's state for /api/drivers.
func (d *Driver) Health() Health {
	d.mu.Lock()
	outSize := d.outSize
	d.mu.Unlock()
	return Health{
		Socket: d.m.Socket, Inputs: d.m.Inputs, Outputs: d.m.Outputs,
		Connected: d.connected.Load(), Configured: d.configured.Load(),
		Modules: int(d.modules.Load()), InBytes: int(d.inLen.Load()), OutBytes: outSize,
		Reads: d.reads.Load(), Writes: d.writes.Load(), Errors: d.errs.Load(),
		RTTus: d.rttUs.Load(), LastError: d.lastErr.Load().(string), Tags: len(d.m.Tags),
	}
}
