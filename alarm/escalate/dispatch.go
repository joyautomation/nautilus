package escalate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/joyautomation/nautilus/alarm"
)

// Message is one notification: an alarm, a level, a recipient.
type Message struct {
	Alarm  alarm.Record
	Policy string
	Level  int    // 1 is the first level
	Repeat int    // 0 is the level's own send, 1… its repeats
	To     string // a principal; the Channel resolves the address
	Reason string // optional, e.g. "joined late: …"
	// AckURL is the link that acknowledges this alarm's occurrence as To
	// from the page (Options.AckLink); "" for none. On a voice call it is
	// the URL the provider posts the callee's keypress to (inbound, #715).
	// It is a credential: a channel puts it in the page (or the call's
	// instructions to the provider) and nowhere else (no log, no row).
	AckURL string
	// AckCode is the code an SMS reply acknowledges with ("ACK <code>",
	// Options.AckCode); "" for none. A credential, as AckURL.
	AckCode string

	// Chatter is set on a chattering alarm's notice (#1249): each level
	// goes once per episode in place of a page per bounce. It carries no
	// AckURL or AckCode: the occurrence they bind changes at every bounce.
	Chatter *alarm.Chatter
	// Group is set on a grouped page during a flood (#1249): Alarm is
	// zero, and there is no AckURL or AckCode (they bind one occurrence),
	// so a call asks for no keypress.
	Group *Group
	// Link opens the HMI screen the page is about (the alarm, or a
	// flood's focus view) under alarms.oncall.ack-url; the person signs
	// in as usual, so it is not a credential. "" without an ack-url.
	Link string
}

// Channel delivers a Message by one means. Send may block up to its own
// timeout and retry on its own, bounded: it runs on the dispatcher's
// sender goroutine, and everything queued behind it waits. It must return
// when its context ends (Options.SendTimeout, or Close): a Send that
// ignores it holds up every page behind it, and Close.
type Channel interface {
	Send(context.Context, Message) error
}

// Engine is what the dispatcher needs of an alarm engine.
type Engine interface {
	Active() []alarm.Record
	Lookup(id string) (alarm.Record, bool)
	Notified(id string, n alarm.Notice)
}

// Options configures a Dispatcher.
type Options struct {
	Config *Config // Prepared
	Engine Engine
	// Channels by kind ("log", …); every kind a level names must be here.
	Channels map[string]Channel

	// Gate is the write gate: true while this controller may act on the
	// plant (leader.Election.IsLeader on a pair or a Kubernetes lease).
	// The sender calls it immediately before every send, never from a
	// cached copy, so a standby whose role went stale sends nothing. Nil
	// means always open (a controller that is not part of an election).
	Gate func() bool
	// Term names the leadership (runtime.Termed). Required with Gate: a
	// send queued in one leadership never leaves in another, and a new
	// leadership starts every escalation's bookkeeping afresh.
	Term func() uint64

	// Every is how often Tick looks, on the engine's clock. Default 1s.
	Every time.Duration
	// Queue is each channel kind's send buffer. Default 256.
	Queue int
	// FailAfter is how many sends in a row must fail on one channel kind
	// before Failing reports it. Default 3.
	FailAfter int
	// SendTimeout bounds one send, every attempt and wait included.
	// Default 30s.
	SendTimeout time.Duration
	// Retries is how many more attempts a send gets after a transient
	// failure (timeout, unreachable, provider-error). Default 2; negative
	// means none. The dispatcher, not the channel, retries, so the gate,
	// the term and the engine are asked again before every attempt: a
	// leader deposed, or an alarm acknowledged, during a slow first try
	// sends no second one.
	Retries int
	// Backoff is the wait before the first retry; it doubles. Default 500ms.
	Backoff time.Duration

	// AckLink mints the link a page by channel kind via carries to
	// acknowledge m's occurrence as m.To (#715), or "" for none (a kind
	// that shows no link, or no alarms.oncall.ack-url). Asked once per
	// send, just before its first attempt, after the gate: a link is
	// minted only for a page that leaves. Nil: no links.
	AckLink func(m Message, via string) string
	// AckCode makes the reply code a page by channel kind via carries
	// (inbound SMS, #715); "" for none. Nil: no codes.
	AckCode func(m Message, via string) string

	// Chatter pages a chattering alarm once per level per episode in the
	// modes that page per bounce (#1249, alarms.chatter.page).
	Chatter bool
	// Flood groups pages during a flood (#1249, alarms.flood.group-pages
	// and individual). It needs an Engine that is a Flooder.
	Flood FloodPaging
	// Home is alarms.oncall.ack-url, the HMI as a person paged reaches it:
	// chatter notices and grouped pages link to the right screen under
	// it. "" for none.
	Home string

	Log *slog.Logger
}

// sendKey names one send: one step of one escalation to one person by one
// channel. An escalation is an alarm id plus the moment it began waiting.
type sendKey struct {
	kind  sendKind
	id    string
	since int64
	step  int
	to    string
	via   string
}

type sendState uint8

const (
	pending sendState = iota + 1 // queued or in flight
	done                         // sent, or failed for good
)

// episode is one escalation of one alarm: it began when the alarm began
// waiting on a person and ends when it stops waiting.
type episode struct {
	wait int64 // the record's WaitMs: one wait is one episode

	// escClock is its clock: since is when it started (the wait, or the
	// activation under ack-or-rtn); policy is chosen at the wait's start,
	// nil when none covers the alarm. lateWhy is why it joined late: a
	// resend after a restart or a takeover when it was first seen in this
	// leadership's first look (an occurrence carried across, #715), else a
	// join late of its own.
	escClock
}

type queued struct {
	key  sendKey
	term uint64
	gen  uint64 // the bookkeeping generation that queued it (Dispatcher.gen)
	msg  Message
}

// Dispatcher runs escalation policies over an engine's records.
type Dispatcher struct {
	cfg      *Config
	eng      Engine
	channels map[string]Channel
	gate     func() bool
	term     func() uint64
	ackLink  func(Message, string) string
	ackCode  func(Message, string) string
	chatter  bool
	flood    FloodPaging
	flooder  Flooder
	home     string
	every    time.Duration
	failN    int
	log      *slog.Logger

	mu       sync.Mutex
	last     time.Time // the last Tick that looked
	lastTerm uint64
	termSeen bool
	episodes map[string]*episode
	sends    map[sendKey]sendState
	fails    map[string]int // consecutive failures by channel kind
	// chats are the chattering alarms' pinned clocks, by alarm id: they
	// last as long as the episode, not the wait (#1249).
	chats map[string]*chatClock
	// batches are the grouped pages in flight, by number (seq); lastBatch
	// when each policy step's last batch went in the flood floodID.
	batches   map[int64]*batch
	lastBatch map[batchKey]time.Time
	floodID   string
	seq       int64
	// gen counts bookkeeping resets (a new leadership). A queued send of
	// an earlier generation touches nothing when it is delivered: its key
	// may already belong to a send of this one.
	gen     uint64
	failing atomic.Bool // some kind has failed FailAfter times in a row

	sendTimeout time.Duration
	retries     int
	backoff     time.Duration
	ctx         context.Context
	cancel      context.CancelFunc

	// q has one queue and one sender goroutine per channel kind, so a mail
	// server that hangs delays only the email behind it, never an SMS.
	q        map[string]chan queued
	inflight sync.WaitGroup
	wg       sync.WaitGroup
	dropped  atomic.Uint64
	closed   bool
}

// New validates the options and starts the sender.
func New(o Options) (*Dispatcher, error) {
	if o.Config == nil || o.Engine == nil {
		return nil, fmt.Errorf("escalate: Config and Engine are required")
	}
	if o.Gate != nil && o.Term == nil {
		return nil, fmt.Errorf("escalate: a Gate needs its Term, so a send queued in one leadership never leaves in another")
	}
	for _, p := range o.Config.Policies {
		for _, l := range p.Levels {
			for _, v := range l.Via {
				if o.Channels[v] == nil {
					return nil, fmt.Errorf("escalate: policy %s names channel %q, which is not configured", p.ID, v)
				}
			}
		}
	}
	d := &Dispatcher{
		cfg: o.Config, eng: o.Engine, channels: o.Channels,
		gate: o.Gate, term: o.Term, ackLink: o.AckLink, ackCode: o.AckCode,
		every: o.Every, failN: o.FailAfter, log: o.Log,
		chatter: o.Chatter, flood: o.Flood, home: strings.TrimRight(o.Home, "/"),
		episodes:  map[string]*episode{},
		sends:     map[sendKey]sendState{},
		fails:     map[string]int{},
		chats:     map[string]*chatClock{},
		batches:   map[int64]*batch{},
		lastBatch: map[batchKey]time.Time{},
	}
	if o.Flood.Group {
		f, ok := o.Engine.(Flooder)
		if !ok {
			return nil, fmt.Errorf("escalate: grouping pages in a flood needs an Engine that reports its flood (Flooder)")
		}
		d.flooder = f
	}
	if d.every <= 0 {
		d.every = time.Second
	}
	if d.failN <= 0 {
		d.failN = 3
	}
	if d.log == nil {
		d.log = slog.Default()
	}
	d.sendTimeout = o.SendTimeout
	if d.sendTimeout <= 0 {
		d.sendTimeout = 30 * time.Second
	}
	switch d.retries, d.backoff = o.Retries, o.Backoff; {
	case d.retries < 0:
		d.retries = 0
	case d.retries == 0:
		d.retries = 2
	}
	if d.backoff <= 0 {
		d.backoff = 500 * time.Millisecond
	}
	d.ctx, d.cancel = context.WithCancel(context.Background())
	n := o.Queue
	if n <= 0 {
		n = 256
	}
	d.q = make(map[string]chan queued, len(o.Channels))
	for kind := range o.Channels {
		q := make(chan queued, n)
		d.q[kind] = q
		d.wg.Add(1)
		go d.sender(q)
	}
	return d, nil
}

// Covers reports whether some policy's match covers d, at any time of day:
// a pair's log keeps such an alarm's occurrence for a takeover (#715).
func (d *Dispatcher) Covers(def alarm.Def) bool {
	for i := range d.cfg.Policies {
		if d.cfg.Policies[i].Match.Covers(def) {
			return true
		}
	}
	return false
}

// Close stops the sender: a send in flight is cancelled and what is
// queued is dropped, so a hung channel cannot hold up a shutdown.
func (d *Dispatcher) Close() {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}
	d.closed = true
	d.cancel()
	for _, q := range d.q {
		close(q)
	}
	d.mu.Unlock()
	d.wg.Wait()
}

// Drain waits until every queued send has been tried. For tests: the
// sender is asynchronous.
func (d *Dispatcher) Drain() { d.inflight.Wait() }

// Dropped counts sends the queue could not take at the moment; each is
// tried again on a later Tick, so a nonzero count means delay, not loss.
func (d *Dispatcher) Dropped() uint64 { return d.dropped.Load() }

// AnyFailing reports whether some channel kind's last FailAfter sends all
// failed: the system alarm's condition. It takes no lock, so the engine
// may read it while evaluating.
func (d *Dispatcher) AnyFailing() bool { return d.failing.Load() }

// Failing lists the channel kinds whose last FailAfter sends all failed.
// It stays raised until a send on that kind succeeds, or this controller
// stops leading.
func (d *Dispatcher) Failing() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	for k, n := range d.fails {
		if n >= d.failN {
			out = append(out, k)
		}
	}
	return out
}

// Tick looks at the engine's records and queues every send that has
// fallen due. Call it on the scan, after the engine's Evaluate; it does
// nothing until Every has passed on now since it last looked, so the
// engine's clock (a virtual one in a test) drives it.
func (d *Dispatcher) Tick(now time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed || (!d.last.IsZero() && now.Sub(d.last) < d.every) {
		return
	}
	d.last = now
	var term uint64
	if d.term != nil {
		term = d.term()
	}
	if d.gate != nil && !d.gate() {
		// A standby looks at nothing; its bookkeeping restarts when it
		// leads. Its channels' failures were a leader's: it raises no
		// failing alarm of its own.
		d.fails = map[string]int{}
		d.failing.Store(false)
		return
	}
	firstLook := !d.termSeen || term != d.lastTerm
	if firstLook {
		// A new leadership: what an earlier one sent is not known here.
		// Every escalation is seen afresh and joins late.
		d.episodes = map[string]*episode{}
		d.sends = map[sendKey]sendState{}
		d.fails = map[string]int{}
		d.chats = map[string]*chatClock{}
		d.batches = map[int64]*batch{}
		d.lastBatch = map[batchKey]time.Time{}
		d.floodID = ""
		d.failing.Store(false)
		d.gen++
		d.lastTerm, d.termSeen = term, true
	}
	// A flood groups the pages of the alarms it covers (#1249). After a
	// takeover the engine carries the flood, so this leadership's first
	// look groups its resends too.
	var flood *alarm.Flood
	if d.flooder != nil {
		flood = d.flooder.Flood()
	}
	if flood == nil || flood.ID != d.floodID {
		clear(d.lastBatch)
		d.floodID = ""
		if flood != nil {
			d.floodID = flood.ID
		}
	}
	var cands map[batchKey][]cand
	if flood != nil {
		cands = map[batchKey][]cand{}
	}
	seen := make(map[string]bool, 16)
	pinned := map[string]bool{}
	for _, r := range d.eng.Active() {
		wait := waitKey(r)
		if wait == 0 || wait > now.UnixMilli() {
			continue // waits on nobody (or its clock ran ahead of ours)
		}
		ep := d.episodes[r.ID]
		if ep == nil || ep.wait != wait {
			// A new wait: the first look, or the alarm was acknowledged
			// (or shelved, or suppressed) and came back between two looks.
			if ep != nil {
				d.end(r.ID)
			}
			ep = &episode{wait: wait}
			ep.policy = d.cfg.Select(r.Def, time.UnixMilli(wait))
			d.episodes[r.ID] = ep
		}
		seen[r.ID] = true
		p := ep.policy
		if p == nil {
			continue
		}
		since := time.UnixMilli(r.WaitMs)
		if p.Stop == StopAckOrRTN || !r.AckRequired {
			// A return to normal ends it, so each activation is its own
			// escalation, from that activation.
			since = time.UnixMilli(r.ActiveMs)
		}
		grouped := flood != nil && (!d.individual(r) || mutEscalate("group_individual_ignored")) &&
			!(firstLook && mutEscalate("takeover_resend_individual"))
		// A bounce that restarted the clock inside a chattering episode is
		// held: the alarm escalates on the episode's clock instead. A wait
		// that began with the episode (or before it) is the latched case,
		// already once per level, and keeps its own clock.
		if d.chatter && r.Chatter != nil && (since.UnixMilli() > r.Chatter.SinceMs ||
			(mutEscalate("chatter_pin_latched") && since.UnixMilli() == r.Chatter.SinceMs)) {
			cc := d.chatClockFor(r, p, now, firstLook)
			pinned[r.ID] = true
			d.hold(r, cc, since.UnixMilli())
			if !mutEscalate("chatter_pages_per_bounce") {
				if cc.policy != nil && chatterWants(r) {
					d.due(r, &cc.escClock, kindChatter, now, term, grouped, cands)
				}
				continue
			}
		}
		if !pagesNow(r, p.Stop) {
			continue // unack-rtn under ack-or-rtn: waits, but pages nobody
		}
		if !ep.since.Equal(since) {
			d.endSends(kindAlarm, r.ID, ep.since)
			ep.restart(since)
			ep.skipBelow, ep.late = joinLate(p.steps, now.Sub(since), d.every)
			ep.lateWhy = joinedLate
			if d.chats[r.ID] != nil {
				// Its chatter just ended with this activation standing.
				ep.lateWhy = afterChatter
			}
			if firstLook {
				// Only the leader sends, and what it sent is not kept: the
				// current level goes again, once.
				ep.lateWhy = d.resendWhy()
			}
		}
		if cc := d.chats[r.ID]; cc != nil && (cc.heldAt == since.UnixMilli() || mutEscalate("chatter_end_skips_new")) &&
			!mutEscalate("chatter_end_burst") {
			// The chatter just ended with the clock it held standing: the
			// pinned clock, which began no later and sent each level due
			// on it, covered the levels due here now; only later ones
			// page. An activation it never held began after the episode
			// ended (one before would have continued it): new, it pages.
			ep.skipBelow, ep.late = max(ep.skipBelow, dueSteps(p.steps, now.Sub(ep.since))), false
		}
		d.due(r, &ep.escClock, kindAlarm, now, term, grouped, cands)
	}
	for id := range d.episodes {
		if !seen[id] {
			d.end(id) // waits on nobody now: acknowledged, normal, shelved, gone
		}
	}
	// A pinned clock outlives a look with its alarm in normal or
	// acknowledged: it ends when the engine says the episode did.
	for id, cc := range d.chats {
		if pinned[id] {
			continue
		}
		occ := ""
		if r, ok := d.eng.Lookup(id); ok {
			if r.Chatter != nil && r.Chatter.SinceMs == cc.since.UnixMilli() && !mutEscalate("chatter_clock_per_wait") {
				// Between bounces (in normal, with no acknowledgement to
				// wait on): its levels still fall due.
				if cc.policy != nil && chatterWants(r) {
					grouped := flood != nil && !d.individual(r)
					d.due(r, &cc.escClock, kindChatter, now, term, grouped, cands)
				}
				continue
			}
			occ = r.Occurrence
		}
		d.endChat(id, cc, occ)
	}
	if len(cands) > 0 {
		d.sendBatches(flood, cands, now, term)
	}
}

// Why a send joined late (Message.Reason, the notify row's reason).
const (
	joinedLate     = "joined late: earlier levels fell due before this controller saw the alarm"
	ResendRestart  = "resend after restart"
	ResendTakeover = "resend after takeover"
)

// resendWhy is a first look's late join: a takeover on a controller in an
// election (a pair's or a lease's member starts as a takeover too), else
// a restart.
func (d *Dispatcher) resendWhy() string {
	if d.gate != nil {
		return ResendTakeover
	}
	return ResendRestart
}

// dueSteps is how many of steps have fallen due after elapsed.
func dueSteps(steps []Step, elapsed time.Duration) int {
	n := 0
	for _, s := range steps {
		if s.At > elapsed {
			break
		}
		n++
	}
	return n
}

// joinLate decides which of an escalation's due steps a controller that
// first sees it after `elapsed` sends: all of them if it saw it within one
// look or two of its start, else only the latest due group (steps due at the same
// moment are one group). It returns the index of the first step to send
// and whether earlier ones were skipped.
func joinLate(steps []Step, elapsed, every time.Duration) (int, bool) {
	if elapsed <= 2*every {
		return 0, false
	}
	last := -1
	for i, s := range steps {
		if s.At <= elapsed {
			last = i
		}
	}
	if last <= 0 {
		return 0, false
	}
	first := last
	for first > 0 && steps[first-1].At == steps[last].At {
		first--
	}
	return first, first > 0
}

func (d *Dispatcher) end(id string) {
	if ep := d.episodes[id]; ep != nil {
		d.endSends(kindAlarm, id, ep.since)
	}
	delete(d.episodes, id)
}

// endSends forgets the finished sends of one clock. A pending one stays
// until the sender resolves it, so it is never queued twice.
func (d *Dispatcher) endSends(kind sendKind, id string, since time.Time) {
	ms := since.UnixMilli()
	for k, st := range d.sends {
		if k.kind == kind && k.id == id && k.since == ms && st == done {
			delete(d.sends, k)
		}
	}
}

// due queues the sends of every step of clk that has fallen due and is
// neither queued nor done. Recipients are resolved now, so a roster's
// handover between two levels pages the new person. Under a flood that
// groups r (grouped), a due step joins its policy step's next batch
// instead (cands), unless a grouped page already covers it or is in
// flight: then only the people a failed grouped page missed are paged.
func (d *Dispatcher) due(r alarm.Record, clk *escClock, kind sendKind, now time.Time, term uint64, grouped bool, cands map[batchKey][]cand) {
	elapsed := now.Sub(clk.since)
	for i, s := range clk.policy.steps {
		if s.At > elapsed {
			break
		}
		if i < clk.skipBelow || clk.skip[i] {
			continue
		}
		lvl := clk.policy.Levels[s.Level]
		reason := ""
		if clk.late {
			reason = clk.lateWhy
		}
		if sg := clk.steps[i]; sg != nil && (sg.pending > 0 || sg.covered) {
			for _, tv := range sg.fallback {
				d.queue(r, clk, kind, i, s, tv.to, tv.via, reason, term)
			}
			continue
		}
		if grouped && (clk.steps[i] == nil || len(clk.steps[i].fallback) == 0) {
			bk := batchKey{policy: clk.policy.ID, step: i}
			cands[bk] = append(cands[bk], cand{r: r, kind: kind, clk: clk, reason: reason})
			continue
		}
		people, ok := clk.people[i]
		if !ok {
			people = d.recipients(lvl.Notify, now)
			clk.people[i] = people
		}
		if len(people) == 0 {
			k := sendKey{kind: kind, id: r.ID, since: clk.since.UnixMilli(), step: i}
			if d.sends[k] == 0 {
				d.sends[k] = done
				d.eng.Notified(r.ID, alarm.Notice{Level: s.Level + 1, Via: strings.Join(lvl.Via, ","),
					Result: alarm.NotifyNobodyOnCall, Reason: "nobody on call on " + strings.Join(lvl.Notify, ", "),
					Occurrence: r.Occurrence})
			}
			continue
		}
		for _, to := range people {
			for _, via := range lvl.Via {
				d.queue(r, clk, kind, i, s, to, via, reason, term)
			}
		}
	}
}

// queue queues one individual send (an alarm's own page, or its chatter
// notice) unless it is queued or done.
func (d *Dispatcher) queue(r alarm.Record, clk *escClock, kind sendKind, i int, s Step, to, via, reason string, term uint64) {
	k := sendKey{kind: kind, id: r.ID, since: clk.since.UnixMilli(), step: i, to: to, via: via}
	if d.sends[k] != 0 {
		return
	}
	msg := Message{Alarm: r, Policy: clk.policy.ID, Level: s.Level + 1, Repeat: s.Repeat, To: to, Reason: reason}
	if kind == kindChatter {
		c := *r.Chatter
		msg.Chatter, msg.Link = &c, d.alarmLink(r.ID)
	}
	d.inflight.Add(1)
	select {
	case d.q[via] <- queued{key: k, term: term, gen: d.gen, msg: msg}:
		d.sends[k] = pending
	default:
		d.inflight.Done()
		d.dropped.Add(1) // tried again on the next look
	}
}

// recipients expands a level's notify list at t, rosters to whoever is on
// call, without duplicates.
func (d *Dispatcher) recipients(notify []string, t time.Time) []string {
	var out []string
	for _, n := range notify {
		if id, ok := strings.CutPrefix(n, RosterPrefix); ok {
			if r := d.cfg.Oncall.Roster(id); r != nil {
				for _, m := range r.OnCall(t, d.cfg.Oncall.Location()) {
					out = appendNew(out, m)
				}
			}
			continue
		}
		out = appendNew(out, n)
	}
	return out
}

// sender delivers one channel kind's queued sends one at a time. The gate
// and the term are read immediately before each send: a send whose
// leadership ended, or that finds the gate closed, does not leave, and its
// step stays due for the next leadership's look.
func (d *Dispatcher) sender(in <-chan queued) {
	defer d.wg.Done()
	for q := range in {
		d.deliver(q)
		d.inflight.Done()
	}
}

func (d *Dispatcher) deliver(q queued) {
	d.mu.Lock()
	mine := q.gen == d.gen && d.sends[q.key] == pending
	d.mu.Unlock()
	if !mine {
		return // queued under bookkeeping since reset: its key is not its own
	}
	if d.ctx.Err() != nil || !d.waiting(q.key) || !d.mayAct(q.term) {
		// Closing; or acknowledged (or over) since it was queued, judged
		// from the engine now, not from the last look; or the gate is
		// closed, or the leadership changed. Not sent, and the step stays
		// due for whoever looks next.
		d.release(q)
		return
	}
	// A chatter notice and a grouped page carry no ack link or code: they
	// bind one occurrence, which a chatter changes at every bounce and a
	// grouped page does not have.
	plain := q.key.kind == kindAlarm || mutEscalate("notice_carries_ack")
	if plain && d.ackLink != nil && q.msg.Alarm.Occurrence != "" {
		q.msg.AckURL = d.ackLink(q.msg, q.key.via)
	}
	if plain && d.ackCode != nil && q.msg.Alarm.Occurrence != "" {
		q.msg.AckCode = d.ackCode(q.msg, q.key.via)
	}
	ctx, cancel := context.WithTimeout(d.ctx, d.sendTimeout)
	err := d.channels[q.key.via].Send(ctx, q.msg)
	for attempt, wait := 0, d.backoff; err != nil && attempt < d.retries && transient(err); attempt, wait = attempt+1, wait*2 {
		select {
		case <-ctx.Done():
		case <-time.After(wait):
		}
		// Asked again before every attempt, as before the first: a retry
		// never leaves for an alarm acknowledged meanwhile, nor from a
		// leadership that ended. What failed is journalled as failed.
		if ctx.Err() != nil || !d.waiting(q.key) || !d.mayAct(q.term) {
			break
		}
		err = d.channels[q.key.via].Send(ctx, q.msg)
	}
	cancel()
	if err != nil && d.ctx.Err() != nil {
		// Cut off by Close: shutting down, not a failed channel, and the
		// page may have gone. Nothing is journalled or counted.
		d.release(q)
		return
	}
	result, reason := alarm.NotifySent, q.msg.Reason
	if err != nil {
		result = alarm.NotifyFailed
		// Only the failure's class (and a numeric code) reaches the row
		// and the log: a provider's text may echo an address or an
		// account.
		f := Failure(err)
		if reason != "" {
			f += "; " + reason
		}
		reason = f
		id := q.msg.Alarm.ID
		if q.msg.Group != nil {
			id = alarm.FloodJournalID
		}
		d.log.Warn("alarm notification failed", "id", id, "to", q.key.to, "via", q.key.via, "level", q.msg.Level, "reason", Failure(err))
	}
	var b *batch
	d.mu.Lock()
	if q.gen == d.gen {
		if q.key.kind == kindGroup {
			b = d.resolveBatch(q.key.since, toVia{q.key.to, q.key.via}, err == nil, err != nil)
			delete(d.sends, q.key) // a batch number is never queued again
		}
		if d.sends[q.key] == pending {
			// A failed send is not tried again: the next level, or a
			// repeat, comes on its own timer.
			d.sends[q.key] = done
		}
		if err != nil {
			d.fails[q.key.via]++
		} else {
			d.fails[q.key.via] = 0
		}
		failing := false
		for _, n := range d.fails {
			failing = failing || n >= d.failN
		}
		d.failing.Store(failing)
	}
	d.mu.Unlock()
	if q.key.kind == kindGroup {
		// Who was told what stays auditable per alarm: each alarm the page
		// listed gets its row, grouped (or failed, and then paged on its
		// own to this person by this channel).
		if b == nil {
			return // bookkeeping since reset: an earlier leadership's page
		}
		result, why := alarm.NotifyGrouped, "flood "+b.floodID
		if err != nil {
			result, why = alarm.NotifyFailed, "flood "+b.floodID+" grouped page: "+Failure(err)+"; paged on its own"
		}
		if q.msg.Reason != "" {
			why += "; " + q.msg.Reason
		}
		for _, ref := range b.listed {
			d.eng.Notified(ref.id, alarm.Notice{Level: q.msg.Level, To: q.key.to, Via: q.key.via,
				Result: result, Reason: why, Occurrence: ref.occ})
		}
		return
	}
	if q.msg.Chatter != nil && err == nil {
		reason = chatterWhy(q.msg.Chatter, reason)
	}
	d.eng.Notified(q.msg.Alarm.ID, alarm.Notice{Level: q.msg.Level, To: q.key.to, Via: q.key.via,
		Result: result, Reason: reason, Occurrence: q.msg.Alarm.Occurrence})
}

// chatterWhy is a chatter notice's row reason.
func chatterWhy(c *alarm.Chatter, reason string) string {
	s := fmt.Sprintf("chattering: %d times in %s", c.Activations, time.Duration(c.WindowMs)*time.Millisecond)
	if reason != "" {
		s += "; " + reason
	}
	return s
}

// transient reports a failure worth another attempt: no answer, no
// connection, or the provider's own error. Never an auth failure, a
// refusal, a TLS failure or a missing contact: those fail again.
func transient(err error) bool {
	class := Failure(err)
	var se *SendError
	if errors.As(err, &se) {
		class = se.Class
	}
	switch class {
	case FailTimeout, FailUnreachable, FailProvider:
		return true
	}
	return false
}

// release forgets a queued send that did not leave, so a later look may
// queue it again.
func (d *Dispatcher) release(q queued) {
	d.mu.Lock()
	if q.gen == d.gen && d.sends[q.key] == pending {
		delete(d.sends, q.key)
		if q.key.kind == kindGroup {
			d.resolveBatch(q.key.since, toVia{q.key.to, q.key.via}, false, false)
		}
	}
	d.mu.Unlock()
}

// waiting reports whether the escalation a send belongs to is still on,
// judged from the engine now. A grouped page is wanted while any alarm it
// lists still is.
func (d *Dispatcher) waiting(k sendKey) bool {
	if k.kind != kindGroup {
		return d.clockWaiting(k.kind, k.id, k.since)
	}
	d.mu.Lock()
	var refs []clockRef
	if b := d.batches[k.since]; b != nil {
		refs = slices.Clone(b.listed)
	}
	d.mu.Unlock()
	for _, ref := range refs {
		if d.clockWaiting(ref.kind, ref.id, ref.since) {
			return true
		}
	}
	return false
}

// clockWaiting reports whether one clock of alarm id is still on: the
// dispatcher still tracks it at the same start, and the engine, asked
// now, still has the alarm waiting on a person under the policy's stop
// rule since that same moment (an alarm's own clock), or still in the
// same chattering episode and wanting its notice (a pinned clock).
func (d *Dispatcher) clockWaiting(kind sendKind, id string, since int64) bool {
	d.mu.Lock()
	var stop string
	var wait int64
	var ok bool
	if kind == kindChatter {
		cc := d.chats[id]
		ok = cc != nil && cc.policy != nil && cc.since.UnixMilli() == since
	} else {
		ep := d.episodes[id]
		ok = ep != nil && ep.policy != nil && ep.since.UnixMilli() == since
		if ok {
			stop, wait = ep.policy.Stop, ep.wait
		}
	}
	d.mu.Unlock()
	if !ok {
		return false
	}
	r, found := d.eng.Lookup(id)
	if !found {
		return false
	}
	if kind == kindChatter {
		return r.Chatter != nil && r.Chatter.SinceMs == since && chatterWants(r)
	}
	return waitKey(r) == wait && pagesNow(r, stop) &&
		(stop != StopAckOrRTN || r.ActiveMs == since)
}

// waitKey is what one escalation of r is keyed on, zero while r pages
// nobody. An alarm that needs an acknowledgement escalates from when it
// began waiting on a person (WaitMs). One with ack-required: false never
// waits on anyone, so it pages while it is active, from its activation
// (ActiveMs), and its escalation ends when it returns to normal (or is
// shelved or suppressed): its AckActive is where it arrives, not an
// operator's acknowledgement.
func waitKey(r alarm.Record) int64 {
	if !r.AckRequired {
		if r.State == alarm.AckActive {
			return r.ActiveMs
		}
		return 0
	}
	return r.WaitMs
}

// pagesNow reports whether r's escalation sends now under stop: an
// unacknowledged alarm (Escalating), or one with ack-required: false,
// which waitKey already holds to its active state.
func pagesNow(r alarm.Record, stop string) bool {
	return r.ActiveMs != 0 && (!r.AckRequired || Escalating(r.State, stop))
}

// mayAct is the gate, asked now: open, and still the leadership that
// queued the send.
func (d *Dispatcher) mayAct(term uint64) bool {
	if d.gate == nil {
		return true
	}
	return d.gate() && d.term() == term
}
