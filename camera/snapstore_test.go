//go:build !js

package camera

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func snapStore(t *testing.T, o SnapOptions) (*SnapStore, string, *time.Time) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "snapshots")
	clock := time.Unix(1_700_000_000, 0)
	if o.Now == nil {
		o.Now = func() time.Time { return clock }
	}
	if o.Free == nil {
		o.Free = func(string) (uint64, uint64, bool) { return 0, 0, false }
	}
	st, err := OpenSnapStore(dir, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st, dir, &clock
}

func TestSnapshotsAreKeptByOccurrenceAndOnlyUnderNamesTheStoreMade(t *testing.T) {
	st, dir, clock := snapStore(t, SnapOptions{})
	ts := clock.UnixMilli()
	sn, err := st.Save(ts, "bin-top", "HiLevel", "occ-1", []byte("\xff\xd8\xff pic"))
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("the directory: %v %v, want 0700", info, err)
	}
	if info, _ := os.Stat(filepath.Join(dir, sn.File)); info == nil || info.Mode().Perm() != 0o600 {
		t.Errorf("the picture's mode: %v", info)
	}
	got := st.ForOccurrence("occ-1")
	if len(got) != 1 || got[0].File != sn.File || got[0].Camera != "bin-top" {
		t.Fatalf("the occurrence's pictures: %+v", got)
	}
	if b, err := st.Read(sn.File); err != nil || !strings.Contains(string(b), "pic") {
		t.Errorf("reading it back: %v", err)
	}
	// a name no request may use never reaches the filesystem
	for _, bad := range []string{"../etc/passwd", "index.jsonl", sn.File + "/..", "1-bin.jpg", "17000000000-a/b.jpg", "17000000000-bin-top.JPG", "17000000000-bin-top.jpg.tmp", ""} {
		if _, err := st.Read(bad); err == nil {
			t.Errorf("Read(%q) was allowed", bad)
		}
	}
	if st.ForOccurrence("") != nil || len(st.ForOccurrence("nope")) != 0 {
		t.Error("an unknown occurrence has pictures")
	}
	// a camera id that is not a file-name-safe word is refused at the door
	if _, err := st.Save(ts, "../x", "A", "o", []byte("x")); err == nil {
		t.Error("a path in a camera id was accepted")
	}
}

func TestSnapshotsSurviveARestartAndDropWhatIsGone(t *testing.T) {
	st, dir, clock := snapStore(t, SnapOptions{})
	a, _ := st.Save(clock.UnixMilli(), "c1", "A", "occ-a", []byte("aaaa"))
	b, _ := st.Save(clock.UnixMilli()+1, "c1", "B", "occ-b", []byte("bbbb"))
	st.Close()
	os.Remove(filepath.Join(dir, b.File))                                         // a picture lost
	os.WriteFile(filepath.Join(dir, "17000000001-stray.jpg"), []byte("x"), 0o600) // litter no index row knows
	os.OpenFile(filepath.Join(dir, indexName), os.O_APPEND|os.O_WRONLY, 0o600)
	f, _ := os.OpenFile(filepath.Join(dir, indexName), os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(`{"file":"170000` + "\n" + `{torn`) // a torn last line
	f.Close()
	st2, err := OpenSnapStore(dir, SnapOptions{Now: func() time.Time { return *clock }, Free: func(string) (uint64, uint64, bool) { return 0, 0, false }})
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	if len(st2.ForOccurrence("occ-a")) != 1 || len(st2.ForOccurrence("occ-b")) != 0 {
		t.Errorf("after a restart: a %d, b %d", len(st2.ForOccurrence("occ-a")), len(st2.ForOccurrence("occ-b")))
	}
	if _, err := os.Stat(filepath.Join(dir, "17000000001-stray.jpg")); err == nil {
		t.Error("a picture no index row knows was kept")
	}
	if _, err := st2.Read(a.File); err != nil {
		t.Error(err)
	}
}

func TestSnapshotRetentionByCountBytesAndAge(t *testing.T) {
	// by count
	st, dir, clock := snapStore(t, SnapOptions{MaxCount: 3})
	for i := 0; i < 5; i++ {
		if _, err := st.Save(clock.UnixMilli()+int64(i), "c", "A", "occ", []byte("p")); err != nil {
			t.Fatal(err)
		}
	}
	if st.Len() != 3 {
		t.Errorf("count bound: %d kept, want 3", st.Len())
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 4 { // three pictures and the index
		t.Errorf("%d files in the directory, want 3 pictures and the index", len(ents))
	}
	// by bytes
	st, _, clock = snapStore(t, SnapOptions{MaxBytes: 10})
	for i := 0; i < 4; i++ {
		st.Save(clock.UnixMilli()+int64(i), "c", "A", "occ", []byte("12345"))
	}
	if st.Bytes() > 10 || st.Len() != 2 {
		t.Errorf("bytes bound: %d bytes in %d pictures, want <=10 in 2", st.Bytes(), st.Len())
	}
	// by age: nothing is kept past the policy, even with nothing written
	st, _, clock = snapStore(t, SnapOptions{KeepDays: 2})
	st.Save(clock.UnixMilli(), "c", "A", "old", []byte("p"))
	*clock = clock.Add(3 * 24 * time.Hour)
	if got := st.ForOccurrence("old"); len(got) != 0 {
		t.Errorf("a picture past keep-days was served: %+v", got)
	}
	if st.Len() != 0 {
		t.Errorf("%d pictures kept past the age bound", st.Len())
	}
}

func TestSnapshotsNeverFillTheFilesystem(t *testing.T) {
	var free uint64 = 1 << 30
	total := uint64(8 << 30)
	fr := func(string) (uint64, uint64, bool) { return free, total, true }
	st, _, clock := snapStore(t, SnapOptions{MaxBytes: 500 << 20, Free: fr})
	// the bytes kept are at most 5% of the free space when it opened
	if want := int64(free / 20); st.maxBytes != want {
		t.Errorf("byte bound %d, want %d (5%% of free)", st.maxBytes, want)
	}
	if _, err := st.Save(clock.UnixMilli(), "c", "A", "o", []byte("p")); err != nil {
		t.Fatal(err)
	}
	// under the floor, writes stop with a reason
	free = 100 << 20
	if _, err := st.Save(clock.UnixMilli()+1, "c", "A", "o2", []byte("p")); !errors.Is(err, ErrLowSpace) {
		t.Errorf("a save with the disk under the floor: %v", err)
	}
	if !st.LowSpace() {
		t.Error("LowSpace is false under the floor")
	}
	free = 1 << 30
	if _, err := st.Save(clock.UnixMilli()+2, "c", "A", "o3", []byte("p")); err != nil {
		t.Errorf("a save after space came back: %v", err)
	}
}

func TestPurgeDropsPicturesAndTheirIndexRows(t *testing.T) {
	st, dir, clock := snapStore(t, SnapOptions{})
	st.Save(clock.UnixMilli(), "c", "A", "old", []byte("p"))
	*clock = clock.Add(10 * 24 * time.Hour)
	st.Save(clock.UnixMilli(), "c", "A", "new", []byte("p"))
	if n := st.Purge(7 * 24 * time.Hour); n != 1 {
		t.Errorf("purged %d older than a week, want 1", n)
	}
	if len(st.ForOccurrence("old")) != 0 || len(st.ForOccurrence("new")) != 1 {
		t.Error("the purge dropped the wrong picture")
	}
	if n := st.Purge(0); n != 1 || st.Len() != 0 {
		t.Errorf("purge all: %d, %d left", n, st.Len())
	}
	if b, _ := os.ReadFile(filepath.Join(dir, indexName)); strings.TrimSpace(string(b)) != "" {
		t.Errorf("the index still holds rows: %q", b)
	}
}

func TestAFloodSharesOnePictureAndEachAlarmFindsIt(t *testing.T) {
	st, _, clock := snapStore(t, SnapOptions{})
	ts := clock.UnixMilli()
	first, _ := st.Save(ts, "c", "A1", "occ-1", []byte("p"))
	if r, ok := st.Recent("c", ts+2000, 5000); !ok || r.File != first.File {
		t.Fatalf("recent inside the window: %v %v", r, ok)
	}
	if _, ok := st.Recent("c", ts+6000, 5000); ok {
		t.Error("a picture outside the window was shared")
	}
	if err := st.Attach(first, "A2", "occ-2"); err != nil {
		t.Fatal(err)
	}
	if len(st.ForOccurrence("occ-2")) != 1 || st.Len() != 1 {
		t.Errorf("the second alarm: %d pictures, %d files kept", len(st.ForOccurrence("occ-2")), st.Len())
	}
}

// A flood's later alarms share the first's picture, and each of their rows
// survives a restart (a deploy must not unlink all but the first occurrence).
func TestSharedPicturesKeepEveryOccurrenceAcrossARestart(t *testing.T) {
	st, dir, clock := snapStore(t, SnapOptions{})
	first, _ := st.Save(clock.UnixMilli(), "c", "A1", "occ-1", []byte("pic"))
	st.Attach(first, "A2", "occ-2")
	st.Attach(first, "A3", "occ-3")
	st.Close()
	st2, err := OpenSnapStore(dir, SnapOptions{Now: func() time.Time { return *clock }, Free: func(string) (uint64, uint64, bool) { return 0, 0, false }})
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	for _, occ := range []string{"occ-1", "occ-2", "occ-3"} {
		if len(st2.ForOccurrence(occ)) != 1 {
			t.Errorf("after a restart %s has %d pictures", occ, len(st2.ForOccurrence(occ)))
		}
	}
	if st2.Len() != 1 || st2.Bytes() != 3 {
		t.Errorf("one shared file counted as %d files, %d bytes (want 1, 3)", st2.Len(), st2.Bytes())
	}
}

// A filesystem that reports nothing free does not trim every picture there is,
// and a picture the bounds drop as it is saved is not reported as kept; a write
// that did not finish leaves no litter.
func TestTheByteBoundNeverTrimsEverythingAndASaveSaysWhenItDidNotKeep(t *testing.T) {
	st, dir, clock := snapStore(t, SnapOptions{})
	st.Save(clock.UnixMilli(), "c", "A", "occ-1", []byte("pic"))
	st.Close()
	zero := func(string) (uint64, uint64, bool) { return 0, 8 << 30, true }
	st2, err := OpenSnapStore(dir, SnapOptions{Now: func() time.Time { return *clock }, Free: zero})
	if err != nil {
		t.Fatal(err)
	}
	if len(st2.ForOccurrence("occ-1")) != 1 {
		t.Error("a filesystem reporting 0 free trimmed the pictures kept")
	}
	st2.Close()
	// larger than the byte bound: not kept, and said so
	st3, _, clock3 := snapStore(t, SnapOptions{MaxBytes: 4})
	if _, err := st3.Save(clock3.UnixMilli(), "c", "A", "occ-big", []byte("larger than four bytes")); !errors.Is(err, ErrNotKept) {
		t.Errorf("a picture over the byte bound: %v", err)
	}
	if len(st3.ForOccurrence("occ-big")) != 0 {
		t.Error("a picture dropped as it was saved is listed")
	}
	// litter from a write that did not finish is swept at open
	os.WriteFile(filepath.Join(dir, "17000000000-x.jpg.tmp"), []byte("half"), 0o600)
	st4, err := OpenSnapStore(dir, SnapOptions{Now: func() time.Time { return *clock }, Free: func(string) (uint64, uint64, bool) { return 0, 0, false }})
	if err != nil {
		t.Fatal(err)
	}
	defer st4.Close()
	if _, err := os.Stat(filepath.Join(dir, "17000000000-x.jpg.tmp")); err == nil {
		t.Error("a .tmp left by a failed write was kept")
	}
}

// One picture larger than the whole byte bound must not delete every older
// picture on its way out, and a restart with a fuller disk must not shrink the
// bound below what is already held.
func TestAnOversizedPictureDeletesNothingAndTheCapCountsWhatIsHeld(t *testing.T) {
	st, _, clock := snapStore(t, SnapOptions{MaxBytes: 20})
	for i := 0; i < 3; i++ {
		if _, err := st.Save(clock.UnixMilli()+int64(i), "c", "A", "occ", []byte("12345")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.Save(clock.UnixMilli()+10, "c", "A", "big", []byte(strings.Repeat("x", 25))); !errors.Is(err, ErrNotKept) {
		t.Errorf("a picture over the bound: %v", err)
	}
	if st.Len() != 3 || st.Bytes() != 15 {
		t.Errorf("an oversized picture left %d pictures, %d bytes (want 3, 15 untouched)", st.Len(), st.Bytes())
	}
	// 15 bytes held, 285 free: the bound is 5% of free plus held (300): 15, so all stay
	// (5% of free alone, 14, would trim one)
	dir := filepath.Join(t.TempDir(), "s")
	o := SnapOptions{MaxBytes: 1000, Now: func() time.Time { return *clock }, Free: func(string) (uint64, uint64, bool) { return 0, 0, false }}
	s1, _ := OpenSnapStore(dir, o)
	for i := 0; i < 3; i++ {
		s1.Save(clock.UnixMilli()+int64(i), "c", "A", "occ", []byte("12345"))
	}
	s1.Close()
	o.Free = func(string) (uint64, uint64, bool) { return 285, 1 << 30, true }
	s2, err := OpenSnapStore(dir, o)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if s2.Len() != 3 {
		t.Errorf("a restart with %d free trimmed what was held: %d pictures kept, want 3", 285, s2.Len())
	}
}
