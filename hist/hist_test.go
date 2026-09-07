package hist

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"testing"
	"time"
)

// TestSeriesIsChartWireFormat pins Series to the exact wire shape a chart
// client expects: an array of [ts, value] pairs, not an object. If Series
// ever stopped being a [][2]float64 alias, this is the test that would
// catch a client-breaking JSON shape change.
func TestSeriesIsChartWireFormat(t *testing.T) {
	var s Series = Series{{1000, 2.5}, {2000, 3.5}}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	want := `[[1000,2.5],[2000,3.5]]`
	if got != want {
		t.Fatalf("Series marshaled as %s, want %s", got, want)
	}
}

// bucketSize mirrors the floor/default computation inside Query so it can
// be tested without a database. Kept in lockstep with Query's inline
// logic; if Query's formula changes, update both.
func bucketSize(from, to time.Time, maxPoints int) float64 {
	if maxPoints < 1 {
		maxPoints = 600
	}
	bucket := to.Sub(from).Seconds() / float64(maxPoints)
	if bucket < 1 {
		bucket = 1
	}
	return bucket
}

func TestBucketSizeDefaultsMaxPoints(t *testing.T) {
	from := time.Unix(0, 0)
	to := from.Add(time.Hour)
	// 3600s / 600 (default) = 6s buckets.
	got := bucketSize(from, to, 0)
	if got != 6 {
		t.Fatalf("bucketSize with maxPoints=0 (default) = %v, want 6", got)
	}
}

func TestBucketSizeFloorsAtOneSecond(t *testing.T) {
	from := time.Unix(0, 0)
	to := from.Add(time.Second)
	// 1s window / 600 points would be far below 1s; must floor to 1.
	got := bucketSize(from, to, 600)
	if got != 1 {
		t.Fatalf("bucketSize floor = %v, want 1", got)
	}
}

func TestBucketSizeScalesWithWindow(t *testing.T) {
	from := time.Unix(0, 0)
	to := from.Add(10 * time.Hour)
	got := bucketSize(from, to, 100)
	want := (10 * time.Hour).Seconds() / 100
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("bucketSize = %v, want %v", got, want)
	}
}

// fakeSink is a compile-time check that Sink is implementable in a few
// lines, as the doc comment promises, and gives the CLI's tests a Sink
// double that needs no Postgres.
type fakeSink struct {
	inserts []map[string]float64
}

func (f *fakeSink) Insert(ts time.Time, vals map[string]float64) error {
	f.inserts = append(f.inserts, vals)
	return nil
}

var _ Sink = (*fakeSink)(nil)

func TestFakeSinkRecordsInserts(t *testing.T) {
	f := &fakeSink{}
	if err := f.Insert(time.Now(), map[string]float64{"a": 1}); err != nil {
		t.Fatal(err)
	}
	if len(f.inserts) != 1 {
		t.Fatalf("got %d inserts, want 1", len(f.inserts))
	}
}

// mkSamples builds an ordered sample slice from (offsetSeconds, value)
// pairs relative to base, for table-driven reducer/bucketize tests.
func mkSamples(base time.Time, pairs ...[2]float64) []sample {
	out := make([]sample, len(pairs))
	for i, p := range pairs {
		out[i] = sample{ts: base.Add(time.Duration(p[0]) * time.Second), v: p[1]}
	}
	return out
}

func TestFirstLastDeltaReducers(t *testing.T) {
	base := time.Unix(1_700_000_000, 0).UTC()
	tests := []struct {
		name        string
		samples     []sample
		first, last float64
	}{
		{"single sample", mkSamples(base, [2]float64{0, 42}), 42, 42},
		{"rising totalizer", mkSamples(base, [2]float64{0, 100}, [2]float64{60, 150}, [2]float64{120, 230}), 100, 230},
		{"falling", mkSamples(base, [2]float64{0, 10}, [2]float64{10, 5}, [2]float64{20, 1}), 10, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := firstValue(tt.samples); got != tt.first {
				t.Errorf("firstValue = %v, want %v", got, tt.first)
			}
			if got := lastValue(tt.samples); got != tt.last {
				t.Errorf("lastValue = %v, want %v", got, tt.last)
			}
			wantDelta := tt.last - tt.first
			if got := deltaValue(tt.samples); got != wantDelta {
				t.Errorf("deltaValue = %v, want %v (last-first, for a period's totalizer total)", got, wantDelta)
			}
		})
	}
}

// TestOntimeSeconds pins the documented interpretation: for each adjacent
// pair, the interval up to the next sample counts when the *earlier*
// sample's value is non-zero (a RUNST recorded as 1 means "running until
// the next recorded sample"), and the last sample in a bucket contributes
// no trailing interval since there is nothing to bound it.
func TestOntimeSeconds(t *testing.T) {
	base := time.Unix(1_700_000_000, 0).UTC()
	tests := []struct {
		name    string
		samples []sample
		want    float64
	}{
		{"empty", nil, 0},
		{"single sample never on (no next sample to bound it)", mkSamples(base, [2]float64{0, 1}), 0},
		{
			"on for the first interval, off for the second",
			mkSamples(base, [2]float64{0, 1}, [2]float64{60, 0}, [2]float64{120, 0}),
			60,
		},
		{
			"off then on: only the on-interval counts",
			mkSamples(base, [2]float64{0, 0}, [2]float64{60, 1}, [2]float64{180, 0}),
			120,
		},
		{
			"on the whole time: trailing sample's own interval is uncounted",
			mkSamples(base, [2]float64{0, 1}, [2]float64{600, 1}, [2]float64{1200, 1}),
			1200,
		},
		{
			"nonzero-but-not-one still counts as on",
			mkSamples(base, [2]float64{0, 2.5}, [2]float64{30, 0}),
			30,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ontimeSeconds(tt.samples); got != tt.want {
				t.Errorf("ontimeSeconds = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBucketizeNoBucketingIsWholeRangeAtFrom(t *testing.T) {
	base := time.Unix(1_700_000_000, 0).UTC()
	series := mkSamples(base, [2]float64{0, 1}, [2]float64{3600, 2}, [2]float64{7200, 3})
	from := base.Add(-time.Minute)
	got := bucketize(series, from, 0)
	if len(got) != 1 {
		t.Fatalf("bucketize with no Bucket = %d buckets, want 1", len(got))
	}
	if !got[0].start.Equal(from) {
		t.Fatalf("bucketize with no Bucket: start = %v, want from %v", got[0].start, from)
	}
	if len(got[0].samples) != 3 {
		t.Fatalf("bucketize with no Bucket: %d samples in the one bucket, want 3", len(got[0].samples))
	}
}

func TestBucketizeEmptySeriesYieldsNoBuckets(t *testing.T) {
	if got := bucketize(nil, time.Now(), time.Hour); got != nil {
		t.Fatalf("bucketize(nil) = %v, want nil (no buckets for a tag with no samples)", got)
	}
}

// TestBucketizeAlignsToEpochFloor pins bucketize's alignment to match
// aggregateSQL's floor(extract(epoch from ts)/$1)*$1: an hour bucket
// starting mid-hour still splits samples at the top of the hour, not at
// the first sample's own timestamp, and a sample landing exactly on an
// hour boundary starts a new bucket rather than joining the prior one.
func TestBucketizeAlignsToEpochFloor(t *testing.T) {
	const hourSecs = 3600
	epoch0 := time.Now().Unix()
	hourStart := time.Unix((epoch0/hourSecs)*hourSecs, 0).UTC()
	nextHour := hourStart.Add(time.Hour)

	series := []sample{
		{ts: hourStart.Add(20 * time.Minute), v: 1}, // mid-first-hour
		{ts: hourStart.Add(50 * time.Minute), v: 2}, // still first hour
		{ts: nextHour, v: 3},                        // exactly on the boundary: new bucket
		{ts: nextHour.Add(10 * time.Minute), v: 4},  // still second hour
	}
	got := bucketize(series, hourStart, time.Hour)
	if len(got) != 2 {
		t.Fatalf("bucketize = %d buckets, want 2 (one per hour)", len(got))
	}
	if !got[0].start.Equal(hourStart) {
		t.Errorf("bucket 0 start = %v, want %v", got[0].start, hourStart)
	}
	if len(got[0].samples) != 2 {
		t.Errorf("bucket 0 has %d samples, want 2", len(got[0].samples))
	}
	if !got[1].start.Equal(nextHour) {
		t.Errorf("bucket 1 start = %v, want %v", got[1].start, nextHour)
	}
	if len(got[1].samples) != 2 {
		t.Errorf("bucket 1 has %d samples, want 2", len(got[1].samples))
	}
}

// TestSQLAggFnsCoversDocumentedFunctions pins which Fn values Aggregate
// dispatches to a plain SQL GROUP BY aggregate (as opposed to the Go-side
// first/last/delta/ontime reducers) — a documentation/regression guard, not
// a live-SQL test, since hist has no fake *sql.DB to execute against.
func TestSQLAggFnsCoversDocumentedFunctions(t *testing.T) {
	want := map[string]string{
		"min":   "min(value)",
		"max":   "max(value)",
		"avg":   "avg(value)",
		"sum":   "sum(value)",
		"count": "count(value)",
	}
	if len(sqlAggFns) != len(want) {
		t.Fatalf("sqlAggFns has %d entries, want %d: %v", len(sqlAggFns), len(want), sqlAggFns)
	}
	for fn, expr := range want {
		if got := sqlAggFns[fn]; got != expr {
			t.Errorf("sqlAggFns[%q] = %q, want %q", fn, got, expr)
		}
	}
	// first/last/delta/ontime must NOT be handled by the SQL path — they
	// need row order that a plain GROUP BY aggregate can't give.
	for _, fn := range []string{"first", "last", "delta", "ontime"} {
		if _, ok := sqlAggFns[fn]; ok {
			t.Errorf("sqlAggFns unexpectedly contains %q; it must go through aggregateGo", fn)
		}
	}
}

// TestAggregateEmptyTagsIsNoop confirms Aggregate short-circuits before
// touching the database when Tags is empty, matching Query's "no tags, no
// work" behavior.
func TestAggregateEmptyTagsIsNoop(t *testing.T) {
	s := &Store{}
	got, err := s.Aggregate(context.Background(), AggQuery{Tags: nil, Fn: "avg"})
	if err != nil {
		t.Fatalf("Aggregate(no tags): %v", err)
	}
	if got != nil {
		t.Fatalf("Aggregate(no tags) = %v, want nil", got)
	}
}

// TestAggregateUnknownFnErrorsWithoutTouchingDB confirms an unrecognized
// Fn is rejected before any query runs — exercised against a zero-value
// Store (nil *sql.DB) to prove the error path never dereferences db.
func TestAggregateUnknownFnErrorsWithoutTouchingDB(t *testing.T) {
	s := &Store{}
	_, err := s.Aggregate(context.Background(), AggQuery{Tags: []string{"a"}, Fn: "median"})
	if err == nil {
		t.Fatal("Aggregate with unknown fn: want error, got nil")
	}
}

// TestSnapshotEmptyTagsIsNoop mirrors TestAggregateEmptyTagsIsNoop for
// Snapshot.
func TestSnapshotEmptyTagsIsNoop(t *testing.T) {
	s := &Store{}
	got, err := s.Snapshot(context.Background(), nil, time.Now())
	if err != nil {
		t.Fatalf("Snapshot(no tags): %v", err)
	}
	if got != nil {
		t.Fatalf("Snapshot(no tags) = %v, want nil", got)
	}
}

// TestStorePostgres is an integration test covering the full Open/Insert/
// Query/Span/Prune round-trip against a real Postgres. CI has no Postgres
// today, so it skips unless NAUTILUS_TEST_DATABASE_URL is set — run it
// locally with e.g.:
//
//	docker run --rm -p 5432:5432 -e POSTGRES_PASSWORD=postgres postgres:16
//	NAUTILUS_TEST_DATABASE_URL="postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable" \
//	  go test ./hist/ -run TestStorePostgres -v
func TestStorePostgres(t *testing.T) {
	url := os.Getenv("NAUTILUS_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set NAUTILUS_TEST_DATABASE_URL to run this test against a real Postgres")
	}

	s, err := Open(url)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	// Start from a clean table so the round-trip is deterministic across
	// repeated runs against the same database.
	if _, err := s.db.Exec("DELETE FROM samples"); err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	first, last, count, err := s.Span()
	if err != nil {
		t.Fatalf("Span (empty): %v", err)
	}
	if !first.IsZero() || !last.IsZero() || count != 0 {
		t.Fatalf("Span (empty) = %v, %v, %d; want zero times and 0 count", first, last, count)
	}

	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	for i := 0; i < 10; i++ {
		ts := base.Add(time.Duration(i) * time.Minute)
		if err := s.Insert(ts, map[string]float64{
			"pv": float64(i),
			"sp": 50,
		}); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}

	first, last, count, err = s.Span()
	if err != nil {
		t.Fatalf("Span: %v", err)
	}
	if count != 20 {
		t.Fatalf("Span count = %d, want 20", count)
	}
	if first.After(last) {
		t.Fatalf("Span first %v after last %v", first, last)
	}

	series, err := s.Query(base.Add(-time.Minute), base.Add(10*time.Minute), []string{"pv", "sp", "missing"}, 600)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(series["pv"]) == 0 {
		t.Fatal("Query: expected pv series data")
	}
	if _, ok := series["missing"]; ok {
		t.Fatal("Query: expected no series for a tag with no samples")
	}
	// tag = ANY($4) via pq.Array must also survive a tag name containing a
	// comma or brace, which the original's hand-built "{a,b,c}" literal
	// would have corrupted.
	if err := s.Insert(base, map[string]float64{"weird,tag{x}": 1}); err != nil {
		t.Fatalf("Insert weird tag: %v", err)
	}
	series, err = s.Query(base.Add(-time.Minute), base.Add(time.Minute), []string{"weird,tag{x}"}, 600)
	if err != nil {
		t.Fatalf("Query weird tag: %v", err)
	}
	if len(series["weird,tag{x}"]) == 0 {
		t.Fatal("Query: expected a series for the comma/brace tag name")
	}

	if err := s.Prune(0); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	_, _, count, err = s.Span()
	if err != nil {
		t.Fatalf("Span (post-prune): %v", err)
	}
	if count != 0 {
		t.Fatalf("Span count after Prune(0) = %d, want 0", count)
	}
}

// TestAggregateAndSnapshotPostgres is an integration test covering
// Aggregate (SQL and Go-side function families) and Snapshot against a
// real Postgres. Same opt-in as TestStorePostgres: skips unless
// NAUTILUS_TEST_DATABASE_URL is set.
func TestAggregateAndSnapshotPostgres(t *testing.T) {
	url := os.Getenv("NAUTILUS_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set NAUTILUS_TEST_DATABASE_URL to run this test against a real Postgres")
	}

	s, err := Open(url)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	if _, err := s.db.Exec("DELETE FROM samples"); err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	// A RUNST-shaped tag: on for the first half of a 2-hour window, off for
	// the second half, sampled every 10 minutes — like a well pump's
	// RUNST at the historian's usual scan cadence. Anchored to a clean
	// hour boundary (not just time.Now()) so the bucketed-max assertion
	// below, which relies on the 2-hour window splitting into exactly two
	// 1-hour buckets, can't flake depending on what minute the test runs at.
	base := time.Now().Truncate(time.Hour).Add(-3 * time.Hour)
	runst := []struct {
		off int
		v   float64
	}{
		{0, 1}, {10, 1}, {20, 1}, {30, 1}, {40, 1}, {50, 1},
		{60, 0}, {70, 0}, {80, 0}, {90, 0}, {100, 0}, {110, 0},
	}
	for _, r := range runst {
		ts := base.Add(time.Duration(r.off) * time.Minute)
		if err := s.Insert(ts, map[string]float64{"WEL1.RUNST": r.v}); err != nil {
			t.Fatalf("Insert RUNST: %v", err)
		}
	}
	// A totalizer rising steadily across the same window, plus a level tag
	// for min/max/avg/Snapshot coverage. Stops at 110min (i=11), one
	// sample interval short of the 2-hour mark, so every sample stays
	// inside the [base, base+2h) window the bucketed-max assertion below
	// relies on splitting into exactly two 1-hour buckets — a sample
	// landing exactly on base+2h would start a third bucket.
	for i := 0; i <= 11; i++ {
		ts := base.Add(time.Duration(i*10) * time.Minute)
		if err := s.Insert(ts, map[string]float64{
			"FIT1.PREV":  float64(1000 + i*5),
			"LIT1.LEVEL": float64(20 - i),
		}); err != nil {
			t.Fatalf("Insert totalizer/level: %v", err)
		}
	}

	from := base.Add(-time.Minute)
	to := base.Add(121 * time.Minute)

	// Whole-range ontime: on for the first ~60 minutes (6 samples at 10min
	// apart, last "on" sample bounds an interval to the first "off"
	// sample), off after.
	rows, err := s.Aggregate(context.Background(), AggQuery{
		Tags: []string{"WEL1.RUNST"}, From: from, To: to, Fn: "ontime",
	})
	if err != nil {
		t.Fatalf("Aggregate ontime: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("Aggregate ontime = %d rows, want 1", len(rows))
	}
	wantOntime := 60 * 60.0 // 60 minutes in seconds
	if rows[0].Value != wantOntime {
		t.Fatalf("Aggregate ontime value = %v, want %v", rows[0].Value, wantOntime)
	}

	// Whole-range delta on the totalizer: last(1055) - first(1000) = 55.
	rows, err = s.Aggregate(context.Background(), AggQuery{
		Tags: []string{"FIT1.PREV"}, From: from, To: to, Fn: "delta",
	})
	if err != nil {
		t.Fatalf("Aggregate delta: %v", err)
	}
	if len(rows) != 1 || rows[0].Value != 55 {
		t.Fatalf("Aggregate delta = %v, want a single row with value 55", rows)
	}

	// min/max/avg on the level tag over the whole range (values 20..9).
	for fn, want := range map[string]float64{"min": 9, "max": 20, "avg": 14.5} {
		rows, err = s.Aggregate(context.Background(), AggQuery{
			Tags: []string{"LIT1.LEVEL"}, From: from, To: to, Fn: fn,
		})
		if err != nil {
			t.Fatalf("Aggregate %s: %v", fn, err)
		}
		if len(rows) != 1 || rows[0].Value != want {
			t.Fatalf("Aggregate %s = %v, want a single row with value %v", fn, rows, want)
		}
	}

	// Bucketed max: two 1-hour buckets over the 2-hour window.
	rows, err = s.Aggregate(context.Background(), AggQuery{
		Tags: []string{"LIT1.LEVEL"}, From: from, To: to, Bucket: time.Hour, Fn: "max",
	})
	if err != nil {
		t.Fatalf("Aggregate bucketed max: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("Aggregate bucketed max = %d rows, want 2", len(rows))
	}

	// Snapshot: value at-or-before the window midpoint should be the 60min
	// sample (1030 on the totalizer).
	at := base.Add(60 * time.Minute)
	snap, err := s.Snapshot(context.Background(), []string{"FIT1.PREV", "missing"}, at)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snap) != 1 {
		t.Fatalf("Snapshot = %d rows, want 1 (missing tag has no sample)", len(snap))
	}
	if snap[0].Tag != "FIT1.PREV" || snap[0].Value != 1030 {
		t.Fatalf("Snapshot = %+v, want FIT1.PREV=1030", snap[0])
	}
}
