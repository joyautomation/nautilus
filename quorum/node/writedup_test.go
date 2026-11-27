package node

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/quorum"
)

// gatedWriter runs a writer whose saves each wait on gate and return what
// is sent on it, recording every Seq saved and every done report.
type gatedWriter struct {
	w     *writer
	gate  chan error
	saved chan uint64
	done  chan uint64
}

func newGatedWriter(t *testing.T) *gatedWriter {
	t.Helper()
	g := &gatedWriter{gate: make(chan error), saved: make(chan uint64, 8), done: make(chan uint64, 8)}
	g.w = newWriter(func(st quorum.VoterState) error {
		g.saved <- st.Promised // the tests write their Seq as Promised
		return <-g.gate
	}, func(seq uint64, err error, _ time.Duration) {
		if err != nil {
			seq |= 1 << 63 // a failed write's report
		}
		g.done <- seq
	}, newFakeClock().now)
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { g.w.run(ctx); close(stopped) }()
	t.Cleanup(func() { cancel(); close(g.gate); <-stopped }) // a save still waiting ends
	return g
}

func (g *gatedWriter) ask(seq uint64) {
	g.w.ask(&quorum.Persist{Seq: seq, State: quorum.VoterState{Promised: seq}})
}

// queued waits until an ask is queued behind the write in flight.
func (g *gatedWriter) queued(t *testing.T) {
	t.Helper()
	waitFor(t, time.Second, func() bool { g.w.mu.Lock(); defer g.w.mu.Unlock(); return g.w.want != nil })
}

func recv(t *testing.T, ch chan uint64, what string) uint64 {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(2 * time.Second):
		t.Fatalf("no %s", what)
		return 0
	}
}

func none(t *testing.T, ch chan uint64, what string) {
	t.Helper()
	select {
	case v := <-ch:
		t.Fatalf("%s %d, want none", what, v)
	case <-time.After(50 * time.Millisecond):
	}
}

// A campaign's re-ask, taken while the first ask's write is in flight,
// asks the same Seq; once that write succeeds the state is on disk, and
// the re-ask's grant left with it (Voter.Persisted), so the writer does
// not write it again: one write per Seq, not two fsyncs for one promise
// on a slow SD card (#1436). write_dedup_off turns it red.
func TestWriterWritesASeqOnce(t *testing.T) {
	g := newGatedWriter(t)
	g.ask(7)
	if s := recv(t, g.saved, "write"); s != 7 {
		t.Fatalf("wrote %d, want 7", s)
	}
	g.ask(7) // the re-ask, behind the write in flight
	g.queued(t)
	g.gate <- nil
	if d := recv(t, g.done, "report"); d != 7 {
		t.Fatalf("reported %#x, want 7 written", d)
	}
	none(t, g.saved, "wrote again")
	g.ask(7) // a re-ask once it is on disk
	none(t, g.saved, "wrote again")
	g.ask(8) // a new Seq is written
	if s := recv(t, g.saved, "write"); s != 8 {
		t.Fatalf("wrote %d, want 8", s)
	}
	g.gate <- nil
	recv(t, g.done, "report")
}

// F25 with a re-ask queued (#1436): the write in flight FAILS while a
// re-ask of the same Seq waits behind it. Only a success counts as
// written, so the queued ask is the retry: the promise ends on disk, or
// its failure is reported again, never skipped as if written (which would
// leave every later ask of that Seq skipped too, and the voter granting
// nothing, silently). write_dedup_failed_counts turns it red.
func TestWriterRetriesASeqItFailedToWrite(t *testing.T) {
	g := newGatedWriter(t)
	g.ask(7)
	recv(t, g.saved, "write")
	g.ask(7) // the re-ask, behind the write in flight
	g.queued(t)
	g.gate <- errors.New("disk full")
	if d := recv(t, g.done, "report"); d != 7|1<<63 {
		t.Fatalf("reported %#x, want 7 failed", d)
	}
	if s := recv(t, g.saved, "retry"); s != 7 {
		t.Fatalf("retried %d, want 7", s)
	}
	g.gate <- nil
	if d := recv(t, g.done, "report"); d != 7 {
		t.Fatalf("reported %#x, want 7 written", d)
	}
	// And with no re-ask queued, a failed Seq's next ask is written.
	g.ask(9)
	recv(t, g.saved, "write")
	g.gate <- errors.New("disk full")
	recv(t, g.done, "report")
	g.ask(9)
	if s := recv(t, g.saved, "write after a failure"); s != 9 {
		t.Fatalf("wrote %d, want 9", s)
	}
	g.gate <- nil
	recv(t, g.done, "report")
}

// F25 end to end on a witness (#1436, the lead's must-test): a campaign's
// grant write fails while the campaign's re-ask waits behind it. The
// failure drops both grants (no campaign leads on a promise not on disk),
// and the promise still ends on disk: the next ask of that term is
// granted at once and the file holds it. Skipping the queued re-ask as
// if the failed write had counted leaves the promise in memory only, and
// every later ask of it skipped: the witness grants that campaign
// nothing, ever. write_dedup_failed_counts turns it red.
func TestWitnessReaskBehindAFailedWriteEndsOnDisk(t *testing.T) {
	const lease = 400 * time.Millisecond
	dir := t.TempDir()
	clk := newFakeClock()
	var fail atomic.Bool // the next write blocks on gate, then fails
	gate, blocked := make(chan struct{}), make(chan struct{}, 1)
	write := func(f *os.File, b []byte) (int, error) {
		if fail.CompareAndSwap(true, false) {
			blocked <- struct{}{}
			<-gate
			return 0, errors.New("disk full")
		}
		return f.Write(b)
	}
	w, stop := startWitness(t, WitnessOptions{Dir: dir, Lease: lease, clock: clk.now, write: write})
	defer stop()
	h := w.Handler()
	vote(t, h, lease, "a,b", quorum.Request{From: "a", Term: 1, Nonce: 5, ID: 1})
	clk.add(3 * lease) // past the restart wait
	id := uint64(1)
	waitFor(t, 2*time.Second, func() bool {
		id++
		_, rep := vote(t, h, lease, "a,b", quorum.Request{From: "a", Term: 2, Nonce: 5, ID: id})
		return rep.Granted
	})

	fail.Store(true)
	var wg sync.WaitGroup
	var granted atomic.Int32
	ask := func(id uint64) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, rep := vote(t, h, lease, "a,b", quorum.Request{From: "a", Term: 7, Nonce: 5, ID: id}); rep.Granted {
				granted.Add(1)
			}
		}()
	}
	ask(100) // the campaign: its grant waits on the write
	select {
	case <-blocked:
	case <-time.After(2 * time.Second):
		t.Fatal("the campaign's grant write never started")
	}
	ask(101) // its re-ask, behind the write in flight
	w.mu.Lock()
	p := w.pairs[testCluster]
	w.mu.Unlock()
	waitFor(t, 2*time.Second, func() bool { p.w.mu.Lock(); defer p.w.mu.Unlock(); return p.w.want != nil })
	close(gate) // the write fails
	wg.Wait()
	if n := granted.Load(); n != 0 {
		t.Fatalf("%d grants of a promise whose write failed", n)
	}
	if _, rep := vote(t, h, lease, "a,b", quorum.Request{From: "a", Term: 7, Nonce: 5, ID: 102}); !rep.Granted {
		t.Fatalf("the next ask of term 7 after the failed write: %+v, want granted (the promise on disk)", rep)
	}
	st, err := (&State{Path: filepath.Join(dir, testCluster+stateSuffix)}).Load()
	if err != nil || st.Promised != 7 || st.GrantedTo != "a" {
		t.Fatalf("on disk: %+v %v, want term 7 promised to a", st, err)
	}
}
