package server

import (
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/oplog"
	"github.com/joyautomation/nautilus/retain"
	"github.com/joyautomation/nautilus/runtime"
)

// #1029 3c-3c-2a: a standby whose log learns a newer history while it runs
// (no restart yet, so the start's retain drop has not run) takes over with
// the old history's substitute still in its retain store. The takeover
// must restore none of it: SubstituteLog.RetainedStale passes the node's
// verdict to the runtime. retained_stale_ignored turns it red (the first
// scan runs on the retained 95).
func TestASupersededStandbysTakeoverRestoresNoneOfItsRetainedSubstitutes(t *testing.T) {
	p := &pair{}
	servers := make([]*httptest.Server, 3)
	for i := range servers {
		servers[i] = httptest.NewUnstartedServer(nil)
		p.urls[i] = "http://" + servers[i].Listener.Addr().String()
	}
	p.lead(0, 1)
	dir := t.TempDir()
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
			apply = func(e oplog.Entry) error { _, err := sl.Apply(e); return err }
		}
		o := oplog.Options{Self: p.urls[i], Peers: p.urls[:], Leadership: seat{p, p.urls[i]},
			Token: "t", Ephemeral: true, Apply: apply}
		if i == 1 {
			// B keeps a mark: only a marked copy records a newer history.
			o.Ephemeral = false
			o.File, o.Mark, o.Identity = filepath.Join(dir, "oplog.jsonl"), filepath.Join(dir, "oplog.mark"), "c/b/box"
		}
		n, err := oplog.New(o)
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
			p.nodes[i].Close()
		}
	})
	firstScan(t, p.rts[0])
	p.rts[1].Scan() // standby
	now := p.clks[1].t
	p.stores[1].Save(retain.State{Substitutes: &retain.SubstituteState{
		Entries: []retain.SubstituteRetain{{ID: 5, Name: "Level", Value: 95.0, By: "op",
			StartMs: now.UnixMilli(), UntilMs: now.Add(time.Hour).UnixMilli(), GrantedMs: time.Hour.Milliseconds()}},
		HighID: 5, LastSeenMs: now.UnixMilli(),
	}})
	if err := p.nodes[1].LearnHistory(oplog.Lineage{History: 1, HistoryID: 7, HistoryBy: "op"}); err != nil {
		t.Fatal(err)
	}
	if !p.nodes[1].RetainedStale() {
		t.Fatal("setup: B's log is not superseded")
	}
	p.gates[0].down.Store(true) // A dies
	p.lead(1, 2)
	if m := firstScan(t, p.rts[1]); m == 95 {
		t.Fatal("B's first scan ran on the superseded history's retained substitute")
	}
	if l := p.rts[1].Substitutes(); len(l) != 0 {
		t.Fatalf("B restored %+v from a superseded log's retain store", l)
	}
}
