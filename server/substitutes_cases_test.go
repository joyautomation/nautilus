package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/audit"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/oplog"
	"github.com/joyautomation/nautilus/retain"
	"github.com/joyautomation/nautilus/runtime"
)

// Redundancy test plan §8.1 cases 7–10 (#707 PR 3b), in process: two
// controllers (A, B) and the witness (W) on three real oplog nodes over
// HTTP, the runtimes' leadership following the log's. These cases join the
// simulated run (T2/T3) when #710 PR 3 mounts oplog in `naut run`; nothing
// here claims more than the in-process run.

// pairProgram latches the input values its first logic scan saw.
const pairProgram = `PROGRAM P
VAR_EXTERNAL Level : REAL; Flow : REAL; First : REAL; Seen : BOOL; END_VAR
IF NOT Seen THEN First := Level; Seen := TRUE; END_IF
END_PROGRAM`

// pairGate is one node's network: down refuses everything; loseWrites
// runs a POST and loses its answer.
type pairGate struct {
	h                http.Handler
	down, loseWrites atomic.Bool
}

func (g *pairGate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if g.down.Load() {
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}
	if g.loseWrites.Load() && r.Method == http.MethodPost {
		g.h.ServeHTTP(httptest.NewRecorder(), r)
		http.Error(w, "lost", http.StatusServiceUnavailable)
		return
	}
	g.h.ServeHTTP(w, r)
}

type pair struct {
	mu     sync.Mutex
	leader string
	term   uint64

	urls   [3]string
	gates  [3]*pairGate
	nodes  [3]*oplog.Node
	rts    [2]*runtime.Runtime
	srvs   [2]*Server
	clks   [2]*subClock
	stores [2]*subStore
}

// seat is one instance's view of the pair's leadership, for both the log
// (oplog.Leadership, Termed) and the runtime (Coordinator, Termed).
type seat struct {
	p    *pair
	self string
}

func (s seat) IsLeader() bool     { s.p.mu.Lock(); defer s.p.mu.Unlock(); return s.p.leader == s.self }
func (s seat) LeaderAddr() string { s.p.mu.Lock(); defer s.p.mu.Unlock(); return s.p.leader }
func (s seat) Term() uint64       { s.p.mu.Lock(); defer s.p.mu.Unlock(); return s.p.term }

func (p *pair) lead(i int, term uint64) {
	p.mu.Lock()
	p.leader, p.term = p.urls[i], term
	p.mu.Unlock()
}

func newPair(t *testing.T) *pair {
	t.Helper()
	p := &pair{}
	servers := make([]*httptest.Server, 3)
	for i := range servers {
		servers[i] = httptest.NewUnstartedServer(nil)
		p.urls[i] = "http://" + servers[i].Listener.Addr().String()
	}
	p.lead(0, 1)
	for i := 0; i < 3; i++ {
		var sl *SubstituteLog
		if i < 2 {
			p.clks[i] = &subClock{t: time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)}
			p.stores[i] = &subStore{}
			rt, err := runtime.New(runtime.Options{
				Program: pairProgram, Driver: nio.NewMemory(), Clock: p.clks[i], Retain: p.stores[i],
				Coordinator:   seat{p, p.urls[i]},
				Inputs:        []string{"Level", "Flow"},
				Seed:          nio.Values{"Level": 40.0, "Flow": 3.0, "First": 0.0, "Seen": false},
				Substitutable: []string{"Level", "Flow"},
			})
			if err != nil {
				t.Fatal(err)
			}
			p.rts[i] = rt
			sl = NewSubstituteLog(rt)
		}
		apply := func(oplog.Entry) error { return nil }
		if sl != nil {
			apply = func(e oplog.Entry) error { _, err := sl.Apply(e); return err }
		}
		n, err := oplog.New(oplog.Options{Self: p.urls[i], Peers: p.urls[:], Leadership: seat{p, p.urls[i]},
			Token: "t", Ephemeral: true, Apply: apply})
		if err != nil {
			t.Fatal(err)
		}
		p.nodes[i] = n
		p.gates[i] = &pairGate{h: n.Handler()}
		servers[i].Config.Handler = p.gates[i]
		servers[i].Start()
		if sl != nil {
			sl.Attach(n)
			p.rts[i].SetIntentLog(sl, 300*time.Millisecond)
			p.srvs[i] = New(p.rts[i], Options{Substitutes: subRules, SubstituteLog: sl})
		}
	}
	t.Cleanup(func() {
		for i := range servers {
			servers[i].Close()
			p.nodes[i].Close()
		}
	})
	return p
}

// firstScan scans rt until its logic has run once, and returns the Level
// that first logic scan saw.
func firstScan(t *testing.T, rt *runtime.Runtime) float64 {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		rt.Scan()
		if rt.Tags().Bool("Seen") {
			return rt.Tags().Real("First")
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("the logic never ran")
	return 0
}

// beat is the leader's heartbeat (a ticker in the wiring): followers apply
// what it says is committed.
func (p *pair) beat() {
	for _, n := range p.nodes {
		n.Heartbeat(context.Background())
	}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(6 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func startOn(t *testing.T, s *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	return call(t, s, "POST", "/api/substitutes", body)
}

// Case 7 (F8): A is cut off from B; a substitute is committed by A and the
// witness; A dies. B's takeover catches the log up and applies it BEFORE
// its first scan, which runs on the substitute.
func TestCase7NewLeadersFirstScanSeesTheSubstitute(t *testing.T) {
	p := newPair(t)
	firstScan(t, p.rts[0])
	p.rts[1].Scan() // standby
	p.gates[1].down.Store(true)
	if r := startOn(t, p.srvs[0], `{"name":"Level"}`); r.Code != 200 {
		t.Fatalf("start on A = %d %s", r.Code, r.Body)
	}
	p.gates[0].down.Store(true) // A dies
	p.gates[1].down.Store(false)
	p.lead(1, 2)
	if got := firstScan(t, p.rts[1]); got != 95 {
		t.Fatalf("B's first scan ran on Level %v; want the substitute 95 (caught up before the scan)", got)
	}
}

// Case 7: the catch-up fails within its wait (A and the witness are
// gone): B takes over on its retained substitutes, flagged, never dropping
// them.
func TestCase7CatchUpFailsKeepsTheRetainedOnes(t *testing.T) {
	p := newPair(t)
	p.rts[1].Scan()
	now := p.clks[1].Now()
	p.stores[1].Save(retain.State{Substitutes: &retain.SubstituteState{
		Entries: []retain.SubstituteRetain{{ID: 1, Name: "Level", Value: 95.0, By: "op",
			StartMs: now.UnixMilli(), UntilMs: now.Add(time.Hour).UnixMilli(), GrantedMs: time.Hour.Milliseconds()}},
		HighID: 1,
	}})
	p.gates[0].down.Store(true)
	p.gates[2].down.Store(true)
	p.lead(1, 2)
	if got := firstScan(t, p.rts[1]); got != 95 {
		t.Fatalf("B's first scan ran on Level %v; want the retained substitute 95", got)
	}
	if !p.rts[1].Tags().Bool(runtime.SubstituteFaultTag) {
		t.Fatal("__Substitute_Fault is false after a failed catch-up")
	}
}

// Case 8: only the leader expires a substitute, and the expiry is a log
// entry the standby applies; the standby never expires one on its own.
func TestCase8OnlyTheLeaderExpires(t *testing.T) {
	p := newPair(t)
	firstScan(t, p.rts[0])
	p.rts[1].Scan()
	if r := startOn(t, p.srvs[0], `{"name":"Flow","value":7,"seconds":60}`); r.Code != 200 {
		t.Fatalf("start = %d %s", r.Code, r.Body)
	}
	p.beat()
	if len(p.rts[1].Substitutes()) != 1 {
		t.Fatal("the standby did not apply the committed start")
	}
	p.clks[0].t = p.clks[0].t.Add(2 * time.Minute)
	p.clks[1].t = p.clks[1].t.Add(2 * time.Minute)
	p.rts[1].Scan()
	if len(p.rts[1].Substitutes()) != 1 {
		t.Fatal("the standby expired a substitute on its own")
	}
	p.rts[0].Scan() // the leader's expiry
	eventually(t, "the standby to apply the leader's expiry", func() bool { p.beat(); return len(p.rts[1].Substitutes()) == 0 })
	found := false
	for _, e := range p.nodes[2].After(0) {
		if e.Kind == oplog.KindUnsubstitute && e.By == "system" && e.Reason == runtime.SubstituteReasonExpired {
			found = true
		}
	}
	if !found {
		t.Fatalf("no unsubstitute (system, expired) in the witness's log: %+v", p.nodes[2].After(0))
	}
	// Journalled once, by the leader that expired it; the standby applying
	// the entry does not journal it again.
	count := func(s *Server) int {
		n := 0
		for _, e := range s.audit.ring.Recent() {
			if e.Action == "substitute.expire" {
				n++
			}
		}
		return n
	}
	eventually(t, "the leader's expiry journalled", func() bool { return count(p.srvs[0]) == 1 })
	if n := count(p.srvs[1]); n != 0 {
		t.Fatalf("the standby journalled %d expiries; the leader journals it", n)
	}
}

// Case 8: a substitute that expired on the old leader just before it died,
// whose unsubstitute never committed, is not revived by the new leader.
func TestCase8AnExpiredSubstituteIsNotRevived(t *testing.T) {
	p := newPair(t)
	firstScan(t, p.rts[0])
	p.rts[1].Scan()
	if r := startOn(t, p.srvs[0], `{"name":"Flow","value":7,"seconds":60}`); r.Code != 200 {
		t.Fatalf("start = %d %s", r.Code, r.Body)
	}
	p.gates[1].down.Store(true)
	p.gates[2].down.Store(true)
	p.clks[0].t = p.clks[0].t.Add(2 * time.Minute)
	p.clks[1].t = p.clks[1].t.Add(2 * time.Minute)
	p.rts[0].Scan() // expires locally; its entry cannot commit
	p.gates[0].down.Store(true)
	p.gates[1].down.Store(false)
	p.gates[2].down.Store(false)
	p.lead(1, 2)
	firstScan(t, p.rts[1])
	if l := p.rts[1].Substitutes(); len(l) != 0 || p.rts[1].Tags().Real("Flow") != 3 {
		t.Fatalf("%+v, Flow %v: the expired substitute was revived", l, p.rts[1].Tags().Real("Flow"))
	}
}

// Case 9: a new leader whose wall clock is an hour behind computes the
// remainder once from until: capped at the granted duration, with the
// fault.
func TestCase9AClockBehindIsCappedAndFaults(t *testing.T) {
	p := newPair(t)
	firstScan(t, p.rts[0])
	p.rts[1].Scan()
	if r := startOn(t, p.srvs[0], `{"name":"Level"}`); r.Code != 200 {
		t.Fatalf("start = %d %s", r.Code, r.Body)
	}
	p.gates[0].down.Store(true)
	p.clks[1].t = p.clks[1].t.Add(-time.Hour)
	p.lead(1, 2)
	firstScan(t, p.rts[1])
	l := p.rts[1].Substitutes()
	if len(l) != 1 || l[0].RemainingMs != time.Hour.Milliseconds() || l[0].Fault != runtime.SubstituteReasonClockCapped {
		t.Fatalf("%+v; want capped at the granted hour, fault clock-capped", l)
	}
}

// Case 10: without quorum, a write the leader proposed before it knew is
// answered "outcome unknown" (503, journalled as unknown): it is stored,
// and may commit later. Once the log knows it has no majority (that write
// failed, or the wiring's pings found the peers gone), a start, stop or
// acknowledgement is refused before anything is proposed (503, refused,
// the no-majority text: test plan case 10, "refused like any operator
// write"). The expiry still happens on time locally, and is recorded once
// the log heals; a stale unsubstitute naming an older entry never ends a
// newer one.
func TestCase10NoQuorum(t *testing.T) {
	p := newPair(t)
	firstScan(t, p.rts[0])
	p.rts[1].Scan()
	r := startOn(t, p.srvs[0], `{"name":"Flow","value":7,"seconds":60}`)
	if r.Code != 200 {
		t.Fatalf("start = %d %s", r.Code, r.Body)
	}
	flow := decodeSubs(t, r.Body.Bytes()).Started.ID
	p.gates[1].down.Store(true)
	p.gates[2].down.Store(true)
	if r := startOn(t, p.srvs[0], `{"name":"Level"}`); r.Code != 503 || !strings.Contains(r.Body.String(), "outcome unknown") {
		t.Fatalf("a start without quorum = %d %s; want 503 outcome unknown", r.Code, r.Body)
	}
	r = call(t, p.srvs[0], "DELETE", "/api/substitutes/"+subID(flow), "")
	if r.Code != 503 || r.Header().Get(outcomeHeader) != "refused" || !strings.Contains(r.Body.String(), DefaultNoMajority) {
		t.Fatalf("a stop once the log knows it has no majority = %d %q %s; want 503 refused, no majority", r.Code, r.Header().Get(outcomeHeader), r.Body)
	}
	unknown, refused := 0, 0
	for _, e := range p.srvs[0].audit.ring.Recent() {
		switch {
		case strings.HasPrefix(e.Action, "substitute.") && e.Result == audit.ResultUnknown:
			unknown++
		case e.Action == "substitute.stop" && e.Result == audit.ResultError: // a 5xx, as the no-leader refusal is journalled
			refused++
		}
	}
	if unknown != 1 || refused != 1 {
		t.Fatalf("journal: %d unknown, %d refused stops; want the start unknown and the stop refused (journalled as error, a 5xx)", unknown, refused)
	}
	// The expiry does not wait for quorum.
	p.clks[0].t = p.clks[0].t.Add(2 * time.Minute)
	p.clks[1].t = p.clks[1].t.Add(2 * time.Minute)
	p.rts[0].Scan()
	if l := p.rts[0].Substitutes(); len(l) != 0 || p.rts[0].Tags().Real("Flow") != 3 {
		t.Fatalf("%+v; want the substitute expired on time without quorum", l)
	}
	// Heal: the expiry is recorded, and the standby applies it. The start
	// answered "outcome unknown" stayed in A's log, unconfirmed: the
	// expiry's commit commits it too, so it takes effect, the same on both
	// controllers. That is the contract the 503 told the operator.
	p.gates[1].down.Store(false)
	p.gates[2].down.Store(false)
	hasFlow := func(rt *runtime.Runtime) bool {
		for _, e := range rt.Substitutes() {
			if e.Name == "Flow" {
				return true
			}
		}
		return false
	}
	eventually(t, "the expiry recorded after the heal", func() bool {
		p.beat()
		return len(p.rts[0].PendingExpiries()) == 0 && !hasFlow(p.rts[1])
	})
	a, b := p.rts[0].Substitutes(), p.rts[1].Substitutes()
	if len(a) != len(b) || len(a) == 1 && (a[0].ID != b[0].ID || a[0].Name != "Level") {
		t.Fatalf("A %+v, B %+v: the unknown-outcome start must be the same on both", a, b)
	}
	// A stale unsubstitute naming an older entry ends nothing.
	old := decodeSubs(t, startOn(t, p.srvs[0], `{"name":"Level"}`).Body.Bytes()).Started.ID
	newer := decodeSubs(t, startOn(t, p.srvs[0], `{"name":"Level"}`).Body.Bytes()).Started.ID
	if _, err := p.srvs[0].subLog.propose(context.Background(), oplog.Entry{Kind: oplog.KindUnsubstitute, Ref: old, By: "late"}); err != nil {
		t.Fatal(err)
	}
	p.beat()
	for i := 0; i < 2; i++ {
		if l := p.rts[i].Substitutes(); len(l) != 1 || l[0].ID != newer {
			t.Fatalf("instance %d: %+v; a stale unsubstitute ended the newer entry", i, l)
		}
	}
}

// A start whose outcome was unknown, retried with the same request ID, is
// answered with the entry the first one started: never two entries.
func TestARetriedStartIsIdempotent(t *testing.T) {
	p := newPair(t)
	firstScan(t, p.rts[0])
	p.rts[1].Scan()
	p.gates[1].loseWrites.Store(true)
	p.gates[2].loseWrites.Store(true)
	if r := startOn(t, p.srvs[0], `{"name":"Level","requestId":"r-1"}`); r.Code != 503 || !strings.Contains(r.Body.String(), "outcome unknown") {
		t.Fatalf("first try = %d %s; want 503 outcome unknown", r.Code, r.Body)
	}
	p.gates[1].loseWrites.Store(false)
	p.gates[2].loseWrites.Store(false)
	// The lost writes marked the peers unreachable; the wiring's pings
	// (every half lease) find them again before a retry is refused.
	p.nodes[0].Ping(context.Background())
	r := startOn(t, p.srvs[0], `{"name":"Level","requestId":"r-1"}`)
	if r.Code != 200 {
		t.Fatalf("retry = %d %s", r.Code, r.Body)
	}
	id := decodeSubs(t, r.Body.Bytes()).Started.ID
	p.beat()
	for i := 0; i < 2; i++ {
		if l := p.rts[i].Substitutes(); len(l) != 1 || l[0].ID != id {
			t.Fatalf("instance %d: %+v; want one entry, %d", i, l, id)
		}
	}
	if again := startOn(t, p.srvs[0], `{"name":"Level","requestId":"r-1"}`); decodeSubs(t, again.Body.Bytes()).Started.ID != id {
		t.Fatal("a third try with the same request ID started another entry")
	}
}
