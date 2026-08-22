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

// AggQuery describes a server-side aggregation request over archived
// samples — the shape compliance/daily reports need (daily min/max/avg per
// tag, flow totals from a totalizer, well runtime hours from a BOOL RUNST,
// bucketed series for a report table or chart) without pulling raw rows
// down to compute them client-side.
type AggQuery struct {
	Tags []string
	From time.Time
	To   time.Time
	// Bucket splits [From,To) into fixed-width windows aligned to the
	// Unix epoch (floor(epoch/bucketSeconds)*bucketSeconds, same alignment
	// Query uses). Zero means "no bucketing" — the whole [From,To) range
	// is treated as a single bucket whose reported Ts is From.
	Bucket time.Duration
	// Fn selects the reduction applied to each tag's samples within a
	// bucket: min|max|avg|sum|count run directly in SQL; first|last|delta|
	// ontime are computed in Go over samples fetched in ts order (see
	// aggregateGo).
	Fn string
}

// AggRow is one aggregated result: Fn applied to Tag's samples in the
// bucket starting at Ts.
type AggRow struct {
	Tag   string    `json:"tag"`
	Ts    time.Time `json:"ts"`
	Value float64   `json:"value"`
}

// sqlAggFns maps the SQL-native aggregate functions to the SQL expression
// computing them over the samples table's value column. first/last/delta/
// ontime are deliberately not here — they need row order, not just a SQL
// aggregate, and are computed by aggregateGo instead.
var sqlAggFns = map[string]string{
	"min":   "min(value)",
	"max":   "max(value)",
	"avg":   "avg(value)",
	"sum":   "sum(value)",
	"count": "count(value)",
}

// Aggregate computes AggQuery.Fn per tag per bucket. min/max/avg/sum/count
// are plain SQL GROUP BY aggregates. first/last/delta (last-first, for
// totalizers) and ontime (seconds a BOOL tag was non-zero — see
// ontimeSeconds) need ordered samples, so they're reduced in Go over rows
// fetched in ts order; correct over fast, which is fine at the query
// volumes a daily/compliance report needs.
func (s *Store) Aggregate(ctx context.Context, q AggQuery) ([]AggRow, error) {
	if len(q.Tags) == 0 {
		return nil, nil
	}
	if _, ok := sqlAggFns[q.Fn]; ok {
		return s.aggregateSQL(ctx, q)
	}
	switch q.Fn {
	case "first", "last", "delta", "ontime":
		return s.aggregateGo(ctx, q)
	}
	return nil, fmt.Errorf("hist: unknown aggregate function %q", q.Fn)
}

// aggregateSQL handles the min/max/avg/sum/count family: a straight GROUP
// BY tag (and, when bucketed, an epoch-floor bucket column matching
// Query's bucketing) computed entirely by Postgres.
func (s *Store) aggregateSQL(ctx context.Context, q AggQuery) ([]AggRow, error) {
	expr := sqlAggFns[q.Fn]
	if q.Bucket <= 0 {
		rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
			SELECT tag, %s
			FROM samples
			WHERE ts >= $1 AND ts < $2 AND tag = ANY($3)
			GROUP BY tag`, expr),
			q.From, q.To, pq.Array(q.Tags))
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []AggRow
		for rows.Next() {
			var tag string
			var val float64
			if err := rows.Scan(&tag, &val); err != nil {
				return nil, err
			}
			out = append(out, AggRow{Tag: tag, Ts: q.From, Value: val})
		}
		return out, rows.Err()
	}

	secs := q.Bucket.Seconds()
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT tag,
		       floor(extract(epoch from ts)/$1)*$1 AS b,
		       %s
		FROM samples
		WHERE ts >= $2 AND ts < $3 AND tag = ANY($4)
		GROUP BY tag, b
		ORDER BY tag, b`, expr),
		secs, q.From, q.To, pq.Array(q.Tags))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AggRow
	for rows.Next() {
		var tag string
		var epoch, val float64
		if err := rows.Scan(&tag, &epoch, &val); err != nil {
			return nil, err
		}
		out = append(out, AggRow{Tag: tag, Ts: time.Unix(int64(epoch), 0).UTC(), Value: val})
	}
	return out, rows.Err()
}

// sample is one ordered (ts, value) pair — the shape aggregateGo and the
// first/last/delta/ontime reducers work over.
type sample struct {
	ts time.Time
	v  float64
}

// aggregateGo handles first/last/delta/ontime: it fetches each tag's
// samples in the window in ts order, splits them into buckets aligned the
// same way aggregateSQL's epoch-floor bucketing is, and reduces each
// bucket in Go.
func (s *Store) aggregateGo(ctx context.Context, q AggQuery) ([]AggRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT tag, ts, value
		FROM samples
		WHERE ts >= $1 AND ts < $2 AND tag = ANY($3)
		ORDER BY tag, ts`,
		q.From, q.To, pq.Array(q.Tags))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	bySeries := make(map[string][]sample)
	for rows.Next() {
		var tag string
		var ts time.Time
		var v float64
		if err := rows.Scan(&tag, &ts, &v); err != nil {
			return nil, err
		}
		bySeries[tag] = append(bySeries[tag], sample{ts: ts, v: v})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var out []AggRow
	for _, tag := range q.Tags {
		for _, b := range bucketize(bySeries[tag], q.From, q.Bucket) {
			var val float64
			switch q.Fn {
			case "first":
				val = firstValue(b.samples)
			case "last":
				val = lastValue(b.samples)
			case "delta":
				val = deltaValue(b.samples)
			case "ontime":
				val = ontimeSeconds(b.samples)
			}
			out = append(out, AggRow{Tag: tag, Ts: b.start, Value: val})
		}
	}
	return out, nil
}

// bucket is one tag's samples falling within [start, start+bucketDur).
type bucket struct {
	start   time.Time
	samples []sample
}

// bucketize splits an ordered sample slice into buckets of width
// bucketDur, aligned to the Unix epoch (floor(unix/secs)*secs), matching
// aggregateSQL's floor(extract(epoch from ts)/$1)*$1. bucketDur <= 0 means
// "no bucketing": the whole slice comes back as one bucket starting at
// from. An empty series yields no buckets at all (nothing to report for a
// tag with no samples in the window).
func bucketize(series []sample, from time.Time, bucketDur time.Duration) []bucket {
	if len(series) == 0 {
		return nil
	}
	if bucketDur <= 0 {
		return []bucket{{start: from, samples: series}}
	}
	secs := int64(bucketDur.Seconds())
	if secs < 1 {
		secs = 1
	}
	var out []bucket
	for _, sm := range series {
		start := time.Unix((sm.ts.Unix()/secs)*secs, 0).UTC()
		if len(out) == 0 || !out[len(out)-1].start.Equal(start) {
			out = append(out, bucket{start: start})
		}
		last := &out[len(out)-1]
		last.samples = append(last.samples, sm)
	}
	return out
}

// firstValue is the earliest sample's value in an ordered, non-empty
// slice.
func firstValue(s []sample) float64 { return s[0].v }

// lastValue is the latest sample's value in an ordered, non-empty slice.
func lastValue(s []sample) float64 { return s[len(s)-1].v }

// deltaValue is last-minus-first: the change over the bucket, which for a
// monotonic totalizer (a flow-quantity-index tag) is that period's total —
// matching UpdateReportFlowTotals' LastValue+delta approach rather than
// naively summing raw sample values.
func deltaValue(s []sample) float64 { return lastValue(s) - firstValue(s) }

// ontimeSeconds sums the seconds a tag was non-zero, read off consecutive
// samples: for each adjacent pair, the interval [s[i-1].ts, s[i].ts) counts
// toward the total when s[i-1]'s value is non-zero (a BOOL RUNST recorded
// as 1 means "running for the interval that follows this sample"). This is
// the runtime-hours source for a well/pump RUNST tag — divide the result
// by 3600 for hours, matching Ignition's UpdateReportRuntimes rollup.
//
// Interpretation and limits: this is a step function reconstructed from
// whatever samples happen to exist, so a value change is only as precise
// as the collector's -interval, and the very last sample in a bucket
// contributes no trailing interval (there is no next sample to bound it) —
// negligible for a daily bucket sampled at seconds-to-minutes resolution.
// Because bucketize splits the series before this runs, an on/off
// transition that straddles a bucket boundary is not split across the two
// buckets; the interval is dropped rather than attributed to either side.
// That is a deliberate correctness-over-precision tradeoff for daily
// reports, not something to rely on for sub-bucket accuracy.
func ontimeSeconds(s []sample) float64 {
	var total float64
	for i := 1; i < len(s); i++ {
		if s[i-1].v != 0 {
			total += s[i].ts.Sub(s[i-1].ts).Seconds()
		}
	}
	return total
}

// Snapshot returns each tag's last value at-or-before at — e.g. a
// reservoir level at 6am, or a well's RUNST at report-window start. A tag
// with no sample at-or-before at is simply absent from the result (not an
// error), same as Query's "series for a tag with no samples" behavior.
func (s *Store) Snapshot(ctx context.Context, tags []string, at time.Time) ([]AggRow, error) {
	if len(tags) == 0 {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT ON (tag) tag, ts, value
		FROM samples
		WHERE ts <= $1 AND tag = ANY($2)
		ORDER BY tag, ts DESC`,
		at, pq.Array(tags))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AggRow
	for rows.Next() {
		var row AggRow
		if err := rows.Scan(&row.Tag, &row.Ts, &row.Value); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
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
