package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/audit"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/runtime"
)

// Substitute values over HTTP (#707 PR 2b): the routes, their rules, the
// audit seam's entries and the expiry the server journals on its own.

var subRules = []SubstituteRule{
	{Tag: "Level", Value: 95.0, MaxMs: time.Hour.Milliseconds(), Role: "supervisor", AckOnStart: true, Rule: 0},
	{Tag: "Flow", Value: 1.0, MaxMs: (10 * time.Minute).Milliseconds(), Role: "operator", OperatorValue: true, Rule: 1},
}

type subClock struct{ t time.Time }

func (c *subClock) Now() time.Time { return c.t }

// newSubRuntime is the test runtime with Level and Flow substitutable, on a
// hand-moved clock.
func newSubRuntime(t *testing.T) (*runtime.Runtime, *subClock, *nio.Memory) {
	t.Helper()
	clk := &subClock{t: time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)}
	drv := nio.NewMemory()
	rt, err := runtime.New(runtime.Options{
		Program: testProgram, Driver: drv, Clock: clk,
		Inputs: []string{"Level", "Flow"}, Outputs: []string{"Out"},
		Seed:          nio.Values{"Level": 40.0, "Flow": 3.0, "SP": 65.0, "Out": 0.0},
		Substitutable: []string{"Level", "Flow"},
	})
	if err != nil {
		t.Fatal(err)
	}
	rt.Scan()
	return rt, clk, drv
}

func subServer(t *testing.T, o Options) *Server {
	t.Helper()
	rt, _, _ := newSubRuntime(t)
	o.Substitutes = subRules
	return New(rt, o)
}

func subAuditServer(t *testing.T) *Server { return subServer(t, Options{}) }

// startSub starts a substitute straight on the runtime, as a prior request
// would have.
func startSub(name string, v any) func(*testing.T, *Server) {
	return func(t *testing.T, s *Server) {
		t.Helper()
		if _, err := s.rt.Substitute(name, runtime.SubstituteRequest{Value: v, For: time.Hour, By: "op", AckOnStart: name == "Level"}); err != nil {
			t.Fatal(err)
		}
	}
}

func decodeSubs(t *testing.T, body []byte) substitutesResponse {
	t.Helper()
	var r substitutesResponse
	if err := json.Unmarshal(body, &r); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	return r
}

// A start, a list, an acknowledgement and an end, over the real handlers:
// the program runs on the substitute, the list says who, until when and
// under which rule, and the entries end exactly once.
func TestSubstituteAPILifecycle(t *testing.T) {
	s := subServer(t, Options{})
	rec := call(t, s, "GET", "/api/substitutes", "")
	if r := decodeSubs(t, rec.Body.Bytes()); rec.Code != 200 || len(r.Substitutes) != 0 || len(r.Rules) != 2 {
		t.Fatalf("GET before any start: %d %s", rec.Code, rec.Body)
	}
	rec = call(t, s, "POST", "/api/substitutes", `{"name":"Level"}`)
	if rec.Code != 200 {
		t.Fatalf("start = %d: %s", rec.Code, rec.Body)
	}
	r := decodeSubs(t, rec.Body.Bytes())
	if r.Started == nil || r.Started.Name != "Level" || r.Started.Value != 95.0 || r.Started.By != "anonymous" ||
		r.Started.GrantedMs != time.Hour.Milliseconds() || r.Started.Role != "supervisor" || !r.Started.AckOnStart || r.Started.Acked {
		t.Fatalf("started = %+v", r.Started)
	}
	s.rt.Scan()
	if got := s.rt.Tags().Real("Level"); got != 95 {
		t.Fatalf("Level = %v after the start; the program does not run on the substitute", got)
	}
	if !s.rt.Tags().Bool("Level__Substituted") || !s.rt.Tags().Bool("__SubstitutesUnacked") {
		t.Fatal("companions not raised")
	}
	id := r.Started.ID
	rec = call(t, s, "POST", "/api/substitutes/ack", `{"ids":[`+subID(id)+`]}`)
	if rec.Code != 200 || !decodeSubs(t, rec.Body.Bytes()).Substitutes[0].Acked || s.rt.Tags().Bool("__SubstitutesUnacked") {
		t.Fatalf("ack = %d %s", rec.Code, rec.Body)
	}
	if rec = call(t, s, "POST", "/api/substitutes/ack", `{"ids":[`+subID(id)+`]}`); rec.Code != http.StatusConflict {
		t.Fatalf("a second ack = %d; want 409: it acknowledged nothing", rec.Code)
	}
	if rec = call(t, s, "DELETE", "/api/substitutes/"+subID(id), ""); rec.Code != 200 || len(decodeSubs(t, rec.Body.Bytes()).Substitutes) != 0 {
		t.Fatalf("stop = %d %s", rec.Code, rec.Body)
	}
	if rec = call(t, s, "DELETE", "/api/substitutes/"+subID(id), ""); rec.Code != http.StatusNotFound {
		t.Fatalf("a second stop = %d; want 404", rec.Code)
	}
	s.rt.Scan()
	if got := s.rt.Tags().Real("Level"); got != 40 {
		t.Fatalf("Level = %v after the stop; want the live 40", got)
	}
}

func subID(n uint64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// What a start may ask: the rule's value unless the rule lets the operator
// choose, seconds up to the rule's max.
func TestSubstituteStartRules(t *testing.T) {
	for name, tc := range map[string]struct {
		body string
		code int
		want string
	}{
		"the rule's own value, spelled out": {`{"name":"Level","value":95}`, 200, ""},
		"another value, rule forbids":       {`{"name":"Level","value":5}`, 403, "does not let the operator choose"},
		"another value, rule allows":        {`{"name":"Flow","value":7.5}`, 200, ""},
		"a value the type cannot hold":      {`{"name":"Flow","value":"fast"}`, 400, "substitute Flow"},
		"seconds within the max":            {`{"name":"Level","seconds":1800}`, 200, ""},
		"seconds over the max":              {`{"name":"Level","seconds":7200}`, 400, "exceeds the rule's maximum"},
		"seconds that overflow a duration":  {`{"name":"Level","seconds":1e300}`, 400, "exceeds the rule's maximum"},
		"9.3e9 seconds":                     {`{"name":"Level","seconds":9.3e9}`, 400, "exceeds the rule's maximum"},
		"zero seconds":                      {`{"name":"Level","seconds":0}`, 400, "at least 1"},
		"under a second":                    {`{"name":"Level","seconds":0.5}`, 400, "at least 1"},
		"negative seconds":                  {`{"name":"Level","seconds":-5}`, 400, "at least 1"},
		"a tag no rule names":               {`{"name":"SP"}`, 400, "not substitutable"},
		"no name":                           {`{}`, 400, "expected"},
		"not json":                          {`nope`, 400, "expected"},
	} {
		s := subServer(t, Options{})
		rec := call(t, s, "POST", "/api/substitutes", tc.body)
		if rec.Code != tc.code || !strings.Contains(rec.Body.String(), tc.want) {
			t.Errorf("%s: %d %s; want %d containing %q", name, rec.Code, rec.Body, tc.code, tc.want)
		}
	}
	// The granted time is the rule's max by default, and seconds when given.
	s := subServer(t, Options{})
	call(t, s, "POST", "/api/substitutes", `{"name":"Flow","seconds":90}`)
	if l := s.rt.Substitutes(); len(l) != 1 || l[0].GrantedMs != 90_000 {
		t.Fatalf("granted = %+v; want 90 s", l)
	}
	call(t, s, "POST", "/api/substitutes", `{"name":"Level"}`)
	for _, e := range s.rt.Substitutes() {
		if e.Name == "Level" && e.GrantedMs != time.Hour.Milliseconds() {
			t.Fatalf("Level granted %d ms; want the rule's max, one hour", e.GrantedMs)
		}
	}
}

// An unauthenticated request is refused for that (401), whatever its body:
// the authorization comes before a decode error is reported.
func TestSubstituteAuthorizationComesBeforeTheDecodeError(t *testing.T) {
	s := subServer(t, Options{AuthToken: "s3cret"})
	for _, tc := range []struct{ method, path, body string }{
		{"POST", "/api/substitutes", `not json`},
		{"POST", "/api/substitutes", `{"name":"Level"}`},
		{"POST", "/api/substitutes/ack", `not json`},
		{"DELETE", "/api/substitutes/xyz", ``},
	} {
		if rec := call(t, s, tc.method, tc.path, tc.body); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s = %d; want 401", tc.method, tc.path, rec.Code)
		}
	}
	if len(s.rt.Substitutes()) != 0 {
		t.Fatal("an unauthenticated request started a substitute")
	}
	with := func(r *http.Request) { r.Header.Set("Authorization", "Bearer s3cret") }
	if rec := call(t, s, "POST", "/api/substitutes", `not json`, with); rec.Code != 400 {
		t.Errorf("an authorized bad body = %d; want 400", rec.Code)
	}
	if rec := call(t, s, "POST", "/api/substitutes", `{"name":"Level"}`, with); rec.Code != 200 {
		t.Errorf("an authorized start = %d", rec.Code)
	}
}

// While the audit journal cannot be written a start and an acknowledgement
// are refused (they change what the program runs on and must be recorded),
// but a stop is allowed: it is the way back to the real reading.
func TestSubstituteActionClassesWhileTheJournalIsDown(t *testing.T) {
	s := subServer(t, Options{AuditUnavailable: errors.New("audit: locked")})
	if rec := call(t, s, "POST", "/api/substitutes", `{"name":"Level"}`); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("start with no journal = %d; want 503", rec.Code)
	}
	startSub("Level", 95.0)(t, s)
	id := s.rt.Substitutes()[0].ID
	if rec := call(t, s, "POST", "/api/substitutes/ack", `{"ids":[`+subID(id)+`]}`); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("ack with no journal = %d; want 503", rec.Code)
	}
	rec := call(t, s, "DELETE", "/api/substitutes/"+subID(id), "")
	if rec.Code != 200 || rec.Header().Get(auditJournalHeader) != "down" {
		t.Fatalf("stop with no journal = %d (%s: %q); want 200 flagged down", rec.Code, auditJournalHeader, rec.Header().Get(auditJournalHeader))
	}
}

// A server that fronts a controller it does not run holds no substitute.
func TestSubstituteRefusedOnAMirror(t *testing.T) {
	s := subServer(t, Options{TagWriter: func(string, any) error { return nil }})
	if rec := call(t, s, "POST", "/api/substitutes", `{"name":"Level"}`); rec.Code != http.StatusNotImplemented {
		t.Fatalf("start on a mirror = %d; want 501", rec.Code)
	}
}

// An expiry has no request, so the server journals it itself: by system,
// from the system, the value the program ran on and the live one it went
// back to; and the entry's start and end are the routes' own entries.
func TestSubstituteExpiryIsJournalled(t *testing.T) {
	rt, clk, drv := newSubRuntime(t)
	s := New(rt, Options{Substitutes: subRules})
	call(t, s, "POST", "/api/substitutes", `{"name":"Flow","seconds":60}`)
	drv.WriteOutputs(nio.Values{"Flow": 3.0}) // the field moves on while the program runs on the substitute
	rt.Scan()
	clk.t = clk.t.Add(61 * time.Second)
	rt.Scan()
	var e audit.Entry
	deadline := time.Now().Add(5 * time.Second)
	for e.Action == "" && time.Now().Before(deadline) {
		for _, x := range s.audit.ring.Recent() {
			if x.Action == "substitute.expire" {
				e = x
			}
		}
		time.Sleep(time.Millisecond)
	}
	if e.Action == "" {
		t.Fatalf("no substitute.expire entry: %+v", s.audit.ring.Recent())
	}
	if e.User != "system" || e.Source != audit.SourceSystem || e.Target != "Flow" || e.Result != audit.ResultOK ||
		e.Detail["reason"] != runtime.SubstituteReasonExpired || e.After.(map[string]any)["live"] != 3.0 {
		t.Fatalf("expiry entry = %+v", e)
	}
	n := 0
	for _, x := range s.audit.ring.Recent() {
		if strings.HasPrefix(x.Action, "substitute.") {
			n++
		}
	}
	if n != 2 { // the start, and the expiry: the runtime's own end is not a second one
		t.Fatalf("%d substitute entries; want the start and the expiry", n)
	}
}

// Starting an address that already has a substitute replaces it. The
// journal's "before" is the LIVE reading (not the substitute the store
// shows), and the start names the entry it replaced, whose end is not a
// request of its own.
func TestSubstituteReplaceIsJournalledAgainstTheLiveReading(t *testing.T) {
	s := subServer(t, Options{})
	first := call(t, s, "POST", "/api/substitutes", `{"name":"Flow","value":7}`)
	if first.Code != 200 {
		t.Fatalf("first start = %d %s", first.Code, first.Body)
	}
	old := decodeSubs(t, first.Body.Bytes()).Started
	s.rt.Scan()
	if rec := call(t, s, "POST", "/api/substitutes", `{"name":"Flow","value":9}`); rec.Code != 200 {
		t.Fatalf("second start = %d %s", rec.Code, rec.Body)
	}
	es := s.audit.ring.Recent()
	e := es[0] // newest first
	before, _ := e.Before.(map[string]any)
	if e.Action != "substitute.start" || before["value"] != 3.0 || before["quality"] != "good" {
		t.Fatalf("before = %v on %s; want the live 3 / good, not the substitute", e.Before, e.Action)
	}
	rep, _ := e.Detail["replaces"].(map[string]any)
	if rep == nil || fmt.Sprint(rep["value"]) != "7" || rep["by"] != "anonymous" || fmt.Sprint(rep["id"]) != fmt.Sprint(old.ID) {
		t.Fatalf("detail.replaces = %v; want the replaced entry (id %d, value 7, by anonymous)", e.Detail["replaces"], old.ID)
	}
	// A first start replaces nothing.
	if first := es[len(es)-1]; first.Detail["replaces"] != nil {
		t.Fatalf("the first start names a replaced entry: %v", first.Detail)
	}
}
