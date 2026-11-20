package users

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// SeenName is the controller's record, beside the users file, of the
// credentials it last saw: what a change made while it was stopped is
// told against when it starts (Store.CatchUp). It holds no password
// hash, only a SHA-256 of each, and token ids and hashes.
const SeenName = ".users-seen.json"

// Store is a running controller's view of the users file and the two
// files beside it (the OIDC pins and the sign-outs, pins.go): what it read
// last, re-read when a file changes (Run polls once a second, so a user
// disabled, a token revoked or a sign-out on the box takes effect within
// a second).
//
// It fails closed: a file that cannot be read, cannot be parsed, that is
// not the controller's own, or that others may read leaves the controller
// with no local users, no OIDC sign-in and no browser session until it is
// fixed, rather than with the last good set (an unreadable pins file read
// as empty would let any account take any name). The failure is told once
// (OnHealth), and so is the recovery; the changes told once it is fixed
// are against the last good files.
type Store struct {
	path     string
	onChange func(by string, changes []Change)
	onHealth func(error)

	mu       sync.RWMutex
	cur      *File // what is served: empty while a file cannot be read
	good     *File // the last file read, the baseline a change is told against
	pins     *Pins // nil while a file cannot be read
	goodPins *Pins
	outs     *Signouts // nil while a file cannot be read
	goodOuts *Signouts
	err      error
	stamps   [3]stamp // the users file's, the pins file's and the signouts file's
}

// PinsPath and SignoutsPath are the side files' paths, beside the users
// file at path.
func PinsPath(path string) string     { return filepath.Join(filepath.Dir(path), PinsName) }
func SignoutsPath(path string) string { return filepath.Join(filepath.Dir(path), SignoutsName) }

func (s *Store) paths() [3]string {
	return [3]string{s.path, PinsPath(s.path), SignoutsPath(s.path)}
}

func (s *Store) statAll() ([3]stamp, error) {
	var out [3]stamp
	for i, p := range s.paths() {
		st, err := stat(p)
		if err != nil {
			return out, err
		}
		out[i] = st
	}
	return out, nil
}

// readAll reads the three files, each checked as the users file is (mode,
// owner, symlink).
func (s *Store) readAll() (*File, *Pins, *Signouts, error) {
	f, err := s.read()
	if err != nil {
		return nil, nil, nil, err
	}
	ps := s.paths()
	for _, p := range ps[1:] {
		if err := checkSide(p); err != nil {
			return nil, nil, nil, err
		}
	}
	pins, err := LoadPins(ps[1])
	if err != nil {
		return nil, nil, nil, err
	}
	outs, err := LoadSignouts(ps[2])
	if err != nil {
		return nil, nil, nil, err
	}
	return f, pins, outs, nil
}

// checkSide checks a side file as the users file is checked.
func checkSide(path string) error {
	if err := CheckMode(path); err != nil {
		return err
	}
	if st, err := os.Lstat(path); err == nil {
		return checkOwner(path, st)
	}
	return nil
}

// stamp is what says the file changed: its modification time, size,
// mode (a chmod that opens it to others must be seen) and identity
// (os.SameFile: Save renames a new file over the old one, so a change
// within the clock's resolution is still seen).
type stamp struct {
	exists bool
	mod    time.Time
	size   int64
	mode   os.FileMode
	fi     os.FileInfo
}

func stat(path string) (stamp, error) {
	st, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return stamp{}, nil
	}
	if err != nil {
		return stamp{}, err
	}
	return stamp{exists: true, mod: st.ModTime(), size: st.Size(), mode: st.Mode(), fi: st}, nil
}

func (a stamp) same(b stamp) bool {
	if a.exists != b.exists {
		return false
	}
	if !a.exists {
		return true
	}
	return a.mod.Equal(b.mod) && a.size == b.size && a.mode == b.mode && os.SameFile(a.fi, b.fi)
}

// Open reads the users file at path. Its errors are the controller's
// refusal to start: an unreadable or malformed file, one that is not the
// controller's own, one others may read, or one in a directory others may
// write to.
func Open(path string) (*Store, error) {
	s := &Store{path: path}
	st, err := s.statAll()
	if err != nil {
		return nil, err
	}
	f, pins, outs, err := s.readAll()
	if err != nil {
		return nil, err
	}
	s.cur, s.good, s.pins, s.goodPins, s.outs, s.goodOuts, s.stamps = f, f, pins, pins, outs, outs, st
	return s, nil
}

// OnChange sets what is told each later change to the file (by whom, and
// what changed), for the journal.
func (s *Store) OnChange(fn func(by string, changes []Change)) {
	s.mu.Lock()
	s.onChange = fn
	s.mu.Unlock()
}

// OnHealth sets what is told when the file stops being readable (the
// error, once per failure) and when it is readable again (nil, once).
func (s *Store) OnHealth(fn func(error)) {
	s.mu.Lock()
	s.onHealth = fn
	s.mu.Unlock()
}

func (s *Store) read() (*File, error) {
	if err := CheckMode(s.path); err != nil {
		return nil, err
	}
	if st, err := os.Lstat(s.path); err == nil {
		if err := checkOwner(s.path, st); err != nil {
			return nil, err
		}
	}
	return Load(s.path)
}

// Path is the users file's path.
func (s *Store) Path() string { return s.path }

// Reload re-reads the files if one changed since they were last read.
func (s *Store) Reload() {
	st, err := s.statAll()
	s.mu.RLock()
	unchanged := err == nil && s.err == nil && st[0].same(s.stamps[0]) && st[1].same(s.stamps[1]) && st[2].same(s.stamps[2])
	s.mu.RUnlock()
	if unchanged {
		return
	}
	var (
		f    *File
		pins *Pins
		outs *Signouts
	)
	if err == nil {
		f, pins, outs, err = s.readAll()
	}
	s.mu.Lock()
	old, oldPins, oldOuts, prevErr, onChange, onHealth := s.good, s.goodPins, s.goodOuts, s.err, s.onChange, s.onHealth
	if err != nil {
		s.cur, s.pins, s.outs, s.err, s.stamps = New(), nil, nil, err, [3]stamp{}
	} else {
		s.cur, s.good, s.pins, s.goodPins, s.outs, s.goodOuts, s.err, s.stamps = f, f, pins, pins, outs, outs, nil, st
	}
	s.mu.Unlock()
	if err != nil {
		// Told once per failure, not once a second while it lasts.
		if onHealth != nil && (prevErr == nil || prevErr.Error() != err.Error()) {
			onHealth(err)
		}
		return
	}
	if prevErr != nil && onHealth != nil {
		onHealth(nil)
	}
	changed := false
	tell := func(by string, cs []Change) {
		if len(cs) > 0 {
			changed = true
			if onChange != nil {
				onChange(by, cs)
			}
		}
	}
	tell(f.ChangedBy, Diff(old, f))
	tell(pins.ChangedBy, diffPins(oldPins, pins))
	tell(outs.ChangedBy, diffSignouts(oldOuts, outs))
	if changed {
		s.writeSeen(f, pins, outs)
	}
}

// ErrUnavailable is the error of a question the Store cannot answer
// while a file cannot be read.
var ErrUnavailable = errors.New("the users files cannot be read")

// Pin is name's pin: ok false when it has none. While a file cannot be
// read it answers ErrUnavailable: no pin is ever assumed.
func (s *Store) Pin(name string) (Pin, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.pins == nil {
		return Pin{}, false, ErrUnavailable
	}
	p, ok := s.pins.Pins[name]
	return p, ok, nil
}

// PinFirst pins name to (issuer, subject) unless it is pinned already,
// under the users lock, re-reading the pins file there (a naut users
// unpin a moment ago counts), and answers the pin name has now: the
// caller compares it with the account signing in. fresh says this call
// made it. What someone else changed since the last read is told first
// (OnChange); the pin made here is not told: the sign-in journals it.
func (s *Store) PinFirst(name, issuer, subject string, now time.Time) (pin Pin, fresh bool, err error) {
	if err := sideName(name); err != nil || issuer == "" || subject == "" {
		return Pin{}, false, fmt.Errorf("pinning %q: an invalid name, or no issuer or subject", name)
	}
	s.mu.RLock()
	failed := s.err != nil
	s.mu.RUnlock()
	if failed {
		return Pin{}, false, ErrUnavailable
	}
	path := PinsPath(s.path)
	var before, after *Pins
	err = locked(filepath.Dir(path), path, func() error {
		if err := checkSide(path); err != nil {
			return err
		}
		p, err := LoadPins(path)
		if err != nil {
			return err
		}
		before = &Pins{Version: p.Version, ChangedBy: p.ChangedBy, ChangedAt: p.ChangedAt, Pins: maps.Clone(p.Pins)}
		if have, ok := p.Pins[name]; ok {
			pin, after = have, p
			return nil
		}
		pin, fresh = Pin{Issuer: issuer, Subject: subject, Pinned: now.UTC().Truncate(time.Second)}, true
		p.Pins[name] = pin
		p.Version, p.ChangedBy, p.ChangedAt = version, "controller", now.UTC().Truncate(time.Second)
		if err := saveJSON(path, p); err != nil {
			return err
		}
		after = p
		return nil
	})
	if err != nil {
		return Pin{}, false, err
	}
	st, serr := stat(path)
	s.mu.Lock()
	old, onChange := s.goodPins, s.onChange
	if s.err == nil && serr == nil {
		// The baseline moves past this pin, so Reload does not tell it as
		// someone else's change.
		s.pins, s.goodPins, s.stamps[1] = after, after, st
	}
	f, outs := s.good, s.goodOuts
	s.mu.Unlock()
	if cs := diffPins(old, before); len(cs) > 0 {
		if onChange != nil {
			onChange(before.ChangedBy, cs)
		}
	}
	if fresh || len(diffPins(old, before)) > 0 {
		s.writeSeen(f, after, outs)
	}
	return pin, fresh, nil
}

// SignedOut is when principal ("user:<name>", "oidc:<name>") was last
// signed out by naut users signout; ok false when never. While a file
// cannot be read it answers ErrUnavailable.
func (s *Store) SignedOut(principal string) (time.Time, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.outs == nil {
		return time.Time{}, false, ErrUnavailable
	}
	at, ok := s.outs.Signouts[principal]
	return at, ok, nil
}

// Run re-reads the file once a second until ctx ends.
func (s *Store) Run(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Reload()
		}
	}
}

// Current is the users file as last read (empty while it cannot be).
func (s *Store) Current() *File {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cur
}

// Err is why the file could not be read last time, nil when it could.
func (s *Store) Err() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.err
}

// TokenUser is the user a personal access token belongs to (File.TokenUser).
func (s *Store) TokenUser(token string) (string, bool) { return s.Current().TokenUser(token) }

// seenRecord is the controller's record of what it last saw: the users
// file as seen() makes it, and the pins and sign-outs as they were.
type seenRecord struct {
	File
	Pins     map[string]Pin       `json:"pins,omitempty"`
	Signouts map[string]time.Time `json:"signouts,omitempty"`
}

// seen is the record of the credentials last seen: the file with each
// password hash replaced by its SHA-256.
func seen(f *File) *File {
	out := &File{Version: version, ChangedBy: f.ChangedBy, ChangedAt: f.ChangedAt, Users: map[string]*User{}}
	for n, u := range f.Users {
		h := sha256.Sum256([]byte(u.Password))
		out.Users[n] = &User{Password: hex.EncodeToString(h[:]), Disabled: u.Disabled, Tokens: append([]Token(nil), u.Tokens...)}
	}
	return out
}

func (s *Store) seenPath() string { return filepath.Join(filepath.Dir(s.path), SeenName) }

func (s *Store) writeSeen(f *File, pins *Pins, outs *Signouts) {
	if f == nil || pins == nil || outs == nil {
		return
	}
	rec := seenRecord{File: *seen(f), Pins: pins.Pins, Signouts: outs.Signouts}
	if b, err := json.Marshal(rec); err == nil {
		_ = writeAtomic(s.seenPath(), b)
	}
}

// CatchUp tells (OnChange, each change marked Stopped) what changed in the
// file while the controller was not running, against its record of what
// it last saw, and records what it sees now. With no record (a first
// start) every user and token is told, as found at the start.
func (s *Store) CatchUp() {
	s.mu.RLock()
	cur, pins, outs, onChange, failed := s.good, s.goodPins, s.goodOuts, s.onChange, s.err != nil
	s.mu.RUnlock()
	if failed {
		return
	}
	last := seenRecord{File: *New()}
	if b, err := os.ReadFile(s.seenPath()); err == nil {
		var rec seenRecord
		if json.Unmarshal(b, &rec) == nil && rec.Users != nil {
			last = rec
		}
	}
	tell := func(by string, cs []Change) {
		for i := range cs {
			cs[i].Stopped = true
		}
		if len(cs) > 0 && onChange != nil {
			onChange(by, cs)
		}
	}
	tell(cur.ChangedBy, Diff(&last.File, seen(cur)))
	tell(pins.ChangedBy, diffPins(&Pins{Pins: last.Pins}, pins))
	tell(outs.ChangedBy, diffSignouts(&Signouts{Signouts: last.Signouts}, outs))
	s.writeSeen(cur, pins, outs)
}
