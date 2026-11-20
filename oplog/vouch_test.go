package oplog

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// #710 PR 3b-4b on the log: a person vouches for one log (a new pair, or
// one whose logs were all lost), the term outlives a lost log, and a
// replica with a mark takes replication only from a leader its own
// election names.

// unsyncedTrio is a new pair's three logs as 3b-4b starts them: empty,
// marked, and unsynced (no UnmarkedSynced: nothing vouches for them yet).
func unsyncedTrio(t *testing.T) *trio {
	t.Helper()
	tr := newTrio(t, nil)
	for i := range 3 {
		tr.stop(i)
		os.Remove(tr.markFile(i))
		tr.unmarked[i] = false
		tr.start(i)
		if tr.nodes[i].Synced() {
			t.Fatalf("n%d starts synced", i)
		}
	}
	return tr
}

// A new pair: three empty, unsynced logs can't sync on their own; a vouch
// for the leader's lets it catch up alone, once, and its writes then sync
// the other two.
func TestVouchStartsANewPair(t *testing.T) {
	tr := unsyncedTrio(t)
	if err := tr.nodes[0].SyncLeader(tr.ctx()); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("three unsynced logs synced without a vouch: %v", err)
	}
	v, err := tr.nodes[0].Vouch(tr.ctx(), "op")
	if err != nil || v.By != "op" || v.At == 0 {
		t.Fatalf("vouch: %+v, %v", v, err)
	}
	if st := tr.nodes[0].Status(); st.Vouch == nil || st.Vouch.By != "op" || st.Unsynced {
		t.Fatalf("status after the vouch: %+v", st)
	}
	mustSync(t, tr, 0)
	if _, err := tr.propose(0, "X"); err != nil {
		t.Fatalf("a write after the vouched catch-up: %v", err)
	}
	for i := 1; i < 3; i++ {
		if !tr.nodes[i].Synced() {
			t.Fatalf("n%d not synced by the vouched leader's write", i)
		}
	}
	// The catch-up alone was one-shot: it is spent, on disk too.
	m, _ := readMark(tr.markFile(0))
	if m.Alone || m.Vouch == nil {
		t.Fatalf("mark after the vouched catch-up: %+v", m)
	}
}

// One shot: a vouched log that has caught up alone once doesn't again.
func TestVouchedCatchUpAloneIsOnce(t *testing.T) {
	tr := unsyncedTrio(t)
	if _, err := tr.nodes[0].Vouch(tr.ctx(), "op"); err != nil {
		t.Fatal(err)
	}
	tr.setDown(1, true)
	tr.setDown(2, true)
	mustSync(t, tr, 0) // alone: nothing else answers
	tr.lead(0)         // a new term, the others still away
	if err := tr.nodes[0].SyncLeader(tr.ctx()); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("a second catch-up alone succeeded: %v", err)
	}
}

// A vouch refuses when a peer holds a later log, naming it; when a peer
// does not answer; and for a lost log. A log already synced needs none.
func TestVouchRefuses(t *testing.T) {
	t.Run("a peer ahead", func(t *testing.T) {
		tr := newTrio(t, nil)
		mustSync(t, tr, 0)
		if _, err := tr.propose(0, "X"); err != nil {
			t.Fatal(err)
		}
		tr.wipe(1) // B: a new identity, empty, unsynced
		_, err := tr.nodes[1].Vouch(tr.ctx(), "op")
		if !errors.Is(err, ErrVouchRefused) || !strings.Contains(err.Error(), tr.urls[0]) || !strings.Contains(err.Error(), "later log") {
			t.Fatalf("vouch over a later log: %v", err)
		}
		if tr.nodes[1].Synced() {
			t.Fatal("refused, but synced")
		}
	})
	t.Run("a peer silent", func(t *testing.T) {
		tr := unsyncedTrio(t)
		tr.setDown(2, true)
		_, err := tr.nodes[0].Vouch(tr.ctx(), "op")
		if !errors.Is(err, ErrVouchRefused) || !strings.Contains(err.Error(), tr.urls[2]) || !strings.Contains(err.Error(), "does not answer") {
			t.Fatalf("vouch with a peer away: %v", err)
		}
	})
	t.Run("a lost log", func(t *testing.T) {
		tr := newTrio(t, nil)
		mustSync(t, tr, 0)
		if _, err := tr.propose(0, "X"); err != nil {
			t.Fatal(err)
		}
		tr.loseLog(1)
		_, err := tr.nodes[1].Vouch(tr.ctx(), "op")
		if !errors.Is(err, ErrVouchRefused) || !strings.Contains(err.Error(), "lost") {
			t.Fatalf("vouch for a lost log: %v", err)
		}
	})
	t.Run("already synced", func(t *testing.T) {
		tr := newTrio(t, nil)
		mustSync(t, tr, 0)
		if _, err := tr.propose(0, "X"); err != nil {
			t.Fatal(err)
		}
		tr.setDown(2, true) // would refuse, if it checked
		v, err := tr.nodes[1].Vouch(tr.ctx(), "op")
		if err != nil || v != (Vouch{}) {
			t.Fatalf("vouch for a synced log: %+v, %v", v, err)
		}
		if m, _ := readMark(tr.markFile(1)); m.Vouch != nil || m.Alone {
			t.Fatalf("a no-op vouch was recorded: %+v", m)
		}
	})
}

// A vouched log that holds what a synced witness holds, uncommitted: the
// vouched leader's catch-up still asks the witness, and takes its commit.
func TestVouchWithASyncedWitnessTakesItsCommit(t *testing.T) {
	tr := newTrio(t, nil)
	mustSync(t, tr, 0)
	// B leads; A and W store X, A misses the commit push.
	tr.lead(1)
	mustSync(t, tr, 1)
	tr.net.mu.Lock()
	tr.net.dropPush[0] = true
	tr.net.mu.Unlock()
	if _, err := tr.propose(1, "X"); err != nil {
		t.Fatal(err)
	}
	tr.net.mu.Lock()
	tr.net.dropPush[0] = false
	tr.net.mu.Unlock()
	if tr.nodes[0].Commit() >= tr.nodes[2].Commit() {
		t.Fatalf("setup: A's commit %d, W's %d", tr.nodes[0].Commit(), tr.nodes[2].Commit())
	}
	// A's mark becomes unreadable: its log stays, unsynced. B is wiped.
	tr.stop(0)
	if err := os.WriteFile(tr.markFile(0), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	tr.start(0)
	tr.wipe(1)
	if tr.nodes[0].Synced() {
		t.Fatal("A synced with an unreadable mark")
	}
	if _, err := tr.nodes[0].Vouch(tr.ctx(), "op"); err != nil {
		t.Fatalf("vouch A, level with W: %v", err)
	}
	tr.lead(0)
	mustSync(t, tr, 0)
	if tr.appliedTimes(0, "X") != 1 {
		t.Fatalf("A applied X %d times after its vouched catch-up, want 1 (W's commit)", tr.appliedTimes(0, "X"))
	}
}

// replicateRaw sends one replication straight to node i's handler.
func replicateRaw(t *testing.T, tr *trio, i int, req replicateReq) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(req)
	r := httptest.NewRequest(http.MethodPost, "/api/oplog/replicate", strings.NewReader(string(b)))
	r.Header.Set("X-Nautilus-Cluster", "t")
	rec := httptest.NewRecorder()
	tr.nodes[i].Handler().ServeHTTP(rec, r)
	return rec
}

// The term outlives a lost log (#1050 review carry): a replica whose log
// was lost still refuses a deposed leader's replication by its term, and
// so is never marked synced by it.
func TestLostLogKeepsItsTerm(t *testing.T) {
	tr := newTrio(t, nil)
	mustSync(t, tr, 0)
	if _, err := tr.propose(0, "X"); err != nil {
		t.Fatal(err)
	}
	term := tr.lead(0)
	mustSync(t, tr, 0)
	if _, err := tr.propose(0, "Y"); err != nil {
		t.Fatal(err)
	}
	tr.loseLog(2)
	rec := replicateRaw(t, tr, 2, replicateReq{From: tr.urls[0], Term: term - 1, Synced: true})
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "stale term") {
		t.Fatalf("a deposed term's replication to a lost log: %d %s", rec.Code, rec.Body.String())
	}
	if tr.nodes[2].Synced() {
		t.Fatal("a lost log was marked synced by a deposed leader")
	}
}

// With a mark, replication comes only from a leader the replica's own
// election names: a new identity (term 0) whose election names no leader
// does not take a synced leader's word.
func TestMarkedReplicaRefusesAnUnnamedLeader(t *testing.T) {
	tr := newTrio(t, nil)
	mustSync(t, tr, 0)
	if _, err := tr.propose(0, "X"); err != nil {
		t.Fatal(err)
	}
	tr.wipe(2)
	tr.m.set("", tr.m.term)
	rec := replicateRaw(t, tr, 2, replicateReq{From: tr.urls[0], Term: 1, Synced: true})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("replication with no leader named: %d %s", rec.Code, rec.Body.String())
	}
	if tr.nodes[2].Synced() {
		t.Fatal("marked synced by a leader its election does not name")
	}
}

// A follower whose leader's log is not synced is not writable, and says
// so: a write would only end "outcome unknown".
func TestFollowerOfAnUnsyncedLeaderIsNotWritable(t *testing.T) {
	tr := unsyncedTrio(t)
	tr.nodes[1].Ping(tr.ctx())
	st := tr.nodes[1].Status()
	if st.Writable || !st.NotSynced {
		t.Fatalf("follower of an unsynced leader: %+v", st)
	}
	if st := tr.nodes[0].Status(); st.Writable || !st.NotSynced {
		t.Fatalf("the unsynced leader itself: %+v", st)
	}
}
