package oplog

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// #1029 3c-3c-2a on the log: a person's acceptance of a loss at a vouch
// (VouchAccept), bound to the picture they saw (Picture.Digest, Mark.
// Accept), and the log history (Mark.History) an override starts, so a
// copy it overrode never counts again and is moved aside when it meets it.

// editMark rewrites replica i's mark with it stopped.
func (tr *trio) editMark(i int, f func(*Mark)) {
	tr.t.Helper()
	tr.stop(i)
	m, err := readMark(tr.markFile(i))
	if err != nil {
		tr.t.Fatal(err)
	}
	f(&m)
	if err := writeMark(tr.markFile(i), m); err != nil {
		tr.t.Fatal(err)
	}
	tr.start(i)
}

func (tr *trio) mark(i int) Mark {
	tr.t.Helper()
	m, err := readMark(tr.markFile(i))
	if err != nil {
		tr.t.Fatal(err)
	}
	return m
}

func (tr *trio) appliedIDs(i int) []string {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	var out []string
	for _, e := range tr.applied[i] {
		out = append(out, e.IDs...)
	}
	return out
}

var digestRE = regexp.MustCompile(`--accept-loss=([0-9a-f]{10})`)

// witnessInPlace: n2, the witness, supersedes in place (ResetInPlace).
func witnessInPlace(i int, o *Options) {
	if i == 2 {
		o.ResetInPlace = true
	}
}

// silentBox is the dead end a silent box leaves: X committed on all
// three, then (divergent) Z committed by B and W while A was away; then
// B's box goes silent and the witness is rebuilt. A, synced but without
// Z, leads alone among the copies: read-only.
func silentBox(t *testing.T, divergent bool) *trio {
	tr := newTrio(t, witnessInPlace)
	mustSync(t, tr, 0)
	if _, err := tr.propose(0, "X"); err != nil {
		t.Fatal(err)
	}
	if divergent {
		tr.setDown(0, true)
		tr.lead(1)
		mustSync(t, tr, 1)
		if _, err := tr.propose(1, "Z"); err != nil {
			t.Fatal(err)
		}
		tr.setDown(0, false)
	}
	tr.setDown(1, true)
	tr.wipe(2)
	tr.lead(0)
	if err := tr.nodes[0].SyncLeader(tr.ctx()); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("A synced alone among the copies without a vouch: %v", err)
	}
	return tr
}

// acceptLoss vouches for replica i: a plain run refuses and prints the
// picture's digest, and the accepting run gives it back.
func acceptLoss(t *testing.T, tr *trio, i int) Picture {
	t.Helper()
	_, pic, err := tr.nodes[i].Vouch(tr.ctx(), "op", nil, VouchAccept{})
	if !errors.Is(err, ErrVouchRefused) || !slices_contains(pic.Needs, NeedLoss) {
		t.Fatalf("a vouch that needs --accept-loss went ahead without it: %v (%+v)", err, pic)
	}
	m := digestRE.FindStringSubmatch(err.Error())
	if m == nil || m[1] != pic.Digest {
		t.Fatalf("the refusal doesn't name --accept-loss=%s: %v", pic.Digest, err)
	}
	v, pic2, err := tr.nodes[i].Vouch(tr.ctx(), "op", nil, VouchAccept{Loss: pic.Digest})
	if err != nil || v.By != "op" {
		t.Fatalf("the accepting vouch: %+v %v (picture %+v, then %+v)", v, err, pic, pic2)
	}
	return pic2
}

func slices_contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// The B-box-silent accept: A's catch-up overrides silent B, as accepted,
// and starts history 1, durably; the rebuilt witness syncs at it. B comes
// back with its disk, its log synced at history 0 and holding Z (committed
// by B and W while A was away): it counts for nothing, takes nothing, and
// at its restart its log is moved aside and it syncs the new history from
// scratch, applying none of the old one's entries.
func TestAcceptedSilentBoxStartsANewHistory(t *testing.T) {
	tr := silentBox(t, true)
	pic := acceptLoss(t, tr, 0)
	if m := tr.mark(0); !m.Alone || m.Accept == nil || !m.Accept.Peers[tr.urls[1]].Silent || m.Accept.Digest != pic.Digest {
		t.Fatalf("A's mark after the accepting vouch: %+v", m)
	}
	mustSync(t, tr, 0)
	if m := tr.mark(0); m.History != 1 || m.Alone || m.Accept != nil {
		t.Fatalf("A's mark after the overriding catch-up: %+v", m)
	}
	if _, err := tr.propose(0, "Y"); err != nil {
		t.Fatalf("A's write at the new history with the witness: %v", err)
	}
	if m := tr.mark(2); m.History != 1 || !m.Synced || !strings.Contains(m.Superseded, ".superseded-") {
		t.Fatalf("the witness's mark: %+v", m)
	}

	// B returns: synced at history 0, holding Z.
	tr.setDown(1, false)
	if seq, _ := tr.holds(1, "Z"); seq == 0 {
		t.Fatal("setup: B lost Z")
	}
	before := len(tr.nodes[1].After(0))
	tr.nodes[0].Heartbeat(tr.ctx())
	if _, err := tr.propose(0, "Y2"); err != nil {
		t.Fatalf("A's write with B back: %v", err)
	}
	st := tr.nodes[1].Status()
	if !st.Unsynced || st.SupersedeTo != 1 || st.History != 0 {
		t.Fatalf("B after hearing history 1: %+v", st)
	}
	if got := len(tr.nodes[1].After(0)); got != before || tr.appliedTimes(1, "Y") != 0 {
		t.Fatalf("superseded B took the new history's entries: %d entries (had %d), Y applied %d", got, before, tr.appliedTimes(1, "Y"))
	}
	if tr.nodes[1].CanSync(time.Minute) {
		t.Fatal("superseded B can sync")
	}
	if m := tr.mark(1); m.SupersedeTo != 1 || m.Synced {
		t.Fatalf("B's supersede is not durable: %+v", m)
	}

	// B restarts: its log moves aside BEFORE it is replayed.
	tr.restart(1)
	if ids := tr.appliedIDs(1); len(ids) != 0 {
		t.Fatalf("B replayed the old history at its restart: %v", ids)
	}
	m := tr.mark(1)
	if m.History != 1 || m.SupersedeTo != 0 || m.Lost != "" || m.Synced || m.Held || !strings.Contains(m.Superseded, ".superseded-") {
		t.Fatalf("B's mark after its restart: %+v", m)
	}
	if b, err := os.ReadFile(m.Superseded); err != nil || !bytes.Contains(b, []byte(`"Z"`)) {
		t.Fatalf("B's old log was not kept at %s: %v", m.Superseded, err)
	}
	tr.nodes[0].Heartbeat(tr.ctx())
	if !tr.nodes[1].Synced() {
		t.Fatalf("B not synced from A after its restart: %+v", tr.nodes[1].Status())
	}
	for _, id := range []string{"X", "Y", "Y2"} {
		if seq, _ := tr.holds(1, id); seq == 0 {
			t.Fatalf("B lacks %s after syncing", id)
		}
	}
	if seq, _ := tr.holds(1, "Z"); seq != 0 {
		t.Fatal("B still holds the accepted-lost Z")
	}
	if tr.appliedTimes(1, "Z") != 0 {
		t.Fatal("B applied Z after its restart")
	}
}

// A leader that hears a higher history while replicating commits nothing
// it stored: it is the old history's.
func TestALeaderThatHearsANewerHistoryCommitsNothing(t *testing.T) {
	tr := newTrio(t, nil)
	mustSync(t, tr, 0)
	if _, err := tr.propose(0, "X"); err != nil {
		t.Fatal(err)
	}
	tr.editMark(1, func(m *Mark) { m.History = 1 })
	_, err := tr.propose(0, "Y")
	if !errors.Is(err, ErrUnconfirmed) {
		t.Fatalf("A committed a write after hearing history 1: %v", err)
	}
	if tr.appliedTimes(0, "Y") != 0 || tr.appliedTimes(2, "Y") != 0 {
		t.Fatal("the old history's write was applied")
	}
	if st := tr.nodes[0].Status(); st.SupersedeTo != 1 || !st.Unsynced {
		t.Fatalf("A after hearing history 1: %+v", st)
	}
	if err := tr.nodes[0].SyncLeader(tr.ctx()); err == nil {
		t.Fatal("superseded A synced again")
	}
}

// The acceptance binds the catch-up to the picture: a copy that answered
// at the vouch and is silent at the catch-up, or a later log beyond the
// one accepted, refuses ("vouch again").
func TestTheCatchUpOverridesOnlyWhatWasAccepted(t *testing.T) {
	t.Run("a copy silent that answered at the vouch", func(t *testing.T) {
		tr := silentBox(t, false)
		acceptLoss(t, tr, 0)
		tr.setDown(2, true)
		err := tr.nodes[0].SyncLeader(tr.ctx())
		if !errors.Is(err, ErrReadOnly) || !strings.Contains(err.Error(), "not what was accepted") || !strings.Contains(err.Error(), "vouch again") {
			t.Fatalf("A overrode the witness, never accepted: %v", err)
		}
		if m := tr.mark(0); !m.Alone || m.History != 0 {
			t.Fatalf("a refused catch-up spent the vouch: %+v", m)
		}
		tr.setDown(2, false)
		mustSync(t, tr, 0)
	})
	t.Run("a later log beyond the one accepted", func(t *testing.T) {
		tr := newTrio(t, nil)
		mustSync(t, tr, 0)
		if _, err := tr.propose(0, "X"); err != nil {
			t.Fatal(err)
		}
		tr.wipe(0)
		tr.wipe(2)
		// B: held, unsynced (mark unreadable), its log later than A's.
		tr.stop(1)
		os.WriteFile(tr.markFile(1), []byte("{"), 0o600)
		tr.start(1)
		pic := acceptLoss(t, tr, 0)
		if p := tr.mark(0).Accept.Peers[tr.urls[1]]; p.Silent || p.LastSeq == 0 {
			t.Fatalf("B accepted as %+v (picture %+v)", p, pic)
		}
		// B's log grows past what was accepted.
		tr.nodes[1].mu.Lock()
		_, err := tr.nodes[1].acceptLocked([]Entry{{Seq: tr.nodes[1].lastLocked() + 1, Term: 1, Kind: KindAck, IDs: []string{"late"}, By: "op"}})
		tr.nodes[1].mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		tr.lead(0)
		err = tr.nodes[0].SyncLeader(tr.ctx())
		if !errors.Is(err, ErrReadOnly) || !strings.Contains(err.Error(), "not what was accepted") {
			t.Fatalf("A overrode a later log than accepted: %v", err)
		}
	})
	t.Run("a later log accepted, now silent", func(t *testing.T) {
		tr := newTrio(t, nil)
		mustSync(t, tr, 0)
		if _, err := tr.propose(0, "X"); err != nil {
			t.Fatal(err)
		}
		tr.wipe(0)
		tr.wipe(2)
		tr.stop(1)
		os.WriteFile(tr.markFile(1), []byte("{"), 0o600)
		tr.start(1)
		acceptLoss(t, tr, 0)
		tr.setDown(1, true)
		tr.lead(0)
		if err := tr.nodes[0].SyncLeader(tr.ctx()); !errors.Is(err, ErrReadOnly) || !strings.Contains(err.Error(), "not what was accepted") {
			t.Fatalf("A overrode B silent, accepted only as later: %v", err)
		}
	})
}

// A counting answer is always taken: a copy accepted as silent that comes
// back counting gives its entries back, and nothing was overridden, so
// no new history starts.
func TestACopyAcceptedAsSilentThatCountsIsTaken(t *testing.T) {
	tr := silentBox(t, true)
	acceptLoss(t, tr, 0)
	tr.setDown(1, false) // B back, synced, holding Z
	mustSync(t, tr, 0)
	if seq, _ := tr.holds(0, "Z"); seq == 0 {
		t.Fatal("A's catch-up did not take Z from B, which counts")
	}
	if m := tr.mark(0); m.History != 0 || m.Alone || m.Accept != nil {
		t.Fatalf("A's mark after a catch-up that overrode nothing: %+v", m)
	}
}

// The dead end of a synced leader alone among the copies (B wiped, the
// witness rebuilt): a plain vouch refuses and prints the picture, the
// accepting one grants the one-shot; every copy answered and none held a
// later log, so nothing is overridden and the history stays; A's writes
// sync B and the witness.
func TestAcceptLossFreesASyncedLeaderAlone(t *testing.T) {
	tr := newTrio(t, nil)
	mustSync(t, tr, 0)
	if _, err := tr.propose(0, "X"); err != nil {
		t.Fatal(err)
	}
	tr.wipe(1)
	tr.wipe(2)
	tr.lead(0)
	if err := tr.nodes[0].SyncLeader(tr.ctx()); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("setup: %v", err)
	}
	if tr.nodes[0].CanSync(time.Minute) {
		t.Fatal("setup: A can sync")
	}
	pic := acceptLoss(t, tr, 0)
	if !pic.Self.Synced || len(pic.Copies) != 2 || pic.Copies[0].Counts || pic.Copies[1].Counts {
		t.Fatalf("picture: %+v", pic)
	}
	if !tr.nodes[0].CanSync(time.Minute) {
		t.Fatal("A can't sync after --accept-loss")
	}
	mustSync(t, tr, 0)
	if m := tr.mark(0); m.History != 0 {
		t.Fatalf("a catch-up that overrode nothing started a history: %+v", m)
	}
	if _, err := tr.propose(0, "Y"); err != nil {
		t.Fatal(err)
	}
	for _, i := range []int{1, 2} {
		if !tr.nodes[i].Synced() {
			t.Fatalf("n%d not synced by A's write", i)
		}
	}
}

// A lost log on both controllers, the witness synced: --accept-lost-log
// takes the witness's entries back. Without it the vouch refuses, naming
// the flag.
func TestAcceptLostLogTakesTheWitnessesEntries(t *testing.T) {
	tr := newTrio(t, nil)
	mustSync(t, tr, 0)
	if _, err := tr.propose(0, "X"); err != nil {
		t.Fatal(err)
	}
	tr.loseLog(0)
	tr.loseLog(1)
	_, pic, err := tr.nodes[0].Vouch(tr.ctx(), "op", nil, VouchAccept{})
	if !errors.Is(err, ErrVouchRefused) || !strings.Contains(err.Error(), "--accept-lost-log="+pic.Digest) || !slices_contains(pic.Needs, NeedLostLog) {
		t.Fatalf("a vouch for a lost log without --accept-lost-log: %v (%+v)", err, pic)
	}
	if slices_contains(pic.Needs, NeedLoss) {
		t.Fatalf("a lost log beside a synced witness needs --accept-loss: %+v", pic)
	}
	if _, _, err := tr.nodes[0].Vouch(tr.ctx(), "op", nil, VouchAccept{LostLog: pic.Digest}); err != nil {
		t.Fatal(err)
	}
	if m := tr.mark(0); m.Lost != "" || !m.Synced || m.Accept != nil {
		t.Fatalf("A's mark: %+v", m)
	}
	tr.lead(0)
	mustSync(t, tr, 0)
	if seq, _ := tr.holds(0, "X"); seq == 0 || tr.appliedTimes(0, "X") != 1 {
		t.Fatal("A did not take X back from the witness")
	}
	if tr.mark(0).History != 0 {
		t.Fatal("--accept-lost-log started a history")
	}
}

// An accepting run must see the picture the plain run printed: a
// different digest refuses and returns the new picture.
func TestADigestFromAnotherPictureRefuses(t *testing.T) {
	tr := silentBox(t, false)
	_, pic, _ := tr.nodes[0].Vouch(tr.ctx(), "op", nil, VouchAccept{})
	tr.setDown(2, true) // the witness is silent too now: the picture changed
	_, pic2, err := tr.nodes[0].Vouch(tr.ctx(), "op", nil, VouchAccept{Loss: pic.Digest})
	if err == nil && pic2.Digest == pic.Digest {
		t.Fatal("setup: the picture did not change")
	}
	if pic2.Digest == pic.Digest {
		t.Fatalf("same digest for two pictures: %+v / %+v", pic, pic2)
	}
	if !errors.Is(err, ErrVouchRefused) || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("a stale digest was taken: %v", err)
	}
	if m := tr.mark(0); m.Alone {
		t.Fatal("a refused vouch granted the one-shot")
	}
}

// The digest names the picture, not the moment: two plain runs on a quiet
// pair print the same one, and a silent copy's error text is left out.
func TestTheDigestIsStable(t *testing.T) {
	tr := silentBox(t, false)
	_, a, _ := tr.nodes[0].Vouch(tr.ctx(), "op", nil, VouchAccept{})
	_, b, _ := tr.nodes[0].Vouch(tr.ctx(), "op", nil, VouchAccept{})
	if a.Digest == "" || a.Digest != b.Digest {
		t.Fatalf("two runs, digests %q and %q", a.Digest, b.Digest)
	}
	c := a
	c.Copies = append([]CopyState(nil), a.Copies...)
	for i := range c.Copies {
		if !c.Copies[i].Answers {
			c.Copies[i].Err = "dial tcp: i/o timeout, another time"
		}
	}
	if c.digest() != a.Digest {
		t.Fatal("the error text changes the digest")
	}
}

// The overriding catch-up's new history is durable before the copy is
// synced for its term or writes: a failed mark write leaves the vouch
// unspent, and the next catch-up overrides and bumps.
func TestTheNewHistoryIsDurableBeforeTheCatchUpEnds(t *testing.T) {
	var mu sync.Mutex
	fail := false
	tr := newTrio(t, func(i int, o *Options) {
		witnessInPlace(i, o)
		if i == 0 {
			o.markWrite = func(m Mark) error {
				mu.Lock()
				defer mu.Unlock()
				if fail && m.History > 0 {
					return errors.New("test: disk full")
				}
				return nil
			}
		}
	})
	mustSync(t, tr, 0)
	if _, err := tr.propose(0, "X"); err != nil {
		t.Fatal(err)
	}
	tr.setDown(1, true)
	tr.wipe(2)
	tr.lead(0)
	acceptLoss(t, tr, 0)
	mu.Lock()
	fail = true
	mu.Unlock()
	if err := tr.nodes[0].SyncLeader(tr.ctx()); err == nil {
		t.Fatal("the catch-up ended though the new history wasn't recorded")
	}
	if tr.nodes[0].TermSynced() {
		t.Fatal("A synced for its term at the old history")
	}
	mu.Lock()
	fail = false
	mu.Unlock()
	mustSync(t, tr, 0)
	if m := tr.mark(0); m.History != 1 || m.Alone {
		t.Fatalf("A's mark after the retried catch-up: %+v", m)
	}
}

// Spent: the acceptance goes with the one-shot it was for, and with a
// sync by the normal path.
func TestTheAcceptanceIsSpent(t *testing.T) {
	t.Run("by the catch-up", func(t *testing.T) {
		tr := silentBox(t, false)
		acceptLoss(t, tr, 0)
		mustSync(t, tr, 0)
		if m := tr.mark(0); m.Accept != nil {
			t.Fatalf("the acceptance outlived its catch-up: %+v", m.Accept)
		}
	})
	t.Run("by a sync from a synced leader", func(t *testing.T) {
		tr := newTrio(t, nil)
		mustSync(t, tr, 0)
		if _, err := tr.propose(0, "X"); err != nil {
			t.Fatal(err)
		}
		tr.wipe(1)
		tr.setDown(2, true)
		_, pic, _ := tr.nodes[1].Vouch(tr.ctx(), "op", nil, VouchAccept{})
		if _, _, err := tr.nodes[1].Vouch(tr.ctx(), "op", nil, VouchAccept{Loss: pic.Digest}); err != nil {
			t.Fatal(err)
		}
		if tr.mark(1).Accept == nil {
			t.Fatal("setup: no acceptance")
		}
		tr.setDown(2, false)
		if _, err := tr.propose(0, "Y"); err != nil {
			t.Fatal(err)
		}
		// The write commits once the witness stored it; B's replication may
		// still be in flight then and be cancelled (a slow fsync: #1177 on
		// Windows). The heartbeat proves B's log for certain.
		tr.nodes[0].Heartbeat(tr.ctx())
		if m := tr.mark(1); m.Accept != nil || m.Alone {
			t.Fatalf("B synced by A's replication kept its acceptance: %+v", m)
		}
	})
}

// A copy at a lower history does not count, even one from before
// histories (an older binary, whose answers carry none and which takes
// any history's replication): the pair fails closed. At history 0 such a
// copy counts as before.
func TestALowerHistoryCopyDoesNotCount(t *testing.T) {
	strip := func(tr *trio, i int) {
		tr.net.set(i, stripHistory(tr.nodes[i].Handler()))
	}
	t.Run("history 0: an older copy counts", func(t *testing.T) {
		tr := newTrio(t, nil)
		strip(tr, 1)
		mustSync(t, tr, 0)
		tr.setDown(2, true)
		if _, err := tr.propose(0, "X"); err != nil {
			t.Fatalf("A with an older B at history 0: %v", err)
		}
	})
	t.Run("history 1: an older copy does not", func(t *testing.T) {
		tr := newTrio(t, nil)
		for _, i := range []int{0, 2} {
			tr.editMark(i, func(m *Mark) { m.History = 1 })
		}
		strip(tr, 1)
		mustSync(t, tr, 0)
		tr.setDown(2, true)
		if _, err := tr.propose(0, "X"); !errors.Is(err, ErrUnconfirmed) {
			t.Fatalf("an older B's answer counted at history 1: %v", err)
		}
		if st := tr.nodes[0].Status(); slices_contains(st.Synced, tr.urls[1]) {
			t.Fatalf("status counts the older B: %+v", st)
		}
	})
}

// stripHistory is a replica from before log histories: it neither reads
// nor answers one.
func stripHistory(h http.Handler) http.Handler {
	drop := func(b []byte) []byte {
		var m map[string]any
		if json.Unmarshal(b, &m) != nil {
			return b
		}
		delete(m, "history")
		delete(m, "supersedeTo")
		delete(m, "superseded")
		out, _ := json.Marshal(m)
		return out
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			b, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(drop(b)))
			r.ContentLength = -1
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		for k, v := range rec.Header() {
			w.Header()[k] = v
		}
		w.WriteHeader(rec.Code)
		w.Write(drop(rec.Body.Bytes()))
	})
}

// A copy keeps one superseded log, the newest; a start that died after
// moving the log aside, before its mark, finds it there.
func TestOneSupersededLogIsKept(t *testing.T) {
	var mu sync.Mutex
	now := time.Unix(1000, 0)
	tr := newTrio(t, func(i int, o *Options) {
		witnessInPlace(i, o)
		o.Now = func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	})
	mustSync(t, tr, 0)
	if _, err := tr.propose(0, "X"); err != nil {
		t.Fatal(err)
	}
	w := tr.nodes[2]
	for h := uint64(1); h <= 2; h++ {
		mu.Lock()
		now = now.Add(time.Second)
		mu.Unlock()
		w.mu.Lock()
		err := w.learnHistoryLocked(Lineage{History: h, HistoryID: h})
		w.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
	}
	kept, _ := filepath.Glob(tr.logFile(2) + ".superseded-*")
	if m := tr.mark(2); len(kept) != 1 || m.Superseded != kept[0] || m.History != 2 {
		t.Fatalf("kept %v, mark %+v", kept, m)
	}

	// B: superseded, its log moved aside by a start that died before its
	// mark.
	tr.stop(1)
	m, _ := readMark(tr.markFile(1))
	m.SupersedeTo = 3
	writeMark(tr.markFile(1), m)
	aside := tr.logFile(1) + ".superseded-1234"
	if err := os.Rename(tr.logFile(1), aside); err != nil {
		t.Fatal(err)
	}
	tr.start(1)
	if m := tr.mark(1); m.History != 3 || m.Superseded != aside || m.Lost != "" {
		t.Fatalf("B's mark: %+v", m)
	}
}

// A fork: two overrides, one on each side, each accepting the other
// controller as silent, both start history 1 under different ids (the
// first's heartbeat never reached the witness). When they meet, neither counts the other, neither
// supersedes, and both say so (Status.Forked); a write from either
// side is refused by the other, never taken. A person then chooses one:
// a vouch on it needs --accept-loss for the forked copies even though it
// is synced, and its catch-up starts history 2, which supersedes the
// other side.
func TestAForkFailsClosedUntilAPersonChooses(t *testing.T) {
	tr := newTrio(t, witnessInPlace)
	mustSync(t, tr, 0)
	if _, err := tr.propose(0, "X"); err != nil {
		t.Fatal(err)
	}
	// The half-lease window (i) leaves: A overrides silent B, the rebuilt
	// witness answering, and dies before its first heartbeat moves the
	// witness to A's history; B then overrides silent A, the witness still
	// at history 0, and the witness takes B's.
	tr.setDown(1, true)
	tr.wipe(2)
	tr.lead(0)
	acceptLoss(t, tr, 0)
	mustSync(t, tr, 0)
	tr.setDown(0, true)
	tr.setDown(1, false)
	tr.lead(1)
	acceptLoss(t, tr, 1)
	mustSync(t, tr, 1)
	tr.nodes[1].Heartbeat(tr.ctx())
	if _, err := tr.propose(1, "B1"); err != nil {
		t.Fatalf("B's write with the witness: %v", err)
	}
	tr.setDown(0, false)
	a, b := tr.mark(0).Lineage, tr.mark(1).Lineage
	if a.History != 1 || b.History != 1 || a.HistoryID == b.HistoryID || a.HistoryID == 0 {
		t.Fatalf("setup: A %+v, B %+v", a, b)
	}
	if w := tr.mark(2).Lineage; !w.same(b) {
		t.Fatalf("the witness at %+v, B at %+v", w, b)
	}
	// A leads: both other copies forked from it. Nothing counts, nothing
	// is superseded, nothing of B's side is taken.
	tr.lead(0)
	if err := tr.nodes[0].SyncLeader(tr.ctx()); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("A synced over a fork: %v", err)
	}
	tr.nodes[1].Ping(tr.ctx())
	for i, want := range []int{2, 1} {
		st := tr.nodes[i].Status()
		if len(st.Forked) != want || st.SupersedeTo != 0 || st.Unsynced {
			t.Fatalf("n%d's status: forked %+v, %+v", i, st.Forked, st)
		}
	}
	if seq, _ := tr.holds(0, "B1"); seq != 0 {
		t.Fatal("A took B's side's entry")
	}
	// B, leading again, gets nothing from A either.
	aLog := len(tr.nodes[0].After(0))
	tr.lead(1)
	mustSync(t, tr, 1)
	tr.nodes[1].Heartbeat(tr.ctx())
	if st := tr.nodes[0].Status(); st.Unsynced || st.SupersedeTo != 0 || len(tr.nodes[0].After(0)) != aLog {
		t.Fatalf("A after B's heartbeat: %+v, log %v", st, tr.nodes[0].After(0))
	}

	// A person chooses A.
	tr.lead(0)
	pic := acceptLoss(t, tr, 0)
	if !pic.Self.Synced || !pic.Copies[0].Forked || !pic.Copies[1].Forked {
		t.Fatalf("picture: %+v", pic)
	}
	mustSync(t, tr, 0)
	if m := tr.mark(0); m.History != 2 {
		t.Fatalf("A's catch-up over the accepted fork started no new history: %+v", m)
	}
	if _, err := tr.propose(0, "A2"); err != nil {
		t.Fatalf("A's write after choosing it: %v", err)
	}
	if st := tr.nodes[1].Status(); st.SupersedeTo != 2 {
		t.Fatalf("B not superseded by history 2: %+v", st)
	}
	tr.restart(1)
	tr.nodes[0].Heartbeat(tr.ctx())
	if !tr.nodes[1].Synced() {
		t.Fatalf("B not synced after its restart: %+v", tr.nodes[1].Status())
	}
	if seq, _ := tr.holds(1, "B1"); seq != 0 {
		t.Fatal("B still holds its side's entry")
	}
	if seq, _ := tr.holds(1, "A2"); seq == 0 {
		t.Fatal("B lacks A2")
	}
}

// (i): an override with every other copy silent is refused, its vouch
// kept: the new history would sit on this copy alone, and the other two,
// back at the old one, could commit there and lose it at first contact.
// override_all_silent turns it red.
func TestAnOverrideWithEveryOtherCopySilentIsRefused(t *testing.T) {
	tr := newTrio(t, witnessInPlace)
	mustSync(t, tr, 0)
	if _, err := tr.propose(0, "X"); err != nil {
		t.Fatal(err)
	}
	tr.setDown(1, true)
	tr.setDown(2, true)
	tr.lead(0)
	acceptLoss(t, tr, 0)
	err := tr.nodes[0].SyncLeader(tr.ctx())
	if !errors.Is(err, ErrReadOnly) || !strings.Contains(err.Error(), "every other copy silent") {
		t.Fatalf("an override with both other copies silent: %v", err)
	}
	if m := tr.mark(0); m.History != 0 || !m.Alone {
		t.Fatalf("the refused override changed the mark: %+v", m)
	}
	tr.setDown(2, false)
	mustSync(t, tr, 0) // the witness counts: nothing to override
	if m := tr.mark(0); m.History != 0 {
		t.Fatalf("history %d with the witness back", m.History)
	}
}

// Ruling (a): once a pair is at a history above 0, a controller whose
// state was wiped (a new identity, an empty log at history 0) learns it on
// first contact and takes it at one restart: until then it takes nothing
// and is not lost; after it, it is at the pair's history and syncs.
func TestAWipedControllerJoiningAPairAtHistoryOneRestartsOnce(t *testing.T) {
	tr := silentBox(t, false)
	acceptLoss(t, tr, 0)
	mustSync(t, tr, 0)
	if _, err := tr.propose(0, "Y"); err != nil {
		t.Fatal(err)
	}
	tr.setDown(1, false)
	tr.wipe(1)
	tr.nodes[0].Heartbeat(tr.ctx())
	st := tr.nodes[1].Status()
	if st.SupersedeTo != 1 || st.Lost != "" || st.LastSeq != 0 || !st.Unsynced {
		t.Fatalf("the wiped controller on first contact: %+v", st)
	}
	tr.restart(1)
	if st := tr.nodes[1].Status(); st.History != 1 || st.SupersedeTo != 0 || st.Lost != "" {
		t.Fatalf("after its restart: %+v", st)
	}
	tr.nodes[0].Heartbeat(tr.ctx())
	if !tr.nodes[1].Synced() {
		t.Fatalf("not synced after its restart: %+v", tr.nodes[1].Status())
	}
	if seq, _ := tr.holds(1, "Y"); seq == 0 {
		t.Fatal("it lacks Y")
	}
}

// The #1053 rule beside (i): a vouched catch-up with one copy answering
// (B, wiped: unsynced, empty) and the witness silent, never accepted,
// waits; X, which B and the witness committed while A was cut off, comes
// back once the witness answers. vouch_alone_partial turns it red (in
// TestDoubleVouchWithASilentWitnessKeepsTheWrite both others are silent,
// which (i) refuses first).
func TestAVouchWaitsForAnUnacceptedSilentCopyThoughAnotherAnswers(t *testing.T) {
	tr := unsyncedTrio(t)
	for _, i := range []int{0, 1} {
		if _, _, err := tr.nodes[i].Vouch(tr.ctx(), "op", nil, VouchAccept{}); err != nil {
			t.Fatalf("vouch n%d: %v", i, err)
		}
	}
	tr.lead(1)
	mustSync(t, tr, 1)
	tr.setDown(0, true)
	if _, err := tr.propose(1, "X"); err != nil {
		t.Fatalf("X on B and W: %v", err)
	}
	tr.setDown(0, false)
	tr.wipe(1)
	tr.net.mu.Lock()
	tr.net.silent[2] = true
	tr.net.mu.Unlock()
	tr.lead(0)
	if err := tr.nodes[0].SyncLeader(tr.ctx()); err == nil {
		t.Fatal("A caught up on its vouch with the witness silent, never accepted")
	}
	tr.net.mu.Lock()
	tr.net.silent[2] = false
	tr.net.mu.Unlock()
	mustSync(t, tr, 0)
	if seq, n := tr.holds(0, "X"); seq == 0 || n != 1 {
		t.Fatalf("A after the witness answered: X at %d, %d times", seq, n)
	}
}

// A spend that can't be written fails closed (#1177 CI, naut-lead): the
// one-shot is spent in this process at once, so nothing here uses it
// again, and the durable write is retried at the next proving
// replication. spend_failure_keeps_alone turns it red.
func TestASpendThatCantBeWrittenIsSpentInMemoryAndRetried(t *testing.T) {
	var mu sync.Mutex
	fail := false
	tr := newTrio(t, func(i int, o *Options) {
		if i == 1 {
			o.markWrite = func(m Mark) error {
				mu.Lock()
				defer mu.Unlock()
				if fail && m.Vouch != nil && !m.Alone {
					return errors.New("test: the spend can't be written")
				}
				return nil
			}
		}
	})
	mustSync(t, tr, 0)
	if _, err := tr.propose(0, "X"); err != nil {
		t.Fatal(err)
	}
	tr.wipe(1)
	tr.setDown(2, true)
	_, pic, _ := tr.nodes[1].Vouch(tr.ctx(), "op", nil, VouchAccept{})
	if _, _, err := tr.nodes[1].Vouch(tr.ctx(), "op", nil, VouchAccept{Loss: pic.Digest}); err != nil {
		t.Fatal(err)
	}
	tr.setDown(2, false)
	mu.Lock()
	fail = true
	mu.Unlock()
	tr.nodes[0].Heartbeat(tr.ctx())
	if m := tr.mark(1); !m.Alone {
		t.Fatalf("setup: the spend was written: %+v", m)
	}
	// Read the mark held in memory, not CanSync: CanSync also counts a
	// peer heard within its bound, and a coarse clock (Windows) can make
	// "now minus the last contact" zero (#1177 CI).
	n := tr.nodes[1]
	n.mu.Lock()
	alone, accept := n.mark.Alone, n.mark.Accept
	n.mu.Unlock()
	if alone || accept != nil {
		t.Fatalf("a one-shot whose spend failed is still armed in memory: alone %v, accept %+v", alone, accept)
	}
	mu.Lock()
	fail = false
	mu.Unlock()
	tr.nodes[0].Heartbeat(tr.ctx())
	if m := tr.mark(1); m.Alone || m.Accept != nil {
		t.Fatalf("the owed spend was not written at the next replication: %+v", m)
	}
}

// A vouch's one-shot and what the person accepted hold for the boot they
// were given in (naut-lead, #1177): a restart drops them, so a spend lost
// to a crash never re-arms an acceptance of a picture that may no longer
// be true. The copy stays synced; vouching again against the picture as
// it is now goes ahead. accept_survives_reboot turns it red.
func TestAVouchDoesNotSurviveARestart(t *testing.T) {
	tr := silentBox(t, false)
	acceptLoss(t, tr, 0)
	tr.restart(0) // vouched, then a crash before the catch-up
	if m := tr.mark(0); m.Alone || m.Accept != nil || !m.Synced {
		t.Fatalf("the mark after the restart: %+v", m)
	}
	tr.lead(0)
	if err := tr.nodes[0].SyncLeader(tr.ctx()); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("a vouch from before the restart was used: %v", err)
	}
	acceptLoss(t, tr, 0) // again, against the picture now
	mustSync(t, tr, 0)
	if m := tr.mark(0); m.History != 1 {
		t.Fatalf("after vouching again: %+v", m)
	}
}
