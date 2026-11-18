package retain

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
)

// recFS is the real file system with every call Save makes recorded, and
// a step at which to stop, as a power cut would: the call fails and
// nothing after it runs.
type recFS struct {
	log    []string
	failAt string // e.g. "write", "sync", "rename": fail that step
}

var errCut = errors.New("power cut")

func (r *recFS) step(s string) error {
	r.log = append(r.log, s)
	if r.failAt != "" && strings.HasPrefix(s, r.failAt) {
		return errCut
	}
	return nil
}

func (r *recFS) Create(name string) (syncFile, error) {
	if err := r.step("create " + filepath.Base(name)); err != nil {
		return nil, err
	}
	f, err := osFS{}.Create(name)
	if err != nil {
		return nil, err
	}
	return &recFile{f: f, r: r, name: filepath.Base(name)}, nil
}

func (r *recFS) Rename(o, n string) error {
	if err := r.step("rename " + filepath.Base(o) + " " + filepath.Base(n)); err != nil {
		return err
	}
	return os.Rename(o, n)
}

func (r *recFS) SyncDir(dir string) error { return r.step("syncdir") }

type recFile struct {
	f    syncFile
	r    *recFS
	name string
}

func (w *recFile) Write(b []byte) (int, error) {
	if err := w.r.step("write " + w.name); err != nil {
		// A cut mid-write: half the data reached the file.
		n, _ := w.f.Write(b[:len(b)/2])
		return n, err
	}
	return w.f.Write(b)
}
func (w *recFile) Sync() error {
	if err := w.r.step("sync " + w.name); err != nil {
		return err
	}
	return w.f.Sync()
}
func (w *recFile) Close() error {
	if err := w.r.step("close " + w.name); err != nil {
		_ = w.f.Close()
		return err
	}
	return w.f.Close()
}

// Save syncs the new data before the rename and the directory after it
// (#842): otherwise a power cut can persist the rename before the data,
// and the controller boots on an empty retain.json.
func TestFileStoreSaveSyncsBeforeAndAfterTheRename(t *testing.T) {
	path := filepath.Join(t.TempDir(), "retain.json")
	rec := &recFS{}
	store := &fileStore{path: path, fs: rec}
	if err := store.Save(State{Tags: map[string]any{"SP": 42.5}}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"create retain.json.tmp",
		"write retain.json.tmp",
		"sync retain.json.tmp",
		"close retain.json.tmp",
		"rename retain.json.tmp retain.json",
		"syncdir",
	}
	if strings.Join(rec.log, "\n") != strings.Join(want, "\n") {
		t.Errorf("Save's calls, in order:\n%s\nwant:\n%s", strings.Join(rec.log, "\n"), strings.Join(want, "\n"))
	}
	got, err := store.Load()
	if err != nil || got.Tags["SP"] != 42.5 {
		t.Errorf("Load after Save: %v, %v", got.Tags, err)
	}
}

// A save cut short at any step before the rename leaves the old file
// whole: Load returns the old values, and the next save goes through.
func TestFileStoreInterruptedSaveKeepsTheOldFile(t *testing.T) {
	for _, step := range []string{"create", "write", "sync", "close", "rename"} {
		t.Run(step, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "retain.json")
			store := &fileStore{path: path}
			if err := store.Save(State{Tags: map[string]any{"SP": 1.5}}); err != nil {
				t.Fatal(err)
			}
			cut := &fileStore{path: path, fs: &recFS{failAt: step}}
			if err := cut.Save(State{Tags: map[string]any{"SP": 42.5}}); err == nil {
				t.Fatalf("a save cut at %s reported success", step)
			}
			got, err := store.Load()
			if err != nil || got.Tags["SP"] != 1.5 {
				t.Fatalf("after a cut at %s: Load = %v, %v; want the old SP 1.5", step, got.Tags, err)
			}
			if err := store.Save(State{Tags: map[string]any{"SP": 7.0}}); err != nil {
				t.Fatalf("the save after a cut at %s: %v", step, err)
			}
			if got, _ := store.Load(); got.Tags["SP"] != 7.0 {
				t.Errorf("after a cut at %s and a good save: %v", step, got.Tags)
			}
		})
	}
}

// A retain.json that cannot be parsed is moved aside before Load returns,
// and the error names where: the runtime starts from initial values and
// saves, and that save must not destroy the evidence.
func TestFileStoreLoadKeepsAnUnusableFileAside(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "retain.json")
	os.WriteFile(path, []byte("broken"), 0o644)
	store := &fileStore{path: path}
	_, err := store.Load()
	if err == nil {
		t.Fatal("Load of a corrupt file returned no error")
	}
	aside, _ := filepath.Glob(path + ".corrupt-*")
	if len(aside) != 1 {
		t.Fatalf("want one retain.json.corrupt-* file, got %q", aside)
	}
	if !strings.Contains(err.Error(), filepath.Base(aside[0])) {
		t.Errorf("the error should say where the file went: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("the corrupt file is still in Save's way")
	}
	if err := store.Save(State{Tags: map[string]any{"SP": 1.5}}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(aside[0]); string(b) != "broken" {
		t.Errorf("the kept file = %q after a save, want the corrupt original", b)
	}
}

// A directory sync that fails is logged (it is not an error: the data is
// written and renamed, only its durability across a power cut is in doubt).
func TestFileStoreSaveLogsAFailedDirSync(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("a directory cannot be synced on Windows; nothing is logged there")
	}
	var buf strings.Builder
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	store := &fileStore{path: filepath.Join(t.TempDir(), "retain.json"), fs: &recFS{failAt: "syncdir"}}
	if err := store.Save(State{Tags: map[string]any{"SP": 1.5}}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !strings.Contains(buf.String(), "could not be synced") {
		t.Errorf("a failed directory sync was not logged: %q", buf.String())
	}
}
