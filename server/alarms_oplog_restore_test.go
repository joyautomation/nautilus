package server

import (
	"context"
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

// noTerm is the term of a log with no takeovers (these tests' leaders):
// never restored (0) equals it, so the log is live as soon as it leads.
func noTerm() uint64 { return 0 }

// #1033 CI: a replica that holds the lease in a new term, but whose
// takeover has not restored the alarm view yet (it waits for inputs and
// the log's catch-up), judges nothing from its engine: Reconcile clears no
// carried ack, an ack is refused (503, retry) rather than checked against
// a standby's occurrences, and a committed ack only reaches the view. Once
// the takeover restores, the ack is on the engine and stays.
func TestAlarmLogWaitsForTheRestore(t *testing.T) {
	var lead atomic.Bool
	var term atomic.Uint64
	eng := newTestAlarms(t, map[string]any{"HiTempAlm": true, "LoFlowAlm": false})
	al := NewAlarmLog(eng, lead.Load, term.Load)
	var mu sync.Mutex
	var got []oplog.Entry
	n, err := oplog.New(oplog.Options{Self: "b", Apply: func(e oplog.Entry) error {
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
	s := New(newTestRuntime(t), Options{Alarms: eng, AlarmLog: al})
	clears := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(slices.DeleteFunc(slices.Clone(got), func(e oplog.Entry) bool { return e.Kind != oplog.KindAckClear }))
	}

	// Standby: HiTemp is active (this engine saw it before), and the
	// leader's ack of it commits; a standby records it in the view only.
	eng.Evaluate()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := n.Propose(ctx, oplog.Entry{Kind: oplog.KindAck, IDs: []string{"HiTemp"}, By: "op"}); err != nil {
		t.Fatal(err)
	}
	if !al.Held(true, nil)["HiTemp"].Acked || state(eng, "HiTemp") != alarm.UnackActive {
		t.Fatalf("standby: view acked %v, engine %s; want the ack in the view only", al.Held(true, nil)["HiTemp"].Acked, state(eng, "HiTemp"))
	}

	// The lease is won in term 2; the takeover is still waiting.
	term.Store(2)
	lead.Store(true)
	for range 3 {
		al.Reconcile() // driveLog's ticks meanwhile
	}
	rec := do(t, s, "POST", "/api/alarms/ack", `{"ids":["HiTemp"],"by":"op2"}`)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), PairAlarmsTakingOver) || rec.Header().Get("Retry-After") == "" {
		t.Errorf("an ack before the restore: %d %s; want 503 %q with Retry-After", rec.Code, rec.Body, PairAlarmsTakingOver)
	}
	time.Sleep(300 * time.Millisecond) // a background ack-clear would have committed by now
	if c := clears(); c != 0 || !al.Held(true, nil)["HiTemp"].Acked {
		t.Fatalf("before the restore: %d ack-clears proposed, view acked %v; want the carried ack untouched", c, al.Held(true, nil)["HiTemp"].Acked)
	}

	// The takeover restores the view: the ack is on the engine, and the
	// log is live from here.
	PairAlarms{Engine: eng, Log: al}.RestoreAlarmsSynced(nil, true)
	eng.Evaluate()
	if st := state(eng, "HiTemp"); st != alarm.AckActive {
		t.Fatalf("after the restore HiTemp is %s; want the carried ack", st)
	}
	al.Reconcile()
	time.Sleep(300 * time.Millisecond)
	if c := clears(); c != 0 {
		t.Fatalf("after the restore: %d ack-clears for an ack the engine holds", c)
	}
	if rec := do(t, s, "POST", "/api/alarms/ack", `{"ids":["HiTemp"],"by":"op2"}`); rec.Code == http.StatusServiceUnavailable {
		t.Fatalf("a live leader refused an ack as still taking over: %s", rec.Body)
	}
}

// A committed ack that lands while the takeover is waiting goes to the
// view only (the engine is not the leader's yet), and the restore puts it
// on the engine.
func TestAlarmAckCommittedDuringTheWaitIsRestored(t *testing.T) {
	var lead atomic.Bool
	var term atomic.Uint64
	eng := newTestAlarms(t, map[string]any{"HiTempAlm": true, "LoFlowAlm": false})
	al := NewAlarmLog(eng, lead.Load, term.Load)
	n, err := oplog.New(oplog.Options{Self: "b", Apply: func(e oplog.Entry) error { _, err := al.Apply(e); return err }})
	if err != nil {
		t.Fatal(err)
	}
	al.Attach(n)
	eng.Evaluate()
	term.Store(3)
	lead.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := n.Propose(ctx, oplog.Entry{Kind: oplog.KindAck, IDs: []string{"HiTemp"}, By: "op"}); err != nil {
		t.Fatal(err)
	}
	if st := state(eng, "HiTemp"); st != alarm.UnackActive {
		t.Fatalf("an ack committed before the restore reached the engine (%s): the restore resets it anyway", st)
	}
	PairAlarms{Engine: eng, Log: al}.RestoreAlarmsSynced(nil, true)
	eng.Evaluate() // the takeover's first scan
	if st := state(eng, "HiTemp"); st != alarm.AckActive {
		t.Fatalf("after the restore HiTemp is %s; want the ack committed during the wait", st)
	}
}
