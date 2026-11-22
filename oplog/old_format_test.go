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
	defer n.Close() // Windows cannot delete the temp dir under an open file
	if n.LastSeq() != 2 || applied != 2 {
		t.Fatalf("old-format entries: lastSeq %d, applied %d; want 2, 2", n.LastSeq(), applied)
	}
}

// An ack's link ID (#715) is carried through the file: a replica that
// restarts replays it, so a link spent before the restart stays spent.
func TestAckLinkSurvivesTheFile(t *testing.T) {
	f := filepath.Join(t.TempDir(), "link.jsonl")
	if err := os.WriteFile(f, []byte(`{"seq":1,"ts":1,"kind":"ack","ids":["X"],"by":"op","link":"00112233445566778899"}`+"\n"+`{"seq":2,"ts":2,"kind":"ack","ids":["Y"],"by":"op"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var links []string
	n, err := New(Options{Self: "a", File: f, Apply: func(e Entry) error { links = append(links, e.Link); return nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	if len(links) != 2 || links[0] != "00112233445566778899" || links[1] != "" {
		t.Fatalf("links replayed = %q", links)
	}
}
