package server

import (
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/alarm"
	"github.com/joyautomation/nautilus/oplog"
)

// #1033 review regression tests (the reviewer's probes S1-S6, as
// assertions). The invariant: the view holds an ack only while the
// leader's engine holds that occurrence acknowledged.

// S1: an ack of a NORMAL alarm is refused (409) and proposes nothing, so
// no ack sits in the view to pre-ack the next occurrence.
func TestReviewAckOfNormalRefused(t *testing.T) {
	var lead atomic.Bool
	lead.Store(true)
	cond := map[string]any{"HiTempAlm": false, "LoFlowAlm": false}
	s, eng, al, got := loggedAlarms(t, cond, &lead)
	eng.Evaluate()
	rec := do(t, s, "POST", "/api/alarms/ack", `{"ids":["LoFlow"],"by":"op"}`)
	if rec.Code != http.StatusConflict || len(*got) != 0 {
		t.Fatalf("ack of a normal alarm: %d %s, %d entries proposed; want 409 and nothing proposed", rec.Code, rec.Body, len(*got))
	}
	cond2 := map[string]any{"HiTempAlm": false, "LoFlowAlm": true}
	eng2 := newTestAlarms(t, cond2)
	eng2.RestoreAlarms(al.Held(true, nil))
	eng2.Evaluate()
	if st := state(eng2, "LoFlow"); st != alarm.UnackActive {
		t.Fatalf("S1: a never-acked occurrence comes up %s after a synced takeover", st)
	}
}

// S2: a replica that led before keeps its frozen engine; every takeover
// resets it before restoring the view, synced or not (engine_not_reset
// turns this red).
func TestReviewFrozenEngineIsReset(t *testing.T) {
	for _, synced := range []bool{false, true} {
		var lead atomic.Bool
		lead.Store(true)
		cond := map[string]any{"HiTempAlm": true, "LoFlowAlm": false}
		s, eng, al, _ := loggedAlarms(t, cond, &lead)
		eng.Evaluate()
		if rec := do(t, s, "POST", "/api/alarms/ack", `{"ids":["HiTemp"],"by":"op"}`); rec.Code != http.StatusOK {
			t.Fatal(rec.Code)
		}
		if st := state(eng, "HiTemp"); st != alarm.AckActive {
			t.Fatal(st)
		}
		lead.Store(false) // demoted: engine frozen
		// Meanwhile on the other leader: occurrence 1 RTN (ack-clear),
		// occurrence 2 starts, never acked.
		al.Apply(oplog.Entry{Kind: oplog.KindAckClear, ID: "HiTemp", By: "system"})
		PairAlarms{Engine: eng, Log: al}.RestoreAlarmsSynced(nil, synced)
		lead.Store(true)
		eng.Evaluate()
		if st := state(eng, "HiTemp"); st != alarm.UnackActive {
			t.Errorf("S2 (synced=%v): occurrence 2, never acked, is %s after the re-takeover", synced, st)
		}
	}
}

// S3: an acked alarm whose tag goes dark (suppressed) ends its acked
// occurrence: an ack-clear is proposed (ack_clear_off turns this red).
func TestReviewSuppressClearsTheAck(t *testing.T) {
	var lead atomic.Bool
	lead.Store(true)
	cond := map[string]any{"HiTempAlm": true, "LoFlowAlm": false}
	s, eng, al, _ := loggedAlarms(t, cond, &lead)
	eng.Evaluate()
	do(t, s, "POST", "/api/alarms/ack", `{"ids":["HiTemp"],"by":"op"}`)
	delete(cond, "HiTempAlm")
	eng.Evaluate()
	t.Logf("dark: %s", state(eng, "HiTemp"))
	cond["HiTempAlm"] = false
	eng.Evaluate()
	time.Sleep(100 * time.Millisecond)
	t.Logf("back: %s; view acked=%v", state(eng, "HiTemp"), al.Held(true, nil)["HiTemp"].Acked)
	eng2 := newTestAlarms(t, map[string]any{"HiTempAlm": true, "LoFlowAlm": false})
	eng2.RestoreAlarms(al.Held(true, nil))
	eng2.Evaluate()
	if st := state(eng2, "HiTemp"); st != alarm.UnackActive {
		t.Fatalf("S3: new occurrence after dark/normal comes up %s", st)
	}
}

// S4: a latched alarm (auto-clear false): acked, RTN to unack-rtn (out of
// ack-active: the ack is cleared), re-activates: a new occurrence comes up
// unacknowledged after a synced takeover.
func TestReviewLatchedReactivation(t *testing.T) {
	var lead atomic.Bool
	lead.Store(true)
	cond := map[string]any{"L": true}
	eng, err := alarm.New(alarm.Options{
		Defs: []alarm.Def{{ID: "Latch", Tag: "L", Name: "latched", Priority: alarm.High, AckRequired: true, AutoClear: false}},
		Read: func(p string) (any, bool) { v, ok := cond[p]; return v, ok },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	al := NewAlarmLog(eng, lead.Load, noTerm)
	n, err := oplog.New(oplog.Options{Self: "a", Apply: func(e oplog.Entry) error { _, err := al.Apply(e); return err }})
	if err != nil {
		t.Fatal(err)
	}
	al.Attach(n)
	s := New(newTestRuntime(t), Options{Alarms: eng, AlarmLog: al})
	eng.Evaluate()
	do(t, s, "POST", "/api/alarms/ack", `{"ids":["Latch"],"by":"op"}`)
	cond["L"] = false
	eng.Evaluate()
	t.Logf("after rtn: %s", state(eng, "Latch"))
	cond["L"] = true
	eng.Evaluate()
	time.Sleep(100 * time.Millisecond)
	t.Logf("re-active (new occurrence): %s; view acked=%v", state(eng, "Latch"), al.Held(true, nil)["Latch"].Acked)
	eng2, _ := alarm.New(alarm.Options{
		Defs: []alarm.Def{{ID: "Latch", Tag: "L", Name: "latched", Priority: alarm.High, AckRequired: true, AutoClear: false}},
		Read: func(p string) (any, bool) { return true, true },
	})
	defer eng2.Close()
	eng2.RestoreAlarms(al.Held(true, nil))
	eng2.Evaluate()
	if st := state(eng2, "Latch"); st != alarm.UnackActive {
		t.Fatalf("S4: latched alarm's new occurrence comes up %s after a synced takeover", st)
	}
}

// S5: the shelf twin of S2: shelved on A, demoted, unshelved through the
// log meanwhile; A re-takes over synced and the alarm is not shelved.
func TestReviewFrozenShelfIsReset(t *testing.T) {
	var lead atomic.Bool
	lead.Store(true)
	cond := map[string]any{"HiTempAlm": true, "LoFlowAlm": false}
	s, eng, al, _ := loggedAlarms(t, cond, &lead)
	eng.Evaluate()
	if rec := do(t, s, "POST", "/api/alarms/shelve", `{"id":"HiTemp","seconds":3600,"by":"op"}`); rec.Code != http.StatusOK {
		t.Fatal(rec.Code, rec.Body)
	}
	lead.Store(false)
	al.Apply(oplog.Entry{Kind: oplog.KindUnshelve, ID: "HiTemp", By: "op2"})
	PairAlarms{Engine: eng, Log: al}.RestoreAlarmsSynced(nil, true)
	lead.Store(true)
	eng.Evaluate()
	if st := state(eng, "HiTemp"); st == alarm.Shelved {
		t.Fatalf("S5: unshelved through the log, still %s after the re-takeover", st)
	}
}

// S6: an UNSYNCED takeover clears the view's acks, so a later synced
// takeover can't pre-ack an occurrence nobody acknowledged.
func TestReviewUnsyncedTakeoverClearsTheView(t *testing.T) {
	var lead atomic.Bool
	cond := map[string]any{"HiTempAlm": false, "LoFlowAlm": false}
	_, eng, al, _ := loggedAlarms(t, cond, &lead)
	al.Apply(oplog.Entry{Kind: oplog.KindAck, IDs: []string{"HiTemp"}, By: "op"}) // as standby
	PairAlarms{Engine: eng, Log: al}.RestoreAlarmsSynced(nil, false)
	if al.Held(true, nil)["HiTemp"].Acked {
		t.Fatal("S6: the view still holds the ack right after an unsynced takeover")
	}
	lead.Store(true)
	cond["HiTempAlm"] = true
	eng.Evaluate()
	cond["HiTempAlm"] = false
	eng.Evaluate()
	cond["HiTempAlm"] = true
	eng.Evaluate()
	time.Sleep(100 * time.Millisecond)
	t.Logf("leader: %s; view acked=%v", state(eng, "HiTemp"), al.Held(true, nil)["HiTemp"].Acked)
	eng2 := newTestAlarms(t, map[string]any{"HiTempAlm": true, "LoFlowAlm": false})
	eng2.RestoreAlarms(al.Held(true, nil))
	eng2.Evaluate()
	if st := state(eng2, "HiTemp"); st != alarm.UnackActive {
		t.Fatalf("S6: never-acked occurrence comes up %s on the next synced takeover", st)
	}
}

// Item 3: an ack or a shelve committed too late lapses, as a substitute
// start does: the leader names it by age alone (Lapse), the entry arrives
// Lapsed and changes nothing; a KindLapse naming an ack or shelf a replica
// had already recorded drops it from that view. Unshelves and ack-clears
// never lapse: late, they are safe.
func TestReviewLateAckOrShelveLapses(t *testing.T) {
	var lead atomic.Bool
	lead.Store(true)
	cond := map[string]any{"HiTempAlm": true, "LoFlowAlm": true}
	_, eng, al, _ := loggedAlarms(t, cond, &lead)
	eng.Evaluate()
	now := time.Now()
	old := now.Add(-2 * AlarmIntentCommitBound).UnixMilli()
	for _, c := range []struct {
		e    oplog.Entry
		want bool
	}{
		{oplog.Entry{Kind: oplog.KindAck, TS: old}, true},
		{oplog.Entry{Kind: oplog.KindShelve, TS: old}, true},
		{oplog.Entry{Kind: oplog.KindAck, TS: now.Add(-time.Second).UnixMilli()}, false},
		{oplog.Entry{Kind: oplog.KindUnshelve, TS: old}, false},
		{oplog.Entry{Kind: oplog.KindAckClear, TS: old}, false},
	} {
		if got := al.Lapse(c.e, now); got != c.want {
			t.Errorf("%s %v old: lapse %v, want %v", c.e.Kind, now.Sub(time.UnixMilli(c.e.TS)).Round(time.Second), got, c.want)
		}
	}
	// A lapsed ack changes neither the engine nor the view.
	if _, err := al.Apply(oplog.Entry{Seq: 5, Kind: oplog.KindAck, IDs: []string{"HiTemp"}, By: "op", Lapsed: true}); err != nil {
		t.Fatal(err)
	}
	if state(eng, "HiTemp") != alarm.UnackActive || al.Held(true, nil)["HiTemp"].Acked {
		t.Fatal("a lapsed ack applied")
	}
	// A KindLapse naming an ack and a shelf this replica recorded drops them.
	lead.Store(false)
	until := now.Add(time.Hour).UnixMilli()
	al.Apply(oplog.Entry{Seq: 6, Kind: oplog.KindAck, IDs: []string{"HiTemp"}, By: "op"})
	al.Apply(oplog.Entry{Seq: 7, Kind: oplog.KindShelve, ID: "LoFlow", Until: until, By: "op"})
	al.Apply(oplog.Entry{Seq: 9, Kind: oplog.KindLapse, Refs: []uint64{6, 7}})
	h := al.Held(true, nil)
	if h["HiTemp"].Acked || h["LoFlow"].ShelfUntilMs != 0 {
		t.Fatalf("a KindLapse left %+v in the view", h)
	}
}

// Item 4: ack-clears queued meanwhile go in one entry.
func TestReviewAckClearsBatched(t *testing.T) {
	var lead atomic.Bool
	lead.Store(true)
	cond := map[string]any{"HiTempAlm": true, "LoFlowAlm": true}
	s, eng, al, got := loggedAlarms(t, cond, &lead)
	eng.Evaluate()
	if rec := do(t, s, "POST", "/api/alarms/ack", `{"all":true,"by":"op"}`); rec.Code != http.StatusOK {
		t.Fatal(rec.Code, rec.Body)
	}
	al.clrMu.Lock()
	al.clrRunning = true // hold the proposer while both are queued
	al.clrMu.Unlock()
	cond["HiTempAlm"], cond["LoFlowAlm"] = false, false
	eng.Evaluate() // both return to normal: two clears queued
	al.clrMu.Lock()
	al.clrRunning = false
	al.clrMu.Unlock()
	al.queueClear("HiTemp") // already queued: no duplicate; starts the proposer
	deadline := time.Now().Add(2 * time.Second)
	for al.Held(true, nil)["LoFlow"].Acked || al.Held(true, nil)["HiTemp"].Acked {
		if time.Now().After(deadline) {
			t.Fatal("the ack-clears never committed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	clears := 0
	for _, e := range *got {
		if e.Kind == oplog.KindAckClear {
			clears++
			if len(e.IDs) != 2 {
				t.Errorf("an ack-clear entry names %v; want both alarms in one", e.IDs)
			}
		}
	}
	if clears != 1 {
		t.Fatalf("%d ack-clear entries; want one for the burst", clears)
	}
}
