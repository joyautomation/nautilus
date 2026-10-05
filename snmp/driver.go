// Package snmp is the SNMP driver of the IT-hardware set
// (docs/design/it-drivers.md §3.1): switches, PDUs and UPSes become ordinary
// nautilus struct tags — a SwitchPort, a UPS — polled over SNMP v2c or v3.
//
// driver.go is the io.Driver surface: construction (New, Option), the poll
// planner and the Poll/Write functions hw.Base runs, and the lifecycle. The
// machinery every IT driver shares — snapshots, __Online / __LastPollMs,
// Quality, backoff, scan classes, the write baseline — is hw.Base; this file
// supplies what is SNMP about it: which PDUs fetch a scan class, and how a
// varbind becomes a member value.
//
// At run time the driver is a dumb executor of explicit bindings — an OID
// per member — the way modbus.Driver knows registers and nothing about
// VFDs. What a SwitchPort IS in IF-MIB terms lives in snmp/profiles and is
// spent at import time.
//
// New NEVER dials. It runs inside `naut check` and `naut build`, in CI,
// with no agent and no credentials in sight, so everything that can fail on
// configuration fails there, offline; the socket and the secrets are
// Start's business.
package snmp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/hw"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/snmp/walk"
)

// walkThreshold is how many bound instances under one table column make a
// GetBulk walk of that column cheaper than naming them in a Get. Below it
// (a chassis serial, UPS output line 1) the OIDs ride in one Get with the
// scalars; at or above it (28 ports' ifOperStatus) the column is walked,
// ceil(rows / max-repetitions) PDUs, instead of one varbind per port.
const walkThreshold = 4

// Option configures New.
type Option func(*Driver)

// WithScanRate sets the default class's poll INTERVAL (default 10s — a
// switch's own counters move every few seconds; brief §4).
func WithScanRate(r time.Duration) Option { return func(d *Driver) { d.scanRate = r } }

// WithScanClass defines a named scan class and its interval.
func WithScanClass(name string, rate time.Duration) Option {
	return func(d *Driver) { d.classRates[name] = rate }
}

// WithTagClass assigns members to a class by globs over "Tag.Member" paths
// and bare tag names ("*.Name", "SW1_Port*"); later assignments win.
// Policy lives in the project, not the generated manifest, so re-running
// `naut snmp import` never erases it.
func WithTagClass(class string, patterns ...string) Option {
	return func(d *Driver) {
		d.assignments = append(d.assignments, hw.ClassAssignment{Class: class, Patterns: patterns})
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

// WithDialer replaces the transport: tests hand the driver an in-memory
// Getter or an agent on 127.0.0.1:0. The default is Dial (gosnmp).
func WithDialer(dial DialFunc) Option { return func(d *Driver) { d.dial = dial } }

// Driver polls a set of SNMP agents and implements io.Driver,
// io.BatchReader and io.QualityReporter (through hw.Base).
type Driver struct {
	manifest    Manifest
	base        *hw.Base
	log         *slog.Logger
	dial        DialFunc
	scanRate    time.Duration
	classRates  map[string]time.Duration
	assignments []hw.ClassAssignment

	sources map[string]*source
	writes  map[string]Write
}

// source is one agent's poll-side state. Base runs a source's polls and
// its writes on ONE goroutine (the source loop), so sess and counters need
// no lock.
type source struct {
	cfg      Source
	plans    map[string]*classPlan
	sess     Getter
	counters map[string]*hw.Counter // "Tag.Member" of every rate binding
	warned   map[string]bool
	perClass map[string]int
	// unbound summarises, per contract type, the members no binding of
	// this source fills — logged once, at the first poll, so "why is
	// CpuPct 0?" has an answer in the log and not only in the manifest.
	unbound  string
	reported bool
}

// classPlan is what one (source, scan class) poll sends: the Get list and
// the column walks, and which bindings read each OID.
type classPlan struct {
	gets    []string
	columns []column
	uses    map[string][]use // OID → the members it feeds
	tags    []string
}

type column struct {
	prefix string // the column OID, walked with GetBulk
	last   string // the largest bound instance: the walk stops once past it
	count  int
}

type use struct {
	tag, member string
	field       hw.Field
	binding     hw.Binding
	counter     *hw.Counter // non-nil for rate bindings
}

// New validates the manifest offline and builds the driver; nothing is
// dialled until Start. Credential variables need not be set here — that is
// a Warnings() condition, reported by `naut check`, never an error.
func New(m Manifest, opts ...Option) (*Driver, error) {
	d := &Driver{
		manifest:   m,
		log:        slog.Default(),
		dial:       Dial,
		scanRate:   hw.DefaultInterval,
		classRates: map[string]time.Duration{},
		sources:    map[string]*source{},
		writes:     map[string]Write{},
	}
	for _, o := range opts {
		o(d)
	}
	if err := m.Validate(); err != nil {
		return nil, fmt.Errorf("snmp: %w", err)
	}

	cfg := hw.Config{
		Kind:        "snmp",
		ScanRate:    d.scanRate,
		ClassRates:  d.classRates,
		Assignments: d.assignments,
		Poll:        d.poll,
		Log:         d.log,
		// Back from an outage or a park: every rate restarts, none is
		// computed across a gap the driver never saw.
		OnReconnect: func(id string) {
			for _, c := range d.sources[id].counters {
				c.Reset()
			}
		},
	}
	for _, s := range m.Sources {
		cfg.Sources = append(cfg.Sources, hw.SourceConfig{
			ID: s.ID, Addr: s.Addr(), Interval: s.Interval, StaleAfter: s.StaleAfter, Enable: s.Enable,
		})
		d.sources[s.ID] = &source{cfg: s, plans: map[string]*classPlan{}, counters: map[string]*hw.Counter{}, warned: map[string]bool{}, perClass: map[string]int{}}
	}
	byTag := map[string]Tag{}
	unbound := map[string]map[string]map[string]bool{} // source → type → member
	for _, t := range m.Tags {
		ty, _ := hw.TypeByName(t.Type)
		for _, f := range ty.Fields {
			if f.Name == "Online" {
				continue // hw.Base mirrors the source's freshness into an unbound Online
			}
			if _, ok := t.Members[f.Name]; !ok {
				if unbound[t.Source] == nil {
					unbound[t.Source] = map[string]map[string]bool{}
				}
				if unbound[t.Source][t.Type] == nil {
					unbound[t.Source][t.Type] = map[string]bool{}
				}
				unbound[t.Source][t.Type][f.Name] = true
			}
		}
		td := hw.TagDecl{Name: t.Name, Type: t.Type, Source: t.Source, Members: map[string]hw.Binding{}}
		for name, mb := range t.Members {
			td.Members[name] = mb.Binding
		}
		cfg.Tags = append(cfg.Tags, td)
		byTag[t.Name] = t
	}
	for _, w := range m.Writes {
		cfg.Writes = append(cfg.Writes, hw.WriteDecl{Name: w.Name, Tag: w.Tag, Member: w.Member})
		d.writes[w.Name] = w
	}
	if len(m.Writes) > 0 {
		cfg.Write = d.write
	}
	base, err := hw.NewBase(cfg)
	if err != nil {
		return nil, err
	}
	d.base = base

	// Plan each (source, class) from the classes Base resolved — the one
	// place the scan-class policy (binding scan-class:, WithTagClass) is
	// decided, so the planner cannot disagree with Quality's bookkeeping.
	for class, paths := range base.ScanClasses() {
		for _, p := range paths {
			tagName, member, _ := strings.Cut(p, ".")
			t := byTag[tagName]
			mb := t.Members[member]
			src := d.sources[t.Source]
			plan := src.plans[class]
			if plan == nil {
				plan = &classPlan{uses: map[string][]use{}}
				src.plans[class] = plan
			}
			oid, _ := walk.ParseOID(mb.OID)
			_, f, _ := hw.FieldOf(t.Type, member)
			u := use{tag: tagName, member: member, field: f, binding: mb.Binding}
			if mb.Rate {
				u.counter = &hw.Counter{}
				src.counters[p] = u.counter
			}
			plan.uses[oid] = append(plan.uses[oid], u)
		}
	}
	for id, src := range d.sources {
		for _, plan := range src.plans {
			plan.build()
		}
		var parts []string
		for _, typ := range sortedKeys(unbound[id]) {
			parts = append(parts, typ+": "+strings.Join(sortedKeys(unbound[id][typ]), ", "))
		}
		src.unbound = strings.Join(parts, "; ")
	}
	return d, nil
}

// build splits a class's OIDs into column walks and one Get list.
func (p *classPlan) build() {
	byParent := map[string][]string{}
	tags := map[string]bool{}
	for oid, uses := range p.uses {
		for _, u := range uses {
			tags[u.tag] = true
		}
		parent := walk.Parent(oid)
		if strings.HasSuffix(oid, ".0") {
			parent = "" // a scalar is never walked
		}
		byParent[parent] = append(byParent[parent], oid)
	}
	for parent, oids := range byParent {
		sort.Slice(oids, func(i, j int) bool { return walk.Compare(oids[i], oids[j]) < 0 })
		if parent == "" || len(oids) < walkThreshold {
			p.gets = append(p.gets, oids...)
			continue
		}
		p.columns = append(p.columns, column{prefix: parent, last: oids[len(oids)-1], count: len(oids)})
	}
	sort.Slice(p.gets, func(i, j int) bool { return walk.Compare(p.gets[i], p.gets[j]) < 0 })
	sort.Slice(p.columns, func(i, j int) bool { return walk.Compare(p.columns[i].prefix, p.columns[j].prefix) < 0 })
	for t := range tags {
		p.tags = append(p.tags, t)
	}
	sort.Strings(p.tags)
}

// ── io.Driver surface (forwarded to hw.Base) ────────────────────────────

// Start launches one poll loop per source. Idempotent.
func (d *Driver) Start(ctx context.Context) { d.base.Start(ctx) }

// Stop ends every loop, waits for them, and closes the sessions.
func (d *Driver) Stop() {
	d.base.Stop()
	for _, s := range d.sources {
		if s.sess != nil {
			_ = s.sess.Close()
			s.sess = nil
		}
	}
}

// ReadInputs returns the latest snapshot; never blocks on the network and
// never errors.
func (d *Driver) ReadInputs() (nio.Values, error) { return d.base.ReadInputs() }

// ReadInputsInto refills dst (io.BatchReader).
func (d *Driver) ReadInputsInto(dst nio.Values) error { return d.base.ReadInputsInto(dst) }

// WriteOutputs queues changed commands and applies Enable tags. The first
// value per command is a baseline, never written.
func (d *Driver) WriteOutputs(v nio.Values) error { return d.base.WriteOutputs(v) }

// Quality reports the non-Good tags (io.QualityReporter).
func (d *Driver) Quality() map[string]nio.Quality { return d.base.Quality() }

// InputNames lists the struct tags and the per-source companions.
func (d *Driver) InputNames() []string { return d.base.InputNames() }

// OutputNames lists the command tags and Enable tags.
func (d *Driver) OutputNames() []string { return d.base.OutputNames() }

// ScanClasses maps each class to the "Tag.Member" paths it polls.
func (d *Driver) ScanClasses() map[string][]string { return d.base.ScanClasses() }

// SetWriteGate installs the leadership gate for commands.
func (d *Driver) SetWriteGate(gate func() bool) { d.base.SetWriteGate(gate) }

// StructDefs is the contract type set.
func (d *Driver) StructDefs() map[string]*ir.StructDef { return d.base.StructDefs() }

// Health is one row per source plus totals.
func (d *Driver) Health() hw.Health { return d.base.Health() }

// Warnings are `naut check`'s non-fatal findings: unset credential
// variables, missing credential files, MD5/DES.
func (d *Driver) Warnings() []string { return d.manifest.Warnings() }

// Plan describes the requests one poll of each (source, class) sends, for
// `naut snmp import --plan` and the tests.
func (d *Driver) Plan() string {
	var b strings.Builder
	ids := sortedKeys(d.sources)
	for _, id := range ids {
		s := d.sources[id]
		for _, class := range sortedKeys(s.plans) {
			p := s.plans[class]
			fmt.Fprintf(&b, "%s/%s: get %d oid(s), walk %d column(s)\n", id, class, len(p.gets), len(p.columns))
			for _, c := range p.columns {
				fmt.Fprintf(&b, "  walk %s (%d rows bound, max-repetitions %d)\n", c.prefix, c.count, s.cfg.maxRepetitions())
			}
		}
	}
	return b.String()
}

// ── poll ─────────────────────────────────────────────────────────────────

// session returns the source's session, dialling on first use or after a
// transport failure. Dialling resets no rate counter: a redial after one
// class failed is no gap for the classes that answered meanwhile. The class
// that failed resets its own (resetClass); a whole source back from an
// outage or a park resets all (Config.OnReconnect).
func (d *Driver) session(ctx context.Context, s *source) (Getter, error) {
	if s.sess != nil {
		return s.sess, nil
	}
	g, err := d.dial(ctx, s.cfg)
	if err != nil {
		return nil, err
	}
	s.sess = g
	return g, nil
}

// resetClass restarts the rates of one class whose poll failed: its next
// sample follows a gap it never observed.
func resetClass(plan *classPlan) {
	for _, uses := range plan.uses {
		for _, u := range uses {
			if u.counter != nil {
				u.counter.Reset()
			}
		}
	}
}

// drop closes a session after a transport failure, so the next poll dials
// afresh (a v3 engine that rebooted needs rediscovery; a v2c socket that
// saw ICMP unreachable is poisoned on some kernels).
func (s *source) drop() {
	if s.sess != nil {
		_ = s.sess.Close()
		s.sess = nil
	}
}

// poll is hw.PollFunc: fetch one scan class of one source.
func (d *Driver) poll(ctx context.Context, sourceID, class string) (hw.Result, error) {
	s := d.sources[sourceID]
	plan := s.plans[class]
	if plan == nil {
		return hw.Result{}, nil
	}
	g, err := d.session(ctx, s)
	if err != nil {
		return hw.Result{}, err
	}
	if !s.reported && s.unbound != "" {
		s.reported = true
		d.log.Info("snmp: members no binding fills stay at zero", "source", sourceID, "unbound", s.unbound)
	}
	got := map[string]sample{}
	bad := map[string]bool{}
	markBad := func(oids []string, why error) {
		for _, oid := range oids {
			for _, u := range plan.uses[oid] {
				bad[u.tag] = true
			}
		}
		d.warnOnce(s, "status:"+why.Error(), "snmp: request refused; its tags are Bad", "source", sourceID, "class", class, "error", why)
	}
	requests := 0

	// Columns: one GetBulk walk each, from the column OID to the last bound
	// instance or the end of the column, whichever comes first.
	for _, c := range plan.columns {
		n, err := d.walkColumn(ctx, g, s, c, got)
		requests += n
		if err != nil {
			var se *StatusError
			if errors.As(err, &se) {
				markBad(columnOIDs(plan, c.prefix), err)
				continue
			}
			s.drop()
			resetClass(plan)
			return hw.Result{}, err
		}
	}
	// Everything else in one Get (chunked by the session).
	if len(plan.gets) > 0 {
		vbs, err := g.Get(ctx, plan.gets)
		requests += (len(plan.gets) + maxOidsPerGet - 1) / maxOidsPerGet
		if err != nil {
			var se *StatusError
			if !errors.As(err, &se) {
				s.drop()
				resetClass(plan)
				return hw.Result{}, err
			}
			markBad(plan.gets, err)
		} else {
			at := time.Now()
			for _, vb := range vbs {
				got[vb.OID] = sample{vb, at}
			}
		}
	}

	s.perClass[class] = requests

	res := hw.Result{Requests: requests}
	for _, oid := range sortedKeys(plan.uses) {
		uses := plan.uses[oid]
		smp, present := got[oid]
		vb := smp.vb
		if !present || vb.Type.IsException() || vb.Type == walk.Null {
			what := "absent from the walk"
			if present {
				what = vb.Type.String()
			}
			for _, u := range uses {
				if !bad[u.tag] {
					d.warnOnce(s, "missing:"+oid, "snmp: bound OID has no value; its tag is Bad", "source", sourceID, "tag", u.tag, "member", u.member, "oid", oid, "answer", what)
				}
				bad[u.tag] = true
			}
			continue
		}
		raw, _, err := RawOf(vb)
		if err != nil {
			for _, u := range uses {
				bad[u.tag] = true
			}
			d.warnOnce(s, "decode:"+oid, "snmp: cannot decode", "source", sourceID, "oid", oid, "error", err)
			continue
		}
		for _, u := range uses {
			in := raw
			if u.binding.Ports != nil {
				if pl, isOctets := PortListRaw(vb); isOctets {
					in = pl
				}
			}
			v, ok, err := u.binding.Apply(u.field, in, u.counter, smp.at)
			if err != nil {
				bad[u.tag] = true
				d.warnOnce(s, "apply:"+u.tag+"."+u.member, "snmp: binding cannot use the wire value; its tag is Bad", "source", sourceID, "tag", u.tag, "member", u.member, "oid", oid, "error", err)
				continue
			}
			if ok {
				res.Updates = append(res.Updates, hw.Update{Tag: u.tag, Member: u.member, Value: v})
			}
		}
	}
	for _, t := range plan.tags {
		if bad[t] {
			res.Bad = append(res.Bad, t)
		}
	}
	return res, nil
}

// sample is one varbind and the moment its response arrived. A rate is
// computed against THAT time, not the end of the poll: a 28-port poll is
// two dozen round trips, and stamping every counter with the poll's end
// time skews each rate by however far into the poll its column was read —
// ±30% at a 50 ms interval under load (the driver test caught it).
type sample struct {
	vb walk.Varbind
	at time.Time
}

// maxOidsPerGet mirrors gosnmp.MaxOids (60), the chunk size the session
// splits a Get into — kept here so the request count is right for any
// Getter.
const maxOidsPerGet = 60

func columnOIDs(p *classPlan, prefix string) []string {
	var out []string
	for oid := range p.uses {
		if walk.HasPrefix(oid, prefix) && !strings.HasSuffix(oid, ".0") {
			out = append(out, oid)
		}
	}
	return out
}

// walkColumn GetBulks one column into got. It stops at the first varbind
// past the column (the next column's first row, or endOfMibView — both are
// how an agent says "table end"), or once past the last bound instance, so
// a 28-port walk costs ceil(rows/max-repetitions) PDUs and not one more.
// tooBig halves max-repetitions and retries: an agent whose response would
// not fit a datagram is answering, not failing.
func (d *Driver) walkColumn(ctx context.Context, g Getter, s *source, c column, got map[string]sample) (int, error) {
	maxRep := s.cfg.maxRepetitions()
	cur := c.prefix
	requests := 0
	for {
		vbs, err := g.GetBulk(ctx, cur, maxRep)
		requests++
		if err != nil {
			var se *StatusError
			if errors.As(err, &se) && se.Status == 1 && maxRep > 1 { // tooBig
				maxRep /= 2
				continue
			}
			return requests, err
		}
		if len(vbs) == 0 {
			return requests, nil
		}
		at := time.Now()
		for _, vb := range vbs {
			if vb.Type == walk.EndOfMibView || !walk.HasPrefix(vb.OID, c.prefix) {
				return requests, nil
			}
			if walk.Compare(vb.OID, cur) <= 0 {
				return requests, &StatusError{Status: 5, OID: vb.OID} // genErr: a non-increasing walk would loop forever
			}
			got[vb.OID] = sample{vb, at}
			cur = vb.OID
			if walk.Compare(cur, c.last) >= 0 {
				return requests, nil
			}
		}
	}
}

func (d *Driver) warnOnce(s *source, key, msg string, args ...any) {
	if s.warned[key] {
		return
	}
	s.warned[key] = true
	d.log.Warn(msg, args...)
}

// ── write ────────────────────────────────────────────────────────────────

// write is hw.WriteFunc: one SetRequest per changed command. The value is
// mapped through Set ({true: 1, false: 2}) when the write declares one;
// otherwise an INT is written as itself and a BOOL as 1/0.
func (d *Driver) write(ctx context.Context, sourceID string, w hw.WriteDecl, v ir.Value) error {
	wr, ok := d.writes[w.Name]
	if !ok {
		return fmt.Errorf("unknown write %s", w.Name)
	}
	var wire int64
	key := valueKey(v)
	if len(wr.Set) > 0 {
		n, ok := wr.Set[key]
		if !ok {
			return fmt.Errorf("write %s: value %s has no set: entry (have %s)", w.Name, key, strings.Join(sortedKeys(wr.Set), ", "))
		}
		wire = n
	} else {
		switch v.Kind {
		case ir.TypeBool:
			if v.B {
				wire = 1
			}
		case ir.TypeInt:
			wire = v.I
		default:
			return fmt.Errorf("write %s: a %s command needs set: to name the wire integer", w.Name, v.Kind)
		}
	}
	oid, _ := walk.ParseOID(wr.OID)
	s := d.sources[sourceID]
	g, err := d.session(ctx, s)
	if err != nil {
		return err
	}
	if err := g.Set(ctx, walk.Varbind{OID: oid, Type: walk.Integer, Int: wire}); err != nil {
		var se *StatusError
		if !errors.As(err, &se) {
			s.drop()
		}
		return err
	}
	d.log.Info("snmp: command written", "source", sourceID, "tag", w.Name, "oid", oid, "value", wire)
	return nil
}

func valueKey(v ir.Value) string {
	switch v.Kind {
	case ir.TypeBool:
		if v.B {
			return "true"
		}
		return "false"
	case ir.TypeInt:
		return fmt.Sprint(v.I)
	case ir.TypeReal:
		return fmt.Sprint(v.F)
	}
	return v.S
}

// ensure the driver satisfies the runtime's interfaces.
var (
	_ nio.Driver          = (*Driver)(nil)
	_ nio.BatchReader     = (*Driver)(nil)
	_ nio.QualityReporter = (*Driver)(nil)
)
