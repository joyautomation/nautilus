// Package server exposes a nautilus Runtime over HTTP so HMIs, the VS Code
// extension's inline live values, and any other observer can read (and
// write) the tag store without bespoke wiring:
//
//	GET  /             a self-contained live dashboard (landing page)
//	GET  /api/state    one JSON Frame — the current tag snapshot
//	GET  /api/stream   Server-Sent Events; one Frame per broadcast tick
//	POST /api/tags     {"name": "TempSP", "value": 65.0} — write one tag
//
// The Frame shape is deliberately generic (every tag, plus scan stats) so
// the hmi kit's frame-generic realtime client and the editor tooling share
// one endpoint. Pure stdlib.
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
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/joyautomation/nautilus/runtime"
)

// indexHTML is the built-in landing page: a self-contained live dashboard
// served at "/", so hitting the controller in a browser shows running tags
// and the API surface instead of a bare 404.
//
//go:embed index.html
var indexHTML []byte

// Frame is one observation of the runtime: the full tag store plus loop
// progress, timestamped server-side.
type Frame struct {
	TS    int64          `json:"ts"` // epoch milliseconds
	Scans uint64         `json:"scans"`
	Tags  map[string]any `json:"tags"`
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
}

// Server fans runtime frames out to SSE clients and answers snapshot reads.
type Server struct {
	rt          *runtime.Runtime
	interval    time.Duration
	authToken   string
	onlineEdits bool

	mu      sync.Mutex
	clients map[chan []byte]struct{}
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
	return &Server{
		rt:          rt,
		interval:    interval,
		authToken:   token,
		onlineEdits: onlineEdits,
		clients:     map[chan []byte]struct{}{},
	}
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
	return Frame{
		TS:    time.Now().UnixMilli(),
		Scans: s.rt.Stats().Count,
		Tags:  s.rt.Tags().All(),
	}
}

// Handler returns the API routes. Mount it directly or under your own mux.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/state", s.handleState)
	mux.HandleFunc("GET /api/stream", s.handleStream)
	mux.HandleFunc("POST /api/tags", s.handleWriteTag)
	mux.HandleFunc("GET /api/program", s.handleGetProgram)
	mux.HandleFunc("PUT /api/program", s.handlePutProgram)
	mux.HandleFunc("POST /api/program/rollback", s.handleRollback)
	mux.HandleFunc("GET /", s.handleIndex)
	return withCORS(mux)
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
// lands here; non-root paths get a real 404 rather than the page.
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(indexHTML)
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.frame())
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

// writeTagRequest is the POST /api/tags payload. Value must be a JSON
// number or boolean — the tag kinds the runtime can store.
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
	// Tags.Set silently ignores anything that isn't a number or bool, so
	// reject those here rather than returning 204 for a write that didn't
	// happen. JSON numbers decode to float64; booleans to bool.
	switch req.Value.(type) {
	case float64, bool:
		s.rt.Tags().Set(req.Name, req.Value)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "value must be a number or boolean", http.StatusUnprocessableEntity)
	}
}
