package oplog

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// Review repro 2: the catch-up refuses "not what was accepted: naut
// redundancy vouch again", but a second vouch does nothing: the copy is
// already synced with Alone pending, so Vouch returns before it judges
// the picture, checks the digest, or replaces Mark.Accept. Setup as the
// PR's own subtest "a later log accepted, now silent": the copy accepted
// as holding a later log dies before the catch-up.
// #1177 review (Fable): "naut redundancy vouch again" after a catch-up
// refused as not what was accepted judges the picture anew (a pending
// one-shot is no reason for a no-op), and a digest of no picture is
// refused on every path. vouch_again_noop and noop_digest_unchecked turn
// it red.
func TestVouchAgainAfterARefusedCatchUpAcceptsTheNewPicture(t *testing.T) {
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
	before := *tr.mark(0).Accept
	tr.setDown(1, true) // B dies
	tr.lead(0)
	err := tr.nodes[0].SyncLeader(tr.ctx())
	if !errors.Is(err, ErrReadOnly) || !strings.Contains(err.Error(), "vouch again") {
		t.Fatalf("setup: %v", err)
	}
	// The person does as told.
	v, pic, err := tr.nodes[0].Vouch(tr.ctx(), "op", nil, VouchAccept{})
	t.Logf("vouch again (no flag): vouch=%+v err=%v needs=%v why=%v digest=%s", v, err, pic.Needs, pic.Why, pic.Digest)
	// A digest that names no picture is not checked on this path either.
	_, _, err2 := tr.nodes[0].Vouch(tr.ctx(), "op", nil, VouchAccept{Loss: "0000000000"})
	_, _, err3 := tr.nodes[0].Vouch(tr.ctx(), "op", nil, VouchAccept{Loss: pic.Digest})
	after := tr.mark(0).Accept
	t.Logf("--accept-loss=0000000000: err=%v; --accept-loss=%s: err=%v; Accept before %+v, after %+v", err2, pic.Digest, err3, before, after)
	if err2 == nil {
		t.Errorf("a vouch with a digest of no picture was answered without a refusal")
	}
	if err == nil && !slices_contains(pic.Needs, NeedLoss) {
		t.Errorf("vouch again neither refused nor named what to accept (B does not answer)")
	}
	if err := tr.nodes[0].SyncLeader(tr.ctx()); err != nil {
		t.Fatalf("DEAD END: after vouching again with the digest printed, the catch-up still refuses: %v", err)
	}
}

// Review repro 3: a fork chosen on the side that already caught up for
// its term (the witness is on its side): the vouch is recorded, but the
// catch-up that would override the fork never runs (syncedTerm == term).
// #1177 review (Fable): a fork chosen on the leader that already caught
// up for its term is resolved at once (the vouch makes it catch up
// again), not at its next term. vouch_keeps_term_synced turns it red.
func TestAForkChosenOnTheSyncedLeaderIsResolved(t *testing.T) {
	tr := newTrio(t, witnessInPlace)
	mustSync(t, tr, 0)
	if _, err := tr.propose(0, "X"); err != nil {
		t.Fatal(err)
	}
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
		t.Fatal(err)
	}
	tr.setDown(0, false) // A is back, forked; B leads, synced for its term, with the witness
	tr.nodes[1].Heartbeat(tr.ctx())
	if st := tr.nodes[1].Status(); len(st.Forked) != 1 {
		t.Fatalf("setup: B's status %+v", st)
	}
	// A person chooses B, on B.
	pic := acceptLoss(t, tr, 1)
	if !pic.Copies[0].Forked {
		t.Fatalf("picture %+v", pic)
	}
	mustSync(t, tr, 1)
	tr.nodes[1].Heartbeat(tr.ctx())
	if _, err := tr.propose(1, "B2"); err != nil {
		t.Fatal(err)
	}
	tr.nodes[1].Heartbeat(tr.ctx())
	m := tr.mark(1)
	st := tr.nodes[1].Status()
	a := tr.nodes[0].Status()
	t.Logf("B's mark after the vouch, a sync, a write and two heartbeats: history %d alone=%v accept=%v; B forked=%+v; A supersedeTo=%d forked=%+v", m.History, m.Alone, m.Accept != nil, st.Forked, a.SupersedeTo, a.Forked)
	if m.History != 2 || a.SupersedeTo != 2 {
		t.Fatalf("the accepted fork was not resolved: B at history %d (want 2), A supersedeTo %d (want 2), vouch still pending (alone=%v)", m.History, a.SupersedeTo, m.Alone)
	}
}

// #1177 review (Fable): a vouch with nothing to accept (synced, another
// copy counts) still refuses a digest that is not this picture's, so the
// journal never records an acceptance of a picture nobody saw; this
// picture's own digest is answered as the no-op it is.
// noop_digest_unchecked turns it red.
func TestANoOpVouchRefusesADigestOfAnotherPicture(t *testing.T) {
	tr := newTrio(t, nil)
	mustSync(t, tr, 0)
	if _, err := tr.propose(0, "X"); err != nil {
		t.Fatal(err)
	}
	_, pic, err := tr.nodes[1].Vouch(tr.ctx(), "op", nil, VouchAccept{})
	if err != nil || len(pic.Needs) != 0 {
		t.Fatalf("setup: %v %+v", err, pic)
	}
	if _, _, err := tr.nodes[1].Vouch(tr.ctx(), "op", nil, VouchAccept{Loss: "0000000000"}); !errors.Is(err, ErrVouchRefused) {
		t.Fatalf("a digest of no picture on a no-op vouch: %v", err)
	}
	if _, _, err := tr.nodes[1].Vouch(tr.ctx(), "op", nil, VouchAccept{LostLog: "0000000000"}); !errors.Is(err, ErrVouchRefused) {
		t.Fatalf("a lost-log digest of no picture on a no-op vouch: %v", err)
	}
	if v, _, err := tr.nodes[1].Vouch(tr.ctx(), "op", nil, VouchAccept{Loss: pic.Digest}); err != nil || v != (Vouch{}) {
		t.Fatalf("this picture's digest on a no-op vouch: %+v %v", v, err)
	}
}
