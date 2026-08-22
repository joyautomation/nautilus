package alarm

import (
	"os"
	"testing"
	"time"
)

// TestPGJournal is an integration test covering the full open / migrate /
// append / query / prune round-trip against a real Postgres. CI has no
// Postgres today, so it skips unless NAUTILUS_TEST_DATABASE_URL is set —
// the same switch hist's Store test uses. Run it locally with e.g.:
//
//	docker run --rm -p 5432:5432 -e POSTGRES_PASSWORD=postgres postgres:16
//	NAUTILUS_TEST_DATABASE_URL="postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable" \
//	  go test ./alarm/ -run TestPGJournal -v
func TestPGJournal(t *testing.T) {
	url := os.Getenv("NAUTILUS_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set NAUTILUS_TEST_DATABASE_URL to run this test against a real Postgres")
	}

	j, err := NewPostgres(url)
	if err != nil {
		t.Fatalf("NewPostgres: %v", err)
	}
	defer j.Close()

	// Start clean so repeated runs against the same database are
	// deterministic. This also proves the DDL is idempotent: the second run
	// of this test opens against a table that already exists.
	if _, err := j.db.Exec("DELETE FROM alarm_events"); err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	base := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
	events := []Event{
		{TS: base.UnixMilli(), ID: "RTU9_FIT.HH", Name: "Flow High High", Kind: KindActive, Priority: High, Site: "RTU9", State: "unack-active"},
		{TS: base.Add(time.Minute).UnixMilli(), ID: "RTU9_FIT.HH", Name: "Flow High High", Kind: KindAck, Priority: High, Site: "RTU9", State: "ack-active", By: "rchon"},
		{TS: base.Add(2 * time.Minute).UnixMilli(), ID: "RTU12_INT_YA", Name: "Intrusion", Kind: KindActive, Priority: Critical, Site: "RTU12", State: "unack-active"},
	}
	for _, e := range events {
		if err := j.Append(e); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	if err := j.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	got, err := j.Query(time.Time{}, time.Time{}, Filter{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("Query returned %d events, want 3", len(got))
	}
	if got[0].ID != "RTU12_INT_YA" {
		t.Errorf("Query is not newest-first: %+v", got[0])
	}
	if got[0].Priority != Critical {
		t.Errorf("priority did not round-trip: %+v", got[0])
	}

	bySite, err := j.Query(time.Time{}, time.Time{}, Filter{Sites: []string{"RTU9"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(bySite) != 2 {
		t.Fatalf("site filter returned %d, want 2", len(bySite))
	}
	byKind, err := j.Query(time.Time{}, time.Time{}, Filter{Kinds: []string{KindAck}})
	if err != nil {
		t.Fatal(err)
	}
	if len(byKind) != 1 || byKind[0].By != "rchon" {
		t.Fatalf("kind filter returned %+v", byKind)
	}
	inRange, err := j.Query(base.Add(90*time.Second), time.Now(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(inRange) != 1 {
		t.Fatalf("range filter returned %d, want 1", len(inRange))
	}

	first, last, count, err := j.Span()
	if err != nil {
		t.Fatal(err)
	}
	if count != 3 || first.After(last) {
		t.Fatalf("Span = %v, %v, %d", first, last, count)
	}

	// Prune keeps the retention window, drops the rest.
	if err := j.Prune(59 * time.Minute); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	after, err := j.Query(time.Time{}, time.Time{}, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 2 {
		t.Fatalf("after Prune: %d events, want 2", len(after))
	}
}
