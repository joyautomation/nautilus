// driver.go is the io.Driver surface of the Redfish driver: construction
// (New, Option), the poll and write functions hw.Base runs, and the
// forwarding of the io.Driver methods to that Base. docs/design/it-drivers.md
// §1, §3.2, §4.
//
// The driver is a dumb executor of explicit bindings (brief §3): a member
// names a resource and a path in it, and nothing here knows what a
// Thermal resource is. A poll of one (source, scan class) fetches every
// resource that class's members name — each ONCE, however many members
// read it — and decodes every member from its one body. Schema knowledge
// (legacy Thermal/Power versus ThermalSubsystem/PowerSubsystem, which
// sensor is the inlet) lives in codegen, where `naut redfish import`
// turns it into those explicit bindings.
//
// New NEVER dials, and never needs the password: `naut check` and `build`
// run it in CI with no BMC and no secrets. The HTTP client is built on the
// source's own goroutine at its first poll.
package redfish

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/joyautomation/nautilus/hw"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/redfish/mockup"
)

// Kind is the driver.type and the hw.Base kind.
const Kind = "redfish"

// Option configures New.
type Option func(*Driver)

// WithScanRate sets the default class's poll interval (default 10s — BMCs
// rate-limit; §12.4). A poll INTERVAL, independent of the program's scan.
func WithScanRate(r time.Duration) Option { return func(d *Driver) { d.scanRate = r } }

// WithScanClass defines a named scan class and its poll interval.
func WithScanClass(name string, rate time.Duration) Option {
	return func(d *Driver) { d.classRates[name] = rate }
}

// WithTagClass assigns members to a class by glob over "Tag.Member" paths
// or bare tag names; the last matching assignment wins (hw.ClassAssignment).
// Policy lives here, not in the generated manifest, so a re-import never
// erases it.
func WithTagClass(class string, patterns ...string) Option {
	return func(d *Driver) {
		d.assign = append(d.assign, hw.ClassAssignment{Class: class, Patterns: patterns})
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

// WithFetcher substitutes the transport per source — tests hand the driver
// a fake with no socket, or a client pointed at an in-process mockup.
func WithFetcher(f func(Source) (Fetcher, error)) Option {
	return func(d *Driver) { d.newFetcher = f }
}

// WithClock substitutes the wall clock rate members are computed against
// (tests step it deterministically; brief §5 says rates use wall time).
func WithClock(now func() time.Time) Option { return func(d *Driver) { d.now = now } }

// Driver polls a set of BMCs. io.Driver, io.BatchReader and
// io.QualityReporter through hw.Base.
type Driver struct {
	m          Manifest
	base       *hw.Base
	log        *slog.Logger
	scanRate   time.Duration
	classRates map[string]time.Duration
	assign     []hw.ClassAssignment
	newFetcher func(Source) (Fetcher, error)
	now        func() time.Time

	srcs   map[string]*source
	writes map[string]Write
}

// source is one BMC's poll state. Everything but mu's fields is touched
// only by that source's loop goroutine (Base runs a source's polls and
// writes sequentially on it).
type source struct {
	cfg      Source
	plans    map[string]*classPlan
	counters map[string]*hw.Counter
	reset    bool            // a transport failure happened: forget counter history
	logged   map[string]bool // one log line per (source, condition)

	// mu guards what other goroutines read: the fetcher (AuthMode, Stop),
	// the absent set (Absent) and the latched write refusal (Health).
	mu      sync.Mutex
	fetcher Fetcher
	absent  map[string]bool // "Tag.Member" whose path resolved to nothing last poll
	// writeErr is the last refused command, latched until a later command
	// on this BMC is accepted. hw.Base clears its row error on the next
	// good poll, so without the latch a refusal can vanish from Health()
	// within one interval, before a dashboard or `naut check` looks.
	writeErr error
}

// classPlan is one (source, class) poll: the resources to fetch, in
// order, and what to decode from each.
type classPlan struct {
	resources []string
	members   map[string][]*memberPlan // resource → members read from it
	tags      map[string][]string      // resource → tags reading it (Bad on failure)
}

type memberPlan struct {
	tag, member string
	key         string // "Tag.Member"
	field       hw.Field
	b           MemberBinding
	path        *Path
}

// powerCmdCarrier: hw.WriteDecl coerces a command value to its target
// member's kind, and Server's read-back member PowerOn is a BOOL — which
// would flatten ForceOff(3) to true. The INT reset command therefore rides
// on an INT member's slot kind; the member itself is never written (the
// struct tag is an input). See the report to the integrator: hw.WriteDecl
// wants its own Kind.
const powerCmdCarrier = "Health"

// New validates the manifest offline and builds the driver. It never dials.
func New(m Manifest, opts ...Option) (*Driver, error) {
	d := &Driver{
		m:          m,
		log:        slog.Default(),
		classRates: map[string]time.Duration{},
		now:        time.Now,
		srcs:       map[string]*source{},
		writes:     map[string]Write{},
	}
	for _, o := range opts {
		o(d)
	}
	if d.newFetcher == nil {
		d.newFetcher = func(s Source) (Fetcher, error) { return NewClient(s) }
	}
	if err := m.Validate(); err != nil {
		return nil, fmt.Errorf("redfish: %w", err)
	}

	cfg := hw.Config{
		Kind:        Kind,
		ScanRate:    d.scanRate,
		ClassRates:  d.classRates,
		Assignments: d.assign,
		Poll:        d.poll,
		Log:         d.log,
	}
	for _, s := range m.Sources {
		cfg.Sources = append(cfg.Sources, hw.SourceConfig{
			ID: s.ID, Addr: s.Host, Interval: s.Interval, StaleAfter: s.StaleAfter, Enable: s.Enable,
		})
		d.srcs[s.ID] = &source{
			cfg:      s,
			plans:    map[string]*classPlan{},
			counters: map[string]*hw.Counter{},
			logged:   map[string]bool{},
			absent:   map[string]bool{},
		}
	}
	tags := map[string]Tag{}
	for _, t := range m.Tags {
		tags[t.Name] = t
		td := hw.TagDecl{Name: t.Name, Type: t.Type, Source: t.Source, Members: map[string]hw.Binding{}}
		for name, mb := range t.Members {
			td.Members[name] = mb.Binding
		}
		cfg.Tags = append(cfg.Tags, td)
	}
	if len(m.Writes) > 0 {
		cfg.Write = d.write
	}
	for _, w := range m.Writes {
		d.writes[w.Name] = w
		cfg.Writes = append(cfg.Writes, hw.WriteDecl{Name: w.Name, Tag: w.Tag, Member: powerCmdCarrier})
	}
	base, err := hw.NewBase(cfg)
	if err != nil {
		return nil, err
	}
	d.base = base

	// Plan each (source, class) from Base's own class routing, so the
	// class a member polls in is decided in exactly one place.
	for class, paths := range base.ScanClasses() {
		for _, p := range paths {
			tagName, member, _ := strings.Cut(p, ".")
			t := tags[tagName]
			mb := t.Members[member]
			_, f, _ := hw.FieldOf(t.Type, member)
			path, _ := ParsePath(mb.Path) // validated above
			s := d.srcs[t.Source]
			cp := s.plans[class]
			if cp == nil {
				cp = &classPlan{members: map[string][]*memberPlan{}, tags: map[string][]string{}}
				s.plans[class] = cp
			}
			res := mb.resourceOf(t)
			if _, seen := cp.members[res]; !seen {
				cp.resources = append(cp.resources, res)
			}
			cp.members[res] = append(cp.members[res], &memberPlan{tag: tagName, member: member, key: p, field: f, b: mb, path: path})
			if !containsStr(cp.tags[res], tagName) {
				cp.tags[res] = append(cp.tags[res], tagName)
			}
		}
	}
	for _, s := range d.srcs {
		for _, cp := range s.plans {
			sort.Strings(cp.resources)
		}
	}
	return d, nil
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// ── io.Driver surface (forwarded to hw.Base) ──────────────────────────────

// Start launches one poll loop per source.
func (d *Driver) Start(ctx context.Context) { d.base.Start(ctx) }

// Stop ends the loops, then logs every session out — after the loops, so
// no poll races the logout.
func (d *Driver) Stop() {
	d.base.Stop()
	for _, s := range d.srcs {
		s.mu.Lock()
		f := s.fetcher
		s.fetcher = nil
		s.mu.Unlock()
		if f != nil {
			f.Close(context.Background())
		}
	}
}

// ReadInputs returns the latest snapshot (never blocks on a BMC).
func (d *Driver) ReadInputs() (nio.Values, error) { return d.base.ReadInputs() }

// ReadInputsInto refills dst with the latest snapshot.
func (d *Driver) ReadInputsInto(dst nio.Values) error { return d.base.ReadInputsInto(dst) }

// WriteOutputs routes Enable tags and PowerCmd commands (baseline first,
// then only changes — hw.Base.WriteOutputs).
func (d *Driver) WriteOutputs(v nio.Values) error { return d.base.WriteOutputs(v) }

// Quality reports the non-Good verdicts per tag.
func (d *Driver) Quality() map[string]nio.Quality { return d.base.Quality() }

// InputNames lists the struct tags and the per-source companions.
func (d *Driver) InputNames() []string { return d.base.InputNames() }

// OutputNames lists the command tags and Enable tags.
func (d *Driver) OutputNames() []string { return d.base.OutputNames() }

// ScanClasses maps each class to its "Tag.Member" paths.
func (d *Driver) ScanClasses() map[string][]string { return d.base.ScanClasses() }

// SetWriteGate installs the leadership gate for commands.
func (d *Driver) SetWriteGate(g func() bool) { d.base.SetWriteGate(g) }

// StructDefs is the contract type set.
func (d *Driver) StructDefs() map[string]*ir.StructDef { return d.base.StructDefs() }

// Health is one row per BMC plus driver totals. A row with no current
// error carries the last refused command instead (see source.writeErr):
// a poll or transport error is the more pressing news, and it wins.
func (d *Driver) Health() hw.Health {
	h := d.base.Health()
	for i := range h.Sources {
		row := &h.Sources[i]
		if row.LastError != "" {
			continue
		}
		s, ok := d.srcs[row.ID]
		if !ok {
			continue
		}
		s.mu.Lock()
		if s.writeErr != nil {
			row.LastError = s.writeErr.Error()
		}
		s.mu.Unlock()
	}
	return h
}

// Manifest returns the manifest the driver was built from.
func (d *Driver) Manifest() Manifest { return d.m }

// Absent lists the "Tag.Member" bindings whose path resolved to nothing on
// their last poll (a null Reading, a property this firmware lacks),
// sorted. The import-then-verify check: after one poll of a freshly
// generated manifest this should be empty.
func (d *Driver) Absent() []string {
	var out []string
	for _, s := range d.srcs {
		s.mu.Lock()
		for k, v := range s.absent {
			if v {
				out = append(out, k)
			}
		}
		s.mu.Unlock()
	}
	sort.Strings(out)
	return out
}

// ── poll ─────────────────────────────────────────────────────────────────

// transport returns the source's fetcher, building it on first use (on the
// loop goroutine — New must not read a CA file a laptop does not have).
func (d *Driver) transport(s *source) (Fetcher, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fetcher != nil {
		return s.fetcher, nil
	}
	f, err := d.newFetcher(s.cfg)
	if err != nil {
		return nil, err
	}
	s.fetcher = f
	return f, nil
}

// poll is the hw.PollFunc: fetch each resource of (source, class) once and
// decode every member bound to it.
func (d *Driver) poll(ctx context.Context, sourceID, class string) (hw.Result, error) {
	s := d.srcs[sourceID]
	cp := s.plans[class]
	if cp == nil {
		return hw.Result{}, nil
	}
	f, err := d.transport(s)
	if err != nil {
		return hw.Result{}, err
	}
	if s.reset {
		// A reconnect: the counters' last samples straddle an outage we did
		// not observe, so the first poll back yields no rate (brief §5).
		for _, c := range s.counters {
			c.Reset()
		}
		s.reset = false
	}
	now := d.now()
	var res hw.Result
	bad := map[string]bool{}
	for _, uri := range cp.resources {
		resp, err := f.Get(ctx, uri)
		if err != nil {
			s.reset = true
			return hw.Result{}, fmt.Errorf("GET %s: %w", uri, err)
		}
		var doc map[string]any
		if resp.OK() {
			doc, err = mockup.Decode(resp.Body)
			if err != nil {
				d.once(s, "json:"+uri, "redfish: resource is not JSON; its tags read Bad", "resource", uri, "error", err)
			}
		} else {
			msg := redfishMessage(resp.Body)
			d.once(s, "status:"+uri+":"+strconv.Itoa(resp.Status), "redfish: resource refused; its tags read Bad", "resource", uri, "status", resp.Status, "message", msg)
		}
		if doc == nil {
			for _, t := range cp.tags[uri] {
				bad[t] = true
			}
			continue
		}
		for _, mp := range cp.members[uri] {
			if v, ok := d.decode(s, mp, doc, now); ok {
				res.Updates = append(res.Updates, hw.Update{Tag: mp.tag, Member: mp.member, Value: v})
			}
		}
	}
	for t := range bad {
		res.Bad = append(res.Bad, t)
	}
	sort.Strings(res.Bad)
	return res, nil
}

// decode reads one member from its resource's body. ok false leaves the
// member as it was: a path that resolves to nothing (logged once per
// source, listed by Absent), a value its binding cannot take.
func (d *Driver) decode(s *source, mp *memberPlan, doc map[string]any, now time.Time) (ir.Value, bool) {
	vals, err := mp.path.Eval(doc)
	if err != nil {
		d.once(s, "path:"+mp.key, "redfish: binding does not resolve", "source", s.cfg.ID, "member", mp.key, "error", err)
		s.setAbsent(mp.key, true)
		return ir.Value{}, false
	}
	if mp.b.Exists != nil {
		s.setAbsent(mp.key, false)
		return ir.BoolVal((len(vals) > 0) == *mp.b.Exists), true
	}
	if len(vals) == 0 {
		// Brief §4: a member the answered resource does not carry stays
		// zero-of-field — one dead sensor must not grey out the server.
		d.once(s, "absent:"+mp.key, "redfish: member absent from its resource (left at its last value)", "source", s.cfg.ID, "member", mp.key, "path", mp.path.String())
		s.setAbsent(mp.key, true)
		return ir.Value{}, false
	}
	s.setAbsent(mp.key, false)
	var raw hw.Raw
	var ok bool
	if mp.b.Agg != "" {
		raw, ok = aggregate(vals, mp.b.Agg)
	} else {
		raw, ok = toRaw(vals[0])
	}
	if !ok {
		d.once(s, "shape:"+mp.key, "redfish: member's path reaches an object or array, not a value", "source", s.cfg.ID, "member", mp.key, "path", mp.path.String())
		return ir.Value{}, false
	}
	var c *hw.Counter
	if mp.b.Rate {
		c = s.counters[mp.key]
		if c == nil {
			c = &hw.Counter{}
			s.counters[mp.key] = c
		}
	}
	v, ok, err := mp.b.Binding.Apply(mp.field, raw, c, now)
	if err != nil {
		d.once(s, "apply:"+mp.key+":"+raw.Key(), "redfish: value does not fit its binding", "source", s.cfg.ID, "member", mp.key, "error", err)
		return ir.Value{}, false
	}
	return v, ok
}

func (s *source) setAbsent(key string, v bool) {
	s.mu.Lock()
	s.absent[key] = v
	s.mu.Unlock()
}

// once logs a condition the first time it happens on a source.
func (d *Driver) once(s *source, key, msg string, args ...any) {
	if s.logged[key] {
		return
	}
	s.logged[key] = true
	d.log.Warn(msg, append([]any{"source", s.cfg.ID}, args...)...)
}

// toRaw turns one JSON scalar into the wire value hw.Binding.Apply takes.
// Integers stay integers (a counter must not lose precision through a
// float), everything else keeps its JSON kind.
func toRaw(v any) (hw.Raw, bool) {
	switch x := v.(type) {
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return hw.RawIntVal(i), true
		}
		if u, err := strconv.ParseUint(x.String(), 10, 64); err == nil {
			return hw.RawUintVal(u), true
		}
		f, err := x.Float64()
		if err != nil {
			return hw.Raw{}, false
		}
		return hw.RawFloatVal(f), true
	case float64:
		return hw.RawFloatVal(x), true
	case string:
		return hw.RawStringVal(x), true
	case bool:
		return hw.RawBoolVal(x), true
	}
	return hw.Raw{}, false
}

// aggregate folds a [*] path's values. Non-numeric values are skipped by
// max/min/sum and counted by count; no numeric value at all is no value.
func aggregate(vals []any, agg string) (hw.Raw, bool) {
	if agg == "count" {
		return hw.RawIntVal(int64(len(vals))), true
	}
	acc, n := 0.0, 0
	for _, v := range vals {
		r, ok := toRaw(v)
		if !ok {
			continue
		}
		var f float64
		switch r.Kind {
		case hw.RawInt:
			f = float64(r.I)
		case hw.RawUint:
			f = float64(r.U)
		case hw.RawFloat:
			f = r.F
		default:
			continue
		}
		switch {
		case n == 0:
			acc = f
		case agg == "max":
			acc = math.Max(acc, f)
		case agg == "min":
			acc = math.Min(acc, f)
		default:
			acc += f
		}
		n++
	}
	if n == 0 {
		return hw.Raw{}, false
	}
	return hw.RawFloatVal(acc), true
}

// ── write ────────────────────────────────────────────────────────────────

// write is the hw.WriteFunc: a PowerCmd change reaches the wire as ONE
// ComputerSystem.Reset POST, and only for a non-zero value — the program
// returns the command to 0 afterwards, and that return is not a command.
// A refusal is latched on the source for Health(); an accepted POST
// clears it. A return to 0 sends nothing and leaves the latch alone.
func (d *Driver) write(ctx context.Context, sourceID string, w hw.WriteDecl, v ir.Value) error {
	sent, err := d.sendWrite(ctx, sourceID, w, v)
	if s, ok := d.srcs[sourceID]; ok && (err != nil || sent) {
		s.mu.Lock()
		s.writeErr = err
		s.mu.Unlock()
	}
	return err
}

// sendWrite does write's work; sent reports that a POST was accepted.
func (d *Driver) sendWrite(ctx context.Context, sourceID string, w hw.WriteDecl, v ir.Value) (sent bool, err error) {
	wb, ok := d.writes[w.Name]
	if !ok {
		return false, fmt.Errorf("no write binding %q", w.Name)
	}
	if v.I == 0 {
		return false, nil
	}
	rt, ok := ResetTypes[v.I]
	if !ok {
		return false, fmt.Errorf("%s = %d: want 0 (none), 1 On, 2 GracefulShutdown, 3 ForceOff or 4 GracefulRestart", w.Name, v.I)
	}
	f, err := d.transport(d.srcs[sourceID])
	if err != nil {
		return false, err
	}
	resp, err := f.Post(ctx, wb.Target, map[string]string{"ResetType": rt})
	if err != nil {
		return false, err
	}
	if !resp.OK() {
		msg := redfishMessage(resp.Body)
		if msg == "" {
			msg = strings.TrimSpace(string(resp.Body))
			if len(msg) > 200 {
				msg = msg[:200]
			}
		}
		return false, fmt.Errorf("POST %s ResetType=%s: HTTP %d: %s", wb.Target, rt, resp.Status, msg)
	}
	d.log.Info("redfish: reset sent", "source", sourceID, "tag", w.Name, "resetType", rt, "target", wb.Target)
	return true, nil
}

// AuthMode reports what a source's client is using (session, basic, none)
// once it has talked to the BMC, for `browse` and the status row.
func (d *Driver) AuthMode(sourceID string) string {
	s, ok := d.srcs[sourceID]
	if !ok {
		return ""
	}
	s.mu.Lock()
	f := s.fetcher
	s.mu.Unlock()
	if c, ok := f.(*Client); ok {
		return c.AuthMode()
	}
	return ""
}
