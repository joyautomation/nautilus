package node

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/joyautomation/nautilus/quorum"
)

// ErrNoState: the state file does not exist (a first start, or a wiped
// disk). Load returns the zero state with it.
var ErrNoState = errors.New("quorum: no state file")

// State is a voter's state file. Save writes it whole to a temporary file
// in the same directory, fsyncs it, renames it over the old one and fsyncs
// the directory, so a crash at any instant leaves either the old state or
// the new one on disk, never a torn file. Load refuses any damage whole
// (quorum.ErrCorrupt): the caller treats it as no state.
type State struct {
	Path string
	// Owner and Cluster are stamped on every write (VoterState.Owner,
	// .Cluster), and CheckOwner compares a loaded file with them.
	Owner   quorum.NodeID
	Cluster string
	// Box is stamped on every write too (VoterState.Box, box.go).
	Box string

	// write writes b to f; tests replace it to crash part-way.
	write func(f *os.File, b []byte) (int, error)
}

func (s *State) tmp() string { return s.Path + ".tmp" }

// Load reads the state. A missing file is ErrNoState; an unreadable one
// wraps quorum.ErrCorrupt or quorum.ErrVersion, or the read error. Either
// way the state returned is the zero VoterState: nothing is half-read.
func (s *State) Load() (quorum.VoterState, error) {
	b, err := os.ReadFile(s.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return quorum.VoterState{}, ErrNoState
	}
	if err != nil {
		return quorum.VoterState{}, fmt.Errorf("read %s: %w", s.Path, err)
	}
	var st quorum.VoterState
	if err := st.UnmarshalBinary(b); err != nil {
		return quorum.VoterState{}, fmt.Errorf("%s: %w", s.Path, err)
	}
	return st, nil
}

// Save makes st the durable state. It returns only once st is on disk
// (both the file's data and the rename), or an error, when the old state
// still stands.
func (s *State) Save(st quorum.VoterState) error {
	st.Owner, st.Cluster, st.Box = s.Owner, s.Cluster, s.Box
	b, err := st.MarshalBinary()
	if err != nil {
		return err
	}
	if mut("state_in_place") {
		return s.writeFile(s.Path, b)
	}
	return writeAtomic(s.Path, b, s.write)
}

// writeAtomic makes b the content of path: written to a temporary file
// beside it, fsynced, renamed over it, and the directory fsynced. write,
// when not nil, replaces the write (tests).
func writeAtomic(path string, b []byte, write func(*os.File, []byte) (int, error)) error {
	tmp := path + ".tmp"
	if err := writeFile(tmp, b, write); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

func (s *State) writeFile(path string, b []byte) error { return writeFile(path, b, s.write) }

// writeFile writes b to path from its start, truncating it, and fsyncs it.
func writeFile(path string, b []byte, write func(*os.File, []byte) (int, error)) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if write == nil {
		write = (*os.File).Write
	}
	if _, err := write(f, b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// ErrNotOwner: the state file was written by another member, or for
// another pair. Starting over it would vote under a name, or for a pair,
// its promises were never made as.
var ErrNotOwner = errors.New("quorum: the state file belongs to another member or pair")

// CheckOwner refuses st when a write stamped it with another owner or
// cluster than s's. A file never written (no owner) passes.
func (s *State) CheckOwner(st quorum.VoterState) error {
	if mut("state_owner_unchecked") || (st.Owner == "" && st.Cluster == "") {
		return nil
	}
	if st.Owner != s.Owner || st.Cluster != s.Cluster {
		return fmt.Errorf("%w: %s was written by %q for cluster %s, and this is %q for cluster %s. "+
			"NAUTILUS_NODE differs per box, and each box keeps its own state directory; a state file copied from another box must not be used",
			ErrNotOwner, s.Path, st.Owner, st.Cluster, s.Owner, s.Cluster)
	}
	return nil
}
