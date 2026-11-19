package oplog

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"sync"
	"testing"
)

// SyncLeader: what a takeover needs before its first scan (#707, redundancy
// test plan §8.1 case 7). A new leader holds every committed entry, applied,
// or says it cannot.

// moving is a Leadership shared by the bench, moved by the test.
type moving struct {
	mu     sync.Mutex
	leader string
	term   uint64
}

func (m *moving) set(leader string, term uint64) {
	m.mu.Lock()
	m.leader, m.term = leader, term
	m.mu.Unlock()
}

// view is one instance's view of the moving leadership.
type view struct {
	self string
	m    *moving
}

func (v view) IsLeader() bool { v.m.mu.Lock(); defer v.m.mu.Unlock(); return v.m.leader == v.self }
func (v view) LeaderAddr() string {
	v.m.mu.Lock()
	defer v.m.mu.Unlock()
	return v.m.leader
}
func (v view) Term() uint64 { v.m.mu.Lock(); defer v.m.mu.Unlock(); return v.m.term }

// movingThree is three() on a leadership the test can move, node 0 first.
func movingThree(t *testing.T) (*bench, *moving) {
	t.Helper()
	b := &bench{applied: make([][]Entry, 3), urls: make([]string, 3)}
	for i := range b.urls {
		s := httptest.NewUnstartedServer(nil)
		b.servers = append(b.servers, s)
		b.urls[i] = "http://" + s.Listener.Addr().String()
	}
	m := &moving{}
	m.set(b.urls[0], 1)
	for i := range b.urls {
		b.nodes = append(b.nodes, b.node(t, i, "", view{self: b.urls[i], m: m}))
		b.servers[i].Config.Handler = b.nodes[i].Handler()
		b.servers[i].Start()
	}
	return b, m
}

func (b *bench) appliedKinds(i int) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for _, e := range b.applied[i] {
		out = append(out, e.Kind)
	}
	return out
}

// The leader dies after its write committed (a majority stored it) but
// before any follower heard the new commit index. The new leader holds the
// entry, uncommitted; SyncLeader must commit and apply it before it
// returns, not leave it for the first write.
func TestSyncLeaderCommitsWhatTheLastTermStored(t *testing.T) {
	b, m := movingThree(t)
	defer b.close()
	ctx := context.Background()
	if _, err := b.nodes[0].Propose(ctx, Entry{Kind: KindAck, IDs: []string{"X"}, By: "op"}); err != nil {
		t.Fatal(err)
	}
	if b.count(1) != 0 || b.nodes[1].LastSeq() != 1 {
		t.Fatalf("precondition: B applied %d, stored up to %d; want X stored, not applied", b.count(1), b.nodes[1].LastSeq())
	}
	b.servers[0].Close() // A dies before a heartbeat
	m.set(b.urls[1], 2)
	if err := b.nodes[1].SyncLeader(ctx); err != nil {
		t.Fatalf("SyncLeader: %v", err)
	}
	if got := b.appliedKinds(1); len(got) != 1 || got[0] != KindAck {
		t.Fatalf("B applied %v after SyncLeader; want the ack, and never the noop", got)
	}
	last := b.nodes[1].LastSeq()
	if err := b.nodes[1].SyncLeader(ctx); err != nil || b.nodes[1].LastSeq() != last {
		t.Fatalf("a second SyncLeader in the term: %v, lastSeq %d → %d; want nothing new", err, last, b.nodes[1].LastSeq())
	}
	b.nodes[1].Heartbeat(ctx)
	if got := b.appliedKinds(2); len(got) != 1 || got[0] != KindAck {
		t.Fatalf("the witness applied %v after the heartbeat; want the ack, never the noop", got)
	}
}

// A leader that cannot reach a majority cannot know it holds every
// committed entry: SyncLeader says so, every time, until it can.
func TestSyncLeaderNeedsAMajority(t *testing.T) {
	ctx := context.Background()
	t.Run("both peers down", func(t *testing.T) {
		b, _ := movingThree(t)
		defer b.close()
		b.servers[1].Close()
		b.servers[2].Close()
		for i := 0; i < 2; i++ { // the failure is not remembered as a sync
			if err := b.nodes[0].SyncLeader(ctx); !errors.Is(err, ErrReadOnly) {
				t.Fatalf("SyncLeader #%d with no peer reachable: %v; want ErrReadOnly", i+1, err)
			}
		}
	})
	t.Run("one peer down", func(t *testing.T) {
		b, _ := movingThree(t)
		defer b.close()
		b.servers[2].Close()
		if err := b.nodes[0].SyncLeader(ctx); err != nil {
			t.Fatalf("SyncLeader with a majority reachable: %v", err)
		}
	})
	t.Run("a follower", func(t *testing.T) {
		b, _ := movingThree(t)
		defer b.close()
		if err := b.nodes[1].SyncLeader(ctx); !errors.Is(err, ErrReadOnly) {
			t.Fatalf("SyncLeader on a follower: %v; want ErrReadOnly", err)
		}
	})
}

// The substitute kinds carry their fields through replication, and an
// unsubstitute and a suback name the entry by its seq.
func TestSubstituteKindsReplicate(t *testing.T) {
	b, _ := movingThree(t)
	defer b.close()
	ctx := context.Background()
	sub, err := b.nodes[1].Propose(ctx, Entry{Kind: KindSubstitute, Tag: "Level", Value: json.RawMessage(`95.5`),
		GrantedMs: 3_600_000, Until: 1_800_000_000_000, AckOnStart: true, By: "op"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.nodes[0].Propose(ctx, Entry{Kind: KindSuback, Refs: []uint64{sub.Seq}, By: "sup"}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.nodes[0].Propose(ctx, Entry{Kind: KindUnsubstitute, Ref: sub.Seq, Reason: "expired", By: "system"}); err != nil {
		t.Fatal(err)
	}
	b.nodes[0].Heartbeat(ctx)
	b.mu.Lock()
	got := append([]Entry(nil), b.applied[2]...)
	b.mu.Unlock()
	if len(got) != 3 {
		t.Fatalf("the witness applied %d entries; want 3", len(got))
	}
	s, a, u := got[0], got[1], got[2]
	if s.Kind != KindSubstitute || s.Tag != "Level" || string(s.Value) != "95.5" || s.GrantedMs != 3_600_000 ||
		s.Until != 1_800_000_000_000 || !s.AckOnStart || s.Origin != b.urls[1] {
		t.Fatalf("substitute entry %+v", s)
	}
	if a.Kind != KindSuback || len(a.Refs) != 1 || a.Refs[0] != s.Seq {
		t.Fatalf("suback entry %+v; want it naming seq %d", a, s.Seq)
	}
	if u.Kind != KindUnsubstitute || u.Ref != s.Seq || u.Reason != "expired" || u.By != "system" {
		t.Fatalf("unsubstitute entry %+v; want it naming seq %d", u, s.Seq)
	}
}
