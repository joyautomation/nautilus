package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/notes"
	"github.com/joyautomation/nautilus/oplog"
)

func notesExport(t *testing.T, sd string, args ...string) (string, string, int) {
	t.Helper()
	var out, errb bytes.Buffer
	if len(args) == 0 || args[len(args)-1] != "list" {
		args = append(args, "export")
	}
	code := notesMain(append([]string{"--state-dir", sd}, args...), func(string) (*project.Manifest, string, error) {
		return nil, "", os.ErrNotExist
	}, false, func(string) string { return "" }, &out, &errb)
	return out.String(), errb.String(), code
}

func rv(id string, n int, at int64, text string) notes.Revision {
	return notes.Revision{Note: id, Rev: n, At: at, By: "alice", Principal: "user:alice", Target: notes.Target{Kind: notes.KindLog}, Text: text}
}

func TestNotesExportFromAControllerOnItsOwn(t *testing.T) {
	sd := t.TempDir()
	s, err := notes.Open(filepath.Join(sd, NotesFile))
	if err != nil {
		t.Fatal(err)
	}
	s.Apply(rv("n1", 1, 1700000000000, "pump 2 vibrating"))
	s.Apply(rv("n1", 2, 1700000100000, "pump 2 vibrating: bearing"))
	r := rv("n1", 3, 1700000200000, "pump 2 vibrating: bearing")
	r.Retract, r.Reason = true, "wrong pump"
	s.Apply(r)
	s.Apply(rv("n2", 1, 1700009000000, "=HYPERLINK(\"http://evil\")"))
	s.Close()

	out, _, code := notesExport(t, sd)
	var got []notes.Note
	if code != 0 || json.Unmarshal([]byte(out), &got) != nil || len(got) != 2 {
		t.Fatalf("json: %d %s", code, out)
	}
	if len(got[0].Revisions) != 3 || got[0].Revisions[0].Text != "pump 2 vibrating" {
		t.Errorf("history not exported whole: %+v", got[0])
	}
	// csv: one row per revision, a formula-looking text is made inert
	out, _, code = notesExport(t, sd, "--format", "csv")
	rows, err := csv.NewReader(strings.NewReader(out)).ReadAll()
	if code != 0 || err != nil || len(rows) != 5 {
		t.Fatalf("csv: %d %v rows %d\n%s", code, err, len(rows), out)
	}
	if rows[4][12] != `'=HYPERLINK("http://evil")` || rows[3][10] != "true" || rows[3][11] != "wrong pump" {
		t.Errorf("rows %v / %v", rows[3], rows[4])
	}
	// a window: only notes first written inside it
	out, _, _ = notesExport(t, sd, "--since", "2023-11-14T22:30:00Z")
	if !strings.Contains(out, "n2") || strings.Contains(out, `"id": "n1"`) {
		t.Errorf("--since: %s", out)
	}
	if _, errs, code := notesExport(t, sd, "--since", "yesterday"); code != 1 || !strings.Contains(errs, "RFC 3339") {
		t.Errorf("bad --since: %d %s", code, errs)
	}
	if _, errs, code := notesExport(t, sd, "--format", "xml"); code != 1 || !strings.Contains(errs, "json or csv") {
		t.Errorf("bad format: %d %s", code, errs)
	}
}

// A pair's notes are its log's committed entries, replayed.
func TestNotesExportFromAPairsLog(t *testing.T) {
	sd := t.TempDir()
	ent := func(seq uint64, r notes.Revision) oplog.Entry {
		e := notes.Entry(r)
		e.Seq, e.Term, e.TS = seq, 1, r.At
		return e
	}
	var lines []string
	add := func(v any) { b, _ := json.Marshal(v); lines = append(lines, string(b)) }
	add(map[string]any{"e": ent(1, rv("n1", 1, 1000, "committed"))})
	add(map[string]any{"e": oplog.Entry{Seq: 2, Term: 1, Kind: oplog.KindAck, IDs: []string{"A"}, By: "op"}})
	add(map[string]any{"c": 2})
	add(map[string]any{"e": ent(3, rv("n2", 1, 2000, "stored but never committed"))})
	os.WriteFile(filepath.Join(sd, pairLogFile), []byte(strings.Join(lines, "\n")+"\n"), 0o600)
	out, _, code := notesExport(t, sd)
	if code != 0 || !strings.Contains(out, "committed") || strings.Contains(out, "never committed") {
		t.Fatalf("%d %s", code, out)
	}
}

func TestNotesExportWithNothingWritten(t *testing.T) {
	out, errs, code := notesExport(t, t.TempDir())
	if code != 0 || strings.TrimSpace(out) != "[]" || !strings.Contains(errs, "nothing has been written") {
		t.Errorf("%d %q %q", code, out, errs)
	}
	if _, _, code := notesExport(t, t.TempDir(), "list"); code != 2 {
		t.Errorf("an unknown subcommand: %d", code)
	}
}
