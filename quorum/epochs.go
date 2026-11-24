package quorum

import "slices"

// Epoch records and marks (#710 S2c-1): what a voter tells a takeover
// about the leaderships it granted, so a hot takeover can tell that no
// leadership came after the image it would go on from (naut-lead's ruling
// B on S2b).
//
//   - The epoch record: per name, the highest epoch the voter granted it
//     in a campaign, written with the grant itself (the grant leaves only
//     once that write is durable), so it costs no extra fsync and outlives
//     a restart. Only a campaign's grant: a renewal (a re-term included)
//     never starts an epoch.
//   - RecordsFrom: the records cover every grant from this term on. A
//     state with grants but no records yet (a file from before the
//     records, or never granted a campaign since) covers what comes after
//     its Promised: that file's life granted nothing above it. A fresh
//     state (never granted: a new install, a lost or rebuilt file) covers
//     nothing until its first campaign grant, since an earlier life of the
//     voter may have granted anything. It is written only with the first
//     campaign grant, so a file stays in its old format until then.
//   - The mark: per name, the in-sync mark the name's last GRANTED renewal
//     carried (Request.Epoch, MarkSeq, MarkCurrent). In memory only, one
//     fsync per renewal being too dear: a restarted voter reports none
//     until the next renewal it grants.

// maxEpochRecords bounds the names a voter keeps records for: a pair has
// two, a spare's replacement keeps its name.
const maxEpochRecords = 4

// recordsFrom is the term from which this voter's records cover every
// grant, 0 for none (see above). A commissioned controller's voter covers
// what came after its Floor too: an adoption raises the floor above every
// term either controller (and, for a lost file, the witness) knew was
// promised, so any leadership an earlier life of this voter granted is at
// or below it. The witness has no floor: a fresh or rebuilt one knows
// nothing until its first campaign grant.
func (v *Voter) recordsFrom() uint64 {
	if v.st.RecordsFrom != 0 {
		return v.st.RecordsFrom
	}
	base := v.st.Promised
	if !v.isWitness && v.st.Incarnation != 0 && !mut("records_from_floor_unset") {
		base = max(base, v.st.Floor)
		return base + 1
	}
	if base != 0 {
		return base + 1
	}
	return 0
}

// Covers reports whether records that start at from cover every grant
// above epoch e: what a hot takeover needs of a voter (HotCheck.Verdict).
func Covers(from, e uint64) bool { return from != 0 && from <= e+1 }

// recordGrant records a campaign's grant of term to name, a boot of it
// with nonce, in the state the grant writes. It reports whether the state
// changed. A grant to another boot of the name than the record's moves
// the record's epoch into Prior first: Prior is then the highest epoch
// this voter granted any EARLIER boot, which a reply to the boot asking
// reports as its own record (recs), so a campaign's own grant never hides
// the asker's leadership in a boot before (#710 S2c-3).
func (v *Voter) recordGrant(name NodeID, term, nonce uint64) bool {
	if mut("grant_keeps_record") {
		return false
	}
	changed := false
	if v.st.RecordsFrom == 0 {
		// Before the state takes term: a file with grants covers from
		// above its Promised, a fresh one from this grant.
		v.st.RecordsFrom = v.recordsFrom()
		if v.st.RecordsFrom == 0 {
			v.st.RecordsFrom = term
		}
		changed = true
	}
	i := slices.IndexFunc(v.st.Epochs, func(r EpochRecord) bool { return r.Name == name })
	if i >= 0 {
		r := v.st.Epochs[i]
		if r.Nonce != nonce && !mut("prior_boot_overwritten") {
			r.Prior, r.Nonce = max(r.Prior, r.Epoch), nonce
		}
		r.Epoch = max(r.Epoch, term)
		if r == v.st.Epochs[i] {
			return changed
		}
		v.st.Epochs = slices.Clone(v.st.Epochs)
		v.st.Epochs[i] = r
		return true
	}
	es := append(slices.Clone(v.st.Epochs), EpochRecord{Name: name, Epoch: term, Nonce: nonce})
	if len(es) > maxEpochRecords {
		// A name too many: forget the lowest record, and with it the claim
		// to know every grant up to it.
		low := 0
		for i, r := range es {
			if r.Epoch < es[low].Epoch {
				low = i
			}
		}
		v.st.RecordsFrom = max(v.st.RecordsFrom, es[low].Epoch+1)
		es = slices.Delete(es, low, low+1)
	}
	v.st.Epochs = es
	return true
}

// markOf is the mark a renewal carries, as a voter that grants it keeps
// it: the leader's epoch, never the vote term the renewal asks.
func markOf(r Request) Mark {
	m := Mark{Known: true, Current: r.MarkCurrent, Term: r.Epoch, Scan: r.MarkSeq}
	if mut("mark_term_compared") {
		m.Term = r.Term
	}
	return m
}

// setMark keeps r's mark as its sender's, when r is a renewal that carries
// one, unless it is older than the one kept: a renewal delayed past a
// later one from the same boot (Request.Seq) never brings back an older
// word (Fable, #1345 S1).
func (v *Voter) setMark(r Request) {
	if !r.Renewal || r.Epoch == 0 {
		return
	}
	if v.syncMarks == nil {
		v.syncMarks, v.syncMarkFrom = map[NodeID]Mark{}, map[NodeID][2]uint64{}
	}
	if from, ok := v.syncMarkFrom[r.From]; ok && from[0] == r.Nonce && r.Seq < from[1] && !mut("mark_from_older_renewal") {
		return
	}
	v.syncMarks[r.From] = markOf(r)
	v.syncMarkFrom[r.From] = [2]uint64{r.Nonce, r.Seq}
}

// Marks is the mark of name's last renewal this voter granted (unknown
// when none since this voter started).
func (v *Voter) Marks(name NodeID) Mark { return v.syncMarks[name] }

// EpochOf is the highest epoch this voter granted name in a campaign, 0
// for none recorded.
func (v *Voter) EpochOf(name NodeID) uint64 {
	for _, r := range v.st.Epochs {
		if r.Name == name {
			return r.Epoch
		}
	}
	return 0
}

// epochBefore is the highest epoch this voter granted name in a campaign
// of a boot other than nonce's: the asker's own record in a reply (recs).
// With mut prior_boot_overwritten it is every boot's, as before #710
// S2c-3, the asker's own campaign included.
func (v *Voter) epochBefore(name NodeID, nonce uint64) uint64 {
	for _, r := range v.st.Epochs {
		if r.Name != name {
			continue
		}
		if r.Nonce == nonce && nonce != 0 && !mut("own_record_after_grant") {
			return r.Prior
		}
		return r.Epoch
	}
	return 0
}

// recs are the records a reply to asker (the boot with nonce) carries:
// asker's own, of its EARLIER boots only (epochBefore), and the other name
// with the highest record or a mark (a pair's peer), of every boot.
func (v *Voter) recs(asker NodeID, nonce uint64) Recs {
	var rs Recs
	rec := func(name NodeID) Rec { return Rec{Name: name, Epoch: v.EpochOf(name), Mark: v.syncMarks[name]} }
	if asker != "" {
		rs[0] = rec(asker)
		rs[0].Epoch = v.epochBefore(asker, nonce)
	}
	// In name order, so a tie goes the same way every time (the
	// simulator replays runs).
	var names []NodeID
	for _, r := range v.st.Epochs {
		names = append(names, r.Name)
	}
	for name := range v.syncMarks {
		names = append(names, name)
	}
	slices.Sort(names)
	var other NodeID
	var best uint64
	for _, name := range names {
		if e := max(v.syncMarks[name].Term, v.EpochOf(name)); name != asker && (other == "" || e > best) {
			other, best = name, e
		}
	}
	if other != "" {
		rs[1] = rec(other)
	}
	return rs
}
