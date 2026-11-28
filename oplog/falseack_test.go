package oplog

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// staleTailWrites (Fable, #1471 review): A, the term-1 leader, holds
// entries past its commit that only C shares (m*: their answers were
// lost) and then u2, u3 on A alone. B leads term 2 from C, so it holds
// the m* but not u2, u3, and commits e. A comes back; C goes down, and the
// commit push to A is lost. Every write B confirms must be on A (#1472),
// and replication alone, no pull, must repair A's stale tail (#1471).
func staleTailWrites(t *testing.T, lim limits, pad, nMatch int) {
	t.Helper()
	b := newLapseBenchLim(t, 3, lim)
	ctx := context.Background()
	var lose, refuse atomic.Bool
	inner := b.srv[2].Config.Handler
	b.srv[2].Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/replicate") {
			if refuse.Load() {
				http.Error(w, "refused", http.StatusServiceUnavailable)
				return
			}
			if lose.Load() {
				inner.ServeHTTP(httptest.NewRecorder(), r) // C stores it, its answer is lost
				http.Error(w, "reply lost", http.StatusServiceUnavailable)
				return
			}
		}
		inner.ServeHTTP(w, r)
	})
	big := strings.Repeat("x", pad)
	a, bb := b.node(0), b.node(1)
	if _, err := a.Propose(ctx, Entry{Kind: KindAck, IDs: []string{"warm"}}); err != nil {
		t.Fatal(err)
	}
	a.Heartbeat(ctx)
	b.down[1].Store(true)
	lose.Store(true)
	for i := range nMatch {
		if _, err := a.Propose(ctx, Entry{Kind: KindAck, IDs: []string{fmt.Sprint("m", i) + big}}); err == nil {
			t.Fatal("an m entry committed")
		}
	}
	lose.Store(false)
	refuse.Store(true)
	for _, id := range []string{"u2", "u3"} {
		if _, err := a.Propose(ctx, Entry{Kind: KindAck, IDs: []string{id}}); err == nil {
			t.Fatal(id + " committed")
		}
	}
	refuse.Store(false)
	b.down[0].Store(true)
	b.down[1].Store(false)
	b.m.set(b.urls[1], 2)
	if _, err := bb.Propose(ctx, Entry{Kind: KindAck, IDs: []string{"e" + big}}); err != nil {
		t.Fatal(err)
	}
	bb.Heartbeat(ctx)
	b.down[0].Store(false)
	b.down[2].Store(true)
	b.push[0].Store(true) // the commit push to A is lost
	confirmed := 0
	for i := range 4 {
		e, err := bb.Propose(ctx, Entry{Kind: KindAck, IDs: []string{fmt.Sprint("w", i)}})
		if err != nil {
			continue
		}
		confirmed++
		held := false
		for _, x := range a.After(0) {
			held = held || x.Seq == e.Seq && x.Term == e.Term && x.IDs[0] == e.IDs[0]
		}
		if !held {
			t.Fatalf("write %d: B confirmed seq %d with C down, and A does not hold it (a false acknowledgement)", i, e.Seq)
		}
	}
	if confirmed == 0 {
		t.Fatal("no write was confirmed: nothing checked the acknowledgements")
	}
	b.push[0].Store(false)
	for range 10 {
		bb.Heartbeat(ctx)
	}
	if a.Commit() != bb.Commit() || a.LastSeq() != bb.LastSeq() {
		t.Fatalf("A at %d/%d after B's heartbeats, B at %d/%d: replication does not repair A's stale tail", a.LastSeq(), a.Commit(), bb.LastSeq(), bb.Commit())
	}
	for _, id := range []string{"u2", "u3"} {
		for _, x := range a.After(0) {
			if x.IDs[0] == id {
				t.Fatalf("A still holds the stale %s", id)
			}
		}
	}
}

func TestStaleTailOneEntryChunks(t *testing.T) {
	staleTailWrites(t, limits{chunkEntries: 1, chunkBytes: 1 << 20}, 0, 1)
}

// At the real limits: two large matching entries fill a chunk.
func TestStaleTailDefaultLimits(t *testing.T) { staleTailWrites(t, limits{}, 400<<10, 2) }
