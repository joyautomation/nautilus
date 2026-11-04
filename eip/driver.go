package eip

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/joyautomation/nautilus/eip/logix"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
)

// Driver polls a Logix controller and implements io.Driver. It owns a
// background connection loop: dial → validate manifest against the live
// controller → poll at ScanRate → reconnect with backoff on transport errors.
// ReadInputs never blocks on the network; it returns the latest snapshot.
// WriteOutputs enqueues changed values; a writer goroutine pushes them to the
// controller (latest value wins per tag).
type Driver struct {
	host     string
	port     int
	slot     int
	scanRate time.Duration
	log      *slog.Logger
	manifest Manifest

	// Scan-class configuration collected from options, resolved in New.
	classRates  map[string]time.Duration
	assignments []classAssignment
	classes     []*scanClass

	defs   map[string]*ir.StructDef // manifest type name -> StructDef
	inputs []TagBinding
	byName map[string]TagBinding // writable bindings by nautilus tag name

	// leafMode marks struct bindings whose root reads the controller refuses
	// (AOI access restrictions); they poll member-by-member instead. Touched
	// only from the run goroutine; Health reads leafModeN (under mu) instead.
	leafMode  map[string]bool
	leafCache map[string][]leafDesc
	// deadLeaves records members the controller permanently refuses to serve
	// (internal words like TIMER.Control). They read as zero.
	deadLeaves map[string]map[string]bool

	mu        sync.Mutex
	snapshot  nio.Values // latest polled values (nautilus tag name -> value)
	connected bool
	// primed is set once every scan class has completed a poll on the
	// current connection, and cleared when the connection changes. The
	// snapshot fills tag by tag during a poll, so before this a reader can
	// see some of the first poll's tags and not the rest.
	primed bool
	// leafModeN is len(leafMode), kept under mu for Health: the map itself
	// belongs to the run goroutine, and Health is called from others (a
	// Sparkplug device's health check, every tick).
	leafModeN int
	lastErr   error
	stats     driverStats

	wmu     sync.Mutex
	pending map[string]any // queued writes (nautilus tag name -> desired value)
	written map[string]any // last value successfully written (for on-change)
	// term counts the write gate's reopens (guarded by wmu). A flush notes
	// it when it takes the queue and records, re-queues or forgets only
	// while it is unchanged: a reopen in between has forgotten the
	// previous term, and what that flush sent belongs to it.
	term  uint64
	wkick chan struct{}

	// gateMu guards writeGate (SetWriteGate). gateShut is true from the
	// first look that finds the gate closed to the first that finds it
	// open again (see gateOpen), and turnMu serializes those transitions.
	// wanted is the request OutputsWanted answers.
	gateMu    sync.Mutex
	writeGate func() bool
	gateShut  atomic.Bool
	turnMu    sync.Mutex
	wanted    atomic.Bool

	cancel context.CancelFunc
	done   chan struct{}

	// onlineTag is the synthesized "<name>__Online" companion (WithOnlineTag),
	// "" when the driver was built without one. started is set by Start:
	// from then on ReadInputs delivers rather than errors (see ReadInputs).
	onlineTag string
	started   atomic.Bool
}

// Option configures New.
type Option func(*Driver)

// WithSlot sets the processor backplane slot (default 0).
func WithSlot(s int) Option { return func(d *Driver) { d.slot = s } }

// WithPort overrides the EtherNet/IP TCP port (default 44818).
func WithPort(p int) Option { return func(d *Driver) { d.port = p } }

// WithScanRate sets the default scan class's poll interval (default 250ms).
// This is the I/O update rate, independent of the runtime's program scan
// interval — the same split a PLC makes between I/O update and logic scan.
func WithScanRate(r time.Duration) Option { return func(d *Driver) { d.scanRate = r } }

// WithLogger sets the structured logger.
func WithLogger(l *slog.Logger) Option { return func(d *Driver) { d.log = l } }

// WithOnlineTag names the driver's synthesized BOOL connection-status input,
// conventionally "<driver name>__Online" (the manifest loader passes
// exactly that: `eip__Online` for an unnamed driver). It is true while the
// controller is connected and every scan class has completed a poll on this
// connection, false otherwise — before the first connect, while
// reconnecting, and between a reconnect and its first full read. It is the
// tag a program checks before trusting this controller's values, the way
// Logix logic checks a connection's fault bit; modbus's
// "<source>__Online" and the Sparkplug host's "<site>__Online" are the
// same idea. The name must not collide with a binding.
func WithOnlineTag(name string) Option { return func(d *Driver) { d.onlineTag = name } }

// DefaultClass is the scan class of every binding that isn't assigned one.
// Its rate is WithScanRate.
const DefaultClass = "default"

// NoPoll is the reserved scan class for tags that stay in the catalog but
// are never polled. They remain valid write targets — a command tag the
// program only ever writes belongs here.
const NoPoll = "none"

// classAssignment maps glob patterns to a scan class, applied in option
// order (the last matching assignment wins).
type classAssignment struct {
	class    string
	patterns []string
}

// scanClass is one resolved poll group: its own interval over the shared
// connection.
type scanClass struct {
	name     string
	rate     time.Duration
	bindings []TagBinding
	next     time.Time
}

// WithScanClass defines (or redefines) a scan class and its poll interval.
// The default class exists implicitly; NoPoll cannot be given a rate.
func WithScanClass(name string, rate time.Duration) Option {
	return func(d *Driver) { d.classRates[name] = rate }
}

// WithTagClass assigns tags to a scan class by glob patterns matched against
// the binding's nautilus name and its device path ("Line1_PIT_*",
// "Program:MainProgram.*"). Assignments live in the driver constructor — not
// the generated manifest — so re-running `naut eip import` never erases
// polling policy. Later assignments override earlier ones.
func WithTagClass(class string, patterns ...string) Option {
	return func(d *Driver) {
		d.assignments = append(d.assignments, classAssignment{class: class, patterns: patterns})
	}
}

// New builds the driver. Call Start to begin polling.
func New(host string, m Manifest, opts ...Option) (*Driver, error) {
	d := &Driver{
		host:       host,
		port:       0,
		slot:       0,
		scanRate:   250 * time.Millisecond,
		log:        slog.Default(),
		manifest:   m,
		snapshot:   nio.Values{},
		pending:    map[string]any{},
		written:    map[string]any{},
		wkick:      make(chan struct{}, 1),
		byName:     map[string]TagBinding{},
		classRates: map[string]time.Duration{},
		leafMode:   map[string]bool{},
		leafCache:  map[string][]leafDesc{},
		deadLeaves: map[string]map[string]bool{},
	}
	for _, o := range opts {
		o(d)
	}
	defs, err := m.structDefs()
	if err != nil {
		return nil, err
	}
	d.defs = defs
	seen := map[string]bool{}
	for _, b := range m.Tags {
		if b.Name == "" || b.Device == "" || b.Type == "" {
			return nil, fmt.Errorf("eip: binding %+v missing Name/Device/Type", b)
		}
		if seen[b.Name] {
			return nil, fmt.Errorf("eip: duplicate binding name %q", b.Name)
		}
		seen[b.Name] = true
		if _, isElem := elementaryCode(b.Type); !isElem && b.Type != "STRING" {
			if _, ok := d.defs[b.Type]; !ok {
				return nil, fmt.Errorf("eip: binding %q references unknown type %q", b.Name, b.Type)
			}
		}
		d.inputs = append(d.inputs, b)
		if b.Writable {
			d.byName[b.Name] = b
		}
	}
	if d.onlineTag != "" && seen[d.onlineTag] {
		return nil, fmt.Errorf("eip: companion tag %q collides with a binding of the same name", d.onlineTag)
	}
	if err := d.resolveClasses(); err != nil {
		return nil, err
	}
	return d, nil
}

// resolveClasses partitions bindings into scan classes: the manifest's
// ScanClass is the base, WithTagClass assignments override in order, and
// everything else lands in the default class.
func (d *Driver) resolveClasses() error {
	if _, ok := d.classRates[DefaultClass]; !ok {
		d.classRates[DefaultClass] = d.scanRate
	}
	if rate, ok := d.classRates[NoPoll]; ok {
		return fmt.Errorf("eip: scan class %q is reserved and cannot have a rate (%v)", NoPoll, rate)
	}
	for _, a := range d.assignments {
		if a.class != NoPoll {
			if _, ok := d.classRates[a.class]; !ok {
				return fmt.Errorf("eip: WithTagClass(%q, ...) references an undefined scan class — add WithScanClass(%q, rate)", a.class, a.class)
			}
		}
	}

	byClass := map[string][]TagBinding{}
	for _, b := range d.inputs {
		class := b.ScanClass
		if class == "" {
			class = DefaultClass
		}
		for _, a := range d.assignments {
			for _, p := range a.patterns {
				nameOK, _ := path.Match(p, b.Name)
				devOK, _ := path.Match(p, b.Device)
				if nameOK || devOK {
					class = a.class
					break
				}
			}
		}
		if class == NoPoll {
			continue
		}
		if _, ok := d.classRates[class]; !ok {
			return fmt.Errorf("eip: binding %q names undefined scan class %q — add WithScanClass(%q, rate)", b.Name, class, class)
		}
		byClass[class] = append(byClass[class], b)
	}

	names := make([]string, 0, len(byClass))
	for n := range byClass {
		names = append(names, n)
	}
	sort.Strings(names)
	d.classes = nil
	for _, n := range names {
		rate := d.classRates[n]
		if rate <= 0 {
			return fmt.Errorf("eip: scan class %q has non-positive rate %v", n, rate)
		}
		d.classes = append(d.classes, &scanClass{name: n, rate: rate, bindings: byClass[n]})
	}
	return nil
}

// ScanClasses reports the resolved poll groups — for diagnostics and tests.
func (d *Driver) ScanClasses() map[string][]string {
	out := make(map[string][]string, len(d.classes))
	for _, c := range d.classes {
		names := make([]string, 0, len(c.bindings))
		for _, b := range c.bindings {
			names = append(names, b.Name)
		}
		sort.Strings(names)
		out[c.name] = names
	}
	return out
}

// InputNames returns the nautilus tag names of all polled bindings —
// convenient for runtime.Options.Inputs. NoPoll bindings never produce
// values, so they aren't inputs.
func (d *Driver) InputNames() []string {
	var out []string
	for _, c := range d.classes {
		for _, b := range c.bindings {
			out = append(out, b.Name)
		}
	}
	if d.onlineTag != "" {
		out = append(out, d.onlineTag)
	}
	sort.Strings(out)
	return out
}

// InputType names the IEC type an input delivers — for a manifest tag with
// no type: that no program declares, so it can still start at the zero of
// the right type (runtime.TagDef.DriverType). An elementary Logix type is
// its IEC namesake, a UDT is the TYPE `naut eip import` generated for it,
// an array is ARRAY[0..n-1] OF either, and the online companion is BOOL.
// "" for a name the driver does not deliver or a Logix type IEC has no
// spelling for.
func (d *Driver) InputType(name string) string {
	if name != "" && name == d.onlineTag {
		return "BOOL"
	}
	for _, b := range d.manifest.Tags {
		if b.Name != name {
			continue
		}
		elem := b.Type
		switch b.Type {
		case "BOOL", "SINT", "INT", "DINT", "LINT", "USINT", "UINT", "UDINT", "ULINT",
			"REAL", "LREAL", "BYTE", "WORD", "DWORD", "LWORD", "STRING":
		default:
			if _, isElem := elementaryCode(b.Type); isElem {
				return "" // a Logix-only elementary (STIME, DATE, …)
			}
			if _, ok := d.defs[b.Type]; !ok {
				return ""
			}
		}
		if b.ArrayLen > 0 {
			return fmt.Sprintf("ARRAY[0..%d] OF %s", b.ArrayLen-1, elem)
		}
		return elem
	}
	return ""
}

// OutputNames returns the nautilus tag names of writable bindings — for
// runtime.Options.Outputs.
func (d *Driver) OutputNames() []string {
	out := make([]string, 0, len(d.byName))
	for n := range d.byName {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Start launches the connection/poll loop. It returns immediately; use
// Health to observe connection state.
func (d *Driver) Start(ctx context.Context) {
	ctx, d.cancel = context.WithCancel(ctx)
	d.done = make(chan struct{})
	d.started.Store(true)
	go d.run(ctx)
}

// Stop tears the driver down.
func (d *Driver) Stop() {
	if d.cancel != nil {
		d.cancel()
		<-d.done
	}
}

// driverStats accumulates the poll-loop counters Health reports. Guarded by
// d.mu (poll counters are bumped under the lock alongside the snapshot).
type driverStats struct {
	polls      uint64
	pollErrors uint64
	reconnects uint64
	sinceMs    int64 // ms epoch of the last connect/disconnect transition
	lastPollMs int64 // ms epoch of the last successful poll
}

// Health reports connection state and poll-loop counters for diagnostics and
// HMI driver-status cards. The first two fields are the original contract;
// the rest are additive.
type Health struct {
	Connected bool   `json:"connected"`
	LastError string `json:"lastError,omitempty"`

	Host string `json:"host"`
	Slot int    `json:"slot"`

	Tags       int    `json:"tags"`       // input bindings polled
	LeafMode   int    `json:"leafMode"`   // struct bindings read member-wise
	Polls      uint64 `json:"polls"`      // successful poll cycles
	PollErrors uint64 `json:"pollErrors"` // failed poll cycles
	Reconnects uint64 `json:"reconnects"` // transport reconnects
	SinceMs    int64  `json:"sinceMs"`    // last connect/disconnect (epoch ms)
	LastPollMs int64  `json:"lastPollMs"` // last successful poll (epoch ms)
	// Primed: every scan class has completed a poll since this connection
	// came up — the snapshot holds a whole read, not the first part of one.
	// A tag that failed its read does not hold this back.
	Primed bool `json:"primed"`
}

// Health returns the current connection health and poll counters.
func (d *Driver) Health() Health {
	d.mu.Lock()
	defer d.mu.Unlock()
	h := Health{
		Connected:  d.connected,
		Primed:     d.primed,
		Host:       d.host,
		Slot:       d.slot,
		Tags:       len(d.inputs),
		LeafMode:   d.leafModeN,
		Polls:      d.stats.polls,
		PollErrors: d.stats.pollErrors,
		Reconnects: d.stats.reconnects,
		SinceMs:    d.stats.sinceMs,
		LastPollMs: d.stats.lastPollMs,
	}
	if d.lastErr != nil {
		h.LastError = d.lastErr.Error()
	}
	return h
}

// ── io.Driver ────────────────────────────────────────────────────────────

// ReadInputs returns the latest polled snapshot, plus the online companion
// when the driver has one (WithOnlineTag).
//
// Before Start it errors ("not connected yet"), so a driver that was wired
// up and never started fails loudly instead of scanning on nothing — the
// Sparkplug host's and modbus's rule. Once started it never errors: a
// controller that is down delivers what it last delivered (nothing, before
// the first connect) with the companion false, and the runtime's per-tag
// quality comes from Quality below. It used to error until the first
// connect, which in a drivers: set failed the WHOLE merged read (io.Multi)
// and so starved every other driver's inputs — the local I/O of a project
// whose Logix partner happened to be down at boot stopped with it.
func (d *Driver) ReadInputs() (nio.Values, error) {
	if !d.started.Load() {
		d.mu.Lock()
		defer d.mu.Unlock()
		if d.lastErr != nil {
			return nil, d.lastErr
		}
		return nil, fmt.Errorf("eip: %s not connected yet", d.host)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make(nio.Values, len(d.snapshot)+1)
	for k, v := range d.snapshot {
		out[k] = v
	}
	if d.onlineTag != "" {
		out[d.onlineTag] = d.connected && d.primed
	}
	return out, nil
}

// Quality reports, per tag, the non-Good verdicts (io.QualityReporter): a
// binding never delivered is NotConnected, and one delivered while the
// controller is not connected now is Stale — its last value, held. The
// companion is the driver's own truth and always Good. Before this the
// runtime could only infer staleness from a failed read, which this driver
// stopped reporting once it had delivered anything: a controller that had
// dropped read Good on every tag it had ever polled.
func (d *Driver) Quality() map[string]nio.Quality {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out map[string]nio.Quality
	for _, c := range d.classes {
		for _, b := range c.bindings {
			_, delivered := d.snapshot[b.Name]
			var q nio.Quality
			switch {
			case !delivered:
				q = nio.NotConnected
			case !d.connected:
				q = nio.Stale
			default:
				continue
			}
			if out == nil {
				out = map[string]nio.Quality{}
			}
			out[b.Name] = q
		}
	}
	return out
}

var _ nio.QualityReporter = (*Driver)(nil)

// WriteOutputs queues changed values for the writer goroutine. Unknown or
// non-writable names are ignored (the runtime hands us exactly the Outputs
// list it was configured with). While the write gate is closed nothing is
// queued (see SetWriteGate).
func (d *Driver) WriteOutputs(vals nio.Values) error {
	if !d.gateOpen() {
		return nil
	}
	d.wmu.Lock()
	changed := false
	for name, v := range vals {
		if _, ok := d.byName[name]; !ok {
			continue
		}
		if prev, ok := d.written[name]; ok && equalValue(prev, v) {
			continue
		}
		if prev, ok := d.pending[name]; ok && equalValue(prev, v) {
			continue
		}
		d.pending[name] = v
		changed = true
	}
	d.wmu.Unlock()
	if changed {
		select {
		case d.wkick <- struct{}{}:
		default:
		}
	}
	return nil
}

// ── write gate ───────────────────────────────────────────────────────

// SetWriteGate installs the write gate: commands leave the driver only
// while gate() is true. `naut run` wires it to redundancy leadership
// (leader.Elector.IsLeader); io.Multi fans it out. Reads are unaffected.
//
// The gate is checked when a command is handed over (WriteOutputs) and
// again when the queue is flushed, before every tag and every member
// write of a struct: a replica that queued commands as leader while the
// controller was down, or that a broken flush re-queued, must not send
// them once the controller is back if it has lost leadership meanwhile
// (#607, the Modbus driver's #595). A closed gate drops the queue, and the
// rest of a flush it closes partway through; a dropped command is
// forgotten and asked for again (OutputsWanted).
//
// While the gate is closed another replica may lead and write the same
// tags, so nothing this driver wrote is trusted to still be there: the
// first look that finds the gate open again forgets every written value
// (and anything still queued from the previous term) and asks the runtime
// for the outputs, so the next push sends every output with the
// program's value, changed or not (#605, the Modbus driver's #600). A
// struct then goes out member by member in full. The driver has no
// start-up baseline, so a replica that started as a standby commands
// every output on its first takeover too (#606).
//
// A func cannot say it changed, so the driver sees the gate close and
// reopen by looking at it (gateOpen): on every WriteOutputs and flush, on
// every connect attempt, and at least once a second otherwise, while
// connected and while waiting out the reconnect backoff. A gate that
// closes and reopens between two looks is not seen: one shorter than
// about a second, or than one connect attempt while the controller is
// down (the dial gives up after 10 s; the browse and validation that
// follow it on a controller that answers are not timed).
func (d *Driver) SetWriteGate(gate func() bool) {
	d.gateMu.Lock()
	d.writeGate = gate
	d.gateMu.Unlock()
}

// gateOpen reports whether commands may leave the driver right now. Every
// call is also a look at the gate (see SetWriteGate): the first look that
// finds it open again after one found it closed forgets every written and
// queued value and asks for the outputs. Callers do not hold wmu.
func (d *Driver) gateOpen() bool {
	d.gateMu.Lock()
	gate := d.writeGate
	d.gateMu.Unlock()
	if gate == nil {
		return true
	}
	open := gate()
	// Steady state is one atomic load. A transition is made under turnMu,
	// and gateShut is cleared only AFTER the forget: a second looker that
	// finds the gate open meanwhile still sees gateShut, waits on turnMu,
	// and so cannot queue against the previous term's written.
	if shut := d.gateShut.Load(); shut == open {
		d.turnMu.Lock()
		switch {
		case !open && !d.gateShut.Load():
			d.gateShut.Store(true)
		case open && d.gateShut.Load():
			d.forgetWritten()
			d.gateShut.Store(false)
			d.wanted.Store(true)
			d.log.Info("eip: write gate open again; every output goes out with the next push", "host", d.host)
		}
		d.turnMu.Unlock()
	}
	return open
}

// forgetWritten empties written and pending and starts a new term: no
// value is the on-change reference any more, and no command from the
// previous term is sent in this one.
func (d *Driver) forgetWritten() {
	d.wmu.Lock()
	clear(d.written)
	clear(d.pending)
	d.term++
	d.wmu.Unlock()
}

// OutputsWanted is the driver's io.OutputsRequester: true once after it
// forgot commands (the write gate dropped them, or reopened), so the
// runtime hands it every output on the next scan.
func (d *Driver) OutputsWanted() bool {
	return d.wanted.Load() && d.wanted.Swap(false)
}

var (
	_ nio.OutputsRequester                   = (*Driver)(nil)
	_ interface{ SetWriteGate(func() bool) } = (*Driver)(nil)
)

// ── connection / poll loop ──────────────────────────────────────────────

type session struct {
	ctrl     *logix.Controller
	registry *logix.Registry
	browse   *logix.BrowseResult
	// liveType maps manifest type name -> live template (validated).
	liveType map[string]*logix.Template
}

func (d *Driver) run(ctx context.Context) {
	defer close(d.done)
	backoff := time.Second
	everConnected := false
	for ctx.Err() == nil {
		d.gateOpen() // a look (see SetWriteGate), on every connect attempt
		sess, err := d.connect(ctx)
		if err != nil {
			d.setErr(err)
			d.log.Warn("eip: connect failed", "host", d.host, "error", err, "retryIn", backoff)
			if !d.backoffWait(ctx, backoff) {
				return
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
		d.mu.Lock()
		d.connected, d.lastErr = true, nil
		d.primed = false
		d.stats.sinceMs = time.Now().UnixMilli()
		if everConnected {
			d.stats.reconnects++
		}
		everConnected = true
		d.mu.Unlock()
		d.log.Info("eip: connected", "host", d.host, "slot", d.slot, "tags", len(d.inputs))

		d.serve(ctx, sess)

		_ = sess.ctrl.Close()
		d.mu.Lock()
		d.connected = false
		d.primed = false
		d.stats.sinceMs = time.Now().UnixMilli()
		d.mu.Unlock()
	}
}

// gateLookEvery bounds the time between two looks at the write gate while
// nothing else looks sooner: the serve loop's idle wake and the reconnect
// backoff (see SetWriteGate).
const gateLookEvery = time.Second

// backoffWait waits wait between connect attempts, looking at the write
// gate at least every gateLookEvery meanwhile, so a loss and regain of
// leadership during a long backoff is seen. False when ctx ended.
func (d *Driver) backoffWait(ctx context.Context, wait time.Duration) bool {
	t := time.NewTimer(wait)
	defer t.Stop()
	look := time.NewTicker(gateLookEvery)
	defer look.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-t.C:
			return true
		case <-look.C:
			d.gateOpen() // see SetWriteGate
		}
	}
}

// connect dials, browses, and validates the manifest against the live
// controller.
func (d *Driver) connect(ctx context.Context) (*session, error) {
	opts := []logix.Option{logix.WithSlot(d.slot), logix.WithLogger(d.log)}
	if d.port != 0 {
		opts = append(opts, logix.WithPort(d.port))
	}
	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ctrl, err := logix.Dial(dialCtx, d.host, opts...)
	if err != nil {
		return nil, err
	}
	browse, err := ctrl.Browse(ctx)
	if err != nil {
		_ = ctrl.Close()
		return nil, fmt.Errorf("eip: browse %s: %w", d.host, err)
	}
	sess := &session{
		ctrl:     ctrl,
		registry: logix.NewRegistry(browse.Templates),
		browse:   browse,
		liveType: map[string]*logix.Template{},
	}
	if err := d.validate(sess); err != nil {
		_ = ctrl.Close()
		return nil, err
	}
	return sess, nil
}

// validate checks every binding against the live tag list and every manifest
// type against the live template. Drift fails the connection with a message
// pointing at re-import.
func (d *Driver) validate(sess *session) error {
	symbols := map[string]logix.Symbol{}
	for _, s := range sess.browse.Symbols {
		symbols[strings.ToUpper(s.Name)] = s
	}
	var problems []string
	for _, b := range d.inputs {
		sym, ok := symbols[strings.ToUpper(b.Device)]
		if !ok {
			// Member paths ("Motor.Cfg") and array elements bind below a
			// symbol; validate their root exists.
			root := b.Device
			if i := strings.IndexAny(root, ".["); i > 0 && !strings.HasPrefix(root, "Program:") {
				root = root[:i]
			} else if strings.HasPrefix(root, "Program:") {
				if j := strings.IndexAny(root[strings.Index(root, ".")+1:], ".["); j >= 0 {
					root = root[:strings.Index(root, ".")+1+j]
				}
			}
			if _, ok := symbols[strings.ToUpper(root)]; !ok {
				problems = append(problems, fmt.Sprintf("tag %q not found on controller", b.Device))
			}
			continue
		}
		if _, isElem := elementaryCode(b.Type); isElem || b.Type == "STRING" {
			continue
		}
		tmpl, ok := sess.browse.Templates[sym.TemplateID()]
		if !ok {
			problems = append(problems, fmt.Sprintf("tag %q: template 0x%x not uploaded", b.Device, sym.TemplateID()))
			continue
		}
		if tmpl.Name != b.Type {
			problems = append(problems, fmt.Sprintf("tag %q is %s on the controller, manifest says %s", b.Device, tmpl.Name, b.Type))
		}
	}
	for _, td := range d.manifest.Types {
		tmpl, ok := sess.browse.TemplateByName(td.Name)
		if !ok {
			problems = append(problems, fmt.Sprintf("type %q not found on controller", td.Name))
			continue
		}
		live := map[string]logix.Member{}
		for _, m := range tmpl.VisibleMembers() {
			live[m.Name] = m
		}
		for _, f := range td.Fields {
			if _, ok := live[f.deviceName()]; !ok {
				problems = append(problems, fmt.Sprintf("type %q lost member %q on the controller", td.Name, f.deviceName()))
			}
		}
		sess.liveType[td.Name] = tmpl
	}
	if len(problems) > 0 {
		return fmt.Errorf("eip: manifest does not match controller (re-run `naut eip import`):\n  %s",
			strings.Join(problems, "\n  "))
	}
	return nil
}

// serve polls until the connection breaks or ctx ends: each scan class runs
// at its own interval over the shared connection, and queued writes flush
// between polls (Controller serializes requests internally).
func (d *Driver) serve(ctx context.Context, sess *session) {
	// Prime every class immediately so the snapshot fills without waiting a
	// full slow-class interval.
	for _, c := range d.classes {
		if !d.pollClass(ctx, sess, c.bindings) {
			return
		}
		c.next = time.Now().Add(c.rate)
	}
	d.mu.Lock()
	d.primed = true
	d.mu.Unlock()
	for {
		// At least once a second, so the write gate is looked at (see
		// SetWriteGate) when no class is due for longer than that.
		wake := gateLookEvery
		now := time.Now()
		for _, c := range d.classes {
			if until := c.next.Sub(now); until < wake {
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
		case <-d.wkick:
			timer.Stop()
			if !d.flushWrites(ctx, sess) {
				return
			}
		case <-timer.C:
			d.gateOpen() // a look, even with nothing queued
			if !d.flushWrites(ctx, sess) {
				return
			}
			now := time.Now()
			for _, c := range d.classes {
				if c.next.After(now) {
					continue
				}
				if !d.pollClass(ctx, sess, c.bindings) {
					return
				}
				c.next = time.Now().Add(c.rate)
			}
		}
	}
}

// pollClass reads one scan class's bindings and refreshes the snapshot.
// Returns false when the connection is broken.
func (d *Driver) pollClass(ctx context.Context, sess *session, bindings []TagBinding) (ok bool) {
	defer func() {
		d.mu.Lock()
		if ok {
			d.stats.polls++
			d.stats.lastPollMs = time.Now().UnixMilli()
		} else {
			d.stats.pollErrors++
		}
		d.mu.Unlock()
	}()
	// Batch elementary scalars through Multiple Service Packets.
	var batchTags []string
	batchBind := map[string]TagBinding{}
	sizes := map[string]int{}
	for _, b := range bindings {
		code, isElem := elementaryCode(b.Type)
		if !isElem || b.ArrayLen > 0 {
			continue
		}
		if t, ok := logix.TypeByCode(code); ok {
			batchTags = append(batchTags, b.Device)
			batchBind[b.Device] = b
			sizes[b.Device] = t.Size
		}
	}
	if len(batchTags) > 0 {
		for _, r := range sess.ctrl.ReadTags(ctx, batchTags, func(tag string) int { return sizes[tag] }) {
			b := batchBind[r.Tag]
			if r.Err != nil {
				d.tagError(b, r.Err)
				if sess.ctrl.Broken() {
					return false
				}
				continue
			}
			d.storeRaw(sess, b, r.RawTag)
		}
	}

	// Structs, strings, and arrays read individually (fragmented as needed).
	// Struct roots the controller refuses (AOI access restrictions) drop to
	// leaf mode permanently for this session.
	var leafBindings []TagBinding
	for _, b := range bindings {
		_, isElem := elementaryCode(b.Type)
		if isElem && b.ArrayLen == 0 {
			continue
		}
		if d.leafMode[b.Name] {
			leafBindings = append(leafBindings, b)
			continue
		}
		count := uint16(1)
		if b.ArrayLen > 0 {
			count = uint16(b.ArrayLen)
		}
		raw, err := sess.ctrl.ReadTag(ctx, b.Device, count)
		if err != nil {
			if sess.ctrl.Broken() {
				d.tagError(b, err)
				return false
			}
			if !isElem && b.Type != "STRING" && b.ArrayLen == 0 {
				d.leafMode[b.Name] = true
				d.mu.Lock()
				d.leafModeN = len(d.leafMode)
				d.mu.Unlock()
				leafBindings = append(leafBindings, b)
				d.log.Info("eip: struct root read refused; switching to member reads",
					"tag", b.Name, "device", b.Device, "error", err)
				continue
			}
			d.tagError(b, err)
			continue
		}
		d.storeRaw(sess, b, raw)
	}
	return d.pollLeaves(ctx, sess, leafBindings)
}

// storeRaw decodes one read and publishes it into the snapshot.
func (d *Driver) storeRaw(sess *session, b TagBinding, raw logix.RawTag) {
	lv, err := sess.registry.Decode(raw)
	if err != nil {
		d.tagError(b, err)
		return
	}
	v, err := d.toIR(b.Type, b.ArrayLen, lv)
	if err != nil {
		d.tagError(b, err)
		return
	}
	d.mu.Lock()
	d.snapshot[b.Name] = v
	d.mu.Unlock()
}

func (d *Driver) tagError(b TagBinding, err error) {
	d.setErr(fmt.Errorf("%s (%s): %w", b.Name, b.Device, err))
	d.log.Warn("eip: tag read failed", "tag", b.Name, "device", b.Device, "error", err)
}

func (d *Driver) setErr(err error) {
	d.mu.Lock()
	d.lastErr = err
	d.mu.Unlock()
}

// errGateClosed stops a flush, or a struct write partway, when the write
// gate has closed; errNewTerm when it has reopened since the flush took
// its queue, whose commands that reopen forgot (they belong to the
// previous term; the push that answers the reopen sends this term's).
var (
	errGateClosed = errors.New("eip: write gate closed")
	errNewTerm    = errors.New("eip: write gate reopened")
)

// goOn looks at the gate before a write of the flush that took its queue
// in term: nil to send it, errGateClosed or errNewTerm to stop.
func (d *Driver) goOn(term uint64) error {
	if !d.gateOpen() {
		return errGateClosed
	}
	d.wmu.Lock()
	defer d.wmu.Unlock()
	if d.term != term {
		return errNewTerm
	}
	return nil
}

// flushWrites pushes pending writes to the controller, in tag-name order.
// Returns false when the connection broke.
//
// The gate is checked again here, not only when the command was queued: a
// replica that lost leadership during an outage must not send what it
// queued while it led, nor what a broken flush re-queued (#607). It is
// checked before every tag and every member write of a struct, so a long
// flush stops once the gate closes. Dropped commands are forgotten (not
// kept as written) and asked for (OutputsWanted), so the push after the
// gate reopens commands them again. A drop is not a failure: no error is
// recorded. A flush that finds the gate reopened since it took its queue
// sends none of the rest either: the reopen forgot them as the previous
// term's, and the push that answers it sends this term's values.
func (d *Driver) flushWrites(ctx context.Context, sess *session) bool {
	d.wmu.Lock()
	if len(d.pending) == 0 {
		d.wmu.Unlock()
		return true
	}
	work := d.pending
	d.pending = map[string]any{}
	term := d.term
	d.wmu.Unlock()
	names := make([]string, 0, len(work))
	for name := range work {
		names = append(names, name)
	}
	sort.Strings(names)

	// drop forgets what this flush dropped. A tag WriteOutputs has queued
	// again since is left to that command, and after a reopen (a new term)
	// nothing of this flush's is left to forget.
	drop := func(dropped []string) {
		d.wmu.Lock()
		if d.term == term {
			for _, n := range dropped {
				if _, again := d.pending[n]; !again {
					delete(d.written, n)
				}
			}
		}
		d.wmu.Unlock()
		d.wanted.Store(true) // the commands it dropped: ask for them
		d.log.Warn("eip: write gate closed; queued writes dropped", "host", d.host, "count", len(dropped))
	}

	ok := true
	for i, name := range names {
		v := work[name]
		if err := d.goOn(term); err != nil {
			if errors.Is(err, errGateClosed) {
				drop(names[i:])
			}
			return true
		}
		b := d.byName[name]
		if held, err := d.writeOne(ctx, sess, b, v, term); err != nil {
			if errors.Is(err, errGateClosed) {
				// Some of the struct's members may have gone out: forgetting
				// it makes the next write send every member.
				drop(names[i:])
				return true
			}
			if errors.Is(err, errNewTerm) {
				return true // the reopen forgot it, and the rest
			}
			d.setErr(fmt.Errorf("write %s: %w", name, err))
			d.log.Warn("eip: write failed", "tag", name, "device", b.Device, "error", err)
			if sess.ctrl.Broken() {
				// Requeue the write that broke and every one after it, in
				// send order, and retry after reconnect (#608). The writes
				// before it were answered, confirmed or refused: a re-send
				// would put back a value another client may have written
				// since. A struct the break cut partway keeps the members
				// the controller confirmed as written, so only the rest go
				// again. A tag queued again during the flight keeps its
				// newer value (last value wins). Not after a reopen: those
				// were the previous term's.
				d.wmu.Lock()
				if d.term == term {
					if held != nil {
						d.written[name] = held
					}
					for _, n := range names[i:] {
						if _, exists := d.pending[n]; !exists {
							d.pending[n] = work[n]
						}
					}
				}
				d.wmu.Unlock()
				ok = false
				break
			}
			continue
		}
		d.wmu.Lock()
		if d.term == term {
			d.written[name] = v
		}
		d.wmu.Unlock()
	}
	return ok
}

// writeOne writes a single nautilus value to the device: elementary scalars
// directly, struct values as per-leaf symbolic writes of the members that
// changed since the last write (all members on the first write), each
// after a look at the gate (goOn) for the flush that took it in term.
//
// When a struct write fails partway, held is what the controller is known
// to hold now: the members confirmed in this write at their new values,
// the rest as last written (unknown, and so sent again, where nothing was
// written before). It is nil for a scalar.
func (d *Driver) writeOne(ctx context.Context, sess *session, b TagBinding, v any, term uint64) (held any, err error) {
	if code, isElem := elementaryCode(b.Type); isElem && b.ArrayLen == 0 {
		data, err := logix.EncodeScalar(code, scalarOf(v))
		if err != nil {
			return nil, err
		}
		return nil, sess.ctrl.WriteTag(ctx, b.Device, code, 1, data)
	}
	iv, ok := v.(ir.Value)
	if !ok {
		return nil, fmt.Errorf("eip: compound tag needs ir.Value, got %T", v)
	}
	var prev *ir.Value
	d.wmu.Lock()
	if p, ok := d.written[b.Name].(ir.Value); ok {
		prev = &p
	}
	d.wmu.Unlock()
	td, ok := d.typeDef(b.Type)
	if !ok {
		return nil, fmt.Errorf("eip: cannot write type %q", b.Type)
	}
	h := heldFrom(iv, prev)
	if err := d.writeStructLeaves(ctx, sess, b.Device, td, iv, &h, term); err != nil {
		return h, err
	}
	return nil, nil
}

// heldFrom is a copy of v's shape holding, per elementary leaf, the value
// last written (prev) or, where there is none, the zero ir.Value, which
// equals nothing: what the controller is known to hold before a write of
// v. writeStructLeaves updates it as each member is confirmed.
func heldFrom(v ir.Value, prev *ir.Value) ir.Value {
	if v.Kind != ir.TypeStruct {
		if prev != nil {
			return *prev
		}
		return ir.Value{}
	}
	out := v
	out.Fld = make([]ir.Value, len(v.Fld))
	for i := range v.Fld {
		var p *ir.Value
		if prev != nil && prev.Kind == ir.TypeStruct && i < len(prev.Fld) {
			p = &prev.Fld[i]
		}
		out.Fld[i] = heldFrom(v.Fld[i], p)
	}
	return out
}

func (d *Driver) typeDef(name string) (TypeDef, bool) {
	for _, t := range d.manifest.Types {
		if t.Name == name {
			return t, true
		}
	}
	return TypeDef{}, false
}

// writeStructLeaves walks manifest fields, writing the elementary leaves
// that differ from held symbolically ("Device.Field.Sub"), and records each
// one the controller confirmed in held. Nested structs recurse; strings and
// arrays inside structs are read-only for now.
func (d *Driver) writeStructLeaves(ctx context.Context, sess *session, devPath string, td TypeDef, v ir.Value, held *ir.Value, term uint64) error {
	for i, f := range td.Fields {
		if i >= len(v.Fld) || i >= len(held.Fld) {
			break
		}
		fv := v.Fld[i]
		leaf := devPath + "." + f.deviceName()
		if code, isElem := elementaryCode(f.Type); isElem && f.ArrayLen == 0 {
			if scalarEqual(fv, held.Fld[i]) {
				continue
			}
			data, err := logix.EncodeScalar(code, plainScalar(fv))
			if err != nil {
				return fmt.Errorf("%s: %w", leaf, err)
			}
			if err := d.goOn(term); err != nil {
				return err
			}
			if err := sess.ctrl.WriteTag(ctx, leaf, code, 1, data); err != nil {
				return err
			}
			held.Fld[i] = fv
			continue
		}
		if sub, ok := d.typeDef(f.Type); ok && f.ArrayLen == 0 {
			if err := d.writeStructLeaves(ctx, sess, leaf, sub, fv, &held.Fld[i], term); err != nil {
				return err
			}
		}
		// Strings and array members: not written in v1.
	}
	return nil
}

// ── value conversion ─────────────────────────────────────────────────────

// toIR converts a decoded logix.Value into the runtime's ir.Value, shaped by
// the manifest type (field order follows the manifest/generated ST, values
// are matched by member name).
func (d *Driver) toIR(typeName string, arrayLen int, lv logix.Value) (any, error) {
	if arrayLen > 0 {
		if len(lv.Elems) == 0 {
			return nil, fmt.Errorf("expected array value for %s[%d]", typeName, arrayLen)
		}
		n := arrayLen
		if n > len(lv.Elems) {
			n = len(lv.Elems)
		}
		arr := make([]ir.Value, n)
		for i := 0; i < n; i++ {
			e, err := d.toIR(typeName, 0, lv.Elems[i])
			if err != nil {
				return nil, err
			}
			arr[i] = toValue(e)
		}
		return ir.Value{Kind: ir.TypeArray, Arr: arr}, nil
	}

	if _, isElem := elementaryCode(typeName); isElem || typeName == "STRING" {
		if lv.Scalar == nil {
			return nil, fmt.Errorf("expected scalar for %s, got %s", typeName, lv.Type)
		}
		return lv.Scalar, nil
	}

	// Struct: order fields per manifest, match by name.
	td, ok := d.typeDef(typeName)
	if !ok {
		return nil, fmt.Errorf("unknown manifest type %q", typeName)
	}
	sd := d.defs[typeName]
	byName := map[string]logix.Value{}
	for _, f := range lv.Fields {
		byName[f.Name] = f.Value
	}
	out := ir.Value{Kind: ir.TypeStruct, Struct: sd, Fld: make([]ir.Value, len(td.Fields))}
	for i, f := range td.Fields {
		fv, ok := byName[f.deviceName()]
		if !ok {
			return nil, fmt.Errorf("device value for %s missing member %s", typeName, f.deviceName())
		}
		conv, err := d.toIR(f.Type, f.ArrayLen, fv)
		if err != nil {
			return nil, err
		}
		out.Fld[i] = toValue(conv)
	}
	return out, nil
}

// toValue normalizes a conversion result (plain scalar or ir.Value) into an
// ir.Value.
func toValue(v any) ir.Value {
	switch x := v.(type) {
	case ir.Value:
		return x
	case bool:
		return ir.BoolVal(x)
	case int64:
		return ir.IntVal(x)
	case float64:
		return ir.RealVal(x)
	case string:
		return ir.StringVal(x)
	}
	return ir.Value{}
}

// scalarOf extracts a plain scalar from whatever the runtime handed us.
func scalarOf(v any) any {
	if iv, ok := v.(ir.Value); ok {
		return plainScalar(iv)
	}
	return v
}

func plainScalar(v ir.Value) any {
	switch v.Kind {
	case ir.TypeBool:
		return v.B
	case ir.TypeReal:
		return v.F
	case ir.TypeInt, ir.TypeTime:
		return v.I
	case ir.TypeString:
		return v.S
	}
	return nil
}

func scalarEqual(a, b ir.Value) bool {
	if a.Kind != b.Kind {
		return false
	}
	switch a.Kind {
	case ir.TypeBool:
		return a.B == b.B
	case ir.TypeReal:
		return a.F == b.F
	case ir.TypeInt, ir.TypeTime:
		return a.I == b.I
	case ir.TypeString:
		return a.S == b.S
	}
	return false
}

// equalValue compares two queued write values for on-change suppression.
func equalValue(a, b any) bool {
	av, aok := a.(ir.Value)
	bv, bok := b.(ir.Value)
	if aok != bok {
		return false
	}
	if !aok {
		return a == b
	}
	return irEqual(av, bv)
}

func irEqual(a, b ir.Value) bool {
	if a.Kind != b.Kind {
		return false
	}
	switch a.Kind {
	case ir.TypeStruct:
		if len(a.Fld) != len(b.Fld) {
			return false
		}
		for i := range a.Fld {
			if !irEqual(a.Fld[i], b.Fld[i]) {
				return false
			}
		}
		return true
	case ir.TypeArray:
		if len(a.Arr) != len(b.Arr) {
			return false
		}
		for i := range a.Arr {
			if !irEqual(a.Arr[i], b.Arr[i]) {
				return false
			}
		}
		return true
	default:
		return scalarEqual(a, b)
	}
}
