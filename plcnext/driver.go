package plcnext

// driver.go is the io.Driver: New never dials (so `naut check` and `naut
// build` pass on a laptop), Start connects to the firmware's socket, runs
// the BusConductor handshake, and polls the input image at the scan rate;
// WriteOutputs merges into a kept output image the loop flushes as one
// span. Each scan is two round trips at most: one Read of the input
// elements plus the bus RUN bit, one Write of the output span if anything
// changed.

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

	connected  atomic.Bool
	configured atomic.Bool
	running    atomic.Bool
	modules    atomic.Int64
	reads      atomic.Uint64
	writes     atomic.Uint64
	errs       atomic.Uint64
	lastErr    atomic.Value // string
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
		c, err := d.dial(ctx)
		if err == nil {
			err = d.session(ctx, c)
			c.Close()
		}
		d.connected.Store(false)
		d.mu.Lock()
		if d.quality == nio.Good || d.quality == nio.Bad {
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
	st, err := conductBus(ctx, c, d.m.BusConductor, d.m.RunPort)
	if err != nil {
		return err
	}
	d.configured.Store(st.Configured)
	d.running.Store(st.Running)
	d.modules.Store(int64(st.Modules))
	d.connected.Store(true)
	d.log.Info("plcnext: connected", "socket", d.m.Socket, "inputs", d.m.Inputs, "outputs", d.m.Outputs,
		"in_bytes", d.inLen, "out_bytes", d.outLen, "modules", st.Modules, "bus_running", st.Running)

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

// poll reads the input span (and the RUN bit) once and flushes a dirty
// output span.
func (d *Driver) poll(ctx context.Context, c dataAccess) error {
	var extra []string
	if d.m.RunPort != "" {
		extra = []string{d.m.RunPort}
	}
	t0 := time.Now()
	cctx, cancel := withTimeout(ctx, defaultCallTO)
	img, flags, err := c.ReadBytes(cctx, d.m.Inputs, d.inLen, extra...)
	cancel()
	if err != nil {
		return err
	}
	d.rttUs.Store(time.Since(t0).Microseconds())
	d.reads.Add(1)
	running := true
	if len(flags) > 0 {
		running = flags[0]
	}
	d.running.Store(running)

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
	// The image is being refreshed, but a bus that is not exchanging
	// process data is delivering stale bytes with a live socket: Bad, the
	// reading, not the link.
	if running {
		d.quality = nio.Good
	} else {
		d.quality = nio.Bad
	}
	var flush []byte
	if d.outDirty {
		flush = append([]byte(nil), d.out...)
		d.outDirty = false
	}
	d.mu.Unlock()

	if flush != nil {
		cctx, cancel := withTimeout(ctx, defaultCallTO)
		err := c.WriteBytes(cctx, d.m.Outputs, 0, flush)
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
	Socket, Inputs, Outputs        string
	Connected, Configured, Running bool
	Modules                        int
	InBytes, OutBytes              int
	Reads, Writes, Errors          uint64
	RTTus                          int64
	LastError                      string
	Tags                           int
}

// Health reports the driver's state for /api/drivers.
func (d *Driver) Health() Health {
	return Health{
		Socket: d.m.Socket, Inputs: d.m.Inputs, Outputs: d.m.Outputs,
		Connected: d.connected.Load(), Configured: d.configured.Load(), Running: d.running.Load(),
		Modules: int(d.modules.Load()), InBytes: d.inLen, OutBytes: d.outLen,
		Reads: d.reads.Load(), Writes: d.writes.Load(), Errors: d.errs.Load(),
		RTTus: d.rttUs.Load(), LastError: d.lastErr.Load().(string), Tags: len(d.m.Tags),
	}
}

// Handshake runs the BusConductor handshake over a real client, for
// `naut plcnext browse`.
func Handshake(ctx context.Context, c *Client, component, runPort string) (BusState, error) {
	return conductBus(ctx, c, component, runPort)
}
