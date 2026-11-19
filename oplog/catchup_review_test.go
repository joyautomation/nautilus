package oplog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// The three holes #1012's owner review found (naut-redundancy's probes,
// kept as tests): a new leader's own stale suffix, a follower's stale
// entry, and a write numbered after a failed catch-up.

// switchable is movingThree with each node's handler switchable: down
// (every request fails) or noGet (its log reads fail, its writes don't).
type switchable struct {
	*bench
	m           *moving
	down, noGet [3]atomic.Bool
}

func switchableThree(t *testing.T) *switchable {
	t.Helper()
	sw := &switchable{bench: &bench{applied: make([][]Entry, 3), urls: make([]string, 3)}, m: &moving{}}
	b := sw.bench
	for i := range b.urls {
		s := httptest.NewUnstartedServer(nil)
		b.servers = append(b.servers, s)
		b.urls[i] = "http://" + s.Listener.Addr().String()
	}
	sw.m.set(b.urls[0], 1)
	for i := range b.urls {
		b.nodes = append(b.nodes, b.node(t, i, "", view{self: b.urls[i], m: sw.m}))
		h := b.nodes[i].Handler()
		i := i
		b.servers[i].Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if sw.down[i].Load() || sw.noGet[i].Load() && r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/oplog") {
				http.Error(w, "down", http.StatusServiceUnavailable)
				return
			}
			h.ServeHTTP(w, r)
		})
		b.servers[i].Start()
	}
	return sw
}

func (b *bench) appliedIDs(i int) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for _, e := range b.applied[i] {
		out = append(out, e.IDs...)
	}
	return out
}

// storeOnly puts an entry in one node's log as if its leader had stored
// it and died before replicating it.
func storeOnly(t *testing.T, n *Node, e Entry) {
	t.Helper()
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, err := n.acceptLocked([]Entry{e}); err != nil {
		t.Fatal(err)
	}
}

// (1) A leader whose own uncommitted entry at seq 1 is stale: B and W
// committed another seq 1 meanwhile. Its catch-up must replace the stale
// one, never commit and apply it.
func TestSyncLeaderReplacesItsOwnStaleSuffix(t *testing.T) {
	sw := switchableThree(t)
	defer sw.close()
	ctx := context.Background()
	storeOnly(t, sw.nodes[0], Entry{Seq: 1, Term: 1, Kind: KindAck, IDs: []string{"STALE"}, By: "op"})
	sw.down[0].Store(true)
	sw.m.set(sw.urls[1], 2)
	if _, err := sw.nodes[1].Propose(ctx, Entry{Kind: KindAck, IDs: []string{"COMMITTED"}, By: "op"}); err != nil {
		t.Fatal(err)
	}
	sw.nodes[1].Heartbeat(ctx)
	sw.down[0].Store(false)
	sw.m.set(sw.urls[0], 3)
	if err := sw.nodes[0].SyncLeader(ctx); err != nil {
		t.Fatalf("SyncLeader: %v", err)
	}
	if got := sw.appliedIDs(0); len(got) != 1 || got[0] != "COMMITTED" {
		t.Fatalf("A applied %v; want only the committed seq 1, never its stale one", got)
	}
}

// (1b) A follower holding a stale entry the leader never had: the
// leader's replication must replace it before the commit index reaches
// it, so it is never applied.
func TestFollowerStaleEntryIsReplaced(t *testing.T) {
	sw := switchableThree(t)
	defer sw.close()
	ctx := context.Background()
	storeOnly(t, sw.nodes[2], Entry{Seq: 1, Term: 1, Kind: KindAck, IDs: []string{"STALE"}, By: "op"})
	sw.down[2].Store(true)
	sw.m.set(sw.urls[1], 2)
	if _, err := sw.nodes[1].Propose(ctx, Entry{Kind: KindAck, IDs: []string{"COMMITTED"}, By: "op"}); err != nil {
		t.Fatal(err)
	}
	sw.down[2].Store(false)
	if _, err := sw.nodes[1].Propose(ctx, Entry{Kind: KindAck, IDs: []string{"NEXT"}, By: "op"}); err != nil {
		t.Fatal(err)
	}
	sw.nodes[1].Heartbeat(ctx)
	if got := sw.appliedIDs(2); len(got) != 2 || got[0] != "COMMITTED" || got[1] != "NEXT" {
		t.Fatalf("W applied %v; want COMMITTED then NEXT, never its stale seq 1", got)
	}
}

// (1b) The same through a heartbeat alone: a commit index never makes a
// follower apply an entry the leader does not hold.
func TestHeartbeatNeverCommitsAStaleEntry(t *testing.T) {
	sw := switchableThree(t)
	defer sw.close()
	ctx := context.Background()
	storeOnly(t, sw.nodes[2], Entry{Seq: 1, Term: 1, Kind: KindAck, IDs: []string{"STALE"}, By: "op"})
	sw.down[2].Store(true)
	sw.m.set(sw.urls[1], 2)
	if _, err := sw.nodes[1].Propose(ctx, Entry{Kind: KindAck, IDs: []string{"COMMITTED"}, By: "op"}); err != nil {
		t.Fatal(err)
	}
	sw.down[2].Store(false)
	sw.nodes[1].Heartbeat(ctx)
	if got := sw.appliedIDs(2); len(got) != 1 || got[0] != "COMMITTED" {
		t.Fatalf("W applied %v after a heartbeat; want the committed seq 1", got)
	}
}

// (2) A write after a failed catch-up is refused: here the log reads
// fail while replication works, and the write would otherwise take seq 1,
// which B and W committed, and overwrite it on W.
func TestNoWriteAfterAFailedCatchUp(t *testing.T) {
	sw := switchableThree(t)
	defer sw.close()
	ctx := context.Background()
	sw.down[0].Store(true)
	sw.m.set(sw.urls[1], 2)
	if _, err := sw.nodes[1].Propose(ctx, Entry{Kind: KindAck, IDs: []string{"COMMITTED"}, By: "op"}); err != nil {
		t.Fatal(err)
	}
	sw.down[0].Store(false)
	sw.noGet[1].Store(true)
	sw.noGet[2].Store(true)
	sw.m.set(sw.urls[0], 3)
	if _, err := sw.nodes[0].Propose(ctx, Entry{Kind: KindAck, IDs: []string{"NEW"}, By: "op"}); err == nil {
		t.Fatal("a write was numbered after a failed catch-up")
	}
	sw.nodes[0].Heartbeat(ctx)
	for _, e := range sw.nodes[2].After(0) {
		if e.Seq == 1 && e.IDs[0] != "COMMITTED" {
			t.Fatalf("W's committed seq 1 was overwritten: %+v", sw.nodes[2].After(0))
		}
	}
}

// An uncommitted entry is replaced whatever its term: A holds a stale
// seq 1 from term 5 (its leader died before replicating it), while the
// log a majority holds has seq 1 from term 2 and seq 2 from term 6. A
// term comparison would refuse the replacement and leave A unable to
// sync, or keep the stale entry.
func TestCatchUpReplacesAHigherTermStaleEntry(t *testing.T) {
	sw := switchableThree(t)
	defer sw.close()
	ctx := context.Background()
	storeOnly(t, sw.nodes[0], Entry{Seq: 1, Term: 5, Kind: KindAck, IDs: []string{"STALE"}, By: "op"})
	sw.down[0].Store(true)
	sw.m.set(sw.urls[1], 2)
	if _, err := sw.nodes[1].Propose(ctx, Entry{Kind: KindAck, IDs: []string{"C1"}, By: "op"}); err != nil {
		t.Fatal(err)
	}
	sw.m.set(sw.urls[1], 6)
	if _, err := sw.nodes[1].Propose(ctx, Entry{Kind: KindAck, IDs: []string{"C2"}, By: "op"}); err != nil {
		t.Fatal(err)
	}
	sw.down[0].Store(false)
	sw.m.set(sw.urls[0], 7)
	if err := sw.nodes[0].SyncLeader(ctx); err != nil {
		t.Fatalf("SyncLeader: %v", err)
	}
	if got := sw.appliedIDs(0); len(got) != 2 || got[0] != "C1" || got[1] != "C2" {
		t.Fatalf("A applied %v; want C1, C2", got)
	}
}

// The leader's own suffix is a candidate too: A (term 3) stored X at seq 1
// on itself and W, a majority, and died before advertising the commit; B
// holds a stale seq 1 from term 2. With W down at A's return, the answers
// are A's own and B's, and A's is the more up to date: X must survive,
// not be replaced by B's stale entry.
func TestCatchUpKeepsTheLeadersOwnBetterSuffix(t *testing.T) {
	sw := switchableThree(t)
	defer sw.close()
	ctx := context.Background()
	storeOnly(t, sw.nodes[1], Entry{Seq: 1, Term: 2, Kind: KindAck, IDs: []string{"STALE"}, By: "op"})
	x := Entry{Seq: 1, Term: 3, Kind: KindAck, IDs: []string{"X"}, By: "op"}
	storeOnly(t, sw.nodes[0], x)
	storeOnly(t, sw.nodes[2], x)
	sw.down[2].Store(true)
	sw.m.set(sw.urls[0], 4)
	if err := sw.nodes[0].SyncLeader(ctx); err != nil {
		t.Fatalf("SyncLeader: %v", err)
	}
	if got := sw.appliedIDs(0); len(got) != 1 || got[0] != "X" {
		t.Fatalf("A applied %v; want X, which a majority stored", got)
	}
}

// A follower's CatchUp asks from its commit index, so a stale entry it
// holds is replaced by the leader's, never kept under the leader's next
// ones and applied.
func TestFollowerCatchUpReplacesAStaleEntry(t *testing.T) {
	sw := switchableThree(t)
	defer sw.close()
	ctx := context.Background()
	storeOnly(t, sw.nodes[2], Entry{Seq: 1, Term: 1, Kind: KindAck, IDs: []string{"STALE"}, By: "op"})
	sw.down[2].Store(true)
	sw.m.set(sw.urls[1], 2)
	for _, id := range []string{"C1", "C2"} {
		if _, err := sw.nodes[1].Propose(ctx, Entry{Kind: KindAck, IDs: []string{id}, By: "op"}); err != nil {
			t.Fatal(err)
		}
	}
	sw.down[2].Store(false)
	if err := sw.nodes[2].CatchUp(ctx); err != nil {
		t.Fatal(err)
	}
	if got := sw.appliedIDs(2); len(got) != 2 || got[0] != "C1" || got[1] != "C2" {
		t.Fatalf("W applied %v after CatchUp; want C1, C2", got)
	}
}
