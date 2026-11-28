//go:build !js

package oplog

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The compaction decision's seam (#1455 PR 2b-1, compact_decide.go): the
// gate, T, Through, the trigger, blocked-since, the re-check at
// sequencing, the shrink, and Options.Base. The keep decision here is the
// toy rule (toyKeep); the server's is tested in package server.

// clock is the trio's shared fake clock; skew offsets one copy's.
type clock struct {
	ms   atomic.Int64
	skew [3]atomic.Int64
}

func (c *clock) now(i int) func() time.Time {
	return func() time.Time { return time.UnixMilli(c.ms.Load() + c.skew[i].Load()) }
}

func (c *clock) advance(d time.Duration) { c.ms.Add(d.Milliseconds()) }

const day = 24 * time.Hour

// decideLims are small thresholds with the real margin, gap and spread.
var decideLims = decideLimits{every: time.Hour, minEntries: 20, heard: time.Hour}

// toyCompactor is the toy keep rule as a Compactor.
func toyCompactor(_ context.Context, in CompactInput) ([]uint64, error) {
	var es []Entry
	in.Entries(func(e Entry) bool { es = append(es, e); return true })
	return toyKeep(es, in.Through, 0), nil
}

// dtrio is a ctrio whose copies decide (Compact set), on a fake clock.
type dtrio struct {
	*ctrio
	clk *clock
}

func decideTrio(t *testing.T, opts func(i int, o *Options)) *dtrio {
	t.Helper()
	clk := &clock{}
	clk.ms.Store(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC).UnixMilli())
	tr := compactTrio(t, func(i int, o *Options) {
		o.Now = clk.now(i)
		o.decide = decideLims
		o.Compact = toyCompactor
		if opts != nil {
			opts(i, o)
		}
	})
	return &dtrio{ctrio: tr, clk: clk}
}

// fillOver proposes n entries at replica i, an hour apart.
func (tr *dtrio) fillOver(i, n int, seed uint64) {
	tr.t.Helper()
	for k := range n {
		tr.clk.advance(time.Hour)
		tr.fill(i, 1, seed*1000+uint64(k))
	}
}

// beat heartbeats from replica i twice: every copy's word (its clock, its
// commit) is fresh at the leader.
func (tr *dtrio) beat(i int) {
	tr.nodes[i].Heartbeat(tr.ctx())
	tr.nodes[i].Heartbeat(tr.ctx())
}

// ready is a log the leader n0 wants to compact: 60 entries over 60 hours,
// then 8 days on, a tail of 3, and fresh words from every copy.
func (tr *dtrio) ready() {
	tr.t.Helper()
	if err := tr.nodes[0].SyncLeader(tr.ctx()); err != nil {
		tr.t.Fatal(err)
	}
	tr.nodes[0].markRules(tr.ctx(), nil) // the first look's marker, at the start
	tr.fillOver(0, 60, 1)
	tr.clk.advance(8 * day)
	tr.fillOver(0, 3, 2)
	tr.beat(0)
}

// TestTheLeaderCompactsOnceTheGateHolds: with every copy heard, synced and
// on the same rules, the leader's look proposes the toy keep list up to the
// newest entry older than the margin by T, and every copy carries it out.
// Writes go on meanwhile (the decision reads committed entries outside the
// lock: run under -race).
func TestTheLeaderCompactsOnceTheGateHolds(t *testing.T) {
	tr := decideTrio(t, nil)
	tr.ready()
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for k := 0; ; k++ {
			select {
			case <-stop:
				return
			default:
			}
			tr.nodes[0].Propose(context.Background(), toyEntry("w", "set", k))
		}
	}()
	got := tr.nodes[0].decideForTest(tr.ctx())
	close(stop)
	wg.Wait()
	if got != decideProposed {
		t.Fatalf("outcome %d, status %+v", got, tr.nodes[0].Status())
	}
	tr.record(0)
	tr.beat(0)
	tr.settle()
	st := tr.nodes[0].Status()
	if st.CompactThrough == 0 || st.CompactBlocked != "" {
		t.Fatalf("status after: %+v", st)
	}
	cut := tr.clk.ms.Load() - 8*day.Milliseconds()
	for _, e := range tr.full() {
		if e.Seq == st.CompactThrough && e.TS > cut {
			t.Fatalf("Through %d has TS %d, inside the margin (%d)", e.Seq, e.TS, cut)
		}
	}
	keep := toyKeep(tr.full(), st.CompactThrough, 0)
	for i := range 3 {
		tr.check(i, tr.expect(st.CompactThrough, keep))
	}
	// Too soon after the last: nothing more is wanted.
	tr.beat(0)
	if got := tr.nodes[0].decideForTest(tr.ctx()); got != decideNotWanted {
		t.Fatalf("a second look at once: outcome %d", got)
	}
}

// TestTheLoopCompactsByItself: the decision loop (every look) proposes
// without a test calling it.
func TestTheLoopCompactsByItself(t *testing.T) {
	tr := decideTrio(t, func(i int, o *Options) { o.decide.every = 20 * time.Millisecond })
	tr.ready()
	deadline := time.Now().Add(10 * time.Second)
	for tr.nodes[0].Status().CompactThrough == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("the loop never compacted: %+v", tr.nodes[0].Status())
		}
		tr.beat(0)
		time.Sleep(10 * time.Millisecond)
	}
}

// TestEachGateReasonBlocks: each condition of §3 that does not hold holds
// compaction back and says why, from when (Status).
func TestEachGateReasonBlocks(t *testing.T) {
	var uncounted atomic.Bool
	for _, c := range []struct {
		name, why string
		opts      func(i int, o *Options)
		before    func(tr *dtrio)
	}{
		{name: "a copy not heard", why: "waiting for http://n2: not heard from",
			before: func(tr *dtrio) { tr.setDown(2, true); tr.clk.advance(2 * time.Hour); tr.beat(0) }},
		{name: "clocks apart", why: "clocks disagree",
			before: func(tr *dtrio) { tr.clk.skew[2].Store((10 * time.Minute).Milliseconds()); tr.beat(0) }},
		{name: "an old build", why: "runs a build from before log compaction",
			opts: func(i int, o *Options) {
				if i == 2 {
					o.CompactRules = 0
				}
			}},
		{name: "newer rules", why: "runs newer keep rules (2)",
			opts: func(i int, o *Options) {
				if i == 2 {
					o.CompactRules = 2
				}
			}},
		{name: "a copy that does not count", why: "its log is not synced, or does not count",
			opts: func(i int, o *Options) {
				o.Counts = func(peer, _ string) bool { return peer != "http://n2" || !uncounted.Load() }
			},
			before: func(tr *dtrio) { uncounted.Store(true); tr.beat(0) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			tr := decideTrio(t, c.opts)
			tr.ready()
			if c.before != nil {
				c.before(tr)
			}
			if got := tr.nodes[0].decideForTest(tr.ctx()); got != decideBlocked {
				t.Fatalf("outcome %d, status %+v", got, tr.nodes[0].Status())
			}
			st := tr.nodes[0].Status()
			if !strings.Contains(st.CompactBlocked, c.why) || st.CompactBlockedSince == 0 || st.CompactThrough != 0 {
				t.Fatalf("status %q since %d through %d, want a block naming %q", st.CompactBlocked, st.CompactBlockedSince, st.CompactThrough, c.why)
			}
		})
	}
}

// TestBlockedSinceIsTheLogsNotTheProcesss (the lead's named test, #1455
// 2b): how long a wanted compaction has waited comes from the log's own
// timestamps, so a restart of the leader does not reset the 7 days, and
// the other controller, leading, says the same.
func TestBlockedSinceIsTheLogsNotTheProcesss(t *testing.T) {
	tr := decideTrio(t, nil)
	tr.ready()
	tr.setDown(2, true)
	tr.clk.advance(2 * time.Hour)
	tr.beat(0)
	if got := tr.nodes[0].decideForTest(tr.ctx()); got != decideBlocked {
		t.Fatalf("outcome %d", got)
	}
	since := tr.nodes[0].Status().CompactBlockedSince
	if since == 0 || since >= tr.clk.ms.Load() {
		t.Fatalf("since %d, now %d: want a time from the log, before now", since, tr.clk.ms.Load())
	}
	// A day later, the leader restarts: the same since.
	tr.clk.advance(day)
	tr.restart(0)
	if err := tr.nodes[0].SyncLeader(tr.ctx()); err != nil {
		t.Fatal(err)
	}
	tr.beat(0)
	tr.nodes[0].decideForTest(tr.ctx())
	if got := tr.nodes[0].Status().CompactBlockedSince; got != since {
		t.Fatalf("after a restart, since %d, before it %d: the wait restarted", got, since)
	}
	// The other controller leads: it says the same, and n0 no longer does.
	tr.lead(1)
	if err := tr.nodes[1].SyncLeader(tr.ctx()); err != nil {
		t.Fatal(err)
	}
	tr.beat(1)
	tr.nodes[1].decideForTest(tr.ctx())
	if got := tr.nodes[1].Status().CompactBlockedSince; got != since {
		t.Fatalf("the other controller leading: since %d, want %d", got, since)
	}
	if got := tr.nodes[0].decideForTest(tr.ctx()); got != decideNotLeader || tr.nodes[0].Status().CompactBlocked != "" {
		t.Fatalf("the old leader still says it waits: %d %q", got, tr.nodes[0].Status().CompactBlocked)
	}

	// Two leaders in a row may each mark their term (neither saw the
	// other's committed): the earliest marker counts, whatever their order.
	t.Run("the earliest of two markers", func(t *testing.T) {
		mark := func(seq uint64, ts int64) stored {
			return storedOf(Entry{Seq: seq, Term: seq, TS: ts, Kind: KindNoop, By: "system", Value: []byte(`{"rules":1}`)})
		}
		snap := []stored{mark(1, 5000), mark(2, 3000), storedOf(Entry{Seq: 3, Term: 3, TS: 4000, Kind: KindAck})}
		if ts, ok := firstRulesTerm(snap, nil); !ok || ts != 3000 {
			t.Fatalf("first rules term %d %t, want the earlier marker's 3000", ts, ok)
		}
	})
	// Only a committed marker counts: a leader that stored one and could
	// not commit it (cut off) says nothing; the next leader's counts.
	t.Run("an uncommitted marker", func(t *testing.T) {
		tr := decideTrio(t, nil)
		// Stored, never committed: a write that missed its majority.
		n0 := tr.nodes[0]
		n0.mu.Lock()
		if _, err := n0.acceptLocked([]Entry{{Seq: 1, Term: 1, TS: tr.clk.ms.Load(), Kind: KindNoop, By: "system", Value: []byte(`{"rules":1}`)}}); err != nil {
			t.Fatal(err)
		}
		stored, snap := n0.lastLocked(), n0.entries[:n0.indexAfterLocked(n0.commit)]
		n0.mu.Unlock()
		if stored != 1 {
			t.Fatal("the marker was not stored")
		}
		if _, ok := firstRulesTerm(snap, nil); ok {
			t.Fatal("an uncommitted marker counts")
		}
		if !n0.needsRulesMark(nil) {
			t.Fatal("an uncommitted marker is taken for a committed one")
		}
		tr.clk.advance(time.Hour)
		tr.setDown(0, true)
		tr.setDown(1, false)
		tr.setDown(2, false)
		tr.lead(1)
		if err := tr.nodes[1].SyncLeader(tr.ctx()); err != nil {
			t.Fatal(err)
		}
		tr.nodes[1].decideForTest(tr.ctx())
		es := tr.nodes[1].After(0)
		if len(es) != 1 || string(es[0].Value) != `{"rules":1}` || es[0].TS != tr.clk.ms.Load() {
			t.Fatalf("the next leader's log: %+v", es)
		}
	})
}

// TestARollingUpgradeUnderSevenDaysRaisesNothing (the lead's second named
// test): a log grown long before compaction was switched on counts its
// wait from the first term a leader on compaction's rules began (its
// carrier says so), so while one copy is still on an old build, the
// compaction is blocked (Status says so) but not for 7 days yet.
func TestARollingUpgradeUnderSevenDaysRaisesNothing(t *testing.T) {
	var rules [3]atomic.Int64
	tr := decideTrio(t, func(i int, o *Options) { o.CompactRules = int(rules[i].Load()) })
	// Months of log on builds from before compaction.
	if err := tr.nodes[0].SyncLeader(tr.ctx()); err != nil {
		t.Fatal(err)
	}
	tr.fillOver(0, 60, 1)
	tr.clk.advance(90 * day)
	tr.fillOver(0, 3, 2)
	// n0 and n1 updated (rules 1), the witness not yet.
	rules[0].Store(1)
	rules[1].Store(1)
	tr.restart(0)
	tr.restart(1)
	tr.lead(0)
	if err := tr.nodes[0].SyncLeader(tr.ctx()); err != nil {
		t.Fatal(err)
	}
	tr.nodes[0].markRules(tr.ctx(), nil)
	marked := tr.clk.ms.Load()
	tr.clk.advance(2 * day)
	tr.beat(0)
	if got := tr.nodes[0].decideForTest(tr.ctx()); got != decideBlocked {
		t.Fatalf("outcome %d", got)
	}
	st := tr.nodes[0].Status()
	if !strings.Contains(st.CompactBlocked, "before log compaction") {
		t.Fatalf("the reason %q does not name the old build", st.CompactBlocked)
	}
	if want := marked + (7 * day).Milliseconds(); st.CompactBlockedSince < want {
		t.Fatalf("since %d, before the switch-on's term plus the margin (%d): the upgrade raises the condition at once", st.CompactBlockedSince, want)
	}
	if left := st.CompactBlockedSince - tr.clk.ms.Load(); left <= 0 {
		t.Fatalf("since is %d ms in the past two days after the switch-on", -left)
	}
}

// TestATransientReasonStartsNoWait: a new leader not caught up for its
// term yet (a takeover) makes a look wait, but neither starts nor clears
// the blocked time; a write in flight is the sequencing's to refuse.
func TestATransientReasonStartsNoWait(t *testing.T) {
	tr := decideTrio(t, nil)
	tr.ready()
	tr.lead(1)
	tr.beat(1)
	if got := tr.nodes[1].decideForTest(tr.ctx()); got != decideWaiting {
		t.Fatalf("outcome %d, status %+v", got, tr.nodes[1].Status())
	}
	if st := tr.nodes[1].Status(); st.CompactBlocked != "" {
		t.Fatalf("a takeover catching up started the blocked time: %q", st.CompactBlocked)
	}
}

// TestThroughIsCommittedEverywhere: Through never passes what any copy
// last said it has committed.
func TestThroughIsCommittedEverywhere(t *testing.T) {
	tr := decideTrio(t, nil)
	tr.ready()
	n := tr.nodes[0]
	n.mu.Lock()
	pw := n.peerWire["http://n2"]
	pw.commit = 30
	n.peerWire["http://n2"] = pw
	n.mu.Unlock()
	if got := n.decideForTest(tr.ctx()); got != decideProposed {
		t.Fatalf("outcome %d", got)
	}
	if got := n.Status().CompactThrough; got == 0 || got > 30 {
		t.Fatalf("compacted through %d, past the witness's commit 30", got)
	}
}

// TestADecisionTheLogMovedUnderIsDiscarded: a decision whose term changed
// and came back, or whose base moved, while it was being made, is thrown
// away: nothing is proposed.
func TestADecisionTheLogMovedUnderIsDiscarded(t *testing.T) {
	for _, c := range []struct {
		name string
		move func(tr *dtrio)
	}{
		{"the term moved and came back", func(tr *dtrio) {
			tr.lead(1)
			tr.lead(0)
			if err := tr.nodes[0].SyncLeader(context.Background()); err != nil {
				tr.t.Error(err)
			}
		}},
		{"a copy went silent", func(tr *dtrio) {
			tr.setDown(2, true)
			tr.clk.advance(2 * time.Hour)
		}},
		{"the base moved", func(tr *dtrio) {
			es := tr.nodes[0].After(0)
			through := es[len(es)/4].Seq
			if _, err := tr.nodes[0].proposeCompact(context.Background(), through, toyKeep(es, through, 0)); err != nil {
				tr.t.Error(err)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			var tr *dtrio
			var once sync.Once
			tr = decideTrio(t, func(i int, o *Options) {
				o.Compact = func(ctx context.Context, in CompactInput) ([]uint64, error) {
					once.Do(func() { c.move(tr) })
					return toyCompactor(ctx, in)
				}
			})
			tr.ready()
			ks := func() int {
				k := 0
				for _, e := range tr.nodes[0].After(0) {
					if e.Kind == KindCompact {
						k++
					}
				}
				return k
			}
			if got := tr.nodes[0].decideForTest(tr.ctx()); got != decideDiscarded {
				t.Fatalf("outcome %d, status %+v", got, tr.nodes[0].Status())
			}
			own := 0
			if c.name == "the base moved" {
				own = 1 // the one the case itself proposed
			}
			if got := ks(); got != own {
				t.Fatalf("%d compactions in the log, want %d: the discarded decision was proposed", got, own)
			}
		})
	}
}

// TestAnOverSizeDecisionCompactsASmallerRange: a K over the entry cap (a
// year's first compaction) is retried on a smaller range, then the rest
// at the next look.
func TestAnOverSizeDecisionCompactsASmallerRange(t *testing.T) {
	tr := decideTrio(t, func(i int, o *Options) {
		o.decide.maxK = 450
		o.Compact = func(_ context.Context, in CompactInput) ([]uint64, error) {
			var keep []uint64 // every other entry: many runs, a long K
			in.Entries(func(e Entry) bool {
				if e.Seq <= in.Through && e.Seq%2 == 0 {
					keep = append(keep, e.Seq)
				}
				return true
			})
			return keep, nil
		}
	})
	tr.ready()
	if got := tr.nodes[0].decideForTest(tr.ctx()); got != decideProposed {
		t.Fatalf("outcome %d, status %+v", got, tr.nodes[0].Status())
	}
	first := tr.nodes[0].Status().CompactThrough
	var want uint64
	for _, e := range tr.nodes[0].After(0) {
		if e.TS <= tr.clk.ms.Load()-(8*day).Milliseconds() {
			want = e.Seq
		}
	}
	if first == 0 || first >= want {
		t.Fatalf("compacted through %d, the whole range is to %d: not smaller", first, want)
	}
}

// TestARefusedDecisionIsReported: the keep decision's own check refusing
// (a bug) proposes nothing and says why, for oplog-compaction-refused,
// until a decision passes or this copy stops leading.
func TestARefusedDecisionIsReported(t *testing.T) {
	refuse := atomic.Bool{}
	refuse.Store(true)
	tr := decideTrio(t, func(i int, o *Options) {
		o.Compact = func(ctx context.Context, in CompactInput) ([]uint64, error) {
			if refuse.Load() {
				return nil, fmt.Errorf("%w: alarms: the digests differ", ErrCompactRefused)
			}
			return toyCompactor(ctx, in)
		}
	})
	tr.ready()
	if got := tr.nodes[0].decideForTest(tr.ctx()); got != decideRefused {
		t.Fatalf("outcome %d", got)
	}
	if st := tr.nodes[0].Status(); !strings.Contains(st.CompactDecisionRefused, "alarms: the digests differ") || st.CompactThrough != 0 {
		t.Fatalf("status %+v", st)
	}
	refuse.Store(false)
	if got := tr.nodes[0].decideForTest(tr.ctx()); got != decideProposed || tr.nodes[0].Status().CompactDecisionRefused != "" {
		t.Fatalf("a passing decision: %d, refused %q", got, tr.nodes[0].Status().CompactDecisionRefused)
	}
}

// TestBaseIsToldBeforeACompactedReplay: a copy that replays a compacted
// log, at its start or when an install lands, tells Options.Base the
// base's Through before the first entry it applies.
func TestBaseIsToldBeforeACompactedReplay(t *testing.T) {
	var mu sync.Mutex
	var events [3][]string
	tr := decideTrio(t, func(i int, o *Options) {
		o.Base = func(through uint64) {
			mu.Lock()
			events[i] = append(events[i], fmt.Sprintf("base %d", through))
			mu.Unlock()
		}
		apply := o.Apply
		o.Apply = func(e Entry) error {
			mu.Lock()
			events[i] = append(events[i], "apply")
			mu.Unlock()
			return apply(e)
		}
	})
	tr.ready()
	if got := tr.nodes[0].decideForTest(tr.ctx()); got != decideProposed {
		t.Fatalf("outcome %d", got)
	}
	tr.beat(0)
	tr.settle()
	through := tr.nodes[0].Status().CompactThrough
	want := fmt.Sprintf("base %d", through)
	first := func(i int) []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(events[i])
	}
	mu.Lock()
	events[1], events[2] = nil, nil
	mu.Unlock()
	tr.restart(1)
	if ev := first(1); len(ev) == 0 || ev[0] != want {
		t.Fatalf("a restart over the compacted file: %v, want %q first", ev, want)
	}
	tr.wipe(2)
	mu.Lock()
	events[2] = nil
	mu.Unlock()
	tr.noPull(2, true)
	tr.heartbeatUntil(0, 20, through)
	if ev := first(2); len(ev) == 0 || ev[0] != want {
		t.Fatalf("an install: %v, want %q first", ev, want)
	}
}

// TestALeaderOnTheRulesMarksItsTerm: a leader with a rules version names
// it in its term's first entry, and one without names nothing (its log is
// as a build from before's).
func TestALeaderOnTheRulesMarksItsTerm(t *testing.T) {
	for _, rules := range []int{0, 1} {
		tr := decideTrio(t, func(i int, o *Options) { o.CompactRules = rules })
		if err := tr.nodes[0].SyncLeader(tr.ctx()); err != nil {
			t.Fatal(err)
		}
		if es := tr.nodes[0].After(0); len(es) != 0 {
			t.Fatalf("rules %d: a takeover with nothing to commit wrote %+v (the marker is the decision loop's)", rules, es)
		}
		tr.nodes[0].decideForTest(tr.ctx())
		es := tr.nodes[0].After(0)
		if rules == 0 {
			if len(es) != 0 {
				t.Fatalf("rules 0: a look wrote %+v", es)
			}
			continue
		}
		if len(es) != 1 || es[0].Kind != KindNoop || string(es[0].Value) != `{"rules":1}` {
			t.Fatalf("rules 1: after a look %+v, want one noop naming the rules", es)
		}
		// Once: the next leader's look writes no other.
		tr.lead(1)
		if err := tr.nodes[1].SyncLeader(tr.ctx()); err != nil {
			t.Fatal(err)
		}
		tr.nodes[1].decideForTest(tr.ctx())
		if got := tr.nodes[1].After(0); len(got) != 1 {
			t.Fatalf("a second term marked again: %+v", got)
		}
	}
}

// TestTheRulesMarkerNamesWhatMustLapse: a marker written while an earlier
// term's entry sits past the commit index commits it, so it is the term's
// carrier: a KindLapse naming what must lapse, never a bare noop.
func TestTheRulesMarkerNamesWhatMustLapse(t *testing.T) {
	tr := decideTrio(t, func(i int, o *Options) {
		o.Lapse = func(e Entry, _ time.Time) bool { return e.Kind == kindToy }
	})
	if err := tr.nodes[0].SyncLeader(tr.ctx()); err != nil {
		t.Fatal(err)
	}
	tr.lead(0) // a new term, caught up, with an older term's entry left over
	if err := tr.nodes[0].SyncLeader(tr.ctx()); err != nil {
		t.Fatal(err)
	}
	n := tr.nodes[0]
	n.mu.Lock()
	stale := toyEntry("k", "set", 1)
	stale.Seq, stale.Term, stale.TS = n.lastLocked()+1, 1, tr.clk.ms.Load()
	if _, err := n.acceptLocked([]Entry{stale}); err != nil {
		n.mu.Unlock()
		t.Fatal(err)
	}
	n.mu.Unlock()
	n.markRules(tr.ctx(), nil)
	es := n.After(0)
	last := es[len(es)-1]
	if last.Kind != KindLapse || !slices.Contains(last.Refs, stale.Seq) || string(last.Value) != `{"rules":1}` {
		t.Fatalf("the marker %+v, want a KindLapse naming %d with the rules", last, stale.Seq)
	}
}
