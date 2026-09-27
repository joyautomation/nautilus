package hw

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

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
)

// Defaults per source (docs/design/it-drivers.md §4). Intervals are
// SECONDS: a switch's own counters move every few seconds and a BMC
// rate-limits, so the default is nowhere near a control scan rate.
const (
	DefaultInterval = 10 * time.Second
	DefaultRetryMin = time.Second
	DefaultRetryMax = 60 * time.Second
	// DefaultClass is the scan class a member with no class belongs to;
	// the same name modbus uses, so `scan-classes:` reads the same way.
	DefaultClass = "default"
	// failuresToError is how many CONSECUTIVE failed polls on a source that
	// has answered before move it to the error state and onto the backoff
	// ladder. One lost UDP datagram is not an outage; three intervals of
	// silence are. A source that has NEVER answered errors on the first
	// failure, like a dial that failed.
	failuresToError = 3
)

// Companion suffixes: the per-source tags Base synthesises beside the
// bindings — the same __Online contract modbus and sparkplug-host keep, plus
// the poll clock a program can watch to know a rate member is meaningful.
const (
	suffixOnline   = "__Online"
	suffixLastPoll = "__LastPollMs"
)

// OnlineTagName is the source's "<id>__Online" BOOL companion.
func OnlineTagName(sourceID string) string { return sourceID + suffixOnline }

// LastPollTagName is the source's "<id>__LastPollMs" DINT companion: the
// wall-clock epoch ms of its last complete poll, 0 before the first.
func LastPollTagName(sourceID string) string { return sourceID + suffixLastPoll }

// SourceConfig is one polled device, the protocol-neutral half of a
// manifest sources: entry (the protocol keeps its own transport settings
// beside it).
type SourceConfig struct {
	ID   string
	Addr string // for status rows: "192.0.2.2:161", "https://bmc1"
	// Interval is the default class's poll period for THIS source; 0 takes
	// Config.ScanRate. Named classes come from Config.ClassRates.
	Interval time.Duration
	RetryMin time.Duration // backoff floor (default 1s)
	RetryMax time.Duration // backoff ceiling (default 60s)
	// StaleAfter: a source whose last complete poll is older than this
	// reports Stale even in the connected state (default 3×Interval).
	StaleAfter time.Duration
	// Enable names an optional BOOL tag: false parks the source. It is a
	// synthesized OUTPUT (the runtime hands its value to WriteOutputs).
	Enable string
}

// TagDecl is one struct tag a driver delivers: its contract type, the source
// it polls, and the binding vocabulary per member. Members absent from the
// map are never updated and stay at zero-of-field.
type TagDecl struct {
	Name    string
	Type    string
	Source  string
	Members map[string]Binding
}

// WriteDecl is one opt-in command: a SCALAR output tag (Name) whose value,
// on change, is written to (Tag, Member) on the wire — the sparkplug-host
// rule for writable UDT members, so the struct tag itself stays an input
// and its member reads back what the device holds.
type WriteDecl struct {
	Name   string
	Tag    string
	Member string
}

// Update is one member value that came back from a poll.
type Update struct {
	Tag    string
	Member string
	Value  ir.Value
}

// Result is what one (source, class) poll produced: the members it read,
// and the tags whose requests failed while the rest of the poll succeeded
// (a 404 on one Redfish resource, noSuchInstance on one row). A Bad tag
// keeps its last value and reads Quality Bad until a later poll clears it.
type Result struct {
	Updates []Update
	Bad     []string
}

// PollFunc fetches one scan class of one source. A non-nil error is a
// TRANSPORT failure (timeout, refused, auth) that counts toward the
// source's failure ladder; per-tag trouble goes in Result.Bad instead.
type PollFunc func(ctx context.Context, source, class string) (Result, error)

// WriteFunc performs one command on the wire.
type WriteFunc func(ctx context.Context, source string, w WriteDecl, v ir.Value) error

// ClassAssignment maps glob patterns (over "Tag.Member" paths and bare tag
// names) to a scan class; applied in order, the last match wins — exactly
// modbus' WithTagClass.
type ClassAssignment struct {
	Class    string
	Patterns []string
}

// Config builds a Base.
type Config struct {
	Kind        string // "snmp" | "redfish" | "prometheus": logs and status
	Sources     []SourceConfig
	Tags        []TagDecl
	Writes      []WriteDecl
	ScanRate    time.Duration            // the default class's interval (0 = DefaultInterval)
	ClassRates  map[string]time.Duration // named classes
	Assignments []ClassAssignment
	Poll        PollFunc
	Write       WriteFunc // nil when the driver has no writable bindings
	Log         *slog.Logger
	Now         func() time.Time // tests; nil = time.Now
}

// Base is the io.Driver machinery shared by the three IT-hardware drivers.
// A protocol package embeds it and forwards the io.Driver methods, or wraps
// it — either way the runtime sees one Driver with the modbus-shaped surface
// (New never dials, Start polls, Stop waits).
type Base struct {
	kind  string
	log   *slog.Logger
	poll  PollFunc
	write WriteFunc
	now   func() time.Time

	sources  []*Source
	bySource map[string]*Source
	tags     map[string]*tagRun
	writes   map[string]*writeRun
	enables  map[string][]*Source
	inputs   []string
	outputs  []string
	classes  map[string][]string

	polls, errs, writesN atomic.Uint64

	gateMu    sync.Mutex
	writeGate func() bool

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

type tagRun struct {
	decl    TagDecl
	sd      *ir.StructDef
	src     *Source
	classOf map[string]string // polled member → class
	derived []derivedMember
	initial ir.Value // zero-of-type with const members applied
}

type derivedMember struct {
	idx   int
	field Field
	expr  *Expr
}

type writeRun struct {
	decl WriteDecl
	src  *Source
	fld  Field
}

type classRun struct {
	name     string
	interval time.Duration
	next     time.Time
	tags     map[string]bool // tags with at least one member in this class
}

// Source is one polled device's live state. Health reads it under mu from
// another goroutine, so every field below is guarded.
type Source struct {
	cfg     SourceConfig
	classes []*classRun

	mu        sync.Mutex
	values    map[string]ir.Value // current struct value per tag (initial until delivered)
	delivered map[string]bool
	bad       map[string]map[string]bool // tag → class → its last poll failed for this tag
	state     string                     // parked | connecting | connected | error
	sinceMs   int64
	lastErr   error
	retries   uint64
	rttMs     float64
	lastPoll  time.Time
	answered  bool // some poll succeeded since the last error state
	failures  int  // consecutive failed polls
	enabled   bool
	pending   map[string]ir.Value // write outputs queued (last value per tag)
	written   map[string]ir.Value // baseline / last value handed to Write
	kick      chan struct{}
}

// NewBase validates the whole configuration offline and builds the driver
// state; nothing is polled until Start.
func NewBase(c Config) (*Base, error) {
	if c.Poll == nil {
		return nil, errors.New("hw: Config.Poll is required")
	}
	b := &Base{
		kind:     c.Kind,
		log:      c.Log,
		poll:     c.Poll,
		write:    c.Write,
		now:      c.Now,
		bySource: map[string]*Source{},
		tags:     map[string]*tagRun{},
		writes:   map[string]*writeRun{},
		enables:  map[string][]*Source{},
		classes:  map[string][]string{},
	}
	if b.log == nil {
		b.log = slog.Default()
	}
	if b.now == nil {
		b.now = time.Now
	}
	if b.kind == "" {
		b.kind = "hw"
	}
	scanRate := c.ScanRate
	if scanRate <= 0 {
		scanRate = DefaultInterval
	}
	rates := map[string]time.Duration{}
	for k, v := range c.ClassRates {
		if v <= 0 {
			return nil, fmt.Errorf("%s: scan class %q has non-positive interval %v", b.kind, k, v)
		}
		rates[k] = v
	}
	if _, ok := rates[DefaultClass]; !ok {
		rates[DefaultClass] = scanRate
	}
	for _, a := range c.Assignments {
		if _, ok := rates[a.Class]; !ok {
			return nil, fmt.Errorf("%s: tag-classes names undefined scan class %q — add it to scan-classes", b.kind, a.Class)
		}
	}

	// Sources.
	if len(c.Sources) == 0 {
		return nil, fmt.Errorf("%s: at least one source is required", b.kind)
	}
	for _, sc := range c.Sources {
		if sc.ID == "" {
			return nil, fmt.Errorf("%s: a source has no id", b.kind)
		}
		if _, dup := b.bySource[sc.ID]; dup {
			return nil, fmt.Errorf("%s: two sources are both named %q", b.kind, sc.ID)
		}
		if sc.RetryMin <= 0 {
			sc.RetryMin = DefaultRetryMin
		}
		if sc.RetryMax <= 0 {
			sc.RetryMax = DefaultRetryMax
		}
		if sc.RetryMax < sc.RetryMin {
			sc.RetryMax = sc.RetryMin
		}
		if sc.Interval <= 0 {
			sc.Interval = rates[DefaultClass]
		}
		if sc.StaleAfter <= 0 {
			sc.StaleAfter = 3 * sc.Interval
		}
		s := &Source{
			cfg:       sc,
			values:    map[string]ir.Value{},
			delivered: map[string]bool{},
			bad:       map[string]map[string]bool{},
			state:     "connecting",
			enabled:   sc.Enable == "",
			pending:   map[string]ir.Value{},
			written:   map[string]ir.Value{},
			kick:      make(chan struct{}, 1),
		}
		b.sources = append(b.sources, s)
		b.bySource[sc.ID] = s
		if sc.Enable != "" {
			b.enables[sc.Enable] = append(b.enables[sc.Enable], s)
		}
	}

	// Tags.
	for _, td := range c.Tags {
		if td.Name == "" {
			return nil, fmt.Errorf("%s: a tag has no name", b.kind)
		}
		if _, dup := b.tags[td.Name]; dup {
			return nil, fmt.Errorf("%s: tag %q is declared twice", b.kind, td.Name)
		}
		if strings.Contains(td.Name, ".") {
			return nil, fmt.Errorf("%s: tag %q: a tag name cannot contain '.'", b.kind, td.Name)
		}
		if _, ok := TypeByName(td.Type); !ok {
			return nil, fmt.Errorf("%s: tag %q: unknown type %q (the IT-hardware set is %s)", b.kind, td.Name, td.Type, TypeNames())
		}
		src, ok := b.bySource[td.Source]
		if !ok {
			return nil, fmt.Errorf("%s: tag %q: unknown source %q", b.kind, td.Name, td.Source)
		}
		initial, _ := Zero(td.Type)
		initial.Fld = append([]ir.Value(nil), initial.Fld...)
		t := &tagRun{decl: td, sd: StructDef(td.Type), src: src, classOf: map[string]string{}, initial: initial}
		members := make([]string, 0, len(td.Members))
		for m := range td.Members {
			members = append(members, m)
		}
		sort.Strings(members)
		for _, m := range members {
			bd := td.Members[m]
			idx, f, ok := FieldOf(td.Type, m)
			if !ok {
				return nil, fmt.Errorf("%s: tag %q: type %s has no member %q", b.kind, td.Name, td.Type, m)
			}
			if err := bd.Validate(f); err != nil {
				return nil, fmt.Errorf("%s: tag %q: %w", b.kind, td.Name, err)
			}
			switch {
			case bd.Derived != "":
				e, _ := ParseExpr(bd.Derived)
				for _, v := range e.Vars() {
					if _, _, ok := FieldOf(td.Type, v); !ok {
						return nil, fmt.Errorf("%s: tag %q: member %s: derived: names %q, which is not a member of %s", b.kind, td.Name, m, v, td.Type)
					}
				}
				t.derived = append(t.derived, derivedMember{idx: idx, field: f, expr: e})
			case bd.Const != nil:
				v, err := coerce(bd.Const, f)
				if err != nil {
					return nil, fmt.Errorf("%s: tag %q: member %s: const: %w", b.kind, td.Name, m, err)
				}
				t.initial.Fld[idx] = v
			default:
				class := bd.ScanClass
				if class == "" {
					class = DefaultClass
				}
				p := td.Name + "." + m
				for _, a := range c.Assignments {
					for _, pat := range a.Patterns {
						if globMatch(pat, p) || globMatch(pat, td.Name) {
							class = a.Class
						}
					}
				}
				if _, ok := rates[class]; !ok {
					return nil, fmt.Errorf("%s: tag %q: member %s names undefined scan class %q — add it to scan-classes", b.kind, td.Name, m, class)
				}
				t.classOf[m] = class
				b.classes[class] = append(b.classes[class], p)
				src.class(class, classInterval(src.cfg, class, rates)).tags[td.Name] = true
			}
		}
		// Derived members evaluate in declaration order of the table, so a
		// derived member may read another derived one declared before it.
		sort.Slice(t.derived, func(i, j int) bool { return t.derived[i].idx < t.derived[j].idx })
		b.tags[td.Name] = t
		src.values[td.Name] = t.initial
	}
	for _, s := range b.sources {
		sort.Slice(s.classes, func(i, j int) bool { return s.classes[i].name < s.classes[j].name })
	}

	// Writes.
	for _, w := range c.Writes {
		if w.Name == "" {
			return nil, fmt.Errorf("%s: a write binding has no name", b.kind)
		}
		if _, dup := b.writes[w.Name]; dup {
			return nil, fmt.Errorf("%s: write %q is declared twice", b.kind, w.Name)
		}
		if _, clash := b.tags[w.Name]; clash {
			return nil, fmt.Errorf("%s: write %q has the same name as a struct tag — a command is its own scalar output tag", b.kind, w.Name)
		}
		t, ok := b.tags[w.Tag]
		if !ok {
			return nil, fmt.Errorf("%s: write %q targets unknown tag %q", b.kind, w.Name, w.Tag)
		}
		_, f, ok := FieldOf(t.decl.Type, w.Member)
		if !ok {
			return nil, fmt.Errorf("%s: write %q: type %s has no member %q", b.kind, w.Name, t.decl.Type, w.Member)
		}
		if b.write == nil {
			return nil, fmt.Errorf("%s: write %q declared but the driver has no write path", b.kind, w.Name)
		}
		b.writes[w.Name] = &writeRun{decl: w, src: t.src, fld: f}
	}

	for name := range b.tags {
		b.inputs = append(b.inputs, name)
	}
	for _, s := range b.sources {
		b.inputs = append(b.inputs, OnlineTagName(s.cfg.ID), LastPollTagName(s.cfg.ID))
	}
	sort.Strings(b.inputs)
	for name := range b.writes {
		b.outputs = append(b.outputs, name)
	}
	for name := range b.enables {
		b.outputs = append(b.outputs, name)
	}
	sort.Strings(b.outputs)
	for _, paths := range b.classes {
		sort.Strings(paths)
	}
	return b, nil
}

func classInterval(sc SourceConfig, class string, rates map[string]time.Duration) time.Duration {
	if class == DefaultClass {
		return sc.Interval
	}
	return rates[class]
}

func globMatch(pattern, name string) bool {
	ok, err := path.Match(pattern, name)
	return err == nil && ok
}

func (s *Source) class(name string, interval time.Duration) *classRun {
	for _, c := range s.classes {
		if c.name == name {
			return c
		}
	}
	c := &classRun{name: name, interval: interval, tags: map[string]bool{}}
	s.classes = append(s.classes, c)
	return c
}

// ── io.Driver surface ──────────────────────────────────────────────────

// InputNames lists every tag the driver delivers: struct tags plus the
// __Online / __LastPollMs companions, sorted (io.Multi routing).
func (b *Base) InputNames() []string { return append([]string(nil), b.inputs...) }

// OutputNames lists the command tags plus the sources' Enable tags.
func (b *Base) OutputNames() []string { return append([]string(nil), b.outputs...) }

// ScanClasses maps each class to the "Tag.Member" paths it polls.
func (b *Base) ScanClasses() map[string][]string {
	out := make(map[string][]string, len(b.classes))
	for k, v := range b.classes {
		out[k] = append([]string(nil), v...)
	}
	return out
}

// StructDefs is the contract set, for a project that checks program TYPEs
// against what the driver delivers.
func (b *Base) StructDefs() map[string]*ir.StructDef { return StructDefs() }

// SetWriteGate installs the leadership / logic-healthy gate: while it
// returns false, queued commands stay queued. Nil means always open.
func (b *Base) SetWriteGate(gate func() bool) {
	b.gateMu.Lock()
	b.writeGate = gate
	b.gateMu.Unlock()
}

func (b *Base) gateOpen() bool {
	b.gateMu.Lock()
	g := b.writeGate
	b.gateMu.Unlock()
	return g == nil || g()
}

// Start launches one poll loop per source. Idempotent: a second Start is a
// no-op until Stop.
func (b *Base) Start(ctx context.Context) {
	if b.cancel != nil {
		return
	}
	ctx, b.cancel = context.WithCancel(ctx)
	for _, s := range b.sources {
		b.wg.Add(1)
		go func(s *Source) {
			defer b.wg.Done()
			b.run(ctx, s)
		}(s)
	}
}

// Stop ends every loop and waits for them.
func (b *Base) Stop() {
	if b.cancel == nil {
		return
	}
	b.cancel()
	b.wg.Wait()
	b.cancel = nil
}

// ReadInputs is the allocating form of ReadInputsInto.
func (b *Base) ReadInputs() (nio.Values, error) {
	out := make(nio.Values, len(b.inputs))
	return out, b.ReadInputsInto(out)
}

// ReadInputsInto refills dst with the latest snapshot: every delivered
// struct tag, and the two companions per source. A tag never delivered is
// absent, so a program reading it faults until the source answers — the
// __Online companion is the guard (the sparkplug-host contract).
func (b *Base) ReadInputsInto(dst nio.Values) error {
	for k := range dst {
		delete(dst, k)
	}
	now := b.now()
	for _, s := range b.sources {
		s.mu.Lock()
		dst[OnlineTagName(s.cfg.ID)] = s.freshLocked(now)
		var lp int64
		if !s.lastPoll.IsZero() {
			lp = s.lastPoll.UnixMilli()
		}
		dst[LastPollTagName(s.cfg.ID)] = lp
		for name, v := range s.values {
			if s.delivered[name] {
				dst[name] = v
			}
		}
		s.mu.Unlock()
	}
	return nil
}

// badLocked: a tag is Bad while ANY scan class's last poll failed for it —
// a port whose counters came back but whose status row 404'd is not to be
// trusted as a whole. Caller holds s.mu.
func (s *Source) badLocked(name string) bool {
	for _, b := range s.bad[name] {
		if b {
			return true
		}
	}
	return false
}

// freshLocked is the __Online verdict: connected AND heard from within
// StaleAfter — a hung agent that takes the socket and never answers is
// not online. Caller holds s.mu.
func (s *Source) freshLocked(now time.Time) bool {
	return s.state == "connected" && !s.lastPoll.IsZero() && now.Sub(s.lastPoll) <= s.cfg.StaleAfter
}

// Quality reports, per tag, the non-Good verdicts (io.QualityReporter):
// NotConnected before the first delivery, Stale when the source is not
// fresh, Bad when the tag's own requests failed on a fresh source.
// Companions and command tags are the driver's own truth: always Good.
func (b *Base) Quality() map[string]nio.Quality {
	var out map[string]nio.Quality
	mark := func(name string, q nio.Quality) {
		if out == nil {
			out = map[string]nio.Quality{}
		}
		out[name] = q
	}
	now := b.now()
	for _, s := range b.sources {
		s.mu.Lock()
		fresh := s.freshLocked(now)
		for name := range s.values {
			switch {
			case !s.delivered[name]:
				mark(name, nio.NotConnected)
			case !fresh:
				mark(name, nio.Stale)
			case s.badLocked(name):
				mark(name, nio.Bad)
			}
		}
		s.mu.Unlock()
	}
	return out
}

// WriteOutputs takes the runtime's changed outputs: Enable tags park or
// wake their sources; command tags are queued per source and flushed by
// its loop. The FIRST value seen per command tag is a baseline that is
// never written — `init: false` on an outlet command must not switch the
// outlet off at boot — and only a change from the last handed-over value
// goes out (modbus §4's rule).
func (b *Base) WriteOutputs(vals nio.Values) error {
	var errs []error
	for name, raw := range vals {
		if srcs, ok := b.enables[name]; ok {
			on, isBool := raw.(bool)
			if !isBool {
				if iv, isIR := raw.(ir.Value); isIR && iv.Kind == ir.TypeBool {
					on, isBool = iv.B, true
				}
			}
			if !isBool {
				errs = append(errs, fmt.Errorf("%s: enable tag %s: want BOOL, got %T", b.kind, name, raw))
				continue
			}
			for _, s := range srcs {
				s.mu.Lock()
				changed := s.enabled != on
				s.enabled = on
				s.mu.Unlock()
				if changed {
					s.wake()
				}
			}
			continue
		}
		w, ok := b.writes[name]
		if !ok {
			continue // not ours (a Multi sibling's, or a tag we deliver)
		}
		v, err := coerce(raw, w.fld)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: write %s: %w", b.kind, name, err))
			continue
		}
		s := w.src
		s.mu.Lock()
		prev, seen := s.written[name]
		if !seen {
			s.written[name] = v // baseline
			s.mu.Unlock()
			continue
		}
		if sameScalar(prev, v) {
			s.mu.Unlock()
			continue
		}
		s.written[name] = v
		s.pending[name] = v
		s.mu.Unlock()
		s.wake()
	}
	return errors.Join(errs...)
}

func sameScalar(a, b ir.Value) bool {
	if a.Kind != b.Kind {
		return false
	}
	switch a.Kind {
	case ir.TypeBool:
		return a.B == b.B
	case ir.TypeReal:
		return a.F == b.F
	case ir.TypeInt:
		return a.I == b.I
	case ir.TypeString:
		return a.S == b.S
	}
	return false
}

func (s *Source) wake() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// ── the loop ───────────────────────────────────────────────────────────

func (b *Base) run(ctx context.Context, s *Source) {
	backoff := s.cfg.RetryMin
	prime := true
	for ctx.Err() == nil {
		if !s.isEnabled() {
			s.setState("parked", nil, b.now())
			prime = true
			select {
			case <-ctx.Done():
				return
			case <-s.kick:
			}
			continue
		}
		now := b.now()
		if prime {
			for _, c := range s.classes {
				c.next = now
			}
			prime = false
		}
		failed := false
		for _, c := range s.classes {
			if c.next.After(now) {
				continue
			}
			t0 := time.Now()
			res, err := b.poll(ctx, s.cfg.ID, c.name)
			rtt := time.Since(t0)
			if ctx.Err() != nil {
				return
			}
			b.polls.Add(1)
			if err != nil {
				b.errs.Add(1)
				s.mu.Lock()
				s.retries++
				s.failures++
				s.lastErr = err
				answered := s.answered
				failures := s.failures
				s.mu.Unlock()
				c.next = b.now().Add(c.interval)
				if !answered || failures >= failuresToError {
					failed = true
					break
				}
				b.log.Warn(b.kind+": poll failed", "source", s.cfg.ID, "class", c.name, "error", err, "failures", failures)
				continue
			}
			done := b.now()
			s.mu.Lock()
			s.failures = 0
			s.answered = true
			s.lastPoll = done
			s.lastErr = nil
			if s.rttMs == 0 {
				s.rttMs = float64(rtt.Milliseconds())
			} else {
				s.rttMs = 0.8*s.rttMs + 0.2*float64(rtt.Milliseconds())
			}
			b.mergeLocked(s, c, res)
			s.mu.Unlock()
			if s.setState("connected", nil, done) {
				b.log.Info(b.kind+": connected", "source", s.cfg.ID, "addr", s.cfg.Addr)
			}
			backoff = s.cfg.RetryMin
			c.next = done.Add(c.interval)
		}
		if failed {
			s.mu.Lock()
			err := s.lastErr
			s.answered = false
			s.mu.Unlock()
			s.setState("error", err, b.now())
			b.log.Warn(b.kind+": source down", "source", s.cfg.ID, "addr", s.cfg.Addr, "error", err, "retryIn", backoff)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			case <-s.kick:
			}
			if backoff *= 2; backoff > s.cfg.RetryMax {
				backoff = s.cfg.RetryMax
			}
			prime = true
			continue
		}
		b.flushWrites(ctx, s)

		wake := time.Hour
		now = b.now()
		for _, c := range s.classes {
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
		case <-s.kick:
			timer.Stop()
		case <-timer.C:
		}
	}
}

// mergeLocked folds one poll's updates into the source's snapshot. Struct
// values are copy-on-write: the store holds the value we delivered last
// time, so mutating its Fld in place would change the store under it with
// no generation bump. A tag that was polled and is not in Bad is delivered;
// then its derived members are recomputed. Caller holds s.mu.
func (b *Base) mergeLocked(s *Source, c *classRun, res Result) {
	fresh := map[string]bool{}
	cow := func(name string) (ir.Value, bool) {
		v, ok := s.values[name]
		if !ok {
			return v, false
		}
		if !fresh[name] {
			v.Fld = append([]ir.Value(nil), v.Fld...)
			fresh[name] = true
			s.values[name] = v
		}
		return v, true
	}
	for _, u := range res.Updates {
		t, ok := b.tags[u.Tag]
		if !ok || t.src != s {
			b.log.Warn(b.kind+": poll delivered an unknown tag", "source", s.cfg.ID, "tag", u.Tag)
			continue
		}
		idx, ok := t.sd.FieldIndex[u.Member]
		if !ok {
			b.log.Warn(b.kind+": poll delivered an unknown member", "source", s.cfg.ID, "tag", u.Tag, "member", u.Member)
			continue
		}
		v, _ := cow(u.Tag)
		v.Fld[idx] = u.Value
		s.values[u.Tag] = v
	}
	bad := map[string]bool{}
	for _, name := range res.Bad {
		bad[name] = true
	}
	for name := range c.tags {
		if s.bad[name] == nil {
			s.bad[name] = map[string]bool{}
		}
		if bad[name] {
			s.bad[name][c.name] = true
			continue
		}
		s.bad[name][c.name] = false
		s.delivered[name] = true
	}
	for name := range fresh {
		t := b.tags[name]
		if len(t.derived) == 0 {
			continue
		}
		v := s.values[name]
		lookup := func(m string) (ir.Value, bool) {
			i, ok := t.sd.FieldIndex[m]
			if !ok {
				return ir.Value{}, false
			}
			return v.Fld[i], true
		}
		for _, d := range t.derived {
			r, err := d.expr.Eval(lookup)
			if err != nil {
				b.log.Warn(b.kind+": derived member", "tag", name, "member", d.field.Name, "error", err)
				continue
			}
			cv, err := coerce(r, d.field)
			if err != nil {
				b.log.Warn(b.kind+": derived member", "tag", name, "member", d.field.Name, "error", err)
				continue
			}
			v.Fld[d.idx] = cv
		}
		s.values[name] = v
	}
}

// flushWrites sends every queued command for a connected source, while
// the write gate is open. A refused command is dropped with its error on
// the row: retrying a Set the device rejected would only repeat the
// refusal, and a transport failure shows up on the next poll anyway.
func (b *Base) flushWrites(ctx context.Context, s *Source) {
	if b.write == nil || !b.gateOpen() {
		return
	}
	s.mu.Lock()
	if len(s.pending) == 0 {
		s.mu.Unlock()
		return
	}
	batch := make(map[string]ir.Value, len(s.pending))
	for k, v := range s.pending {
		batch[k] = v
	}
	s.pending = map[string]ir.Value{}
	s.mu.Unlock()
	names := make([]string, 0, len(batch))
	for k := range batch {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, name := range names {
		w := b.writes[name]
		if err := b.write(ctx, s.cfg.ID, w.decl, batch[name]); err != nil {
			b.errs.Add(1)
			s.mu.Lock()
			s.lastErr = fmt.Errorf("write %s: %w", name, err)
			s.mu.Unlock()
			b.log.Warn(b.kind+": write failed", "source", s.cfg.ID, "tag", name, "error", err)
			continue
		}
		b.writesN.Add(1)
	}
}

func (s *Source) isEnabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enabled
}

// setState records a transition; returns true when the state changed.
func (s *Source) setState(state string, err error, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.lastErr = err
	}
	if s.state == state {
		return false
	}
	s.state = state
	s.sinceMs = now.UnixMilli()
	return true
}

// ── health ─────────────────────────────────────────────────────────────

// Health reports one row per source plus driver-wide free-running totals,
// for internal/project/drivers.go's /api/drivers adapter.
type Health struct {
	Kind    string         `json:"kind"`
	Sources []SourceHealth `json:"sources"`
	Polls   uint64         `json:"polls"`
	Writes  uint64         `json:"writes"`
	Errors  uint64         `json:"errors"`
}

// SourceHealth is one source's row.
type SourceHealth struct {
	ID    string `json:"id"`
	Addr  string `json:"addr"`
	State string `json:"state"` // connected | connecting | error | parked
	// Fresh is the __Online verdict: connected and heard from within
	// StaleAfter. A connected-but-silent source is the case it separates.
	Fresh        bool    `json:"fresh"`
	SinceMs      int64   `json:"sinceMs"`
	LastError    string  `json:"lastError,omitempty"`
	Retries      uint64  `json:"retries"`
	RTTMs        float64 `json:"rttMs"`
	LastPollMs   int64   `json:"lastPollMs"`
	Tags         int     `json:"tags"`
	BadTags      int     `json:"badTags"`
	QueuedWrites int     `json:"queuedWrites"`
}

// Health returns the current per-source states and counters.
func (b *Base) Health() Health {
	h := Health{Kind: b.kind, Polls: b.polls.Load(), Writes: b.writesN.Load(), Errors: b.errs.Load()}
	now := b.now()
	for _, s := range b.sources {
		s.mu.Lock()
		row := SourceHealth{
			ID:           s.cfg.ID,
			Addr:         s.cfg.Addr,
			State:        s.state,
			Fresh:        s.freshLocked(now),
			SinceMs:      s.sinceMs,
			Retries:      s.retries,
			RTTMs:        s.rttMs,
			Tags:         len(s.values),
			QueuedWrites: len(s.pending),
		}
		if !s.lastPoll.IsZero() {
			row.LastPollMs = s.lastPoll.UnixMilli()
		}
		if s.lastErr != nil {
			row.LastError = s.lastErr.Error()
		}
		for name := range s.bad {
			if s.badLocked(name) {
				row.BadTags++
			}
		}
		s.mu.Unlock()
		h.Sources = append(h.Sources, row)
	}
	return h
}
