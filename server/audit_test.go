package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/audit"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/leader"
	"github.com/joyautomation/nautilus/runtime"
)

// writeRoute matches a route registration whose method changes something.
var writeRoute = regexp.MustCompile(`mux\.Handle(?:Func)?\("((?:POST|PUT|PATCH|DELETE) [^"]+)"`)

// writeRoutes scans server/'s sources for the write routes: the pattern of
// each, and whether its line wraps the handler in s.audited.
func writeRoutes(t *testing.T) map[string]bool {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(src), "\n") {
			if m := writeRoute.FindStringSubmatch(line); m != nil {
				out[m[1]] = strings.Contains(line, "s.audited(")
			}
		}
	}
	if len(out) < 12 {
		t.Fatalf("the scan found %d write routes, want at least the 12 of #709's PR 3: %v", len(out), out)
	}
	return out
}

// Every route that changes something records its entry through the
// seam. A new write route registered without s.audited fails here.
func TestEveryWriteRouteIsAudited(t *testing.T) {
	for pattern, wrapped := range writeRoutes(t) {
		if !wrapped {
			t.Errorf("%s is registered without s.audited: wrap it, and add its rows to auditRows", pattern)
		}
	}
}

// auditFixture is everything the write routes need: tags, an active
// alarm, online edits, a program history, and an SFC chart on its own
// runtime.
func auditServer(t *testing.T, o Options) *Server {
	t.Helper()
	eng := newTestAlarms(t, map[string]any{"HiTempAlm": true})
	eng.Evaluate()
	o.Alarms = eng
	o.AlarmShelveTimes = []time.Duration{5 * time.Minute}
	o.OnlineEdits = true
	h := testHistory()
	o.History = func() *ProgramHistory { return h }
	o.SourcesAt = sourcesFromHistory(h)
	return New(newTestRuntime(t), o)
}

func sfcServer(t *testing.T) *Server {
	t.Helper()
	rt, err := runtime.New(runtime.Options{Program: chartProgram, Tags: []runtime.TagDef{runtime.Setpoint("Go", false)}})
	if err != nil {
		t.Fatal(err)
	}
	rt.Scan()
	return New(rt)
}

type auditRow struct {
	name    string
	pattern string // the route registration this row drives
	server  func(*testing.T) *Server
	prep    func(*testing.T, *Server)
	method  string
	path    string
	body    string
	header  map[string]string
	// The one entry the request must leave.
	action, target, user, source, result string
	before, after                        any // checked when non-nil
	check                                func(*testing.T, audit.Entry)
}

func plain(o Options) func(*testing.T) *Server {
	return func(t *testing.T) *Server { return auditServer(t, o) }
}

// auditRows drives every write route, accepted and refused, through the
// real handler.
func auditRows() []auditRow {
	none := plain(Options{})
	tok := plain(Options{AuthToken: "s3cret"})
	return []auditRow{
		{name: "tag write", pattern: "POST /api/tags", server: none,
			method: "POST", path: "/api/tags", body: `{"name":"SP","value":70}`,
			action: "tag.write", target: "SP", user: "anonymous", source: "api", result: "ok", before: 65.0, after: 70.0},
		{name: "tag write to an input", pattern: "POST /api/tags", server: none,
			method: "POST", path: "/api/tags", body: `{"name":"Level","value":1}`,
			action: "tag.write", target: "Level", user: "anonymous", source: "api", result: "refused",
			check: wantReason("driver-owned input")},
		{name: "tag write with the token, from the IDE", pattern: "POST /api/tags", server: tok,
			method: "POST", path: "/api/tags", body: `{"name":"SP","value":71}`,
			header: map[string]string{"Authorization": "Bearer s3cret", "X-Nautilus-Client": "ide"},
			action: "tag.write", target: "SP", user: "token", source: "ide", result: "ok"},
		{name: "tag write with a wrong token", pattern: "POST /api/tags", server: tok,
			method: "POST", path: "/api/tags", body: `{"name":"SP","value":71}`,
			header: map[string]string{"X-Nautilus-Token": "guess"},
			action: "tag.write", target: "SP", user: "anonymous", source: "api", result: "refused",
			check: func(t *testing.T, e audit.Entry) {
				if strings.Contains(fmt.Sprint(e), "guess") {
					t.Errorf("the presented token is in the entry: %+v", e)
				}
			}},
		{name: "tag write from a cross-origin page", pattern: "POST /api/tags", server: none,
			method: "POST", path: "/api/tags", body: `{"name":"SP","value":1}`,
			header: map[string]string{"Origin": "http://evil.example"},
			action: "tag.write", target: "SP", user: "anonymous", source: "hmi", result: "refused"},
		{name: "tag write through a facade's TagWriter", pattern: "POST /api/tags",
			server: plain(Options{TagWriter: func(string, any) error { return nil }}),
			method: "POST", path: "/api/tags", body: `{"name":"SP","value":3}`,
			action: "tag.write", target: "SP", user: "anonymous", source: "api", result: "ok", after: 3.0},
		{name: "tag write a facade's controller refuses", pattern: "POST /api/tags",
			server: plain(Options{TagWriter: func(string, any) error { return errors.New("controller offline") }}),
			method: "POST", path: "/api/tags", body: `{"name":"SP","value":3}`,
			action: "tag.write", target: "SP", user: "anonymous", source: "api", result: "error",
			check: wantReason("controller offline")},

		{name: "force", pattern: "POST /api/forces", server: none,
			method: "POST", path: "/api/forces", body: `{"name":"SP","value":12}`,
			action: "force.set", target: "SP", user: "anonymous", source: "api", result: "ok", after: 12.0},
		{name: "force without a value", pattern: "POST /api/forces", server: none,
			method: "POST", path: "/api/forces", body: `{"name":"SP"}`,
			action: "force.set", target: "SP", user: "anonymous", source: "api", result: "refused"},
		{name: "unforce", pattern: "DELETE /api/forces/{name}", server: none, prep: force("SP", 5.0),
			method: "DELETE", path: "/api/forces/SP",
			action: "force.clear", target: "SP", user: "anonymous", source: "api", result: "ok", before: 5.0},
		{name: "unforce what is not forced", pattern: "DELETE /api/forces/{name}", server: none,
			method: "DELETE", path: "/api/forces/SP",
			action: "force.clear", target: "SP", user: "anonymous", source: "api", result: "refused"},
		{name: "clear every force", pattern: "POST /api/forces/clear", server: none, prep: force("SP", 5.0),
			method: "POST", path: "/api/forces/clear",
			action: "force.clear-all", user: "anonymous", source: "api", result: "ok", before: map[string]any{"SP": 5.0}},
		{name: "clear every force, wrong token", pattern: "POST /api/forces/clear", server: tok,
			method: "POST", path: "/api/forces/clear",
			action: "force.clear-all", user: "anonymous", source: "api", result: "refused"},

		{name: "sfc step", pattern: "POST /api/sfc/step", server: sfcServer,
			method: "POST", path: "/api/sfc/step", body: `{"step":"Fill","pou":"Batch"}`,
			action: "sfc.step", target: "Batch.Fill", user: "anonymous", source: "api", result: "ok"},
		{name: "sfc step to no step", pattern: "POST /api/sfc/step", server: sfcServer,
			method: "POST", path: "/api/sfc/step", body: `{"step":"Nowhere"}`,
			action: "sfc.step", target: "Nowhere", user: "anonymous", source: "api", result: "refused"},
		{name: "sfc transition", pattern: "POST /api/sfc/transition", server: sfcServer,
			method: "POST", path: "/api/sfc/transition", body: `{"transition":"Start"}`,
			action: "sfc.transition", target: "Start", user: "anonymous", source: "api", result: "ok"},
		{name: "sfc transition, no name", pattern: "POST /api/sfc/transition", server: sfcServer,
			method: "POST", path: "/api/sfc/transition", body: `{}`,
			action: "sfc.transition", user: "anonymous", source: "api", result: "refused"},

		{name: "ack", pattern: "POST /api/alarms/ack", server: none,
			method: "POST", path: "/api/alarms/ack", body: `{"ids":["HiTemp"],"by":"op1"}`,
			action: "alarm.ack", target: "HiTemp", user: "anonymous", source: "api", result: "ok",
			check: func(t *testing.T, e audit.Entry) {
				if e.Detail["by"] != "op1" {
					t.Errorf("detail.by = %v, want the name the client gave", e.Detail["by"])
				}
			}},
		{name: "ack nothing", pattern: "POST /api/alarms/ack", server: none,
			method: "POST", path: "/api/alarms/ack", body: `{"ids":[],"by":"op1"}`,
			action: "alarm.ack", user: "anonymous", source: "api", result: "refused"},
		{name: "shelve", pattern: "POST /api/alarms/shelve", server: none,
			method: "POST", path: "/api/alarms/shelve", body: `{"id":"HiTemp","seconds":300}`,
			action: "alarm.shelve", target: "HiTemp", user: "anonymous", source: "api", result: "ok",
			after: map[string]any{"seconds": 300.0}},
		{name: "shelve with no time", pattern: "POST /api/alarms/shelve", server: none,
			method: "POST", path: "/api/alarms/shelve", body: `{"id":"HiTemp"}`,
			action: "alarm.shelve", target: "HiTemp", user: "anonymous", source: "api", result: "refused"},
		{name: "unshelve", pattern: "POST /api/alarms/unshelve", server: none, prep: shelve("HiTemp"),
			method: "POST", path: "/api/alarms/unshelve", body: `{"id":"HiTemp"}`,
			action: "alarm.unshelve", target: "HiTemp", user: "anonymous", source: "api", result: "ok"},
		{name: "unshelve no alarm", pattern: "POST /api/alarms/unshelve", server: none,
			method: "POST", path: "/api/alarms/unshelve", body: `{"id":"Nope"}`,
			action: "alarm.unshelve", target: "Nope", user: "anonymous", source: "api", result: "refused"},

		{name: "online edit", pattern: "PUT /api/program", server: none,
			method: "PUT", path: "/api/program", body: `{"source":` + jsonString(editedProgram) + `}`,
			action: "program.edit", target: "main", user: "anonymous", source: "api", result: "ok",
			check: func(t *testing.T, e audit.Entry) {
				if e.Before == nil || e.After == nil || e.Before == e.After {
					t.Errorf("before %v, after %v: want the two program hashes", e.Before, e.After)
				}
				if e.Detail["bytes"] != len(editedProgram) || strings.Contains(fmt.Sprint(e), "SP - Level") {
					t.Errorf("want the source's length and hash, never the source: %+v", e)
				}
			}},
		{name: "online edit while edits are off", pattern: "PUT /api/program",
			server: func(t *testing.T) *Server { return New(newTestRuntime(t)) },
			method: "PUT", path: "/api/program", body: `{"source":` + jsonString(editedProgram) + `}`,
			action: "program.edit", target: "main", user: "anonymous", source: "api", result: "refused",
			check: wantReason("online edits are disabled")},
		{name: "rollback", pattern: "POST /api/program/rollback", server: none, prep: edit(editedProgram),
			method: "POST", path: "/api/program/rollback",
			action: "program.rollback", target: "main", user: "anonymous", source: "api", result: "ok",
			check: func(t *testing.T, e audit.Entry) {
				if e.Before == nil || e.After == nil || e.Before == e.After {
					t.Errorf("before %v, after %v: want the hash rolled back from, and to", e.Before, e.After)
				}
			}},
		{name: "rollback with nothing to go back to", pattern: "POST /api/program/rollback", server: none,
			method: "POST", path: "/api/program/rollback",
			action: "program.rollback", target: "main", user: "anonymous", source: "api", result: "refused"},
		{name: "activate", pattern: "POST /api/program/activate", server: none,
			method: "POST", path: "/api/program/activate", body: `{"sha":"bbbb111122223333bbbb111122223333bbbb1111"}`,
			action: "program.activate", target: "bbbb111122223333bbbb111122223333bbbb1111", user: "anonymous", source: "api", result: "ok"},
		{name: "activate an unknown commit", pattern: "POST /api/program/activate", server: none,
			method: "POST", path: "/api/program/activate", body: `{"sha":"cccc"}`,
			action: "program.activate", target: "cccc", user: "anonymous", source: "api", result: "refused"},
		{name: "activate with no history", pattern: "POST /api/program/activate",
			server: func(t *testing.T) *Server { return New(newTestRuntime(t), Options{OnlineEdits: true}) },
			method: "POST", path: "/api/program/activate", body: `{"sha":"cccc"}`,
			action: "program.activate", target: "cccc", user: "anonymous", source: "api", result: "error"},
	}
}

func wantReason(sub string) func(*testing.T, audit.Entry) {
	return func(t *testing.T, e audit.Entry) {
		if r, _ := e.Detail["reason"].(string); !strings.Contains(r, sub) {
			t.Errorf("detail.reason = %q, want it to say %q", r, sub)
		}
	}
}

func force(name string, v any) func(*testing.T, *Server) {
	return func(t *testing.T, s *Server) {
		if err := s.rt.Tags().Force(name, v); err != nil {
			t.Fatal(err)
		}
	}
}

func shelve(id string) func(*testing.T, *Server) {
	return func(t *testing.T, s *Server) {
		if err := s.alarms.Shelve(id, time.Now().Add(time.Minute), "op1"); err != nil {
			t.Fatal(err)
		}
	}
}

func edit(src string) func(*testing.T, *Server) {
	return func(t *testing.T, s *Server) {
		if _, err := s.rt.Program().SwapWarm(src); err != nil {
			t.Fatal(err)
		}
	}
}

// Exactly one entry per write path, accepted and refused, with the right
// action, target, principal, source and result; and every write route the
// scan finds has an accepted row and a refused one here.
func TestEveryWritePathJournalsExactlyOneEntry(t *testing.T) {
	covered := map[string]map[string]bool{}
	for _, row := range auditRows() {
		if covered[row.pattern] == nil {
			covered[row.pattern] = map[string]bool{}
		}
		covered[row.pattern][row.result] = true
		t.Run(row.name, func(t *testing.T) {
			srv := row.server(t)
			if row.prep != nil {
				row.prep(t, srv)
			}
			before := len(srv.audit.ring.Recent())
			rec := call(t, srv, row.method, row.path, row.body, func(r *http.Request) {
				for k, v := range row.header {
					r.Header.Set(k, v)
				}
			})
			es := srv.audit.ring.Recent()
			if len(es)-before != 1 {
				t.Fatalf("%s %s answered %d and left %d entries, want exactly 1: %+v", row.method, row.path, rec.Code, len(es)-before, es)
			}
			e := es[0]
			got := []string{e.Action, e.Target, e.User, e.Source, e.Result}
			want := []string{row.action, row.target, row.user, row.source, row.result}
			if strings.Join(got, "|") != strings.Join(want, "|") {
				t.Errorf("entry action|target|user|source|result = %s, want %s (status %d: %s)",
					strings.Join(got, "|"), strings.Join(want, "|"), rec.Code, rec.Body)
			}
			if e.ID == "" || e.TS.IsZero() || e.From == "" {
				t.Errorf("entry lacks id, ts or from: %+v", e)
			}
			if row.before != nil && fmt.Sprint(e.Before) != fmt.Sprint(row.before) {
				t.Errorf("before = %v, want %v", e.Before, row.before)
			}
			if row.after != nil && fmt.Sprint(e.After) != fmt.Sprint(row.after) {
				t.Errorf("after = %v, want %v", e.After, row.after)
			}
			if row.result != audit.ResultOK && e.Detail["reason"] == "" {
				t.Errorf("a %s entry has no reason: %+v", row.result, e)
			}
			if row.check != nil {
				row.check(t, e)
			}
		})
	}
	var missing []string
	for pattern := range writeRoutes(t) {
		if !covered[pattern][audit.ResultOK] || !covered[pattern][audit.ResultRefused] {
			missing = append(missing, pattern)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("write routes without an accepted and a refused row in auditRows: %v", missing)
	}
}

// Reads are not actions: they leave nothing.
func TestReadsAreNotJournalled(t *testing.T) {
	srv := auditServer(t, Options{})
	for _, p := range []string{"/api/state", "/api/meta", "/api/forces", "/api/alarms", "/api/program", "/api/audit"} {
		call(t, srv, "GET", p, "")
	}
	if es := srv.audit.ring.Recent(); len(es) != 0 {
		t.Fatalf("reads left entries: %+v", es)
	}
}

// The entry is recorded before the client is answered, and the answer is
// the handler's, unchanged.
func TestEntryIsRecordedBeforeTheAnswer(t *testing.T) {
	rec := &orderRecorder{}
	srv := New(newTestRuntime(t), Options{Audit: rec})
	w := &orderWriter{ResponseWriter: httptest.NewRecorder(), rec: rec}
	req := httptest.NewRequest("POST", "http://localhost/api/tags", strings.NewReader(`{"name":"SP","value":70}`))
	srv.Handler().ServeHTTP(w, req)
	if rec.n != 1 || !w.recordedFirst {
		t.Fatalf("recorded %d, answered after the record: %v", rec.n, w.recordedFirst)
	}
	if code := w.ResponseWriter.(*httptest.ResponseRecorder).Code; code != http.StatusNoContent {
		t.Errorf("status %d, want the handler's 204", code)
	}
}

type orderRecorder struct {
	mu sync.Mutex
	n  int
}

func (o *orderRecorder) Record(e audit.Entry) (audit.Entry, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.n++
	return e, nil
}

type orderWriter struct {
	http.ResponseWriter
	rec           *orderRecorder
	recordedFirst bool
}

func (w *orderWriter) WriteHeader(code int) {
	w.rec.mu.Lock()
	w.recordedFirst = w.rec.n == 1
	w.rec.mu.Unlock()
	w.ResponseWriter.WriteHeader(code)
}

// flaky fails while down is set.
type flaky struct {
	mu   sync.Mutex
	down bool
	got  []audit.Entry
}

func (f *flaky) Record(e audit.Entry) (audit.Entry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return e, errors.New("disk full")
	}
	f.got = append(f.got, e)
	return e, nil
}

// While the journal cannot be written the action is still allowed (the
// interim policy until design §6 Q5), the entry is in the ring, and it is
// written, with the same id and in order, once the journal recovers.
func TestJournalFailureAllowsTheActionAndCatchesUp(t *testing.T) {
	logs := captureLog(t)
	j := &flaky{down: true}
	srv := New(newTestRuntime(t), Options{Audit: j})
	if rec := call(t, srv, "POST", "/api/tags", `{"name":"SP","value":70}`); rec.Code != http.StatusNoContent {
		t.Fatalf("write while the journal is down = %d, want it allowed", rec.Code)
	}
	if v, _ := srv.rt.Tags().ReadPath("SP"); v != 70.0 {
		t.Fatalf("SP = %v: the write did not land", v)
	}
	kept := srv.audit.ring.Recent()
	if len(kept) != 1 || kept[0].Target != "SP" {
		t.Fatalf("the ring has %+v, want the entry the journal could not take", kept)
	}
	if !strings.Contains(logs.String(), "audit journal: unwritable") {
		t.Errorf("the failure was not logged: %q", logs.String())
	}
	j.mu.Lock()
	j.down = false
	j.mu.Unlock()
	srv.audit.tick()
	call(t, srv, "POST", "/api/tags", `{"name":"SP","value":71}`)
	if len(j.got) != 2 || j.got[0].ID != kept[0].ID || j.got[1].After != 71.0 {
		t.Fatalf("journal after recovery = %+v, want the kept entry (same id) then the new one", j.got)
	}
}

// Refusals from one address are journalled in full five times a minute,
// then summed up in one "refused ×n" entry when the window closes, even if
// nothing else happens. Accepted actions are never coalesced.
func TestRefusalsAreCoalesced(t *testing.T) {
	srv := New(newTestRuntime(t))
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	srv.audit.now = func() time.Time { return now }
	for i := 0; i < 8; i++ {
		call(t, srv, "POST", "/api/tags", `{"name":"Level","value":1}`, func(r *http.Request) {
			r.RemoteAddr = fmt.Sprintf("192.0.2.9:%d", 40000+i) // a new port each time
		})
	}
	if n := len(srv.audit.ring.Recent()); n != refuseFull {
		t.Fatalf("8 refusals in a minute left %d entries, want %d", n, refuseFull)
	}
	// Another address has its own allowance.
	call(t, srv, "POST", "/api/tags", `{"name":"Level","value":1}`, func(r *http.Request) { r.RemoteAddr = "192.0.2.10:1" })
	if n := len(srv.audit.ring.Recent()); n != refuseFull+1 {
		t.Fatalf("another address's refusal: %d entries", n)
	}
	now = now.Add(refuseWindow)
	srv.audit.tick()
	s := srv.audit.ring.Recent()[0]
	if s.Result != audit.ResultRefused || s.Action != "tag.write" || s.Detail["coalesced"] != 3 ||
		!strings.Contains(fmt.Sprint(s.Detail["reason"]), "driver-owned input") || hostOf(s.From) != "192.0.2.9" {
		t.Fatalf("summary = %+v, want refused ×3 from 192.0.2.9 with the reason", s)
	}
	srv.audit.tick()
	if n := len(srv.audit.ring.Recent()); n != refuseFull+2 {
		t.Fatalf("a second tick wrote again: %d entries", n)
	}
	for i := 0; i < 2*refuseFull; i++ {
		call(t, srv, "POST", "/api/tags", fmt.Sprintf(`{"name":"SP","value":%d}`, i))
	}
	if n := len(srv.audit.ring.Recent()); n != 3*refuseFull+2 {
		t.Fatalf("accepted writes were coalesced: %d entries", n)
	}
}

// A value that is not a number is journalled as one ("NaN", "+Inf"), in
// a scalar before and inside a struct's, and GET /api/audit still
// answers.
func TestNonFiniteBeforeIsJournalled(t *testing.T) {
	rt, err := runtime.New(runtime.Options{
		Program: testProgram, Driver: nio.NewMemory(),
		Inputs: []string{"Level"}, Outputs: []string{"Out"},
		Seed: nio.Values{"Level": 1.0, "SP": math.Inf(1), "Out": 0.0},
	})
	if err != nil {
		t.Fatal(err)
	}
	rt.Scan()
	dir := t.TempDir()
	j, err := audit.OpenFile(dir, audit.FileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	srv := New(rt, Options{Audit: j})
	if rec := call(t, srv, "POST", "/api/tags", `{"name":"SP","value":1}`); rec.Code != http.StatusNoContent {
		t.Fatalf("write = %d %s", rec.Code, rec.Body)
	}
	udt := New(newUDTRuntime(t), Options{Audit: j})
	if err := udt.rt.Tags().SetPath("P101.Speed", math.NaN()); err != nil {
		t.Fatal(err)
	}
	if rec := call(t, udt, "POST", "/api/tags", `{"name":"P101","value":{"Speed":1}}`); rec.Code != http.StatusNoContent {
		t.Fatalf("struct write = %d %s", rec.Code, rec.Body)
	}
	b, _ := os.ReadFile(filepath.Join(dir, mustOnlyFile(t, dir)))
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"before":"+Inf"`) || !strings.Contains(lines[1], `"Speed":"NaN"`) {
		t.Fatalf("journal:\n%s", b)
	}
	for _, s := range []*Server{srv, udt} {
		if rec := call(t, s, "GET", "/api/audit", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), "NaN") && !strings.Contains(rec.Body.String(), "+Inf") {
			t.Fatalf("GET /api/audit = %d %s", rec.Code, rec.Body)
		}
	}
	if r, err := audit.Verify(dir); err != nil || !r.OK() {
		t.Fatalf("verify: %+v %v", r, err)
	}
}

func mustOnlyFile(t *testing.T, dir string) string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil || len(es) != 1 {
		t.Fatalf("journal dir: %v %v", es, err)
	}
	return es[0].Name()
}

// GET /api/audit serves the recent entries newest first, filtered, and
// needs the token when one is set.
func TestAuditEndpoint(t *testing.T) {
	srv := New(newTestRuntime(t), Options{AuthToken: "s3cret"})
	auth := func(r *http.Request) { r.Header.Set("Authorization", "Bearer s3cret") }
	call(t, srv, "POST", "/api/tags", `{"name":"SP","value":1}`, auth)
	call(t, srv, "POST", "/api/tags", `{"name":"SP","value":2}`) // no token: refused
	call(t, srv, "POST", "/api/forces", `{"name":"SP","value":3}`, auth)

	if rec := call(t, srv, "GET", "/api/audit", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("GET /api/audit without the token = %d, want 401", rec.Code)
	}
	get := func(q string) auditResponse {
		t.Helper()
		rec := call(t, srv, "GET", "/api/audit"+q, "", auth)
		if rec.Code != 200 {
			t.Fatalf("GET /api/audit%s = %d %s", q, rec.Code, rec.Body)
		}
		var out auditResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	all := get("")
	if len(all.Entries) != 3 || all.Entries[0].Action != "force.set" || all.Truncated {
		t.Fatalf("all = %+v", all)
	}
	if got := get("?action=tag.write&result=refused"); len(got.Entries) != 1 || got.Entries[0].User != "anonymous" {
		t.Fatalf("refused tag writes = %+v", got)
	}
	if got := get("?user=token&limit=1"); len(got.Entries) != 1 || !got.Truncated || got.Entries[0].Action != "force.set" {
		t.Fatalf("limit 1 = %+v", got)
	}
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	if got := get("?from=" + future); len(got.Entries) != 0 {
		t.Fatalf("from the future = %+v", got)
	}
	if rec := call(t, srv, "GET", "/api/audit?limit=0", "", auth); rec.Code != http.StatusBadRequest {
		t.Fatalf("limit=0 = %d", rec.Code)
	}
}

// A write sent to a standby is journalled once, by the leader that
// applies it, and not by the standby that passed it on.
func TestProxiedWriteIsJournalledOnTheLeader(t *testing.T) {
	leaderSrv := New(newTestRuntime(t))
	ts := httptest.NewServer(leaderSrv.Handler())
	defer ts.Close()
	standby := New(newTestRuntime(t), Options{Cluster: fakeCluster{status: leader.Status{
		Mode: "cluster", Pod: "plc-1", Leader: "plc-0", LeaderAddr: standbyAddr(ts),
	}}})
	if rec := call(t, standby, "POST", "/api/tags", `{"name":"SP","value":70}`); rec.Code != http.StatusNoContent {
		t.Fatalf("write via the standby = %d %s", rec.Code, rec.Body)
	}
	if n := len(standby.audit.ring.Recent()); n != 0 {
		t.Errorf("the standby journalled %d entries", n)
	}
	if es := leaderSrv.audit.ring.Recent(); len(es) != 1 || es[0].Target != "SP" || es[0].Result != "ok" {
		t.Errorf("the leader's journal = %+v, want the one write", es)
	}
}

// A browser that names its client in X-Nautilus-Client passes preflight.
func TestPreflightAllowsTheClientHeader(t *testing.T) {
	srv := New(newTestRuntime(t), Options{AuthToken: "s3cret"})
	rec := call(t, srv, "OPTIONS", "/api/tags", "", func(r *http.Request) {
		r.Header.Set("Origin", "http://hmi.example")
		r.Header.Set("Access-Control-Request-Method", "POST")
	})
	if h := rec.Header().Get("Access-Control-Allow-Headers"); !strings.Contains(h, "X-Nautilus-Client") {
		t.Errorf("Access-Control-Allow-Headers = %q", h)
	}
}

// Server.Run writes a closed window's summary on its own, so a burst of
// refusals followed by silence is not lost.
func TestRunWritesTheSummaryAfterSilence(t *testing.T) {
	prev := auditUpkeep
	auditUpkeep = 5 * time.Millisecond
	t.Cleanup(func() { auditUpkeep = prev })
	srv := New(newTestRuntime(t))
	var mu sync.Mutex
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	srv.audit.now = func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	for i := 0; i < refuseFull+2; i++ {
		call(t, srv, "POST", "/api/tags", `{"name":"Level","value":1}`)
	}
	mu.Lock()
	now = now.Add(refuseWindow)
	mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Run(ctx)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if es := srv.audit.ring.Recent(); len(es) == refuseFull+1 && es[0].Detail["coalesced"] == 2 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no summary after the window closed: %+v", srv.audit.ring.Recent())
}
