// naut-lead's review repros for #1012, kept as regression tests (each
// failed on main and on the PR's first head).
package oplog

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// gate: down = refuse all; lose = run the handler, then answer 503 (the
// response is lost); loseWrites = the same for POSTs only (a log read
// still answers); noGet = refuse GETs only.
type gate struct {
	h                             http.Handler
	down, lose, loseWrites, noGet atomic.Bool
}

func (g *gate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if g.down.Load() || (g.noGet.Load() && r.Method == http.MethodGet) {
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}
	if g.lose.Load() || g.loseWrites.Load() && r.Method == http.MethodPost {
		g.h.ServeHTTP(httptest.NewRecorder(), r)
		http.Error(w, "lost", http.StatusServiceUnavailable)
		return
	}
	g.h.ServeHTTP(w, r)
}

func gated(t *testing.T) (*bench, *moving, []*gate) {
	b := &bench{applied: make([][]Entry, 3), urls: make([]string, 3)}
	for i := range b.urls {
		s := httptest.NewUnstartedServer(nil)
		b.servers = append(b.servers, s)
		b.urls[i] = "http://" + s.Listener.Addr().String()
	}
	m := &moving{}
	m.set(b.urls[0], 1)
	gs := make([]*gate, 3)
	for i := range b.urls {
		b.nodes = append(b.nodes, b.node(t, i, "", view{self: b.urls[i], m: m}))
		gs[i] = &gate{h: b.nodes[i].Handler()}
		b.servers[i].Config.Handler = gs[i]
		b.servers[i].Start()
	}
	return b, m, gs
}

func appliedHas(b *bench, i int, id string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, e := range b.applied[i] {
		if len(e.IDs) > 0 && e.IDs[0] == id {
			return true
		}
	}
	return false
}

// B holds an unconfirmed STALE at seq 2 (its reply to A was lost); C leads
// term 2 and commits Y; B leads term 3. On the PR's first head B's catch-up
// asked after its last entry and its noop committed its own seq 2 while A
// and C applied something else there. Unconfirmed writes may still be
// applied (their outcome is unknown, not "no"), so what must hold is that
// every node applies the same entries in the same order, Y among them.
func TestReviewDivergentSuffixCommittedByNoop(t *testing.T) {
	b, m, g := gated(t)
	defer b.close()
	ctx := context.Background()
	if _, err := b.nodes[0].Propose(ctx, Entry{Kind: KindAck, IDs: []string{"P"}, By: "op"}); err != nil {
		t.Fatal(err)
	}
	b.nodes[0].Heartbeat(ctx)
	g[1].lose.Store(true)
	g[2].down.Store(true)
	if _, err := b.nodes[0].Propose(ctx, Entry{Kind: KindAck, IDs: []string{"STALE"}, By: "op"}); err == nil {
		t.Fatal("want failure")
	}
	g[1].lose.Store(false)
	g[1].down.Store(true)
	g[2].down.Store(false)
	m.set(b.urls[2], 2)
	if _, err := b.nodes[2].Propose(ctx, Entry{Kind: KindAck, IDs: []string{"Y"}, By: "op"}); err != nil {
		t.Fatal(err)
	}
	b.nodes[2].Heartbeat(ctx)
	g[1].down.Store(false)
	m.set(b.urls[1], 3)
	if err := b.nodes[1].SyncLeader(ctx); err != nil {
		t.Fatal(err)
	}
	b.nodes[1].Heartbeat(ctx)
	sameApplied(t, b)
	if !appliedHas(b, 1, "Y") {
		t.Error("B did not apply Y, which C committed")
	}
}

// sameApplied fails unless every node applied the same entries in the same
// order, or a prefix of the longest (a node that has not heard the latest
// commit index yet).
func sameApplied(t *testing.T, b *bench) {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	longest := b.applied[0]
	for _, a := range b.applied {
		if len(a) > len(longest) {
			longest = a
		}
	}
	for i, a := range b.applied {
		for j := range a {
			if a[j].Seq != longest[j].Seq || !a[j].same(longest[j]) {
				t.Fatalf("node %d applied %+v at position %d; another applied %+v there", i, a[j], j, longest[j])
			}
		}
	}
}

// A write whose reply was lost is reported as unconfirmed (its outcome
// unknown), never as refused: B stored it, and after a takeover B may
// commit and apply it. A client told ErrUnconfirmed re-reads the state.
func TestReviewToldNoThenApplied(t *testing.T) {
	b, m, g := gated(t)
	defer b.close()
	ctx := context.Background()
	g[1].lose.Store(true)
	g[2].down.Store(true)
	g[1].lose.Store(false)
	g[1].loseWrites.Store(true)
	_, err := b.nodes[0].Propose(ctx, Entry{Kind: KindSubstitute, Tag: "Level", By: "op"})
	if !errors.Is(err, ErrUnconfirmed) {
		t.Fatalf("a write whose reply was lost: %v; want ErrUnconfirmed (outcome unknown), not a plain refusal", err)
	}
	g[1].loseWrites.Store(false)
	g[0].down.Store(true)
	g[2].down.Store(false)
	m.set(b.urls[1], 2)
	if err := b.nodes[1].SyncLeader(ctx); err != nil {
		t.Fatal(err)
	}
	if b.count(1) != 1 {
		t.Fatal("B holds the unconfirmed write and a majority now: it commits it")
	}
	b.nodes[1].Heartbeat(ctx)
	sameApplied(t, b)
}

// The write path goes on after a failed catch-up and overwrites an acked,
// stored-but-uncommitted entry on the only follower holding it.
func TestReviewWriteAfterFailedCatchUpLosesAck(t *testing.T) {
	b, m, g := gated(t)
	defer b.close()
	ctx := context.Background()
	g[2].down.Store(true)
	if _, err := b.nodes[0].Propose(ctx, Entry{Kind: KindAck, IDs: []string{"X"}, By: "op"}); err != nil {
		t.Fatal(err)
	}
	g[0].down.Store(true)
	g[2].down.Store(false)
	g[1].noGet.Store(true)
	m.set(b.urls[2], 2)
	_, _ = b.nodes[2].Propose(ctx, Entry{Kind: KindAck, IDs: []string{"Z"}, By: "op"})
	for _, e := range b.nodes[1].After(0) {
		if len(e.IDs) > 0 && e.IDs[0] == "X" {
			return
		}
	}
	t.Error("acked X is gone from every live log")
}

// No failover: a write that missed its majority after a good catch-up (B
// stored it, its reply was lost; C was down) must not leave its (seq,
// term) to a different entry, or B's copy passes the previous-entry check
// and is applied in place of the committed one. The write is kept in the
// leader's log (its outcome is unknown, not "no"), so seq 1 holds the
// same entry everywhere.
func TestReviewFollowerDivergesWithoutFailover(t *testing.T) {
	b, _, g := gated(t)
	defer b.close()
	ctx := context.Background()
	g[1].loseWrites.Store(true)
	g[2].down.Store(true)
	if _, err := b.nodes[0].Propose(ctx, Entry{Kind: KindAck, IDs: []string{"STALE"}, By: "op"}); err == nil {
		t.Fatal("want failure")
	}
	if len(b.nodes[1].After(0)) != 1 {
		t.Fatal("precondition: B stored the write whose reply was lost")
	}
	g[1].loseWrites.Store(false)
	g[1].down.Store(true)
	g[2].down.Store(false)
	if _, err := b.nodes[0].Propose(ctx, Entry{Kind: KindAck, IDs: []string{"W2"}, By: "op"}); err != nil {
		t.Fatal(err)
	}
	g[1].down.Store(false)
	if _, err := b.nodes[0].Propose(ctx, Entry{Kind: KindAck, IDs: []string{"W3"}, By: "op"}); err != nil {
		t.Fatal(err)
	}
	b.nodes[0].Heartbeat(ctx)
	for i := range b.nodes {
		e := b.nodes[i].After(0)
		if len(e) == 0 || e[0].IDs[0] != b.nodes[0].After(0)[0].IDs[0] {
			t.Fatalf("node %d holds %+v at seq 1; A holds %+v: the logs diverged", i, e, b.nodes[0].After(0))
		}
	}
	for i := range b.nodes {
		if appliedHas(b, i, "W2") != appliedHas(b, 1, "W2") || appliedHas(b, i, "STALE") != appliedHas(b, 1, "STALE") {
			t.Fatalf("the nodes applied different entries")
		}
	}
}
