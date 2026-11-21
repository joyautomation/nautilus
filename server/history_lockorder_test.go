package server

import (
	"context"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/alarm"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/oplog"
	"github.com/joyautomation/nautilus/runtime"
)

// Review repro 1a: PairAlarms.restore calls AlarmLog.superseded() ->
// oplog.Node.RetainedStale() (n.mu) while restoring() holds liveMu; the
// log's commit calls AlarmLog.Apply (liveMu) while holding n.mu.
// #1177 review (Fable): a takeover's alarm restore asks whether the log is
// superseded under liveMu, which a commit's Apply takes under the node's
// lock: the answer must never need that lock. stale_under_lock turns it
// red (a deadlock, caught by the timeout).
func TestASupersededCheckInTheAlarmRestoreNeverWaitsOnACommit(t *testing.T) {
	var lead atomic.Bool
	lead.Store(true)
	eng, err := alarm.New(alarm.Options{
		Defs: []alarm.Def{{ID: "Latch", Tag: "L", Name: "latched", Priority: alarm.High, AckRequired: true}},
		Read: func(p string) (any, bool) { return true, true },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	al := NewAlarmLog(eng, lead.Load, noTerm)
	inApply := make(chan struct{})
	var once sync.Once
	n, err := oplog.New(oplog.Options{Self: "a", Apply: func(e oplog.Entry) error {
		once.Do(func() { close(inApply) }) // commitTo holds the node's lock here
		time.Sleep(200 * time.Millisecond) // stands for the fsyncs before Apply under that lock
		_, err := al.Apply(e)
		return err
	}})
	if err != nil {
		t.Fatal(err)
	}
	al.Attach(n)
	eng.Evaluate()
	done := make(chan string, 2)
	go func() {
		_, _ = n.Propose(context.Background(), oplog.Entry{Kind: oplog.KindAck, IDs: []string{"Latch"}, By: "op"})
		done <- "commit"
	}()
	<-inApply
	go func() {
		PairAlarms{Engine: eng, Log: al}.RestoreAlarmsSynced(nil, false) // a takeover's restore
		done <- "restore"
	}()
	for i := 0; i < 2; i++ {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("DEADLOCK: the takeover's alarm restore (liveMu -> oplog n.mu) and the log's commit (n.mu -> liveMu) wait on each other")
		}
	}
}

// Review repro 1b: an unsynced takeover (the catch-up still running when
// its wait ends) calls SubstituteLog.RetainedStale -> n.mu under the
// runtime's subMu and tag lock, while that catch-up, at its commit, calls
// Apply -> ApplySubstitute (subMu) under n.mu. The scan never returns.
// #1177 review (Fable): an unsynced takeover (its catch-up still running
// past the wait) asks whether the log is superseded under subMu and the
// tag lock, which the catch-up's commit takes in ApplySubstitute under the
// node's lock: the new leader must scan. stale_under_lock turns it red.
func TestAnUnsyncedTakeoverNeverWaitsOnTheCatchUpsCommit(t *testing.T) {
	p := &pair{}
	servers := make([]*httptest.Server, 3)
	for i := range servers {
		servers[i] = httptest.NewUnstartedServer(nil)
		p.urls[i] = "http://" + servers[i].Listener.Addr().String()
	}
	p.lead(0, 1)
	var slowB atomic.Bool
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
			slow := i == 1
			apply = func(e oplog.Entry) error {
				if slow && slowB.Load() {
					// Under the node's lock (commitTo): stands for the log's
					// fsyncs before Apply, past the takeover's 300 ms wait.
					time.Sleep(600 * time.Millisecond)
				}
				_, err := sl.Apply(e)
				return err
			}
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
		}
	})
	firstScan(t, p.rts[0])
	p.rts[1].Scan() // standby
	p.gates[1].down.Store(true)
	if r := startOn(t, p.srvs[0], `{"name":"Level"}`); r.Code != 200 {
		t.Fatalf("start on A = %d %s", r.Code, r.Body)
	}
	p.gates[0].down.Store(true) // A dies
	p.gates[1].down.Store(false)
	slowB.Store(true)
	p.lead(1, 2)
	done := make(chan struct{})
	go func() {
		for i := 0; i < 400 && !p.rts[1].Tags().Bool("Seen"); i++ {
			p.rts[1].Scan()
			time.Sleep(5 * time.Millisecond)
		}
		close(done)
	}()
	select {
	case <-done:
		if !p.rts[1].Tags().Bool("Seen") {
			t.Fatal("B's logic never ran")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("DEADLOCK: B's takeover scan (subMu+tag lock -> oplog n.mu via RetainedStale) and its catch-up's commit (n.mu -> ApplySubstitute subMu) wait on each other: the new leader never scans")
	}
}
