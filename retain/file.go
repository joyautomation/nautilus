package retain

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	goruntime "runtime"
	"time"
)

// fileStore retains State in a JSON file. Save writes a sibling ".tmp"
// file, syncs it to disk, renames it over the target and syncs the
// directory (#842). A crash or power loss at any point leaves either the
// old file or the whole new one: never an empty or half-written file,
// which Load would report as corrupt and the runtime would replace with
// the tags' initial values. Without the syncs the rename is atomic only
// in the file system's memory, and a power cut can still persist the
// rename before the data it points at.
type fileStore struct {
	path string
	fs   fsys // nil: the real file system
}

// fsys is the file system calls Save makes, so a test can see their order
// and stop part-way, as a power cut would.
type fsys interface {
	Create(name string) (syncFile, error)
	Rename(oldpath, newpath string) error
	// SyncDir makes a rename in dir durable. Best effort: a directory
	// cannot be synced on every platform (Windows), and its journal covers
	// the rename there; Save logs a failure anywhere else.
	SyncDir(dir string) error
}

type syncFile interface {
	io.Writer
	Sync() error
	Close() error
}

type osFS struct{}

func (osFS) Create(name string) (syncFile, error) {
	return os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
}
func (osFS) Rename(oldpath, newpath string) error { return os.Rename(oldpath, newpath) }
func (osFS) SyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func (f *fileStore) Kind() string { return "file" }

// Load reads State from disk. A missing file means nothing has been
// retained yet, not a failure, so it returns a zero State and a nil error.
// A file that cannot be read or parsed is moved aside first, to
// <path>.corrupt-<UTC time>, and the error says where: the runtime then
// starts from the tags' initial values, and its first save must not
// destroy the evidence. A ".tmp" left by an interrupted Save is never read.
func (f *fileStore) Load() (State, error) {
	b, err := os.ReadFile(f.path)
	if os.IsNotExist(err) {
		return State{}, nil
	}
	if err == nil {
		var s State
		if err = json.Unmarshal(b, &s); err == nil {
			return s, nil
		}
	}
	return State{}, f.keepAside(err)
}

// keepAside renames the unusable file out of Save's way and says where it
// went in the error it returns.
func (f *fileStore) keepAside(cause error) error {
	aside := f.path + ".corrupt-" + time.Now().UTC().Format("20060102T150405.000Z")
	if err := os.Rename(f.path, aside); err != nil {
		return fmt.Errorf("%s: %w (and it could not be kept aside: %v)", f.path, cause, err)
	}
	return fmt.Errorf("%s: %w; kept as %s", f.path, cause, aside)
}

func (f *fileStore) Save(s State) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	fs := f.fs
	if fs == nil {
		fs = osFS{}
	}
	tmp := f.path + ".tmp"
	w, err := fs.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := w.Write(b); err != nil {
		_ = w.Close()
		return err
	}
	// The data is on disk before the rename can point the name at it.
	if err := w.Sync(); err != nil {
		_ = w.Close()
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	if err := fs.Rename(tmp, f.path); err != nil {
		return err
	}
	// And the rename itself is on disk before Save reports success. Best
	// effort: Windows cannot sync a directory; anywhere else a failure is
	// logged, since the rename may not survive a power cut.
	if err := fs.SyncDir(filepath.Dir(f.path)); err != nil && goruntime.GOOS != "windows" {
		slog.Warn("retain: the directory could not be synced after a save; a power cut may undo it",
			"path", f.path, "error", err)
	}
	return nil
}
