package auditpg

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/audit"
)

// The tests here run against a real Postgres and skip without one:
//
//	docker run --rm -p 5432:5432 -e POSTGRES_PASSWORD=postgres postgres:16
//	NAUTILUS_TEST_DATABASE_URL="postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable" \
//	  go test ./internal/auditpg/ -run Postgres -v
//
// CI's postgres job runs them. The tailer's own logic (what is copied,
// when it stops) is tested without a database in audit/tail_test.go.

func testDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("NAUTILUS_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set NAUTILUS_TEST_DATABASE_URL to run this test against a real Postgres")
	}
	return dsn
}

// fresh drops the copy's tables, so each test starts as a new database
// does and proves the sink creates what is missing.
func fresh(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`DROP TABLE IF EXISTS audit_entries, audit_gaps`); err != nil {
		t.Fatal(err)
	}
	return db
}

func journal(t *testing.T) (string, *audit.FileJournal) {
	t.Helper()
	dir := t.TempDir()
	j, err := audit.OpenFile(dir, audit.FileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { j.Close() })
	return dir, j
}

func rec(t *testing.T, j *audit.FileJournal, e audit.Entry) {
	t.Helper()
	if e.User == "" {
		e.User = "user:op1"
	}
	if e.Source == "" {
		e.Source = audit.SourceHMI
	}
	if e.Action == "" {
		e.Action = "tag.write"
	}
	if e.Result == "" {
		e.Result = audit.ResultOK
	}
	if _, err := j.Record(e); err != nil {
		t.Fatal(err)
	}
}

func open(t *testing.T, dsn, controller string) *Store {
	t.Helper()
	s, err := Open(dsn, controller)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func stepOK(t *testing.T, tl *audit.Tailer) int {
	t.Helper()
	n, err := tl.Step(context.Background())
	if err != nil {
		t.Fatalf("Step: %v", err)
	}
	return n
}

// journalLines is every line of the journal, oldest first.
func journalLines(t *testing.T, dir string) []string {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(dir, "audit-*.jsonl"))
	var out []string
	for _, f := range files {
		data, _ := os.ReadFile(f)
		out = append(out, strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")...)
	}
	return out
}

// The copy holds every line byte for byte (the line column is what the
// chain hashes), its fields are queryable, a NUL in a value does not stop
// it, and a restarted sink goes on from the newest row.
func TestPostgresAuditCopy(t *testing.T) {
	dsn := testDSN(t)
	db := fresh(t, dsn)
	dir, j := journal(t)
	rec(t, j, audit.Entry{Target: "Line1.SpeedSP", Before: 42.0, After: 55.0, Detail: map[string]any{"area": "receiving"}, From: "192.0.2.22"})
	rec(t, j, audit.Entry{Target: "Line1.Note", After: "a\x00b", Detail: map[string]any{"k\x00": []any{"x\x00"}}})
	// A NUL in a text field too: the target and the user come from the
	// request.
	rec(t, j, audit.Entry{Target: "bad\x00name", User: "user:op\x00", From: "192.0.2.\x00", Result: audit.ResultRefused})
	rec(t, j, audit.Entry{Action: "force.set", Target: "Line1.Run", Result: audit.ResultRefused, Detail: map[string]any{"reason": "elevation required"}})

	tl := audit.NewTailer(dir, open(t, dsn, "plant-a"), j.Head)
	if n := stepOK(t, tl); n != 4 {
		t.Fatalf("copied %d, want 4", n)
	}
	rows, err := db.Query(`SELECT seq, line, hash FROM audit_entries WHERE controller = 'plant-a' ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var seq int64
		var line, hash string
		rows.Scan(&seq, &line, &hash)
		if hash != audit.LineHash([]byte(line)) {
			t.Errorf("seq %d: hash %s is not the line's", seq, hash)
		}
		got = append(got, line)
	}
	rows.Close()
	if strings.Join(got, "\n") != strings.Join(journalLines(t, dir), "\n") {
		t.Fatalf("the copy's lines are not the journal's:\n%s", strings.Join(got, "\n"))
	}
	var area, result string
	if err := db.QueryRow(`SELECT detail->>'area', result FROM audit_entries WHERE controller = 'plant-a' AND seq = 1`).Scan(&area, &result); err != nil || area != "receiving" || result != "ok" {
		t.Errorf("seq 1 detail.area %q result %q (%v)", area, result, err)
	}
	var note string
	db.QueryRow(`SELECT after#>>'{}' FROM audit_entries WHERE controller = 'plant-a' AND seq = 2`).Scan(&note)
	if note != "a\uFFFDb" {
		t.Errorf("seq 2 after %q, want the NUL replaced in the jsonb column", note)
	}
	var target, user string
	db.QueryRow(`SELECT target, "user" FROM audit_entries WHERE controller = 'plant-a' AND seq = 3`).Scan(&target, &user)
	if target != "bad\uFFFDname" || user != "user:op\uFFFD" {
		t.Errorf("seq 3 target %q user %q, want the NULs replaced", target, user)
	}

	// Restart: a new store and tailer go on from the newest row.
	rec(t, j, audit.Entry{Target: "Line1.SpeedSP", After: 60.0})
	tl2 := audit.NewTailer(dir, open(t, dsn, "plant-a"), j.Head)
	if n := stepOK(t, tl2); n != 1 {
		t.Fatalf("after a restart copied %d, want 1", n)
	}
	var count, max int64
	db.QueryRow(`SELECT count(*), max(seq) FROM audit_entries WHERE controller = 'plant-a'`).Scan(&count, &max)
	if count != 5 || max != 5 {
		t.Errorf("%d rows up to seq %d, want 5 and 5", count, max)
	}
}

// Two controllers share the database under their own names: each resumes
// from its own rows.
func TestPostgresAuditCopyKeysByController(t *testing.T) {
	dsn := testDSN(t)
	fresh(t, dsn)
	dirA, a := journal(t)
	dirB, b := journal(t)
	rec(t, a, audit.Entry{Target: "A"})
	rec(t, a, audit.Entry{Target: "A"})
	rec(t, b, audit.Entry{Target: "B"})
	stepOK(t, audit.NewTailer(dirA, open(t, dsn, "plant-a"), a.Head))
	stepOK(t, audit.NewTailer(dirB, open(t, dsn, "plant-b"), b.Head))
	last, ok, err := open(t, dsn, "plant-b").Last(context.Background())
	if err != nil || !ok || last.Seq != 1 {
		t.Errorf("plant-b's newest row: %+v %v %v, want seq 1", last, ok, err)
	}
}

// A row the copy already holds is skipped when it is the same line, and is
// a mismatch when it is another (two controllers under one name).
func TestPostgresAuditCopyConflict(t *testing.T) {
	dsn := testDSN(t)
	fresh(t, dsn)
	dir, j := journal(t)
	rec(t, j, audit.Entry{Target: "A"})
	s := open(t, dsn, "plant-a")
	stepOK(t, audit.NewTailer(dir, s, j.Head))
	line := journalLines(t, dir)[0]
	same := audit.Line{File: "audit-x.jsonl", Offset: int64(len(line) + 1), Seq: 1, Hash: audit.LineHash([]byte(line)), Raw: []byte(line),
		Entry: audit.Entry{ID: "x", TS: time.Now(), User: "u", Source: "hmi", Action: "tag.write", Result: "ok"}}
	if err := s.Apply(context.Background(), audit.CopyBatch{Lines: []audit.Line{same}}); err != nil {
		t.Errorf("re-applying the same line: %v", err)
	}
	other := same
	other.Raw = []byte(`{"other":true}`)
	other.Hash = audit.LineHash(other.Raw)
	err := s.Apply(context.Background(), audit.CopyBatch{Lines: []audit.Line{other}})
	var mm *audit.CopyMismatch
	if !errors.As(err, &mm) {
		t.Errorf("another line as seq 1: %v, want a mismatch", err)
	}

	// Another journal under the same name: the tailer stops at resume.
	dir2, j2 := journal(t)
	rec(t, j2, audit.Entry{Target: "Other"})
	rec(t, j2, audit.Entry{Target: "Other"})
	_, err = audit.NewTailer(dir2, open(t, dsn, "plant-a"), j2.Head).Step(context.Background())
	if !errors.As(err, &mm) {
		t.Errorf("a second journal under one name: %v, want a mismatch", err)
	}
}

// A gap (files pruned before the copy read them) is a row of its own,
// recorded with the batch that follows it.
func TestPostgresAuditCopyRecordsGaps(t *testing.T) {
	dsn := testDSN(t)
	db := fresh(t, dsn)
	s := open(t, dsn, "plant-a")
	l := func(seq uint64) audit.Line {
		raw := []byte(`{"seq":` + string(rune('0'+seq)) + `}`)
		return audit.Line{File: "audit-20261120.jsonl", Offset: int64(seq) * 10, Seq: seq, Hash: audit.LineHash(raw), Raw: raw,
			Entry: audit.Entry{ID: "x", TS: time.Now(), User: "u", Source: "hmi", Action: "tag.write", Result: "ok"}}
	}
	ctx := context.Background()
	if err := s.Apply(ctx, audit.CopyBatch{Lines: []audit.Line{l(1)}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Apply(ctx, audit.CopyBatch{Gap: &audit.Gap{AfterSeq: 1, NextSeq: 4, AfterFile: "audit-20261118.jsonl"}, Lines: []audit.Line{l(4)}}); err != nil {
		t.Fatal(err)
	}
	var after, next int64
	var file string
	if err := db.QueryRow(`SELECT after_seq, next_seq, after_file FROM audit_gaps WHERE controller = 'plant-a'`).Scan(&after, &next, &file); err != nil {
		t.Fatal(err)
	}
	if after != 1 || next != 4 || file != "audit-20261118.jsonl" {
		t.Errorf("gap %d..%d in %s", after, next, file)
	}
}

// A role with INSERT and SELECT only on tables an administrator created
// runs the sink: it creates nothing, updates nothing, deletes nothing.
func TestPostgresAuditCopyRunsWithInsertOnly(t *testing.T) {
	dsn := testDSN(t)
	db := fresh(t, dsn)
	u, err := url.Parse(dsn)
	if err != nil || u.Scheme != "postgres" && u.Scheme != "postgresql" {
		t.Skip("NAUTILUS_TEST_DATABASE_URL is not a postgres:// URL; the role test needs to swap its user")
	}
	// The administrator creates the tables (as the sink would) and grants
	// the controller's role INSERT and SELECT only.
	admin := open(t, dsn, "admin")
	if err := admin.ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`DROP ROLE IF EXISTS naut_audit_sink_test`,
		`CREATE ROLE naut_audit_sink_test LOGIN PASSWORD 'sinktest'`,
		`GRANT INSERT, SELECT ON audit_entries, audit_gaps TO naut_audit_sink_test`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	t.Cleanup(func() {
		db.Exec(`DROP TABLE IF EXISTS audit_entries, audit_gaps`)
		db.Exec(`DROP ROLE IF EXISTS naut_audit_sink_test`)
	})
	u.User = url.UserPassword("naut_audit_sink_test", "sinktest")
	dir, j := journal(t)
	rec(t, j, audit.Entry{Target: "A"})
	rec(t, j, audit.Entry{Target: "B"})
	tl := audit.NewTailer(dir, open(t, u.String(), "plant-a"), j.Head)
	if n := stepOK(t, tl); n != 2 {
		t.Fatalf("copied %d, want 2", n)
	}
	rec(t, j, audit.Entry{Target: "C"})
	stepOK(t, audit.NewTailer(dir, open(t, u.String(), "plant-a"), j.Head))
	var count int64
	db.QueryRow(`SELECT count(*) FROM audit_entries WHERE controller = 'plant-a'`).Scan(&count)
	if count != 3 {
		t.Errorf("%d rows, want 3", count)
	}
}
