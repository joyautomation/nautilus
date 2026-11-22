package checkout

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
	"unicode/utf8"
)

// Outcomes of checking a point.
const (
	Pass = "pass"
	Fail = "fail"
	Note = "note" // a remark with no verdict: "wired, not yet powered"
)

// States of a run.
const (
	StateOpen      = "open"
	StateAbandoned = "abandoned"
	StateSealed    = "sealed"
)

// Errors the store answers with.
var (
	ErrNoRun   = errors.New("checkout: no such run")
	ErrNotOpen = errors.New("checkout: that run is not open")
	ErrRunOpen = errors.New("checkout: a run is already open: finish or abandon it first")
	ErrCorrupt = errors.New("checkout: the checkout file is damaged")
	// ErrForceActive: a checkout force already stands (one at a time).
	ErrForceActive = errors.New("checkout: a checkout force already stands: release it first")
	// ErrNoForce: there is no checkout force to end.
	ErrNoForce = errors.New("checkout: no checkout force stands")
)

// How a checkout force ended.
const (
	ForceReleased    = "released"                  // by a person
	ForceExpired     = "expired"                   // its time ran out
	ForceAbandoned   = "run abandoned"             // its run was abandoned
	ForceInterrupted = "interrupted"               // the controller restarted, or lost the force
	ForceReplaced    = "replaced"                  // someone else changed or cleared it
	ForceRefused     = "not applied"               // the force table refused the value
	ForceInterlock   = "equipment back in service" // its interlock stopped holding
)

// Kinds of entry in the file.
const (
	kindRun      = "run"
	kindResult   = "result"
	kindAbandon  = "abandon"
	kindForce    = "force"    // a checkout force began on a point
	kindForceEnd = "forceend" // and ended
	kindSeal     = "seal"     // the run was signed off (seal.go)
)

// entry is one line of the file.
type entry struct {
	Seq  int64  `json:"seq"`
	At   int64  `json:"at"` // epoch ms, this controller's wall clock: informational
	Prev string `json:"prev,omitempty"`
	Kind string `json:"kind"`
	Run  string `json:"run"`

	// kind run
	Rehearsal bool   `json:"rehearsal,omitempty"`
	SheetHash string `json:"sheet,omitempty"`
	Title     string `json:"title,omitempty"`

	// kind run, from the controller: a hash of the devices it was begun
	// against, and why the run could never be signed off (virtual devices)
	EvSha  string `json:"evsha,omitempty"`
	Unreal string `json:"unreal,omitempty"`

	// kind result: what the controller saw standing on the point's tag when it
	// recorded ("forced: X", "substituted: X"), from the controller
	Override string `json:"override,omitempty"`

	// kind result
	Point   string `json:"point,omitempty"`
	Outcome string `json:"outcome,omitempty"`
	Seen    any    `json:"seen,omitempty"`
	Quality string `json:"quality,omitempty"`

	// kind force: the value forced and the seconds it may stand; kind
	// forceend: how it ended (Reason)
	Forced  any `json:"forced,omitempty"`
	Seconds int `json:"seconds,omitempty"`

	// kind seal: the SHA-256 of the signed payload, and the signature (seal.go)
	SealSha string `json:"sealsha,omitempty"`
	Sig     string `json:"sig,omitempty"`

	// kinds result, abandon and seal
	Note   string `json:"note,omitempty"`
	Reason string `json:"reason,omitempty"`

	By        string `json:"by"`
	Principal string `json:"principal,omitempty"`
}

// Result is one recorded result.
type Result struct {
	Run     string `json:"run"`
	Seq     int64  `json:"seq"`
	At      int64  `json:"at"`
	Point   string `json:"point"`
	Outcome string `json:"outcome"`
	Note    string `json:"note,omitempty"`
	// Seen is what the controller read at the point's tag when the result
	// was recorded, and Quality its quality: written by the controller, never
	// by the client.
	Seen    any    `json:"seen,omitempty"`
	Quality string `json:"quality,omitempty"`
	// Override says a force or a substitute (other than this checkout's own
	// force on the point) stood on the point's tag when the result was
	// recorded: what the reading then says is not the wiring's. Only a fail or
	// a note is recorded so; the controller refuses a pass.
	Override  string `json:"override,omitempty"`
	By        string `json:"by"`
	Principal string `json:"principal,omitempty"`
	// Force is the checkout force the point last had in the run, when it had
	// one: what was forced, how long it was allowed, how it ended (empty while
	// it stands), and the sequence number of the entry that began it, so the
	// sheet can be checked against the audit journal.
	Force *ForceRef `json:"force,omitempty"`
}

// ForceRef says a point was forced during the checkout.
type ForceRef struct {
	Seq     int64  `json:"seq"`
	Value   any    `json:"value"`
	Seconds int    `json:"seconds"`
	Ended   string `json:"ended,omitempty"`
}

// ForceRec is a checkout force: the point, the value, who began it and when,
// and the seconds it may stand.
type ForceRec struct {
	Run       string `json:"run"`
	Point     string `json:"point"`
	Seq       int64  `json:"seq"`
	At        int64  `json:"at"`
	Value     any    `json:"value"`
	Seconds   int    `json:"seconds"`
	By        string `json:"by"`
	Principal string `json:"principal,omitempty"`
}

// Run is one checkout against a sheet.
type Run struct {
	ID        string `json:"id"`
	State     string `json:"state"`
	Rehearsal bool   `json:"rehearsal,omitempty"`
	SheetHash string `json:"sheet"`
	Title     string `json:"title,omitempty"`
	At        int64  `json:"at"`
	By        string `json:"by"`
	Principal string `json:"principal,omitempty"`
	// Reason is why an abandoned run was abandoned, by whom and when.
	Reason  string `json:"reason,omitempty"`
	EndedAt int64  `json:"endedAt,omitempty"`
	EndedBy string `json:"endedBy,omitempty"`
	// EvidenceSha is a hash of the devices the controller was connected to when
	// the run began, and Unreal why the run can never be signed off (the
	// controller found virtual devices or no field driver then): both are the
	// controller's, never the client's.
	EvidenceSha string `json:"evidenceSha,omitempty"`
	Unreal      string `json:"unreal,omitempty"`
	// SealedAt, SealedBy and SealSeq say a sealed run was signed off: when,
	// by whom, and the sequence number of the seal entry.
	SealedAt    int64  `json:"sealedAt,omitempty"`
	SealedBy    string `json:"sealedBy,omitempty"`
	SealSeq     int64  `json:"sealSeq,omitempty"`
	Results     int    `json:"results"`
	lastResults map[string][]Result
	// forces is each point's checkout forces in the run, oldest first.
	forces map[string][]*forceState
	// standing is the checkout force that has begun and not ended.
	standing *forceState
}

type forceState struct {
	rec   ForceRec
	ended string
}

// Store is the runs and their results, kept in one append-only file.
type Store struct {
	mu   sync.Mutex
	path string
	f    appendFile
	now  func() time.Time
	// off is the size of the good part of the file: where a failed append is
	// cut back to, so a short write never has the next entry glued onto it.
	off int64
	// broken is why the store stopped taking entries: a write failed and the
	// file could not be cut back. It is cleared by opening the file again.
	broken error

	seq  int64
	prev string
	runs map[string]*Run
	ord  []string

	// key signs sealed runs (seal.go): created beside the file at Open.
	key         *signKey
	keyReplaced bool
}

// appendFile is the file as the store uses it (a test's can fail).
type appendFile interface {
	Write([]byte) (int, error)
	Sync() error
	Truncate(int64) error
	Close() error
}

// MaxLine bounds one entry's encoded line: a result's reading is shortened
// to fit, so no entry the store acknowledges is one it cannot read back.
const MaxLine = 256 << 10

// ErrInvalid marks a refusal that is the caller's (a bad outcome, a missing
// note, text too long), as opposed to the store failing.
var ErrInvalid = errors.New("checkout: invalid request")

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrInvalid}, a...)...)
}

// Open reads the file (creating it when absent) and returns the store.
// A line that is not the next in the chain, or does not parse, is
// ErrCorrupt: the file is never repaired or rewritten. A final line with no
// newline is a write that was cut short before it was acknowledged, and is
// cut off.
func Open(path string, now func() time.Time) (*Store, error) {
	if now == nil {
		now = time.Now
	}
	s := &Store{path: path, now: now, runs: map[string]*Run{}}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		good := len(data)
		if i := bytes.LastIndexByte(data, '\n'); i != len(data)-1 {
			good = i + 1 // a torn last line: drop it
			if i < 0 {
				good = 0
			}
		}
		if err := s.replay(bytes.NewReader(data[:good])); err != nil {
			return nil, err
		}
		if good != len(data) {
			if err := os.Truncate(path, int64(good)); err != nil {
				return nil, err
			}
		}
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	s.f = f
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	s.off = st.Size()
	syncDir(filepath.Dir(path))
	var created bool
	if s.key, created, err = loadOrCreateKey(filepath.Join(filepath.Dir(path), KeyFile)); err != nil {
		f.Close()
		return nil, fmt.Errorf("checkout: the signing key: %w", err)
	}
	for _, r := range s.runs {
		if r.State == StateSealed && created {
			s.keyReplaced = true
		}
	}
	s.sweepSealed()
	return s, nil
}

// Close closes the file.
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

func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
}

func (s *Store) replay(r io.Reader) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Bytes()
		var e entry
		if err := json.Unmarshal(line, &e); err != nil {
			return fmt.Errorf("%w: line %d: %v", ErrCorrupt, s.seq+1, err)
		}
		if e.Seq != s.seq+1 || e.Prev != s.prev {
			return fmt.Errorf("%w: line %d is not the next entry of the chain", ErrCorrupt, s.seq+1)
		}
		if err := s.apply(e); err != nil {
			return fmt.Errorf("%w: line %d: %v", ErrCorrupt, e.Seq, err)
		}
		s.seq, s.prev = e.Seq, lineHash(line)
	}
	return sc.Err()
}

func lineHash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// apply folds an entry into the in-memory view.
func (s *Store) apply(e entry) error {
	switch e.Kind {
	case kindRun:
		if e.Run == "" || s.runs[e.Run] != nil {
			return errors.New("a run that is empty or already begun")
		}
		s.runs[e.Run] = &Run{ID: e.Run, State: StateOpen, Rehearsal: e.Rehearsal, SheetHash: e.SheetHash, Title: e.Title,
			EvidenceSha: e.EvSha, Unreal: e.Unreal, At: e.At, By: e.By, Principal: e.Principal, lastResults: map[string][]Result{}, forces: map[string][]*forceState{}}
		s.ord = append(s.ord, e.Run)
	case kindResult:
		r := s.runs[e.Run]
		if r == nil || r.State != StateOpen {
			return errors.New("a result for a run that is not open")
		}
		res := Result{Run: e.Run, Seq: e.Seq, At: e.At, Point: e.Point,
			Outcome: e.Outcome, Note: e.Note, Seen: e.Seen, Quality: e.Quality, Override: e.Override, By: e.By, Principal: e.Principal}
		if fs := r.forces[e.Point]; len(fs) > 0 {
			last := fs[len(fs)-1]
			res.Force = &ForceRef{Seq: last.rec.Seq, Value: last.rec.Value, Seconds: last.rec.Seconds, Ended: last.ended}
		}
		r.lastResults[e.Point] = append(r.lastResults[e.Point], res)
		r.Results++
	case kindForce:
		r := s.runs[e.Run]
		if r == nil || r.State != StateOpen {
			return errors.New("a force for a run that is not open")
		}
		if r.standing != nil {
			return errors.New("a force while another stands")
		}
		fs := &forceState{rec: ForceRec{Run: e.Run, Point: e.Point, Seq: e.Seq, At: e.At, Value: e.Forced, Seconds: e.Seconds, By: e.By, Principal: e.Principal}}
		r.forces[e.Point] = append(r.forces[e.Point], fs)
		r.standing = fs
	case kindForceEnd:
		r := s.runs[e.Run]
		if r == nil || r.standing == nil || r.standing.rec.Point != e.Point {
			return errors.New("the end of a force that does not stand")
		}
		r.standing.ended = e.Reason
		r.standing = nil
	case kindAbandon:
		r := s.runs[e.Run]
		if r == nil || r.State != StateOpen {
			return errors.New("an abandon of a run that is not open")
		}
		if r.standing != nil {
			return errors.New("an abandon while a force stands")
		}
		r.State, r.Reason, r.EndedAt, r.EndedBy = StateAbandoned, e.Reason, e.At, e.By
	case kindSeal:
		r := s.runs[e.Run]
		if r == nil || r.State != StateOpen {
			return errors.New("a seal of a run that is not open")
		}
		if r.standing != nil {
			return errors.New("a seal while a force stands")
		}
		if r.Rehearsal {
			return errors.New("a seal of a rehearsal run")
		}
		r.State, r.SealedAt, r.SealedBy, r.SealSeq = StateSealed, e.At, e.By, e.Seq
	default:
		return fmt.Errorf("unknown entry kind %q", e.Kind)
	}
	return nil
}

// append writes e as the next line, fsyncs the file, and only then folds it
// into the view. A write or sync that fails cuts the file back to its last
// good size, so the entry that failed leaves nothing behind; if that fails
// too, the store takes no more entries until it is opened again.
func (s *Store) append(e entry) error {
	if s.f == nil {
		return errors.New("checkout: the store is closed")
	}
	if s.broken != nil {
		return fmt.Errorf("checkout: the store stopped after a failed write (%v): restart the controller", s.broken)
	}
	e.Seq, e.At, e.Prev = s.seq+1, s.now().UnixMilli(), s.prev
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if len(line) > MaxLine && e.Seen != nil {
		e.Seen = "(too large to record)"
		if line, err = json.Marshal(e); err != nil {
			return err
		}
	}
	if len(line) > MaxLine {
		return invalid("the entry is larger than %d bytes", MaxLine)
	}
	out := append(line, '\n')
	if _, err := s.f.Write(out); err != nil {
		return s.undo(err)
	}
	if err := s.f.Sync(); err != nil {
		return s.undo(err)
	}
	if err := s.apply(e); err != nil {
		return s.undo(err)
	}
	s.off += int64(len(out))
	s.seq, s.prev = e.Seq, lineHash(line)
	return nil
}

// undo cuts the file back to its last good size after a failed append and
// returns err; if it cannot, the store is stopped.
func (s *Store) undo(err error) error {
	if terr := s.f.Truncate(s.off); terr != nil {
		s.broken = fmt.Errorf("%v; and the file could not be cut back: %v", err, terr)
	}
	return err
}

func newID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func (s *Store) openRun() *Run {
	for _, id := range s.ord {
		if r := s.runs[id]; r.State == StateOpen {
			return r
		}
	}
	return nil
}

// Begin starts a run against the sheet with the hash. There is one open run
// at a time. A rehearsal run (against virtual devices) is marked so for good:
// a run's kind is never changed afterwards.
func (s *Store) Begin(rehearsal bool, sheetHash, title, by, principal string) (Run, error) {
	return s.BeginWith(rehearsal, sheetHash, title, by, principal, "", "")
}

// BeginWith is Begin with what the controller found out about its own devices:
// a hash of the evidence (the run is sealed only against the same devices) and,
// when the controller can already tell the run is not against real field I/O,
// why (such a run can never be sealed).
func (s *Store) BeginWith(rehearsal bool, sheetHash, title, by, principal, evidenceSha, unreal string) (Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.openRun() != nil {
		return Run{}, ErrRunOpen
	}
	e := entry{Kind: kindRun, Run: newID(), Rehearsal: rehearsal, SheetHash: sheetHash, Title: title, By: by, Principal: principal, EvSha: evidenceSha, Unreal: unreal}
	if err := s.append(e); err != nil {
		return Run{}, err
	}
	return s.runs[e.Run].view(), nil
}

// Record adds a result for a point of an open run. A fail and a note say
// why: a pass may have no words.
func (s *Store) Record(run, point, outcome, note string, seen any, quality, by, principal string) (Result, error) {
	return s.RecordWith(run, point, outcome, note, seen, quality, "", by, principal)
}

// RecordWith is Record with what the controller saw standing on the point's
// tag: a force or a substitute that is not this checkout's own. A pass is
// refused while one stands (a reading through an override proves nothing about
// the wiring); a fail or a note is recorded with it.
func (s *Store) RecordWith(run, point, outcome, note string, seen any, quality, override, by, principal string) (Result, error) {
	if override != "" && outcome == Pass {
		return Result{}, invalid("a pass is not recorded while %s: the reading is not the wiring's. Clear it and record the pass again; a fail or a note may be recorded now", override)
	}
	switch outcome {
	case Pass:
	case Fail, Note:
		if note == "" {
			return Result{}, invalid("a %s says why: write a note", outcome)
		}
	default:
		return Result{}, invalid("outcome %q: want pass, fail or note", outcome)
	}
	if point == "" || utf8.RuneCountInString(point) > MaxID {
		return Result{}, invalid("a result names a point")
	}
	if utf8.RuneCountInString(note) > MaxNote {
		return Result{}, invalid("the note is longer than %d characters", MaxNote)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runs[run]
	if r == nil {
		return Result{}, ErrNoRun
	}
	if r.State != StateOpen {
		return Result{}, ErrNotOpen
	}
	e := entry{Kind: kindResult, Run: run, Point: point, Outcome: outcome, Note: note, Seen: seen, Quality: quality, Override: override, By: by, Principal: principal}
	if err := s.append(e); err != nil {
		return Result{}, err
	}
	rs := r.lastResults[point]
	return rs[len(rs)-1], nil
}

// StartForce records that a checkout force began on a point of an open run:
// the value, and the seconds it may stand. One force stands at a time.
func (s *Store) StartForce(run, point string, value any, seconds int, by, principal string) (ForceRec, error) {
	if point == "" || utf8.RuneCountInString(point) > MaxID {
		return ForceRec{}, invalid("a force names a point")
	}
	if seconds < 1 {
		return ForceRec{}, invalid("a force stands for at least a second")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runs[run]
	if r == nil {
		return ForceRec{}, ErrNoRun
	}
	if r.State != StateOpen {
		return ForceRec{}, ErrNotOpen
	}
	if r.standing != nil {
		return ForceRec{}, ErrForceActive
	}
	if err := s.append(entry{Kind: kindForce, Run: run, Point: point, Forced: value, Seconds: seconds, By: by, Principal: principal}); err != nil {
		return ForceRec{}, err
	}
	return r.standing.rec, nil
}

// EndForce records that the standing checkout force of an open run ended, and
// how (one of the Force... constants).
func (s *Store) EndForce(run, how, by string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runs[run]
	if r == nil {
		return ErrNoRun
	}
	return s.endForceLocked(r, how, by)
}

func (s *Store) endForceLocked(r *Run, how, by string) error {
	if r.standing == nil {
		return ErrNoForce
	}
	return s.append(entry{Kind: kindForceEnd, Run: r.ID, Point: r.standing.rec.Point, Reason: how, By: by})
}

// Standing is the checkout force that has begun and not ended, in the open
// run.
func (s *Store) Standing() (ForceRec, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r := s.openRun(); r != nil && r.standing != nil {
		return r.standing.rec, true
	}
	return ForceRec{}, false
}

// InterruptStanding ends a force the file says is still standing: the forces
// of a controller do not survive it, so one left standing in the file belongs
// to a process that is gone. It returns how many it ended. The controller
// calls it once, at start.
func (s *Store) InterruptStanding() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, id := range s.ord {
		if r := s.runs[id]; r.State == StateOpen && r.standing != nil {
			if err := s.endForceLocked(r, ForceInterrupted, "controller"); err != nil {
				return n, err
			}
			n++
		}
	}
	return n, nil
}

// Abandon ends an open run without sealing it. Its results stay in the file
// and in the view; it can no longer take results.
func (s *Store) Abandon(run, reason, by, principal string) (Run, error) {
	if reason == "" {
		return Run{}, invalid("abandoning a run says why: give a reason")
	}
	if utf8.RuneCountInString(reason) > MaxReason {
		return Run{}, invalid("the reason is longer than %d characters", MaxReason)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runs[run]
	if r == nil {
		return Run{}, ErrNoRun
	}
	if r.State != StateOpen {
		return Run{}, ErrNotOpen
	}
	if r.standing != nil {
		if err := s.endForceLocked(r, ForceAbandoned, by); err != nil {
			return Run{}, err
		}
	}
	if err := s.append(entry{Kind: kindAbandon, Run: run, Reason: reason, By: by, Principal: principal}); err != nil {
		return Run{}, err
	}
	return r.view(), nil
}

func (r *Run) view() Run { v := *r; v.lastResults = nil; return v }

// Current is the open run, if there is one.
func (s *Store) Current() (Run, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r := s.openRun(); r != nil {
		return r.view(), true
	}
	return Run{}, false
}

// Get returns a run.
func (s *Store) Get(id string) (Run, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r := s.runs[id]; r != nil {
		return r.view(), true
	}
	return Run{}, false
}

// List returns every run, oldest first.
func (s *Store) List() []Run {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Run, 0, len(s.ord))
	for _, id := range s.ord {
		out = append(out, s.runs[id].view())
	}
	return out
}

// Latest is each point's newest result in a run.
func (s *Store) Latest(run string) map[string]Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]Result{}
	if r := s.runs[run]; r != nil {
		for p, rs := range r.lastResults {
			out[p] = rs[len(rs)-1]
		}
	}
	return out
}

// History is every result recorded for a point of a run, oldest first.
func (s *Store) History(run, point string) []Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r := s.runs[run]; r != nil {
		return append([]Result(nil), r.lastResults[point]...)
	}
	return nil
}

// Results is every result of a run in the order recorded.
func (s *Store) Results(run string) []Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.resultsLocked(run)
}

func (s *Store) resultsLocked(run string) []Result {
	var out []Result
	if r := s.runs[run]; r != nil {
		for _, rs := range r.lastResults {
			out = append(out, rs...)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out
}
