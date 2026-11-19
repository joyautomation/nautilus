package runtime

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
)

// Substitutes in time (#707 PR 1b): the remainder rule, expiry on the
// monotonic clock, the companions, acknowledgement and events.

// The remainder rule, against values worked out by hand from #707 (plan
// item 6, naut-lead's downtime rule) and redundancy test plan §8.1 case 9:
// a substitute granted 1 h, started at T0, until T0+1h.
func TestSubstituteRemainderTable(t *testing.T) {
	t0 := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	until := t0.Add(time.Hour)
	const h = time.Hour
	for _, tc := range []struct {
		name    string
		mode    remainderMode
		now     time.Time
		left    time.Duration
		expired bool
		capped  bool
	}{
		{"restart 20 min in: 40 min left", remainRestart, t0.Add(20 * time.Minute), 40 * time.Minute, false, false},
		{"restart after 2 h down: downtime counts", remainRestart, t0.Add(2 * h), 0, true, false},
		{"restart exactly at until", remainRestart, until, 0, true, false},
		{"restart 1 ms before until", remainRestart, until.Add(-time.Millisecond), time.Millisecond, false, false},
		{"restart on a clock gone back past the start", remainRestart, t0.Add(-time.Minute), 0, true, false},
		{"restart on a clock gone back 1 ms past the start", remainRestart, t0.Add(-time.Millisecond), 0, true, false},
		{"takeover 20 min in", remainTakeover, t0.Add(20 * time.Minute), 40 * time.Minute, false, false},
		{"takeover 1 h ahead: expired", remainTakeover, t0.Add(h + time.Millisecond), 0, true, false},
		{"takeover 10 s behind: skew, capped quietly", remainTakeover, t0.Add(-10 * time.Second), h, false, false},
		{"takeover exactly the skew behind", remainTakeover, t0.Add(-30 * time.Second), h, false, false},
		{"takeover 31 s behind: capped, a fault", remainTakeover, t0.Add(-31 * time.Second), h, false, true},
		{"takeover 1 h behind: capped, a fault", remainTakeover, t0.Add(-h), h, false, true},
	} {
		got := substituteRemainder(t0, until, h, tc.now, time.Time{}, tc.mode)
		want := remainder{left: tc.left, expired: tc.expired, capped: tc.capped}
		if got != want {
			t.Errorf("%s: got %+v; want %+v", tc.name, got, want)
		}
	}
	if SubstituteSkew != 30*time.Second {
		t.Errorf("SubstituteSkew = %v; the agreed bound (#707, §8.1 case 9) is 30 s", SubstituteSkew)
	}
}

// subClock is a wall clock and a monotonic clock a test moves apart, as a
// wall-clock step does.
type subClock struct {
	mu   sync.Mutex
	wall time.Time
	mono time.Duration
}

func (c *subClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.wall
}

func (c *subClock) monoNow() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mono
}

// advance moves both clocks: time passing.
func (c *subClock) advance(d time.Duration) {
	c.mu.Lock()
	c.wall, c.mono = c.wall.Add(d), c.mono+d
	c.mu.Unlock()
}

// step moves the wall clock alone: NTP, or someone setting the date.
func (c *subClock) step(d time.Duration) {
	c.mu.Lock()
	c.wall = c.wall.Add(d)
	c.mu.Unlock()
}

// hookDriver is a field driver that can fail its read or run a hook in it
// (a read that takes time).
type hookDriver struct {
	*fieldDriver
	mu     sync.Mutex
	fail   bool
	onRead func()
}

func (d *hookDriver) ReadInputs() (nio.Values, error) {
	d.mu.Lock()
	fail, hook := d.fail, d.onRead
	d.mu.Unlock()
	if hook != nil {
		hook()
	}
	if fail {
		return nil, errors.New("bus timeout")
	}
	return d.fieldDriver.ReadInputs()
}

func (d *hookDriver) setFail(b bool) { d.mu.Lock(); d.fail = b; d.mu.Unlock() }

// expiryProgram reads the companions the way a project's logic would:
// a flag, a start permissive, and a latch of the expiry pulse. Its last
// line writes a companion, which must not stick.
const expiryProgram = `PROGRAM Sub
VAR_EXTERNAL
  Level : REAL; Flow : REAL;
  Level__Substituted : BOOL; Flow__Substituted : BOOL;
  __SubstitutesUnacked : BOOL; __Substitute_Expired : BOOL;
  SawFlag : BOOL; SawExpired : BOOL; Permit : BOOL; Mirror : REAL;
END_VAR
SawFlag := Level__Substituted;
Mirror := Level;
IF __Substitute_Expired THEN SawExpired := TRUE; END_IF;
Permit := NOT __SubstitutesUnacked;
Flow__Substituted := TRUE;
END_PROGRAM`

func newExpiryRig(t *testing.T, coord Coordinator) (*Runtime, *hookDriver, *subClock) {
	t.Helper()
	clk := &subClock{wall: time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC), mono: 1000 * time.Hour}
	d := &hookDriver{fieldDriver: &fieldDriver{in: nio.Values{"Level": 10.0, "Flow": 3.0}}}
	rt, err := New(Options{
		Program: expiryProgram, Driver: d, Clock: clk, Coordinator: coord,
		Tags: []TagDef{
			Input("Level"), Input("Flow"),
			State("SawFlag", false), State("SawExpired", false), State("Permit", false), State("Mirror", 0.0),
		},
		Substitutable: []string{"Level", "Flow"},
	})
	if err != nil {
		t.Fatal(err)
	}
	rt.mono = clk.monoNow
	return rt, d, clk
}

func subFor(t *testing.T, rt *Runtime, addr string, v any, d time.Duration, ack bool) Substitution {
	t.Helper()
	s, err := rt.Substitute(addr, SubstituteRequest{Value: v, For: d, By: "op", AckOnStart: ack})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// §8.1 case 9 on a steady leader: the granted duration is timed on the
// monotonic clock, so a wall-clock step either way neither ends nor
// extends the substitute.
func TestSubstituteExpiryIgnoresAWallClockStep(t *testing.T) {
	rt, _, clk := newExpiryRig(t, nil)
	rt.Scan()
	subFor(t, rt, "Level", 95, time.Hour, false)
	clk.advance(10 * time.Minute)
	clk.step(time.Hour) // the wall clock is now past until
	rt.Scan()
	if got := rt.Tags().Real("Mirror"); got != 95 {
		t.Fatalf("Mirror = %v after a +1 h wall step 10 min in; the step ended the substitute", got)
	}
	clk.step(-2 * time.Hour)
	clk.advance(50*time.Minute - time.Millisecond)
	rt.Scan()
	if got := rt.Tags().Real("Mirror"); got != 95 || rt.Tags().SubstituteCount() != 1 {
		t.Fatalf("Mirror = %v, %d active 1 ms before the granted hour", got, rt.Tags().SubstituteCount())
	}
	if l := rt.Substitutes(); len(l) != 1 || l[0].RemainingMs != 0 && l[0].RemainingMs != 1 {
		t.Fatalf("Substitutes() = %+v; want 1 ms left, whatever the wall clock says", l)
	}
	clk.advance(time.Millisecond)
	rt.Scan()
	if got := rt.Tags().Real("Mirror"); got != 10 || rt.Tags().SubstituteCount() != 0 {
		t.Fatalf("Mirror = %v, %d active at the granted hour (a -1 h wall step); want the live 10, none", got, rt.Tags().SubstituteCount())
	}
	if !rt.Tags().Bool("SawExpired") {
		t.Fatal("the scan that put the live value back did not see __Substitute_Expired")
	}
}

// An expiry is evaluated after the read whether or not the read worked:
// a bus that keeps failing does not keep a substitute running. The live
// value returns, and with it the tag's own quality: Stale, since the read
// failed.
func TestSubstituteExpiresWhenTheReadFails(t *testing.T) {
	rt, d, clk := newExpiryRig(t, nil)
	rt.Scan()
	subFor(t, rt, "Level", 95, time.Minute, false)
	d.setFail(true)
	rt.Scan()
	if q := rt.TagQuality("Level"); q != nio.Substituted {
		t.Fatalf("Level quality %v under a failed read; want substituted", q)
	}
	clk.advance(time.Minute)
	rt.Scan()
	if got := rt.Tags().Real("Mirror"); got != 10 || rt.Tags().SubstituteCount() != 0 {
		t.Fatalf("Mirror = %v with %d active: the failed read postponed the expiry", got, rt.Tags().SubstituteCount())
	}
	if q := rt.TagQuality("Level"); q != nio.Stale {
		t.Fatalf("Level quality %v after expiry under a failed read; want stale", q)
	}
}

// __Substitute_Expired stays true until a main scan's logic has run with
// it AND a second has passed, whichever is later. With no substitute left,
// the pulse alone keeps the substitute step running until it clears.
func TestSubstituteExpiredPulse(t *testing.T) {
	expired := func(rt *Runtime) bool { return rt.Tags().Bool(SubstituteExpiredTag) }
	t.Run("fast scans hold it a second", func(t *testing.T) {
		rt, _, clk := newExpiryRig(t, nil)
		rt.Scan()
		subFor(t, rt, "Level", 95, time.Minute, false)
		clk.advance(time.Minute)
		rt.Scan()
		if !expired(rt) || !rt.Tags().Bool("SawExpired") {
			t.Fatal("no pulse on the expiring scan")
		}
		for i := 0; i < 9; i++ { // nine more scans, 100 ms apart: 0.9 s
			clk.advance(100 * time.Millisecond)
			rt.Scan()
			if !expired(rt) {
				t.Fatalf("pulse cleared %d ms after the expiry; want ≥ 1 s", (i+1)*100)
			}
		}
		clk.advance(100 * time.Millisecond)
		rt.Scan()
		if expired(rt) {
			t.Fatal("pulse still true 1 s and 10 scans after the expiry")
		}
	})
	t.Run("a slow first read after a takeover", func(t *testing.T) {
		// The takeover expires the entry before any logic runs; then the
		// read takes 2 s. The second has passed, but no logic has seen
		// the pulse yet: it must survive into the scan.
		coord := &flag{lead: true}
		rt, d, clk := newExpiryRig(t, coord)
		rt.Scan()
		subFor(t, rt, "Level", 95, time.Minute, false)
		coord.lead = false
		rt.Scan()
		clk.advance(2 * time.Minute)
		coord.lead = true
		d.mu.Lock()
		d.onRead = func() { clk.advance(2 * time.Second) }
		d.mu.Unlock()
		rt.Scan()
		if rt.Tags().SubstituteCount() != 0 {
			t.Fatal("setup: the takeover did not expire the entry")
		}
		if !rt.Tags().Bool("SawExpired") {
			t.Fatal("the pulse cleared on time alone before any logic saw it")
		}
	})
}

// The companions follow the table from scan one (the program binds them),
// per root, through a replace, two members ending one at a time and a
// partial ack. Nobody but the runtime sets them: an API write and a force
// are refused, and a program's write does not stick.
func TestSubstituteCompanions(t *testing.T) {
	rt, _, _ := newExpiryRig(t, nil)
	rt.Scan()
	tags := rt.Tags()
	flagOf := func(n string) bool { return tags.Bool(n + SubstitutedSuffix) }
	if flagOf("Level") || flagOf("Flow") || tags.Bool("SawFlag") || !tags.Bool("Permit") {
		t.Fatal("companions not false on scan one")
	}
	a := subFor(t, rt, "Level", 95, time.Hour, true)
	if !flagOf("Level") || flagOf("Flow") || !tags.Bool(SubstitutesUnackedTag) {
		t.Fatal("Level__Substituted / __SubstitutesUnacked not set with the substitute, before any scan")
	}
	rt.Scan()
	if !tags.Bool("SawFlag") || tags.Bool("Permit") {
		t.Fatal("the program did not see the flag and the unacked interlock on the next scan")
	}
	if flagOf("Flow") {
		t.Fatal("the program's write to Flow__Substituted stuck")
	}
	b := subFor(t, rt, "Flow", 1, time.Hour, true)
	a2 := subFor(t, rt, "Level", 96, time.Hour, true) // a replace: new entry, unacked
	if acked := rt.AckSubstitutes([]uint64{a.ID, b.ID}, "op"); len(acked) != 1 || acked[0].ID != b.ID {
		t.Fatalf("AckSubstitutes(stale %d, %d) acked %+v; want only %d", a.ID, b.ID, acked, b.ID)
	}
	if !tags.Bool(SubstitutesUnackedTag) {
		t.Fatal("__SubstitutesUnacked cleared while the replacing entry is unacked")
	}
	if acked := rt.AckSubstitutes([]uint64{a2.ID, a2.ID, b.ID}, "op"); len(acked) != 1 || acked[0].ID != a2.ID {
		t.Fatalf("second ack acked %+v; want only %d", acked, a2.ID)
	}
	if tags.Bool(SubstitutesUnackedTag) {
		t.Fatal("__SubstitutesUnacked still true with every entry acked")
	}
	rt.Restore(b.ID, "op")
	if flagOf("Flow") || !flagOf("Level") {
		t.Fatal("ending Flow's entry did not clear exactly Flow__Substituted")
	}
	for _, err := range tags.Write([]WriteOp{{Path: "Level__Substituted", Value: false}, {Path: SubstitutesUnackedTag, Value: true}}) {
		if !errors.Is(err, ErrReadOnly) {
			t.Fatalf("a write to a companion: %v; want ErrReadOnly", err)
		}
	}
	if err := tags.Force("Level__Substituted", false); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("a force on a companion: %v; want ErrReadOnly", err)
	}
	if !flagOf("Level") || tags.Bool(SubstitutesUnackedTag) {
		t.Fatal("a refused write changed a companion")
	}
}

// A member substitute raises its root's flag; the flag stays while any
// member is substituted.
func TestSubstituteCompanionMembers(t *testing.T) {
	const lib = "TYPE Pump : STRUCT Run : BOOL; Speed : REAL; END_STRUCT; END_TYPE"
	const prog = `PROGRAM M
VAR_EXTERNAL P1 : Pump; P1__Substituted : BOOL; Seen : BOOL; END_VAR
Seen := P1__Substituted;
END_PROGRAM`
	fd := &fieldDriver{in: nio.Values{}}
	rt, err := New(Options{
		Program: prog, Libraries: []string{lib}, Driver: fd,
		Tags:          []TagDef{Typed("P1", RoleInput, "Pump"), State("Seen", false)},
		Substitutable: []string{"P1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	pt := rt.tags.types[ir.NameKey("P1")]
	fd.set("P1", ir.Zero(pt))
	rt.Scan()
	run := subFor(t, rt, "P1.Run", false, time.Hour, false)
	speed := subFor(t, rt, "P1.Speed", 5, time.Hour, false)
	rt.Scan()
	if !rt.Tags().Bool("Seen") {
		t.Fatal("a member substitute did not raise P1__Substituted")
	}
	rt.Restore(run.ID, "op")
	if !rt.Tags().Bool("P1__Substituted") {
		t.Fatal("P1__Substituted fell while P1.Speed is still substituted")
	}
	rt.Restore(speed.ID, "op")
	if rt.Tags().Bool("P1__Substituted") {
		t.Fatal("P1__Substituted still true with no member substituted")
	}
}

// Events come out in the order the changes were made, and each entry ends
// exactly once: a Restore of an entry that has already expired is not one.
func TestSubstituteEvents(t *testing.T) {
	rt, _, clk := newExpiryRig(t, nil)
	rt.Scan()
	got := make(chan SubstituteEvent, 32)
	cancel := rt.OnSubstituteEvent(func(e SubstituteEvent) { got <- e })
	defer cancel()

	a := subFor(t, rt, "Level", 95, time.Minute, true)
	a2 := subFor(t, rt, "Level", 96, time.Minute, true)
	rt.AckSubstitutes([]uint64{a2.ID}, "sup")
	b := subFor(t, rt, "Flow", 1, time.Hour, false)
	rt.Restore(b.ID, "op2")
	clk.advance(time.Minute)
	rt.Scan() // a2 expires
	if _, ok := rt.Restore(a2.ID, "late"); ok {
		t.Fatal("Restore of an expired entry reported success")
	}
	want := []struct {
		kind, reason, by string
		id               uint64
	}{
		{SubstituteEventStart, "", "op", a.ID},
		{SubstituteEventStop, SubstituteReasonReplaced, "op", a.ID},
		{SubstituteEventStart, "", "op", a2.ID},
		{SubstituteEventAck, "", "sup", a2.ID},
		{SubstituteEventStart, "", "op", b.ID},
		{SubstituteEventStop, "", "op2", b.ID},
		{SubstituteEventExpire, SubstituteReasonExpired, "system", a2.ID},
	}
	for i, w := range want {
		select {
		case e := <-got:
			if e.Kind != w.kind || e.Reason != w.reason || e.By != w.by || e.Entry.ID != w.id {
				t.Fatalf("event %d = %s/%s by %s id %d; want %s/%s by %s id %d",
					i, e.Kind, e.Reason, e.By, e.Entry.ID, w.kind, w.reason, w.by, w.id)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("event %d (%s) never came", i, w.kind)
		}
	}
	select {
	case e := <-got:
		t.Fatalf("an extra event: %+v", e)
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	subFor(t, rt, "Flow", 2, time.Hour, false)
	select {
	case e := <-got:
		t.Fatalf("an event after cancel: %+v", e)
	case <-time.After(50 * time.Millisecond):
	}
}

// A listener that blocks does not hold up a scan, and loses nothing.
func TestSubstituteEventsNeverBlockTheScan(t *testing.T) {
	rt, _, clk := newExpiryRig(t, nil)
	rt.Scan()
	release := make(chan struct{})
	var mu sync.Mutex
	var n int
	rt.OnSubstituteEvent(func(SubstituteEvent) {
		<-release
		mu.Lock()
		n++
		mu.Unlock()
	})
	for i := 0; i < 5; i++ {
		subFor(t, rt, "Level", 95, time.Second, false)
		clk.advance(time.Second)
		done := make(chan struct{})
		go func() { rt.Scan(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("a scan waited on a blocked event listener")
		}
	}
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		got := n
		mu.Unlock()
		if got == 10 { // five starts, five expiries
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d events delivered; want 10", got)
		}
		time.Sleep(time.Millisecond)
	}
}

// §8.1 case 9 at a takeover: the remainder is computed once from until,
// so a takeover never restarts the full duration; a clock far behind is
// capped at the granted duration and reported as a fault.
func TestTakeoverRetimesSubstitutesFromUntil(t *testing.T) {
	t.Run("the remainder carries over", func(t *testing.T) {
		coord := &flag{lead: true}
		rt, _, clk := newExpiryRig(t, coord)
		rt.Scan()
		subFor(t, rt, "Level", 95, time.Hour, false)
		clk.advance(40 * time.Minute)
		coord.lead = false
		rt.Scan()
		coord.lead = true
		rt.Scan() // takeover
		if l := rt.Substitutes(); len(l) != 1 || l[0].RemainingMs != (20*time.Minute).Milliseconds() {
			t.Fatalf("after the takeover: %+v; want 20 min left", l)
		}
		clk.advance(20 * time.Minute)
		rt.Scan()
		if rt.Tags().SubstituteCount() != 0 {
			t.Fatal("still substituted at the granted hour: the takeover restarted the duration")
		}
	})
	t.Run("an entry this process never timed", func(t *testing.T) {
		// As PR 3's oplog will hand a new leader an entry another replica
		// started: no local deadline, only until. Its remainder is until −
		// now, not the granted duration.
		coord := &flag{lead: true}
		rt, _, clk := newExpiryRig(t, coord)
		rt.Scan()
		subFor(t, rt, "Level", 95, time.Hour, false)
		coord.lead = false
		rt.Scan()
		rt.tags.mu.Lock()
		for _, ss := range rt.tags.subs {
			for i := range ss.entries {
				ss.entries[i].timed, ss.entries[i].deadline = false, 0
			}
		}
		rt.tags.mu.Unlock()
		clk.advance(40 * time.Minute)
		coord.lead = true
		rt.Scan()
		if l := rt.Substitutes(); len(l) != 1 || l[0].RemainingMs != (20*time.Minute).Milliseconds() {
			t.Fatalf("after the takeover: %+v; want 20 min left from until", l)
		}
	})
	t.Run("a wall step back before the edge does not stretch it", func(t *testing.T) {
		// 40 min into the hour, the wall clock goes back an hour, then the
		// replica loses and regains the lease. until − now is 80 min,
		// capped to 60: the entry must still end at the granted hour, 20
		// min on, and the step is reported.
		coord := &flag{lead: true}
		rt, _, clk := newExpiryRig(t, coord)
		rt.Scan()
		subFor(t, rt, "Level", 95, time.Hour, false)
		events := make(chan SubstituteEvent, 8)
		rt.OnSubstituteEvent(func(e SubstituteEvent) { events <- e })
		clk.advance(40 * time.Minute)
		clk.step(-time.Hour)
		coord.lead = false
		rt.Scan()
		coord.lead = true
		rt.Scan()
		if l := rt.Substitutes(); len(l) != 1 || l[0].RemainingMs != (20*time.Minute).Milliseconds() {
			t.Fatalf("after the takeover: %+v; want the 20 min this process still held", l)
		}
		waitFault(t, events, SubstituteReasonClockCapped)
		clk.advance(20 * time.Minute)
		rt.Scan()
		if rt.Tags().SubstituteCount() != 0 {
			t.Fatal("still substituted at the granted hour: the takeover stretched it")
		}
	})
	t.Run("a clock an hour behind is capped and reported", func(t *testing.T) {
		coord := &flag{lead: true}
		rt, _, clk := newExpiryRig(t, coord)
		rt.Scan()
		subFor(t, rt, "Level", 95, time.Hour, false)
		events := make(chan SubstituteEvent, 8)
		rt.OnSubstituteEvent(func(e SubstituteEvent) { events <- e })
		coord.lead = false
		rt.Scan()
		clk.step(-time.Hour)
		coord.lead = true
		rt.Scan()
		if l := rt.Substitutes(); len(l) != 1 || l[0].RemainingMs != time.Hour.Milliseconds() {
			t.Fatalf("after the takeover: %+v; want capped at the granted hour", l)
		}
		waitFault(t, events, SubstituteReasonClockCapped)
	})
}

// waitFault waits for a fault event with this reason, passing over the
// events before it (the start, delivered to the first listener).
func waitFault(t *testing.T, events <-chan SubstituteEvent, reason string) SubstituteEvent {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case e := <-events:
			if e.Kind == SubstituteEventFault {
				if e.Reason != reason {
					t.Fatalf("fault event %+v; want reason %s", e, reason)
				}
				return e
			}
		case <-deadline:
			t.Fatalf("no fault event (%s)", reason)
		}
	}
}

// A standby never scans, so it never expires a substitute on its own
// (§8.1 case 8): the leader's expiry is the one that counts.
func TestStandbyDoesNotExpireSubstitutes(t *testing.T) {
	coord := &flag{lead: true}
	rt, _, clk := newExpiryRig(t, coord)
	rt.Scan()
	subFor(t, rt, "Level", 95, time.Minute, false)
	coord.lead = false
	clk.advance(time.Hour)
	for i := 0; i < 3; i++ {
		rt.Scan()
	}
	if rt.Tags().SubstituteCount() != 1 {
		t.Fatal("a standby expired a substitute")
	}
}

// Only a substitutable input can be substituted, so no substitute runs
// without its flag; New refuses a list it cannot honour.
func TestSubstitutableRefusals(t *testing.T) {
	d := &fieldDriver{in: nio.Values{"Level": 10.0, "Flow": 1.0}}
	rt, err := New(Options{
		Program: `PROGRAM P VAR_EXTERNAL Level : REAL; Flow : REAL; END_VAR END_PROGRAM`, Driver: d,
		Tags: []TagDef{Input("Level"), Input("Flow")}, Substitutable: []string{"Level"},
	})
	if err != nil {
		t.Fatal(err)
	}
	rt.Scan()
	if _, err := rt.Substitute("Flow", SubstituteRequest{Value: 1, For: time.Hour}); err == nil || !strings.Contains(err.Error(), "not substitutable") {
		t.Fatalf("Substitute on an input not in Substitutable: %v", err)
	}
	for name, o := range map[string]Options{
		"an output": {Tags: []TagDef{Input("Level"), Output("Out", Init(false))}, Substitutable: []string{"Out"}},
		"a name taken": {Tags: []TagDef{Input("Level"), State("Level__Substituted", true)},
			Substitutable: []string{"Level"}},
		"listed twice": {Tags: []TagDef{Input("Level")}, Substitutable: []string{"Level", "level"}},
	} {
		o.Program = `PROGRAM P VAR_EXTERNAL Level : REAL; END_VAR END_PROGRAM`
		o.Driver = d
		_, err := New(o)
		if err == nil || !strings.Contains(err.Error(), wantErr[name]) {
			t.Errorf("%s: New said %v; want an error containing %q", name, err, wantErr[name])
		}
	}
}

var wantErr = map[string]string{
	"an output":    "is not a driver-bound input",
	"a name taken": "the runtime sets Level__Substituted",
	"listed twice": "listed twice",
}

// A whole struct input substituted: the substitute holds as a whole over
// every delivery, and Restore gives the delivered value back.
func TestSubstituteWholeStruct(t *testing.T) {
	const lib = "TYPE Pump : STRUCT Run : BOOL; Speed : REAL; END_STRUCT; END_TYPE"
	fd := &fieldDriver{in: nio.Values{}}
	rt, err := New(Options{
		Program: "PROGRAM M\nVAR_EXTERNAL P1 : Pump; S : REAL; END_VAR\nS := P1.Speed;\nEND_PROGRAM", Libraries: []string{lib}, Driver: fd,
		Tags:          []TagDef{Typed("P1", RoleInput, "Pump"), State("S", 0.0)},
		Substitutable: []string{"P1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	pt := rt.tags.types[ir.NameKey("P1")]
	pv, _ := ir.SetFieldTyped(ir.Zero(pt), pt, []string{"Speed"}, 20.0, "")
	fd.set("P1", pv)
	rt.Scan()
	s := subFor(t, rt, "P1", map[string]any{"Run": true, "Speed": 55.0}, time.Hour, false)
	for i := 0; i < 2; i++ {
		rt.Scan()
	}
	if v, _ := rt.Tags().ReadPath("P1.Run"); v != true || rt.Tags().Real("S") != 55 {
		t.Fatalf("P1.Run = %v, S = %v; want the substitute's true and 55", v, rt.Tags().Real("S"))
	}
	rt.Restore(s.ID, "op")
	if v, _ := rt.Tags().ReadPath("P1.Speed"); v != 20.0 {
		t.Fatalf("P1.Speed = %v after Restore; want the delivered 20", v)
	}
}

// The flag follows its input whatever spelling the store keeps it under
// (a seed spelled differently from the input binding).
func TestSubstituteCompanionCaseFolding(t *testing.T) {
	d := &fieldDriver{in: nio.Values{"Level": 10.0}}
	rt, err := New(Options{
		Program: `PROGRAM P VAR_EXTERNAL Level : REAL; END_VAR END_PROGRAM`, Driver: d,
		Inputs: []string{"Level"}, Seed: nio.Values{"LEVEL": 0.0}, Substitutable: []string{"level"},
	})
	if err != nil {
		t.Fatal(err)
	}
	rt.Scan()
	subFor(t, rt, "Level", 95, time.Hour, false)
	if !rt.Tags().Bool("level__Substituted") {
		t.Fatal("level__Substituted not raised for an input the store keeps as LEVEL")
	}
}

// An active whole-tag substitute costs a scan no allocation beyond what
// the same scan costs without one: the expiry check walks the table in
// place, and each delivery has the substitute laid over it unboxed.
func TestSubstituteScanAllocs(t *testing.T) {
	rt, _, _ := newExpiryRig(t, nil)
	rt.Scan()
	base := testing.AllocsPerRun(50, rt.Scan)
	subFor(t, rt, "Level", 95, time.Hour, false)
	if got := testing.AllocsPerRun(50, rt.Scan); got > base {
		t.Fatalf("%v allocations per scan with an active substitute; %v without", got, base)
	}
}

// The production clock runs, and never runs backwards.
func TestBootClockAdvances(t *testing.T) {
	c := bootClock()
	a := c()
	time.Sleep(2 * time.Millisecond)
	if b := c(); b-a < 2*time.Millisecond {
		t.Fatalf("boot clock moved %v across a 2 ms sleep", b-a)
	}
}

// An expiry and a capped remainder reach the journal even with no event
// listener registered (there is none until PR 2's audit): the tag, the
// entry's ID and the reason.
func TestSubstituteExpiryIsLogged(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(prev)
	coord := &flag{lead: true}
	rt, _, clk := newExpiryRig(t, coord)
	rt.Scan()
	s := subFor(t, rt, "Level", 95, time.Hour, false)
	coord.lead = false
	rt.Scan()
	clk.step(-time.Hour)
	coord.lead = true
	rt.Scan() // takeover: capped
	clk.advance(time.Hour)
	rt.Scan() // expired
	logs := buf.String()
	id := fmt.Sprintf("id=%d", s.ID)
	for _, want := range []string{
		"substitute expired", "substitute=Level", id, "reason=" + SubstituteReasonExpired,
		"capped at the granted duration", "reason=" + SubstituteReasonClockCapped,
	} {
		if !strings.Contains(logs, want) {
			t.Fatalf("the journal lacks %q:\n%s", want, logs)
		}
	}
}

// Acknowledging an entry that never waited for one acknowledges nothing:
// no change, no event.
func TestAckOfAnEntryNotAckOnStart(t *testing.T) {
	rt, _, _ := newExpiryRig(t, nil)
	rt.Scan()
	got := make(chan SubstituteEvent, 8)
	defer rt.OnSubstituteEvent(func(e SubstituteEvent) {
		if e.Kind == SubstituteEventAck {
			got <- e
		}
	})()
	s := subFor(t, rt, "Level", 95, time.Hour, false)
	if acked := rt.AckSubstitutes([]uint64{s.ID}, "op"); len(acked) != 0 {
		t.Fatalf("acked %+v; the entry was not ack-on-start", acked)
	}
	if l := rt.Substitutes(); len(l) != 1 || l[0].Acked {
		t.Fatalf("Substitutes() = %+v; want it not marked acked", l)
	}
	select {
	case e := <-got:
		t.Fatalf("a spurious ack event: %+v", e)
	case <-time.After(50 * time.Millisecond):
	}
}

// Only a leader starts a substitute: one a standby took would be hidden
// from the leader's list and stop, yet survive a takeover and go live
// later. A replicated entry brings its own ID and is every replica's to
// apply.
func TestStandbyRefusesAStartButAppliesAReplicatedEntry(t *testing.T) {
	coord := &flag{lead: true}
	rt, _, _ := newExpiryRig(t, coord)
	rt.Scan()
	coord.lead = false
	rt.Scan()
	if _, err := rt.Substitute("Level", SubstituteRequest{Value: 95.0, For: time.Hour, By: "op"}); err == nil || !strings.Contains(err.Error(), "not leading") {
		t.Fatalf("a standby's start: %v; want it refused", err)
	}
	if rt.Tags().SubstituteCount() != 0 {
		t.Fatal("a standby holds a substitute it refused")
	}
	if _, err := rt.Substitute("Level", SubstituteRequest{Value: 95.0, For: time.Hour, By: "op", ID: 41}); err != nil {
		t.Fatalf("a replicated entry on a standby: %v", err)
	}
	coord.lead = true
	rt.Scan() // takeover
	if _, err := rt.Substitute("Flow", SubstituteRequest{Value: 1.0, For: time.Hour, By: "op"}); err != nil {
		t.Fatalf("the new leader's start: %v", err)
	}
}
