package project

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/alarm"
	"github.com/joyautomation/nautilus/runtime"
)

// stepClock is a stopped clock for the runtime (runtime.Clock).
type stepClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *stepClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *stepClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// staleRole is a coordinator whose cached role says it leads: what a
// standby's own view can say after it lost the lease and before it has
// noticed. The write gate (Project.WriteGate) is the lease itself.
type staleRole struct{}

func (staleRole) IsLeader() bool { return true }
func (staleRole) Term() uint64   { return 7 }

const escalationManifest = alarmTags + `
alarms:
  defs:
    - { id: intrusion, tag: RTU9_ZONE15_INT_001_YA, area: zone15, priority: high }
  oncall:
    rosters:
      - { id: ops, members: [alice] }
  escalation:
    - id: zone15
      match: { area: zone15 }
      levels:
        - { after: 0s, notify: [roster:ops], via: [log] }
`

func notifyRows(t *testing.T, eng *alarm.Engine) []alarm.Event {
	t.Helper()
	evs, err := eng.Journal(time.Time{}, time.Time{}, alarm.Filter{Kinds: []string{alarm.KindNotify}})
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

// naut run's escalation is gated by the write gate itself, asked before
// each send, not by the runtime's view of its role: a standby whose role
// is stale (its coordinator still says leader, so it scans and evaluates)
// but whose lease is gone pages nobody.
func TestEscalationStaleRoleSendsNothing(t *testing.T) {
	proj, err := Load(alarmFS(escalationManifest, nil), "")
	if err != nil {
		t.Fatal(err)
	}
	clk := &stepClock{t: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)}
	var lease atomic.Bool
	proj.Runtime.Clock = clk
	proj.Runtime.Coordinator = staleRole{}
	proj.WriteGate = lease.Load
	_, rt := loadAlarmsWith(t, proj)
	a, err := proj.NewAlarms(rt)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if a.Escalation == nil {
		t.Fatal("naut run built no escalation dispatcher")
	}

	rt.Tags().SetPath("RTU9_ZONE15_INT_001_YA", true)
	for range 5 {
		clk.add(time.Second)
		rt.Scan()
		a.Escalation.Drain()
	}
	if s := a.Engine.Summary().Unacked; s != 1 {
		t.Fatalf("the alarm never came up (unacked %d): the test proves nothing", s)
	}
	if rows := notifyRows(t, a.Engine); len(rows) != 0 {
		t.Fatalf("a controller without the lease paged: %+v", rows)
	}

	// The control: with the lease, the same scans page alice.
	lease.Store(true)
	clk.add(time.Second)
	rt.Scan()
	a.Escalation.Drain()
	rows := notifyRows(t, a.Engine)
	if len(rows) != 1 || rows[0].To != "alice" || rows[0].Via != "log" || rows[0].Result != alarm.NotifySent {
		t.Fatalf("with the lease: %+v, want one sent row to alice by log", rows)
	}
}

// The acceptance harness and nautilus test never page anyone.
func TestEscalationOnlyUnderNautRun(t *testing.T) {
	proj, rt := loadAlarms(t, alarmFS(escalationManifest, nil))
	eng, err := proj.AlarmEngine(rt)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	rt.Tags().SetPath("RTU9_ZONE15_INT_001_YA", true)
	rt.Scan()
	if rows := notifyRows(t, eng); len(rows) != 0 {
		t.Fatalf("a test engine paged: %+v", rows)
	}
	// The send is asynchronous, so the journal alone could miss one: the
	// non-durable build must have no dispatcher at all.
	a, err := proj.alarms(rt, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if a.Escalation != nil {
		t.Fatal("the non-durable (test) alarm build has an escalation dispatcher")
	}
}

// A level naming a channel this release does not have fails at load, and
// the message names the key and what there is.
func TestEscalationUnknownChannelFailsAtLoad(t *testing.T) {
	_, err := Load(alarmFS(strings.Replace(escalationManifest, "via: [log]", "via: [sms]", 1), nil), "")
	if err == nil || !strings.Contains(err.Error(), `alarms.escalation[0] (zone15).levels[0].via[0]: no channel "sms"`) ||
		!strings.Contains(err.Error(), "have: log") {
		t.Fatalf("err %v", err)
	}
}

// naut run declares the failing-channel system alarm beside the
// manifest's (escalate's own tests drive Failing).
func TestEscalationDeclaresSystemAlarm(t *testing.T) {
	proj, err := Load(alarmFS(escalationManifest, nil), "")
	if err != nil {
		t.Fatal(err)
	}
	_, rt := loadAlarmsWith(t, proj)
	a, err := proj.NewAlarms(rt)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	found := false
	for _, d := range a.Defs {
		if d.ID == EscalationAlarmID {
			found = true
		}
	}
	if !found {
		t.Fatalf("no %s system alarm among %d definitions", EscalationAlarmID, len(a.Defs))
	}
}

func loadAlarmsWith(t *testing.T, proj *Project) (*Project, *runtime.Runtime) {
	t.Helper()
	rt, err := runtime.New(proj.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	return proj, rt
}

// untermed is a coordinator that names no leadership term.
type untermed struct{}

func (untermed) IsLeader() bool { return true }

// A write gate whose elector names no term is refused: a page queued under
// one leader could otherwise leave under the next.
func TestEscalationNeedsATermedElector(t *testing.T) {
	proj, err := Load(alarmFS(escalationManifest, nil), "")
	if err != nil {
		t.Fatal(err)
	}
	proj.Runtime.Coordinator = untermed{}
	proj.WriteGate = func() bool { return true }
	_, rt := loadAlarmsWith(t, proj)
	if _, err := proj.NewAlarms(rt); err == nil || !strings.Contains(err.Error(), "termed elector") {
		t.Fatalf("NewAlarms with an untermed gate: %v", err)
	}
}
