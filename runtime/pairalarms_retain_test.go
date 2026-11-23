package runtime

import (
	"sync"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/retain"
)

// recPairAlarms is a pair's alarm retainer that records the restores a
// takeover makes.
type recPairAlarms struct {
	mu       sync.Mutex
	restores []bool // synced, per restore
}

func (p *recPairAlarms) RetainedAlarms() map[string]retain.AlarmRetain { return nil }
func (p *recPairAlarms) RestoreAlarms(m map[string]retain.AlarmRetain) {
	p.RestoreAlarmsSynced(m, false)
}
func (p *recPairAlarms) RestoreAlarmsSynced(_ map[string]retain.AlarmRetain, synced bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.restores = append(p.restores, synced)
}

func (p *recPairAlarms) got() []bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]bool(nil), p.restores...)
}

// #1033 CI: a pair's takeover restores the log's alarm view even when its
// retain store can't be read. Without it the engine stays frozen at an
// earlier leadership, and the alarm log, live only once a takeover has
// restored it, never counts it live.
func TestPairTakeoverRestoresAlarmsWhenTheStoreFails(t *testing.T) {
	clk := noon()
	coord := lead(false)
	rt, _ := newRetainSubRig(t, coord, &fakeStore{fail: true}, clk)
	pa := &recPairAlarms{}
	rt.SetAlarms(pa)
	rt.SetIntentLog(&fakeLog{}, time.Second)
	rt.Scan() // standby
	coord.set(true)
	scanUntilLogic(t, rt)
	if got := pa.got(); len(got) != 1 || !got[0] {
		t.Fatalf("restores at the takeover: %v; want one synced restore from the log", got)
	}
}

// pairFloodAlarms is a pair's alarm retainer that could also carry a
// flood: the runtime must never hand it the store's, since a pair's log
// carries its own (#1249).
type pairFloodAlarms struct {
	recPairAlarms
	floods int
}

func (p *pairFloodAlarms) RetainedFlood() *retain.AlarmFlood { return nil }
func (p *pairFloodAlarms) RestoreFlood(*retain.AlarmFlood)   { p.floods++ }

// A pair's takeover restores its alarms from the log alone: the retain
// store's flood slot is never a second source.
func TestPairTakeoverNeverRestoresTheStoresFlood(t *testing.T) {
	clk := noon()
	coord := lead(false)
	store := &fakeStore{state: retain.State{AlarmFlood: &retain.AlarmFlood{ID: "flood-1", SinceMs: 1, OverMs: 1}}}
	rt, _ := newRetainSubRig(t, coord, store, clk)
	pa := &pairFloodAlarms{}
	rt.SetAlarms(pa)
	rt.SetIntentLog(&fakeLog{}, time.Second)
	rt.Scan()
	coord.set(true)
	scanUntilLogic(t, rt)
	if got := pa.got(); len(got) != 1 || pa.floods != 0 {
		t.Fatalf("restores %v, flood restores %d; want one alarm restore and no flood from the store", got, pa.floods)
	}
}
