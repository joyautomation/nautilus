package server

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/alarm"
	"github.com/joyautomation/nautilus/oplog"
)

// Occurrences at the server (#715 PR 1): an ack names the occurrence the
// person saw, a pair's log binds its acks to occurrences, keeps the
// covered alarms' occurrences for a takeover, and a takeover continues
// them only from a synced view.

func occOf(t *testing.T, eng *alarm.Engine, id string) alarm.Record {
	t.Helper()
	r, ok := eng.Lookup(id)
	if !ok {
		t.Fatalf("no alarm %s", id)
	}
	return r
}

// An ack naming an occurrence that ended is refused (409) and changes
// nothing; naming the current one, it acks.
func TestAckNamesItsOccurrence(t *testing.T) {
	cond := map[string]any{"HiTempAlm": true, "LoFlowAlm": false}
	s, eng := alarmServer(t, cond)
	eng.Evaluate()
	cur := occOf(t, eng, "HiTemp").Occurrence
	rec := do(t, s, "POST", "/api/alarms/ack", `{"ids":["HiTemp"],"by":"op","occurrences":{"HiTemp":"00000000deadbeef"}}`)
	if rec.Code != http.StatusConflict || state(eng, "HiTemp") != alarm.UnackActive {
		t.Fatalf("ack of an ended occurrence: %d %s, state %s; want 409 and nothing acked", rec.Code, rec.Body, state(eng, "HiTemp"))
	}
	rec = do(t, s, "POST", "/api/alarms/ack", `{"ids":["HiTemp"],"by":"op","occurrences":{"HiTemp":"`+cur+`"}}`)
	if rec.Code != http.StatusOK || state(eng, "HiTemp") != alarm.AckActive {
		t.Fatalf("ack of the current occurrence: %d %s", rec.Code, rec.Body)
	}
	if !json.Valid(rec.Body.Bytes()) {
		t.Fatal(rec.Body)
	}
}

// Through a pair's log: the ack entry names each alarm's occurrence (and
// its activation); a committed ack whose occurrence ended before it
// applied acknowledges nothing.
func TestLoggedAckIsBoundToItsOccurrence(t *testing.T) {
	var lead atomic.Bool
	lead.Store(true)
	cond := map[string]any{"HiTempAlm": true, "LoFlowAlm": true}
	s, eng, al, got := loggedAlarms(t, cond, &lead)
	eng.Evaluate()
	hi := occOf(t, eng, "HiTemp")
	if rec := do(t, s, "POST", "/api/alarms/ack", `{"ids":["HiTemp"],"by":"op","occurrences":{"HiTemp":"00000000deadbeef"}}`); rec.Code != http.StatusConflict || len(*got) != 0 {
		t.Fatalf("an ended occurrence through the log: %d, %d proposed; want 409, nothing proposed", rec.Code, len(*got))
	}
	if rec := do(t, s, "POST", "/api/alarms/ack", `{"all":true,"by":"op"}`); rec.Code != http.StatusOK {
		t.Fatalf("ack all: %d %s", rec.Code, rec.Body)
	}
	e := (*got)[0]
	if len(e.Occs) != 2 || e.Occs[0].ID != "HiTemp" || e.Occs[0].Occ != hi.Occurrence || e.Occs[0].ActiveMs != hi.ActiveMs {
		t.Fatalf("ack entry occurrences %+v; want HiTemp on %s first", e.Occs, hi.Occurrence)
	}

	// LoFlow returns to normal and activates again: a new occurrence. An
	// ack of the old one, committed now, acknowledges nothing.
	cond["LoFlowAlm"] = false
	eng.Evaluate()
	cond["LoFlowAlm"] = true
	eng.Evaluate()
	old := e.Occs[1]
	if _, err := al.Apply(oplog.Entry{Kind: oplog.KindAck, IDs: []string{"LoFlow"}, Occs: []oplog.Occ{old}, By: "late"}); err != nil {
		t.Fatal(err)
	}
	if st := state(eng, "LoFlow"); st != alarm.UnackActive {
		t.Fatalf("LoFlow %s after a late ack of its ended occurrence; want unack-active", st)
	}
}

// standbyLog is a standby's alarm log over its own engine (a replica that
// stands by, takes committed entries into its view, then takes over).
func standbyLog(t *testing.T, cond map[string]any) (*alarm.Engine, *AlarmLog, *atomic.Bool) {
	t.Helper()
	var lead atomic.Bool
	eng := newTestAlarms(t, cond)
	al := NewAlarmLog(eng, lead.Load, noTerm)
	return eng, al, &lead
}

// A synced takeover continues the occurrences the view holds, if the
// field re-asserts them: an acknowledged alarm no escalation covers
// arrives ack-active on its occurrence (from its ack entry), a covered one
// waits since when it began waiting (from occurrence-start). An unsynced
// takeover continues none.
func TestTakeoverContinuesTheViewsOccurrences(t *testing.T) {
	for _, synced := range []bool{true, false} {
		cond := map[string]any{"HiTempAlm": true, "LoFlowAlm": true}
		eng, al, lead := standbyLog(t, cond)
		act := time.Now().Add(-20 * time.Minute).UnixMilli()
		wait := time.Now().Add(-15 * time.Minute).UnixMilli()
		for _, e := range []oplog.Entry{
			{Kind: oplog.KindAck, IDs: []string{"HiTemp"}, Occs: []oplog.Occ{{ID: "HiTemp", Occ: "1111111111111111", ActiveMs: act}}, By: "op", TS: act + 1000},
			{Kind: oplog.KindOccurrenceStart, Occs: []oplog.Occ{{ID: "LoFlow", Occ: "2222222222222222", ActiveMs: act, WaitMs: wait}}, By: "system"},
		} {
			if _, err := al.Apply(e); err != nil {
				t.Fatal(err)
			}
		}
		lead.Store(true)
		PairAlarms{Engine: eng, Log: al}.RestoreAlarmsSynced(nil, synced)
		eng.Evaluate()
		hi, lo := occOf(t, eng, "HiTemp"), occOf(t, eng, "LoFlow")
		if synced {
			if hi.State != alarm.AckActive || hi.Occurrence != "1111111111111111" || hi.ActiveMs != act {
				t.Errorf("synced: HiTemp %s %s %d; want ack-active on its occurrence", hi.State, hi.Occurrence, hi.ActiveMs)
			}
			if lo.State != alarm.UnackActive || lo.Occurrence != "2222222222222222" || lo.WaitMs != wait {
				t.Errorf("synced: LoFlow %s %s wait %d; want unack-active on its occurrence, waiting since %d", lo.State, lo.Occurrence, lo.WaitMs, wait)
			}
		} else {
			if hi.State != alarm.UnackActive || hi.Occurrence == "1111111111111111" || lo.Occurrence == "2222222222222222" {
				t.Errorf("unsynced: HiTemp %s %s, LoFlow %s; want new occurrences, nothing acked", hi.State, hi.Occurrence, lo.Occurrence)
			}
		}
	}
}

// The leader keeps the covered alarms' occurrences in the view, batched at
// its log tick: occurrence-start when one begins waiting, occurrence-end
// when it ends; an alarm no policy covers proposes nothing; one with an
// ack-clear names the occurrence it clears.
func TestLeaderSyncsOccurrences(t *testing.T) {
	var lead atomic.Bool
	lead.Store(true)
	cond := map[string]any{"HiTempAlm": false, "LoFlowAlm": false}
	eng := newTestAlarms(t, cond)
	al := NewAlarmLog(eng, lead.Load, noTerm)
	var mu sync.Mutex
	var got []oplog.Entry
	n, err := oplog.New(oplog.Options{Self: "a", Apply: func(e oplog.Entry) error {
		mu.Lock()
		got = append(got, e)
		mu.Unlock()
		_, err := al.Apply(e)
		return err
	}})
	if err != nil {
		t.Fatal(err)
	}
	al.Attach(n)
	al.SetOccurrenceMatch(func(d alarm.Def) bool { return d.ID == "HiTemp" })
	PairAlarms{Engine: eng, Log: al}.RestoreAlarmsSynced(nil, true) // live

	kinds := func() []string {
		mu.Lock()
		defer mu.Unlock()
		var k []string
		for _, e := range got {
			k = append(k, e.Kind)
		}
		return k
	}
	waitFor := func(want []string) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for !slices.Equal(kinds(), want) {
			if time.Now().After(deadline) {
				t.Fatalf("proposed %v; want %v", kinds(), want)
			}
			al.Reconcile()
			time.Sleep(10 * time.Millisecond)
		}
	}
	cond["HiTempAlm"], cond["LoFlowAlm"] = true, true
	eng.Evaluate()
	al.Reconcile()
	waitFor([]string{oplog.KindOccurrenceStart})
	mu.Lock()
	start := got[0]
	mu.Unlock()
	hi := occOf(t, eng, "HiTemp")
	if len(start.Occs) != 1 || start.Occs[0] != (oplog.Occ{ID: "HiTemp", Occ: hi.Occurrence, ActiveMs: hi.ActiveMs, WaitMs: hi.WaitMs}) {
		t.Fatalf("occurrence-start %+v; want HiTemp's alone", start.Occs)
	}
	al.Reconcile() // nothing moved: nothing proposed
	time.Sleep(50 * time.Millisecond)
	if k := kinds(); len(k) != 1 {
		t.Fatalf("a second tick proposed %v", k)
	}
	// Its condition clears (unack-rtn: no takeover continues it): the
	// view's occurrence ends.
	cond["HiTempAlm"] = false
	eng.Evaluate()
	waitFor([]string{oplog.KindOccurrenceStart, oplog.KindOccurrenceEnd})
	if h := al.Held(true, nil)["HiTemp"]; h.Occurrence != "" {
		t.Fatalf("view after the end %+v", h)
	}
}

// The residual the package comment states, pinned both ways: an
// acknowledged occurrence ends and a new one begins; if the ack-clear
// (naming the ended occurrence) committed, a takeover starts the new one
// unacknowledged; if the leader died first, the takeover takes the
// field's condition for the old occurrence and carries its ack. Narrowed
// to one log round trip, not closed.
func TestAckClearResidualIsNarrowed(t *testing.T) {
	for _, cleared := range []bool{true, false} {
		cond := map[string]any{"HiTempAlm": true, "LoFlowAlm": false}
		eng, al, lead := standbyLog(t, cond)
		x := oplog.Occ{ID: "HiTemp", Occ: "3333333333333333", ActiveMs: time.Now().Add(-time.Hour).UnixMilli()}
		if _, err := al.Apply(oplog.Entry{Kind: oplog.KindAck, IDs: []string{"HiTemp"}, Occs: []oplog.Occ{x}, By: "op"}); err != nil {
			t.Fatal(err)
		}
		if cleared {
			if _, err := al.Apply(oplog.Entry{Kind: oplog.KindAckClear, IDs: []string{"HiTemp"}, Occs: []oplog.Occ{{ID: "HiTemp", Occ: x.Occ}}, By: "system"}); err != nil {
				t.Fatal(err)
			}
		}
		lead.Store(true)
		PairAlarms{Engine: eng, Log: al}.RestoreAlarmsSynced(nil, true)
		eng.Evaluate()
		hi := occOf(t, eng, "HiTemp")
		if cleared && (hi.State != alarm.UnackActive || hi.Occurrence == x.Occ) {
			t.Errorf("ack-clear committed: HiTemp %s on %s; want a new occurrence, unacknowledged", hi.State, hi.Occurrence)
		}
		if !cleared && (hi.State != alarm.AckActive || hi.Occurrence != x.Occ) {
			t.Errorf("leader died before the ack-clear: HiTemp %s on %s; the residual as documented is ack-active on %s", hi.State, hi.Occurrence, x.Occ)
		}
	}
}

// An occurrence-start naming another occurrence than the view's ack drops
// that ack (its occurrence ended; its ack-clear may still be on its way):
// a takeover never carries it to the new one. An occurrence-end of the
// view's occurrence drops its ack too.
func TestOccurrenceEntriesDropStaleAcks(t *testing.T) {
	_, al, _ := standbyLog(t, map[string]any{"HiTempAlm": true, "LoFlowAlm": true})
	x := oplog.Occ{ID: "HiTemp", Occ: "4444444444444444", ActiveMs: 1000}
	y := oplog.Occ{ID: "LoFlow", Occ: "5555555555555555", ActiveMs: 1000}
	for _, e := range []oplog.Entry{
		{Kind: oplog.KindAck, IDs: []string{"HiTemp", "LoFlow"}, Occs: []oplog.Occ{x, y}, By: "op"},
		{Kind: oplog.KindOccurrenceStart, Occs: []oplog.Occ{{ID: "HiTemp", Occ: "6666666666666666", ActiveMs: 2000, WaitMs: 2000}}, By: "system"},
		{Kind: oplog.KindOccurrenceEnd, Occs: []oplog.Occ{{ID: "LoFlow", Occ: y.Occ}}, By: "system"},
	} {
		if _, err := al.Apply(e); err != nil {
			t.Fatal(err)
		}
	}
	held := al.Held(true, nil)
	if h := held["HiTemp"]; h.Acked || h.Occurrence != "6666666666666666" || h.WaitMs != 2000 {
		t.Errorf("HiTemp held %+v; want the new occurrence, no ack", h)
	}
	if l := held["LoFlow"]; l.Acked || l.Occurrence != "" {
		t.Errorf("LoFlow held %+v; want nothing after its end", l)
	}
}

// An acknowledged alarm that returns to normal proposes its ack-clear at
// once, naming the occurrence that ended, and the view drops that
// occurrence with the ack in the same commit (not at the next log tick):
// the residual stays one round trip.
func TestAckClearEndsTheAckedOccurrence(t *testing.T) {
	var lead atomic.Bool
	lead.Store(true)
	cond := map[string]any{"HiTempAlm": true, "LoFlowAlm": false}
	s, eng, al, _ := loggedAlarms(t, cond, &lead)
	PairAlarms{Engine: eng, Log: al}.RestoreAlarmsSynced(nil, true) // live
	eng.Evaluate()
	if rec := do(t, s, "POST", "/api/alarms/ack", `{"ids":["HiTemp"],"by":"op"}`); rec.Code != http.StatusOK {
		t.Fatalf("ack: %d %s", rec.Code, rec.Body)
	}
	if h := al.Held(true, nil)["HiTemp"]; !h.Acked || h.Occurrence == "" {
		t.Fatalf("view after the ack %+v; want the ack with its occurrence", h)
	}
	cond["HiTempAlm"] = false
	eng.Evaluate() // ack-active to normal: ack-clear queued (no Reconcile here)
	deadline := time.Now().Add(3 * time.Second)
	for {
		h := al.Held(true, nil)["HiTemp"]
		if !h.Acked && h.Occurrence == "" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("view after the ack-clear %+v; want neither the ack nor its occurrence", h)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A takeover that could not continue an occurrence (unsynced) catches the
// log up later. A committed ack of the occurrence that stood is adopted
// once the log is caught up; one whose occurrence the log shows ended
// (an ack-clear naming it, an occurrence-end, a later occurrence-start)
// is not, and the journal says why (naut-redundancy's case: the old
// leader recorded the end and a new occurrence, then died).
func TestLateAckAdoptsOnlyWhatTheLogLeavesStanding(t *testing.T) {
	x := oplog.Occ{ID: "HiTemp", Occ: "8888888888888888", ActiveMs: time.Now().Add(-time.Hour).UnixMilli()}
	ack := oplog.Entry{Kind: oplog.KindAck, IDs: []string{"HiTemp"}, Occs: []oplog.Occ{x}, By: "op"}
	for _, tc := range []struct {
		name  string
		after []oplog.Entry
		adopt bool
		note  string
	}{
		{"stood", nil, true, ""},
		{"ack-clear of it", []oplog.Entry{{Kind: oplog.KindAckClear, IDs: []string{"HiTemp"}, Occs: []oplog.Occ{{ID: "HiTemp", Occ: x.Occ}}}}, false, "its ack was cleared"},
		{"ended", []oplog.Entry{{Kind: oplog.KindOccurrenceEnd, Occs: []oplog.Occ{{ID: "HiTemp", Occ: x.Occ}}}}, false, "occurrence ended"},
		{"a later one began", []oplog.Entry{{Kind: oplog.KindOccurrenceStart, Occs: []oplog.Occ{{ID: "HiTemp", Occ: "9999999999999999", ActiveMs: x.ActiveMs + 1000, WaitMs: x.ActiveMs + 1000}}}}, false, "later occurrence"},
	} {
		var lead atomic.Bool
		lead.Store(true)
		cond := map[string]any{"HiTempAlm": true, "LoFlowAlm": false}
		_, eng, al, _ := loggedAlarms(t, cond, &lead)
		PairAlarms{Engine: eng, Log: al}.RestoreAlarmsSynced(nil, false) // unsynced: nothing continued
		eng.Evaluate()                                                   // HiTemp stands, a new occurrence
		for _, e := range append([]oplog.Entry{ack}, tc.after...) {
			if _, err := al.Apply(e); err != nil {
				t.Fatal(err)
			}
		}
		if st := state(eng, "HiTemp"); st != alarm.UnackActive {
			t.Fatalf("%s: %s before the log is judged caught up; want unack-active (the adoption waits)", tc.name, st)
		}
		al.Reconcile() // caught up: decide
		hi := occOf(t, eng, "HiTemp")
		if tc.adopt {
			if hi.State != alarm.AckActive || hi.Occurrence != x.Occ {
				t.Errorf("%s: %s on %s; want ack-active on %s", tc.name, hi.State, hi.Occurrence, x.Occ)
			}
			continue
		}
		if hi.State != alarm.UnackActive || hi.Occurrence == x.Occ {
			t.Errorf("%s: %s on %s; want unack-active on its own occurrence", tc.name, hi.State, hi.Occurrence)
		}
		evs, _ := eng.Journal(time.Time{}, time.Time{}, alarm.Filter{})
		found := false
		for _, e := range evs {
			found = found || (strings.Contains(e.Name, "ack of HiTemp by op not applied") && strings.Contains(e.Name, tc.note))
		}
		if !found {
			t.Errorf("%s: no journal note saying the ack was not applied (%q)", tc.name, tc.note)
		}
	}
}

// While the log is not caught up (this copy unsynced), a pending adoption
// waits: Reconcile neither adopts it nor clears its ack from the view as
// one the engine does not hold.
func TestPendingAdoptionWaitsForTheCatchUp(t *testing.T) {
	var lead atomic.Bool
	lead.Store(true)
	cond := map[string]any{"HiTempAlm": true, "LoFlowAlm": false}
	eng := newTestAlarms(t, cond)
	al := NewAlarmLog(eng, lead.Load, noTerm)
	al.clrRetry = time.Hour // a clear proposed here would never commit
	dir := t.TempDir()
	n, err := oplog.New(oplog.Options{Self: "a", Peers: []string{"a", "b", "w"}, File: dir + "/log.jsonl", Mark: dir + "/log.mark", Token: "t",
		Leadership: fixedLeader{}, Apply: func(e oplog.Entry) error { _, err := al.Apply(e); return err }})
	if err != nil {
		t.Fatal(err)
	}
	if n.Synced() {
		t.Skip("a fresh marked copy reads synced; the case needs an unsynced one")
	}
	al.Attach(n)
	PairAlarms{Engine: eng, Log: al}.RestoreAlarmsSynced(nil, false)
	eng.Evaluate()
	x := oplog.Occ{ID: "HiTemp", Occ: "abababababababab", ActiveMs: time.Now().Add(-time.Hour).UnixMilli()}
	if _, err := al.Apply(oplog.Entry{Kind: oplog.KindAck, IDs: []string{"HiTemp"}, Occs: []oplog.Occ{x}, By: "op"}); err != nil {
		t.Fatal(err)
	}
	al.Reconcile()
	al.clrMu.Lock()
	queued := slices.Clone(al.clr)
	al.clrMu.Unlock()
	if len(queued) != 0 || !al.Held(true, nil)["HiTemp"].Acked || state(eng, "HiTemp") != alarm.UnackActive {
		t.Fatalf("unsynced: clears queued %v, view acked %v, state %s; want nothing cleared, the ack kept waiting, unacked",
			queued, al.Held(true, nil)["HiTemp"].Acked, state(eng, "HiTemp"))
	}
}

// fixedLeader says this copy leads, to nobody.
type fixedLeader struct{}

func (fixedLeader) IsLeader() bool     { return true }
func (fixedLeader) LeaderAddr() string { return "" }

// What syncOccurrences held back while an adoption was pending is
// proposed by the pass that decides it (naut-redundancy): the adoption is
// refused (the alarm's condition dropped meanwhile), and the same
// Reconcile records the occurrence the alarm now stands on.
func TestHeldBackOccurrenceIsProposedOnceDecided(t *testing.T) {
	var lead atomic.Bool
	lead.Store(true)
	cond := map[string]any{"HiTempAlm": true, "LoFlowAlm": false}
	_, eng, al, got := loggedAlarms(t, cond, &lead)
	al.SetOccurrenceMatch(func(d alarm.Def) bool { return d.ID == "HiTemp" })
	PairAlarms{Engine: eng, Log: al}.RestoreAlarmsSynced(nil, false)
	eng.Evaluate()
	x := oplog.Occ{ID: "HiTemp", Occ: "cdcdcdcdcdcdcdcd", ActiveMs: time.Now().Add(-time.Hour).UnixMilli()}
	if _, err := al.Apply(oplog.Entry{Kind: oplog.KindAck, IDs: []string{"HiTemp"}, Occs: []oplog.Occ{x}, By: "op"}); err != nil {
		t.Fatal(err)
	}
	cond["HiTempAlm"] = false
	eng.Evaluate() // unack-rtn: it did not stand
	cond["HiTempAlm"] = true
	eng.Evaluate() // standing again, same occurrence (a flap)
	al.Reconcile() // decides: refused; then syncs
	cur := occOf(t, eng, "HiTemp")
	deadline := time.Now().Add(3 * time.Second)
	for {
		h := al.Held(true, nil)["HiTemp"]
		if h.Occurrence == cur.Occurrence && h.WaitMs == cur.WaitMs && !h.Acked {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("view %+v; want the occurrence HiTemp stands on now (%s), no ack; proposed %+v", h, cur.Occurrence, *got)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if cur.State != alarm.UnackActive {
		t.Fatalf("HiTemp %s; want unack-active (the adoption was refused)", cur.State)
	}
}

// Reconcile with an adoption pending, while the log applies a commit
// (#1198 review): Reconcile asks the node whether the log is caught up,
// and the node applies the commit (AlarmLog.Apply) under its own lock;
// asking it under AlarmLog.mu deadlocked both. The commit must go through.
func TestReconcileDuringACommitWithAnAdoptionPending(t *testing.T) {
	var lead atomic.Bool
	lead.Store(true)
	cond := map[string]any{"HiTempAlm": true, "LoFlowAlm": false}
	eng := newTestAlarms(t, cond)
	al := NewAlarmLog(eng, lead.Load, noTerm)
	reconciling := make(chan struct{})
	n, err := oplog.New(oplog.Options{Self: "a", Apply: func(e oplog.Entry) error {
		if e.Kind == oplog.KindUnshelve {
			go func() { close(reconciling); al.Reconcile() }()
			<-reconciling
			time.Sleep(200 * time.Millisecond) // Reconcile reaches the node meanwhile
		}
		_, err := al.Apply(e)
		return err
	}})
	if err != nil {
		t.Fatal(err)
	}
	al.Attach(n)
	PairAlarms{Engine: eng, Log: al}.RestoreAlarmsSynced(nil, true)
	eng.Evaluate()
	al.mu.Lock()
	al.adopt = map[string]adoption{"HiTemp": {occ: "5656565656565656", activeMs: 1, by: "op"}}
	al.mu.Unlock()
	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := n.Propose(ctx, oplog.Entry{Kind: oplog.KindUnshelve, ID: "LoFlow", By: "op"})
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("propose: %v", err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("a commit applied while Reconcile ran with an adoption pending never finished: the log and AlarmLog deadlocked")
	}
}

// A pending adoption belongs to the leadership that held it: the next
// takeover's restore drops it, so it never acks anything there.
func TestPendingAdoptionClearedAtTheNextTakeover(t *testing.T) {
	var lead atomic.Bool
	lead.Store(true)
	cond := map[string]any{"HiTempAlm": true, "LoFlowAlm": false}
	_, eng, al, _ := loggedAlarms(t, cond, &lead)
	al.mu.Lock()
	al.adopt = map[string]adoption{"HiTemp": {occ: "7878787878787878", activeMs: 1, by: "op"}}
	al.mu.Unlock()
	PairAlarms{Engine: eng, Log: al}.RestoreAlarmsSynced(nil, false) // a new leadership
	eng.Evaluate()
	al.Reconcile()
	if st := state(eng, "HiTemp"); st != alarm.UnackActive {
		t.Fatalf("HiTemp %s; want unack-active (an earlier leadership's adoption is not this one's)", st)
	}
}

// The scan never waits on the pair's alarm log (#1053: control never
// waits on operator data). The engine's hook runs under the engine's lock
// on the scan; with AlarmLog.mu held, or the oplog node's lock held by a
// commit being applied, an evaluation that ends an acknowledged
// occurrence still returns at once, and the ack-clear follows once they
// are released.
func TestScanNeverWaitsOnTheAlarmLog(t *testing.T) {
	var lead atomic.Bool
	lead.Store(true)
	cond := map[string]any{"HiTempAlm": true, "LoFlowAlm": true}
	eng := newTestAlarms(t, cond)
	al := NewAlarmLog(eng, lead.Load, noTerm)
	hold := make(chan struct{})
	held := make(chan struct{})
	n, err := oplog.New(oplog.Options{Self: "a", Apply: func(e oplog.Entry) error {
		if e.Kind == oplog.KindUnshelve && e.By == "hold" {
			close(held)
			<-hold // the node's lock stays held while this commit applies
		}
		_, err := al.Apply(e)
		return err
	}})
	if err != nil {
		t.Fatal(err)
	}
	al.Attach(n)
	PairAlarms{Engine: eng, Log: al}.RestoreAlarmsSynced(nil, true)
	eng.Evaluate()
	for _, id := range []string{"HiTemp", "LoFlow"} {
		occ := occOf(t, eng, id)
		if _, err := n.Propose(context.Background(), oplog.Entry{Kind: oplog.KindAck, IDs: []string{id},
			Occs: []oplog.Occ{{ID: id, Occ: occ.Occurrence, ActiveMs: occ.ActiveMs}}, By: "op"}); err != nil {
			t.Fatal(err)
		}
	}
	scan := func(what string) {
		t.Helper()
		done := make(chan struct{})
		go func() { eng.Evaluate(); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("the scan's evaluation waited on %s", what)
		}
	}

	al.mu.Lock()
	cond["HiTempAlm"] = false // ack-active to normal: its ack is to be cleared
	scan("AlarmLog.mu")
	al.mu.Unlock()

	go func() {
		_, _ = n.Propose(context.Background(), oplog.Entry{Kind: oplog.KindUnshelve, ID: "LoFlow", By: "hold"})
	}()
	<-held
	cond["LoFlowAlm"] = false
	scan("the oplog node's lock")
	close(hold)

	deadline := time.Now().Add(3 * time.Second)
	for al.Held(true, nil)["HiTemp"].Acked || al.Held(true, nil)["LoFlow"].Acked {
		if time.Now().After(deadline) {
			t.Fatalf("the acks were not cleared once released: %+v", al.Held(true, nil))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// An alarm's end handed off from the scan, judged after the view took an
// ack of a later occurrence, does not clear that later ack.
func TestLateHandOffKeepsALaterAck(t *testing.T) {
	var lead atomic.Bool
	lead.Store(true)
	_, _, al, _ := loggedAlarms(t, map[string]any{"HiTempAlm": true, "LoFlowAlm": false}, &lead)
	al.clrRetry = time.Hour
	al.mu.Lock()
	al.view["HiTemp"] = alarmIntent{acked: true, ackBy: "op", occ: "bbbbbbbbbbbbbbbb", activeMs: 2}
	al.mu.Unlock()
	al.clearIfAcked(endedOcc{id: "HiTemp", occ: "aaaaaaaaaaaaaaaa", gen: 1})
	al.clrMu.Lock()
	queued := slices.Clone(al.clr)
	al.clrMu.Unlock()
	if len(queued) != 0 {
		t.Fatalf("an end of occurrence a queued a clear of the ack of b: %v", queued)
	}
	al.clearIfAcked(endedOcc{id: "HiTemp", occ: "bbbbbbbbbbbbbbbb", gen: 1})
	al.clrMu.Lock()
	queued = slices.Clone(al.clr)
	al.clrMu.Unlock()
	if !slices.Equal(queued, []string{"HiTemp"}) {
		t.Fatalf("an end of the acked occurrence queued %v; want HiTemp", queued)
	}

	// While a late ack waits to adopt, the view's ack is for another
	// occurrence than the engine's: the alarm did not stand, so its ack is
	// cleared, and the clear names the view's occurrence (naming the
	// engine's would clear the occurrence the leader records next).
	al.clrMu.Lock()
	al.clr, al.clrOcc = nil, nil
	al.clrMu.Unlock()
	al.mu.Lock()
	al.view["LoFlow"] = alarmIntent{acked: true, ackBy: "op", occ: "cccccccccccccccc", activeMs: 2}
	al.adopt = map[string]adoption{"LoFlow": {occ: "cccccccccccccccc", activeMs: 2, by: "op"}}
	al.mu.Unlock()
	al.clearIfAcked(endedOcc{id: "LoFlow", occ: "dddddddddddddddd", gen: 1})
	al.clrMu.Lock()
	named := al.clrOcc["LoFlow"]
	al.clrMu.Unlock()
	if named != "cccccccccccccccc" {
		t.Fatalf("the clear names %q; want the view's acked occurrence", named)
	}
}

// A superseded log's view goes, and its pending adoptions with it.
func TestDropViewDropsPendingAdoptions(t *testing.T) {
	var lead atomic.Bool
	lead.Store(true)
	_, _, al, _ := loggedAlarms(t, map[string]any{"HiTempAlm": true, "LoFlowAlm": false}, &lead)
	al.mu.Lock()
	al.adopt = map[string]adoption{"HiTemp": {occ: "dededededededede", activeMs: 1, by: "op"}}
	al.mu.Unlock()
	al.dropView()
	al.mu.Lock()
	n := len(al.adopt)
	al.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d pending adoptions outlived the dropped view", n)
	}
}

// An event handed off from the scan before the view took an ack (an
// alarm's activation, judged only after the operator's ack committed)
// never clears that ack: the late judgement goes by when the event fired
// (#1198 CI: TestAlarmAckThroughTheLog saw the ack of the alarm's own
// activation cleared).
func TestHandOffNeverClearsAnAckTakenAfterTheEvent(t *testing.T) {
	var lead atomic.Bool
	lead.Store(true)
	_, _, al, _ := loggedAlarms(t, map[string]any{"HiTempAlm": true, "LoFlowAlm": false}, &lead)
	al.clrRetry = time.Hour
	ev := endedOcc{id: "HiTemp", occ: "efefefefefefefef", gen: al.ackGen.Load()} // the activation fires
	if _, err := al.Apply(oplog.Entry{Kind: oplog.KindAck, IDs: []string{"HiTemp"},
		Occs: []oplog.Occ{{ID: "HiTemp", Occ: "efefefefefefefef", ActiveMs: 1}}, By: "op"}); err != nil {
		t.Fatal(err)
	}
	al.clearIfAcked(ev) // judged after the ack
	al.clrMu.Lock()
	queued := slices.Clone(al.clr)
	al.clrMu.Unlock()
	if len(queued) != 0 {
		t.Fatalf("the activation's hand-off cleared the ack taken after it: %v", queued)
	}
}

// A flood that fills the hand-off still never holds the scan: the
// evaluation returns, the end it could not hand off is dropped, and
// Reconcile, at the next log tick, clears the ack the engine no longer
// holds.
func TestAFullHandOffDropsAndReconcileClears(t *testing.T) {
	var lead atomic.Bool
	lead.Store(true)
	cond := map[string]any{"HiTempAlm": true, "LoFlowAlm": false}
	s, eng, al, _ := loggedAlarms(t, cond, &lead)
	PairAlarms{Engine: eng, Log: al}.RestoreAlarmsSynced(nil, true)
	eng.Evaluate()
	if rec := do(t, s, "POST", "/api/alarms/ack", `{"ids":["HiTemp"],"by":"op"}`); rec.Code != http.StatusOK {
		t.Fatalf("ack: %d %s", rec.Code, rec.Body)
	}
	al.mu.Lock() // the consumer blocks on its next judgement
	for full := false; !full; {
		select {
		case al.ended <- endedOcc{id: "LoFlow"}:
		default:
			full = true
		}
	}
	cond["HiTempAlm"] = false
	done := make(chan struct{})
	go func() { eng.Evaluate(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		al.mu.Unlock()
		t.Fatal("the scan's evaluation waited on a full hand-off")
	}
	al.mu.Unlock()
	al.Reconcile()
	deadline := time.Now().Add(3 * time.Second)
	for al.Held(true, nil)["HiTemp"].Acked {
		if time.Now().After(deadline) {
			t.Fatal("Reconcile did not clear the ack whose end the full hand-off dropped")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
