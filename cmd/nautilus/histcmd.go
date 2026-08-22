// `nautilus historian`: a standalone daemon that archives a controller's
// tags into Postgres and serves downsampled history back out. Separate
// process, separate binary target from the controller — the scan loop
// keeps running whether or not history is being collected, and the
// nautilus runtime never imports package hist (see hist's doc comment).
//
// This ports mini-scada's cmd/historian, generalized: the original hardcoded
// seven tag names from a bespoke JSON shape (a specific PID loop's process
// variables). nautilus tags are open-ended, so instead of a fixed struct
// this polls the generic Frame the runtime's GET /api/state already serves
// and records whatever numeric/boolean tags match a glob pattern.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/joyautomation/nautilus/hist"
)

const historianUsage = `nautilus historian — archive a controller's tags into Postgres

Polls a running controller's GET /api/state once per interval and records
every numeric or boolean leaf matching a --tags pattern, then serves
downsampled history back out over HTTP for an HMI trend chart.

A struct (UDT) tag's value arrives as a nested JSON object — an AnalogInput
tag "FIT_001" with a scaled reading at its VALUE member serializes as
{"VALUE": 42.5, "HH": true, ...}. The collector walks into it and records
each numeric/bool leaf under a dotted path: "FIT_001.VALUE", "FIT_001.HH".
An array member "FIT_001.LOG" walks to "FIT_001.LOG[0]", "FIT_001.LOG[1]",
etc. Strings are skipped at any depth. -tags / -exclude patterns match
against this full dotted leaf name (path.Match syntax — "*" matches "."
too, since it only treats "/" as a separator), so a pattern without a dot
("RTU9_*") only matches top-level scalar tags unless it ends in "*", and
"*.VALUE" / "*_FIT_*" / "*Alm" match leaves at any depth.

For a UDT-heavy fleet where every instrument is a struct, a good starting
point is:

  -tags '*.VALUE,*.RUNST,*Alm,*.HH,*.H,*.L,*.LL'

Usage:
  nautilus historian -source <url> -db <dsn> [flags]

Flags:
  -addr         Listen address for the historian's own HTTP API (default :9100)
  -source       Controller base URL to poll, e.g. http://plc:8080
                (env NAUTILUS_SOURCE_URL). Required.
  -db           Postgres DSN (env DATABASE_URL). Required.
  -retention    How long to keep samples before pruning (default 168h)
  -interval     Poll/collect interval (default 1s)
  -tags         Comma-separated glob patterns (path.Match syntax) selecting
                which dotted leaf names to archive, e.g. "PIT_*,*.VALUE"
                (default "*" — every numeric/bool leaf, top-level or
                nested; narrow this before pointing at a real fleet)
  -exclude      Comma-separated glob patterns (same syntax) excluding
                dotted leaf names that would otherwise match -tags, e.g.
                "*.SIM*,*.DB" to drop simulation/diagnostic members
  -deadband     Absolute value change required to record a matched tag
                again before -min-interval elapses (default 0: any change
                counts). No effect unless -min-interval is also set.
  -min-interval Force-record each matched tag at least this often even
                without a qualifying change — a heartbeat so a flatlined
                tag doesn't vanish from a trend (default 0: disabled,
                meaning every matched tag is recorded on every poll,
                exactly like today). Setting this enables change-based
                filtering: record on change (past -deadband) OR at least
                every -min-interval. A fleet trending mostly-static
                instrument state might use "-deadband 0 -min-interval 60s".

HTTP API:
  GET /history        ?from&to (ms epoch), ?maxPoints, ?tags (CSV) — downsampled series
  GET /history/span    earliest/latest sample and total count
  GET /history/agg     ?tags (CSV) &from&to (ms epoch) &bucket (Go duration,
                        e.g. "1h"; omit for the whole [from,to) range as one
                        bucket) &fn=min|max|avg|sum|first|last|count|delta|
                        ontime — [{tag, ts (bucket start, ms epoch), value}].
                        delta is last-first (a totalizer's period total);
                        ontime is seconds the tag was non-zero, read off
                        consecutive samples (a BOOL RUNST's runtime).
  GET /history/at       ?tags (CSV) &at (ms epoch) — each tag's last value
                        at-or-before at, e.g. a reservoir level at 6am.
  GET /healthz         liveness probe
`

// frameTagsResponse is the slice of server.Frame this command needs: just
// the tags map. Decoding into this instead of the full Frame keeps
// histcmd.go from importing package server for one field, and tolerates
// the Frame growing new fields over time.
type frameTagsResponse struct {
	Tags map[string]any `json:"tags"`
}

// runHistorian is the entry point for `nautilus historian`.
func runHistorian(args []string) int {
	fs := flag.NewFlagSet("historian", flag.ExitOnError)
	addr := fs.String("addr", ":9100", "listen address")
	source := fs.String("source", envDefault("NAUTILUS_SOURCE_URL", ""), "controller base URL to poll")
	dbURL := fs.String("db", envDefault("DATABASE_URL", ""), "Postgres DSN")
	retention := fs.Duration("retention", 168*time.Hour, "how long to keep samples")
	interval := fs.Duration("interval", time.Second, "poll/collect interval")
	tagsFlag := fs.String("tags", "*", "comma-separated glob patterns selecting dotted leaf names to archive")
	excludeFlag := fs.String("exclude", "", "comma-separated glob patterns excluding dotted leaf names that would otherwise match -tags")
	deadband := fs.Float64("deadband", 0, "absolute change required to record a matched tag again before -min-interval elapses")
	minInterval := fs.Duration("min-interval", 0, "force-record each matched tag at least this often even without a qualifying change (0 disables change-based filtering: record every poll, as before)")
	fs.Usage = func() { fmt.Fprint(os.Stderr, historianUsage) }
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if *source == "" {
		fmt.Fprintln(os.Stderr, "nautilus historian: -source / NAUTILUS_SOURCE_URL is required")
		return 2
	}
	if *dbURL == "" {
		fmt.Fprintln(os.Stderr, "nautilus historian: -db / DATABASE_URL is required")
		return 2
	}
	patterns := splitCSVHist(*tagsFlag)
	if len(patterns) == 0 {
		patterns = []string{"*"}
	}
	excludes := splitCSVHist(*excludeFlag)

	// Retry until Postgres is reachable: the historian and its database
	// commonly start together (e.g. as sibling containers), and there is
	// no useful fallback besides waiting.
	var store *hist.Store
	for {
		s, err := hist.Open(*dbURL)
		if err == nil {
			store = s
			break
		}
		fmt.Fprintf(os.Stderr, "nautilus historian: waiting for database: %v\n", err)
		time.Sleep(3 * time.Second)
	}
	defer store.Close()
	fmt.Fprintf(os.Stderr, "nautilus historian: connected to database, polling %s\n", *source)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runCollector(ctx, store, *source, patterns, excludes, *interval, *deadband, *minInterval)
	go pruneLoop(ctx, store, *retention)

	srv := &historianServer{store: store}
	fmt.Fprintf(os.Stderr, "nautilus historian: serving history on %s\n", *addr)
	if err := http.ListenAndServe(*addr, srv.handler()); err != nil {
		fmt.Fprintln(os.Stderr, "nautilus historian:", err)
		return 1
	}
	return 0
}

// sampleOf extracts the archivable sample from one /api/state poll: every
// numeric or boolean leaf (recorded as 0/1) reachable from frameTags,
// flattened to a dotted "Tag.MEMBER.SUB" / "Tag.MEMBER[3]" name (see
// flattenLeaves), whose dotted name matches at least one of patterns and
// none of excludes. Strings are skipped at any depth — the historian only
// archives scalar process data, and a member holding e.g. online-edit
// source text would otherwise poison a numeric series.
//
// Pure and side-effect free so it's testable without an HTTP server or a
// database — see histcmd_test.go.
func sampleOf(frameTags map[string]any, patterns, excludes []string) map[string]float64 {
	leaves := make(map[string]float64)
	for name, v := range frameTags {
		flattenLeaves(name, v, leaves)
	}
	out := make(map[string]float64, len(leaves))
	for name, v := range leaves {
		if !matchesAny(name, patterns) {
			continue
		}
		if matchesAny(name, excludes) {
			continue
		}
		out[name] = v
	}
	return out
}

// flattenLeaves recurses into a JSON-decoded tag value (as produced by
// decoding GET /api/state's tags map into map[string]any — see
// frameTagsResponse), writing one entry into out per numeric or boolean
// leaf it finds, keyed by a dotted path rooted at name.
//
// A struct (UDT) tag arrives as a JSON object (map[string]any) — each
// member is appended as "name.MEMBER", recursing further for a
// nested struct. An array member arrives as []any — each element is
// appended as "name[i]". Strings and nil are dropped at any depth, same
// as sampleOf's original top-level-only behavior.
func flattenLeaves(name string, v any, out map[string]float64) {
	switch t := v.(type) {
	case float64:
		out[name] = t
	case bool:
		if t {
			out[name] = 1
		} else {
			out[name] = 0
		}
	case map[string]any:
		for member, mv := range t {
			flattenLeaves(name+"."+member, mv, out)
		}
	case []any:
		for i, ev := range t {
			flattenLeaves(fmt.Sprintf("%s[%d]", name, i), ev, out)
		}
	default:
		// string, nil — not an archivable scalar at any depth.
	}
}

// matchesAny reports whether name matches any of the glob patterns
// (path.Match syntax — "*", "?", "[...]"; "*" matches "." since
// path.Match only treats "/" as a separator, which is what lets a pattern
// like "*.VALUE" or "*_FIT_*" match a dotted leaf name such as
// "RTU9_WEL15_FIT_001.VALUE"). A malformed pattern never matches rather
// than erroring, since a typo'd -tags/-exclude flag shouldn't crash the
// collector loop.
func matchesAny(name string, patterns []string) bool {
	for _, p := range patterns {
		if ok, err := path.Match(p, name); err == nil && ok {
			return true
		}
	}
	return false
}

// changeFilter tracks, per tag, the last value and time recorded so
// runCollector can apply the optional -deadband / -min-interval
// change-based filtering across successive polls. Safe for concurrent use
// though the collector only ever drives it from one goroutine today.
type changeFilter struct {
	mu    sync.Mutex
	state map[string]changeState
}

type changeState struct {
	value float64
	at    time.Time
}

// apply narrows sample down to the entries that should actually be
// recorded at now, given deadband/minInterval policy, and updates f's
// state for every entry it keeps.
//
// minInterval <= 0 disables filtering entirely: apply returns sample
// unchanged and leaves f's state untouched — this is the default, and
// preserves the historian's original behavior of recording every matched
// tag on every poll.
//
// With minInterval > 0, a tag is kept when it's new (never recorded
// before), when it moved by more than deadband since its last recorded
// value, or when at least minInterval has elapsed since it was last
// recorded — so a flatlined tag still gets a periodic heartbeat sample
// instead of vanishing from a trend between real changes.
func (f *changeFilter) apply(now time.Time, sample map[string]float64, deadband float64, minInterval time.Duration) map[string]float64 {
	if minInterval <= 0 {
		return sample
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.state == nil {
		f.state = make(map[string]changeState)
	}
	out := make(map[string]float64, len(sample))
	for tag, v := range sample {
		prev, seen := f.state[tag]
		changed := !seen || math.Abs(v-prev.value) > deadband
		stale := !seen || now.Sub(prev.at) >= minInterval
		if changed || stale {
			out[tag] = v
			f.state[tag] = changeState{value: v, at: now}
		}
	}
	return out
}

// runCollector polls source once per interval and archives a sample into
// sink. Kept separate from sampleOf so the network/decode/insert plumbing
// and the pure filtering logic can be tested independently.
//
// filter state (last value/time per tag) is scoped to one runCollector
// call, matching the collector's own lifetime.
func runCollector(ctx context.Context, sink hist.Sink, source string, patterns, excludes []string, interval time.Duration, deadband float64, minInterval time.Duration) {
	hc := &http.Client{Timeout: 2 * time.Second}
	t := time.NewTicker(interval)
	defer t.Stop()
	filter := &changeFilter{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			collectOnce(hc, sink, source, patterns, excludes, filter, deadband, minInterval)
		}
	}
}

// collectOnce performs one poll-and-archive step. Split out from
// runCollector so a test can drive it directly against an httptest server
// and a fake Sink without a ticker in the way. filter may be nil (or a
// zero-value &changeFilter{}), in which case passing minInterval <= 0
// disables filtering, matching the historian's default.
//
// Timestamps are taken here, at the collector, rather than trusting the
// source's own clock or a value embedded in the response — as the
// original mini-scada historian did. That means a sample can carry up to
// one interval's jitter relative to when the controller actually scanned
// it (network + JSON decode latency), which is acceptable for trend
// charts but worth knowing if sub-second alignment ever matters.
func collectOnce(hc *http.Client, sink hist.Sink, source string, patterns, excludes []string, filter *changeFilter, deadband float64, minInterval time.Duration) {
	resp, err := hc.Get(source + "/api/state")
	if err != nil {
		// Source momentarily unreachable — e.g. mid-failover, where the
		// standby proxy means the Service answers again in seconds. Skip
		// this tick and try again next interval rather than erroring out.
		return
	}
	defer resp.Body.Close()
	var frame frameTagsResponse
	if err := json.NewDecoder(resp.Body).Decode(&frame); err != nil {
		return
	}
	sample := sampleOf(frame.Tags, patterns, excludes)
	now := time.Now()
	if filter != nil {
		sample = filter.apply(now, sample, deadband, minInterval)
	}
	if err := sink.Insert(now, sample); err != nil {
		fmt.Fprintf(os.Stderr, "nautilus historian: insert: %v\n", err)
	}
}

func pruneLoop(ctx context.Context, store *hist.Store, keep time.Duration) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := store.Prune(keep); err != nil {
				fmt.Fprintf(os.Stderr, "nautilus historian: prune: %v\n", err)
			}
		}
	}
}

// histStore is the subset of *hist.Store the historian's HTTP API drives,
// factored out so handler tests can exercise handleAgg/handleAt against a
// fake without a real Postgres — mirroring hist.Sink's role for the
// collector side.
type histStore interface {
	Query(from, to time.Time, tags []string, maxPoints int) (map[string]hist.Series, error)
	Span() (first, last time.Time, count int64, err error)
	Aggregate(ctx context.Context, q hist.AggQuery) ([]hist.AggRow, error)
	Snapshot(ctx context.Context, tags []string, at time.Time) ([]hist.AggRow, error)
}

var _ histStore = (*hist.Store)(nil)

type historianServer struct{ store histStore }

func (s *historianServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /history", s.handleQuery)
	mux.HandleFunc("GET /history/span", s.handleSpan)
	mux.HandleFunc("GET /history/agg", s.handleAgg)
	mux.HandleFunc("GET /history/at", s.handleAt)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	return mux
}

func (s *historianServer) handleQuery(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	to := parseMsHist(q.Get("to"), time.Now())
	from := parseMsHist(q.Get("from"), to.Add(-time.Hour))
	maxPoints, _ := strconv.Atoi(q.Get("maxPoints"))
	// Unlike the original (which defaulted to its hardcoded 7-tag list),
	// there is no fixed tag set to fall back to here — an empty/missing
	// tags param yields an empty series map rather than guessing.
	tags := splitCSVHist(q.Get("tags"))
	series, err := s.store.Query(from, to, tags, maxPoints)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"from": from.UnixMilli(), "to": to.UnixMilli(), "series": series,
	})
}

// aggRowJSON is the wire shape for /history/agg and /history/at: hist.AggRow
// with Ts as a ms-epoch int, matching every other endpoint's timestamp
// convention (handleQuery, handleSpan) instead of hist.AggRow's Go-domain
// time.Time.
type aggRowJSON struct {
	Tag   string  `json:"tag"`
	Ts    int64   `json:"ts"`
	Value float64 `json:"value"`
}

func toAggRowsJSON(rows []hist.AggRow) []aggRowJSON {
	out := make([]aggRowJSON, len(rows))
	for i, r := range rows {
		out[i] = aggRowJSON{Tag: r.Tag, Ts: r.Ts.UnixMilli(), Value: r.Value}
	}
	return out
}

// handleAgg serves GET /history/agg — bucketed min/max/avg/sum/count/
// first/last/delta/ontime per tag, computed server-side by hist.Store.
// Aggregate so a compliance/daily report never has to pull raw rows down
// to compute its own totals.
func (s *historianServer) handleAgg(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	tags := splitCSVHist(q.Get("tags"))
	if len(tags) == 0 {
		http.Error(w, "tags is required", http.StatusBadRequest)
		return
	}
	fn := q.Get("fn")
	if fn == "" {
		http.Error(w, "fn is required", http.StatusBadRequest)
		return
	}
	to := parseMsHist(q.Get("to"), time.Now())
	from := parseMsHist(q.Get("from"), to.Add(-24*time.Hour))
	var bucket time.Duration
	if b := q.Get("bucket"); b != "" {
		d, err := time.ParseDuration(b)
		if err != nil {
			http.Error(w, "bad bucket: "+err.Error(), http.StatusBadRequest)
			return
		}
		bucket = d
	}
	rows, err := s.store.Aggregate(r.Context(), hist.AggQuery{
		Tags: tags, From: from, To: to, Bucket: bucket, Fn: fn,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(toAggRowsJSON(rows))
}

// handleAt serves GET /history/at — each tag's last value at-or-before
// ?at, e.g. a reservoir level snapshot at 6am.
func (s *historianServer) handleAt(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	tags := splitCSVHist(q.Get("tags"))
	if len(tags) == 0 {
		http.Error(w, "tags is required", http.StatusBadRequest)
		return
	}
	at := parseMsHist(q.Get("at"), time.Now())
	rows, err := s.store.Snapshot(r.Context(), tags, at)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(toAggRowsJSON(rows))
}

func (s *historianServer) handleSpan(w http.ResponseWriter, r *http.Request) {
	first, last, count, err := s.store.Span()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"first": first.UnixMilli(), "last": last.UnixMilli(), "count": count,
	})
}

func parseMsHist(s string, def time.Time) time.Time {
	if s == "" {
		return def
	}
	ms, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return def
	}
	return time.UnixMilli(ms)
}

// splitCSVHist splits a comma-separated flag/query value, dropping empty
// fields (so a trailing comma or empty string yields no elements).
func splitCSVHist(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func envDefault(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
