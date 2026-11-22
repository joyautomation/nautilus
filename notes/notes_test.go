package notes

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/oplog"
)

func rev(id string, n int, at int64, text string) Revision {
	return Revision{Note: id, Rev: n, At: at, By: "alice", Principal: "user:alice", Target: Target{Kind: KindLog}, Text: text}
}

func TestApplyIsIdempotentAndOrdered(t *testing.T) {
	s := New()
	r1 := rev("n1", 1, 100, "pump 2 vibrating")
	if ok, err := s.Apply(r1); !ok || err != nil {
		t.Fatalf("first: %v %v", ok, err)
	}
	if ok, err := s.Apply(r1); ok || err != nil {
		t.Fatalf("the same revision again changed something: %v %v", ok, err)
	}
	// a different revision under a held number, or a gap, is refused
	if _, err := s.Apply(rev("n1", 1, 100, "something else")); !errors.Is(err, ErrRevOrder) {
		t.Errorf("a different revision 1: %v", err)
	}
	if _, err := s.Apply(rev("n1", 3, 200, "gap")); !errors.Is(err, ErrRevOrder) {
		t.Errorf("a gap: %v", err)
	}
	if _, err := s.Apply(rev("zz", 2, 200, "no note")); !errors.Is(err, ErrRevOrder) {
		t.Errorf("a revision of nothing: %v", err)
	}
	if ok, err := s.Apply(rev("n1", 2, 200, "pump 2 vibrating: bearing")); !ok || err != nil {
		t.Fatalf("rev 2: %v %v", ok, err)
	}
	n, _ := s.Get("n1")
	if len(n.Revisions) != 2 || n.First().Text != "pump 2 vibrating" || n.Latest().Text != "pump 2 vibrating: bearing" || !n.Edited() {
		t.Errorf("%+v", n)
	}
	if s.Count() != 1 {
		t.Errorf("count %d", s.Count())
	}
}

// Nothing is overwritten or deleted: a retraction is one more revision, and
// the text of the earlier ones is still there.
func TestRetractionKeepsHistory(t *testing.T) {
	s := New()
	s.Apply(rev("n1", 1, 100, "wrong plant"))
	r := rev("n1", 2, 200, "wrong plant")
	r.Retract, r.Reason = true, "wrong plant"
	s.Apply(r)
	n, _ := s.Get("n1")
	if !n.Retracted() || n.First().Text != "wrong plant" {
		t.Fatalf("%+v", n)
	}
	if got := s.List(Query{}); len(got) != 0 {
		t.Errorf("a retracted note is listed by default: %d", len(got))
	}
	if got := s.List(Query{Retracted: true}); len(got) != 1 {
		t.Errorf("with retracted: %d", len(got))
	}
	if s.CountFor(KindLog, "", "") != 0 {
		t.Error("a retracted note is counted")
	}
}

func TestFileKeepsWhatWasAnsweredAndReopens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notes.jsonl")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Apply(rev("n1", 1, 100, "first"))
	s.Apply(rev("n1", 2, 150, "first, edited"))
	s.Apply(rev("n2", 1, 200, "second"))
	s.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if s2.Count() != 2 {
		t.Fatalf("reopened with %d notes", s2.Count())
	}
	n, _ := s2.Get("n1")
	if len(n.Revisions) != 2 {
		t.Errorf("revisions %d", len(n.Revisions))
	}
	// writes after a reopen append, and are seen by the next open
	s2.Apply(rev("n3", 1, 300, "third"))
	s2.Close()
	s3, _ := Open(path)
	if s3.Count() != 3 {
		t.Errorf("after an append: %d", s3.Count())
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm()&0o077 != 0 {
		t.Errorf("notes file mode %v", fi.Mode().Perm())
	}
}

func TestATornLastLineIsDroppedDamageIsRefused(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.jsonl")
	good := `{"note":"n1","rev":1,"at":100,"by":"alice","target":{"kind":"log"},"text":"kept"}` + "\n"
	os.WriteFile(path, []byte(good+`{"note":"n2","rev":1,"at":2`), 0o600)
	s, err := Open(path)
	if err != nil || s.Count() != 1 {
		t.Fatalf("a torn last line: %v, %d notes", err, s.Count())
	}
	s.Close()
	if b, _ := os.ReadFile(path); string(b) != good {
		t.Errorf("the torn line was not cut: %q", b)
	}
	// damage before the end is an error, and the file is left as it is
	bad := good + "not json\n" + good
	os.WriteFile(path, []byte(bad), 0o600)
	if _, err := Open(path); !errors.Is(err, ErrCorrupt) {
		t.Errorf("damage: %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != bad {
		t.Error("a damaged file was changed")
	}
}

func TestQuery(t *testing.T) {
	s := New()
	add := func(id string, at int64, by string, tg Target, kind string) {
		s.Apply(Revision{Note: id, Rev: 1, At: at, By: by, Principal: "user:" + by, Target: tg, Kind: kind, Text: id})
	}
	add("a", 100, "alice", Target{Kind: KindTag, ID: "P1"}, "")
	add("b", 200, "bob", Target{Kind: KindAlarm, ID: "HiTemp", Occurrence: "00ff"}, "")
	add("c", 300, "alice", Target{Kind: KindLog}, NoteHandover)
	ids := func(q Query) string {
		var out []string
		for _, n := range s.List(q) {
			out = append(out, n.ID)
		}
		return strings.Join(out, ",")
	}
	for _, c := range []struct {
		q    Query
		want string
	}{
		{Query{}, "c,b,a"},
		{Query{TargetKind: KindTag}, "a"},
		{Query{TargetKind: KindAlarm, TargetID: "HiTemp", Occurrence: "00ff"}, "b"},
		{Query{TargetKind: KindAlarm, Occurrence: "11"}, ""},
		{Query{Author: "alice"}, "c,a"},
		{Query{Author: "user:bob"}, "b"},
		{Query{Kind: NoteHandover}, "c"},
		{Query{Since: 150, Until: 300}, "b"},
		{Query{Limit: 1}, "c"},
	} {
		if got := ids(c.q); got != c.want {
			t.Errorf("%+v: %q, want %q", c.q, got, c.want)
		}
	}
	if s.CountFor(KindAlarm, "HiTemp", "") != 1 || s.CountFor(KindAlarm, "HiTemp", "00ff") != 1 || s.CountFor(KindAlarm, "HiTemp", "zz") != 0 {
		t.Error("CountFor")
	}
}

func TestTextAndTargetChecks(t *testing.T) {
	for _, bad := range []string{"", "   ", strings.Repeat("x", MaxText+1), "a\x00b", "bell\a", "bad\xff"} {
		if CheckText(bad) == nil {
			t.Errorf("text %q accepted", bad)
		}
	}
	for _, ok := range []string{"hello", "two\nlines\tand a tab", strings.Repeat("é", MaxText)} {
		if err := CheckText(ok); err != nil {
			t.Errorf("text %q refused: %v", ok[:min(len(ok), 10)], err)
		}
	}
	for _, bad := range []Target{
		{Kind: "tank"}, {Kind: KindTag}, {Kind: KindLog, ID: "x"}, {Kind: KindTag, ID: "P1", Occurrence: "00ff"},
		{Kind: KindAlarm, ID: "A", Occurrence: "XYZ"}, {Kind: KindAsset, ID: strings.Repeat("a", MaxID+1)}, {Kind: KindAlarm, ID: "A", ActiveMs: -1},
	} {
		if CheckTarget(bad) == nil {
			t.Errorf("target %+v accepted", bad)
		}
	}
	for _, ok := range []Target{{Kind: KindLog}, {Kind: KindAsset, ID: "Pump 2"}, {Kind: KindAlarm, ID: "HiTemp", Occurrence: "00ff00ff", ActiveMs: 5}} {
		if err := CheckTarget(ok); err != nil {
			t.Errorf("target %+v refused: %v", ok, err)
		}
	}
}

func TestEntryRoundTripAndReplay(t *testing.T) {
	r := rev("n1", 1, 0, "text")
	r.Target = Target{Kind: KindAlarm, ID: "HiTemp", Occurrence: "00ff", ActiveMs: 7}
	r.Kind, r.Re = NoteHandover, "n0"
	e := Entry(r)
	e.TS, e.Principal = 500, "user:alice"
	got, ok := FromEntry(e)
	r.At = 500
	if !ok || got != r {
		t.Fatalf("%+v vs %+v", got, r)
	}
	if _, ok := FromEntry(oplog.Entry{Kind: oplog.KindAck}); ok {
		t.Error("an ack is not a note")
	}
	// Replay builds the index from the committed entries, in order, skipping the rest.
	r2 := rev("n1", 2, 0, "text, edited")
	e2 := Entry(r2)
	e2.TS = 600
	s, bad := Replay([]oplog.Entry{{Kind: oplog.KindAck, IDs: []string{"A"}}, e, e2, e})
	if bad != 0 || s.Count() != 1 {
		t.Fatalf("bad %d count %d", bad, s.Count())
	}
	n, _ := s.Get("n1")
	if len(n.Revisions) != 2 || n.Latest().At != 600 {
		t.Errorf("%+v", n)
	}
}

func TestResetEmptiesThePairIndexOnly(t *testing.T) {
	s := New()
	s.Apply(rev("n1", 1, 1, "x"))
	s.Reset()
	if s.Count() != 0 {
		t.Error("the pair's index was not emptied")
	}
	path := filepath.Join(t.TempDir(), "n.jsonl")
	f, _ := Open(path)
	f.Apply(rev("n1", 1, 1, "x"))
	f.Reset()
	if f.Count() != 1 {
		t.Error("a standalone store was reset: its file is the only copy")
	}
}

func TestNewIDIsRandom(t *testing.T) {
	a, b := NewID(), NewID()
	if a == b || len(a) != 32 {
		t.Errorf("%q %q", a, b)
	}
}
