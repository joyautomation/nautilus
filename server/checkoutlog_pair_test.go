//go:build !js

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/auth"
	"github.com/joyautomation/nautilus/checkout"
	"github.com/joyautomation/nautilus/leader"
	"github.com/joyautomation/nautilus/oplog"
)

// The I/O checkout on a pair (#719 slice 5), on real oplog nodes: a run is on
// both controllers, a seal signed on one is served and verified from the
// other, writes without a majority are refused and never queued, a replica
// that lost its file catches up by replay, and a superseded history empties
// the store.

type checkoutPair struct {
	*pair
	cls [3]*CheckoutLog
}

func newCheckoutPair(t *testing.T) *checkoutPair {
	t.Helper()
	p := &pair{}
	cp := &checkoutPair{pair: p}
	servers := make([]*httptest.Server, 3)
	for i := range servers {
		servers[i] = httptest.NewUnstartedServer(nil)
		p.urls[i] = "http://" + servers[i].Listener.Addr().String()
	}
	p.lead(0, 1)
	for i := 0; i < 3; i++ {
		cl, err := NewCheckoutLog(ckDir(t))
		if err != nil {
			t.Fatal(err)
		}
		cl.Store().SetSheet(ckSheet())
		cp.cls[i] = cl
		n, err := oplog.New(oplog.Options{Self: p.urls[i], Peers: p.urls[:], Leadership: seat{p: p, self: p.urls[i]},
			Token: "t", Ephemeral: true, Apply: func(e oplog.Entry) error { _, err := cl.Apply(e); return err }})
		if err != nil {
			t.Fatal(err)
		}
		cl.Attach(n)
		p.nodes[i] = n
		p.gates[i] = &pairGate{h: n.Handler()}
		servers[i].Config.Handler = p.gates[i]
		servers[i].Start()
	}
	t.Cleanup(func() {
		for i := range servers {
			servers[i].Close()
			p.nodes[i].Close()
		}
	})
	if err := p.nodes[0].SyncLeader(t.Context()); err != nil {
		t.Fatal(err)
	}
	return cp
}

func (cp *checkoutPair) wait(t *testing.T, ok func() bool) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if ok() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the replicas did not converge")
}

func TestACheckoutOnAPairIsOnBothControllers(t *testing.T) {
	cp := newCheckoutPair(t)
	a, b := cp.cls[0].Store(), cp.cls[1].Store()
	run, err := a.Begin(false, ckSheet().Hash(), "T", "alice", "user:alice")
	if err != nil {
		t.Fatal(err)
	}
	// the standby's HMI records: the entry is forwarded to the leader and committed
	for _, pt := range ckSheet().Points {
		if _, err := b.Record(run.ID, pt.ID, checkout.Pass, "", 1.5, "good", "alice", "user:alice"); err != nil {
			t.Fatalf("record on the standby: %v", err)
		}
	}
	cp.wait(t, func() bool { g, ok := cp.cls[2].Store().Get(run.ID); return ok && g.Results == len(ckSheet().Points) })
	for i := 0; i < 3; i++ {
		if g, ok := cp.cls[i].Store().Get(run.ID); !ok || g.Results != 3 || g.By != "alice" {
			t.Errorf("controller %d: %+v %v", i, g, ok)
		}
	}
	if !bytes.Equal(a.ExportChain(), b.ExportChain()) || !bytes.Equal(a.ExportChain(), cp.cls[2].Store().ExportChain()) {
		t.Errorf("the controllers hold different canonical chains")
	}
}

func TestASealSignedOnOneControllerVerifiesFromTheOtherOnAPair(t *testing.T) {
	cp := newCheckoutPair(t)
	a, b := cp.cls[0].Store(), cp.cls[1].Store()
	run, _ := a.Begin(false, ckSheet().Hash(), "T", "alice", "user:alice")
	for _, pt := range ckSheet().Points {
		if _, err := a.Record(run.ID, pt.ID, checkout.Pass, "", 1.5, "good", "alice", "user:alice"); err != nil {
			t.Fatal(err)
		}
	}
	// signed on the standby (a supervisor's HMI), applied everywhere
	if _, err := b.Seal(checkout.SealInput{Run: run.ID, Sheet: ckSheet(), By: "bob", Principal: "user:bob", Mode: "two-person"}); err != nil {
		t.Fatalf("seal on the standby: %v", err)
	}
	cp.wait(t, func() bool { g, _ := cp.cls[0].Store().Get(run.ID); return g.State == checkout.StateSealed })
	for i, cl := range cp.cls[:2] {
		rec, err := cl.Store().ReadSealed(run.ID)
		if err != nil {
			t.Fatalf("controller %d: %v", i, err)
		}
		if rep, err := checkout.Verify(rec, a.ExportChain()); err != nil || !rep.ChainChecked || rep.Signer != "bob" {
			t.Errorf("controller %d: verify %+v %v", i, rep, err)
		}
	}
	// the key that signed is the signing controller's, on both
	if a.KeyFingerprint() == b.KeyFingerprint() {
		t.Errorf("the two controllers share a signing key")
	}
}

func TestACheckoutWriteWithoutAMajorityIsRefusedAndNeverQueued(t *testing.T) {
	cp := newCheckoutPair(t)
	cp.gates[1].down.Store(true)
	cp.gates[2].down.Store(true)
	for i := 0; i < 3; i++ {
		cp.nodes[0].Ping(t.Context())
	}
	a := cp.cls[0].Store()
	if _, err := a.Begin(false, ckSheet().Hash(), "T", "alice", "user:alice"); !errors.Is(err, checkout.ErrNoMajority) {
		t.Fatalf("begin without a majority: %v", err)
	}
	if len(a.List()) != 0 {
		t.Error("a refused begin was kept in memory")
	}
	cp.gates[1].down.Store(false)
	cp.gates[2].down.Store(false)
	cp.nodes[0].Heartbeat(t.Context())
	time.Sleep(50 * time.Millisecond)
	for i := 0; i < 2; i++ {
		if n := len(cp.cls[i].Store().List()); n != 0 {
			t.Errorf("controller %d holds a run nobody was told was begun", i)
		}
	}
	if _, err := a.Begin(false, ckSheet().Hash(), "T", "alice", "user:alice"); err != nil {
		t.Errorf("begin once the peers are back: %v", err)
	}
}

// A controller that was away while a run was recorded catches up from the
// log when it returns: the same store, the same canonical chain.
func TestAControllerThatWasAwayCatchesUpTheRuns(t *testing.T) {
	cp := newCheckoutPair(t)
	cp.gates[2].down.Store(true) // the witness's copy is away
	a := cp.cls[0].Store()
	run, err := a.Begin(false, ckSheet().Hash(), "T", "alice", "user:alice")
	if err != nil {
		t.Fatal(err)
	}
	for _, pt := range ckSheet().Points {
		if _, err := a.Record(run.ID, pt.ID, checkout.Pass, "", 1.5, "good", "alice", "user:alice"); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := cp.cls[2].Store().Get(run.ID); ok {
		t.Fatal("setup: the away replica already has the run")
	}
	cp.gates[2].down.Store(false)
	cp.nodes[0].Heartbeat(t.Context())
	cp.nodes[2].CatchUp(t.Context())
	cp.wait(t, func() bool { g, ok := cp.cls[2].Store().Get(run.ID); return ok && g.Results == 3 })
	if !bytes.Equal(cp.cls[2].Store().ExportChain(), a.ExportChain()) {
		t.Errorf("a caught-up replica's chain differs")
	}
}

// Entries of other kinds are left for the next handler; a checkout entry with
// no body is loud, not skipped.
func TestCheckoutLogHandsOtherKindsOn(t *testing.T) {
	cl, err := NewCheckoutLog(ckDir(t))
	if err != nil {
		t.Fatal(err)
	}
	if done, _ := cl.Apply(oplog.Entry{Kind: oplog.KindNote}); done {
		t.Error("a note entry was taken by the checkout")
	}
	if done, err := cl.Apply(oplog.Entry{Kind: oplog.KindCheckout}); !done || err == nil {
		t.Errorf("a checkout entry with no body: done=%v err=%v", done, err)
	}
}

// A server over a pair's log store answers the checkout (the 503 is for a
// pair whose store is a file), a person on the standby's screen records into
// the one run, both keys are on the page, and a force is refused on the standby.
func TestAPairsServersAnswerTheCheckoutAndShowBothKeys(t *testing.T) {
	cp := newCheckoutPair(t)
	var standing [2]atomic.Bool
	standing[0].Store(true)
	standing[1].Store(true) // both answer for the test's sign-in; the second is then made the standby
	mk := func(i int) *Server {
		co := &CheckoutOptions{Store: cp.cls[i].Store(), Sheet: ckSheet(), Signoff: "two-person"}
		srv, _, _ := patServerSettings(t, newTestRuntime(t), ckOpts(Options{Cluster: pairSeat{&standing[i]}}, co), authSettingsFor())
		ckReg(srv, co)
		return srv
	}
	lead, stand := mk(0), mk(1)
	// each controller says which key it signs with; both pages show both
	if err := cp.cls[0].Store().AnnounceKey("a"); err != nil {
		t.Fatal(err)
	}
	if err := cp.cls[1].Store().AnnounceKey("b"); err != nil {
		t.Fatal(err)
	}
	cp.wait(t, func() bool {
		return len(cp.cls[0].Store().KeyFingerprints()) == 2 && len(cp.cls[1].Store().KeyFingerprints()) == 2
	})
	eng := ckAs(t, lead, "akhan")
	rec := call(t, lead, "GET", "/api/checkout", "", eng)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/checkout on a pair's leader: %d %s", rec.Code, rec.Body)
	}
	var v checkoutView
	json.Unmarshal(rec.Body.Bytes(), &v)
	if v.KeyFingerprints["a"] == "" || v.KeyFingerprints["b"] == "" || v.KeyFingerprints["a"] == v.KeyFingerprints["b"] || v.KeyFingerprint != v.KeyFingerprints["a"] {
		t.Errorf("the page's keys: own %q, pair %v", v.KeyFingerprint, v.KeyFingerprints)
	}
	// begun on one controller, recorded on the other's server: one run
	var run checkout.Run
	json.Unmarshal(call(t, lead, "POST", "/api/checkout/runs", `{}`, eng).Body.Bytes(), &run)
	seng := ckAs(t, stand, "akhan")
	if rec := call(t, stand, "POST", "/api/checkout/runs/"+run.ID+"/results", `{"point":"SP","outcome":"pass"}`, seng); rec.Code != http.StatusCreated {
		t.Fatalf("a result through the other controller's server: %d %s", rec.Code, rec.Body)
	}
	cp.wait(t, func() bool { g, _ := cp.cls[0].Store().Get(run.ID); return g.Results == 1 })
	// the second becomes the standby: a force drives an output, so not from it
	standing[1].Store(false)
	rec = call(t, stand, "POST", "/api/checkout/runs/"+run.ID+"/force", `{"point":"OUT","value":1,"seconds":10,"confirm":"OUT"}`, seng)
	// (the standby's server turns any write away before the checkout sees it)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "standby") {
		t.Errorf("a force from the standby: %d %s", rec.Code, rec.Body)
	}
}

// pairSeat is a pair member whose leadership a test can take away.
type pairSeat struct{ leads *atomic.Bool }

func (c pairSeat) Status() leader.Status {
	return leader.Status{Mode: "quorum", IsLeader: c.leads.Load(), LeaderAddr: "http://127.0.0.1:1"}
}

func authSettingsFor() auth.Settings {
	return auth.Settings{Users: map[string][]auth.Grant{
		"akhan":  {{Role: auth.Engineer}},
		"jsmith": {{Role: auth.Supervisor}},
		"ghost":  {{Role: auth.Operator}},
	}}
}

// Propose can return on a follower once the leader committed, before this
// replica applied the entry: the adapter waits for its commit index, and says
// "unknown" rather than success if it does not come.
func TestAProposeIsNotAnswerUntilItIsAppliedHere(t *testing.T) {
	var commit uint64
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := waitApplied(ctx, func() uint64 { return commit }, 5); !errors.Is(err, checkout.ErrUnknown) {
		t.Errorf("an entry not applied here: %v, want ErrUnknown", err)
	}
	go func() { time.Sleep(50 * time.Millisecond); atomic.StoreUint64(&commit, 5) }()
	ctx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()
	if err := waitApplied(ctx2, func() uint64 { return atomic.LoadUint64(&commit) }, 5); err != nil {
		t.Errorf("an entry applied while waiting: %v", err)
	}
}

// A force the record says stands that this controller is not driving is ended;
// one it is driving is left.
func TestReconcileEndsAForceNothingDrives(t *testing.T) {
	st := newCkStore(t)
	co := &CheckoutOptions{Store: st, Sheet: ckSheet()}
	run, _ := st.Begin(false, ckSheet().Hash(), "T", "alice", "user:alice")
	if n, err := co.ReconcileForces(); n != 0 || err != nil {
		t.Fatalf("nothing stands: %d %v", n, err)
	}
	rec, err := st.StartForce(run.ID, "OUT", 5.5, 30, "alice", "user:alice")
	if err != nil {
		t.Fatal(err)
	}
	// driven here (the slot names it): left
	co.ck.mu.Lock()
	co.ck.force = &ckForce{run: run.ID, point: "OUT"}
	co.ck.mu.Unlock()
	if n, _ := co.ReconcileForces(); n != 0 {
		t.Errorf("a force this controller drives was ended")
	}
	// not driven (the other controller's, or a start whose answer was lost): ended
	co.ck.mu.Lock()
	co.ck.force = nil
	co.ck.mu.Unlock()
	if n, err := co.ReconcileForces(); n != 1 || err != nil {
		t.Fatalf("a force nothing drives: %d %v", n, err)
	}
	if _, ok := st.Standing(); ok {
		t.Errorf("the force still stands (seq %d)", rec.Seq)
	}
}
