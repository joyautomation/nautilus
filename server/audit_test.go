package server

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/joyautomation/nautilus/checkout"
	"github.com/joyautomation/nautilus/notes"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/alarm/acklink"
	"github.com/joyautomation/nautilus/audit"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/leader"
	"github.com/joyautomation/nautilus/oplog"
	"github.com/joyautomation/nautilus/runtime"
)

// writeRoutes reads server/'s sources for the route registrations: every
// Handle or HandleFunc call, on any mux, whose pattern is not a GET or HEAD
// (a pattern with no method takes every method, so it is a write route
// too), with whether its handler is an s.audited(...) call. A pattern that
// is not a string literal cannot be judged and fails the test.
func writeRoutes(t *testing.T) map[string]bool {
	t.Helper()
	return scanWriteRoutes(t, ".", 12)
}

func scanWriteRoutes(t *testing.T, dir string, atLeast int) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 2 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || (sel.Sel.Name != "Handle" && sel.Sel.Name != "HandleFunc") {
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				t.Errorf("%s: a route registered with a pattern that is not a string literal; the audit test cannot tell whether it writes", fset.Position(call.Pos()))
				return true
			}
			pattern, _ := strconv.Unquote(lit.Value)
			if method, _, ok := strings.Cut(pattern, " "); ok && (method == "GET" || method == "HEAD") {
				return true
			}
			wrapped := false
			if h, ok := call.Args[1].(*ast.CallExpr); ok {
				if hs, ok := h.Fun.(*ast.SelectorExpr); ok && hs.Sel.Name == "audited" {
					wrapped = true
				}
			}
			out[pattern] = wrapped
			return true
		})
	}
	if len(out) < atLeast {
		t.Fatalf("the scan found %d write routes, want at least %d: %v", len(out), atLeast, out)
	}
	return out
}

// The scan itself: a route with no method, one on another mux, and one
// with a method other than GET are all write routes it must see.
func TestWriteRouteScanSeesEveryRegistration(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "routes.go"), []byte(`package server

func routes(s *Server, mux, other *http.ServeMux) {
	mux.HandleFunc("GET /api/state", s.handleState)
	mux.HandleFunc("/api/anything", s.handleWriteTag)
	other.Handle("PATCH /api/x", http.HandlerFunc(s.handleWriteTag))
	mux.Handle("POST /api/tags", s.audited("tag.write", s.tagTarget, s.handleWriteTag))
}
`), 0o644)
	got := scanWriteRoutes(t, dir, 0)
	want := map[string]bool{"/api/anything": false, "PATCH /api/x": false, "POST /api/tags": true}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("scan = %v, want %v", got, want)
	}
}

// selfJournalled are the only write routes outside the audited seam:
// sign-in and sign-out, which must work for an anonymous request and while
// the journal is down, and journal themselves (auth.signin, auth.signout;
// TestSignInAndOutJournalThemselves).
var selfJournalled = map[string]bool{"POST /api/signin": true, "POST /api/signout": true, "POST /api/elevate": true}

// Every route that changes something records its entry through the
// seam. A new write route registered without s.audited fails here.
func TestEveryWriteRouteIsAudited(t *testing.T) {
	for pattern, wrapped := range writeRoutes(t) {
		if !wrapped && !selfJournalled[pattern] {
			t.Errorf("%s is registered without s.audited: wrap it, and add its rows to auditRows", pattern)
		}
	}
}

// auditFixture is everything the write routes need: tags, an active
// alarm, online edits, a program history, and an SFC chart on its own
// runtime.
func auditServer(t *testing.T, o Options) *Server {
	t.Helper()
	return auditServerRT(t, newTestRuntime(t), o)
}

func auditServerRT(t *testing.T, rt *runtime.Runtime, o Options) *Server {
	t.Helper()
	eng := newTestAlarms(t, map[string]any{"HiTempAlm": true})
	eng.Evaluate()
	o.Alarms = eng
	o.AlarmShelveTimes = []time.Duration{5 * time.Minute}
	o.OnlineEdits = true
	h := testHistory()
	o.History = func() *ProgramHistory { return h }
	o.SourcesAt = sourcesFromHistory(h)
	return New(rt, o)
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
	// also is a second permission the action needs on top of its own (an
	// import writes tags, so it needs the write permission too).
	also string
	// person marks a route only a person may use (push, #722): the tests
	// that put service tokens in the principal's place skip it, and
	// push_test.go holds its own.
	person bool
	// pathOf, when set, is the path for the server the row made (an ack
	// link's token is minted per server).
	pathOf func(*Server) string
	// reqOf, when set, finishes the request for the server the row made
	// (a provider's signed form, #715 inbound).
	reqOf func(*Server, *http.Request)
	// The one entry the request must leave.
	action, target, user, source, result string
	before, after                        any // checked when non-nil
	check                                func(*testing.T, audit.Entry)
}

func ackAll(t *testing.T, s *Server) {
	t.Helper()
	if _, err := s.alarms.Ack(nil, "prep"); err != nil {
		t.Fatal(err)
	}
}

// loggedAuditServer is an audit server whose alarm writes go through a
// pair's operator-intent log (#710), the path "ack all" takes on a
// redundant pair: it answers with the ids it resolved.
// loggedAuditApplied counts the entries the log committed for the last
// loggedAuditServer, so a row can prove the request went through the log
// and not the engine directly.
var loggedAuditApplied atomic.Int32

func loggedAuditServer(t *testing.T) *Server {
	t.Helper()
	loggedAuditApplied.Store(0)
	s := auditServer(t, Options{})
	al := NewAlarmLog(s.alarms, func() bool { return true }, noTerm)
	n, err := oplog.New(oplog.Options{Self: "a", Apply: func(e oplog.Entry) error {
		loggedAuditApplied.Add(1)
		_, err := al.Apply(e)
		return err
	}})
	if err != nil {
		t.Fatal(err)
	}
	al.Attach(n)
	s.alarmLog = al
	return s
}

// linkServer is an audit server that sends ack links (#715).
func linkServer(t *testing.T) *Server {
	s := auditServer(t, Options{})
	withLinks(t, s)
	return s
}

// mintFor is a link's token for alarm id's current occurrence.
func mintFor(s *Server, id, principal string) string {
	r, _ := s.alarms.Lookup(id)
	tok, _ := s.ackLinks.Mint(id, r.Occurrence, principal, "email", 1, time.Hour)
	return tok
}

func plain(o Options) func(*testing.T) *Server {
	return func(t *testing.T) *Server { return auditServer(t, o) }
}

// auditRows drives every write route, accepted and refused, through the
// real handler.
func auditRows() []auditRow {
	pushAs := map[string]string{} // the person's sign-in, set by the server factory
	pushSrv := func(t *testing.T) *Server {
		rig := newPushRig(t, &subStore{}, nil)
		c := signIn(t, rig.srv, "jsmith")
		pushAs["Cookie"], pushAs["Origin"] = c.Name+"="+c.Value, "http://localhost"
		return rig.srv
	}
	pushAdmin := map[string]string{}
	pushAdminSrv := func(t *testing.T) *Server {
		rig := newPushRig(t, &subStore{}, nil)
		c := signIn(t, rig.srv, "akhan")
		pushAdmin["Cookie"], pushAdmin["Origin"] = c.Name+"="+c.Value, "http://localhost"
		return rig.srv
	}
	pushBody, pushID := auditSubscription()
	noteTok := plain(Options{AuthToken: "s3cret", Notes: mustNoteStore()})
	noteAs := map[string]string{} // jsmith's sign-in, for the rows that need a person
	noteSrv := func(t *testing.T) *Server {
		rig := newNoteRig(t)
		rig.srv.noteClock = nil // the rows run on the real clock
		c := signIn(t, rig.srv, "jsmith")
		noteAs["Cookie"], noteAs["Origin"] = c.Name+"="+c.Value, "http://localhost"
		return rig.srv
	}
	seedNote := func(t *testing.T, s *Server) {
		if _, err := s.notes.Apply(notes.Revision{Note: "n-audit-1", Rev: 1, At: time.Now().UnixMilli(), By: "jsmith", Principal: "user:jsmith", Target: notes.Target{Kind: notes.KindLog}, Text: "seeded"}); err != nil {
			t.Fatal(err)
		}
	}
	// the checkout rows: akhan (an engineer) signs in; prep begins a run the
	// result and abandon rows act on
	ckHdr := map[string]string{}
	ckSrv := func(t *testing.T) *Server {
		srv, _ := ckServer(t)
		c := signIn(t, srv, "akhan")
		ckHdr["Cookie"], ckHdr["Origin"] = c.Name+"="+c.Value, "http://localhost"
		return srv
	}
	ckRun := func(t *testing.T, s *Server) {
		if _, err := ckOf(s).Store.Begin(false, ckOf(s).Sheet.Hash(), "T", "akhan", "user:akhan"); err != nil {
			t.Fatal(err)
		}
	}
	// a run another person passed in full, for the seal row (akhan signs it off)
	ckRunSealable := func(t *testing.T, s *Server) {
		ckOf(s).Evidence = realDevices
		run, err := ckOf(s).Store.Begin(false, ckOf(s).Sheet.Hash(), "T", "carol", "user:carol")
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range ckOf(s).Sheet.Points {
			if _, err := ckOf(s).Store.Record(run.ID, p.ID, checkout.Pass, "", 1, "good", "carol", "user:carol"); err != nil {
				t.Fatal(err)
			}
		}
	}
	// a run with the equipment out of service, for the force row; and one with
	// a force standing, for the release row
	ckRunOOS := func(t *testing.T, s *Server) {
		ckRun(t, s)
		if err := s.alarms.TakeOutOfService("HiTempAlm", time.Now().Add(time.Hour), "akhan", "I/O checkout"); err != nil {
			t.Fatal(err)
		}
	}
	ckRunForced := func(t *testing.T, s *Server) {
		ckRunOOS(t, s)
		run, _ := ckOf(s).Store.Current()
		if _, err := ckOf(s).Store.StartForce(run.ID, "OUT", 5.5, 60, "akhan", "user:akhan"); err != nil {
			t.Fatal(err)
		}
		gen, _, err := s.rt.Tags().ForceIfFree("Out", 5.5)
		if err != nil {
			t.Fatal(err)
		}
		ckOf(s).ck.mu.Lock()
		ckOf(s).ck.force = &ckForce{run: run.ID, point: "OUT", tag: "Out", by: "akhan", gen: gen, ready: true, until: time.Now().Add(time.Minute),
			pt: checkout.Point{ID: "OUT", Tag: "Out"}, timer: time.AfterFunc(time.Hour, func() {}), done: make(chan struct{})}
		ckOf(s).ck.mu.Unlock()
	}
	ckPath := func(tail string) func(*Server) string {
		return func(s *Server) string {
			r, _ := ckOf(s).Store.Current()
			return "/api/checkout/runs/" + r.ID + tail
		}
	}
	none := plain(Options{})
	tok := plain(Options{AuthToken: "s3cret"})
	return []auditRow{
		{name: "checkout begin", pattern: "POST /api/checkout/runs", server: ckSrv, person: true, header: ckHdr,
			method: "POST", path: "/api/checkout/runs", body: `{"rehearsal":true}`,
			action: "checkout.begin", target: "*", user: "user:akhan", source: "hmi", result: "ok"},
		{name: "checkout begin with no credential", pattern: "POST /api/checkout/runs", server: ckSrv, person: true,
			method: "POST", path: "/api/checkout/runs", body: `{}`,
			action: "checkout.begin", target: "", user: "anonymous", source: "api", result: "refused"},
		{name: "checkout result", pattern: "POST /api/checkout/runs/{id}/results", server: ckSrv, person: true, header: ckHdr, prep: ckRun,
			method: "POST", pathOf: ckPath("/results"), body: `{"point":"SP","outcome":"pass"}`,
			action: "checkout.result", target: "SP", user: "user:akhan", source: "hmi", result: "ok"},
		{name: "checkout result with no credential", pattern: "POST /api/checkout/runs/{id}/results", server: ckSrv, person: true, prep: ckRun,
			method: "POST", pathOf: ckPath("/results"), body: `{"point":"SP","outcome":"pass"}`,
			action: "checkout.result", target: "", user: "anonymous", source: "api", result: "refused"},
		{name: "checkout force", pattern: "POST /api/checkout/runs/{id}/force", server: ckSrv, person: true, header: ckHdr, prep: ckRunOOS,
			method: "POST", pathOf: ckPath("/force"), body: `{"point":"OUT","value":5.5,"seconds":30,"confirm":"OUT"}`,
			action: "checkout.force", target: "Out", user: "user:akhan", source: "hmi", result: "ok"},
		{name: "checkout force with no credential", pattern: "POST /api/checkout/runs/{id}/force", server: ckSrv, person: true, prep: ckRunOOS,
			method: "POST", pathOf: ckPath("/force"), body: `{"point":"OUT","value":5.5,"seconds":30,"confirm":"OUT"}`,
			action: "checkout.force", target: "", user: "anonymous", source: "api", result: "refused"},
		{name: "checkout release", pattern: "POST /api/checkout/runs/{id}/force/release", server: ckSrv, person: true, header: ckHdr, prep: ckRunForced,
			method: "POST", pathOf: ckPath("/force/release"), body: `{}`,
			action: "checkout.release", target: "Out", user: "user:akhan", source: "hmi", result: "ok"},
		{name: "checkout release with no credential", pattern: "POST /api/checkout/runs/{id}/force/release", server: ckSrv, person: true, prep: ckRunForced,
			method: "POST", pathOf: ckPath("/force/release"), body: `{}`,
			action: "checkout.release", target: "", user: "anonymous", source: "api", result: "refused"},
		{name: "checkout abandon", pattern: "POST /api/checkout/runs/{id}/abandon", server: ckSrv, person: true, header: ckHdr, prep: ckRun,
			method: "POST", pathOf: ckPath("/abandon"), body: `{"reason":"wrong sheet"}`,
			action: "checkout.abandon", target: "*", user: "user:akhan", source: "hmi", result: "ok"},
		{name: "checkout abandon with no credential", pattern: "POST /api/checkout/runs/{id}/abandon", server: ckSrv, person: true, prep: ckRun,
			method: "POST", pathOf: ckPath("/abandon"), body: `{"reason":"wrong sheet"}`,
			action: "checkout.abandon", target: "", user: "anonymous", source: "api", result: "refused"},
		{name: "checkout seal", pattern: "POST /api/checkout/runs/{id}/seal", server: ckSrv, person: true, header: ckHdr, prep: ckRunSealable,
			method: "POST", pathOf: ckPath("/seal"), body: `{"note":"all points walked"}`,
			action: "checkout.seal", target: "*", user: "user:akhan", source: "hmi", result: "ok",
			check: func(t *testing.T, e audit.Entry) {
				m, _ := e.After.(map[string]any)
				for _, k := range []string{"sealSha256", "exportSha256", "keyFingerprint"} {
					if v, _ := m[k].(string); len(v) < 16 {
						t.Errorf("the seal's audit entry has no %s: %v", k, e.After)
					}
				}
				if m["note"] != "all points walked" {
					t.Errorf("after.note = %v", m["note"])
				}
			}},
		{name: "checkout seal with no credential", pattern: "POST /api/checkout/runs/{id}/seal", server: ckSrv, person: true, prep: ckRunSealable,
			method: "POST", pathOf: ckPath("/seal"), body: `{}`,
			action: "checkout.seal", target: "", user: "anonymous", source: "api", result: "refused"},
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
			// Refused by the write policy: the body is never read, so the
			// entry names no target and no value.
			action: "tag.write", target: "", user: "anonymous", source: "api", result: "refused",
			check: func(t *testing.T, e audit.Entry) {
				if strings.Contains(fmt.Sprint(e), "guess") {
					t.Errorf("the presented token is in the entry: %+v", e)
				}
				if e.Before != nil || e.After != nil {
					t.Errorf("an unauthorized refusal journalled values: %+v", e)
				}
			}},
		{name: "tag write from a cross-origin page", pattern: "POST /api/tags", server: none,
			method: "POST", path: "/api/tags", body: `{"name":"SP","value":1}`,
			header: map[string]string{"Origin": "http://evil.example"},
			action: "tag.write", target: "", user: "anonymous", source: "hmi", result: "refused"},
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

		{name: "clear a duplicate mark", pattern: "POST /api/cluster/clear-duplicate", server: pairServer("done"),
			method: "POST", path: "/api/cluster/clear-duplicate", body: `{"name":"a","copyStopped":true}`,
			header: map[string]string{"Authorization": "Bearer s3cret"},
			action: "redundancy.clear-duplicate", target: "a", user: "token", source: "api", result: "ok",
			check: wantDetail("attest", copyStoppedAttest)},
		{name: "clear a duplicate mark, no credential configured", pattern: "POST /api/cluster/clear-duplicate", server: pairServerNoToken("done"),
			method: "POST", path: "/api/cluster/clear-duplicate", body: `{"name":"a","copyStopped":true}`,
			action: "redundancy.clear-duplicate", target: "a", user: "anonymous", source: "api", result: "refused",
			check: wantReason("NAUTILUS_TOKEN")},
		{name: "clear a duplicate mark, the copy not said stopped", pattern: "POST /api/cluster/clear-duplicate", server: pairServer("done"),
			method: "POST", path: "/api/cluster/clear-duplicate", body: `{"name":"a"}`,
			header: map[string]string{"Authorization": "Bearer s3cret"},
			action: "redundancy.clear-duplicate", target: "a", user: "token", source: "api", result: "refused",
			check: wantReason("copyStopped")},
		{name: "clear a duplicate mark, the check phase refuses", pattern: "POST /api/cluster/clear-duplicate", server: pairServer("refused"),
			method: "POST", path: "/api/cluster/clear-duplicate", body: `{"name":"a","copyStopped":true}`,
			header: map[string]string{"Authorization": "Bearer s3cret"},
			action: "redundancy.clear-duplicate", target: "a", user: "token", source: "api", result: "refused",
			check: wantReason("fake refused")},
		{name: "commission a pair", pattern: "POST /api/cluster/commission", server: pairServer("done"),
			method: "POST", path: "/api/cluster/commission", body: `{}`,
			header: map[string]string{"Authorization": "Bearer s3cret"},
			action: "redundancy.commission", target: "pair", user: "token", source: "api", result: "ok"},
		{name: "commission a pair, a member was commissioned", pattern: "POST /api/cluster/commission", server: pairServer("refused"),
			method: "POST", path: "/api/cluster/commission", body: `{}`,
			header: map[string]string{"Authorization": "Bearer s3cret"},
			action: "redundancy.commission", target: "pair", user: "token", source: "api", result: "refused",
			check: wantReason("fake refused")},
		{name: "commission a pair, no credential configured", pattern: "POST /api/cluster/commission", server: pairServerNoToken("done"),
			method: "POST", path: "/api/cluster/commission", body: `{}`,
			action: "redundancy.commission", target: "pair", user: "anonymous", source: "api", result: "refused",
			check: wantReason("NAUTILUS_TOKEN")},
		{name: "re-commission with a floor, box replaced", pattern: "POST /api/cluster/recommission", server: pairServer("done"),
			method: "POST", path: "/api/cluster/recommission", body: `{"floor":5000,"boxReplaced":true}`,
			header: map[string]string{"Authorization": "Bearer s3cret"},
			action: "redundancy.recommission", target: "b", user: "token", source: "api", result: "ok",
			check: wantDetail("attestBox", boxReplacedAttest)},
		{name: "re-commission, the live-copy check's members", pattern: "POST /api/cluster/recommission", server: pairServer("done"),
			method: "POST", path: "/api/cluster/recommission", body: `{"floor":5000}`,
			header: map[string]string{"Authorization": "Bearer s3cret"},
			action: "redundancy.recommission", target: "b", user: "token", source: "api", result: "ok",
			check: wantLiveChecked(func(e audit.Entry) any { return e.After })},
		{name: "re-commission refused, the live-copy check's members", pattern: "POST /api/cluster/recommission", server: pairServer("refused"),
			method: "POST", path: "/api/cluster/recommission", body: `{"floor":5000}`,
			header: map[string]string{"Authorization": "Bearer s3cret"},
			action: "redundancy.recommission", target: "b", user: "token", source: "api", result: "refused",
			check: wantLiveChecked(func(e audit.Entry) any { return e.Detail })},
		{name: "re-commission from a spare", pattern: "POST /api/cluster/recommission", server: pairServer("done"),
			method: "POST", path: "/api/cluster/recommission", body: `{"fromSpare":true,"copyStopped":true}`,
			header: map[string]string{"Authorization": "Bearer s3cret"},
			action: "redundancy.recommission", target: "b", user: "token", source: "api", result: "ok",
			check: wantDetail("attest", incumbentStoppedAttest)},
		{name: "re-commission from a spare, the incumbent not said stopped", pattern: "POST /api/cluster/recommission", server: pairServer("done"),
			method: "POST", path: "/api/cluster/recommission", body: `{"fromSpare":true}`,
			header: map[string]string{"Authorization": "Bearer s3cret"},
			action: "redundancy.recommission", target: "b", user: "token", source: "api", result: "refused",
			check: wantReason("copyStopped")},
		{name: "re-commission from a spare, cleared at one voter only", pattern: "POST /api/cluster/recommission", server: pairServer("incomplete"),
			method: "POST", path: "/api/cluster/recommission", body: `{"fromSpare":true,"copyStopped":true}`,
			header: map[string]string{"Authorization": "Bearer s3cret"},
			action: "redundancy.recommission", target: "b", user: "token", source: "api", result: "error",
			check: wantReason("fake incomplete")},
		{name: "re-commission accepting a loss", pattern: "POST /api/cluster/recommission", server: pairServer("done"),
			method: "POST", path: "/api/cluster/recommission", body: `{"floor":5000,"acceptLoss":"0123456789"}`,
			header: map[string]string{"Authorization": "Bearer s3cret"},
			action: "redundancy.recommission", target: "b", user: "token", source: "api", result: "ok",
			check: wantDetail("acceptLoss", "0123456789")},
		{name: "vouch, the picture printed", pattern: "POST /api/cluster/vouch", server: pairServer("refused"),
			method: "POST", path: "/api/cluster/vouch", body: `{}`,
			header: map[string]string{"Authorization": "Bearer s3cret"},
			action: "redundancy.vouch", target: "b", user: "token", source: "api", result: "refused",
			check: wantReason("fake refused")},
		{name: "vouch accepting a loss", pattern: "POST /api/cluster/vouch", server: pairServer("done"),
			method: "POST", path: "/api/cluster/vouch", body: `{"acceptLoss":"0123456789"}`,
			header: map[string]string{"Authorization": "Bearer s3cret"},
			action: "redundancy.vouch", target: "b", user: "token", source: "api", result: "ok",
			check: func(t *testing.T, e audit.Entry) {
				wantDetail("acceptLoss", "0123456789")(t, e)
				wantDetail("attestLoss", acceptLossAttest)(t, e)
			}},
		{name: "vouch accepting a lost log, not a digest", pattern: "POST /api/cluster/vouch", server: pairServer("done"),
			method: "POST", path: "/api/cluster/vouch", body: `{"acceptLostLog":"yes"}`,
			header: map[string]string{"Authorization": "Bearer s3cret"},
			action: "redundancy.vouch", target: "b", user: "token", source: "api", result: "refused",
			check: wantReason("ten hex digits")},
		{name: "vouch, no credential configured", pattern: "POST /api/cluster/vouch", server: pairServerNoToken("done"),
			method: "POST", path: "/api/cluster/vouch", body: `{}`,
			action: "redundancy.vouch", target: "b", user: "anonymous", source: "api", result: "refused",
			check: wantReason("NAUTILUS_TOKEN")},
		{name: "re-commission, no credential configured", pattern: "POST /api/cluster/recommission", server: pairServerNoToken("done"),
			method: "POST", path: "/api/cluster/recommission", body: `{"floor":5000}`,
			action: "redundancy.recommission", target: "b", user: "anonymous", source: "api", result: "refused",
			check: wantReason("NAUTILUS_TOKEN")},
		{name: "clear a duplicate mark, a member still marks it", pattern: "POST /api/cluster/clear-duplicate", server: pairServer("incomplete"),
			method: "POST", path: "/api/cluster/clear-duplicate", body: `{"name":"a","copyStopped":true}`,
			header: map[string]string{"Authorization": "Bearer s3cret"},
			action: "redundancy.clear-duplicate", target: "a", user: "token", source: "api", result: "error",
			check: wantReason("fake incomplete")},

		{name: "parameter write", pattern: "PUT /api/iolink/{port}/params/{ref}", server: paramAuditServer(newFakeParams()),
			method: "PUT", path: "/api/iolink/LEVEL1/params/V_Setpoint", body: `{"value":500}`,
			action: "param.write", target: "LEVEL1/V_Setpoint", user: "anonymous", source: "api", result: "ok", before: int64(100), after: int64(500)},
		{name: "parameter write to the wrong device", pattern: "PUT /api/iolink/{port}/params/{ref}", server: paramAuditServer(wrongDeviceParams()),
			method: "PUT", path: "/api/iolink/LEVEL1/params/V_Setpoint", body: `{"value":500}`,
			action: "param.write", target: "LEVEL1/V_Setpoint", user: "anonymous", source: "api", result: "refused",
			check: wantReason("WrongDevice")},
		{name: "substitute start", pattern: "POST /api/substitutes", server: subAuditServer,
			method: "POST", path: "/api/substitutes", body: `{"name":"Level"}`,
			action: "substitute.start", target: "Level", user: "anonymous", source: "api", result: "ok",
			before: map[string]any{"quality": "good", "value": 40.0},
			check: func(t *testing.T, e audit.Entry) {
				after, _ := e.After.(map[string]any)
				if after["value"] != 95.0 || after["id"] == nil || after["untilMs"] == nil {
					t.Errorf("after = %v; want the substitute's value, id and until", e.After)
				}
				if e.Detail["role"] != "supervisor" || e.Detail["ackOnStart"] != true {
					t.Errorf("detail = %v; want the rule's role and ack-on-start", e.Detail)
				}
			}},
		{name: "substitute start of a tag no rule names", pattern: "POST /api/substitutes", server: subAuditServer,
			method: "POST", path: "/api/substitutes", body: `{"name":"SP"}`,
			action: "substitute.start", target: "SP", user: "anonymous", source: "api", result: "refused",
			check: wantReason("not substitutable")},
		{name: "substitute stop", pattern: "DELETE /api/substitutes/{id}", server: subAuditServer, prep: startSub("Level", 95.0),
			method: "DELETE", path: "/api/substitutes/1",
			action: "substitute.stop", target: "Level", user: "anonymous", source: "api", result: "ok",
			check: func(t *testing.T, e audit.Entry) {
				before, _ := e.Before.(map[string]any)
				if before["value"] != 95.0 || before["live"] != 40.0 {
					t.Errorf("before = %v; want the substitute and the live value", e.Before)
				}
			}},
		{name: "substitute stop of none", pattern: "DELETE /api/substitutes/{id}", server: subAuditServer,
			method: "DELETE", path: "/api/substitutes/9",
			action: "substitute.stop", user: "anonymous", source: "api", result: "refused",
			check: wantReason("no substitute 9 is active")},
		{name: "substitute ack", pattern: "POST /api/substitutes/ack", server: subAuditServer, prep: startSub("Level", 95.0),
			method: "POST", path: "/api/substitutes/ack", body: `{"ids":[1]}`,
			action: "substitute.ack", target: "Level", user: "anonymous", source: "api", result: "ok"},
		{name: "substitute ack of nothing", pattern: "POST /api/substitutes/ack", server: subAuditServer,
			method: "POST", path: "/api/substitutes/ack", body: `{"ids":[]}`,
			action: "substitute.ack", user: "anonymous", source: "api", result: "refused"},

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
		// An ack naming an occurrence that ended is refused; the entry
		// records the occurrences it named for its own ids, no others (#715).
		{name: "ack of an ended occurrence", pattern: "POST /api/alarms/ack", server: none,
			method: "POST", path: "/api/alarms/ack", body: `{"ids":["HiTemp"],"by":"op1","occurrences":{"HiTemp":"00000000deadbeef","Padding":"x"}}`,
			action: "alarm.ack", target: "HiTemp", user: "anonymous", source: "api", result: "refused",
			check: func(t *testing.T, e audit.Entry) {
				if got := fmt.Sprint(e.Detail["occurrences"]); got != "map[HiTemp:00000000deadbeef]" {
					t.Errorf("detail.occurrences = %s, want HiTemp's alone", got)
				}
			}},
		// An acknowledge-all has the target "*"; the entry says which alarms it
		// acknowledged (one entry for the one request).
		{name: "ack all", pattern: "POST /api/alarms/ack", server: none,
			method: "POST", path: "/api/alarms/ack", body: `{"all":true,"by":"op1"}`,
			action: "alarm.ack", target: "*", user: "anonymous", source: "api", result: "ok",
			after: []string{"HiTemp"}},
		// Through a pair's log the entry names the alarms too: the ids the
		// log resolved "all" to, not the empty request.
		{name: "ack all through the log", pattern: "POST /api/alarms/ack", server: loggedAuditServer,
			method: "POST", path: "/api/alarms/ack", body: `{"all":true,"by":"op1"}`,
			action: "alarm.ack", target: "*", user: "anonymous", source: "api", result: "ok",
			after: []string{"HiTemp"},
			check: func(t *testing.T, _ audit.Entry) {
				if n := loggedAuditApplied.Load(); n != 1 {
					t.Errorf("the log committed %d entries, want 1: the ack did not go through the log", n)
				}
			}},
		// The HMI's Ack All sends ids ["*"], not "all": the same acknowledge-all,
		// on one controller and through a pair's log (it was a 404 `no alarm "*"`
		// on a pair).
		{name: "ack all as ids *", pattern: "POST /api/alarms/ack", server: none,
			method: "POST", path: "/api/alarms/ack", body: `{"ids":["*"],"by":"op1"}`,
			action: "alarm.ack", target: "*", user: "anonymous", source: "api", result: "ok",
			after: []string{"HiTemp"}},
		{name: "ack all as ids * through the log", pattern: "POST /api/alarms/ack", server: loggedAuditServer,
			method: "POST", path: "/api/alarms/ack", body: `{"ids":["*"],"by":"op1"}`,
			action: "alarm.ack", target: "*", user: "anonymous", source: "api", result: "ok",
			after: []string{"HiTemp"},
			check: func(t *testing.T, _ audit.Entry) {
				if n := loggedAuditApplied.Load(); n != 1 {
					t.Errorf("the log committed %d entries, want 1: the ack did not go through the log", n)
				}
			}},
		{name: "ack all, a second time", pattern: "POST /api/alarms/ack", server: none, prep: ackAll,
			method: "POST", path: "/api/alarms/ack", body: `{"all":true,"by":"op1"}`,
			action: "alarm.ack", target: "*", user: "anonymous", source: "api", result: "ok",
			after: []string{}},
		{name: "ack nothing", pattern: "POST /api/alarms/ack", server: none,
			method: "POST", path: "/api/alarms/ack", body: `{"ids":[],"by":"op1"}`,
			action: "alarm.ack", user: "anonymous", source: "api", result: "refused"},
		// An ack link (#715): the person it names, via: link; a link that
		// does not check is refused and journalled anonymously.
		{name: "ack by link", pattern: "POST /alarm-ack/{token}", server: linkServer, person: true,
			method: "POST", pathOf: func(s *Server) string {
				return acklink.Path + mintFor(s, "HiTemp", "alice")
			},
			action: "alarm.ack", target: "HiTemp", user: "user:alice", source: "api", result: "ok",
			check: func(t *testing.T, e audit.Entry) {
				if e.Detail["via"] != "link" || e.Detail["link"] == "" {
					t.Errorf("detail %v, want via: link and the link's id", e.Detail)
				}
			}},
		{name: "ack by a forged link", pattern: "POST /alarm-ack/{token}", server: linkServer, person: true,
			method: "POST", path: acklink.Path + "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
			action: "alarm.ack", user: "anonymous", source: "api", result: "refused"},
		// Inbound (#715): an SMS reply's code, a call's keypress, signed by
		// the provider; a request whose signature does not check is
		// journalled anonymously.
		{name: "ack by sms reply", pattern: "POST /alarm-inbound/sms", server: inboundServer, person: true,
			method: "POST", path: InboundSMSPath,
			reqOf: func(s *Server, r *http.Request) {
				occ, _ := s.alarms.Lookup("HiTemp")
				code := s.AckCode("HiTemp", occ.Occurrence, "alice")
				signed(smsToken, InboundSMSPath, url.Values{"From": {alicePhone}, "Body": {"ACK " + code}})(r)
			},
			action: "alarm.ack", target: "HiTemp", user: "user:alice", source: "api", result: "ok",
			check: func(t *testing.T, e audit.Entry) {
				if e.Detail["via"] != "sms" {
					t.Errorf("detail %v, want via: sms", e.Detail)
				}
			}},
		{name: "ack by an unsigned sms", pattern: "POST /alarm-inbound/sms", server: inboundServer, person: true,
			method: "POST", path: InboundSMSPath, body: "From=%2B15550102&Body=ACK+12345678",
			header: map[string]string{"Content-Type": "application/x-www-form-urlencoded"},
			action: "alarm.ack", user: "anonymous", source: "api", result: "refused"},
		{name: "ack by keypress", pattern: "POST /alarm-inbound/voice/{token}", server: inboundServer, person: true,
			method: "POST", pathOf: func(s *Server) string {
				occ, _ := s.alarms.Lookup("HiTemp")
				return strings.TrimPrefix(s.VoiceAckURL("HiTemp", occ.Occurrence, "alice", 1), inboundBase)
			},
			reqOf: func(s *Server, r *http.Request) {
				signed(voiceToken, r.URL.Path, url.Values{"To": {alicePhone}, "Digits": {"1"}})(r)
			},
			action: "alarm.ack", target: "HiTemp", user: "user:alice", source: "api", result: "ok",
			check: func(t *testing.T, e audit.Entry) {
				if e.Detail["via"] != "voice" {
					t.Errorf("detail %v, want via: voice", e.Detail)
				}
			}},
		{name: "ack by a forged keypress", pattern: "POST /alarm-inbound/voice/{token}", server: inboundServer, person: true,
			method: "POST", path: InboundVoicePath + "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", body: "To=%2B15550102&Digits=1",
			header: map[string]string{"Content-Type": "application/x-www-form-urlencoded", "X-Twilio-Signature": "AAAAAAAAAAAAAAAAAAAAAAAAAAA="},
			action: "alarm.ack", user: "anonymous", source: "api", result: "refused"},
		{name: "shelve", pattern: "POST /api/alarms/shelve", server: none,
			method: "POST", path: "/api/alarms/shelve", body: `{"id":"HiTemp","seconds":300}`,
			action: "alarm.shelve", target: "HiTemp", user: "anonymous", source: "api", result: "ok",
			after: map[string]any{"seconds": 300.0}},
		{name: "shelve from a chatter badge keeps its reason", pattern: "POST /api/alarms/shelve", server: none,
			method: "POST", path: "/api/alarms/shelve", body: `{"id":"HiTemp","seconds":900,"reason":"seal weeping, check Friday","via":"chatter"}`,
			action: "alarm.shelve", target: "HiTemp", user: "anonymous", source: "api", result: "ok",
			after: map[string]any{"seconds": 900.0},
			check: func(t *testing.T, e audit.Entry) {
				if e.Detail["reason"] != "seal weeping, check Friday" || e.Detail["via"] != "chatter" {
					t.Errorf("detail %v, want the reason and via: chatter", e.Detail)
				}
			}},
		{name: "shelve keeps a 200-character reason whole", pattern: "POST /api/alarms/shelve", server: none,
			method: "POST", path: "/api/alarms/shelve", body: `{"id":"HiTemp","seconds":900,"reason":"` + strings.Repeat("é", 200) + `","via":"chatter"}`,
			action: "alarm.shelve", target: "HiTemp", user: "anonymous", source: "api", result: "ok",
			after: map[string]any{"seconds": 900.0},
			check: func(t *testing.T, e audit.Entry) {
				if got, _ := e.Detail["reason"].(string); got != strings.Repeat("é", 200) {
					t.Errorf("audit reason = %d bytes, want the 200 characters whole", len(got))
				}
			}},
		{name: "shelve from a chatter badge with no reason", pattern: "POST /api/alarms/shelve", server: none,
			method: "POST", path: "/api/alarms/shelve", body: `{"id":"HiTemp","seconds":900,"reason":"  ","via":"chatter"}`,
			action: "alarm.shelve", target: "HiTemp", user: "anonymous", source: "api", result: "refused"},
		{name: "shelve with no time", pattern: "POST /api/alarms/shelve", server: none,
			method: "POST", path: "/api/alarms/shelve", body: `{"id":"HiTemp"}`,
			action: "alarm.shelve", target: "HiTemp", user: "anonymous", source: "api", result: "refused"},
		{name: "unshelve", pattern: "POST /api/alarms/unshelve", server: none, prep: shelve("HiTemp"),
			method: "POST", path: "/api/alarms/unshelve", body: `{"id":"HiTemp"}`,
			action: "alarm.unshelve", target: "HiTemp", user: "anonymous", source: "api", result: "ok"},
		{name: "unshelve no alarm", pattern: "POST /api/alarms/unshelve", server: none,
			method: "POST", path: "/api/alarms/unshelve", body: `{"id":"Nope"}`,
			action: "alarm.unshelve", target: "Nope", user: "anonymous", source: "api", result: "refused"},
		{name: "out of service", pattern: "POST /api/alarms/out-of-service", server: none,
			method: "POST", path: "/api/alarms/out-of-service", body: `{"equipment":"HiTempAlm","seconds":3600,"reason":"seal change"}`,
			action: "alarm.out-of-service", target: "HiTempAlm", user: "anonymous", source: "api", result: "ok",
			after: map[string]any{"seconds": 3600.0, "reason": "seal change"}},
		{name: "out of service with no reason", pattern: "POST /api/alarms/out-of-service", server: none,
			method: "POST", path: "/api/alarms/out-of-service", body: `{"equipment":"HiTempAlm","seconds":3600}`,
			action: "alarm.out-of-service", target: "HiTempAlm", user: "anonymous", source: "api", result: "refused"},
		{name: "in service", pattern: "POST /api/alarms/in-service", server: none, prep: outOfService("HiTempAlm"),
			method: "POST", path: "/api/alarms/in-service", body: `{"equipment":"HiTempAlm"}`,
			action: "alarm.in-service", target: "HiTempAlm", user: "anonymous", source: "api", result: "ok"},
		{name: "in service, not out", pattern: "POST /api/alarms/in-service", server: none,
			method: "POST", path: "/api/alarms/in-service", body: `{"equipment":"HiTempAlm"}`,
			action: "alarm.in-service", target: "HiTempAlm", user: "anonymous", source: "api", result: "refused"},

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

		// Live I/O configuration (#706): staged from a commit or an upload,
		// validated, applied. The upload's contents are never journalled.
		{name: "io stage from a commit", pattern: "POST /api/io/staged", server: ioSrv,
			method: "POST", path: "/api/io/staged", body: `{"sha":"abc1234"}`,
			action: "config.stage", target: "abc1234", user: "anonymous", source: "api", result: "ok",
			after: map[string]any{"id": "1", "state": "pending"}},
		{name: "io stage an upload", pattern: "POST /api/io/staged", server: ioSrv,
			method: "POST", path: "/api/io/staged", body: `{"driver":"modbus","files":{"modbus.yaml":"secret-ish"}}`,
			action: "config.stage", target: "upload", user: "anonymous", source: "api", result: "ok",
			check: func(t *testing.T, e audit.Entry) {
				if e.Detail["files"] != "modbus.yaml" || e.Detail["bytes"] != 10 || strings.Contains(fmt.Sprint(e), "secret-ish") {
					t.Errorf("upload entry: %+v", e)
				}
			}},
		{name: "io stage a commit with no snapshot", pattern: "POST /api/io/staged", server: ioSrv,
			method: "POST", path: "/api/io/staged", body: `{"sha":"deadbee"}`,
			action: "config.stage", target: "deadbee", user: "anonymous", source: "api", result: "refused"},
		{name: "io validate", pattern: "POST /api/io/staged/{id}/validate", server: ioSrv, prep: ioStaged(false),
			method: "POST", path: "/api/io/staged/1/validate",
			action: "config.validate", target: "1", user: "anonymous", source: "api", result: "ok",
			after: map[string]any{"id": "1", "state": "valid"}},
		{name: "io validate nothing", pattern: "POST /api/io/staged/{id}/validate", server: ioSrv,
			method: "POST", path: "/api/io/staged/9/validate",
			action: "config.validate", target: "9", user: "anonymous", source: "api", result: "refused"},
		{name: "io apply", pattern: "POST /api/io/staged/{id}/apply", server: ioSrv, prep: ioStaged(true),
			method: "POST", path: "/api/io/staged/1/apply",
			action: "config.apply", target: "1", user: "anonymous", source: "api", result: "ok",
			after: map[string]any{"id": "1", "state": "applied", "from": shaA, "changes": []map[string]string{}, "kept": false}},
		{name: "io apply nothing", pattern: "POST /api/io/staged/{id}/apply", server: ioSrv,
			method: "POST", path: "/api/io/staged/9/apply",
			action: "config.apply", target: "9", user: "anonymous", source: "api", result: "refused"},
		{name: "io commit an applied candidate", pattern: "POST /api/config/commit", server: ioCommitSrv, prep: ioStagedApplied,
			method: "POST", path: "/api/config/commit", body: `{"id":"1"}`,
			action: "config.commit", target: "1", user: "anonymous", source: "api", result: "ok",
			after: map[string]any{"commit": "c0ffee", "branch": "main", "files": "modbus.yaml"}},
		{name: "io commit what was not applied", pattern: "POST /api/config/commit", server: ioCommitSrv, prep: ioStaged(true),
			method: "POST", path: "/api/config/commit", body: `{"id":"1"}`,
			action: "config.commit", target: "1", user: "anonymous", source: "api", result: "refused"},
		{name: "io save a candidate for the next restart", pattern: "POST /api/config/save", server: ioSaveSrv, prep: ioStaged(true),
			method: "POST", path: "/api/config/save", body: `{"id":"1"}`,
			action: "config.save", target: "1", user: "anonymous", source: "api", result: "ok",
			after: map[string]any{"commit": "c0ffee", "branch": "main", "files": "nautilus.yaml", "pendingRestart": true}},
		{name: "io save what a restart would not fix", pattern: "POST /api/config/save", server: ioCommitSrv, prep: ioStaged(true),
			method: "POST", path: "/api/config/save", body: `{"id":"1"}`,
			action: "config.save", target: "1", user: "anonymous", source: "api", result: "refused"},
		{name: "parameter import", pattern: "POST /api/parameters/import", server: paramImportSrv,
			method: "POST", path: "/api/parameters/import", body: `{"version":1,"parameters":{"SP":70}}`,
			also: "write", action: "param.import", target: "1 parameters", user: "anonymous", source: "api", result: "ok",
			before: map[string]any{"SP": 65.0}, after: map[string]any{"SP": 70.0}},
		{name: "parameter import out of range", pattern: "POST /api/parameters/import", server: paramImportSrv,
			method: "POST", path: "/api/parameters/import", body: `{"version":1,"parameters":{"SP":95}}`,
			action: "param.import", target: "1 parameters", user: "anonymous", source: "api", result: "refused"},
		{name: "io cancel a pending revert", pattern: "POST /api/io/revert/cancel", server: ioRevertSrv,
			method: "POST", path: "/api/io/revert/cancel",
			action: "config.revert.cancel", target: "", user: "anonymous", source: "api", result: "ok"},
		{name: "io cancel with none pending", pattern: "POST /api/io/revert/cancel", server: ioSrv,
			method: "POST", path: "/api/io/revert/cancel",
			action: "config.revert.cancel", target: "", user: "anonymous", source: "api", result: "refused"},
		{name: "push confirm revocations", pattern: "POST /api/push/confirm-revocations", server: pushAdminSrv, person: true, header: pushAdmin,
			method: "POST", path: "/api/push/confirm-revocations",
			action: "push.confirm-revocations", target: "push", user: "user:akhan", source: "hmi", result: "ok", after: map[string]any{"ended": 0}},
		{name: "push confirm revocations with no credential", pattern: "POST /api/push/confirm-revocations", server: pushAdminSrv, person: true,
			method: "POST", path: "/api/push/confirm-revocations",
			action: "push.confirm-revocations", target: "", user: "anonymous", source: "api", result: "refused"},
		{name: "note add", pattern: "POST /api/notes", server: noteTok,
			method: "POST", path: "/api/notes", body: `{"target":{"kind":"tag","id":"SP"},"text":"setpoint raised"}`,
			header: map[string]string{"Authorization": "Bearer s3cret"},
			action: "note.add", target: "tag:SP", user: "token", source: "api", result: "ok"},
		{name: "note add with no credential", pattern: "POST /api/notes", server: noteTok,
			method: "POST", path: "/api/notes", body: `{"target":{"kind":"tag","id":"SP"},"text":"setpoint raised"}`,
			action: "note.add", target: "", user: "anonymous", source: "api", result: "refused"},
		{name: "note revise", pattern: "POST /api/notes/{id}/revise", server: noteSrv, person: true, header: noteAs, prep: seedNote,
			method: "POST", path: "/api/notes/n-audit-1/revise", body: `{"text":"seeded, corrected"}`,
			action: "note.revise", target: "note:n-audit-1", user: "user:jsmith", source: "hmi", result: "ok"},
		{name: "note revise with no credential", pattern: "POST /api/notes/{id}/revise", server: noteSrv, person: true, prep: seedNote,
			method: "POST", path: "/api/notes/n-audit-1/revise", body: `{"text":"seeded, corrected"}`,
			action: "note.revise", target: "", user: "anonymous", source: "api", result: "refused"},
		{name: "note retract", pattern: "POST /api/notes/{id}/retract", server: noteSrv, person: true, header: noteAs, prep: seedNote,
			method: "POST", path: "/api/notes/n-audit-1/retract", body: `{"reason":"typo"}`,
			action: "note.retract", target: "note:n-audit-1", user: "user:jsmith", source: "hmi", result: "ok"},
		{name: "note retract with no credential", pattern: "POST /api/notes/{id}/retract", server: noteSrv, person: true, prep: seedNote,
			method: "POST", path: "/api/notes/n-audit-1/retract", body: `{"reason":"typo"}`,
			action: "note.retract", target: "", user: "anonymous", source: "api", result: "refused"},
		{name: "push subscribe", pattern: "POST /api/push/subscribe", server: pushSrv, person: true, header: pushAs,
			method: "POST", path: "/api/push/subscribe", body: pushBody,
			action: "push.subscribe", target: "push:" + pushID, user: "user:jsmith", source: "hmi", result: "ok"},
		{name: "push subscribe with no credential", pattern: "POST /api/push/subscribe", server: pushSrv, person: true,
			method: "POST", path: "/api/push/subscribe", body: pushBody,
			action: "push.subscribe", target: "", user: "anonymous", source: "api", result: "refused"},
		{name: "push unsubscribe", pattern: "DELETE /api/push/subscribe", server: pushSrv, person: true, header: pushAs,
			prep: func(t *testing.T, s *Server) {
				if rec := call(t, s, "POST", "/api/push/subscribe", pushBody, func(r *http.Request) {
					for k, v := range pushAs {
						r.Header.Set(k, v)
					}
				}); rec.Code != http.StatusCreated {
					t.Fatalf("prep: %d %s", rec.Code, rec.Body)
				}
			},
			method: "DELETE", path: "/api/push/subscribe", body: `{"id":"` + pushID + `"}`,
			action: "push.unsubscribe", target: "push:" + pushID, user: "user:jsmith", source: "hmi", result: "ok"},
		{name: "push unsubscribe with no credential", pattern: "DELETE /api/push/subscribe", server: pushSrv, person: true,
			method: "DELETE", path: "/api/push/subscribe", body: `{"id":"` + pushID + `"}`,
			action: "push.unsubscribe", target: "", user: "anonymous", source: "api", result: "refused"},
	}
}

// ioRevertSrv has an apply whose watch will revert it.
func ioRevertSrv(t *testing.T) *Server {
	f := &fakeIO{valid: true, watch: &IOWatch{Gen: 1, Revert: true, State: "watching", Pending: []string{"device DEV_A"}}}
	return New(newTestRuntime(t), ioOptions(f, true))
}

func ioSrv(t *testing.T) *Server {
	return New(newTestRuntime(t), ioOptions(&fakeIO{valid: true, drivers: []string{"modbus"}}, true))
}

// ioCommitSrv is ioSrv on a controller that can commit.
func ioCommitSrv(t *testing.T) *Server {
	f := &fakeIO{valid: true, drivers: []string{"modbus"}, commit: func(IOCommit) (IOCommitted, error) {
		return IOCommitted{Commit: "c0ffee", Branch: "main", Files: []string{"modbus.yaml"}}, nil
	}}
	return New(newTestRuntime(t), ioOptions(f, true))
}

// ioSaveSrv is a controller whose staged candidate is invalid only because it
// needs a restart, and that can commit: a save for restart goes through.
func ioSaveSrv(t *testing.T) *Server {
	f := &fakeIO{valid: false, drivers: []string{"modbus"},
		restart: func(any) (bool, map[string]string) { return true, map[string]string{"nautilus.yaml": "x"} },
		save: func(IOSave) (IOCommitted, error) {
			return IOCommitted{Commit: "c0ffee", Branch: "main", Files: []string{"nautilus.yaml"}}, nil
		}}
	return New(newTestRuntime(t), ioOptions(f, true))
}

// ioStagedApplied stages, validates and applies candidate 1.
func ioStagedApplied(t *testing.T, s *Server) {
	ioStaged(true)(t, s)
	if w, _ := doJSON(t, s.Handler(), "POST", "/api/io/staged/1/apply", nil); w.Code != http.StatusOK {
		t.Fatalf("apply: %d", w.Code)
	}
}

// ioStaged stages candidate 1 (and validates it), outside the seam.
func ioStaged(validate bool) func(*testing.T, *Server) {
	return func(t *testing.T, s *Server) {
		h := s.Handler()
		if w, _ := doJSON(t, h, "POST", "/api/io/staged", map[string]any{"sha": "abc1234"}); w.Code != http.StatusCreated {
			t.Fatalf("stage: %d", w.Code)
		}
		if validate {
			if w, _ := doJSON(t, h, "POST", "/api/io/staged/1/validate", nil); w.Code != http.StatusOK {
				t.Fatalf("validate: %d", w.Code)
			}
		}
	}
}

// fakePair is a redundant pair member for the clear-duplicate route: it
// answers every clear with outcome, and records who asked.
type fakePair struct {
	outcome string
	by      []string
}

func (f *fakePair) Status() leader.Status {
	return leader.Status{Mode: "quorum", Pod: "b", Leader: "b", IsLeader: true, Marked: []string{"a"}}
}

func (f *fakePair) ClearDuplicate(_ context.Context, name, by string) (any, string, string) {
	f.by = append(f.by, by)
	return map[string]any{"name": name}, f.outcome, "fake " + f.outcome
}

func (f *fakePair) Commission(_ context.Context, by, acceptLoss, acceptLostLog string) (any, string, string) {
	f.by = append(f.by, by)
	return map[string]any{"cluster": "c", "acceptLoss": acceptLoss}, f.outcome, "fake " + f.outcome
}

func (f *fakePair) Recommission(_ context.Context, by string, inc, floor uint64, boxReplaced, fromSpare bool, acceptLoss, acceptLostLog string) (any, string, string) {
	f.by = append(f.by, by)
	rep := map[string]any{"floor": floor, "fromSpare": fromSpare, "acceptLoss": acceptLoss}
	if !fromSpare {
		rep["live"] = []map[string]any{{"member": "a", "error": "a is not reachable"}, {"member": "witness"}}
		rep["liveUnchecked"] = []string{"a did not answer: a live copy of b reachable only through a can't be ruled out; check that the old box is off"}
	}
	return rep, f.outcome, "fake " + f.outcome
}

func (f *fakePair) Vouch(_ context.Context, by, acceptLoss, acceptLostLog string) (any, string, string) {
	f.by = append(f.by, by)
	return map[string]any{"picture": map[string]any{"digest": "0123456789"}, "acceptLoss": acceptLoss, "acceptLostLog": acceptLostLog}, f.outcome, "fake " + f.outcome
}

func pairServer(outcome string) func(*testing.T) *Server {
	return plain(Options{AuthToken: "s3cret", Cluster: &fakePair{outcome: outcome}})
}

func pairServerNoToken(outcome string) func(*testing.T) *Server {
	return plain(Options{Cluster: &fakePair{outcome: outcome}})
}

func wantDetail(key, want string) func(*testing.T, audit.Entry) {
	return func(t *testing.T, e audit.Entry) {
		if got, _ := e.Detail[key].(string); got != want {
			t.Errorf("detail.%s = %q, want %q", key, got, want)
		}
	}
}

// wantLiveChecked: the entry (where says which part) records the
// recommission's live-copy check: who answered it, and what was not
// checked (#1029 3c-3c-2b).
func wantLiveChecked(where func(audit.Entry) any) func(*testing.T, audit.Entry) {
	return func(t *testing.T, e audit.Entry) {
		b, _ := json.Marshal(where(e))
		if !strings.Contains(string(b), `"liveAnswered":["witness"]`) || !strings.Contains(string(b), "a did not answer") {
			t.Errorf("the entry doesn't record the live-copy check: %s", b)
		}
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

func outOfService(eq string) func(*testing.T, *Server) {
	return func(t *testing.T, s *Server) {
		if err := s.alarms.TakeOutOfService(eq, time.Now().Add(time.Hour), "op1", "seal change"); err != nil {
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
			path := row.path
			if row.pathOf != nil {
				path = row.pathOf(srv)
			}
			rec := call(t, srv, row.method, path, row.body, func(r *http.Request) {
				for k, v := range row.header {
					r.Header.Set(k, v)
				}
				if row.reqOf != nil {
					row.reqOf(srv, r)
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
			// from is the client's address without the port (design §4).
			if e.From != "192.0.2.1" {
				t.Errorf("from = %q, want the address without its port", e.From)
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
		if selfJournalled[pattern] {
			continue
		}
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

// While the journal cannot be written an operator action is still allowed
// (design §6 Q5), the entry is in the ring, and it is written, with the
// same id and in order, once the journal recovers.
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
	all, err := os.ReadDir(dir)
	var es []string
	for _, e := range all {
		if audit.IsJournalFile(e.Name()) {
			es = append(es, e.Name())
		}
	}
	if err != nil || len(es) != 1 {
		t.Fatalf("journal dir: %v %v", all, err)
	}
	return es[0]
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
	// And the page may read whether its action reached the journal.
	rec = call(t, srv, "GET", "/api/state", "", func(r *http.Request) {
		r.Header.Set("Origin", "http://hmi.example")
		r.Header.Set("Authorization", "Bearer s3cret")
	})
	if h := rec.Header().Get("Access-Control-Expose-Headers"); !strings.Contains(h, "X-Nautilus-Audit-Journal") {
		t.Errorf("Access-Control-Expose-Headers = %q", h)
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

// An authorized write's body is capped: past the cap the request is
// refused (413), journalled once, and its body never held.
func TestWriteBodyIsCapped(t *testing.T) {
	srv := New(newTestRuntime(t), Options{AuthToken: "s3cret"})
	big := `{"name":"SP","value":"` + strings.Repeat("x", maxWriteBody) + `"}`
	rec := call(t, srv, "POST", "/api/tags", big, func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer s3cret")
	})
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("a body over the cap = %d, want 413", rec.Code)
	}
	es := srv.audit.ring.Recent()
	if len(es) != 1 || es[0].Result != audit.ResultRefused || es[0].Action != "tag.write" || es[0].Detail["status"] != http.StatusRequestEntityTooLarge {
		t.Fatalf("entries = %+v, want one refused tag.write", es)
	}
	// Under the cap, the handler still decides.
	if rec := call(t, srv, "POST", "/api/tags", `{"name":"SP","value":1}`, func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer s3cret")
	}); rec.Code != http.StatusNoContent {
		t.Fatalf("a small write = %d", rec.Code)
	}
}

// On shutdown the seam writes a summary for a refusal window that is
// still open, however young, so a burst just before a stop is not lost.
func TestFlushAuditClosesOpenWindows(t *testing.T) {
	srv := New(newTestRuntime(t))
	for i := 0; i < refuseFull+3; i++ {
		call(t, srv, "POST", "/api/tags", `{"name":"Level","value":1}`)
	}
	srv.FlushAudit()
	es := srv.audit.ring.Recent()
	if len(es) != refuseFull+1 || es[0].Detail["coalesced"] != 3 {
		t.Fatalf("after FlushAudit: %+v, want the refused ×3 summary", es)
	}
}

// countingBody counts what a handler reads of a request body.
type countingBody struct {
	r io.Reader
	n int
}

func (c *countingBody) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}
func (c *countingBody) Close() error { return nil }

// A request the write policy refuses (no token, a cross-origin page) has
// its body neither read nor journalled: a probe with a 1 MiB tag name and
// a 15 MiB value costs one small entry, not 16 MiB in the ring and on disk.
func TestUnauthorizedRefusalReadsNoBody(t *testing.T) {
	big := `{"name":"` + strings.Repeat("n", 1<<20) + `","value":"` + strings.Repeat("v", 15<<20) + `"}`
	for _, tc := range []struct {
		name string
		srv  *Server
		mod  func(*http.Request)
		code int
	}{
		{"no token", New(newTestRuntime(t), Options{AuthToken: "s3cret"}), func(*http.Request) {}, http.StatusUnauthorized},
		{"cross-origin", New(newTestRuntime(t)), func(r *http.Request) { r.Header.Set("Origin", "http://evil.example") }, http.StatusForbidden},
	} {
		body := &countingBody{r: strings.NewReader(big)}
		rec := call(t, tc.srv, "POST", "/api/tags", "", tc.mod, func(r *http.Request) { r.Body = body })
		if rec.Code != tc.code {
			t.Fatalf("%s: %d %s, want %d", tc.name, rec.Code, rec.Body, tc.code)
		}
		if body.n != 0 {
			t.Errorf("%s: %d bytes of the body were read", tc.name, body.n)
		}
		es := tc.srv.audit.ring.Recent()
		if len(es) != 1 {
			t.Fatalf("%s: %d entries", tc.name, len(es))
		}
		if b, _ := json.Marshal(es[0]); len(b) > 2048 || es[0].Target != "" || es[0].After != nil {
			t.Errorf("%s: the entry is %d bytes: %.300s", tc.name, len(b), b)
		}
	}
}

// What an authorized client sends is capped too: a target at targetMax,
// a refused action's values at refusedValueMax, an accepted one's at
// valueMax, each over its cap replaced by a marker with its size and hash.
func TestJournalledValuesAreCapped(t *testing.T) {
	srv := New(newTestRuntime(t))
	call(t, srv, "POST", "/api/tags", `{"name":"`+strings.Repeat("n", 1<<20)+`","value":1}`)
	big := strings.Repeat("v", 100<<10)
	// 10 KiB: under valueMax, over the refusal's cap.
	mid := strings.Repeat("m", 10<<10)
	call(t, srv, "POST", "/api/tags", `{"name":"Level","value":"`+mid+`"}`)
	es := srv.audit.ring.Recent()
	if len(es) != 2 {
		t.Fatalf("entries: %d", len(es))
	}
	if n := len(es[1].Target); es[1].Result != "refused" || n > targetMax+len("…") {
		t.Errorf("an unknown 1 MiB tag name journalled a %d-byte target (%s)", n, es[1].Result)
	}
	sum := sha256.Sum256([]byte(`"` + mid + `"`))
	m, _ := es[0].After.(map[string]any)
	if es[0].Result != "refused" || m["truncated"] != true || m["sha256"] != hex.EncodeToString(sum[:]) {
		t.Errorf("a refused 10 KiB value journalled as %.200v", es[0].After)
	}
	if b, _ := json.Marshal(es[0]); len(b) > 2*refusedValueMax {
		t.Errorf("the refused entry is %d bytes", len(b))
	}

	// An accepted value is kept whole up to valueMax.
	fac := New(newTestRuntime(t), Options{TagWriter: func(string, any) error { return nil }})
	call(t, fac, "POST", "/api/tags", `{"name":"SP","value":"`+mid+`"}`)
	call(t, fac, "POST", "/api/tags", `{"name":"SP","value":"`+big+`"}`)
	es = fac.audit.ring.Recent()
	if len(es) != 2 || es[1].Result != "ok" || es[1].After != mid {
		t.Fatalf("an accepted 10 KiB value was not kept whole: %.200v", es)
	}
	if m, _ := es[0].After.(map[string]any); m["truncated"] != true {
		t.Errorf("an accepted 100 KiB value journalled as %.200v", es[0].After)
	}
}

func auditCard(t *testing.T, srv *Server) *DriverStatus {
	t.Helper()
	var ds []DriverStatus
	if err := json.Unmarshal(call(t, srv, "GET", "/api/drivers", "").Body.Bytes(), &ds); err != nil {
		t.Fatal(err)
	}
	for i := range ds {
		if ds[i].Kind == "audit" {
			return &ds[i]
		}
	}
	return nil
}

// While operator actions are journalled in memory only and elevated ones
// are refused (#709, design §6 question 5), the drivers block says so: a journal that could not be
// opened, or one that cannot be written, until it catches up.
func TestJournalTroubleIsOnTheDriversBlock(t *testing.T) {
	if c := auditCard(t, New(newTestRuntime(t))); c != nil {
		t.Errorf("a healthy server shows %+v", c)
	}
	off := New(newTestRuntime(t), Options{AuditUnavailable: errors.New("audit: mkdir /ro/audit: read-only file system")})
	if c := auditCard(t, off); c == nil || c.State != "error" || !strings.Contains(c.LastError, "read-only") ||
		!strings.Contains(c.Message, "memory only") {
		t.Errorf("an unopened journal shows %+v", c)
	}

	captureLog(t)
	j := &flaky{down: true}
	srv := New(newTestRuntime(t), Options{Audit: j})
	call(t, srv, "POST", "/api/tags", `{"name":"SP","value":70}`)
	c := auditCard(t, srv)
	if c == nil || c.State != "error" || c.LastError != "disk full" || len(c.Metrics) == 0 || c.Metrics[0].Value != 1 {
		t.Fatalf("a failing journal shows %+v", c)
	}
	j.mu.Lock()
	j.down = false
	j.mu.Unlock()
	srv.audit.tick()
	if c := auditCard(t, srv); c != nil {
		t.Errorf("after the journal caught up: %+v", c)
	}
}

// A write a standby answers itself never reaches the leader's seam, so the
// standby journals it: the held lease's 503 and the dead leader's 502,
// coalesced like any refusal.
func TestStandbyAnswersAreJournalled(t *testing.T) {
	held := New(newTestRuntime(t), Options{Cluster: fakeCluster{status: leader.Status{
		Mode: "cluster", Pod: "plc-0", Leader: "plc-0", LeaderAddr: "127.0.0.1:1",
	}}})
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	held.audit.now = func() time.Time { return now }
	for i := 0; i < refuseFull+3; i++ {
		if rec := call(t, held, "POST", "/api/tags", `{"name":"SP","value":70}`); rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("held lease: %d", rec.Code)
		}
	}
	call(t, held, "GET", "/api/state", "")
	held.FlushAudit()
	es := held.audit.ring.Recent()
	if len(es) != refuseFull+1 || es[0].Detail["coalesced"] != 3 || es[1].Action != "tag.write" ||
		es[1].Result != "error" || es[1].Detail["status"] != http.StatusServiceUnavailable ||
		!strings.Contains(fmt.Sprint(es[1].Detail["reason"]), "write gate is closed") {
		t.Fatalf("held lease journal: %+v", es)
	}

	defer func(d time.Duration) { failoverWait = d }(failoverWait)
	failoverWait = 50 * time.Millisecond
	gone := httptest.NewServer(http.NotFoundHandler())
	addr := standbyAddr(gone)
	gone.Close()
	standby := New(newTestRuntime(t), Options{Cluster: fakeCluster{status: leader.Status{
		Mode: "cluster", Pod: "plc-1", Leader: "plc-0", LeaderAddr: addr,
	}}})
	if rec := call(t, standby, "DELETE", "/api/forces/SP", ""); rec.Code != http.StatusBadGateway {
		t.Fatalf("dead leader: %d", rec.Code)
	}
	es = standby.audit.ring.Recent()
	if len(es) != 1 || es[0].Action != "force.clear" || es[0].Result != "error" || es[0].Detail["status"] != http.StatusBadGateway {
		t.Fatalf("dead leader journal: %+v", es)
	}
}

// stuck takes one entry, fails the next, then blocks every later write
// until released: a journal catching up on a slow disk.
type stuck struct {
	mu       sync.Mutex
	n        int
	blocked  chan struct{}
	release  chan struct{}
	signaled bool
}

func (s *stuck) Record(e audit.Entry) (audit.Entry, error) {
	s.mu.Lock()
	s.n++
	n := s.n
	if n > 1 && !s.signaled {
		s.signaled = true
		close(s.blocked)
	}
	s.mu.Unlock()
	if n == 1 {
		return e, errors.New("disk full")
	}
	<-s.release
	return e, nil
}

// The journal's upkeep runs beside the broadcast loop: while it is stuck
// catching up (fsyncing a backlog), the live frames keep coming.
func TestAuditUpkeepDoesNotStallTheFrames(t *testing.T) {
	prev := auditUpkeep
	auditUpkeep = 5 * time.Millisecond
	t.Cleanup(func() { auditUpkeep = prev })
	captureLog(t)
	j := &stuck{blocked: make(chan struct{}), release: make(chan struct{})}
	srv := New(newTestRuntime(t), Options{Audit: j, Interval: 5 * time.Millisecond})
	call(t, srv, "POST", "/api/tags", `{"name":"SP","value":70}`) // kept: the journal failed
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Run(ctx)
	select {
	case <-j.blocked:
	case <-time.After(5 * time.Second):
		t.Fatal("the upkeep never retried the kept entry")
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	// Unstick the journal first, so ts.Close does not wait on a handler
	// the test has failed on.
	defer close(j.release)
	rctx, rcancel := context.WithTimeout(ctx, 3*time.Second)
	defer rcancel()
	req, _ := http.NewRequestWithContext(rctx, "GET", ts.URL+"/api/stream", nil)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(nil, 1<<20)
	frames := 0
	for frames < 3 && sc.Scan() {
		if strings.HasPrefix(sc.Text(), "data: ") {
			frames++
		}
	}
	if frames < 3 {
		t.Fatalf("%d frames while the journal was catching up, want 3 (%v)", frames, sc.Err())
	}
}

// While the journal cannot be written (or could not be opened), elevated
// actions are refused and never applied, and operator actions still go
// through (#709, design §6 question 5). The refusal is journalled, with its
// target; a request without the credential is refused for that first.
func TestJournalDownRefusesElevatedActions(t *testing.T) {
	captureLog(t)
	j := &flaky{down: true}
	params := newFakeParams()
	srv := auditServer(t, Options{Audit: j, Params: params.hook()})
	edit(editedProgram)(t, srv) // something to roll back
	if srv.AuditJournalDown() {
		t.Fatal("down before any write failed")
	}
	// The write that finds the journal broken has already happened.
	if rec := call(t, srv, "POST", "/api/tags", `{"name":"SP","value":70}`); rec.Code != http.StatusNoContent {
		t.Fatalf("operator write = %d", rec.Code)
	}
	if !srv.AuditJournalDown() {
		t.Fatal("not down after a failed write")
	}
	elevated := []struct{ method, path, body string }{
		{"PUT", "/api/program", `{"source":"` + strings.ReplaceAll(editedProgram, "\n", `\n`) + `"}`},
		{"POST", "/api/program/activate", `{"sha":"abc"}`},
		{"POST", "/api/forces", `{"name":"SP","value":12}`},
		// A parameter written to a device on an IO-Link port (#705).
		{"PUT", "/api/iolink/LEVEL1/params/V_Setpoint", `{"value": 500}`},
	}
	for _, c := range elevated {
		rec := call(t, srv, c.method, c.path, c.body)
		if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "audit journal cannot be written") {
			t.Errorf("%s %s while the journal is down = %d %s, want 503", c.method, c.path, rec.Code, rec.Body)
		}
	}
	if fv := srv.rt.Tags().ForcedValues(); len(fv) != 0 {
		t.Errorf("a refused force was applied: %v", fv)
	}
	if len(params.writes) != 0 {
		t.Errorf("a parameter write the journal refused reached the device: %v", params.writes)
	}
	es := srv.audit.ring.Recent()
	// Newest first: the parameter write was the last request.
	if e := es[0]; e.Action != "param.write" || e.Target != "LEVEL1/V_Setpoint" || e.Detail["status"] != http.StatusServiceUnavailable {
		t.Errorf("the refused parameter write's entry: %+v", e)
	}
	if e := es[1]; e.Action != "force.set" || e.Target != "SP" || e.Detail["status"] != http.StatusServiceUnavailable {
		t.Errorf("the refused force's entry: %+v", e)
	}
	// Operator actions, and the ways back to normal (clearing forces,
	// rolling an edit back), are carried out, and say the journal is down.
	steps := []struct{ method, path, body string }{
		{"POST", "/api/tags", `{"name":"SP","value":71}`},
		{"POST", "/api/alarms/ack", `{"ids":["HiTemp"]}`},
		{"DELETE", "/api/forces/SP", ""},
		{"POST", "/api/forces/clear", ""},
		{"POST", "/api/program/rollback", ""},
	}
	for i, c := range steps {
		if i == 2 { // forces to clear, set behind the API's back
			srv.rt.Tags().Force("SP", 9.0)
			srv.rt.Tags().Force("Level", 1.0)
		}
		rec := call(t, srv, c.method, c.path, c.body)
		if rec.Code >= 300 || rec.Header().Get("X-Nautilus-Audit-Journal") != "down" {
			t.Errorf("%s %s while the journal is down = %d %q %s, want it done and the header", c.method, c.path, rec.Code, rec.Header().Get("X-Nautilus-Audit-Journal"), rec.Body)
		}
	}
	if fv := srv.rt.Tags().ForcedValues(); len(fv) != 0 {
		t.Errorf("forces left after clearing: %v", fv)
	}
	// Recovered: forces are allowed again.
	j.mu.Lock()
	j.down = false
	j.mu.Unlock()
	srv.audit.tick()
	if rec := call(t, srv, "POST", "/api/forces", `{"name":"SP","value":12}`); rec.Code != http.StatusOK || rec.Header().Get("X-Nautilus-Audit-Journal") != "" {
		t.Errorf("force after recovery = %d %s (header %q)", rec.Code, rec.Body, rec.Header().Get("X-Nautilus-Audit-Journal"))
	}

	// A journal that could not be opened: down for the process's life.
	off := auditServer(t, Options{AuditUnavailable: errors.New("audit: locked")})
	if rec := call(t, off, "POST", "/api/forces", `{"name":"SP","value":12}`); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("force with no journal = %d", rec.Code)
	}
	if rec := call(t, off, "POST", "/api/tags", `{"name":"SP","value":1}`); rec.Code != http.StatusNoContent {
		t.Errorf("operator write with no journal = %d", rec.Code)
	}
	// Without the credential, the refusal is for that.
	tok := auditServer(t, Options{AuthToken: "s3cret", AuditUnavailable: errors.New("audit: locked")})
	if rec := call(t, tok, "POST", "/api/forces", `{"name":"SP","value":12}`); rec.Code != http.StatusUnauthorized {
		t.Errorf("unauthorized force with no journal = %d", rec.Code)
	}
}

// Every action the seam journals is classed operator or elevated, on
// purpose: an unclassed one (a new route) fails here, and until it is
// classed it counts as elevated, refused while the journal is down.
func TestEveryActionIsClassed(t *testing.T) {
	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); !ok || sel.Sel.Name != "audited" {
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				t.Errorf("%s: an audited action that is not a string literal cannot be classed", fset.Position(call.Pos()))
				return true
			}
			action, _ := strconv.Unquote(lit.Value)
			n++
			if c := actionClass[action]; c != operator && c != recovery && c != strict {
				t.Errorf("%s: action %q is not classed: add it to actionClass as operator, recovery or strict (until then it is refused while the journal is down)", fset.Position(call.Pos()), action)
			}
			return true
		})
	}
	if n < 12 {
		t.Fatalf("found %d audited actions, want at least 12", n)
	}
	// Fail closed: an action nobody classed is strict.
	for _, a := range []string{"config.apply", "substitute.start", ""} {
		if !journalStrict(a) {
			t.Errorf("unclassed %q is not strict", a)
		}
	}
	if journalStrict("tag.write") || journalStrict("force.clear") || !journalStrict("force.set") {
		t.Error("the classes are swapped")
	}
}

// Live I/O configuration is elevated: while the journal is down a stage,
// validate or apply is refused before it reaches the engine.
func TestJournalDownRefusesIOConfig(t *testing.T) {
	captureLog(t)
	f := &fakeIO{valid: true, drivers: []string{"modbus"}}
	o := ioOptions(f, true)
	o.Audit = &flaky{down: true}
	srv := New(newTestRuntime(t), o)
	call(t, srv, "POST", "/api/tags", `{"name":"SP","value":70}`) // finds the journal broken
	for _, p := range []string{"/api/io/staged", "/api/io/staged/1/validate", "/api/io/staged/1/apply"} {
		rec := call(t, srv, "POST", p, `{"sha":"abc1234"}`)
		if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "audit journal cannot be written") {
			t.Errorf("POST %s while the journal is down = %d %s", p, rec.Code, rec.Body)
		}
	}
	if len(f.staged) != 0 || f.applied != 0 {
		t.Errorf("the engine was reached: staged %v, applied %d", f.staged, f.applied)
	}
}

// The seam reads a body as the handlers do: the first JSON value, whatever
// follows. A write with trailing bytes is carried out, so its entry must
// name the same target and values as without them.
func TestTrailingBytesKeepTheTarget(t *testing.T) {
	n := 0
	for _, row := range auditRows() {
		if row.result != "ok" || !strings.HasPrefix(row.body, "{") {
			continue
		}
		n++
		entry := func(body string) audit.Entry {
			srv := row.server(t)
			if row.prep != nil {
				row.prep(t, srv)
			}
			call(t, srv, row.method, row.path, body, func(r *http.Request) {
				for k, v := range row.header {
					r.Header.Set(k, v)
				}
			})
			return srv.audit.ring.Recent()[0]
		}
		clean, trailing := entry(row.body), entry(row.body+" x")
		// a note's id is random per request: the rest of what it names is compared
		ids := regexp.MustCompile(`[0-9a-f]{32}`)
		same := func(a, b any) bool {
			return ids.ReplaceAllString(fmt.Sprint(a), "<id>") == ids.ReplaceAllString(fmt.Sprint(b), "<id>")
		}
		if trailing.Result != clean.Result || trailing.Target != clean.Target || !same(trailing.After, clean.After) {
			t.Errorf("%s: with trailing bytes the entry is %s %q %v, without %s %q %v",
				row.name, trailing.Result, trailing.Target, trailing.After, clean.Result, clean.Target, clean.After)
		}
	}
	if n < 8 {
		t.Fatalf("only %d accepted rows with a body", n)
	}
}

// A surface's refusals by the auth: block (class audit.ClassAuth, the
// Sparkplug node's) are coalesced per target and apart from its other
// refusals, and a surface does not share HTTP's window from the same
// address (#1107 review): a host retrying one closed tag every scan must
// not fold an auth refusal of a sensitive tag into a summary without its
// target.
func TestAuthRefusalsKeepTheirOwnWindow(t *testing.T) {
	srv := New(newTestRuntime(t))
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	srv.audit.now = func() time.Time { return now }
	sp := srv.Surface(audit.SourceSparkplug)
	refused := func(target string, auth bool) audit.Entry {
		e := audit.Entry{User: "sparkplug:broker", Action: "tag.write", Target: target, From: "127.0.0.1:1883",
			Result: audit.ResultRefused, Detail: map[string]any{"reason": "not writable"}}
		if auth {
			e.Detail["class"] = audit.ClassAuth
		}
		return e
	}
	// HTTP's refusals from 127.0.0.1 use up their own window.
	for i := 0; i < 2*refuseFull; i++ {
		call(t, srv, "POST", "/api/tags", `{"name":"Level","value":1}`, func(r *http.Request) { r.RemoteAddr = "127.0.0.1:5000" })
	}
	// The broker floods one closed tag.
	for i := 0; i < 2*refuseFull; i++ {
		sp.RecordBatch([]audit.Entry{refused("Closed", false)})
	}
	// The auth refusals of two tags each get their own allowance.
	sp.RecordBatch([]audit.Entry{refused("Valve_A", true), refused("Valve_B", true)})
	full := map[string]int{}
	for _, e := range srv.audit.ring.Recent() {
		full[e.Source+" "+e.Target]++
	}
	if full["sparkplug Closed"] != refuseFull || full["sparkplug Valve_A"] != 1 || full["sparkplug Valve_B"] != 1 {
		t.Fatalf("journalled in full: %v", full)
	}
	// A flood of auth refusals of one tag is summarised WITH its target.
	for i := 0; i < 2*refuseFull; i++ {
		sp.RecordBatch([]audit.Entry{refused("Valve_A", true)})
	}
	now = now.Add(refuseWindow)
	srv.audit.tick()
	var summary *audit.Entry
	for _, e := range srv.audit.ring.Recent() {
		if e.Detail["coalesced"] != nil && e.Source == audit.SourceSparkplug && e.Detail["class"] == audit.ClassAuth {
			e := e
			summary = &e
		}
	}
	if summary == nil || summary.Target != "Valve_A" || summary.Detail["coalesced"] != 2*refuseFull+1-refuseFull {
		t.Fatalf("auth summary = %+v", summary)
	}
}

// The user filter takes what the Who column shows ("alice") as well as the
// full principal ("user:alice"); a bare word never matches the other kinds
// of principal by their prefix.
func TestAuditUserFilterTakesTheBareName(t *testing.T) {
	srv := New(newTestRuntime(t))
	for _, u := range []string{"user:alice", "user:bob", "token:alice", "system"} {
		srv.audit.record(audit.Entry{ID: audit.NewID(time.Now()), User: u, Source: "api", Action: "tag.write", Result: "ok"})
	}
	users := func(q string) string {
		t.Helper()
		rec := call(t, srv, "GET", "/api/audit?user="+q, "")
		var out auditResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		var us []string
		for _, e := range out.Entries {
			us = append(us, e.User)
		}
		sort.Strings(us)
		return strings.Join(us, ",")
	}
	for q, want := range map[string]string{
		"alice":       "user:alice",
		"ALICE":       "user:alice",
		"user:alice":  "user:alice",
		"token:alice": "token:alice",
		"system":      "system",
		"user":        "",
		"ali":         "",
	} {
		if got := users(q); got != want {
			t.Errorf("?user=%s matched %q, want %q", q, got, want)
		}
	}
}

// RecordSystem journals a start-time fact as the system, with its target
// and detail (#715: an alarm channel that does not verify its server).
func TestRecordSystem(t *testing.T) {
	srv := New(newTestRuntime(t), Options{Audit: &flaky{}})
	srv.RecordSystem("alarm.channel-insecure", "email", map[string]any{"reason": "insecure: true"})
	es := srv.audit.ring.Recent()
	if len(es) != 1 || es[0].User != "system" || es[0].Source != audit.SourceSystem ||
		es[0].Action != "alarm.channel-insecure" || es[0].Target != "email" || es[0].Result != audit.ResultOK {
		t.Fatalf("ring = %+v", es)
	}
}
