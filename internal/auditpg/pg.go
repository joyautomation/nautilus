// Package auditpg keeps a copy of the runtime audit journal in Postgres
// (#709 PR 9, docs/design/identity-audit.md §4): `audit.sink: postgres`.
//
// It is a store, nothing more (and the anchors' role check). audit.Tailer reads the journal's files,
// checks their chain and decides what is copied; this package keeps the
// rows and says which is the newest. The files stay the source of truth.
//
// The copy is written with INSERT and read with SELECT only: no row is
// ever updated or deleted, and the position the tailer resumes from is the
// newest row (its file and byte offset), not a cursor row that would need
// UPDATE. So a role that holds only INSERT and SELECT on these tables can
// run the sink, and cannot rewrite what it already copied.
//
// Only the CLI imports it (docs/design/context-map.md §2).
package auditpg

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/joyautomation/nautilus/audit"
	_ "github.com/lib/pq" // the postgres driver
)

// Tables are created only when they are missing, so a database
// administrator can create them under another role (granting the sink's
// role INSERT and SELECT only) and the sink, lacking CREATE, still starts.
const entriesDDL = `
CREATE TABLE audit_entries (
  controller text NOT NULL,
  seq bigint NOT NULL,
  id text NOT NULL,
  ts timestamptz NOT NULL,
  "user" text NOT NULL,
  source text NOT NULL,
  action text NOT NULL,
  target text,
  result text NOT NULL,
  before jsonb,
  after jsonb,
  detail jsonb,
  "from" text,
  session text,
  prev text,
  hash text NOT NULL,
  file text NOT NULL,
  file_offset bigint NOT NULL,
  line text NOT NULL,
  copied_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (controller, seq));
CREATE INDEX audit_entries_ts ON audit_entries (controller, ts);`

// The anchor (#709 PR 9b): the copy's newest line, written every
// audit.anchor-every, at once after a copied audit.prune, and at a clean
// stop. at and writer are the database's: the sink's role is never
// granted those columns, so it cannot set them (nor copied_at above).
const anchorsDDL = `
CREATE TABLE audit_anchors (
  controller text NOT NULL,
  seq bigint NOT NULL,
  hash text NOT NULL,
  reason text NOT NULL,
  at timestamptz NOT NULL DEFAULT now(),
  writer text NOT NULL DEFAULT current_user);
CREATE INDEX audit_anchors_seq ON audit_anchors (controller, seq);`

// The columns the sink's role may INSERT: everything but the defaults
// that are evidence (copied_at, noticed, at, writer).
var insertColumns = map[string][]string{
	"audit_entries": {"controller", "seq", "id", "ts", `"user"`, "source", "action", "target", "result", "before", "after", "detail", `"from"`, "session", "prev", "hash", "file", "file_offset", "line"},
	"audit_gaps":    {"controller", "after_seq", "next_seq", "after_file"},
	"audit_anchors": {"controller", "seq", "hash", "reason"},
}

// evidenceColumns are the columns whose database default is evidence: a
// role that can INSERT into one can forge it.
var evidenceColumns = [][2]string{{"audit_entries", "copied_at"}, {"audit_anchors", "at"}, {"audit_anchors", "writer"}}

// Tables are the copy's tables, in creation order.
var Tables = []string{"audit_entries", "audit_gaps", "audit_anchors"}

const gapsDDL = `
CREATE TABLE audit_gaps (
  controller text NOT NULL,
  after_seq bigint NOT NULL,
  next_seq bigint NOT NULL,
  after_file text NOT NULL,
  noticed timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (controller, next_seq));`

// Store is the Postgres copy for one controller.
type Store struct {
	db         *sql.DB
	controller string

	mu     sync.Mutex
	schema bool // tables found or created
}

// Open returns a store over dsn for the controller named controller (the
// key that lets several controllers share one database). It does not
// connect: the first Last or Apply does, so a database that is down at
// start delays the copy, never the controller.
func Open(dsn, controller string) (*Store, error) {
	if controller == "" {
		return nil, errors.New("auditpg: a controller name is required")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}
	// A light writer beside the historian: two connections are plenty.
	db.SetMaxOpenConns(2)
	return &Store{db: db, controller: controller}, nil
}

// Close closes the database handle.
func (s *Store) Close() error { return s.db.Close() }

// ensure creates whichever table is missing, once per store.
func (s *Store) ensure(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.schema {
		return nil
	}
	for _, t := range []struct{ name, ddl string }{{"audit_entries", entriesDDL}, {"audit_gaps", gapsDDL}, {"audit_anchors", anchorsDDL}} {
		var reg sql.NullString
		if err := s.db.QueryRowContext(ctx, `SELECT to_regclass($1)::text`, t.name).Scan(&reg); err != nil {
			return err
		}
		if reg.Valid {
			continue
		}
		if _, err := s.db.ExecContext(ctx, t.ddl); err != nil {
			return fmt.Errorf("auditpg: create %s: %w (a database administrator creates the tables: naut audit pg-schema --role <this role> prints the statements)", t.name, err)
		}
	}
	// A copy made before 9b has no copied_at: add it (as the owner), or
	// say what to run.
	var has bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.columns
		WHERE table_name = 'audit_entries' AND column_name = 'copied_at' AND table_schema = current_schema())`).Scan(&has); err != nil {
		return err
	}
	if !has {
		if _, err := s.db.ExecContext(ctx, addCopiedAt); err != nil {
			return fmt.Errorf("auditpg: audit_entries has no copied_at column: %w (a database administrator runs the statements naut audit pg-schema --role <this role> prints)", err)
		}
	}
	s.schema = true
	return nil
}

const addCopiedAt = `ALTER TABLE audit_entries ADD COLUMN IF NOT EXISTS copied_at timestamptz NOT NULL DEFAULT now()`

// Schema is the SQL a database administrator runs, as the role that will
// own the tables, so that role (the controller's) can run the sink and
// cannot rewrite the copy or the anchors: SELECT, and INSERT only on the
// columns that are not evidence. It is safe to run again (an upgrade from
// a copy made before 9b adds copied_at and narrows the grants).
func Schema(role string) string {
	r := quoteIdent(role)
	var b strings.Builder
	b.WriteString("-- The audit journal's copy and anchors (naut audit pg-schema). Run as the\n")
	b.WriteString("-- role that owns them, never as " + r + ".\n")
	for _, ddl := range []string{entriesDDL, gapsDDL, anchorsDDL} {
		ddl = strings.ReplaceAll(ddl, "CREATE TABLE ", "CREATE TABLE IF NOT EXISTS ")
		ddl = strings.ReplaceAll(ddl, "CREATE INDEX ", "CREATE INDEX IF NOT EXISTS ")
		b.WriteString(strings.TrimSpace(ddl) + "\n")
	}
	b.WriteString(addCopiedAt + ";\n")
	b.WriteString("REVOKE ALL ON " + strings.Join(Tables, ", ") + " FROM " + r + ";\n")
	b.WriteString("GRANT SELECT ON " + strings.Join(Tables, ", ") + " TO " + r + ";\n")
	for _, t := range Tables {
		b.WriteString("GRANT INSERT (" + strings.Join(insertColumns[t], ", ") + ") ON " + t + " TO " + r + ";\n")
	}
	return b.String()
}

func quoteIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

// Protection checks whether role could rewrite the copy or the anchors:
// it, or any role it is a member of (it can SET ROLE to any of them), is a
// superuser, holds CREATEROLE, owns one of the tables, holds UPDATE,
// DELETE or TRUNCATE on one, or may INSERT into a column whose default is
// the evidence (copied_at, an anchor's at or writer). A role that no
// longer exists cannot be checked, so it is not protected.
func (s *Store) Protection(ctx context.Context, role string) (audit.WriterCheck, error) {
	if err := s.ensure(ctx); err != nil {
		return audit.WriterCheck{}, err
	}
	var exists bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, role).Scan(&exists); err != nil {
		return audit.WriterCheck{}, err
	}
	if !exists {
		return audit.WriterCheck{Why: "the role no longer exists, so what it could do cannot be checked"}, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT rolname, rolsuper, rolcreaterole FROM pg_roles WHERE pg_has_role($1, oid, 'MEMBER')`, role)
	if err != nil {
		return audit.WriterCheck{}, err
	}
	type member struct {
		name         string
		super, creat bool
	}
	var members []member
	for rows.Next() {
		var m member
		if err := rows.Scan(&m.name, &m.super, &m.creat); err != nil {
			rows.Close()
			return audit.WriterCheck{}, err
		}
		members = append(members, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return audit.WriterCheck{}, err
	}
	var why []string
	as := func(m member) string {
		if m.name == role {
			return "it"
		}
		return fmt.Sprintf("it can SET ROLE %s, which", m.name)
	}
	for _, m := range members {
		if m.super && m.name == role {
			// A superuser can do anything; the rest would only repeat it.
			return audit.WriterCheck{Why: "it is a superuser"}, nil
		}
		if m.super {
			why = append(why, as(m)+" is a superuser")
			continue
		}
		if m.creat {
			why = append(why, as(m)+" holds CREATEROLE")
		}
		for _, t := range Tables {
			var owner bool
			var privs []string
			if err := s.db.QueryRowContext(ctx, `SELECT c.relowner = r.oid FROM pg_class c, pg_roles r WHERE c.oid = to_regclass($1) AND r.rolname = $2`, t, m.name).Scan(&owner); err != nil {
				return audit.WriterCheck{}, fmt.Errorf("auditpg: %s: %w", t, err)
			}
			if owner {
				why = append(why, as(m)+" owns "+t)
				continue
			}
			for _, p := range []string{"UPDATE", "DELETE", "TRUNCATE"} {
				var has bool
				if err := s.db.QueryRowContext(ctx, `SELECT has_table_privilege($1, $2, $3)`, m.name, t, p).Scan(&has); err != nil {
					return audit.WriterCheck{}, err
				}
				if has {
					privs = append(privs, p)
				}
			}
			if len(privs) > 0 {
				why = append(why, as(m)+" holds "+strings.Join(privs, ", ")+" on "+t)
			}
		}
		for _, c := range evidenceColumns {
			var has bool
			if err := s.db.QueryRowContext(ctx, `SELECT has_column_privilege($1, $2, $3, 'INSERT')`, m.name, c[0], c[1]).Scan(&has); err != nil {
				return audit.WriterCheck{}, fmt.Errorf("auditpg: %s.%s: %w", c[0], c[1], err)
			}
			if has {
				why = append(why, as(m)+" may INSERT "+c[0]+"."+c[1])
			}
		}
	}
	if len(why) > 0 {
		return audit.WriterCheck{Why: strings.Join(why, "; ")}, nil
	}
	return audit.WriterCheck{Protected: true}, nil
}

// Role is the database role the store connects as.
func (s *Store) Role(ctx context.Context) (string, error) {
	var r string
	err := s.db.QueryRowContext(ctx, `SELECT current_user`).Scan(&r)
	return r, err
}

// Anchor writes the copy's newest row as an anchor, in one statement, so
// an anchor never runs ahead of the copy. It returns the anchored Seq, 0
// when the copy holds nothing yet.
func (s *Store) Anchor(ctx context.Context, reason string) (uint64, error) {
	if err := s.ensure(ctx); err != nil {
		return 0, err
	}
	var seq int64
	err := s.db.QueryRowContext(ctx, `INSERT INTO audit_anchors (controller, seq, hash, reason)
		SELECT controller, seq, hash, $2 FROM audit_entries WHERE controller = $1 ORDER BY seq DESC LIMIT 1
		RETURNING seq`, s.controller, reason).Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("auditpg: anchor: %w", err)
	}
	return uint64(seq), nil
}

// AnchoredKeep is the retention (seconds, -1 forever) the newest anchor
// covers: the keep of the newest audit.retention row at or before it.
func (s *Store) AnchoredKeep(ctx context.Context) (keep int64, ok bool, err error) {
	if err := s.ensure(ctx); err != nil {
		return 0, false, err
	}
	var k sql.NullInt64
	err = s.db.QueryRowContext(ctx, `SELECT (detail->>'keep')::bigint FROM audit_entries
		WHERE controller = $1 AND action = 'audit.retention'
		  AND seq <= (SELECT max(seq) FROM audit_anchors WHERE controller = $1)
		ORDER BY seq DESC LIMIT 1`, s.controller).Scan(&k)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return k.Int64, k.Valid, nil
}

// Controllers is every controller the anchors name.
func (s *Store) Controllers(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT controller FROM audit_anchors ORDER BY 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Anchors is every anchor for the controller.
func (s *Store) Anchors(ctx context.Context) ([]audit.Anchor, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT seq, hash, at, reason, writer FROM audit_anchors WHERE controller = $1 ORDER BY at, seq`, s.controller)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []audit.Anchor
	for rows.Next() {
		var a audit.Anchor
		var seq int64
		if err := rows.Scan(&seq, &a.Hash, &a.At, &a.Reason, &a.Writer); err != nil {
			return nil, err
		}
		a.Seq = uint64(seq)
		out = append(out, a)
	}
	return out, rows.Err()
}

// Gaps is every gap recorded for the controller.
func (s *Store) Gaps(ctx context.Context) ([]audit.Gap, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT after_seq, next_seq, after_file FROM audit_gaps WHERE controller = $1 ORDER BY next_seq`, s.controller)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []audit.Gap
	for rows.Next() {
		var g audit.Gap
		var a, n int64
		if err := rows.Scan(&a, &n, &g.AfterFile); err != nil {
			return nil, err
		}
		g.AfterSeq, g.NextSeq = uint64(a), uint64(n)
		out = append(out, g)
	}
	return out, rows.Err()
}

// Rows streams the controller's copy in Seq order: next returns one row at
// a time, ok false at the end; call done when finished.
func (s *Store) Rows(ctx context.Context) (next func() (audit.CopyRow, bool, error), done func(), err error) {
	rows, err := s.db.QueryContext(ctx, `SELECT seq, hash, file, copied_at, line FROM audit_entries WHERE controller = $1 ORDER BY seq`, s.controller)
	if err != nil {
		return nil, nil, err
	}
	next = func() (audit.CopyRow, bool, error) {
		if !rows.Next() {
			return audit.CopyRow{}, false, rows.Err()
		}
		var r audit.CopyRow
		var seq int64
		var line string
		if err := rows.Scan(&seq, &r.Hash, &r.File, &r.CopiedAt, &line); err != nil {
			return r, false, err
		}
		r.Seq, r.Line = uint64(seq), []byte(line)
		return r, true, nil
	}
	return next, func() { rows.Close() }, nil
}

// Last is the newest line the copy holds for this controller.
func (s *Store) Last(ctx context.Context) (audit.Line, bool, error) {
	if err := s.ensure(ctx); err != nil {
		return audit.Line{}, false, err
	}
	var l audit.Line
	var seq int64
	err := s.db.QueryRowContext(ctx,
		`SELECT seq, hash, file, file_offset FROM audit_entries WHERE controller = $1 ORDER BY seq DESC LIMIT 1`,
		s.controller).Scan(&seq, &l.Hash, &l.File, &l.Offset)
	if errors.Is(err, sql.ErrNoRows) {
		return audit.Line{}, false, nil
	}
	if err != nil {
		return audit.Line{}, false, err
	}
	l.Seq = uint64(seq)
	return l, true, nil
}

const insertEntry = `INSERT INTO audit_entries
  (controller, seq, id, ts, "user", source, action, target, result, before, after, detail, "from", session, prev, hash, file, file_offset, line)
  VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)
  ON CONFLICT (controller, seq) DO NOTHING`

// Apply adds the batch in one transaction. A row the copy already holds
// with the same hash is skipped; with another hash it is a mismatch: two
// controllers writing under one name, or a journal rewritten under a copy
// that holds the original.
func (s *Store) Apply(ctx context.Context, b audit.CopyBatch) error {
	if err := s.ensure(ctx); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if g := b.Gap; g != nil {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO audit_gaps (controller, after_seq, next_seq, after_file) VALUES ($1,$2,$3,$4) ON CONFLICT DO NOTHING`,
			s.controller, int64(g.AfterSeq), int64(g.NextSeq), g.AfterFile); err != nil {
			return err
		}
	}
	ins, err := tx.PrepareContext(ctx, insertEntry)
	if err != nil {
		return err
	}
	defer ins.Close()
	for _, l := range b.Lines {
		e := l.Entry
		res, err := ins.ExecContext(ctx, s.controller, int64(l.Seq), text(e.ID), e.TS, text(e.User), text(e.Source), text(e.Action),
			nullText(e.Target), text(e.Result), jsonb(e.Before), jsonb(e.After), jsonbMap(e.Detail),
			nullText(e.From), nullText(e.Session), nullText(e.Prev), l.Hash, l.File, l.Offset, string(l.Raw))
		if err != nil {
			return fmt.Errorf("auditpg: seq %d: %w", l.Seq, err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			var have string
			if err := tx.QueryRowContext(ctx, `SELECT hash FROM audit_entries WHERE controller = $1 AND seq = $2`,
				s.controller, int64(l.Seq)).Scan(&have); err != nil {
				return err
			}
			if have != l.Hash {
				return &audit.CopyMismatch{File: l.File, Offset: l.Offset,
					Reason: fmt.Sprintf("the copy already holds another line as seq %d for controller %q (two controllers under one name?)", l.Seq, s.controller)}
			}
		}
	}
	return tx.Commit()
}

// Postgres refuses a NUL character in text and in a jsonb string, and
// JSON allows one (a target or a value from a request can hold it). One
// would stop the copy for good, so NULs become U+FFFD in the queryable
// columns. The line column keeps the exact line the chain hashes: there a
// NUL is the escape \u0000, six ordinary characters.

func text(s string) string { return strings.ReplaceAll(s, "\x00", "\uFFFD") }

func nullText(s string) sql.NullString { return sql.NullString{String: text(s), Valid: s != ""} }

// jsonb encodes a value for a jsonb column, nil as NULL, NULs replaced.
func jsonb(v any) any {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(noNUL(v))
	if err != nil {
		return nil
	}
	return string(b)
}

func jsonbMap(m map[string]any) any {
	if m == nil {
		return nil
	}
	return jsonb(m)
}

func noNUL(v any) any {
	switch x := v.(type) {
	case string:
		return strings.ReplaceAll(x, "\x00", "\uFFFD")
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[strings.ReplaceAll(k, "\x00", "\uFFFD")] = noNUL(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = noNUL(e)
		}
		return out
	}
	return v
}

var (
	_ audit.CopyStore = (*Store)(nil)
	_ audit.Anchorer  = (*Store)(nil)
)
