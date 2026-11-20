package oplog

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// #1050's Opus review: the holes it found, each as a test.

// unsyncedLeaderSetup is the reviewer's schedule up to the moment the
// stale leader is back: n0 stored x@1 alone in term 1 (ErrUnconfirmed);
// n1 led term 2 and committed E@1 on n1 and n2, and n2 missed the commit
// push, so it holds E uncommitted; n0 leads term 3, with n1 down, and has
// not caught up yet.
//
// seenTerm2: n0 also took a heartbeat of term 2 (its term rises to 2) but
// lost the resend after it, so it still holds x, not E: a follower's term
// check alone no longer stops a pull from it.
func unsyncedLeaderSetup(t *testing.T, seenTerm2 bool) *trio {
	t.Helper()
	tr := newTrio(t, nil)
	mustSync(t, tr, 0)
	tr.setDown(1, true)
	tr.setDown(2, true)
	if _, err := tr.propose(0, "x"); !errors.Is(err, ErrUnconfirmed) {
		t.Fatalf("x alone on n0: %v", err)
	}
	tr.setDown(0, true)
	tr.setDown(1, false)
	tr.setDown(2, false)
	tr.lead(1)
	mustSync(t, tr, 1)
	tr.net.mu.Lock()
	tr.net.dropPush[2] = true
	tr.net.mu.Unlock()
	if _, err := tr.propose(1, "E"); err != nil {
		t.Fatalf("E on n1 and n2: %v", err)
	}
	if seenTerm2 {
		tr.setDown(0, false)
		tr.net.mu.Lock()
		tr.net.cutAfter[0] = 1 // the heartbeat reaches it, the resend doesn't
		tr.net.mu.Unlock()
		tr.nodes[1].Heartbeat(tr.ctx())
		n0 := tr.nodes[0]
		n0.mu.Lock()
		term := n0.term
		n0.mu.Unlock()
		if seq, _ := tr.holds(0, "E"); seq != 0 || term != 2 {
			t.Fatalf("setup: n0 at term %d, E at %d", term, seq)
		}
	}
	tr.net.mu.Lock()
	tr.net.dropPush[2] = false
	tr.net.mu.Unlock()
	if tr.nodes[2].Commit() != 0 {
		t.Fatal("setup: n2 learnt E's commit")
	}
	tr.setDown(1, true)
	tr.setDown(0, false)
	tr.lead(0)
	return tr
}

// A leader that has not caught up for its term sends nothing: its
// heartbeat would have the follower replace E (committed in term 2, its
// push lost) with its own leftover x, and its catch-up then commit x.
func TestUnsyncedLeaderHeartbeatDoesNotOverwriteACommittedEntry(t *testing.T) {
	tr := unsyncedLeaderSetup(t, false)
	tr.nodes[0].Heartbeat(tr.ctx()) // before SyncLeader, as driveLog's ticker can
	mustSync(t, tr, 0)
	if seq, _ := tr.holds(0, "E"); seq != 1 {
		t.Fatalf("n0 synced without E at seq 1 (holds it at %d); x at %d", seq, func() uint64 { s, _ := tr.holds(0, "x"); return s }())
	}
}

// Nor does a follower take its entries by pulling.
func TestFollowerTakesNothingFromAnUnsyncedLeader(t *testing.T) {
	tr := unsyncedLeaderSetup(t, true)
	if err := tr.nodes[2].CatchUp(tr.ctx()); err == nil {
		if seq, _ := tr.holds(2, "E"); seq == 0 {
			t.Fatal("n2 pulled from a leader not caught up for its term, and lost E")
		}
	}
	mustSync(t, tr, 0)
	if seq, _ := tr.holds(0, "E"); seq != 1 {
		t.Fatalf("n0 synced without E at seq 1 (holds it at %d)", seq)
	}
	// Once caught up, its followers take from it again.
	if err := tr.nodes[2].CatchUp(tr.ctx()); err != nil {
		t.Fatalf("n2's pull from a synced leader: %v", err)
	}
}

// A peer that took everything it was sent within the rounds but still ends
// short of the leader's log does not hold the new entry, so it must not
// count: here the write is stored on A alone.
func TestAPeerStillBehindDoesNotCount(t *testing.T) {
	defer func(a int) { maxChunkEntries = a }(maxChunkEntries)
	maxChunkEntries = 1
	tr := newTrio(t, nil)
	mustSync(t, tr, 0)
	tr.setDown(1, true)
	for k := range 100 {
		if _, err := tr.propose(0, "a"+string(rune('A'+k%26))+string(rune('a'+k/26))); err != nil {
			t.Fatal(err)
		}
	}
	tr.setDown(1, false)
	tr.setDown(2, true)
	_, err := tr.propose(0, "E")
	if seq, _ := tr.holds(1, "E"); err == nil && seq == 0 {
		t.Fatalf("E answered committed while B holds up to %d without it", tr.nodes[1].LastSeq())
	}
	if !errors.Is(err, ErrUnconfirmed) {
		t.Fatalf("E with W down and B behind: %v, want ErrUnconfirmed", err)
	}
}

// A rebuilt replica that has taken the first chunk only (it ends below the
// leader's commit index) is not synced: its log would still miss committed
// entries.
func TestFirstChunkDoesNotSyncARebuiltReplica(t *testing.T) {
	defer func(a int) { maxChunkEntries = a }(maxChunkEntries)
	maxChunkEntries = 2
	tr := newTrio(t, nil)
	mustSync(t, tr, 0)
	for _, id := range []string{"a", "b", "c", "d", "e", "f"} {
		if _, err := tr.propose(0, id); err != nil {
			t.Fatal(err)
		}
	}
	tr.wipe(2)
	// The heartbeat (a gap), then one chunk; then W goes down.
	tr.net.mu.Lock()
	tr.net.cutAfter[2] = 2
	tr.net.mu.Unlock()
	tr.nodes[0].Heartbeat(tr.ctx())
	if got := tr.nodes[2].LastSeq(); got == 0 || got >= tr.nodes[0].Commit() {
		t.Fatalf("setup: W took up to %d of %d", got, tr.nodes[0].Commit())
	}
	if tr.nodes[2].Synced() {
		t.Fatalf("W synced after one chunk, holding up to %d of the %d committed", tr.nodes[2].LastSeq(), tr.nodes[0].Commit())
	}
	tr.setDown(2, false)
	tr.nodes[0].Heartbeat(tr.ctx())
	if !tr.nodes[2].Synced() {
		t.Fatal("W not synced once it has the whole log")
	}
}

// An entry over the size cap is refused at its proposal, before anything
// is stored: one stored could not be read back (the file's line limit) or
// fetched (a reply's cap).
func TestTooLargeEntryIsRefusedAtProposal(t *testing.T) {
	tr := newTrio(t, nil)
	mustSync(t, tr, 0)
	big, _ := json.Marshal(strings.Repeat("v", maxEntryBytes))
	for _, at := range []int{0, 1} { // the leader, and a follower forwarding
		_, err := tr.nodes[at].Propose(tr.ctx(), Entry{Kind: KindSubstitute, Tag: "t", Value: big, By: "op"})
		if !errors.Is(err, ErrTooLarge) {
			t.Fatalf("a %d-byte entry at n%d: %v, want ErrTooLarge", len(big), at, err)
		}
	}
	if got := tr.nodes[0].LastSeq(); got != 0 {
		t.Fatalf("the leader stored a refused entry (last %d)", got)
	}
	if maxResp < maxBody {
		t.Fatalf("a reply cap (%d) under the body cap (%d): a body one replica took may not be fetched", maxResp, maxBody)
	}
}

// A follower marks itself synced only from a replication that says its
// leader is synced for its term: the same entries from a leader that isn't
// leave it unsynced. (An unsynced leader sends nothing at all now, so this
// is the follower's own rule, pinned on its handler.)
func TestFollowerSyncsOnlyFromASyncedLeadersRequest(t *testing.T) {
	tr := newTrio(t, nil)
	mustSync(t, tr, 0)
	if _, err := tr.propose(0, "X"); err != nil {
		t.Fatal(err)
	}
	tr.wipe(2)
	a := tr.nodes[0]
	req := replicateReq{From: tr.urls[0], Node: "n0", Term: tr.m.term, Commit: a.Commit(), Entries: a.After(0)}
	var resp replicateResp
	if err := a.call(tr.ctx(), tr.urls[2], "/api/oplog/replicate", req, &resp); err != nil {
		t.Fatal(err)
	}
	if tr.nodes[2].Synced() || !resp.Unsynced {
		t.Fatal("W synced from a request whose leader says it is not synced")
	}
	req.Synced = true
	resp = replicateResp{} // omitempty: a reused value would keep the first answer's flag
	if err := a.call(tr.ctx(), tr.urls[2], "/api/oplog/replicate", req, &resp); err != nil {
		t.Fatal(err)
	}
	if !tr.nodes[2].Synced() || resp.Unsynced {
		t.Fatal("W not synced from a synced leader's request proving its log")
	}
}
