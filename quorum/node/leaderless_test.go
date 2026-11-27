package node

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/quorum"
)

// The slow-write and no-leader reports (#1436). All on the fake clock:
// the vote calls go through a fake transport that answers at once, or
// not at all.

// logBuf is a log handler's writer the test reads.
type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// count is how many log lines at level hold msg.
func (l *logBuf) count(level, msg string) int {
	n := 0
	for _, line := range strings.Split(l.String(), "\n") {
		if strings.Contains(line, "level="+level) && strings.Contains(line, msg) {
			n++
		}
	}
	return n
}

// How a fake voter answers.
type voterWay int

const (
	wayCut       voterWay = iota // no answer: a refused connection
	wayGrant                     // grants every campaign, refuses probes
	wayHeld                      // answers probes, never a campaign (its write holds the grant)
	wayInc9                      // grants, as witness incarnation 9 (the pair counts 5)
	wayMarked                    // refuses everything, marking the asker a duplicate
	wayRefuse                    // refuses every campaign: promised a higher term elsewhere
	wayRefuseLow                 // refuses every campaign, its promise below the term (a restart wait)
)

// fakeVoters answers a's vote calls for b and the witness.
type fakeVoters struct {
	mu  sync.Mutex
	way map[string]voterWay // by host
	cn  map[string]string   // the certificate each host answers under (peer TLS), by host
	// answered counts the requests other than campaigns each host
	// answered (probes).
	answered map[string]int
}

func (f *fakeVoters) probes(host string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.answered[host]
}

func (f *fakeVoters) RoundTrip(r *http.Request) (*http.Response, error) {
	f.mu.Lock()
	way, cn := f.way[r.URL.Hostname()], f.cn[r.URL.Hostname()]
	f.mu.Unlock()
	var cs *tls.ConnectionState
	if cn != "" {
		cs = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{Subject: pkix.Name{CommonName: cn}}}}
	}
	from := quorum.NodeID("b")
	if r.URL.Hostname() == "witness.test" {
		from = WitnessID
	}
	if way == wayCut {
		return nil, errors.New("connection refused")
	}
	if !strings.HasSuffix(r.URL.Path, "/vote") {
		return &http.Response{StatusCode: http.StatusNoContent, Body: http.NoBody, Header: http.Header{}, Request: r, TLS: cs}, nil
	}
	b, _ := io.ReadAll(r.Body)
	var req quorum.Request
	if err := req.UnmarshalBinary(b); err != nil {
		return nil, err
	}
	campaign := req.Term != 0 && !req.Renewal
	if !campaign {
		f.mu.Lock()
		if f.answered == nil {
			f.answered = map[string]int{}
		}
		f.answered[r.URL.Hostname()]++
		f.mu.Unlock()
	}
	rep := quorum.Reply{From: from, To: req.From, Nonce: req.Nonce, ID: req.ID, Term: req.Term, Incarnation: 5, Promised: req.Term}
	switch way {
	case wayGrant:
		rep.Granted = campaign
	case wayHeld:
		if campaign {
			<-r.Context().Done()
			return nil, r.Context().Err()
		}
	case wayInc9:
		rep.Granted, rep.Incarnation = campaign, 9
	case wayMarked:
		rep.Marked = true
	case wayRefuse:
		rep.Promised = req.Term + 5
	case wayRefuseLow:
		rep.Promised = 0
	}
	out, _ := rep.MarshalBinary()
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(out)), Header: http.Header{}, Request: r, TLS: cs}, nil
}

// lelector is controller a with the fake voters, its writer running, and
// its looks driven by the test.
type lelector struct {
	e      *Elector
	clk    *fakeClock
	o      Options
	logs   *logBuf
	voters *fakeVoters
}

// newLelector builds a (commissioned at incarnation 5 unless not), with
// write as its state write (nil: the file's own).
func newLelector(t *testing.T, peer, witness voterWay, commissioned bool, write func(*os.File, []byte) (int, error)) *lelector {
	t.Helper()
	o := testOptions(t, "a", "b")
	o.PeerAddr, o.WitnessAddr = "peer.test:1", "witness.test:1"
	clk := newFakeClock()
	logs := &logBuf{}
	fv := &fakeVoters{way: map[string]voterWay{"peer.test": peer, "witness.test": witness}}
	o.clock, o.write, o.client = clk.now, write, &http.Client{Transport: fv}
	o.Log = slog.New(slog.NewTextHandler(logs, nil))
	if commissioned {
		if err := (&State{Path: o.StatePath}).Save(quorum.VoterState{Incarnation: 5, Gen: 1, Lease: o.Lease}); err != nil {
			t.Fatal(err)
		}
	}
	e, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Close)
	ctx, cancel := context.WithCancel(context.Background())
	e.mu.Lock()
	e.ctx = ctx
	e.mu.Unlock()
	done := make(chan struct{})
	go func() { e.w.run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return &lelector{e: e, clk: clk, o: o, logs: logs, voters: fv}
}

// pastHold moves the clock past a's restart hold and leases more.
func (l *lelector) pastHold(leases int) {
	l.e.mu.Lock()
	hold := l.e.c.HoldUntil()
	l.e.mu.Unlock()
	if d := hold.Sub(l.clk.now()); d > 0 {
		l.clk.add(d)
	}
	l.clk.add(time.Duration(leases) * l.o.Lease)
}

// cause looks until the no-leader report's cause holds want, and returns it.
func (l *lelector) cause(t *testing.T, want string) string {
	t.Helper()
	var got string
	end := time.Now().Add(2 * time.Second)
	for time.Now().Before(end) {
		l.e.look()
		if ll := l.e.Status().Leaderless; ll != nil {
			got = ll.Cause
			if strings.Contains(got, want) {
				return got
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("no-leader cause %q, want it to hold %q", got, want)
	return ""
}

// heldWrite is a state write that blocks until release, which the test
// registers as a cleanup after newLelector's (so it runs first).
func heldWrite() (write func(*os.File, []byte) (int, error), release func()) {
	ch := make(chan struct{})
	var once sync.Once
	return func(f *os.File, b []byte) (int, error) {
			<-ch
			return f.Write(b)
		}, func() {
			once.Do(func() { close(ch) })
		}
}

// The cause a controller with no leader names, per case: never a slow
// disk where none was measured, and never this controller's own disk
// when its own grant would not have made a majority.
func TestLeaderlessCause(t *testing.T) {
	noDisk := func(t *testing.T, cause string) {
		t.Helper()
		if strings.Contains(cause, "disk") || strings.Contains(cause, "own voter") {
			t.Fatalf("cause %q blames a disk", cause)
		}
	}
	// notSlow: nor a voter's write.
	notSlow := func(t *testing.T, cause string) {
		t.Helper()
		noDisk(t, cause)
		if strings.Contains(cause, "has not confirmed its write") {
			t.Fatalf("cause %q blames a voter's write", cause)
		}
	}
	t.Run("cut", func(t *testing.T) {
		l := newLelector(t, wayCut, wayCut, true, nil)
		l.pastHold(5)
		got := l.cause(t, "voter b does not answer")
		if !strings.Contains(got, "voter w does not answer") {
			t.Fatalf("cause %q", got)
		}
		notSlow(t, got)
	})
	t.Run("cut, own write held", func(t *testing.T) {
		// Its own grant would make no majority: its disk is not the cause.
		write, release := heldWrite()
		l := newLelector(t, wayCut, wayCut, true, write)
		t.Cleanup(release)
		l.pastHold(5)
		l.e.look()
		l.clk.add(l.o.Lease)
		notSlow(t, l.cause(t, "voter w does not answer"))
	})
	t.Run("own disk slow", func(t *testing.T) {
		write, release := heldWrite()
		l := newLelector(t, wayGrant, wayCut, true, write)
		t.Cleanup(release)
		l.pastHold(5)
		l.e.look() // the campaign, at the frozen clock: b grants it
		waitFor(t, 2*time.Second, func() bool {
			l.e.mu.Lock()
			defer l.e.mu.Unlock()
			w, ok := l.e.word("b")
			return ok && w.Granted
		})
		// Its own write still held, past the budget; and past the
		// unheard campaign's back-off (two leases from its last re-ask),
		// at the look that asks again: b's word, its grant of the first
		// campaign, is still current. b is cut from here, so that grant
		// stays its latest word.
		l.voters.mu.Lock()
		l.voters.way["peer.test"] = wayCut
		l.voters.mu.Unlock()
		l.clk.add(3 * l.o.Lease)
		l.e.look()
		got := ""
		if ll := l.e.Status().Leaderless; ll != nil {
			got = ll.Cause
		}
		if !strings.Contains(got, "this controller's own voter has not confirmed its write: its state disk is slow") {
			t.Fatalf("cause %q", got)
		}
		if !strings.Contains(got, "voter w does not answer") {
			t.Fatalf("cause %q", got)
		}
	})
	t.Run("wrong witness incarnation", func(t *testing.T) {
		l := newLelector(t, wayCut, wayInc9, true, nil)
		l.pastHold(5)
		got := l.cause(t, "voter w's answer is not counted: it answered as witness incarnation 9, and this controller counts 5")
		notSlow(t, got)
	})
	t.Run("witness write failing", func(t *testing.T) {
		// F25: it answers probes, and its write never confirms a grant.
		l := newLelector(t, wayCut, wayHeld, true, nil)
		l.pastHold(5)
		got := l.cause(t, "voter w has not confirmed its write (slow or failing); see its log")
		if !strings.Contains(got, "voter b does not answer") {
			t.Fatalf("cause %q", got)
		}
		noDisk(t, got)
	})
	t.Run("witness under an uncounted certificate", func(t *testing.T) {
		// Answers under another incarnation's certificate may come from
		// another copy: they prove nothing of the counted witness's write.
		l := newLelector(t, wayCut, wayHeld, true, nil)
		l.e.call.tls = true
		l.voters.mu.Lock()
		l.voters.cn = map[string]string{"witness.test": "witness@9"}
		l.voters.mu.Unlock()
		l.pastHold(5)
		l.e.look()
		// Its probe answered (and that answer handled) before the cause
		// is read.
		waitFor(t, time.Second, func() bool { return l.voters.probes("witness.test") > 0 })
		time.Sleep(50 * time.Millisecond)
		notSlow(t, l.cause(t, "voter w does not answer"))
	})
	t.Run("refused", func(t *testing.T) {
		l := newLelector(t, wayRefuse, wayRefuse, true, nil)
		l.pastHold(5)
		got := l.cause(t, "voter b refused this controller's campaign")
		if !strings.Contains(got, "it had promised term") {
			t.Fatalf("cause %q does not name the promise", got)
		}
		notSlow(t, got)
	})
	t.Run("refused naming no promise", func(t *testing.T) {
		// A voter in its restart wait (or uncommissioned) refuses with a
		// promise below the term: that promise is no reason.
		l := newLelector(t, wayRefuseLow, wayRefuseLow, true, nil)
		l.pastHold(5)
		got := l.cause(t, "voter b refused this controller's campaign")
		if strings.Contains(got, "promised") {
			t.Fatalf("cause %q names a promise that refused nothing", got)
		}
		notSlow(t, got)
	})
	t.Run("duplicate mark", func(t *testing.T) {
		l := newLelector(t, wayCut, wayMarked, true, nil)
		l.pastHold(5)
		notSlow(t, l.cause(t, "this controller's name is marked duplicated"))
	})
	t.Run("uncommissioned", func(t *testing.T) {
		l := newLelector(t, wayCut, wayCut, false, nil)
		l.pastHold(5)
		notSlow(t, l.cause(t, "this controller is not commissioned"))
		// Its own reports cover it: no error for it.
		if n := l.logs.count("ERROR", "seen no leader"); n != 0 {
			t.Fatalf("%d no-leader errors logged for an uncommissioned controller:\n%s", n, l.logs)
		}
	})
	t.Run("driver drop", func(t *testing.T) {
		// A reply the driver drops before the candidate sees it (an
		// incarnation this controller has not adopted, under peer TLS)
		// is an answer, not counted, not silence.
		l := newLelector(t, wayCut, wayCut, true, nil)
		l.pastHold(5)
		l.e.look()
		l.e.mu.Lock()
		term := l.e.c.VoteTerm()
		l.e.mu.Unlock()
		l.e.unadoptedWitness(quorum.Reply{From: WitnessID, To: "a", Term: term, Incarnation: 9, Granted: true})
		notSlow(t, l.cause(t, "voter w's answer is not counted: it answered as witness incarnation 9, which this controller has not adopted"))
	})
	t.Run("spare", func(t *testing.T) {
		l := newLelector(t, wayCut, wayCut, true, nil)
		l.pastHold(5)
		l.cause(t, "does not answer")
		l.e.spare.Store(true)
		if ll := l.e.Status().Leaderless; ll != nil {
			t.Fatalf("a spare reports no leader: %+v", ll)
		}
	})
}

// The no-leader report starts after the restart wait, past
// leaderlessAfter leases; its error is logged once, then once a minute;
// a leader seen again ends it.
func TestLeaderlessReportTiming(t *testing.T) {
	l := newLelector(t, wayCut, wayCut, true, nil)
	l.e.mu.Lock()
	hold := l.e.c.HoldUntil()
	l.e.mu.Unlock()
	if hold.Sub(l.clk.now()) < l.o.Lease/8 {
		t.Fatalf("restart hold ends %v after the start, want a lease or so", hold.Sub(l.clk.now()))
	}
	// leaderlessAfter leases after the start, but not after the hold.
	l.clk.add(leaderlessAfter*l.o.Lease + l.o.Lease/16)
	for range 5 {
		l.e.look()
	}
	if ll := l.e.Status().Leaderless; ll != nil {
		t.Fatalf("reported %+v before %d leases past the restart wait", ll, leaderlessAfter)
	}
	l.pastHold(leaderlessAfter)
	l.clk.add(l.o.Lease / 8)
	l.cause(t, "does not answer")
	waitFor(t, time.Second, func() bool { return l.logs.count("ERROR", "seen no leader") == 1 })
	l.clk.add(noteEvery / 2)
	l.e.look()
	if n := l.logs.count("ERROR", "seen no leader"); n != 1 {
		t.Fatalf("%d errors within a minute, want 1", n)
	}
	l.clk.add(noteEvery / 2)
	l.e.look()
	if n := l.logs.count("ERROR", "seen no leader"); n != 2 {
		t.Fatalf("%d errors after a minute, want 2", n)
	}
	// b leads: a's voter grants it and sees its lease.
	l.e.running.Store(true)
	l.e.mu.Lock()
	term := l.e.v.State().Promised + 1
	l.e.mu.Unlock()
	if code, rep := vote(t, l.e.Handler(), l.o.Lease, "", quorum.Request{From: "b", Term: term, Nonce: 7, ID: 1}); !rep.Granted {
		t.Fatalf("b's campaign: %d %+v", code, rep)
	}
	l.e.look()
	if ll := l.e.Status().Leaderless; ll != nil {
		t.Fatalf("still reported with b leading: %+v", ll)
	}
	if n := l.logs.count("INFO", "a leader is seen again"); n != 1 {
		t.Fatalf("%d leader-again lines, want 1:\n%s", n, l.logs)
	}
}

// A write's time runs from the oldest ask it serves, its queue behind a
// write in flight included: what a grant held behind it waited.
func TestWriteTimedFromItsOldestAsk(t *testing.T) {
	clk := newFakeClock()
	gate := make(chan struct{})
	type report struct {
		seq  uint64
		took time.Duration
	}
	got := make(chan report, 4)
	w := newWriter(func(quorum.VoterState) error { <-gate; return nil },
		func(seq uint64, _ error, took time.Duration) { got <- report{seq, took} }, clk.now)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.run(ctx); close(done) }()
	defer func() { cancel(); close(gate); <-done }()
	w.ask(&quorum.Persist{Seq: 1})
	waitFor(t, time.Second, func() bool {
		return w.waiting(clk.now()) == 0 && func() bool { w.mu.Lock(); defer w.mu.Unlock(); return w.busy }()
	})
	clk.add(100 * time.Millisecond)
	w.ask(&quorum.Persist{Seq: 2}) // queued behind seq 1
	clk.add(50 * time.Millisecond)
	w.ask(&quorum.Persist{Seq: 3}) // covers seq 2: still timed from seq 2's ask
	if d := w.waiting(clk.now()); d != 150*time.Millisecond {
		t.Fatalf("waiting %v, want 150ms (the write in flight's oldest ask)", d)
	}
	clk.add(50 * time.Millisecond)
	gate <- struct{}{} // seq 1 lands at 200 ms
	if r := <-got; r.seq != 1 || r.took != 200*time.Millisecond {
		t.Fatalf("first write %+v, want seq 1 in 200ms", r)
	}
	clk.add(100 * time.Millisecond)
	gate <- struct{}{} // seq 3 lands at 300 ms, asked (as seq 2) at 100 ms
	if r := <-got; r.seq != 3 || r.took != 200*time.Millisecond {
		t.Fatalf("second write %+v, want seq 3 in 200ms (from seq 2's ask, its queue included)", r)
	}
	// A re-ask's write of seq 3 again, queued while seq 3 was in flight,
	// releases nothing new: not timed (its time would count the first
	// write's twice).
	w.ask(&quorum.Persist{Seq: 3})
	clk.add(100 * time.Millisecond)
	gate <- struct{}{}
	if r := <-got; r.seq != 3 || r.took >= 0 {
		t.Fatalf("rewrite %+v, want seq 3 untimed", r)
	}
}

// A controller's slow write is warned of past half the budget, at most
// once a minute, and Status shows the last and slowest.
func TestSlowWriteWarned(t *testing.T) {
	var clk *fakeClock
	var slow time.Duration
	var mu sync.Mutex
	write := func(f *os.File, b []byte) (int, error) {
		mu.Lock()
		clk.add(slow) // the write takes slow on the fake clock
		mu.Unlock()
		return f.Write(b)
	}
	l := newLelector(t, wayCut, wayCut, true, write)
	clk = l.clk
	budget := writeBudget(l.o.Lease)
	if budget != l.o.Lease*3/4 {
		t.Fatalf("budget %v at a %v lease, want the re-ask window's end, %v", budget, l.o.Lease, l.o.Lease*3/4)
	}
	seq := uint64(1000)
	ask := func() {
		l.e.mu.Lock()
		seq++
		p := &quorum.Persist{Seq: seq, State: l.e.v.State()}
		l.e.mu.Unlock()
		l.e.w.ask(p)
	}
	set := func(d time.Duration) { mu.Lock(); slow = d; mu.Unlock() }
	set(budget/2 - time.Millisecond) // under the line
	ask()
	waitFor(t, time.Second, func() bool { return l.e.Status().StateWriteLast == budget/2-time.Millisecond })
	if n := l.logs.count("WARN", "voter state write"); n != 0 {
		t.Fatalf("warned of a write under half the budget:\n%s", l.logs)
	}
	set(budget/2 + time.Millisecond)
	ask()
	waitFor(t, time.Second, func() bool { return l.logs.count("WARN", "voter state write") == 1 })
	set(budget + time.Millisecond)
	ask()
	waitFor(t, time.Second, func() bool { return l.e.Status().StateWriteMax == budget+time.Millisecond })
	if n := l.logs.count("WARN", "voter state write"); n != 1 {
		t.Fatalf("%d warnings within a minute, want 1", n)
	}
	l.clk.add(noteEvery)
	set(budget / 2 * 3 / 2)
	ask()
	waitFor(t, time.Second, func() bool { return l.logs.count("WARN", "voter state write") == 2 })
	if s := l.e.Status(); s.StateWriteBudget != budget || s.StateWriteLast != budget/2*3/2 {
		t.Fatalf("status %v last %v max %v", s.StateWriteBudget, s.StateWriteLast, s.StateWriteMax)
	}
}

// A grant released after its asker stopped waiting is counted, but not
// when a later re-ask of the same campaign was answered by the same write.
func TestLateGrantCounted(t *testing.T) {
	for _, c := range []struct {
		name  string
		reask bool
		want  uint64
	}{{"abandoned", false, 1}, {"re-ask answered", true, 0}} {
		t.Run(c.name, func(t *testing.T) {
			release := make(chan struct{})
			var once sync.Once
			l := newLelector(t, wayCut, wayCut, true, func(f *os.File, b []byte) (int, error) {
				<-release
				return f.Write(b)
			})
			t.Cleanup(func() { once.Do(func() { close(release) }) })
			l.pastHold(1)
			l.e.running.Store(true)
			term := l.e.v.State().Promised + 1
			ask := func(ctx context.Context, id uint64) chan quorum.Reply {
				out := make(chan quorum.Reply, 1)
				r := httptest.NewRequestWithContext(ctx, http.MethodPost, Prefix+testCluster+"/vote", bytes.NewReader(voteBody(t, quorum.Request{From: "b", Term: term, Nonce: 7, ID: id})))
				r.Header.Set(LeaseHeader, strconv.FormatInt(int64(l.o.Lease), 10))
				go func() {
					rec := httptest.NewRecorder()
					l.e.Handler().ServeHTTP(rec, r)
					var rep quorum.Reply
					_ = rep.UnmarshalBinary(rec.Body.Bytes())
					out <- rep
				}()
				return out
			}
			ctx1, give1 := context.WithCancel(context.Background())
			first := ask(ctx1, 1)
			waitFor(t, time.Second, func() bool { l.e.mu.Lock(); defer l.e.mu.Unlock(); return len(l.e.waiters) == 1 })
			give1() // b's call times out
			<-first
			var second chan quorum.Reply
			if c.reask {
				second = ask(context.Background(), 2)
				waitFor(t, time.Second, func() bool { l.e.mu.Lock(); defer l.e.mu.Unlock(); return len(l.e.waiters) == 1 })
			}
			once.Do(func() { close(release) })
			if second != nil {
				if rep := <-second; !rep.Granted {
					t.Fatalf("the re-ask: %+v", rep)
				}
			}
			waitFor(t, time.Second, func() bool { l.e.mu.Lock(); defer l.e.mu.Unlock(); return l.e.durable > 0 })
			if got := l.e.Status().LateGrants; got != c.want {
				t.Fatalf("late grants %d, want %d", got, c.want)
			}
		})
	}
}

// The witness judges a pair's writes by the lease the pair's votes state,
// not by its own --lease (the longest any pair may have).
func TestWitnessJudgesWritesByThePairLease(t *testing.T) {
	clk := newFakeClock()
	logs := &logBuf{}
	pair := 400 * time.Millisecond
	took := writeBudget(pair)/2 + 10*time.Millisecond // slow for the pair, not for a 4 s lease
	var writes sync.Mutex
	n := 0
	w, stop := startWitness(t, WitnessOptions{Dir: t.TempDir(), Lease: 4 * time.Second, clock: clk.now, Log: slog.New(slog.NewTextHandler(logs, nil)),
		write: func(f *os.File, b []byte) (int, error) {
			writes.Lock()
			n++
			if n > 1 {
				clk.add(took)
			}
			writes.Unlock()
			return f.Write(b)
		}})
	defer stop()
	vote(t, w.Handler(), pair, "a,b", quorum.Request{From: "a", Term: 1, Nonce: 5, ID: 1})
	// The incarnation's write done before the clock moves: else it is
	// timed across the jump.
	w.mu.Lock()
	pw := w.pairs[testCluster].w
	w.mu.Unlock()
	waitFor(t, time.Second, func() bool {
		pw.mu.Lock()
		defer pw.mu.Unlock()
		return !pw.busy && pw.want == nil
	})
	if logs.count("WARN", "voter state write") != 0 {
		t.Fatalf("warned before the pair's slow write:\n%s", logs)
	}
	clk.add(5 * time.Second) // past its restart wait
	if code, rep := vote(t, w.Handler(), pair, "a,b", quorum.Request{From: "a", Term: 2, Nonce: 5, ID: 2}); !rep.Granted {
		t.Fatalf("campaign: %d %+v", code, rep)
	}
	waitFor(t, time.Second, func() bool { return logs.count("WARN", "voter state write") == 1 })
}

// The start-up probe times writes the way the state is written, in a file
// apart from the state's, and leaves nothing behind.
func TestStateDirProbe(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ProbeFile+".tmp"), []byte("left by a crash"), 0o600); err != nil {
		t.Fatal(err)
	}
	var writes int
	took, err := ProbeStateDir(dir, func(f *os.File, b []byte) (int, error) {
		writes++
		if !strings.HasPrefix(filepath.Base(f.Name()), ProbeFile) {
			t.Errorf("probe wrote %s", f.Name())
		}
		time.Sleep(5 * time.Millisecond)
		return f.Write(b)
	})
	if err != nil {
		t.Fatal(err)
	}
	if writes != probeWrites || took < 5*time.Millisecond {
		t.Fatalf("%d writes, slowest %v", writes, took)
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Fatalf("left %v", left)
	}
	// The witness's start reads every *.quorum; a controller's state is
	// quorum.state.
	if strings.HasSuffix(ProbeFile, stateSuffix) || strings.HasSuffix(ProbeFile+".tmp", stateSuffix) || strings.HasPrefix(ProbeFile, "quorum.state") {
		t.Fatalf("probe file %s looks like a state file", ProbeFile)
	}
}

// The probe's verdict: an error past the write budget, a warning past
// half of it, info under; and the error is logged as one.
func TestProbeVerdict(t *testing.T) {
	lease := 400 * time.Millisecond
	budget := writeBudget(lease)
	for _, c := range []struct {
		took time.Duration
		want slog.Level
	}{{budget / 4, slog.LevelInfo}, {budget/2 + time.Millisecond, slog.LevelWarn}, {budget + time.Millisecond, slog.LevelError}} {
		if got, msg := ProbeVerdict(c.took, lease); got != c.want {
			t.Errorf("%v: %v (%s), want %v", c.took, got, msg, c.want)
		}
	}
	if _, msg := ProbeVerdict(budget+time.Millisecond, lease); !strings.Contains(msg, "needs under "+budget.String()) {
		t.Errorf("error %q does not name the budget", msg)
	}
	logs := &logBuf{}
	tiny := 8 * time.Millisecond // a 6 ms budget
	ProbeAndLog(slog.New(slog.NewTextHandler(logs, nil)), "redundancy", t.TempDir(), tiny, func(f *os.File, b []byte) (int, error) {
		time.Sleep(10 * time.Millisecond)
		return f.Write(b)
	})
	if logs.count("ERROR", "too slow for this lease") != 1 {
		t.Fatalf("no probe error logged:\n%s", logs)
	}
}
