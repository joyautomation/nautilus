package escalate

import (
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/alarm"
)

// Paging under chattering and floods (#1249 slice 3).
//
// Chattering. In the modes that page per bounce (stop: ack-or-rtn,
// ack-required: false, an acknowledgement between bounces, a latched
// alarm acknowledged while active), every activation restarts the
// escalation clock and pages again. While an alarm chatters, a clock that
// a bounce started after the episode began is held, and the alarm
// escalates on one clock pinned to the episode's start instead: each
// level goes once per episode, as a chatter notice. Levels above the
// first still climb on that clock. A latched alarm that was never
// acknowledged waits from its first activation, which is the episode's
// start or before it, so it is untouched: it already pages once per
// level. The pinned clock lives as long as the episode, not the wait, so
// a bounce through normal does not lose what was sent.
//
// Floods. While the engine reports a flood, an alarm that is not
// individual (critical priority and class safety always are, plus what
// alarms.flood.individual adds) keeps its own escalation clock, but a
// step that falls due joins a batch per (policy, step) instead of paging:
// a batch goes as one grouped page per person per channel, the first at
// once, later ones at most every RegroupEvery for alarms newly due at that
// step. A step is covered for an alarm once a grouped page listing it was
// delivered; a grouped send that fails for good falls back to individual
// pages to that person by that channel. A grouped page carries no ack
// link, reply code or keypress (they bind one occurrence). When the flood
// ends, steps not yet covered page individually as usual.

// RegroupEvery is the least time between two grouped pages for the same
// policy step in one flood: alarms that fall due at a step after its
// batch went wait at most this long for the next one.
const RegroupEvery = 5 * time.Minute

// Why a page was held or followed a chatter (the notify row's reason).
const (
	afterChatter = "after chattering"
)

// Group is a grouped page's content: the flood, and the alarms the page
// is about (covered alarms whose step fell due, in time order).
type Group struct {
	Flood    alarm.FloodBrief
	FirstOut []alarm.Brief
	Groups   []alarm.FloodGroup
	Alarms   []GroupAlarm
}

// GroupAlarm is one alarm a grouped page lists.
type GroupAlarm struct {
	ID         string
	Name       string
	Priority   alarm.Priority
	Area       string
	State      alarm.State
	ActiveMs   int64
	Chattering bool
}

// Flooder is what grouping needs of an engine: the flood in progress, or
// nil (alarm.Engine has it).
type Flooder interface {
	Flood() *alarm.Flood
}

// FloodPaging configures grouping. Critical priority and class safety
// always page individually, whatever Priorities and Classes say.
type FloodPaging struct {
	Group      bool
	Priorities []alarm.Priority
	Classes    []string
}

// sendKind tells a send's key apart: an alarm's own clock, its chatter
// clock, or a grouped page.
type sendKind uint8

const (
	kindAlarm sendKind = iota
	kindChatter
	kindGroup
)

// escClock is one escalation clock: an alarm's own (an episode's) or its
// chattering episode's.
type escClock struct {
	since  time.Time
	policy *Policy
	// skipBelow marks the steps that fell due before this controller saw
	// the escalation, sent by nobody here: a controller that joins late
	// sends only the latest due step, never a burst of stale levels.
	skipBelow int
	late      bool
	lateWhy   string
	// people holds each step's recipients, resolved once, when the step
	// first fell due: a roster's handover after that pages the new person
	// at the next level, never with the levels already sent.
	people map[int][]string
	// steps is each step's flood grouping, by step index (nil: never
	// grouped).
	steps map[int]*stepGroup
	// skip marks due steps not sent: a pinned clock that starts (or moves
	// to another policy) late sends each level once, its latest due step.
	skip map[int]bool
}

func (c *escClock) restart(since time.Time) {
	c.since = since
	c.people = map[int][]string{}
	c.steps = nil
	c.skip = nil
}

// oncePerLevel marks, of the steps due after elapsed, every one but the
// latest of its level: a clock that begins with several of a level's
// repeats already due sends that level once, not as a burst.
func oncePerLevel(steps []Step, elapsed time.Duration) map[int]bool {
	if mutEscalate("chatter_repeats_burst") {
		return nil
	}
	latest := map[int]int{}
	for i, s := range steps {
		if s.At > elapsed {
			break
		}
		latest[s.Level] = i
	}
	var skip map[int]bool
	for i, s := range steps {
		if s.At > elapsed {
			break
		}
		if latest[s.Level] != i {
			if skip == nil {
				skip = map[int]bool{}
			}
			skip[i] = true
		}
	}
	return skip
}

// stepGroup is one step of one clock under flood grouping.
type stepGroup struct {
	pending  int  // grouped sends in flight that list it
	covered  bool // a grouped page listing it was delivered
	fallback []toVia
}

type toVia struct{ to, via string }

// chatClock is a chattering alarm's pinned clock.
type chatClock struct {
	escClock
	heldAt  int64 // the bounce clock last held (its since, ms)
	held    int   // bounce clocks held
	heldRow bool
}

// batchKey names a policy step's batches in a flood.
type batchKey struct {
	policy string
	step   int
}

// clockRef is an alarm's clock as a batch listed it.
type clockRef struct {
	kind   sendKind
	id     string
	since  int64
	policy *Policy // the clock's policy when listed: a pinned clock may move
	occ    string
	clk    *escClock
}

// batch is one grouped page in flight: its sends to every person by
// every channel, and the clocks it lists.
type batch struct {
	floodID     string
	step        int
	outstanding int
	listed      []clockRef
	key         batchKey
	at          time.Time // when it was queued: its policy step's floor
	told        bool      // some send of it was delivered or failed for good
}

// cand is an alarm step waiting for its policy step's next batch.
type cand struct {
	r      alarm.Record
	kind   sendKind
	clk    *escClock
	reason string
}

// individual reports whether r pages on its own in a flood.
// Classes compare ignoring case: "Safety" is the safety class, never a
// grouped one.
func (d *Dispatcher) individual(r alarm.Record) bool {
	safety := strings.EqualFold(r.Class, alarm.SafetyClass)
	if mutEscalate("safety_case_exact") {
		safety = r.Class == alarm.SafetyClass
	}
	if r.Priority == alarm.Critical || safety || slices.Contains(d.flood.Priorities, r.Priority) {
		return true
	}
	return slices.ContainsFunc(d.flood.Classes, func(c string) bool { return strings.EqualFold(c, r.Class) })
}

// chatterWants reports whether a chattering alarm's notice is still
// wanted: not shelved or suppressed, and still waiting on a person (any
// state for an alarm that needs no acknowledgement, whose bounces are its
// chatter).
func chatterWants(r alarm.Record) bool {
	switch r.State {
	case alarm.Shelved, alarm.Suppressed:
		return false
	}
	if r.Suppression != "" {
		return false
	}
	return !r.AckRequired || r.State == alarm.UnackActive || r.State == alarm.UnackRTN
}

// chatClockFor is r's pinned clock, started (or restarted, for a new
// episode) at r's episode start, under p: the policy of the wait the
// bounce began (the one its own clock would page under). A later bounce's
// wait under another policy (a when: window passed) moves the pinned
// clock to it: its levels are due from the episode start, once each. Under
// d.mu.
func (d *Dispatcher) chatClockFor(r alarm.Record, p *Policy, now time.Time, firstLook bool) *chatClock {
	since := time.UnixMilli(r.Chatter.SinceMs)
	cc := d.chats[r.ID]
	if cc != nil && cc.since.Equal(since) {
		if cc.policy != p && !mutEscalate("chatter_policy_at_start") {
			cc.policy, cc.people, cc.steps, cc.skipBelow, cc.late = p, map[int][]string{}, nil, 0, false
			cc.skip = oncePerLevel(p.steps, now.Sub(since))
		}
		return cc
	}
	if cc != nil {
		d.endChat(r.ID, cc, r.Occurrence)
	}
	cc = &chatClock{}
	cc.restart(since)
	cc.policy = p
	if mutEscalate("chatter_policy_at_start") {
		cc.policy = d.cfg.Select(r.Def, since)
	}
	// Every level due goes: the bounces before the episode was seen may
	// have paged nobody (a first level that waits longer than a bounce
	// lasts). Only a leadership's first look joins late, as everywhere.
	if firstLook || mutEscalate("chatter_joins_late") {
		cc.skipBelow, cc.late = joinLate(p.steps, now.Sub(since), d.every)
		cc.lateWhy = d.resendWhy()
	} else {
		cc.skip = oncePerLevel(p.steps, now.Sub(since))
	}
	d.chats[r.ID] = cc
	return cc
}

// hold counts a bounce clock the pinned one holds, and journals the first
// of an episode. Under d.mu.
func (d *Dispatcher) hold(r alarm.Record, cc *chatClock, bounce int64) {
	if cc.heldAt == bounce || mutEscalate("chatter_pages_per_bounce") {
		return
	}
	cc.heldAt = bounce
	cc.held++
	if !cc.heldRow {
		cc.heldRow = true
		d.eng.Notified(r.ID, alarm.Notice{Result: alarm.NotifyHeld, Occurrence: r.Occurrence,
			Reason: fmt.Sprintf("chattering since %s: a page per bounce is held, each level goes once as a chatter notice",
				cc.since.UTC().Format(time.RFC3339))})
	}
}

// endChat ends a chattering alarm's pinned clock: its finished sends are
// forgotten (a pending one stays until the sender resolves it), and an
// episode that held bounces says how many. Under d.mu.
func (d *Dispatcher) endChat(id string, cc *chatClock, occ string) {
	d.endSends(kindChatter, id, cc.since)
	delete(d.chats, id)
	if cc.held > 0 {
		d.eng.Notified(id, alarm.Notice{Result: alarm.NotifyHeld, Occurrence: occ,
			Reason: fmt.Sprintf("chattering ended: %d %s paged as one notice per level", cc.held, plural(cc.held, "bounce", "bounces"))})
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// sendBatches queues each policy step's grouped page whose candidates are
// waiting and whose last batch is at least RegroupEvery old. Under d.mu.
func (d *Dispatcher) sendBatches(f *alarm.Flood, cands map[batchKey][]cand, now time.Time, term uint64) {
	keys := make([]batchKey, 0, len(cands))
	for k := range cands {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].policy != keys[j].policy {
			return keys[i].policy < keys[j].policy
		}
		return keys[i].step < keys[j].step
	})
	for _, bk := range keys {
		list := cands[bk]
		if last, ok := d.lastBatch[bk]; ok && now.Sub(last) < RegroupEvery && !mutEscalate("regroup_unbounded") {
			continue
		}
		p := list[0].clk.policy
		s := p.steps[bk.step]
		lvl := p.Levels[s.Level]
		sort.SliceStable(list, func(i, j int) bool { return list[i].r.ActiveMs < list[j].r.ActiveMs })
		g := &Group{Flood: f.FloodBrief, FirstOut: f.FirstOut, Groups: f.Groups}
		listed := make([]clockRef, 0, len(list))
		reason := ""
		for _, c := range list {
			g.Alarms = append(g.Alarms, GroupAlarm{ID: c.r.ID, Name: c.r.Name, Priority: c.r.Priority, Area: c.r.Area,
				State: c.r.State, ActiveMs: c.r.ActiveMs, Chattering: c.r.Chatter != nil})
			listed = append(listed, clockRef{kind: c.kind, id: c.r.ID, since: c.clk.since.UnixMilli(), policy: c.clk.policy, occ: c.r.Occurrence, clk: c.clk})
			if c.reason != "" && (reason == "" || c.reason == ResendTakeover || c.reason == ResendRestart) {
				reason = c.reason
			}
		}
		people := d.recipients(lvl.Notify, now)
		if len(people) == 0 {
			for _, c := range listed {
				c.clk.stepGroup(bk.step).covered = true
				d.eng.Notified(c.id, alarm.Notice{Level: s.Level + 1, Via: strings.Join(lvl.Via, ","),
					Result: alarm.NotifyNobodyOnCall, Reason: "flood " + f.ID + ": nobody on call on " + strings.Join(lvl.Notify, ", "),
					Occurrence: c.occ})
			}
			continue // nobody to tell: no floor, the next ones get their rows at once
		}
		d.seq++
		b := &batch{floodID: f.ID, step: bk.step, listed: listed, key: bk, at: now}
		var dropped []toVia
		for _, to := range people {
			for _, via := range lvl.Via {
				k := sendKey{kind: kindGroup, id: p.ID, since: d.seq, step: bk.step, to: to, via: via}
				msg := Message{Policy: p.ID, Level: s.Level + 1, Repeat: s.Repeat, To: to, Reason: reason,
					Group: g, Link: d.link("/alarms?focus=1")}
				d.inflight.Add(1)
				select {
				case d.q[via] <- queued{key: k, term: term, gen: d.gen, msg: msg}:
					d.sends[k] = pending
					b.outstanding++
				default:
					d.inflight.Done()
					d.dropped.Add(1)
					dropped = append(dropped, toVia{to, via})
				}
			}
		}
		if b.outstanding == 0 {
			continue // nothing taken: the candidates are tried at the next look
		}
		d.batches[d.seq] = b
		for _, c := range listed {
			sg := c.clk.stepGroup(bk.step)
			sg.pending += b.outstanding
			// A send the queue could not take is not lost: those alarms
			// page that person by that channel on their own, retried at
			// every look until the queue takes it.
			if !mutEscalate("group_drop_lost") {
				sg.fallback = append(sg.fallback, dropped...)
			}
		}
		d.lastBatch[bk] = now
	}
}

func (c *escClock) stepGroup(i int) *stepGroup {
	if c.steps == nil {
		c.steps = map[int]*stepGroup{}
	}
	sg := c.steps[i]
	if sg == nil {
		sg = &stepGroup{}
		c.steps[i] = sg
	}
	return sg
}

// resolveBatch settles one grouped send of batch seq for the clocks it
// lists: delivered covers their step, failed sends them individually to
// that person by that channel, released (not sent) leaves them to the
// next batch. It returns the batch (nil if gone). Under d.mu.
func (d *Dispatcher) resolveBatch(seq int64, tv toVia, sent, failed bool) *batch {
	b := d.batches[seq]
	if b == nil {
		return nil
	}
	for _, ref := range b.listed {
		if ref.clk.since.UnixMilli() != ref.since || ref.clk.policy != ref.policy {
			continue // the clock restarted (a new wait) or moved policy since
		}
		sg := ref.clk.steps[b.step]
		if sg == nil {
			continue
		}
		sg.pending--
		switch {
		case sent, failed && mutEscalate("group_covers_on_failure"):
			sg.covered = true
		case failed:
			sg.fallback = append(sg.fallback, tv)
		}
	}
	b.told = b.told || sent || failed
	b.outstanding--
	if b.outstanding <= 0 {
		delete(d.batches, seq)
		// A batch released whole (its alarms moved on before it left, or
		// the gate closed) told nobody: it does not hold the next one back.
		if !b.told && d.lastBatch[b.key].Equal(b.at) && !mutEscalate("released_batch_floors") {
			delete(d.lastBatch, b.key)
		}
	}
	return b
}

// link is the HMI path under alarms.oncall.ack-url, "" without one.
func (d *Dispatcher) link(path string) string {
	if d.home == "" {
		return ""
	}
	return d.home + path
}

func (d *Dispatcher) alarmLink(id string) string {
	return d.link("/alarms?alarm=" + url.QueryEscape(id))
}
