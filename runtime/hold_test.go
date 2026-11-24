//go:build !js

package runtime

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
)

// The takeover hold (#710 (b2), hold.go): after a takeover the new leader
// scans with its outputs held, writes only the safe values, and pushes
// everything once the program (or an operator) releases.

const holdMain = `PROGRAM Main
VAR_EXTERNAL Go : BOOL; Ready : BOOL; Valve : REAL; Pump : BOOL; END_VAR
VAR held : BOOL; n : DINT; END_VAR
held := TAKEOVER_HOLD();
n := n + 1;
Valve := 42.0;
Pump := TRUE;
IF Ready THEN Go := TRUE; END_IF;
END_PROGRAM`

// holdDriver records what the runtime sends it.
type holdDriver struct {
	mu          sync.Mutex
	ready       bool
	pushes      []nio.Values
	safe        []nio.Values
	forgets     int
	deliver     bool // safe values land at once
	undelivered []string
	wanted      atomic.Bool
}

func (d *holdDriver) ReadInputs() (nio.Values, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return nio.Values{"Ready": d.ready}, nil
}

func (d *holdDriver) WriteOutputs(v nio.Values) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.pushes = append(d.pushes, cloneValues(v))
	return nil
}

func (d *holdDriver) InputNames() []string  { return []string{"Ready"} }
func (d *holdDriver) OutputNames() []string { return []string{"Valve", "Pump"} }
func (d *holdDriver) OutputsWanted() bool   { return d.wanted.Swap(false) }

func (d *holdDriver) WriteSafe(v nio.Values) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.safe = append(d.safe, cloneValues(v))
	d.undelivered = nil
	if !d.deliver {
		for k := range v {
			d.undelivered = append(d.undelivered, k)
		}
	}
	return nil
}

func (d *holdDriver) SafeUndelivered() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.undelivered)
}

func (d *holdDriver) ForgetOutputs() {
	d.mu.Lock()
	d.forgets++
	d.mu.Unlock()
}

func (d *holdDriver) land() {
	d.mu.Lock()
	d.undelivered = nil
	d.mu.Unlock()
}

func (d *holdDriver) setReady(b bool) {
	d.mu.Lock()
	d.ready = b
	d.mu.Unlock()
}

func (d *holdDriver) counts() (pushes, safe, forgets int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.pushes), len(d.safe), d.forgets
}

func (d *holdDriver) lastPush() nio.Values {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.pushes) == 0 {
		return nil
	}
	return d.pushes[len(d.pushes)-1]
}

func cloneValues(v nio.Values) nio.Values {
	out := make(nio.Values, len(v))
	for k, x := range v {
		out[k] = x
	}
	return out
}

func holdOptions(clk Clock, d *holdDriver, coord Coordinator) Options {
	return Options{
		Program: holdMain,
		Tags: []TagDef{
			State("Go", false),
			Input("Ready", Init(false)),
			Output("Valve", Init(0.0)),
			Output("Pump", Init(false)),
		},
		Inputs:      []string{"Ready"},
		Outputs:     []string{"Valve", "Pump"},
		Driver:      d,
		Scan:        time.Hour,
		Clock:       clk,
		Retain:      &memStore{},
		Coordinator: coord,
	}
}

// holdRig is a leader runtime, its standby with a takeover hold, and the
// standby's shadow of the leader's images.
type holdRig struct {
	lead, stby *Runtime
	lc, sc     *imgClock
	coord      *keepTerm
	ld, sd     *holdDriver
	sh         *Shadow
	img        *Image
	maxAge     time.Duration // the flush's hot-max-age: 0 makes it cold
	events     chan HoldEvent
}

func newHoldRig(t *testing.T, cfg *TakeoverHold) *holdRig {
	t.Helper()
	r := &holdRig{
		lc:     &imgClock{t: time.Unix(1_700_000_000, 0)},
		sc:     &imgClock{t: time.Unix(1_700_003_600, 0)},
		coord:  &keepTerm{},
		ld:     &holdDriver{},
		sd:     &holdDriver{},
		maxAge: 10 * time.Second,
		events: make(chan HoldEvent, 64),
	}
	var err error
	if r.lead, err = New(holdOptions(r.lc, r.ld, nil)); err != nil {
		t.Fatal(err)
	}
	if r.stby, err = New(holdOptions(r.sc, r.sd, r.coord)); err != nil {
		t.Fatal(err)
	}
	if cfg != nil {
		if err := r.stby.SetTakeoverHold(cfg, func(ev HoldEvent) { r.events <- ev }); err != nil {
			t.Fatal(err)
		}
	}
	r.coord.takeover.Store(true)
	r.sh = r.stby.NewShadow()
	r.stby.SetTakeoverFlush(func() (*HotImage, error) {
		h := r.sh.Take()
		r.sh.Reset()
		if h != nil {
			h.MaxAge = r.maxAge
		}
		return h, nil
	})
	return r
}

// stream runs the leader a few scans and hands one image (term 4) to the
// standby's shadow; edit, when set, changes the image first.
func (r *holdRig) stream(t *testing.T, edit func(*Image)) {
	t.Helper()
	for range 3 {
		r.lc.step(100 * time.Millisecond)
		r.lead.Scan()
	}
	r.lead.RequestImage()
	r.lc.step(100 * time.Millisecond)
	r.lead.Scan()
	r.img = &Image{}
	if !r.lead.TakeImage(r.img) {
		t.Fatal("no image")
	}
	r.img.Term, r.img.Build = 4, "b"
	if edit != nil {
		edit(r.img)
	}
	if rep, err := r.sh.Apply(r.img); err != nil || !rep.InSync() {
		t.Fatalf("shadow apply: %+v, %v", rep, err)
	}
}

// takeOver makes the standby lead in term 4 and scans until it has taken
// over (its first scan as leader done).
func (r *holdRig) takeOver(t *testing.T) TakeoverReport {
	t.Helper()
	r.coord.term.Store(4)
	r.coord.lead.Store(true)
	deadline := time.Now().Add(10 * time.Second)
	for {
		r.sc.step(100 * time.Millisecond)
		r.stby.Scan()
		if _, lead := r.stby.LeadingTerm(); lead {
			return r.stby.LastTakeover()
		}
		if time.Now().After(deadline) {
			t.Fatal("the standby never took over")
		}
		time.Sleep(time.Millisecond)
	}
}

// scan runs one standby scan d after the last.
func (r *holdRig) scan(d time.Duration) {
	r.sc.step(d)
	r.stby.Scan()
}

// held is what the program's TAKEOVER_HOLD() answered on its last scan.
func (r *holdRig) held(t *testing.T) bool {
	t.Helper()
	return local(t, r.stby.TaskProgram(MainTaskName), "held").B
}

// next is the next hold event of kind, failing on any other in between.
// Events reach on from a goroutine of their own: it waits a little.
func (r *holdRig) next(t *testing.T, kind HoldEventKind) HoldEvent {
	t.Helper()
	select {
	case ev := <-r.events:
		if ev.Kind != kind {
			t.Fatalf("hold event %+v, want %s", ev, kind)
		}
		return ev
	case <-time.After(2 * time.Second):
		t.Fatalf("no hold event, want %s", kind)
		return HoldEvent{}
	}
}

func (r *holdRig) none(t *testing.T) {
	t.Helper()
	select {
	case ev := <-r.events:
		t.Fatalf("hold event %+v, want none", ev)
	case <-time.After(50 * time.Millisecond):
	}
}

var holdCfg = TakeoverHold{Release: "Go", Max: 5 * time.Second, Safe: nio.Values{"Valve": 0.0}}

// A cold takeover holds: the program scans with TAKEOVER_HOLD() TRUE and
// live inputs, nothing is pushed, the safe value is written once, and the
// driver forgets what it sent. The program's release pushes the whole
// output set on that very scan, and TAKEOVER_HOLD() is FALSE from the
// next. hold_never_starts, hold_drivers_remember and hold_safe_unsent turn
// it red.
func TestTakeoverHoldHoldsUntilTheProgramReleases(t *testing.T) {
	cfg := holdCfg
	r := newHoldRig(t, &cfg)
	r.maxAge = 0 // cold
	r.stream(t, nil)
	if rep := r.takeOver(t); rep.Hot {
		t.Fatal("the takeover went hot; the test needs a cold one")
	}
	ev := r.next(t, HoldStarted)
	if ev.Inherited || ev.Age != 0 {
		t.Fatalf("start event %+v, want a new hold of age 0", ev)
	}
	if !r.held(t) {
		t.Fatal("TAKEOVER_HOLD() FALSE on the first scan after a cold takeover")
	}
	for range 3 {
		r.scan(100 * time.Millisecond)
	}
	pushes, safe, forgets := r.sd.counts()
	if pushes != 0 {
		t.Fatalf("%d pushes while the hold held: %v", pushes, r.sd.lastPush())
	}
	if safe != 1 || forgets != 1 {
		t.Fatalf("safe writes %d, forgets %d; want 1 and 1", safe, forgets)
	}
	if got := r.sd.safe[0]; len(got) != 1 || got["Valve"] != 0.0 {
		t.Fatalf("safe write %v, want Valve 0", got)
	}
	if st := r.stby.HoldStatus(); !st.Holding || st.Age < 300*time.Millisecond {
		t.Fatalf("status %+v, want holding for 300 ms or more", st)
	}

	r.sd.setReady(true) // the program releases on this scan
	r.scan(100 * time.Millisecond)
	if ev := r.next(t, HoldReleased); ev.By != HoldByProgram {
		t.Fatalf("release event %+v, want by the program", ev)
	}
	p := r.sd.lastPush()
	if len(p) != 2 || p["Valve"] != 42.0 || p["Pump"] != true {
		t.Fatalf("the releasing scan pushed %v, want every output (Valve 42, Pump TRUE)", p)
	}
	r.scan(100 * time.Millisecond)
	if r.held(t) || r.stby.HoldStatus().Holding {
		t.Fatal("still held after the release")
	}
}

// A hot takeover from a leader that was not holding does not hold under
// on: cold; with on: always it does, and the release tag the leader's
// program had set TRUE (carried by the hot takeover) is FALSE at the
// hold's start, so it does not release at once. A start (the coordinator
// says no takeover) never holds. hold_release_tag_kept turns it red.
func TestTakeoverHoldWhenItHolds(t *testing.T) {
	for _, c := range []struct {
		name           string
		always, hot    bool
		takeover, hold bool
	}{
		{"hot, on cold", false, true, true, false},
		{"hot, on always", true, true, true, true},
		{"cold, a start", false, false, false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			cfg := holdCfg
			cfg.Always = c.always
			r := newHoldRig(t, &cfg)
			r.coord.takeover.Store(c.takeover)
			if !c.hot {
				r.maxAge = 0
			}
			r.ld.setReady(true) // the leader's program released: Go TRUE, carried hot
			r.stream(t, nil)
			if rep := r.takeOver(t); rep.Hot != c.hot {
				t.Fatalf("hot %v, want %v (%s)", rep.Hot, c.hot, rep.Reason)
			}
			r.scan(100 * time.Millisecond)
			if got := r.held(t); got != c.hold {
				t.Fatalf("TAKEOVER_HOLD() %v on the second scan, want %v", got, c.hold)
			}
			goTag, _ := r.stby.Tags().ReadGlobal("Go")
			if c.hold && goTag.B {
				t.Fatal("the release tag is TRUE in a hold: the carried TRUE was not cleared at its start")
			}
			if c.hot && !c.hold && !goTag.B {
				t.Fatal("Go not carried by the hot takeover (the test needs the carried TRUE)")
			}
			if pushes, _, _ := r.sd.counts(); (pushes == 0) != c.hold {
				t.Fatalf("%d pushes in two scans, holding %v", pushes, c.hold)
			}
		})
	}
}

// A hot takeover from a leader that was still holding inherits its hold
// (on: cold too), with its age: the max is not restarted by passing the
// lead across. hold_not_inherited and hold_age_restarts turn it red.
func TestTakeoverHoldInheritedWithItsAge(t *testing.T) {
	cfg := holdCfg
	r := newHoldRig(t, &cfg)
	r.stream(t, func(img *Image) { img.Holding, img.HoldAge = true, 3*time.Second })
	if rep := r.takeOver(t); !rep.Hot {
		t.Fatalf("cold takeover (%s); the test needs a hot one", rep.Reason)
	}
	ev := r.next(t, HoldStarted)
	if !ev.Inherited || ev.Age < 3*time.Second {
		t.Fatalf("start event %+v, want inherited, 3 s old or more", ev)
	}
	if !r.held(t) {
		t.Fatal("TAKEOVER_HOLD() FALSE after a hot takeover from a holding leader")
	}
	r.scan(2 * time.Second) // 5 s and more since the hold began on the old leader
	r.next(t, HoldAtMax)
	if pushes, _, _ := r.sd.counts(); pushes != 0 {
		t.Fatal("pushed while the inherited hold held")
	}
}

// At max the hold raises its alarm and STAYS held (naut-lead's Q4 ruling):
// nothing is pushed, a reminder follows every further max. Only the
// program or an operator releases; ReleaseHold names the operator, and
// the next scan pushes everything. hold_no_max turns it red.
func TestTakeoverHoldStaysHeldAtMax(t *testing.T) {
	cfg := holdCfg
	r := newHoldRig(t, &cfg)
	r.sd.deliver = true
	r.maxAge = 0
	r.stream(t, nil)
	r.takeOver(t)
	r.next(t, HoldStarted)
	r.scan(5 * time.Second)
	r.next(t, HoldAtMax)
	if st := r.stby.HoldStatus(); !st.Holding || !st.AtMax {
		t.Fatalf("status %+v, want holding at max", st)
	}
	r.scan(time.Second)
	r.none(t)
	r.scan(4 * time.Second)
	r.next(t, HoldReminder)
	if pushes, _, _ := r.sd.counts(); pushes != 0 {
		t.Fatal("pushed at max: the hold must stay held")
	}
	if !r.held(t) {
		t.Fatal("TAKEOVER_HOLD() FALSE at max")
	}
	if err := r.stby.ReleaseHold("user:akhan"); err != nil {
		t.Fatal(err)
	}
	if ev := r.next(t, HoldReleased); ev.By != "user:akhan" {
		t.Fatalf("release event %+v, want by user:akhan", ev)
	}
	r.scan(100 * time.Millisecond)
	if p := r.sd.lastPush(); len(p) != 2 {
		t.Fatalf("the scan after an operator's release pushed %v, want every output", p)
	}
	if err := r.stby.ReleaseHold("user:akhan"); !errors.Is(err, ErrNotHolding) {
		t.Fatalf("a second release: %v, want ErrNotHolding", err)
	}
}

// on-max: release is the opt-in fail-open: at max the alarm, then the
// release, by "max", on that scan; the status keeps the alarm raised a
// while, so the alarm engine sees it. hold_release_at_max_ignored turns it
// red.
func TestTakeoverHoldReleasesAtMaxWhenOptedIn(t *testing.T) {
	cfg := holdCfg
	cfg.ReleaseAtMax = true
	r := newHoldRig(t, &cfg)
	r.sd.deliver = true
	r.maxAge = 0
	r.stream(t, nil)
	r.takeOver(t)
	r.next(t, HoldStarted)
	r.scan(5 * time.Second)
	r.next(t, HoldAtMax)
	if ev := r.next(t, HoldReleased); ev.By != HoldByMax {
		t.Fatalf("release %+v, want by max", ev)
	}
	if p := r.sd.lastPush(); len(p) != 2 {
		t.Fatalf("pushed %v at the release, want every output", p)
	}
	// The alarm must see it (a polled level): raised for a while after.
	if st := r.stby.HoldStatus(); st.Holding || !st.ReleasedAtMax {
		t.Fatalf("status %+v right after the release at max, want released and its alarm raised", st)
	}
	r.scan(11 * time.Second)
	if st := r.stby.HoldStatus(); st.ReleasedAtMax {
		t.Fatal("the release-at-max alarm is still raised 11 s on")
	}
}

// Only the program writes the release tag: an external write and a force
// are refused, naming the operator's way out. hold_release_tag_writable
// turns it red.
func TestTakeoverHoldReleaseTagIsTheProgramsOnly(t *testing.T) {
	cfg := holdCfg
	r := newHoldRig(t, &cfg)
	tags := r.stby.Tags()
	if err := tags.Write([]WriteOp{{Path: "Go", Value: true}})[0]; !errors.Is(err, ErrReadOnly) {
		t.Fatalf("an external write of the release tag: %v, want ErrReadOnly", err)
	}
	if out := tags.WriteSet([]WriteOp{{Path: "go", Value: true}}, "user:x", false); !errors.Is(out[0].Err, ErrReadOnly) {
		t.Fatalf("a set write of the release tag: %v, want ErrReadOnly", out[0].Err)
	}
	if err := tags.Force("Go", true); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("a force of the release tag: %v, want ErrReadOnly", err)
	}
	if err := r.stby.SetTakeoverHold(&TakeoverHold{Release: "Valve", Max: time.Second}, nil); err == nil {
		t.Fatal("a REAL release tag accepted")
	}
}

// A safe value not delivered within SafeAckWait raises its event (and the
// status flag), cleared once it lands; a driver that asks for its outputs
// again gets the safe values again, not the program's. hold_safe_unsent
// turns it red.
func TestTakeoverHoldSafeValueDelivery(t *testing.T) {
	cfg := holdCfg
	cfg.Max = time.Minute
	r := newHoldRig(t, &cfg)
	r.maxAge = 0
	r.stream(t, nil)
	r.takeOver(t)
	r.next(t, HoldStarted)
	r.scan(time.Second)
	r.none(t)
	r.scan(time.Second)
	if ev := r.next(t, HoldSafeUndelivered); !slices.Equal(ev.Undelivered, []string{"Valve"}) {
		t.Fatalf("undelivered %v, want [Valve]", ev.Undelivered)
	}
	if st := r.stby.HoldStatus(); !st.SafeAlarm || !slices.Equal(st.Undelivered, []string{"Valve"}) {
		t.Fatalf("status %+v, want the safe alarm on Valve", st)
	}
	r.sd.wanted.Store(true)
	r.scan(100 * time.Millisecond)
	if pushes, safe, _ := r.sd.counts(); pushes != 0 || safe != 2 {
		t.Fatalf("after the driver asked: %d pushes, %d safe writes; want 0 and 2", pushes, safe)
	}
	r.sd.land()
	r.scan(100 * time.Millisecond)
	r.next(t, HoldSafeDelivered)
	if st := r.stby.HoldStatus(); st.SafeAlarm {
		t.Fatal("the safe alarm stays after delivery")
	}
}

// A holding leader's image says so, with its age, and the wire carries it
// (format 3); a format 2 image carries no hold. hold_not_imaged turns it
// red.
func TestTakeoverHoldRidesInTheImage(t *testing.T) {
	cfg := holdCfg
	r := newHoldRig(t, &cfg)
	r.maxAge = 0
	r.stream(t, nil)
	r.takeOver(t)
	r.scan(time.Second)
	r.stby.RequestImage()
	r.scan(100 * time.Millisecond)
	img := &Image{}
	if !r.stby.TakeImage(img) {
		t.Fatal("no image from the new leader")
	}
	if !img.Holding || img.HoldAge < time.Second {
		t.Fatalf("image hold %v %s, want holding a second or more", img.Holding, img.HoldAge)
	}
	b, err := EncodeImage(nil, img)
	if err != nil {
		t.Fatal(err)
	}
	var back Image
	if err := DecodeImage(b, &back); err != nil {
		t.Fatal(err)
	}
	if !back.Holding || back.HoldAge != img.HoldAge.Truncate(time.Millisecond) {
		t.Fatalf("decoded hold %v %s, want %v %s", back.Holding, back.HoldAge, img.Holding, img.HoldAge)
	}
	b2, err := EncodeImageFormat(nil, img, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := DecodeImage(b2, &back); err != nil || back.Holding {
		t.Fatalf("format 2: holding %v, err %v; want no hold", back.Holding, err)
	}
	if !slices.Contains(ImageFormats, 3) || ImageFormatFor(ImageFormats) != 3 {
		t.Fatalf("formats %v: a leader of this build sends 3 to a standby of this build", ImageFormats)
	}
}

// recChannel answers at once and records the writes that reach it.
type recChannel struct {
	mu     sync.Mutex
	writes int
}

func (c *recChannel) Read(context.Context, string, ir.Value, string) (ir.Value, error) {
	return ir.Value{}, nil
}

func (c *recChannel) Write(context.Context, string, ir.Value, string) error {
	c.mu.Lock()
	c.writes++
	c.mu.Unlock()
	return nil
}

func (c *recChannel) Close() error { return nil }

func (c *recChannel) count() int { c.mu.Lock(); defer c.mu.Unlock(); return c.writes }

// A WRITE the program posts while the hold holds never reaches the channel,
// even when the hold has released by the time it is due; a READ does, and
// a WRITE posted after the release does. hold_channel_writes turns it red.
func TestTakeoverHoldRefusesChannelWrites(t *testing.T) {
	ch := &recChannel{}
	h := newCommHub([]Channel{{Name: "peer", Channel: ch}})
	defer h.close()
	var held atomic.Bool
	held.Store(true)
	h.setHeld(held.Load)
	wait := func(ticket int64) ir.CommResult {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			if res, done := h.Poll(ticket); done {
				return res
			}
			if time.Now().After(deadline) {
				t.Fatal("the request never finished")
			}
			time.Sleep(time.Millisecond)
		}
	}
	tk, code := h.PostWrite("peer", "x", ir.BoolVal(true), "")
	if code != ir.CommOK {
		t.Fatalf("post: %d", code)
	}
	if res := wait(tk); res.Status != ir.CommLost || ch.count() != 0 {
		t.Fatalf("a write while held: status %d, %d writes sent; want lost, none", res.Status, ch.count())
	}
	rd, _ := h.PostRead("peer", "x", ir.BoolVal(false), "")
	if res := wait(rd); res.Status != 0 {
		t.Fatalf("a read while held: status %d, want 0", res.Status)
	}
	held.Store(false)
	tk, _ = h.PostWrite("peer", "x", ir.BoolVal(true), "")
	if res := wait(tk); res.Status != 0 || ch.count() != 1 {
		t.Fatalf("a write after the release: status %d, %d sent; want 0, 1", res.Status, ch.count())
	}
}

// HoldStatus never waits for leadMu: the pair's alarms read it under the
// alarm engine's lock, which a takeover takes under leadMu (Fable M1).
func TestTakeoverHoldStatusTakesNoLeadMu(t *testing.T) {
	cfg := holdCfg
	r := newHoldRig(t, &cfg)
	r.stby.leadMu.Lock()
	defer r.stby.leadMu.Unlock()
	done := make(chan struct{})
	go func() { r.stby.HoldStatus(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("HoldStatus waits for leadMu")
	}
}

// A re-send of the safe values (the driver asked) starts its own 2 s wait:
// no alarm the moment it is sent again late in a hold (Fable S3). A driver
// swapped in during the hold gets them on the next scan (S4).
// hold_replace_unsafe turns it red.
func TestTakeoverHoldSafeResendAndReplace(t *testing.T) {
	cfg := holdCfg
	cfg.Max = time.Minute
	r := newHoldRig(t, &cfg)
	r.sd.deliver = true
	r.maxAge = 0
	r.stream(t, nil)
	r.takeOver(t)
	r.next(t, HoldStarted)
	r.scan(3 * time.Second)
	r.sd.deliver = false // the next write does not land at once
	r.sd.wanted.Store(true)
	r.scan(100 * time.Millisecond)
	r.none(t)
	if st := r.stby.HoldStatus(); st.SafeAlarm {
		t.Fatal("the safe alarm raised the moment a late re-send went out")
	}
	r.scan(2 * time.Second)
	r.next(t, HoldSafeUndelivered)
	r.sd.land()
	r.scan(100 * time.Millisecond)
	r.next(t, HoldSafeDelivered)

	_, before, _ := r.sd.counts()
	r.stby.holdDriverReplaced()
	r.scan(100 * time.Millisecond)
	if _, after, _ := r.sd.counts(); after != before+1 {
		t.Fatalf("safe writes %d after a driver swap, want %d", after, before+1)
	}
}

// A push that raced a hold's start (a task's gate took over between the
// scan's look and its push) is forgotten at once (Fable N1).
func TestTakeoverHoldForgetsARacedPush(t *testing.T) {
	cfg := holdCfg
	r := newHoldRig(t, &cfg)
	r.stby.outSent.Store(true)
	r.stby.holdAfterPush()
	if _, _, f := r.sd.counts(); f != 0 || !r.stby.outSent.Load() {
		t.Fatal("forgot with no hold holding")
	}
	r.stby.tags.red.hold.Store(true)
	r.stby.holdAfterPush()
	if _, _, f := r.sd.counts(); f != 1 || r.stby.outSent.Load() {
		t.Fatalf("forgets %d, outSent %v after a raced push; want 1, false", f, r.stby.outSent.Load())
	}
}

// The hold survives a same-term resume (no takeover: nothing restarts), and
// a step-down then a lease won in a new term is a new takeover: a new hold.
func TestTakeoverHoldAcrossAResumeAndANewTerm(t *testing.T) {
	cfg := holdCfg
	cfg.Max = time.Minute
	r := newHoldRig(t, &cfg)
	r.sd.deliver = true
	r.maxAge = 0
	r.stream(t, nil)
	r.takeOver(t)
	r.next(t, HoldStarted)
	r.scan(time.Second)
	r.coord.lead.Store(false)
	r.scan(100 * time.Millisecond)
	r.coord.lead.Store(true) // the same term: a resume
	r.scan(100 * time.Millisecond)
	r.scan(100 * time.Millisecond)
	r.none(t)
	if st := r.stby.HoldStatus(); !st.Holding || st.Age < time.Second || !r.held(t) {
		t.Fatalf("after a resume: %+v, held %v; want the same hold going on", st, r.held(t))
	}
	if pushes, _, _ := r.sd.counts(); pushes != 0 {
		t.Fatal("pushed across a resume while held")
	}
	r.coord.lead.Store(false)
	r.scan(100 * time.Millisecond)
	r.coord.term.Store(5)
	r.coord.lead.Store(true)
	// The takeover's flush runs on its own goroutine: real time, as takeOver.
	deadline := time.Now().Add(10 * time.Second)
	for {
		r.scan(100 * time.Millisecond)
		if term, lead := r.stby.LeadingTerm(); lead && term == 5 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no takeover in term 5")
		}
		time.Sleep(time.Millisecond)
	}
	if ev := r.next(t, HoldStarted); ev.Age != 0 {
		t.Fatalf("a new term's hold %+v, want a new one", ev)
	}
}

// A hot takeover from a holding leader holds even when the coordinator
// does not call it a takeover (Fable N5): a hold the program has not
// released is never dropped.
func TestTakeoverHoldInheritedWhateverTheCoordinatorSays(t *testing.T) {
	cfg := holdCfg
	r := newHoldRig(t, &cfg)
	r.coord.takeover.Store(false)
	r.stream(t, func(img *Image) { img.Holding, img.HoldAge = true, time.Second })
	if rep := r.takeOver(t); !rep.Hot {
		t.Fatalf("cold (%s); the test needs hot", rep.Reason)
	}
	if !r.held(t) {
		t.Fatal("a holding leader's hold dropped: the coordinator called it a start")
	}
}

// blockChannel answers a READ only when let go, and records the writes
// that reach it.
type blockChannel struct {
	recChannel
	gate chan struct{}
}

func (c *blockChannel) Read(ctx context.Context, v string, s ir.Value, e string) (ir.Value, error) {
	<-c.gate
	return ir.Value{}, nil
}

// A WRITE posted while the hold holds is refused even when it comes due
// after the release (it waited behind a READ): the program computed it
// held. hold_channel_writes turns it red.
func TestTakeoverHoldRefusesAWritePostedHeld(t *testing.T) {
	ch := &blockChannel{gate: make(chan struct{})}
	h := newCommHub([]Channel{{Name: "peer", Channel: ch}})
	defer h.close()
	var held atomic.Bool
	held.Store(true)
	h.setHeld(held.Load)
	rd, _ := h.PostRead("peer", "x", ir.BoolVal(false), "")
	wr, _ := h.PostWrite("peer", "x", ir.BoolVal(true), "")
	held.Store(false)
	close(ch.gate)
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, rdone := h.Poll(rd)
		res, wdone := h.Poll(wr)
		if rdone && wdone {
			if res.Status != ir.CommLost || ch.count() != 0 {
				t.Fatalf("a write posted held, due released: status %d, %d sent; want lost, none", res.Status, ch.count())
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the requests never finished")
		}
		time.Sleep(time.Millisecond)
	}
}
