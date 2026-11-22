package pgjournal

import (
	"os"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/alarm"
)

// TestPGJournal is an integration test covering the full open / migrate /
// append / query / prune round-trip against a real Postgres. CI has no
// Postgres today, so it skips unless NAUTILUS_TEST_DATABASE_URL is set —
// the same switch hist's Store test uses. Run it locally with e.g.:
//
//	docker run --rm -p 5432:5432 -e POSTGRES_PASSWORD=postgres postgres:16
//	NAUTILUS_TEST_DATABASE_URL="postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable" \
//	  go test ./alarm/pgjournal/ -run TestPGJournal -v
func TestPGJournal(t *testing.T) {
	url := os.Getenv("NAUTILUS_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set NAUTILUS_TEST_DATABASE_URL to run this test against a real Postgres")
	}

	j, err := New(url)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer j.Close()

	// Start clean so repeated runs against the same database are
	// deterministic. This also proves the DDL is idempotent: the second run
	// of this test opens against a table that already exists.
	if _, err := j.db.Exec("DELETE FROM alarm_events"); err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	base := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
	events := []alarm.Event{
		{TS: base.UnixMilli(), ID: "RTU9_FIT.HH", Name: "Flow High High", Kind: alarm.KindActive, Priority: alarm.High, Site: "RTU9", State: "unack-active"},
		{TS: base.Add(time.Minute).UnixMilli(), ID: "RTU9_FIT.HH", Name: "Flow High High", Kind: alarm.KindAck, Priority: alarm.High, Site: "RTU9", State: "ack-active", By: "operator1"},
		{TS: base.Add(2 * time.Minute).UnixMilli(), ID: "RTU12_INT_YA", Name: "Intrusion", Kind: alarm.KindActive, Priority: alarm.Critical, Site: "RTU12", State: "unack-active"},
	}
	for _, e := range events {
		if err := j.Append(e); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	if err := j.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	got, err := j.Query(time.Time{}, time.Time{}, alarm.Filter{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("Query returned %d events, want 3", len(got))
	}
	if got[0].ID != "RTU12_INT_YA" {
		t.Errorf("Query is not newest-first: %+v", got[0])
	}
	if got[0].Priority != alarm.Critical {
		t.Errorf("priority did not round-trip: %+v", got[0])
	}

	bySite, err := j.Query(time.Time{}, time.Time{}, alarm.Filter{Sites: []string{"RTU9"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(bySite) != 2 {
		t.Fatalf("site filter returned %d, want 2", len(bySite))
	}
	byKind, err := j.Query(time.Time{}, time.Time{}, alarm.Filter{Kinds: []string{alarm.KindAck}})
	if err != nil {
		t.Fatal(err)
	}
	if len(byKind) != 1 || byKind[0].By != "operator1" {
		t.Fatalf("kind filter returned %+v", byKind)
	}
	inRange, err := j.Query(base.Add(90*time.Second), time.Now(), alarm.Filter{})
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

	// Prune keeps the retention window, drops the rest. The cutoff sits
	// half-way between the first event (an hour old) and the second (59
	// minutes old): at exactly 59 minutes the second event's survival
	// depended on the milliseconds between Append and Prune.
	if err := j.Prune(59*time.Minute + 30*time.Second); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	after, err := j.Query(time.Time{}, time.Time{}, alarm.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 2 {
		t.Fatalf("after Prune: %d events, want 2", len(after))
	}

	// A notify row (#715) keeps its own columns; a state change leaves
	// them NULL and reads back zero; ExceptKinds drops the kind.
	note := alarm.Event{TS: base.Add(3 * time.Minute).UnixMilli(), ID: "RTU12_INT_YA", Name: "Intrusion", Kind: alarm.KindNotify,
		Priority: alarm.Critical, Site: "RTU12", State: "unack-active", By: "system",
		Level: 2, To: "oidc:carol", Via: "log", Result: alarm.NotifySent, Reason: "joined late: x", Occurrence: "0123456789abcdef"}
	if err := j.Append(note); err != nil {
		t.Fatal(err)
	}
	if err := j.Flush(); err != nil {
		t.Fatal(err)
	}
	notes, err := j.Query(time.Time{}, time.Time{}, alarm.Filter{Kinds: []string{alarm.KindNotify}})
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 1 || notes[0] != note {
		t.Fatalf("notify row = %+v, want %+v", notes, note)
	}
	rest, err := j.Query(time.Time{}, time.Time{}, alarm.Filter{ExceptKinds: []string{alarm.KindNotify}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rest) != 2 || rest[0].Level != 0 || rest[0].To != "" {
		t.Fatalf("ExceptKinds notify = %+v, want the 2 state changes with empty notify fields", rest)
	}
}

// A table an older nautilus created (no notify columns) is migrated in
// place at open, and its rows still read.
func TestPGJournalMigratesOldTable(t *testing.T) {
	url := os.Getenv("NAUTILUS_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set NAUTILUS_TEST_DATABASE_URL to run this test against a real Postgres")
	}
	j, err := New(url)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`DROP TABLE IF EXISTS alarm_events`,
		`CREATE TABLE alarm_events (ts timestamptz NOT NULL, id text NOT NULL, name text, kind text NOT NULL,
		  priority text, site text, state text, "by" text)`,
		`INSERT INTO alarm_events (ts, id, kind) VALUES (now(), 'OLD', 'active')`,
	} {
		if _, err := j.db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	j.Close()

	j, err = New(url)
	if err != nil {
		t.Fatalf("open over an old table: %v", err)
	}
	defer j.Close()
	got, err := j.Query(time.Time{}, time.Time{}, alarm.Filter{})
	if err != nil || len(got) != 1 || got[0].ID != "OLD" {
		t.Fatalf("old row after migration = %+v, %v", got, err)
	}
}
