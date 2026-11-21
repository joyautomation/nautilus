package node

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/quorum"
)

// #1029 3c-2: the driver's half of the spare.

// eventOf drains e's queued events and reports the last of kind k.
func eventOf(e *Elector, k EventKind) (Event, bool) {
	var got Event
	found := false
	for {
		select {
		case ev := <-e.events:
			if ev.Kind == k {
				got, found = ev, true
			}
		default:
			return got, found
		}
	}
}

// A state file of a format above this release's (a later release wrote
// it; from format 6 on, maybe a spare's) is refused at start, and left
// exactly as it was: treated as lost, it would make a copy a member
// again. The refusal names the fix. newer_state_moved_aside moves it
// aside and starts uncommissioned.
func TestANewerStateFileIsRefusedAndLeftAlone(t *testing.T) {
	o := testOptions(t, "a", "b")
	b, _ := quorum.VoterState{Promised: 4, GrantedTo: "a", Incarnation: 9, Spare: true}.MarshalBinary()
	b[3] = 8 // magic "nqv", then the version
	body := b[:len(b)-4]
	b = binary.BigEndian.AppendUint32(append([]byte(nil), body...), crc32.Checksum(body, crc32.MakeTable(crc32.Castagnoli)))
	if err := os.WriteFile(o.StatePath, b, 0o600); err != nil {
		t.Fatal(err)
	}
	e, err := New(o)
	if err == nil {
		e.Close()
		t.Fatal("started over a state file of a newer format")
	}
	if !strings.Contains(err.Error(), "newer release") || !strings.Contains(err.Error(), "run the release that wrote it") {
		t.Fatalf("the refusal doesn't name the fix: %v", err)
	}
	after, _ := os.ReadFile(o.StatePath)
	if !bytes.Equal(after, b) {
		t.Fatal("the newer file was changed or moved")
	}
	if aside, _ := filepath.Glob(o.StatePath + ".*"); len(aside) > 1 { // the lock file only
		t.Fatalf("files set aside: %v", aside)
	}
}

// A spare's flag is read before anything else at start, the box check
// included (a copy's box differs, and the check would treat its file as
// lost): it starts silent, says so (EventSpare at every start), names no
// leader, leaves its file in place, refuses a re-commission, and answers
// the peer's votes, adopts and hellos as a stopped controller would.
// spare_forgotten_on_box_change and spare_answers_votes turn it red.
func TestASpareStartsSilentWhateverTheBox(t *testing.T) {
	o := testOptions(t, "a", "b")
	clk := newFakeClock()
	o.clock = clk.now
	o.Box = BoxFingerprint("machine-copy", []string{"aa:bb:cc:00:00:02"})
	st := &State{Path: o.StatePath, Owner: "a", Cluster: testCluster, Box: BoxFingerprint("machine-1", []string{"aa:bb:cc:00:00:01"})}
	if err := st.Save(quorum.VoterState{Promised: 1002, GrantedTo: "a", Incarnation: 7, Gen: 1, Floor: 1000, Lease: o.Lease, Spare: true}); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(o.StatePath)
	e, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	s := e.Status()
	if !s.Spare || s.BoxChanged != "" || s.Leader != "" || s.Leading {
		t.Fatalf("status %+v: want a spare, no box change, no leader", s)
	}
	if ev, ok := eventOf(e, EventSpare); !ok || !strings.Contains(ev.Detail, "duplicate of a - spare") {
		t.Fatalf("no spare event at start: %+v", ev)
	}
	if after, _ := os.ReadFile(o.StatePath); !bytes.Equal(after, before) {
		t.Fatal("the spare's file was changed or moved aside")
	}
	if _, err := e.RecommissionBy(context.Background(), "op", 7, 2000); !errors.Is(err, ErrSpare) {
		t.Fatalf("re-commission of a spare: %v, want ErrSpare", err)
	}
	running(t, e.Run)
	waitFor(t, time.Second, e.running.Load)
	h := e.Handler()
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/vote"}, {http.MethodPost, "/adopt"}, {http.MethodGet, "/hello?c=0123456789abcdef0123&from=b"},
	} {
		var body []byte
		if c.path == "/vote" {
			body = voteBody(t, quorum.Request{From: "b", Term: 1003, Nonce: 5, ID: 1, Seq: 1})
		}
		r := httptest.NewRequest(c.method, Prefix+testCluster+c.path, bytes.NewReader(body))
		r.Header.Set(LeaseHeader, "400000000")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s: %d, want 503 (a spare answers nobody)", c.path, rec.Code)
		}
	}
}

// The decision is written before the copy goes quiet: while the flag's
// write is blocked the copy is still the held copy it was (it answers
// its peer, and is no spare), and once the write is durable it is silent.
// spare_flag_after_silence goes quiet before the write.
func TestASpareGoesSilentOnlyOnceItsFlagIsWritten(t *testing.T) {
	o := testOptions(t, "a", "b")
	clk := newFakeClock()
	o.clock = clk.now
	st := &State{Path: o.StatePath, Owner: "a", Cluster: testCluster}
	if err := st.Save(quorum.VoterState{Promised: 1002, GrantedTo: "a", Incarnation: 7, Gen: 1, Floor: 1000, Lease: o.Lease}); err != nil {
		t.Fatal(err)
	}
	var block atomic.Bool
	var spareEv atomic.Value
	o.OnEvent = func(ev Event) {
		if ev.Kind == EventSpare {
			spareEv.Store(ev)
		}
	}
	release := make(chan struct{})
	o.write = func(f *os.File, b []byte) (int, error) {
		if block.Load() {
			<-release
		}
		return f.Write(b)
	}
	e, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	var freed sync.Once
	free := func() { freed.Do(func() { close(release) }) }
	running(t, e.Run)
	// After running: cleanups run last-in first, and Run's stop waits for
	// the writer, so the write must be free by then.
	t.Cleanup(free)
	waitFor(t, time.Second, e.running.Load)
	block.Store(true)
	// Both other voters name another boot of "a" for a lease.
	for i := 0; i <= 4; i++ {
		for _, from := range []quorum.NodeID{"b", WitnessID} {
			e.reply(from, quorum.Reply{From: from, To: "a", Nonce: e.Nonce(), ID: quorum.ProbeID, Marked: true, HeldBy: 0xA0})
		}
		if i < 4 {
			clk.add(o.Lease / 4)
		}
	}
	e.mu.Lock()
	decided := e.spareSeq != 0 || e.spare.Load()
	e.mu.Unlock()
	if !decided {
		t.Fatal("no decision on both other voters' word for a lease")
	}
	if e.Spare() {
		t.Fatal("silent before its flag is written")
	}
	free()
	waitFor(t, 2*time.Second, e.Spare)
	got, err := st.Load()
	if err != nil || !got.Spare {
		t.Fatalf("the flag on disk: %+v %v", got, err)
	}
	waitFor(t, time.Second, func() bool { return spareEv.Load() != nil })
}

// spareOnDisk writes a spare's state file for o.
func spareOnDisk(t *testing.T, o Options) {
	t.Helper()
	st := &State{Path: o.StatePath, Owner: o.Self, Cluster: testCluster}
	if err := st.Save(quorum.VoterState{Promised: 1002, GrantedTo: o.Self, Incarnation: 7, Gen: 1, Floor: 1000, Lease: o.Lease, Spare: true}); err != nil {
		t.Fatal(err)
	}
}

// A spare sends nothing: no vote, probe or hello reaches its peer or the
// witness while it runs. One that kept probing would stay the fresh boot
// of its name at the voters, and its original, restarting, would be named
// a copy of it. spare_still_looks turns it red.
func TestASpareSendsNothing(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	o := testOptions(t, "a", "b")
	o.PeerAddr, o.WitnessAddr = srv.Listener.Addr().String(), srv.Listener.Addr().String()
	spareOnDisk(t, o)
	e, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	running(t, e.Run)
	waitFor(t, time.Second, e.running.Load)
	time.Sleep(4 * o.Lease) // sixteen looks
	if n := hits.Load(); n != 0 {
		t.Fatalf("a spare sent %d requests to its peer and the witness", n)
	}
}

// A failed write of the flag leaves the held copy it was: not silent, and
// no panic when the next reply decides again; once a write lands it is a
// spare.
func TestAFailedSpareFlagWriteLeavesTheCopyHeld(t *testing.T) {
	o := testOptions(t, "a", "b")
	clk := newFakeClock()
	o.clock = clk.now
	st := &State{Path: o.StatePath, Owner: "a", Cluster: testCluster}
	if err := st.Save(quorum.VoterState{Promised: 1002, GrantedTo: "a", Incarnation: 7, Gen: 1, Floor: 1000, Lease: o.Lease}); err != nil {
		t.Fatal(err)
	}
	var failing atomic.Bool
	o.write = func(f *os.File, b []byte) (int, error) {
		if failing.Load() {
			return 0, errors.New("disk full")
		}
		return f.Write(b)
	}
	e, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	running(t, e.Run)
	waitFor(t, time.Second, e.running.Load)
	failing.Store(true)
	words := func() {
		for _, from := range []quorum.NodeID{"b", WitnessID} {
			e.reply(from, quorum.Reply{From: from, To: "a", Nonce: e.Nonce(), ID: quorum.ProbeID, Marked: true, HeldBy: 0xA0})
		}
	}
	for i := 0; i <= 4; i++ {
		words()
		if i < 4 {
			clk.add(o.Lease / 4)
		}
	}
	waitFor(t, time.Second, func() bool { return e.Status().StateWriteError != "" })
	words() // decides again: the write failed
	time.Sleep(50 * time.Millisecond)
	if e.Spare() {
		t.Fatal("silent though no flag write landed")
	}
	failing.Store(false)
	words()
	waitFor(t, 2*time.Second, e.Spare)
	if got, err := st.Load(); err != nil || !got.Spare {
		t.Fatalf("the flag on disk: %+v %v", got, err)
	}
}

// Unspare clears the flag on disk (tests, and 3c-3's command): the
// controller is a member again at its next start.
func TestUnspareMakesAMemberAtTheNextStart(t *testing.T) {
	o := testOptions(t, "a", "b")
	spareOnDisk(t, o)
	e, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	stop := running(t, e.Run)
	waitFor(t, time.Second, e.running.Load)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := e.Unspare(ctx); err != nil {
		t.Fatal(err)
	}
	if !e.Spare() {
		t.Fatal("unspared in place: it stays silent until restarted")
	}
	stop()
	e.Close()
	e, err = New(o)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if s := e.Status(); s.Spare || !s.Commissioned {
		t.Fatalf("after Unspare and a restart: %+v", s)
	}
}

// A peer connection the elector dials closes with a reset (SO_LINGER 0),
// never a FIN: a process that exits drops the vote frames its kernel
// still holds rather than retransmitting them late, through a link
// outage, as a dead boot's frames (#1133 review). peer_dial_lingers turns
// it red.
func TestPeerConnectionsCloseWithAReset(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan error, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			got <- err
			return
		}
		defer c.Close()
		_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, err = c.Read(make([]byte, 1))
		got <- err
	}()
	dial := resetOnClose((&net.Dialer{Timeout: time.Second}).DialContext)
	c, err := dial(context.Background(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if err := <-got; !errors.Is(err, syscall.ECONNRESET) {
		t.Fatalf("the peer saw %v on close, want a reset (ECONNRESET)", err)
	}
}
