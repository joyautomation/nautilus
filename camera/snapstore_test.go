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
