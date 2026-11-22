package escalate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/alarm"
	"gopkg.in/yaml.v3"
)

// ---- config, rosters, windows ----

func prepare(t *testing.T, src string, channels ...string) *Config {
	t.Helper()
	var w struct {
		Oncall     Oncall   `yaml:"oncall"`
		Escalation []Policy `yaml:"escalation"`
	}
	if err := yaml.Unmarshal([]byte(src), &w); err != nil {
		t.Fatalf("yaml: %v", err)
	}
	if len(channels) == 0 {
		channels = []string{"email", "sms", "log"}
	}
	c := &Config{Oncall: w.Oncall, Policies: w.Escalation}
	if err := c.Prepare(channels); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	return c
}

func prepareErr(src string) error {
	var w struct {
		Oncall     Oncall   `yaml:"oncall"`
		Escalation []Policy `yaml:"escalation"`
	}
	if err := yaml.Unmarshal([]byte(src), &w); err != nil {
		return err
	}
	c := &Config{Oncall: w.Oncall, Policies: w.Escalation}
	return c.Prepare([]string{"email", "sms"})
}

func chicago(t *testing.T, s string) time.Time {
	t.Helper()
	loc, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Fatal(err)
	}
	v, err := time.ParseInLocation("2006-01-02 15:04", s, loc)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestRosterRotation(t *testing.T) {
	c := prepare(t, `
oncall:
  timezone: America/Chicago
  rosters:
    - id: all
      members: [alice, dave]
    - id: day
      members: [alice, bob, carol]
      rotate: daily
      start: 2026-11-02
      handover: "07:00"
    - id: week
      members: [alice, bob]
      rotate: weekly
      start: 2026-11-02
      handover: "07:00"
`)
	loc := c.Oncall.Location()
	cases := []struct {
		roster, at string
		want       []string
	}{
		{"all", "2026-11-05 03:00", []string{"alice", "dave"}},
		{"day", "2026-11-02 07:00", []string{"alice"}},
		{"day", "2026-11-02 06:59", []string{"carol"}}, // before the first handover: the turn before
		{"day", "2026-11-03 06:59", []string{"alice"}},
		{"day", "2026-11-03 07:00", []string{"bob"}},
		{"day", "2026-11-04 12:00", []string{"carol"}},
		{"day", "2026-11-05 12:00", []string{"alice"}},
		{"day", "2026-10-31 12:00", []string{"bob"}}, // before start: the rotation runs backwards too
		{"week", "2026-11-09 06:59", []string{"alice"}},
		{"week", "2026-11-09 07:00", []string{"bob"}},
		{"week", "2026-11-16 07:00", []string{"alice"}},
		// Daylight saving ends 2026-11-01 and begins 2027-03-14: the
		// handover stays at 07:00 local on both sides.
		{"day", "2026-11-01 07:00", []string{"carol"}},
		{"day", "2026-11-01 06:59", []string{"bob"}},
		{"day", "2027-03-14 06:59", []string{"carol"}},
		{"day", "2027-03-14 07:00", []string{"alice"}},
	}
	for _, tc := range cases {
		got := c.Oncall.Roster(tc.roster).OnCall(chicago(t, tc.at), loc)
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s at %s: on call %v, want %v", tc.roster, tc.at, got, tc.want)
		}
	}
}

func TestRosterShifts(t *testing.T) {
	c := prepare(t, `
oncall:
  timezone: America/Chicago
  rosters:
    - id: crew
      shifts:
        - { days: [mon-fri], from: "06:00", to: "18:00", members: [alice, bob] }
        - { days: [mon-fri], from: "18:00", to: "06:00", members: [carol] }
        - { days: [sat-sun], from: "00:00", to: "00:00", members: [dave] }
`)
	loc := c.Oncall.Location()
	r := c.Oncall.Roster("crew")
	cases := []struct {
		at   string
		want []string
	}{
		{"2026-11-20 06:00", []string{"alice", "bob"}}, // Friday
		{"2026-11-20 17:59", []string{"alice", "bob"}},
		{"2026-11-20 18:00", []string{"carol"}},
		{"2026-11-21 05:59", []string{"carol", "dave"}}, // Saturday morning: Friday's night shift runs on
		{"2026-11-21 06:00", []string{"dave"}},
		{"2026-11-23 05:59", []string{}}, // Monday before day shift: Sunday has no night shift
		{"2026-11-23 06:00", []string{"alice", "bob"}},
	}
	for _, tc := range cases {
		got := r.OnCall(chicago(t, tc.at), loc)
		if !slices.Equal(got, tc.want) && !(len(got) == 0 && len(tc.want) == 0) {
			t.Errorf("at %s: on call %v, want %v", tc.at, got, tc.want)
		}
	}
}

func TestPrepareErrors(t *testing.T) {
	cases := []struct{ src, want string }{
		{`oncall: {timezone: Mars/Olympus}`, "alarms.oncall.timezone"},
		{`oncall: {rosters: [{id: a}]}`, "needs members or shifts"},
		{`oncall: {rosters: [{id: a, members: [x], rotate: daily}]}`, "rosters[0] (a).start"},
		{`oncall: {rosters: [{id: a, members: [x], rotate: hourly, start: 2026-01-01}]}`, "is not daily or weekly"},
		{`oncall: {rosters: [{id: a, members: ["bad name"]}]}`, "members[0]"},
		{`oncall: {rosters: [{id: a, members: [x]}, {id: a, members: [y]}]}`, "used twice"},
		{`oncall: {rosters: [{id: a, shifts: [{days: [funday], from: "06:00", to: "07:00", members: [x]}]}]}`, "days[0]"},
		{`escalation: [{id: p, levels: []}]`, "at least one level"},
		{`escalation: [{id: p, levels: [{after: 0s, notify: [roster:nope], via: [email]}]}]`, `no roster "nope"`},
		{`escalation: [{id: p, levels: [{after: 0s, notify: [alice], via: [pager]}]}]`, `no channel "pager"`},
		{`escalation: [{id: p, levels: [{after: 5m, notify: [alice], via: [email]}, {after: 1m, notify: [bob], via: [sms]}]}]`, "levels go in order"},
		{`escalation: [{id: p, repeat: {times: 2}, levels: [{after: 0s, notify: [alice], via: [email]}]}]`, "repeat.every"},
		{`escalation: [{id: p, stop: never, levels: [{after: 0s, notify: [alice], via: [email]}]}]`, "stop"},
		{`escalation: [{id: p, match: {priority: [urgent]}, levels: [{after: 0s, notify: [alice], via: [email]}]}]`, "match.priority[0]"},
		{`escalation: [{id: p, levels: [{after: 0s, notify: ["svc:x"], via: [email]}]}]`, "not a person"},
		{`escalation: [{levels: [{after: 0s, notify: [alice], via: [email]}]}]`, "id is required"},
		{`oncall: {ack-url: "http://hmi.example"}`, "plaintext"},
		{`oncall: {ack-url: "ftp://hmi.example"}`, "must be https"},
		{`oncall: {ack-url: "https://u:p@hmi.example"}`, "no user"},
		{`oncall: {ack-url: "https://hmi.example/?a=b"}`, "no query"},
		{`oncall: {ack-url: "hmi.example"}`, "plain URL"},
		{`oncall: {ack-url-insecure: true}`, "without an ack-url"},
		{`oncall: {ack-url: "https://hmi.example", ack-url-insecure: true}`, "only for an http"},
		{`oncall: {ack-url: "https://hmi.example", ack-from-notification: email}`, "ack-from-notification"},
	}
	for _, tc := range cases {
		err := prepareErr(tc.src)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s:\n  err %v, want it to mention %q", tc.src, err, tc.want)
		}
	}
}

func TestSelectAndSteps(t *testing.T) {
	c := prepare(t, `
oncall:
  timezone: America/Chicago
escalation:
  - id: night
    match: { priority: [high, critical], area: "roast*" }
    when: { from: "18:00", to: "06:00" }
    levels: [{after: 0s, notify: [alice], via: [sms]}]
  - id: roast
    match: { area: "ROAST*" }
    levels:
      - {after: 0s, notify: [alice], via: [email]}
      - {after: 10m, notify: [bob], via: [sms]}
    repeat: { every: 15m, times: 2 }
  - id: everything
    levels: [{after: 30m, notify: [carol], via: [email]}]
`)
	high := alarm.Def{ID: "TT1", Area: "roast2", Priority: alarm.High}
	low := alarm.Def{ID: "TT2", Area: "roast2", Priority: alarm.Low}
	other := alarm.Def{ID: "LT1", Area: "loadout", Priority: alarm.High}
	pick := func(d alarm.Def, at string) string {
		if p := c.Select(d, chicago(t, at)); p != nil {
			return p.ID
		}
		return ""
	}
	for _, tc := range []struct {
		d        alarm.Def
		at, want string
	}{
		{high, "2026-11-20 22:00", "night"},
		{high, "2026-11-20 12:00", "roast"}, // outside the night window: the next policy
		{low, "2026-11-20 22:00", "roast"},
		{other, "2026-11-20 22:00", "everything"},
	} {
		if got := pick(tc.d, tc.at); got != tc.want {
			t.Errorf("%s at %s: policy %q, want %q", tc.d.ID, tc.at, got, tc.want)
		}
	}
	want := []Step{{0, 0, 0}, {1, 0, 10 * time.Minute}, {1, 1, 25 * time.Minute}, {1, 2, 40 * time.Minute}}
	if got := c.Policies[1].Steps(); !slices.Equal(got, want) {
		t.Errorf("steps %v, want %v", got, want)
	}
}

func TestEscalating(t *testing.T) {
	for _, tc := range []struct {
		s    alarm.State
		stop string
		want bool
	}{
		{alarm.UnackActive, StopAck, true},
		{alarm.UnackRTN, StopAck, true},
		{alarm.UnackRTN, StopAckOrRTN, false},
		{alarm.UnackActive, StopAckOrRTN, true},
		{alarm.AckActive, StopAck, false},
		{alarm.Shelved, StopAck, false},
		{alarm.Suppressed, StopAck, false},
		{alarm.Normal, StopAck, false},
	} {
		if got := Escalating(tc.s, tc.stop); got != tc.want {
			t.Errorf("Escalating(%s, %s) = %v", tc.s, tc.stop, got)
		}
	}
}

// ---- the dispatcher, over a real engine on a stopped clock ----

type rig struct {
	t *testing.T
	// now is the test goroutine's clock; advance moves it. The engine
	// reads clock instead, because Engine.Notified runs on a channel's
	// sender goroutine while the test advances time.
	now    time.Time
	clock  atomic.Pointer[time.Time]
	tags   map[string]any
	eng    *alarm.Engine
	ring   *alarm.RingJournal
	d      *Dispatcher
	email  *Recorder
	sms    *Recorder
	gate   atomic.Bool
	term   atomic.Uint64
	mu     sync.Mutex
	notify []alarm.Event // what the engine's Notifiers heard
}

const policy = `
oncall:
  timezone: America/Chicago
  rosters:
    - id: operators
      members: [alice, dave]
    - id: maintenance
      members: [bob, erin]
      rotate: daily
      start: 2026-11-02
      handover: "07:00"
escalation:
  - id: roast
    match: { area: roast }
    levels:
      - {after: 0s,  notify: [roster:operators], via: [email]}
      - {after: 10m, notify: [roster:maintenance], via: [sms]}
      - {after: 30m, notify: [carol], via: [sms, email]}
  - id: rtn
    match: { area: dryer }
    stop: ack-or-rtn
    levels:
      - {after: 0s,  notify: [alice], via: [email]}
      - {after: 5m,  notify: [bob], via: [sms]}
`

// newRig starts at 06:00 Chicago on a Friday; maintenance's turn is erin's
// until 07:00, bob's after.
func newRig(t *testing.T, gated bool, defs ...alarm.Def) *rig {
	t.Helper()
	return newRigWith(t, gated, nil, defs...)
}

// newRigWith is newRig with more dispatcher options (opt changes them).
func newRigWith(t *testing.T, gated bool, opt func(*Options), defs ...alarm.Def) *rig {
	t.Helper()
	r := &rig{t: t, tags: map[string]any{}}
	r.setNow(chicago(t, "2026-11-20 06:00"))
	if len(defs) == 0 {
		defs = []alarm.Def{
			{ID: "TT1", Tag: "TT1", Name: "Roaster temp high", Area: "roast", Priority: alarm.High, AckRequired: true, AutoClear: true, Shelvable: true},
			{ID: "DR1", Tag: "DR1", Name: "Dryer fault", Area: "dryer", Priority: alarm.Medium, AckRequired: true, AutoClear: true, Shelvable: true},
			{ID: "LT1", Tag: "LT1", Name: "Loadout level", Area: "loadout", Priority: alarm.Low, AckRequired: true, AutoClear: true},
		}
	}
	for _, d := range defs {
		r.tags[d.Tag] = false
	}
	r.ring = alarm.NewRing(1000)
	var err error
	r.eng, err = alarm.New(alarm.Options{Defs: defs, Now: func() time.Time { return *r.clock.Load() },
		Read: func(p string) (any, bool) { v, ok := r.tags[p]; return v, ok }, Journal: r.ring})
	if err != nil {
		t.Fatal(err)
	}
	r.eng.OnEvent(func(e alarm.Event) {
		r.mu.Lock()
		r.notify = append(r.notify, e)
		r.mu.Unlock()
	})
	t.Cleanup(func() { r.eng.Close() })
	r.email, r.sms = &Recorder{}, &Recorder{}
	r.gate.Store(true)
	r.term.Store(1)
	o := Options{Config: prepare(t, policy), Engine: r.eng,
		Channels: map[string]Channel{"email": r.email, "sms": r.sms, "log": LogChannel{}}}
	if gated {
		o.Gate, o.Term = r.gate.Load, r.term.Load
	}
	if opt != nil {
		opt(&o)
	}
	r.d, err = New(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.d.Close)
	r.eng.Evaluate()
	r.d.Tick(r.now)
	return r
}

// run advances the clock a second at a time, scanning and looking each
// second, as naut run does.
func (r *rig) run(d time.Duration) {
	for end := r.now.Add(d); r.now.Before(end); {
		r.advance(time.Second)
		r.eng.Evaluate()
		r.d.Tick(r.now)
		r.d.Drain()
	}
}

func (r *rig) set(tag string, v bool) { r.tags[tag] = v }

// setNow and advance move the rig's clock: the test goroutine's copy and
// the one the engine reads.
func (r *rig) setNow(t time.Time) {
	r.now = t
	r.clock.Store(&t)
}

func (r *rig) advance(d time.Duration) { r.setNow(r.now.Add(d)) }

// sent lists what reached the channels as "level:to:via", in order per
// channel kind (email first, then sms).
func (r *rig) sent() []string {
	var out []string
	for _, x := range []struct {
		via string
		rec *Recorder
	}{{"email", r.email}, {"sms", r.sms}} {
		for _, m := range x.rec.Messages() {
			out = append(out, fmt.Sprintf("%s:%d:%s:%s", m.Alarm.ID, m.Level, m.To, x.via))
		}
	}
	return out
}

// notified is the journal's notify rows, oldest first, as
// "id:level:to:via:result".
func (r *rig) notified() []string {
	evs, err := r.ring.Query(time.Time{}, time.Time{}, alarm.Filter{Kinds: []string{alarm.KindNotify}})
	if err != nil {
		r.t.Fatal(err)
	}
	var out []string
	for i := len(evs) - 1; i >= 0; i-- {
		e := evs[i]
		out = append(out, fmt.Sprintf("%s:%d:%s:%s:%s", e.ID, e.Level, e.To, e.Via, e.Result))
	}
	return out
}

func sameSet(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

func (r *rig) wantSent(want ...string) {
	r.t.Helper()
	if got := r.sent(); !sameSet(got, want) {
		r.t.Fatalf("sent %v\n want %v", got, want)
	}
}

// The issue's done-when, with fakes: two levels on two channels, and the
// acknowledgement stops the third.
func TestTwoLevelsTwoChannelsAckStops(t *testing.T) {
	r := newRig(t, false)
	r.set("TT1", true)
	r.run(time.Second)
	r.wantSent("TT1:1:alice:email", "TT1:1:dave:email")

	r.run(10 * time.Minute)
	// 06:10: maintenance is erin's until 07:00.
	r.wantSent("TT1:1:alice:email", "TT1:1:dave:email", "TT1:2:erin:sms")

	if n, err := r.eng.Ack([]string{"TT1"}, "erin"); err != nil || n != 1 {
		t.Fatalf("ack: %d %v", n, err)
	}
	r.run(40 * time.Minute)
	r.wantSent("TT1:1:alice:email", "TT1:1:dave:email", "TT1:2:erin:sms")

	got := r.notified()
	want := []string{"TT1:1:alice:email:sent", "TT1:1:dave:email:sent", "TT1:2:erin:sms:sent"}
	if !sameSet(got, want) {
		t.Fatalf("journal %v, want %v", got, want)
	}
	// A notification is journalled, never handed to the Notifiers or the
	// pair's OnEvent hook.
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.notify {
		if e.Kind == alarm.KindNotify {
			t.Fatalf("OnEvent heard a notify row: %+v", e)
		}
	}
}

// Unacknowledged, it climbs every level and repeats the last.
func TestClimbsAndRepeats(t *testing.T) {
	r := newRig(t, false)
	r.set("TT1", true)
	r.run(31 * time.Minute)
	r.wantSent("TT1:1:alice:email", "TT1:1:dave:email", "TT1:2:erin:sms", "TT1:3:carol:sms", "TT1:3:carol:email")
}

// A latched alarm that flaps between unack-active and unack-rtn is one
// wait: the clock is not restarted by each activation.
func TestFlappingKeepsClimbing(t *testing.T) {
	r := newRig(t, false)
	for i := 0; i < 6; i++ { // 6 × 2 min: on a minute, off a minute
		r.set("TT1", true)
		r.run(time.Minute)
		r.set("TT1", false)
		r.run(time.Minute)
	}
	got := r.sent()
	if !slices.Contains(got, "TT1:2:erin:sms") {
		t.Fatalf("a flapping alarm never reached level 2: %v", got)
	}
	if n := strings.Count(strings.Join(got, " "), "TT1:1:alice"); n != 1 {
		t.Fatalf("level 1 sent %d times to alice, want once: %v", n, got)
	}
}

// Acknowledged and back between two looks is a new wait: it starts at
// level 1 again rather than carrying on the old clock.
func TestAckAndBackIsANewWait(t *testing.T) {
	r := newRig(t, false)
	r.set("TT1", true)
	r.run(9 * time.Minute)
	// Ack, return to normal and come back within one scan, no look between.
	r.advance(100 * time.Millisecond)
	r.eng.Ack([]string{"TT1"}, "alice")
	r.set("TT1", false)
	r.eng.Evaluate()
	r.set("TT1", true)
	r.eng.Evaluate()
	r.run(2 * time.Minute) // 11 min after the first activation, 2 after the second
	got := r.sent()
	if slices.Contains(got, "TT1:2:erin:sms") {
		t.Fatalf("the old wait's clock carried over the ack: %v", got)
	}
	if n := strings.Count(strings.Join(got, " "), "TT1:1:alice"); n != 2 {
		t.Fatalf("level 1 to alice %d times, want twice (once per wait): %v", n, got)
	}
}

// stop: ack-or-rtn ends the escalation at a return to normal.
func TestStopOnRTN(t *testing.T) {
	r := newRig(t, false)
	r.set("DR1", true)
	r.run(time.Minute)
	r.set("DR1", false)
	r.run(10 * time.Minute)
	r.wantSent("DR1:1:alice:email")
}

// A shelf stops it; when the shelf ends and the alarm still waits, a new
// escalation starts.
func TestShelveStops(t *testing.T) {
	r := newRig(t, false)
	r.set("TT1", true)
	r.run(time.Minute)
	if err := r.eng.Shelve("TT1", r.now.Add(time.Hour), "alice"); err != nil {
		t.Fatal(err)
	}
	r.run(59 * time.Minute)
	r.wantSent("TT1:1:alice:email", "TT1:1:dave:email")
}

// The roster is read when a level is sent: maintenance hands over from
// erin to bob at 07:00.
func TestRosterReadAtSend(t *testing.T) {
	r := newRig(t, false)
	r.run(55 * time.Minute) // 06:55
	r.set("TT1", true)
	r.run(11 * time.Minute) // level 2 at 07:05
	got := r.sent()
	if !slices.Contains(got, "TT1:2:bob:sms") || slices.Contains(got, "TT1:2:erin:sms") {
		t.Fatalf("level 2 went to %v, want bob (on call since 07:00)", got)
	}
}

// An alarm no policy covers pages nobody.
func TestNoPolicyNoPage(t *testing.T) {
	r := newRig(t, false)
	r.set("LT1", true)
	r.run(time.Hour)
	r.wantSent()
}

// The write gate is asked before every send. A controller whose gate is
// closed sends nothing; when it opens, the due steps go.
func TestGateClosedSendsNothing(t *testing.T) {
	r := newRig(t, true)
	r.gate.Store(false)
	r.set("TT1", true)
	r.run(15 * time.Minute)
	r.wantSent()
	if got := r.notified(); len(got) != 0 {
		t.Fatalf("a closed gate journalled %v", got)
	}
	r.gate.Store(true)
	r.term.Store(2) // it won the lease: a new leadership
	r.run(time.Second)
	// It joined 15 minutes in: only the latest due level, not a burst,
	// journalled as a takeover's resend (its first look as leader).
	r.wantSent("TT1:2:erin:sms")
	evs, _ := r.ring.Query(time.Time{}, time.Time{}, alarm.Filter{Kinds: []string{alarm.KindNotify}})
	if len(evs) != 1 || evs[0].Reason != ResendTakeover {
		t.Fatalf("journal %+v, want one row with reason %q", evs, ResendTakeover)
	}
}

// restartRig is r's controller restarted after down: a fresh engine that
// restores r's retained alarms, and a fresh dispatcher, on r's clock.
func restartRig(t *testing.T, r *rig, down time.Duration, gated bool) *rig {
	t.Helper()
	snap := r.eng.Snapshot()
	n := &rig{t: t, tags: map[string]any{}}
	n.setNow(r.now.Add(down))
	for k, v := range r.tags {
		n.tags[k] = v
	}
	n.ring = alarm.NewRing(1000)
	defs := make([]alarm.Def, 0, 3)
	for _, rec := range r.eng.Records() {
		defs = append(defs, rec.Def)
	}
	var err error
	n.eng, err = alarm.New(alarm.Options{Defs: defs, Now: func() time.Time { return *n.clock.Load() },
		Read: func(p string) (any, bool) { v, ok := n.tags[p]; return v, ok }, Journal: n.ring})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { n.eng.Close() })
	n.eng.Restore(snap)
	n.email, n.sms = &Recorder{}, &Recorder{}
	n.gate.Store(true)
	n.term.Store(r.term.Load() + 1)
	o := Options{Config: prepare(t, policy), Engine: n.eng,
		Channels: map[string]Channel{"email": n.email, "sms": n.sms, "log": LogChannel{}}}
	if gated {
		o.Gate, o.Term = n.gate.Load, n.term.Load
	}
	n.d, err = New(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(n.d.Close)
	n.eng.Evaluate()
	n.d.Tick(n.now)
	n.d.Drain()
	return n
}

// A restart no longer restarts escalation at level 1 (#715 PR 1): the
// carried occurrence keeps its wait, the current level goes once more,
// journalled "resend after restart" on that occurrence, and the next
// level falls due on the original clock.
func TestRestartResendsTheCurrentLevelOnce(t *testing.T) {
	r := newRig(t, false)
	r.set("TT1", true)
	r.run(15 * time.Minute) // levels 1 (alice, dave) and 2 (erin) sent
	rec, _ := r.eng.Lookup("TT1")
	occ := rec.Occurrence
	if occ == "" {
		t.Fatal("no occurrence")
	}

	n := restartRig(t, r, time.Minute, false) // back at 06:16
	n.run(5 * time.Second)
	n.wantSent("TT1:2:erin:sms")
	evs, _ := n.ring.Query(time.Time{}, time.Time{}, alarm.Filter{Kinds: []string{alarm.KindNotify}})
	if len(evs) != 1 || evs[0].Reason != ResendRestart || evs[0].Occurrence != occ {
		t.Fatalf("journal %+v, want one row %q on occurrence %s", evs, ResendRestart, occ)
	}
	n.run(14 * time.Minute) // past 06:30: level 3 on the original clock, not 15 minutes later
	n.wantSent("TT1:2:erin:sms", "TT1:3:carol:sms", "TT1:3:carol:email")
}

// A restart whose clock went back (an RTC with a dead battery, no NTP
// yet) still pages: the carried wait is never later than now.
func TestRestartWithTheClockBackStillPages(t *testing.T) {
	r := newRig(t, false)
	r.set("TT1", true)
	r.run(time.Minute)
	n := restartRig(t, r, -2*time.Hour, false)
	n.run(5 * time.Second)
	if got := n.sent(); len(got) == 0 {
		t.Fatal("a restart with the clock set back paged nobody")
	}
}

// After a takeover the same, labelled as a takeover.
func TestTakeoverResendLabel(t *testing.T) {
	r := newRig(t, true)
	r.set("TT1", true)
	r.run(15 * time.Minute)
	n := restartRig(t, r, 2*time.Second, true)
	n.run(2 * time.Second)
	n.wantSent("TT1:2:erin:sms")
	evs, _ := n.ring.Query(time.Time{}, time.Time{}, alarm.Filter{Kinds: []string{alarm.KindNotify}})
	if len(evs) != 1 || evs[0].Reason != ResendTakeover {
		t.Fatalf("journal %+v, want one row %q", evs, ResendTakeover)
	}
}

// A late join that is not a leadership's first look keeps its own reason:
// the gate closes and opens again within one leadership (the same term),
// and an alarm that began waiting meanwhile joins late, not as a resend.
func TestLateJoinOutsideTheFirstLook(t *testing.T) {
	r := newRig(t, true)
	r.run(time.Second)
	r.gate.Store(false)
	r.set("TT1", true)
	r.run(15 * time.Minute)
	r.gate.Store(true) // same term: not a new leadership
	r.run(2 * time.Second)
	r.wantSent("TT1:2:erin:sms")
	evs, _ := r.ring.Query(time.Time{}, time.Time{}, alarm.Filter{Kinds: []string{alarm.KindNotify}})
	if len(evs) != 1 || evs[0].Reason != joinedLate {
		t.Fatalf("journal %+v, want one row %q", evs, joinedLate)
	}
}

// holdChannel is a Channel whose first Send waits to be released, so a
// test can change the world between a send being queued and it leaving.
type holdChannel struct {
	rec     *Recorder
	started chan struct{}
	release chan struct{}
	first   atomic.Bool
}

func (h *holdChannel) Send(ctx context.Context, m Message) error {
	if h.first.CompareAndSwap(false, true) {
		close(h.started)
		<-h.release
	}
	return h.rec.Send(ctx, m)
}

// The stale-role case: the gate was open when the sends were queued and
// has closed (or the leadership changed) before they leave. The sender
// reads the gate itself, now, so nothing more leaves.
func TestStaleRoleSendsNothing(t *testing.T) {
	for _, change := range []string{"gate closes", "term changes", "acknowledged"} {
		t.Run(change, func(t *testing.T) {
			r := newRig(t, true)
			h := &holdChannel{rec: r.email, started: make(chan struct{}), release: make(chan struct{})}
			r.d.channels["email"] = h
			r.set("TT1", true)
			r.advance(time.Second)
			r.eng.Evaluate()
			r.d.Tick(r.now) // queues alice and dave by email
			<-h.started     // the first is in flight, the second queued
			switch change {
			case "gate closes":
				r.gate.Store(false)
			case "term changes":
				r.term.Store(2)
			case "acknowledged":
				// Acked while the send queued behind the in-flight one
				// waits, with no look between: the sender asks the engine
				// itself, and the queued send does not leave.
				r.eng.Ack([]string{"TT1"}, "alice")
			}
			close(h.release)
			r.d.Drain()
			if got := r.sent(); len(got) != 1 {
				t.Fatalf("sent %v after the %s, want only the one already in flight", got, change)
			}
			if got := r.notified(); len(got) != 1 {
				t.Fatalf("journal %v, want the one send", got)
			}
		})
	}
}

// Sends that keep failing on one channel raise Failing; a success clears it.
func TestFailingChannel(t *testing.T) {
	r := newRig(t, false)
	r.sms.SetErr(errors.New("provider down"))
	r.set("TT1", true)
	r.run(31 * time.Minute) // sms: erin (level 2), carol (level 3)
	if f := r.d.Failing(); len(f) != 0 {
		t.Fatalf("Failing %v after two failures, want none until three", f)
	}
	r.set("DR1", true)
	r.run(6 * time.Minute) // sms: bob (DR1 level 2)
	if f := r.d.Failing(); !slices.Equal(f, []string{"sms"}) {
		t.Fatalf("Failing %v, want [sms]", f)
	}
	if !r.d.AnyFailing() {
		t.Fatal("AnyFailing false while sms is failing: the system alarm would not rise")
	}
	got := r.notified()
	if !slices.Contains(got, "TT1:2:erin:sms:failed") {
		t.Fatalf("journal %v, want the failure", got)
	}
	r.sms.SetErr(nil)
	r.eng.Ack([]string{"*"}, "alice")
	r.run(time.Second)
	r.set("TT1", false)
	r.set("DR1", false)
	r.run(time.Second)
	r.set("DR1", true)
	r.run(6 * time.Minute)
	if f := r.d.Failing(); len(f) != 0 {
		t.Fatalf("Failing %v after a success, want none", f)
	}
	if r.d.AnyFailing() {
		t.Fatal("AnyFailing still true after a success")
	}
}

// Nobody on call is journalled, not silently skipped.
func TestNobodyOnCall(t *testing.T) {
	defs := []alarm.Def{{ID: "X", Tag: "X", Area: "night", Priority: alarm.High, AckRequired: true, AutoClear: true}}
	r := newRig(t, false, defs...)
	c := prepare(t, `
oncall:
  timezone: America/Chicago
  rosters:
    - id: nights
      shifts: [{from: "18:00", to: "06:00", members: [carol]}]
escalation:
  - id: p
    levels: [{after: 0s, notify: [roster:nights], via: [sms]}]
`)
	r.d.Close()
	d, err := New(Options{Config: c, Engine: r.eng, Channels: map[string]Channel{"sms": r.sms}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	r.d = d
	r.run(time.Hour) // 07:00: nobody on nights
	r.set("X", true)
	r.run(2 * time.Second)
	r.wantSent()
	evs, _ := r.ring.Query(time.Time{}, time.Time{}, alarm.Filter{Kinds: []string{alarm.KindNotify}})
	if len(evs) != 1 || evs[0].Result != alarm.NotifyNobodyOnCall || evs[0].Level != 1 {
		t.Fatalf("journal %+v, want one nobody-on-call row at level 1", evs)
	}
}

func TestNewRefuses(t *testing.T) {
	c := prepare(t, policy)
	eng, _ := alarm.New(alarm.Options{Read: func(string) (any, bool) { return nil, false }})
	defer eng.Close()
	if _, err := New(Options{Config: c, Engine: eng, Channels: map[string]Channel{"email": &Recorder{}}}); err == nil ||
		!strings.Contains(err.Error(), `"sms"`) {
		t.Fatalf("New with a missing channel: %v", err)
	}
	if _, err := New(Options{Config: c, Engine: eng, Gate: func() bool { return true },
		Channels: map[string]Channel{"email": &Recorder{}, "sms": &Recorder{}}}); err == nil ||
		!strings.Contains(err.Error(), "Term") {
		t.Fatalf("New with a Gate and no Term: %v", err)
	}
}

// A send the closed gate held back goes once the gate opens again in the
// same leadership (a scan stall, not a takeover): it is due, not done.
func TestGateReopensSameTerm(t *testing.T) {
	r := newRig(t, true)
	h := &holdChannel{rec: r.email, started: make(chan struct{}), release: make(chan struct{})}
	r.d.channels["email"] = h
	r.set("TT1", true)
	r.advance(time.Second)
	r.eng.Evaluate()
	r.d.Tick(r.now)
	<-h.started
	r.gate.Store(false)
	close(h.release)
	r.d.Drain()
	if got := r.sent(); len(got) != 1 {
		t.Fatalf("sent %v with the gate closed, want only the one in flight", got)
	}
	r.gate.Store(true)
	r.run(2 * time.Second)
	r.wantSent("TT1:1:alice:email", "TT1:1:dave:email")
}

// The hour a daylight-saving change repeats is compared as instants: a
// 01:30 handover on the night clocks fall back happens once, at the first
// 01:30, and the second pass through 01:00–01:30 stays with the new turn.
func TestRosterHandoverInRepeatedHour(t *testing.T) {
	c := prepare(t, `
oncall:
  timezone: America/Chicago
  rosters:
    - { id: night, members: [x, y], rotate: daily, start: 2026-10-31, handover: "01:30" }
    - id: shift
      shifts: [{ from: "01:30", to: "05:00", members: [z] }]
`)
	loc := c.Oncall.Location()
	night, shift := c.Oncall.Roster("night"), c.Oncall.Roster("shift")
	first := time.Date(2026, 11, 1, 6, 45, 0, 0, time.UTC)  // 01:45 CDT, after the handover
	second := time.Date(2026, 11, 1, 7, 15, 0, 0, time.UTC) // 01:15 CST, the hour again
	before := time.Date(2026, 11, 1, 6, 15, 0, 0, time.UTC) // 01:15 CDT, before it
	if got := night.OnCall(before, loc); !slices.Equal(got, []string{"x"}) {
		t.Errorf("01:15 CDT: %v, want x (31 Oct's turn)", got)
	}
	for _, at := range []time.Time{first, second} {
		if got := night.OnCall(at, loc); !slices.Equal(got, []string{"y"}) {
			t.Errorf("%s: %v, want y (1 Nov's turn, from the first 01:30)", at.In(loc).Format("15:04 MST"), got)
		}
		if got := shift.OnCall(at, loc); !slices.Equal(got, []string{"z"}) {
			t.Errorf("%s: shift %v, want z (the shift began at the first 01:30)", at.In(loc).Format("15:04 MST"), got)
		}
	}
}

// A handover after a level was sent pages the new person at the next
// level, never with the levels already sent.
func TestHandoverNoBurst(t *testing.T) {
	r := newRig(t, false)
	r.run(40 * time.Minute) // 06:40
	r.set("TT1", true)
	r.run(30 * time.Minute) // level 2 to erin at 06:50; handover to bob at 07:00
	got := r.sent()
	if !slices.Contains(got, "TT1:2:erin:sms") {
		t.Fatalf("level 2 should have gone to erin at 06:50: %v", got)
	}
	for _, m := range got {
		if strings.Contains(m, ":bob:") {
			t.Fatalf("bob was paged with a level already sent to erin: %v", got)
		}
	}
}

// A send queued in one leadership that is delivered after the next one
// has queued its own touches nothing: each person gets the current level
// once from the new leadership, never twice.
func TestTermChangeNoDuplicate(t *testing.T) {
	r := newRig(t, true)
	h := &holdChannel{rec: r.email, started: make(chan struct{}), release: make(chan struct{})}
	r.d.channels["email"] = h
	r.set("TT1", true)
	r.advance(time.Second)
	r.eng.Evaluate()
	r.d.Tick(r.now) // term 1 queues alice and dave
	<-h.started     // alice in flight, dave queued
	r.term.Store(2)
	r.advance(time.Second)
	r.eng.Evaluate()
	r.d.Tick(r.now) // term 2 queues both again
	close(h.release)
	r.d.Drain()
	r.run(5 * time.Second)
	n := strings.Count(strings.Join(r.sent(), " "), "TT1:1:dave:email")
	if n != 1 {
		t.Fatalf("dave paged %d times, want once: %v", n, r.sent())
	}
}

// An annunciate-only alarm (ack-required false) never waits on a person,
// yet it pages while it is active (#715 PR 3, deviation 6): its levels
// run from its activation, and a return to normal stops them.
func TestAnnunciateOnlyPagesWhileActive(t *testing.T) {
	r := newRig(t, false, alarm.Def{ID: "TT9", Tag: "TT9", Area: "roast", Priority: alarm.High, AckRequired: false, AutoClear: true})
	r.set("TT9", true)
	r.run(11 * time.Minute)
	r.wantSent("TT9:1:alice:email", "TT9:1:dave:email", "TT9:2:erin:sms")
	r.set("TT9", false)
	r.run(time.Hour)
	r.wantSent("TT9:1:alice:email", "TT9:1:dave:email", "TT9:2:erin:sms")
	// Active again: a new escalation, from level 1.
	r.set("TT9", true)
	r.run(2 * time.Second)
	if n := strings.Count(strings.Join(r.sent(), " "), "TT9:1:alice:email"); n != 2 {
		t.Fatalf("a new activation did not page level 1 again: %v", r.sent())
	}
}

// A shelved annunciate-only alarm pages nobody, and its escalation ends.
func TestAnnunciateOnlyShelvedIsSilent(t *testing.T) {
	r := newRig(t, false, alarm.Def{ID: "TT9", Tag: "TT9", Area: "roast", Priority: alarm.High, AckRequired: false, AutoClear: true, Shelvable: true})
	r.set("TT9", true)
	r.run(time.Second)
	r.wantSent("TT9:1:alice:email", "TT9:1:dave:email")
	if err := r.eng.Shelve("TT9", r.now.Add(2*time.Hour), "alice"); err != nil {
		t.Fatal(err)
	}
	r.run(time.Hour)
	r.wantSent("TT9:1:alice:email", "TT9:1:dave:email")
}

// An acknowledged alarm that needs an ack sits in ack-active too, and
// pages nobody: only ack-required: false makes ack-active page.
func TestAckedActiveIsSilent(t *testing.T) {
	r := newRig(t, false)
	r.set("TT1", true)
	r.run(time.Second)
	if _, err := r.eng.Ack([]string{"TT1"}, "alice"); err != nil {
		t.Fatal(err)
	}
	r.run(time.Hour)
	r.wantSent("TT1:1:alice:email", "TT1:1:dave:email")
}

// An annunciate-only alarm that returns to normal between the look that
// queued a send and the send itself is not paged for: the sender asks the
// engine again, keyed on the activation, not on a wait it never had.
func TestAnnunciateOnlyRTNBeforeSend(t *testing.T) {
	r := newRig(t, false, alarm.Def{ID: "TT9", Tag: "TT9", Area: "roast", Priority: alarm.High, AckRequired: false, AutoClear: true})
	h := &holdChannel{rec: r.email, started: make(chan struct{}), release: make(chan struct{})}
	r.d.channels["email"] = h
	r.set("TT9", true)
	r.advance(time.Second)
	r.eng.Evaluate()
	r.d.Tick(r.now) // queues alice and dave
	<-h.started     // alice in flight, dave queued behind her
	r.set("TT9", false)
	r.eng.Evaluate()
	close(h.release)
	r.d.Drain()
	r.wantSent("TT9:1:alice:email")
}

// Each channel kind has its own sender: a mail server that hangs does not
// hold up an SMS queued behind it.
func TestHungChannelDelaysOnlyItsOwnKind(t *testing.T) {
	r := newRig(t, false)
	h := &holdChannel{rec: r.email, started: make(chan struct{}), release: make(chan struct{})}
	r.d.channels["email"] = h
	r.set("TT1", true)
	for i := 0; i < 11*60; i++ {
		r.advance(time.Second)
		r.eng.Evaluate()
		r.d.Tick(r.now)
	}
	<-h.started
	deadline := time.Now().Add(5 * time.Second)
	for len(r.sms.Messages()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := r.sms.Messages(); len(got) != 1 || got[0].To != "erin" {
		t.Fatalf("level 2's SMS waited behind a hung email: %v", got)
	}
	close(h.release)
	r.d.Drain()
}

// stuck is a Channel that never answers on its own: it returns only when
// its context ends.
type stuck struct{ entered chan struct{} }

func (c stuck) Send(ctx context.Context, _ Message) error {
	select {
	case c.entered <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return ctx.Err()
}

// A send that hangs is cut off at SendTimeout and journalled failed, so
// one dead endpoint cannot stall every page behind it.
func TestSendTimeout(t *testing.T) {
	r := newRig(t, false)
	r.d.Close()
	d, err := New(Options{Config: prepare(t, policy), Engine: r.eng, SendTimeout: 20 * time.Millisecond,
		Channels: map[string]Channel{"email": stuck{make(chan struct{}, 1)}, "sms": r.sms}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	r.d = d
	r.set("TT1", true)
	done := make(chan struct{})
	go func() { r.run(time.Second); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a hung channel held the sender past its timeout")
	}
	if got := r.notified(); !sameSet(got, []string{"TT1:1:alice:email:failed", "TT1:1:dave:email:failed"}) {
		t.Fatalf("journal %v, want both email sends failed", got)
	}
}

// Close cancels a send in flight instead of waiting on it.
func TestCloseCancelsInFlight(t *testing.T) {
	r := newRig(t, false)
	r.d.Close()
	ch := stuck{make(chan struct{}, 1)}
	d, err := New(Options{Config: prepare(t, policy), Engine: r.eng, SendTimeout: time.Hour,
		Channels: map[string]Channel{"email": ch, "sms": r.sms}})
	if err != nil {
		t.Fatal(err)
	}
	r.set("TT1", true)
	r.advance(time.Second)
	r.eng.Evaluate()
	d.Tick(r.now)
	<-ch.entered
	closed := make(chan struct{})
	go func() { d.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close waited on a hung send")
	}
	// Cut off by shutdown, not failed: nothing is journalled.
	if got := r.notified(); len(got) != 0 {
		t.Fatalf("a send cut off by Close was journalled: %v", got)
	}
}

// The failing flag is a leader's: a new leadership starts it clear.
func TestFailingResetsOnNewTerm(t *testing.T) {
	r := newRig(t, true)
	r.sms.SetErr(errors.New("provider down"))
	r.set("TT1", true)
	r.set("DR1", true)
	r.run(31 * time.Minute)
	if !r.d.AnyFailing() {
		t.Fatal("sms never counted as failing: the test proves nothing")
	}
	r.term.Store(2)
	r.run(time.Second)
	if r.d.AnyFailing() || len(r.d.Failing()) != 0 {
		t.Fatalf("still failing after a new term: %v", r.d.Failing())
	}
}

// A standby raises no failing alarm of its own: the failures were counted
// while it led.
func TestFailingClearsOnStandby(t *testing.T) {
	r := newRig(t, true)
	r.sms.SetErr(errors.New("provider down"))
	r.set("TT1", true)
	r.set("DR1", true)
	r.run(31 * time.Minute)
	if !r.d.AnyFailing() {
		t.Fatal("sms never counted as failing: the test proves nothing")
	}
	r.gate.Store(false)
	r.run(time.Second)
	if r.d.AnyFailing() {
		t.Fatal("a standby still reports a failing channel")
	}
}

// A time the spring-forward change skips lands just after the gap: a
// 02:30 handover on 2027-03-14 in Chicago happens at 03:00 CDT.
func TestHandoverInSkippedHour(t *testing.T) {
	c := prepare(t, `
oncall:
  timezone: America/Chicago
  rosters:
    - { id: r, members: [x, y], rotate: daily, start: 2027-03-13, handover: "02:30" }
`)
	loc := c.Oncall.Location()
	r := c.Oncall.Roster("r")
	before := time.Date(2027, 3, 14, 7, 59, 0, 0, time.UTC) // 01:59 CST
	after := time.Date(2027, 3, 14, 8, 0, 0, 0, time.UTC)   // 03:00 CDT
	if got := r.OnCall(before, loc); !slices.Equal(got, []string{"x"}) {
		t.Errorf("01:59 CST: %v, want x", got)
	}
	if got := r.OnCall(after, loc); !slices.Equal(got, []string{"y"}) {
		t.Errorf("03:00 CDT: %v, want y (the skipped 02:30 handover lands at 03:00)", got)
	}
}

// A failed send's notify row and log line carry its class (and a numeric
// code), never the error's text, which may echo an address or account.
func TestFailureReasonIsClassOnly(t *testing.T) {
	var logs strings.Builder
	r := newRig(t, false)
	r.d.log = slog.New(slog.NewTextHandler(&logs, nil))
	r.email.SetErr(fmt.Errorf("550 5.1.1 <alice@example.com>: mailbox unavailable: %w",
		&SendError{Class: FailRejected, Code: 550, Proto: "smtp"}))
	r.set("TT1", true)
	r.run(time.Second)
	r.email.SetErr(errors.New("dial tcp: lookup +15550100.example.com: no such host"))
	r.set("TT1", false)
	r.run(time.Second)
	if _, err := r.eng.Ack([]string{"TT1"}, "alice"); err != nil {
		t.Fatal(err)
	}
	r.run(time.Second)
	r.set("TT1", true)
	r.run(time.Second)
	evs, err := r.ring.Query(time.Time{}, time.Time{}, alarm.Filter{Kinds: []string{alarm.KindNotify}})
	if err != nil {
		t.Fatal(err)
	}
	reasons := map[string]bool{}
	for _, e := range evs {
		reasons[e.Reason] = true
	}
	if !reasons["rejected (smtp 550)"] || !reasons["error"] {
		t.Fatalf("reasons %v, want the classes rejected (smtp 550) and error", reasons)
	}
	all := fmt.Sprint(evs) + logs.String()
	for _, leak := range []string{"example.com", "15550100", "mailbox"} {
		if strings.Contains(all, leak) {
			t.Errorf("%q reached the journal or the log:\n%s", leak, all)
		}
	}
}

func TestFailureClasses(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{nil, ""},
		{&SendError{Class: FailAuth, Code: 401, Proto: "http"}, "auth (http 401)"},
		{fmt.Errorf("wrapped: %w", Failed(FailNoContact)), "no-contact"},
		{context.DeadlineExceeded, "timeout"},
		{&net.OpError{Op: "dial", Err: errors.New("connection refused to 192.0.2.1")}, "unreachable"},
		{errors.New("anything at all +15550100"), "error"},
	} {
		if got := Failure(c.err); got != c.want {
			t.Errorf("Failure(%v) = %q, want %q", c.err, got, c.want)
		}
	}
}

// scripted is a Channel whose Sends fail as errs says, in order (nil
// after the list), running between after each attempt.
type scripted struct {
	mu      sync.Mutex
	errs    []error
	calls   int
	between func()
}

func (c *scripted) Send(context.Context, Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	var err error
	if len(c.errs) > 0 {
		err, c.errs = c.errs[0], c.errs[1:]
	}
	if c.between != nil {
		c.between()
	}
	return err
}

func (c *scripted) n() int { c.mu.Lock(); defer c.mu.Unlock(); return c.calls }

func retryRig(t *testing.T, ch *scripted) *rig {
	t.Helper()
	r := newRig(t, true)
	r.d.channels["email"] = ch
	r.d.backoff = time.Millisecond
	return r
}

func lastNotify(t *testing.T, r *rig, to string) alarm.Event {
	t.Helper()
	evs, _ := r.ring.Query(time.Time{}, time.Time{}, alarm.Filter{Kinds: []string{alarm.KindNotify}})
	for _, e := range evs {
		if e.To == to {
			return e
		}
	}
	t.Fatalf("no notify row for %s", to)
	return alarm.Event{}
}

// A transient failure (a timeout) is tried again by the dispatcher; an
// auth failure is not.
func TestDispatcherRetriesTransientOnly(t *testing.T) {
	ch := &scripted{errs: []error{Failed(FailTimeout), Failed(FailProvider)}}
	r := retryRig(t, ch)
	r.set("TT1", true)
	r.run(time.Second)
	if e := lastNotify(t, r, "alice"); e.Result != alarm.NotifySent {
		t.Fatalf("alice after a timeout and a 5xx: %+v, want sent on the third attempt", e)
	}

	ch2 := &scripted{errs: []error{Failed(FailAuth), Failed(FailAuth), Failed(FailAuth), Failed(FailAuth)}}
	r2 := retryRig(t, ch2)
	r2.set("TT1", true)
	r2.run(time.Second)
	if n := ch2.n(); n != 2 { // alice and dave, once each
		t.Fatalf("an auth failure was retried: %d sends for two people", n)
	}
}

// An acknowledgement, or the end of this leadership, during a failed
// attempt stops the retry: the dispatcher asks the engine and the gate
// again before every attempt, not only the first.
func TestRetryAsksAgainBeforeEachAttempt(t *testing.T) {
	for _, c := range []struct {
		name string
		stop func(r *rig)
	}{
		{"acknowledged", func(r *rig) { r.eng.Ack([]string{"TT1"}, "alice") }},
		{"gate closed", func(r *rig) { r.gate.Store(false) }},
		{"term changed", func(r *rig) { r.term.Store(9) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			ch := &scripted{errs: []error{Failed(FailTimeout), Failed(FailTimeout), Failed(FailTimeout)}}
			r := retryRig(t, ch)
			once := sync.Once{}
			ch.between = func() { once.Do(func() { c.stop(r) }) }
			r.set("TT1", true)
			r.advance(time.Second)
			r.eng.Evaluate()
			r.d.Tick(r.now)
			r.d.Drain()
			if n := ch.n(); n != 1 {
				t.Fatalf("%d attempts after the alarm stopped waiting or the leadership ended, want 1", n)
			}
		})
	}
}

// A page carries the ack link (#715) the hook mints for its alarm's
// occurrence, person and channel, minted only for a page that leaves
// (after the gate), never journalled.
func TestPagesCarryTheAckLink(t *testing.T) {
	var mu sync.Mutex
	var minted []string
	r := newRigWith(t, true, func(o *Options) {
		o.AckLink = func(m Message, via string) string {
			mu.Lock()
			defer mu.Unlock()
			if m.Alarm.Occurrence == "" {
				t.Errorf("a link asked for with no occurrence: %+v", m)
			}
			minted = append(minted, fmt.Sprintf("%s:%d:%s:%s", m.Alarm.ID, m.Level, m.To, via))
			return "https://hmi.example/alarm-ack/" + m.To + "-" + via
		}
	})
	r.gate.Store(false)
	r.set("TT1", true)
	r.run(time.Second)
	mu.Lock()
	if len(minted) != 0 {
		t.Fatalf("minted %v behind a closed gate", minted)
	}
	mu.Unlock()
	r.gate.Store(true)
	r.term.Store(2)
	r.run(time.Second)
	for _, m := range r.email.Messages() {
		if m.AckURL != "https://hmi.example/alarm-ack/"+m.To+"-email" {
			t.Fatalf("email to %s carries %q", m.To, m.AckURL)
		}
	}
	if len(r.email.Messages()) == 0 {
		t.Fatal("nothing sent")
	}
	evs, _ := r.ring.Query(time.Time{}, time.Time{}, alarm.Filter{})
	for _, e := range evs {
		b, _ := json.Marshal(e)
		if strings.Contains(string(b), "alarm-ack") {
			t.Fatalf("a link reached the journal: %s", b)
		}
	}
}

// An ack-url is https, or http only by an explicit ack-url-insecure; the
// default is that the link alone acknowledges (James, #715).
func TestAckURLAccepted(t *testing.T) {
	c := prepare(t, `oncall: {ack-url: "https://hmi.example:8443/plant"}`)
	if c.Oncall.AckFrom() != AckByLink || DefaultAckFrom != AckByLink {
		t.Fatalf("default %q", c.Oncall.AckFrom())
	}
	c = prepare(t, `oncall: {ack-url: "http://10.0.0.5:8080", ack-url-insecure: true, ack-from-notification: sign-in}`)
	if c.Oncall.AckFrom() != AckBySignIn {
		t.Fatalf("sign-in %q", c.Oncall.AckFrom())
	}
}
