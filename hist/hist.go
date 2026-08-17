// Package hist is the historian's storage layer: a thin wrapper over
// Postgres for time-series tag samples. This is a separate concern from
// the controller — the nautilus runtime never imports it, only the
// `nautilus historian` CLI binary does — so it's free to use a database
// driver where the runtime stays close to stdlib.
package hist

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	// Imported non-blank (rather than the original's blank import) because
	// Query uses pq.Array directly; its driver registers itself via init()
	// either way.
	"github.com/lib/pq"
)

// Sink archives timestamped samples. Store (Postgres) implements it; a
// test or an alternative TSDB implements it in a few lines — the collector
// in cmd/nautilus depends on this interface, not on Store directly, so it
// can be driven by a fake in tests without a real database.
type Sink interface {
	Insert(ts time.Time, vals map[string]float64) error
}

// Store is a Postgres-backed Sink plus the range-query API the historian's
// HTTP endpoints serve from.
type Store struct {
	db *sql.DB
}

// Open connects and runs migrations. url is a standard postgres DSN.
func Open(url string) (*Store, error) {
	db, err := sql.Open("postgres", url)
	if err != nil {
		return nil, err
	}
	// A historian is a light writer (one bulk insert per interval) and a
	// light reader (occasional range queries) — a handful of connections
	// is plenty and keeps it from crowding out the database's other users.
	db.SetMaxOpenConns(4)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		return nil, err
	}
	s := &Store{db: db}
	return s, s.migrate(ctx)
}

// migrate creates the schema if it doesn't already exist, so Open is safe
// to call against a fresh database or one the historian has run against
// before — no separate migration step to remember.
func (s *Store) migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS samples (
			ts    timestamptz NOT NULL,
			tag   text        NOT NULL,
			value double precision
		);
		CREATE INDEX IF NOT EXISTS samples_tag_ts ON samples (tag, ts DESC);`)
	return err
}

// Insert writes one timestamped set of tag values as a single multi-row
// INSERT, so a collector interval's worth of samples costs one round trip
// regardless of how many tags it's archiving.
func (s *Store) Insert(ts time.Time, vals map[string]float64) error {
	if len(vals) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString("INSERT INTO samples (ts, tag, value) VALUES ")
	args := make([]any, 0, len(vals)*3)
	i := 1
	first := true
	for tag, v := range vals {
		if !first {
			b.WriteString(",")
		}
		first = false
		fmt.Fprintf(&b, "($%d,$%d,$%d)", i, i+1, i+2)
		args = append(args, ts, tag, v)
		i += 3
	}
	_, err := s.db.Exec(b.String(), args...)
	return err
}

// Series is one tag's downsampled history: [unixMs, value] pairs. Kept as
// a type alias (not a defined type) because it is a chart wire format —
// callers that json.Marshal a map[string]Series need exactly [][2]float64
// on the wire, not a named type that would encode the same but read
// differently in a signature.
type Series = [][2]float64

// Query returns bucket-averaged series for the given tags over [from,to].
// The window is split into ~maxPoints time buckets so responses stay
// bounded regardless of range.
func (s *Store) Query(from, to time.Time, tags []string, maxPoints int) (map[string]Series, error) {
	if maxPoints < 1 {
		maxPoints = 600
	}
	bucket := to.Sub(from).Seconds() / float64(maxPoints)
	if bucket < 1 {
		bucket = 1
	}
	// FIX vs. the original: tags used to be rendered into a hand-built
	// "{a,b,c}" Postgres array literal (pgArray) and passed as text. That's
	// injection-adjacent (a tag name containing a comma or brace corrupts
	// the literal) and was never necessary — pq.Array lets database/sql
	// bind the slice as a real parameter.
	rows, err := s.db.Query(`
		SELECT tag,
		       floor(extract(epoch from ts)/$1)*$1 AS b,
		       avg(value)
		FROM samples
		WHERE ts >= $2 AND ts <= $3 AND tag = ANY($4)
		GROUP BY tag, b
		ORDER BY b`,
		bucket, from, to, pq.Array(tags))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]Series, len(tags))
	for rows.Next() {
		var tag string
		var epoch, val float64
		if err := rows.Scan(&tag, &epoch, &val); err != nil {
			return nil, err
		}
		out[tag] = append(out[tag], [2]float64{epoch * 1000, val})
	}
	return out, rows.Err()
}

// Span returns the timestamp of the earliest and latest sample, so the
// HMI can show how much history exists.
func (s *Store) Span() (first, last time.Time, count int64, err error) {
	err = s.db.QueryRow(`SELECT min(ts), max(ts), count(*) FROM samples`).
		Scan(&nullTime{&first}, &nullTime{&last}, &count)
	return
}

// Prune deletes samples older than the retention window. Called on an
// hourly ticker by the historian daemon, so this stays a plain DELETE
// rather than anything batched — an hour's worth of aging-out rows is
// never enough to worry about lock duration.
func (s *Store) Prune(olderThan time.Duration) error {
	_, err := s.db.Exec(`DELETE FROM samples WHERE ts < $1`, time.Now().Add(-olderThan))
	return err
}

func (s *Store) Close() error { return s.db.Close() }

// nullTime lets min()/max() return NULL on an empty table without error:
// Span on a fresh database yields zero times and a zero count instead of
// a scan error.
type nullTime struct{ t *time.Time }

func (n *nullTime) Scan(v any) error {
	if v == nil {
		return nil
	}
	if t, ok := v.(time.Time); ok {
		*n.t = t
	}
	return nil
}
