package quorum

import (
	"testing"
	"time"
)

// Epoch records and marks (#710 S2c-1, epochs.go).

// A campaign's grant records its epoch in the grant's own write, so the
// record is durable before the grant leaves and survives a restart. A
// renewal, a re-term included, records nothing. grant_keeps_record turns
// it red.
func TestACampaignGrantRecordsItsEpoch(t *testing.T) {
	v := ready("W")
	now := at(lease)
	p, out := v.Handle(now, Request{From: "A", Term: 5, Nonce: 0xA, ID: 1})
	if p == nil || len(out) != 0 {
		t.Fatalf("a campaign's grant left before its write: %v %+v", p, out)
	}
	if p.State.RecordsFrom != 5 || len(p.State.Epochs) != 1 || p.State.Epochs[0] != (EpochRecord{Name: "A", Epoch: 5, Nonce: 0xA}) {
		t.Fatalf("the grant's write: RecordsFrom %d, records %+v; want 5 and A at 5", p.State.RecordsFrom, p.State.Epochs)
	}
	out = v.Persisted(p.Seq, true)
	if len(out) != 1 || !out[0].Granted {
		t.Fatalf("the grant: %+v", out)
	}
	// The reply tells A of its EARLIER boots' epochs only (#710 S2c-3):
	// none here, so its own campaign does not show.
	if r, ok := out[0].Recs.Of("A"); !ok || r.Epoch != 0 || out[0].RecordsFrom != 5 {
		t.Fatalf("the reply's records: %+v from %d", out[0].Recs, out[0].RecordsFrom)
	}
	// Renewals, one at a higher vote term (a re-term), start no epoch.
	if !grant(t, v, now.Add(100*time.Millisecond), Request{From: "A", Term: 5, Nonce: 0xA, ID: 2, Renewal: true, Epoch: 5}) ||
		!grant(t, v, now.Add(200*time.Millisecond), Request{From: "A", Term: 6, Nonce: 0xA, ID: 3, Renewal: true, Epoch: 5}) {
		t.Fatal("a renewal refused")
	}
	if e := v.EpochOf("A"); e != 5 {
		t.Fatalf("A's record after a re-term: %d, want 5", e)
	}
	// B's campaign once A's promise has run out.
	later := now.Add(200*time.Millisecond + lease)
	if !grant(t, v, later, Request{From: "B", Term: 7, Nonce: 0xB, ID: 1}) {
		t.Fatal("B's campaign refused")
	}
	r := restart(t, v, later.Add(time.Second), 2)
	if r.EpochOf("A") != 5 || r.EpochOf("B") != 7 || r.recordsFrom() != 5 {
		t.Fatalf("after a restart: A %d, B %d, from %d; want 5, 7, 5", r.EpochOf("A"), r.EpochOf("B"), r.recordsFrom())
	}
}

// RecordsFrom: a fresh state (a new install, a lost or rebuilt file)
// knows nothing until its first campaign grant; a file with grants but no
// records knows what came after its Promised, and stays in its old format
// until a campaign's grant writes records.
func TestRecordsCoverOnlyWhatTheStateKnows(t *testing.T) {
	if f := ready("A").recordsFrom(); f != 0 {
		t.Fatalf("an uncommissioned voter's records from %d, want 0 (none)", f)
	}
	w, _ := NewWitness("W", lease, VoterState{}, 0, func() uint64 { return 7 })
	if f := w.recordsFrom(); f != 0 {
		t.Fatalf("a fresh witness's records from %d, want 0 (a rebuilt one knows nothing)", f)
	}
	// A commissioned controller's voter: any leadership an earlier life
	// granted is at or below the floor its adoption set.
	if f := NewVoter("A", lease, VoterState{Incarnation: 9, Gen: 1, Floor: 1000}, 0).recordsFrom(); f != 1001 {
		t.Fatalf("a commissioned voter's records from %d, want 1001 (above its floor)", f)
	}
	v := NewVoter("W", lease, VoterState{Promised: 12, GrantedTo: "A", Incarnation: 9}, 0)
	if f := v.recordsFrom(); f != 13 {
		t.Fatalf("an old file's records from %d, want 13 (above its Promised)", f)
	}
	now := at(lease)
	// A's lease runs on through a re-term: a write, still the old format.
	p, _ := v.Handle(now, Request{From: "A", Term: 14, Nonce: 0xA, ID: 1, Renewal: true, Epoch: 12})
	if p == nil {
		t.Fatal("a re-term wrote nothing")
	}
	if b, _ := p.State.MarshalBinary(); b[len(stateMagic)] != stateVersionPlain {
		t.Fatalf("a renewal's write is format %d, want %d (no records yet)", b[len(stateMagic)], stateVersionPlain)
	}
	v.Persisted(p.Seq, true)
	if f := v.recordsFrom(); f != 15 {
		t.Fatalf("after the re-term, records from %d, want 15", f)
	}
	later := now.Add(lease)
	p, _ = v.Handle(later, Request{From: "B", Term: 16, Nonce: 0xB, ID: 1})
	if p == nil || p.State.RecordsFrom != 15 {
		t.Fatalf("the first campaign grant's write: %+v, want records from 15", p)
	}
	if b, _ := p.State.MarshalBinary(); b[len(stateMagic)] != stateVersion {
		t.Fatalf("a file with records is format %d, want %d", b[len(stateMagic)], stateVersion)
	}
}

// A mark comes only from a renewal the voter granted, once the grant
// exists (its write durable), and holds the leader's epoch, not the
// renewal's vote term. A restarted voter knows none. mark_from_refused_renewal,
// reply_mark_after_grant and mark_term_compared turn it red.
func TestAMarkComesOnlyFromAGrantedRenewal(t *testing.T) {
	v := ready("W")
	now := at(lease)
	if !grant(t, v, now, Request{From: "A", Term: 5, Nonce: 0xA, ID: 1}) {
		t.Fatal("A's campaign refused")
	}
	if m := v.Marks("A"); m.Known {
		t.Fatalf("a campaign set a mark: %+v", m)
	}
	if !grant(t, v, now.Add(time.Millisecond), Request{From: "A", Term: 5, Nonce: 0xA, ID: 2, Renewal: true, Epoch: 5, MarkSeq: 10, MarkCurrent: true}) {
		t.Fatal("A's renewal refused")
	}
	if m := v.Marks("A"); m != (Mark{Known: true, Current: true, Term: 5, Scan: 10}) {
		t.Fatalf("A's mark: %+v", m)
	}
	// B's renewal, refused under A's promise, sets nothing.
	if grant(t, v, now.Add(2*time.Millisecond), Request{From: "B", Term: 5, Nonce: 0xB, ID: 1, Renewal: true, Epoch: 3, MarkSeq: 99, MarkCurrent: true}) {
		t.Fatal("B's renewal granted under A's promise")
	}
	if m := v.Marks("B"); m.Known {
		t.Fatalf("a refused renewal set a mark: %+v", m)
	}
	// A re-term: the mark keeps the epoch, and waits for the write.
	p, out := v.Handle(now.Add(3*time.Millisecond), Request{From: "A", Term: 6, Nonce: 0xA, ID: 3, Renewal: true, Epoch: 5, MarkSeq: 11})
	if p == nil || len(out) != 0 {
		t.Fatalf("a re-term answered before its write: %v %+v", p, out)
	}
	if m := v.Marks("A"); m.Scan != 10 {
		t.Fatalf("a held grant's mark taken before its write: %+v", m)
	}
	v.Persisted(p.Seq, false) // the write failed: no grant, no mark
	if m := v.Marks("A"); m.Scan != 10 {
		t.Fatalf("a failed write's mark taken: %+v", m)
	}
	p, _ = v.Handle(now.Add(4*time.Millisecond), Request{From: "A", Term: 6, Nonce: 0xA, ID: 4, Renewal: true, Epoch: 5, MarkSeq: 12})
	if out := v.Persisted(p.Seq, true); len(out) != 1 || !out[0].Granted {
		t.Fatalf("the re-term's grant: %+v", out)
	}
	if m := v.Marks("A"); m != (Mark{Known: true, Term: 5, Scan: 12}) {
		t.Fatalf("after a re-term to 6 in epoch 5: %+v, want epoch 5, seq 12, not current", m)
	}
	// A reply to B carries A's record and mark: what B's takeover reads.
	later := now.Add(5*time.Millisecond + lease)
	p, _ = v.Handle(later, Request{From: "B", Term: 7, Nonce: 0xB, ID: 2})
	out = v.Persisted(p.Seq, true)
	if r, ok := out[0].Recs.Of("A"); !ok || r.Epoch != 5 || r.Mark.Scan != 12 {
		t.Fatalf("B's grant carries %+v; want A at epoch 5 with its mark", out[0].Recs)
	}
	if m := restart(t, v, later.Add(time.Second), 2).Marks("A"); m.Known {
		t.Fatalf("a restarted voter knows a mark: %+v", m)
	}
}

// A renewal delayed past a later one from the same boot never brings back
// its older word: the standby fell behind, and a late "current" must not
// undo it (Fable, #1345 S1). mark_from_older_renewal turns it red.
func TestAnOlderRenewalNeverReplacesANewerMark(t *testing.T) {
	v := ready("W")
	now := at(lease)
	if !grant(t, v, now, Request{From: "A", Term: 5, Nonce: 0xA, ID: 1, Seq: 1}) {
		t.Fatal("A's campaign refused")
	}
	newer := Request{From: "A", Term: 5, Nonce: 0xA, ID: 3, Seq: 3, Renewal: true, Epoch: 5, MarkSeq: 40}
	older := Request{From: "A", Term: 5, Nonce: 0xA, ID: 2, Seq: 2, Renewal: true, Epoch: 5, MarkSeq: 40, MarkCurrent: true}
	if !grant(t, v, now.Add(time.Millisecond), newer) || !grant(t, v, now.Add(2*time.Millisecond), older) {
		t.Fatal("a renewal refused")
	}
	if m := v.Marks("A"); m.Current {
		t.Fatalf("the delayed older renewal's mark replaced the newer one: %+v", m)
	}
	// Another boot of A starts its own sequence.
	again := Request{From: "A", Term: 5, Nonce: 0xA2, ID: 1, Seq: 1, Renewal: true, Epoch: 5, MarkSeq: 41, MarkCurrent: true}
	v.setMark(again)
	if m := v.Marks("A"); !m.Current || m.Scan != 41 {
		t.Fatalf("a new boot's first mark not taken: %+v", m)
	}
}
