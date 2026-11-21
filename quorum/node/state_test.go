package node

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/joyautomation/nautilus/quorum"
)

var sample = quorum.VoterState{Promised: 41, GrantedTo: "b", Incarnation: 0xfeed, Gen: 3, Floor: 40,
	InSync: quorum.Mark{Known: true, Current: true, Term: 39, Scan: 1234}}

func TestStateRoundTripAndMissing(t *testing.T) {
	s := &State{Path: filepath.Join(t.TempDir(), "q.state")}
	if _, err := s.Load(); !errors.Is(err, ErrNoState) {
		t.Fatalf("missing file: %v, want ErrNoState", err)
	}
	if err := s.Save(sample); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load()
	if err != nil || !reflect.DeepEqual(got, sample) {
		t.Fatalf("Load = %+v, %v; want %+v", got, err, sample)
	}
	if _, err := os.Stat(s.tmp()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the temporary file was left behind: %v", err)
	}
}

// A file torn at any byte, or with any byte flipped, is refused whole: the
// controller starts uncommissioned rather than from half a state (F26).
func TestStateTornAtEveryOffset(t *testing.T) {
	good, _ := sample.MarshalBinary()
	s := &State{Path: filepath.Join(t.TempDir(), "q.state")}
	for k := range len(good) {
		if err := os.WriteFile(s.Path, good[:k], 0o600); err != nil {
			t.Fatal(err)
		}
		st, err := s.Load()
		if !errors.Is(err, quorum.ErrCorrupt) || !reflect.DeepEqual(st, quorum.VoterState{}) {
			t.Fatalf("torn at %d of %d: %+v, %v; want the zero state and ErrCorrupt", k, len(good), st, err)
		}
		flipped := append([]byte(nil), good...)
		flipped[k] ^= 0x10
		if err := os.WriteFile(s.Path, flipped, 0o600); err != nil {
			t.Fatal(err)
		}
		if st, err := s.Load(); err == nil {
			t.Fatalf("byte %d flipped: read %+v", k, st)
		}
	}
}

// A crash part-way through a write, at any byte, leaves the old state
// readable: the new one goes to a temporary file and is renamed over it
// only once it is whole and synced.
func TestStateCrashMidWriteKeepsTheOld(t *testing.T) {
	old := quorum.VoterState{Promised: 7, GrantedTo: "a", Incarnation: 9, Gen: 1, Floor: 6}
	next, _ := sample.MarshalBinary()
	crash := errors.New("power lost")
	for k := range len(next) {
		s := &State{Path: filepath.Join(t.TempDir(), "q.state")}
		if err := s.Save(old); err != nil {
			t.Fatal(err)
		}
		s.write = func(f *os.File, b []byte) (int, error) {
			n, _ := f.Write(b[:k])
			return n, crash
		}
		if err := s.Save(sample); !errors.Is(err, crash) {
			t.Fatalf("crash at %d: Save = %v", k, err)
		}
		got, err := s.Load()
		if err != nil || !reflect.DeepEqual(got, old) {
			t.Fatalf("crash at byte %d of %d: Load = %+v, %v; want the old state %+v", k, len(next), got, err, old)
		}
	}
}

func TestStateFileLockedToOneProcess(t *testing.T) {
	p := filepath.Join(t.TempDir(), "q.state.lock")
	free, err := lock(p)
	if err != nil {
		t.Fatal(err)
	}
	defer free()
	if _, err := lock(p); err == nil {
		t.Fatal("a second holder of one voter state was let in")
	}
}
