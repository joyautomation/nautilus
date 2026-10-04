package oplog

import (
	"os"
	"path/filepath"
	"testing"
)

// A log written by the previous version (one bare Entry per line) must
// load after the upgrade. Fails at cb38c5b: every line parses as an empty
// fileLine and is silently skipped.
func TestOldFormatFile(t *testing.T) {
	f := filepath.Join(t.TempDir(), "old.jsonl")
	if err := os.WriteFile(f, []byte(`{"seq":1,"ts":1,"kind":"ack","ids":["X"],"by":"op"}`+"\n"+`{"seq":2,"ts":2,"kind":"shelve","id":"Y","until":9,"by":"op"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	applied := 0
	n, err := New(Options{Self: "a", File: f, Apply: func(Entry) error { applied++; return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if n.LastSeq() != 2 || applied != 2 {
		t.Fatalf("old-format entries: lastSeq %d, applied %d; want 2, 2", n.LastSeq(), applied)
	}
}
