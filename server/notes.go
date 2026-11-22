package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/joyautomation/nautilus/auth"
	"github.com/joyautomation/nautilus/notes"
	"github.com/joyautomation/nautilus/oplog"
)

// Operator notes and the shift log (#720):
//
//	POST /api/notes              {target:{kind,id,occurrence?,activeMs?}, text, kind?, re?}: a note
//	POST /api/notes/{id}/revise  {text}: its author, within an hour
//	POST /api/notes/{id}/retract {reason}: its author within the hour, or a holder of audit
//	GET  /api/notes              ?target=&id=&occurrence=&kind=&author=&since=&until=&limit=&retracted=1
//	GET  /api/notes/{id}         the note with every revision
//
// A note is never overwritten or deleted: an edit is a new revision, a
// retraction one more, and the text of the earlier ones stays (the
// notes package). The author is the session's principal, never a field of
// the body. A person (user or OIDC) writes, revises and retracts; a
// service token may only add. Past the hour, a correction is an addendum
// (re: the note's id) by anyone who may write notes there.
//
// Alone, a controller keeps the notes in a file (Options.Notes). On a pair
// the replicated log is the store (Options.NotesLog): a write is proposed
// as an acknowledgement is and answered when a majority holds it; a copy
// that cannot count a majority refuses it with the same reason as an ack,
// and an unconfirmed one says "outcome unknown", with the note's id, so
// the page keeps what the person wrote.

// NotesLog adapts a pair's oplog node for the notes: it applies the note
// entries it commits to the index, and carries the proposals.
type NotesLog struct {
	store *notes.Store
	node  *oplog.Node
	seen  atomic.Pointer[func(oplog.Entry)]

	supMu sync.Mutex
	sup   *supersededCache
	// supersededAt, when set, says where the replaced history is (a test's).
	supersededAt func() (file, by string)
}

// NewNotesLog is the pair's notes: an index the log fills.
func NewNotesLog() *NotesLog { return &NotesLog{store: notes.New()} }

// Store is the index the log fills.
func (l *NotesLog) Store() *notes.Store { return l.store }

// Attach sets the oplog node proposals go to.
func (l *NotesLog) Attach(n *oplog.Node) { l.node = n }

func (l *NotesLog) observe(seen func(oplog.Entry)) { l.seen.Store(&seen) }

func (l *NotesLog) tell(e oplog.Entry) {
	if f := l.seen.Load(); f != nil && l.node != nil && e.Origin == l.node.Self() {
		(*f)(e)
	}
}

// Apply takes a committed entry if it is a note, and reports whether it was
// (an Applier composed of several hands the others on). A build from before
// notes falls through on the kind and keeps the entry whole in its log (the
// body travels in Value, which it preserves), so it indexes the notes when
// it is upgraded and replays.
func (l *NotesLog) Apply(e oplog.Entry) (bool, error) {
	if e.Kind != oplog.KindNote {
		return false, nil
	}
	defer l.tell(e)
	r, ok := notes.FromEntry(e)
	if !ok {
		// a note entry with no readable note is loud, not skipped
		return true, fmt.Errorf("note entry %d carries no readable note (written by a build that dropped its body?)", e.Seq)
	}
	_, err := l.store.Apply(r)
	return true, err
}

// SupersededNotes are the notes a copy's replaced log history held: a
// record, shown read-only and never applied to the live index.
type SupersededNotes struct {
	Store   *notes.Store
	By      string    // who accepted the loss (naut redundancy vouch)
	MovedAt time.Time // when the old log was moved aside
}

type supersededCache struct {
	file string
	mod  time.Time
	size int64
	val  SupersededNotes
}

// Superseded reads the newest replaced history of this copy's log, if one
// was kept: its committed note entries, replayed into a store of their own.
// ok is false when there is none. It is read on demand and cached by the
// file's size and time.
func (l *NotesLog) Superseded() (SupersededNotes, bool) {
	var file, by string
	switch {
	case l.supersededAt != nil: // a test's
		file, by = l.supersededAt()
	case l.node != nil:
		file, by = l.node.SupersededLog()
	}
	if file == "" {
		return SupersededNotes{}, false
	}
	fi, err := os.Stat(file)
	if err != nil {
		return SupersededNotes{}, false
	}
	l.supMu.Lock()
	defer l.supMu.Unlock()
	if c := l.sup; c != nil && c.file == file && c.mod.Equal(fi.ModTime()) && c.size == fi.Size() && c.val.By == by {
		return c.val, true
	}
	es, err := oplog.ReadCommitted(file)
	if err != nil {
		return SupersededNotes{}, false
	}
	st, _ := notes.Replay(es)
	moved := time.Time{}
	if i := strings.LastIndex(file, ".superseded-"); i >= 0 {
		if u, err := strconv.ParseInt(file[i+len(".superseded-"):], 10, 64); err == nil {
			moved = time.Unix(u, 0).UTC()
		}
	}
	v := SupersededNotes{Store: st, By: by, MovedAt: moved}
	l.sup = &supersededCache{file: file, mod: fi.ModTime(), size: fi.Size(), val: v}
	return v, true
}

// EndSuperseded empties the index when this copy's log history is replaced
// (a person accepted its loss, naut redundancy vouch): its notes are then
// exactly the new history's.
func (l *NotesLog) EndSuperseded() { l.store.Reset() }

// PairNoMajorityNotes is a pair's no-majority refusal for note writes.
const PairNoMajorityNotes = "operator-intent log has no majority: neither the other controller nor the witness answers; notes cannot be written until one of them returns"

type noteTarget struct {
	Kind       string `json:"kind"`
	ID         string `json:"id,omitempty"`
	Occurrence string `json:"occurrence,omitempty"`
	ActiveMs   int64  `json:"activeMs,omitempty"`
}

type noteRequest struct {
	Target noteTarget `json:"target"`
	Text   string     `json:"text"`
	Kind   string     `json:"kind"`
	Re     string     `json:"re"`
	Reason string     `json:"reason"`
}

// noteView is a note as the API says it: the latest text, who first wrote
// it, and the history's length (GET /api/notes/{id} has the history).
type noteView struct {
	ID         string     `json:"id"`
	Target     noteTarget `json:"target"`
	Kind       string     `json:"kind"`
	Re         string     `json:"re,omitempty"`
	Text       string     `json:"text"`
	By         string     `json:"by"`
	Principal  string     `json:"principal,omitempty"`
	At         int64      `json:"at"`
	Revisions  int        `json:"revisions"`
	Edited     bool       `json:"edited,omitempty"`
	EditedAt   int64      `json:"editedAt,omitempty"`
	EditedBy   string     `json:"editedBy,omitempty"`
	Retracted  bool       `json:"retracted,omitempty"`
	Reason     string     `json:"reason,omitempty"`
	EditableTo int64      `json:"editableUntil,omitempty"` // the author's edit window ends (epoch ms)
}

func viewOf(n notes.Note) noteView {
	f, l := n.First(), n.Latest()
	v := noteView{
		ID: n.ID, Target: noteTarget(f.Target), Kind: f.Kind, Re: f.Re, Text: l.Text, By: f.By, Principal: f.Principal,
		At: f.At, Revisions: len(n.Revisions), Edited: n.Edited(), Retracted: n.Retracted(),
		EditableTo: f.At + notes.EditWindowMs,
	}
	if v.Kind == "" {
		v.Kind = notes.NoteNote
	}
	if n.Edited() {
		v.EditedAt, v.EditedBy = l.At, l.By
	}
	if n.Retracted() {
		v.Reason = l.Reason
	}
	return v
}

type notesAnswer struct {
	Notes []noteView `json:"notes"`
}

// noteClockSkewMs is how far ahead of this controller's clock a note's
// stamp may be and still count as just written.
const noteClockSkewMs = 5 * 60 * 1000

// notesStore is the store this server reads and writes: the pair's index,
// else the file's; nil when notes are not on.
func (s *Server) notesStore() *notes.Store {
	if s.notesLog != nil {
		return s.notesLog.store
	}
	return s.notes
}

func (s *Server) notesOff(w http.ResponseWriter) bool {
	if s.notesStore() != nil {
		return false
	}
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "this controller keeps no notes: notes belong to people, so it needs an auth: block with users (naut users add) and a state directory to keep them in (auth.state-dir, or systemd StateDirectory=)"})
	return true
}

func (s *Server) notesNow() time.Time {
	if s.noteClock != nil {
		return s.noteClock()
	}
	return time.Now()
}

// noteAuthor is the name a note carries for the principal: the user's own,
// else what the journal calls it ("token:ci").
func noteAuthor(principal string) string {
	if n, ok := strings.CutPrefix(principal, "user:"); ok {
		return n
	}
	return principal
}

// notePrincipal checks who is writing: a person may do everything, a token
// may add (add true), anyone else may do nothing. msg is the refusal.
func (s *Server) notePrincipal(r *http.Request, add bool) (auth.Principal, int, string) {
	a := s.actorOf(r)
	switch a.pr.Kind {
	case auth.KindUser, auth.KindOIDC:
		return a.pr, 0, ""
	case auth.KindToken, auth.KindService:
		if add {
			return a.pr, 0, ""
		}
		return a.pr, http.StatusForbidden, "a token may add a note but not change one: only a person's own notes can be revised or retracted, by that person"
	}
	return a.pr, http.StatusUnauthorized, "sign in to write a note" + signInHint
}

// noteScope authorizes writing about target: a tag or an alarm's tag
// within the principal's areas; any other target (an asset, an area, the
// shift log) anywhere the permission is held.
func (s *Server) noteScope(r *http.Request, t notes.Target) (int, string) {
	scope := ""
	switch t.Kind {
	case notes.KindTag:
		scope = t.ID
	case notes.KindAlarm:
		scope = auth.All // an alarm with no tag, or one since removed, is whole-controller (as acknowledging it is)
		if s.alarms != nil {
			if rec, ok := s.alarms.Lookup(t.ID); ok && rec.Tag != "" {
				scope = rec.Tag
			}
		}
	}
	if scope == "" {
		if s.policy == nil || s.policy.Holds(s.actorOf(r).pr, PermNote) {
			return s.authorizeWrite(r)
		}
		return s.authorize(r, PermNote, auth.All) // says why it is refused
	}
	return s.authorize(r, PermNote, scope)
}

// noteTargetExists checks what a manifest can: a tag, an alarm. An asset
// is an equipment id of a mimic or a scene (the controller does not read
// those at run time) and an area a name in the auth: block, so neither is
// checked.
func (s *Server) noteTargetExists(t notes.Target) string {
	switch t.Kind {
	case notes.KindTag:
		base := t.ID
		if i := strings.IndexAny(base, ".["); i >= 0 {
			base = base[:i]
		}
		if s.rt.Tags().TypeOf(base) == nil {
			return "no tag named " + t.ID + " (a note about equipment that is not a tag has the asset target)"
		}
	case notes.KindAlarm:
		if s.alarms == nil {
			return "this controller has no alarms"
		}
		if _, ok := s.alarms.Lookup(t.ID); !ok {
			return "no alarm named " + t.ID
		}
	}
	return ""
}

func (s *Server) handleNoteAdd(w http.ResponseWriter, r *http.Request) {
	if s.notesOff(w) {
		return
	}
	if code, msg := s.authorizeWrite(r); code != 0 {
		writeJSON(w, code, map[string]string{"error": msg})
		return
	}
	pr, code, msg := s.notePrincipal(r, true)
	if code != 0 {
		writeJSON(w, code, map[string]string{"error": msg})
		return
	}
	var req noteRequest
	if err := decodeNoteBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	t := notes.Target(req.Target)
	var orig notes.Note // the note an addendum answers, read once
	if req.Re != "" {
		// an addendum is about what the note it answers is about, unless it says otherwise
		var ok bool
		orig, ok = s.notesStore().Get(req.Re)
		if !ok {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "re: there is no note " + req.Re})
			return
		}
		if req.Target == (noteTarget{}) {
			t = orig.First().Target
		}
	}
	if err := notes.CheckTarget(t); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := notes.CheckText(req.Text); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	switch req.Kind {
	case "", notes.NoteNote, notes.NoteHandover:
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "kind is note or handover"})
		return
	}
	if msg := s.noteTargetExists(t); msg != "" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": msg})
		return
	}
	if code, msg := s.noteScope(r, t); code != 0 {
		writeJSON(w, code, map[string]string{"error": msg})
		return
	}
	if req.Re != "" {
		// an addendum is about its note too: writing on the note's target is
		// the permission, naming another target does not get around it
		if code, msg := s.noteScope(r, orig.First().Target); code != 0 {
			writeJSON(w, code, map[string]string{"error": msg})
			return
		}
	}
	rev := notes.Revision{
		Note: notes.NewID(), Rev: 1, By: noteAuthor(pr.String()), Principal: pr.String(),
		Target: t, Kind: req.Kind, Re: req.Re, Text: req.Text,
	}
	if rev.Kind == notes.NoteNote {
		rev.Kind = ""
	}
	s.writeNote(w, r, rev, http.StatusCreated)
}

func (s *Server) handleNoteRevise(w http.ResponseWriter, r *http.Request) {
	s.reviseOrRetract(w, r, false)
}

func (s *Server) handleNoteRetract(w http.ResponseWriter, r *http.Request) {
	s.reviseOrRetract(w, r, true)
}

func (s *Server) reviseOrRetract(w http.ResponseWriter, r *http.Request, retract bool) {
	if s.notesOff(w) {
		return
	}
	if code, msg := s.authorizeWrite(r); code != 0 {
		writeJSON(w, code, map[string]string{"error": msg})
		return
	}
	pr, code, msg := s.notePrincipal(r, false)
	if code != 0 {
		writeJSON(w, code, map[string]string{"error": msg})
		return
	}
	var req noteRequest
	if err := decodeNoteBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	n, ok := s.notesStore().Get(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such note"})
		return
	}
	f := n.First()
	if code, msg := s.noteScope(r, f.Target); code != 0 {
		writeJSON(w, code, map[string]string{"error": msg})
		return
	}
	if n.Retracted() {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "this note was retracted: its history stays, and it cannot be changed again (write a new note)"})
		return
	}
	author := f.Principal == pr.String()
	// An hour from the first revision, by the clock that stamped it: a stamp
	// ahead of this controller's clock by more than a little (a bad clock,
	// a failover between controllers whose clocks differ) is not inside it,
	// so a window never stretches to hours.
	age := s.notesNow().UnixMilli() - f.At
	within := age <= notes.EditWindowMs && age >= -noteClockSkewMs
	override := false
	switch {
	case author && within:
	case retract && s.policy != nil && s.policy.Holds(pr, auth.Audit):
		override = !author // a supervisor's retraction at any time
	case author:
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "the hour you have to change your own note is over: add an addendum (a new note with re: " + n.ID + ")"})
		return
	default:
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "only the author changes a note, within an hour of writing it" + func() string {
			if retract {
				return " (a supervisor may retract one at any time)"
			}
			return ""
		}() + ": to correct this one, add an addendum (a new note with re: " + n.ID + ")"})
		return
	}
	rev := notes.Revision{
		Note: n.ID, Rev: len(n.Revisions) + 1, By: noteAuthor(pr.String()), Principal: pr.String(),
		Target: f.Target, Kind: f.Kind, Re: f.Re,
	}
	if retract {
		if err := notes.CheckReason(req.Reason); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		rev.Retract, rev.Reason, rev.Override = true, req.Reason, override
		rev.Text = n.Latest().Text // the text stays as it was
	} else {
		if err := notes.CheckText(req.Text); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		rev.Text = req.Text
	}
	s.writeNote(w, r, rev, http.StatusOK)
}

// writeNote keeps one revision: through the pair's log when there is one,
// else to the file, and answers with the note.
func (s *Server) writeNote(w http.ResponseWriter, r *http.Request, rev notes.Revision, okCode int) {
	if s.notesLog == nil {
		rev.At = s.notesNow().UnixMilli()
		if _, err := s.notes.Apply(rev); err != nil {
			if errors.Is(err, notes.ErrRevOrder) {
				writeJSON(w, http.StatusConflict, map[string]string{"error": "the note changed since you read it: reload it and try again"})
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "the note could not be saved: " + err.Error()})
			return
		}
		s.noteAnswer(w, rev.Note, okCode)
		return
	}
	if s.refuseNoteWithoutMajority(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), substituteProposeWait)
	defer cancel()
	if _, err := s.proposeFor(ctx, r, notes.Entry(rev), true, s.notesLog.node.Propose); err != nil {
		noteUnknown(w, err, rev.Note)
		return
	}
	// The log ordered this proposal and every copy applies it, unless an
	// earlier one took its revision number (two changes to one note proposed
	// together: both commit, the second applies nowhere). Say so: it was not
	// kept.
	if n, ok := s.notesStore().Get(rev.Note); !ok || len(n.Revisions) < rev.Rev ||
		n.Revisions[rev.Rev-1].Principal != s.principal(r) || n.Revisions[rev.Rev-1].Text != rev.Text ||
		n.Revisions[rev.Rev-1].Retract != rev.Retract {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "the note changed while you were saving: your change was not applied; reload it and try again"})
		return
	}
	s.noteAnswer(w, rev.Note, okCode)
}

func (s *Server) noteAnswer(w http.ResponseWriter, id string, code int) {
	n, ok := s.notesStore().Get(id)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "the note was not found after it was kept"})
		return
	}
	writeJSON(w, code, viewOf(n))
}

// refuseNoteWithoutMajority is the acknowledgements' rule (the read-only
// reason a copy that cannot count a majority gives): nothing is queued.
func (s *Server) refuseNoteWithoutMajority(w http.ResponseWriter) bool {
	st := s.notesLog.node.Status()
	if st.Writable {
		return false
	}
	msg := PairNoMajorityNotes
	switch {
	case st.NotSynced:
		msg = LogNotSynced
	case st.Uncounted:
		msg = LogCopiesNotSynced
	}
	if st.Reason != "" {
		msg += " (" + st.Reason + ")"
	}
	refuse(w, msg, http.StatusServiceUnavailable)
	return true
}

// noteUnknown answers a note whose proposal was not confirmed: it may
// still be committed. The note's id lets the page ask (GET /api/notes/{id})
// and keep what the person wrote until it knows.
func noteUnknown(w http.ResponseWriter, err error, id string) {
	if errors.Is(err, oplog.ErrTooLarge) {
		refuse(w, "too large for the operator-intent log: "+err.Error(), http.StatusRequestEntityTooLarge)
		return
	}
	w.Header().Set(outcomeHeader, "unknown")
	writeJSON(w, http.StatusServiceUnavailable, map[string]string{
		"error": "outcome unknown: the note was passed to the replicated log but not confirmed, so it may still be kept; read GET /api/notes/" + id + " to see (" + err.Error() + ")",
		"note":  id,
	})
}

func decodeNoteBody(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return errors.New("the body is JSON: " + err.Error())
	}
	return nil
}

func (s *Server) handleNoteGet(w http.ResponseWriter, r *http.Request) {
	if s.notesOff(w) {
		return
	}
	n, ok := s.notesStore().Get(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such note"})
		return
	}
	writeJSON(w, http.StatusOK, struct {
		noteView
		History []notes.Revision `json:"history"`
	}{viewOf(n), n.Revisions})
}

func (s *Server) handleNotesList(w http.ResponseWriter, r *http.Request) {
	if s.notesOff(w) {
		return
	}
	q := r.URL.Query()
	ms := func(k string) int64 { v, _ := strconv.ParseInt(q.Get(k), 10, 64); return v }
	lim, _ := strconv.Atoi(q.Get("limit"))
	if q.Get("superseded") == "1" {
		s.handleNotesSuperseded(w, q, ms, lim)
		return
	}
	found := s.notesStore().List(notes.Query{
		TargetKind: q.Get("target"), TargetID: q.Get("id"), Occurrence: q.Get("occurrence"),
		Kind: q.Get("kind"), Author: q.Get("author"), Since: ms("since"), Until: ms("until"),
		Retracted: q.Get("retracted") == "1", Limit: lim,
	})
	out := notesAnswer{Notes: make([]noteView, 0, len(found))}
	for _, n := range found {
		out.Notes = append(out.Notes, viewOf(n))
	}
	writeJSON(w, http.StatusOK, out)
}

// noteAuditTarget names what a note write is about in the journal: a new
// note by what it is about, a revision by the note.
func noteAddTarget(_ *http.Request, body []byte) auditTarget {
	var req noteRequest
	// the first JSON value, as the handler reads it
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&req); err != nil {
		return auditTarget{}
	}
	t := req.Target.Kind
	if req.Target.ID != "" {
		t += ":" + req.Target.ID
	}
	if t == "" && req.Re != "" {
		t = "re:" + req.Re // an addendum that names no target of its own
	}
	return auditTarget{target: t, answered: noteAfter}
}

func noteChangeTarget(r *http.Request, _ []byte) auditTarget {
	return auditTarget{target: "note:" + r.PathValue("id"), answered: noteAfter}
}

// noteAfter is what the journal keeps of a note write: the note, the
// revision and the text, so the audit export is self-contained.
func noteAfter(body []byte) any {
	var v noteView
	if json.Unmarshal(body, &v) != nil || v.ID == "" {
		return nil
	}
	return map[string]any{"note": v.ID, "revision": v.Revisions, "text": v.Text, "by": v.By, "retracted": v.Retracted}
}

// supersededAnswer is the notes of a replaced log history: labelled, and
// never mixed with the live ones.
type supersededAnswer struct {
	Notes []noteView `json:"notes"`
	// History says what it is: who accepted the loss of that history
	// (naut redundancy vouch) and when its log was moved aside.
	History *struct {
		By      string `json:"by,omitempty"`
		MovedAt int64  `json:"movedAt,omitempty"`
	} `json:"history,omitempty"`
}

// handleNotesSuperseded answers GET /api/notes?superseded=1: the notes the
// copy's replaced history held (a record: a person accepted losing that
// history's operator state, not its shift log), read from the log file kept
// aside, never applied, never editable (they are not in the live index).
// A note the live history also holds is not repeated.
func (s *Server) handleNotesSuperseded(w http.ResponseWriter, q map[string][]string, ms func(string) int64, lim int) {
	get := func(k string) string {
		if v := q[k]; len(v) > 0 {
			return v[0]
		}
		return ""
	}
	out := supersededAnswer{Notes: []noteView{}}
	if s.notesLog != nil {
		if sup, ok := s.notesLog.Superseded(); ok {
			var moved int64
			if !sup.MovedAt.IsZero() {
				moved = sup.MovedAt.UnixMilli()
			}
			out.History = &struct {
				By      string `json:"by,omitempty"`
				MovedAt int64  `json:"movedAt,omitempty"`
			}{sup.By, moved}
			if lim <= 0 || lim > 1000 {
				lim = 200
			}
			// the limit counts what is shown: the notes the live history also
			// holds are left out first
			for _, n := range sup.Store.List(notes.Query{
				TargetKind: get("target"), TargetID: get("id"), Occurrence: get("occurrence"), Kind: get("kind"), Author: get("author"),
				Since: ms("since"), Until: ms("until"), Retracted: get("retracted") == "1", Limit: 1000,
			}) {
				if _, live := s.notesStore().Get(n.ID); live {
					continue
				}
				if len(out.Notes) < lim {
					out.Notes = append(out.Notes, viewOf(n))
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleNotesCounts answers GET /api/notes/counts?target=alarm: how many
// live notes each target of that kind has, for an "N notes" beside it.
func (s *Server) handleNotesCounts(w http.ResponseWriter, r *http.Request) {
	if s.notesOff(w) {
		return
	}
	kind := r.URL.Query().Get("target")
	if err := notes.CheckTarget(notes.Target{Kind: kind, ID: "x"}); err != nil && kind != notes.KindLog {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "target is asset, tag, alarm or area"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"target": kind, "counts": s.notesStore().Counts(kind)})
}
