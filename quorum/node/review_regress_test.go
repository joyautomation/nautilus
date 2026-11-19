package node

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
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

// The #1021 review's findings, each as a test that fails without its fix
// (each has a mutation row in tools/quorum-mutants).

// vote posts req to h for testCluster, stating lease and members, and
// returns the status and the reply.
func vote(t *testing.T, h http.Handler, lease time.Duration, members string, req quorum.Request) (int, quorum.Reply) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, Prefix+testCluster+"/vote", bytes.NewReader(voteBody(t, req)))
	r.Header.Set(LeaseHeader, strconv.FormatInt(int64(lease), 10))
	if members != "" {
		r.Header.Set(MembersHeader, members)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	var rep quorum.Reply
	_ = rep.UnmarshalBinary(rec.Body.Bytes())
	return rec.Code, rep
}

// startWitness starts a witness and returns it with its stop.
func startWitness(t *testing.T, o WitnessOptions) (*Witness, func()) {
	t.Helper()
	if o.Token == "" {
		o.Token = testToken
	}
	w, err := NewWitness(o)
	if err != nil {
		t.Fatal(err)
	}
	stop := running(t, w.Run)
	waitFor(t, time.Second, func() bool { return w.running.Load() })
	return w, func() { stop(); w.Close() }
}

// (1) HIGH: a witness restarted with a shorter --lease must not grant
// inside a promise made under the longer one. All on 4 s; A leads term 7
// on {A, W}; W restarts at once on 2 s; B asks just over 2 s later
// (naut-lead's repro, reproduced on 9351179c).
func TestReviewWitnessShorterLeaseRestart(t *testing.T) {
	dir := t.TempDir()
	clk := newFakeClock()
	w1, stop1 := startWitness(t, WitnessOptions{Dir: dir, Lease: 4 * time.Second, clock: clk.now})
	vote(t, w1.Handler(), 4*time.Second, "a,b", quorum.Request{From: "a", Term: 1, Nonce: 5, ID: 1})
	clk.add(5 * time.Second)
	waitFor(t, 2*time.Second, func() bool {
		_, rep := vote(t, w1.Handler(), 4*time.Second, "a,b", quorum.Request{From: "a", Term: 7, Nonce: 5, ID: 2})
		return rep.Granted
	})
	aEnd := clk.now().Add(time.Duration(float64(4*time.Second) * (1 - DefaultRho)))
	stop1()
	w2, stop2 := startWitness(t, WitnessOptions{Dir: dir, Lease: 2 * time.Second, clock: clk.now})
	defer stop2()
	// A's renewal, on 4 s, is still seen in the wait (not refused for
	// its lease), and granted nothing.
	if code, rep := vote(t, w2.Handler(), 4*time.Second, "a,b", quorum.Request{From: "a", Term: 7, Renewal: true, Nonce: 5, ID: 3}); code != http.StatusOK || rep.Granted {
		t.Fatalf("A's renewal in the restart wait: %d %+v", code, rep)
	}
	vote(t, w2.Handler(), 2*time.Second, "a,b", quorum.Request{From: "b", Term: 8, Nonce: 9, ID: 9})
	clk.add(2*time.Second + time.Millisecond)
	for i := range 20 {
		_, rep := vote(t, w2.Handler(), 2*time.Second, "a,b", quorum.Request{From: "b", Term: 8, Nonce: 9, ID: uint64(10 + i)})
		if rep.Granted && clk.now() < aEnd {
			t.Fatalf("the witness granted B term 8 at %v, inside A's term-7 lease (to %v)", clk.now(), aEnd)
		}
		time.Sleep(5 * time.Millisecond)
	}
	// Once the 4 s lease is over, B is granted, and from then on a 4 s
	// lease is refused.
	clk.add(2 * time.Second)
	waitFor(t, 2*time.Second, func() bool {
		_, rep := vote(t, w2.Handler(), 2*time.Second, "a,b", quorum.Request{From: "b", Term: 8, Nonce: 9, ID: 99})
		return rep.Granted
	})
	if code, _ := vote(t, w2.Handler(), 4*time.Second, "a,b", quorum.Request{From: "a", Term: 9, Nonce: 5, ID: 4}); code != http.StatusConflict {
		t.Fatalf("a 4 s lease after the wait: %d, want 409", code)
	}
}

// commissioned starts a controller elector from a commissioned state file,
// on a fake clock past its restart wait. write, when set, replaces its
// state writes.
func commissioned(t *testing.T, write func(*os.File, []byte) (int, error), onEvent func(Event)) (*Elector, *fakeClock, Options) {
	t.Helper()
	o := testOptions(t, "a", "b")
	clk := newFakeClock()
	o.clock, o.write, o.OnEvent = clk.now, write, onEvent
	if err := (&State{Path: o.StatePath}).Save(quorum.VoterState{Incarnation: 5, Gen: 1, Lease: o.Lease}); err != nil {
		t.Fatal(err)
	}
	e, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Close)
	running(t, e.Run)
	waitFor(t, time.Second, e.running.Load)
	clk.add(2 * o.Lease)
	return e, clk, o
}

// (2) A controller whose state write fails sends no grant.
func TestReviewControllerFailedWriteSendsNoGrant(t *testing.T) {
	e, _, o := commissioned(t, func(*os.File, []byte) (int, error) { return 0, os.ErrPermission }, nil)
	code, rep := vote(t, e.Handler(), o.Lease, "", quorum.Request{From: "b", Term: 3, Nonce: 7, ID: 1})
	if rep.Granted {
		t.Fatalf("granted (%d) with its state write failing", code)
	}
	if s := e.Status(); s.StateWriteError == "" {
		t.Fatalf("the failed write isn't in the status: %+v", s)
	}
}

// (2) The witness, likewise.
func TestReviewWitnessFailedWriteSendsNoGrant(t *testing.T) {
	var writes atomic.Int32
	clk := newFakeClock()
	lease := 300 * time.Millisecond
	w, stop := startWitness(t, WitnessOptions{Dir: t.TempDir(), Lease: lease, clock: clk.now,
		write: func(f *os.File, b []byte) (int, error) {
			if writes.Add(1) > 1 { // the incarnation's first write succeeds
				return 0, os.ErrPermission
			}
			return f.Write(b)
		}})
	defer stop()
	vote(t, w.Handler(), lease, "a,b", quorum.Request{From: "a", Term: 1, Nonce: 5, ID: 1})
	waitFor(t, time.Second, func() bool { return writes.Load() == 1 })
	clk.add(2 * lease)
	if code, rep := vote(t, w.Handler(), lease, "a,b", quorum.Request{From: "a", Term: 2, Nonce: 5, ID: 2}); rep.Granted {
		t.Fatalf("granted (%d) with its state write failing", code)
	}
}

// (2, 4) A controller's voter refuses a lease longer than its own, and
// says so (an event), never silently.
func TestReviewControllerRefusesALongerLease(t *testing.T) {
	var mu sync.Mutex
	var evs []Event
	e, _, o := commissioned(t, nil, func(ev Event) { mu.Lock(); evs = append(evs, ev); mu.Unlock() })
	if code, _ := vote(t, e.Handler(), o.Lease+1, "", quorum.Request{From: "b", Term: 3, Nonce: 7, ID: 1}); code != http.StatusConflict {
		t.Fatalf("a longer lease: %d, want 409", code)
	}
	waitFor(t, time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, ev := range evs {
			if ev.Kind == EventLeaseMismatch {
				return true
			}
		}
		return false
	})
	if code, rep := vote(t, e.Handler(), o.Lease, "", quorum.Request{From: "b", Term: 3, Nonce: 7, ID: 2}); code != http.StatusOK || !rep.Granted {
		t.Fatalf("its own lease: %d %+v, want granted", code, rep)
	}
}

// (2) A controller's state file is locked to one process.
func TestReviewControllerStateLocked(t *testing.T) {
	o := testOptions(t, "a", "b")
	e, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if e2, err := New(o); err == nil {
		e2.Close()
		t.Fatal("a second elector started on one state file")
	}
}

// (2) The witness's state directory is locked to one process.
func TestReviewWitnessDirLocked(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWitness(WitnessOptions{Dir: dir, Token: testToken})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if w2, err := NewWitness(WitnessOptions{Dir: dir, Token: testToken}); err == nil {
		w2.Close()
		t.Fatal("a second witness started on one state directory")
	}
}

// (3) A cluster at the witness is bound to its two controllers at its
// first vote, durably: a vote from anyone else, or naming other members,
// is refused, before and after a restart.
func TestReviewWitnessBindsClusterToItsMembers(t *testing.T) {
	dir := t.TempDir()
	clk := newFakeClock()
	w, stop := startWitness(t, WitnessOptions{Dir: dir, Lease: time.Second, clock: clk.now})
	if code, _ := vote(t, w.Handler(), time.Second, "a,b", quorum.Request{From: "a", Term: 1, Nonce: 1, ID: 1}); code != http.StatusOK {
		t.Fatalf("a member's first vote: %d", code)
	}
	for _, c := range []struct {
		from    quorum.NodeID
		members string
	}{
		{"zz", "a,b"}, {"zz", ""}, {"a", "a,c"}, {"c", "a,c"}, {"a", "b,a"}, {"a", "a,a"},
	} {
		if code, _ := vote(t, w.Handler(), time.Second, c.members, quorum.Request{From: c.from, Term: 2, Nonce: 2, ID: 2}); code != http.StatusForbidden {
			t.Errorf("from %s naming %q: %d, want 403", c.from, c.members, code)
		}
	}
	stop()
	w2, stop2 := startWitness(t, WitnessOptions{Dir: dir, Lease: time.Second, clock: clk.now})
	defer stop2()
	if code, _ := vote(t, w2.Handler(), time.Second, "a,c", quorum.Request{From: "c", Term: 2, Nonce: 2, ID: 3}); code != http.StatusForbidden {
		t.Fatalf("after a restart, other members: %d, want 403", code)
	}
}

// (3) A vote that would jump the terms is refused at the witness: the
// review's repro pushed a pair's promise to MaxUint64, which jams it for
// good.
func TestReviewWitnessTermJumpRefused(t *testing.T) {
	clk := newFakeClock()
	w, stop := startWitness(t, WitnessOptions{Dir: t.TempDir(), Lease: time.Second, clock: clk.now})
	defer stop()
	vote(t, w.Handler(), time.Second, "a,b", quorum.Request{From: "a", Term: 1, Nonce: 1, ID: 1})
	clk.add(2 * time.Second)
	waitFor(t, 2*time.Second, func() bool {
		_, rep := vote(t, w.Handler(), time.Second, "a,b", quorum.Request{From: "a", Term: 1, Nonce: 1, ID: 2})
		return rep.Granted
	})
	if code, rep := vote(t, w.Handler(), time.Second, "a,b", quorum.Request{From: "a", Term: ^uint64(0), Nonce: 1, ID: 3}); code != http.StatusConflict || rep.Granted {
		t.Fatalf("a jump to MaxUint64: %d %+v, want 409", code, rep)
	}
	if code, rep := vote(t, w.Handler(), time.Second, "a,b", quorum.Request{From: "a", Term: 1 + MaxTermJump, Nonce: 1, ID: 4}); code != http.StatusOK || !rep.Granted {
		t.Fatalf("a jump of exactly MaxTermJump: %d %+v, want granted", code, rep)
	}
	if ps := w.Pairs(); ps[0].Promised != 1+MaxTermJump {
		t.Fatalf("promised %d", ps[0].Promised)
	}
}

// (3) The same at a controller's voter.
func TestReviewControllerTermJumpRefused(t *testing.T) {
	e, _, o := commissioned(t, nil, nil)
	waitFor(t, 2*time.Second, func() bool {
		_, rep := vote(t, e.Handler(), o.Lease, "", quorum.Request{From: "b", Term: 3, Nonce: 7, ID: 1})
		return rep.Granted
	})
	if code, rep := vote(t, e.Handler(), o.Lease, "", quorum.Request{From: "b", Term: 4 + MaxTermJump, Nonce: 7, ID: 2}); code != http.StatusConflict || rep.Granted {
		t.Fatalf("a jump: %d %+v, want 409", code, rep)
	}
}

// (3) An operator's floor far above the known terms is refused: the
// voters would refuse the pair's next campaign as a jump.
func TestReviewRecommissionFloorBounded(t *testing.T) {
	o := testOptions(t, "a", "b")
	e, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if err := e.Recommission(context.Background(), 9, e.KnownFloor()+MaxTermJump); err == nil || !strings.Contains(err.Error(), "term jump") {
		t.Fatalf("a floor MaxTermJump above: %v", err)
	}
}

// (4) The client-token answer binds who asks, who answers and the
// direction: a question reflected back at its asker (a man in the middle
// with the peer token asks A itself, as B, and returns A's own answer)
// doesn't pass for the peer's.
func TestReviewHelloReflectedAnswerFails(t *testing.T) {
	o := testOptions(t, "a", "b")
	o.ClientToken = "token-of-a"
	var a *Elector
	mitm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		q.Set("from", "b") // ask A as if B asked
		rr := httptest.NewRequest(http.MethodGet, r.URL.Path+"?"+q.Encode(), nil)
		rec := httptest.NewRecorder()
		a.Handler().ServeHTTP(rec, rr)
		var h helloAnswer
		_ = json.Unmarshal(rec.Body.Bytes(), &h)
		h.Node = "b"
		_ = json.NewEncoder(w).Encode(h)
	}))
	defer mitm.Close()
	u, _ := url.Parse(mitm.URL)
	o.PeerAddr = u.Host
	var err error
	if a, err = New(o); err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	a.checkToken(context.Background())
	if got := a.Status().ClientToken; got != TokenDiffers {
		t.Fatalf("a reflected answer read as %v, want differs", got)
	}
	// And the hello answers only the peer.
	rr := httptest.NewRequest(http.MethodGet, Prefix+testCluster+"/hello?c=0123456789abcdef&from=zz", nil)
	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, rr)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("hello from a stranger: %d", rec.Code)
	}
}

// (4) LimitListener holds connections over its cap until one closes.
func TestReviewLimitListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	l := LimitListener(ln, 1)
	defer l.Close()
	accepted := make(chan net.Conn, 2)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			accepted <- c
		}
	}()
	c1, _ := net.Dial("tcp", ln.Addr().String())
	defer c1.Close()
	c2, _ := net.Dial("tcp", ln.Addr().String())
	defer c2.Close()
	first := <-accepted
	select {
	case <-accepted:
		t.Fatal("a second connection accepted over the cap")
	case <-time.After(100 * time.Millisecond):
	}
	first.Close()
	select {
	case c := <-accepted:
		c.Close()
	case <-time.After(2 * time.Second):
		t.Fatal("the held connection was never accepted")
	}
}

// The witness's member files are plain, one line.
func TestReviewMembersFile(t *testing.T) {
	dir := t.TempDir()
	w, stop := startWitness(t, WitnessOptions{Dir: dir, Lease: time.Second})
	defer stop()
	vote(t, w.Handler(), time.Second, "a,b", quorum.Request{From: "b", Term: 1, Nonce: 1, ID: 1})
	b, err := os.ReadFile(filepath.Join(dir, testCluster+membersSuffix))
	if err != nil || string(b) != "a,b\n" {
		t.Fatalf("members file %q, %v", b, err)
	}
}
