package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/audit"
)

// A streamed route is accepted only when its stream says so: a last
// {"event":"done"} line is ok; an error line is a failure; a stream that
// ends with progress lines only (cancelled, returned early) is a failure
// "ended without a result", and the journal gets exactly one entry each.
func TestAuditedStreamFailsClosed(t *testing.T) {
	cases := []struct {
		name, body, result, reason string
	}{
		{"done", `{"event":"progress","message":"a"}` + "\n" + `{"event":"done","applied":"online edit"}` + "\n", audit.ResultOK, ""},
		{"done then warm-up progress", `{"event":"done"}` + "\n" + `{"event":"progress","message":"warming"}` + "\n", audit.ResultOK, ""},
		{"error", `{"event":"progress","message":"a"}` + "\n" + `{"error":"controller said no","event":"error","status":500}` + "\n", audit.ResultError, "controller said no"},
		{"progress only", `{"event":"progress","message":"a"}` + "\n" + `{"event":"progress","message":"b"}` + "\n", audit.ResultError, "ended without a result"},
		{"nothing written", ``, audit.ResultError, "ended without a result"},
		{"a progress message quoting the marker", `{"event":"progress","message":"\"event\":\"done\""}` + "\n", audit.ResultError, "ended without a result"},
		{"done without its newline", `{"event":"progress"}` + "\n" + `{"event":"done"}`, audit.ResultOK, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := New(newTestRuntime(t), Options{})
			h := srv.AuditedStream("logix.deploy", nil, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/x-ndjson")
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(c.body))
			})
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/logix/deploy", strings.NewReader(`{}`)))
			if rec.Code != 200 || rec.Body.String() != c.body {
				t.Fatalf("the client got %d %q; want the stream untouched", rec.Code, rec.Body)
			}
			var es []audit.Entry
			for _, e := range srv.audit.ring.Recent() {
				if e.Action == "logix.deploy" {
					es = append(es, e)
				}
			}
			if len(es) != 1 || es[0].Result != c.result {
				t.Fatalf("entries = %+v; want one with result %s", es, c.result)
			}
			if c.reason != "" && !strings.Contains(es[0].Detail["reason"].(string), c.reason) {
				t.Fatalf("reason = %v; want %q", es[0].Detail["reason"], c.reason)
			}
		})
	}
}
