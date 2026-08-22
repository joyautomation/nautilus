// driver.go is the io.Driver surface of the Sparkplug host application:
// construction (New, Option), the read/write seam the runtime scans against,
// and the Start/Stop lifecycle. The transport itself lives in mqtt.go.
//
// New NEVER dials. buildDriver runs inside `nautilus check` and
// `nautilus build`, i.e. in CI with no broker, so everything that can fail on
// bad configuration fails here, offline, and the connection is Start's job —
// the same split eip makes.
//
// OWNED BY B3. Shared types live in types.go.

package host

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
)

// Configuration defaults, from docs/design/sparkplug-host.md §1.
const (
	defaultKeepalive       = 30 * time.Second
	defaultReorderTimeout  = 5 * time.Second
	defaultCommandInterval = 100 * time.Millisecond
	defaultStateForm       = "3.0"
	clientIDPrefix         = "nautilus-host-"
)

// Recognised StateForm values: the Sparkplug 3.0 retained JSON certificate,
// the legacy 2.x retained "ONLINE"/"OFFLINE" text, or both (nautilus's own
// edge subscribes to both — primaryhost.go).
const (
	StateForm30   = "3.0"
	StateForm2x   = "2.x"
	StateFormBoth = "both"
)

// Option configures New.
type Option func(*Driver)

// WithLogger sets the structured logger, overriding Config.Log.
func WithLogger(l *slog.Logger) Option {
	return func(d *Driver) {
		if l != nil {
			d.log = l
		}
	}
}

// WithDiscovery sets the policy for metrics seen on the wire but absent from
// the manifest: DiscoveryLog (default), DiscoveryIgnore or DiscoveryStrict.
// An unrecognised mode is rejected by New.
func WithDiscovery(mode string) Option {
	return func(d *Driver) { d.discovery = mode }
}

// New builds the host driver from a manifest and a connection config. It
// validates both and builds every index the scan path needs, but does not
// touch the network — call Start for that.
func New(m Manifest, cfg Config, opts ...Option) (*Driver, error) {
	if cfg.BrokerURL == "" {
		return nil, fmt.Errorf("host: BrokerURL is required (tcp://host:1883)")
	}
	if cfg.HostID == "" {
		return nil, fmt.Errorf("host: HostID is required (the STATE topic spBv1.0/STATE/<host-id>)")
	}
	if cfg.Keepalive <= 0 {
		cfg.Keepalive = defaultKeepalive
	}
	if cfg.ReorderTimeout <= 0 {
		cfg.ReorderTimeout = defaultReorderTimeout
	}
	if cfg.CommandInterval <= 0 {
		cfg.CommandInterval = defaultCommandInterval
	}
	if cfg.StateForm == "" {
		cfg.StateForm = defaultStateForm
	}
	switch cfg.StateForm {
	case StateForm30, StateForm2x, StateFormBoth:
	default:
		return nil, fmt.Errorf("host: unknown state-form %q (want %q, %q or %q)",
			cfg.StateForm, StateForm30, StateForm2x, StateFormBoth)
	}
	if cfg.ClientID == "" {
		cfg.ClientID = clientIDPrefix + cfg.HostID
	}
	// RebirthOnStart defaults to true; NoRebirthOnStart is the opt-out (a
	// Config literal cannot distinguish "unset" from "off").
	cfg.RebirthOnStart = cfg.RebirthOnStart || !cfg.NoRebirthOnStart

	log := cfg.Log
	if log == nil {
		log = slog.Default()
	}

	d := &Driver{
		cfg:         cfg,
		manifest:    m,
		log:         log,
		discovery:   DiscoveryLog,
		defs:        map[string]*ir.StructDef{},
		byName:      map[string]Binding{},
		byMetric:    map[metricKey]Binding{},
		members:     map[string]memberOut{},
		nodeCfg:     map[string]Node{},
		rebirthTags: map[string]string{},
		nodes:       map[nodeKey]*nodeState{},
		values:      nio.Values{},
		unknown:     map[metricKey]*discovery{},
		pending:     map[string]any{},
		written:     map[string]any{},
		wkick:       make(chan struct{}, 1),
		rebirths:    make(chan nodeKey, 256),
	}
	for _, o := range opts {
		o(d)
	}
	switch d.discovery {
	case DiscoveryLog, DiscoveryIgnore, DiscoveryStrict:
	default:
		return nil, fmt.Errorf("host: unknown discovery mode %q (want %q, %q or %q)",
			d.discovery, DiscoveryLog, DiscoveryIgnore, DiscoveryStrict)
	}

	// buildIndexes runs Manifest.Validate itself — which is also where the
	// reserved "Node Control/Rebirth" binding is rejected — and then fills
	// defs/inputs/byName/byMetric/nodeCfg and the synthesized companion tag
	// sets, rebirthTags among them.
	if err := d.buildIndexes(); err != nil {
		return nil, err
	}
	d.groups = resolveGroups(cfg, m)
	d.initValues()
	// The state machine asks for rebirths from inside paho's ordered message
	// goroutine, under d.mu — hand it a non-blocking enqueue, never the wire.
	d.onRebirthNeeded = d.enqueueRebirth

	return d, nil
}

// resolveGroups picks the groups to subscribe: the explicit list, else the
// manifest's own group, else the "+" wildcard (consume everything).
func resolveGroups(cfg Config, m Manifest) []string {
	if len(cfg.GroupIDs) > 0 {
		return append([]string(nil), cfg.GroupIDs...)
	}
	if m.Group != "" {
		return []string{m.Group}
	}
	return []string{"+"}
}

// ── lifecycle ────────────────────────────────────────────────────────────

// Start launches the connection loop and the outbound command writer. It
// returns immediately; Status reports what happened.
func (d *Driver) Start(ctx context.Context) {
	ctx, d.cancel = context.WithCancel(ctx)
	d.done = make(chan struct{})
	d.mu.Lock()
	d.started = true
	d.mu.Unlock()
	go d.run(ctx)
}

// Stop publishes the STATE death certificate, waits for the token, and only
// then tears the connection down — the TCK requires the host's own death
// publish before both clean and unclean teardown, and the broker firing the
// LWT is explicitly not a substitute (design §2). The DISCONNECT itself
// happens in run's session cleanup, after the context is cancelled, so the
// ordering death → DISCONNECT holds.
func (d *Driver) Stop() {
	if d.cancel == nil {
		return
	}
	d.publishStateDeath()
	d.cancel()
	<-d.done
}

// ── io.Driver ────────────────────────────────────────────────────────────

// ReadInputs returns the whole snapshot — offline nodes included, with
// __Online=false. Once Start has been called it never errors: Sparkplug's
// last value *is* the value, so a death holds values rather than clearing
// them, and quality rides on the synthesized companion tags. Data metrics
// stay absent until first seen, which preserves "reads fault" for a site that
// has never birthed.
//
// Before Start it errors like eip, so a project wired up but never started
// fails loudly rather than scanning zeros.
func (d *Driver) ReadInputs() (nio.Values, error) {
	d.mu.Lock()
	started := d.started
	d.mu.Unlock()
	if !started {
		return nil, fmt.Errorf("host: not connected yet")
	}
	return d.snapshot(), nil
}

// WriteOutputs queues changed values for the command writer. The runtime
// rebuilds and hands us *all* outputs every scan (runtime.go:590-608), so
// change detection is ours: suppress against both the last published value
// and anything already queued, then nudge the writer.
//
// Two kinds of name land here: writable manifest bindings (→ NCMD/DCMD) and
// the synthesized <site>__Rebirth outputs (rising edge → NCMD Rebirth).
// Anything else is ignored — the runtime hands us exactly the Outputs list it
// was configured with, but a hand-wired driver may not.
func (d *Driver) WriteOutputs(vals nio.Values) error {
	d.wmu.Lock()
	changed := false
	for name, v := range vals {
		_, writable := d.byName[name]
		_, isRebirth := d.rebirthTags[name]
		if !writable && !isRebirth {
			continue
		}
		if prev, ok := d.written[name]; ok && sameValue(prev, v) {
			continue
		}
		if prev, ok := d.pending[name]; ok && sameValue(prev, v) {
			continue
		}
		d.pending[name] = v
		changed = true
	}
	d.wmu.Unlock()
	if changed {
		d.kickWriter()
	}
	return nil
}

// kickWriter opens the coalesce window without blocking.
func (d *Driver) kickWriter() {
	select {
	case d.wkick <- struct{}{}:
	default:
	}
}

// RequestRebirth publishes NCMD Node Control/Rebirth to one manifest edge
// node, synchronously. It is the operator/API path; the state machine's own
// gap recovery goes through onRebirthNeeded instead.
func (d *Driver) RequestRebirth(edge string) error {
	group, ok := d.groupFor(edge)
	if !ok {
		return fmt.Errorf("host: unknown edge node %q (not in the manifest)", edge)
	}
	if err := d.publishRebirth(group, edge); err != nil {
		return err
	}
	d.countRebirth()
	return nil
}

// enqueueRebirth is the non-blocking half: safe to call under d.mu and from
// paho's message goroutine.
func (d *Driver) enqueueRebirth(group, edge string) {
	select {
	case d.rebirths <- nodeKey{Group: group, EdgeNode: edge}:
	default:
		d.log.Warn("host: rebirth queue full; dropping request", "group", group, "node", edge)
	}
}

// groupFor resolves an edge node's group. Manifest nodes all live under the
// manifest's group; the resolved subscription list is the fallback for a
// driver built from a group-less manifest.
func (d *Driver) groupFor(edge string) (string, bool) {
	if _, ok := d.nodeCfg[edge]; !ok {
		return "", false
	}
	if d.manifest.Group != "" {
		return d.manifest.Group, true
	}
	if len(d.groups) > 0 && d.groups[0] != "+" {
		return d.groups[0], true
	}
	return "", false
}

// ── value helpers ────────────────────────────────────────────────────────

// sameValue compares two queued write values for on-change suppression. It is
// the host's copy of eip's equalValue: the runtime hands compound tags across
// as ir.Value and scalars as plain Go values, so both shapes must compare.
func sameValue(a, b any) bool {
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

// irValueOf normalizes whatever the runtime handed us into an ir.Value, which
// is what sparkplug.MetricFromValue speaks.
func irValueOf(v any) (ir.Value, bool) {
	switch x := v.(type) {
	case ir.Value:
		return x, true
	case bool:
		return ir.BoolVal(x), true
	case int64:
		return ir.IntVal(x), true
	case int:
		return ir.IntVal(int64(x)), true
	case int32:
		return ir.IntVal(int64(x)), true
	case float64:
		return ir.RealVal(x), true
	case float32:
		return ir.RealVal(float64(x)), true
	case string:
		return ir.StringVal(x), true
	}
	return ir.Value{}, false
}

// truthy reads a rebirth output tag's value as a bool.
func truthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case ir.Value:
		return x.Kind == ir.TypeBool && x.B
	}
	return false
}
