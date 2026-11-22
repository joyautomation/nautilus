package oplog

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
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
	v, _, err := tr.nodes[0].Vouch(tr.ctx(), "op", nil, VouchAccept{})
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
	if _, _, err := tr.nodes[0].Vouch(tr.ctx(), "op", nil, VouchAccept{}); err != nil {
		t.Fatal(err)
	}
	mustSync(t, tr, 0) // alone: the others answer, unsynced
	tr.lead(0)         // a new term, the others still unsynced (nothing was replicated)
	if err := tr.nodes[0].SyncLeader(tr.ctx()); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("a second catch-up alone succeeded: %v", err)
	}
}

// A vouch refuses when a peer holds a later log, naming it; when a peer
// does not answer; and for a lost log. A log already synced needs none.
func TestVouchRefuses(t *testing.T) {
	t.Run("a peer ahead that does not count", func(t *testing.T) {
		tr := newTrio(t, nil)
		mustSync(t, tr, 0)
		if _, err := tr.propose(0, "X"); err != nil {
			t.Fatal(err)
		}
		tr.wipe(1) // B: a new identity, empty, unsynced
		for _, i := range []int{0, 2} {
			// Their marks unreadable: their logs kept, held, unsynced.
			tr.stop(i)
			if err := os.WriteFile(tr.markFile(i), []byte("{"), 0o600); err != nil {
				t.Fatal(err)
			}
			tr.start(i)
		}
		_, _, err := tr.nodes[1].Vouch(tr.ctx(), "op", nil, VouchAccept{})
		if !errors.Is(err, ErrVouchRefused) || !strings.Contains(err.Error(), tr.urls[0]) || !strings.Contains(err.Error(), "does not count") {
			t.Fatalf("vouch over a later log that doesn't count: %v", err)
		}
		if tr.nodes[1].Synced() {
			t.Fatal("refused, but synced")
		}
	})
	t.Run("a peer silent", func(t *testing.T) {
		tr := unsyncedTrio(t)
		tr.setDown(2, true)
		_, _, err := tr.nodes[0].Vouch(tr.ctx(), "op", nil, VouchAccept{})
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
		_, _, err := tr.nodes[1].Vouch(tr.ctx(), "op", nil, VouchAccept{})
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
		v, _, err := tr.nodes[1].Vouch(tr.ctx(), "op", nil, VouchAccept{})
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
	if _, _, err := tr.nodes[0].Vouch(tr.ctx(), "op", nil, VouchAccept{}); err != nil {
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

// A new replica's mark is written before its log file exists: a start
// that can't write the mark leaves no log file, which a later start
// would take for a log from before marks (and count as synced).
func TestMarkIsWrittenBeforeTheLog(t *testing.T) {
	dir := t.TempDir()
	file := dir + "/log.jsonl"
	_, err := New(Options{Self: "s", File: file, Mark: dir + "/missing/log.mark", Identity: "x"})
	if err == nil {
		t.Fatal("New with a mark it can't write")
	}
	if _, err := os.Stat(file); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a log file was left without a mark: %v", err)
	}
}

// An unconfirmed write the witness alone stored (the leader's answer from
// it lost, the standby down), then the leader dies: the standby's takeover
// adopts it from the witness and commits it once, by its carrier; or, when
// it is older than the bound (Options.Lapse), names it lapsed. A copy on
// the witness proves nothing about a commit (Raft §5.4.2), so age alone
// decides (#710 3b-4b, KindLapse with the witness a replica).
func TestUnconfirmedWriteOnlyTheWitnessStored(t *testing.T) {
	for _, late := range []bool{false, true} {
		t.Run(map[bool]string{false: "within the bound", true: "too late"}[late], func(t *testing.T) {
			tr := newTrio(t, func(_ int, o *Options) {
				o.Lapse = func(Entry, time.Time) bool { return late }
			})
			mustSync(t, tr, 0)
			tr.setDown(1, true)
			tr.net.mu.Lock()
			tr.net.dropReply[2] = true
			tr.net.mu.Unlock()
			if _, err := tr.propose(0, "X"); !errors.Is(err, ErrUnconfirmed) {
				t.Fatalf("X with the standby down and the witness's answer lost: %v", err)
			}
			tr.net.mu.Lock()
			tr.net.dropReply[2] = false
			tr.net.mu.Unlock()
			if seq, _ := tr.holds(2, "X"); seq == 0 {
				t.Fatal("setup: the witness doesn't hold X")
			}
			tr.setDown(0, true) // the leader dies
			tr.setDown(1, false)
			tr.lead(1)
			mustSync(t, tr, 1)
			tr.mu.Lock()
			var applied []Entry
			for _, e := range tr.applied[1] {
				if len(e.IDs) == 1 && e.IDs[0] == "X" {
					applied = append(applied, e)
				}
			}
			tr.mu.Unlock()
			if len(applied) != 1 || applied[0].Lapsed != late {
				t.Fatalf("B applied X %d times (%+v); want once, lapsed %v", len(applied), applied, late)
			}
		})
	}
}

// A synced leader whose other copies answer but are unsynced says so
// (Uncounted), rather than that none answers.
func TestLeaderWithUnsyncedCopiesSaysUncounted(t *testing.T) {
	tr := newTrio(t, nil)
	mustSync(t, tr, 0)
	tr.wipe(1)
	tr.wipe(2)
	tr.nodes[0].Ping(tr.ctx())
	if st := tr.nodes[0].Status(); st.Writable || !st.Uncounted || st.NotSynced {
		t.Fatalf("status: %+v", st)
	}
	tr.setDown(1, true)
	tr.setDown(2, true)
	tr.nodes[0].Ping(tr.ctx())
	if st := tr.nodes[0].Status(); st.Uncounted {
		t.Fatalf("none answers, yet uncounted: %+v", st)
	}
}

// A replica opened before its identity could be known (Pending) counts
// for nothing until Rebind: the same identity as its mark's and it counts
// as the mark says; another, and its log is kept aside and it starts
// empty and unsynced; the placeholder it was opened under, and the mark is
// taken as the new identity's.
func TestRebindAPendingReplica(t *testing.T) {
	open := func(t *testing.T, dir, identity string, pending, unmarked bool) *Node {
		t.Helper()
		n, err := New(Options{Self: "s", Peers: []string{"s", "p"}, Leadership: Static{Self: "s", Leader: "s"}, Token: "t",
			File: dir + "/log.jsonl", Mark: dir + "/log.mark", Identity: identity, Pending: pending, UnmarkedSynced: unmarked})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { n.Close() })
		return n
	}
	seed := func(t *testing.T, dir string) {
		t.Helper()
		n := open(t, dir, "c/a/box1", false, true)
		n.mu.Lock()
		if _, err := n.acceptLocked([]Entry{{Seq: 1, Term: 1, Kind: KindAck, IDs: []string{"X"}}}); err != nil {
			t.Fatal(err)
		}
		n.commitTo(1)
		n.mu.Unlock()
		n.Close()
	}
	t.Run("same box", func(t *testing.T) {
		dir := t.TempDir()
		seed(t, dir)
		n := open(t, dir, "c/a/box1", true, false)
		if n.Synced() {
			t.Fatal("pending, yet synced")
		}
		if err := n.Rebind("c/a/box1", "c/a/?"); err != nil || !n.Synced() || n.LastSeq() != 1 {
			t.Fatalf("rebind to its own box: %v, synced %v, last %d", err, n.Synced(), n.LastSeq())
		}
	})
	t.Run("another box", func(t *testing.T) {
		dir := t.TempDir()
		seed(t, dir)
		n := open(t, dir, "c/a/box1", true, false)
		if err := n.Rebind("c/a/box2", "c/a/?"); err != nil || n.Synced() || n.LastSeq() != 0 {
			t.Fatalf("rebind to another box: %v, synced %v, last %d", err, n.Synced(), n.LastSeq())
		}
		if m, _ := readMark(dir + "/log.mark"); m.Identity != "c/a/box2" {
			t.Fatalf("mark %+v", m)
		}
	})
	t.Run("another box tells whoever applied the old log", func(t *testing.T) {
		dir := t.TempDir()
		seed(t, dir)
		n := open(t, dir, "c/a/box1", true, false)
		emptied := 0
		n.o.OnEmptied = func() { emptied++ }
		if err := n.Rebind("c/a/box2", "c/a/?"); err != nil || emptied != 1 {
			t.Fatalf("rebind to another box: %v, told %d times", err, emptied)
		}
		// the same box, and the placeholder, empty nothing: nobody is told
		dir2 := t.TempDir()
		seed(t, dir2)
		n2 := open(t, dir2, "c/a/box1", true, false)
		n2.o.OnEmptied = func() { emptied++ }
		n2.Rebind("c/a/box1", "c/a/?")
		if emptied != 1 {
			t.Errorf("a rebind to the same box told %d times", emptied-1)
		}
	})
	t.Run("placeholder", func(t *testing.T) {
		dir := t.TempDir()
		n := open(t, dir, "c/a/?", true, true)
		// Adopted, but not synced: nothing says the placeholder's log was
		// this box's (#1053 review).
		if err := n.Rebind("c/a/box3", "c/a/?"); err != nil || n.Synced() {
			t.Fatalf("rebind from the placeholder: %v, synced %v", err, n.Synced())
		}
		if m, _ := readMark(dir + "/log.mark"); m.Identity != "c/a/box3" {
			t.Fatalf("mark %+v", m)
		}
	})
}

// CanSync: synced, and either a vouch's catch-up is pending or another
// counting copy was heard; never for a synced copy whose others are all
// lost (#1053).
func TestCanSync(t *testing.T) {
	tr := newTrio(t, nil)
	mustSync(t, tr, 0)
	if _, err := tr.propose(0, "X"); err != nil {
		t.Fatal(err)
	}
	tr.nodes[0].Ping(tr.ctx())
	if !tr.nodes[0].CanSync(time.Second) {
		t.Fatal("a synced copy with counting peers can't sync")
	}
	tr.wipe(1)
	tr.wipe(2)
	tr.nodes[0].Ping(tr.ctx())
	if tr.nodes[0].CanSync(time.Second) {
		t.Fatal("a synced copy whose others are all lost can sync")
	}
	if tr.nodes[1].CanSync(time.Second) {
		t.Fatal("an unsynced copy can sync")
	}

	v := unsyncedTrio(t)
	if _, _, err := v.nodes[0].Vouch(v.ctx(), "op", nil, VouchAccept{}); err != nil {
		t.Fatal(err)
	}
	if !v.nodes[0].CanSync(time.Second) {
		t.Fatal("a vouched copy, its catch-up pending, can't sync")
	}
}

// The reviewer's schedule (#1053): both controllers vouched at genesis
// (the second passes: the first is level, not ahead). B leads, every copy
// answering, and commits X on B and W. B dies, and W is silent during A's
// catch-up: A's vouched catch-up must not go ahead alone and number Y over
// X. It fails until W answers, and then A holds X.
func TestDoubleVouchWithASilentWitnessKeepsTheWrite(t *testing.T) {
	tr := unsyncedTrio(t)
	for _, i := range []int{0, 1} {
		if _, _, err := tr.nodes[i].Vouch(tr.ctx(), "op", nil, VouchAccept{}); err != nil {
			t.Fatalf("vouch n%d: %v", i, err)
		}
	}
	tr.lead(1)
	mustSync(t, tr, 1)
	tr.setDown(0, true) // A partitioned: X goes to B and W only
	if _, err := tr.propose(1, "X"); err != nil {
		t.Fatalf("X on B and W: %v", err)
	}
	tr.setDown(0, false)
	tr.setDown(1, true) // B dies
	tr.net.mu.Lock()
	tr.net.silent[2] = true
	tr.net.mu.Unlock()
	tr.lead(0)
	if err := tr.nodes[0].SyncLeader(tr.ctx()); err == nil {
		if _, err := tr.propose(0, "Y"); err == nil {
			t.Logf("W after Y: %+v", tr.nodes[2].After(0))
		}
		t.Fatal("A caught up alone on its vouch with W silent")
	}
	tr.net.mu.Lock()
	tr.net.silent[2] = false
	tr.net.mu.Unlock()
	mustSync(t, tr, 0)
	if seq, n := tr.holds(0, "X"); seq == 0 || n != 1 {
		t.Fatalf("A after W answered: X at %d, %d times", seq, n)
	}
}

// A and B lost, the witness synced and longer (#1053 review, naut-lead's
// example): a vouch for A is not refused for the witness's later log,
// since the witness counts; A and the witness then count, and the
// witness's entries come back.
func TestVouchOverACountingLongerWitnessRecoversItsEntries(t *testing.T) {
	tr := newTrio(t, nil)
	mustSync(t, tr, 0)
	if _, err := tr.propose(0, "X"); err != nil {
		t.Fatal(err)
	}
	tr.wipe(0)
	tr.wipe(1)
	if _, _, err := tr.nodes[0].Vouch(tr.ctx(), "op", nil, VouchAccept{}); err != nil {
		t.Fatalf("vouch A with the witness counting and longer: %v", err)
	}
	tr.lead(0)
	mustSync(t, tr, 0)
	if seq, n := tr.holds(0, "X"); seq == 0 || n != 1 || tr.appliedTimes(0, "X") != 1 {
		t.Fatalf("A after its catch-up: X at %d (%d copies, applied %d)", seq, n, tr.appliedTimes(0, "X"))
	}
}

// "Heard recently" is within the caller's bound (a pair's lease), not the
// 10 s Status uses: a copy last heard two leases ago doesn't make a synced
// copy able to sync (#1053 review).
func TestCanSyncNeedsACopyHeardWithinTheBound(t *testing.T) {
	var mu sync.Mutex
	now := time.Unix(1_000_000, 0)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	tr := newTrio(t, func(_ int, o *Options) { o.Now = clock })
	mustSync(t, tr, 0)
	tr.nodes[0].Ping(tr.ctx())
	if !tr.nodes[0].CanSync(time.Second) {
		t.Fatal("a copy heard just now doesn't count")
	}
	mu.Lock()
	now = now.Add(2 * time.Second)
	mu.Unlock()
	if tr.nodes[0].CanSync(time.Second) {
		t.Fatal("a copy heard two leases ago still makes it able to sync")
	}
}

// A vouch re-checks after its asks: a log a synced leader synced
// meanwhile is not vouched for (no record, no one-shot catch-up).
func TestVouchSyncedMeanwhileRecordsNothing(t *testing.T) {
	var tr *trio
	tr = newTrio(t, func(i int, o *Options) {
		if i == 1 {
			o.afterVouchAsk = func() {
				n := tr.nodes[1]
				n.mu.Lock()
				n.becomeSyncedLocked("test: a synced leader's replication")
				n.mu.Unlock()
			}
		}
	})
	tr.wipe(1)
	v, _, err := tr.nodes[1].Vouch(tr.ctx(), "op", nil, VouchAccept{})
	if err != nil || v != (Vouch{}) {
		t.Fatalf("vouch for a log synced meanwhile: %+v, %v", v, err)
	}
	if m, _ := readMark(tr.markFile(1)); m.Vouch != nil || m.Alone || !m.Synced {
		t.Fatalf("mark: %+v", m)
	}
}

// A vouched catch-up doesn't go ahead alone while a copy that answers but
// doesn't count yet holds a later log (#1053: after a re-commission the
// witness's copy counts only once its quiet period is over): it waits,
// and then the witness's entries come back.
func TestVouchedCatchUpWaitsForALongerCopyToCount(t *testing.T) {
	var wCounts sync.Mutex
	counting := true
	tr := newTrio(t, func(_ int, o *Options) {
		o.Counts = func(peer, _ string) bool {
			if peer != "http://n2" {
				return true
			}
			wCounts.Lock()
			defer wCounts.Unlock()
			return counting
		}
	})
	mustSync(t, tr, 0)
	if _, err := tr.propose(0, "X"); err != nil {
		t.Fatal(err)
	}
	tr.wipe(0)
	tr.wipe(1)
	if _, _, err := tr.nodes[0].Vouch(tr.ctx(), "op", nil, VouchAccept{}); err != nil {
		t.Fatalf("vouch A, the witness counting: %v", err)
	}
	wCounts.Lock()
	counting = false // its quiet period
	wCounts.Unlock()
	tr.lead(0)
	if err := tr.nodes[0].SyncLeader(tr.ctx()); err == nil {
		t.Fatalf("A caught up alone over the witness's longer log; A holds %v", tr.nodes[0].After(0))
	}
	wCounts.Lock()
	counting = true
	wCounts.Unlock()
	mustSync(t, tr, 0)
	if seq, n := tr.holds(0, "X"); seq == 0 || n != 1 {
		t.Fatalf("A once the witness counts: X at %d, %d copies", seq, n)
	}
}
