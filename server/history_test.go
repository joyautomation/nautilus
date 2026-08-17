package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/runtime"
)

// The /api prefix is the server's, not the historian's: /api/history/span
// must arrive at the daemon as /history/span, body passing straight back.
func TestHistoryProxiesWithTrimmedPath(t *testing.T) {
	var gotPath, gotQuery string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		io.WriteString(w, `{"first":0}`)
	}))
	defer backend.Close()

	rt, err := runtime.New(runtime.Options{Program: "PROGRAM P END_PROGRAM"})
	if err != nil {
		t.Fatal(err)
	}
	srv := New(rt, Options{HistorianURL: backend.URL})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/history/span?from=1", nil))

	if gotPath != "/history/span" {
		t.Fatalf("historian saw path %q, want /history/span", gotPath)
	}
	if gotQuery != "from=1" {
		t.Fatalf("historian lost the query: %q", gotQuery)
	}
	if body := rec.Body.String(); !strings.Contains(body, `"first"`) {
		t.Fatalf("body did not pass through: %q", body)
	}
}

// No historian is a configuration state, not an error state — the answer
// says what to configure.
func TestHistoryWithoutHistorianIs503(t *testing.T) {
	rt, err := runtime.New(runtime.Options{Program: "PROGRAM P END_PROGRAM"})
	if err != nil {
		t.Fatal(err)
	}
	srv := New(rt)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/history", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "historian") {
		t.Fatalf("the 503 does not say what is missing: %q", rec.Body.String())
	}
}
