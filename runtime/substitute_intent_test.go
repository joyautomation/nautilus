package runtime

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/retain"
)

// Substitutes through the operator-intent log (#707 PR 3b), against a fake
// log: the takeover's catch-up before the first scan, the fallback when it
// fails, idempotent apply, and the leader's expiry as an entry.

// atomicLead is a Coordinator safe to read from the runtime's background
// goroutines (the catch-up retry, the expiry loop) while the test moves it.
type atomicLead struct{ on atomic.Bool }

func lead(b bool) *atomicLead        { c := &atomicLead{}; c.on.Store(b); return c }
func (c *atomicLead) IsLeader() bool { return c.on.Load() }
func (c *atomicLead) set(b bool)     { c.on.Store(b) }

// fakeLog is an IntentLog the test steers.
type fakeLog struct {
	mu       sync.Mutex
	block    chan struct{} // SyncLeader waits on it (nil: returns at once)
	syncErr  error
	onSync   func() // runs inside SyncLeader before it returns nil (the catch-up's applies)
	syncs    int
	expErr   error
	expiries []uint64
	expTries int
}

func (f *fakeLog) SyncLeader(ctx context.Context) error {
	f.mu.Lock()
	f.syncs++
	block, err, on := f.block, f.syncErr, f.onSync
	f.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err != nil {
		return err
	}
	if on != nil {
		on()
	}
	return nil
}

func (f *fakeLog) ProposeExpiry(_ context.Context, id uint64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.expTries++
	if f.expErr != nil {
		return f.expErr
	}
	f.expiries = append(f.expiries, id)
	return nil
}

func (f *fakeLog) set(fn func(*fakeLog)) { f.mu.Lock(); fn(f); f.mu.Unlock() }

func (f *fakeLog) proposed() []uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]uint64(nil), f.expiries...)
}

func replicated(clk *subClock, id uint64, name string, v float64, granted time.Duration) ReplicatedSubstitute {
	return ReplicatedSubstitute{ID: id, Name: name, Value: v, By: "op",
		UntilMs: clk.Now().Add(granted).UnixMilli(), GrantedMs: granted.Milliseconds()}
}

// scanUntil scans until the logic has run (Mirror moved off its seed),
// for up to 5 s, and returns Mirror after the first scan that ran logic.
func scanUntilLogic(t *testing.T, rt *Runtime) float64 {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		rt.Scan()
		if m := rt.Tags().Real("Mirror"); m != 0 {
			return m
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("the logic never ran")
	return 0
}

// §8.1 case 7, the runtime's side: the takeover holds the first scan until
// the log's catch-up returns, and the catch-up's entries are what the
// first scan's logic runs on.
func TestTakeoverWaitsForTheLogCatchUp(t *testing.T) {
	clk := noon()
	coord := lead(false)
	rt, _ := newRetainSubRig(t, coord, nil, clk)
	rt.Scan() // standby: never scanned, Mirror at its seed 0
	release := make(chan struct{})
	log := &fakeLog{block: release}
	log.onSync = func() {
		if err := rt.ApplySubstitute(replicated(clk, 7, "Level", 95, time.Hour)); err != nil {
			t.Error(err)
		}
	}
	rt.SetIntentLog(log, 5*time.Second)
	coord.set(true)
	for i := 0; i < 3; i++ {
		rt.Scan()
	}
	if m := rt.Tags().Real("Mirror"); m != 0 {
		t.Fatalf("Mirror = %v while the catch-up runs; want no scan yet", m)
	}
	close(release)
	if m := scanUntilLogic(t, rt); m != 95 {
		t.Fatalf("the first scan ran on Level %v; want the caught-up substitute 95", m)
	}
}

// The catch-up does not finish within its wait: the takeover runs on the
// retained entries the log has not delivered, each marked catch-up-failed,
// with __Substitute_Fault; a later catch-up confirms them and clears it.
func TestTakeoverFallsBackWhenTheCatchUpFails(t *testing.T) {
	clk := noon()
	store := &fakeStore{state: retain.State{Substitutes: &retain.SubstituteState{
		Entries: []retain.SubstituteRetain{{ID: 5, Name: "Level", Value: 95.0, By: "op",
			StartMs: clk.Now().UnixMilli(), UntilMs: clk.Now().Add(time.Hour).UnixMilli(), GrantedMs: time.Hour.Milliseconds()}},
		HighID: 5,
	}}}
	coord := lead(false)
	rt, _ := newRetainSubRig(t, coord, store, clk)
	rt.Scan()
	log := &fakeLog{syncErr: errors.New("no majority")}
	rt.SetIntentLog(log, 50*time.Millisecond)
	events := collect(rt)
	coord.set(true)
	if m := scanUntilLogic(t, rt); m != 95 {
		t.Fatalf("first scan on Level %v; want the retained substitute 95", m)
	}
	if l := rt.Substitutes(); len(l) != 1 || l[0].Fault != SubstituteReasonCatchUpFailed || !rt.Tags().Bool(SubstituteFaultTag) {
		t.Fatalf("%+v, fault %v; want the retained entry marked catch-up-failed and the fault up", l, rt.Tags().Bool(SubstituteFaultTag))
	}
	// The log answers now and delivers entry 5: the retry confirms it.
	log.set(func(f *fakeLog) {
		f.syncErr = nil
		f.onSync = func() {
			_ = rt.ApplySubstitute(ReplicatedSubstitute{ID: 5, Name: "Level", Value: 95.0, By: "op", UntilMs: clk.Now().Add(time.Hour).UnixMilli(), GrantedMs: time.Hour.Milliseconds()})
		}
	})
	waitFor(t, "the fault to clear", func() bool { return !rt.Tags().Bool(SubstituteFaultTag) })
	if l := rt.Substitutes(); len(l) != 1 || l[0].Fault != "" {
		t.Fatalf("%+v; want entry 5 confirmed", l)
	}
	evs := events()
	if !hasEvent(evs, SubstituteEventFault, SubstituteReasonCatchUpFailed) || !hasEvent(evs, SubstituteEventFaultClear, SubstituteReasonCatchUpFailed) {
		t.Fatalf("events %+v; want the catch-up fault and its clearing", evs)
	}
}

// A retained entry the log never confirms ends once a catch-up succeeds:
// the log is the truth.
func TestUnconfirmedRetainedEntryEndsAfterACatchUp(t *testing.T) {
	clk := noon()
	store := &fakeStore{state: retain.State{Substitutes: &retain.SubstituteState{
		Entries: []retain.SubstituteRetain{{ID: 5, Name: "Level", Value: 95.0, By: "op",
			StartMs: clk.Now().UnixMilli(), UntilMs: clk.Now().Add(time.Hour).UnixMilli(), GrantedMs: time.Hour.Milliseconds()}},
		HighID: 5,
	}}}
	coord := lead(false)
	rt, _ := newRetainSubRig(t, coord, store, clk)
	rt.Scan()
	log := &fakeLog{syncErr: errors.New("no majority")}
	rt.SetIntentLog(log, 50*time.Millisecond)
	coord.set(true)
	scanUntilLogic(t, rt)
	log.set(func(f *fakeLog) { f.syncErr = nil })
	waitFor(t, "the unconfirmed entry to end", func() bool { return len(rt.Substitutes()) == 0 })
	if rt.Tags().Bool(SubstituteFaultTag) {
		t.Fatal("the fault is still up after the catch-up")
	}
}

// With the catch-up done, the log wins: a retained entry it does not hold
// is not applied.
func TestSyncedTakeoverIgnoresTheRetainedSet(t *testing.T) {
	clk := noon()
	store := &fakeStore{state: retain.State{Substitutes: &retain.SubstituteState{
		Entries: []retain.SubstituteRetain{{ID: 9, Name: "Flow", Value: 7.0, By: "op",
			StartMs: clk.Now().UnixMilli(), UntilMs: clk.Now().Add(time.Hour).UnixMilli(), GrantedMs: time.Hour.Milliseconds()}},
		HighID: 9,
	}}}
	coord := lead(false)
	rt, _ := newRetainSubRig(t, coord, store, clk)
	rt.Scan()
	rt.SetIntentLog(&fakeLog{}, time.Second)
	rt.tags.setAny("Flow", ir.RealVal(3)) // an input with an init: it exists before any read
	events := collect(rt)
	coord.set(true)
	scanUntilLogic(t, rt)
	if l := rt.Substitutes(); len(l) != 0 || rt.Tags().Bool(SubstituteFaultTag) {
		t.Fatalf("%+v; want nothing: the log holds no substitute", l)
	}
	if evs := events(); len(evs) != 0 {
		t.Fatalf("events %+v; a synced takeover never touches the retained set", evs)
	}
}

// Applying committed entries is idempotent and names entries by seq: the
// same entry twice is one; a retry with the same request ID is not
// applied; an unsubstitute naming an older seq ends nothing; a suback
// acknowledges; every event is marked replicated. A standby applies them.
func TestApplyingLogEntries(t *testing.T) {
	clk := noon()
	coord := lead(false)
	rt, _ := newRetainSubRig(t, coord, nil, clk)
	rt.Scan()
	rt.tags.setAny("Level", ir.RealVal(10)) // a standby that has read its inputs
	events := collect(rt)
	a := replicated(clk, 3, "Level", 95, time.Hour)
	a.AckOnStart, a.RequestID = true, "req-1"
	for i := 0; i < 2; i++ {
		if err := rt.ApplySubstitute(a); err != nil {
			t.Fatal(err)
		}
	}
	retry := replicated(clk, 4, "Level", 95, time.Hour)
	retry.RequestID = "req-1"
	if err := rt.ApplySubstitute(retry); err != nil {
		t.Fatal(err)
	}
	if l := rt.Substitutes(); len(l) != 1 || l[0].ID != 3 {
		t.Fatalf("%+v; want entry 3 alone (the same entry twice, then a retry)", l)
	}
	newer := replicated(clk, 6, "Level", 96, time.Hour)
	if err := rt.ApplySubstitute(newer); err != nil {
		t.Fatal(err)
	}
	rt.ApplyUnsubstitute(3, "late", "") // older: replaced already
	if l := rt.Substitutes(); len(l) != 1 || l[0].ID != 6 {
		t.Fatalf("%+v; a stale unsubstitute ended the newer entry", l)
	}
	rt.ApplySuback([]uint64{6}, "sup")
	rt.ApplyUnsubstitute(6, "system", SubstituteReasonExpired)
	if l := rt.Substitutes(); len(l) != 0 {
		t.Fatalf("%+v; want none", l)
	}
	for _, e := range events() {
		if !e.Replicated {
			t.Fatalf("event %+v is not marked replicated", e)
		}
	}
}

// Only the leader expires an entry, at once and locally, and records the
// expiry in the log; a log that cannot take it (no quorum) does not delay
// the expiry, and the record is retried until it is taken.
func TestLeaderExpiryIsProposedAndNeverWaits(t *testing.T) {
	clk := noon()
	coord := lead(true)
	rt, _ := newRetainSubRig(t, coord, nil, clk)
	log := &fakeLog{expErr: errors.New("no quorum")}
	rt.SetIntentLog(log, time.Second)
	scanUntilLogic(t, rt)
	if err := rt.ApplySubstitute(replicated(clk, 4, "Level", 95, time.Minute)); err != nil {
		t.Fatal(err)
	}
	clk.advance(2 * time.Minute)
	rt.Scan()
	if l := rt.Substitutes(); len(l) != 0 || rt.Tags().Real("Mirror") != 10 {
		t.Fatalf("%+v, Mirror %v; want it expired on time without quorum", l, rt.Tags().Real("Mirror"))
	}
	if p := rt.PendingExpiries(); len(p) != 1 || p[0] != 4 {
		t.Fatalf("pending expiries %v; want 4, retried until the log takes it", p)
	}
	log.set(func(f *fakeLog) { f.expErr = nil })
	waitFor(t, "the expiry recorded", func() bool { p := log.proposed(); return len(p) == 1 && p[0] == 4 })
	waitFor(t, "the queue empty", func() bool { return len(rt.PendingExpiries()) == 0 })
}

// A failed catch-up raises __Substitute_Fault even with nothing retained
// (no store at all): substitutes the log holds may be missing. The
// background catch-up keeps trying while this replica holds the lease,
// even while its reads fail (it is not "fresh" then), and clears the
// fault once the log answers.
func TestCatchUpFaultWithNothingRetained(t *testing.T) {
	clk := noon()
	coord := lead(false)
	rt, d := newRetainSubRig(t, coord, nil, clk)
	rt.Scan()
	log := &fakeLog{syncErr: errors.New("no majority")}
	rt.SetIntentLog(log, 20*time.Millisecond)
	// The takeover's read waits until the background catch-up has made
	// its first try: at that moment the replica is not "fresh" yet, so a
	// retry that stopped on standby() instead of the lease would quit.
	var once sync.Once
	d.mu.Lock()
	d.onRead = func() {
		once.Do(func() {
			deadline := time.Now().Add(time.Second)
			for time.Now().Before(deadline) {
				log.mu.Lock()
				n := log.syncs
				log.mu.Unlock()
				if n >= 2 {
					return
				}
				time.Sleep(time.Millisecond)
			}
		})
	}
	d.mu.Unlock()
	coord.set(true)
	waitFor(t, "the takeover", func() bool { rt.Scan(); return rt.Tags().Bool(SubstituteFaultTag) })
	log.set(func(f *fakeLog) { f.syncErr = nil })
	waitFor(t, "the retry to clear the fault", func() bool { return !rt.Tags().Bool(SubstituteFaultTag) })
}

// The once-only rule on a request ID comes from the log: a retry applied
// after the first entry has ended still does not start another.
func TestRequestIDAppliesOnceEvenAfterTheEntryEnded(t *testing.T) {
	clk := noon()
	rt, _ := newRetainSubRig(t, lead(false), nil, clk)
	rt.Scan()
	rt.tags.setAny("Level", ir.RealVal(10))
	a := replicated(clk, 3, "Level", 95, time.Hour)
	a.RequestID = "r-7"
	if err := rt.ApplySubstitute(a); err != nil {
		t.Fatal(err)
	}
	rt.ApplyUnsubstitute(3, "op", "")
	retry := replicated(clk, 5, "Level", 95, time.Hour)
	retry.RequestID = "r-7"
	if err := rt.ApplySubstitute(retry); err != nil {
		t.Fatal(err)
	}
	if l := rt.Substitutes(); len(l) != 0 {
		t.Fatalf("%+v; a retry of an ended start started another", l)
	}
}

func (r *Runtime) retrying() bool {
	r.subMu.Lock()
	defer r.subMu.Unlock()
	return r.intent.retrying
}

func (r *Runtime) expiryLoopRunning() bool {
	r.intent.expMu.Lock()
	defer r.intent.expMu.Unlock()
	return r.intent.expRunning
}

// naut-lead's probe: a failed catch-up installs retained entry 5 (marked);
// the lease is lost before the background retry succeeds (the retry
// stops); the next takeover's catch-up succeeds. Entry 5, which the log
// never committed, must end there and the fault clear, not run on to its
// until.
func TestASuccessfulCatchUpReconcilesAnEarlierFallback(t *testing.T) {
	clk := noon()
	store := &fakeStore{state: retain.State{Substitutes: &retain.SubstituteState{
		Entries: []retain.SubstituteRetain{{ID: 5, Name: "Level", Value: 95.0, By: "op",
			StartMs: clk.Now().UnixMilli(), UntilMs: clk.Now().Add(time.Hour).UnixMilli(), GrantedMs: time.Hour.Milliseconds()}},
		HighID: 5,
	}}}
	coord := lead(false)
	rt, _ := newRetainSubRig(t, coord, store, clk)
	rt.Scan()
	log := &fakeLog{syncErr: errors.New("no majority")}
	rt.SetIntentLog(log, 20*time.Millisecond)
	coord.set(true)
	if m := scanUntilLogic(t, rt); m != 95 {
		t.Fatalf("first scan on %v; want the retained 95", m)
	}
	coord.set(false)
	rt.Scan()
	waitFor(t, "the background retry to stop on the lost lease", func() bool { return !rt.retrying() })
	log.set(func(f *fakeLog) { f.syncErr = nil }) // the log answers, without entry 5
	coord.set(true)
	waitFor(t, "the second takeover to reconcile", func() bool {
		rt.Scan()
		return len(rt.Substitutes()) == 0
	})
	if l := rt.Substitutes(); len(l) != 0 || rt.Tags().Real("Level") != 10 || rt.Tags().Bool(SubstituteFaultTag) {
		t.Fatalf("%+v, Level %v, fault %v; want entry 5 ended and the fault clear", l, rt.Tags().Real("Level"), rt.Tags().Bool(SubstituteFaultTag))
	}
}

// A request ID reused for a different start is refused, not answered with
// the first entry; one whose first entry was refused at apply stays free;
// and request IDs are forgotten after RequestIDWindow on the log's clock.
func TestRequestIDRules(t *testing.T) {
	clk := noon()
	rt, _ := newRetainSubRig(t, lead(false), nil, clk)
	rt.Scan()
	rt.tags.setAny("Level", ir.RealVal(10))
	a := replicated(clk, 3, "Level", 95, time.Hour)
	a.RequestID = "r-1"
	if err := rt.ApplySubstitute(a); err != nil {
		t.Fatal(err)
	}
	other := replicated(clk, 4, "Level", 96, time.Hour)
	other.RequestID = "r-1"
	if err := rt.ApplySubstitute(other); !errors.Is(err, ErrRequestReused) {
		t.Fatalf("r-1 for 96: %v; want ErrRequestReused", err)
	}
	if l := rt.Substitutes(); len(l) != 1 || l[0].ID != 3 || l[0].Value != 95.0 {
		t.Fatalf("%+v; want entry 3 (95) untouched", l)
	}
	bogus := replicated(clk, 5, "Nope", 1, time.Hour)
	bogus.RequestID = "r-2"
	_ = rt.ApplySubstitute(bogus) // refused at apply
	ok := replicated(clk, 6, "Flow", 7, time.Hour)
	ok.RequestID = "r-2"
	rt.tags.setAny("Flow", ir.RealVal(3))
	if err := rt.ApplySubstitute(ok); err != nil {
		t.Fatal(err)
	}
	if _, found := rt.SubstituteByRequest("r-2"); !found {
		t.Fatal("r-2 did not start: a request ID whose first entry was refused must stay free")
	}
	// Nine days on, on the log's clock: r-1 is forgotten.
	rt.ApplyUnsubstitute(3, "op", "")
	later := func(id uint64, req string, v float64) ReplicatedSubstitute {
		start := clk.Now().Add(9 * 24 * time.Hour)
		return ReplicatedSubstitute{ID: id, Name: "Level", Value: v, By: "op", RequestID: req,
			UntilMs: start.Add(time.Hour).UnixMilli(), GrantedMs: time.Hour.Milliseconds()}
	}
	if err := rt.ApplySubstitute(later(7, "r-1", 97)); err != nil {
		t.Fatalf("r-1 after the window: %v; want it free again", err)
	}
}

// The expiry retry stops when the lease is lost: the new leader owns
// expiry and records its own.
func TestExpiryRetryStopsOnLeaseLoss(t *testing.T) {
	clk := noon()
	coord := lead(true)
	rt, _ := newRetainSubRig(t, coord, nil, clk)
	log := &fakeLog{expErr: errors.New("no quorum")}
	rt.SetIntentLog(log, time.Second)
	scanUntilLogic(t, rt)
	if err := rt.ApplySubstitute(replicated(clk, 4, "Level", 95, time.Minute)); err != nil {
		t.Fatal(err)
	}
	clk.advance(2 * time.Minute)
	rt.Scan()
	if len(rt.PendingExpiries()) != 1 {
		t.Fatal("the expiry was not queued")
	}
	// After the loop's first (failed) try it sleeps; the lease goes then.
	waitFor(t, "the first try", func() bool { log.mu.Lock(); defer log.mu.Unlock(); return log.expTries >= 1 })
	coord.set(false)
	log.set(func(f *fakeLog) { f.expErr = nil })
	waitFor(t, "the expiry loop to stop", func() bool { return !rt.expiryLoopRunning() })
	if p := log.proposed(); len(p) != 0 || len(rt.PendingExpiries()) != 0 {
		t.Fatalf("proposed %v, pending %v; a replica that lost the lease must leave expiry to the new leader", p, rt.PendingExpiries())
	}
}
