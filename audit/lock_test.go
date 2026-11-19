//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package audit

import (
	"strings"
	"testing"
)

// One journal directory, one writer: a second OpenFile while the first is
// open is refused (two `naut run`s in one directory would interleave two
// chains), and the directory opens again once the first is closed.
func TestJournalDirectoryIsLocked(t *testing.T) {
	dir := t.TempDir()
	j, err := OpenFile(dir, FileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.Record(Entry{Action: "tag.write", Result: ResultOK}); err != nil {
		t.Fatal(err)
	}
	if j2, err := OpenFile(dir, FileOptions{}); err == nil || !strings.Contains(err.Error(), "open in another process") {
		if j2 != nil {
			j2.Close()
		}
		t.Fatalf("second OpenFile of an open journal: err = %v", err)
	}
	j.Close()
	j3, err := OpenFile(dir, FileOptions{})
	if err != nil {
		t.Fatalf("reopen after Close: %v", err)
	}
	defer j3.Close()
	if r := mustVerify(t, dir); r.Entries != 1 {
		t.Fatalf("report %+v", r)
	}
}
