// Package server exposes a nautilus Runtime over HTTP so HMIs, the VS Code
// extension's inline live values, and any other observer can read (and
// write) the tag store without bespoke wiring:
//
//	GET  /             a self-contained live dashboard (landing page); its
//	                   tag table writes setpoints back through POST /api/tags
//	GET  /api/state    one JSON Frame — the current tag snapshot
//	                   ?tags=glob,glob — only the tags matching these
//	GET  /api/stream   Server-Sent Events; one Frame per broadcast tick
//	                   ?tags=glob,glob — only the tags matching these
//	                   ?delta=1        — after the first frame, send only
//	                                     what changed (see handleStream)
//	                   ?blocks=delta   — and gate the NON-tag blocks the
//	                                     same way (scan/drivers/alarms)
//	                   ?full=1         — never send deltas on this stream
//	GET  /api/meta     tag descriptions/units, I/O binding, scan target
//	POST /api/tags     {"name": "TempSP", "value": 65.0} — write one tag,
//	                   or one member of a struct tag by dotted path:
//	                   {"name": "P101.Drive.Speed", "value": 60.0}, or several
//	                   at once: {"name": "P101", "value": {"Cmd": true}}
//	GET  /api/cluster  this replica's redundancy status (leader.Status JSON)
//	GET  /assets/…     the dashboard's logo, favicon and brand fonts
//
// The Frame shape is deliberately generic (every tag, plus the scan loop's
// full PLC-style diagnostics) so the hmi kit's frame-generic realtime client
// and the editor tooling share one endpoint. Pure stdlib.
//
// Options.HMI turns the controller into a one-process HMI deploy: a built
// SPA (SvelteKit's `adapter-static` output, or any other static bundle)
// takes over "/" — with SPA-fallback routing — and the built-in dashboard
// above moves to "/_nautilus/" so it stays reachable. See Options.HMI and
// the manifest's `server.hmi`.
//
// Redundancy (Options.Cluster) makes every replica answerable behind a load
// balancer even though only the leader scans: GET /api/cluster always
// answers locally so a dashboard can see each replica's own view, and a
// standby transparently reverse-proxies everything else under /api/ to
// whoever holds the lease, since its own tag store is stale. See
// proxyStandby.
//
// Security is progressive. Reads are always open (LAN dashboards, editor
// live values). Writes are same-origin-only by default — enough to stop a
// random browser page from actuating outputs, with zero configuration.
// Set Options.AuthToken to require a token on writes (and allow authorized
// cross-origin writers); see authorizeWrite.
//
//	srv := server.New(rt)
//	go srv.Run(ctx)                       // broadcast loop
//	http.ListenAndServe(":8080", srv.Handler())
package server

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/joyautomation/nautilus/alarm"
	"github.com/joyautomation/nautilus/leader"
	"github.com/joyautomation/nautilus/runtime"
)

// indexHTML is the built-in landing page: a self-contained live dashboard
// served at "/", so hitting the controller in a browser shows running tags
// and the API surface instead of a bare 404. It is also the zeroth HMI: the
// tag table writes setpoints through POST /api/tags, which is enough to
// commission a loop before anyone has built a real operator screen. It
// offers that only on tags that are neither an Input nor an Output, since
// those two are reclaimed by the driver and the logic within one scan —
// which is why /api/meta's I/O lists are load-bearing for the page, not
// just documentation.
//
//go:embed index.html
var indexHTML []byte

// staticFS carries the page's brand furniture: the nautilus spiral, the
// favicon the docs site uses, and latin subsets of the three brand faces
// (Righteous / Space Grotesk / IBM Plex Mono — see assets/fonts/LICENSE).
// They ride in the binary rather than come off a CDN because a controller
// usually sits on a plant network that can't reach one, and a dashboard that
// silently degrades to Arial on the machine it's supposed to represent is
// not the product. All four faces together are ~65 KB.
//
//go:embed assets
var staticFS embed.FS

// assetTypes maps the extensions under assets/ to their content types. Go's
// mime table doesn't know woff2, and a font served as octet-stream is a
// coin-flip across browsers.
var assetTypes = map[string]string{
	".svg":   "image/svg+xml",
	".png":   "image/png",
	".woff2": "font/woff2",
}

// Frame is one observation of the runtime: the full tag store plus the scan
// loop's diagnostics, timestamped server-side. Scan carries the full
// PLC-style runtime diagnostics (phase breakdown, history, histogram) so a
// diagnostics page needs nothing but the stream — a fresh client gets the
// whole picture from its first frame. Locals are the program's retained VAR
// values (a PI integral, latches, FB instances with their pins) — the watch
// window's view inside the POU, read-only.
//
// # Delta frames
//
// On a delta stream (see handleStream) Tags carries only what CHANGED since
// this client's previous frame, and Seq/Full say which kind of frame this
// is: Full marks a complete snapshot to replace state with, and every other
// frame is a merge.
//
// The NON-tag blocks — Scan, Drivers, Alarms — follow the same rule, for
// the same reason. Measured on the Pomona WRD host, they are ~18 kB per
// frame (a 55-device driver status alone is ~13 kB), so a client that
// filtered its tags down to nothing still pulled 4.3 MB a minute: a floor
// no amount of tag filtering could get under. On a delta frame each of
// them is present only when it CHANGED (Drivers, Alarms) or when its
// cadence came round (Scan — see Options.DiagnosticsInterval), and ABSENT
// means unchanged, exactly as it does for a tag. A full frame — the first
// one and every resync — always carries all of them, which is what makes
// "absent means unchanged" safe: a client is never more than one resync
// away from a block it can vouch for.
//
// A plain (non-delta) client's frames carry every block every tick, no Seq
// and no Full, byte-for-byte what this server has always sent.
type Frame struct {
	TS     int64          `json:"ts"` // epoch milliseconds
	Scans  uint64         `json:"scans"`
	Tags   map[string]any `json:"tags"`
	Locals map[string]any `json:"locals,omitempty"`
	// Scan is the scan loop's diagnostics. A pointer so a delta frame can
	// omit it: the block is ~5 kB of history rings, and it is nil on the
	// delta frames between diagnostics cadences. Never nil on a full frame
	// or on a plain stream.
	Scan    *runtime.ScanStats `json:"scan,omitempty"`
	Drivers []DriverStatus     `json:"drivers,omitempty"`
	// Quality is the tags whose value is not to be trusted — ONLY the
	// non-Good ones, keyed by tag name, valued with io.Quality's token
	// ("stale" | "bad" | "notConnected"). Absent from the map means Good,
	// which is what keeps a healthy 10,000-tag plant paying nothing for the
	// feature: the field is omitted entirely. A name here need not appear
	// in Tags — "notConnected" is exactly the source that has never
	// delivered a value. See runtime.Runtime.Quality.
	Quality map[string]string `json:"quality,omitempty"`
	// Seq counts the frames sent to ONE client, from 1. Present only on a
	// delta stream, where it is the gap detector: frames are built from a
	// per-client generation and are never silently dropped mid-stream, so a
	// seq that skips means the connection did, and the client should
	// reconnect for a fresh full frame rather than merge into a state it
	// can no longer vouch for.
	Seq uint64 `json:"seq,omitempty"`
	// Full marks a complete snapshot: replace, don't merge. True on a delta
	// stream's first frame, on each periodic resync, and whenever the tag
	// SET changed (a delta cannot express a deletion). Omitted on a plain
	// stream, whose every frame is full by definition.
	Full bool `json:"full,omitempty"`
	// Alarms is the alarm engine's counts — never the rows. Present only
	// when Options.Alarms is set, so a controller without alarms and an
	// HMI built before them see exactly the frame they saw before. Its
	// Rev bumps on any state change, which is how an HMI knows to refetch
	// GET /api/alarms and how it knows not to — and, on a delta stream,
	// what decides whether the block goes on the wire at all: an unchanged
	// Rev is a block the client already holds.
	Alarms *alarm.Summary `json:"alarms,omitempty"`
}

// Options tunes the server; zero values mean defaults.
type Options struct {
	// Interval is the SSE broadcast period. Default 250ms — fast enough
	// for live editor values and HMI needles, slow enough to be negligible
	// load. Snapshots are taken only while at least one client is connected.
	Interval time.Duration

	// ResyncInterval is how often a delta stream sends a full frame anyway
	// (default 30s). Deltas are gap-free by construction — a frame that is
	// not enqueued does not advance the client's generation — so this is
	// not a correctness crutch; it is the bound on how long a client can
	// stay wrong if something outside that argument ever does go wrong (a
	// merge bug, a proxy that rewrites bodies, a client that reloaded its
	// own state from somewhere). Thirty seconds costs a 10,000-tag stream
	// about one full frame per 120 deltas and puts a ceiling on any drift.
	// Negative disables the periodic resync entirely.
	ResyncInterval time.Duration

	// DiagnosticsInterval is how often a DELTA frame carries the scan
	// diagnostics block (default 3s). Unlike the driver status and the
	// alarm counts, the scan block changes every single scan — there is no
	// "unchanged" to gate on — and it is ~5 kB of history rings, so at a
	// 250 ms tick it was 20 kB/s of the stream on its own.
	//
	// A cadence loses nothing as long as it is shorter than the span the
	// rings cover: Recent/Periods hold 180 samples, which is 18 s at a
	// 100 ms scan, so a block every 2 s still delivers every sample a
	// diagnostics page plots — just in batches. The live headline number
	// (Frame.Scans) rides every frame regardless.
	//
	// Zero uses the default; negative sends the block on every frame (what
	// this server did before the cadence existed). A plain, non-delta
	// stream and /api/state are unaffected — they always carry it.
	DiagnosticsInterval time.Duration

	// AuthToken turns on write authentication (progressive enhancement).
	// When empty (the default) nautilus runs unauthenticated: writes are
	// allowed only from same-origin browser pages and non-browser clients
	// (see authorizeWrite). When set, a tag write must present the token in
	// an "Authorization: Bearer <token>" or "X-Nautilus-Token: <token>"
	// header, which also permits authorized cross-origin writers. Reads are
	// never gated — dashboards and editor live values stay open on the LAN.
	AuthToken string

	// OnlineEdits enables the program endpoints (PUT /api/program, POST
	// /api/program/rollback) — PLC-style online edits of the running ST
	// program. Off by default: pushing logic is remote code execution on a
	// control system, so a controller must opt in (think keyswitch in
	// REMOTE). Online edits are ephemeral by design — a restart reverts to
	// the program the binary embeds; committing the source is how an edit
	// becomes permanent. Program writes honor AuthToken like tag writes.
	OnlineEdits bool

	// Drivers, if set, is polled for field-driver / publisher status and
	// served at GET /api/drivers (and included in each stream frame). It
	// keeps the server package free of any specific driver dependency —
	// the runner adapts eip.Health / sparkplug.Status into DriverStatus.
	Drivers func() []DriverStatus

	// Cluster reports this replica's redundancy state — leader.Elector's
	// Status method satisfies it. When set, GET /api/cluster serves it, and
	// requests a standby cannot answer from its own (stale, non-scanning)
	// tag store are reverse-proxied to the leader. Nil = no redundancy.
	Cluster interface{ Status() leader.Status }

	// HistorianURL, when set, proxies GET /api/history* to a historian
	// daemon (`nautilus historian`) at that base URL, so the HMI keeps one
	// origin for live and archived data. History reads answer on ANY
	// replica — the archive lives in the historian, not the tag store, so
	// a standby's copy is as good as the leader's. Empty = 503 with a
	// pointer at the missing configuration.
	HistorianURL string

	// History, when set, feeds GET /api/program/history: the project's
	// captured git provenance (see ProgramHistory). It's a getter, not a
	// value, so the runner can capture lazily — a built binary decodes its
	// embedded snapshot on first request, `nautilus run` shells out to git
	// then — without holding up the scan loop's start. Nil (or a getter
	// returning nil) serves an empty history; the endpoint never 404s.
	History func() *ProgramHistory

	// SourcesAt resolves a commit sha from History to every task's composed
	// program source (libraries joined ahead, exactly as boot composes
	// them), keyed by task name — what POST /api/program/activate swaps in.
	// The runner wires this through the project loader so composition rules
	// live in one place; nil disables activation while leaving the history
	// readable.
	SourcesAt func(sha string) (map[string]string, error)

	// HMI, when set, serves a built HMI — a SvelteKit `adapter-static`
	// build, or any other static SPA bundle — at "/", so the controller is
	// a one-process deploy: no separate web server for the operator screen.
	// An unmatched, non-"/api" path falls back to the bundle's index.html
	// (SPA client-side routing), and the built-in dashboard moves to
	// "/_nautilus/" (its assets to "/_nautilus/assets/") so it stays
	// reachable without colliding with whatever the HMI's own build puts
	// under "/assets/". Nil (the default): the dashboard keeps "/" exactly
	// as before.
	//
	// The HMI must call the API same-origin (a relative "/api/..." base
	// URL, not an absolute host) — see the manifest's `server.hmi` and the
	// "Serving the HMI from the controller" guide.
	HMI fs.FS

	// Alarms, when set, serves the five /api/alarms* routes and puts the
	// engine's Summary on every stream frame. Nil (the default): those
	// routes 404 and the frame carries no alarms field at all — which is
	// what the HMI kit's AlarmClient reads as "this controller has no
	// alarm engine" and renders nothing for, rather than showing an empty
	// list that looks like a quiet plant.
	Alarms *alarm.Engine

	// AlarmShelveTimes is the shelf durations an operator screen offers,
	// served on /api/meta as seconds. Empty uses alarm.DefaultShelveTimes.
	// It is configuration, not policy: the engine accepts any deadline in
	// the future, and this is only what the picker suggests.
	AlarmShelveTimes []time.Duration
}

// DriverStatus is a field driver's or publisher's health, rendered by the
// HMI's driver-status components. The envelope is generic; Metrics and
// Extra carry the protocol-specific detail without the server needing to
// know the protocol.
//
// # Keeping it off the wire
//
// A driver status is the most expensive block on a frame — a Sparkplug host
// fronting 55 edge nodes renders ~13 kB of device rows and Extra — and on a
// delta stream it is sent only when it CHANGED (see hashDrivers). What
// "changed" means is therefore part of this type's contract, and an adapter
// that renders free-running values into it defeats the whole mechanism:
//
//   - AsOfMs is stamped by the server, not the adapter, and is excluded
//     from the comparison — it says WHEN this block was built, which is
//     what lets a client render "last poll 0.4s" from AtMs without the age
//     drifting upward between blocks.
//   - A metric that free-runs (a message counter, a poll count) sets
//     DriverMetric.Volatile, and one that reports a moment in time sets
//     AtMs instead of pre-rendering an age into Text.
//   - Extra keys that free-run are named in VolatileExtra.
//
// Volatile values still ride along whenever the block IS sent; they simply
// do not, on their own, put 13 kB on 4 frames a second. Their worst-case
// staleness on a delta stream is one resync (30 s by default).
type DriverStatus struct {
	Kind      string         `json:"kind"`    // "ethernet-ip" | "sparkplug"
	Name      string         `json:"name"`    // display name (host, or group/node)
	Detail    string         `json:"detail"`  // one-line address/target
	State     string         `json:"state"`   // connected|connecting|waiting|degraded|error|offline
	Message   string         `json:"message"` // human sentence for the current state
	SinceMs   int64          `json:"sinceMs"` // epoch ms the current state began (0 = unknown)
	LastError string         `json:"lastError,omitempty"`
	Metrics   []DriverMetric `json:"metrics,omitempty"` // labeled scalar readouts
	Devices   []DriverDevice `json:"devices,omitempty"` // sub-devices (Sparkplug devices, etc.)
	Extra     map[string]any `json:"extra,omitempty"`   // protocol-specific structured fields

	// AsOfMs is when this status was OBSERVED, stamped by the server. On a
	// delta stream the block may be several seconds older than the frame
	// carrying it, so every age rendered from it (a metric's AtMs) must be
	// measured against this, not against the reader's clock — otherwise a
	// perfectly healthy "last publish 0.2s" creeps up to 30s and back on
	// every resync, which reads as a plant going quiet.
	AsOfMs int64 `json:"asOfMs,omitempty"`

	// VolatileExtra names Extra keys that free-run — a message count, a
	// last-seen timestamp, anything that moves on its own. They are
	// excluded from the change comparison, so they do not by themselves
	// push the whole status onto the wire. Never serialised.
	VolatileExtra []string `json:"-"`
}

// DriverMetric is one labeled readout on a status card (poll rate, msgs, …).
type DriverMetric struct {
	Label string  `json:"label"`
	Value float64 `json:"value"`
	Unit  string  `json:"unit,omitempty"`
	Text  string  `json:"text,omitempty"` // set for non-numeric values (overrides Value)

	// AtMs is a moment in time (epoch ms) the reader should render as an
	// age — "last poll", "last publish". Prefer it to pre-rendering the age
	// into Text: a rendered age changes on every build, which on a delta
	// stream would put the whole driver block on the wire four times a
	// second. Clients render it against DriverStatus.AsOfMs. Adapters may
	// set Text as well, for readers that predate this field; that text is
	// ignored for change detection whenever AtMs is set.
	AtMs int64 `json:"atMs,omitempty"`

	// Volatile marks a free-running readout — a counter that ticks with
	// traffic rather than with anything an operator would call a change.
	// Excluded from the change comparison (see DriverStatus). Never
	// serialised: it is a statement about the value, not part of it.
	Volatile bool `json:"-"`
}

// DriverDevice is one sub-device under a driver (a Sparkplug device).
type DriverDevice struct {
	ID     string `json:"id"`
	Online bool   `json:"online"`
	Detail string `json:"detail,omitempty"`
}

// Server fans runtime frames out to SSE clients and answers snapshot reads.
type Server struct {
	rt          *runtime.Runtime
	interval    time.Duration
	authToken   string
	onlineEdits bool
	drivers     func() []DriverStatus
	cluster     interface{ Status() leader.Status }
	historian   string
	historyFn   func() *ProgramHistory
	sourcesAt   func(sha string) (map[string]string, error)
	hmi         fs.FS
	alarms      *alarm.Engine
	shelveTimes []time.Duration

	resync time.Duration
	diag   time.Duration

	mu      sync.Mutex
	clients map[*client]struct{}
	active  string // last activated commit sha; see setActive

	// chBuf is the broadcast loop's reusable Change buffer — one delta
	// sweep per tick, shared by every delta client (see broadcast). Touched
	// only from the broadcast goroutine.
	chBuf []runtime.Change

	// The non-tag blocks' change tracking, all touched ONLY from the
	// broadcast goroutine (like chBuf) — a client's own copy of where it
	// has got to lives on the client, under its mutex.
	//
	// driversHash is the last hashDrivers value; driversRev counts the
	// times it changed, so a client compares one integer instead of a hash
	// it would have to store. scanRev counts diagnostics cadences.
	driversHash uint64
	driversRev  uint64
	scanRev     uint64
	scanAt      time.Time

	// Tag-map memo for the frame builder. A frame's Tags block is the
	// plain-JSON rendering of the WHOLE store, and on a controller with
	// thousands of tags that is by far the most expensive part of a frame —
	// rebuilt four times a second whether or not a single value moved.
	// runtime.Tags.Generation says whether it moved. The memoised map is
	// never mutated after it is built (a change replaces it wholesale), so
	// concurrent frame encoders may share one.
	tagMu   sync.Mutex
	tagGen  uint64
	tagSnap map[string]any
}

// New builds a Server over a runtime.
func New(rt *runtime.Runtime, opts ...Options) *Server {
	interval := 250 * time.Millisecond
	resync := defaultResync
	diag := defaultDiagnostics
	token := ""
	onlineEdits := false
	if len(opts) > 0 {
		if opts[0].Interval > 0 {
			interval = opts[0].Interval
		}
		if opts[0].ResyncInterval != 0 {
			resync = opts[0].ResyncInterval
		}
		if opts[0].DiagnosticsInterval != 0 {
			diag = opts[0].DiagnosticsInterval
		}
		token = opts[0].AuthToken
		onlineEdits = opts[0].OnlineEdits
	}
	s := &Server{
		rt:          rt,
		interval:    interval,
		resync:      resync,
		diag:        diag,
		authToken:   token,
		onlineEdits: onlineEdits,
		clients:     map[*client]struct{}{},
	}
	if len(opts) > 0 {
		s.drivers = opts[0].Drivers
		s.cluster = opts[0].Cluster
		s.historian = opts[0].HistorianURL
		s.historyFn = opts[0].History
		s.sourcesAt = opts[0].SourcesAt
		s.hmi = opts[0].HMI
		s.alarms = opts[0].Alarms
		s.shelveTimes = opts[0].AlarmShelveTimes
	}
	if len(s.shelveTimes) == 0 {
		s.shelveTimes = alarm.DefaultShelveTimes
	}
	return s
}

// Run drives the SSE broadcast loop until ctx is cancelled. Without it the
// HTTP endpoints still work; /api/stream just never emits frames.
func (s *Server) Run(ctx context.Context) {
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.broadcast()
		}
	}
}

// tagsFrame is the frame's Tags block, rebuilt only when the tag store has
// actually changed. The generation is read BEFORE the rendering, so it can
// only ever under-state what the map holds — one redundant rebuild, never a
// stale frame.
func (s *Server) tagsFrame() map[string]any {
	tags := s.rt.Tags()
	gen := tags.Generation()
	s.tagMu.Lock()
	defer s.tagMu.Unlock()
	if s.tagSnap == nil || gen != s.tagGen {
		s.tagSnap, s.tagGen = tags.All(), gen
	}
	return s.tagSnap
}

// frame builds one complete Frame over every tag — the whole-store
// observation /api/state answers with and a plain SSE client receives every
// tick. pats, when non-empty, keeps only the tags (and quality entries)
// matching one of those globs; see matchAny.
func (s *Server) frame(pats []string) Frame {
	stats := s.rt.Stats()
	now := time.Now()
	f := Frame{
		TS:      now.UnixMilli(),
		Scans:   stats.Count,
		Tags:    filterTags(s.tagsFrame(), pats),
		Locals:  s.rt.AllLocals(),
		Scan:    &stats,
		Quality: filterQuality(qualityJSON(s.rt.Quality()), pats),
		Drivers: s.driverStatus(now),
	}
	f.Alarms = s.alarmSummary()
	return f
}

// driverStatus polls the configured driver provider and stamps each status
// with the moment it was observed. The stamp is the server's job, not the
// adapter's, because it is what a client renders a metric's age against —
// see DriverStatus.AsOfMs, and hashDrivers for why the stamp is excluded
// from the block's change comparison.
func (s *Server) driverStatus(now time.Time) []DriverStatus {
	if s.drivers == nil {
		return nil
	}
	ds := s.drivers()
	ms := now.UnixMilli()
	for i := range ds {
		if ds[i].AsOfMs == 0 {
			ds[i].AsOfMs = ms
		}
	}
	return ds
}

// Handler returns the API routes. Mount it directly or under your own mux.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/state", s.handleState)
	mux.HandleFunc("GET /api/meta", s.handleMeta)
	mux.HandleFunc("GET /api/drivers", s.handleDrivers)
	// Alarms: always registered, 404 when no engine — see server/alarms.go.
	// Registering them unconditionally is what makes "this controller has
	// no alarm engine" a message rather than a bare route-not-found.
	mux.HandleFunc("GET /api/alarms", s.handleAlarms)
	mux.HandleFunc("GET /api/alarms/journal", s.handleAlarmJournal)
	mux.HandleFunc("POST /api/alarms/ack", s.handleAlarmAck)
	mux.HandleFunc("POST /api/alarms/shelve", s.handleAlarmShelve)
	mux.HandleFunc("POST /api/alarms/unshelve", s.handleAlarmUnshelve)
	mux.HandleFunc("GET /api/stream", s.handleStream)
	mux.HandleFunc("POST /api/tags", s.handleWriteTag)
	mux.HandleFunc("GET /api/program", s.handleGetProgram)
	mux.HandleFunc("PUT /api/program", s.handlePutProgram)
	mux.HandleFunc("POST /api/program/rollback", s.handleRollback)
	mux.HandleFunc("GET /api/program/history", s.handleProgramHistory)
	mux.HandleFunc("POST /api/program/activate", s.handleActivate)
	mux.HandleFunc("GET /api/cluster", s.handleCluster)
	mux.HandleFunc("GET /api/history", s.handleHistory)
	mux.HandleFunc("GET /api/history/", s.handleHistory)
	if s.hmi != nil {
		// The HMI owns "/" (and whatever paths its own build wants under
		// it, "/assets/" included); the built-in dashboard moves out of
		// the way to "/_nautilus/" so it stays reachable without a name
		// collision.
		mux.HandleFunc("GET /_nautilus/", s.handleNautilusIndex)
		mux.HandleFunc("GET /_nautilus/assets/", handleAsset)
		mux.Handle("GET /", s.handleHMI())
	} else {
		mux.HandleFunc("GET /assets/", handleAsset)
		mux.HandleFunc("GET /", s.handleIndex)
	}
	return withCORS(s.proxyStandby(mux))
}

// proxyStandby wraps mux so that on a standby replica, requests it cannot
// safely answer from its own tag store are transparently reverse-proxied to
// the leader — the point being that a load balancer can hit any replica and
// still reach live data and accept writes. It only ever changes behavior
// when Options.Cluster is set; with no Cluster configured every request
// falls straight through to mux, exactly as before redundancy existed.
//
// Everything outside "/api/" is always answered locally, even on a
// standby: the built-in dashboard, its assets, and — when server.hmi is
// configured — the whole HMI build, SPA-fallback routes included. None of
// it reads the tag store, so a load-balancer health probe or a browser
// always gets a page, whichever replica it lands on, and a client-side
// route two levels deep in the HMI doesn't need special-casing here (it
// was never a "/api/" path to begin with). Within "/api/", GET
// /api/cluster is also always local (each replica reports its own view of
// the cluster — showing that divergence is the entire point), and so is
// /api/history*: the archive lives in the historian, not the tag store,
// so a standby answers it as well as the leader and keeps answering it
// mid-failover.
func (s *Server) proxyStandby(mux http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cluster == nil {
			mux.ServeHTTP(w, r)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/api/") ||
			r.URL.Path == "/api/cluster" ||
			strings.HasPrefix(r.URL.Path, "/api/history") {
			mux.ServeHTTP(w, r)
			return
		}
		st := s.cluster.Status()
		if st.IsLeader || st.LeaderAddr == "" {
			// We're the leader, or nobody's address is known yet (a fresh
			// cluster still electing, or a standalone-mode elector that never
			// sets LeaderAddr) — handle locally rather than erroring a
			// request that has nowhere better to go.
			mux.ServeHTTP(w, r)
			return
		}
		proxy := httputil.NewSingleHostReverseProxy(&url.URL{Scheme: "http", Host: st.LeaderAddr})
		// -1 disables buffering: an SSE frame must reach the client the
		// instant the leader emits it, not whenever the proxy's timer next
		// fires.
		proxy.FlushInterval = -1
		proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
			http.Error(w, "leader unavailable: "+err.Error(), http.StatusBadGateway)
		}
		proxy.ServeHTTP(w, r)
	})
}

// withCORS allows browser HMIs served from another origin (e.g. a Vite dev
// server) to call the API. The controller API carries no credentials, so a
// wildcard is appropriate; put a real gateway in front for anything exposed
// beyond the machine/plant network.
func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			// Allow the auth headers so an authorized cross-origin writer's
			// preflight succeeds; Content-Type for JSON bodies.
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Nautilus-Token")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// handleIndex serves the landing page at exactly "/". Because "GET /" is
// the catch-all pattern, anything not matched by a more specific route
// lands here; non-root paths get a real 404 rather than the page. Only
// mounted when no HMI is configured — see handleHMI.
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(indexHTML)
}

// handleNautilusIndex is handleIndex's counterpart when an HMI has claimed
// "/": the same page, moved to "/_nautilus/" so it's still reachable
// (linking a build's operator screen back to the raw tag table is exactly
// the point of moving it rather than dropping it).
func (s *Server) handleNautilusIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/_nautilus/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(indexHTML)
}

// handleHMI serves a configured HMI build (Options.HMI) at "/": a real
// file in the tree is served as-is (content type, range requests, caching
// — all http.FileServer's usual behavior); anything else falls back to
// the bundle's own index.html, so client-side routing (SvelteKit, or any
// other SPA router) resolves a deep link like "/tanks/101" itself instead
// of getting a 404 from this server, which has no idea such a route
// exists.
func (s *Server) handleHMI() http.Handler {
	fsrv := http.FileServer(http.FS(s.hmi))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		lookup := path.Clean(p)
		if lookup == "." || strings.HasSuffix(p, "/") {
			// The root, or a directory-style path (trailing slash): the
			// file that answers it — if any — is its own index.html, same
			// as http.FileServer's own directory convention.
			lookup = path.Join(lookup, "index.html")
		}
		if st, err := fs.Stat(s.hmi, lookup); err == nil && !st.IsDir() {
			fsrv.ServeHTTP(w, r)
			return
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/"
		fsrv.ServeHTTP(w, r2)
	})
}

// handleAsset serves the embedded logo, favicon and fonts. Paths are matched
// against the embedded tree, so nothing outside it is reachable; anything not
// in the tree (including a traversal attempt) is a plain 404.
//
// The cache lifetime is long because these bytes only change when the binary
// does, and a font re-fetched on every page load is a visible flash of
// fallback text on a wall-mounted dashboard that reloads all day.
//
// Mounted at both "/assets/" (no HMI configured) and "/_nautilus/assets/"
// (HMI configured, dashboard moved) — the "_nautilus/" prefix, if present,
// is stripped before the embedded-tree lookup so one handler serves both.
func handleAsset(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/")
	name = strings.TrimPrefix(name, "_nautilus/")
	b, err := staticFS.ReadFile(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if ct := assetTypes[path.Ext(name)]; ct != "" {
		w.Header().Set("Content-Type", ct)
	} else {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	}
	w.Header().Set("Cache-Control", "public, max-age=604800")
	w.Write(b)
}

// handleState answers one whole-store Frame. `?tags=` narrows it to the
// matching tags — the same globs /api/stream takes, so a screen that
// subscribes to a subset fetches the same subset on load instead of pulling
// half a megabyte to read forty points.
func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	pats, err := parseTagFilter(r.URL.Query().Get("tags"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.frame(pats))
}

// handleDrivers serves the current field-driver / publisher status list.
// Empty (but 200) when no Drivers provider is configured.
func (s *Server) handleDrivers(w http.ResponseWriter, r *http.Request) {
	out := s.driverStatus(time.Now())
	if out == nil {
		out = []DriverStatus{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

// handleCluster reports this replica's own view of redundancy: its pod
// name, who it believes holds the lease, and whether that's itself. It is
// never proxied (see proxyStandby) — a standby and the leader disagreeing
// briefly during a failover is the signal the dashboard exists to show, not
// a bug to paper over by asking the leader about itself.
//
// With no Cluster configured this still answers, as a standalone leader —
// so a dashboard can read /api/cluster unconditionally instead of special-
// casing "redundancy isn't wired up" as a 404.
func (s *Server) handleCluster(w http.ResponseWriter, r *http.Request) {
	st := leader.Status{Mode: "standalone", IsLeader: true}
	if s.cluster != nil {
		st = s.cluster.Status()
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(st)
}

// handleHistory forwards /api/history* to the historian daemon, trimming
// the /api prefix (/api/history/span → {historian}/history/span). The HMI
// keeps one origin; where the archive actually lives is deployment detail.
func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	if s.historian == "" {
		http.Error(w, "no historian configured (set server.historian in the manifest, or NAUTILUS_HISTORIAN_URL)", http.StatusServiceUnavailable)
		return
	}
	target, err := url.Parse(s.historian)
	if err != nil {
		http.Error(w, "bad historian url: "+err.Error(), http.StatusInternalServerError)
		return
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	orig := proxy.Director
	proxy.Director = func(req *http.Request) {
		orig(req)
		req.URL.Path = strings.TrimPrefix(r.URL.Path, "/api")
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		http.Error(w, "historian unavailable: "+err.Error(), http.StatusBadGateway)
	}
	proxy.ServeHTTP(w, r)
}

// metaResponse is the static tag documentation for an HMI: descriptions and
// units (runtime.Options.Meta) plus which tags are driver-bound. A tag table
// derives quality from this + the frame: an input while the scan reports
// ioHealthy=false is showing a stale, held value.
type metaResponse struct {
	Tags         map[string]runtime.TagMeta `json:"tags"`
	Inputs       []string                   `json:"inputs"`
	Outputs      []string                   `json:"outputs"`
	ScanTargetMs float64                    `json:"scanTargetMs"`
	// MemberWrites says this controller accepts POST /api/tags with a dotted
	// member path ("P101.Drive.Speed") or an object payload merging into a
	// struct tag. It is a capability flag, not a policy: writability itself
	// is still the root tag's (a member of an Input is refused like the
	// Input is). An HMI that binds UDT members needs it because the answer
	// used to be no — a dotted name silently created a junk tag — so a
	// faceplate built for a newer runtime must be able to tell, and disable
	// its controls against an older one instead of writing into the void.
	MemberWrites bool `json:"memberWrites"`
	// Alarms says this controller runs an alarm engine, so /api/alarms*
	// will answer. Same capability-flag reasoning as MemberWrites: an HMI
	// that renders an alarm banner needs to know whether to render one at
	// all, and finding out by taking a 404 makes an ordinary page load
	// look like an error.
	Alarms bool `json:"alarms"`
	// Quality says this controller can report per-tag data quality: it runs
	// a driver that implements io.QualityReporter, or simply has
	// driver-bound inputs the runtime can mark Stale on a read failure.
	// The capability flag matters more here than anywhere else in this
	// response, because the FALSE case is invisible: an empty `quality` map
	// looks exactly like a healthy plant, and an HMI that cannot tell the
	// two apart would render a confident green badge on a controller that
	// has no idea. With this false, a screen shows no quality indication at
	// all rather than a reassuring one.
	Quality bool `json:"quality"`
	// Deltas says GET /api/stream understands `?delta=1` and `?tags=`. An
	// HMI kit newer than the controller must not ask for a delta stream
	// from a server that will ignore the parameter and send full frames
	// the client then merges as if they were partial — which happens to be
	// harmless (a full frame merged over a subset is the full state), but
	// the client would never see `full` and could not tell resync from
	// steady state. Cheaper to advertise.
	Deltas bool `json:"deltas"`
	// BlockDeltas says GET /api/stream understands `?blocks=delta`: the
	// non-tag blocks (scan diagnostics, driver status, alarm counts) sent
	// only when they change, instead of on every frame. Advertised for the
	// same reason as Deltas — a client that merges partial blocks against
	// a controller that does not implement them is merely paying for
	// nothing, but a client that DOESN'T merge them against a controller
	// that does would blank a driver panel between changes, so the
	// capability is asked for explicitly rather than assumed.
	BlockDeltas bool `json:"blockDeltas"`
	// ShelveTimes is the shelf durations the operator screen's picker
	// offers, in SECONDS — the unit every other duration in this API's
	// JSON is not, but the one the HMI kit's DEFAULT_SHELVE_TIMES_S uses,
	// and a shelf of 4.5 milliseconds is not a thing anyone wants.
	ShelveTimes []int `json:"shelveTimes"`
}

func (s *Server) handleMeta(w http.ResponseWriter, r *http.Request) {
	meta := s.rt.Meta()
	if meta == nil {
		meta = map[string]runtime.TagMeta{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(metaResponse{
		Tags:         meta,
		Inputs:       nonNilStrings(s.rt.Inputs()),
		Outputs:      nonNilStrings(s.rt.Outputs()),
		ScanTargetMs: s.rt.Stats().TargetMs,
		MemberWrites: true,
		Alarms:       s.alarms != nil,
		Quality:      s.rt.ReportsQuality(),
		Deltas:       true,
		BlockDeltas:  true,
		ShelveTimes:  shelveSeconds(s.shelveTimes),
	})
}

func shelveSeconds(ds []time.Duration) []int {
	out := make([]int, 0, len(ds))
	for _, d := range ds {
		out = append(out, int(d.Seconds()))
	}
	return out
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// authorizeWrite decides whether a tag-write request may proceed, returning
// (0, "") to allow or an (HTTP status, message) to reject.
//
// Base layer — no AuthToken configured: writes must be same-origin. A
// browser page from another origin (the drive-by CSRF case) carries an
// Origin header that won't match the host and is refused; non-browser
// clients (curl, the LSP, server-to-server) send no Origin and are allowed.
// This costs nothing to run and needs no setup.
//
// Progressive enhancement — AuthToken set: the request must present the
// token, and a valid token authorizes the write from any origin (an
// attacker's page can't read or guess it, so CORS is irrelevant to safety).
func (s *Server) authorizeWrite(r *http.Request) (int, string) {
	if s.authToken != "" {
		if tokenMatches(r, s.authToken) {
			return 0, ""
		}
		return http.StatusUnauthorized, "missing or invalid auth token"
	}
	if sameOrigin(r) {
		return 0, ""
	}
	return http.StatusForbidden, "cross-origin writes require an auth token (start nautilus with one, e.g. NAUTILUS_TOKEN)"
}

// tokenMatches reports whether the request carries the expected token, in
// either "Authorization: Bearer <t>" or "X-Nautilus-Token: <t>" form.
// Comparison is constant-time.
func tokenMatches(r *http.Request, token string) bool {
	got := r.Header.Get("X-Nautilus-Token")
	if got == "" {
		if a := r.Header.Get("Authorization"); strings.HasPrefix(a, "Bearer ") {
			got = strings.TrimPrefix(a, "Bearer ")
		}
	}
	return got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1
}

// sameOrigin reports whether a request is safe from a CSRF standpoint: it
// either carries no Origin header (a non-browser client) or an Origin whose
// host matches the request's Host.
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true // not a browser-issued cross-origin request
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return u.Host == r.Host
}

// writeTagRequest is the POST /api/tags payload.
//
// Name addresses a whole tag ("TempSP") or one member of a struct tag by a
// dotted path ("P101.Drive.Speed") — the same paths a test manifest's
// `given:`/`expect:` and an HMI's tag bindings already use.
//
// Value is a JSON number or boolean for a scalar; for a struct tag it may
// also be an OBJECT of member → value, which merges (members it names are
// set, every other member keeps its current value).
type writeTagRequest struct {
	Name  string `json:"name"`
	Value any    `json:"value"`
}

func (s *Server) handleWriteTag(w http.ResponseWriter, r *http.Request) {
	if code, msg := s.authorizeWrite(r); code != 0 {
		http.Error(w, msg, code)
		return
	}
	var req writeTagRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		http.Error(w, `expected {"name": ..., "value": ...}`, http.StatusBadRequest)
		return
	}
	// A member write — a dotted name, or an object payload merging into a
	// struct tag — resolves through the tag's own StructDef and is
	// read-modify-written whole. It must address something that EXISTS: a
	// flat assignment of "P101.Spede" would otherwise create a top-level tag
	// under that literal name, which no program reads and a Sparkplug edge
	// would publish as a bogus metric. That is a 400 with the reason.
	_, isObject := req.Value.(map[string]any)
	if isObject || strings.Contains(req.Name, ".") {
		root, _, _ := strings.Cut(req.Name, ".")
		if msg := s.refuseMemberWrite(root); msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		if err := s.rt.Tags().SetPath(req.Name, req.Value); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// Whole-tag scalar write, unchanged: Tags.Set silently ignores anything
	// that isn't a number or bool, so reject those here rather than
	// returning 204 for a write that didn't happen. JSON numbers decode to
	// float64; booleans to bool.
	switch req.Value.(type) {
	case float64, bool:
		s.rt.Tags().Set(req.Name, req.Value)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "value must be a number or boolean", http.StatusUnprocessableEntity)
	}
}

// refuseMemberWrite reports why a member write to this root tag must not
// proceed, or "" to allow it. The role rule is applied to the ROOT tag, not
// to the member: the store holds whole tags, so writing P101.Cmd rewrites
// P101.
//
// Only a driver-owned INPUT is refused. A whole-tag write to one is still
// accepted (that is PLC forcing — it lands, and the driver takes the tag
// back on the next scan, which is visible and self-correcting), but a MEMBER
// write reads a base the driver replaces wholesale before the next scan, so
// the edit cannot survive even one cycle: it is a lost update dressed up as
// a command. Everything else stays writable — setpoints and state because
// they are the operator's, and OUTPUTS because that is exactly how a
// Sparkplug host commands an edge node: the operator writes the output tag
// and the host driver publishes it as a command.
func (s *Server) refuseMemberWrite(root string) string {
	for _, in := range s.rt.Inputs() {
		if in == root {
			return "tag " + root + " is a driver-owned input: the driver replaces its whole " +
				"value before every scan, so a member write would be discarded unread — " +
				"write the setpoint or command tag the logic reads instead"
		}
	}
	return ""
}
