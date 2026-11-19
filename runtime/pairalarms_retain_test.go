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
