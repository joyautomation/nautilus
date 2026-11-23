package checkout

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// loglet is a stand-in for the operator-intent log: one sequencer that numbers
// each proposal, stamps it, and applies it to every replica in that order
// before answering (as a committed entry is applied everywhere).
type loglet struct {
	mu       sync.Mutex
	seq      uint64
	replicas []*Store
	down     bool             // no majority
	slow     bool             // an answer that never comes
	before   func(seq uint64) // runs after numbering, before applying (to interleave)
	after    func(seq uint64) // runs after applying, before the proposer is answered
}

func (l *loglet) proposeFor(me *Store) func(context.Context, string, []byte) (uint64, error) {
	return func(ctx context.Context, by string, body []byte) (uint64, error) {
		l.mu.Lock()
		defer l.mu.Unlock()
		if l.down {
			return 0, ErrNoMajority
		}
		if l.slow {
			return 0, ErrUnknown
		}
		l.seq++
		ts := int64(1_000_000 + l.seq)
		if l.before != nil {
			l.before(l.seq)
		}
		mine := l.seq
		for _, r := range l.replicas {
			r.ApplyLogged(mine, ts, body)
		}
		if l.after != nil {
			l.after(mine)
		}
		return mine, nil
	}
}

func newPairStores(t *testing.T) (*loglet, *Store, *Store) {
	t.Helper()
	l := &loglet{}
	mk := func() *Store {
		s, err := OpenLog(tmpDir(t), nil)
		if err != nil {
			t.Fatal(err)
		}
		s.SetSheet(sheet())
		s.AttachLog(l.proposeFor(s))
		l.replicas = append(l.replicas, s)
		return s
	}
	return l, mk(), mk()
}

func passAllLogged(t *testing.T, s *Store, run, by string) {
	t.Helper()
	for _, pt := range sheet().Points {
		if _, err := s.Record(run, pt.ID, Pass, "", 1.5, "good", by, "user:"+by); err != nil {
			t.Fatal(err)
		}
	}
}

// What is recorded on either controller is on both, and the canonical results
// file is the same bytes on both.
func TestAPairHoldsOneRunOnBothControllers(t *testing.T) {
	_, a, b := newPairStores(t)
	run, err := a.Begin(false, sheet().Hash(), "T", "alice", "user:alice")
	if err != nil {
		t.Fatal(err)
	}
	// recorded on the other controller (a standby's HMI forwards to the leader)
	if _, err := b.Record(run.ID, "XV101", Pass, "", 2, "good", "bob", "user:bob"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Record(run.ID, "LSH101", Fail, "stuck", 0, "good", "alice", "user:alice"); err != nil {
		t.Fatal(err)
	}
	for _, s := range []*Store{a, b} {
		if got, ok := s.Get(run.ID); !ok || got.Results != 2 || got.By != "alice" {
			t.Errorf("a controller's view: %+v %v", got, ok)
		}
		if l := s.Latest(run.ID); l["XV101"].By != "bob" || l["LSH101"].Outcome != Fail {
			t.Errorf("latest %+v", l)
		}
	}
	if !bytes.Equal(a.ExportChain(), b.ExportChain()) || len(a.ExportChain()) == 0 {
		t.Errorf("the controllers hold different canonical chains:\n%s\n%s", a.ExportChain(), b.ExportChain())
	}
}

// A second run begun on the other controller at the same moment loses: every
// replica skips it alike and the proposer is told.
func TestARunBegunTwiceOnAPairLosesTheSecond(t *testing.T) {
	_, a, b := newPairStores(t)
	if _, err := a.Begin(false, sheet().Hash(), "T", "alice", "user:alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Begin(false, sheet().Hash(), "T", "bob", "user:bob"); !errors.Is(err, ErrRunOpen) {
		t.Errorf("the second begin: %v, want ErrRunOpen", err)
	}
	if len(a.List()) != 1 || len(b.List()) != 1 {
		t.Errorf("runs a=%d b=%d", len(a.List()), len(b.List()))
	}
	if !bytes.Equal(a.ExportChain(), b.ExportChain()) {
		t.Errorf("a skipped entry is in one chain")
	}
}

// No majority: nothing is recorded anywhere and nothing is queued; an answer
// that never comes is "unknown".
func TestAPairRefusesWritesWithoutAMajority(t *testing.T) {
	l, a, b := newPairStores(t)
	l.down = true
	if _, err := a.Begin(false, sheet().Hash(), "T", "alice", "user:alice"); !errors.Is(err, ErrNoMajority) {
		t.Errorf("begin without a majority: %v", err)
	}
	l.down, l.slow = false, true
	if _, err := a.Begin(false, sheet().Hash(), "T", "alice", "user:alice"); !errors.Is(err, ErrUnknown) {
		t.Errorf("begin with no answer: %v", err)
	}
	l.slow = false
	if len(a.List())+len(b.List()) != 0 {
		t.Errorf("a refused begin was recorded")
	}
	if _, err := a.Begin(false, sheet().Hash(), "T", "alice", "user:alice"); err != nil {
		t.Errorf("begin once the log is back: %v", err)
	}
}

// A seal signed on one controller is served, and verifies against the
// canonical chain, from the other.
func TestASealOnOneControllerVerifiesFromTheOther(t *testing.T) {
	_, a, b := newPairStores(t)
	run, _ := a.Begin(false, sheet().Hash(), "T", "alice", "user:alice")
	passAllLogged(t, b, run.ID, "alice")
	sr, err := a.Seal(sealIn(run.ID, "bob"))
	if err != nil {
		t.Fatal(err)
	}
	for name, s := range map[string]*Store{"the signer": a, "the other": b} {
		rec, err := s.ReadSealed(run.ID)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		rep, err := Verify(rec, a.ExportChain())
		if err != nil || !rep.ChainChecked || rep.Signer != "bob" {
			t.Fatalf("%s: verify %+v %v", name, rep, err)
		}
		if g, _ := s.Get(run.ID); g.State != StateSealed || g.SealedBy != "bob" {
			t.Errorf("%s: %+v", name, g)
		}
	}
	// the signer's key, on its own controller; the record names it
	var got SealedRecord
	rec, _ := b.ReadSealed(run.ID)
	json.Unmarshal(rec, &got)
	if got.PublicKey != sr.PublicKey {
		t.Errorf("the other controller serves another key")
	}
	if _, err := a.Record(run.ID, "XV101", Fail, "late", nil, "", "alice", "user:alice"); !errors.Is(err, ErrNotOpen) {
		t.Errorf("a result after the seal: %v", err)
	}
}

// An entry that commits between a seal being signed and being applied makes
// apply refuse the seal; the signer builds it again and it seals.
func TestASealThatRacedAResultIsSignedAgain(t *testing.T) {
	l, a, b := newPairStores(t)
	run, _ := a.Begin(false, sheet().Hash(), "T", "alice", "user:alice")
	passAllLogged(t, a, run.ID, "alice")
	raced := false
	l.before = func(seq uint64) {
		if raced {
			return
		}
		raced = true
		// another controller's entry commits first (the log is single-writer: this goes through its sequencer's lock held, so apply it directly as the next entry)
		body, _ := json.Marshal(entry{Kind: kindResult, Run: run.ID, Point: "LSH101", Outcome: Pass, Note: "rechecked", By: "carol", Principal: "user:carol"})
		for _, r := range l.replicas {
			r.ApplyLogged(seq, 1_000_000+int64(seq), body)
		}
		l.seq++ // the seal takes the next number
	}
	if _, err := a.Seal(sealIn(run.ID, "bob")); err != nil {
		t.Fatalf("a raced seal: %v", err)
	}
	rec, err := b.ReadSealed(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(rec, b.ExportChain()); err != nil {
		t.Errorf("the seal made after the race: %v", err)
	}
	if !strings.Contains(string(rec), "rechecked") {
		t.Errorf("the result that raced the seal is not in the signed export")
	}
}

// A replica that rebuilds a record that does not match what was signed (another
// sheet) refuses to serve it.
func TestAReplicaWithAnotherSheetRefusesTheSealedRecord(t *testing.T) {
	_, a, b := newPairStores(t)
	run, _ := a.Begin(false, sheet().Hash(), "T", "alice", "user:alice")
	passAllLogged(t, a, run.ID, "alice")
	if _, err := a.Seal(sealIn(run.ID, "bob")); err != nil {
		t.Fatal(err)
	}
	// a replica that replayed after the sheet changed has no cached record
	os.Remove(b.SealedPath(run.ID))
	other := sheet()
	other.Points[0].Expect = "edited"
	b.SetSheet(other)
	if _, err := b.ReadSealed(run.ID); err == nil || !strings.Contains(err.Error(), "does not hold the sheet") {
		t.Errorf("a replica with another sheet: %v", err)
	}
	b.SetSheet(nil)
	if _, err := b.ReadSealed(run.ID); err == nil {
		t.Errorf("a replica with no sheet served the record")
	}
}

// Replaying the same committed sequence into a fresh store gives the same
// store, byte for byte (a replica catching up, or a restart), and Reset takes
// a superseded history out.
func TestReplayGivesTheSameStoreAndResetEmptiesIt(t *testing.T) {
	_, a, _ := newPairStores(t)
	run, _ := a.Begin(false, sheet().Hash(), "T", "alice", "user:alice")
	passAllLogged(t, a, run.ID, "alice")
	a.Seal(sealIn(run.ID, "bob"))
	fresh, _ := OpenLog(tmpDir(t), nil)
	fresh.SetSheet(sheet())
	fresh.AttachLog(func(context.Context, string, []byte) (uint64, error) { return 0, ErrNoMajority }) // a replica catching up proposes nothing
	// replay what the leader's log holds: its canonical chain, entry by entry
	for i, line := range bytes.Split(bytes.TrimRight(a.ExportChain(), "\n"), []byte("\n")) {
		var e entry
		json.Unmarshal(line, &e)
		e.Seq, e.Prev = 0, ""
		body, _ := json.Marshal(e)
		fresh.ApplyLogged(uint64(i+1), e.At, body)
	}
	if !bytes.Equal(fresh.ExportChain(), a.ExportChain()) {
		t.Errorf("a replayed store's chain differs")
	}
	if _, err := fresh.ReadSealed(run.ID); err != nil {
		t.Errorf("a replayed store cannot serve the sealed record: %v", err)
	}
	fresh.Reset()
	if len(fresh.List()) != 0 || len(fresh.ExportChain()) != 0 {
		t.Errorf("Reset left %d runs", len(fresh.List()))
	}
}

// The commit's apply takes the store's lock while the proposer waits: that is
// not a deadlock, and concurrent writers on both controllers all land.
func TestConcurrentWritersOnAPairAllLand(t *testing.T) {
	_, a, b := newPairStores(t)
	run, _ := a.Begin(false, sheet().Hash(), "T", "alice", "user:alice")
	var wg sync.WaitGroup
	done := make(chan struct{})
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s := a
			if i%2 == 1 {
				s = b
			}
			if _, err := s.Record(run.ID, "XV101", Note, "n", i, "good", "alice", "user:alice"); err != nil {
				t.Errorf("record %d: %v", i, err)
			}
		}(i)
	}
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("deadlock: concurrent writers on a pair did not finish")
	}
	if g, _ := a.Get(run.ID); g.Results != 20 {
		t.Errorf("results %d, want 20", g.Results)
	}
	if !bytes.Equal(a.ExportChain(), b.ExportChain()) {
		t.Errorf("chains differ")
	}
}

// A takeover ends a force the old leader left standing in the log.
func TestATakeoverEndsAForceTheLogStillHas(t *testing.T) {
	_, a, b := newPairStores(t)
	run, _ := a.Begin(false, sheet().Hash(), "T", "alice", "user:alice")
	if _, err := a.StartForce(run.ID, "XV101", true, 30, "alice", "user:alice"); err != nil {
		t.Fatal(err)
	}
	if _, ok := b.Standing(); !ok {
		t.Fatal("the other controller does not see the force")
	}
	n, err := b.InterruptStanding() // the new leader
	if err != nil || n != 1 {
		t.Fatalf("interrupt: %d %v", n, err)
	}
	if _, ok := a.Standing(); ok {
		t.Errorf("the force still stands on the old leader's view")
	}
	if got := a.Latest(run.ID); got != nil && len(got) > 0 {
		t.Logf("latest %v", got)
	}
	// a seal is refused while it stands, accepted after
	passAllLogged(t, a, run.ID, "alice")
	if _, err := a.Seal(sealIn(run.ID, "bob")); err != nil {
		t.Errorf("seal after the interrupt: %v", err)
	}
	_ = os.Stdout
}

// A replica whose copy of the entries differs from the signer's (histories
// that diverged) does not serve the record: what it rebuilds does not hash to
// what was signed.
func TestAReplicaWithAnotherHistoryRefusesTheSealedRecord(t *testing.T) {
	_, a, b := newPairStores(t)
	run, _ := a.Begin(false, sheet().Hash(), "T", "alice", "user:alice")
	passAllLogged(t, a, run.ID, "alice")
	if _, err := a.Seal(sealIn(run.ID, "bob")); err != nil {
		t.Fatal(err)
	}
	// b's copy holds a result a's does not: not the history the signer signed
	os.Remove(b.SealedPath(run.ID))
	b.mu.Lock()
	b.runs[run.ID].lastResults["XV101"] = append(b.runs[run.ID].lastResults["XV101"], Result{Run: run.ID, Seq: 99, Point: "XV101", Outcome: Fail, Note: "extra", By: "x"})
	b.mu.Unlock()
	if _, err := b.ReadSealed(run.ID); err == nil || !strings.Contains(err.Error(), "does not match what was signed") {
		t.Errorf("a replica with another history: %v", err)
	}
}

// Two controllers begin a run in the same moment (neither saw the other's):
// the log orders them, apply on every replica accepts the first and skips the
// second, and the second's proposer is told so.
func TestTwoBeginsInTheSameMomentLoseTheSecondOnEveryReplica(t *testing.T) {
	l, a, b := newPairStores(t)
	l.before = func(seq uint64) {
		l.before = nil
		// the other controller's begin is ordered first
		body, _ := json.Marshal(entry{Kind: kindRun, Run: "firstrun", SheetHash: sheet().Hash(), By: "bob", Principal: "user:bob"})
		for _, r := range l.replicas {
			r.ApplyLogged(seq, 1_000_000+int64(seq), body)
		}
		l.seq++
	}
	if _, err := a.Begin(false, sheet().Hash(), "T", "alice", "user:alice"); !errors.Is(err, ErrRunOpen) {
		t.Fatalf("the begin that lost: %v, want ErrRunOpen", err)
	}
	for name, s := range map[string]*Store{"a": a, "b": b} {
		if rs := s.List(); len(rs) != 1 || rs[0].ID != "firstrun" {
			t.Errorf("%s holds %+v", name, rs)
		}
	}
	if !bytes.Equal(a.ExportChain(), b.ExportChain()) {
		t.Errorf("the chains differ after a skipped entry")
	}
}

// An end that arrives late, for a force that is gone, does not end the next
// force on the same point (a timer on the controller that lost the lead, or an
// interrupt written by two leaders in turn).
func TestALateEndDoesNotEndTheNextForce(t *testing.T) {
	_, a, b := newPairStores(t)
	run, _ := a.Begin(false, sheet().Hash(), "T", "alice", "user:alice")
	f1, err := a.StartForce(run.ID, "XV101", true, 30, "alice", "user:alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := b.EndForceOf(run.ID, f1.Seq, ForceInterrupted, "controller"); err != nil { // the new leader's interrupt
		t.Fatal(err)
	}
	f2, err := b.StartForce(run.ID, "XV101", false, 30, "bob", "user:bob")
	if err != nil {
		t.Fatal(err)
	}
	// the old leader's expiry timer fires late, for the first force
	if err := a.EndForceOf(run.ID, f1.Seq, ForceExpired, "controller"); !errors.Is(err, ErrNoForce) {
		t.Errorf("a late end for the first force: %v, want ErrNoForce", err)
	}
	if st, ok := b.Standing(); !ok || st.Seq != f2.Seq {
		t.Errorf("the second force no longer stands: %+v %v", st, ok)
	}
	// and the same through apply, when the late end is already in the log
	body, _ := json.Marshal(entry{Kind: kindForceEnd, Run: run.ID, Point: "XV101", PrevSeq: f1.Seq, Reason: ForceExpired, By: "controller"})
	a.ApplyLogged(1000, 2_000_000, body)
	if st, ok := a.Standing(); !ok || st.Seq != f2.Seq {
		t.Errorf("apply let a late end stop the next force: %+v %v", st, ok)
	}
}

// A seal with no signed payload is refused at apply on every replica: it would
// leave a sealed run nobody can serve a record for.
func TestASealWithoutAPayloadIsRefusedAtApply(t *testing.T) {
	_, a, b := newPairStores(t)
	run, _ := a.Begin(false, sheet().Hash(), "T", "alice", "user:alice")
	passAllLogged(t, a, run.ID, "alice")
	body, _ := json.Marshal(entry{Kind: kindSeal, Run: run.ID, SealSha: "x", By: "bob", Principal: "user:bob"})
	for _, s := range []*Store{a, b} {
		s.ApplyLogged(500, 3_000_000, body)
		if g, _ := s.Get(run.ID); g.State != StateOpen {
			t.Errorf("a seal with no payload sealed the run: %+v", g)
		}
	}
}

// A record kept when the seal was applied is still served after checkout.yaml
// is edited for the next phase: it does not depend on today's sheet.
func TestASealedRecordSurvivesAnEditOfTheSheet(t *testing.T) {
	_, a, b := newPairStores(t)
	run, _ := a.Begin(false, sheet().Hash(), "T", "alice", "user:alice")
	passAllLogged(t, a, run.ID, "alice")
	if _, err := a.Seal(sealIn(run.ID, "bob")); err != nil {
		t.Fatal(err)
	}
	next := sheet()
	next.Points = append(next.Points, Point{ID: "NEW", Tag: "n", Kind: DI, Method: Observe})
	a.SetSheet(next)
	b.SetSheet(next)
	for name, s := range map[string]*Store{"the signer": a, "the other": b} {
		rec, err := s.ReadSealed(run.ID)
		if err != nil {
			t.Fatalf("%s after the sheet changed: %v", name, err)
		}
		if _, err := Verify(rec, a.ExportChain()); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// What apply checks of a seal, from the entries and the chain alone: the
// signature, the key, the hash, the run, the signer, the two-person rule and
// the results. A forged or inconsistent seal closes nothing.
func TestApplyRefusesASealThatIsNotWhatItSaysItIs(t *testing.T) {
	l, a, b := newPairStores(t)
	run, _ := a.Begin(false, sheet().Hash(), "T", "alice", "user:alice")
	passAllLogged(t, a, run.ID, "alice")
	// build a genuine payload with the controller's own key, then break one thing at a time
	mk := func(edit func(*SealPayload, *entry)) bool {
		pl := SealPayload{Format: SealFormat, Notice: SealNotice, Run: run.ID, Mode: "two-person", Signer: "bob", SignerPrincipal: "user:bob",
			Recorders: []string{"user:alice"}, PrevSeq: a.seq, PrevHash: a.prev, KeyFingerprint: Fingerprint(a.key.pub), ExportSha256: "0"}
		e := entry{Kind: kindSeal, Run: run.ID, By: "bob", Principal: "user:bob", PrevSeq: a.seq, PubKey: b64pub(a.key)}
		if edit != nil {
			edit(&pl, &e)
		}
		pb, _ := json.Marshal(pl)
		e.Payload, e.SealSha = pb, PayloadSha256(pb)
		e.Sig = base64.StdEncoding.EncodeToString(ed25519.Sign(a.key.priv, pb))
		if e.Note == "badsig" {
			e.Sig = base64.StdEncoding.EncodeToString(make([]byte, 64))
			e.Note = ""
		}
		if e.Note == "badsha" {
			e.SealSha, e.Note = strings.Repeat("0", 64), ""
		}
		body, _ := json.Marshal(e)
		l.mu.Lock()
		l.seq++
		seq := l.seq
		l.mu.Unlock()
		for _, r := range l.replicas {
			r.ApplyLogged(seq, 9_000_000+int64(seq), body)
		}
		g, _ := a.Get(run.ID)
		return g.State == StateSealed
	}
	bad := map[string]func(*SealPayload, *entry){
		"a signature that does not match":  func(p *SealPayload, e *entry) { e.Note = "badsig" },
		"a hash that is not the payload's": func(p *SealPayload, e *entry) { e.Note = "badsha" },
		"another run's payload":            func(p *SealPayload, e *entry) { p.Run = "otherrun" },
		"a key the payload does not name":  func(p *SealPayload, e *entry) { p.KeyFingerprint = "00000000-00000000-00000000-00000000" },
		"another signer than the entry":    func(p *SealPayload, e *entry) { p.Signer = "eve" },
		"a place in the chain that moved":  func(p *SealPayload, e *entry) { p.PrevSeq = a.seq - 1 },
		"the person who took part (two-person)": func(p *SealPayload, e *entry) {
			p.SignerPrincipal, e.Principal, p.Signer, e.By = "user:alice", "user:alice", "alice", "alice"
		},
	}
	for name, edit := range bad {
		if mk(edit) {
			t.Errorf("a seal with %s closed the run", name)
		}
	}
	// and a point whose latest result is a fail
	a.Record(run.ID, "LSH101", Fail, "stuck", nil, "", "alice", "user:alice")
	if mk(nil) {
		t.Errorf("a seal over a failing point closed the run")
	}
	a.Record(run.ID, "LSH101", Pass, "", nil, "", "alice", "user:alice")
	for _, s := range []*Store{a, b} {
		if g, _ := s.Get(run.ID); g.State != StateOpen {
			t.Errorf("a refused seal left the run %s", g.State)
		}
	}
}

// An entry a newer build wrote, or of a kind this build does not know, stops
// this replica's checkout (it says so) rather than let it go on to a state its
// peer's differs from.
func TestAReplicaThatCannotReadAnEntryHaltsInsteadOfDiverging(t *testing.T) {
	_, a, _ := newPairStores(t)
	run, _ := a.Begin(false, sheet().Hash(), "T", "alice", "user:alice")
	body, _ := json.Marshal(map[string]any{"v": 2, "kind": "result", "run": run.ID, "point": "XV101", "outcome": "pass", "by": "x"})
	a.ApplyLogged(900, 1, body)
	if why := a.Halted(); !strings.Contains(why, "newer build") {
		t.Fatalf("halted: %q", why)
	}
	if _, err := a.Record(run.ID, "XV101", Pass, "", nil, "", "alice", "user:alice"); err == nil || !strings.Contains(err.Error(), "newer build") {
		t.Errorf("a write after the halt: %v", err)
	}
	_, a2, _ := newPairStores(t)
	body2, _ := json.Marshal(map[string]any{"kind": "futurething", "run": "x"})
	a2.ApplyLogged(1, 1, body2)
	if why := a2.Halted(); !strings.Contains(why, "does not know") {
		t.Errorf("an unknown kind: %q", why)
	}
}

// What a proposer is told is the entry it proposed, as apply made it, not
// whatever the store holds once the lock is retaken.
func TestAWriterIsToldItsOwnResultNotALaterOne(t *testing.T) {
	l, a, _ := newPairStores(t)
	run, _ := a.Begin(false, sheet().Hash(), "T", "alice", "user:alice")
	l.after = func(seq uint64) {
		l.after = nil
		body, _ := json.Marshal(entry{Kind: kindResult, Run: run.ID, Point: "XV101", Outcome: Fail, Note: "someone else, a moment later", By: "carol", Principal: "user:carol"})
		l.seq++
		for _, r := range l.replicas {
			r.ApplyLogged(l.seq, 5_000_000, body)
		}
	}
	res, err := a.Record(run.ID, "XV101", Pass, "", 1, "good", "alice", "user:alice")
	if err != nil {
		t.Fatal(err)
	}
	if res.By != "alice" || res.Outcome != Pass {
		t.Errorf("the writer was told %+v, not its own result", res)
	}
}
