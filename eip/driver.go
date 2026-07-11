package eip

import (
	"context"
	"fmt"
	"log/slog"
	"path"
	"sort"
	"strings"
	"sync"
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
	// only from the run goroutine.
	leafMode  map[string]bool
	leafCache map[string][]leafDesc
	// deadLeaves records members the controller permanently refuses to serve
	// (internal words like TIMER.Control). They read as zero.
	deadLeaves map[string]map[string]bool

	mu        sync.Mutex
	snapshot  nio.Values // latest polled values (nautilus tag name -> value)
	connected bool
	lastErr   error

	wmu     sync.Mutex
	pending map[string]any // queued writes (nautilus tag name -> desired value)
	written map[string]any // last value successfully written (for on-change)
	wkick   chan struct{}

	cancel context.CancelFunc
	done   chan struct{}
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
// the binding's nautilus name and its device path ("RTU60_ZONE13_*",
// "Program:MainProgram.*"). Assignments live in the driver constructor — not
// the generated manifest — so re-running `nautilus eip import` never erases
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
	sort.Strings(out)
	return out
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
	go d.run(ctx)
}

// Stop tears the driver down.
func (d *Driver) Stop() {
	if d.cancel != nil {
		d.cancel()
		<-d.done
	}
}

// Health reports connection state for diagnostics/HMI.
type Health struct {
	Connected bool
	LastError string
}

// Health returns the current connection health.
func (d *Driver) Health() Health {
	d.mu.Lock()
	defer d.mu.Unlock()
	h := Health{Connected: d.connected}
	if d.lastErr != nil {
		h.LastError = d.lastErr.Error()
	}
	return h
}

// ── io.Driver ────────────────────────────────────────────────────────────

// ReadInputs returns the latest polled snapshot. While disconnected it
// returns an error so the runtime holds last-known values.
func (d *Driver) ReadInputs() (nio.Values, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.connected && len(d.snapshot) == 0 {
		if d.lastErr != nil {
			return nil, d.lastErr
		}
		return nil, fmt.Errorf("eip: %s not connected yet", d.host)
	}
	out := make(nio.Values, len(d.snapshot))
	for k, v := range d.snapshot {
		out[k] = v
	}
	return out, nil
}

// WriteOutputs queues changed values for the writer goroutine. Unknown or
// non-writable names are ignored (the runtime hands us exactly the Outputs
// list it was configured with).
func (d *Driver) WriteOutputs(vals nio.Values) error {
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
	for ctx.Err() == nil {
		sess, err := d.connect(ctx)
		if err != nil {
			d.setErr(err)
			d.log.Warn("eip: connect failed", "host", d.host, "error", err, "retryIn", backoff)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
		d.mu.Lock()
		d.connected, d.lastErr = true, nil
		d.mu.Unlock()
		d.log.Info("eip: connected", "host", d.host, "slot", d.slot, "tags", len(d.inputs))

		d.serve(ctx, sess)

		_ = sess.ctrl.Close()
		d.mu.Lock()
		d.connected = false
		d.mu.Unlock()
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
			if _, ok := live[f.Name]; !ok {
				problems = append(problems, fmt.Sprintf("type %q lost member %q on the controller", td.Name, f.Name))
			}
		}
		sess.liveType[td.Name] = tmpl
	}
	if len(problems) > 0 {
		return fmt.Errorf("eip: manifest does not match controller (re-run `nautilus eip import`):\n  %s",
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
	for {
		wake := time.Duration(time.Hour)
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
func (d *Driver) pollClass(ctx context.Context, sess *session, bindings []TagBinding) bool {
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

// flushWrites pushes pending writes to the controller. Returns false when
// the connection broke.
func (d *Driver) flushWrites(ctx context.Context, sess *session) bool {
	d.wmu.Lock()
	if len(d.pending) == 0 {
		d.wmu.Unlock()
		return true
	}
	work := d.pending
	d.pending = map[string]any{}
	d.wmu.Unlock()

	ok := true
	for name, v := range work {
		b := d.byName[name]
		if err := d.writeOne(ctx, sess, b, v); err != nil {
			d.setErr(fmt.Errorf("write %s: %w", name, err))
			d.log.Warn("eip: write failed", "tag", name, "device", b.Device, "error", err)
			if sess.ctrl.Broken() {
				// Requeue everything not yet attempted; retry after reconnect.
				d.wmu.Lock()
				for n, pv := range work {
					if _, exists := d.pending[n]; !exists {
						d.pending[n] = pv
					}
				}
				d.wmu.Unlock()
				ok = false
				break
			}
			continue
		}
		d.wmu.Lock()
		d.written[name] = v
		d.wmu.Unlock()
	}
	return ok
}

// writeOne writes a single nautilus value to the device: elementary scalars
// directly, struct values as per-leaf symbolic writes of the members that
// changed since the last write (all members on the first write).
func (d *Driver) writeOne(ctx context.Context, sess *session, b TagBinding, v any) error {
	if code, isElem := elementaryCode(b.Type); isElem && b.ArrayLen == 0 {
		data, err := logix.EncodeScalar(code, scalarOf(v))
		if err != nil {
			return err
		}
		return sess.ctrl.WriteTag(ctx, b.Device, code, 1, data)
	}
	iv, ok := v.(ir.Value)
	if !ok {
		return fmt.Errorf("eip: compound tag needs ir.Value, got %T", v)
	}
	var prev *ir.Value
	d.wmu.Lock()
	if p, ok := d.written[b.Name].(ir.Value); ok {
		prev = &p
	}
	d.wmu.Unlock()
	td, ok := d.typeDef(b.Type)
	if !ok {
		return fmt.Errorf("eip: cannot write type %q", b.Type)
	}
	return d.writeStructLeaves(ctx, sess, b.Device, td, iv, prev)
}

func (d *Driver) typeDef(name string) (TypeDef, bool) {
	for _, t := range d.manifest.Types {
		if t.Name == name {
			return t, true
		}
	}
	return TypeDef{}, false
}

// writeStructLeaves walks manifest fields, writing changed elementary leaves
// symbolically ("Device.Field.Sub"). Nested structs recurse; strings and
// arrays inside structs are read-only for now.
func (d *Driver) writeStructLeaves(ctx context.Context, sess *session, devPath string, td TypeDef, v ir.Value, prev *ir.Value) error {
	for i, f := range td.Fields {
		if i >= len(v.Fld) {
			break
		}
		fv := v.Fld[i]
		var pv *ir.Value
		if prev != nil && i < len(prev.Fld) {
			pv = &prev.Fld[i]
		}
		leaf := devPath + "." + f.Name
		if code, isElem := elementaryCode(f.Type); isElem && f.ArrayLen == 0 {
			if pv != nil && scalarEqual(fv, *pv) {
				continue
			}
			data, err := logix.EncodeScalar(code, plainScalar(fv))
			if err != nil {
				return fmt.Errorf("%s: %w", leaf, err)
			}
			if err := sess.ctrl.WriteTag(ctx, leaf, code, 1, data); err != nil {
				return err
			}
			continue
		}
		if sub, ok := d.typeDef(f.Type); ok && f.ArrayLen == 0 {
			if err := d.writeStructLeaves(ctx, sess, leaf, sub, fv, pv); err != nil {
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
		fv, ok := byName[f.Name]
		if !ok {
			return nil, fmt.Errorf("device value for %s missing member %s", typeName, f.Name)
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
