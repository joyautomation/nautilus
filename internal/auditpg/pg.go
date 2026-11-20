// Package auditpg keeps a copy of the runtime audit journal in Postgres
// (#709 PR 9, docs/design/identity-audit.md §4): `audit.sink: postgres`.
//
// It is a store, nothing more. audit.Tailer reads the journal's files,
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
  PRIMARY KEY (controller, seq));
CREATE INDEX audit_entries_ts ON audit_entries (controller, ts);`

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
	for _, t := range []struct{ name, ddl string }{{"audit_entries", entriesDDL}, {"audit_gaps", gapsDDL}} {
		var reg sql.NullString
		if err := s.db.QueryRowContext(ctx, `SELECT to_regclass($1)::text`, t.name).Scan(&reg); err != nil {
			return err
		}
		if reg.Valid {
			continue
		}
		if _, err := s.db.ExecContext(ctx, t.ddl); err != nil {
			return fmt.Errorf("auditpg: create %s: %w (a database administrator can create it and grant this role INSERT and SELECT on it)", t.name, err)
		}
	}
	s.schema = true
	return nil
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

var _ audit.CopyStore = (*Store)(nil)
