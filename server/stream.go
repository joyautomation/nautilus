package server

// SSE streaming: who is connected, what each of them is owed, and the two
// ways a stream is made small enough for a tablet.
//
// The problem this file exists to solve was measured on the Pomona WRD
// demo: 10,000-odd tags on the central host, /api/state 571 KB, and one SSE
// client pulling ~2 MB every ten seconds — four full renderings of the
// entire plant per second, whether or not anything moved. Fine for the one
// wall screen it was built for; ruinous for a handful of tablets on plant
// wifi, and there is no version of "more clients" that gets better.
//
// Two independent reductions, either usable alone:
//
//   - `?tags=` — a client that draws forty points subscribes to forty
//     points. Glob patterns over dotted tag names (path.Match), applied to
//     every frame including the first, and to /api/state as well so the
//     initial load matches the subscription.
//   - `?delta=1` — after the first full frame, send only the tags that
//     CHANGED. The runtime's write generations make this nearly free: each
//     client's entire delta state is one uint64 (the store generation it
//     was last brought up to date at), and "what changed since" is one
//     integer comparison per tag with no value comparison anywhere. See
//     runtime.Tags' "Write generations" and Tags.ChangedSince.
//
// Deltas are opt-in, not the default. The frame shape is a public API with
// clients this repo does not own — the VS Code extension's inline values,
// whatever anyone wired to /api/stream with curl — and silently switching
// them to partial frames would corrupt every one of them in a way that
// looks like the plant went quiet rather than like a protocol change. The
// HMI kit opts in (RealtimeClient's `delta` defaults to true), which covers
// the clients that matter without breaking the ones that don't ask.
//
// # Why deltas cannot lose an update
//
// A client's lastGen advances only when a frame is actually ENQUEUED to it.
// The broadcast loop drops frames for a client whose buffer is full (it
// must — a slow tablet cannot be allowed to stall the loop), and a dropped
// frame simply leaves lastGen where it was, so the next frame carries the
// changes the dropped one would have. A client that disconnects gets a new
// *client on reconnect and therefore a full frame. There is no path that
// skips a change, which is why Seq exists only to detect a broken
// CONNECTION, not a lost frame.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/runtime"
)

// defaultResync is how often a delta stream sends a full frame anyway.
// See Options.ResyncInterval for why a gap-free protocol still has one.
const defaultResync = 30 * time.Second

// maxTagPatterns caps `?tags=` so a request cannot turn a glob list into a
// per-tag CPU cost multiplier. Forty patterns is far past any real screen's
// subscription (which is usually one or two prefixes).
const maxTagPatterns = 40

// client is one connected SSE subscriber and everything the broadcast loop
// owes it: the channel its frames go down, what it asked for, and where its
// delta stream has got to.
type client struct {
	ch    chan []byte
	delta bool     // ?delta=1 and not ?full=1
	pats  []string // ?tags= globs; nil = every tag

	// mu guards the rest. It is taken by the broadcast goroutine once per
	// tick per client and by handleStream once at connect — never held
	// across an encode or a send.
	mu sync.Mutex
	// lastGen is the store generation this client has been brought up to
	// date at: the frame it was last SENT contained every change up to
	// here. Its whole delta state, in one integer.
	lastGen uint64
	// lastKeyGen is Tags.NameGeneration at that frame. When the tag SET
	// changes, a delta can no longer express the difference (it has no way
	// to say "this tag is gone"), so the next frame is full.
	lastKeyGen uint64
	seq        uint64
	lastFull   time.Time
}

// snapshot copies the client's delta state for one broadcast decision.
func (c *client) snapshot() (lastGen, lastKeyGen, seq uint64, lastFull time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastGen, c.lastKeyGen, c.seq, c.lastFull
}

// sent records that a frame carrying every change up to gen was enqueued.
// Called ONLY on a successful enqueue — see this file's header on why a
// dropped frame must leave lastGen alone.
func (c *client) sent(gen, keyGen uint64, full bool, at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastGen, c.lastKeyGen = gen, keyGen
	c.seq++
	if full {
		c.lastFull = at
	}
}

// broadcast builds and fans out one tick's frames.
//
// The work is arranged so the expensive parts happen at most once per tick
// no matter how many clients are connected: one shared sweep of the tag
// store for the delta clients (from the OLDEST generation any of them is
// at, then filtered per client from that slice), one rendering of each
// changed value, one whole-store render for the full-frame clients, and one
// JSON encoding shared by every client asking for the plain, unfiltered
// full frame — which is what the built-in dashboard and the editor ask for,
// and is byte-for-byte the frame this server has always sent.
func (s *Server) broadcast() {
	s.mu.Lock()
	cs := make([]*client, 0, len(s.clients))
	for c := range s.clients {
		cs = append(cs, c)
	}
	s.mu.Unlock()
	if len(cs) == 0 {
		return // nobody listening — skip the snapshot
	}

	tags := s.rt.Tags()
	// Both stamps are read BEFORE anything is built, and this is the whole
	// safety argument for the delta stream: a client is credited with the
	// generation read here, so a write that lands WHILE this tick is being
	// assembled is stamped as not-yet-sent and goes out next tick. Reading
	// them afterwards would credit the client with changes its frame does
	// not contain — a value frozen on screen until the next resync, which
	// is the exact failure this design exists to make impossible. The cost
	// of getting it right is that a tag is occasionally sent twice.
	gen := tags.Generation()
	keyGen := tags.NameGeneration()
	now := time.Now()

	// Decide per client, and find the oldest generation any delta client
	// needs, so one sweep serves all of them.
	deltaFor := make([]bool, len(cs))
	from := make([]uint64, len(cs))
	var minGen uint64
	haveDelta, needFull := false, false
	for i, c := range cs {
		lastGen, lastKeyGen, seq, lastFull := c.snapshot()
		d := c.delta && seq > 0 && lastKeyGen == keyGen &&
			(s.resync < 0 || now.Sub(lastFull) < s.resync)
		deltaFor[i], from[i] = d, lastGen
		if d {
			if !haveDelta || lastGen < minGen {
				minGen = lastGen
			}
			haveDelta = true
		} else {
			needFull = true
		}
	}

	// One sweep, one render of each changed value, shared by every delta
	// client. A client further behind than minGen does not exist by
	// construction; a client AHEAD of it filters the slice down.
	var changed []runtime.Change
	var rendered map[string]any
	if haveDelta {
		changed, _ = tags.ChangedSince(minGen, s.chBuf[:0])
		s.chBuf = changed
		rendered = make(map[string]any, len(changed))
		for i := range changed {
			rendered[changed[i].Name] = runtime.Plain(changed[i].Value)
		}
	}

	// The shared parts of every frame this tick.
	stats := s.rt.Stats()
	base := Frame{
		TS:    now.UnixMilli(),
		Scans: stats.Count,
		Scan:  stats,
	}
	if s.drivers != nil {
		base.Drivers = s.drivers()
	}
	base.Alarms = s.alarmSummary()
	quality := qualityJSON(s.rt.Quality())

	locals := s.rt.AllLocals()
	var fullTags map[string]any
	if needFull {
		fullTags = s.tagsFrame()
	}

	// Encoding is memoised across clients that would receive IDENTICAL
	// bytes, which in practice is nearly all of them: clients tick together,
	// so they share a generation and a sequence number, and a fleet of
	// tablets on the same screen shares a filter too. Without this a
	// 50-tablet delta stream costs 50 JSON encodings a tick to save bytes —
	// trading the server's CPU for the network's, which is not the trade
	// anyone asked for.
	enc := map[frameKey][]byte{}
	filtered := map[string]map[string]any{}
	deltas := map[deltaKey]map[string]any{}
	for i, c := range cs {
		f := base
		f.Locals = locals
		f.Quality = filterQuality(quality, c.pats)
		patsKey := strings.Join(c.pats, ",")
		full := !deltaFor[i]
		if deltaFor[i] {
			dk := deltaKey{from: from[i], pats: patsKey}
			d, ok := deltas[dk]
			if !ok {
				d = pickChanged(changed, rendered, from[i], c.pats)
				deltas[dk] = d
			}
			f.Tags = d
			f.Seq = c.seqPeek() + 1
		} else {
			t, ok := filtered[patsKey]
			if !ok {
				t = filterTags(fullTags, c.pats)
				filtered[patsKey] = t
			}
			f.Tags = t
			if c.delta {
				// A delta client's full frame: mark it so the client
				// replaces rather than merges. A plain client's frame stays
				// exactly as it always was — no seq, no full marker.
				f.Full = true
				f.Seq = c.seqPeek() + 1
			}
		}
		k := frameKey{delta: deltaFor[i], full: f.Full, from: from[i], seq: f.Seq, pats: patsKey}
		b, ok := enc[k]
		if !ok {
			var err error
			if b, err = json.Marshal(f); err != nil {
				continue
			}
			enc[k] = b
		}
		select {
		case c.ch <- b:
			c.sent(gen, keyGen, full || f.Full, now)
		default:
			// Slow client — drop the frame, never block the loop. Its
			// lastGen stays put, so the next frame carries what this one
			// would have: a dropped frame costs latency, never content.
		}
	}
}

// frameKey identifies frames that encode to the same bytes. `from` and
// `seq` are what separate two delta clients at different points in the
// stream; everything else on a tick's frames is shared.
// deltaKey identifies the change sets that are the same map: two clients
// at the same generation with the same filter are owed the same tags, and
// clients tick together, so this is nearly always one map for all of them.
type deltaKey struct {
	from uint64
	pats string
}

type frameKey struct {
	delta bool
	full  bool
	from  uint64
	seq   uint64
	pats  string
}

// seqPeek reads the client's frame counter for the frame about to be built.
// The value is only advisory until sent() commits it — a dropped frame
// reuses the same number, which is exactly right: seq counts frames the
// client actually receives.
func (c *client) seqPeek() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.seq
}

// pickChanged narrows one tick's shared change list to what this client is
// owed: everything newer than its own generation, matching its filter,
// already rendered.
func pickChanged(changed []runtime.Change, rendered map[string]any, from uint64, pats []string) map[string]any {
	out := make(map[string]any)
	for i := range changed {
		ch := &changed[i]
		if ch.Gen <= from || !matchAny(pats, ch.Name) {
			continue
		}
		out[ch.Name] = rendered[ch.Name]
	}
	return out
}

// handleStream serves the SSE stream.
//
// Query parameters, all optional and all backwards compatible — a request
// with none of them gets exactly the stream this endpoint has always
// served:
//
//	?tags=Tank*,P101*   only these tags, glob-matched with path.Match over
//	                    the dotted name. Applies to the first frame too.
//	?delta=1            after the first frame, send only what changed.
//	?full=1             never send deltas on this connection (overrides
//	                    delta=1) — the escape hatch for a client that has
//	                    reason to distrust its own merge.
//
// A delta client that sees a gap in `seq` should RECONNECT rather than ask
// for a resync: a new connection is a new client and gets a full frame by
// construction, and a stream whose sequence broke is one whose transport is
// already suspect.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	q := r.URL.Query()
	pats, err := parseTagFilter(q.Get("tags"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	delta := flag(q, "delta") && !flag(q, "full")

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Tell buffering reverse proxies (nginx, some ingress controllers) not to
	// hold the response — an SSE stream never "finishes", so a proxy that
	// waits for EOF starves the client. Doesn't affect a direct connection;
	// a client-side inspecting proxy/extension can still buffer regardless.
	w.Header().Set("X-Accel-Buffering", "no")

	c := &client{ch: make(chan []byte, 8), delta: delta, pats: pats}

	// The first frame is built and written BEFORE the client is registered,
	// which is what keeps its seq honest: registering first would let the
	// broadcast loop enqueue a frame that reaches the wire AFTER this one
	// but carries a LOWER seq, and a client's gap detector would fire on
	// its own first two frames. Building first also means the generation
	// stamped here predates registration, so the first delta may resend a
	// tag or two — a superset is always correct.
	tags := s.rt.Tags()
	// Read before the frame is built, for the reason broadcast spells out:
	// a stamp that predates the content is a tag resent, a stamp that
	// postdates it is a tag lost.
	gen := tags.Generation()
	keyGen := tags.NameGeneration()
	f := s.frame(pats)
	if delta {
		f.Full, f.Seq = true, 1
	}
	if b, err := json.Marshal(f); err == nil {
		fmt.Fprintf(w, "data: %s\n\n", b)
		fl.Flush()
		c.lastGen, c.lastKeyGen, c.seq, c.lastFull = gen, keyGen, 1, time.Now()
	}

	s.mu.Lock()
	s.clients[c] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.clients, c)
		s.mu.Unlock()
	}()

	for {
		select {
		case <-r.Context().Done():
			return
		case b := <-c.ch:
			fmt.Fprintf(w, "data: %s\n\n", b)
			fl.Flush()
		}
	}
}

// flag reads a query flag the way every HTTP API eventually has to:
// present-and-not-explicitly-false. A bare `?delta`, `?delta=1` and
// `?delta=true` all mean yes; `?delta=0` and `?delta=false` mean no; absent
// means no.
func flag(q url.Values, name string) bool {
	if !q.Has(name) {
		return false
	}
	switch strings.ToLower(q.Get(name)) {
	case "0", "false", "no", "off":
		return false
	}
	return true
}

// parseTagFilter splits a `?tags=` value into glob patterns and rejects the
// ones path.Match cannot compile — an unterminated character class would
// otherwise silently match nothing, and a screen bound to it would render
// as an empty plant rather than as an error.
func parseTagFilter(v string) ([]string, error) {
	if strings.TrimSpace(v) == "" {
		return nil, nil
	}
	var pats []string
	for _, p := range strings.Split(v, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, err := path.Match(p, "x"); err != nil {
			return nil, fmt.Errorf("bad tag pattern %q: %v", p, err)
		}
		pats = append(pats, p)
		if len(pats) > maxTagPatterns {
			return nil, fmt.Errorf("too many tag patterns (max %d)", maxTagPatterns)
		}
	}
	return pats, nil
}

// matchAny reports whether a tag name matches any pattern — with no
// patterns meaning "everything", so an unfiltered caller costs one length
// check per tag and nothing else.
//
// path.Match's separator is "/", which no nautilus tag name contains, so a
// "*" spans a whole dotted name: `Tank*` matches `Tank101.Level`, and
// `*.Level` matches the member address form a screen binds with.
func matchAny(pats []string, name string) bool {
	if len(pats) == 0 {
		return true
	}
	for _, p := range pats {
		if ok, _ := path.Match(p, name); ok {
			return true
		}
	}
	return false
}

// filterTags narrows a whole-store tag map to the matching names, returning
// the map itself when there is no filter (the common case — no copy, and
// the memoised snapshot stays shared).
func filterTags(all map[string]any, pats []string) map[string]any {
	if len(pats) == 0 {
		return all
	}
	out := make(map[string]any)
	for k, v := range all {
		if matchAny(pats, k) {
			out[k] = v
		}
	}
	return out
}

// filterQuality is filterTags for the quality map. A filtered stream must
// not carry quality for tags it isn't sending — the entries are the payload
// budget the "non-Good only" rule exists to protect.
func filterQuality(q map[string]string, pats []string) map[string]string {
	if len(q) == 0 {
		return nil
	}
	if len(pats) == 0 {
		return q
	}
	var out map[string]string
	for k, v := range q {
		if matchAny(pats, k) {
			if out == nil {
				out = make(map[string]string)
			}
			out[k] = v
		}
	}
	return out
}

// qualityJSON renders the runtime's quality map into the frame's wire form:
// the io.Quality tokens, nil when everything is Good so the field is
// omitted from the JSON entirely.
func qualityJSON(q map[string]nio.Quality) map[string]string {
	if len(q) == 0 {
		return nil
	}
	out := make(map[string]string, len(q))
	for k, v := range q {
		out[k] = v.String()
	}
	return out
}
