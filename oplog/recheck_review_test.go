// naut-redundancy's re-check probes for #1012, kept as regression tests.
package oplog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// (ii) a gap answer, then a mismatch on the resend: one resend only.
func TestRecheckGapThenMismatch(t *testing.T) {
	sw := switchableThree(t)
	defer sw.close()
	ctx := context.Background()
	if _, err := sw.nodes[0].Propose(ctx, Entry{Kind: KindAck, IDs: []string{"X"}, By: "op"}); err != nil {
		t.Fatal(err)
	}
	sw.nodes[0].Heartbeat(ctx)
	storeOnly(t, sw.nodes[2], Entry{Seq: 2, Term: 1, Kind: KindAck, IDs: []string{"STALE"}, By: "op"})
	sw.m.set(sw.urls[0], 2)
	sw.down[2].Store(true)
	for _, id := range []string{"Y", "Z"} {
		if _, err := sw.nodes[0].Propose(ctx, Entry{Kind: KindAck, IDs: []string{id}, By: "op"}); err != nil {
			t.Fatal(err)
		}
	}
	sw.down[2].Store(false)
	for range 5 {
		sw.nodes[0].Heartbeat(ctx)
	}
	t.Logf("W log %+v", sw.nodes[2].After(0))
	if got := sw.nodes[2].After(1); len(got) != 2 || got[0].IDs[0] != "Y" {
		t.Fatalf("W never converged after 5 heartbeats")
	}
}

// (i) a failed write's seq reused in the same term.
func TestRecheckSeqReuse(t *testing.T) {
	b := &bench{applied: make([][]Entry, 3), urls: make([]string, 3)}
	var down, noGet, loseReply [3]atomic.Bool
	for i := range b.urls {
		s := httptest.NewUnstartedServer(nil)
		b.servers = append(b.servers, s)
		b.urls[i] = "http://" + s.Listener.Addr().String()
	}
	m := &moving{}
	m.set(b.urls[0], 2)
	for i := range b.urls {
		b.nodes = append(b.nodes, b.node(t, i, "", view{self: b.urls[i], m: m}))
		h := b.nodes[i].Handler()
		i := i
		b.servers[i].Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if down[i].Load() || noGet[i].Load() && r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/oplog") {
				http.Error(w, "down", http.StatusServiceUnavailable)
				return
			}
			if loseReply[i].Load() && r.Method == http.MethodPost {
				h.ServeHTTP(httptest.NewRecorder(), r) // done, but the answer is lost
				http.Error(w, "lost", http.StatusServiceUnavailable)
				return
			}
			h.ServeHTTP(w, r)
		})
		b.servers[i].Start()
	}
	defer b.close()
	ctx := context.Background()
	// X: W stores it, its answer is lost; B is down. X stays unconfirmed in A's log.
	down[1].Store(true)
	loseReply[2].Store(true)
	_, err := b.nodes[0].Propose(ctx, Entry{Kind: KindAck, IDs: []string{"X"}, By: "op"})
	t.Logf("X: %v", err)
	loseReply[2].Store(false)
	// Y: W's reads fail (so catch-up uses A+B), and W is down for the write.
	down[1].Store(false)
	noGet[2].Store(true)
	down[2].Store(true)
	_, err = b.nodes[0].Propose(ctx, Entry{Kind: KindAck, IDs: []string{"Y"}, By: "op"})
	t.Logf("Y: %v", err)
	// Z: all up.
	down[2].Store(false)
	noGet[2].Store(false)
	_, err = b.nodes[0].Propose(ctx, Entry{Kind: KindAck, IDs: []string{"Z"}, By: "op"})
	t.Logf("Z: %v", err)
	b.nodes[0].Heartbeat(ctx)
	t.Logf("A applied %v, W applied %v", b.appliedIDs(0), b.appliedIDs(2))
	// X was unconfirmed (W stored it, its answer was lost): it may be
	// applied, but then everywhere, at the same seq. On dafc0817 A applied
	// [Y Z] and W [X Z].
	sameApplied(t, b)
	if got := b.appliedIDs(0); len(got) == 0 || got[len(got)-1] != "Z" {
		t.Fatalf("A applied %v; want Z last", got)
	}
}
