package server

import (
	stdcsv "encoding/csv"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/audit"
)

func csvRows(t *testing.T, body string) [][]string {
	t.Helper()
	rows, err := stdcsv.NewReader(strings.NewReader(body)).ReadAll()
	if err != nil {
		t.Fatalf("not CSV: %v", err)
	}
	return rows
}

// GET /api/audit.csv exports the journal's files, oldest first, past the
// ring's 1000 entries; the filters and the token work as on /api/audit; a
// cell that would run as a formula is made text.
func TestAuditCSVReachesPastTheRing(t *testing.T) {
	dir := t.TempDir()
	j, err := audit.OpenFile(dir, audit.FileOptions{MaxSize: 64 << 10})
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	srv := New(newTestRuntime(t), Options{Audit: j, AuthToken: "s3cret"})
	auth := func(r *http.Request) { r.Header.Set("Authorization", "Bearer s3cret") }
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	const n = 2500
	for i := 0; i < n; i++ {
		user := "user:alice"
		if i%5 == 0 {
			user = "user:bob"
		}
		target := fmt.Sprintf("T%d", i)
		if i == 7 {
			target = "=HYPERLINK(\"http://x\")"
		}
		if _, err := j.Record(audit.Entry{TS: base.Add(time.Duration(i) * time.Minute), User: user, Source: audit.SourceAPI, Action: "tag.write", Target: target, Result: audit.ResultOK}); err != nil {
			t.Fatal(err)
		}
	}
	// What the ring holds is only the newest of what the server itself
	// recorded; the export must not depend on it.
	if rec := call(t, srv, "GET", "/api/audit.csv", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("without the token = %d, want 401", rec.Code)
	}
	rec := call(t, srv, "GET", "/api/audit.csv", "", auth)
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/csv") || !strings.Contains(rec.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("GET /api/audit.csv = %d %q %q", rec.Code, rec.Header().Get("Content-Type"), rec.Header().Get("Content-Disposition"))
	}
	rows := csvRows(t, rec.Body.String())
	if len(rows[0]) != len(auditCSVColumns) || rows[0][0] != "ts" {
		t.Fatalf("header = %v", rows[0])
	}
	// The retention entries the journal wrote itself are not here: only what this test recorded.
	if len(rows)-1 != n || rows[1][6] != "T0" || rows[n][6] != fmt.Sprintf("T%d", n-1) {
		t.Fatalf("%d rows, first %q, last %q; want %d oldest first", len(rows)-1, rows[1][6], rows[len(rows)-1][6], n)
	}
	if rows[8][6] != "'=HYPERLINK(\"http://x\")" {
		t.Fatalf("a formula cell = %q; want it made text", rows[8][6])
	}
	// Filters: bob wrote every fifth; a time range cuts both ends; limit stops.
	rows = csvRows(t, call(t, srv, "GET", "/api/audit.csv?user=bob", "", auth).Body.String())
	if len(rows)-1 != n/5 {
		t.Fatalf("user=bob: %d rows, want %d", len(rows)-1, n/5)
	}
	from, to := base.Add(1000*time.Minute).Format(time.RFC3339), base.Add(1999*time.Minute).Format(time.RFC3339)
	rows = csvRows(t, call(t, srv, "GET", "/api/audit.csv?from="+from+"&to="+to, "", auth).Body.String())
	if len(rows)-1 != 1000 || rows[1][6] != "T1000" {
		t.Fatalf("range: %d rows, first %q; want 1000 from T1000", len(rows)-1, rows[1][6])
	}
	rows = csvRows(t, call(t, srv, "GET", "/api/audit.csv?limit=3", "", auth).Body.String())
	if len(rows)-1 != 3 {
		t.Fatalf("limit=3: %d rows", len(rows)-1)
	}
	if rec := call(t, srv, "GET", "/api/audit.csv?from=junk", "", auth); rec.Code != 400 {
		t.Fatalf("a bad from = %d, want 400", rec.Code)
	}
}

// Without a journal on disk the export is the ring (the playground).
func TestAuditCSVFromTheRing(t *testing.T) {
	srv := New(newTestRuntime(t), Options{})
	call(t, srv, "POST", "/api/tags", `{"name":"SP","value":1}`)
	rows := csvRows(t, call(t, srv, "GET", "/api/audit.csv", "").Body.String())
	if len(rows) != 2 || rows[1][5] != "tag.write" {
		t.Fatalf("rows = %v", rows)
	}
}
