//go:build !js

package camera

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// The pictures kept when an alarm activates (#724 slice 2). A still is a file
// under the controller's state directory, named by the moment and the camera
// and nothing a request can choose; an append-only index says which alarm
// occurrence each picture is for, so the alarm journal's rows are not changed
// (a snapshot is attached to an occurrence by that key). Retention is by count
// and by bytes, the oldest out.

// Retention bounds.
const (
	DefaultMaxSnaps     = 500
	DefaultMaxSnapBytes = 200 << 20
	indexName           = "index.jsonl"
)

// Snap is one kept picture.
type Snap struct {
	File   string `json:"file"`
	TS     int64  `json:"ts"` // epoch ms
	Camera string `json:"camera"`
	// Occurrence is the alarm occurrence it is for; Alarm is that alarm's id.
	Occurrence string `json:"occurrence"`
	Alarm      string `json:"alarm"`
	Size       int    `json:"size"`
}

var snapName = regexp.MustCompile(`^[0-9]{10,16}-[A-Za-z0-9_-]{1,64}\.jpg$`)

// ValidSnapName reports whether name is a file this store would have made: the
// only names a request may ask for.
func ValidSnapName(name string) bool { return snapName.MatchString(name) }

// ErrLowSpace says the filesystem is too full to keep another picture: the
// pictures never take the space the retained state and the operator-intent log
// need.
var ErrLowSpace = errors.New("camera: the state filesystem is nearly full: no more pictures are kept")

// ErrNotKept says a picture was dropped as it was saved: it is larger than the
// byte bound or older than the age bound.
var ErrNotKept = errors.New("camera: the picture is outside the retention bounds (too large, or older than keep-days) and was not kept")

// SnapOptions bound the store. Zero values take the defaults.
type SnapOptions struct {
	MaxCount int
	MaxBytes int64
	KeepDays int
	// Now is the clock (a test's); nil is time.Now.
	Now func() time.Time
	// Free reports the free and total bytes of the filesystem holding dir; nil
	// asks the system. ok false: unknown, no space bound.
	Free func(dir string) (free, total uint64, ok bool)
}

// SnapStore keeps the pictures.
type SnapStore struct {
	dir       string
	maxCount  int
	maxBytes  int64
	keep      time.Duration
	now       func() time.Time
	free      func(string) (uint64, uint64, bool)
	low       bool
	mu        sync.Mutex
	snaps     []Snap // oldest first
	total     int64
	lastShot  map[string]Snap // camera -> its latest picture, for coalescing
	indexFile *os.File
}

// OpenSnapStore opens (making, mode 0700) dir and reads its index. Pictures the
// index does not know, and index rows whose file is gone, are dropped.
//
// The bytes kept are at most the configured bytes and 5% of the filesystem's free
// space when the store opens, so a small box's disk is never filled by pictures;
// writes stop (ErrLowSpace) while the free space is under a floor.
func OpenSnapStore(dir string, o SnapOptions) (*SnapStore, error) {
	maxCount, maxBytes := o.MaxCount, o.MaxBytes
	if maxCount <= 0 {
		maxCount = DefaultMaxSnaps
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxSnapBytes
	}
	keepDays := o.KeepDays
	if keepDays <= 0 {
		keepDays = DefaultKeepDays
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	now, free := o.Now, o.Free
	if now == nil {
		now = time.Now
	}
	if free == nil {
		free = diskFree
	}
	st := &SnapStore{dir: dir, maxCount: maxCount, maxBytes: maxBytes, keep: time.Duration(keepDays) * 24 * time.Hour, now: now, free: free, lastShot: map[string]Snap{}}
	if err := st.load(); err != nil {
		return nil, err
	}
	// 5% of the space the pictures could have (the free space and what they hold),
	// when that is a bound at all: a filesystem reporting nothing free is the
	// floor's to refuse writes for, not a reason to trim every picture there is,
	// and a fuller disk after a restart does not shrink what is already held by
	// the bytes it holds
	if f, _, ok := free(dir); ok {
		st.mu.Lock()
		if cap := int64((f + uint64(st.total)) / 20); cap > 0 && cap < st.maxBytes {
			st.maxBytes = cap
			st.sweepLocked()
		}
		st.mu.Unlock()
	}
	return st, nil
}

func (s *SnapStore) load() error {
	f, err := os.Open(filepath.Join(s.dir, indexName))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	known := map[string]bool{}
	sized := map[string]int{}
	if f != nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64<<10), 64<<10)
		for sc.Scan() {
			var sn Snap
			if json.Unmarshal(sc.Bytes(), &sn) != nil || !ValidSnapName(sn.File) {
				continue // a torn last line
			}
			// one row per (picture, occurrence): a flood's later alarms share the
			// first's file, and each of their rows is kept
			size, seen := sized[sn.File]
			if !seen {
				st, err := os.Stat(filepath.Join(s.dir, sn.File))
				if err != nil {
					continue
				}
				size = int(st.Size())
				sized[sn.File] = size
				s.total += int64(size)
			}
			sn.Size = size
			known[sn.File] = true
			s.snaps = append(s.snaps, sn)
		}
		f.Close()
		if err := sc.Err(); err != nil && !errors.Is(err, io.EOF) {
			return err
		}
	}
	sort.SliceStable(s.snaps, func(i, j int) bool { return s.snaps[i].TS < s.snaps[j].TS })
	for _, sn := range s.snaps {
		s.lastShot[sn.Camera] = sn
	}
	// pictures no index row knows are litter (a crash between the file and its row)
	if ents, err := os.ReadDir(s.dir); err == nil {
		for _, e := range ents {
			n := e.Name()
			if (ValidSnapName(n) && !known[n]) || strings.HasSuffix(n, ".jpg.tmp") {
				os.Remove(filepath.Join(s.dir, n)) // a picture no row knows, a write that did not finish
			}
		}
	}
	s.trimLocked()
	return s.rewriteIndexLocked()
}

// Close closes the index.
func (s *SnapStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.indexFile != nil {
		err := s.indexFile.Close()
		s.indexFile = nil
		return err
	}
	return nil
}

// Recent is the camera's latest picture no older than window ms before ts, if
// any: an alarm flood shares one picture rather than fetching a still per alarm.
func (s *SnapStore) Recent(camera string, ts, windowMs int64) (Snap, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sn, ok := s.lastShot[camera]
	if ok && ts-sn.TS < windowMs && ts >= sn.TS {
		return sn, true
	}
	return Snap{}, false
}

// Save keeps jpeg as camera's picture at ts for the alarm occurrence.
func (s *SnapStore) Save(ts int64, cam, alarm, occurrence string, jpeg []byte) (Snap, error) {
	if !idOK(cam) || ts <= 0 {
		return Snap{}, errors.New("camera: a picture needs a camera id and a time")
	}
	name := fmt.Sprintf("%d-%s.jpg", ts, cam)
	if !ValidSnapName(name) {
		return Snap{}, errors.New("camera: not a name this store makes")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lowSpaceLocked() {
		return Snap{}, ErrLowSpace
	}
	// a picture larger than the whole byte bound is refused before anything is
	// written or trimmed: keeping it would mean deleting every older picture and
	// then itself
	if int64(len(jpeg)) > s.maxBytes {
		return Snap{}, ErrNotKept
	}
	path := filepath.Join(s.dir, name)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, jpeg, 0o600); err != nil {
		os.Remove(tmp) // a full disk leaves half a file
		return Snap{}, err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return Snap{}, err
	}
	sn := Snap{File: name, TS: ts, Camera: cam, Occurrence: occurrence, Alarm: alarm, Size: len(jpeg)}
	if err := s.appendIndexLocked(sn); err != nil {
		os.Remove(path)
		return Snap{}, err
	}
	s.snaps = append(s.snaps, sn)
	s.total += int64(sn.Size)
	s.lastShot[cam] = sn
	if s.trimLocked() {
		_ = s.rewriteIndexLocked()
	}
	// a picture the bounds would drop at once (larger than the byte bound, older
	// than keep-days) was not kept: say so rather than journal a success
	kept := false
	for _, k := range s.snaps {
		if k.File == name {
			kept = true
			break
		}
	}
	if !kept {
		return Snap{}, ErrNotKept
	}
	return sn, nil
}

// lowSpaceLocked reports whether the filesystem is under the floor: less than 5%
// of it, or 256 MB, free.
func (s *SnapStore) lowSpaceLocked() bool {
	f, t, ok := s.free(s.dir)
	if !ok {
		s.low = false
		return false
	}
	floor := t / 20
	if floor < 256<<20 {
		floor = 256 << 20
	}
	s.low = f < floor
	return s.low
}

// LowSpace says whether the last write was refused for want of space.
func (s *SnapStore) LowSpace() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lowSpaceLocked()
}

// Purge drops every picture older than olderThan (all of them when it is zero)
// and returns how many pictures went.
func (s *SnapStore) Purge(olderThan time.Duration) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	cut := s.now().UnixMilli() - olderThan.Milliseconds()
	files := s.uniqueLocked()
	n := 0
	drop := map[string]bool{}
	for _, sn := range files {
		if olderThan == 0 || sn.TS < cut {
			drop[sn.File] = true
			os.Remove(filepath.Join(s.dir, sn.File))
			if l, ok := s.lastShot[sn.Camera]; ok && l.File == sn.File {
				delete(s.lastShot, sn.Camera)
			}
			s.total -= int64(sn.Size)
			n++
		}
	}
	if n == 0 {
		return 0
	}
	kept := s.snaps[:0]
	for _, sn := range s.snaps {
		if !drop[sn.File] {
			kept = append(kept, sn)
		}
	}
	s.snaps = kept
	_ = s.rewriteIndexLocked()
	return n
}

// Attach records that an existing picture is also for another occurrence (a
// flood's later alarms sharing the first's picture).
func (s *SnapStore) Attach(sn Snap, alarm, occurrence string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range s.snaps {
		if k.File == sn.File {
			a := k
			a.Alarm, a.Occurrence = alarm, occurrence
			if err := s.appendIndexLocked(a); err != nil {
				return err
			}
			s.snaps = append(s.snaps, a)
			return nil
		}
	}
	return errors.New("camera: that picture is gone")
}

// ForOccurrence lists the pictures kept for an alarm occurrence, oldest first.
func (s *SnapStore) ForOccurrence(occurrence string) []Snap {
	if occurrence == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()
	var out []Snap
	seen := map[string]bool{}
	for _, sn := range s.snaps {
		if sn.Occurrence == occurrence && !seen[sn.File] {
			seen[sn.File] = true
			out = append(out, sn)
		}
	}
	return out
}

// Read returns a kept picture by file name; only names this store made.
func (s *SnapStore) Read(name string) ([]byte, error) {
	if !ValidSnapName(name) {
		return nil, os.ErrNotExist
	}
	s.mu.Lock()
	s.sweepLocked()
	ok := false
	for _, sn := range s.snaps {
		if sn.File == name {
			ok = true
			break
		}
	}
	s.mu.Unlock()
	if !ok {
		return nil, os.ErrNotExist
	}
	return os.ReadFile(filepath.Join(s.dir, name))
}

// sweepLocked drops what the policy no longer keeps (the age bound passes while
// nothing is written).
func (s *SnapStore) sweepLocked() {
	if s.trimLocked() {
		_ = s.rewriteIndexLocked()
	}
}

// OccurrencesOf lists the alarm occurrences a kept picture is attached to (a
// flood's later alarms share the first's file), for the journal's record of who
// looked at it. Empty when it is not kept.
func (s *SnapStore) OccurrencesOf(name string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, sn := range s.snaps {
		if sn.File == name {
			out = append(out, sn.Occurrence)
		}
	}
	return out
}

// Len and Bytes are what is kept.
func (s *SnapStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.uniqueLocked())
}

func (s *SnapStore) Bytes() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.total
}

// uniqueLocked is the distinct files kept (an index row per occurrence, a file
// per picture).
func (s *SnapStore) uniqueLocked() map[string]Snap {
	m := map[string]Snap{}
	for _, sn := range s.snaps {
		m[sn.File] = sn
	}
	return m
}

// trimLocked drops the oldest pictures (their files and every index row for
// them) until both bounds hold, and reports whether any went.
func (s *SnapStore) trimLocked() bool {
	files := s.uniqueLocked()
	order := make([]Snap, 0, len(files))
	for _, sn := range files {
		order = append(order, sn)
	}
	sort.Slice(order, func(i, j int) bool { return order[i].TS < order[j].TS })
	var total int64
	for _, sn := range order {
		total += int64(sn.Size)
	}
	s.total = total
	dropped := map[string]bool{}
	// by age first: pictures of people are not kept past the policy
	cut := s.now().Add(-s.keep).UnixMilli()
	for _, sn := range order {
		if sn.TS < cut {
			dropped[sn.File] = true
			s.total -= int64(sn.Size)
			os.Remove(filepath.Join(s.dir, sn.File))
			if l, ok := s.lastShot[sn.Camera]; ok && l.File == sn.File {
				delete(s.lastShot, sn.Camera)
			}
		}
	}
	for len(order)-len(dropped) > s.maxCount || s.total > s.maxBytes {
		var victim Snap
		found := false
		for _, sn := range order {
			if !dropped[sn.File] {
				victim, found = sn, true
				break
			}
		}
		if !found {
			break
		}
		dropped[victim.File] = true
		s.total -= int64(victim.Size)
		os.Remove(filepath.Join(s.dir, victim.File))
		if l, ok := s.lastShot[victim.Camera]; ok && l.File == victim.File {
			delete(s.lastShot, victim.Camera)
		}
	}
	if len(dropped) == 0 {
		return false
	}
	kept := s.snaps[:0]
	for _, sn := range s.snaps {
		if !dropped[sn.File] {
			kept = append(kept, sn)
		}
	}
	s.snaps = kept
	return true
}

func (s *SnapStore) appendIndexLocked(sn Snap) error {
	if s.indexFile == nil {
		f, err := os.OpenFile(filepath.Join(s.dir, indexName), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		s.indexFile = f
	}
	b, _ := json.Marshal(sn)
	_, err := s.indexFile.Write(append(b, '\n'))
	return err
}

// rewriteIndexLocked writes the index afresh from what is kept (after a trim, so
// the file does not grow for ever with rows for pictures that are gone).
func (s *SnapStore) rewriteIndexLocked() error {
	if s.indexFile != nil {
		s.indexFile.Close()
		s.indexFile = nil
	}
	path := filepath.Join(s.dir, indexName)
	tmp := path + ".tmp"
	var b strings.Builder
	for _, sn := range s.snaps {
		j, _ := json.Marshal(sn)
		b.Write(j)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
