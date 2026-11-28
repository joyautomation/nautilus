//go:build !js

package oplog

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Compaction on the wire (#1455 PR 2a-2, docs/design/oplog-compaction.md
// §5-§6): a copy behind the base (wiped, lost, diverged) is refilled by an
// install, pushed by the leader or pulled, and ends holding exactly the
// full log filtered to the kept entries and the tail (the oracle, as in
// compact_test.go); a copy that can't take one is named, never resent to.

// smallPages makes a base span many pages.
func smallPages(i int, o *Options) {
	o.lim.chunkEntries = 3
	o.lim.chunkBytes = 1 << 20
}

// noPull cuts replica i's own calls, so what it gets is pushed.
func (tr *ctrio) noPull(i int, v bool) {
	tr.net.mu.Lock()
	for j := range 3 {
		if j != i {
			tr.net.cut[i][j] = v
		}
	}
	tr.net.mu.Unlock()
}

// compacted fills replica 0 (leading) with n entries and compacts through
// through: the full log, the keep list.
func (tr *ctrio) compacted(n int, through uint64, seed uint64) []uint64 {
	tr.t.Helper()
	mustSync(tr.t, tr.trio, 0)
	full := tr.fill(0, n, seed)
	keep := toyKeep(full, through, 0)
	tr.compact(0, through, keep)
	return keep
}

// heartbeatUntil sends replica 0's heartbeat until replica i is synced at
// the base, at most rounds times.
func (tr *ctrio) heartbeatUntil(i, rounds int, through uint64) {
	tr.t.Helper()
	for range rounds {
		tr.nodes[0].Heartbeat(tr.ctx())
		if st := tr.nodes[i].Status(); !st.Unsynced && st.CompactThrough == through {
			return
		}
	}
	tr.t.Fatalf("n%d not refilled after %d heartbeats: %+v", i, rounds, tr.nodes[i].Status())
}

// notes counts the note entries of es.
func notes(es []Entry) int {
	k := 0
	for _, e := range es {
		if e.Kind == KindNote {
			k++
		}
	}
	return k
}

func TestAWipedWitnessIsRefilledByPush(t *testing.T) {
	for _, dense := range []bool{false, true} {
		t.Run(fmt.Sprintf("leader file dense %v", dense), func(t *testing.T) {
			// dense: the leader is compacted logically, its file not yet
			// rewritten (its memory still holds the dropped entries): the
			// install comes from the base, not from what memory holds.
			tr := compactTrio(t, func(i int, o *Options) {
				smallPages(i, o)
				o.ResetInPlace = i == 2
				if dense && i == 0 {
					o.lim.compactDelay = func() time.Duration { return time.Hour }
				}
			})
			mustSync(t, tr.trio, 0)
			full := tr.fill(0, 60, 21)
			keep := toyKeep(full, 40, 0)
			k, err := tr.nodes[0].proposeCompact(tr.ctx(), 40, keep)
			if err != nil {
				t.Fatal(err)
			}
			tr.hist[k.Seq] = k
			tr.nodes[0].Heartbeat(tr.ctx())
			if dense {
				if st := tr.nodes[0].Status(); !st.CompactPending {
					t.Fatalf("the leader's file is already rewritten: %+v", st)
				}
			}
			tr.wipe(2)
			tr.noPull(2, true) // the witness never pulls
			tr.heartbeatUntil(2, 10, 40)
			want := tr.expect(40, keep)
			if notes(tr.nodes[2].After(0)) != notes(full) {
				t.Fatalf("the witness holds %d notes, the full log %d", notes(tr.nodes[2].After(0)), notes(full))
			}
			tr.noPull(2, false)
			if !dense {
				tr.check(2, want)
			} else if got := entryJSON(tr.nodes[2].After(0)); fmt.Sprint(got) != fmt.Sprint(entryJSON(want)) {
				t.Fatalf("the witness holds\n%v\nwant\n%v", got, entryJSON(want))
			}
		})
	}
}

func TestAWipedControllerIsRefilledByPushWhileTheOtherLeads(t *testing.T) {
	tr := compactTrio(t, smallPages)
	keep := tr.compacted(50, 30, 22)
	tr.wipe(1)
	tr.noPull(1, true)
	tr.heartbeatUntil(1, 10, 30)
	tr.noPull(1, false)
	// It counts: a write commits with the witness down.
	tr.setDown(2, true)
	if _, err := tr.propose(0, "WITH-B"); err != nil {
		t.Fatalf("a write with A and the refilled B: %v", err)
	}
	tr.setDown(2, false)
	tr.record(0)
	tr.check(1, tr.expect(30, keep))
}

func TestALostLeaderIsRefilledByPull(t *testing.T) {
	tr := compactTrio(t, smallPages)
	keep := tr.compacted(50, 30, 23)
	tr.loseLog(1)
	if st := tr.nodes[1].Status(); st.Lost == "" {
		t.Fatalf("B lost its log: %+v", st)
	}
	tr.lead(1)
	if err := tr.nodes[1].SyncLeader(tr.ctx()); err != nil {
		t.Fatalf("a lost leader's catch-up from a compacted copy and the witness: %v", err)
	}
	if _, err := tr.propose(1, "B-LEADS"); err != nil {
		t.Fatal(err)
	}
	tr.record(1)
	want := tr.expect(30, keep)
	if notes(tr.nodes[1].After(0)) != notes(tr.full()) {
		t.Fatalf("B holds %d notes, the full log %d", notes(tr.nodes[1].After(0)), notes(tr.full()))
	}
	tr.check(1, want)
}

func TestAWipedFollowerIsRefilledByItsOwnPull(t *testing.T) {
	tr := compactTrio(t, smallPages)
	keep := tr.compacted(50, 30, 24)
	tr.wipe(1)
	for range 10 {
		if err := tr.nodes[1].CatchUp(tr.ctx()); err != nil {
			t.Fatalf("a wiped follower's pull: %v", err)
		}
		if st := tr.nodes[1].Status(); st.CompactThrough == 30 {
			break
		}
	}
	tr.nodes[0].Heartbeat(tr.ctx()) // the synced leader's word syncs it
	tr.check(1, tr.expect(30, keep))
}

// The push and the pull meet in the one staging slot, page by page, with
// requests handled late and out of turn: one install, the same log.
func TestPushAndPullRefillOneCopyTogether(t *testing.T) {
	for _, late := range []bool{false, true} {
		t.Run(fmt.Sprintf("late %v", late), func(t *testing.T) {
			tr := compactTrio(t, smallPages)
			keep := tr.compacted(60, 40, 25)
			tr.wipe(1)
			if late {
				tr.net.mu.Lock()
				tr.net.delay[1] = true
				tr.net.mu.Unlock()
			}
			var wg sync.WaitGroup
			var done atomic.Bool
			for g := range 3 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for k := 0; k < 30 && !done.Load(); k++ {
						if g == 0 {
							_ = tr.nodes[1].CatchUp(tr.ctx())
						} else {
							tr.nodes[0].Heartbeat(tr.ctx())
						}
						if st := tr.nodes[1].Status(); !st.Unsynced && st.CompactThrough == 40 {
							done.Store(true)
						}
					}
				}()
			}
			wg.Wait()
			tr.net.mu.Lock()
			tr.net.delay[1] = false
			tr.net.mu.Unlock()
			tr.net.late.Wait()
			if !done.Load() {
				t.Fatalf("B never refilled: %+v", tr.nodes[1].Status())
			}
			tr.check(1, tr.expect(40, keep))
		})
	}
}

// A copy behind the base on a build that can't take an install (no rules
// version) is named and never resent to: one request a heartbeat, no
// install.
func TestAnOldCopyBehindTheBaseIsNamedAndNotResentTo(t *testing.T) {
	tr := compactTrio(t, func(i int, o *Options) {
		smallPages(i, o)
		if i == 2 {
			o.CompactRules = 0
		}
	})
	tr.compacted(40, 25, 26)
	tr.wipe(2)
	var reqs, installs atomic.Int64
	tr.net.mu.Lock()
	tr.net.tamper = func(from, to int, path string, body []byte) []byte {
		if to == 2 && strings.HasSuffix(path, "/replicate") {
			reqs.Add(1)
			if bytes.Contains(body, []byte(`"install"`)) {
				installs.Add(1)
			}
		}
		return body
	}
	tr.net.mu.Unlock()
	for range 5 {
		tr.nodes[0].Heartbeat(tr.ctx())
	}
	if n, in := reqs.Load(), installs.Load(); n != 5 || in != 0 {
		t.Fatalf("5 heartbeats sent %d requests to the old copy, %d installs; want 5 and none", n, in)
	}
	st := tr.nodes[0].Status()
	if len(st.BehindBase) != 1 || !strings.Contains(st.BehindBase[0], "build from before log compaction") || !strings.Contains(st.BehindBase[0], "update it") {
		t.Fatalf("the leader's status names %v; want the old copy", st.BehindBase)
	}
	if es := tr.nodes[2].After(0); len(es) != 0 {
		t.Fatalf("the old copy took %v", entryJSON(es))
	}
	// Writes go on with A and B.
	if _, err := tr.propose(0, "GOES-ON"); err != nil {
		t.Fatal(err)
	}
}

// An install that does not check out (an entry changed on the way, or a
// base header that is not the one its K made) is applied nowhere.
func TestAnInstallThatDoesNotCheckOutIsNotApplied(t *testing.T) {
	for _, tc := range []struct {
		name string
		mut  func(*installReq)
	}{
		{"entry term", func(in *installReq) {
			if len(in.Entries) > 0 {
				in.Entries[0].Term += 7
			}
		}},
		{"entry dropped", func(in *installReq) {
			if in.K != nil && len(in.Entries) > 0 {
				in.Entries = in.Entries[:len(in.Entries)-1]
			}
		}},
		{"header not K's", func(in *installReq) { in.Digest = strings.Repeat("0", 64) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := compactTrio(t, func(i int, o *Options) { o.ResetInPlace = i == 2 })
			tr.compacted(40, 25, 27)
			tr.wipe(2)
			tr.net.mu.Lock()
			tr.net.tamper = func(from, to int, path string, body []byte) []byte {
				var q replicateReq
				if to != 2 || json.Unmarshal(body, &q) != nil || q.Install == nil {
					return body
				}
				tc.mut(q.Install)
				b, _ := json.Marshal(q)
				return b
			}
			tr.net.mu.Unlock()
			for range 3 {
				tr.nodes[0].Heartbeat(tr.ctx())
			}
			if st := tr.nodes[2].Status(); st.CompactThrough != 0 || st.Commit != 0 || st.LastSeq != 0 || !st.Unsynced {
				t.Fatalf("a tampered install was applied: %+v", st)
			}
			// Refused at the install, not taken and then found out by the
			// tail's K (the witness would have diverged and started over).
			if aside, _ := filepath.Glob(tr.logFile(2) + ".diverged-*"); len(aside) != 0 {
				t.Fatalf("a tampered install was taken, then diverged: %v", aside)
			}
			tr.net.mu.Lock()
			tr.net.tamper = nil
			tr.net.mu.Unlock()
			tr.heartbeatUntil(2, 5, 25) // and an honest one is, after
		})
	}
}

// The K the leader then replicates in the tail must be the one the
// installed base came from: one that is not is a divergence.
func TestATailCompactionThatIsNotTheInstalledOneDiverges(t *testing.T) {
	for _, tc := range []struct {
		name string
		mut  func(*Entry)
	}{
		{"another digest", func(e *Entry) {
			var b compactBody
			json.Unmarshal(e.Value, &b)
			b.Digest = strings.Repeat("1", 64)
			e.Value, _ = json.Marshal(b)
		}},
		{"not a compaction", func(e *Entry) { e.Kind, e.Value, e.IDs = KindAck, nil, []string{"NOT-K"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := compactTrio(t, nil)
			tr.compacted(40, 25, 28)
			tr.wipe(1)
			tr.noPull(1, true)
			tr.net.mu.Lock()
			tr.net.tamper = func(from, to int, path string, body []byte) []byte {
				var q replicateReq
				if to != 1 || json.Unmarshal(body, &q) != nil || q.Install != nil {
					return body
				}
				changed := false
				for i, e := range q.Entries {
					if e.Kind == KindCompact {
						tc.mut(&q.Entries[i])
						changed = true
					}
				}
				if !changed {
					return body
				}
				b, _ := json.Marshal(q)
				return b
			}
			tr.net.mu.Unlock()
			for range 3 {
				tr.nodes[0].Heartbeat(tr.ctx())
			}
			if st := tr.nodes[1].Status(); st.CompactThrough != 25 || st.Diverged == "" {
				t.Fatalf("B, installed, then sent another entry at its K's seq: %+v; want diverged", st)
			}
		})
	}
}

// An install onto a copy that holds committed entries is refused, and
// changes nothing there.
func TestAnInstallOntoACommittedCopyIsRefused(t *testing.T) {
	tr := compactTrio(t, nil)
	mustSync(t, tr.trio, 0)
	full := tr.fill(0, 40, 29)
	tr.setDown(1, true) // B misses the compaction, its log committed past Through
	k, err := tr.nodes[0].proposeCompact(tr.ctx(), 25, toyKeep(full, 25, 0))
	if err != nil {
		t.Fatal(err)
	}
	tr.hist[k.Seq] = k
	tr.nodes[0].Heartbeat(tr.ctx())
	tr.setDown(1, false)
	before := entryJSON(tr.nodes[1].After(0))
	n := tr.nodes[0]
	n.mu.Lock()
	in := n.installPageLocked(0)
	commit := n.commit
	n.mu.Unlock()
	if in == nil || in.K == nil {
		t.Fatalf("one page holds the base: %+v", in)
	}
	var resp replicateResp
	err = n.call(tr.ctx(), tr.urls[1], "/api/oplog/replicate",
		replicateReq{From: n.o.Self, Node: n.o.Node, Term: tr.m.term, Commit: commit, Install: in}, &resp)
	var he *httpError
	if !errors.As(err, &he) || he.code != http.StatusConflict || !strings.Contains(he.body, "committed entries") {
		t.Fatalf("an install onto a copy with committed entries: %v; want refused", err)
	}
	if after := entryJSON(tr.nodes[1].After(0)); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Fatalf("B's log changed: %v", after)
	}
}

// A copy that holds the entry at the base's Through, at its term, but
// whose commit index is below it, reads on from Through (its log is the
// leader's up to there) and carries out the compaction itself.
func TestACopyHoldingThroughReadsOnFromIt(t *testing.T) {
	tr := compactTrio(t, nil)
	mustSync(t, tr.trio, 0)
	tr.net.mu.Lock()
	tr.net.dropPush[1] = true // B never hears the last commit push
	tr.net.mu.Unlock()
	full := tr.fill(0, 30, 30)
	last := full[len(full)-1].Seq
	if c := tr.nodes[1].Commit(); c >= last || tr.nodes[1].LastSeq() != last {
		t.Fatalf("B at commit %d of %d (holds %d)", c, last, tr.nodes[1].LastSeq())
	}
	tr.setDown(1, true)
	keep := toyKeep(full, last, 0)
	k, err := tr.nodes[0].proposeCompact(tr.ctx(), last, keep)
	if err != nil {
		t.Fatal(err)
	}
	tr.hist[k.Seq] = k
	tr.nodes[0].Heartbeat(tr.ctx())
	tr.setDown(1, false)
	tr.net.mu.Lock()
	tr.net.dropPush[1] = false
	tr.net.mu.Unlock()
	if err := tr.nodes[1].CatchUp(tr.ctx()); err != nil {
		t.Fatalf("B's pull, below the leader's base but holding it: %v", err)
	}
	if err := tr.nodes[1].compactedForTest(tr.ctx()); err != nil {
		t.Fatal(err)
	}
	tr.check(1, tr.expect(last, keep))
}

// A crash inside an install leaves a copy that is lost (held, empty) or
// installed, never half of one; the next push refills it.
func TestAnInstallSurvivesACrash(t *testing.T) {
	for _, step := range []string{"install-held", "install-written"} {
		t.Run(step, func(t *testing.T) {
			var crash atomic.Bool
			tr := compactTrio(t, func(i int, o *Options) {
				if i == 1 {
					o.compactCrash = func(s string) bool { return s == step && crash.Load() }
				}
			})
			keep := tr.compacted(40, 25, 31)
			crash.Store(true)
			tr.wipe(1)
			tr.noPull(1, true)
			tr.nodes[0].Heartbeat(tr.ctx())
			crash.Store(false)
			tr.restart(1)
			st := tr.nodes[1].Status()
			switch step {
			case "install-held":
				if st.Lost == "" || st.LastSeq != 0 {
					t.Fatalf("crashed before the base was written: %+v; want lost, empty", st)
				}
			case "install-written":
				if st.CompactThrough != 25 || st.Commit < 25 || !st.Unsynced {
					t.Fatalf("crashed after the base was written: %+v; want it held, unsynced", st)
				}
			}
			tr.heartbeatUntil(1, 5, 25)
			tr.noPull(1, false)
			tr.check(1, tr.expect(25, keep))
		})
	}
}

// A node with compaction off says nothing of it on the wire: its answers
// are a build from before's, key for key.
func TestAnUnwiredNodeSaysNothingOfCompaction(t *testing.T) {
	newKeys := []string{"compactRules", "now", "base", "staged", "basePage", "after", "k"}
	for _, wired := range []bool{false, true} {
		t.Run(fmt.Sprintf("wired %v", wired), func(t *testing.T) {
			tr := newTrio(t, func(i int, o *Options) {
				if wired {
					o.CompactRules = 1
				}
			})
			mustSync(t, tr, 0)
			if _, err := tr.propose(0, "X"); err != nil {
				t.Fatal(err)
			}
			n := tr.nodes[1]
			keys := func(code int, body []byte) map[string]any {
				var m map[string]any
				if err := json.Unmarshal(body, &m); err != nil {
					t.Fatalf("%d %s: %v", code, body, err)
				}
				return m
			}
			do := func(method, path string, body any) map[string]any {
				var rd *bytes.Reader
				if body != nil {
					b, _ := json.Marshal(body)
					rd = bytes.NewReader(b)
				} else {
					rd = bytes.NewReader(nil)
				}
				r := httptest.NewRequest(method, path, rd)
				r.Header.Set("X-Nautilus-Cluster", "t")
				w := httptest.NewRecorder()
				n.Handler().ServeHTTP(w, r)
				return keys(w.Code, w.Body.Bytes())
			}
			answers := map[string]map[string]any{
				"page":      do("GET", "/api/oplog?after=0&page=1", nil),
				"replicate": do("POST", "/api/oplog/replicate", replicateReq{From: tr.urls[0], Node: "n0", Term: tr.m.term, PrevSeq: n.LastSeq(), PrevTerm: tr.m.term}),
				"gap":       do("POST", "/api/oplog/replicate", replicateReq{From: tr.urls[0], Node: "n0", Term: tr.m.term, PrevSeq: 99, PrevTerm: tr.m.term}),
			}
			for name, m := range answers {
				for _, k := range newKeys {
					_, has := m[k]
					if !wired && has {
						t.Fatalf("an unwired node's %s answer carries %q: %v", name, k, m)
					}
				}
				if _, has := m["compactRules"]; wired && !has {
					t.Fatalf("a wired node's %s answer does not say its rules: %v", name, m)
				}
			}
		})
	}
}

// Install pages are taken by position: a duplicate or one out of turn is
// ignored (the answer says where to go on), never staged twice, and one
// after the install changes nothing.
func TestInstallPagesAreTakenByPosition(t *testing.T) {
	tr := compactTrio(t, func(i int, o *Options) {
		smallPages(i, o)
		o.ResetInPlace = i == 2
	})
	tr.compacted(50, 30, 32)
	tr.wipe(2)
	n := tr.nodes[0]
	var pages []*installReq
	n.mu.Lock()
	for after := uint64(0); ; {
		in := n.installPageLocked(after)
		pages = append(pages, in)
		if in.K != nil {
			break
		}
		after = in.Entries[len(in.Entries)-1].Seq
	}
	commit := n.commit
	n.mu.Unlock()
	if len(pages) < 3 {
		t.Fatalf("the base spans %d pages; want several", len(pages))
	}
	send := func(in *installReq) (replicateResp, error) {
		var resp replicateResp
		err := n.call(tr.ctx(), tr.urls[2], "/api/oplog/replicate",
			replicateReq{From: n.o.Self, Node: n.o.Node, Term: tr.m.term, Commit: commit, Install: in}, &resp)
		return resp, err
	}
	order := []int{0, 0, 2, 1}
	for k := 2; k < len(pages); k++ {
		order = append(order, k)
	}
	order = append(order, 0) // late, after the install
	var resp replicateResp
	for _, k := range order {
		var err error
		if resp, err = send(pages[k]); err != nil {
			t.Fatalf("page %d: %v", k, err)
		}
	}
	st := tr.nodes[2].Status()
	if st.CompactThrough != 30 || st.Commit != 30 || resp.Have != 30 {
		t.Fatalf("after its pages in that order: %+v (have %d); want installed at 30", st, resp.Have)
	}
	tr.heartbeatUntil(2, 3, 30)
	tr.check(2, tr.expect(30, tr.hist2keep(30)))
}

// hist2keep is the toy keep list of the uncompacted log through through.
func (tr *ctrio) hist2keep(through uint64) []uint64 { return toyKeep(tr.full(), through, 0) }

// An install page proves nothing of what the copy holds past its commit
// index (Fable, 2a-2 review): a copy with an old term's suffix answering
// it with its last entry made a write count there that it did not hold.
func TestAnInstallPageProvesNothingPastTheCommit(t *testing.T) {
	tr := compactTrio(t, smallPages)
	tr.setDown(1, true)
	tr.lead(0)
	mustSync(t, tr.trio, 0)
	full := tr.fill(0, 50, 22)
	tr.compact(0, 30, toyKeep(full, 30, 0))
	tr.setDown(1, false)
	tr.noPull(1, true)
	var es []Entry
	for s := uint64(1); s <= 80; s++ {
		es = append(es, Entry{Seq: s, Term: 1, Kind: KindAck, IDs: []string{"STALE"}, By: "op"})
	}
	b := tr.nodes[1]
	b.mu.Lock()
	_, err := b.acceptLocked(es) // an old term's suffix, never committed
	b.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	tr.setDown(2, true)
	_, err = tr.propose(0, "X")
	if seq, _ := tr.holds(1, "X"); err == nil && seq == 0 {
		t.Fatal("a write committed with the witness down and B not holding it")
	}
}

// The one staging slot: the push wins a tie at the same term, the newer
// term wins, and a staging below the term this copy now runs at is stale
// (Fable, 2a-2 review: a wiped copy pushed to, then leading, could not
// pull from a copy still at the old term).
func TestTheStagingSlotIsArbitrated(t *testing.T) {
	tr := compactTrio(t, smallPages)
	tr.compacted(40, 25, 33)
	tr.wipe(1)
	a, b := tr.nodes[0], tr.nodes[1]
	a.mu.Lock()
	push := a.installPageLocked(0)
	term, lin := tr.m.term, a.mark.Lineage
	a.mu.Unlock()
	pull, err := b.fetchPage(tr.ctx(), tr.urls[2], 0, true)
	if err != nil || !pull.BasePage {
		t.Fatalf("W's base page: %+v %v", pull, err)
	}
	stage := func(from string, term uint64, push bool, in *installReq) error {
		b.mu.Lock()
		defer b.mu.Unlock()
		_, err := b.stageLocked(from, term, lin, push, in.wireBase, in.After, in.Entries, in.K)
		return err
	}
	pullReq := &installReq{wireBase: *pull.Base, After: pull.After, Entries: pull.Entries, K: pull.K}
	if err := stage(tr.urls[0], term, true, push); err != nil {
		t.Fatalf("the push's first page: %v", err)
	}
	if err := stage(tr.urls[2], pull.Term, false, pullReq); !errors.Is(err, errInstallBusy) {
		t.Fatalf("a pull at the push's term: %v; want busy (the push wins a tie)", err)
	}
	if err := stage(tr.urls[2], term-1, true, pullReq); !errors.Is(err, errInstallBusy) {
		t.Fatalf("a push at an older term: %v; want busy", err)
	}
	tr.lead(1) // B now runs at a newer term: A's staging is stale
	if err := stage(tr.urls[2], pull.Term, false, pullReq); err != nil {
		t.Fatalf("a pull after B leads at a newer term: %v; want the stale push replaced", err)
	}
	if err := b.SyncLeader(tr.ctx()); err != nil {
		t.Fatalf("B, leading, catches up from the compacted copies: %v", err)
	}
	if st := b.Status(); st.CompactThrough != 25 || st.Unsynced {
		t.Fatalf("B after its catch-up: %+v", st)
	}
}

// An installed base whose tail has not come yet is served all the same,
// its K kept in the header, before and after a restart (Fable, 2a-2
// review: it answered 409, and a lost leader counted it silent).
func TestAnInstalledBaseIsServedBeforeItsTail(t *testing.T) {
	tr := compactTrio(t, func(i int, o *Options) {
		smallPages(i, o)
		o.ResetInPlace = i == 2
	})
	keep := tr.compacted(40, 25, 34)
	tr.wipe(2)
	n := tr.nodes[0]
	n.mu.Lock()
	var pages []*installReq
	for after := uint64(0); ; {
		in := n.installPageLocked(after)
		pages = append(pages, in)
		if in.K != nil {
			break
		}
		after = in.Entries[len(in.Entries)-1].Seq
	}
	commit := n.commit
	n.mu.Unlock()
	for _, in := range pages {
		var resp replicateResp
		if err := n.call(tr.ctx(), tr.urls[2], "/api/oplog/replicate", replicateReq{From: n.o.Self, Node: n.o.Node, Term: tr.m.term, Commit: commit, Install: in}, &resp); err != nil {
			t.Fatal(err)
		}
	}
	read := func() []Entry {
		var got []Entry
		var k *Entry
		for after := uint64(0); k == nil; {
			p, err := tr.nodes[1].fetchPage(tr.ctx(), tr.urls[2], after, true)
			if err != nil || !p.BasePage {
				t.Fatalf("W's base page after %d: %+v %v", after, p, err)
			}
			got = append(got, p.Entries...)
			k = p.K
			if len(p.Entries) > 0 {
				after = p.Entries[len(p.Entries)-1].Seq
			}
		}
		if k.Kind != KindCompact || k.Seq <= 25 {
			t.Fatalf("W serves K %+v", k)
		}
		return got
	}
	var want []Entry
	for _, s := range keep {
		want = append(want, tr.hist[s])
	}
	if got := read(); fmt.Sprint(entryJSON(got)) != fmt.Sprint(entryJSON(want)) {
		t.Fatalf("W serves\n%v\nwant\n%v", entryJSON(got), entryJSON(want))
	}
	tr.restart(2)
	if got := read(); fmt.Sprint(entryJSON(got)) != fmt.Sprint(entryJSON(want)) {
		t.Fatalf("W restarted serves\n%v\nwant\n%v", entryJSON(got), entryJSON(want))
	}
}

// A copy behind the base that refuses its install (it holds committed
// entries behind it) is named, and not sent it every heartbeat.
func TestACopyThatRefusesItsInstallIsNamed(t *testing.T) {
	tr := compactTrio(t, nil)
	mustSync(t, tr.trio, 0)
	tr.fill(0, 10, 35)
	tr.setDown(1, true)
	full := tr.fill(0, 30, 36)
	through := full[len(full)-5].Seq
	tr.compact(0, through, toyKeep(full, through, 0))
	tr.setDown(1, false)
	if c := tr.nodes[1].Commit(); c == 0 || c >= through {
		t.Fatalf("B at commit %d, the base at %d", c, through)
	}
	var installs atomic.Int64
	tr.net.mu.Lock()
	tr.net.tamper = func(from, to int, path string, body []byte) []byte {
		if to == 1 && bytes.Contains(body, []byte(`"install"`)) {
			installs.Add(1)
		}
		return body
	}
	tr.net.mu.Unlock()
	for range 3 {
		tr.nodes[0].Heartbeat(tr.ctx())
	}
	st := tr.nodes[0].Status()
	if len(st.BehindBase) != 1 || !strings.Contains(st.BehindBase[0], "refuses its install") {
		t.Fatalf("the leader names %v; want B, refusing", st.BehindBase)
	}
	if k := installs.Load(); k > 3 {
		t.Fatalf("%d install pages in 3 heartbeats; want at most one each", k)
	}
}
