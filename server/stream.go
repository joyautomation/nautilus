package server

// SSE streaming: who is connected, what each of them is owed, and the three
// ways a stream is made small enough for a tablet.
//
// The problem this file exists to solve was measured on the Pomona WRD
// demo: 10,000-odd tags on the central host, /api/state 571 KB, and one SSE
// client pulling ~2 MB every ten seconds — four full renderings of the
// entire plant per second, whether or not anything moved. Fine for the one
// wall screen it was built for; ruinous for a handful of tablets on plant
// wifi, and there is no version of "more clients" that gets better.
//
// Three independent reductions, each usable alone — two over the tags, and
// one over everything else on the frame (see "The frame floor" below):
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
// # The frame floor
//
// Those two reductions left a floor neither could get under. Measured on
// the same host: every frame carried ~17.9 kB of NON-tag payload — ~12.8 kB
// of driver status (55 device rows and the driver's own Extra), ~5 kB of
// scan diagnostics (two 180-sample history rings and a histogram), and the
// alarm summary — rebuilt and re-sent four times a second whether or not
// anything in them had moved. A client subscribed to zero tags still pulled
// 4.35 MB a minute.
//
// So a delta frame now gates those blocks by the same philosophy as the
// tags: send it when it CHANGED, and let absent mean unchanged.
//
//   - drivers — hashed over everything an operator would call a change and
//     nothing that free-runs (see hashDrivers), one revision counter for
//     the fleet, one integer per client.
//   - alarms — the engine already publishes a Rev that bumps when an alarm
//     moves and never otherwise. Gate on it, and skip building the summary
//     at all when nobody is owed one.
//   - scan — no "unchanged" exists: every scan changes it. So it rides a
//     CADENCE instead (Options.DiagnosticsInterval, 3s), which loses no
//     samples because the block is a history ring covering far longer than
//     the cadence.
//
// A full frame — the first and every resync — always carries all of them,
// which is what keeps "absent means unchanged" honest: no client is more
// than one resync from a block it can vouch for. And the same enqueue
// discipline as the tags applies, for the same reason: a client's record of
// which blocks it holds advances only when a frame is actually enqueued, so
// a dropped frame re-offers the block on the next tick.
//
// Deltas are opt-in, not the default. The frame shape is a public API with
// clients this repo does not own — the VS Code extension's inline values,
// whatever anyone wired to /api/stream with curl — and silently switching
// them to partial frames would corrupt every one of them in a way that
// looks like the plant went quiet rather than like a protocol change. The
// HMI kit opts in (RealtimeClient's `delta` defaults to true), which covers
// the clients that matter without breaking the ones that don't ask.
//
// The block gate is opt-in AGAIN, on top of deltas (`?blocks=delta`), for
// the same argument one level down: an HMI built against the older protocol
// merges tags but not blocks, so gating them for it would blank its driver
// panel between changes — the same failure wearing the same disguise. A kit
// that knows how to merge them asks; nothing else is affected.
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
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash"
	"hash/fnv"
	"io"
	"math"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/joyautomation/nautilus/alarm"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/runtime"
)

// defaultResync is how often a delta stream sends a full frame anyway.
// See Options.ResyncInterval for why a gap-free protocol still has one.
const defaultResync = 30 * time.Second

// defaultDiagnostics is how often a delta frame carries the scan block.
// See Options.DiagnosticsInterval for why this one is a cadence and not a
// change gate: the scan diagnostics change every scan, by definition.
//
// Three seconds is chosen against the block's own history rings: they hold
// 180 samples, so anything up to 180× the scan target loses no samples, and
// 3 s clears that for any scan of 17 ms or slower — every real controller.
// It is also what keeps the block from dominating what is left of the
// stream: at ~5 kB it is 1.7 kB/s here, against 0.5 kB/s for everything
// else a quiet delta client receives.
const defaultDiagnostics = 3 * time.Second

// maxTagPatterns caps `?tags=` so a request cannot turn a glob list into a
// per-tag CPU cost multiplier. Forty patterns is far past any real screen's
// subscription (which is usually one or two prefixes).
const maxTagPatterns = 40

// client is one connected SSE subscriber and everything the broadcast loop
// owes it: the channel its frames go down, what it asked for, and where its
// delta stream has got to.
type client struct {
	ch     chan []byte
	delta  bool     // ?delta=1 and not ?full=1
	blocks bool     // ?blocks=delta — gate the non-tag blocks too
	pats   []string // ?tags= globs; nil = every tag

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
	// seen is lastGen's counterpart for the non-tag blocks: which version
	// of each the client has actually been SENT. Same discipline — it
	// advances only on a successful enqueue, so a dropped frame re-offers
	// the block rather than losing it.
	seen blockRevs
}

// blockRevs is which version of each non-tag block a client holds — the
// whole state behind "send it only when it changed", three integers wide.
//
// Zero means "never sent": a freshly connected client is deliberately
// credited with nothing, even though its first frame was full, because the
// revisions are the broadcast goroutine's and reading them from the HTTP
// handler would be a race whose losing side is a block the client never
// gets. It costs one repeat of the non-tag blocks on the first broadcast
// after connect, which is a superset, and a superset is always correct.
type blockRevs struct {
	drivers uint64 // Server.driversRev of the driver block last sent
	scan    uint64 // Server.scanRev of the scan block last sent
	alarms  uint64 // alarm.Summary.Rev of the alarm block last sent
	// alarmsSent distinguishes "sent the summary at rev 0" — a controller
	// whose alarms have never moved — from "never sent one".
	alarmsSent bool
}

// snapshot copies the client's delta state for one broadcast decision.
func (c *client) snapshot() (lastGen, lastKeyGen, seq uint64, lastFull time.Time, seen blockRevs) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastGen, c.lastKeyGen, c.seq, c.lastFull, c.seen
}

// sent records that a frame carrying every change up to gen — and the
// blocks named by seen — was enqueued. Called ONLY on a successful enqueue
// — see this file's header on why a dropped frame must leave lastGen alone,
// which applies to the non-tag blocks exactly as it does to the tags.
func (c *client) sent(gen, keyGen uint64, full bool, at time.Time, seen blockRevs) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastGen, c.lastKeyGen = gen, keyGen
	c.seen = seen
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
	held := make([]blockRevs, len(cs))
	var minGen uint64
	haveDelta, needFull, anyGate := false, false, false
	for i, c := range cs {
		lastGen, lastKeyGen, seq, lastFull, seen := c.snapshot()
		d := c.delta && seq > 0 && lastKeyGen == keyGen &&
			(s.resync < 0 || now.Sub(lastFull) < s.resync)
		deltaFor[i], from[i], held[i] = d, lastGen, seen
		anyGate = anyGate || (d && c.blocks)
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

	// The shared parts of every frame this tick. Scans — the counter a
	// dashboard watches to see the loop turning — rides every frame; the
	// diagnostics BLOCK behind it does not (see below).
	stats := s.rt.Stats()
	base := Frame{
		TS:    now.UnixMilli(),
		Scans: stats.Count,
	}
	quality := qualityJSON(s.rt.Quality())

	// This tick's verdict on each non-tag block. They are the reason a
	// client that filtered its tags down to nothing still pulled megabytes
	// a minute: ~18 kB of driver status, scan history and alarm counts,
	// rebuilt and re-sent four times a second whether or not anything in
	// them moved. Each is now gated the way tags are — by change, or (for
	// the scan block, which changes every scan by definition) by cadence —
	// and the verdict is computed ONCE for the fleet, so clients that tick
	// together still share one encoding.
	if s.diag < 0 || s.scanAt.IsZero() || now.Sub(s.scanAt) >= s.diag {
		s.scanRev++
		s.scanAt = now
	}
	ds := s.driverStatus(now)
	// Hashing the driver block is worth doing only for a client that gates
	// on it — a fleet of plain clients pays nothing for a feature it is not
	// using. `driversRev == 0` forces the first evaluation to count, so a
	// freshly connected client (which holds revision zero) is always owed
	// the block whatever the hash happens to be.
	if anyGate {
		if h := hashDrivers(ds); h != s.driversHash || s.driversRev == 0 {
			s.driversHash, s.driversRev = h, s.driversRev+1
		}
	}
	// The alarm summary carries a revision of its own — Rev bumps when an
	// alarm moves and never otherwise — so the block needs no hash. Rev is
	// also far cheaper than Summary (which walks every instance), so the
	// summary itself is built only if some client turns out to be owed it.
	var alarmRev uint64
	var alarmSum *alarm.Summary
	if s.alarms != nil {
		alarmRev = s.alarms.Rev()
	}
	summary := func() *alarm.Summary {
		if alarmSum == nil {
			alarmSum = s.alarmSummary()
		}
		return alarmSum
	}

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

		// The non-tag blocks. A whole-store frame carries all of them —
		// that is what makes a full frame a state a client can be rebuilt
		// from, and what makes "absent means unchanged" safe on every other
		// frame. So does a client that did not ask for `?blocks=delta`,
		// which is every client written before the gate existed. `next` is
		// what this client will hold ONCE the frame is enqueued; it is
		// committed in sent(), never before, so a dropped frame re-offers
		// every block it was carrying.
		next := held[i]
		gate := c.blocks && !full
		if !gate || next.scan != s.scanRev {
			f.Scan, next.scan = &stats, s.scanRev
		}
		if !gate || next.drivers != s.driversRev {
			f.Drivers, next.drivers = ds, s.driversRev
		}
		if s.alarms != nil && (!gate || !next.alarmsSent || next.alarms != alarmRev) {
			f.Alarms = summary()
			next.alarms, next.alarmsSent = alarmRev, true
		}

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
		k := frameKey{
			delta: deltaFor[i], full: f.Full, from: from[i], seq: f.Seq, pats: patsKey,
			// Two clients holding different versions of a non-tag block are
			// owed different bytes, so the memo has to know: a fleet in
			// step shares one encoding, and a tablet that just reconnected
			// gets its own rather than someone else's.
			scan: f.Scan != nil, drivers: f.Drivers != nil, alarms: f.Alarms != nil,
		}
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
			c.sent(gen, keyGen, full || f.Full, now, next)
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
	delta   bool
	full    bool
	from    uint64
	seq     uint64
	pats    string
	scan    bool
	drivers bool
	alarms  bool
}

// hashDrivers reduces a driver-status list to one number over everything an
// operator would call a change — and deliberately not over the things that
// move on their own.
//
// This is the whole of "send the driver block only when it changed", and
// the exclusions are the load-bearing part. A Sparkplug host fronting 55
// edge nodes renders ~13 kB of status; if a message counter or a
// pre-rendered "born 3m" put that on the wire, the gate would open on every
// single frame and buy nothing. So: DriverStatus.AsOfMs (the server's own
// stamp), any metric marked Volatile, any metric's AtMs, the Text of a
// metric that HAS an AtMs (that text is a rendering of the age), and the
// Extra keys the adapter named in VolatileExtra are all excluded. Everything
// else — state, message, device roster, error, the rest of Extra — is in.
//
// Excluded does not mean unsent: the current value of every counter rides
// the block whenever the block goes out, and a full frame goes out on every
// resync. It means a counter cannot, by itself, cost 13 kB four times a
// second.
func hashDrivers(ds []DriverStatus) uint64 {
	h := fnv.New64a()
	var buf [8]byte
	u64 := func(v uint64) {
		binary.LittleEndian.PutUint64(buf[:], v)
		h.Write(buf[:])
	}
	// Length-prefixed, so no concatenation of two fields can be mistaken
	// for a different split of the same bytes.
	str := func(s string) { u64(uint64(len(s))); io.WriteString(h, s) }

	u64(uint64(len(ds)))
	for i := range ds {
		d := &ds[i]
		str(d.Kind)
		str(d.Name)
		str(d.Detail)
		str(d.State)
		str(d.Message)
		str(d.LastError)
		u64(uint64(d.SinceMs))
		u64(uint64(len(d.Metrics)))
		for j := range d.Metrics {
			m := &d.Metrics[j]
			str(m.Label)
			str(m.Unit)
			if m.Volatile {
				continue
			}
			u64(math.Float64bits(m.Value))
			if m.AtMs == 0 {
				str(m.Text)
			}
		}
		u64(uint64(len(d.Devices)))
		for j := range d.Devices {
			dev := &d.Devices[j]
			str(dev.ID)
			str(dev.Detail)
			if dev.Online {
				u64(1)
			} else {
				u64(0)
			}
		}
		hashExtra(h, d.Extra, d.VolatileExtra)
	}
	return h.Sum64()
}

// hashExtra folds a driver's protocol-specific Extra into h, minus what it
// declared volatile. encoding/json sorts map keys, which is what makes the
// rendering stable across two builds of the same map.
//
// A VolatileExtra entry is either a top-level key ("nodes") or a PATH into
// the structure ("nodes.*.lastMsgMs"), because the churn that matters is
// usually nested. That is not a hypothesis: the first live deploy of the
// block gate put a 55-site Sparkplug host's status on every frame anyway,
// because each element of Extra["nodes"] carried a last-message stamp and a
// sequence number that stepped on every message — two fields, one level
// down, where excluding the whole "nodes" key would have thrown away the
// roster (online, stale, tag counts) that the gate exists to notice.
//
// In a path, "*" matches every key of a map or every element of a list, and
// a list is also transparent to a plain segment, so "nodes.lastMsgMs" and
// "nodes.*.lastMsgMs" both reach into a list of node objects. A leaf "*"
// means every key at that level is volatile.
func hashExtra(h hash.Hash64, extra map[string]any, volatile []string) {
	if len(extra) == 0 {
		h.Write([]byte{0})
		return
	}
	m := extra
	var paths [][]string
	if len(volatile) > 0 {
		m = make(map[string]any, len(extra))
		for k, v := range extra {
			skip := false
			for _, vol := range volatile {
				if k == vol {
					skip = true
					break
				}
			}
			if !skip {
				m[k] = v
			}
		}
		for _, vol := range volatile {
			if strings.Contains(vol, ".") {
				paths = append(paths, strings.Split(vol, "."))
			}
		}
	}
	b, err := json.Marshal(m)
	if err == nil && len(paths) > 0 {
		// Scrubbing happens on the rendered JSON rather than on the Go
		// values because Extra holds whatever the adapter put there —
		// structs, slices of structs, maps — and JSON is the one shape all
		// of them agree on. It costs a round trip per tick, paid only by a
		// driver that declared a nested path, and only while a client is
		// actually gating on the block.
		if scrubbed, ok := scrubPaths(b, paths); ok {
			b = scrubbed
		}
		// A scrub that fails hashes the unscrubbed bytes: the block then
		// goes out more often than it needs to, which is the harmless
		// direction.
	}
	if err != nil {
		// A value JSON cannot render (a NaN, a channel) is a bug in the
		// adapter, not a reason to stall the stream. Fall back to the key
		// set: the block then changes only when a key appears or goes,
		// which is the conservative direction — fewer bytes, never a lost
		// state change, since state lives in the typed fields above.
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			io.WriteString(h, k)
		}
		return
	}
	h.Write(b)
}

// scrubPaths re-renders JSON with the values at the given paths removed.
// Reports false if the round trip fails, which leaves the caller hashing
// what it already had.
func scrubPaths(b []byte, paths [][]string) ([]byte, bool) {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, false
	}
	for _, p := range paths {
		deleteAt(v, p)
	}
	out, err := json.Marshal(v)
	if err != nil {
		return nil, false
	}
	return out, true
}

// deleteAt removes the value at one path from a decoded JSON tree, treating
// a list as transparent: a roster's INDEX is not a stable address (nodes
// come and go), so a segment addressing a list applies to every element.
func deleteAt(v any, path []string) {
	if len(path) == 0 {
		return
	}
	seg := path[0]
	switch t := v.(type) {
	case map[string]any:
		if len(path) == 1 {
			if seg == "*" {
				for k := range t {
					delete(t, k)
				}
			} else {
				delete(t, seg)
			}
			return
		}
		if seg == "*" {
			for _, child := range t {
				deleteAt(child, path[1:])
			}
			return
		}
		if child, ok := t[seg]; ok {
			deleteAt(child, path[1:])
		}
	case []any:
		rest := path
		if seg == "*" {
			rest = path[1:]
			if len(rest) == 0 {
				return // "a.*" on a list: the list itself was the target
			}
		}
		for _, child := range t {
			deleteAt(child, rest)
		}
	}
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
//	?blocks=delta       gate the NON-tag blocks (scan diagnostics, driver
//	                    status, alarm counts) the same way — send each only
//	                    when it changed. Requires delta=1; ignored without
//	                    it. Opt-in for the same reason deltas are: a client
//	                    that does not merge them would show a blank driver
//	                    panel between changes, which looks like a plant
//	                    going quiet rather than like a protocol it has not
//	                    implemented. `?blocks=full` (the default) sends
//	                    every block on every frame, as this endpoint always
//	                    has.
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
	// The non-tag block gate rides on deltas — a client receiving whole
	// frames has nothing to merge a partial block into.
	blocks := delta && strings.EqualFold(strings.TrimSpace(q.Get("blocks")), "delta")

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Tell buffering reverse proxies (nginx, some ingress controllers) not to
	// hold the response — an SSE stream never "finishes", so a proxy that
	// waits for EOF starves the client. Doesn't affect a direct connection;
	// a client-side inspecting proxy/extension can still buffer regardless.
	w.Header().Set("X-Accel-Buffering", "no")

	c := &client{ch: make(chan []byte, 8), delta: delta, blocks: blocks, pats: pats}

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
