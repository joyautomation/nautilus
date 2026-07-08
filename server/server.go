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
//	srv := server.New(rt)
//	go srv.Run(ctx)                       // broadcast loop
//	http.ListenAndServe(":8080", srv.Handler())
package server

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
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
}

// Server fans runtime frames out to SSE clients and answers snapshot reads.
type Server struct {
	rt       *runtime.Runtime
	interval time.Duration

	mu      sync.Mutex
	clients map[chan []byte]struct{}
}

// New builds a Server over a runtime.
func New(rt *runtime.Runtime, opts ...Options) *Server {
	interval := 250 * time.Millisecond
	if len(opts) > 0 && opts[0].Interval > 0 {
		interval = opts[0].Interval
	}
	return &Server{
		rt:       rt,
		interval: interval,
		clients:  map[chan []byte]struct{}{},
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
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
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

// writeTagRequest is the POST /api/tags payload. Value takes any JSON
// scalar; the tag store coerces types.
type writeTagRequest struct {
	Name  string `json:"name"`
	Value any    `json:"value"`
}

func (s *Server) handleWriteTag(w http.ResponseWriter, r *http.Request) {
	var req writeTagRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		http.Error(w, `expected {"name": ..., "value": ...}`, http.StatusBadRequest)
		return
	}
	s.rt.Tags().Set(req.Name, req.Value)
	w.WriteHeader(http.StatusNoContent)
}
