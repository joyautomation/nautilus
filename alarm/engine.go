package alarm

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/retain"
)

// ReadPath resolves one condition path — a flat tag name, or "tag.member"
// into a struct tag — to its current value. ok is false when the path does
// not exist, which is a normal condition, not an error: a site that has
// never birthed has no tags, and one dark site must not fault the engine.
//
// The value may be a Go bool, an ir.Value, or any numeric type; anything
// else counts as unreadable. Taking `any` rather than ir.Value is what
// keeps this package free of a runtime import — wiring is a two-line
// adapter over Tags.ReadPath, and a test is a map literal.
type ReadPath func(path string) (any, bool)

// Options configures an Engine. Defs and Read are required.
type Options struct {
	// Defs is the materialized definition set, normally the output of
	// Expand. The engine does not re-expand rules: definitions are fixed
	// for the life of the engine, which is what makes Evaluate a linear
	// walk over a preallocated slice.
	Defs []Def

	// Read reaches into the tag store. Called at most twice per definition
	// per Evaluate (enable, then condition) and never concurrently with
	// itself.
	Read ReadPath

	// Now supplies every timestamp and drives both delays and shelf expiry.
	// Wire it to the runtime's clock — not time.Now — so a test that
	// advances a stopped virtual clock walks a five-minute on-delay in a
	// microsecond. Defaults to time.Now.
	Now func() time.Time

	// Journal records events. Nil builds a RingJournal of Keep entries, so
	// the journal view works with no database configured at all.
	Journal Journal
	Keep    int

	// Notify runs off the scan goroutine on a bounded queue; a slow
	// endpoint drops events with a counter rather than stalling the scan.
	Notify []Notifier

	// CarryGrace and CarryHold bound how long an occurrence carried across
	// a restart (RestoreAlarms) waits for its condition. A reading of true
	// continues it; enable false ends it. A reading of false ends it once
	// CarryGrace has passed since the restore: zero (a redundant pair,
	// whose takeover already waits for live inputs) means the first false
	// reading ends it; a standalone controller sets a grace, because its
	// first scans may read a driver's defaults before its first read. A
	// false reading inside the grace never lets an acknowledgement carry.
	// An unreadable condition (a remote site not yet birthed) holds it up
	// to CarryHold (default DefaultCarryHold), then ends it.
	CarryGrace time.Duration
	CarryHold  time.Duration

	// Substituted, when set, reports whether a condition path reads a
	// substituted signal (#707), and the substituted address that covers
	// it: such an alarm is suppressed, reason "signal substituted". Called
	// once per definition per Evaluate, under the scan, like Read: it must
	// be cheap and must not allocate while nothing is substituted.
	Substituted func(path string) (addr string, ok bool)

	Log *slog.Logger
}

// DefaultCarryHold is Options.CarryHold when it is zero.
const DefaultCarryHold = 2 * time.Minute

// DefaultKeep is the in-memory journal depth when Options.Keep is zero.
const DefaultKeep = 5000

// notifyQueue is the depth of the buffered hand-off to notifiers. Sized so
// an alarm flood — a site dropping with a few hundred alarms up — queues
// rather than drops, while a wedged webhook still cannot grow memory
// without bound.
const notifyQueue = 1024

// Engine holds one alarm instance per definition and runs the ISA-18.2
// transition table on demand.
//
// It owns no goroutine that touches the tag store and starts no ticker:
// Evaluate is called by whoever owns the scan, which is what makes the
// engine's behaviour a pure function of (definitions, tag values, clock).
type Engine struct {
	mu    sync.Mutex
	inst  []instance
	byID  map[string]int
	rev   uint64
	read  ReadPath
	now   func() time.Time
	jrn   Journal
	log   *slog.Logger
	evals uint64

	notifiers []Notifier
	notifyCh  chan Event
	// onEvent, when set (OnEvent), hears every event as it is journalled,
	// under mu: it must not call back into the engine, and must not block.
	onEvent   func(Event)
	notifyWG  sync.WaitGroup
	dropped   atomic.Uint64
	closeOnce sync.Once

	carryGrace, carryHold time.Duration

	// oos is each piece of equipment out of service (TakeOutOfService),
	// by equipment name; substituted is Options.Substituted.
	oos         map[string]outOfService
	substituted func(path string) (string, bool)

	// missing counts definitions whose condition path did not resolve on
	// the last pass — the "how much of the fleet is dark" number, which
	// deserves to be visible rather than inferred from a log.
	missing int
}

// New validates the definitions and builds the engine. Ids must be unique
// and non-empty and every definition must name a condition path; those are
// the invariants every later operation assumes.
func New(o Options) (*Engine, error) {
	if o.Read == nil {
		return nil, fmt.Errorf("alarm: Options.Read is required (it is how the engine reads conditions)")
	}
	now := o.Now
	if now == nil {
		now = time.Now
	}
	log := o.Log
	if log == nil {
		log = slog.Default()
	}
	jrn := o.Journal
	if jrn == nil {
		keep := o.Keep
		if keep <= 0 {
			keep = DefaultKeep
		}
		jrn = NewRing(keep)
	}

	e := &Engine{
		inst:       make([]instance, 0, len(o.Defs)),
		byID:       make(map[string]int, len(o.Defs)),
		read:       o.Read,
		now:        now,
		jrn:        jrn,
		log:        log,
		notifiers:  o.Notify,
		carryGrace: o.CarryGrace,
		carryHold:  o.CarryHold,

		substituted: o.Substituted,
	}
	if e.carryHold <= 0 {
		e.carryHold = DefaultCarryHold
	}
	for _, d := range o.Defs {
		if d.ID == "" {
			d.ID = d.Tag
		}
		if d.ID == "" {
			return nil, fmt.Errorf("alarm: a definition has neither id nor tag")
		}
		if d.Tag == "" {
			return nil, fmt.Errorf("alarm %q: tag: is required — it is the BOOL condition to watch", d.ID)
		}
		if _, dup := e.byID[d.ID]; dup {
			return nil, fmt.Errorf("alarm %q is declared twice", d.ID)
		}
		if d.Name == "" {
			d.Name = d.ID
		}
		if d.Equipment == "" {
			d.Equipment = defaultEquipment(d)
		}
		e.byID[d.ID] = len(e.inst)
		e.inst = append(e.inst, instance{def: d})
		e.inst[len(e.inst)-1].derive()
	}

	if len(e.notifiers) > 0 {
		e.notifyCh = make(chan Event, notifyQueue)
		e.notifyWG.Add(1)
		go e.notifyLoop()
	}
	return e, nil
}

// Close stops the notifier queue and drains what is already on it. The
// journal is NOT closed: the engine was handed one and does not own its
// lifetime. Evaluate must not be called afterwards.
func (e *Engine) Close() error {
	e.closeOnce.Do(func() {
		if e.notifyCh != nil {
			close(e.notifyCh)
			e.notifyWG.Wait()
		}
	})
	return nil
}

// Evaluate runs one pass over every definition: read enable, read the
// condition, apply the delays, run the transition table, emit what changed.
//
// Called once after each scan, inside the scan's own lock, so the tag store
// is consistent and nothing else can be writing it. It never blocks on a
// notifier and never returns an error — a definition that cannot be read is
// Suppressed with a reason, which is a state an operator can see, not a
// failure that would take the scan down with it.
func (e *Engine) Evaluate() {
	e.mu.Lock()
	defer e.mu.Unlock()

	now := e.now()
	e.evals++
	e.missing = 0
	e.expireOutOfService(now)
	for i := range e.inst {
		e.evalOne(&e.inst[i], now)
	}
}

func (e *Engine) evalOne(in *instance, now time.Time) {
	in.warn = warnNone
	// An operator's out of service first: it holds whatever the field says.
	if o, ok := e.oos[in.eqKey]; ok {
		e.suppressOOS(in, now, o)
		return
	}
	// Enable next, and only then the condition: a whole dark site costs
	// one map hit per definition rather than two.
	if in.def.Enable != "" {
		v, ok := e.read(in.def.Enable)
		b, isBool := truth(v)
		switch {
		case !ok:
			e.holdCarry(in, now)
			e.suppress(in, now, "enable tag "+in.def.Enable+" is not in this project")
			return
		case !isBool:
			e.holdCarry(in, now)
			e.suppress(in, now, "enable tag "+in.def.Enable+" is not a BOOL")
			return
		case !b:
			if !mutAlarm("carry_through_enable") {
				e.endCarry(in, "enable "+in.def.Enable+" is false") // designed suppression ends it, as it does live
			}
			e.suppressBy(in, now, SuppressDesigned, in.def.Enable+" is false", "")
			return
		}
	}
	if e.suppressWhen(in) {
		if in.carry != nil {
			e.endCarry(in, "suppress-when "+in.def.SuppressWhen+" is true")
		}
		e.suppressBy(in, now, SuppressDesigned, in.swReason, "")
		return
	}
	if e.substituted != nil {
		if addr, ok := e.substituted(in.def.Tag); ok {
			e.holdCarry(in, now)
			reason := in.reason
			if in.state != Suppressed || in.suppKind != SuppressSubstituted || in.subAddr != addr {
				reason, in.subAddr = "signal substituted: "+addr, addr
			}
			e.suppressBy(in, now, SuppressSubstituted, reason, "")
			return
		}
	}

	v, ok := e.read(in.def.Tag)
	if !ok {
		e.missing++
		e.holdCarry(in, now)
		e.suppress(in, now, "no tag "+in.def.Tag+" (the site may never have birthed)")
		return
	}
	raw, isBool := truth(v)
	if !isBool {
		e.holdCarry(in, now)
		e.suppress(in, now, in.def.Tag+" is not a BOOL")
		return
	}

	if in.state == Suppressed {
		e.unsuppress(in, now)
	}
	if !in.judged {
		in.judged, in.adoptable = true, raw && in.carry == nil
	} else if !raw {
		in.adoptable = false
	}
	if in.carry != nil {
		e.judgeCarry(in, raw, now)
	}

	in.qualify(raw, now)

	// A shelf always expires. Restoring the state it interrupted — rather
	// than dropping to Normal — is what stops a shelf from being a
	// backdoor acknowledgement.
	if in.state == Shelved && !now.Before(in.shelfUntil) {
		in.state = in.prior
		in.shelfUntil, in.shelfBy = time.Time{}, ""
		e.emit(in, now, KindUnshelve, "")
	}
	if in.state == Shelved {
		return // silenced: the condition still tracks, but nothing annunciates
	}

	for _, kind := range in.step(now) {
		e.emit(in, now, kind, "")
	}
}

func (e *Engine) suppress(in *instance, now time.Time, reason string) {
	e.suppressBy(in, now, SuppressUnreadable, reason, "")
}

// suppressBy suppresses in with a kind and a reason; by names who did it
// (an operator's out of service), journalled on the suppress row.
func (e *Engine) suppressBy(in *instance, now time.Time, kind, reason, by string) {
	in.suppKind, in.suppBy = kind, by
	if kind == SuppressOutOfService || kind == SuppressDesigned {
		// Designed or an operator's: whatever activates afterwards is a
		// new occurrence, which an ack retained from before the restart
		// was never about (Fable review of #1222).
		in.retainedAck = false
	}
	// Suppressed, unreadable or disabled: whatever reads true later has not
	// stood throughout, as a carry would judge it (#1198 review).
	if !mutAlarm("adopt_after_suppress") {
		in.judged, in.adoptable = true, false
	} else {
		in.adoptable = false
	}
	if in.state == Suppressed {
		in.reason = reason // the reason can change (enable false → tag gone)
		return
	}
	in.state = Suppressed
	in.reason = reason
	in.cond = false
	in.started = false // the delay re-baselines when the site comes back
	e.emit(in, now, KindSuppress, by)
}

func (e *Engine) unsuppress(in *instance, now time.Time) {
	in.state = Normal
	in.reason = ""
	e.emit(in, now, KindUnsuppress, "")
	// A shelf outlives a dark site: shelving an alarm and then losing the
	// node should not un-silence it the moment the node returns.
	if !in.shelfUntil.IsZero() && now.Before(in.shelfUntil) {
		in.prior = Normal
		in.state = Shelved
	}
}

// emit journals one state change and hands it to the notifiers. Rev bumps
// here and in Rename, so "Rev changed" means exactly "an alarm moved or
// now reads differently".
func (e *Engine) emit(in *instance, now time.Time, kind, by string) {
	e.rev++
	if in.state == Normal {
		in.adoptable = false // the occurrence that stood has ended
	}
	if !in.state.Unacked() {
		in.waitAt = time.Time{}
	} else if in.waitAt.IsZero() {
		in.waitAt = now
	}
	ev := Event{
		TS:         now.UnixMilli(),
		ID:         in.def.ID,
		Name:       in.displayName(),
		Kind:       kind,
		Priority:   in.def.Priority,
		Site:       in.def.Site,
		State:      in.state.String(),
		By:         by,
		Occurrence: in.occ,
	}
	if err := e.jrn.Append(ev); err != nil {
		e.log.Warn("alarm journal append failed", "id", ev.ID, "kind", ev.Kind, "err", err)
	}
	if e.onEvent != nil {
		e.onEvent(ev)
	}
	if e.notifyCh != nil {
		select {
		case e.notifyCh <- ev:
		default:
			// Dropping is the correct failure: a scan that waits on an HTTP
			// endpoint is a controller that stops controlling.
			e.dropped.Add(1)
		}
	}
}

func (e *Engine) notifyLoop() {
	defer e.notifyWG.Done()
	ctx := context.Background()
	for ev := range e.notifyCh {
		for _, n := range e.notifiers {
			if err := n.Notify(ctx, ev); err != nil {
				e.log.Warn("alarm notify failed", "id", ev.ID, "kind", ev.Kind, "err", err)
			}
		}
	}
}

// Dropped counts events the notifier queue could not accept. Nonzero means
// a notifier is slower than the alarm rate, which is worth a diagnostic
// line rather than a silent gap.
func (e *Engine) Dropped() uint64 { return e.dropped.Load() }

// Record is one definition plus its live state — the /api/alarms row.
type Record struct {
	Def
	State State `json:"state"`
	Cond  bool  `json:"cond"` // the qualified condition, post-delay

	ActiveMs     int64 `json:"activeMs,omitempty"`
	RTNMs        int64 `json:"rtnMs,omitempty"`
	AckMs        int64 `json:"ackMs,omitempty"`
	ShelfUntilMs int64 `json:"shelfUntilMs,omitempty"`
	// WaitMs is when the alarm began waiting on a person: its first
	// unacknowledged state since it was last acknowledged, shelved,
	// suppressed or normal. Zero while it waits on nobody.
	WaitMs int64 `json:"waitMs,omitempty"`

	AckBy   string `json:"ackBy,omitempty"`
	ShelfBy string `json:"shelfBy,omitempty"`

	// Occurrence names the alarm's current (or, once normal, its last)
	// occurrence: an ack may name it, and is then refused by any other.
	Occurrence string `json:"occurrence,omitempty"`

	Count  int    `json:"count"`            // activations since start, for flood detection
	Reason string `json:"reason,omitempty"` // why suppressed

	// Suppression is the kind of suppression while suppressed
	// (SuppressOutOfService, SuppressDesigned, SuppressSubstituted,
	// SuppressUnreadable); SuppressedBy who did it, for an operator's out
	// of service, and SuppressUntilMs when that ends.
	Suppression     string `json:"suppression,omitempty"`
	SuppressedBy    string `json:"suppressedBy,omitempty"`
	SuppressUntilMs int64  `json:"suppressUntilMs,omitempty"`

	// Warning says why the alarm is shown although its definition may
	// have meant it hidden: its suppress-when could not be read.
	Warning string `json:"warning,omitempty"`
}

func (in *instance) record() Record {
	r := Record{
		Def:    in.def,
		State:  in.state,
		Cond:   in.cond,
		AckBy:  in.ackBy,
		Count:  in.count,
		Reason: in.reason,

		Warning: in.warning(),

		Occurrence: in.occ,
	}
	r.Name = in.displayName() // the record is a view: the lent name, if any
	if !in.activeAt.IsZero() {
		r.ActiveMs = in.activeAt.UnixMilli()
	}
	if !in.rtnAt.IsZero() {
		r.RTNMs = in.rtnAt.UnixMilli()
	}
	if !in.ackAt.IsZero() {
		r.AckMs = in.ackAt.UnixMilli()
	}
	if !in.waitAt.IsZero() {
		r.WaitMs = in.waitAt.UnixMilli()
	}
	if !in.shelfUntil.IsZero() {
		r.ShelfUntilMs = in.shelfUntil.UnixMilli()
		r.ShelfBy = in.shelfBy
	}
	if in.state == Suppressed {
		r.Suppression, r.SuppressedBy = in.suppKind, in.suppBy
	}
	return r
}

// recordOf is in's record, with its equipment's out-of-service end while
// that suppresses it. Under e.mu.
func (e *Engine) recordOf(in *instance) Record {
	r := in.record()
	if r.Suppression == SuppressOutOfService {
		if o, ok := e.oos[in.eqKey]; ok {
			left, _ := o.remaining(e.now())
			r.SuppressUntilMs = e.now().Add(left).UnixMilli()
		}
	}
	return r
}

// Lookup returns one alarm's record by id, the way Records shows it.
func (e *Engine) Lookup(id string) (Record, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	i, ok := e.byID[id]
	if !ok {
		return Record{}, false
	}
	return e.recordOf(&e.inst[i]), true
}

// Active returns everything annunciating — active, unack-RTN and shelved —
// sorted worst first, then newest first, which is the order the alarm table
// displays and therefore the order the server should not have to compute.
func (e *Engine) Active() []Record {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]Record, 0, 32)
	for i := range e.inst {
		if e.inst[i].state.Annunciating() {
			out = append(out, e.recordOf(&e.inst[i]))
		}
	}
	sortRecords(out)
	return out
}

// Records returns every definition with its state, in id order — what
// `naut alarms list` prints and the auditable answer to "did the 14
// rules really expand to what we think".
func (e *Engine) Records() []Record {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]Record, len(e.inst))
	for i := range e.inst {
		out[i] = e.recordOf(&e.inst[i])
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func sortRecords(out []Record) {
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Priority != out[j].Priority {
			return out[i].Priority > out[j].Priority
		}
		if out[i].ActiveMs != out[j].ActiveMs {
			return out[i].ActiveMs > out[j].ActiveMs
		}
		return out[i].ID < out[j].ID
	})
}

// Brief is the one alarm a banner names: enough to render a line, not
// enough to be a second copy of the table.
type Brief struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Priority Priority `json:"priority"`
	Ms       int64    `json:"ms"`
}

// Summary is counts only — never the whole list on a 250 ms frame.
//
// Active counts alarms wanting attention (unack-active, ack-active,
// unack-rtn); Shelved and Suppressed are counted separately because they
// are deliberately NOT wanting attention. Rev bumps on any state change, so
// an HMI refetches the full list exactly when something moved.
type Summary struct {
	Active     int            `json:"active"`
	Unacked    int            `json:"unacked"`
	Shelved    int            `json:"shelved"`
	Suppressed int            `json:"suppressed"`
	ByPriority map[string]int `json:"byPriority"`
	Worst      Priority       `json:"worst"`
	Newest     *Brief         `json:"newest,omitempty"`
	Rev        uint64         `json:"rev"`
}

// Summary computes the banner's numbers in one pass.
func (e *Engine) Summary() Summary {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := Summary{ByPriority: map[string]int{}, Rev: e.rev}
	var newest, newestUnacked *instance
	for i := range e.inst {
		in := &e.inst[i]
		switch in.state {
		case Shelved:
			s.Shelved++
			continue
		case Suppressed:
			s.Suppressed++
			continue
		case Normal:
			continue
		}
		s.Active++
		s.ByPriority[in.def.Priority.String()]++
		if in.def.Priority > s.Worst {
			s.Worst = in.def.Priority
		}
		if in.state.Unacked() {
			s.Unacked++
			if newestUnacked == nil || in.activeAt.After(newestUnacked.activeAt) {
				newestUnacked = in
			}
		}
		if newest == nil || in.activeAt.After(newest.activeAt) {
			newest = in
		}
	}
	// The banner names the newest UNACKED alarm when there is one — that is
	// the thing an operator has not seen — and otherwise the newest active.
	if newestUnacked != nil {
		newest = newestUnacked
	}
	if newest != nil {
		s.Newest = &Brief{
			ID: newest.def.ID, Name: newest.displayName(),
			Priority: newest.def.Priority, Ms: newest.activeAt.UnixMilli(),
		}
	}
	return s
}

// Rev is Summary().Rev without the rest of the pass, for a caller that only
// needs to know whether anything moved.
func (e *Engine) Rev() uint64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.rev
}

// Missing is how many definitions did not resolve on the last Evaluate —
// the fleet-is-dark number.
func (e *Engine) Missing() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.missing
}

// ErrNoAlarm is the error Ack, Shelve, Unshelve and Rename wrap when an id
// names no alarm, so a caller can tell "no such alarm" (an HTTP 404) from
// a refusal about one that exists (a 400). The message reads `no alarm "X"`.
var ErrNoAlarm = errors.New("no alarm")

// Ack acknowledges alarms and returns how many actually changed. A nil or
// ["*"] id list means all, which is the "acknowledge everything" button.
//
// by is an audit string the engine does not authenticate: nautilus has one
// token, not user accounts. The HMI supplies the operator's name and the
// journal records what it was told — which is worth saying plainly rather
// than implying an identity the system does not have.
func (e *Engine) Ack(ids []string, by string) (int, error) {
	acked, err := e.AckIDs(ids, by)
	return len(acked), err
}

// ErrOtherOccurrence is the error an ack naming an occurrence wraps when
// the alarm is on another one now (an HTTP 409): it returned to normal
// and activated again since the person saw it.
var ErrOtherOccurrence = errors.New("a new occurrence since; open the HMI to see it")

// AckIDs is Ack that says which alarms it acknowledged (the ones that
// actually changed), in the order it did them. The audit journal records
// them for an acknowledge-all, whose target is only "*".
func (e *Engine) AckIDs(ids []string, by string) ([]string, error) {
	return e.AckIDsAt(ids, nil, by)
}

// AckIDsAt is AckIDs where occ names, for some of the ids, the occurrence
// the ack is for: if any of those alarms is on another occurrence now,
// nothing is acknowledged and the error wraps ErrOtherOccurrence. An id
// occ does not name is acknowledged on whatever occurrence it is on (an
// HMI that predates occurrences). With ids nil (all), occ is ignored.
func (e *Engine) AckIDsAt(ids []string, occ map[string]string, by string) ([]string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.now()
	acked := []string{}

	if len(ids) == 0 || (len(ids) == 1 && ids[0] == "*") {
		for i := range e.inst {
			if e.inst[i].ack(now, by) {
				acked = append(acked, e.inst[i].def.ID)
				e.emit(&e.inst[i], now, KindAck, by)
			}
		}
		return acked, nil
	}

	// Resolve every id before changing anything: a typo in one id should
	// not leave half the request applied.
	idx := make([]int, 0, len(ids))
	for _, id := range ids {
		i, ok := e.byID[id]
		if !ok {
			return nil, fmt.Errorf("%w %q", ErrNoAlarm, id)
		}
		if err := e.inst[i].sameOccurrence(occ); err != nil {
			return nil, err
		}
		idx = append(idx, i)
	}
	for _, i := range idx {
		if e.inst[i].ack(now, by) {
			acked = append(acked, e.inst[i].def.ID)
			e.emit(&e.inst[i], now, KindAck, by)
		}
	}
	return acked, nil
}

// AckCommitted applies an acknowledgement a pair's log committed for
// occurrence occ of alarm id (activated at activeMs). On that occurrence
// it acks as AckIDsAt does. On another, it is refused (ErrOtherOccurrence)
// unless the alarm has stood since this engine's last restore (adoptable):
// a takeover that could not catch the log up began a new occurrence for
// it, and the log now proves the ack was of the occurrence that stood
// through the takeover. The alarm then takes that occurrence's ID and
// activation, and is acknowledged.
func (e *Engine) AckCommitted(id, occ string, activeMs int64, by string) (bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	i, ok := e.byID[id]
	if !ok {
		return false, fmt.Errorf("%w %q", ErrNoAlarm, id)
	}
	in := &e.inst[i]
	if occ != in.occ {
		st := in.underShelf()
		if !in.adoptable || in.occ == "" || st != UnackActive || occ == "" || activeMs <= 0 || mutAlarm("adopt_off") {
			return false, fmt.Errorf("alarm %q: %w", id, ErrOtherOccurrence)
		}
		in.occ, in.activeAt, in.adoptable = occ, notAfter(time.UnixMilli(activeMs), e.now()), false
	}
	now := e.now()
	if !in.ack(now, by) {
		return false, nil
	}
	e.emit(in, now, KindAck, by)
	return true, nil
}

// sameOccurrence refuses an ack occ names for another occurrence than
// in's current one.
func (in *instance) sameOccurrence(occ map[string]string) error {
	want, named := occ[in.def.ID]
	if !named || want == in.occ || mutAlarm("ack_any_occurrence") {
		return nil
	}
	return fmt.Errorf("alarm %q: %w", in.def.ID, ErrOtherOccurrence)
}

// Shelve silences one alarm until a deadline. There is no permanent shelf:
// until must be in the future, and Evaluate restores the state the shelf
// interrupted the moment it passes.
func (e *Engine) Shelve(id string, until time.Time, by string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	i, ok := e.byID[id]
	if !ok {
		return fmt.Errorf("%w %q", ErrNoAlarm, id)
	}
	in := &e.inst[i]
	if !in.def.Shelvable {
		return fmt.Errorf("alarm %q is not shelvable", id)
	}
	now := e.now()
	if !until.After(now) {
		return fmt.Errorf("alarm %q: shelve until %s is not in the future", id, until.Format(time.RFC3339))
	}
	if in.state != Shelved && in.state != Suppressed {
		in.prior = in.state
		in.state = Shelved
	}
	in.shelfUntil, in.shelfBy = until, by
	e.emit(in, now, KindShelve, by)
	return nil
}

// Unshelve ends a shelf early, restoring the state it interrupted.
func (e *Engine) Unshelve(id string, by string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	i, ok := e.byID[id]
	if !ok {
		return fmt.Errorf("%w %q", ErrNoAlarm, id)
	}
	in := &e.inst[i]
	if in.shelfUntil.IsZero() {
		return fmt.Errorf("alarm %q is not shelved", id)
	}
	if in.state == Shelved {
		in.state = in.prior
	}
	in.shelfUntil, in.shelfBy = time.Time{}, ""
	e.emit(in, e.now(), KindUnshelve, by)
	return nil
}

// Rename gives an alarm the name it reads by from now on: on its record,
// the banner's newest line, and every journal row and notification after
// this. It is for an alarm whose condition is fixed but whose meaning is
// not: one alarm for "any of the site's Prometheus rules is firing on this
// host" reads as the rule that is, set by whoever knows, before the
// evaluation that activates it. "" returns to the definition's own name.
// A change bumps Rev, so an HMI refetches the list and shows it; renaming
// to the current name is free.
func (e *Engine) Rename(id, name string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	i, ok := e.byID[id]
	if !ok {
		return fmt.Errorf("%w %q", ErrNoAlarm, id)
	}
	in := &e.inst[i]
	if name == in.def.Name {
		name = "" // its own name is no loan
	}
	if in.lent != name {
		in.lent = name
		e.rev++
	}
	return nil
}

// OnEvent registers fn to hear every event as it is journalled (a
// redundant pair's leader proposes an ack-clear when an acknowledged alarm
// returns to normal, #710 PR 3b-2c). fn runs under the engine's lock: it
// must neither call the engine nor block. Call it before the first
// Evaluate.
func (e *Engine) OnEvent(fn func(Event)) {
	e.mu.Lock()
	e.onEvent = fn
	e.mu.Unlock()
}

// Notice is one notification an escalation policy sent or tried to send
// (#715), as Notified journals it. To is a principal, never an address.
type Notice struct {
	Level  int    // 1 is the first level
	To     string // principal: a user name or oidc:<name>
	Via    string // channel kind
	Result string // NotifySent, NotifyFailed, NotifyNobodyOnCall
	Reason string // optional: "joined late: …"
	// Occurrence is the occurrence the notification was for, as the
	// queued message carried it (not the alarm's current one).
	Occurrence string
}

// Notified journals a KindNotify row for alarm id, with the alarm's name,
// priority, site and current state, under the engine's clock. Like Note it
// only journals: it is not a state change, so it bumps no Rev and reaches
// neither the Notifiers nor OnEvent (a webhook notifier must not hear of
// its own escalation, and a pair's ack-clear hook has nothing to do with
// it). An unknown id is journalled bare: the alarm may have left the
// definition set while a send was in flight.
func (e *Engine) Notified(id string, n Notice) {
	e.mu.Lock()
	defer e.mu.Unlock()
	ev := Event{TS: e.now().UnixMilli(), ID: id, Kind: KindNotify, By: "system",
		Level: n.Level, To: n.To, Via: n.Via, Result: n.Result, Reason: n.Reason, Occurrence: n.Occurrence}
	if i, ok := e.byID[id]; ok {
		in := &e.inst[i]
		ev.Name, ev.Priority, ev.Site, ev.State = in.displayName(), in.def.Priority, in.def.Site, in.state.String()
	}
	if err := e.jrn.Append(ev); err != nil {
		e.log.Warn("alarm journal append failed", "id", id, "kind", ev.Kind, "err", err)
	}
}

// Note journals a system event that belongs to no alarm definition
// (kind, a name that says what happened), as the engine's own events
// are: "acks not carried: takeover unsynced", for one.
func (e *Engine) Note(kind, name string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	ev := Event{TS: e.now().UnixMilli(), ID: "__nautilus.redundancy", Name: name, Kind: kind, By: "system"}
	if err := e.jrn.Append(ev); err != nil {
		e.log.Warn("alarm journal append failed", "kind", kind, "err", err)
	}
}

// Journal answers a journal query from whatever sink was configured.
func (e *Engine) Journal(from, to time.Time, f Filter) ([]Event, error) {
	return e.jrn.Query(from, to, f)
}

// RetainedAlarms is the operator state worth persisting: acknowledgement
// and shelf, keyed by definition id, and for an alarm whose condition is
// present, its occurrence: ID, activation and wait (#715), so a restart
// neither re-pages from level 1 nor kills the links already sent. The
// state itself is not kept: a retained occurrence is a claim the field
// must re-assert (RestoreAlarms), never one the file makes alone.
func (e *Engine) RetainedAlarms() map[string]retain.AlarmRetain {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.now()
	out := make(map[string]retain.AlarmRetain, 8)
	for i := range e.inst {
		in := &e.inst[i]
		var r retain.AlarmRetain
		// An alarm counts as acknowledged if an operator discharged it and
		// it has not re-activated since.
		if !in.ackAt.IsZero() && (in.state == AckActive || (in.state == Shelved && in.prior == AckActive)) {
			r.Acked, r.AckBy, r.AckMs = true, in.ackBy, in.ackAt.UnixMilli()
		}
		if !in.shelfUntil.IsZero() {
			r.ShelfUntilMs, r.ShelfBy = in.shelfUntil.UnixMilli(), in.shelfBy
		}
		if c := in.carry; c != nil {
			// Still held from the last restore: kept as it came, so a
			// second restart before the condition was read loses nothing.
			r.Occurrence, r.ActiveMs = c.occ, c.activeAt.UnixMilli()
			if !c.waitAt.IsZero() {
				r.WaitMs = c.waitAt.UnixMilli()
			}
			// Its ack only while no false reading came first: the next
			// restore would not know one had (#1198 review).
			if c.acked && !r.Acked && (!c.sawFalse || mutAlarm("carry_ack_resaved_after_false")) {
				r.Acked, r.AckBy, r.AckMs = true, c.ackBy, c.ackAt.UnixMilli()
			}
		} else if st := in.underShelf(); in.occ != "" && (st == UnackActive || st == AckActive) {
			r.Occurrence, r.ActiveMs = in.occ, in.activeAt.UnixMilli()
			if !in.waitAt.IsZero() {
				r.WaitMs = in.waitAt.UnixMilli()
			}
		}
		if o, ok := e.oos[in.eqKey]; ok {
			o.retained(&r, now)
		}
		if r == (retain.AlarmRetain{}) {
			continue // omit the overwhelming majority that are simply normal
		}
		out[in.def.ID] = r
	}
	return out
}

// ResetOperatorState forgets every acknowledgement and shelf, and every
// alarm's state, as a fresh engine would start: the next Evaluate
// re-derives each alarm from the field. A redundant pair calls it at
// every takeover, before RestoreAlarms, because a replica that led before
// keeps an engine frozen at its last leadership: acks of occurrences that
// ended since, shelves undone since (#1033 review). Nothing is journalled;
// Rename's names stay.
func (e *Engine) ResetOperatorState() {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i := range e.inst {
		in := &e.inst[i]
		lent, def := in.lent, in.def
		*in = instance{def: def, lent: lent}
		in.derive()
	}
	e.oos = nil
	e.rev++
}

// RestoreAlarms reapplies retained operator state. Call it once, before the
// first Evaluate: the acknowledgement is held aside and applied to the
// FIRST activation of each alarm, because the field will re-assert the
// condition on the next scan and would otherwise re-annunciate hundreds of
// alarms an operator has already seen. Ids no longer in the definition set
// are ignored — a definition can legitimately disappear from a manifest.
func (e *Engine) RestoreAlarms(m map[string]retain.AlarmRetain) {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.now()
	for id, r := range m {
		i, ok := e.byID[id]
		if !ok {
			continue
		}
		in := &e.inst[i]
		switch {
		case r.Occurrence != "" && r.ActiveMs > 0:
			// The occurrence the condition was present for, held until the
			// condition is read (judgeCarry); its ack, if any, goes with it
			// and applies to nothing else.
			c := &carried{occ: r.Occurrence, activeAt: time.UnixMilli(r.ActiveMs),
				falseUntil: now.Add(e.carryGrace), holdUntil: now.Add(e.carryHold)}
			if r.WaitMs > 0 {
				c.waitAt = time.UnixMilli(r.WaitMs)
			}
			if r.Acked {
				c.acked, c.ackBy, c.ackAt = true, r.AckBy, time.UnixMilli(r.AckMs)
			}
			in.carry = c
		case r.Acked:
			// Written before occurrences: applied to the first activation,
			// as it always was.
			in.retainedAck, in.retainedAckBy = true, r.AckBy
			in.retainedAckAt = time.UnixMilli(r.AckMs)
		}
		if r.OOSEquipment != "" {
			e.restoreOutOfService(r, now)
		}
		if r.ShelfUntilMs > 0 {
			until := time.UnixMilli(r.ShelfUntilMs)
			if until.After(now) && in.def.Shelvable {
				in.shelfUntil, in.shelfBy = until, r.ShelfBy
				in.prior, in.state = Normal, Shelved
			}
		}
	}
}

// underShelf is in's state, or under a shelf the state it interrupted.
func (in *instance) underShelf() State {
	if in.state == Shelved {
		return in.prior
	}
	return in.state
}

// holdCarry is a carried occurrence's evaluation with no condition to
// read: it waits, up to its hold.
func (e *Engine) holdCarry(in *instance, now time.Time) {
	if in.carry != nil && !now.Before(in.carry.holdUntil) && !mutAlarm("carry_unbounded") {
		e.endCarry(in, "its condition could not be read")
	}
}

// judgeCarry decides a carried occurrence on a reading of its condition:
// true continues it; false ends it, unless inside the grace, where it
// waits on, no longer acknowledged.
func (e *Engine) judgeCarry(in *instance, raw bool, now time.Time) {
	c := in.carry
	switch {
	case raw:
		in.carry = nil
		e.carryOn(in, c, now)
	case !now.Before(c.falseUntil) && !mutAlarm("carry_on_false"):
		e.endCarry(in, "the field no longer asserted it")
	default:
		c.sawFalse = true
	}
}

// endCarry ends in's carried occurrence. One that was acknowledged says
// so in the journal (a system note, as Note writes): the ack is not
// carried, so the alarm's next activation annunciates, and an operator
// reading the journal should see why. Under e.mu.
func (e *Engine) endCarry(in *instance, why string) {
	c := in.carry
	in.carry = nil
	if c == nil || !c.acked || mutAlarm("carry_end_silent") {
		return
	}
	ev := Event{TS: e.now().UnixMilli(), ID: "__nautilus.alarms", Kind: "system", By: "system",
		Name:       "ack of " + in.def.ID + " by " + c.ackBy + " not carried: its occurrence ended while this controller was stopped or taking over (" + why + ")",
		Occurrence: c.occ}
	if err := e.jrn.Append(ev); err != nil {
		e.log.Warn("alarm journal append failed", "kind", ev.Kind, "err", err)
	}
}

// carryOn continues the carried occurrence c: the condition is present
// again, so the alarm is on it as it was, with its ID, activation and
// wait, and acknowledged if it was (and no false reading came first). The
// condition counts as qualified (it was before the restore): no on-delay.
// Nothing is journalled and nobody is notified: from the plant's side
// nothing happened, and a report counts no new activation.
func (e *Engine) carryOn(in *instance, c *carried, now time.Time) {
	target := &in.state
	if in.state == Shelved {
		target = &in.prior
	}
	if *target != Normal {
		return // already on an occurrence (cannot happen before the first activation)
	}
	in.raw, in.rawSince, in.started, in.cond = true, now, true, true
	// Times from another clock (the last run's, or the old leader's) are
	// never later than now: a wait in the future pages nobody until the
	// clock reaches it (#1198 review: an RTC set back at a restart).
	in.occ, in.activeAt, in.rtnAt = c.occ, notAfter(c.activeAt, now), time.Time{}
	in.ackAt, in.ackBy = time.Time{}, ""
	in.retainedAck = false
	if c.acked && (!c.sawFalse || mutAlarm("carry_ack_after_false")) && in.def.AckRequired {
		*target = AckActive
		in.ackAt, in.ackBy = c.ackAt, c.ackBy
	} else {
		*target = in.collapse(UnackActive)
	}
	in.waitAt = time.Time{}
	if in.state.Unacked() {
		in.waitAt = now
		if !c.waitAt.IsZero() && !mutAlarm("carry_wait_reset") {
			in.waitAt = notAfter(c.waitAt, now)
		}
	}
	e.rev++
}

// notAfter is t, or now if t is later: a time carried from another clock.
func notAfter(t, now time.Time) time.Time {
	if t.After(now) && !mutAlarm("carry_unclamped") {
		return now
	}
	return t
}

// Snapshot and Restore are RetainedAlarms and RestoreAlarms under the names
// the rest of the codebase uses for the same idea. Provided so a wiring
// site reads consistently with retain's other callers; they are the same
// call.
func (e *Engine) Snapshot() map[string]retain.AlarmRetain { return e.RetainedAlarms() }
func (e *Engine) Restore(m map[string]retain.AlarmRetain) { e.RestoreAlarms(m) }

// truth coerces a tag value to a BOOL condition. ok is false for anything
// that is not a boolean or a number — a STRING tag named as a condition is
// a manifest mistake, and reporting it as "not a BOOL" beats guessing.
func truth(v any) (val, ok bool) {
	switch x := v.(type) {
	case nil:
		return false, false
	case bool:
		return x, true
	case ir.Value:
		switch x.Kind {
		case ir.TypeBool:
			return x.B, true
		case ir.TypeInt, ir.TypeTime:
			return x.I != 0, true
		case ir.TypeReal:
			return x.F != 0, true
		}
		return false, false
	case float64:
		return x != 0, true
	case float32:
		return x != 0, true
	case int:
		return x != 0, true
	case int32:
		return x != 0, true
	case int64:
		return x != 0, true
	}
	return false, false
}
