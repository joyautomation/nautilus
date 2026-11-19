package runtime

import (
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/joyautomation/nautilus/lang/ir"
)

// Substitutes in time (#707 PR 1b): expiry, the companion tags, the
// start acknowledgement and the event stream.
//
// # Timing
//
// A substitute is granted a duration. Its wall-clock start and until are
// recorded for people and for replication, but the running controller
// times it on a MONOTONIC clock (CLOCK_BOOTTIME on Linux), from when it
// applied it: a wall-clock step (NTP, an operator setting the date) then
// neither ends nor extends it. The clock is the injected one under
// Options.Clock (naut test runs it on virtual time).
//
// The wall clock is consulted once per entry, where a monotonic deadline
// cannot carry over: a takeover (another replica, or this one after a
// spell as standby) and, in PR 3, a restart from the retain store. There
// the remainder is computed once from until (substituteRemainder) and
// timed monotonically from then on, so a takeover never restarts the full
// duration and downtime counts. An entry this process was already timing
// (it led before) keeps the earlier of the two deadlines: a wall clock
// stepped back meanwhile never stretches it.
//
// Expiry is evaluated once per MAIN scan, after the input read whether or
// not it succeeded, and before the logic, so the scan that sees the live
// value back also sees __Substitute_Expired. Only a leader scans, so a
// standby never expires an entry on its own; nothing here waits for a
// quorum or a commit.
//
// # Companions
//
// Options.Substitutable names the inputs that may be substituted. Each
// gets a read-only BOOL <tag>__Substituted, true while the tag or any
// member of it is substituted, and the resource gets __SubstitutesUnacked
// (an ack-on-start substitute is not yet acknowledged) and
// __Substitute_Expired (a substitute just expired: held until a main
// scan's logic has run with it AND a second has passed, so the alarm
// engine, which evaluates after each main scan, and every sampler on its
// own tick both see it). They are recomputed from the table, in one
// place, under the store lock that changes the table, so no scan's
// snapshot sees a substitute without its flag.

// SubstituteSkew is how far the wall clocks of a redundant pair may
// disagree. At a takeover, a remainder up to the granted duration plus
// this is taken as the clocks' ordinary disagreement and capped at the
// granted duration; beyond it the new leader's clock was stepped, and the
// cap is reported as a fault (SubstituteEvent kind "fault").
const SubstituteSkew = 30 * time.Second

// The companion tags (see above).
const (
	// SubstitutedSuffix follows a substitutable tag's name: "LS_1__Substituted".
	SubstitutedSuffix = "__Substituted"
	// SubstitutesUnackedTag is true while an ack-on-start substitute
	// awaits its acknowledgement: a program's start interlocks on it.
	SubstitutesUnackedTag = "__SubstitutesUnacked"
	// SubstituteExpiredTag pulses when a substitute expires.
	SubstituteExpiredTag = "__Substitute_Expired"
	// SubstituteFaultTag is true while a substitute needs a person: an
	// entry's remainder was capped against a stepped clock, or the retain
	// store could not be loaded, so substitutes it held may be missing.
	// It is not latched (the alarm layer latches); every set and clear is
	// an event (fault, fault-clear) and a log line, so a transient fault
	// is never invisible.
	SubstituteFaultTag = "__Substitute_Fault"
)

// substitutePulse is the least time __Substitute_Expired stays true.
const substitutePulse = time.Second

// Substitute event kinds.
const (
	SubstituteEventStart  = "start"
	SubstituteEventStop   = "stop"   // ended by someone (Reason "replaced": a newer start took the address)
	SubstituteEventExpire = "expire" // ended by its duration (By "system")
	SubstituteEventAck    = "ack"
	SubstituteEventFault  = "fault" // the entry runs on, but something needs a person (Reason says what)
	// SubstituteEventFaultClear: a fault is over (Reason is the fault's).
	// An entry's fault clears when the entry ends.
	SubstituteEventFaultClear = "fault-clear"
)

// Substitute event reasons.
const (
	SubstituteReasonReplaced = "replaced"
	SubstituteReasonExpired  = "expired"
	// SubstituteReasonClockCapped: at a takeover, until lay more than the
	// granted duration plus SubstituteSkew ahead of this replica's clock;
	// the remainder was capped at the granted duration.
	SubstituteReasonClockCapped = "clock-capped"
	// SubstituteReasonExpiredWhileDown: at a restart, the entry's time ran
	// out while the controller was down, or the wall clock went backwards
	// across the restart (substitute_retain.go).
	SubstituteReasonExpiredWhileDown = "expired-while-down"
	// SubstituteReasonRefusedAtLoad: a retained entry the project no longer
	// allows (its rule, its input or its type changed in a deploy) ended
	// at the load instead of running on.
	SubstituteReasonRefusedAtLoad = "refused-at-load"
	// SubstituteReasonSuperseded: at a takeover from a shared retain store,
	// an entry this replica held from an earlier leadership was not in the
	// store (the leader since then ended it, or it began just before this
	// replica lost the lease and was never saved), so it stops here. It is
	// journalled, since in the second case nobody else recorded its end.
	SubstituteReasonSuperseded = "superseded"
	// SubstituteReasonRetainLoadFailed: the retain store could not be
	// loaded, so substitutes it held may be missing (a fault with no
	// entry).
	SubstituteReasonRetainLoadFailed = "retain-load-failed"
)

// SubstituteEvent is one change to the substitute table, as audit and
// journal consumers hear it (OnSubstituteEvent). Every entry ends with
// exactly one stop or expire.
type SubstituteEvent struct {
	Kind   string       `json:"kind"`
	Reason string       `json:"reason,omitempty"`
	Entry  Substitution `json:"entry"`
	By     string       `json:"by"`
	AtMs   int64        `json:"atMs"`
}

// goMonoClock is Go's monotonic clock, as a duration since it was taken.
func goMonoClock() func() time.Duration {
	start := time.Now()
	return func() time.Duration { return time.Since(start) }
}

// monoFor is a runtime's substitute clock: the virtual clock under
// Options.Clock, else the machine's boot clock.
func monoFor(c Clock) func() time.Duration {
	if c != nil {
		return func() time.Duration { return time.Duration(c.Now().UnixNano()) }
	}
	return bootClock()
}

// remainderMode says whose clock an entry's until is being read against.
type remainderMode uint8

const (
	// remainRestart: this controller's own entry, across its restart. A
	// clock that went back past the start went backwards: expired.
	remainRestart remainderMode = iota
	// remainTakeover: an entry another replica (or an earlier leadership)
	// timed. A clock behind by more than SubstituteSkew is capped.
	remainTakeover
)

// remainder is what is left of an entry when its deadline is re-derived
// from the wall clock.
type remainder struct {
	left    time.Duration
	expired bool
	capped  bool // beyond the granted duration plus SubstituteSkew: a fault
}

// substituteRemainder is an entry's remainder on the wall clock now: until
// − now, so time the controller was down counts. Zero or less has expired.
// It never exceeds the granted duration. An entry the process is already
// timing never ends later for it (rederiveLocked).
//
// In restart mode the clock must not have gone backwards across the
// restart: a now before the start, or before lastSeen (the last time the
// controller saved while a substitute ran; zero when unknown), has, and
// the entry expires rather than trust it. Example: started 12:00 for 8 h,
// restarted at a true 15:00 with the clock reading 12:30; until − now
// would leave 7.5 h, but lastSeen (about 15:00) is later than now.
func substituteRemainder(start, until time.Time, granted time.Duration, now, lastSeen time.Time, m remainderMode) remainder {
	left := until.Sub(now)
	if left <= 0 || m == remainRestart && (now.Before(start) || now.Before(lastSeen)) {
		return remainder{expired: true}
	}
	if left > granted {
		return remainder{left: granted, capped: left > granted+SubstituteSkew}
	}
	return remainder{left: left}
}

// subCompanion is one substitutable root and its __Substituted tag.
type subCompanion struct{ root, flag string }

// syncSubLocked sets every companion from the table, and whether a scan
// has substitute work to do. Every change to the table or the pulse calls
// it. Caller holds t.mu.
func (t *Tags) syncSubLocked() {
	unacked := false
	for _, ss := range t.subs {
		for i := range ss.entries {
			if e := &ss.entries[i]; e.AckOnStart && !e.Acked {
				unacked = true
			}
		}
	}
	for _, c := range t.subComp {
		tv, _, ok := t.lookupLocked(c.root)
		t.setOwnedLocked(c.flag, ok && tv.substituted)
	}
	fault := t.subLoadFault
	for _, ss := range t.subs {
		for i := range ss.entries {
			if ss.entries[i].Fault != "" {
				fault = true
			}
		}
	}
	t.setOwnedLocked(t.subUnackedTag, unacked)
	t.setOwnedLocked(t.subExpiredTag, t.pulseOn)
	t.setOwnedLocked(t.subFaultTag, fault)
	t.subBusy.Store(len(t.subs) > 0 || t.pulseOn)
}

// setOwnedLocked stores a runtime-owned BOOL, past the read-only rule.
// Caller holds t.mu.
func (t *Tags) setOwnedLocked(name string, b bool) {
	if name == "" {
		return
	}
	if tv := t.vals[name]; tv != nil {
		t.storeLocked(name, tv, ir.BoolVal(b))
	}
}

// expireLocked ends every entry whose deadline has passed, as an expiry,
// first clearing a pulse that has been seen long enough. Caller holds t.mu.
func (t *Tags) expireLocked(now time.Duration, at int64) []SubstituteEvent {
	if t.pulseOn && t.pulseSeen && now-t.pulseAt >= substitutePulse {
		t.pulseOn = false
	}
	var ids []uint64
	for _, ss := range t.subs {
		for i := range ss.entries {
			if ss.entries[i].deadline <= now {
				ids = append(ids, ss.entries[i].ID)
			}
		}
	}
	evs := t.endExpiredLocked(ids, now, at, SubstituteReasonExpired)
	t.syncSubLocked()
	return evs
}

// endExpiredLocked ends the entries ids as expired, for reason, and arms
// the pulse. Caller holds t.mu and calls syncSubLocked after.
func (t *Tags) endExpiredLocked(ids []uint64, now time.Duration, at int64, reason string) []SubstituteEvent {
	if len(ids) == 0 {
		return nil
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	evs := make([]SubstituteEvent, 0, len(ids))
	for _, id := range ids {
		if s, ok := t.unsubstituteLocked(id); ok {
			evs = append(evs, SubstituteEvent{Kind: SubstituteEventExpire, Reason: reason, Entry: s, By: "system", AtMs: at})
			evs = append(evs, faultCleared(s, at)...)
		}
	}
	t.armPulseLocked(now)
	return evs
}

// armPulseLocked starts (or restarts) the __Substitute_Expired pulse: a
// substitute ended without anyone stopping it, and the live value is back
// in the logic. Caller holds t.mu and calls syncSubLocked after.
func (t *Tags) armPulseLocked(now time.Duration) {
	t.pulseOn, t.pulseAt, t.pulseSeen = true, now, false
}

// faultCleared is the fault-clear event for an entry that ended while it
// carried a fault, or nothing.
func faultCleared(s Substitution, at int64) []SubstituteEvent {
	if s.Fault == "" {
		return nil
	}
	return []SubstituteEvent{{Kind: SubstituteEventFaultClear, Reason: s.Fault, Entry: s, By: "system", AtMs: at}}
}

// rederiveLocked re-times every entry from its wall-clock until (see
// substituteRemainder), ending those that have expired. lastSeen counts in
// restart mode only (zero: unknown). Caller holds t.mu.
func (t *Tags) rederiveLocked(wall, lastSeen time.Time, now time.Duration, m remainderMode) []SubstituteEvent {
	var ids []uint64
	var evs []SubstituteEvent
	for _, ss := range t.subs {
		for i := range ss.entries {
			e := &ss.entries[i]
			rm := substituteRemainder(time.UnixMilli(e.StartMs), time.UnixMilli(e.UntilMs),
				time.Duration(e.GrantedMs)*time.Millisecond, wall, lastSeen, m)
			if rm.expired {
				ids = append(ids, e.ID)
				continue
			}
			// An entry this process has been timing keeps the earlier of
			// its deadline and the wall clock's: a wall-clock step back
			// before the edge must not stretch it past its granted time.
			if !e.timed || now+rm.left < e.deadline {
				e.deadline = now + rm.left
			}
			e.timed = true
			if rm.capped && e.Fault == "" {
				e.Fault = SubstituteReasonClockCapped
				evs = append(evs, SubstituteEvent{Kind: SubstituteEventFault, Reason: SubstituteReasonClockCapped,
					Entry: t.reportLocked(ss, e), By: "system", AtMs: wall.UnixMilli()})
			}
		}
	}
	reason := SubstituteReasonExpired
	if m == remainRestart {
		reason = SubstituteReasonExpiredWhileDown
	}
	evs = append(evs, t.endExpiredLocked(ids, now, wall.UnixMilli(), reason)...)
	t.syncSubLocked()
	return evs
}

// expireSubstitutes is the main scan's substitute step, after the read
// and before the logic. Idle (no entry, no pulse) it is one atomic load.
func (r *Runtime) expireSubstitutes() {
	if !r.tags.subBusy.Load() && r.subPendingN.Load() == 0 {
		return
	}
	now := r.mono()
	r.subMu.Lock()
	defer r.subMu.Unlock()
	// The read before this step may have hung past the lease: a replica
	// that is no longer leading leaves the entry for the leader, as the
	// output step leaves the outputs (#621). Checked under subMu, so no
	// start or stop slips in between.
	if r.coord != nil && !r.coord.IsLeader() {
		return
	}
	var evs []SubstituteEvent
	if r.subPendingN.Load() > 0 {
		evs = r.applyPendingSubstitutes()
	}
	r.tags.mu.Lock()
	evs = append(evs, r.tags.expireLocked(now, r.wallNow().UnixMilli())...)
	r.tags.mu.Unlock()
	logSubEvents(evs)
	r.subEvents.push(evs)
}

// logSubEvents puts the changes the runtime made on its own (an expiry, a
// capped remainder) in the journal, whoever listens for events: nobody may.
// Called after the store lock is released, so a slow log sink never holds
// up a reader of the store.
func logSubEvents(evs []SubstituteEvent) {
	for _, e := range evs {
		switch e.Kind {
		case SubstituteEventExpire:
			slog.Info("runtime: substitute expired; the live value is back",
				"substitute", e.Entry.Name, "id", e.Entry.ID, "reason", e.Reason, "grantedMs", e.Entry.GrantedMs)
		case SubstituteEventFault:
			switch e.Reason {
			case SubstituteReasonClockCapped:
				slog.Warn("runtime: substitute's until lies beyond its granted duration on this clock; capped at the granted duration",
					"substitute", e.Entry.Name, "id", e.Entry.ID, "reason", e.Reason, "until", time.UnixMilli(e.Entry.UntilMs), "now", time.UnixMilli(e.AtMs))
			default:
				slog.Warn("runtime: substitute fault", "substitute", e.Entry.Name, "id", e.Entry.ID, "reason", e.Reason)
			}
		case SubstituteEventFaultClear:
			slog.Info("runtime: substitute fault cleared", "substitute", e.Entry.Name, "id", e.Entry.ID, "reason", e.Reason)
		case SubstituteEventStop:
			if e.Reason == SubstituteReasonSuperseded {
				slog.Info("runtime: substitute ended at the retain load: the store holds another entry for it, or none",
					"substitute", e.Entry.Name, "id", e.Entry.ID)
			}
		}
	}
}

// pulseSeen records that a main scan's logic ran while
// __Substitute_Expired was true.
func (r *Runtime) pulseSeen() {
	if !r.tags.subBusy.Load() {
		return
	}
	r.tags.mu.Lock()
	if r.tags.pulseOn {
		r.tags.pulseSeen = true
	}
	r.tags.mu.Unlock()
}

// retimeSubstitutes re-derives every entry's deadline at a takeover:
// once, from until on this replica's wall clock.
func (r *Runtime) retimeSubstitutes() {
	if r.tags.SubstituteCount() == 0 {
		return
	}
	r.subMu.Lock()
	defer r.subMu.Unlock()
	r.tags.mu.Lock()
	evs := r.tags.rederiveLocked(r.wallNow(), time.Time{}, r.mono(), remainTakeover)
	r.tags.mu.Unlock()
	logSubEvents(evs)
	r.subEvents.push(evs)
}

// AckSubstitutes acknowledges the named entries that are active, wait for
// an acknowledgement and have not had one, and returns them. An ID not in
// that state (ended, replaced, never ack-on-start, already acknowledged)
// is passed over, so acknowledging a stale list never acknowledges a
// newer substitute.
func (r *Runtime) AckSubstitutes(ids []uint64, by string) []Substitution {
	if r.standby() {
		return nil // see Restore: a standby's ack would be undone at its takeover
	}
	r.subMu.Lock()
	defer r.subMu.Unlock()
	want := make(map[uint64]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	at := r.wallNow().UnixMilli()
	var acked []Substitution
	var evs []SubstituteEvent
	t := r.tags
	t.mu.Lock()
	for _, ss := range t.subs {
		for i := range ss.entries {
			e := &ss.entries[i]
			if !want[e.ID] || !e.AckOnStart || e.Acked {
				continue
			}
			e.Acked = true
			s := t.reportLocked(ss, e)
			acked = append(acked, s)
			evs = append(evs, SubstituteEvent{Kind: SubstituteEventAck, Entry: s, By: by, AtMs: at})
		}
	}
	if len(acked) > 0 {
		t.subRev++
		t.syncSubLocked()
	}
	t.mu.Unlock()
	r.subEvents.push(evs)
	sort.Slice(acked, func(i, j int) bool { return acked[i].ID < acked[j].ID })
	return acked
}

// OnSubstituteEvent registers fn to hear every change to the substitute
// table, in the order the changes were made. fn runs on a goroutine of its
// own, never under a lock of the runtime, so a slow listener delays only
// later events, never a scan; none is dropped. cancel unregisters it.
func (r *Runtime) OnSubstituteEvent(fn func(SubstituteEvent)) (cancel func()) {
	return r.subEvents.add(fn)
}

// subEventQueue hands events from under the runtime's locks to the
// listeners. Its goroutine runs only while there is something to deliver.
type subEventQueue struct {
	mu        sync.Mutex
	q         []SubstituteEvent
	running   bool
	listeners []subListener
	next      int
}

type subListener struct {
	id int
	fn func(SubstituteEvent)
}

func (e *subEventQueue) add(fn func(SubstituteEvent)) func() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.next++
	id := e.next
	e.listeners = append(e.listeners, subListener{id, fn})
	// Events queued before anyone listened (a restart's expired-while-down,
	// from the retain load before the server registers) go to the first
	// listener.
	if len(e.q) > 0 && !e.running {
		e.running = true
		go e.drain()
	}
	return func() {
		e.mu.Lock()
		defer e.mu.Unlock()
		for i, l := range e.listeners {
			if l.id == id {
				e.listeners = append(e.listeners[:i:i], e.listeners[i+1:]...)
				return
			}
		}
	}
}

// subEventBacklog bounds the events kept while nobody listens.
const subEventBacklog = 1024

// push queues evs. Callers hold the lock that ordered the changes (subMu),
// so the queue's order is theirs. While nobody listens they are kept, up
// to subEventBacklog (the oldest go first, logged), for the first
// listener: the retain load runs before the server registers its audit
// listener, and an expired-while-down record must reach the journal.
func (e *subEventQueue) push(evs []SubstituteEvent) {
	if len(evs) == 0 {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.q = append(e.q, evs...)
	if len(e.listeners) == 0 {
		if n := len(e.q) - subEventBacklog; n > 0 {
			slog.Warn("runtime: substitute events dropped: nobody is listening", "dropped", n)
			e.q = append(e.q[:0:0], e.q[n:]...)
		}
		return
	}
	if !e.running {
		e.running = true
		go e.drain()
	}
}

func (e *subEventQueue) drain() {
	for {
		e.mu.Lock()
		if len(e.q) == 0 {
			e.running = false
			e.mu.Unlock()
			return
		}
		batch := e.q
		e.q = nil
		ls := append([]subListener(nil), e.listeners...)
		e.mu.Unlock()
		for _, ev := range batch {
			for _, l := range ls {
				deliverSubEvent(l.fn, ev)
			}
		}
	}
}

func deliverSubEvent(fn func(SubstituteEvent), ev SubstituteEvent) {
	defer func() {
		if p := recover(); p != nil {
			slog.Error("runtime: substitute event listener panicked", "panic", p)
		}
	}()
	fn(ev)
}

// declareSubstituteCompanions adds the companions of o.Substitutable to
// o.Tags as BOOL state tags starting false, and returns their names. A
// name the project already uses for something else is refused: the
// runtime would overwrite it every time the table changed.
func declareSubstituteCompanions(o *Options) ([]string, error) {
	if len(o.Substitutable) == 0 {
		return nil, nil
	}
	used := map[string]string{}
	note := func(n string) {
		if n != "" {
			used[ir.NameKey(n)] = n
		}
	}
	for _, d := range o.Tags {
		note(d.Name)
	}
	for n := range o.Seed {
		note(n)
	}
	for _, n := range o.Inputs {
		note(n)
	}
	for _, n := range o.Outputs {
		note(n)
	}
	note(o.DtTag)
	seen := map[string]bool{}
	var names []string
	for _, n := range o.Substitutable {
		if seen[ir.NameKey(n)] {
			return nil, fmt.Errorf("substitutable %s is listed twice", n)
		}
		seen[ir.NameKey(n)] = true
		names = append(names, n+SubstitutedSuffix)
	}
	names = append(names, SubstitutesUnackedTag, SubstituteExpiredTag, SubstituteFaultTag)
	tags := slices.Clone(o.Tags)
	for _, n := range names {
		if other, ok := used[ir.NameKey(n)]; ok {
			return nil, fmt.Errorf("tag %s is declared, but the runtime sets %s from the substitute table: rename it", other, n)
		}
		tags = append(tags, State(n, false, Desc("set by the runtime from the substitute table (read-only)")))
	}
	o.Tags = tags
	return names, nil
}

// ownSubstituteCompanions marks the declared companions runtime-owned and
// ties each __Substituted flag to its input. Every substitutable name must
// be a driver-bound input.
func (r *Runtime) ownSubstituteCompanions(subs, comps []string) error {
	if len(subs) == 0 {
		return nil
	}
	r.subRoots = make(map[string]struct{}, len(subs))
	t := r.tags
	t.mu.Lock()
	defer t.mu.Unlock()
	for i, n := range subs {
		root := ""
		for _, in := range r.inputs {
			if ir.SameName(in, n) {
				root = in
				break
			}
		}
		if root == "" {
			return fmt.Errorf("substitutable %s is not a driver-bound input: only an input can be substituted", n)
		}
		r.subRoots[ir.NameKey(root)] = struct{}{}
		t.subComp = append(t.subComp, subCompanion{root: root, flag: comps[i]})
	}
	t.subUnackedTag, t.subExpiredTag, t.subFaultTag = comps[len(subs)], comps[len(subs)+1], comps[len(subs)+2]
	for _, n := range comps {
		tv := t.vals[n]
		if tv == nil {
			return fmt.Errorf("substitute companion %s was not seeded", n)
		}
		tv.owned = true
	}
	t.syncSubLocked()
	return nil
}
