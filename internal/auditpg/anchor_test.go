package auditpg

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/audit"
)

// asRole is dsn with its user swapped for role (password "t"), skipping
// when the DSN is not a URL.
func asRole(t *testing.T, dsn, role string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil || u.Scheme != "postgres" && u.Scheme != "postgresql" {
		t.Skip("NAUTILUS_TEST_DATABASE_URL is not a postgres:// URL; the role tests swap its user")
	}
	u.User = url.UserPassword(role, "t")
	return u.String()
}

// roles creates the named login roles fresh, and drops them after.
func roles(t *testing.T, db *sql.DB, names ...string) {
	t.Helper()
	drop := func() {
		for _, n := range names {
			db.Exec(`DROP OWNED BY ` + quoteIdent(n))
			db.Exec(`DROP ROLE IF EXISTS ` + quoteIdent(n))
		}
	}
	drop()
	for _, n := range names {
		if _, err := db.Exec(`CREATE ROLE ` + quoteIdent(n) + ` LOGIN PASSWORD 't'`); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		db.Exec(`DROP TABLE IF EXISTS audit_entries, audit_gaps, audit_anchors`)
		drop()
	})
}

func protection(t *testing.T, s *Store, role string) audit.WriterCheck {
	t.Helper()
	c, err := s.Protection(context.Background(), role)
	if err != nil {
		t.Fatalf("Protection(%s): %v", role, err)
	}
	return c
}

// pg-schema's grants give a role the copy and the anchors, and nothing
// that could rewrite them: the role check calls it protected, the sink
// runs and anchors under it, its anchors carry it as writer with the
// database's time, and it can neither change a row nor set a column that
// is evidence. Every way round that is caught by the check.
func TestPostgresAnchorRoleCheck(t *testing.T) {
	dsn := testDSN(t)
	db := fresh(t, dsn)
	asRole(t, dsn, "x")
	roles(t, db, "naut_9b_sink", "naut_9b_wide", "naut_9b_update", "naut_9b_super", "naut_9b_creat", "naut_9b_owner")
	ctx := context.Background()
	// The administrator runs pg-schema's statements, twice (it is safe to
	// run again).
	for i := 0; i < 2; i++ {
		if _, err := db.Exec(Schema("naut_9b_sink")); err != nil {
			t.Fatalf("pg-schema's SQL: %v", err)
		}
	}
	admin := open(t, dsn, "plant-a")
	if c := protection(t, admin, "postgres"); c.Protected || !strings.Contains(c.Why, "superuser") {
		t.Errorf("the superuser: %+v", c)
	}
	if c := protection(t, admin, "naut_9b_sink"); !c.Protected {
		t.Fatalf("pg-schema's role: %+v, want protected", c)
	}

	// The sink runs and anchors under that role.
	dir, j := journal(t)
	rec(t, j, audit.Entry{Target: "A"})
	rec(t, j, audit.Entry{Target: "B"})
	sink := open(t, asRole(t, dsn, "naut_9b_sink"), "plant-a")
	tl := audit.NewTailer(dir, sink, j.Head)
	tl.AnchorTo(sink, time.Hour)
	before := time.Now().Add(-time.Minute)
	stepOK(t, tl)
	if st := tl.Status(); st.Anchored != 2 || st.AnchorError != "" {
		t.Fatalf("status %+v, want anchored at seq 2", st)
	}
	anchors, err := admin.Anchors(ctx)
	if err != nil || len(anchors) != 1 {
		t.Fatalf("anchors %+v %v", anchors, err)
	}
	if a := anchors[0]; a.Writer != "naut_9b_sink" || a.Reason != audit.AnchorStart || a.Seq != 2 || a.At.Before(before) {
		t.Errorf("anchor %+v", a)
	}
	if role, _ := sink.Role(ctx); role != "naut_9b_sink" {
		t.Errorf("Role %q", role)
	}
	// It cannot rewrite or forge.
	sdb, _ := sql.Open("postgres", asRole(t, dsn, "naut_9b_sink"))
	defer sdb.Close()
	for _, q := range []string{
		`UPDATE audit_entries SET line = 'x'`,
		`DELETE FROM audit_anchors`,
		`TRUNCATE audit_entries`,
		`INSERT INTO audit_anchors (controller, seq, hash, reason, writer) VALUES ('plant-a', 9, 'h', 'r', 'someone')`,
		`INSERT INTO audit_anchors (controller, seq, hash, reason, at) VALUES ('plant-a', 9, 'h', 'r', now() - interval '1 day')`,
		`INSERT INTO audit_entries (controller, seq, id, ts, "user", source, action, result, hash, file, file_offset, line, copied_at)
		 VALUES ('plant-a', 9, 'i', now(), 'u', 's', 'a', 'ok', 'h', 'f', 1, 'l', now() - interval '1 day')`,
	} {
		if _, err := sdb.Exec(q); err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Errorf("%s: %v, want permission denied", q, err)
		}
	}

	// Every way round is caught.
	for _, q := range []string{
		`GRANT INSERT, SELECT ON audit_entries, audit_gaps, audit_anchors TO naut_9b_wide`, // 9a's advice: the whole row
		`GRANT SELECT, UPDATE ON audit_entries TO naut_9b_update`,
		`GRANT postgres TO naut_9b_super`,
		`ALTER ROLE naut_9b_creat CREATEROLE`,
		`ALTER TABLE audit_gaps OWNER TO naut_9b_owner`,
		`GRANT naut_9b_owner TO naut_9b_sink`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	for role, why := range map[string]string{
		"naut_9b_wide":   "may INSERT audit_anchors.writer",
		"naut_9b_update": "holds UPDATE on audit_entries",
		"naut_9b_super":  "can SET ROLE postgres, which is a superuser",
		"naut_9b_creat":  "it holds CREATEROLE",
		"naut_9b_owner":  "it owns audit_gaps",
		"naut_9b_sink":   "can SET ROLE naut_9b_owner, which owns audit_gaps",
		"naut_9b_gone":   "no longer exists",
	} {
		if c := protection(t, admin, role); c.Protected || !strings.Contains(c.Why, why) {
			t.Errorf("%s: %+v, want not protected because %q", role, c, why)
		}
	}
	if c := protection(t, admin, "naut_9b_wide"); !strings.Contains(c.Why, "audit_entries.copied_at") || !strings.Contains(c.Why, "audit_anchors.at") {
		t.Errorf("the wide grant: %+v, want every evidence column named", c)
	}
}

// A copy made before 9b (no copied_at) gains the column when the sink owns
// the table; pg-schema's SQL adds it too, and narrows 9a's whole-row
// grant to the columns that are not evidence.
func TestPostgresAnchorUpgradesA9aCopy(t *testing.T) {
	dsn := testDSN(t)
	db := fresh(t, dsn)
	asRole(t, dsn, "x")
	roles(t, db, "naut_9b_up")
	old := strings.Replace(entriesDDL, "  copied_at timestamptz NOT NULL DEFAULT now(),\n", "", 1)
	if old == entriesDDL {
		t.Fatal("the 9a shape was not made")
	}
	if _, err := db.Exec(old); err != nil {
		t.Fatal(err)
	}
	s := open(t, dsn, "plant-a")
	if err := s.ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	var has bool
	db.QueryRow(`SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'audit_entries' AND column_name = 'copied_at')`).Scan(&has)
	if !has {
		t.Error("copied_at not added")
	}
	// 9a told the administrator to grant INSERT and SELECT on the tables.
	if _, err := db.Exec(`GRANT INSERT, SELECT ON audit_entries, audit_gaps, audit_anchors TO naut_9b_up`); err != nil {
		t.Fatal(err)
	}
	if c := protection(t, s, "naut_9b_up"); c.Protected {
		t.Fatal("9a's grant reads as protected")
	}
	if _, err := db.Exec(Schema("naut_9b_up")); err != nil {
		t.Fatalf("pg-schema's SQL over a 9a copy: %v", err)
	}
	if c := protection(t, s, "naut_9b_up"); !c.Protected {
		t.Errorf("after pg-schema: %+v, want the grant narrowed", c)
	}
}

// verify --anchor's reads, against a real database: the copy's rows with
// the database's copied_at, the anchors with their at and writer. An
// honest journal verifies (UNPROTECTED as the superuser writes the
// anchors); a prune stamped ahead of the database's clock is BROKEN.
func TestPostgresVerifyAnchored(t *testing.T) {
	dsn := testDSN(t)
	fresh(t, dsn)
	ctx := context.Background()
	dir := t.TempDir()
	j, err := audit.OpenFile(dir, audit.FileOptions{Keep: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if _, err := j.RecordRetention("op"); err != nil {
		t.Fatal(err)
	}
	rec(t, j, audit.Entry{Target: "A"})
	s := open(t, dsn, "plant-a")
	tl := audit.NewTailer(dir, s, j.Head)
	tl.AnchorTo(s, time.Hour)
	stepOK(t, tl)

	run := func() audit.AnchorReport {
		t.Helper()
		anchors, err := s.Anchors(ctx)
		if err != nil {
			t.Fatal(err)
		}
		gaps, err := s.Gaps(ctx)
		if err != nil {
			t.Fatal(err)
		}
		writers := map[string]audit.WriterCheck{}
		for _, a := range anchors {
			writers[a.Writer] = protection(t, s, a.Writer)
		}
		next, done, err := s.Rows(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer done()
		r, err := audit.VerifyAnchored(audit.AnchorInput{Dir: dir, Copy: next, Gaps: gaps, Anchors: anchors, Writers: writers})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	if r := run(); r.Verdict != audit.Unprotected || r.Compared != 2 || len(r.Problems) != 0 {
		t.Fatalf("honest: %s compared %d problems %v", r.Verdict, r.Compared, r.Problems)
	}
	if k, ok, err := s.AnchoredKeep(ctx); err != nil || !ok || k != 86400 {
		t.Errorf("AnchoredKeep %d %v %v, want 1d", k, ok, err)
	}
	if c, _ := s.Controllers(ctx); len(c) != 1 || c[0] != "plant-a" {
		t.Errorf("Controllers %v", c)
	}

	// A forger prunes today's file by a prune stamped ten days ahead, so
	// the file reads old enough: the database's clock says otherwise.
	files, _ := filepath.Glob(filepath.Join(dir, "audit-*.jsonl"))
	name := filepath.Base(files[0])
	data, _ := os.ReadFile(files[0])
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	last := lines[len(lines)-1]
	if _, err := j.Record(audit.Entry{TS: time.Now().Add(10 * 24 * time.Hour), User: "system", Source: audit.SourceSystem, Action: audit.ActionPrune,
		Target: name, Result: audit.ResultOK, Detail: map[string]any{"last": audit.LineHash([]byte(last)), "seq": float64(len(lines)), "keep": float64(86400)}}); err != nil {
		t.Fatal(err)
	}
	stepOK(t, tl)
	r := run()
	if r.Verdict != audit.Broken || !strings.Contains(strings.Join(r.Problems, "\n"), "its time was set ahead") {
		t.Errorf("a prune stamped ahead: %s %v", r.Verdict, r.Problems)
	}
}
