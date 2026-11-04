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
// "<source>__Online" is a BOOL input that is true while the device answers:
// the guard a program reads before trusting the source's tags, exactly like
// sparkplug/host's "<site>__Online" (issue #539). It starts false and goes
// true on the first answer on a connection, not on the TCP dial. An answer
// is a reply, or an exception other than the gateway ones: 0x02 (illegal
// data address) is the device talking about one bad span, which per-tag
// quality already reports. Gateway exceptions 0x0A (path unavailable) and
// 0x0B (target failed to respond) mean nobody answered. The source goes
// offline, still connected to the gateway, once every block's latest read
// got one AND the last gatewayMissesToOffline requests in a row did (one
// RS-485 timeout behind a gateway is not an outage).
//
// For a polled source only reads decide: a write's answer may come from the
// gateway itself, or from a keep-alive that answers while the reads do not.
// A source with nothing polled goes by its writes instead: with a rewrite:
// binding it waits for the first re-assert's answer; without one there is
// nothing to ask until the program commands it, so it is online on connect
// (a program that gates its commands on __Online would otherwise never
// send one). A lost connection or a parked source is offline.
const suffixOnline = "__Online"

// suffixOutages names the second per-source companion: "<source>__Outages"
// is a DINT input counting the times __Online has fallen from true to false
// since the driver started. Since __Online goes true only on an answer, the
// first connect is not an outage, and neither is starting offline or a
// connection the device never answered on; Stop is not one either. A
// gateway whose field device stops answering is one. A program compares it
// with last scan's value to latch an outage it might otherwise miss between
// scans (issue #449; optommp and plcnext deliver the same name).
const suffixOutages = "__Outages"

// gatewayMissesToOffline is how many gateway no-answers in a row, with no
// answer between them, a source needs before it goes offline: a single
// RS-485 timeout or CRC error behind a gateway is noise, not an outage.
const gatewayMissesToOffline = 3

// gatewayNoAnswer reports a gateway exception that means the device behind
// the gateway did not answer: 0x0A gateway path unavailable, 0x0B gateway
// target device failed to respond.
func gatewayNoAnswer(err error) bool {
	var exc ExceptionError
	return errors.As(err, &exc) && (exc.Code == 0x0A || exc.Code == 0x0B)
}

// writeNotRun reports an exception to a write that says the write did not
// run, so the value is sent again with the next WriteOutputs (see wroteOK):
// a gateway no-answer (0x0A, 0x0B) or 0x06 server device busy. Not 0x04
// server device failure: the write may have partly run.
func writeNotRun(err error) bool {
	var exc ExceptionError
	return gatewayNoAnswer(err) || (errors.As(err, &exc) && exc.Code == 0x06)
}

// OnlineTagName is the source's synthesized BOOL companion tag name — shared
// with the generator so tags/modbus.yaml declares exactly what the driver
// delivers.
func (s Source) OnlineTagName() string { return s.ID + suffixOnline }

// OutagesTagName is the source's synthesized DINT outage counter tag name.
func (s Source) OutagesTagName() string { return s.ID + suffixOutages }

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

	inputs  []string // polled bindings + __Online/__Outages companions, sorted
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
	online   bool       // the device answers (see suffixOnline)
	outages  int64      // online → offline transitions since New
	answered bool       // the device answered on the current connection (resets backoff)
	gwFailed int        // blocks whose latest read got a gateway no-answer (0x0A/0x0B)
	gwMisses int        // gateway no-answers in a row since the last answer
	state    string     // parked | connecting | connected | error
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
	gwFail      bool      // latest read got a gateway no-answer (guarded by source.mu)
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

		// The companions are names the driver owns; a binding that claims
		// one would collide with the delivery (sparkplug/host's
		// claimCompanion).
		for _, name := range [...]string{cfg.OnlineTagName(), cfg.OutagesTagName()} {
			if bound[name] {
				return fmt.Errorf("modbus: source %s: companion tag %q collides with a binding of the same name", cfg.ID, name)
			}
			d.inputs = append(d.inputs, name)
		}

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

// SetWriteGate installs the write gate WriteOutputs, the rewrite loop and
// the flush consult: commands leave the driver only while gate() is true.
// A command queued while the gate was open, during an outage or put back
// by a broken flush, is dropped if the gate has closed by the time it
// would go out, and so is the rest of a flush the gate closes partway
// through. A dropped command is forgotten, so the first WriteOutputs push
// after the gate reopens commands the same value again. The runtime
// pushes on an output change, the first scan, after a failed write and
// on a redundancy takeover, so a replica that regains leadership pushes
// at once; a gate that reopens without a takeover (logic healthy again)
// waits for the next output change. The project wires the gate to
// redundancy leadership (leader.Elector.IsLeader), and a "logic healthy"
// signal can be composed into the same func.
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
// bindings plus the synthesized "<source>__Online" and "<source>__Outages"
// companions — for runtime.Options.Inputs. The companions are present from Start even though
// a data tag stays absent until first read, so logic can interlock on
// __Online before the first poll (the sparkplug-host contract).
func (d *Driver) InputNames() []string {
	return append([]string(nil), d.inputs...)
}

// InputType names the IEC type an input delivers, from its binding's
// format — what a manifest tag with no type: that no program declares
// starts at the zero of until the first poll (runtime.TagDef.DriverType):
// BOOL for coils, discretes, bool and bit:N; DINT for an unscaled integer
// format, which is what the driver delivers (int64); REAL for a float
// format or anything scaled. A source's __Online companion is BOOL and its
// __Outages companion DINT. "" for a name the driver does not deliver.
func (d *Driver) InputType(name string) string {
	for _, s := range d.sources {
		switch name {
		case s.cfg.OnlineTagName():
			return "BOOL"
		case s.cfg.OutagesTagName():
			return "DINT"
		}
	}
	for _, t := range d.manifest.Tags {
		if t.Name != name {
			continue
		}
		f, err := t.format()
		if err != nil {
			return ""
		}
		switch {
		case f.Kind == "bool" || f.Kind == "bit":
			return "BOOL"
		case f.integer() && (t.Scale == 0 || t.Scale == 1) && t.Offset == 0:
			return "DINT"
		default:
			return "REAL"
		}
	}
	return ""
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

// ReadInputs merges the latest per-source snapshots, __Online and
// __Outages companions included. Once Start has been called it never
// errors: a dead source holds its last values (the tag store must never see
// a zero because a cable was pulled) and quality rides on Quality() and the
// companions. A data tag
// never delivered stays absent from the delivery: the runtime holds it at
// its init: or zero (a protocol input, runtime.TagDef.Comms) and Quality
// says NotConnected — __Online is the guard.
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
			delivered[s.cfg.OutagesTagName()] = true
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
		dst[s.cfg.OutagesTagName()] = s.outages
		n += 2
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
// the device has answered on a connection, not on a successful dial, and
// __Online goes true on that answer too (heardLocked), never on the dial.
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
		s.answered = false
		s.gwFailed, s.gwMisses = 0, 0
		for _, br := range s.blocks {
			br.gwFail = false
		}
		if len(s.blocks) == 0 && len(s.rewrites) == 0 {
			// Write-only, nothing re-asserted: nothing to ask the device
			// until the program commands it (see suffixOnline).
			s.online = true
		}
		s.mu.Unlock()
		s.setState("connected", nil)
		d.log.Info("modbus: connected", "source", s.cfg.ID, "addr", s.cfg.Addr(), "blocks", len(s.blocks))

		// A device that closes a connection it has been idle on (a
		// server-side idle timeout) is not an outage: reconnect on the
		// spot and carry on as if nothing happened, online and uncounted.
		// Only a reconnect that fails makes it one, below.
		closed := d.serve(ctx, s, c)
		for closed && ctx.Err() == nil && s.isEnabled() {
			_ = c.Close()
			nc, err := d.dialSource(ctx, s)
			if err != nil {
				if ctx.Err() == nil {
					d.errs.Add(1)
					s.mu.Lock()
					s.lastErr = fmt.Errorf("reconnect after an idle close: %w", err)
					s.retries++
					s.mu.Unlock()
				}
				break
			}
			// Debug: a device with a short idle timeout does this all day.
			d.log.Debug("modbus: device closed an idle connection; reconnected",
				"source", s.cfg.ID, "addr", s.cfg.Addr())
			c = nc
			closed = d.resume(ctx, s, c)
		}

		_ = c.Close()
		s.mu.Lock()
		answered := s.answered
		// An outage is __Online falling: a connection the device never
		// answered on (a gateway that accepts TCP and stays silent) never
		// went online, so losing it is not one, and neither is the driver
		// stopping.
		s.goOfflineLocked(ctx.Err() == nil)
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
	if tc, ok := nc.(*net.TCPConn); ok {
		// Best effort: a platform that refuses the counts keeps its own
		// keepalive, and the idle check still sees a closed or reset link.
		_ = tc.SetKeepAliveConfig(linkKeepAlive)
	}
	return newTCPConn(nc, s.cfg.Timeout), nil
}

// idleChecker is the part of a connection that can tell, between
// requests and without sending one, whether the link is still up.
// tcpConn implements it; scripted test conns do not.
type idleChecker interface {
	checkIdle() error
	quietFor() time.Duration
	heldFor() time.Duration
	age() time.Duration
}

// idleCheckEvery is how often serve asks an idle connection whether it is
// still up: the longest a source waits with nothing due before it looks.
// It matters for a source with nothing to poll (write-only bindings, no
// rewrite:), whose loop would otherwise sleep until the next command, and
// for slow scan classes. The check sends nothing to the device (issue #552).
const idleCheckEvery = time.Second

// checkBeforeSendAfter: a request about to go out on a link that has been
// quiet this long is preceded by the idle check, so a connection the
// device closed on its idle timeout since the last check is reconnected
// quietly instead of failing the request (an outage). It costs one
// idlePeek on a healthy link, and only after a quiet spell; a device
// whose idle timeout is at least this long never sees a request on a
// connection it has closed, short of a close inside the peek itself.
const checkBeforeSendAfter = 100 * time.Millisecond

// quietCloseAfter is how long the device must be known to have kept a
// connection (a response, or an idle check that found it up, at least this
// long after the dial) for its clean close to be a quiet reconnect rather
// than an outage. Real idle timeouts are seconds to minutes; a device that
// accepts and hangs up at once (a crash-looping server, a gateway out of
// connection slots) must still go offline and back off. serve checks every
// connection once it is this old, so a close before then is seen promptly.
const quietCloseAfter = 100 * time.Millisecond

// linkKeepAlive is the TCP keepalive on every source connection. A link
// that is cut silently (a pulled cable, a dead switch) sends no FIN, so an
// idle socket would never learn it is gone; keepalive probes are TCP
// segments, not Modbus requests, and after Count unanswered ones the
// kernel fails the socket, which the next idle check reads. A link idle
// for Idle is declared dead Interval×Count later: 5 + 3×3 = 14 s, and the
// source is offline within idleCheckEvery after that (15 s in all).
// Keepalive only runs on a quiet link; a request sent into a dead one
// fails on its Timeout, as before.
var linkKeepAlive = net.KeepAliveConfig{
	Enable:   true,
	Idle:     5 * time.Second,
	Interval: 3 * time.Second,
	Count:    3,
}

// serve polls one connected source until the connection breaks, the source
// is disabled, or ctx ends. On connect: flush the queued writes first (a
// command must not wait behind a full poll cycle), re-assert every Rewrite
// binding that has a value, then prime every block so the snapshot fills
// without waiting a slow class's interval.
//
// It returns true only when the device closed the connection cleanly while
// no request was outstanding (an idle timeout on the device), which run
// answers with a quiet reconnect (see resume) rather than an outage.
func (d *Driver) serve(ctx context.Context, s *source, c conn) (idleClosed bool) {
	if !d.prime(ctx, s, c) {
		return false
	}
	return d.loop(ctx, s, c)
}

// resume carries a source over to the connection that replaced one the
// device closed while idle: the queued writes go out, then the loop
// carries on with the poll and rewrite schedule as it was, nothing
// re-asserted or primed, so a device whose idle timeout is shorter than a
// slow scan class is not read more often than configured. It returns what
// serve does.
func (d *Driver) resume(ctx context.Context, s *source, c conn) bool {
	if !d.flushWrites(ctx, s, c) {
		return false
	}
	return d.loop(ctx, s, c)
}

// prime is serve's on-connect step: re-assert, flush, read every block.
// False when the connection broke.
func (d *Driver) prime(ctx context.Context, s *source, c conn) bool {
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
		return false
	}
	for _, br := range s.blocks {
		if !d.pollBlock(ctx, s, c, br) {
			return false
		}
		br.next = time.Now().Add(br.rate)
	}
	return true
}

// loop is serve's steady state: wait for the next due block, rewrite or
// command, and check the link while nothing is due. It returns true when
// the device closed the connection while idle.
func (d *Driver) loop(ctx context.Context, s *source, c conn) bool {
	for {
		if ctx.Err() != nil || !s.isEnabled() {
			return false
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
		idle := wake > idleCheckEvery
		if idle {
			wake = idleCheckEvery // nothing due soon: look at the link meanwhile
		}
		if ic, ok := c.(idleChecker); ok && ic.heldFor() < quietCloseAfter {
			// Not yet known to have been held quietCloseAfter: look as
			// soon as it is that old, so a device that hangs up at once is
			// told apart from one with an idle timeout (see linkUp).
			if until := quietCloseAfter - ic.age(); until < wake {
				wake, idle = max(until, 0), true
			}
		}
		timer := time.NewTimer(wake)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-s.kick:
			timer.Stop()
			if !s.isEnabled() {
				return false
			}
			if up, closed := d.linkUp(ctx, s, c, checkBeforeSendAfter); !up {
				return closed
			}
			if !d.flushWrites(ctx, s, c) {
				return false
			}
		case <-timer.C:
			quiet := checkBeforeSendAfter
			if idle {
				quiet = 0 // woke only to look
			}
			if up, closed := d.linkUp(ctx, s, c, quiet); !up {
				return closed
			}
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
				return false
			}
			for _, br := range s.blocks {
				if br.next.After(now) || br.parkedUntil.After(now) {
					continue
				}
				if !d.pollBlock(ctx, s, c, br) {
					return false
				}
				br.next = time.Now().Add(br.rate)
			}
		}
	}
}

// linkUp runs the idle check on c, if it has one and nothing has been sent
// on it for at least quiet. up false means the link is gone and serve
// returns. closed true says how: the device closed it cleanly with nothing
// outstanding, after keeping it at least quietCloseAfter, which run
// answers with a quiet reconnect. A close sooner than that is an outage
// like the rest. Anything else (a reset, a keepalive that went unanswered,
// unsolicited bytes, a socket closed under us) is recorded as the error,
// and run drops the connection, takes the source offline (an outage if it
// was online), and reconnects with backoff, as for a failed read.
func (d *Driver) linkUp(ctx context.Context, s *source, c conn, quiet time.Duration) (up, closed bool) {
	ic, ok := c.(idleChecker)
	if !ok || ic.quietFor() < quiet {
		return true, false
	}
	err := ic.checkIdle()
	if err == nil {
		return true, false
	}
	if ctx.Err() != nil {
		return false, false
	}
	if held := ic.heldFor(); errors.Is(err, errIdleClosed) {
		if held >= quietCloseAfter {
			return false, true
		}
		err = fmt.Errorf("%w before it had been held %v", err, quietCloseAfter)
	}
	d.errs.Add(1)
	s.mu.Lock()
	s.lastErr = err
	s.retries++
	s.mu.Unlock()
	d.log.Warn("modbus: link lost while idle", "source", s.cfg.ID, "addr", s.cfg.Addr(), "error", err)
	return false, false
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
			if gatewayNoAnswer(err) {
				s.blockNoAnswerLocked(br, ctx.Err() == nil)
			} else {
				s.blockAnsweredLocked(br) // the device is there; this span is refused
			}
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
	s.blockAnsweredLocked(br)
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
// the write that broke and the ones not yet attempted stay queued for the
// reconnect. Writes answered in this flush, confirmed or with an
// exception, are not re-queued: a re-send would put back a value someone
// else may have written since. One a gateway answered 0x0A/0x0B for is
// forgotten instead, so the next WriteOutputs sends it (see wroteOK). With
// the write gate closed the queue is dropped instead, and if the gate
// closes partway through, the rest of the flush is (see SetWriteGate).
func (d *Driver) flushWrites(ctx context.Context, s *source, c conn) bool {
	s.mu.Lock()
	if len(s.pending) == 0 {
		s.mu.Unlock()
		return true
	}
	work := s.pending
	s.pending = map[string]any{}
	s.mu.Unlock()

	// The gate is checked again here, not only when the command was
	// queued: a replica that lost leadership during an outage must not
	// send what it queued while it led, nor what a broken flush put back,
	// nor a re-assert queued just before the gate closed. It is checked
	// again before every bit RMW, register run and coil run, so a long
	// flush stops once the gate closes. Dropped commands are forgotten
	// (not recorded as written), so the next WriteOutputs push after the
	// gate reopens commands the same value again. A drop is not a failure:
	// no error is counted and __Online is left to the reads.
	drop := func(names []string) {
		s.mu.Lock()
		for _, n := range names {
			// Forget only what this flush dropped: a tag WriteOutputs has
			// queued again since keeps the written value that command
			// recorded.
			if _, again := s.pending[n]; !again && sameScalar(s.written[n], work[n]) {
				delete(s.written, n)
			}
		}
		s.mu.Unlock()
		d.log.Warn("modbus: write gate closed; queued writes dropped", "source", s.cfg.ID, "count", len(names))
	}
	if !d.gateOpen() {
		names := make([]string, 0, len(work))
		for n := range work {
			names = append(names, n)
		}
		drop(names)
		return true
	}

	regWrites, coilWrites, bitWrites := d.stageWrites(s, work)
	regRuns, coilRuns := coalesceRegs(regWrites), coalesceCoils(coilWrites)

	// rest names the write at bi/ri/ci and every one after it, in send
	// order: bit RMWs from bi, register runs from ri, coil runs from ci.
	rest := func(bi, ri, ci int) []string {
		var names []string
		for _, bw := range bitWrites[bi:] {
			names = append(names, bw.names...)
		}
		for _, run := range regRuns[ri:] {
			names = append(names, run.names...)
		}
		for _, run := range coilRuns[ci:] {
			names = append(names, run.names...)
		}
		return names
	}

	// requeue puts back the write that broke and every one after it. A tag
	// queued again while this flush was in flight keeps that newer value
	// (last value wins).
	requeue := func(bi, ri, ci int) {
		s.mu.Lock()
		for _, n := range rest(bi, ri, ci) {
			if _, exists := s.pending[n]; !exists {
				s.pending[n] = work[n]
			}
		}
		s.mu.Unlock()
	}

	// gateClosed drops the write at bi/ri/ci and every one after it when
	// the gate has closed since the flush began. The flush returns as soon
	// as it does, so a drop is logged at most once per flush.
	gateClosed := func(bi, ri, ci int) bool {
		if d.gateOpen() {
			return false
		}
		drop(rest(bi, ri, ci))
		return true
	}

	// bit:N writables read-modify-write their register one at a time — the
	// other 15 bits belong to the device (or to sibling bindings).
	for i, bw := range bitWrites {
		if gateClosed(i, 0, 0) {
			return true
		}
		cur, err := readRegisters(ctx, c, s.cfg.UnitID, FCReadHoldingRegisters, bw.addr, 1)
		if err == nil {
			// Merge in normalized (big-endian) space, then restore the
			// source's wire order — wireOrder is its own inverse.
			w := wireOrder(cur, s.cfg.WordOrder, s.cfg.ByteOrder)[0]
			w = setBit(w, bw.bit, bw.on)
			out := wireOrder([]uint16{w}, s.cfg.WordOrder, s.cfg.ByteOrder)[0]
			err = writeSingleRegister(ctx, c, s.cfg.UnitID, bw.addr, out)
		}
		if !d.wroteOK(ctx, s, work, bw.names, err) {
			requeue(i, 0, 0)
			return false
		}
	}

	for i, run := range regRuns {
		if gateClosed(len(bitWrites), i, 0) {
			return true
		}
		var err error
		if len(run.values) == 1 {
			err = writeSingleRegister(ctx, c, s.cfg.UnitID, run.addr, run.values[0])
		} else {
			err = writeMultipleRegisters(ctx, c, s.cfg.UnitID, run.addr, run.values)
		}
		if !d.wroteOK(ctx, s, work, run.names, err) {
			requeue(len(bitWrites), i, 0)
			return false
		}
	}
	for i, run := range coilRuns {
		if gateClosed(len(bitWrites), len(regRuns), i) {
			return true
		}
		var err error
		if len(run.values) == 1 {
			err = writeSingleCoil(ctx, c, s.cfg.UnitID, run.addr, run.values[0])
		} else {
			err = writeMultipleCoils(ctx, c, s.cfg.UnitID, run.addr, run.values)
		}
		if !d.wroteOK(ctx, s, work, run.names, err) {
			requeue(len(bitWrites), len(regRuns), i)
			return false
		}
	}
	return true
}

// wroteOK accounts one device write. An exception is an answer on a
// healthy connection, so the write is never re-queued (that would loop),
// and what follows depends on who answered:
//
//   - 0x0A/0x0B (gatewayNoAnswer) and 0x06 (busy): the write did not run.
//     The gateway answered for a device that never got the command (#596),
//     or the device was busy and did not carry it out (#602). The write is
//     dropped from the queue and its tags are forgotten from written, so
//     the next WriteOutputs sends the program's value then, changed or
//     not: no replay of the value sent here, and no backoff of its own. A
//     rewrite: binding is not forgotten: its re-assert sends written again
//     on its period, and forgetting it would stop the keep-alive until the
//     program changed it. As for a gate drop, a tag is forgotten only while
//     written still holds the value this write carried (work) and the tag
//     is not queued again: a newer command keeps its on-change reference.
//     For a write-only source a gateway no-answer counts toward going
//     offline, once per write (#550); 0x06 is the device answering.
//   - Any other exception: the device refused THIS write, or (0x04) failed
//     partway and a re-send could fire a write-to-trigger register twice.
//     It is dropped and stays in written, so the same value is not sent
//     again.
//
// A transport error means reconnect: false, and the caller re-queues this
// write and everything not yet attempted.
func (d *Driver) wroteOK(ctx context.Context, s *source, work map[string]any, names []string, err error) bool {
	if err == nil {
		d.writes.Add(1)
		// A confirmed write is the device answering, as a read is: a
		// write-only source (a command or keep-alive word) goes online and
		// counts its outages too, and its backoff ladder resets.
		s.mu.Lock()
		s.writeAnsweredLocked()
		s.mu.Unlock()
		return true
	}
	d.errs.Add(1)
	var exc ExceptionError
	if errors.As(err, &exc) {
		s.mu.Lock()
		s.lastErr = fmt.Errorf("write %v: %w", names, err)
		s.excs++
		notRun := writeNotRun(err)
		if gatewayNoAnswer(err) {
			s.writeNoAnswerLocked(ctx.Err() == nil)
		} else {
			s.writeAnsweredLocked() // the device answered (busy, or refused)
		}
		if notRun {
			for _, n := range names {
				if d.byName[n].b.Rewrite > 0 {
					continue
				}
				// The gate drop's rule: forget only the value this write
				// carried, and not a tag WriteOutputs has queued again.
				if _, again := s.pending[n]; !again && sameScalar(s.written[n], work[n]) {
					delete(s.written, n)
				}
			}
		}
		s.mu.Unlock()
		if notRun {
			d.log.Warn("modbus: write not delivered; will resend", "source", s.cfg.ID, "tags", names, "error", err)
		} else {
			d.log.Warn("modbus: write refused", "source", s.cfg.ID, "tags", names, "error", err)
		}
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

// heardLocked records an answer from the device: the source is online
// and the backoff ladder resets. Caller holds s.mu.
func (s *source) heardLocked() {
	s.online = true
	s.answered = true
	s.gwMisses = 0
}

// blockAnsweredLocked records an answer to br's read (data, or an
// exception other than a gateway no-answer). Caller holds s.mu.
func (s *source) blockAnsweredLocked(br *blockRun) {
	if br.gwFail {
		br.gwFail = false
		s.gwFailed--
	}
	s.heardLocked()
}

// blockNoAnswerLocked records a gateway no-answer (0x0A/0x0B) to br's
// read. Once every block's latest read got one, nothing behind the gateway
// answers: the source goes offline, an outage when outage is true and it
// was online. Caller holds s.mu.
func (s *source) blockNoAnswerLocked(br *blockRun, outage bool) {
	if !br.gwFail {
		br.gwFail = true
		s.gwFailed++
	}
	s.gwMisses++
	if s.gwFailed == len(s.blocks) && s.gwMisses >= gatewayMissesToOffline {
		s.goOfflineLocked(outage)
	}
}

// writeAnsweredLocked records an answer to a write. Only a source with
// nothing polled goes by it; for a polled source it resets the backoff
// ladder and nothing else, since reads decide __Online. Caller holds s.mu.
func (s *source) writeAnsweredLocked() {
	if len(s.blocks) > 0 {
		s.answered = true
		return
	}
	s.heardLocked()
}

// writeNoAnswerLocked records a gateway no-answer to a write: for a source
// with nothing polled, gatewayMissesToOffline in a row take it offline.
// Caller holds s.mu.
func (s *source) writeNoAnswerLocked(outage bool) {
	if len(s.blocks) > 0 {
		return
	}
	s.gwMisses++
	if s.gwMisses >= gatewayMissesToOffline {
		s.goOfflineLocked(outage)
	}
}

// goOfflineLocked takes the source offline, counting an outage
// (__Outages) when outage is true and the source was online. Caller holds
// s.mu.
func (s *source) goOfflineLocked(outage bool) {
	if s.online && outage {
		s.outages++
	}
	s.online = false
}

// Interface conformance — the same trio io.Memory proves.
var (
	_ nio.BatchReader     = (*Driver)(nil)
	_ nio.QualityReporter = (*Driver)(nil)
)
