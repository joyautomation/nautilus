package notes

import "github.com/joyautomation/nautilus/oplog"

// Entry is the log entry that proposes r, for a pair.
func Entry(r Revision) oplog.Entry {
	return oplog.NoteEntryOf(r.By, oplog.NoteEntry{
		ID: r.Note, Rev: r.Rev,
		TargetKind: r.Target.Kind, TargetID: r.Target.ID, Occurrence: r.Target.Occurrence, ActiveMs: r.Target.ActiveMs,
		Kind: r.Kind, Text: r.Text, Re: r.Re, Retract: r.Retract, Reason: r.Reason, Override: r.Override,
	})
}

// FromEntry is the revision a committed KindNote entry holds, at the
// leader's time. ok is false for any other kind, or an entry with no body
// (see Missing).
func FromEntry(e oplog.Entry) (Revision, bool) {
	n, ok := e.NoteBody()
	if !ok {
		return Revision{}, false
	}
	return Revision{
		Note: n.ID, Rev: n.Rev, At: e.TS, By: e.By, Principal: e.Principal,
		Target: Target{Kind: n.TargetKind, ID: n.TargetID, Occurrence: n.Occurrence, ActiveMs: n.ActiveMs},
		Kind:   n.Kind, Re: n.Re, Text: n.Text, Retract: n.Retract, Reason: n.Reason, Override: n.Override,
	}, true
}

// Missing reports a KindNote entry that carries no readable note: counted
// and refused loudly, never skipped as if it were another kind.
func Missing(e oplog.Entry) bool {
	if e.Kind != oplog.KindNote {
		return false
	}
	_, ok := e.NoteBody()
	return !ok
}

// Replay builds the index a log's committed entries make, as a replica
// does at start: what `naut notes export` shows of a pair's stopped
// controller. An entry the notes cannot take is skipped and counted: a
// revision that lost a race (two edits of one note proposed together: the
// log committed both, and none applies the second), a gap, or a note entry
// with no readable body.
func Replay(es []oplog.Entry) (*Store, int) {
	s := New()
	bad := 0
	for _, e := range es {
		if Missing(e) {
			bad++
			continue
		}
		if r, ok := FromEntry(e); ok {
			if _, err := s.Apply(r); err != nil {
				bad++
			}
		}
	}
	return s, bad
}
