package alarm

import (
	"context"
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

	Log *slog.Logger
}

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
	notifyWG  sync.WaitGroup
	dropped   atomic.Uint64
	closeOnce sync.Once

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
		inst:      make([]instance, 0, len(o.Defs)),
		byID:      make(map[string]int, len(o.Defs)),
		read:      o.Read,
		now:       now,
		jrn:       jrn,
		log:       log,
		notifiers: o.Notify,
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
		e.byID[d.ID] = len(e.inst)
		e.inst = append(e.inst, instance{def: d})
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
	for i := range e.inst {
		e.evalOne(&e.inst[i], now)
	}
}

func (e *Engine) evalOne(in *instance, now time.Time) {
	// Enable first, and only then the condition: a whole dark site costs
	// one map hit per definition rather than two.
	if in.def.Enable != "" {
		v, ok := e.read(in.def.Enable)
		b, isBool := truth(v)
		switch {
		case !ok:
			e.suppress(in, now, "enable tag "+in.def.Enable+" is not in this project")
			return
		case !isBool:
			e.suppress(in, now, "enable tag "+in.def.Enable+" is not a BOOL")
			return
		case !b:
			e.suppress(in, now, in.def.Enable+" is false")
			return
		}
	}

	v, ok := e.read(in.def.Tag)
	if !ok {
		e.missing++
		e.suppress(in, now, "no tag "+in.def.Tag+" (the site may never have birthed)")
		return
	}
	raw, isBool := truth(v)
	if !isBool {
		e.suppress(in, now, in.def.Tag+" is not a BOOL")
		return
	}

	if in.state == Suppressed {
		e.unsuppress(in, now)
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
	if in.state == Suppressed {
		in.reason = reason // the reason can change (enable false → tag gone)
		return
	}
	in.state = Suppressed
	in.reason = reason
	in.cond = false
	in.started = false // the delay re-baselines when the site comes back
	e.emit(in, now, KindSuppress, "")
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
// here and only here, so "Rev changed" means exactly "an alarm moved".
func (e *Engine) emit(in *instance, now time.Time, kind, by string) {
	e.rev++
	ev := Event{
		TS:       now.UnixMilli(),
		ID:       in.def.ID,
		Name:     in.def.Name,
		Kind:     kind,
		Priority: in.def.Priority,
		Site:     in.def.Site,
		State:    in.state.String(),
		By:       by,
	}
	if err := e.jrn.Append(ev); err != nil {
		e.log.Warn("alarm journal append failed", "id", ev.ID, "kind", ev.Kind, "err", err)
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

	AckBy   string `json:"ackBy,omitempty"`
	ShelfBy string `json:"shelfBy,omitempty"`

	Count  int    `json:"count"`            // activations since start, for flood detection
	Reason string `json:"reason,omitempty"` // why suppressed
}

func (in *instance) record() Record {
	r := Record{
		Def:    in.def,
		State:  in.state,
		Cond:   in.cond,
		AckBy:  in.ackBy,
		Count:  in.count,
		Reason: in.reason,
	}
	if !in.activeAt.IsZero() {
		r.ActiveMs = in.activeAt.UnixMilli()
	}
	if !in.rtnAt.IsZero() {
		r.RTNMs = in.rtnAt.UnixMilli()
	}
	if !in.ackAt.IsZero() {
		r.AckMs = in.ackAt.UnixMilli()
	}
	if !in.shelfUntil.IsZero() {
		r.ShelfUntilMs = in.shelfUntil.UnixMilli()
		r.ShelfBy = in.shelfBy
	}
	return r
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
			out = append(out, e.inst[i].record())
		}
	}
	sortRecords(out)
	return out
}

// Records returns every definition with its state, in id order — what
// `nautilus alarms list` prints and the auditable answer to "did the 14
// rules really expand to what we think".
func (e *Engine) Records() []Record {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]Record, len(e.inst))
	for i := range e.inst {
		out[i] = e.inst[i].record()
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
			ID: newest.def.ID, Name: newest.def.Name,
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

// Ack acknowledges alarms and returns how many actually changed. A nil or
// ["*"] id list means all, which is the "acknowledge everything" button.
//
// by is an audit string the engine does not authenticate: nautilus has one
// token, not user accounts. The HMI supplies the operator's name and the
// journal records what it was told — which is worth saying plainly rather
// than implying an identity the system does not have.
func (e *Engine) Ack(ids []string, by string) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.now()

	if len(ids) == 0 || (len(ids) == 1 && ids[0] == "*") {
		n := 0
		for i := range e.inst {
			if e.inst[i].ack(now, by) {
				n++
				e.emit(&e.inst[i], now, KindAck, by)
			}
		}
		return n, nil
	}

	// Resolve every id before changing anything: a typo in one id should
	// not leave half the request applied.
	idx := make([]int, 0, len(ids))
	for _, id := range ids {
		i, ok := e.byID[id]
		if !ok {
			return 0, fmt.Errorf("no alarm %q", id)
		}
		idx = append(idx, i)
	}
	n := 0
	for _, i := range idx {
		if e.inst[i].ack(now, by) {
			n++
			e.emit(&e.inst[i], now, KindAck, by)
		}
	}
	return n, nil
}

// Shelve silences one alarm until a deadline. There is no permanent shelf:
// until must be in the future, and Evaluate restores the state the shelf
// interrupted the moment it passes.
func (e *Engine) Shelve(id string, until time.Time, by string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	i, ok := e.byID[id]
	if !ok {
		return fmt.Errorf("no alarm %q", id)
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
		return fmt.Errorf("no alarm %q", id)
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

// Journal answers a journal query from whatever sink was configured.
func (e *Engine) Journal(from, to time.Time, f Filter) ([]Event, error) {
	return e.jrn.Query(from, to, f)
}

// RetainedAlarms is the operator state worth persisting: acknowledgement
// and shelf, keyed by definition id. Active and RTN are deliberately absent
// — they re-derive from the field on the next scan, and a retained "active"
// would be a claim about the plant made by a file.
func (e *Engine) RetainedAlarms() map[string]retain.AlarmRetain {
	e.mu.Lock()
	defer e.mu.Unlock()
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
		if r == (retain.AlarmRetain{}) {
			continue // omit the overwhelming majority that are simply normal
		}
		out[in.def.ID] = r
	}
	return out
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
		if r.Acked {
			in.retainedAck, in.retainedAckBy = true, r.AckBy
			in.retainedAckAt = time.UnixMilli(r.AckMs)
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
