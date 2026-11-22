package alarm

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Live chattering and flood detection (#1249 slice 1).
//
// The rules are the ISA-18.2 ones the after-the-fact reports apply
// (alarm/rationalize), applied as activations arrive:
//
//   - an alarm CHATTERS when count of its activations fall within window
//     (inclusive: last minus first <= window). Windows that overlap are
//     one episode, as rationalize.Chattering counts them, so an episode
//     ends window after its last qualifying activation;
//   - the plant is in a FLOOD when more than threshold activations of all
//     alarms fall within window, and stays in one until the count has
//     been at or under the threshold for one full window (hysteresis, so
//     a flood does not flicker at its edge).
//
// An activation counts when the transition table makes it (an `active`
// row: a flap from unack-rtn counts, as in rationalize.Activations), but
// not when it is the re-assertion of a condition that stood: its
// condition has read true at every reading since the alarm was
// re-baselined (the engine's start, a takeover's reset, a return from
// suppression), or it falls inside the start grace (Options.CarryGrace),
// where a standalone controller's first scans may read a driver's
// defaults. A restart, a takeover or a site coming back is not a flood.
//
// Both end by time, on the scan, whatever the alarm's state: a shelved
// or suppressed alarm never steps. Nothing here is shelved, suppressed or
// paged: detection only marks and journals.

// ISA-18.2 defaults for live detection.
const (
	DefaultChatterWindow  = 60 * time.Second
	DefaultChatterCount   = 3
	DefaultFloodWindow    = 10 * time.Minute
	DefaultFloodThreshold = 10

	// FloodRing is how many activations the flood detector holds: the
	// count in a window, and the peak, read as at least this many past it
	// (Flood.Saturated). The threshold must be below it.
	FloodRing = 1024
)

// Journal kinds of detection rows. Like KindNotify they are not state
// changes: they never reach Notifiers or OnEvent, and the HTTP journal
// view leaves them out unless a client names them.
const (
	KindChatter    = "chatter"
	KindChatterEnd = "chatter-end"
	KindFlood      = "flood"
	KindFloodEnd   = "flood-end"
)

// FloodJournalID is the id flood rows are journalled under.
const FloodJournalID = "__nautilus.flood"

// ChatterConfig is `alarms.chatter:`. Zero fields take the defaults.
type ChatterConfig struct {
	Window Duration `yaml:"window" json:"window,omitempty"`
	Count  int      `yaml:"count" json:"count,omitempty"`
}

// FloodConfig is `alarms.flood:`. Zero fields take the defaults. A flood
// is MORE than Threshold activations within Window.
type FloodConfig struct {
	Window    Duration `yaml:"window" json:"window,omitempty"`
	Threshold int      `yaml:"threshold" json:"threshold,omitempty"`
}

// Resolved is c with its defaults filled in.
func (c ChatterConfig) Resolved() ChatterConfig {
	if c.Window == 0 {
		c.Window = Duration(DefaultChatterWindow)
	}
	if c.Count == 0 {
		c.Count = DefaultChatterCount
	}
	return c
}

// Resolved is c with its defaults filled in.
func (c FloodConfig) Resolved() FloodConfig {
	if c.Window == 0 {
		c.Window = Duration(DefaultFloodWindow)
	}
	if c.Threshold == 0 {
		c.Threshold = DefaultFloodThreshold
	}
	return c
}

// Validate refuses a chatter setting the detector cannot honour.
func (c ChatterConfig) Validate() error {
	c = c.Resolved()
	switch {
	case time.Duration(c.Window) < time.Second:
		return fmt.Errorf("alarms.chatter.window: %s is under a second", time.Duration(c.Window))
	case c.Count < 2:
		return fmt.Errorf("alarms.chatter.count: %d: an alarm chatters when it activates at least twice (want 2 or more)", c.Count)
	case c.Count > 100:
		return fmt.Errorf("alarms.chatter.count: %d is over 100", c.Count)
	}
	return nil
}

// Validate refuses a flood setting the detector cannot honour.
func (c FloodConfig) Validate() error {
	c = c.Resolved()
	switch {
	case time.Duration(c.Window) < time.Second:
		return fmt.Errorf("alarms.flood.window: %s is under a second", time.Duration(c.Window))
	case c.Threshold < 1:
		return fmt.Errorf("alarms.flood.threshold: %d: want 1 or more", c.Threshold)
	case c.Threshold >= FloodRing:
		return fmt.Errorf("alarms.flood.threshold: %d: want under %d", c.Threshold, FloodRing)
	}
	return nil
}

// chatterState is one alarm's chatter detector: a ring of its last count
// activation times, and the episode it is in. Kept outside instance, so
// ResetOperatorState's zeroing of an instance does not drop the ring's
// backing array (Engine.det).
type chatterState struct {
	ring []int64 // ms, len = count; ring[n%len] is the oldest once full
	n    int     // activations recorded

	on       bool
	since    int64 // the episode's first qualifying window began (ms)
	last     int64 // its last qualifying activation: it ends window after
	acts     int   // activations in the episode
	episodes int   // episodes since the engine started (or was reset)
}

// chatterEpisode is an episode's numbers, for its rows.
type chatterEpisode struct {
	since, last int64
	acts        int
}

func (c *chatterState) episode() chatterEpisode {
	return chatterEpisode{since: c.since, last: c.last, acts: c.acts}
}

// add records an activation at ms, and reports whether an episode began
// with it. One in progress continues with any qualifying window that
// starts at or before its last qualifying activation, as the report
// merges overlapping windows. (A qualifying window starting after it
// cannot arrive while it lasts: the (count-1)th activation after it forms
// a window that starts there. So an episode in progress never splits; it
// ends by time, Engine.detectTick, before the next can begin.)
func (c *chatterState) add(ms, window int64) (started bool) {
	k := len(c.ring)
	c.ring[c.n%k] = ms
	c.n++
	qualifies := false
	if c.n >= k && !mutAlarm("chatter_never") {
		qualifies = ms-c.ring[c.n%k] <= window // the oldest of the last k
	}
	switch {
	case c.on:
		c.acts++
		if qualifies && !mutAlarm("chatter_no_merge") {
			c.last = ms
		}
		return false
	case qualifies:
		c.on, c.since, c.last, c.acts = true, c.ring[c.n%k], ms, k
		c.episodes++
		return true
	}
	return false
}

// endAt is the instant the episode ends unless an activation continues
// it: no window starting at or before its last qualifying activation can
// qualify after it.
func (c *chatterState) endAt(window int64) int64 { return c.last + window }

func (c *chatterState) reset() {
	ring := c.ring
	clear(ring)
	*c = chatterState{ring: ring}
}

// detect is one alarm's detection state (Engine.det, parallel to inst).
type detect struct {
	chat chatterState
	// baselined: the alarm has been read since it was last re-baselined
	// (the engine's start, ResetOperatorState, a suppression); standing:
	// its condition has read true at every reading since (inside the
	// start grace, any true reading makes it standing). An activation of
	// a standing condition is no new activation.
	baselined, standing bool
	// floodActs counts the alarm's activations in the current flood, for
	// the flood's equipment groups.
	floodActs int
}

// FloodDetector is the live flood rule, shared by the engine and the
// report's sliding mode (rationalize.SlidingFloods), so both give the
// same answer from the same activations. Times are epoch ms; Add and
// Tick must be called in time order. Not safe for concurrent use.
type FloodDetector struct {
	window    int64
	threshold int

	ring []floodAct // FloodRing slots, oldest at head
	head int
	n    int // activations in the window (at most len(ring))

	on        bool
	since     int64 // when the count first went over the threshold
	overUntil int64 // the last instant the count was over the threshold
	total     int   // activations since the flood began
	peak      int   // the most in any window during it
	saturated bool  // the ring overflowed during it
	first     []floodAct
}

type floodAct struct {
	ms  int64
	idx int32 // the alarm (Engine.inst index; the caller's own key)
}

// NewFloodDetector is a detector for c (defaults filled in).
func NewFloodDetector(c FloodConfig) *FloodDetector {
	c = c.Resolved()
	return &FloodDetector{window: time.Duration(c.Window).Milliseconds(), threshold: c.Threshold,
		ring: make([]floodAct, FloodRing), first: make([]floodAct, 0, floodFirstOut)}
}

// floodFirstOut is how many of a flood's first alarms it names.
const floodFirstOut = 3

func (d *FloodDetector) at(i int) floodAct { return d.ring[(d.head+i)%len(d.ring)] }

// evict drops activations that have left the window ending at now.
func (d *FloodDetector) evict(now int64) {
	for d.n > 0 && now-d.ring[d.head].ms > d.window {
		d.head = (d.head + 1) % len(d.ring)
		d.n--
	}
}

// Tick ends the flood if it has been at or under the threshold for one
// full window by now, and reports the instant it ended.
func (d *FloodDetector) Tick(now int64) (ended bool, endMs int64) {
	d.evict(now)
	if !d.on {
		return false, 0
	}
	end := d.overUntil + d.window
	if mutAlarm("flood_no_hysteresis") {
		end = d.overUntil
	}
	if now <= end {
		return false, 0
	}
	d.on = false
	return true, end
}

// Add records one activation of alarm idx at ms, and reports whether a
// flood began with it. Call Tick(ms) first.
func (d *FloodDetector) Add(ms int64, idx int32) (started bool) {
	d.evict(ms)
	if d.n == len(d.ring) {
		d.head = (d.head + 1) % len(d.ring) // the oldest still in the window: count saturates
		d.n--
		if d.on {
			d.saturated = true
		}
	}
	d.ring[(d.head+d.n)%len(d.ring)] = floodAct{ms: ms, idx: idx}
	d.n++
	over := d.n > d.threshold
	if over {
		// The count stays over the threshold until the (threshold+1)th
		// newest activation leaves the window.
		d.overUntil = d.at(d.n-1-d.threshold).ms + d.window
	}
	if d.on {
		d.total++
		d.peak = max(d.peak, d.n)
		return false
	}
	if !over {
		return false
	}
	d.on, d.since, d.total, d.peak, d.saturated = true, ms, d.n, d.n, false
	// The first alarms, each once, in time order: a chattering one does
	// not fill the list by itself.
	d.first = d.first[:0]
next:
	for i := 0; i < d.n && len(d.first) < floodFirstOut; i++ {
		a := d.at(i)
		for _, f := range d.first {
			if f.idx == a.idx && !mutAlarm("first_out_repeats") {
				continue next
			}
		}
		d.first = append(d.first, a)
	}
	return true
}

// On reports whether a flood is in progress.
func (d *FloodDetector) On() bool { return d.on }

// Count is how many activations are in the window ending at the last Add
// or Tick.
func (d *FloodDetector) Count() int { return d.n }

// EachInWindow calls fn for each activation in the window, oldest first.
func (d *FloodDetector) EachInWindow(fn func(ms int64, idx int32)) {
	for i := range d.n {
		a := d.at(i)
		fn(a.ms, a.idx)
	}
}

func (d *FloodDetector) reset() {
	ring, first := d.ring, d.first[:0]
	*d = FloodDetector{window: d.window, threshold: d.threshold, ring: ring, first: first}
}

// FloodEpisode is one flood as the sliding rule finds it.
type FloodEpisode struct {
	Start, End int64 // ms; End 0 while it is still on
	Total      int   // activations from its start to its end
	Peak       int   // the most in any window
	Saturated  bool  // Peak read as at least FloodRing
	FirstOut   []int32
}

// Episode is the flood in progress (or the last one), as a FloodEpisode.
func (d *FloodDetector) Episode() FloodEpisode {
	ep := FloodEpisode{Start: d.since, Total: d.total, Peak: d.peak, Saturated: d.saturated}
	for _, a := range d.first {
		ep.FirstOut = append(ep.FirstOut, a.idx)
	}
	return ep
}

// Chatter is an alarm's chattering episode, on its record while it lasts.
type Chatter struct {
	SinceMs     int64 `json:"sinceMs"`
	LastMs      int64 `json:"lastMs"`
	Activations int   `json:"activations"` // in this episode
	Episodes    int   `json:"episodes"`    // since the engine started
	WindowMs    int64 `json:"windowMs"`
	Count       int   `json:"count"` // the configured count
}

// FloodBrief is a flood in progress, as the summary carries it: numbers
// only, so a frame costs nothing.
type FloodBrief struct {
	ID        string `json:"id"`
	SinceMs   int64  `json:"sinceMs"`
	Count     int    `json:"count"` // activations in the current window
	Total     int    `json:"total"` // since the flood began
	Peak      int    `json:"peak"`
	WindowMs  int64  `json:"windowMs"`
	Threshold int    `json:"threshold"`
	Saturated bool   `json:"saturated,omitempty"`
}

// Flood is a flood in progress with what an operator reads first: the
// first alarms of it, in time order (the best free guess at the cause),
// and the equipment the activations came from.
type Flood struct {
	FloodBrief
	FirstOut []Brief      `json:"firstOut"`
	Groups   []FloodGroup `json:"groups"`
}

// FloodGroup is one piece of equipment's share of a flood.
type FloodGroup struct {
	Equipment string `json:"equipment"`
	Area      string `json:"area,omitempty"`
	Count     int    `json:"count"`
}

// floodGroupsTop is how many equipment groups a flood names.
const floodGroupsTop = 5

// FloodEventVersion is FloodEvent's version.
const FloodEventVersion = 1

// FloodEvent is a flood's start or end, for a consumer outside the
// engine (an incident card, say): plain facts, versioned. The journal's
// flood rows are the record; this is a hook (OnFlood).
type FloodEvent struct {
	Version     int               `json:"version"`
	ID          string            `json:"id"`
	Kind        string            `json:"kind"` // "start" or "end"
	StartMs     int64             `json:"startMs"`
	EndMs       int64             `json:"endMs,omitempty"`
	Count       int               `json:"count"`
	Total       int               `json:"total"`
	Peak        int               `json:"peak"`
	WindowMs    int64             `json:"windowMs"`
	Threshold   int               `json:"threshold"`
	Saturated   bool              `json:"saturated,omitempty"`
	FirstOut    []Brief           `json:"firstOut"`
	Groups      []FloodGroup      `json:"groups"`
	Activations []FloodActivation `json:"activations"`
}

// FloodActivation is one activation in a FloodEvent's window.
type FloodActivation struct {
	ID string `json:"id"`
	Ms int64  `json:"ms"`
}

// OnFlood registers fn to hear every flood's start and end. fn runs under
// the engine's lock, on the scan: it must neither call the engine nor
// block (hand the event to a goroutine of your own). Call it before the
// first Evaluate.
func (e *Engine) OnFlood(fn func(FloodEvent)) {
	e.mu.Lock()
	e.onFlood = fn
	e.mu.Unlock()
}

// inGrace reports whether now is inside the start grace.
func (e *Engine) inGrace(now time.Time) bool {
	return !e.baseAt.IsZero() && now.Before(e.baseAt.Add(e.carryGrace)) && !mutAlarm("grace_counts")
}

// baseline tracks whether in's condition has stood since it was last
// re-baselined, on a reading raw. Called on every reading, before the
// transition table.
func (e *Engine) baseline(d *detect, raw bool, now time.Time) {
	switch {
	case !d.baselined:
		d.baselined, d.standing = true, raw
	case e.inGrace(now):
		d.standing = d.standing || raw
	case !raw:
		d.standing = false
	}
}

// counted reports whether an activation of d at now is a new one.
func (e *Engine) counted(d *detect, now time.Time) bool {
	if mutAlarm("restart_counts") {
		return true
	}
	return !d.standing && !e.inGrace(now)
}

// activated feeds one counted activation of inst[i] to both detectors.
// Under e.mu, on the scan.
func (e *Engine) activated(i int, now time.Time) {
	in, d := &e.inst[i], &e.det[i]
	ms := now.UnixMilli()
	if d.chat.add(ms, e.chatWindow) {
		e.chattering++
		e.journalChatter(in, d.chat.episode(), KindChatter, ms)
	}
	if e.flood.Add(ms, int32(i)) {
		e.floodSeq++
		e.floodID = floodID(now, e.floodSeq)
		for j := range e.det {
			e.det[j].floodActs = 0
		}
		for k := 0; k < e.flood.n; k++ {
			e.det[e.flood.at(k).idx].floodActs++
		}
		e.journalFlood(ms, true)
	} else if e.flood.on {
		d.floodActs++
	}
}

// floodID names a flood: its start, and a sequence for two in one
// millisecond. Readable in a journal row, unique for the engine's life.
func floodID(now time.Time, seq uint64) string {
	return "flood-" + strconv.FormatInt(now.UnixMilli(), 36) + "-" + strconv.FormatUint(seq, 36)
}

// detectTick ends, on the scan, the episodes whose time has passed. It
// allocates nothing unless one ends. Under e.mu.
func (e *Engine) detectTick(now time.Time) {
	ms := now.UnixMilli()
	if ended, at := e.flood.Tick(ms); ended {
		e.journalFlood(at, false)
	}
	if e.chattering == 0 {
		return
	}
	for i := range e.det {
		d := &e.det[i]
		if d.chat.on && ms > d.chat.endAt(e.chatWindow) {
			d.chat.on = false
			e.chattering--
			e.journalChatter(&e.inst[i], d.chat.episode(), KindChatterEnd, d.chat.endAt(e.chatWindow))
		}
	}
}

// journalChatter writes a chatter row of kind for episode ep.
func (e *Engine) journalChatter(in *instance, ep chatterEpisode, kind string, atMs int64) {
	e.appendSystem(Event{TS: atMs, ID: in.def.ID, Name: in.displayName(), Kind: kind, By: "system",
		Priority: in.def.Priority, Site: in.def.Site, State: in.state.String(), Occurrence: in.occ,
		Count: ep.acts, WindowMs: e.chatWindow, SinceMs: ep.since})
}

// journalFlood writes a flood's start (or end) row at atMs and tells
// OnFlood.
func (e *Engine) journalFlood(atMs int64, start bool) {
	f := e.floodState()
	ev := Event{TS: atMs, ID: FloodJournalID, Kind: KindFloodEnd, By: "system", Episode: e.floodID,
		Count: f.Total, Peak: f.Peak, WindowMs: f.WindowMs, SinceMs: f.SinceMs}
	ev.Name = fmt.Sprintf("alarm flood ended: %d activations, peak %d in %s (limit %d)",
		f.Total, f.Peak, time.Duration(f.WindowMs)*time.Millisecond, f.Threshold)
	if start {
		ev.Kind, ev.Count = KindFlood, f.Count
		ev.Name = fmt.Sprintf("alarm flood: %d in %s (limit %d)", f.Count, time.Duration(f.WindowMs)*time.Millisecond, f.Threshold)
		ids := make([]string, len(f.FirstOut))
		for i, b := range f.FirstOut {
			ids[i] = b.ID
		}
		ev.Reason = "first out: " + strings.Join(ids, ", ")
	}
	e.appendSystem(ev)
	if e.onFlood == nil {
		return
	}
	fe := FloodEvent{Version: FloodEventVersion, ID: e.floodID, Kind: "start", StartMs: f.SinceMs,
		Count: f.Count, Total: f.Total, Peak: f.Peak, WindowMs: f.WindowMs, Threshold: f.Threshold,
		Saturated: f.Saturated, FirstOut: f.FirstOut, Groups: f.Groups}
	if !start {
		fe.Kind, fe.EndMs = "end", atMs
	}
	fe.Activations = make([]FloodActivation, 0, e.flood.n)
	e.flood.EachInWindow(func(ms int64, idx int32) {
		fe.Activations = append(fe.Activations, FloodActivation{ID: e.inst[idx].def.ID, Ms: ms})
	})
	e.onFlood(fe)
}

// appendSystem journals a detection row. Not a state change: no
// Notifiers, no OnEvent, no wait bookkeeping (emit's); Rev bumps, so an
// HMI refetches and shows the badge or the banner.
func (e *Engine) appendSystem(ev Event) {
	e.rev++
	if err := e.jrn.Append(ev); err != nil {
		e.log.Warn("alarm journal append failed", "id", ev.ID, "kind", ev.Kind, "err", err)
	}
}

// floodBrief is the flood in progress (or the last one), numbers only:
// it allocates nothing (the summary calls it every frame). Under e.mu.
func (e *Engine) floodBrief() FloodBrief {
	d := e.flood
	return FloodBrief{ID: e.floodID, SinceMs: d.since, Count: d.n, Total: d.total,
		Peak: d.peak, WindowMs: d.window, Threshold: d.threshold, Saturated: d.saturated}
}

// floodState is floodBrief with the flood's first out and groups. Under
// e.mu.
func (e *Engine) floodState() Flood {
	f := Flood{FloodBrief: e.floodBrief(), FirstOut: []Brief{}, Groups: []FloodGroup{}}
	for _, a := range e.flood.first {
		in := &e.inst[a.idx]
		f.FirstOut = append(f.FirstOut, Brief{ID: in.def.ID, Name: in.displayName(), Priority: in.def.Priority, Ms: a.ms})
	}
	type agg struct {
		g     FloodGroup
		mixed bool
	}
	by := map[string]*agg{}
	var keys []string
	for i := range e.det {
		n := e.det[i].floodActs
		if n == 0 {
			continue
		}
		in := &e.inst[i]
		a, ok := by[in.eqKey]
		if !ok {
			a = &agg{g: FloodGroup{Equipment: in.def.Equipment, Area: in.def.Area}}
			by[in.eqKey] = a
			keys = append(keys, in.eqKey)
		} else if a.g.Area != in.def.Area {
			a.mixed = true
		}
		a.g.Count += n
	}
	for _, k := range keys {
		g := by[k].g
		if by[k].mixed {
			g.Area = ""
		}
		f.Groups = append(f.Groups, g)
	}
	sort.Slice(f.Groups, func(i, j int) bool {
		if f.Groups[i].Count != f.Groups[j].Count {
			return f.Groups[i].Count > f.Groups[j].Count
		}
		return f.Groups[i].Equipment < f.Groups[j].Equipment
	})
	if len(f.Groups) > floodGroupsTop {
		f.Groups = f.Groups[:floodGroupsTop]
	}
	return f
}

// Flood returns the flood in progress, or nil when there is none.
func (e *Engine) Flood() *Flood {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.flood.on {
		return nil
	}
	e.flood.evict(e.now().UnixMilli())
	f := e.floodState()
	return &f
}

// chatterOf is in's chatter episode for its record, nil when it is not
// chattering.
func (e *Engine) chatterOf(i int) *Chatter {
	c := &e.det[i].chat
	if !c.on {
		return nil
	}
	return &Chatter{SinceMs: c.since, LastMs: c.last, Activations: c.acts, Episodes: c.episodes,
		WindowMs: e.chatWindow, Count: len(c.ring)}
}

// resetDetect forgets every episode and ring (a takeover's reset: what a
// frozen engine held is an earlier leadership's). Nothing is journalled.
func (e *Engine) resetDetect() {
	for i := range e.det {
		e.det[i].chat.reset()
		e.det[i].baselined, e.det[i].standing, e.det[i].floodActs = false, false, 0
	}
	e.chattering = 0
	e.flood.reset()
	e.baseAt = time.Time{}
}
