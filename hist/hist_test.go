package hist

import (
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
