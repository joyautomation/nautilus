package server

// The alarm API's contract, from the outside: five routes, 404 on a
// controller with no engine, writes gated exactly like a tag write, and a
// summary on the frame that only appears when there is one to report.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/alarm"
)

// newTestAlarms builds an engine over a tiny map of conditions the test
// drives directly — no runtime needed, since the engine reads through one
// injected function.
func newTestAlarms(t *testing.T, cond map[string]any) *alarm.Engine {
	t.Helper()
	eng, err := alarm.New(alarm.Options{
		Defs: []alarm.Def{
			{ID: "HiTemp", Tag: "HiTempAlm", Name: "High temperature",
				Priority: alarm.High, Site: "Plant", AckRequired: true, AutoClear: true, Shelvable: true},
			{ID: "LoFlow", Tag: "LoFlowAlm", Name: "Low flow",
				Priority: alarm.Medium, Site: "Plant", AckRequired: true, AutoClear: true, Shelvable: true},
		},
		Read: func(p string) (any, bool) { v, ok := cond[p]; return v, ok },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { eng.Close() })
	return eng
}

func alarmServer(t *testing.T, cond map[string]any) (*Server, *alarm.Engine) {
	t.Helper()
	eng := newTestAlarms(t, cond)
	return New(newTestRuntime(t), Options{Alarms: eng, AlarmShelveTimes: []time.Duration{5 * time.Minute}}), eng
}

func do(t *testing.T, s *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, r)
	return rec
}

// The HMI kit's AlarmClient is written against a 404: it flips
// `supported` to false and renders nothing. An empty 200 would look like a
// quiet plant, which is the one wrong answer available.
func TestAlarmRoutes404WithoutAnEngine(t *testing.T) {
	srv := New(newTestRuntime(t))
	for _, tc := range []struct{ method, path, body string }{
		{"GET", "/api/alarms", ""},
		{"GET", "/api/alarms/journal", ""},
		{"POST", "/api/alarms/ack", `{"all":true}`},
		{"POST", "/api/alarms/shelve", `{"id":"HiTemp","seconds":60}`},
		{"POST", "/api/alarms/unshelve", `{"id":"HiTemp"}`},
	} {
		rec := do(t, srv, tc.method, tc.path, tc.body)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404", tc.method, tc.path, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "alarms:") {
			t.Errorf("%s %s: the 404 should name the manifest key, got %q",
				tc.method, tc.path, rec.Body.String())
		}
	}
	// And /api/meta says so up front, so an HMI never has to take a 404 to
	// find out.
	var meta metaResponse
	rec := do(t, srv, "GET", "/api/meta", "")
	if err := json.Unmarshal(rec.Body.Bytes(), &meta); err != nil {
		t.Fatal(err)
	}
	if meta.Alarms {
		t.Error("/api/meta claims an alarm engine on a controller with none")
	}
}

func TestAlarmsListAndSummary(t *testing.T) {
	cond := map[string]any{"HiTempAlm": true, "LoFlowAlm": false}
	srv, eng := alarmServer(t, cond)
	eng.Evaluate()

	rec := do(t, srv, "GET", "/api/alarms", "")
	if rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var got alarmsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Alarms) != 1 || got.Alarms[0].ID != "HiTemp" {
		t.Fatalf("alarms = %+v, want just HiTemp", got.Alarms)
	}
	if got.Alarms[0].State != alarm.UnackActive {
		t.Errorf("state = %s, want unack-active", got.Alarms[0].State)
	}
	if got.Summary.Active != 1 || got.Summary.Unacked != 1 || got.Summary.Rev == 0 {
		t.Errorf("summary = %+v", got.Summary)
	}
	if got.Summary.ByPriority["high"] != 1 {
		t.Errorf("byPriority = %v", got.Summary.ByPriority)
	}
	if got.TS == 0 {
		t.Error("no timestamp on the response")
	}
}

// The frame carries counts, never rows — and carries nothing at all when
// there is no engine, so an older HMI sees exactly the frame it saw before.
func TestFrameCarriesAlarmSummary(t *testing.T) {
	cond := map[string]any{"HiTempAlm": true, "LoFlowAlm": false}
	srv, eng := alarmServer(t, cond)
	eng.Evaluate()

	var f Frame
	rec := do(t, srv, "GET", "/api/state", "")
	if err := json.Unmarshal(rec.Body.Bytes(), &f); err != nil {
		t.Fatal(err)
	}
	if f.Alarms == nil {
		t.Fatal("frame carries no alarms summary")
	}
	if f.Alarms.Active != 1 || f.Alarms.Worst != alarm.High {
		t.Errorf("frame summary = %+v", f.Alarms)
	}

	plain := New(newTestRuntime(t))
	rec = do(t, plain, "GET", "/api/state", "")
	if strings.Contains(rec.Body.String(), `"alarms"`) {
		t.Errorf("a controller with no engine put an alarms field on the frame: %s", rec.Body)
	}
}

func TestMetaReportsAlarmsAndShelveTimes(t *testing.T) {
	srv, _ := alarmServer(t, map[string]any{})
	var meta metaResponse
	rec := do(t, srv, "GET", "/api/meta", "")
	if err := json.Unmarshal(rec.Body.Bytes(), &meta); err != nil {
		t.Fatal(err)
	}
	if !meta.Alarms {
		t.Error("/api/meta does not report the alarm engine")
	}
	if len(meta.ShelveTimes) != 1 || meta.ShelveTimes[0] != 300 {
		t.Errorf("shelveTimes = %v, want [300] (seconds)", meta.ShelveTimes)
	}
}

func TestAckShelveAndUnshelve(t *testing.T) {
	cond := map[string]any{"HiTempAlm": true, "LoFlowAlm": true}
	srv, eng := alarmServer(t, cond)
	eng.Evaluate()

	rec := do(t, srv, "POST", "/api/alarms/ack", `{"ids":["HiTemp"],"by":"rchon"}`)
	if rec.Code != 200 {
		t.Fatalf("ack status = %d: %s", rec.Code, rec.Body)
	}
	var acked struct {
		Acked int `json:"acked"`
	}
	json.Unmarshal(rec.Body.Bytes(), &acked)
	if acked.Acked != 1 {
		t.Errorf("acked = %d, want 1", acked.Acked)
	}

	// `all` is one request, not two thousand.
	rec = do(t, srv, "POST", "/api/alarms/ack", `{"all":true,"by":"rchon"}`)
	json.Unmarshal(rec.Body.Bytes(), &acked)
	if acked.Acked != 1 {
		t.Errorf("ack-all acked = %d, want 1 (the one still unacknowledged)", acked.Acked)
	}

	// An unknown id is a 404 naming it, and nothing is half-applied.
	rec = do(t, srv, "POST", "/api/alarms/ack", `{"ids":["Nope"],"by":"x"}`)
	if rec.Code != http.StatusNotFound {
		t.Errorf("ack of an unknown id = %d, want 404", rec.Code)
	}

	rec = do(t, srv, "POST", "/api/alarms/shelve", `{"id":"LoFlow","seconds":600,"by":"rchon"}`)
	if rec.Code != 200 {
		t.Fatalf("shelve status = %d: %s", rec.Code, rec.Body)
	}
	var recd alarm.Record
	if err := json.Unmarshal(rec.Body.Bytes(), &recd); err != nil {
		t.Fatal(err)
	}
	if recd.State != alarm.Shelved || recd.ShelfBy != "rchon" || recd.ShelfUntilMs == 0 {
		t.Errorf("shelve returned %+v", recd)
	}

	rec = do(t, srv, "POST", "/api/alarms/unshelve", `{"id":"LoFlow","by":"rchon"}`)
	if rec.Code != 200 {
		t.Fatalf("unshelve status = %d: %s", rec.Code, rec.Body)
	}
	json.Unmarshal(rec.Body.Bytes(), &recd)
	if recd.State == alarm.Shelved {
		t.Errorf("still shelved after unshelve: %+v", recd)
	}

	// There is no permanent shelf: a request with neither deadline nor
	// duration is a 400 that says so.
	rec = do(t, srv, "POST", "/api/alarms/shelve", `{"id":"LoFlow","by":"rchon"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "permanent shelf") {
		t.Errorf("open-ended shelve = %d %q", rec.Code, rec.Body)
	}
}

// Writes go through authorizeWrite unchanged — the same token rules as a
// tag write, and reads stay open.
func TestAlarmWritesHonorAuth(t *testing.T) {
	cond := map[string]any{"HiTempAlm": true, "LoFlowAlm": false}
	eng := newTestAlarms(t, cond)
	eng.Evaluate()
	srv := New(newTestRuntime(t), Options{Alarms: eng, AuthToken: "s3cret"})

	for _, tc := range []struct{ path, body string }{
		{"/api/alarms/ack", `{"all":true}`},
		{"/api/alarms/shelve", `{"id":"HiTemp","seconds":60}`},
		{"/api/alarms/unshelve", `{"id":"HiTemp"}`},
	} {
		rec := do(t, srv, "POST", tc.path, tc.body)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("POST %s without a token = %d, want 401", tc.path, rec.Code)
		}
	}
	// Reads are never gated: a wall dashboard on the LAN keeps working.
	if rec := do(t, srv, "GET", "/api/alarms", ""); rec.Code != 200 {
		t.Errorf("GET /api/alarms with auth configured = %d, want 200", rec.Code)
	}

	r := httptest.NewRequest("POST", "/api/alarms/ack", strings.NewReader(`{"all":true,"by":"rchon"}`))
	r.Header.Set("X-Nautilus-Token", "s3cret")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, r)
	if rec.Code != 200 {
		t.Fatalf("authorized ack = %d: %s", rec.Code, rec.Body)
	}
}

func TestJournalQuery(t *testing.T) {
	cond := map[string]any{"HiTempAlm": true, "LoFlowAlm": false}
	srv, eng := alarmServer(t, cond)
	eng.Evaluate()
	eng.Ack([]string{"HiTemp"}, "rchon")

	var out journalResponse
	rec := do(t, srv, "GET", "/api/alarms/journal", "")
	if rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Events) != 2 {
		t.Fatalf("events = %+v, want an active and an ack", out.Events)
	}

	// Filters: comma-separated, AND across fields.
	rec = do(t, srv, "GET", "/api/alarms/journal?kind=ack&site=Plant", "")
	json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Events) != 1 || out.Events[0].Kind != "ack" {
		t.Errorf("filtered events = %+v", out.Events)
	}
	// A site nothing is at matches nothing, and answers with an empty
	// LIST rather than null — a journal page should not have to null-check.
	rec = do(t, srv, "GET", "/api/alarms/journal?site=Nowhere", "")
	if body := rec.Body.String(); !strings.Contains(body, `"events":[]`) {
		t.Errorf("empty journal = %s", body)
	}
	// The free-text filter reaches names as well as ids.
	rec = do(t, srv, "GET", "/api/alarms/journal?q=high+temp", "")
	json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Events) != 2 {
		t.Errorf("q= matched %d events, want 2", len(out.Events))
	}

	rec = do(t, srv, "GET", "/api/alarms/journal?from=nonsense", "")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("a malformed from= = %d, want 400", rec.Code)
	}
}

func TestParseTimeAcceptsBothForms(t *testing.T) {
	want := time.UnixMilli(1700000000000)
	for _, s := range []string{"1700000000000", want.UTC().Format(time.RFC3339)} {
		got, err := parseTime(s, time.Time{})
		if err != nil {
			t.Fatalf("parseTime(%q): %v", s, err)
		}
		if !got.Equal(want) {
			t.Errorf("parseTime(%q) = %s, want %s", s, got, want)
		}
	}
	if _, err := parseTime("half past four", time.Time{}); err == nil {
		t.Error("parseTime accepted prose")
	}
}
