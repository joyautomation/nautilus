//go:build !js

package oplog

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Compaction carried out (#1455 PR 2a-1, docs/design/oplog-compaction.md):
// the oracle is the full log as it was before any compaction. A compacted
// copy must hold, serve and replay exactly the kept entries up to Through
// and the whole tail, entry for entry; and a toy consumer, keeping by a toy
// dead rule, must end where a full replay does.

// ctrio is a trio with compaction on, and every entry the leader ever held
// (hist), by seq: the uncompacted log.
type ctrio struct {
	*trio
	hist map[uint64]Entry
}

func compactTrio(t *testing.T, opts func(i int, o *Options)) *ctrio {
	t.Helper()
	tr := newTrio(t, func(i int, o *Options) {
		o.CompactRules = 1
		o.lim.compactDelay = func() time.Duration { return 0 }
		o.lim.compactRetry = 20 * time.Millisecond
		if opts != nil {
			opts(i, o)
		}
	})
	return &ctrio{trio: tr, hist: map[uint64]Entry{}}
}

// toy entries: a key set to a value, or deleted. The toy consumer's state
// is the map; an entry up to Through is dead unless it is its key's last
// one there and a set. Notes are always kept, acks never.
const kindToy = "toy"

func toyEntry(key, op string, v int) Entry {
	b, _ := json.Marshal(v)
	return Entry{Kind: kindToy, ID: key, Tag: op, Value: b, By: "op"}
}

// fill proposes n random entries at replica i and records the log.
func (tr *ctrio) fill(i, n int, seed uint64) []Entry {
	tr.t.Helper()
	r := rand.New(rand.NewPCG(seed, 7))
	for k := range n {
		var e Entry
		switch p := r.IntN(10); {
		case p < 5:
			e = toyEntry(fmt.Sprintf("k%d", r.IntN(6)), "set", r.IntN(1000))
		case p < 7:
			e = toyEntry(fmt.Sprintf("k%d", r.IntN(6)), "del", 0)
		case p < 9:
			e = NoteEntryOf("op", NoteEntry{ID: fmt.Sprintf("n%d-%d", seed, k), Rev: 1, Text: "a note"})
		default:
			e = Entry{Kind: KindAck, IDs: []string{fmt.Sprintf("a%d", k)}, By: "op"}
		}
		if _, err := tr.nodes[i].Propose(tr.ctx(), e); err != nil {
			tr.t.Fatalf("propose %d: %v", k, err)
		}
	}
	tr.record(i)
	return tr.full()
}

// record adds what replica i holds to the uncompacted log.
func (tr *ctrio) record(i int) {
	for _, e := range tr.nodes[i].After(0) {
		tr.hist[e.Seq] = e
	}
}

// full is the uncompacted log, in order.
func (tr *ctrio) full() []Entry {
	out := make([]Entry, 0, len(tr.hist))
	for _, e := range tr.hist {
		out = append(out, e)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Seq < out[b].Seq })
	return out
}

// toyKeep is the keep list the toy rule gives for the log up to through.
func toyKeep(full []Entry, through uint64, _ int) []uint64 {
	last := map[string]Entry{}
	var keep []uint64
	for _, e := range full {
		if e.Seq > through {
			break
		}
		switch e.Kind {
		case kindToy:
			last[e.ID] = e
		case KindNote:
			keep = append(keep, e.Seq)
		}
	}
	for _, e := range last {
		if e.Tag == "set" {
			keep = append(keep, e.Seq)
		}
	}
	slices.Sort(keep)
	return keep
}

func toyState(es []Entry) map[string]string {
	st := map[string]string{}
	for _, e := range es {
		if e.Kind != kindToy {
			continue
		}
		if e.Tag == "set" {
			st[e.ID] = string(e.Value)
		} else {
			delete(st, e.ID)
		}
	}
	return st
}

// compact proposes a compaction at replica i and waits until every copy
// has rewritten its file to it.
func (tr *ctrio) compact(i int, through uint64, keep []uint64) Entry {
	tr.t.Helper()
	k, err := tr.nodes[i].proposeCompact(tr.ctx(), through, keep)
	if err != nil {
		tr.t.Fatalf("compact through %d: %v", through, err)
	}
	tr.hist[k.Seq] = k
	tr.nodes[i].Heartbeat(tr.ctx())
	tr.settle()
	return k
}

// settle waits for every running copy's file to hold its newest base.
func (tr *ctrio) settle() {
	tr.t.Helper()
	for j, n := range tr.nodes {
		if n == nil {
			continue
		}
		if err := n.compactedForTest(tr.ctx()); err != nil {
			tr.t.Fatalf("n%d never rewrote its file: %v", j, err)
		}
	}
}

// expect is the uncompacted log filtered to the kept entries up to through
// and the whole tail.
func (tr *ctrio) expect(through uint64, keep []uint64) []Entry {
	var out []Entry
	for _, e := range tr.full() {
		if e.Seq > through || slices.Contains(keep, e.Seq) {
			out = append(out, e)
		}
	}
	return out
}

func entryJSON(es []Entry) []string {
	out := make([]string, len(es))
	for i, e := range es {
		b, _ := json.Marshal(e)
		out[i] = string(b)
	}
	return out
}

// applies is what a replay of want applies (Apply never sees a noop or a
// compaction).
func applies(want []Entry) []Entry {
	var out []Entry
	for _, e := range want {
		if e.Kind != KindNoop && e.Kind != KindCompact {
			out = append(out, e)
		}
	}
	return out
}

// check: replica i holds want, and so does its file, and a restart replays
// it, ending in the toy state of the full log.
func (tr *ctrio) check(i int, want []Entry) {
	tr.t.Helper()
	if got := entryJSON(tr.nodes[i].After(0)); !slices.Equal(got, entryJSON(want)) {
		tr.t.Fatalf("n%d holds\n%v\nwant\n%v", i, got, entryJSON(want))
	}
	read, err := ReadCommitted(tr.logFile(i))
	if err != nil {
		tr.t.Fatalf("n%d ReadCommitted: %v", i, err)
	}
	if got := entryJSON(read); !slices.Equal(got, entryJSON(want)) {
		tr.t.Fatalf("n%d's file reads\n%v\nwant\n%v", i, got, entryJSON(want))
	}
	tr.restart(i)
	tr.mu.Lock()
	applied := slices.Clone(tr.applied[i])
	tr.mu.Unlock()
	if got := entryJSON(applied); !slices.Equal(got, entryJSON(applies(want))) {
		tr.t.Fatalf("n%d replayed\n%v\nwant\n%v", i, got, entryJSON(applies(want)))
	}
	if got, full := toyState(applied), toyState(tr.full()); fmt.Sprint(got) != fmt.Sprint(full) {
		tr.t.Fatalf("n%d's toy state after a compacted replay %v, after the full log %v", i, got, full)
	}
	if st := tr.nodes[i].Status(); st.Unsynced || st.Lost != "" || st.Diverged != "" {
		tr.t.Fatalf("n%d after restart: %+v", i, st)
	}
}

func TestCompactedCopyHoldsServesAndReplaysTheKeptLog(t *testing.T) {
	for seed := range uint64(12) {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			tr := compactTrio(t, nil)
			mustSync(t, tr.trio, 0)
			r := rand.New(rand.NewPCG(seed, 1))
			full := tr.fill(0, 20+r.IntN(40), seed)
			through := 1 + r.Uint64N(tr.nodes[0].Commit()-1)
			keep := toyKeep(full, through, 0)
			tr.compact(0, through, keep)
			for i := range 3 {
				tr.check(i, tr.expect(through, keep))
			}
			// A second compaction over the first: some kept entries are
			// dropped now, the first K among them.
			mustSync(t, tr.trio, 0)
			full = tr.fill(0, 10+r.IntN(30), seed+100)
			through2 := tr.nodes[0].Commit() - r.Uint64N(5)
			keep2 := toyKeep(full, through2, 0)
			tr.compact(0, through2, keep2)
			for i := range 3 {
				tr.check(i, tr.expect(through2, keep2))
			}
		})
	}
}

// Several committed Ks a copy never rewrote its file for are all carried
// out at its next start, in order.
func TestSeveralCommittedCompactionsCarriedOutAtStart(t *testing.T) {
	var slow atomic.Bool
	slow.Store(true)
	tr := compactTrio(t, func(i int, o *Options) {
		if i == 1 {
			o.lim.compactDelay = func() time.Duration {
				if slow.Load() {
					return time.Hour
				}
				return 0
			}
		}
	})
	mustSync(t, tr.trio, 0)
	full := tr.fill(0, 30, 3)
	k1 := toyKeep(full, 10, 0)
	if _, err := tr.nodes[0].proposeCompact(tr.ctx(), 10, k1); err != nil {
		t.Fatal(err)
	}
	tr.record(0)
	full = tr.fill(0, 10, 4)
	k2 := toyKeep(full, 25, 0)
	if _, err := tr.nodes[0].proposeCompact(tr.ctx(), 25, k2); err != nil {
		t.Fatal(err)
	}
	tr.record(0)
	tr.nodes[0].Heartbeat(tr.ctx())
	if st := tr.nodes[1].Status(); st.CompactThrough != 25 || !st.CompactPending {
		t.Fatalf("B before its rewrite: %+v; want the base at 25, the file pending", st)
	}
	if b, _ := os.ReadFile(tr.logFile(1)); strings.HasPrefix(string(b), `{"b":`) {
		t.Fatal("B's file was rewritten while its worker waits")
	}
	slow.Store(false)
	tr.restart(1)
	tr.settle()
	tr.check(1, tr.expect(25, k2))
}

// A held K is a commit floor: a copy holding one, its commit line below its
// Through, replays up to Through at open, and a new leader's lapse names
// nothing at or below it, nor the K.
func TestAHeldCompactionIsACommitFloor(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "log.jsonl")
	var lines []byte
	for s := uint64(1); s <= 8; s++ {
		e := toyEntry("k", "set", int(s))
		e.Seq, e.Term, e.TS = s, 1, 1
		lines = appendLine(lines, fileLine{E: &e})
	}
	c := uint64(2)
	lines = appendLine(lines, fileLine{C: &c})
	body, _ := json.Marshal(compactBody{V: 1, Rules: 1, Through: 6, ThroughTerm: 1, Keep: [][2]uint64{{6, 6}}, Kept: 1})
	k := Entry{Seq: 9, Term: 1, TS: 1, Kind: KindCompact, By: "system", Value: body}
	lines = appendLine(lines, fileLine{E: &k})
	if err := os.WriteFile(f, lines, 0o600); err != nil {
		t.Fatal(err)
	}
	var applied []uint64
	n, err := New(Options{Self: "http://a", File: f, Apply: func(e Entry) error { applied = append(applied, e.Seq); return nil },
		Lapse: func(Entry, time.Time) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	if n.Commit() != 6 || !slices.Equal(applied, []uint64{1, 2, 3, 4, 5, 6}) {
		t.Fatalf("commit %d, applied %v; want 6 and 1..6 (the held K's Through is committed)", n.Commit(), applied)
	}
	n.mu.Lock()
	c2 := n.carrierLocked(2)
	n.mu.Unlock()
	if c2.Kind != KindLapse || !slices.Equal(c2.Refs, []uint64{7, 8}) {
		t.Fatalf("a new leader's carrier %+v; want a lapse of 7 and 8 only (not the floor, not the K)", c2)
	}

	// In the process too: a copy that took the K by a catch-up page,
	// committed below it.
	m, err := New(Options{Self: "http://b", Lapse: func(Entry, time.Time) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	m.mu.Lock()
	var es []Entry
	for s := uint64(1); s <= 8; s++ {
		e := toyEntry("k", "set", int(s))
		e.Seq, e.Term = s, 1
		es = append(es, e)
	}
	if _, err := m.acceptLocked(append(es, k)); err != nil {
		t.Fatal(err)
	}
	m.commitTo(2)
	c3 := m.carrierLocked(2)
	m.mu.Unlock()
	if c3.Kind != KindLapse || !slices.Equal(c3.Refs, []uint64{7, 8}) {
		t.Fatalf("in process, a new leader's carrier %+v; want a lapse of 7 and 8 only", c3)
	}
}

// A compaction never splits a lapse from the entries it names: a Through
// between them is refused, one past the lapse is not.
func TestCompactionDoesNotSplitALapse(t *testing.T) {
	tr := compactTrio(t, func(_ int, o *Options) { o.Lapse = func(Entry, time.Time) bool { return true } })
	mustSync(t, tr.trio, 0)
	tr.fill(0, 5, 1)
	tr.setDown(1, true)
	tr.setDown(2, true)
	if _, err := tr.propose(0, "LATE"); !errors.Is(err, ErrUnconfirmed) {
		t.Fatalf("a write with both peers down: %v", err)
	}
	named, _ := tr.holds(0, "LATE")
	tr.setDown(1, false)
	tr.setDown(2, false)
	tr.lead(0)
	mustSync(t, tr.trio, 0)
	tr.record(0)
	var lapse uint64
	for _, e := range tr.full() {
		if e.Kind == KindLapse && slices.Contains(e.Refs, named) {
			lapse = e.Seq
		}
	}
	if lapse == 0 {
		t.Fatalf("no lapse names %d: %v", named, entryJSON(tr.full()))
	}
	tr.fill(0, 3, 2)
	for _, through := range []uint64{named, lapse - 1} {
		if _, err := tr.nodes[0].proposeCompact(tr.ctx(), through, nil); err == nil || !strings.Contains(err.Error(), "the lapse at") {
			t.Fatalf("compact through %d, between %d and its lapse at %d: %v; want refused", through, named, lapse, err)
		}
	}
	keep := toyKeep(tr.full(), lapse, 0)
	tr.compact(0, lapse, keep)
	for i := range 3 {
		tr.check(i, tr.expect(lapse, keep))
	}
}

// A crash at any step of the rewrite leaves a log that opens whole, not
// lost, and the next start finishes the compaction.
func TestCompactionSurvivesACrashAtEveryStep(t *testing.T) {
	for _, step := range []string{"written", "synced", "delta", "precompact", "renamed", "dirsynced"} {
		t.Run(step, func(t *testing.T) {
			var crash atomic.Bool
			crash.Store(true)
			tr := compactTrio(t, func(i int, o *Options) {
				if i == 1 {
					o.compactCrash = func(s string) bool { return s == step && crash.Load() }
					o.lim.compactRetry = time.Hour
				}
			})
			mustSync(t, tr.trio, 0)
			full := tr.fill(0, 30, 5)
			keep := toyKeep(full, 20, 0)
			k, err := tr.nodes[0].proposeCompact(tr.ctx(), 20, keep)
			if err != nil {
				t.Fatal(err)
			}
			tr.hist[k.Seq] = k
			tr.nodes[0].Heartbeat(tr.ctx())
			deadline := time.Now().Add(5 * time.Second)
			for tr.nodes[1].Status().CompactThrough != 20 && time.Now().Before(deadline) {
				time.Sleep(5 * time.Millisecond)
			}
			time.Sleep(50 * time.Millisecond) // the worker reaches the crash point
			crash.Store(false)
			tr.restart(1) // what a crash leaves: the files as they stand
			if st := tr.nodes[1].Status(); st.Lost != "" || st.Unsynced {
				t.Fatalf("B after a crash at %s: %+v", step, st)
			}
			tr.settle()
			tr.check(1, tr.expect(20, keep))
			if pre, _ := filepath.Glob(tr.logFile(1) + ".precompact-*"); len(pre) != 1 {
				t.Fatalf("B keeps %v from before the compaction; want one", pre)
			}
		})
	}
}

// The rewrite holds the node's lock only to swap the file in: while the new
// file's sync is blocked, the copy still answers.
func TestTheLockIsFreeWhileTheRewriteSyncs(t *testing.T) {
	block := make(chan struct{})
	var blocked atomic.Bool
	tr := compactTrio(t, func(i int, o *Options) {
		if i == 1 {
			o.fileSync = func(f *os.File) error {
				if strings.HasSuffix(f.Name(), ".compact") && blocked.CompareAndSwap(false, true) {
					<-block
				}
				return f.Sync()
			}
		}
	})
	mustSync(t, tr.trio, 0)
	full := tr.fill(0, 30, 6)
	if _, err := tr.nodes[0].proposeCompact(tr.ctx(), 20, toyKeep(full, 20, 0)); err != nil {
		t.Fatal(err)
	}
	tr.nodes[0].Heartbeat(tr.ctx())
	for !blocked.Load() {
		time.Sleep(time.Millisecond)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = tr.nodes[1].Synced()
		_ = tr.nodes[1].Status()
		if _, err := tr.propose(0, "WHILE"); err != nil {
			t.Errorf("a write while B's rewrite syncs: %v", err)
		}
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("B did not answer while its rewrite's sync was blocked")
	}
	if seq, _ := tr.holds(1, "WHILE"); seq == 0 {
		t.Fatal("B did not take the write while its rewrite synced")
	}
	close(block)
	tr.record(0)
	tr.settle()
	tr.check(1, tr.expect(20, toyKeep(full, 20, 0)))
}

// A build overtaken by a log replaced wholesale (the witness reset in place
// to a newer history) is thrown away, never renamed over the new log.
func TestARewriteOvertakenByAResetIsThrownAway(t *testing.T) {
	block := make(chan struct{})
	var blocked atomic.Bool
	tr := compactTrio(t, func(i int, o *Options) {
		if i == 2 {
			o.ResetInPlace = true
			o.fileSync = func(f *os.File) error {
				if strings.HasSuffix(f.Name(), ".compact") && blocked.CompareAndSwap(false, true) {
					<-block
				}
				return f.Sync()
			}
		}
	})
	mustSync(t, tr.trio, 0)
	full := tr.fill(0, 30, 7)
	if _, err := tr.nodes[0].proposeCompact(tr.ctx(), 20, toyKeep(full, 20, 0)); err != nil {
		t.Fatal(err)
	}
	tr.nodes[0].Heartbeat(tr.ctx())
	for !blocked.Load() {
		time.Sleep(time.Millisecond)
	}
	w := tr.nodes[2]
	w.mu.Lock()
	err := w.learnHistoryLocked(Lineage{History: 1, HistoryID: 42, HistoryBy: "someone"})
	w.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	close(block)
	time.Sleep(100 * time.Millisecond)
	if es := w.After(0); len(es) != 0 {
		t.Fatalf("W after its reset holds %d entries; want none (the old log's rewrite was thrown away)", len(es))
	}
	if b, _ := os.ReadFile(tr.logFile(2)); len(b) != 0 {
		t.Fatalf("W's new log file holds %.80q; want it empty", b)
	}
}

// A copy a compaction does not fit has diverged: it counts for nothing and
// takes nothing, and its next start moves its log aside and starts empty;
// the witness's starts over at once.
func TestACompactionThatDoesNotFitDiverges(t *testing.T) {
	tr := compactTrio(t, func(i int, o *Options) { o.ResetInPlace = i == 2 })
	mustSync(t, tr.trio, 0)
	full := tr.fill(0, 30, 8)
	keep := toyKeep(full, 20, 0)
	for _, i := range []int{1, 2} {
		// The copy holds a kept entry at another term (as a copy whose log
		// is not the others' would).
		n := tr.nodes[i]
		n.mu.Lock()
		n.entries[n.indexAfterLocked(keep[0]-1)].Term = 99
		n.mu.Unlock()
	}
	if _, err := tr.nodes[0].proposeCompact(tr.ctx(), 20, keep); err != nil {
		t.Fatal(err)
	}
	tr.nodes[0].Heartbeat(tr.ctx())
	b := tr.nodes[1]
	st := b.Status()
	if st.Diverged == "" || !st.Unsynced {
		t.Fatalf("B, its log not A's: %+v; want diverged and unsynced", st)
	}
	if aside, _ := filepath.Glob(tr.logFile(2) + ".diverged-*"); len(aside) != 1 {
		t.Fatalf("W, diverged, should start over at once: aside %v", aside)
	}
	// W, started over empty, is refilled by the leader's push (#1455 PR
	// 2a-2), within that same heartbeat's resend, and syncs from it; B
	// takes nothing.
	if _, err := tr.propose(0, "AFTER"); err != nil {
		t.Fatalf("a write with W refilled by push: %v", err)
	}
	if seq, _ := tr.holds(1, "AFTER"); seq != 0 {
		t.Fatal("B, diverged, took a write")
	}
	if seq, _ := tr.holds(2, "AFTER"); seq == 0 {
		t.Fatal("W, refilled, does not hold the write")
	}
	if w := tr.nodes[2].Status(); w.Diverged != "" || w.Unsynced || w.CompactThrough != 20 {
		t.Fatalf("W refilled: %+v; want synced at the base", w)
	}
	tr.restart(1)
	st = tr.nodes[1].Status()
	aside, _ := filepath.Glob(tr.logFile(1) + ".diverged-*")
	if st.Diverged != "" || st.Lost == "" || st.LastSeq != 0 || len(aside) != 1 {
		t.Fatalf("B restarted: %+v, aside %v; want an empty log, the old one aside", st, aside)
	}
}

// A file put back from before its last compaction is a lost log, as a
// missing one is; so is one whose header does not match its base.
func TestAnOlderOrDamagedCompactedFileIsLost(t *testing.T) {
	tr := compactTrio(t, nil)
	mustSync(t, tr.trio, 0)
	full := tr.fill(0, 30, 9)
	tr.compact(0, 20, toyKeep(full, 20, 0))
	pre, _ := filepath.Glob(tr.logFile(1) + ".precompact-*")
	if len(pre) != 1 {
		t.Fatalf("B keeps %v from before the compaction", pre)
	}
	tr.stop(1)
	if err := os.Rename(pre[0], tr.logFile(1)); err != nil {
		t.Fatal(err)
	}
	tr.start(1)
	if st := tr.nodes[1].Status(); st.Lost == "" || !st.Unsynced {
		t.Fatalf("B on its log from before the compaction: %+v; want lost", st)
	}

	tr.stop(2)
	b, _ := os.ReadFile(tr.logFile(2))
	i := strings.Index(string(b), `"digest":"`) + len(`"digest":"`)
	b[i] ^= 1
	if err := os.WriteFile(tr.logFile(2), b, 0o600); err != nil {
		t.Fatal(err)
	}
	tr.start(2)
	if st := tr.nodes[2].Status(); st.Lost == "" {
		t.Fatalf("W on a file whose header names another base: %+v; want lost", st)
	}
}

// A copy behind the base gets neither a sparse page nor a sparse resend:
// the page is refused (409 compacted), and so is the resend to a copy that
// can't take an install (n2: no rules version, as a build from before).
func TestNothingBelowTheBaseIsServedAsTheLog(t *testing.T) {
	tr := compactTrio(t, func(i int, o *Options) {
		if i == 2 {
			o.CompactRules = 0
		}
	})
	mustSync(t, tr.trio, 0)
	full := tr.fill(0, 30, 10)
	tr.compact(0, 20, toyKeep(full, 20, 0))
	_, err := tr.nodes[1].fetchPage(tr.ctx(), tr.urls[0], 0, false)
	var he *httpError
	if !errors.As(err, &he) || he.code != http.StatusConflict || !strings.Contains(he.body, "compacted") {
		t.Fatalf("a page after 0 from a log compacted through 20: %v; want 409 compacted", err)
	}
	if _, err := tr.nodes[1].fetchPage(tr.ctx(), tr.urls[0], 20, false); err != nil {
		t.Fatalf("a page after the base: %v", err)
	}
	tr.wipe(2)
	_, err = tr.nodes[0].replicateTo(tr.ctx(), tr.urls[2], nil, tr.m.term)
	if !errors.Is(err, errBehindBase) {
		t.Fatalf("a heartbeat to an empty copy: %v; want refused, behind the base", err)
	}
	if es := tr.nodes[2].After(0); len(es) != 0 {
		t.Fatalf("the empty copy took %v", entryJSON(es))
	}
}

// A compacted copy takes a replication whose previous entry is at or below
// its base, as matched, and the entries up to it as held.
func TestReplicationFromBelowTheBaseIsTaken(t *testing.T) {
	tr := compactTrio(t, nil)
	mustSync(t, tr.trio, 0)
	full := tr.fill(0, 30, 11)
	tr.compact(0, 20, toyKeep(full, 20, 0))
	es := tr.full()
	a := tr.nodes[0]
	req := replicateReq{From: tr.urls[0], Node: "n0", Term: tr.m.term, Commit: a.Commit(), PrevSeq: 2, PrevTerm: es[1].Term, Entries: es[2:], Synced: true}
	var resp replicateResp
	if err := a.call(tr.ctx(), tr.urls[1], "/api/oplog/replicate", req, &resp); err != nil {
		t.Fatalf("replication from seq 2 to a copy compacted through 20: %v", err)
	}
	if resp.Have != es[len(es)-1].Seq {
		t.Fatalf("Have %d; want %d", resp.Have, es[len(es)-1].Seq)
	}
}

// Free disk under twice the log's size: the rewrite waits, and says so;
// the copy is compacted logically all the same, and rewrites once there is
// room.
func TestTooLittleFreeDiskHoldsTheRewrite(t *testing.T) {
	var room atomic.Bool
	tr := compactTrio(t, func(i int, o *Options) {
		if i == 1 {
			o.freeDisk = func(string) (int64, error) {
				if room.Load() {
					return 1 << 40, nil
				}
				return 10, nil
			}
		}
	})
	mustSync(t, tr.trio, 0)
	full := tr.fill(0, 30, 12)
	keep := toyKeep(full, 20, 0)
	if _, err := tr.nodes[0].proposeCompact(tr.ctx(), 20, keep); err != nil {
		t.Fatal(err)
	}
	tr.record(0)
	tr.nodes[0].Heartbeat(tr.ctx())
	deadline := time.Now().Add(5 * time.Second)
	for tr.nodes[1].Status().CompactRefused == "" && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	st := tr.nodes[1].Status()
	if st.CompactRefused == "" || !st.CompactPending || st.CompactThrough != 20 {
		t.Fatalf("B with 10 bytes free: %+v; want the rewrite refused, the base logical", st)
	}
	if b, _ := os.ReadFile(tr.logFile(1)); strings.HasPrefix(string(b), `{"b":`) {
		t.Fatal("B rewrote its file with too little free disk")
	}
	room.Store(true)
	tr.settle()
	if st := tr.nodes[1].Status(); st.CompactRefused != "" {
		t.Fatalf("B with room: %+v", st)
	}
	tr.check(1, tr.expect(20, keep))
}

// The witness keeps no copy from before a compaction; a controller does.
func TestOnlyAControllerKeepsTheLogFromBefore(t *testing.T) {
	tr := compactTrio(t, func(i int, o *Options) { o.ResetInPlace = i == 2 })
	mustSync(t, tr.trio, 0)
	full := tr.fill(0, 30, 13)
	tr.compact(0, 20, toyKeep(full, 20, 0))
	for i, want := range []int{1, 1, 0} {
		if pre, _ := filepath.Glob(tr.logFile(i) + ".precompact-*"); len(pre) != want {
			t.Fatalf("n%d keeps %v; want %d", i, pre, want)
		}
	}
}

// Compaction is off unless the build's keep-rules version is set: no
// production wiring sets it until the decision is built (PR 2b).
func TestCompactionIsOffUnlessSwitchedOn(t *testing.T) {
	tr := newTrio(t, nil)
	mustSync(t, tr, 0)
	if _, err := tr.propose(0, "X"); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.nodes[0].proposeCompact(tr.ctx(), 1, nil); !errors.Is(err, errCompactOff) {
		t.Fatalf("compaction with no rules version: %v; want off", err)
	}
}

// A committed compaction found not to fit at start (the replay carries it
// out) diverges the copy as one found in the process does: the mark keeps
// its identity and says so, and the start after moves the log aside.
func TestACompactionThatDoesNotFitAtStartDiverges(t *testing.T) {
	dir := t.TempDir()
	f, mk := filepath.Join(dir, "log.jsonl"), filepath.Join(dir, "log.mark")
	var lines []byte
	for s := uint64(1); s <= 5; s++ {
		e := toyEntry("k", "set", int(s))
		e.Seq, e.Term, e.TS = s, 1, 1
		lines = appendLine(lines, fileLine{E: &e})
	}
	body, _ := json.Marshal(compactBody{V: 1, Rules: 1, Through: 3, ThroughTerm: 1, Keep: [][2]uint64{{3, 3}}, Kept: 1, Digest: "not this log's"})
	k := Entry{Seq: 6, Term: 1, TS: 1, Kind: KindCompact, By: "system", Value: body}
	lines = appendLine(lines, fileLine{E: &k})
	c := uint64(6)
	lines = appendLine(lines, fileLine{C: &c})
	if err := os.WriteFile(f, lines, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeMark(mk, Mark{Identity: "x", Held: true, Synced: true}); err != nil {
		t.Fatal(err)
	}
	o := Options{Self: "http://a", File: f, Mark: mk, Identity: "x"}
	n, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	st := n.Status()
	n.Close()
	m, err := readMark(mk)
	if err != nil {
		t.Fatal(err)
	}
	if st.Diverged == "" || !st.Unsynced || m.Identity != "x" || m.Diverged == "" || m.Synced {
		t.Fatalf("after a start that found the compaction does not fit: status %+v, mark %+v; want diverged, unsynced, identity kept", st, m)
	}
	n, err = New(o)
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	aside, _ := filepath.Glob(f + ".diverged-*")
	if st := n.Status(); st.Diverged != "" || st.Lost == "" || st.LastSeq != 0 || len(aside) != 1 {
		t.Fatalf("the start after: %+v, aside %v; want an empty log, the old one aside", st, aside)
	}
}

// A compaction is refused while entries are uncommitted: an earlier K among
// them would leave the new one following the base from before it, and every
// copy that commits both would diverge (Fable, 2a-1 review).
func TestNoCompactionOverAnUncommittedOne(t *testing.T) {
	var fail atomic.Bool
	tr := compactTrio(t, func(i int, o *Options) {
		if i == 0 {
			o.fileSync = func(f *os.File) error {
				if fail.Load() && strings.HasSuffix(f.Name(), ".jsonl") {
					return errors.New("no space left on device")
				}
				return f.Sync()
			}
		}
	})
	mustSync(t, tr.trio, 0)
	full := tr.fill(0, 30, 8)
	tr.setDown(1, true)
	tr.setDown(2, true)
	if _, err := tr.nodes[0].proposeCompact(tr.ctx(), 10, toyKeep(full, 10, 0)); !errors.Is(err, ErrUnconfirmed) {
		t.Fatalf("K1 with both peers down: %v; want unconfirmed", err)
	}
	tr.setDown(1, false)
	tr.setDown(2, false)
	fail.Store(true) // the next write's catch-up succeeds, its store fails
	if _, err := tr.propose(0, "X"); err == nil {
		t.Fatal("a write whose store failed was confirmed")
	}
	fail.Store(false)
	if _, err := tr.nodes[0].proposeCompact(tr.ctx(), 25, toyKeep(full, 25, 0)); err == nil || !strings.Contains(err.Error(), "not committed yet") {
		t.Fatalf("K2 over an uncommitted K1: %v; want refused", err)
	}
	tr.lead(1)
	if err := tr.nodes[1].SyncLeader(tr.ctx()); err != nil {
		t.Fatalf("B takes over: %v", err)
	}
	tr.nodes[1].Heartbeat(tr.ctx())
	for i, n := range tr.nodes {
		if st := n.Status(); st.Diverged != "" || st.Lost != "" {
			t.Fatalf("n%d after the takeover: %+v", i, st)
		}
	}
}

// The seam refuses a keep list that keeps an entry but drops the lapse that
// names it: replayed without the lapse, the entry would be carried out.
func TestTheKeepListKeepsALapseWithWhatItNames(t *testing.T) {
	tr := compactTrio(t, func(_ int, o *Options) { o.Lapse = func(Entry, time.Time) bool { return true } })
	mustSync(t, tr.trio, 0)
	tr.fill(0, 5, 1)
	tr.setDown(1, true)
	tr.setDown(2, true)
	tr.propose(0, "LATE")
	named, _ := tr.holds(0, "LATE")
	tr.setDown(1, false)
	tr.setDown(2, false)
	tr.lead(0)
	mustSync(t, tr.trio, 0)
	tr.fill(0, 3, 2)
	tr.record(0)
	var lapse uint64
	for _, e := range tr.full() {
		if e.Kind == KindLapse {
			lapse = e.Seq
		}
	}
	through := tr.nodes[0].Commit()
	if _, err := tr.nodes[0].proposeCompact(tr.ctx(), through, []uint64{named}); err == nil || !strings.Contains(err.Error(), "drops the lapse") {
		t.Fatalf("keeping %d without its lapse at %d: %v; want refused", named, lapse, err)
	}
	keep := slices.Compact(slices.Sorted(slices.Values(append(toyKeep(tr.full(), through, 0), named, lapse))))
	tr.compact(0, through, keep)
	for i := range 3 {
		tr.check(i, tr.expect(through, keep))
	}
}

// The witness that finds at start a compaction its log does not fit starts
// over at once, not at a second start.
func TestAWitnessThatDivergesAtStartStartsOver(t *testing.T) {
	dir := t.TempDir()
	f, mk := filepath.Join(dir, "log.jsonl"), filepath.Join(dir, "log.mark")
	var lines []byte
	for s := uint64(1); s <= 5; s++ {
		e := toyEntry("k", "set", int(s))
		e.Seq, e.Term, e.TS = s, 1, 1
		lines = appendLine(lines, fileLine{E: &e})
	}
	body, _ := json.Marshal(compactBody{V: 1, Rules: 1, Through: 3, ThroughTerm: 1, Keep: [][2]uint64{{3, 3}}, Kept: 1, Digest: "not this log's"})
	k := Entry{Seq: 6, Term: 1, TS: 1, Kind: KindCompact, By: "system", Value: body}
	lines = appendLine(lines, fileLine{E: &k})
	c := uint64(6)
	lines = appendLine(lines, fileLine{C: &c})
	if err := os.WriteFile(f, lines, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeMark(mk, Mark{Identity: "w", Held: true, Synced: true}); err != nil {
		t.Fatal(err)
	}
	n, err := New(Options{Self: "http://w", File: f, Mark: mk, Identity: "w", ResetInPlace: true})
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	aside, _ := filepath.Glob(f + ".diverged-*")
	if st := n.Status(); st.Diverged != "" || st.Lost == "" || st.LastSeq != 0 || len(aside) != 1 {
		t.Fatalf("the witness's start: %+v, aside %v; want an empty log, the old one aside", st, aside)
	}
}

// The header keeps the keep-rules version: a build on older rules does not
// compact a log compacted under newer ones, after a restart too.
func TestTheHeaderKeepsTheRulesVersion(t *testing.T) {
	rules := 2
	tr := compactTrio(t, func(_ int, o *Options) { o.CompactRules = rules })
	mustSync(t, tr.trio, 0)
	full := tr.fill(0, 20, 14)
	tr.compact(0, 10, toyKeep(full, 10, 0))
	rules = 1
	tr.restart(0)
	mustSync(t, tr.trio, 0)
	tr.fill(0, 5, 15)
	if _, err := tr.nodes[0].proposeCompact(tr.ctx(), tr.nodes[0].Commit(), toyKeep(tr.full(), tr.nodes[0].Commit(), 0)); err == nil || !strings.Contains(err.Error(), "keep rules 2") {
		t.Fatalf("rules 1 over a log compacted under rules 2: %v; want refused", err)
	}
}

// A diverged copy commits and applies nothing more, with no mark too.
func TestADivergedCopyAppliesNothingMore(t *testing.T) {
	applied := map[uint64]int{}
	m, err := New(Options{Self: "http://b", Apply: func(e Entry) error { applied[e.Seq]++; return nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	var es []Entry
	for s := uint64(1); s <= 5; s++ {
		e := toyEntry("k", "set", int(s))
		e.Seq, e.Term = s, 1
		es = append(es, e)
	}
	body, _ := json.Marshal(compactBody{V: 1, Rules: 1, Through: 3, ThroughTerm: 1, Keep: [][2]uint64{{3, 3}}, Kept: 1, Digest: "x"})
	k := Entry{Seq: 6, Term: 1, Kind: KindCompact, By: "system", Value: body}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.acceptLocked(append(es, k)); err != nil {
		t.Fatal(err)
	}
	if err := m.commitTo(6); !errors.Is(err, errDiverged) {
		t.Fatalf("a commit over a compaction that does not fit: %v", err)
	}
	if err := m.commitTo(6); !errors.Is(err, errDiverged) || applied[1] != 1 || m.syncedLocked() {
		t.Fatalf("again: %v, entry 1 applied %d times, synced %v", err, applied[1], m.syncedLocked())
	}
}
