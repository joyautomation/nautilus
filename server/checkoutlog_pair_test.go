//go:build !js

package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
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
		n, err := oplog.New(oplog.Options{Self: p.urls[i], Peers: p.urls[:], Leadership: seat{p, p.urls[i]},
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
	mk := func(i int, leaderNow bool) (*Server, *CheckoutOptions) {
		co := &CheckoutOptions{Store: cp.cls[i].Store(), Sheet: ckSheet(), Signoff: "two-person"}
		srv, _, _ := patServerSettings(t, newTestRuntime(t), ckOpts(Options{Cluster: fakeCluster{status: leader.Status{Mode: "quorum", IsLeader: leaderNow}}}, co), authSettingsFor())
		ckReg(srv, co)
		return srv, co
	}
	lead, _ := mk(0, true)
	stand, _ := mk(1, false)
	// each controller says which key it signs with; both pages show both
	if err := cp.cls[0].Store().AnnounceKey("a"); err != nil {
		t.Fatal(err)
	}
	if err := cp.cls[1].Store().AnnounceKey("b"); err != nil {
		t.Fatal(err)
	}
	cp.wait(t, func() bool { return len(cp.cls[0].Store().KeyFingerprints()) == 2 && len(cp.cls[1].Store().KeyFingerprints()) == 2 })
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
	// begun on the leader's screen, recorded from the standby's
	var run checkout.Run
	json.Unmarshal(call(t, lead, "POST", "/api/checkout/runs", `{}`, eng).Body.Bytes(), &run)
	seng := ckAs(t, stand, "akhan")
	if rec := call(t, stand, "POST", "/api/checkout/runs/"+run.ID+"/results", `{"point":"SP","outcome":"pass"}`, seng); rec.Code != http.StatusCreated {
		t.Fatalf("a result from the standby's screen: %d %s", rec.Code, rec.Body)
	}
	cp.wait(t, func() bool { g, _ := cp.cls[0].Store().Get(run.ID); return g.Results == 1 })
	// a force drives an output: not from the standby
	rec = call(t, stand, "POST", "/api/checkout/runs/"+run.ID+"/force", `{"point":"OUT","value":1,"seconds":10,"confirm":"OUT"}`, seng)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "standby") {
		t.Errorf("a force from the standby: %d %s", rec.Code, rec.Body)
	}
}

func authSettingsFor() auth.Settings {
	return auth.Settings{Users: map[string][]auth.Grant{
		"akhan":  {{Role: auth.Engineer}},
		"jsmith": {{Role: auth.Supervisor}},
		"ghost":  {{Role: auth.Operator}},
	}}
}
