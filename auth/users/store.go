package users

import (
	"context"
	"errors"
	"os"
	"sync"
	"time"
)

// Store is a running controller's view of the users file: what it read
// last, re-read when the file changes (Run polls once a second, so a user
// disabled or a token revoked on the box stops working within a second).
//
// It fails closed: a file that cannot be read, cannot be parsed, or that
// others may read leaves the controller with no local users until it is
// fixed, rather than with the last good set. Each failure is told once,
// and the changes told once it is fixed are against the last good file.
type Store struct {
	path     string
	onChange func(by string, changes []Change)
	onError  func(error)

	mu    sync.RWMutex
	cur   *File // what is served: empty while the file cannot be read
	good  *File // the last file read, the baseline a change is told against
	err   error
	stamp stamp
}

// stamp is what says the file changed: its modification time, size,
// mode (a chmod that opens it to others must be seen) and identity (os.SameFile: Save renames a new file over the old one, so a
// change within the clock's resolution is still seen).
type stamp struct {
	exists bool
	mod    time.Time
	size   int64
	mode   os.FileMode
	fi     os.FileInfo
}

func stat(path string) (stamp, error) {
	st, err := os.Stat(path)
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
// refusal to start: an unreadable or malformed file, or one others may
// read.
func Open(path string) (*Store, error) {
	s := &Store{path: path}
	st, err := stat(path)
	if err != nil {
		return nil, err
	}
	f, err := s.read()
	if err != nil {
		return nil, err
	}
	s.cur, s.good, s.stamp = f, f, st
	return s, nil
}

// OnChange sets what is told each later change to the file (by whom, and
// what changed), for the journal.
func (s *Store) OnChange(fn func(by string, changes []Change)) {
	s.mu.Lock()
	s.onChange = fn
	s.mu.Unlock()
}

// OnError sets what is told each later failure to read the file.
func (s *Store) OnError(fn func(error)) {
	s.mu.Lock()
	s.onError = fn
	s.mu.Unlock()
}

func (s *Store) read() (*File, error) {
	if err := CheckMode(s.path); err != nil {
		return nil, err
	}
	return Load(s.path)
}

// Path is the users file's path.
func (s *Store) Path() string { return s.path }

// Reload re-reads the file if it changed since it was last read.
func (s *Store) Reload() {
	st, err := stat(s.path)
	s.mu.RLock()
	unchanged := err == nil && s.err == nil && st.same(s.stamp)
	s.mu.RUnlock()
	if unchanged {
		return
	}
	var f *File
	if err == nil {
		f, err = s.read()
	}
	s.mu.Lock()
	old, prevErr, onChange, onError := s.good, s.err, s.onChange, s.onError
	if err != nil {
		s.cur, s.err, s.stamp = New(), err, stamp{}
	} else {
		s.cur, s.good, s.err, s.stamp = f, f, nil, st
	}
	s.mu.Unlock()
	if err != nil {
		// Told once per failure, not once a second while it lasts.
		if onError != nil && (prevErr == nil || prevErr.Error() != err.Error()) {
			onError(err)
		}
		return
	}
	if cs := Diff(old, f); len(cs) > 0 && onChange != nil {
		onChange(f.ChangedBy, cs)
	}
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
