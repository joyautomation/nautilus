// Package notes is the operator notes and shift log (#720): what a person
// wrote about an asset, a tag, an alarm, an area or the plant, who wrote
// it and when, and every revision since.
//
// A controller on its own keeps them in an append-only file, fsynced per
// revision. On a redundant pair the replicated log (oplog) is the store:
// the notes are an index of the note entries it holds, kept in memory and
// rebuilt by replaying the log at every start, and never read back from a
// file of their own. So a note is on every copy, a copy that was wiped is
// brought every note back by the log's catch-up, and when a person
// accepts a copy's loss (naut redundancy vouch) a superseded history's
// notes leave the live view with the rest of that history: the index is
// exactly the log's.
//
// A note is never overwritten and never deleted. An edit is a new
// revision that names the note and its number; a retraction is a revision
// flagged retracted, and the text of every earlier revision stays. A
// revision is applied idempotently by (note, revision) and a note is
// named by a random id the proposer chose, never by a log sequence number
// (a new log history numbers them again).
//
// It imports only the standard library and oplog (for the one entry the
// log carries); who may write what, and the one hour an author has to edit
// their own note, are the server's and decided before a revision exists
// (docs/design/context-map.md §2).
package notes

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"
)

// Limits of one note.
const (
	MaxText   = 4096 // characters
	MaxReason = 256
	MaxID     = 128
	// EditWindowMs is how long after writing a note its author may revise
	// or retract it; after that a correction is an addendum.
	EditWindowMs = 60 * 60 * 1000
)

// Target kinds.
const (
	KindAsset = "asset"
	KindTag   = "tag"
	KindAlarm = "alarm"
	KindArea  = "area"
	KindLog   = "log" // the plant-wide shift log: no id
)

// Kinds of note (revision 1).
const (
	NoteNote     = "note"
	NoteHandover = "handover" // highlighted for the next shift
)

// Target is what a note is about.
type Target struct {
	Kind string `json:"kind"`
	ID   string `json:"id,omitempty"`
	// Occurrence and ActiveMs link an alarm note to one occurrence of the
	// alarm: the occurrence id #715 gives it, and when it went active (the
	// stand-in while an engine gives no id, and a check beside it after).
	Occurrence string `json:"occurrence,omitempty"`
	ActiveMs   int64  `json:"activeMs,omitempty"`
}

// Revision is one stored version of a note.
type Revision struct {
	Note string `json:"note"`
	Rev  int    `json:"rev"`
	At   int64  `json:"at"` // epoch ms; the leader's for a pair, else this controller's
	// By is the author's name as it was written (kept if the person is
	// removed); Principal is their audit form ("user:alice").
	By        string `json:"by"`
	Principal string `json:"principal,omitempty"`
	// Target, Kind and Re are set on revision 1 and carried by the rest.
	Target Target `json:"target"`
	Kind   string `json:"kind,omitempty"`
	Re     string `json:"re,omitempty"`
	Text   string `json:"text"`
	// Retract withdraws the note; Reason says why; Override says someone
	// other than the author did.
	Retract  bool   `json:"retract,omitempty"`
	Reason   string `json:"reason,omitempty"`
	Override bool   `json:"override,omitempty"`
}

// Note is a note with every revision, oldest first.
type Note struct {
	ID        string     `json:"id"`
	Revisions []Revision `json:"revisions"`
}

// First is the note as first written.
func (n *Note) First() Revision { return n.Revisions[0] }

// Latest is its newest revision.
func (n *Note) Latest() Revision { return n.Revisions[len(n.Revisions)-1] }

// Retracted reports whether its newest revision withdraws it.
func (n *Note) Retracted() bool { return n.Latest().Retract }

// Edited reports whether it has been revised (a retraction counts).
func (n *Note) Edited() bool { return len(n.Revisions) > 1 }

// Errors the store answers with.
var (
	ErrNoNote    = errors.New("notes: no such note")
	ErrRevOrder  = errors.New("notes: not the next revision of that note")
	ErrCorrupt   = errors.New("notes: the notes file is damaged")
	ErrDuplicate = errors.New("notes: that note already exists")
)

// NewID is a note id: 128 random bits, hex.
func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // crypto/rand never fails on a supported platform
	}
	return hex.EncodeToString(b[:])
}

// CheckText refuses text a note may not hold: empty, over MaxText
// characters, or not valid UTF-8; and control characters other than
// newline and tab (a note is plain text: nothing renders it as markup).
func CheckText(t string) error {
	if strings.TrimSpace(t) == "" {
		return errors.New("a note needs some text")
	}
	if !utf8.ValidString(t) {
		return errors.New("a note's text is not valid UTF-8")
	}
	if utf8.RuneCountInString(t) > MaxText {
		return fmt.Errorf("a note is at most %d characters (this one is %d): split it, or make the rest an addendum", MaxText, utf8.RuneCountInString(t))
	}
	for _, r := range t {
		if r < ' ' && r != '\n' && r != '\t' || r == 0x7f {
			return errors.New("a note's text may hold a newline and a tab, no other control character")
		}
	}
	return nil
}

// CheckReason refuses a retraction's reason that is empty, over MaxReason
// characters or holds a control character.
func CheckReason(t string) error {
	if strings.TrimSpace(t) == "" {
		return errors.New("a retraction needs a reason")
	}
	if utf8.RuneCountInString(t) > MaxReason {
		return fmt.Errorf("a reason is at most %d characters", MaxReason)
	}
	if !utf8.ValidString(t) || strings.ContainsFunc(t, func(r rune) bool { return r < ' ' || r == 0x7f }) {
		return errors.New("a reason is one line of plain text")
	}
	return nil
}

// CheckTarget refuses a malformed target (not whether it exists: the
// manifest decides that).
func CheckTarget(t Target) error {
	switch t.Kind {
	case KindLog:
		if t.ID != "" || t.Occurrence != "" || t.ActiveMs != 0 {
			return errors.New("the shift log target has no id or occurrence")
		}
		return nil
	case KindAsset, KindTag, KindArea, KindAlarm:
	default:
		return fmt.Errorf("target kind %q is not asset, tag, alarm, area or log", t.Kind)
	}
	if t.ID == "" || len(t.ID) > MaxID || strings.ContainsFunc(t.ID, func(r rune) bool { return r < ' ' || r == 0x7f }) {
		return fmt.Errorf("a %s target needs an id of at most %d characters", t.Kind, MaxID)
	}
	if t.Kind != KindAlarm && (t.Occurrence != "" || t.ActiveMs != 0) {
		return errors.New("only an alarm note names an occurrence")
	}
	if t.Occurrence != "" && (len(t.Occurrence) > 64 || strings.Trim(t.Occurrence, "0123456789abcdef") != "") {
		return errors.New("an occurrence is the lowercase hex id the alarm gave it")
	}
	if t.ActiveMs < 0 {
		return errors.New("activeMs is epoch milliseconds")
	}
	return nil
}

// Store is the notes: in memory for reading, and, standing alone, in a file
// for keeping.
type Store struct {
	path string // "": the pair's index, kept by the log
	off  int64  // the file's length after the last whole revision

	mu    sync.Mutex
	f     *os.File
	notes map[string]*Note
	order []string // note ids by first revision's time then arrival
}

// Open reads the notes file (made if absent). A torn last line (a crash in
// the middle of a write) is dropped; damage before it is an error, and the
// file is left as it was.
func Open(path string) (*Store, error) {
	s := &Store{path: path, notes: map[string]*Note{}}
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	good := 0
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Bytes()
		var r Revision
		if err := json.Unmarshal(line, &r); err != nil {
			rest := data[good:]
			if i := bytes.IndexByte(rest, '\n'); i >= 0 && len(bytes.TrimSpace(rest[i+1:])) > 0 {
				return nil, fmt.Errorf("%w: %s: line %q is not a revision, and more follows it", ErrCorrupt, path, trunc(string(line), 60))
			}
			break // a torn last line
		}
		good += len(line) + 1
		if _, err := s.insertLocked(r); err != nil && !errors.Is(err, errSeen) {
			return nil, fmt.Errorf("%w: %s: %v", ErrCorrupt, path, err)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrCorrupt, path, err) // never silently drop what follows a line it cannot read
	}
	if good > len(data) {
		good = len(data) // the last line had no newline to count
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if good < len(data) {
		if err := f.Truncate(int64(good)); err != nil {
			f.Close()
			return nil, err
		}
	}
	end, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		f.Close()
		return nil, err
	}
	// a last line that parsed but has no newline would share its line with
	// the next append, and both would be lost at the next open
	if end > 0 && (good == 0 || data[good-1] != '\n') {
		if _, err := f.Write([]byte{'\n'}); err != nil {
			f.Close()
			return nil, err
		}
		end++
	}
	s.f, s.off = f, end
	return s, nil
}

// ReadFile reads a notes file without changing it: not created, not
// truncated, not held open. A tool that reads a controller's file while it
// runs (naut notes export) uses it, never Open.
func ReadFile(path string) (*Store, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	s := &Store{path: path, notes: map[string]*Note{}}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	good := 0
	for sc.Scan() {
		line := sc.Bytes()
		var r Revision
		if err := json.Unmarshal(line, &r); err != nil {
			rest := data[good:]
			if i := bytes.IndexByte(rest, '\n'); i >= 0 && len(bytes.TrimSpace(rest[i+1:])) > 0 {
				return nil, fmt.Errorf("%w: %s: line %q is not a revision, and more follows it", ErrCorrupt, path, trunc(string(line), 60))
			}
			break
		}
		good += len(line) + 1
		if _, err := s.insertLocked(r); err != nil && !errors.Is(err, errSeen) {
			return nil, fmt.Errorf("%w: %s: %v", ErrCorrupt, path, err)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrCorrupt, path, err)
	}
	return s, nil
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// New is a store that keeps nothing itself: the index of a pair's log
// (see the package comment), filled by Apply as the log replays and
// commits, and emptied by Reset when the log's history is replaced.
func New() *Store { return &Store{notes: map[string]*Note{}} }

// Reset empties the index: the log's history was replaced (a copy that
// was superseded starts again from the new history's entries). It is for
// the pair's index only; a standalone store is never reset.
func (s *Store) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.path != "" {
		return
	}
	s.notes, s.order = map[string]*Note{}, nil
}

// Close releases the file.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f == nil {
		return nil
	}
	err := s.f.Close()
	s.f = nil
	return err
}

var errSeen = errors.New("seen")

// insertLocked puts r in the index. A revision already held (same note and
// number, same content) is errSeen: nothing changed. The next number is
// taken; a gap or a different revision under a held number is an error.
func (s *Store) insertLocked(r Revision) (*Note, error) {
	if r.Note == "" || r.Rev < 1 {
		return nil, errors.New("a revision names its note and its number")
	}
	n := s.notes[r.Note]
	if n == nil {
		if r.Rev != 1 {
			return nil, fmt.Errorf("%w: %s revision %d with no note", ErrRevOrder, r.Note, r.Rev)
		}
		n = &Note{ID: r.Note, Revisions: []Revision{r}}
		s.notes[r.Note] = n
		s.order = append(s.order, r.Note)
		return n, nil
	}
	have := len(n.Revisions)
	switch {
	case r.Rev <= have:
		a, _ := json.Marshal(n.Revisions[r.Rev-1])
		b, _ := json.Marshal(r)
		if bytes.Equal(a, b) {
			return n, errSeen
		}
		return nil, fmt.Errorf("%w: %s revision %d differs from the one held", ErrRevOrder, r.Note, r.Rev)
	case r.Rev == have+1:
		n.Revisions = append(n.Revisions, r)
		return n, nil
	}
	return nil, fmt.Errorf("%w: %s revision %d after %d", ErrRevOrder, r.Note, r.Rev, have)
}

// Apply keeps r: the file first (standing alone), then the index, so a
// revision that is answered is on disk. It is idempotent: a revision already held is not
// written again (false, nil). A revision the notes cannot take (a gap, a
// different one under a held number) is an error and nothing is kept.
func (s *Store) Apply(r Revision) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// would it be kept? try on a scratch copy of the one note
	if n := s.notes[r.Note]; n != nil {
		if r.Rev <= len(n.Revisions) {
			a, _ := json.Marshal(n.Revisions[r.Rev-1])
			b, _ := json.Marshal(r)
			if bytes.Equal(a, b) {
				return false, nil
			}
			return false, fmt.Errorf("%w: %s revision %d differs from the one held", ErrRevOrder, r.Note, r.Rev)
		}
		if r.Rev != len(n.Revisions)+1 {
			return false, fmt.Errorf("%w: %s revision %d after %d", ErrRevOrder, r.Note, r.Rev, len(n.Revisions))
		}
	} else if r.Rev != 1 {
		return false, fmt.Errorf("%w: %s revision %d with no note", ErrRevOrder, r.Note, r.Rev)
	}
	if s.path != "" {
		if s.f == nil {
			return false, errors.New("notes: the store is closed")
		}
		line, err := json.Marshal(r)
		if err != nil {
			return false, err
		}
		line = append(line, '\n')
		// a write that fails part way (a full disk) or a sync that fails must
		// not leave half a line, or a line that was answered "not kept", for
		// the next append to glue onto or contradict: back to the last whole
		// revision, then the error
		rollback := func() {
			_ = s.f.Truncate(s.off)
			_, _ = s.f.Seek(s.off, io.SeekStart)
		}
		if _, err := s.f.Write(line); err != nil {
			rollback()
			return false, err
		}
		if err := s.f.Sync(); err != nil {
			rollback()
			return false, err
		}
		s.off += int64(len(line))
	}
	if _, err := s.insertLocked(r); err != nil {
		return false, err // unreachable after the checks above
	}
	return true, nil
}

// Get is a note with its revisions.
func (s *Store) Get(id string) (Note, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.notes[id]
	if n == nil {
		return Note{}, false
	}
	return Note{ID: n.ID, Revisions: append([]Revision(nil), n.Revisions...)}, true
}

// Query narrows List.
type Query struct {
	TargetKind, TargetID, Occurrence string
	Kind, Author                     string // note kind; author's name or principal
	Since, Until                     int64  // first revision's time, epoch ms; 0: unbounded
	Retracted                        bool   // include retracted notes
	Limit                            int    // 0: 200
}

// List is the notes that match, newest first (by first revision).
func (s *Store) List(q Query) []Note {
	if q.Limit <= 0 || q.Limit > 1000 {
		q.Limit = 200
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Note
	for _, id := range s.order {
		n := s.notes[id]
		f := n.First()
		switch {
		case n.Retracted() && !q.Retracted,
			q.TargetKind != "" && f.Target.Kind != q.TargetKind,
			q.TargetID != "" && f.Target.ID != q.TargetID,
			q.Occurrence != "" && f.Target.Occurrence != q.Occurrence,
			q.Kind != "" && kindOf(f) != q.Kind,
			q.Author != "" && f.By != q.Author && f.Principal != q.Author,
			q.Since > 0 && f.At < q.Since,
			q.Until > 0 && f.At >= q.Until:
			continue
		}
		out = append(out, Note{ID: n.ID, Revisions: append([]Revision(nil), n.Revisions...)})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].First().At > out[j].First().At })
	if len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out
}

func kindOf(r Revision) string {
	if r.Kind == "" {
		return NoteNote
	}
	return r.Kind
}

// Count is how many notes the store holds (retracted included).
func (s *Store) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.notes)
}

// CountFor counts the live (not retracted) notes about an alarm id, and
// the ones about one of its occurrences (occurrence "" counts every one):
// what the alarms page shows as "N notes".
func (s *Store) CountFor(kind, id, occurrence string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, note := range s.notes {
		f := note.First()
		if note.Retracted() || f.Target.Kind != kind || f.Target.ID != id {
			continue
		}
		if occurrence != "" && f.Target.Occurrence != occurrence {
			continue
		}
		n++
	}
	return n
}

// All is every note, oldest first by first revision: what an export
// writes.
func (s *Store) All() []Note {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Note, 0, len(s.notes))
	for _, id := range s.order {
		n := s.notes[id]
		out = append(out, Note{ID: n.ID, Revisions: append([]Revision(nil), n.Revisions...)})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].First().At < out[j].First().At })
	return out
}

// Counts is how many live (not retracted) notes each target of the kind
// has, by target id: what a screen shows as "N notes" beside an alarm or a
// tag without reading the notes.
func (s *Store) Counts(kind string) map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]int{}
	for _, n := range s.notes {
		f := n.First()
		if !n.Retracted() && f.Target.Kind == kind && f.Target.ID != "" {
			out[f.Target.ID]++
		}
	}
	return out
}
