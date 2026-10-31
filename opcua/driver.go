// driver.go is the io.Driver surface of the OPC UA client driver:
// construction (New, Option), the read/write seam the runtime scans
// against, and the Start/Stop lifecycle. One server per driver, one
// session, requests strictly sequential; the connection is client.go, the
// value codec value.go.
//
// Reads are polled, not subscribed. Each scan class is one batched Read
// service call on its own timer — the same shape eip and modbus have, and
// for the hundreds of tags a gateway or a skid exposes it is a few
// kilobytes a second. Subscriptions (monitored items, the Publish queue)
// are the protocol's own change-notification mechanism and would cut that
// further for very large sites; they are deliberately NOT in this first cut,
// because the polled path has no reconnect-era state to get wrong.
//
// New NEVER dials. buildDriver runs inside `naut check` and `naut build`,
// i.e. in CI with no server in sight, so everything that can fail on bad
// configuration fails here, offline, and the connection is Start's job —
// the same split eip, modbus and sparkplug/host make.
package opcua

import (
	"context"
	"fmt"
	"log/slog"
	"path"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gopcua/opcua/ua"
	nio "github.com/joyautomation/nautilus/io"
)

// Defaults.
const (
	defaultScanRate = time.Second
	defaultTimeout  = 5 * time.Second
	defaultRetryMin = time.Second
	defaultRetryMax = 60 * time.Second
	// defaultMaxNodes caps one Read/Write at this many nodes. The server's
	// advertised MaxNodesPerRead (read at connect) lowers it; a
	// Bad_TooManyOperations response halves it.
	defaultMaxNodes = 500
	// defaultName is the driver's identity when the project gives none —
	// the companion tag is "<name>__Online".
	defaultName = "opcua"
)

// suffixOnline names the driver-synthesized companion: "<name>__Online" is
// a BOOL input that is true while the session is up — the guard a program
// reads before trusting the server's tags, exactly like modbus's
// "<source>__Online" and sparkplug/host's "<site>__Online".
const suffixOnline = "__Online"

// Option configures New.
type Option func(*Driver)

// WithName sets the driver's identity: the status row, the log fields,
// and — unless WithOnlineTag names it — the companion tag
// "<name>__Online", the name reduced to an identifier ("opcua-2" →
// opcua_2__Online). Default "opcua".
func WithName(name string) Option {
	return func(d *Driver) {
		if name != "" {
			d.name = name
		}
	}
}

// WithOnlineTag names the BOOL companion explicitly. The project loader
// passes the same name it gives every single-connection driver's
// companion (project.onlineTagFor), so the import and Load agree.
func WithOnlineTag(name string) Option {
	return func(d *Driver) { d.onlineTag = name }
}

// WithSecurity selects the endpoint policy and mode (see Security).
func WithSecurity(policy, mode string) Option {
	return func(d *Driver) { d.conn.Security = Security{Policy: policy, Mode: mode} }
}

// WithAuth selects the UserName token. The password is never in a
// manifest: the project reads NAUTILUS_OPCUA_PASSWORD.
func WithAuth(user, pass string) Option {
	return func(d *Driver) { d.conn.Username, d.conn.Password = user, pass }
}

// WithPKIDir sets where the client certificate lives (or is generated).
func WithPKIDir(dir string) Option { return func(d *Driver) { d.conn.PKIDir = dir } }

// WithApplicationURI sets the URI announced to the server and put in the
// certificate (default DefaultApplicationURI).
func WithApplicationURI(uri string) Option {
	return func(d *Driver) { d.conn.ApplicationURI = uri }
}

// WithTimeout bounds the dial and every request (default 5s).
func WithTimeout(t time.Duration) Option { return func(d *Driver) { d.conn.Timeout = t } }

// WithRetry bounds the reconnect backoff ladder (defaults 1s/60s).
func WithRetry(minDelay, maxDelay time.Duration) Option {
	return func(d *Driver) { d.retryMin, d.retryMax = minDelay, maxDelay }
}

// WithMaxNodes caps the nodes per Read/Write request (default 500, and
// never more than the server advertises).
func WithMaxNodes(n int) Option {
	return func(d *Driver) {
		if n > 0 {
			d.maxNodes = n
		}
	}
}

// WithScanRate sets the default scan class's poll interval (default 1s).
func WithScanRate(r time.Duration) Option { return func(d *Driver) { d.scanRate = r } }

// WithScanClass defines (or redefines) a scan class and its poll interval.
// The default class exists implicitly; NoPoll cannot be given a rate.
func WithScanClass(name string, rate time.Duration) Option {
	return func(d *Driver) { d.classRates[name] = rate }
}

// WithTagClass assigns tags to a scan class by glob patterns matched
// against the binding's nautilus name. Assignments live in the driver
// constructor — not the generated manifest — so re-running
// `naut opcua import` never erases polling policy. Later assignments
// override earlier ones.
func WithTagClass(class string, patterns ...string) Option {
	return func(d *Driver) {
		d.assignments = append(d.assignments, classAssignment{class: class, patterns: patterns})
	}
}

// WithLogger sets the structured logger.
func WithLogger(l *slog.Logger) Option {
	return func(d *Driver) {
		if l != nil {
			d.log = l
		}
	}
}

// classAssignment maps glob patterns to a scan class, applied in option
// order (the last matching assignment wins).
type classAssignment struct {
	class    string
	patterns []string
}

// Driver polls one OPC UA server and implements io.Driver, io.BatchReader
// and io.QualityReporter. A background goroutine owns the session: dial →
// resolve namespaces → prime every class → poll per scan class → reconnect
// with backoff on transport errors. ReadInputs never blocks on the network;
// it returns the latest snapshot. WriteOutputs enqueues changed values; the
// session loop pushes them (latest value wins per tag).
type Driver struct {
	endpoint  string
	name      string
	onlineTag string
	manifest  Manifest
	conn      Connect
	scanRate  time.Duration
	retryMin  time.Duration
	retryMax  time.Duration
	maxNodes  int
	log       *slog.Logger

	classRates  map[string]time.Duration
	assignments []classAssignment

	classes  []*scanClass
	bindings []*binding
	byName   map[string]*binding
	inputs   []string
	outputs  []string

	// mu guards everything the session goroutine writes and other
	// goroutines read: snapshot, state, health, pending.
	mu       sync.Mutex
	snapshot nio.Values
	online   bool
	answered bool // a read succeeded on this session (resets backoff)
	primed   bool // every scan class has been read once on this session
	state    string
	sinceMs  int64
	lastErr  error
	retries  uint64
	rttMs    float64
	server   serverInfo
	pending  map[string]any
	written  map[string]any

	gateMu    sync.Mutex
	writeGate func() bool

	wmu      sync.Mutex
	baseline bool

	started atomic.Bool
	reads   atomic.Uint64
	writes  atomic.Uint64
	errs    atomic.Uint64

	kick   chan struct{}
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// serverInfo is what the driver learned about the server at connect.
type serverInfo struct {
	policy     string
	mode       string
	namespaces []string
	maxNodes   int
}

// binding is one manifest binding plus its live state. node is the id as
// resolved against the server's namespace table; bad and delivered feed
// Quality. All guarded by Driver.mu once Start has run.
type binding struct {
	TagBinding
	node      *ua.NodeID
	bad       bool
	delivered bool
	// answered: the server has answered this binding on THIS session. A
	// value carried over from the last session is Stale until it has.
	answered bool
	// missing: the binding's namespace URI is not in the server's live
	// namespace table, so its node id cannot be resolved. It is Bad and is
	// neither read nor written until a connect finds the URI again —
	// reading the stale index could return another namespace's node.
	missing bool
}

// scanClass is one poll group and its schedule, owned by the session loop.
type scanClass struct {
	name     string
	rate     time.Duration
	bindings []*binding
	next     time.Time
}

// New builds the driver: validates the manifest, resolves scan classes,
// parses every node id, and synthesizes the companion. It NEVER dials —
// call Start to begin polling.
func New(endpoint string, m Manifest, opts ...Option) (*Driver, error) {
	if endpoint == "" {
		return nil, fmt.Errorf("opcua: endpoint is required (opc.tcp://host:4840)")
	}
	if err := m.Validate(); err != nil {
		return nil, fmt.Errorf("opcua: manifest: %w", err)
	}
	d := &Driver{
		endpoint:   endpoint,
		name:       defaultName,
		manifest:   m,
		scanRate:   defaultScanRate,
		retryMin:   defaultRetryMin,
		retryMax:   defaultRetryMax,
		maxNodes:   defaultMaxNodes,
		log:        slog.Default(),
		classRates: map[string]time.Duration{},
		byName:     map[string]*binding{},
		snapshot:   nio.Values{},
		state:      "connecting",
		pending:    map[string]any{},
		written:    map[string]any{},
		baseline:   true,
		kick:       make(chan struct{}, 1),
	}
	for _, o := range opts {
		o(d)
	}
	if d.conn.Timeout <= 0 {
		d.conn.Timeout = defaultTimeout
	}
	if d.retryMin <= 0 {
		d.retryMin = defaultRetryMin
	}
	if d.retryMax < d.retryMin {
		d.retryMax = d.retryMin
	}
	policyURI, mode, err := d.conn.Security.parse()
	if err != nil {
		return nil, fmt.Errorf("opcua: %w", err)
	}
	if d.conn.Username != "" && (policyURI == ua.SecurityPolicyURINone || mode == ua.MessageSecurityModeNone) {
		return nil, fmt.Errorf("opcua: username %q with security None: a password is never sent on an unsecured channel — use Sign or SignAndEncrypt, or drop username", d.conn.Username)
	}
	d.conn.SessionName = "nautilus " + d.name
	if d.onlineTag == "" {
		d.onlineTag = OnlineTagFor(d.name)
	}
	if err := d.resolveClasses(); err != nil {
		return nil, err
	}
	if err := d.buildBindings(); err != nil {
		return nil, err
	}
	return d, nil
}

// resolveClasses checks every class in use has a positive rate.
func (d *Driver) resolveClasses() error {
	if _, ok := d.classRates[DefaultClass]; !ok {
		d.classRates[DefaultClass] = d.scanRate
	}
	if rate, ok := d.classRates[NoPoll]; ok {
		return fmt.Errorf("opcua: scan class %q is reserved and cannot have a rate (%v)", NoPoll, rate)
	}
	for _, a := range d.assignments {
		if a.class == NoPoll {
			continue
		}
		if _, ok := d.classRates[a.class]; !ok {
			return fmt.Errorf("opcua: WithTagClass(%q, ...) references an undefined scan class — add WithScanClass(%q, rate)", a.class, a.class)
		}
	}
	for name, rate := range d.classRates {
		if rate <= 0 {
			return fmt.Errorf("opcua: scan class %q has non-positive rate %v", name, rate)
		}
	}
	return nil
}

// buildBindings parses the node ids, applies tag-class assignments, groups
// the polled bindings per class, and builds the input/output name lists.
func (d *Driver) buildBindings() error {
	online := d.OnlineTagName()
	byClass := map[string]*scanClass{}
	for _, t := range d.manifest.Tags {
		if t.Name == online {
			return fmt.Errorf("opcua: companion tag %q collides with a binding of the same name", online)
		}
		nid, err := ua.ParseNodeID(t.NodeID)
		if err != nil {
			return fmt.Errorf("opcua: tag %s: nodeid %q: %w", t.Name, t.NodeID, err)
		}
		b := &binding{TagBinding: t, node: nid}
		class := t.ScanClass
		if class == "" {
			class = DefaultClass
		}
		for _, a := range d.assignments {
			for _, p := range a.patterns {
				if ok, _ := path.Match(p, t.Name); ok {
					class = a.class
					break
				}
			}
		}
		b.ScanClass = class
		if class != NoPoll {
			rate, ok := d.classRates[class]
			if !ok {
				return fmt.Errorf("opcua: binding %q names undefined scan class %q — add WithScanClass(%q, rate)", t.Name, class, class)
			}
			sc := byClass[class]
			if sc == nil {
				sc = &scanClass{name: class, rate: rate}
				byClass[class] = sc
			}
			sc.bindings = append(sc.bindings, b)
			d.inputs = append(d.inputs, t.Name)
		}
		d.bindings = append(d.bindings, b)
		d.byName[t.Name] = b
		if t.Writable {
			d.outputs = append(d.outputs, t.Name)
		}
	}
	names := make([]string, 0, len(byClass))
	for n := range byClass {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		d.classes = append(d.classes, byClass[n])
	}
	d.inputs = append(d.inputs, online)
	sort.Strings(d.inputs)
	sort.Strings(d.outputs)
	return nil
}

// OnlineTagName is the synthesized BOOL companion's name — shared with the
// generator so tags/opcua.yaml declares exactly what the driver delivers.
func (d *Driver) OnlineTagName() string { return d.onlineTag }

// OnlineTagFor is the companion a driver called name delivers:
// "<name>__Online" with the name reduced to an ST identifier (anything but
// a letter, digit or underscore becomes "_", as does a leading digit). The
// same reduction project.onlineTagFor applies.
func OnlineTagFor(name string) string {
	if name == "" {
		name = defaultName
	}
	b := []byte(name)
	for i, c := range b {
		if !(c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' && i > 0) {
			b[i] = '_'
		}
	}
	return string(b) + suffixOnline
}

// InputType names the IEC type an input delivers — what a manifest tag
// with no type: that no program declares starts at the zero of until the
// first read (runtime.TagDef.DriverType). The codec delivers bool, int64,
// float64 and string, so: Boolean is BOOL; the integers that fit 32 bits
// signed are DINT; UInt32, Int64, UInt64 and DateTime (unix milliseconds)
// are LINT; Float is REAL, Double LREAL; String is STRING. The __Online
// companion is BOOL. "" for a name the driver does not deliver.
func (d *Driver) InputType(name string) string {
	if name != "" && name == d.onlineTag {
		return "BOOL"
	}
	b, ok := d.byName[name]
	if !ok || b.ScanClass == NoPoll {
		return ""
	}
	return iecType(b.Type)
}

// iecType maps a binding's OPC UA built-in type onto the IEC type the
// codec's delivered value seeds as (see InputType).
func iecType(t string) string {
	switch t {
	case "Boolean":
		return "BOOL"
	case "SByte", "Byte", "Int16", "UInt16", "Int32":
		return "DINT"
	case "UInt32", "Int64", "UInt64", "DateTime":
		return "LINT"
	case "Float":
		return "REAL"
	case "Double":
		return "LREAL"
	case "String":
		return "STRING"
	}
	return ""
}

// Warnings are the configuration's non-fatal findings, for `naut check`:
// a username with no password in the environment, and no pki-dir when the
// configured security can pick an endpoint that needs a certificate.
func (d *Driver) Warnings() []string {
	var out []string
	if d.conn.Username != "" && d.conn.Password == "" {
		out = append(out, fmt.Sprintf("opcua driver %q: username %q is set but NAUTILUS_OPCUA_PASSWORD is empty here", d.name, d.conn.Username))
	}
	if m := strings.ToLower(d.conn.Security.Mode); d.conn.Username != "" && m != "sign" && m != "signandencrypt" {
		out = append(out, fmt.Sprintf("opcua driver %q: username %q without security-mode: Sign or SignAndEncrypt — the session is refused if the server offers only an unsecured endpoint, rather than send the password in the clear", d.name, d.conn.Username))
	}
	if d.conn.PKIDir == "" && !strings.EqualFold(d.conn.Security.Policy, "None") {
		out = append(out, fmt.Sprintf("opcua driver %q: no pki-dir — the session is refused if the endpoint chosen needs a client certificate (any security policy but None)", d.name))
	}
	return out
}

// Online is the <name>__Online verdict: the session is up and every scan
// class has been read once on it. It is also the Sparkplug device's health
// (project.driverHealth), so a DBIRTH never goes out half-filled.
func (d *Driver) Online() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.online && d.primed
}

// Name is the driver's identity (WithName).
func (d *Driver) Name() string { return d.name }

// Endpoint is the URL the driver dials.
func (d *Driver) Endpoint() string { return d.endpoint }

// SetWriteGate installs the write gate WriteOutputs consults: commands
// leave the driver only while gate() is true. The project wires it to
// redundancy leadership (leader.Elector.IsLeader). Reads are unaffected.
func (d *Driver) SetWriteGate(gate func() bool) {
	d.gateMu.Lock()
	d.writeGate = gate
	d.gateMu.Unlock()
}

func (d *Driver) gateOpen() bool {
	d.gateMu.Lock()
	gate := d.writeGate
	d.gateMu.Unlock()
	return gate == nil || gate()
}

// ── names / classes ──────────────────────────────────────────────────────

// InputNames returns every tag the driver delivers values for: the polled
// bindings plus the "<name>__Online" companion — for
// runtime.Options.Inputs. The companion is present from Start even though a
// data tag stays absent until first read, so logic can interlock on
// __Online before the first poll.
func (d *Driver) InputNames() []string { return append([]string(nil), d.inputs...) }

// OutputNames returns the writable bindings — for runtime.Options.Outputs.
func (d *Driver) OutputNames() []string { return append([]string(nil), d.outputs...) }

// ScanClasses reports the resolved poll groups — for diagnostics and tests.
func (d *Driver) ScanClasses() map[string][]string {
	out := map[string][]string{}
	for _, c := range d.classes {
		for _, b := range c.bindings {
			out[c.name] = append(out[c.name], b.Name)
		}
		sort.Strings(out[c.name])
	}
	return out
}

// ── lifecycle ────────────────────────────────────────────────────────────

// Start launches the session loop. It returns immediately; use Health to
// observe connection state.
func (d *Driver) Start(ctx context.Context) {
	ctx, d.cancel = context.WithCancel(ctx)
	d.wmu.Lock()
	d.baseline = true
	d.wmu.Unlock()
	d.started.Store(true)
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		d.run(ctx)
	}()
}

// Stop tears the session loop down and waits.
func (d *Driver) Stop() {
	if d.cancel != nil {
		d.cancel()
		d.wg.Wait()
	}
}

// ── io.Driver ────────────────────────────────────────────────────────────

// ReadInputs returns the latest snapshot, companion included. Once Start
// has been called it never errors: a dead session holds its last values
// (the tag store must never see a zero because a link dropped) and quality
// rides on Quality() and the companion. A data tag never delivered stays
// absent. Before Start it errors, so a project wired up but never started
// fails loudly rather than scanning zeros.
func (d *Driver) ReadInputs() (nio.Values, error) {
	if !d.started.Load() {
		return nil, fmt.Errorf("opcua: not started yet")
	}
	out := make(nio.Values, len(d.inputs))
	d.snapshotInto(out)
	return out, nil
}

// ReadInputsInto is ReadInputs without the per-scan allocation
// (io.BatchReader).
func (d *Driver) ReadInputsInto(dst nio.Values) error {
	if !d.started.Load() {
		return fmt.Errorf("opcua: not started yet")
	}
	if dst == nil {
		return fmt.Errorf("opcua: ReadInputsInto needs a non-nil map to fill")
	}
	n := d.snapshotInto(dst)
	if len(dst) != n {
		d.mu.Lock()
		for k := range dst {
			if _, ok := d.snapshot[k]; !ok && k != d.OnlineTagName() {
				delete(dst, k)
			}
		}
		d.mu.Unlock()
	}
	return nil
}

func (d *Driver) snapshotInto(dst nio.Values) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	for k, v := range d.snapshot {
		dst[k] = v
	}
	dst[d.OnlineTagName()] = d.online && d.primed
	return len(d.snapshot) + 1
}

// WriteOutputs queues changed values for the session loop.
//
// OUTPUTS ARE COMMANDS; AN UNCHANGED OUTPUT SINCE START IS NOT A COMMAND.
// The rules (modbus's, minus its keep-alive special case):
//
//  1. GATE. While the write gate is closed (not leader, logic faulted) no
//     command is queued or recorded.
//  2. BASELINE. The first snapshot after Start is recorded, not written —
//     it is the state of the world at t=0, not a set of commands.
//  3. CHANGE. Thereafter a value equal to the last handed-over value is not
//     a command. A changed value is queued, last value per tag, and held
//     across a disconnect — flushed when the session reconnects.
func (d *Driver) WriteOutputs(vals nio.Values) error {
	d.wmu.Lock()
	baseline := d.baseline
	d.baseline = false
	d.wmu.Unlock()
	if !d.gateOpen() {
		return nil
	}
	kicked := false
	d.mu.Lock()
	for name, v := range vals {
		b, ok := d.byName[name]
		if !ok || !b.Writable {
			continue
		}
		if baseline {
			if _, queued := d.pending[name]; !queued {
				d.written[name] = v
			}
		} else if prev, ok := d.written[name]; ok && sameScalar(prev, v) {
			// unchanged since the last value we accounted for
		} else {
			d.written[name] = v
			d.pending[name] = v
			kicked = true
		}
	}
	d.mu.Unlock()
	if kicked {
		nudge(d.kick)
	}
	return nil
}

func nudge(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

// sameScalar compares two handed-over output values for on-change
// suppression, only ever with ==-safe kinds.
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

// ── session loop ─────────────────────────────────────────────────────────

// run is the session's life: connecting ⇄ connected with 1s→60s backoff on
// dial/IO errors. The ladder resets only once the server has answered a
// read on a session, not on a successful dial.
func (d *Driver) run(ctx context.Context) {
	backoff := d.retryMin
	for ctx.Err() == nil {
		sess, err := Dial(ctx, d.endpoint, d.conn)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			d.errs.Add(1)
			d.mu.Lock()
			d.retries++
			d.mu.Unlock()
			d.setState("error", err)
			d.log.Warn("opcua: connect failed", "driver", d.name, "endpoint", d.endpoint, "error", err, "retryIn", backoff)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff *= 2; backoff > d.retryMax {
				backoff = d.retryMax
			}
			continue
		}

		d.onConnect(ctx, sess)
		d.serve(ctx, sess)

		// Offline first: Close can take its timeout, and the values must
		// not read Good meanwhile.
		// The state row goes down with __Online, not after Close.
		d.mu.Lock()
		d.online, d.primed = false, false
		answered := d.answered
		d.mu.Unlock()
		if ctx.Err() == nil {
			d.setState("error", nil)
		}
		sess.Close()
		if ctx.Err() != nil {
			return
		}
		if answered {
			backoff = d.retryMin
		}
		d.log.Warn("opcua: session lost", "driver", d.name, "endpoint", d.endpoint, "retryIn", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > d.retryMax {
			backoff = d.retryMax
		}
	}
}

// onConnect records what the server is, resolves every binding's namespace
// against the live table, and learns the server's read limit.
func (d *Driver) onConnect(ctx context.Context, sess *Session) {
	nss := sess.Client.Namespaces()
	info := serverInfo{
		policy:     policyName(sess.Endpoint.SecurityPolicyURI),
		mode:       modeName(sess.Endpoint.SecurityMode),
		namespaces: nss,
		maxNodes:   d.maxNodes,
	}
	index := make(map[string]uint16, len(nss))
	for i, uri := range nss {
		index[uri] = uint16(i)
	}
	// MaxNodesPerRead is Server/ServerCapabilities/OperationLimits; a
	// server that does not publish it (or answers 0) keeps our default.
	rctx, cancel := context.WithTimeout(ctx, d.conn.Timeout)
	resp, err := sess.Client.Read(rctx, &ua.ReadRequest{NodesToRead: []*ua.ReadValueID{{
		NodeID: ua.NewNumericNodeID(0, maxNodesPerReadNode), AttributeID: ua.AttributeIDValue}}})
	cancel()
	if err == nil && len(resp.Results) == 1 && good(resp.Results[0].Status) && resp.Results[0].Value != nil {
		if n, ok := toValue(resp.Results[0].Value); ok {
			if lim, _ := toInt(n); lim > 0 && int(lim) < info.maxNodes {
				info.maxNodes = int(lim)
			}
		}
	}

	d.mu.Lock()
	remapped := 0
	missing := 0
	for _, b := range d.bindings {
		b.bad, b.answered, b.missing = false, false, false
		if b.Namespace == "" {
			continue
		}
		idx, ok := index[b.Namespace]
		if !ok {
			// Not on this server. The index the node id carries may now
			// belong to another namespace with a node of the same id, so
			// it is not read at all: Bad until the URI comes back.
			b.bad, b.missing = true, true
			missing++
			d.log.Warn("opcua: namespace not on server; tag reads Bad", "driver", d.name, "tag", b.Name, "namespace", b.Namespace)
			continue
		}
		if b.node.Namespace() != idx {
			b.node = withNamespace(b.node, idx)
			remapped++
		}
	}
	d.server = info
	d.online, d.answered, d.primed = true, false, false
	d.mu.Unlock()
	d.setState("connected", nil)
	d.log.Info("opcua: connected", "driver", d.name, "endpoint", d.endpoint,
		"policy", info.policy, "mode", info.mode, "tags", len(d.bindings), "remapped", remapped, "missing", missing, "maxNodes", info.maxNodes)
}

// serverStateNode is Server_ServerStatus_State (ns=0;i=2259), the liveness
// read for a class none of whose bindings can be resolved.
const serverStateNode = 2259

// maxNodesPerReadNode is Server_ServerCapabilities_OperationLimits_
// MaxNodesPerRead (Part 5 §6.3.11), ns=0;i=11705.
const maxNodesPerReadNode = 11705

// withNamespace re-targets a node id at another namespace index. Numeric
// ids are rebuilt (a two-/four-byte encoding cannot carry a wide index);
// the rest are copied and re-stamped.
func withNamespace(n *ua.NodeID, ns uint16) *ua.NodeID {
	switch n.Type() {
	case ua.NodeIDTypeTwoByte, ua.NodeIDTypeFourByte, ua.NodeIDTypeNumeric:
		return ua.NewNumericNodeID(ns, n.IntID())
	}
	c := *n
	if err := c.SetNamespace(ns); err != nil {
		return n
	}
	return &c
}

// serve polls one connected session until it breaks or ctx ends. On
// connect: flush the queued writes first (a command must not wait behind
// a full poll cycle), then prime every class so the snapshot fills without
// waiting a slow class's interval.
func (d *Driver) serve(ctx context.Context, sess *Session) {
	if !d.flushWrites(ctx, sess) {
		return
	}
	for _, c := range d.classes {
		if !d.pollClass(ctx, sess, c) {
			return
		}
		c.next = time.Now().Add(c.rate)
	}
	d.mu.Lock()
	d.primed = true
	d.mu.Unlock()
	for {
		if ctx.Err() != nil {
			return
		}
		wake := time.Hour
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
		case <-d.kick:
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
				if !d.pollClass(ctx, sess, c) {
					return
				}
				c.next = time.Now().Add(c.rate)
			}
		}
	}
}

// pollClass reads one class's bindings in chunks of at most maxNodes.
// Returns false when the session can no longer be trusted (transport
// error → reconnect with backoff). A per-node bad status marks just that
// binding Bad; the rest of the class keeps delivering.
func (d *Driver) pollClass(ctx context.Context, sess *Session, c *scanClass) bool {
	d.mu.Lock()
	limit := d.server.maxNodes
	// A binding whose namespace is missing is not read (see onConnect).
	live := make([]*binding, 0, len(c.bindings))
	for _, b := range c.bindings {
		if !b.missing {
			live = append(live, b)
		}
	}
	d.mu.Unlock()
	if limit <= 0 {
		limit = 1
	}
	if len(live) == 0 && len(c.bindings) > 0 {
		// Every binding in the class is unresolvable. Still ask the server
		// something on the class's schedule, so a dead session is noticed
		// and __Online falls.
		rctx, cancel := context.WithTimeout(ctx, d.conn.Timeout)
		_, err := sess.Client.Read(rctx, &ua.ReadRequest{NodesToRead: []*ua.ReadValueID{{
			NodeID: ua.NewNumericNodeID(0, serverStateNode), AttributeID: ua.AttributeIDValue}}})
		cancel()
		if err != nil {
			d.errs.Add(1)
			d.mu.Lock()
			d.lastErr = err
			d.retries++
			d.mu.Unlock()
			return false
		}
		d.mu.Lock()
		d.answered = true
		d.mu.Unlock()
		return true
	}
	for start := 0; start < len(live); {
		end := min(start+limit, len(live))
		chunk := live[start:end]
		ids := make([]*ua.ReadValueID, len(chunk))
		d.mu.Lock()
		for i, b := range chunk {
			ids[i] = &ua.ReadValueID{NodeID: b.node, AttributeID: ua.AttributeIDValue}
		}
		d.mu.Unlock()

		t0 := time.Now()
		rctx, cancel := context.WithTimeout(ctx, d.conn.Timeout)
		resp, err := sess.Client.Read(rctx, &ua.ReadRequest{NodesToRead: ids, TimestampsToReturn: ua.TimestampsToReturnSource})
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return false
			}
			if sc, ok := err.(ua.StatusCode); ok && sc == ua.StatusBadTooManyOperations && limit > 1 {
				// The server's limit is lower than it said (or than our
				// default): halve and retry this chunk.
				limit = max(limit/2, 1)
				d.mu.Lock()
				d.server.maxNodes = limit
				d.mu.Unlock()
				d.log.Warn("opcua: read too large, halving", "driver", d.name, "maxNodes", limit)
				continue
			}
			d.errs.Add(1)
			d.mu.Lock()
			d.lastErr = err
			d.retries++
			d.mu.Unlock()
			d.log.Warn("opcua: read failed", "driver", d.name, "class", c.name, "error", err)
			return false
		}
		if len(resp.Results) != len(chunk) {
			d.errs.Add(1)
			d.mu.Lock()
			d.lastErr = fmt.Errorf("read: %d results for %d nodes", len(resp.Results), len(chunk))
			d.mu.Unlock()
			return false
		}
		rtt := time.Since(t0)
		d.reads.Add(1)

		d.mu.Lock()
		if d.rttMs == 0 {
			d.rttMs = float64(rtt.Microseconds()) / 1000
		} else {
			d.rttMs = 0.8*d.rttMs + 0.2*float64(rtt.Microseconds())/1000
		}
		d.answered = true
		for i, b := range chunk {
			dv := resp.Results[i]
			b.answered = true
			if dv == nil || !good(dv.Status) {
				if !b.bad {
					if dv != nil {
						d.lastErr = fmt.Errorf("tag %s: %v", b.Name, dv.Status)
					}
				}
				b.bad = true
				continue
			}
			v, ok := toValue(dv.Value)
			if !ok {
				if !b.bad {
					d.lastErr = fmt.Errorf("tag %s: value is not a scalar built-in", b.Name)
				}
				b.bad = true
				continue
			}
			b.bad = false
			b.delivered = true
			d.snapshot[b.Name] = v
		}
		d.mu.Unlock()
		start = end
	}
	return true
}

// flushWrites pushes the pending writes with the Write service. Returns
// false when the session broke; unattempted writes stay queued for the
// reconnect. A per-node bad status means the server refused THIS write on
// a healthy session: it is dropped (re-queueing would loop forever) and
// reported.
func (d *Driver) flushWrites(ctx context.Context, sess *Session) bool {
	d.mu.Lock()
	if len(d.pending) == 0 {
		d.mu.Unlock()
		return true
	}
	work := d.pending
	d.pending = map[string]any{}
	limit := d.server.maxNodes
	d.mu.Unlock()
	if limit <= 0 {
		limit = 1
	}

	// The gate is checked again here, not only when the command was
	// queued: a replica that lost leadership during an outage must not
	// send what it queued while it led. Dropped commands are forgotten
	// (not recorded as written), so the same value is a command again
	// once the gate reopens.
	if !d.gateOpen() {
		d.mu.Lock()
		for name := range work {
			delete(d.written, name)
		}
		d.mu.Unlock()
		d.log.Warn("opcua: write gate closed; queued writes dropped", "driver", d.name, "count", len(work))
		return true
	}

	var items []*ua.WriteValue
	var names []string
	for name, v := range work {
		b := d.byName[name]
		d.mu.Lock()
		missing := b.missing
		if missing {
			delete(d.written, name)
		}
		d.mu.Unlock()
		if missing {
			d.errs.Add(1)
			d.log.Warn("opcua: write dropped: namespace not on server", "driver", d.name, "tag", name, "namespace", b.Namespace)
			continue
		}
		variant, err := toVariant(b.Type, v)
		if err != nil {
			d.errs.Add(1)
			d.mu.Lock()
			d.lastErr = fmt.Errorf("write %s: %w", name, err)
			d.mu.Unlock()
			d.log.Warn("opcua: write dropped", "driver", d.name, "tag", name, "error", err)
			continue
		}
		d.mu.Lock()
		node := b.node
		d.mu.Unlock()
		items = append(items, &ua.WriteValue{
			NodeID:      node,
			AttributeID: ua.AttributeIDValue,
			Value:       &ua.DataValue{EncodingMask: ua.DataValueValue, Value: variant},
		})
		names = append(names, name)
	}

	requeue := func(from int) {
		d.mu.Lock()
		for _, n := range names[from:] {
			if _, exists := d.pending[n]; !exists {
				d.pending[n] = work[n]
			}
		}
		d.mu.Unlock()
	}

	for start := 0; start < len(items); {
		end := min(start+limit, len(items))
		wctx, cancel := context.WithTimeout(ctx, d.conn.Timeout)
		resp, err := sess.Client.Write(wctx, &ua.WriteRequest{NodesToWrite: items[start:end]})
		cancel()
		if err != nil {
			d.errs.Add(1)
			if ctx.Err() == nil {
				d.mu.Lock()
				d.lastErr = fmt.Errorf("write %v: %w", names[start:end], err)
				d.retries++
				d.mu.Unlock()
				d.log.Warn("opcua: write failed", "driver", d.name, "tags", names[start:end], "error", err)
			}
			requeue(start)
			return false
		}
		for i, sc := range resp.Results {
			if good(sc) {
				d.writes.Add(1)
				continue
			}
			d.errs.Add(1)
			d.mu.Lock()
			d.lastErr = fmt.Errorf("write %s: %v", names[start+i], sc)
			d.mu.Unlock()
			d.log.Warn("opcua: write refused", "driver", d.name, "tag", names[start+i], "status", sc)
		}
		start = end
	}
	return true
}

// setState records a state transition with its start time; lastErr is
// kept when err is nil so the reason survives into the backoff wait.
func (d *Driver) setState(state string, err error) {
	d.mu.Lock()
	if d.state != state {
		d.state = state
		d.sinceMs = time.Now().UnixMilli()
	}
	if err != nil {
		d.lastErr = err
	}
	d.mu.Unlock()
}

// Interface conformance.
var (
	_ nio.Driver          = (*Driver)(nil)
	_ nio.BatchReader     = (*Driver)(nil)
	_ nio.QualityReporter = (*Driver)(nil)
)
