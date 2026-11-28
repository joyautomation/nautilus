package oplog

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

// hookedHop runs a hook before a replica's requests go out.
type hookedHop struct {
	hop
	before func(*http.Request)
}

func (h hookedHop) RoundTrip(r *http.Request) (*http.Response, error) {
	if h.before != nil {
		h.before(r)
	}
	return h.hop.RoundTrip(r)
}

// A new leader reading the chosen log page by page stops when that copy
// stops counting (Fable, #1455 review): W loses its log between B's pages
// and comes back empty, unsynced, at the same term and history. Its empty
// page must not end B's catch-up as if the log ended there: B would be
// synced on 17 of 41 entries and could commit a different entry at a seq
// A committed.
func TestLeaderCatchUpStopsWhenTheChosenCopyLosesItsLog(t *testing.T) {
	var gets atomic.Int32
	var lose func()
	tr := newTrio(t, func(i int, o *Options) {
		o.lim = limits{chunkEntries: 8, chunkBytes: 1 << 20}
		if i == 1 {
			h := o.transport.(hop)
			o.transport = hookedHop{hop: h, before: func(r *http.Request) {
				if r.URL.Host == "n2" && strings.HasPrefix(r.URL.Path, "/api/oplog") && r.Method == http.MethodGet {
					if gets.Add(1) == 3 && lose != nil {
						lose()
					}
				}
			}}
		}
	})
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
	lose = func() { tr.loseLog(2) }
	tr.lead(1)
	err := tr.nodes[1].SyncLeader(tr.ctx())
	if gets.Load() < 3 {
		t.Fatalf("B asked W for %d pages, the test needs 3", gets.Load())
	}
	if err == nil || tr.nodes[1].TermSynced() {
		t.Fatalf("B synced its term (error %v) on %d entries, with W's log lost mid-read", err, tr.nodes[1].LastSeq())
	}
}

// writeLog writes a log file of entries (each an ack of its own ID) and a
// commit mark.
func writeLog(t *testing.T, f string, commit uint64, es ...Entry) {
	t.Helper()
	var b strings.Builder
	for _, e := range es {
		l, _ := json.Marshal(fileLine{E: &e})
		b.Write(append(l, '\n'))
	}
	l, _ := json.Marshal(fileLine{C: &commit})
	b.Write(append(l, '\n'))
	if err := os.WriteFile(f, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

func ack(seq, term uint64, id string) Entry {
	return Entry{Seq: seq, Term: term, TS: 1, Kind: KindAck, IDs: []string{id}, By: "op"}
}

// A replacement of an uncommitted suffix whose rewrite fails leaves the
// old suffix in memory, as it is in the file (Fable, #1455 review): before,
// memory held the new one, the next append went on after the old one in
// the file, and an answer counted an entry no disk held.
func TestFailedRewriteKeepsTheOldSuffix(t *testing.T) {
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		t.Skip("needs a directory this process cannot write to")
	}
	dir := filepath.Join(t.TempDir(), "log")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(dir, "log.jsonl")
	writeLog(t, f, 1, ack(1, 1, "A"), ack(2, 1, "B"), ack(3, 1, "C"))
	n, err := New(Options{Self: "a", File: f})
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o700)
	n.mu.Lock()
	_, err = n.acceptLocked([]Entry{ack(2, 2, "B2")})
	n.mu.Unlock()
	if err == nil {
		t.Fatal("the rewrite in a read-only directory succeeded")
	}
	if es := n.entriesForTest(); len(es) != 3 || es[1].IDs[0] != "B" {
		t.Fatalf("after a failed rewrite memory holds %+v, want the old 1, 2, 3", es)
	}
}

// A batch that replaces a suffix (rewritten, so durable) and then appends,
// and whose final sync fails, rolls back to the rewritten log: not to the
// length before the batch, which would bring back what the rewrite
// replaced (Fable, #1455 review).
func TestRollbackAfterAMidBatchRewrite(t *testing.T) {
	f := filepath.Join(t.TempDir(), "log.jsonl")
	writeLog(t, f, 1, ack(1, 1, "A"), ack(2, 1, "B"), ack(3, 1, "C"))
	fail := true
	n, err := New(Options{Self: "a", File: f, fileSync: func(f *os.File) error {
		if fail {
			fail = false
			return fmt.Errorf("injected sync failure")
		}
		return f.Sync()
	}})
	if err != nil {
		t.Fatal(err)
	}
	n.mu.Lock()
	_, err = n.acceptLocked([]Entry{ack(2, 2, "B2"), ack(3, 2, "C2")})
	n.mu.Unlock()
	if err == nil {
		t.Fatal("a batch whose sync failed was taken")
	}
	es := n.entriesForTest()
	if len(es) != 2 || es[1].IDs[0] != "B2" {
		t.Fatalf("after the failed sync memory holds %+v, want 1 and the rewritten 2", es)
	}
	n.Close()
	n, err = New(Options{Self: "a", File: f})
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	if got := n.entriesForTest(); len(got) != 2 || got[1].IDs[0] != "B2" {
		t.Fatalf("after a restart the file holds %+v, want what memory held", got)
	}
}

// A newer term seen between the pages of a leader's catch-up (a newer
// leader's request) ends it: nothing more is taken, and the term is not
// synced (Fable, #1455 review: the pages are read without the lock, so a
// newer leader's entries could otherwise be replaced by the old log's).
func TestLeaderCatchUpStopsAtANewerTerm(t *testing.T) {
	var gets atomic.Int32
	var newer func()
	tr := newTrio(t, func(i int, o *Options) {
		o.lim = limits{chunkEntries: 8, chunkBytes: 1 << 20}
		if i == 1 {
			h := o.transport.(hop)
			o.transport = hookedHop{hop: h, before: func(r *http.Request) {
				if r.URL.Host == "n2" && strings.HasPrefix(r.URL.Path, "/api/oplog") && r.Method == http.MethodGet {
					if gets.Add(1) == 3 && newer != nil {
						newer()
					}
				}
			}}
		}
	})
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
	term := tr.lead(1)
	b := tr.nodes[1]
	newer = func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if err := b.raiseTermLocked(term + 1); err != nil {
			t.Error(err)
		}
	}
	before := b.LastSeq()
	err := b.SyncLeader(tr.ctx())
	if gets.Load() < 3 {
		t.Fatalf("B asked W for %d pages, the test needs 3", gets.Load())
	}
	if err == nil || b.TermSynced() {
		t.Fatalf("B synced term %d (error %v) after seeing term %d mid-way", term, err, term+1)
	}
	if got := b.LastSeq(); got > before+16 {
		t.Fatalf("B took entries up to %d after seeing a newer term (two pages end at %d)", got, before+16)
	}
}
