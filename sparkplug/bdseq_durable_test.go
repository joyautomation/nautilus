package sparkplug

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

// saveBdSeq syncs the new value before renaming it into place, and the
// directory after (#842). Seen through the seams: when the file is synced
// the bdSeq file still holds the old value (the rename has not happened),
// and when the directory is synced it holds the new one.
func TestBdSeqSaveSyncsBeforeAndAfterTheRename(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bdseq")
	if err := os.WriteFile(path, []byte("5"), 0o644); err != nil {
		t.Fatal(err)
	}
	read := func() string { b, _ := os.ReadFile(path); return string(b) }
	var seen []string
	oldSync, oldDir := fsyncFile, syncDir
	t.Cleanup(func() { fsyncFile, syncDir = oldSync, oldDir })
	fsyncFile = func(f *os.File) error {
		seen = append(seen, "sync "+filepath.Base(f.Name())+" while bdseq="+read())
		return f.Sync()
	}
	syncDir = func(dir string) { seen = append(seen, "syncdir while bdseq="+read()) }

	n := &Node{cfg: Config{BdSeqFile: path}, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	n.saveBdSeq(6)

	want := []string{"sync bdseq.tmp while bdseq=5", "syncdir while bdseq=6"}
	if len(seen) != len(want) || seen[0] != want[0] || seen[1] != want[1] {
		t.Errorf("saveBdSeq's syncs: %q, want %q", seen, want)
	}
	if read() != "6" {
		t.Errorf("bdseq = %q after saving 6", read())
	}
}
