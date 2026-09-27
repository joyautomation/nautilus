// driver.go is the io.Driver surface of the Prometheus scrape driver:
// construction (New, Option), the Poll function hw.Base calls per (source,
// class), and the credential/transport plumbing around one GET per poll.
// Base (hw/base.go) owns everything else — snapshots, __Online/__LastPollMs
// companions, Quality, Health, scan-class routing, the write gate — so this
// file is short: it is the part that is actually protocol-specific.
//
// New NEVER dials, exactly like modbus and the design brief's shared rule:
// buildDriver runs inside `naut check`/`naut build`, in CI with no exporter
// in sight, so a bad manifest fails there — the network call is Start's job.
package prom

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/joyautomation/nautilus/hw"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
)

func basicAuthValue(userPass string) string {
	return base64.StdEncoding.EncodeToString([]byte(userPass))
}

// Defaults per source (docs/design/it-drivers.md §3.3).
const (
	defaultTimeout  = 5 * time.Second
	defaultInterval = 15 * time.Second
)

// Fetcher is the transport seam: given a source's URL and headers, return
// the scrape body. WithFetcher substitutes it in tests that want no socket
// at all; the default (httpFetch) is a real GET.
type Fetcher func(ctx context.Context, url string, headers map[string]string, insecure bool, timeout time.Duration) ([]byte, error)

// Option configures New.
type Option func(*Driver)

// WithScanRate sets the default scan class's poll interval (default 15s —
// node_exporter's own scrape cadence, not a control scan rate).
func WithScanRate(r time.Duration) Option { return func(d *Driver) { d.scanRate = r } }

// WithScanClass defines (or redefines) a scan class and its poll interval.
func WithScanClass(name string, rate time.Duration) Option {
	return func(d *Driver) { d.classRates[name] = rate }
}

// WithTagClass assigns tags to a scan class by glob patterns matched
// against "Tag.Member" paths and bare tag names (modbus'
// WithTagClass/hw.ClassAssignment). Later assignments override earlier
// ones.
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

// WithFetcher substitutes the scrape transport — tests hand the driver a
// fake fetcher so unit tests need no socket at all; the stand-in-based
// driver tests use a real one (a real *http.Client against prom/serve on
// 127.0.0.1:0) and so never need this option.
func WithFetcher(f Fetcher) Option { return func(d *Driver) { d.fetch = f } }

// Driver scrapes a set of Prometheus exporters and implements io.Driver,
// io.BatchReader and io.QualityReporter by forwarding to hw.Base — see
// hw/doc.go for the split: Base owns the poll loop and snapshots, this file
// owns fetching a body and turning it into hw.Updates.
type Driver struct {
	base *hw.Base

	scanRate    time.Duration
	classRates  map[string]time.Duration
	assignments []hw.ClassAssignment
	log         *slog.Logger
	fetch       Fetcher

	sources map[string]*sourceRuntime
	tags    map[string]*tagRuntime
	// classOf is "Tag.Member" → the FINAL resolved scan class, read back
	// from hw.Base.ScanClasses() once New has built it — the ground truth
	// after WithTagClass overrides, so poll.go never has to re-derive
	// Base's own class-assignment rules.
	classOf map[string]string
}

// sourceRuntime is one source's resolved transport settings plus its own
// rate-counter store and "was the last poll of each class down" tracking —
// see rate.go for why prom keeps float64 counters instead of hw.Counter,
// and the doc comment on resetIfReconnected below for the reconnect rule.
type sourceRuntime struct {
	cfg     Source
	headers map[string]string
	logger  *slog.Logger

	mu       sync.Mutex
	counters map[string]*hw.Counter // key: "tag.member" or "tag.member.selector"
	wasDown  map[string]bool        // key: class name
	warned   map[string]bool        // "member absent" logged once per source
}

// tagRuntime is one struct tag's compiled bindings: the parsed Expr per
// expr-bound member (parsed once at New, not per poll) and which class each
// member belongs to (for filtering a (source, class) poll to its members).
type tagRuntime struct {
	decl    Tag
	src     *sourceRuntime
	members map[string]compiledBinding
}

type compiledBinding struct {
	b     Binding
	field hw.Field
	class string
	expr  *hw.Expr // non-nil when b.Expr != ""
}

// New builds the driver: validates the manifest, resolves scan classes and
// credentials, and builds the hw.Base every forwarded method rides on. It
// NEVER scrapes — call Start to begin polling.
func New(m Manifest, opts ...Option) (*Driver, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	d := &Driver{
		scanRate:   defaultInterval,
		classRates: map[string]time.Duration{},
		log:        slog.Default(),
		sources:    map[string]*sourceRuntime{},
		tags:       map[string]*tagRuntime{},
	}
	for _, o := range opts {
		o(d)
	}
	if d.fetch == nil {
		d.fetch = httpFetch
	}

	cfg := hw.Config{
		Kind:        "prometheus",
		ScanRate:    d.scanRate,
		ClassRates:  d.classRates,
		Assignments: d.assignments,
		Log:         d.log,
		Poll:        d.poll,
		// Write stays nil: scraping is one-way (docs/design/it-drivers.md
		// §7 is generic across the three drivers, but Prometheus has no
		// wire path to write anything down). hw.NewBase already refuses a
		// manifest with writes: declared when Write is nil, naming them.
	}

	for _, s := range m.Sources {
		hdr, err := d.resolveCredentials(s)
		if err != nil {
			return nil, fmt.Errorf("prom: source %s: %w", s.ID, err)
		}
		sr := &sourceRuntime{
			cfg: s, headers: hdr, logger: d.log,
			counters: map[string]*hw.Counter{},
			wasDown:  map[string]bool{},
			warned:   map[string]bool{},
		}
		d.sources[s.ID] = sr
		cfg.Sources = append(cfg.Sources, hw.SourceConfig{
			ID: s.ID, Addr: s.URL, Interval: s.Interval,
			StaleAfter: s.StaleAfter, Enable: s.Enable,
		})
	}

	for _, tg := range m.Tags {
		sr, ok := d.sources[tg.Source]
		if !ok {
			return nil, fmt.Errorf("prom: tag %s: unknown source %q", tg.Name, tg.Source) // Validate already caught this
		}
		tr := &tagRuntime{decl: tg, src: sr, members: map[string]compiledBinding{}}
		hwMembers := map[string]hw.Binding{}
		for member, b := range tg.Members {
			_, f, _ := hw.FieldOf(tg.Type, member)
			cb := compiledBinding{b: b, field: f, class: b.ScanClass}
			if b.Expr != "" {
				cb.expr, _ = hw.ParseExpr(b.Expr) // Validate already parsed it once
			}
			tr.members[member] = cb
			hwMembers[member] = b.Binding
		}
		d.tags[tg.Name] = tr
		cfg.Tags = append(cfg.Tags, hw.TagDecl{Name: tg.Name, Type: tg.Type, Source: tg.Source, Members: hwMembers})
	}
	for _, w := range m.Writes {
		cfg.Writes = append(cfg.Writes, w)
	}

	base, err := hw.NewBase(cfg)
	if err != nil {
		return nil, err
	}
	d.base = base
	d.classOf = map[string]string{}
	for class, paths := range base.ScanClasses() {
		for _, p := range paths {
			d.classOf[p] = class
		}
	}
	return d, nil
}

// resolveCredentials reads *-env/*-file into request headers. Validate
// already refused both-set; an unset variable is a WARNING, not an error —
// `naut check` runs on laptops with no secrets in the environment at all
// (docs/design/it-drivers.md §6.2) — so New proceeds with no header rather
// than failing, and Warnings reports it for the caller that wants to tell
// the operator.
func (d *Driver) resolveCredentials(s Source) (map[string]string, error) {
	headers := map[string]string{}
	bearer, err := readCredential(s.BearerEnv, s.BearerFile)
	if err != nil {
		return nil, err
	}
	if bearer != "" {
		headers["Authorization"] = "Bearer " + bearer
	}
	basic, err := readCredential(s.BasicEnv, s.BasicFile)
	if err != nil {
		return nil, err
	}
	if basic != "" {
		headers["Authorization"] = "Basic " + basicAuthValue(basic)
	}
	return headers, nil
}

func readCredential(env, file string) (string, error) {
	switch {
	case env != "":
		return os.Getenv(env), nil // unset is "", a warning-level condition — see Manifest.Warnings
	case file != "":
		raw, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("reading %s: %w", file, err)
		}
		return strings.TrimSpace(string(raw)), nil
	}
	return "", nil
}

// Warnings lists credential variables named but not actually set — surfaced
// by `naut check`, never by New (docs/design/it-drivers.md §6.2: check runs
// on laptops without the secrets, so an unset variable cannot be an error).
func (m Manifest) Warnings() []string {
	var out []string
	check := func(id, key, env string) {
		if env != "" && os.Getenv(env) == "" {
			out = append(out, fmt.Sprintf("source %s: %s=%s is unset", id, key, env))
		}
	}
	for _, s := range m.Sources {
		check(s.ID, "bearer-env", s.BearerEnv)
		check(s.ID, "basic-env", s.BasicEnv)
	}
	sort.Strings(out)
	return out
}

// ── forwarded io.Driver surface ─────────────────────────────────────────

func (d *Driver) Start(ctx context.Context) { d.base.Start(ctx) }
func (d *Driver) Stop()                     { d.base.Stop() }

func (d *Driver) ReadInputs() (nio.Values, error)      { return d.base.ReadInputs() }
func (d *Driver) ReadInputsInto(dst nio.Values) error  { return d.base.ReadInputsInto(dst) }
func (d *Driver) WriteOutputs(vals nio.Values) error   { return d.base.WriteOutputs(vals) }
func (d *Driver) Quality() map[string]nio.Quality      { return d.base.Quality() }
func (d *Driver) InputNames() []string                 { return d.base.InputNames() }
func (d *Driver) OutputNames() []string                { return d.base.OutputNames() }
func (d *Driver) ScanClasses() map[string][]string     { return d.base.ScanClasses() }
func (d *Driver) SetWriteGate(gate func() bool)        { d.base.SetWriteGate(gate) }
func (d *Driver) StructDefs() map[string]*ir.StructDef { return d.base.StructDefs() }
func (d *Driver) Health() hw.Health                    { return d.base.Health() }

// ── the transport ────────────────────────────────────────────────────────

func httpFetch(ctx context.Context, url string, headers map[string]string, insecure bool, timeout time.Duration) ([]byte, error) {
	client := &http.Client{Timeout: timeout}
	if insecure {
		client.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec // opt-in, source-scoped
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("scrape %s: HTTP %d", url, resp.StatusCode)
	}
	return body, nil
}
