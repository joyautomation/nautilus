package server

import (
	"context"
	"errors"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/oplog"
)

func init() { // once: the timings are read by goroutines that outlive a test
	sessionRetryEvery = 20 * time.Millisecond
	sessionProposeWait = time.Second
}

// fakeBus is a pair's operator log, in memory: a proposal commits (or is
// refused when readOnly), then every attached SessionLog applies it, in
// order, as the real log does on each replica.
type fakeBus struct {
	mu       sync.Mutex
	readOnly bool
	synced   bool
	log      []oplog.Entry
	members  []*SessionLog
}

type busEnd struct{ b *fakeBus }

func (e busEnd) Propose(_ context.Context, en oplog.Entry) (oplog.Entry, error) {
	e.b.mu.Lock()
	defer e.b.mu.Unlock()
	if e.b.readOnly {
		return oplog.Entry{}, errors.New("read-only")
	}
	e.b.log = append(e.b.log, en)
	for _, m := range e.b.members {
		m.Apply(en)
	}
	return en, nil
}
func (e busEnd) Synced() bool { e.b.mu.Lock(); defer e.b.mu.Unlock(); return e.b.synced }

// pairOfStores is two controllers' session stores joined by one log.
func pairOfStores(t *testing.T) (a, b *sessions, la, lb *SessionLog, bus *fakeBus) {
	t.Helper()
	bus = &fakeBus{synced: true}
	mk := func() (*sessions, *SessionLog) {
		ss := newSessions(time.Hour)
		l := NewSessionLog()
		l.bind(ss)
		l.Attach(busEnd{bus})
		bus.members = append(bus.members, l)
		return ss, l
	}
	a, la = mk()
	b, lb = mk()
	return
}

func waitFor(t *testing.T, what string, f func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if f() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// A session opened on one controller works on the other once the log has
// carried it: the cookie is recognised by its hash, with no key.
func TestSessionSurvivesATakeover(t *testing.T) {
	a, b, _, _, _ := pairOfStores(t)
	id, _ := a.open("alice", "cred1")
	waitFor(t, "the open to reach b", func() bool { _, ok := b.lookup(id); return ok })
	se, _ := b.lookup(id)
	if se.user != "alice" || se.cred != "cred1" || se.local {
		t.Errorf("replicated session = %+v", se)
	}
	if !se.authAt.IsZero() {
		t.Errorf("elevation travelled: authAt %v, want zero (re-enter the password after a takeover)", se.authAt)
	}
	waitFor(t, "a to learn it is committed", func() bool { return !a.isLocal(sessionHash(id)) })
}

// A sign-out reaches the other controller before the sign-out returns, so
// a takeover straight after cannot bring the session back.
func TestSignOutIsFailClosedAcrossAPair(t *testing.T) {
	a, b, _, _, _ := pairOfStores(t)
	id, _ := a.open("alice", "c")
	waitFor(t, "the open to reach b", func() bool { _, ok := b.lookup(id); return ok })
	a.signOut(id) // returns after the end committed
	if _, ok := b.lookup(id); ok {
		t.Error("the other controller still honours a session that was signed out")
	}
}

// An end that arrives before its open (a late retry, a replay) wins.
func TestEndBeatsAnOpenInAnyOrder(t *testing.T) {
	a, _, _, _, _ := pairOfStores(t)
	exp := time.Now().Add(time.Hour)
	e := oplog.SessionEntry{Hash: "h1", User: "alice", Expires: exp.UnixMilli(), Created: time.Now().UnixMilli()}
	e.Op = oplog.SessionEnd
	a.endByLog(e)
	e.Op = oplog.SessionOpen
	a.adopt(e)
	if a.has("h1") {
		t.Error("an open after its end brought the session back")
	}
}

// A read-only log still lets alice sign in; her session is local, works
// here, does not validate on the peer, and replicates when the log returns.
func TestSignInDoesNotWaitForTheLog(t *testing.T) {
	a, b, _, _, bus := pairOfStores(t)
	bus.mu.Lock()
	bus.readOnly = true
	bus.mu.Unlock()
	id, se := a.open("alice", "c")
	if !se.local {
		t.Error("session issued with the log read-only is not marked local")
	}
	if _, ok := a.lookup(id); !ok {
		t.Fatal("alice cannot use her session here")
	}
	time.Sleep(100 * time.Millisecond)
	if _, ok := b.lookup(id); ok {
		t.Error("a local session validates on the peer before it is replicated")
	}
	bus.mu.Lock()
	bus.readOnly = false
	bus.mu.Unlock()
	waitFor(t, "the retry to replicate it", func() bool { _, ok := b.lookup(id); return ok })
	waitFor(t, "it to stop being local", func() bool { return !a.isLocal(sessionHash(id)) })
}

// A copy of the log not known complete honours no session that came
// through it, only local ones.
func TestUnsyncedLogHonoursOnlyLocalSessions(t *testing.T) {
	a, b, _, _, bus := pairOfStores(t)
	id, _ := a.open("alice", "c")
	waitFor(t, "the open to reach b", func() bool { _, ok := b.lookup(id); return ok })
	bus.mu.Lock()
	bus.synced = false
	bus.mu.Unlock()
	if _, ok := b.lookup(id); ok {
		t.Error("an unsynced copy honoured a replicated session")
	}
	// a local session still works there
	bus.mu.Lock()
	bus.readOnly = true
	bus.mu.Unlock()
	lid, _ := b.open("bob", "c")
	if _, ok := b.lookup(lid); !ok {
		t.Error("a local session is refused on an unsynced copy")
	}
}

// A controller that becomes a standby ends the sessions only it holds
// (local ones) and keeps the pair's: the log ends those.
func TestStandbyKeepsReplicatedSessions(t *testing.T) {
	a, _, _, _, bus := pairOfStores(t)
	rid, _ := a.open("alice", "c")
	waitFor(t, "committed", func() bool { return !a.isLocal(sessionHash(rid)) })
	bus.mu.Lock()
	bus.readOnly = true
	bus.mu.Unlock()
	lid, _ := a.open("bob", "c")
	if n := a.endAll(); n != 1 {
		t.Errorf("endAll ended %d, want only the local one", n)
	}
	if _, ok := a.lookup(rid); !ok {
		t.Error("the pair's session was ended when this controller became a standby")
	}
	if _, ok := a.lookup(lid); ok {
		t.Error("the local session survived on a standby")
	}
}

// The same rules over the real operator log: three replicas, the leader
// moves, a peer drops out.
func TestSessionsOverTheRealLog(t *testing.T) {
	p := &pair{}
	servers := make([]*httptest.Server, 3)
	for i := range servers {
		servers[i] = httptest.NewUnstartedServer(nil)
		p.urls[i] = "http://" + servers[i].Listener.Addr().String()
	}
	p.lead(0, 1)
	var stores [3]*sessions
	var logs [3]*SessionLog
	for i := 0; i < 3; i++ {
		ss := newSessions(time.Hour)
		l := NewSessionLog()
		l.bind(ss)
		stores[i], logs[i] = ss, l
		n, err := oplog.New(oplog.Options{Self: p.urls[i], Peers: p.urls[:], Leadership: seat{p, p.urls[i]},
			Token: "t", Ephemeral: true, Apply: func(e oplog.Entry) error { _, err := l.Apply(e); return err }})
		if err != nil {
			t.Fatal(err)
		}
		l.Attach(n)
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

	// alice signs in at the leader a(0); the log carries it to b(1).
	id, _ := stores[0].open("alice", "c")
	eventually(t, "b to honour alice's cookie", func() bool { _, ok := stores[1].lookup(id); return ok })

	// the leader dies: b leads. alice's cookie works, she signs out there, and
	// a (when it returns) does not honour it.
	p.lead(1, 2)
	if _, ok := stores[1].lookup(id); !ok {
		t.Fatal("alice's cookie does not work at the new leader")
	}
	stores[1].end(id)
	eventually(t, "a to drop the signed-out session", func() bool { _, ok := stores[0].lookup(id); return !ok })

	// no majority: bob still signs in at b, his session is local and a does
	// not honour it; the peers return, it replicates.
	p.gates[0].down.Store(true)
	p.gates[2].down.Store(true)
	for i := 0; i < 3; i++ {
		p.nodes[1].Ping(t.Context())
	}
	bid, bse := stores[1].open("bob", "c")
	if !bse.local {
		t.Error("a sign-in with no majority is not local")
	}
	if _, ok := stores[1].lookup(bid); !ok {
		t.Fatal("bob cannot use his session where he signed in")
	}
	if _, ok := stores[0].lookup(bid); ok {
		t.Error("a controller honours a session the log never carried")
	}
	p.gates[0].down.Store(false)
	p.gates[2].down.Store(false)
	p.nodes[1].Heartbeat(t.Context())
	eventually(t, "bob's session to replicate", func() bool { _, ok := stores[0].lookup(bid); return ok })
}

// Reading a cookie while the log commits session entries must not deadlock:
// the log applies under its own lock and then takes the store's, so the store
// must never ask the log anything under its lock (Fable review of #1211).
func TestLookupsDuringCommitsDoNotDeadlock(t *testing.T) {
	a, b, _, _, _ := pairOfStores(t)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					b.lookup("some-cookie")
					b.byHashLive("some-hash")
				}
			}
		}()
	}
	done := make(chan struct{})
	go func() {
		for i := 0; i < 200; i++ {
			id, _ := a.open("alice", "c")
			a.signOut(id)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("deadlock: sign-ins and sign-outs stopped while cookies were being read")
	}
	close(stop)
	wg.Wait()
}

// A session ended here while its open was still owed to the log does not come
// back when the open commits later.
func TestLocalEndLeavesATombstone(t *testing.T) {
	a, b, _, _, bus := pairOfStores(t)
	bus.mu.Lock()
	bus.readOnly = true
	bus.mu.Unlock()
	id, se := a.open("alice", "c")
	a.signOut(id) // log read-only: the end is owed too
	// an open that was in flight reaches the log after all
	a.adopt(oplog.SessionEntry{Op: oplog.SessionOpen, Hash: se.hash, User: "alice", Expires: se.expires.UnixMilli(), Created: se.created.UnixMilli()})
	if a.has(se.hash) {
		t.Error("a session signed out locally came back when its open arrived")
	}
	_ = b
}

// The log's replay at start reaches the store when it is bound later.
func TestReplayBeforeTheStoreIsBound(t *testing.T) {
	l := NewSessionLog()
	exp := time.Now().Add(time.Hour).UnixMilli()
	l.Apply(oplog.SessionEntryOf("system", oplog.SessionEntry{Op: oplog.SessionOpen, Hash: "h1", User: "alice", Expires: exp}))
	l.Apply(oplog.SessionEntryOf("system", oplog.SessionEntry{Op: oplog.SessionOpen, Hash: "h2", User: "bob", Expires: exp}))
	l.Apply(oplog.SessionEntryOf("system", oplog.SessionEntry{Op: oplog.SessionEnd, Hash: "h2", Expires: exp}))
	ss := newSessions(time.Hour)
	l.bind(ss)
	if !ss.has("h1") {
		t.Error("an open replayed before the store existed was dropped")
	}
	if ss.has("h2") {
		t.Error("an ended session came back from the replay")
	}
}

// An end that could not commit is kept in a file, and a restart sends it.
func TestOwedEndsSurviveARestart(t *testing.T) {
	file := t.TempDir() + "/ends.json"
	bus := &fakeBus{synced: true, readOnly: true}
	ss := newSessions(time.Hour)
	l := NewSessionLog()
	l.SetFile(file)
	l.bind(ss)
	l.Attach(busEnd{bus})
	bus.members = append(bus.members, l)
	id, _ := ss.open("alice", "c")
	ss.end(id)
	time.Sleep(60 * time.Millisecond)
	l.Close() // the process ends with the end still owed
	// a new process: the log is back, the file is read, the end goes out
	bus.mu.Lock()
	bus.readOnly = false
	bus.mu.Unlock()
	ss2 := newSessions(time.Hour)
	l2 := NewSessionLog()
	l2.SetFile(file)
	l2.bind(ss2)
	l2.Attach(busEnd{bus})
	bus.members = append(bus.members, l2)
	waitFor(t, "the owed end to be sent", func() bool {
		bus.mu.Lock()
		defer bus.mu.Unlock()
		for _, e := range bus.log {
			if b, ok := e.SessionBody(); ok && b.Op == oplog.SessionEnd {
				return true
			}
		}
		return false
	})
	l2.Close()
}

// A sign-in that replaces a cookie, or evicts the oldest session, never waits
// for the log.
func TestReplacingACookieDoesNotWaitForTheLog(t *testing.T) {
	a, _, _, _, bus := pairOfStores(t)
	id, _ := a.open("alice", "c")
	bus.mu.Lock()
	bus.readOnly = true
	bus.mu.Unlock()
	start := time.Now()
	a.end(id) // the sign-in path
	if d := time.Since(start); d > sessionEndWait/2 {
		t.Errorf("replacing a cookie waited %v for the log", d)
	}
}
