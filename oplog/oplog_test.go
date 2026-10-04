package oplog

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
)

type bench struct {
	nodes   []*Node
	servers []*httptest.Server
	applied [][]Entry
	mu      sync.Mutex
}

// three nodes, the first the leader, each applying into its own slice.
func three(t *testing.T, dir string) *bench {
	t.Helper()
	b := &bench{applied: make([][]Entry, 3)}
	urls := make([]string, 3)
	for i := range urls {
		s := httptest.NewUnstartedServer(nil)
		b.servers = append(b.servers, s)
		urls[i] = "http://" + s.Listener.Addr().String()
	}
	for i := range urls {
		i := i
		file := ""
		if dir != "" {
			file = filepath.Join(dir, "n"+string(rune('0'+i))+".jsonl")
		}
		n, err := New(Options{
			Self: urls[i], Peers: urls, Leadership: Static{Self: urls[i], Leader: urls[0]}, File: file,
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
		b.nodes = append(b.nodes, n)
		b.servers[i].Config.Handler = n.Handler()
		b.servers[i].Start()
	}
	return b
}

func (b *bench) count(i int) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.applied[i])
}

func TestProposeFromFollowerCommitsEverywhere(t *testing.T) {
	b := three(t, "")
	defer func() {
		for _, s := range b.servers {
			s.Close()
		}
	}()
	ctx := context.Background()
	e, err := b.nodes[2].Propose(ctx, Entry{Kind: KindAck, IDs: []string{"NODE1.Fault"}, By: "op"})
	if err != nil {
		t.Fatal(err)
	}
	if e.Seq != 1 || e.Origin != b.nodes[2].o.Self || e.TS == 0 {
		t.Fatalf("entry = %+v", e)
	}
	for i := range b.nodes {
		if b.count(i) != 1 || b.nodes[i].LastSeq() != 1 {
			t.Errorf("node %d: applied %d, lastSeq %d", i, b.count(i), b.nodes[i].LastSeq())
		}
	}
	if _, err := b.nodes[0].Propose(ctx, Entry{Kind: KindShelve, ID: "X", Until: 5, By: "op"}); err != nil {
		t.Fatal(err)
	}
	if b.nodes[1].LastSeq() != 2 || b.count(1) != 2 {
		t.Errorf("second entry did not reach node 1")
	}
	for i := range b.nodes {
		if st := b.nodes[i].Status(); !st.Writable {
			t.Errorf("node %d not writable: %s", i, st.Reason)
		}
	}
}

func TestMinorityIsReadOnly(t *testing.T) {
	b := three(t, "")
	defer func() {
		for _, s := range b.servers {
			s.Close()
		}
	}()
	ctx := context.Background()
	// One peer down: still a majority (2 of 3).
	b.servers[1].Close()
	if _, err := b.nodes[0].Propose(ctx, Entry{Kind: KindAck, IDs: []string{"a"}, By: "op"}); err != nil {
		t.Fatalf("2 of 3 must commit: %v", err)
	}
	// Two peers down: the leader alone cannot commit, and says so.
	b.servers[2].Close()
	_, err := b.nodes[0].Propose(ctx, Entry{Kind: KindAck, IDs: []string{"b"}, By: "op"})
	if !errors.Is(err, ErrReadOnly) {
		t.Fatalf("1 of 3 must be read-only, got %v", err)
	}
	if st := b.nodes[0].Status(); st.Writable {
		t.Error("leader without a majority must report read-only")
	}
	// A follower whose leader is gone is read-only too.
	b.servers[0].Close()
	if st := b.nodes[1].Status(); st.Writable {
		t.Error("follower with no leader must report read-only")
	}
}

func TestCatchUpAndReplay(t *testing.T) {
	dir := t.TempDir()
	b := three(t, dir)
	ctx := context.Background()
	// Node 2 is away while two entries commit.
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
	if b.nodes[2].LastSeq() != 2 || b.count(2) != 2 {
		t.Fatalf("catch-up: lastSeq %d applied %d", b.nodes[2].LastSeq(), b.count(2))
	}
	// A restart replays the file into Apply, in order, once.
	var replayed []Entry
	n, err := New(Options{Self: b.nodes[2].o.Self, Peers: b.nodes[2].o.Peers, File: filepath.Join(dir, "n2.jsonl"), Apply: func(e Entry) error {
		replayed = append(replayed, e)
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	if len(replayed) != 2 || replayed[1].Seq != 2 || n.LastSeq() != 2 {
		t.Fatalf("replayed %+v", replayed)
	}
	b.servers[0].Close()
	b.servers[1].Close()
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
