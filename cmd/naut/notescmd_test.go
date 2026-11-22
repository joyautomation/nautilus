package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// Every free-text column is quoted, not only the text: a reason or an
// asset id that starts with = would otherwise run in a spreadsheet.
func TestNotesCSVQuotesEveryFreeTextColumn(t *testing.T) {
	sd := t.TempDir()
	s, _ := notes.Open(filepath.Join(sd, NotesFile))
	r := rv("n1", 1, 1700000000000, "text")
	r.Target = notes.Target{Kind: notes.KindAsset, ID: "=cmd|' /C calc'!A0"}
	r.By = "+evil"
	s.Apply(r)
	r2 := rv("n1", 2, 1700000100000, "text")
	r2.Target, r2.Retract, r2.Reason = r.Target, true, "@SUM(1+1)"
	r2.By = "-evil"
	s.Apply(r2)
	s.Close()
	out, _, _ := notesExport(t, sd, "--format", "csv")
	rows, _ := csv.NewReader(strings.NewReader(out)).ReadAll()
	for _, row := range rows[1:] {
		for i, cell := range row {
			if cell != "" && strings.ContainsRune("=+-@", rune(cell[0])) {
				t.Errorf("column %s starts with %q: %q", rows[0][i], cell[0], cell)
			}
		}
	}
	if rows[1][6] != "'=cmd|' /C calc'!A0" || rows[2][11] != "'@SUM(1+1)" || rows[2][3] != "'-evil" {
		t.Errorf("%v / %v", rows[1], rows[2])
	}
}

// With a file and a pair's log both in the directory (a controller moved
// from alone to a pair), the one written last is read, and it is said.
func TestNotesExportReadsTheOneWrittenLast(t *testing.T) {
	sd := t.TempDir()
	s, _ := notes.Open(filepath.Join(sd, NotesFile))
	s.Apply(rv("old", 1, 1000, "from before the pair"))
	s.Close()
	e := notes.Entry(rv("new", 1, 2000, "from the pair"))
	e.Seq, e.Term, e.TS = 1, 1, 2000
	b, _ := json.Marshal(map[string]any{"e": e})
	c, _ := json.Marshal(map[string]any{"c": 1})
	logf := filepath.Join(sd, pairLogFile)
	os.WriteFile(logf, []byte(string(b)+"\n"+string(c)+"\n"), 0o600)
	future := time.Now().Add(time.Hour)
	os.Chtimes(logf, future, future)
	out, errs, code := notesExport(t, sd)
	if code != 0 || !strings.Contains(out, "from the pair") || strings.Contains(out, "from before the pair") || !strings.Contains(errs, "both") {
		t.Errorf("%d %s / %s", code, out, errs)
	}
}

// Exporting a file a running controller holds changes nothing in it (a torn
// last line is not cut, nothing is created or rewritten).
func TestNotesExportIsReadOnly(t *testing.T) {
	sd := t.TempDir()
	path := filepath.Join(sd, NotesFile)
	body := `{"note":"n1","rev":1,"at":100,"by":"alice","target":{"kind":"log"},"text":"kept"}` + "\n" + `{"note":"n2","rev":1,"at":2`
	os.WriteFile(path, []byte(body), 0o600)
	before, _ := os.Stat(path)
	out, _, code := notesExport(t, sd)
	after, _ := os.Stat(path)
	if b, _ := os.ReadFile(path); code != 0 || string(b) != body || !after.ModTime().Equal(before.ModTime()) || !strings.Contains(out, "kept") {
		t.Errorf("code %d, file %q", code, b)
	}
}

// A pair whose log was replaced keeps the old one aside: its notes are in
// the export, flagged, and a note the live history also holds is not
// repeated.
func TestNotesExportIncludesTheSupersededHistoryFlagged(t *testing.T) {
	sd := t.TempDir()
	write := func(path string, rs ...notes.Revision) {
		var lines []string
		for i, r := range rs {
			e := notes.Entry(r)
			e.Seq, e.Term, e.TS = uint64(i+1), 1, r.At
			b, _ := json.Marshal(map[string]any{"e": e})
			lines = append(lines, string(b))
		}
		c, _ := json.Marshal(map[string]any{"c": len(rs)})
		os.WriteFile(path, []byte(strings.Join(append(lines, string(c)), "\n")+"\n"), 0o600)
	}
	write(filepath.Join(sd, pairLogFile), rv("both", 1, 3000, "in both"), rv("live", 1, 4000, "live only"))
	oldFile := filepath.Join(sd, pairLogFile+".superseded-1700000000")
	write(oldFile, rv("both", 1, 3000, "in both"), rv("old", 1, 1000, "handover from the old history"))
	// a newer file by name that the mark does NOT name: the mark's is the history the server shows
	write(filepath.Join(sd, pairLogFile+".superseded-1800000000"), rv("other", 1, 500, "from a different history"))
	mark, _ := json.Marshal(map[string]any{"identity": "c/a/box1", "superseded": oldFile, "historyBy": "akhan"})
	os.WriteFile(filepath.Join(sd, pairLogMark), mark, 0o600)
	out, _, code := notesExport(t, sd)
	var got []struct {
		ID         string `json:"id"`
		Superseded bool   `json:"superseded"`
		MovedAside string `json:"movedAside"`
		AcceptedBy string `json:"acceptedBy"`
	}
	if code != 0 || json.Unmarshal([]byte(out), &got) != nil || len(got) != 3 {
		t.Fatalf("%d %s", code, out)
	}
	for _, n := range got {
		switch n.ID {
		case "old":
			if !n.Superseded || n.MovedAside != "2023-11-14T22:13:20Z" || n.AcceptedBy != "akhan" {
				t.Errorf("the old note: %+v", n)
			}
		case "both", "live":
			if n.Superseded {
				t.Errorf("%s is flagged superseded", n.ID)
			}
		default:
			t.Errorf("unexpected %s", n.ID)
		}
	}
	csvOut, _, _ := notesExport(t, sd, "--format", "csv")
	rows, _ := csv.NewReader(strings.NewReader(csvOut)).ReadAll()
	var hist []string
	for _, r := range rows[1:] {
		hist = append(hist, r[0]+"="+r[13])
	}
	if !strings.Contains(strings.Join(hist, ","), "old=superseded 2023-11-14T22:13:20Z (accepted by akhan)") || !strings.Contains(strings.Join(hist, ","), "live=live") {
		t.Errorf("%v", hist)
	}
}

// The guide writes the flags after the subcommand (`naut notes export
// --format csv --since ...`): they parse there as they do before it, and a
// stray word is still a usage error (#720 demo: it printed usage and exited 2).
func TestNotesExportFlagsAfterTheSubcommand(t *testing.T) {
	sd := t.TempDir()
	s, _ := notes.Open(filepath.Join(sd, NotesFile))
	s.Apply(rv("n1", 1, 1700000000000, "pump 2 vibrating"))
	s.Close()
	run := func(args ...string) (string, int) {
		var out, errb bytes.Buffer
		code := notesMain(args, func(string) (*project.Manifest, string, error) { return nil, "", os.ErrNotExist },
			false, func(string) string { return "" }, &out, &errb)
		return out.String(), code
	}
	out, code := run("export", "--format", "csv", "--state-dir", sd)
	rows, err := csv.NewReader(strings.NewReader(out)).ReadAll()
	if code != 0 || err != nil || len(rows) != 2 {
		t.Fatalf("flags after export: exit %d, %v, rows %d\n%s", code, err, len(rows), out)
	}
	if out, code := run("--state-dir", sd, "export", "--since", "2030-01-01"); code != 0 || strings.TrimSpace(out) != "[]" && strings.TrimSpace(out) != "null" {
		t.Errorf("--since after export did not filter: exit %d %q", code, out)
	}
	if _, code := run("export", "--state-dir", sd, "stray"); code != 2 {
		t.Errorf("a stray word after the flags: exit %d, want 2", code)
	}
}
