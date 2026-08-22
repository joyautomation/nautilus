// Package server exposes a nautilus Runtime over HTTP so HMIs, the VS Code
// extension's inline live values, and any other observer can read (and
// write) the tag store without bespoke wiring:
//
//	GET  /             a self-contained live dashboard (landing page); its
//	                   tag table writes setpoints back through POST /api/tags
//	GET  /api/state    one JSON Frame — the current tag snapshot
//	GET  /api/stream   Server-Sent Events; one Frame per broadcast tick
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
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

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
type Frame struct {
	TS      int64             `json:"ts"` // epoch milliseconds
	Scans   uint64            `json:"scans"`
	Tags    map[string]any    `json:"tags"`
	Locals  map[string]any    `json:"locals,omitempty"`
	Scan    runtime.ScanStats `json:"scan"`
	Drivers []DriverStatus    `json:"drivers,omitempty"`
}

// Options tunes the server; zero values mean defaults.
type Options struct {
	// Interval is the SSE broadcast period. Default 250ms — fast enough
	// for live editor values and HMI needles, slow enough to be negligible
	// load. Snapshots are taken only while at least one client is connected.
	Interval time.Duration

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
}

// DriverStatus is a field driver's or publisher's health, rendered by the
// HMI's driver-status components. The envelope is generic; Metrics and
// Extra carry the protocol-specific detail without the server needing to
// know the protocol.
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
}

// DriverMetric is one labeled readout on a status card (poll rate, msgs, …).
type DriverMetric struct {
	Label string  `json:"label"`
	Value float64 `json:"value"`
	Unit  string  `json:"unit,omitempty"`
	Text  string  `json:"text,omitempty"` // set for non-numeric values (overrides Value)
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

	mu      sync.Mutex
	clients map[chan []byte]struct{}
	active  string // last activated commit sha; see setActive
}

// New builds a Server over a runtime.
func New(rt *runtime.Runtime, opts ...Options) *Server {
	interval := 250 * time.Millisecond
	token := ""
	onlineEdits := false
	if len(opts) > 0 {
		if opts[0].Interval > 0 {
			interval = opts[0].Interval
		}
		token = opts[0].AuthToken
		onlineEdits = opts[0].OnlineEdits
	}
	s := &Server{
		rt:          rt,
		interval:    interval,
		authToken:   token,
		onlineEdits: onlineEdits,
		clients:     map[chan []byte]struct{}{},
	}
	if len(opts) > 0 {
		s.drivers = opts[0].Drivers
		s.cluster = opts[0].Cluster
		s.historian = opts[0].HistorianURL
		s.historyFn = opts[0].History
		s.sourcesAt = opts[0].SourcesAt
		s.hmi = opts[0].HMI
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

func (s *Server) broadcast() {
	s.mu.Lock()
	n := len(s.clients)
	s.mu.Unlock()
	if n == 0 {
		return // nobody listening — skip the snapshot
	}
	b, err := json.Marshal(s.frame())
	if err != nil {
		return
	}
	s.mu.Lock()
	for ch := range s.clients {
		select {
		case ch <- b:
		default: // slow client — drop the frame, never block the loop
		}
	}
	s.mu.Unlock()
}

func (s *Server) frame() Frame {
	stats := s.rt.Stats()
	f := Frame{
		TS:     time.Now().UnixMilli(),
		Scans:  stats.Count,
		Tags:   s.rt.Tags().All(),
		Locals: s.rt.AllLocals(),
		Scan:   stats,
	}
	if s.drivers != nil {
		f.Drivers = s.drivers()
	}
	return f
}

// Handler returns the API routes. Mount it directly or under your own mux.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/state", s.handleState)
	mux.HandleFunc("GET /api/meta", s.handleMeta)
	mux.HandleFunc("GET /api/drivers", s.handleDrivers)
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

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.frame())
}

// handleDrivers serves the current field-driver / publisher status list.
// Empty (but 200) when no Drivers provider is configured.
func (s *Server) handleDrivers(w http.ResponseWriter, r *http.Request) {
	var out []DriverStatus
	if s.drivers != nil {
		out = s.drivers()
	}
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
	})
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Tell buffering reverse proxies (nginx, some ingress controllers) not to
	// hold the response — an SSE stream never "finishes", so a proxy that
	// waits for EOF starves the client. Doesn't affect a direct connection;
	// a client-side inspecting proxy/extension can still buffer regardless.
	w.Header().Set("X-Accel-Buffering", "no")

	ch := make(chan []byte, 8)
	s.mu.Lock()
	s.clients[ch] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.clients, ch)
		s.mu.Unlock()
	}()

	// Send one frame immediately so a fresh client (editor decorations, a
	// just-opened HMI) isn't blank until the next tick.
	if b, err := json.Marshal(s.frame()); err == nil {
		fmt.Fprintf(w, "data: %s\n\n", b)
		fl.Flush()
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case b := <-ch:
			fmt.Fprintf(w, "data: %s\n\n", b)
			fl.Flush()
		}
	}
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
