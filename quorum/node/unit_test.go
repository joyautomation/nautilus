package node

import (
	"bytes"
	"context"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/quorum"
)

const testToken = "peer-secret-0123456789"

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
	return Options{Pair: "plant", Self: self, Peer: peer, PeerAddr: "127.0.0.1:1", WitnessAddr: "127.0.0.1:1",
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
		r := httptest.NewRequest(http.MethodPost, "http://"+c.host+Prefix+"plant/vote", nil)
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
		r := httptest.NewRequest(http.MethodPost, Prefix+"plant/vote", bytes.NewReader(voteBody(t, quorum.Request{From: "a", Nonce: 5, ID: 1})))
		if c.lease != "" {
			r.Header.Set(LeaseHeader, c.lease)
		}
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
	clk.add(299 * time.Millisecond)
	if !e.IsLeader() {
		t.Fatal("closed before the lease end")
	}
	clk.add(time.Millisecond)
	if e.IsLeader() || e.Term() != 0 {
		t.Fatalf("at the lease end, with no look since: IsLeader %v, Term %d; want closed, 0", e.IsLeader(), e.Term())
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
	o.Pair = "../etc"
	if _, err := New(o); err == nil {
		t.Error("took a pair name that is a path")
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
			r := httptest.NewRequest(http.MethodPost, Prefix+"plant/vote", bytes.NewReader(voteBody(t, req)))
			r.Header.Set(LeaseHeader, strconv.FormatInt(int64(time.Second), 10))
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
	path := filepath.Join(dir, "plant"+stateSuffix)
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
	p, err := w.pair("plant")
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
		p, err := w.pair("plant")
		if err != nil {
			t.Fatal(err)
		}
		waitFor(t, time.Second, func() bool {
			_, err := (&State{Path: filepath.Join(dir, "plant"+stateSuffix)}).Load()
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
