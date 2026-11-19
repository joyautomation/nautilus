package server

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/audit"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/retain"
	"github.com/joyautomation/nautilus/runtime"
)

// Substitutes across a restart, as the journal sees them (#707 PR 3a).
// naut run loads the retain store (LoadRetained) before it builds the
// server, so what the load decides on its own must still reach the
// journal once the server listens.

type subStore struct {
	mu   sync.Mutex
	st   retain.State
	fail bool
}

func (s *subStore) Load() (retain.State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return retain.State{}, errors.New("store unreadable")
	}
	return s.st, nil
}

func (s *subStore) Save(st retain.State) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return errors.New("store unwritable")
	}
	s.st = st
	return nil
}

func (s *subStore) Kind() string { return "test" }

// restartedServer is naut run's order: the runtime, its retain load, then
// the server, then the first scan.
func restartedServer(t *testing.T, store retain.Store, clk *subClock) (*Server, *runtime.Runtime) {
	t.Helper()
	rt, err := runtime.New(runtime.Options{
		Program: testProgram, Driver: nio.NewMemory(), Clock: clk, Retain: store,
		Inputs: []string{"Level", "Flow"}, Outputs: []string{"Out"},
		Seed:          nio.Values{"Level": 40.0, "Flow": 3.0, "SP": 65.0, "Out": 0.0},
		Substitutable: []string{"Level", "Flow"},
	})
	if err != nil {
		t.Fatal(err)
	}
	rt.LoadRetained()
	s := New(rt, Options{Substitutes: subRules})
	rt.Scan()
	return s, rt
}

func waitAudit(t *testing.T, s *Server, action string) audit.Entry {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, x := range s.audit.ring.Recent() {
			if x.Action == action {
				return x
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("no %s entry: %+v", action, s.audit.ring.Recent())
	return audit.Entry{}
}

// A substitute whose time ran out while the controller was down expires
// at the load, before the server exists, and is journalled once it does:
// substitute.expire by system, reason expired-while-down.
func TestSubstituteExpiredWhileDownIsJournalled(t *testing.T) {
	clk := &subClock{t: time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)}
	start := clk.t.Add(-3 * time.Hour)
	store := &subStore{st: retain.State{Substitutes: &retain.SubstituteState{
		Entries: []retain.SubstituteRetain{{
			ID: 4, Name: "Level", Value: 95.0, By: "op",
			StartMs: start.UnixMilli(), UntilMs: start.Add(time.Hour).UnixMilli(), GrantedMs: time.Hour.Milliseconds(),
		}},
		HighID: 4,
	}}}
	s, rt := restartedServer(t, store, clk)
	if len(rt.Substitutes()) != 0 || rt.Tags().Real("Level") != 40 {
		t.Fatalf("substitutes %+v, Level %v: want it expired at the load", rt.Substitutes(), rt.Tags().Real("Level"))
	}
	e := waitAudit(t, s, "substitute.expire")
	if e.User != "system" || e.Source != audit.SourceSystem || e.Target != "Level" ||
		e.Detail["reason"] != runtime.SubstituteReasonExpiredWhileDown {
		t.Fatalf("expiry entry = %+v", e)
	}
}

// A store that cannot be loaded raises __Substitute_Fault before the
// server exists; the fault is journalled once it does.
func TestSubstituteLoadFaultIsJournalled(t *testing.T) {
	clk := &subClock{t: time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)}
	s, rt := restartedServer(t, &subStore{fail: true}, clk)
	if !rt.Tags().Bool(runtime.SubstituteFaultTag) {
		t.Fatal("__Substitute_Fault is false after the store failed to load")
	}
	e := waitAudit(t, s, "substitute.fault")
	if e.User != "system" || e.Detail["reason"] != runtime.SubstituteReasonRetainLoadFailed {
		t.Fatalf("fault entry = %+v", e)
	}
}

// A seeded input's retained substitute is in place from the load, and the
// list reports it with its fault field empty.
func TestSubstituteRestoredAtTheLoadIsListed(t *testing.T) {
	clk := &subClock{t: time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)}
	start := clk.t.Add(-10 * time.Minute)
	store := &subStore{st: retain.State{Substitutes: &retain.SubstituteState{
		Entries: []retain.SubstituteRetain{{
			ID: 2, Name: "Flow", Value: 7.0, By: "op",
			StartMs: start.UnixMilli(), UntilMs: start.Add(time.Hour).UnixMilli(), GrantedMs: time.Hour.Milliseconds(),
		}},
		HighID: 2,
	}}}
	s, rt := restartedServer(t, store, clk)
	if rt.Tags().Real("Flow") != 7 {
		t.Fatalf("Flow = %v; want the retained substitute", rt.Tags().Real("Flow"))
	}
	r := call(t, s, "GET", "/api/substitutes", "")
	l := decodeSubs(t, r.Body.Bytes()).Substitutes
	if len(l) != 1 || l[0].ID != 2 || l[0].RemainingMs != (50*time.Minute).Milliseconds() || l[0].Fault != "" {
		t.Fatalf("list %+v; want entry 2 with 50 min left", l)
	}
}

// A takeover's superseded stop is journalled (the store may never have
// seen the entry, so nobody else recorded its end); a person's stop and a
// replace are not journalled here a second time.
func TestSupersededStopIsJournalled(t *testing.T) {
	s := subAuditServer(t)
	entry := runtime.Substitution{ID: 9, Name: "Level", Value: 95.0, By: "op"}
	s.auditSubstituteEvent(runtime.SubstituteEvent{Kind: runtime.SubstituteEventStop, Entry: entry, By: "op", AtMs: 1})
	s.auditSubstituteEvent(runtime.SubstituteEvent{Kind: runtime.SubstituteEventStop, Reason: runtime.SubstituteReasonReplaced, Entry: entry, By: "op", AtMs: 2})
	s.auditSubstituteEvent(runtime.SubstituteEvent{Kind: runtime.SubstituteEventStop, Reason: runtime.SubstituteReasonSuperseded, Entry: entry, By: "system", AtMs: 3})
	var got []audit.Entry
	for _, x := range s.audit.ring.Recent() {
		if x.Action == "substitute.stop" {
			got = append(got, x)
		}
	}
	if len(got) != 1 || got[0].User != "system" || got[0].Detail["reason"] != runtime.SubstituteReasonSuperseded || got[0].Target != "Level" {
		t.Fatalf("substitute.stop entries %+v; want exactly the superseded one", got)
	}
}

type standbyCoord struct{}

func (standbyCoord) IsLeader() bool { return false }

// On a replica that is not leading (a request reaches one only when no
// leader is known to forward it to), a start, stop or acknowledgement is
// refused with 503: its next takeover would undo it.
func TestSubstituteRoutesRefusedWhenNotLeading(t *testing.T) {
	clk := &subClock{t: time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)}
	rt, err := runtime.New(runtime.Options{
		Program: testProgram, Driver: nio.NewMemory(), Clock: clk, Coordinator: standbyCoord{},
		Inputs: []string{"Level", "Flow"}, Outputs: []string{"Out"},
		Seed:          nio.Values{"Level": 40.0, "Flow": 3.0, "SP": 65.0, "Out": 0.0},
		Substitutable: []string{"Level", "Flow"},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := New(rt, Options{Substitutes: subRules})
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/api/substitutes", `{"name":"Flow","value":7}`},
		{"DELETE", "/api/substitutes/1", ""},
		{"POST", "/api/substitutes/ack", `{"ids":[1]}`},
	} {
		if r := call(t, s, c.method, c.path, c.body); r.Code != 503 || !strings.Contains(r.Body.String(), "no leader") {
			t.Errorf("%s %s on a standby = %d %s; want 503 no leader", c.method, c.path, r.Code, r.Body)
		}
	}
}

// Over the API: a start the retain store does not take is refused with
// 503 and nothing runs; a stop it does not take still ends the substitute
// and answers kept: false.
func TestSubstituteRoutesSaveBeforeAnswering(t *testing.T) {
	store := &subStore{}
	rt, err := runtime.New(runtime.Options{
		Program: testProgram, Driver: nio.NewMemory(), Retain: store,
		Inputs: []string{"Level", "Flow"}, Outputs: []string{"Out"},
		Seed:          nio.Values{"Level": 40.0, "Flow": 3.0, "SP": 65.0, "Out": 0.0},
		Substitutable: []string{"Level", "Flow"},
	})
	if err != nil {
		t.Fatal(err)
	}
	rt.LoadRetained()
	s := New(rt, Options{Substitutes: subRules})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { rt.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	var first *httptest.ResponseRecorder
	for i := 0; i < 200; i++ { // until the first scan has taken over
		if first = call(t, s, "POST", "/api/substitutes", `{"name":"Flow","value":7}`); first.Code == 200 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if first.Code != 200 || !strings.Contains(first.Body.String(), `"kept":true`) {
		t.Fatalf("start = %d %s; want 200 kept", first.Code, first.Body)
	}
	id := decodeSubs(t, first.Body.Bytes()).Started.ID
	store.mu.Lock()
	store.fail = true
	store.mu.Unlock()
	if r := call(t, s, "POST", "/api/substitutes", `{"name":"Level"}`); r.Code != 503 {
		t.Fatalf("a start the store refused = %d %s; want 503", r.Code, r.Body)
	}
	if l := rt.Substitutes(); len(l) != 1 || l[0].Name != "Flow" {
		t.Fatalf("%+v; want only Flow: the refused start must not run", l)
	}
	r := call(t, s, "DELETE", "/api/substitutes/"+subID(id), "")
	if r.Code != 200 || !strings.Contains(r.Body.String(), `"kept":false`) || len(rt.Substitutes()) != 0 {
		t.Fatalf("a stop the store refused = %d %s; want 200, kept false, and the stop live", r.Code, r.Body)
	}
}
