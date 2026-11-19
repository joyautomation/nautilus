package server

import (
	"errors"
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
