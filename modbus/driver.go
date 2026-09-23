// driver.go is the io.Driver surface of the Modbus TCP driver: construction
// (New, Option), the read/write seam the runtime scans against, and the
// Start/Stop lifecycle. One goroutine per source, one TCP connection,
// requests strictly sequential; the wire layer is tcp.go, the value codec
// encode.go, and the block-read schedule plan.go.
//
// New NEVER dials. buildDriver runs inside `naut check` and
// `naut build`, i.e. in CI with no device in sight, so everything that
// can fail on bad configuration fails here, offline, and the connection is
// Start's job — the same split eip and sparkplug/host make.
package modbus

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"path"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	nio "github.com/joyautomation/nautilus/io"
)

// Defaults per source, from docs/design/modbus.md §1–2.
const (
	defaultScanRate = time.Second
	defaultTimeout  = 3 * time.Second
	defaultRetryMin = time.Second
	defaultRetryMax = 60 * time.Second
)

// exceptionsToPark is how many CONSECUTIVE exception responses on one block
// park that block for one backoff period (brief §2): the first exception
// already marks the block's tags Bad, and parking stops a permanently
// illegal address from burning a request slot every cycle.
const exceptionsToPark = 3

// suffixOnline names the driver-synthesized per-source companion:
// "<source>__Online" is a BOOL input that is true while the source's
// connection is up — the guard a program reads before trusting the source's
// tags, exactly like sparkplug/host's "<site>__Online".
const suffixOnline = "__Online"

// OnlineTagName is the source's synthesized BOOL companion tag name — shared
// with the generator so tags/modbus.yaml declares exactly what the driver
// delivers.
func (s Source) OnlineTagName() string { return s.ID + suffixOnline }

// Option configures New.
type Option func(*Driver)

// WithScanRate sets the default scan class's poll interval (default 1s).
// This is the I/O update rate, independent of the runtime's program scan
// interval — the same split a PLC makes between I/O update and logic scan.
func WithScanRate(r time.Duration) Option { return func(d *Driver) { d.scanRate = r } }

// WithScanClass defines (or redefines) a scan class and its poll interval.
// The default class exists implicitly; NoPoll cannot be given a rate.
func WithScanClass(name string, rate time.Duration) Option {
	return func(d *Driver) { d.classRates[name] = rate }
}

// WithTagClass assigns tags to a scan class by glob patterns matched against
// the binding's nautilus name (eip's rule; modbus has no device path worth
// globbing). Assignments live in the driver constructor — not the generated
// manifest — so re-running `naut modbus import` never erases polling
// policy. Later assignments override earlier ones.
func WithTagClass(class string, patterns ...string) Option {
	return func(d *Driver) {
		d.assignments = append(d.assignments, classAssignment{class: class, patterns: patterns})
	}
}

// WithBlockGap sets the block-coalescing gap tolerance in registers/coils:
// how many unaddressed addresses one read may span to avoid a second
// round-trip. 0 never bridges a hole — the escape hatch for devices that
// fault a read touching an unimplemented register (brief §9 risk 4).
// Default DefaultBlockGap.
func WithBlockGap(n int) Option { return func(d *Driver) { d.gap = n } }

// WithLogger sets the structured logger.
func WithLogger(l *slog.Logger) Option {
	return func(d *Driver) {
		if l != nil {
			d.log = l
		}
	}
}

// WithDialer substitutes the TCP dial — tests hand the driver an in-process
// slave, or a recording wrapper that asserts what hits the wire.
func WithDialer(dial func(ctx context.Context, addr string) (net.Conn, error)) Option {
	return func(d *Driver) { d.dial = dial }
}

// classAssignment maps glob patterns to a scan class, applied in option
// order (the last matching assignment wins).
type classAssignment struct {
	class    string
	patterns []string
}

// Driver polls a set of Modbus TCP sources and implements io.Driver,
// io.BatchReader and io.QualityReporter. Each source owns a background
// goroutine: dial → prime every block → poll per scan class → reconnect
// with backoff on transport errors. ReadInputs never blocks on the network;
// it merges the latest per-source snapshots. WriteOutputs enqueues changed
// values per source; each source's loop pushes them to its device (latest
// value wins per tag).
type Driver struct {
	manifest Manifest
	plan     Plan
	scanRate time.Duration
	gap      int
	log      *slog.Logger
	dial     func(ctx context.Context, addr string) (net.Conn, error)

	classRates  map[string]time.Duration
	assignments []classAssignment

	sources  []*source
	bySource map[string]*source
	// byName routes writable bindings; enables routes the Enable command
	// tags (a source's Enable tag is a synthesized OUTPUT name: the runtime
	// hands its value to WriteOutputs, and the driver parks or wakes the
	// source instead of writing a register).
	byName  map[string]outBinding
	enables map[string][]*source

	inputs  []string // polled bindings + __Online companions, sorted
	outputs []string // writable bindings + enable tags, sorted

	// gateMu guards writeGate; the gate itself is read on every write and
	// every rewrite fire. Nil means always open (standalone, no redundancy).
	gateMu    sync.Mutex
	writeGate func() bool

	// baseline is armed by Start: the first WriteOutputs snapshot after it
	// describes the world, it does not command it (sparkplug-host's rule).
	// Bindings with Rewrite > 0 are the exception — their baseline value IS
	// queued, because a keep-alive word must be written on connect.
	wmu      sync.Mutex
	baseline bool

	started atomic.Bool
	reads   atomic.Uint64 // successful block reads
	writes  atomic.Uint64 // successful device writes
	errs    atomic.Uint64 // transport errors + exceptions + refused writes

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// outBinding is one writable binding with its source resolved.
type outBinding struct {
	src *source
	b   TagBinding
	f   Format
}

// source is one Modbus TCP endpoint's runtime state. Its loop goroutine is
// the only writer of conn state; mu guards what other goroutines read
// (snapshot, health) and write (pending, enabled).
type source struct {
	cfg    Source
	blocks []*blockRun

	mu       sync.Mutex
	snapshot nio.Values // decoded values, held across disconnects
	online   bool
	answered bool   // a read succeeded on the current connection (resets backoff)
	state    string // parked | connecting | connected | error
	sinceMs  int64
	lastErr  error
	retries  uint64
	excs     uint64
	rttMs    float64 // EWMA of request round-trip
	enabled  bool

	// pending is the last-value write queue (tag name → desired value),
	// held across disconnects and flushed on reconnect. written is the last
	// value handed over per output tag — the on-change reference and what
	// the rewrite loop re-asserts.
	pending  map[string]any
	written  map[string]any
	rewrites []*rewriteRun

	// kick wakes the loop: a queued write, an enable flip, a stop.
	kick chan struct{}
}

// blockRun is one plan block plus its schedule and exception state. Owned by
// the source loop except bad, which Quality reads under mu.
type blockRun struct {
	Block
	rate time.Duration
	next time.Time

	excCount    int       // consecutive exceptions
	parkedUntil time.Time // exception parking (excCount ≥ exceptionsToPark)
	bad         bool      // tags Bad while true (guarded by source.mu)
}

// rewriteRun schedules one Rewrite binding's re-assert (brief §4.1).
type rewriteRun struct {
	name   string
	period time.Duration
	next   time.Time
}

// New builds the driver: validates the manifest, computes the block-read
// plan, resolves scan classes, and synthesizes the per-source companions.
// It NEVER dials — call Start to begin polling.
func New(m Manifest, opts ...Option) (*Driver, error) {
	d := &Driver{
		manifest:   m,
		scanRate:   defaultScanRate,
		gap:        -1, // BuildPlan maps negative to DefaultBlockGap
		log:        slog.Default(),
		classRates: map[string]time.Duration{},
		bySource:   map[string]*source{},
		byName:     map[string]outBinding{},
		enables:    map[string][]*source{},
		baseline:   true,
	}
	for _, o := range opts {
		o(d)
	}
	if d.dial == nil {
		d.dial = func(ctx context.Context, addr string) (net.Conn, error) {
			var nd net.Dialer
			return nd.DialContext(ctx, "tcp", addr)
		}
	}

	// Tag-class assignments rewrite the effective scan class BEFORE the
	// plan is built, so the plan's (source, table, class) grouping is the
	// one that will actually poll.
	eff := m
	eff.Tags = append([]TagBinding(nil), m.Tags...)
	for i := range eff.Tags {
		for _, a := range d.assignments {
			for _, p := range a.patterns {
				if ok, _ := path.Match(p, eff.Tags[i].Name); ok {
					eff.Tags[i].ScanClass = a.class
					break
				}
			}
		}
	}
	d.manifest = eff

	plan, err := BuildPlan(eff, d.gap)
	if err != nil {
		return nil, err
	}
	d.plan = plan

	if err := d.resolveClasses(); err != nil {
		return nil, err
	}
	if err := d.buildSources(); err != nil {
		return nil, err
	}
	return d, nil
}

// resolveClasses checks every class the plan uses has a positive rate. The
// default class's rate is WithScanRate; NoPoll is reserved.
func (d *Driver) resolveClasses() error {
	if _, ok := d.classRates[DefaultClass]; !ok {
		d.classRates[DefaultClass] = d.scanRate
	}
	if rate, ok := d.classRates[NoPoll]; ok {
		return fmt.Errorf("modbus: scan class %q is reserved and cannot have a rate (%v)", NoPoll, rate)
	}
	for _, a := range d.assignments {
		if a.class == NoPoll {
			continue
		}
		if _, ok := d.classRates[a.class]; !ok {
			return fmt.Errorf("modbus: WithTagClass(%q, ...) references an undefined scan class — add WithScanClass(%q, rate)", a.class, a.class)
		}
	}
	for _, b := range d.plan.Blocks {
		rate, ok := d.classRates[b.Class]
		if !ok {
			return fmt.Errorf("modbus: binding %q names undefined scan class %q — add WithScanClass(%q, rate)", b.Bindings[0].Name, b.Class, b.Class)
		}
		if rate <= 0 {
			return fmt.Errorf("modbus: scan class %q has non-positive rate %v", b.Class, rate)
		}
	}
	return nil
}

// buildSources assembles the per-source runtime state, the write routing,
// the synthesized companion/enable names, and the input/output name lists.
func (d *Driver) buildSources() error {
	bound := make(map[string]bool, len(d.manifest.Tags))
	for _, t := range d.manifest.Tags {
		bound[t.Name] = true
	}

	for _, sc := range d.manifest.Sources {
		cfg := sc
		if cfg.Timeout <= 0 {
			cfg.Timeout = defaultTimeout
		}
		if cfg.RetryMin <= 0 {
			cfg.RetryMin = defaultRetryMin
		}
		if cfg.RetryMax <= 0 {
			cfg.RetryMax = defaultRetryMax
		}
		s := &source{
			cfg:      cfg,
			snapshot: nio.Values{},
			state:    "connecting",
			enabled:  true,
			pending:  map[string]any{},
			written:  map[string]any{},
			kick:     make(chan struct{}, 1),
		}
		d.sources = append(d.sources, s)
		d.bySource[cfg.ID] = s

		// The companion is a name the driver owns; a binding that claims it
		// would collide with the delivery (sparkplug/host's claimCompanion).
		online := cfg.OnlineTagName()
		if bound[online] {
			return fmt.Errorf("modbus: source %s: companion tag %q collides with a binding of the same name", cfg.ID, online)
		}
		d.inputs = append(d.inputs, online)

		if cfg.Enable != "" {
			if bound[cfg.Enable] {
				return fmt.Errorf("modbus: source %s: enable tag %q collides with a binding of the same name", cfg.ID, cfg.Enable)
			}
			d.enables[cfg.Enable] = append(d.enables[cfg.Enable], s)
		}
	}

	for _, blk := range d.plan.Blocks {
		s := d.bySource[blk.Source]
		br := &blockRun{Block: blk, rate: d.classRates[blk.Class]}
		s.blocks = append(s.blocks, br)
		for _, t := range blk.Bindings {
			d.inputs = append(d.inputs, t.Name)
		}
	}

	for _, t := range d.manifest.Tags {
		if !t.Writable {
			continue
		}
		s := d.bySource[t.Source]
		f, _ := t.format() // Validate (in BuildPlan) already vetted it
		d.byName[t.Name] = outBinding{src: s, b: t, f: f}
		d.outputs = append(d.outputs, t.Name)
		if t.Rewrite > 0 {
			s.rewrites = append(s.rewrites, &rewriteRun{name: t.Name, period: t.Rewrite})
		}
	}
	for name := range d.enables {
		d.outputs = append(d.outputs, name)
	}
	sort.Strings(d.inputs)
	sort.Strings(d.outputs)
	return nil
}

// SetWriteGate installs the write gate the rewrite loop and WriteOutputs
// consult: commands leave the driver only while gate() is true. The project
// wires it to redundancy leadership (leader.Elector.IsLeader), and a
// "logic healthy" signal can be composed into the same func.
//
// A FALSE GATE STOPS RE-ASSERTS BY DESIGN: a device whose keep-alive word
// (Banner SC10, i550 control word, an IO-Link master's Modbus watchdog)
// goes quiet trips its own fail-safe — which is exactly what must happen
// when this replica is not the leader or its logic is faulted, instead of a
// hung controller masking the fault by refreshing a stale command
// (brief §9 risks 1 and 3). Reads are unaffected: quality and __Online keep
// reporting.
func (d *Driver) SetWriteGate(gate func() bool) {
	d.gateMu.Lock()
	d.writeGate = gate
	d.gateMu.Unlock()
}

// gateOpen reports whether commands may leave the driver right now.
func (d *Driver) gateOpen() bool {
	d.gateMu.Lock()
	gate := d.writeGate
	d.gateMu.Unlock()
	return gate == nil || gate()
}

// ── names / classes ──────────────────────────────────────────────────────

// InputNames returns every tag the driver delivers values for: the polled
// bindings plus the synthesized "<source>__Online" companions — for
// runtime.Options.Inputs. The companions are present from Start even though
// a data tag stays absent until first read, so logic can interlock on
// __Online before the first poll (the sparkplug-host contract).
func (d *Driver) InputNames() []string {
	return append([]string(nil), d.inputs...)
}

// OutputNames returns the tags the driver accepts writes for: the writable
// bindings plus each source's Enable tag — for runtime.Options.Outputs. An
// Enable tag is a command to the DRIVER (park or wake the source), not a
// register write.
func (d *Driver) OutputNames() []string {
	return append([]string(nil), d.outputs...)
}

// ScanClasses reports the resolved poll groups — for diagnostics and tests.
func (d *Driver) ScanClasses() map[string][]string {
	out := map[string][]string{}
	for _, blk := range d.plan.Blocks {
		for _, t := range blk.Bindings {
			out[blk.Class] = append(out[blk.Class], t.Name)
		}
	}
	for _, names := range out {
		sort.Strings(names)
	}
	return out
}

// Plan returns the computed block-read plan (for `import --plan` and tests).
func (d *Driver) Plan() Plan { return d.plan }

// ── lifecycle ────────────────────────────────────────────────────────────

// Start launches one polling loop per source. It returns immediately; use
// Health to observe connection state.
func (d *Driver) Start(ctx context.Context) {
	ctx, d.cancel = context.WithCancel(ctx)
	d.wmu.Lock()
	d.baseline = true
	d.wmu.Unlock()
	d.started.Store(true)
	for _, s := range d.sources {
		d.wg.Add(1)
		go func(s *source) {
			defer d.wg.Done()
			d.run(ctx, s)
		}(s)
	}
}

// Stop tears every source loop down and waits.
func (d *Driver) Stop() {
	if d.cancel != nil {
		d.cancel()
		d.wg.Wait()
	}
}

// ── io.Driver ────────────────────────────────────────────────────────────

// ReadInputs merges the latest per-source snapshots, __Online companions
// included. Once Start has been called it never errors: a dead source holds
// its last values (the tag store must never see a zero because a cable was
// pulled) and quality rides on Quality() and the companions. A data tag
// never delivered stays absent, which preserves "reads fault" for a source
// that has never answered — __Online is the guard.
//
// Before Start it errors, so a project wired up but never started fails
// loudly rather than scanning zeros.
func (d *Driver) ReadInputs() (nio.Values, error) {
	if !d.started.Load() {
		return nil, fmt.Errorf("modbus: not started yet")
	}
	out := make(nio.Values, len(d.inputs))
	d.snapshotInto(out)
	return out, nil
}

// ReadInputsInto is ReadInputs without the per-scan allocation
// (io.BatchReader): same delivery, same semantics, into the runtime's map.
func (d *Driver) ReadInputsInto(dst nio.Values) error {
	if !d.started.Load() {
		return fmt.Errorf("modbus: not started yet")
	}
	if dst == nil {
		return fmt.Errorf("modbus: ReadInputsInto needs a non-nil map to fill")
	}
	n := d.snapshotInto(dst)
	// dst must describe THIS delivery: drop keys the driver no longer
	// holds. Our key set only grows (values hold across disconnects), so
	// equal sizes prove the sets match — the io.Memory trick.
	if len(dst) != n {
		delivered := make(map[string]bool, n)
		for _, s := range d.sources {
			delivered[s.cfg.OnlineTagName()] = true
			s.mu.Lock()
			for k := range s.snapshot {
				delivered[k] = true
			}
			s.mu.Unlock()
		}
		for k := range dst {
			if !delivered[k] {
				delete(dst, k)
			}
		}
	}
	return nil
}

// snapshotInto fills dst with every delivered value plus the companions and
// returns how many keys the driver delivered.
func (d *Driver) snapshotInto(dst nio.Values) int {
	n := 0
	for _, s := range d.sources {
		s.mu.Lock()
		for k, v := range s.snapshot {
			dst[k] = v
			n++
		}
		dst[s.cfg.OnlineTagName()] = s.online
		n++
		s.mu.Unlock()
	}
	return n
}

// WriteOutputs queues changed values for the source loops.
//
// OUTPUTS ARE COMMANDS; AN UNCHANGED OUTPUT SINCE START IS NOT A COMMAND.
// The rules, in order (sparkplug-host's, adapted for keep-alive bindings):
//
//  1. ENABLE tags act immediately, gate or no gate, baseline or no
//     baseline: they command the DRIVER (park/wake a source), not a device.
//  2. GATE. While the write gate is closed (not leader, logic faulted) no
//     device command is queued or recorded — see SetWriteGate: the silence
//     is what trips a device-side watchdog, by design.
//  3. BASELINE. The first snapshot after Start is recorded, not written —
//     it is the state of the world at t=0, not a set of commands. EXCEPT
//     bindings with Rewrite > 0: a keep-alive word must be asserted on
//     connect (an SC10 drops its enable otherwise), so their baseline value
//     is queued.
//  4. CHANGE. Thereafter a value equal to the last handed-over value is not
//     a command. A changed value is queued per source, last value per tag,
//     and held across a disconnect — flushed when the source reconnects,
//     reported as queued writes on the device row.
func (d *Driver) WriteOutputs(vals nio.Values) error {
	kicked := map[*source]bool{}
	for name, v := range vals {
		if srcs, ok := d.enables[name]; ok {
			on, err := toBool(v)
			if err != nil {
				d.log.Warn("modbus: enable tag with non-bool value", "tag", name, "value", v)
				continue
			}
			for _, s := range srcs {
				s.mu.Lock()
				changed := s.enabled != on
				s.enabled = on
				s.mu.Unlock()
				if changed {
					kicked[s] = true
				}
			}
		}
	}

	d.wmu.Lock()
	baseline := d.baseline
	d.baseline = false
	d.wmu.Unlock()

	gateOpen := d.gateOpen()
	for name, v := range vals {
		ob, ok := d.byName[name]
		if !ok {
			continue // enable tags handled above; unknown names ignored
		}
		if !gateOpen {
			continue
		}
		s := ob.src
		s.mu.Lock()
		if baseline && ob.b.Rewrite <= 0 {
			// Record, never send. A tag with a command already queued
			// keeps it: that one WAS asked for.
			if _, queued := s.pending[name]; !queued {
				s.written[name] = v
			}
		} else if prev, ok := s.written[name]; ok && sameScalar(prev, v) {
			// unchanged since the last value we accounted for
		} else {
			s.written[name] = v
			s.pending[name] = v
			kicked[s] = true
		}
		s.mu.Unlock()
	}
	for s := range kicked {
		nudge(s.kick)
	}
	return nil
}

// nudge wakes a source loop without blocking.
func nudge(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

// sameScalar compares two handed-over output values for on-change
// suppression, only ever with ==-safe kinds: modbus tags are scalars, and a
// compound value someone hands anyway must not panic an `any ==` — it just
// always reads as changed and lets the encoder report it.
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
	case string:
		y, ok := b.(string)
		return ok && x == y
	}
	return false
}

// ── connection / poll loop ───────────────────────────────────────────────

// run is one source's life: parked ⇄ connecting ⇄ connected with 1s→60s
// backoff on dial/IO errors (brief §2). The backoff ladder resets only once
// the device has answered a read on a connection, not on a successful dial.
func (d *Driver) run(ctx context.Context, s *source) {
	backoff := s.cfg.RetryMin
	for ctx.Err() == nil {
		if !s.isEnabled() {
			s.setState("parked", nil)
			select {
			case <-ctx.Done():
				return
			case <-s.kick:
			}
			continue
		}

		c, err := d.dialSource(ctx, s)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			d.errs.Add(1)
			s.mu.Lock()
			s.retries++
			s.mu.Unlock()
			s.setState("error", err)
			d.log.Warn("modbus: connect failed", "source", s.cfg.ID, "addr", s.cfg.Addr(), "error", err, "retryIn", backoff)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			case <-s.kick: // an enable flip should park promptly
			}
			if backoff *= 2; backoff > s.cfg.RetryMax {
				backoff = s.cfg.RetryMax
			}
			continue
		}
		s.mu.Lock()
		s.online, s.answered = true, false
		s.mu.Unlock()
		s.setState("connected", nil)
		d.log.Info("modbus: connected", "source", s.cfg.ID, "addr", s.cfg.Addr(), "blocks", len(s.blocks))

		d.serve(ctx, s, c)

		_ = c.Close()
		s.mu.Lock()
		s.online = false
		answered := s.answered
		s.mu.Unlock()
		if ctx.Err() != nil || !s.isEnabled() {
			continue // shutting down, or parking: no wait, no escalation
		}
		s.setState("error", nil) // lastErr already set by whatever broke it
		// A connection that broke is a failure like a dial that failed, and
		// waits the same way — otherwise a device that accepts TCP and never
		// answers (a hung gateway, the wrong port) is re-dialed every Timeout
		// forever with no backoff at all. A link that did answer starts the
		// ladder over; one that never did keeps climbing it.
		if answered {
			backoff = s.cfg.RetryMin
		}
		d.log.Warn("modbus: connection lost", "source", s.cfg.ID, "addr", s.cfg.Addr(), "retryIn", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		case <-s.kick:
		}
		if backoff *= 2; backoff > s.cfg.RetryMax {
			backoff = s.cfg.RetryMax
		}
	}
}

// dialSource opens the TCP connection and wraps it in the framing layer.
func (d *Driver) dialSource(ctx context.Context, s *source) (*tcpConn, error) {
	dctx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
	defer cancel()
	nc, err := d.dial(dctx, s.cfg.Addr())
	if err != nil {
		return nil, err
	}
	return newTCPConn(nc, s.cfg.Timeout), nil
}

// serve polls one connected source until the connection breaks, the source
// is disabled, or ctx ends. On connect: flush the queued writes first (a
// command must not wait behind a full poll cycle), re-assert every Rewrite
// binding that has a value, then prime every block so the snapshot fills
// without waiting a slow class's interval.
func (d *Driver) serve(ctx context.Context, s *source, c conn) {
	now := time.Now()
	s.mu.Lock()
	for _, rw := range s.rewrites {
		if v, ok := s.written[rw.name]; ok {
			s.pending[rw.name] = v // re-assert after an outage, on the spot
		}
		rw.next = now.Add(rw.period)
	}
	s.mu.Unlock()
	if !d.flushWrites(ctx, s, c) {
		return
	}
	for _, br := range s.blocks {
		if !d.pollBlock(ctx, s, c, br) {
			return
		}
		br.next = time.Now().Add(br.rate)
	}

	for {
		if ctx.Err() != nil || !s.isEnabled() {
			return
		}
		wake := time.Hour
		now := time.Now()
		for _, br := range s.blocks {
			next := br.next
			if br.parkedUntil.After(next) {
				next = br.parkedUntil
			}
			if until := next.Sub(now); until < wake {
				wake = until
			}
		}
		for _, rw := range s.rewrites {
			if until := rw.next.Sub(now); until < wake {
				wake = until
			}
		}
		if wake < 0 {
			wake = 0
		}
		timer := time.NewTimer(wake)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-s.kick:
			timer.Stop()
			if !s.isEnabled() {
				return
			}
			if !d.flushWrites(ctx, s, c) {
				return
			}
		case <-timer.C:
			now := time.Now()
			for _, rw := range s.rewrites {
				if rw.next.After(now) {
					continue
				}
				rw.next = now.Add(rw.period)
				if !d.gateOpen() {
					continue // a closed gate stops re-asserts, by design
				}
				s.mu.Lock()
				if v, ok := s.written[rw.name]; ok {
					s.pending[rw.name] = v
				}
				s.mu.Unlock()
			}
			if !d.flushWrites(ctx, s, c) {
				return
			}
			for _, br := range s.blocks {
				if br.next.After(now) || br.parkedUntil.After(now) {
					continue
				}
				if !d.pollBlock(ctx, s, c, br) {
					return
				}
				br.next = time.Now().Add(br.rate)
			}
		}
	}
}

// pollBlock executes one block read and decodes it into the snapshot.
// Returns false when the connection can no longer be trusted (transport
// error → reconnect with backoff). An exception response is a per-block
// failure: the block's tags go Bad, the rest of the source keeps polling,
// and three consecutive exceptions park the block for one backoff period.
func (d *Driver) pollBlock(ctx context.Context, s *source, c conn, br *blockRun) bool {
	t0 := time.Now()
	var regs []uint16
	var bits []bool
	var err error
	if registerTable(br.Table) {
		regs, err = readRegisters(ctx, c, s.cfg.UnitID, br.FC(), br.Start, br.Count)
	} else {
		bits, err = readBits(ctx, c, s.cfg.UnitID, br.FC(), br.Start, br.Count)
	}
	if err != nil {
		d.errs.Add(1)
		var exc ExceptionError
		if errors.As(err, &exc) {
			br.excCount++
			s.mu.Lock()
			s.excs++
			s.lastErr = fmt.Errorf("%s %d..%d: %w", br.Table, br.Start, int(br.Start)+int(br.Count)-1, err)
			br.bad = true
			s.mu.Unlock()
			if br.excCount == exceptionsToPark {
				br.parkedUntil = time.Now().Add(s.cfg.RetryMin)
				d.log.Warn("modbus: block parked after consecutive exceptions",
					"source", s.cfg.ID, "table", br.Table, "start", br.Start,
					"count", br.Count, "exception", err, "for", s.cfg.RetryMin)
			} else if br.excCount > exceptionsToPark {
				br.excCount = exceptionsToPark // re-park each backoff, don't overflow
				br.parkedUntil = time.Now().Add(s.cfg.RetryMin)
			}
			return true
		}
		if ctx.Err() != nil {
			return false
		}
		s.mu.Lock()
		s.lastErr = err
		s.retries++
		s.mu.Unlock()
		d.log.Warn("modbus: read failed", "source", s.cfg.ID, "table", br.Table,
			"start", br.Start, "error", err)
		return false
	}
	rtt := time.Since(t0)
	d.reads.Add(1)

	s.mu.Lock()
	if s.rttMs == 0 {
		s.rttMs = float64(rtt.Microseconds()) / 1000
	} else {
		s.rttMs = 0.8*s.rttMs + 0.2*float64(rtt.Microseconds())/1000
	}
	br.excCount = 0
	br.bad = false
	s.answered = true
	for _, t := range br.Bindings {
		off := int(t.Address) - int(br.Start)
		if regs != nil {
			f, _ := t.format()
			v, derr := decodeRegisters(f, regs[off:off+f.Words()], s.cfg.WordOrder, s.cfg.ByteOrder, t.Scale, t.Offset)
			if derr != nil {
				s.lastErr = fmt.Errorf("tag %s: %w", t.Name, derr)
				continue
			}
			s.snapshot[t.Name] = v
		} else {
			s.snapshot[t.Name] = bits[off]
		}
	}
	s.mu.Unlock()
	return true
}

// flushWrites pushes this source's pending writes to the device, coalescing
// contiguous holding registers into FC16 (one register FC6), contiguous
// coils into FC15 (one coil FC5). Returns false when the connection broke;
// unattempted writes stay queued for the reconnect.
func (d *Driver) flushWrites(ctx context.Context, s *source, c conn) bool {
	s.mu.Lock()
	if len(s.pending) == 0 {
		s.mu.Unlock()
		return true
	}
	work := s.pending
	s.pending = map[string]any{}
	s.mu.Unlock()

	regWrites, coilWrites, bitWrites := d.stageWrites(s, work)

	requeue := func() {
		s.mu.Lock()
		for n, v := range work {
			if _, exists := s.pending[n]; !exists {
				s.pending[n] = v
			}
		}
		s.mu.Unlock()
	}

	// bit:N writables read-modify-write their register one at a time — the
	// other 15 bits belong to the device (or to sibling bindings).
	for _, bw := range bitWrites {
		cur, err := readRegisters(ctx, c, s.cfg.UnitID, FCReadHoldingRegisters, bw.addr, 1)
		if err == nil {
			// Merge in normalized (big-endian) space, then restore the
			// source's wire order — wireOrder is its own inverse.
			w := wireOrder(cur, s.cfg.WordOrder, s.cfg.ByteOrder)[0]
			w = setBit(w, bw.bit, bw.on)
			out := wireOrder([]uint16{w}, s.cfg.WordOrder, s.cfg.ByteOrder)[0]
			err = writeSingleRegister(ctx, c, s.cfg.UnitID, bw.addr, out)
		}
		if !d.wroteOK(ctx, s, bw.names, err) {
			requeue()
			return false
		}
	}

	for _, run := range coalesceRegs(regWrites) {
		var err error
		if len(run.values) == 1 {
			err = writeSingleRegister(ctx, c, s.cfg.UnitID, run.addr, run.values[0])
		} else {
			err = writeMultipleRegisters(ctx, c, s.cfg.UnitID, run.addr, run.values)
		}
		if !d.wroteOK(ctx, s, run.names, err) {
			requeue()
			return false
		}
	}
	for _, run := range coalesceCoils(coilWrites) {
		var err error
		if len(run.values) == 1 {
			err = writeSingleCoil(ctx, c, s.cfg.UnitID, run.addr, run.values[0])
		} else {
			err = writeMultipleCoils(ctx, c, s.cfg.UnitID, run.addr, run.values)
		}
		if !d.wroteOK(ctx, s, run.names, err) {
			requeue()
			return false
		}
	}
	return true
}

// wroteOK accounts one device write. An exception means the device refused
// THIS write on a healthy connection: it is dropped (re-queueing would loop
// forever) and reported. A transport error means reconnect: false, and the
// caller re-queues everything unattempted.
func (d *Driver) wroteOK(ctx context.Context, s *source, names []string, err error) bool {
	if err == nil {
		d.writes.Add(1)
		return true
	}
	d.errs.Add(1)
	var exc ExceptionError
	if errors.As(err, &exc) {
		s.mu.Lock()
		s.lastErr = fmt.Errorf("write %v: %w", names, err)
		s.excs++
		s.mu.Unlock()
		d.log.Warn("modbus: write refused", "source", s.cfg.ID, "tags", names, "error", err)
		return true
	}
	if ctx.Err() == nil {
		s.mu.Lock()
		s.lastErr = fmt.Errorf("write %v: %w", names, err)
		s.retries++
		s.mu.Unlock()
		d.log.Warn("modbus: write failed", "source", s.cfg.ID, "tags", names, "error", err)
	}
	return false
}

// regWrite / coilWrite / bitWrite are staged single-address writes, ready
// to coalesce.
type regWrite struct {
	addr  uint16
	words []uint16
	name  string
}
type coilWrite struct {
	addr uint16
	on   bool
	name string
}
type bitWrite struct {
	addr  uint16
	bit   int
	on    bool
	names []string
}

// stageWrites encodes one batch of pending values into wire shapes. Encode
// failures (a value out of the format's range) are dropped with an error —
// a clamped setpoint silently commanding something else is worse.
func (d *Driver) stageWrites(s *source, work map[string]any) ([]regWrite, []coilWrite, []bitWrite) {
	var regs []regWrite
	var coils []coilWrite
	var bits []bitWrite
	for name, v := range work {
		ob := d.byName[name]
		t := ob.b
		if t.Table == TableCoil {
			on, err := toBool(v)
			if err != nil {
				d.dropWrite(s, name, err)
				continue
			}
			coils = append(coils, coilWrite{addr: t.Address, on: on, name: name})
			continue
		}
		if ob.f.Kind == "bit" {
			on, err := toBool(v)
			if err != nil {
				d.dropWrite(s, name, err)
				continue
			}
			bits = append(bits, bitWrite{addr: t.Address, bit: ob.f.Bit, on: on, names: []string{name}})
			continue
		}
		words, err := encodeRegisters(ob.f, v, s.cfg.WordOrder, s.cfg.ByteOrder, t.Scale, t.Offset)
		if err != nil {
			d.dropWrite(s, name, err)
			continue
		}
		regs = append(regs, regWrite{addr: t.Address, words: words, name: name})
	}
	// Merge bit writes to the same register into one read-modify-write.
	sort.Slice(bits, func(i, j int) bool {
		if bits[i].addr != bits[j].addr {
			return bits[i].addr < bits[j].addr
		}
		return bits[i].bit < bits[j].bit
	})
	var merged []bitWrite
	for _, bw := range bits {
		if n := len(merged); n > 0 && merged[n-1].addr == bw.addr {
			// Can't merge two different bits into one register value here
			// without the device's other bits; keep them as separate RMWs
			// unless it is literally the same bit.
			if merged[n-1].bit == bw.bit {
				merged[n-1].on = bw.on
				merged[n-1].names = append(merged[n-1].names, bw.names...)
				continue
			}
		}
		merged = append(merged, bw)
	}
	return regs, coils, merged
}

func (d *Driver) dropWrite(s *source, name string, err error) {
	d.errs.Add(1)
	s.mu.Lock()
	s.lastErr = fmt.Errorf("write %s: %w", name, err)
	s.mu.Unlock()
	d.log.Warn("modbus: write dropped", "source", s.cfg.ID, "tag", name, "error", err)
}

// regRun / coilRun are coalesced contiguous writes.
type regRun struct {
	addr   uint16
	values []uint16
	names  []string
}
type coilRun struct {
	addr   uint16
	values []bool
	names  []string
}

// coalesceRegs merges EXACTLY adjacent register writes into FC16 runs. No
// gap tolerance on the write side: a write must never touch a register
// nobody commanded. A later write to the same address wins (last value per
// tag already guarantees one value per binding; two bindings sharing an
// address overwrite in address order, which validation already constrains
// to single-word aliases).
func coalesceRegs(ws []regWrite) []regRun {
	sort.Slice(ws, func(i, j int) bool { return ws[i].addr < ws[j].addr })
	var runs []regRun
	end := -1
	for _, w := range ws {
		if n := len(runs); n > 0 && int(w.addr) == end && len(runs[n-1].values)+len(w.words) <= maxWriteRegisters {
			runs[n-1].values = append(runs[n-1].values, w.words...)
			runs[n-1].names = append(runs[n-1].names, w.name)
			end += len(w.words)
			continue
		}
		runs = append(runs, regRun{addr: w.addr, values: append([]uint16(nil), w.words...), names: []string{w.name}})
		end = int(w.addr) + len(w.words)
	}
	return runs
}

// coalesceCoils merges exactly adjacent coil writes into FC15 runs.
func coalesceCoils(ws []coilWrite) []coilRun {
	sort.Slice(ws, func(i, j int) bool { return ws[i].addr < ws[j].addr })
	var runs []coilRun
	end := -1
	for _, w := range ws {
		if n := len(runs); n > 0 && int(w.addr) == end && len(runs[n-1].values) < maxWriteBits {
			runs[n-1].values = append(runs[n-1].values, w.on)
			runs[n-1].names = append(runs[n-1].names, w.name)
			end++
			continue
		}
		runs = append(runs, coilRun{addr: w.addr, values: []bool{w.on}, names: []string{w.name}})
		end = int(w.addr) + 1
	}
	return runs
}

// ── source state helpers ─────────────────────────────────────────────────

func (s *source) isEnabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enabled
}

func (s *source) setState(state string, err error) {
	s.mu.Lock()
	if s.state != state {
		s.state = state
		s.sinceMs = time.Now().UnixMilli()
	}
	if err != nil {
		s.lastErr = err
	}
	if state == "connected" {
		s.lastErr = nil
	}
	s.mu.Unlock()
}

func (s *source) setOnline(on bool) {
	s.mu.Lock()
	s.online = on
	s.mu.Unlock()
}

// Interface conformance — the same trio io.Memory proves.
var (
	_ nio.BatchReader     = (*Driver)(nil)
	_ nio.QualityReporter = (*Driver)(nil)
)
