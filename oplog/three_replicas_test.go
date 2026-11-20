package oplog

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The operator-intent log on three replicas, the witness the third (#710
// PR 3b-4a): only a synced replica counts, a lost log is seen, the leader
// doesn't wait on a silent peer, Admit decides who may replicate, and a
// rebuilt replica is filled in chunks.

func mustSync(t *testing.T, tr *trio, i int) {
	t.Helper()
	if err := tr.nodes[i].SyncLeader(tr.ctx()); err != nil {
		t.Fatalf("n%d SyncLeader: %v", i, err)
	}
}

// An entry A and W committed while B was down is still there after W
// lost its log file and B took over (naut-lead's test for 3b-4): W comes
// back lost and counts for nothing, so B can't sync from B and W alone;
// once A is back, B syncs and holds the entry, applied once.
func TestEntryAckedByAAndWSurvivesWLosingItsLog(t *testing.T) {
	tr := newTrio(t, nil)
	mustSync(t, tr, 0)
	tr.setDown(1, true)
	if _, err := tr.propose(0, "X"); err != nil {
		t.Fatalf("A+W commit X: %v", err)
	}
	tr.loseLog(2)
	if tr.nodes[2].Synced() || tr.nodes[2].Lost() == "" {
		t.Fatalf("W after losing its log: synced %v, lost %q; want unsynced and lost", tr.nodes[2].Synced(), tr.nodes[2].Lost())
	}
	if st := tr.nodes[2].Status(); !st.Unsynced || st.Lost == "" {
		t.Fatalf("W's status doesn't say its log was lost: %+v", st)
	}
	tr.setDown(1, false)
	tr.setDown(0, true) // A dies before anyone copied X to B
	tr.lead(1)
	err := tr.nodes[1].SyncLeader(tr.ctx())
	if err == nil {
		if seq, _ := tr.holds(1, "X"); seq == 0 {
			t.Fatal("B synced from B and a W that lost its log, and X is gone")
		}
		t.Fatal("B synced with a lost W counted")
	}
	if !errors.Is(err, ErrReadOnly) {
		t.Fatalf("B's sync: %v, want ErrReadOnly", err)
	}
	tr.setDown(0, false) // A returns
	mustSync(t, tr, 1)
	if seq, times := tr.holds(1, "X"); seq == 0 || times != 1 {
		t.Fatalf("B after syncing with A: X at seq %d, %d times; want once", seq, times)
	}
	if k := tr.appliedTimes(1, "X"); k != 1 {
		t.Fatalf("B applied X %d times, want 1", k)
	}
	tr.nodes[1].Heartbeat(tr.ctx())
	if !tr.nodes[2].Synced() {
		t.Fatal("W not synced after a synced leader's heartbeat")
	}
	if seq, _ := tr.holds(2, "X"); seq == 0 {
		t.Fatal("W synced without X")
	}
	if l := tr.nodes[2].Lost(); l != "" {
		t.Fatalf("W still reports its loss (%q) once synced", l)
	}
	tr.restart(2)
	if !tr.nodes[2].Synced() || tr.nodes[2].Lost() != "" {
		t.Fatal("W's re-sync did not survive a restart")
	}
}

// A lost log under the held mark comes back non-counting: kept aside as
// .lost-<time>, the replica's own log empty, its answers marked unsynced,
// and a leader's catch-up that needs it refused, until a synced leader
// has given it the log again.
func TestLostLogUnderTheHeldMarkComesBackNonCounting(t *testing.T) {
	tr := newTrio(t, nil)
	mustSync(t, tr, 0)
	if _, err := tr.propose(0, "X"); err != nil {
		t.Fatal(err)
	}
	tr.loseLog(1)
	if got := tr.nodes[1].LastSeq(); got != 0 {
		t.Fatalf("B's log after the loss holds up to %d", got)
	}
	if tr.nodes[1].Lost() != "(missing)" {
		t.Fatalf("B's Lost = %q, want (missing)", tr.nodes[1].Lost())
	}
	// A truncated-to-empty file is lost too, and kept aside.
	tr.stop(2)
	if err := os.WriteFile(tr.logFile(2), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	tr.start(2)
	if !strings.Contains(tr.nodes[2].Lost(), ".lost-") {
		t.Fatalf("W's emptied log not kept aside: Lost = %q", tr.nodes[2].Lost())
	}
	// A, re-leading, can't count either of them.
	tr.lead(0)
	if err := tr.nodes[0].SyncLeader(tr.ctx()); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("A's sync with two lost replicas: %v, want ErrReadOnly", err)
	}
	if st := tr.nodes[0].Status(); st.Writable {
		t.Fatalf("A's status says writable with two unsynced peers: %+v", st)
	}
	// Losses survive a restart: still not counting, still reported (the
	// alarm lasts as long as the loss).
	tr.restart(1)
	if tr.nodes[1].Synced() {
		t.Fatal("B synced after a restart, without any leader")
	}
	if tr.nodes[1].Lost() != "(missing)" {
		t.Fatalf("B's loss forgotten across a restart: Lost = %q", tr.nodes[1].Lost())
	}
}

// The case a counted empty replica loses (finding 4 of the 3b-4 plan): X
// committed on A and W while B was down; W rebuilt from scratch (a new
// identity, no mark); A dies before its heartbeat copied X to B. B and the
// empty W must not sync.
func TestRebuiltWitnessDoesNotCountUntilSynced(t *testing.T) {
	tr := newTrio(t, nil)
	mustSync(t, tr, 0)
	tr.setDown(1, true)
	if _, err := tr.propose(0, "X"); err != nil {
		t.Fatal(err)
	}
	tr.wipe(2)
	if tr.nodes[2].Synced() {
		t.Fatal("a rebuilt replica starts synced")
	}
	tr.setDown(1, false)
	tr.setDown(0, true)
	tr.lead(1)
	if err := tr.nodes[1].SyncLeader(tr.ctx()); err == nil {
		seq, _ := tr.holds(1, "X")
		t.Fatalf("B synced from B and a rebuilt W (X at seq %d)", seq)
	}
	// Nor may B propose: its write would be numbered over X's seq.
	if _, err := tr.propose(1, "Y"); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("B's write with a rebuilt W: %v, want ErrReadOnly", err)
	}
	tr.setDown(0, false)
	mustSync(t, tr, 1)
	if seq, times := tr.holds(1, "X"); seq == 0 || times != 1 {
		t.Fatalf("X on B after A returned: seq %d, %d times", seq, times)
	}
}

// Only a synced leader syncs a follower: a leader whose own log was lost
// sends its heartbeat, and the follower it reaches stays unsynced.
func TestOnlyASyncedLeaderSyncsAFollower(t *testing.T) {
	tr := newTrio(t, nil)
	mustSync(t, tr, 0)
	if _, err := tr.propose(0, "X"); err != nil {
		t.Fatal(err)
	}
	tr.wipe(2)
	tr.loseLog(0)
	tr.lead(0) // an unsynced controller may lead control (3b-4b); its log may not
	tr.nodes[0].Heartbeat(tr.ctx())
	if tr.nodes[2].Synced() {
		t.Fatal("W synced by a leader whose own log was lost")
	}
	// One counting peer (B) is not enough for an unsynced leader: the
	// entry B lacks may be on the replica that doesn't count (naut-lead,
	// 3b-4a).
	if err := tr.nodes[0].SyncLeader(tr.ctx()); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("A, unsynced, synced from one counting peer: %v", err)
	}
	// B is the only replica that counts: nobody can prove B whole, so the
	// log waits for a person (two faults), and nothing is lost: B has X.
	tr.lead(1)
	if err := tr.nodes[1].SyncLeader(tr.ctx()); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("B's sync with A and W unsynced: %v", err)
	}
	if seq, _ := tr.holds(1, "X"); seq == 0 {
		t.Fatal("B lost X")
	}
}

// A write commits without waiting for a silent peer, and so does a
// takeover's catch-up: every peer is asked at once.
func TestASilentPeerDoesNotHoldUpWritesOrCatchUp(t *testing.T) {
	tr := newTrio(t, nil)
	mustSync(t, tr, 0)
	tr.net.mu.Lock()
	tr.net.silent[1] = true // B's box is gone: no answer, no refusal
	tr.net.mu.Unlock()
	start := time.Now()
	if _, err := tr.propose(0, "X"); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > tr.timeout/2 {
		t.Fatalf("a write with B silent took %v (the call timeout is %v)", d, tr.timeout)
	}
	tr.lead(0)
	start = time.Now()
	mustSync(t, tr, 0)
	if d := time.Since(start); d > tr.timeout/2 {
		t.Fatalf("a catch-up with B silent took %v", d)
	}
}

// With Admit, a replica takes replication only from the member it admits
// (a witness: the one its vote names, at that term), whatever address the
// sender claims.
func TestAdmitDecidesWhoMayReplicate(t *testing.T) {
	admitted := "n0"
	tr := newTrio(t, func(i int, o *Options) {
		if i == 2 {
			o.Admit = func(node string, term uint64) error {
				if node != admitted {
					return fmt.Errorf("my vote names %s", admitted)
				}
				return nil
			}
		}
	})
	mustSync(t, tr, 0)
	if _, err := tr.propose(0, "X"); err != nil {
		t.Fatal(err)
	}
	if seq, _ := tr.holds(2, "X"); seq == 0 {
		t.Fatal("W refused the member it admits")
	}
	tr.lead(1) // W's vote still names n0
	mustSync(t, tr, 1)
	if _, err := tr.propose(1, "Y"); err != nil {
		t.Fatal(err) // A and B
	}
	if seq, _ := tr.holds(2, "Y"); seq != 0 {
		t.Fatal("W took replication from a member it doesn't admit")
	}
}

// A rebuilt replica is filled a chunk at a time, by the leader's
// heartbeats and by its own catch-up read, page by page: neither ever
// sends the whole log in one body.
func TestResendAndCatchUpGoInChunks(t *testing.T) {
	defer func(a, b int, c, d int64) { maxChunkEntries, maxChunkBytes, maxBody, maxResp = a, b, c, d }(maxChunkEntries, maxChunkBytes, maxBody, maxResp)
	maxChunkEntries, maxChunkBytes, maxBody, maxResp = 8, 4096, 16<<10, 16<<10
	tr := newTrio(t, nil)
	mustSync(t, tr, 0)
	val, _ := json.Marshal(strings.Repeat("v", 400))
	const total = 120
	for k := range total {
		if _, err := tr.nodes[0].Propose(tr.ctx(), Entry{Kind: KindSubstitute, Tag: fmt.Sprint("t", k), Value: val, By: "op"}); err != nil {
			t.Fatal(err)
		}
	}
	tr.wipe(2)
	for range 20 {
		tr.nodes[0].Heartbeat(tr.ctx())
		if tr.nodes[2].LastSeq() == tr.nodes[0].LastSeq() {
			break
		}
	}
	if got, want := tr.nodes[2].LastSeq(), tr.nodes[0].LastSeq(); got != want || !tr.nodes[2].Synced() {
		t.Fatalf("rebuilt W after heartbeats: up to %d of %d, synced %v", got, want, tr.nodes[2].Synced())
	}
	tr.wipe(1)
	if err := tr.nodes[1].CatchUp(tr.ctx()); err != nil {
		t.Fatalf("B's paged catch-up: %v", err)
	}
	if got, want := tr.nodes[1].LastSeq(), tr.nodes[0].LastSeq(); got != want {
		t.Fatalf("B after its catch-up: up to %d of %d", got, want)
	}
}

// A reader from before pages (no page=1) still gets the whole tail in one
// answer: it would take a first page for all of it.
func TestUnpagedReaderGetsTheWholeTail(t *testing.T) {
	defer func(a, b int) { maxChunkEntries, maxChunkBytes = a, b }(maxChunkEntries, maxChunkBytes)
	maxChunkEntries, maxChunkBytes = 4, 4096
	tr := newTrio(t, nil)
	mustSync(t, tr, 0)
	for k := range 10 {
		if _, err := tr.propose(0, fmt.Sprint("a", k)); err != nil {
			t.Fatal(err)
		}
	}
	var got afterResp
	if err := tr.nodes[1].call(tr.ctx(), tr.urls[0], "/api/oplog?after=0", nil, &got); err != nil {
		t.Fatal(err)
	}
	if got.More || uint64(len(got.Entries)) != tr.nodes[0].LastSeq() {
		t.Fatalf("an unpaged read got %d of %d entries (more %v)", len(got.Entries), tr.nodes[0].LastSeq(), got.More)
	}
	if err := tr.nodes[1].call(tr.ctx(), tr.urls[0], "/api/oplog?after=0&page=1", nil, &got); err != nil {
		t.Fatal(err)
	}
	if !got.More || len(got.Entries) != 4 {
		t.Fatalf("a paged read got %d entries (more %v), want a page of 4", len(got.Entries), got.More)
	}
}

// A log damaged before its last line is not a torn write: with a mark it
// is lost, kept aside whole (not truncated), nothing in it applied.
// Without a mark the old rule stands: dropped from the bad line on.
func TestDamagedLogIsKeptAsideWhole(t *testing.T) {
	tr := newTrio(t, nil)
	mustSync(t, tr, 0)
	for _, id := range []string{"X", "Y", "Z"} {
		if _, err := tr.propose(0, id); err != nil {
			t.Fatal(err)
		}
	}
	tr.stop(1)
	data, err := os.ReadFile(tr.logFile(1))
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.SplitAfter(data, []byte("\n"))
	lines[1] = []byte("{garbage\n")
	damaged := bytes.Join(lines, nil)
	if err := os.WriteFile(tr.logFile(1), damaged, 0o600); err != nil {
		t.Fatal(err)
	}
	tr.start(1)
	kept := tr.nodes[1].Lost()
	if !strings.Contains(kept, ".lost-") || tr.nodes[1].Synced() || tr.nodes[1].LastSeq() != 0 {
		t.Fatalf("damaged log: lost %q, synced %v, last %d", kept, tr.nodes[1].Synced(), tr.nodes[1].LastSeq())
	}
	if got, _ := os.ReadFile(kept); !bytes.Equal(got, damaged) {
		t.Fatal("the damaged log was not kept whole")
	}
	if k := tr.appliedTimes(1, "X"); k != 0 {
		t.Fatalf("B applied X from a damaged log (%d times)", k)
	}
}

// A mark written for another identity is not this replica's: its log is
// kept aside as .previous-<time> and it starts empty and unsynced. A log
// from before marks keeps counting only when the caller says it is whole.
func TestIdentityAndUnmarkedLogs(t *testing.T) {
	tr := newTrio(t, nil)
	mustSync(t, tr, 0)
	if _, err := tr.propose(0, "X"); err != nil {
		t.Fatal(err)
	}
	tr.stop(1)
	tr.ident[1] = "n1-other"
	tr.start(1)
	if tr.nodes[1].Synced() || tr.nodes[1].LastSeq() != 0 {
		t.Fatal("another identity's log was taken as this replica's")
	}
	if m, _ := filepath.Glob(tr.logFile(1) + ".previous-*"); len(m) != 1 {
		t.Fatalf("the previous identity's log was not kept aside: %v", m)
	}
	// A log from before marks: no mark file.
	tr.stop(2)
	os.Remove(tr.markFile(2))
	tr.unmarked[2] = true
	tr.start(2)
	if !tr.nodes[2].Synced() || tr.nodes[2].LastSeq() == 0 {
		t.Fatal("a log from before marks, said whole, does not count")
	}
	tr.stop(2)
	os.Remove(tr.markFile(2))
	tr.unmarked[2] = false
	tr.start(2)
	if tr.nodes[2].Synced() {
		t.Fatal("an unmarked log counts without the caller saying it is whole")
	}
	m, err := readMark(tr.markFile(2))
	if err != nil || !m.Held {
		t.Fatalf("an unmarked log with entries is not marked held: %+v %v", m, err)
	}
}

// Counts lets the caller refuse a synced peer by its identity: a witness
// counts only at the incarnation the election counts.
func TestCountsRefusesAnIdentity(t *testing.T) {
	tr := newTrio(t, func(i int, o *Options) {
		o.Counts = func(peer, identity string) bool { return identity != "n2-1" }
	})
	mustSync(t, tr, 0)
	tr.setDown(1, true)
	if _, err := tr.propose(0, "X"); !errors.Is(err, ErrUnconfirmed) {
		t.Fatalf("a write stored by A and an uncounted W: %v, want ErrUnconfirmed", err)
	}
}
