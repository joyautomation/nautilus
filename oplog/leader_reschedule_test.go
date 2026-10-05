package oplog

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// Leadership moves to an instance that lost its file (a reschedule): the
// cluster must still take a write. Fails at cb38c5b: the new leader's
// entries conflict with the committed ones its peers hold, and a leader
// never fetches what it lacks.
func TestNewLeaderMissingCommitted(t *testing.T) {
	dir := t.TempDir()
	srv := make([]*httptest.Server, 3)
	urls := make([]string, 3)
	for i := range srv {
		srv[i] = httptest.NewUnstartedServer(nil)
		urls[i] = "http://" + srv[i].Listener.Addr().String()
	}
	start := func(leader int, gen uint64) []*Node {
		ns := make([]*Node, 3)
		for i := range ns {
			n, err := New(Options{Self: urls[i], Peers: urls, Token: "t", File: filepath.Join(dir, string(rune('a'+i))+".jsonl"),
				Leadership: Static{Self: urls[i], Leader: urls[leader], Generation: gen}})
			if err != nil {
				t.Fatal(err)
			}
			ns[i] = n
			srv[i].Config.Handler = n.Handler()
		}
		return ns
	}
	ns := start(0, 1)
	for i := range srv {
		srv[i].Start()
		defer srv[i].Close()
	}
	ctx := context.Background()
	for _, id := range []string{"X", "Y"} {
		if _, err := ns[0].Propose(ctx, Entry{Kind: KindAck, IDs: []string{id}, By: "op"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, n := range ns {
		n.Close()
	}
	os.Remove(filepath.Join(dir, "c.jsonl")) // node C rescheduled: its file is gone
	ns = start(2, 2)                         // the lease moves to C, term 2
	for _, n := range ns {
		n.Heartbeat(ctx)
		_ = n.CatchUp(ctx)
	}
	if _, err := ns[2].Propose(ctx, Entry{Kind: KindAck, IDs: []string{"Z"}, By: "op"}); err != nil {
		t.Fatalf("the cluster cannot take a write after leadership moved to an instance that lost its file: %v", err)
	}
	if ns[2].LastSeq() != 3 {
		t.Fatalf("new leader lastSeq = %d, want 3 (X, Y, then Z)", ns[2].LastSeq())
	}
}
