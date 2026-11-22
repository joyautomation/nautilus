package notes

import "github.com/joyautomation/nautilus/oplog"

// Entry is the log entry that proposes r, for a pair.
func Entry(r Revision) oplog.Entry {
	return oplog.Entry{
		Kind: oplog.KindNote, By: r.By,
		Note: &oplog.NoteEntry{
			ID: r.Note, Rev: r.Rev,
			TargetKind: r.Target.Kind, TargetID: r.Target.ID, Occurrence: r.Target.Occurrence, ActiveMs: r.Target.ActiveMs,
			Kind: r.Kind, Text: r.Text, Re: r.Re, Retract: r.Retract, Reason: r.Reason, Override: r.Override,
		},
	}
}

// FromEntry is the revision a committed KindNote entry holds, at the
// leader's time. ok is false for any other kind, or an entry with no body.
func FromEntry(e oplog.Entry) (Revision, bool) {
	if e.Kind != oplog.KindNote || e.Note == nil {
		return Revision{}, false
	}
	n := e.Note
	return Revision{
		Note: n.ID, Rev: n.Rev, At: e.TS, By: e.By, Principal: e.Principal,
		Target: Target{Kind: n.TargetKind, ID: n.TargetID, Occurrence: n.Occurrence, ActiveMs: n.ActiveMs},
		Kind:   n.Kind, Re: n.Re, Text: n.Text, Retract: n.Retract, Reason: n.Reason, Override: n.Override,
	}, true
}

// Replay builds the index a log's committed entries make, as a replica
// does at start: what `naut notes export` shows of a pair's stopped
// controller. An entry the notes cannot take (a gap) is skipped and counted.
func Replay(es []oplog.Entry) (*Store, int) {
	s := New()
	bad := 0
	for _, e := range es {
		if r, ok := FromEntry(e); ok {
			if _, err := s.Apply(r); err != nil {
				bad++
			}
		}
	}
	return s, bad
}
