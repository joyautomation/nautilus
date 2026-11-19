package node

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/quorum"
)

const testToken = "peer-secret-0123456789"

// testCluster is a cluster ID for tests.
const testCluster = "0123456789abcdef0123456789abcdef"

// fakeClock is a clock a test moves by hand.
type fakeClock struct{ t atomic.Int64 }

func (c *fakeClock) now() quorum.Instant { return quorum.Instant(c.t.Load()) }
func (c *fakeClock) add(d time.Duration) { c.t.Add(int64(d)) }
func newFakeClock() *fakeClock           { c := &fakeClock{}; c.t.Store(int64(time.Hour)); return c }
func voteBody(t *testing.T, r quorum.Request) []byte {
	t.Helper()
	b, err := r.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func testOptions(t *testing.T, self, peer quorum.NodeID) Options {
	return Options{Cluster: testCluster, Self: self, Peer: peer, PeerAddr: "127.0.0.1:1", WitnessAddr: "127.0.0.1:1",
		Lease: 400 * time.Millisecond, StatePath: filepath.Join(t.TempDir(), "quorum.state"), Token: testToken}
}

// The peer listener answers nothing without the peer token, whatever the
// Host: not on localhost, not by IP, not by name, and not with the client
// token (design §6.1, test plan §8.2 case 1).
func TestGuardRequiresThePeerToken(t *testing.T) {
	if _, err := Guard("", http.NotFoundHandler()); err == nil {
		t.Fatal("Guard built without a token")
	}
	h, err := Guard(testToken, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) }))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		host, token string
		want        int
	}{
		{"localhost:9450", "", http.StatusUnauthorized},
		{"127.0.0.1:9450", "", http.StatusUnauthorized},
		{"[::1]:9450", "", http.StatusUnauthorized},
		{"10.1.2.3:9450", "", http.StatusUnauthorized},
		{"ctl-b:9450", "", http.StatusUnauthorized},
		{"127.0.0.1:9450", "client-token", http.StatusUnauthorized},
		{"ctl-b:9450", testToken + "x", http.StatusUnauthorized},
		{"ctl-b:9450", testToken, http.StatusTeapot},
		{"10.1.2.3:9450", testToken, http.StatusTeapot},
	} {
		r := httptest.NewRequest(http.MethodPost, "http://"+c.host+Prefix+testCluster+"/vote", nil)
		if c.token != "" {
			r.Header.Set(TokenHeader, c.token)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != c.want {
			t.Errorf("Host %s, token %q: %d, want %d", c.host, c.token, rec.Code, c.want)
		}
	}
}

// A voter refuses a vote whose lease is longer than its own promise: the
// requester's lease would outlive the promise it rests on.
func TestVoteLeaseLongerThanThePromiseRefused(t *testing.T) {
	w, err := NewWitness(WitnessOptions{Dir: t.TempDir(), Token: testToken, Lease: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	running(t, w.Run)
	waitFor(t, time.Second, func() bool { return w.running.Load() })
	h := w.Handler()
	for _, c := range []struct {
		lease string
		want  int
	}{
		{"", http.StatusConflict},
		{"garbage", http.StatusConflict},
		{strconv.FormatInt(int64(2*time.Second), 10), http.StatusConflict},
		{strconv.FormatInt(int64(time.Second+1), 10), http.StatusConflict},
		{strconv.FormatInt(int64(time.Second), 10), http.StatusOK},
		{strconv.FormatInt(int64(time.Second/2), 10), http.StatusOK},
	} {
		r := httptest.NewRequest(http.MethodPost, Prefix+testCluster+"/vote", bytes.NewReader(voteBody(t, quorum.Request{From: "a", Nonce: 5, ID: 1})))
		if c.lease != "" {
			r.Header.Set(LeaseHeader, c.lease)
		}
		r.Header.Set(MembersHeader, "a,b")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != c.want {
			t.Errorf("lease %q: %d, want %d", c.lease, rec.Code, c.want)
		}
	}
}

// The gate is judged against the clock when it is READ: an elector whose
// loop has stalled (or is starved) cannot hold it open past the lease end.
func TestGateJudgedWhenRead(t *testing.T) {
	clk := newFakeClock()
	o := testOptions(t, "a", "b")
	o.clock = clk.now
	e, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	e.gate.Store(&gateState{epoch: 7, end: clk.now().Add(300 * time.Millisecond), open: true})
	if !e.IsLeader() || e.Term() != 7 {
		t.Fatalf("inside the lease: IsLeader %v, Term %d", e.IsLeader(), e.Term())
	}
	// It closes the fence margin before the lease end (design §3.4).
	clk.add(300*time.Millisecond - e.Fence() - time.Millisecond)
	if !e.IsLeader() {
		t.Fatal("closed before the fence margin")
	}
	clk.add(time.Millisecond)
	if e.IsLeader() || e.Term() != 7 {
		t.Fatalf("at the fence margin, with no look since: IsLeader %v, Term %d; want closed, and the last term (7) kept", e.IsLeader(), e.Term())
	}
}

// Design §3.4: the gate closes a fence margin (net delay plus send margin)
// BEFORE the lease ends, not at its end, so a send that passed the check
// still leaves while the lease holds; and SendGate gives, from the same
// read, the budget a send may take: the lease left minus the net delay.
func TestGateClosesAFenceMarginBeforeTheLeaseEnd(t *testing.T) {
	clk := newFakeClock()
	o := testOptions(t, "a", "b")
	o.clock = clk.now
	o.NetDelay, o.SendMargin = 50*time.Millisecond, 20*time.Millisecond
	e, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	e.gate.Store(&gateState{epoch: 3, end: clk.now().Add(time.Second), open: true})
	if ok, budget, term := e.SendGate(); !ok || budget != 950*time.Millisecond || term != 3 {
		t.Fatalf("1 s left: SendGate %v %s term %d; want open with 950 ms (the lease left minus the 50 ms net delay), term 3", ok, budget, term)
	}
	clk.add(929 * time.Millisecond) // 71 ms left: just over the 70 ms margin
	if ok, budget, _ := e.SendGate(); !ok || budget != 21*time.Millisecond || !e.Status().Leading {
		t.Fatalf("71 ms left: SendGate %v %s, Leading %v; want open with 21 ms", ok, budget, e.Status().Leading)
	}
	clk.add(time.Millisecond) // 70 ms left: the margin
	if ok, budget, _ := e.SendGate(); ok || budget != 0 || e.IsLeader() || e.Status().Leading {
		t.Fatalf("70 ms left: SendGate %v %s, IsLeader %v, Leading %v; want closed a fence margin before the lease end", ok, budget, e.IsLeader(), e.Status().Leading)
	}
}

// The margin's parts scale with a short lease and stay under a quarter of
// any lease; one set beyond that is refused at New.
func TestFenceDefaultsAndBound(t *testing.T) {
	for _, c := range []struct{ lease, dn, ms time.Duration }{
		{2 * time.Second, 50 * time.Millisecond, 20 * time.Millisecond},
		{200 * time.Millisecond, 25 * time.Millisecond, 12500 * time.Microsecond},
	} {
		dn, ms := FenceDefaults(c.lease)
		if dn != c.dn || ms != c.ms || dn+ms >= c.lease/4 {
			t.Errorf("FenceDefaults(%s) = %s, %s; want %s, %s, under a quarter of the lease", c.lease, dn, ms, c.dn, c.ms)
		}
	}
	o := testOptions(t, "a", "b")
	o.Lease, o.NetDelay, o.SendMargin = 2*time.Second, 480*time.Millisecond, 20*time.Millisecond
	if _, err := New(o); err == nil || !strings.Contains(err.Error(), "fence margin") {
		t.Fatalf("a 500 ms margin on a 2 s lease: %v; want refused", err)
	}
}

// The boot nonce is drawn at random at every start and never persisted:
// two starts on one state file get two nonces, and the file never holds
// either (#1008 (c)).
func TestBootNonceDrawnPerStartNeverPersisted(t *testing.T) {
	o := testOptions(t, "a", "b")
	var nonces []uint64
	for range 3 {
		e, err := New(o)
		if err != nil {
			t.Fatal(err)
		}
		n := e.Nonce()
		if n == 0 {
			t.Fatal("a zero nonce")
		}
		for _, m := range nonces {
			if m == n {
				t.Fatalf("two starts drew the same nonce %#x", n)
			}
		}
		nonces = append(nonces, n)
		// Make the voter write its state: an operator's floor.
		stop := running(t, e.Run)
		if err := e.Recommission(context.Background(), 0xabc, e.KnownFloor()+10); err != nil {
			t.Fatal(err)
		}
		stop()
		e.Close()
		b, err := os.ReadFile(o.StatePath)
		if err != nil {
			t.Fatal(err)
		}
		var be [8]byte
		binary.BigEndian.PutUint64(be[:], n)
		le := binary.LittleEndian.AppendUint64(nil, n)
		if bytes.Contains(b, be[:]) || bytes.Contains(b, le) {
			t.Fatalf("the state file holds the boot nonce %#x", n)
		}
	}
}

func TestNewRefuses(t *testing.T) {
	o := testOptions(t, "a", "b")
	o.Token = ""
	if _, err := New(o); err == nil {
		t.Error("started a pair member without a peer token")
	}
	o = testOptions(t, "a", "b")
	o.Cluster = "../etc"
	if _, err := New(o); err == nil {
		t.Error("took a cluster ID that is a path")
	}
	o = testOptions(t, "a", "a")
	if _, err := New(o); err == nil {
		t.Error("took a peer with its own name")
	}
	saved := bootClock
	bootClock = false
	defer func() { bootClock = saved }()
	if _, err := New(testOptions(t, "a", "b")); err == nil {
		t.Error("a controller started without CLOCK_BOOTTIME")
	}
}

// A controller whose state file can't be read starts uncommissioned (it
// grants nothing until it re-adopts), and keeps the bad file aside.
func TestUnreadableStateStartsUncommissioned(t *testing.T) {
	o := testOptions(t, "a", "b")
	good, _ := sample.MarshalBinary()
	if err := os.WriteFile(o.StatePath, good[:len(good)-3], 0o600); err != nil {
		t.Fatal(err)
	}
	e, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if s := e.Status(); s.Commissioned || s.Incarnation != 0 {
		t.Fatalf("started commissioned from a torn file: %+v", s)
	}
	kept, _ := filepath.Glob(o.StatePath + ".unreadable-*")
	if len(kept) != 1 {
		t.Fatalf("the unreadable file was not kept aside: %v", kept)
	}
}

// A grant leaves only once the state it promises is on disk: the vote's
// answer waits for the write (fsync before answer, at the driver).
func TestGrantLeavesOnlyAfterTheWrite(t *testing.T) {
	release := make(chan struct{})
	var writes atomic.Int32
	var writing atomic.Bool
	clk := newFakeClock()
	// The incarnation's first write goes through; every later one (the
	// grant's) blocks until released.
	w, err := NewWitness(WitnessOptions{Dir: t.TempDir(), Token: testToken, Lease: time.Second, clock: clk.now,
		write: func(f *os.File, b []byte) (int, error) {
			if writes.Add(1) > 1 {
				writing.Store(true)
				<-release
			}
			return f.Write(b)
		}})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	running(t, w.Run)
	// Runs before running's cleanup (LIFO): a failure below must not leave
	// the writer blocked, or stopping Run would wait for it for ever.
	var freed sync.Once
	free := func() { freed.Do(func() { close(release) }) }
	t.Cleanup(free)
	waitFor(t, time.Second, func() bool { return w.running.Load() })
	h := w.Handler()
	vote := func(req quorum.Request) chan *httptest.ResponseRecorder {
		out := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			r := httptest.NewRequest(http.MethodPost, Prefix+testCluster+"/vote", bytes.NewReader(voteBody(t, req)))
			r.Header.Set(LeaseHeader, strconv.FormatInt(int64(time.Second), 10))
			r.Header.Set(MembersHeader, "a,b")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)
			out <- rec
		}()
		return out
	}
	// The first vote creates the pair and its incarnation is written; the
	// witness is in its restart wait, so it is refused at once.
	if rec := <-vote(quorum.Request{From: "a", Term: 1, Nonce: 5, ID: 1}); rec.Code != http.StatusOK {
		t.Fatalf("first vote: %d", rec.Code)
	}
	waitFor(t, time.Second, func() bool { return writes.Load() == 1 })
	clk.add(2 * time.Second) // past the restart wait
	got := vote(quorum.Request{From: "a", Term: 1, Nonce: 5, ID: 2})
	waitFor(t, time.Second, writing.Load)
	select {
	case rec := <-got:
		var rep quorum.Reply
		_ = rep.UnmarshalBinary(rec.Body.Bytes())
		t.Fatalf("answered (%d, granted=%v) while its state write was still blocked", rec.Code, rep.Granted)
	case <-time.After(150 * time.Millisecond):
	}
	free()
	select {
	case rec := <-got:
		var rep quorum.Reply
		if rec.Code != http.StatusOK || rep.UnmarshalBinary(rec.Body.Bytes()) != nil || !rep.Granted {
			t.Fatalf("after the write: %d %+v", rec.Code, rep)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no grant after the write")
	}
}

// The witness won't start over a state file it can't read, unless told to
// rebuild: then the file is kept aside and the pair gets a new
// incarnation, which its controllers adopt only together.
func TestWitnessRefusesUnreadableStateWithoutRebuild(t *testing.T) {
	dir := t.TempDir()
	good, _ := quorum.VoterState{Incarnation: 77}.MarshalBinary()
	path := filepath.Join(dir, testCluster+stateSuffix)
	if err := os.WriteFile(path, good[:len(good)-1], 0o600); err != nil {
		t.Fatal(err)
	}
	if w, err := NewWitness(WitnessOptions{Dir: dir, Token: testToken}); err == nil {
		w.Close()
		t.Fatal("started over an unreadable state file")
	}
	w, err := NewWitness(WitnessOptions{Dir: dir, Token: testToken, Rebuild: true, draw: func() uint64 { return 88 }})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if kept, _ := filepath.Glob(path + ".unreadable-*"); len(kept) != 1 {
		t.Fatalf("not kept aside: %v", kept)
	}
	running(t, w.Run)
	waitFor(t, time.Second, func() bool { return w.running.Load() })
	p, err := w.pair(testCluster, "a,b")
	if err != nil {
		t.Fatal(err)
	}
	if inc := p.v.State().Incarnation; inc != 88 {
		t.Fatalf("incarnation %d after a rebuild, want a new one (88)", inc)
	}
}

// A witness with a readable state keeps its incarnation across a restart.
func TestWitnessKeepsItsIncarnation(t *testing.T) {
	dir := t.TempDir()
	start := func(draw uint64) uint64 {
		w, err := NewWitness(WitnessOptions{Dir: dir, Token: testToken, draw: func() uint64 { return draw }})
		if err != nil {
			t.Fatal(err)
		}
		defer w.Close()
		defer running(t, w.Run)()
		waitFor(t, time.Second, func() bool { return w.running.Load() })
		p, err := w.pair(testCluster, "a,b")
		if err != nil {
			t.Fatal(err)
		}
		waitFor(t, time.Second, func() bool {
			_, err := (&State{Path: filepath.Join(dir, testCluster+stateSuffix)}).Load()
			return err == nil
		})
		return p.v.State().Incarnation
	}
	if a, b := start(1), start(2); a != 1 || b != 1 {
		t.Fatalf("incarnations %d then %d, want 1 then 1", a, b)
	}
}

func waitFor(t *testing.T, d time.Duration, ok func() bool) {
	t.Helper()
	end := time.Now().Add(d)
	for !ok() {
		if time.Now().After(end) {
			t.Fatal("timed out")
		}
		time.Sleep(time.Millisecond)
	}
}

// running runs run until stop, or the test's end, and waits for it to
// return: before the test's temporary directories are removed.
func running(t *testing.T, run func(context.Context)) (stop func()) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { run(ctx); close(done) }()
	var once sync.Once
	stop = func() { once.Do(func() { cancel(); <-done }) }
	t.Cleanup(stop)
	return stop
}

// A state file is bound to the member and the pair that wrote it (#1028
// review): a controller refuses to start over one another member wrote
// (NAUTILUS_NODE copied with an env file, the state kept) or one written
// for another pair, and starts over its own. The witness refuses another
// pair's file under a pair's name.
func TestStateFileBoundToItsOwner(t *testing.T) {
	clk := newFakeClock()
	write := func(path string, owner quorum.NodeID, cluster string) {
		st := &State{Path: path, Owner: owner, Cluster: cluster}
		if err := st.Save(quorum.VoterState{Promised: 1002, GrantedTo: owner, Incarnation: 7, Gen: 1, Floor: 1000, Lease: 400 * time.Millisecond}); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		name    string
		owner   quorum.NodeID
		cluster string
		ok      bool
	}{
		{"its own", "a", testCluster, true},
		{"the other member's", "b", testCluster, false},
		{"another pair's", "a", NewClusterID(), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			o := testOptions(t, "a", "b")
			o.clock = clk.now
			write(o.StatePath, c.owner, c.cluster)
			e, err := New(o)
			if c.ok {
				if err != nil {
					t.Fatal(err)
				}
				defer e.Close()
				if !e.Status().Commissioned {
					t.Error("its own state was not loaded")
				}
				return
			}
			if !errors.Is(err, ErrNotOwner) {
				t.Fatalf("started over %s state file: %v", c.name, err)
			}
			if _, err := os.Stat(o.StatePath); err != nil {
				t.Errorf("the refused file was moved or removed: %v", err)
			}
		})
	}
	// The witness: another pair's file under this pair's name.
	dir := t.TempDir()
	write(filepath.Join(dir, testCluster+stateSuffix), WitnessID, NewClusterID())
	w, stop := startWitness(t, WitnessOptions{Dir: dir, Lease: time.Second, clock: clk.now})
	defer stop()
	if _, err := w.pair(testCluster, "a,b"); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("the witness took another pair's state file: %v", err)
	}
}

// The hello's peer info (#710 PR 3b-2b, test plan F30). A peer naming
// another configuration of the pair holds this controller's campaigns
// (a leader would keep renewing) and raises config-differs; once it
// matches again the hold lifts. A peer name answering under a boot nonce
// it had given up for a newer one is two processes; a restart (a new
// nonce that stays) is not.
func TestHelloPeerInfo(t *testing.T) {
	o := testOptions(t, "a", "b")
	clk := newFakeClock()
	o.clock = clk.now
	o.Config = ConfigHash("ctl-a:9450", "ctl-b:9450", "wit:9460")
	var events []Event
	var mu sync.Mutex
	o.OnEvent = func(ev Event) { mu.Lock(); events = append(events, ev); mu.Unlock() }
	e, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	now := clk.now()
	if !e.mayCampaign(now) {
		t.Fatal("held before any hello")
	}
	e.heard(peerInfo{Config: ConfigHash("ctl-a:9450", "ctl-b:9450", "other-wit:9460"), Nonce: 1, Caps: []string{KeepsLiveIO}})
	if !e.Status().ConfigDiffers || e.mayCampaign(now) {
		t.Fatal("a peer with another witness did not hold this controller's campaigns")
	}
	if caps, known := e.PeerCaps(); !known || len(caps) != 1 || caps[0] != KeepsLiveIO {
		t.Fatalf("peer caps %v %v", caps, known)
	}
	// A peer that stops saying so (down, or its link) holds nothing once
	// three hellos have gone unheard: a lease that lapsed meanwhile can be
	// won again.
	e.mu.Lock()
	e.cfgDiffAt = time.Now().Add(-e.cfgStale())
	e.mu.Unlock()
	if !e.mayCampaign(now) {
		t.Fatal("a mismatch the peer stopped reporting still holds this controller's campaigns")
	}
	e.heard(peerInfo{Config: ConfigHash("ctl-a:9450", "ctl-b:9450", "other-wit:9460"), Nonce: 1, Caps: []string{KeepsLiveIO}})
	if e.mayCampaign(now) {
		t.Fatal("a fresh report of the mismatch did not hold campaigns again")
	}
	e.heard(peerInfo{Config: o.Config, Nonce: 1})
	if e.Status().ConfigDiffers || !e.mayCampaign(now) {
		t.Fatal("the hold did not lift once the configuration matched")
	}
	if caps, _ := e.PeerCaps(); len(caps) != 0 {
		t.Fatalf("a hello with no caps left %v", caps)
	}
	// Nonces: 1 (above), then a restart to 2, then 2 again: no duplicate.
	e.heard(peerInfo{Config: o.Config, Nonce: 2})
	e.heard(peerInfo{Config: o.Config, Nonce: 2})
	if n := e.Status().HelloDuplicates; n != 0 {
		t.Fatalf("a restart counted as %d duplicates", n)
	}
	e.heard(peerInfo{Config: o.Config, Nonce: 1}) // the old one answers again
	if n := e.Status().HelloDuplicates; n != 1 {
		t.Fatalf("nonces 1, 2, 1 counted %d duplicates, want 1", n)
	}
	ectx, ecancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	e.emit(ectx) // drains the events queued so far
	ecancel()
	mu.Lock()
	defer mu.Unlock()
	kinds := map[EventKind]int{}
	for _, ev := range events {
		kinds[ev.Kind]++
	}
	if kinds[EventConfigDiffers] != 2 || kinds[EventDuplicateMember] != 1 {
		t.Fatalf("events %v; want config-differs raised and cleared, and one duplicate-member", kinds)
	}
}

// The answerer's peer info is bound to the asker's challenge and both
// names: an answer whose info doesn't match its MAC is ignored.
func TestHelloInfoBoundByItsMAC(t *testing.T) {
	pi := peerInfo{Config: "c", Nonce: 7, Caps: []string{KeepsLiveIO}}
	m := infoMAC(testToken, "challenge-0123456789", "a", "b", pi)
	if !sameMAC(m, infoMAC(testToken, "challenge-0123456789", "a", "b", pi)) {
		t.Fatal("the MAC does not verify")
	}
	for name, other := range map[string]string{
		"another challenge": infoMAC(testToken, "challenge-9876543210", "a", "b", pi),
		"reflected":         infoMAC(testToken, "challenge-0123456789", "b", "a", pi),
		"other caps":        infoMAC(testToken, "challenge-0123456789", "a", "b", peerInfo{Config: "c", Nonce: 7}),
		"another token":     infoMAC("other", "challenge-0123456789", "a", "b", pi),
	} {
		if sameMAC(m, other) {
			t.Errorf("%s: the MAC still verifies", name)
		}
	}
}

// A state file written on another box (#1029) is treated as lost: the
// controller starts uncommissioned (so a cloned disk never votes as the
// original), keeps the file aside, and reports which part of the box
// changed. The same box loads it as before (box_unchecked turns this
// red).
func TestStateFromAnotherBoxIsLost(t *testing.T) {
	clk := newFakeClock()
	here := BoxFingerprint("machine-1", []string{"aa:bb:cc:00:00:01"})
	for _, c := range []struct {
		name, written, want string
	}{
		{"the same box", here, ""},
		{"another NIC", BoxFingerprint("machine-1", []string{"aa:bb:cc:00:00:02"}), "peer-link MAC changed"},
		{"another machine", BoxFingerprint("machine-2", []string{"aa:bb:cc:00:00:01"}), "machine-id changed"},
		{"both", BoxFingerprint("machine-2", []string{"aa:bb:cc:00:00:09"}), "machine-id and peer-link MAC changed"},
	} {
		t.Run(c.name, func(t *testing.T) {
			o := testOptions(t, "a", "b")
			o.clock, o.Box = clk.now, here
			st := &State{Path: o.StatePath, Owner: "a", Cluster: testCluster, Box: c.written}
			if err := st.Save(quorum.VoterState{Promised: 1002, GrantedTo: "a", Incarnation: 7, Gen: 1, Floor: 1000, Lease: o.Lease}); err != nil {
				t.Fatal(err)
			}
			e, err := New(o)
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			s := e.Status()
			if c.want == "" {
				if !s.Commissioned || s.BoxChanged != "" {
					t.Fatalf("the same box: commissioned %v, box changed %q", s.Commissioned, s.BoxChanged)
				}
				return
			}
			if s.Commissioned || !strings.Contains(s.BoxChanged, c.want) || !strings.Contains(s.BoxChanged, "COPY") {
				t.Fatalf("commissioned %v, box changed %q; want uncommissioned, naming %q and how to tell a clone", s.Commissioned, s.BoxChanged, c.want)
			}
			if _, err := os.Stat(o.StatePath); !os.IsNotExist(err) {
				t.Error("the other box's file is still in place")
			}
			kept, _ := filepath.Glob(o.StatePath + ".other-box-*")
			if len(kept) != 1 {
				t.Errorf("the other box's file was not kept aside: %v", kept)
			}
		})
	}
}

// A box that can't be read yet (BoxFunc answers "": a peer address that
// doesn't resolve) holds the controller: it neither votes nor campaigns,
// and leaves its file untouched, until it can be read; then the check
// runs (#1034 review: never skipped). A box that turns out the same
// carries on commissioned; another is treated as lost (box_wait_skipped
// turns this red).
func TestUnreadableBoxHoldsTheVoter(t *testing.T) {
	stored := BoxFingerprint("machine-1", []string{"aa:bb:cc:00:00:01"})
	for _, c := range []struct {
		name, later string
		lost        bool
	}{
		{"then the same box", stored, false},
		{"then another box (a clone)", BoxFingerprint("machine-1", []string{"aa:bb:cc:00:00:09"}), true},
	} {
		t.Run(c.name, func(t *testing.T) {
			o := testOptions(t, "a", "b")
			var box atomic.Value
			box.Store("")
			o.BoxFunc = func() string { return box.Load().(string) }
			st := &State{Path: o.StatePath, Owner: "a", Cluster: testCluster, Box: stored}
			if err := st.Save(quorum.VoterState{Promised: 1002, GrantedTo: "a", Incarnation: 7, Gen: 1, Floor: 1000, Lease: o.Lease}); err != nil {
				t.Fatal(err)
			}
			e, err := New(o)
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			if !e.Status().BoxPending {
				t.Fatal("an unreadable box did not hold the controller")
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() { e.Run(ctx); close(done) }()
			defer func() { cancel(); <-done }()
			time.Sleep(4 * o.Lease)
			if e.running.Load() || !e.Status().BoxPending {
				t.Fatal("the controller runs (votes) while its box can't be read")
			}
			if got, err := (&State{Path: o.StatePath}).Load(); err != nil || got.Box != stored {
				t.Fatalf("the file was touched while held: %+v, %v", got, err)
			}
			box.Store(c.later)
			deadline := time.Now().Add(5 * time.Second)
			for e.Status().BoxPending || !e.running.Load() {
				if time.Now().After(deadline) {
					t.Fatal("the controller did not start once its box could be read")
				}
				time.Sleep(10 * time.Millisecond)
			}
			s := e.Status()
			if c.lost {
				if s.Commissioned || !strings.Contains(s.BoxChanged, "peer-link MAC") {
					t.Fatalf("another box: commissioned %v, box changed %q; want treated as lost", s.Commissioned, s.BoxChanged)
				}
			} else if !s.Commissioned || s.BoxChanged != "" {
				t.Fatalf("the same box: commissioned %v, box changed %q", s.Commissioned, s.BoxChanged)
			}
		})
	}
}
