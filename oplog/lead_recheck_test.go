// naut-lead's re-review probes for #1012 at fec8d915, kept as regression
// tests.
package oplog

import (
	"context"
	"net/http/httptest"
	"testing"
)

// (1) leaderCatchUp scores its own EMPTY suffix as term 0, so a peer's
// stale older-term entry above the leader's commit outranks the leader's
// own newer-term committed log. Raft: (t3,1) beats (t2,2).
func TestRecheckOwnEmptySuffixLosesToOlderTerm(t *testing.T) {
	sw := switchableThree(t)
	defer sw.close()
	ctx := context.Background()
	// B: X (t1, its own unconfirmed write as t1 leader), Y (t2, ditto as t2 leader).
	storeOnly(t, sw.nodes[1], Entry{Seq: 1, Term: 1, Kind: KindUnsubstitute, Ref: 0, IDs: []string{"X"}, By: "op"})
	storeOnly(t, sw.nodes[1], Entry{Seq: 2, Term: 2, Kind: KindUnsubstitute, Ref: 1, IDs: []string{"Y"}, By: "op"})
	// t3: A leads with C while B is away and commits Z at seq 1.
	sw.down[1].Store(true)
	sw.m.set(sw.urls[0], 3)
	if _, err := sw.nodes[0].Propose(ctx, Entry{Kind: KindSubstitute, IDs: []string{"Z"}, By: "op"}); err != nil {
		t.Fatal(err)
	}
	sw.nodes[0].Heartbeat(ctx)
	// The lease moves (t4, A again) as B returns, before any heartbeat
	// reaches B. C is away now, so the catch-up weighs only A's own log
	// against B's (with C answering, C's (t3, 1) outranks B's either way
	// and hides the rule).
	sw.down[1].Store(false)
	sw.down[2].Store(true)
	sw.m.set(sw.urls[0], 4)
	if err := sw.nodes[0].SyncLeader(ctx); err != nil {
		t.Fatal(err)
	}
	t.Logf("A log %+v", sw.nodes[0].After(0))
	t.Logf("A applied %v", sw.appliedIDs(0))
	for _, id := range sw.appliedIDs(0) {
		if id == "Y" {
			t.Error("A committed and applied Y (t2), written on top of X, now on top of Z: its Ref 1 names a different entry")
		}
	}
}

// (2) The follower's Propose path accepts the leader's answer and commits
// to its commit index with no previous-entry check.
func TestRecheckProposeAnswerSkipsPrevCheck(t *testing.T) {
	b, m, g := gated(t)
	defer b.close()
	ctx := context.Background()
	storeOnly(t, b.nodes[1], Entry{Seq: 1, Term: 1, Kind: KindAck, IDs: []string{"STALE"}, By: "op"})
	g[1].down.Store(true) // B's inbound is down (its outbound works)
	m.set(b.urls[2], 2)
	if _, err := b.nodes[2].Propose(ctx, Entry{Kind: KindAck, IDs: []string{"C1"}, By: "op"}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.nodes[1].Propose(ctx, Entry{Kind: KindAck, IDs: []string{"Z"}, By: "op"}); err != nil {
		t.Fatal(err)
	}
	t.Logf("B applied %v, C applied %v", b.appliedIDs(1), b.appliedIDs(2))
	sameApplied(t, b)
}

// (3) An in-memory log (File "") with a fixed term would let a restarted
// leader reuse (seq, term) pairs a follower holds for other entries, and
// the previous-entry check would pass them. New refuses several peers
// with no File, except through the test-only Ephemeral option.
func TestNewRefusesAMemoryLogWithPeers(t *testing.T) {
	peers := []string{"http://a", "http://b", "http://c"}
	lead := Static{Self: "http://a", Leader: "http://a"}
	if _, err := New(Options{Self: "http://a", Peers: peers, Token: "t", Leadership: lead}); err == nil {
		t.Fatal("several peers with no File: want a refusal")
	}
	n, err := New(Options{Self: "http://a", Peers: peers, Token: "t", Leadership: lead, Ephemeral: true})
	if err != nil {
		t.Fatalf("Ephemeral (tests only): %v", err)
	}
	n.Close()
}

// (3) The same sequence with the log on file: the restarted leader still
// holds its unconfirmed U, numbers after it, and every node applies the
// same entries.
func TestRecheckFileLogRestartKeepsSeqTerm(t *testing.T) {
	dir := t.TempDir()
	b := &bench{applied: make([][]Entry, 3), urls: make([]string, 3)}
	for i := range b.urls {
		s := httptest.NewUnstartedServer(nil)
		b.servers = append(b.servers, s)
		b.urls[i] = "http://" + s.Listener.Addr().String()
	}
	m := &moving{}
	m.set(b.urls[0], 1)
	g := make([]*gate, 3)
	for i := range b.urls {
		b.nodes = append(b.nodes, b.node(t, i, dir, view{self: b.urls[i], m: m}))
		g[i] = &gate{h: b.nodes[i].Handler()}
		b.servers[i].Config.Handler = g[i]
		b.servers[i].Start()
	}
	defer b.close()
	ctx := context.Background()
	g[1].loseWrites.Store(true)
	g[2].down.Store(true)
	if _, err := b.nodes[0].Propose(ctx, Entry{Kind: KindAck, IDs: []string{"U"}, By: "op"}); err == nil {
		t.Fatal("want unconfirmed")
	}
	g[1].loseWrites.Store(false)
	// A restarts from its file, under the same term.
	g[0].down.Store(true)
	b.nodes[0].Close()
	b.mu.Lock()
	b.applied[0] = nil
	b.mu.Unlock()
	b.nodes[0] = b.node(t, 0, dir, view{self: b.urls[0], m: m})
	g[0].h = b.nodes[0].Handler()
	g[0].down.Store(false)
	g[1].down.Store(true)
	g[2].down.Store(false)
	if _, err := b.nodes[0].Propose(ctx, Entry{Kind: KindAck, IDs: []string{"W"}, By: "op"}); err != nil {
		t.Fatal(err)
	}
	g[1].down.Store(false)
	if _, err := b.nodes[0].Propose(ctx, Entry{Kind: KindAck, IDs: []string{"Z"}, By: "op"}); err != nil {
		t.Fatal(err)
	}
	b.nodes[0].Heartbeat(ctx)
	t.Logf("A %v B %v C %v", b.appliedIDs(0), b.appliedIDs(1), b.appliedIDs(2))
	sameApplied(t, b)
}
