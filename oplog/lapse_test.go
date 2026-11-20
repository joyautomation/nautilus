package oplog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A late start (#1030 review): a substitute start proposed while the log
// had no majority is stored on its proposer alone and answered "outcome
// unknown". If that proposer dies and comes back much later, the new
// leader's catch-up adopts it, and committing it would start the
// substitute then, on every replica. KindLapse names it instead, and every
// replica records it as lapsed.

const lapseBound = time.Minute

// lapseBench is n replicas with files, a movable leadership, a shared
// movable clock, per-replica network cuts, and what each applied.
type lapseBench struct {
	t       *testing.T
	dir     string
	urls    []string
	srv     []*httptest.Server
	nodes   []*Node
	down    []atomic.Bool
	push    []atomic.Bool // refuse entry-less replications (a lost commit push)
	m       *moving
	clock   atomic.Int64 // unix ms
	mu      sync.Mutex
	applied [][]Entry
}

func newLapseBench(t *testing.T, n int) *lapseBench {
	t.Helper()
	b := &lapseBench{t: t, dir: t.TempDir(), urls: make([]string, n), srv: make([]*httptest.Server, n),
		nodes: make([]*Node, n), down: make([]atomic.Bool, n), push: make([]atomic.Bool, n), m: &moving{}, applied: make([][]Entry, n)}
	b.clock.Store(time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC).UnixMilli())
	for i := range b.srv {
		b.srv[i] = httptest.NewUnstartedServer(nil)
		b.urls[i] = "http://" + b.srv[i].Listener.Addr().String()
	}
	b.m.set(b.urls[0], 1)
	for i := range b.srv {
		b.open(i)
		i := i
		b.srv[i].Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if b.down[i].Load() {
				http.Error(w, "down", http.StatusServiceUnavailable)
				return
			}
			pushGate(b.node(i).Handler(), &b.push[i]).ServeHTTP(w, r)
		})
		b.srv[i].Start()
	}
	t.Cleanup(func() {
		for i := range b.srv {
			b.srv[i].Close()
			b.node(i).Close()
		}
	})
	return b
}

func (b *lapseBench) node(i int) *Node { b.mu.Lock(); defer b.mu.Unlock(); return b.nodes[i] }

// open (re)opens replica i from its file, replaying it.
func (b *lapseBench) open(i int) {
	b.t.Helper()
	b.mu.Lock()
	b.applied[i] = nil
	b.mu.Unlock()
	n, err := New(Options{Self: b.urls[i], Peers: b.urls, Leadership: view{self: b.urls[i], m: b.m}, Token: "t",
		File: filepath.Join(b.dir, string(rune('a'+i))), Timeout: 500 * time.Millisecond,
		Now: func() time.Time { return time.UnixMilli(b.clock.Load()) },
		Lapse: func(e Entry, now time.Time) bool {
			return e.Kind == KindSubstitute && now.Sub(time.UnixMilli(e.TS)) > lapseBound
		},
		Apply: func(e Entry) error {
			b.mu.Lock()
			b.applied[i] = append(b.applied[i], e)
			b.mu.Unlock()
			return nil
		}})
	if err != nil {
		b.t.Fatal(err)
	}
	b.mu.Lock()
	b.nodes[i] = n
	b.mu.Unlock()
}

func (b *lapseBench) wait(d time.Duration) { b.clock.Add(d.Milliseconds()) }

// start is how replica i applied the substitute entry for tag: "" (not at
// all), "started" or "lapsed"; and whether a KindLapse naming it came.
func (b *lapseBench) start(i int, tag string) (how string, lapseSeen bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var seq uint64
	for _, e := range b.applied[i] {
		switch {
		case e.Kind == KindSubstitute && e.Tag == tag:
			seq, how = e.Seq, "started"
			if e.Lapsed {
				how = "lapsed"
			}
		case e.Kind == KindLapse && seq != 0 && slices.Contains(e.Refs, seq):
			lapseSeen = true
		}
	}
	return how, lapseSeen
}

// unknownStart has replica 0 (the leader, term 1) store a start of tag
// while replica dead is down: it misses its majority. Then 0 dies.
func (b *lapseBench) unknownStart(tag string, dead int) {
	b.t.Helper()
	ctx := context.Background()
	if _, err := b.node(0).Propose(ctx, Entry{Kind: KindAck, IDs: []string{"warm"}}); err != nil {
		b.t.Fatal(err) // term 1 synced while all were up
	}
	b.down[dead].Store(true)
	if _, err := b.node(0).Propose(ctx, Entry{Kind: KindSubstitute, Tag: tag}); err == nil {
		b.t.Fatal("the start committed without its majority")
	}
	b.down[dead].Store(false)
	b.down[0].Store(true)
}

// The probe's case: A's start is unknown, A dies, B leads unsynced (no
// majority without A) and refuses writes; A comes back two hours later.
// B's catch-up adopts the start; it is committed as lapsed on both, never
// started (lapse_ignored turns this red).
func TestLateUnknownStartLapses(t *testing.T) {
	b := newLapseBench(t, 2)
	ctx := context.Background()
	b.unknownStart("Level", 1)
	b.m.set(b.urls[1], 2)
	if err := b.node(1).SyncLeader(ctx); err == nil {
		t.Fatal("B synced without A")
	}
	b.wait(2 * time.Hour)
	b.down[0].Store(false)
	if err := b.node(1).SyncLeader(ctx); err != nil {
		t.Fatal(err)
	}
	b.node(1).Heartbeat(ctx)
	for i, n := range []string{"A", "B"} {
		if how, lapse := b.start(i, "Level"); how != "lapsed" || !lapse {
			t.Errorf("%s applied the late start as %q (KindLapse seen %v); want lapsed", n, how, lapse)
		}
	}
	// Replay agrees: B restarted from its file, A from an empty file.
	b.node(1).Close()
	b.open(1)
	if how, _ := b.start(1, "Level"); how != "lapsed" {
		t.Errorf("B's replay applied it as %q", how)
	}
	// A restarted leader is a new takeover: it catches up before A may
	// take anything from it (#1050 review).
	if err := b.node(1).SyncLeader(ctx); err != nil {
		t.Fatal(err)
	}
	b.node(0).Close()
	os.Remove(filepath.Join(b.dir, "a"))
	b.open(0)
	if err := b.node(0).CatchUp(ctx); err != nil {
		t.Fatal(err)
	}
	if how, _ := b.start(0, "Level"); how != "lapsed" {
		t.Errorf("A caught up from an empty file and applied it as %q", how)
	}
}

// Within the bound, a leftover start is carried out as before: A comes
// back 10 s later.
func TestLeftoverStartWithinTheBoundStarts(t *testing.T) {
	b := newLapseBench(t, 2)
	ctx := context.Background()
	b.unknownStart("Level", 1)
	b.m.set(b.urls[1], 2)
	b.wait(10 * time.Second)
	b.down[0].Store(false)
	if err := b.node(1).SyncLeader(ctx); err != nil {
		t.Fatal(err)
	}
	b.node(1).Heartbeat(ctx)
	for i, n := range []string{"A", "B"} {
		if how, _ := b.start(i, "Level"); how != "started" {
			t.Errorf("%s applied a leftover inside the bound as %q; want started", n, how)
		}
	}
}

// The #1032 review's case, a regression test: B's catch-up adopts the
// stale start, and its leadership ends before it appends its KindLapse.
// The next leader's catch-up now finds the start on both replicas; it is
// still lapsed (by age alone: copies prove nothing about a commit).
func TestAdoptedCopyStillLapses(t *testing.T) {
	b := newLapseBench(t, 2)
	ctx := context.Background()
	b.unknownStart("Level", 1)
	b.m.set(b.urls[1], 2)
	b.wait(2 * time.Hour)
	b.down[0].Store(false)
	nb := b.node(1)
	nb.seqMu.Lock()
	if err := nb.leaderCatchUp(ctx, 2); err != nil { // B adopts the start ...
		nb.seqMu.Unlock()
		t.Fatal(err)
	}
	nb.seqMu.Unlock()
	b.m.set(b.urls[0], 3) // ... and loses the lease before its carrier
	if err := b.node(0).SyncLeader(ctx); err != nil {
		t.Fatal(err)
	}
	b.node(0).Heartbeat(ctx)
	for i, n := range []string{"A", "B"} {
		if how, _ := b.start(i, "Level"); how != "lapsed" {
			t.Errorf("%s applied the 2-hour-old unknown start as %q; want lapsed", n, how)
		}
	}
}

// The accepted cost (#1032 review): a start that WAS committed (stored on
// a majority, answered 200) whose commit push was lost, caught up by a new
// leader more than the bound later, lapses too: copies can't prove the
// commit. The replica that started it ends it loudly (the runtime's
// ApplyLapse); the others record it lapsed.
func TestCommittedStartCaughtUpLateLapses(t *testing.T) {
	b := newLapseBench(t, 3)
	ctx := context.Background()
	if _, err := b.node(0).Propose(ctx, Entry{Kind: KindAck, IDs: []string{"warm"}}); err != nil {
		t.Fatal(err)
	}
	b.push[1].Store(true)
	b.push[2].Store(true) // the commit push is lost: B and C hold it uncommitted
	if _, err := b.node(0).Propose(ctx, Entry{Kind: KindSubstitute, Tag: "Level"}); err != nil {
		t.Fatal(err)
	}
	b.push[1].Store(false)
	b.push[2].Store(false)
	b.down[0].Store(true)
	b.wait(2 * time.Hour)
	b.m.set(b.urls[1], 2)
	if err := b.node(1).SyncLeader(ctx); err != nil {
		t.Fatal(err)
	}
	if how, lapse := b.start(1, "Level"); how != "lapsed" || !lapse {
		t.Fatalf("B applied it as %q (KindLapse naming it: %v); want lapsed", how, lapse)
	}
	b.down[0].Store(false)
	b.node(1).Heartbeat(ctx)
	if how, lapse := b.start(0, "Level"); how != "started" || !lapse {
		t.Fatalf("A: %q, KindLapse naming it %v; want A's start, then the KindLapse that ends it", how, lapse)
	}
}

// The commit was known only outside the majority the new leader heard: A
// stored the start on A and C (a majority), committed it, carried it out
// and answered 200, but the push to C was lost and B never stored it. B
// leads, hears B and C only (the start on one of them): it lapses it. A
// had already started it, so the KindLapse reaches A's Apply naming it,
// for A to end it (the runtime journals that loudly); B and C record it
// lapsed. All three end without it running.
func TestLapseReachesAReplicaThatStartedIt(t *testing.T) {
	b := newLapseBench(t, 3)
	ctx := context.Background()
	if _, err := b.node(0).Propose(ctx, Entry{Kind: KindAck, IDs: []string{"warm"}}); err != nil {
		t.Fatal(err)
	}
	b.down[1].Store(true)
	b.push[2].Store(true)
	if _, err := b.node(0).Propose(ctx, Entry{Kind: KindSubstitute, Tag: "Level"}); err != nil {
		t.Fatal(err)
	}
	if how, _ := b.start(0, "Level"); how != "started" {
		t.Fatalf("A applied its committed start as %q", how)
	}
	b.push[2].Store(false)
	b.down[1].Store(false)
	b.down[0].Store(true)
	b.wait(2 * time.Hour)
	b.m.set(b.urls[1], 2)
	if err := b.node(1).SyncLeader(ctx); err != nil {
		t.Fatal(err)
	}
	b.node(1).Heartbeat(ctx)
	for i, n := range []string{"B", "C"} {
		if how, _ := b.start(i+1, "Level"); how != "lapsed" {
			t.Errorf("%s applied it as %q; want lapsed", n, how)
		}
	}
	b.down[0].Store(false)
	b.node(1).Heartbeat(ctx)
	if how, lapse := b.start(0, "Level"); how != "started" || !lapse {
		t.Fatalf("A: %q, KindLapse naming it %v; want the KindLapse to reach A's Apply after its start", how, lapse)
	}
}
