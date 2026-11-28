package oplog

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// syncSpy counts one replica's log syncs, and can hold or fail the next.
type syncSpy struct {
	who   int
	n     atomic.Int64
	mu    sync.Mutex
	hold  chan struct{} // non-nil: the next sync waits on it
	fail  int           // syncs left to fail
	onHit func()        // runs at a held or failed sync, under the node's mu
}

func (s *syncSpy) opts(i int, o *Options) {
	if i != s.who {
		return
	}
	o.fileSync = func(f *os.File) error {
		s.n.Add(1)
		s.mu.Lock()
		hold, fail, hit := s.hold, s.fail > 0, s.onHit
		s.hold = nil
		if fail {
			s.fail--
		}
		s.mu.Unlock()
		if hit != nil && (hold != nil || fail) {
			hit()
		}
		if hold != nil {
			<-hold
		}
		if fail {
			return errors.New("injected sync failure")
		}
		return f.Sync()
	}
}

// A replacement's catch-up and a lagging peer's resend sync the log once
// per batch of entries, not once per entry (#1455: on a Verdin a year's
// catch-up was about half an hour of fsyncs).
func TestCatchUpSyncsOncePerBatch(t *testing.T) {
	spy := &syncSpy{who: 1}
	both := func(i int, o *Options) {
		withLimits(limits{chunkEntries: 8, chunkBytes: 1 << 20})(i, o)
		spy.opts(i, o)
	}
	tr := newTrio(t, both)
	mustSync(t, tr, 0)
	const total = 64
	for k := range total {
		if _, err := tr.propose(0, fmt.Sprint("a", k)); err != nil {
			t.Fatal(err)
		}
	}
	tr.wipe(1)
	spy.n.Store(0)
	if err := tr.nodes[1].CatchUp(tr.ctx()); err != nil {
		t.Fatalf("B's catch-up: %v", err)
	}
	last := tr.nodes[0].LastSeq()
	if got := tr.nodes[1].LastSeq(); got != last {
		t.Fatalf("B caught up to %d of %d", got, last)
	}
	pages := int64(last/8 + 1)
	if got := spy.n.Load(); got > pages+1 {
		t.Fatalf("B's catch-up of %d entries in %d pages synced its log %d times; want at most one per page and one for the commit", last, pages, got)
	}

	// The witness rebuilt: the leader's resend fills it in chunks, one
	// request each, every request one sync for its entries and one for
	// its commit line.
	spy2 := &syncSpy{who: 2}
	tr.opts = func(i int, o *Options) {
		withLimits(limits{chunkEntries: 8, chunkBytes: 1 << 20})(i, o)
		spy2.opts(i, o)
	}
	tr.wipe(2)
	spy2.n.Store(0)
	for range 40 {
		tr.nodes[0].Heartbeat(tr.ctx())
		if tr.nodes[2].LastSeq() == last {
			break
		}
	}
	if got := tr.nodes[2].LastSeq(); got != last {
		t.Fatalf("W refilled to %d of %d", got, last)
	}
	if got := spy2.n.Load(); got > 2*(pages+2) {
		t.Fatalf("W's refill of %d entries synced its log %d times; want about two per chunk of 8", last, got)
	}
}

// No entry is acknowledged, and no commit moves, before the sync of the
// batch that holds it (#1455): the leader's write waits on the follower's
// disk, not on its memory.
func TestNoAckBeforeTheBatchSync(t *testing.T) {
	spy := &syncSpy{who: 1}
	tr := newTrio(t, spy.opts)
	mustSync(t, tr, 0)
	if _, err := tr.propose(0, "before"); err != nil {
		t.Fatal(err)
	}
	tr.setDown(2, true) // the write needs B
	release := make(chan struct{})
	var commitAtSync, lastAtSync uint64
	held := make(chan struct{})
	spy.mu.Lock()
	spy.hold = release
	spy.onHit = func() {
		// Under B's mu, in the goroutine that took the entry.
		commitAtSync, lastAtSync = tr.nodes[1].commit, tr.nodes[1].lastLocked()
		close(held)
	}
	spy.mu.Unlock()
	commitBefore := tr.nodes[1].Commit()
	done := make(chan error, 1)
	go func() { _, err := tr.propose(0, "held"); done <- err }()
	select {
	case <-held:
	case err := <-done:
		t.Fatalf("the write returned (%v) without B's log being synced", err)
	case <-time.After(5 * time.Second):
		t.Fatal("B's log was never synced for the write")
	}
	if commitAtSync != commitBefore {
		t.Fatalf("B's commit moved to %d before the batch's sync (was %d)", commitAtSync, commitBefore)
	}
	if lastAtSync != commitBefore+1 {
		t.Fatalf("at the sync B held up to %d, want the new entry %d", lastAtSync, commitBefore+1)
	}
	select {
	case err := <-done:
		t.Fatalf("the write was acknowledged (%v) while B's sync was still running", err)
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("the write after B's sync: %v", err)
	}
}

// A batch whose sync fails is not stored: it leaves memory and the file,
// so no later answer counts it, and the leader sends it again.
func TestFailedBatchSyncIsNotKept(t *testing.T) {
	spy := &syncSpy{who: 1}
	tr := newTrio(t, spy.opts)
	mustSync(t, tr, 0)
	if _, err := tr.propose(0, "before"); err != nil {
		t.Fatal(err)
	}
	tr.setDown(2, true)
	before := tr.nodes[1].LastSeq()
	st, _ := os.Stat(tr.logFile(1))
	spy.mu.Lock()
	spy.fail = 1
	spy.mu.Unlock()
	if _, err := tr.propose(0, "lost-sync"); err == nil {
		t.Fatal("a write whose only other copy failed its sync was confirmed")
	}
	if got := tr.nodes[1].LastSeq(); got != before {
		t.Fatalf("B holds up to %d after its sync failed, want %d: an answer would count an entry the disk may not hold", got, before)
	}
	if st2, _ := os.Stat(tr.logFile(1)); st2.Size() != st.Size() {
		t.Fatalf("B's file is %d bytes after a failed sync, was %d", st2.Size(), st.Size())
	}
	// Once the leader has caught up again (a missed write leaves its term
	// unsynced) its heartbeats bring the entry to B; a restart then reads
	// a whole, dense log.
	mustSync(t, tr, 0)
	for range 10 {
		tr.nodes[0].Heartbeat(tr.ctx())
		if tr.nodes[1].LastSeq() == tr.nodes[0].LastSeq() {
			break
		}
	}
	if got, want := tr.nodes[1].LastSeq(), tr.nodes[0].LastSeq(); got != want {
		t.Fatalf("B after the resend: %d of %d", got, want)
	}
	tr.restart(1)
	es := tr.nodes[1].After(0)
	for k, e := range es {
		if e.Seq != uint64(k+1) {
			t.Fatalf("B's log after a restart: entry %d is seq %d", k, e.Seq)
		}
	}
	if seq, times := tr.holds(1, "lost-sync"); seq == 0 || times != 1 {
		t.Fatalf("B holds the resent write %d times (seq %d)", times, seq)
	}
}

// A crash in the middle of a batch's write leaves whole lines and at most
// one cut short at the end: a torn write, never a lost log. The replica
// keeps what is whole, uncommitted, and its next catch-up completes.
func TestCrashMidBatchIsATornWrite(t *testing.T) {
	tr := newTrio(t, nil)
	mustSync(t, tr, 0)
	for k := range 3 {
		if _, err := tr.propose(0, fmt.Sprint("a", k)); err != nil {
			t.Fatal(err)
		}
	}
	tr.stop(1)
	pre, err := os.ReadFile(tr.logFile(1))
	if err != nil {
		t.Fatal(err)
	}
	for k := range 20 {
		if _, err := tr.propose(0, fmt.Sprint("b", k)); err != nil {
			t.Fatal(err)
		}
	}
	tr.start(1)
	if err := tr.nodes[1].CatchUp(tr.ctx()); err != nil {
		t.Fatal(err)
	}
	commitPre := uint64(0)
	full, _ := os.ReadFile(tr.logFile(1))
	tr.stop(1)
	batch := full[len(pre):] // the catch-up's entry lines, then its commit line
	n0 := tr.nodes[0]
	for cut := 1; cut < len(batch); cut += 1 + cut%13 {
		data := append(append([]byte(nil), pre...), batch[:cut]...)
		if err := os.WriteFile(tr.logFile(1), data, 0o600); err != nil {
			t.Fatal(err)
		}
		tr.start(1)
		b := tr.nodes[1]
		if b.Lost() != "" || !b.Synced() {
			t.Fatalf("cut at %d of the batch: B's log reads as lost (%q), synced %v; want a torn write", cut, b.Lost(), b.Synced())
		}
		if commitPre == 0 {
			commitPre = b.Commit()
		}
		if b.Commit() > commitPre && b.LastSeq() < n0.LastSeq() {
			t.Fatalf("cut at %d: B committed to %d with only %d of %d stored", cut, b.Commit(), b.LastSeq(), n0.LastSeq())
		}
		if err := b.CatchUp(tr.ctx()); err != nil {
			t.Fatalf("cut at %d: catch-up: %v", cut, err)
		}
		if b.LastSeq() != n0.LastSeq() || b.Commit() != n0.Commit() {
			t.Fatalf("cut at %d: B after its catch-up at %d/%d, the leader %d/%d", cut, b.LastSeq(), b.Commit(), n0.LastSeq(), n0.Commit())
		}
		tr.stop(1)
	}
}
