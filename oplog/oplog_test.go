package oplog

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

type bench struct {
	nodes   []*Node
	servers []*httptest.Server
	applied [][]Entry
	mu      sync.Mutex
	urls    []string
}

// three nodes, the first the leader, each applying into its own slice.
func three(t *testing.T, dir string) *bench {
	t.Helper()
	b := &bench{applied: make([][]Entry, 3)}
	b.urls = make([]string, 3)
	for i := range b.urls {
		s := httptest.NewUnstartedServer(nil)
		b.servers = append(b.servers, s)
		b.urls[i] = "http://" + s.Listener.Addr().String()
	}
	for i := range b.urls {
		b.nodes = append(b.nodes, b.node(t, i, dir, Static{Self: b.urls[i], Leader: b.urls[0]}))
		b.servers[i].Config.Handler = b.nodes[i].Handler()
		b.servers[i].Start()
	}
	return b
}

func (b *bench) node(t *testing.T, i int, dir string, lead Leadership) *Node {
	t.Helper()
	file := ""
	if dir != "" {
		file = filepath.Join(dir, fmt.Sprintf("n%d.jsonl", i))
	}
	n, err := New(Options{
		Self: b.urls[i], Peers: b.urls, Leadership: lead, File: file, Token: "t",
		Apply: func(e Entry) error {
			b.mu.Lock()
			b.applied[i] = append(b.applied[i], e)
			b.mu.Unlock()
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func (b *bench) count(i int) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.applied[i])
}

func (b *bench) close() {
	for _, s := range b.servers {
		s.Close()
	}
}

func TestProposeFromFollowerCommitsEverywhere(t *testing.T) {
	b := three(t, "")
	defer b.close()
	ctx := context.Background()
	e, err := b.nodes[2].Propose(ctx, Entry{Kind: KindAck, IDs: []string{"NODE1.Fault"}, By: "op"})
	if err != nil {
		t.Fatal(err)
	}
	if e.Seq != 1 || e.Term != 1 || e.Origin != b.urls[2] || e.TS == 0 {
		t.Fatalf("entry = %+v", e)
	}
	// The leader and the proposer applied at commit; the third stored it
	// and applies once it hears the commit index (the next heartbeat).
	if b.count(0) != 1 || b.count(2) != 1 || b.nodes[1].LastSeq() != 1 {
		t.Fatalf("applied %d %d %d, node1 last %d", b.count(0), b.count(1), b.count(2), b.nodes[1].LastSeq())
	}
	b.nodes[0].Heartbeat(ctx)
	if b.count(1) != 1 || b.nodes[1].Commit() != 1 {
		t.Fatalf("node 1 did not apply on heartbeat: applied %d commit %d", b.count(1), b.nodes[1].Commit())
	}
	if _, err := b.nodes[0].Propose(ctx, Entry{Kind: KindShelve, ID: "X", Until: 5, By: "op"}); err != nil {
		t.Fatal(err)
	}
	if b.nodes[1].LastSeq() != 2 {
		t.Errorf("second entry did not reach node 1")
	}
	for i := range b.nodes {
		if st := b.nodes[i].Status(); !st.Writable {
			t.Errorf("node %d not writable: %s", i, st.Reason)
		}
	}
}

// Operators writing at the same moment: every reported success is in
// every log, applied once, with distinct numbers; no success is lost and
// no failure is applied anywhere.
func TestConcurrentProposals(t *testing.T) {
	b := three(t, "")
	defer b.close()
	const n = 20
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := map[string]uint64{}
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("A%02d", i)
			e, err := b.nodes[i%3].Propose(context.Background(), Entry{Kind: KindAck, IDs: []string{id}, By: "op"})
			if err == nil {
				mu.Lock()
				ok[id] = e.Seq
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if len(ok) != n {
		t.Fatalf("only %d of %d committed on a healthy cluster", len(ok), n)
	}
	seqs := map[uint64]bool{}
	for _, s := range ok {
		if seqs[s] {
			t.Fatalf("seq %d reported twice", s)
		}
		seqs[s] = true
	}
	b.nodes[0].Heartbeat(context.Background())
	for i := range b.nodes {
		got := b.nodes[i].After(0)
		if len(got) != n || b.count(i) != n || b.nodes[i].Commit() != n {
			t.Errorf("node %d: %d in log, %d applied, commit %d", i, len(got), b.count(i), b.nodes[i].Commit())
		}
		for k := range got {
			if got[k].Seq != uint64(k+1) {
				t.Errorf("node %d: seq %d at index %d", i, got[k].Seq, k)
			}
		}
	}
}

func TestMinorityIsReadOnlyAndNothingLeaks(t *testing.T) {
	b := three(t, "")
	defer b.close()
	ctx := context.Background()
	b.servers[1].Close()
	if _, err := b.nodes[0].Propose(ctx, Entry{Kind: KindAck, IDs: []string{"a"}, By: "op"}); err != nil {
		t.Fatalf("2 of 3 must commit: %v", err)
	}
	b.servers[2].Close()
	_, err := b.nodes[0].Propose(ctx, Entry{Kind: KindAck, IDs: []string{"b"}, By: "op"})
	if !errors.Is(err, ErrReadOnly) {
		t.Fatalf("1 of 3 must be read-only, got %v", err)
	}
	// The failed write is gone from the leader's log and was never applied.
	if b.nodes[0].LastSeq() != 1 || b.count(0) != 1 {
		t.Fatalf("a refused write leaked: last %d applied %d", b.nodes[0].LastSeq(), b.count(0))
	}
	if st := b.nodes[0].Status(); st.Writable {
		t.Error("leader without a majority must report read-only")
	}
	b.servers[0].Close()
	if st := b.nodes[1].Status(); st.Writable {
		t.Error("follower with no leader must report read-only")
	}
}

func TestCatchUpAndReplay(t *testing.T) {
	dir := t.TempDir()
	b := three(t, dir)
	defer b.close()
	ctx := context.Background()
	b.servers[2].Close()
	for _, id := range []string{"a", "b"} {
		if _, err := b.nodes[0].Propose(ctx, Entry{Kind: KindAck, IDs: []string{id}, By: "op"}); err != nil {
			t.Fatal(err)
		}
	}
	if b.nodes[2].LastSeq() != 0 {
		t.Fatal("node 2 should have missed them")
	}
	if err := b.nodes[2].CatchUp(ctx); err != nil {
		t.Fatal(err)
	}
	if b.nodes[2].LastSeq() != 2 || b.count(2) != 2 || b.nodes[2].Commit() != 2 {
		t.Fatalf("catch-up: last %d applied %d commit %d", b.nodes[2].LastSeq(), b.count(2), b.nodes[2].Commit())
	}
	// A restart replays the committed prefix into Apply, in order, once.
	var replayed []Entry
	n, err := New(Options{Self: b.urls[2], Peers: b.urls, Leadership: Static{Self: b.urls[2], Leader: b.urls[0]}, Token: "t", File: filepath.Join(dir, "n2.jsonl"), Apply: func(e Entry) error {
		replayed = append(replayed, e)
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	if len(replayed) != 2 || replayed[1].Seq != 2 || n.LastSeq() != 2 || n.Commit() != 2 {
		t.Fatalf("replayed %+v", replayed)
	}
}

func TestTornLastLineIsDropped(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "n.jsonl")
	n, err := New(Options{Self: "http://a", File: file, Apply: func(Entry) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b"} {
		if _, err := n.Propose(context.Background(), Entry{Kind: KindAck, IDs: []string{id}}); err != nil {
			t.Fatal(err)
		}
	}
	n.Close()
	f, _ := os.OpenFile(file, os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = f.WriteString(`{"e":{"seq":3,"term":1,"kind":"ack","ids":["c`) // crashed mid-write
	f.Close()
	var replayed int
	m, err := New(Options{Self: "http://a", File: file, Apply: func(Entry) error { replayed++; return nil }})
	if err != nil {
		t.Fatalf("a torn last line must not stop the instance: %v", err)
	}
	defer m.Close()
	if m.LastSeq() != 2 || replayed != 2 {
		t.Fatalf("last %d replayed %d", m.LastSeq(), replayed)
	}
	// And the file is clean again: a third write lands at 3.
	if e, err := m.Propose(context.Background(), Entry{Kind: KindAck, IDs: []string{"c"}}); err != nil || e.Seq != 3 {
		t.Fatalf("after truncation: %+v %v", e, err)
	}
}

func TestStaleLeaderIsRefused(t *testing.T) {
	b := three(t, "")
	defer b.close()
	ctx := context.Background()
	// The cluster moves to term 2 with node 1 as leader.
	for i := range b.nodes {
		b.nodes[i].o.Leadership = Static{Self: b.urls[i], Leader: b.urls[1], Generation: 2}
	}
	if _, err := b.nodes[1].Propose(ctx, Entry{Kind: KindAck, IDs: []string{"new"}, By: "op"}); err != nil {
		t.Fatal(err)
	}
	// Node 0 still believes it leads at term 1 and pushes an entry.
	b.nodes[0].o.Leadership = Static{Self: b.urls[0], Leader: b.urls[0], Generation: 1}
	_, err := b.nodes[0].Propose(ctx, Entry{Kind: KindAck, IDs: []string{"stale"}, By: "op"})
	if err == nil {
		t.Fatal("a deposed leader must not commit")
	}
	for i := 1; i < 3; i++ {
		for _, e := range b.nodes[i].After(0) {
			if e.IDs[0] == "stale" {
				t.Fatalf("node %d accepted the stale leader's entry", i)
			}
		}
	}
}

func TestNewRefusesUnsafeOptions(t *testing.T) {
	peers := []string{"http://a", "http://b", "http://c"}
	if _, err := New(Options{Self: "http://a", Peers: peers, Leadership: Static{Self: "http://a", Leader: "http://a"}}); err == nil {
		t.Error("peers without a token must be refused")
	}
	if _, err := New(Options{Self: "http://a", Peers: peers, Token: "t"}); err == nil {
		t.Error("peers without a leadership must be refused")
	}
	if _, err := New(Options{Self: "http://z", Peers: peers, Token: "t", Leadership: Static{Self: "http://z", Leader: "http://a"}}); err == nil {
		t.Error("a self outside the peers must be refused")
	}
}

func TestStandalone(t *testing.T) {
	var applied int
	n, err := New(Options{Self: "http://a", Apply: func(Entry) error { applied++; return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := n.Propose(context.Background(), Entry{Kind: KindAck, IDs: []string{"x"}}); err != nil || applied != 1 {
		t.Fatalf("standalone must commit at once: %v %d", err, applied)
	}
	if !n.Status().Writable {
		t.Error("standalone is always writable")
	}
}

// The rewrite (a follower dropping an uncommitted suffix) replaces the file
// while the node holds it open. Windows refuses a rename over an open file,
// so this is the test the Windows CI job exists for: rewrite, keep
// appending, and reopen to the same log.
func TestRewriteThenAppendThenReopen(t *testing.T) {
	file := filepath.Join(t.TempDir(), "n.jsonl")
	n, err := New(Options{Self: "http://a", File: file, Apply: func(Entry) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, id := range []string{"a", "b"} {
		if _, err := n.Propose(ctx, Entry{Kind: KindAck, IDs: []string{id}}); err != nil {
			t.Fatal(err)
		}
	}
	n.mu.Lock()
	err = n.rewrite()
	n.mu.Unlock()
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if e, err := n.Propose(ctx, Entry{Kind: KindAck, IDs: []string{"c"}}); err != nil || e.Seq != 3 {
		t.Fatalf("append after rewrite: %+v %v", e, err)
	}
	if err := n.Close(); err != nil {
		t.Fatal(err)
	}
	var replayed int
	m, err := New(Options{Self: "http://a", File: file, Apply: func(Entry) error { replayed++; return nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if m.LastSeq() != 3 || replayed != 3 {
		t.Fatalf("after reopen: last %d replayed %d, want 3 and 3", m.LastSeq(), replayed)
	}
	if left, _ := filepath.Glob(file + ".*.tmp"); len(left) != 0 {
		t.Fatalf("temp files left behind: %v", left)
	}
}
