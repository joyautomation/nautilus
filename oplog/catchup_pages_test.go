package oplog

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"testing"
)

// A follower's catch-up takes each page as it arrives (#1455): cut off
// after two pages, it holds those two pages' entries, uncommitted, and its
// next catch-up goes on from its commit index and completes. The commit
// index moves once, with the last page, so a lapse and the entries it
// names are applied in one range (#1466).
func TestCatchUpTakesEachPageAsItArrives(t *testing.T) {
	tr := newTrio(t, withLimits(limits{chunkEntries: 8, chunkBytes: 1 << 20}))
	mustSync(t, tr, 0)
	for k := range 40 {
		if _, err := tr.propose(0, fmt.Sprint("a", k)); err != nil {
			t.Fatal(err)
		}
	}
	tr.wipe(1)
	tr.net.mu.Lock()
	tr.net.cutAfter[0] = 2 // the leader answers two pages, then is cut off
	tr.net.mu.Unlock()
	if err := tr.nodes[1].CatchUp(tr.ctx()); err == nil {
		t.Fatal("a catch-up cut off after two pages succeeded")
	}
	if got := tr.nodes[1].LastSeq(); got != 16 {
		t.Fatalf("B holds up to %d after two pages of 8, want 16: each page is taken as it arrives", got)
	}
	if got := tr.nodes[1].Commit(); got != 0 {
		t.Fatalf("B committed to %d before the last page", got)
	}
	tr.setDown(0, false)
	if err := tr.nodes[1].CatchUp(tr.ctx()); err != nil {
		t.Fatal(err)
	}
	if b, a := tr.nodes[1], tr.nodes[0]; b.LastSeq() != a.LastSeq() || b.Commit() != a.Commit() {
		t.Fatalf("B after its second catch-up at %d/%d, the leader %d/%d", b.LastSeq(), b.Commit(), a.LastSeq(), a.Commit())
	}
	if got := tr.appliedTimes(1, "a39"); got != 1 {
		t.Fatalf("B applied the last write %d times", got)
	}
}

// A new leader far behind reads the most advanced log page by page into
// its own (#1455: it gathered every page of every peer first). Cut off
// mid-way it holds what it read, uncommitted, and is not synced; the next
// try completes.
func TestLeaderCatchUpReadsTheChosenLogPageByPage(t *testing.T) {
	tr := newTrio(t, withLimits(limits{chunkEntries: 8, chunkBytes: 1 << 20}))
	mustSync(t, tr, 0)
	if _, err := tr.propose(0, "first"); err != nil {
		t.Fatal(err)
	}
	tr.setDown(1, true)
	for k := range 40 {
		if _, err := tr.propose(0, fmt.Sprint("a", k)); err != nil {
			t.Fatal(err)
		}
	}
	tr.stop(0)
	tr.setDown(1, false)
	before, commitBefore := tr.nodes[1].LastSeq(), tr.nodes[1].Commit()
	tr.lead(1)
	tr.net.mu.Lock()
	tr.net.cutAfter[2] = 2 // W answers B's first page and one more, then is cut off
	tr.net.mu.Unlock()
	if err := tr.nodes[1].SyncLeader(tr.ctx()); err == nil {
		t.Fatal("a leader catch-up cut off mid-way succeeded")
	}
	b := tr.nodes[1]
	if got := b.LastSeq(); got != before+16 {
		t.Fatalf("B holds up to %d after two of W's pages, want %d", got, before+16)
	}
	if b.Commit() != commitBefore || b.TermSynced() {
		t.Fatalf("B committed to %d (was %d), term synced %v, before the last page", b.Commit(), commitBefore, b.TermSynced())
	}
	tr.setDown(2, false)
	mustSync(t, tr, 1)
	if w := tr.nodes[2]; b.LastSeq() < w.LastSeq() || b.Commit() < w.Commit() {
		t.Fatalf("B after its catch-up at %d/%d, W %d/%d", b.LastSeq(), b.Commit(), w.LastSeq(), w.Commit())
	}
	if seq, times := tr.holds(1, "a39"); seq == 0 || times != 1 {
		t.Fatalf("B holds W's last write %d times", times)
	}
}

// A log of any length is read: the catch-up refused one over 16,384 pages
// (about 8.4M entries at 512 a page), so past that a replacement could
// never catch up (#1455). Pages of one entry make the old limit 16,384
// entries.
func TestCatchUpHasNoPageLimit(t *testing.T) {
	if testing.Short() {
		t.Skip("reads 16,400 pages")
	}
	tr := newTrio(t, func(i int, o *Options) {
		o.lim = limits{chunkEntries: 1, chunkBytes: 1 << 20}
		o.fileSync = func(*os.File) error { return nil } // speed: the disk is not what is tested
	})
	mustSync(t, tr, 0)
	if _, err := tr.propose(0, "first"); err != nil {
		t.Fatal(err)
	}
	last, term := tr.nodes[0].LastSeq(), tr.nodes[0].After(0)[0].Term
	tr.stop(0)
	f, err := os.OpenFile(tr.logFile(0), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	w := bufio.NewWriter(f)
	const more = 16_400
	for k := range uint64(more) {
		e := Entry{Seq: last + 1 + k, Term: term, TS: 1, Kind: KindAck, IDs: []string{fmt.Sprint("x", k)}, By: "op"}
		b, _ := json.Marshal(fileLine{E: &e})
		w.Write(append(b, '\n'))
	}
	c := last + more
	b, _ := json.Marshal(fileLine{C: &c})
	w.Write(append(b, '\n'))
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	tr.start(0)
	mustSync(t, tr, 0)
	tr.wipe(1)
	if err := tr.nodes[1].CatchUp(tr.ctx()); err != nil {
		t.Fatalf("a catch-up of %d pages: %v", more, err)
	}
	if got, want := tr.nodes[1].LastSeq(), tr.nodes[0].LastSeq(); got != want {
		t.Fatalf("B caught up to %d of %d", got, want)
	}
}
