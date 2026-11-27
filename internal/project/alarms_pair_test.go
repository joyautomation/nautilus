package project

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/alarm"
	"github.com/joyautomation/nautilus/alarm/channel"
	"github.com/joyautomation/nautilus/alarm/channel/channeltest"
	"github.com/joyautomation/nautilus/alarm/escalate"
)

const pairAlarmManifest = alarmTags + `
alarms:
  defs:
    - { id: intrusion, tag: RTU9_ZONE15_INT_001_YA, area: zone15 }
  channels:
    email: { smtp: "127.0.0.1:1", from: alarms@example.com }
  oncall:
    rosters:
      - { id: ops, members: [alice] }
  escalation:
    - id: system
      match: { class: system }
      levels:
        - { after: 0s, notify: [roster:ops], via: [email] }
`

// A pair member's no-leader alarm (#1436) is raised on an engine of its
// own, on its own timer, never the scan's: it pages through the
// controller's channels while its condition holds, with no scan at all,
// carries no ack link or reply code (nobody can acknowledge it: it ends
// when a leader is seen), is not acknowledgeable whatever its Def says,
// stays out of the scan's engine, and reports its sends failing so the
// member's hello stops vouching for it.
func TestPairAlarmPagesOnItsOwnEngine(t *testing.T) {
	proj, rt := loadAlarms(t, alarmFS(pairAlarmManifest, nil))
	proj.AlarmDirectory = &channeltest.Dir{Addrs: map[string]map[string]string{"alice": {"email": "a@example.com"}}}
	proj.AckLink = func(escalate.Message, string) string { return "https://hmi.example/ack" }
	proj.AckCode = func(escalate.Message, string) string { return "1234" }
	sent := make(chan escalate.Message, 16)
	var fail atomic.Bool
	withSeams(t, nil, func(*channel.Config, channel.Directory, func(context.Context, escalate.Message) error) (map[string]escalate.Channel, error) {
		return map[string]escalate.Channel{"email": chanFunc(func(_ context.Context, m escalate.Message) error {
			sent <- m
			if fail.Load() {
				return errors.New("refused")
			}
			return nil
		})}, nil
	})
	old := PairAlarmEvery
	PairAlarmEvery = 10 * time.Millisecond
	t.Cleanup(func() { PairAlarmEvery = old })
	var up atomic.Bool
	var ep atomic.Uint64
	const id = "nautilus.pair-leaderless"
	proj.PairAlarm = &PairAlarm{
		Def:    alarm.Def{ID: id, Tag: "__nautilus.pair-leaderless", Name: "no leader", Priority: alarm.Critical, Class: "system", AutoClear: true, AckRequired: true},
		Active: up.Load, Episode: ep.Load,
	}
	a, err := proj.NewAlarms(rt)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if a.Pair == nil {
		t.Fatal("no no-leader alarm engine")
	}
	if _, ok := a.Engine.Lookup(id); ok {
		t.Fatal("the no-leader alarm is in the scan's engine, which a pair with no leader never evaluates")
	}
	time.Sleep(50 * time.Millisecond)
	if len(sent) != 0 {
		t.Fatalf("%d pages with the condition clear", len(sent))
	}

	page := func() escalate.Message {
		t.Helper()
		select {
		case m := <-sent:
			return m
		case <-time.After(5 * time.Second):
			t.Fatal("no page")
		}
		return escalate.Message{}
	}
	// Raised, with no scan at all.
	ep.Store(1)
	up.Store(true)
	m := page()
	if m.Alarm.ID != id || m.To != "alice" {
		t.Fatalf("paged %+v, want %s to alice", m, id)
	}
	if m.AckURL != "" || m.AckCode != "" {
		t.Fatalf("the page offers an acknowledgement (link %q, code %q) nobody can make", m.AckURL, m.AckCode)
	}
	if r, ok := a.Pair.Engine.Lookup(id); !ok || r.AckRequired {
		t.Fatalf("the alarm's record %+v: want one that is not acknowledged", r)
	}
	up.Store(false)
	waitPair(t, func() bool { r, _ := a.Pair.Engine.Lookup(id); return !r.Cond })
	if a.Pair.Failing() {
		t.Fatal("failing after a page that went")
	}

	// One page refused makes it failing (one page a spell: never three in
	// a row), and one that goes clears it.
	fail.Store(true)
	ep.Store(2)
	up.Store(true)
	page()
	a.Pair.Escalation.Drain()
	if !a.Pair.Failing() {
		t.Fatal("its page was refused and it does not read as failing: its hello would keep the peer from paging")
	}
	up.Store(false)
	waitPair(t, func() bool { r, _ := a.Pair.Engine.Lookup(id); return !r.Cond })
	if !a.Pair.Failing() {
		t.Fatal("the failure was forgotten when the spell ended")
	}
	for len(sent) > 0 {
		<-sent // the refused page's retries
	}
	fail.Store(false)
	ep.Store(3)
	up.Store(true)
	page()
	a.Pair.Escalation.Drain()
	if a.Pair.Failing() {
		t.Fatal("still failing after a page went")
	}
}

func waitPair(t *testing.T, ok func() bool) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatal("timed out")
}
