package runtime

import (
	"errors"
	"strings"
	"sync"
	"testing"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/retain"
)

// The push rules (struct wiring, #1458 slice 4; instance_push.go,
// docs/design/one-struct-model.md §2.3): which of an instance's to: and both:
// leaves a driver is handed, and when.

// pushDriver delivers in, records every push, asks for every output on cue,
// and fails a write on cue. A peer (echo) takes what it is handed for a key
// it delivers, so its next delivery carries it.
type pushDriver struct {
	mu     sync.Mutex
	in     nio.Values
	pushes []nio.Values
	want   bool
	fail   bool
	echo   bool
	// during runs inside WriteOutputs, once, without the driver's lock.
	during func()
}

func (d *pushDriver) ReadInputs() (nio.Values, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make(nio.Values, len(d.in))
	for k, v := range d.in {
		out[k] = v
	}
	return out, nil
}

func (d *pushDriver) WriteOutputs(v nio.Values) error {
	d.mu.Lock()
	if f := d.during; f != nil {
		d.during = nil
		d.mu.Unlock()
		f()
		d.mu.Lock()
	}
	defer d.mu.Unlock()
	c := make(nio.Values, len(v))
	for k, x := range v {
		c[k] = x
	}
	d.pushes = append(d.pushes, c)
	if d.fail {
		return errors.New("the device did not answer")
	}
	if d.echo {
		for k, x := range v {
			if _, ok := d.in[k]; ok {
				d.in[k] = x
			}
		}
	}
	return nil
}

func (d *pushDriver) OutputsWanted() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	w := d.want
	d.want = false
	return w
}

func (d *pushDriver) set(k string, v any) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.in == nil {
		d.in = nio.Values{}
	}
	d.in[k] = v
}

func (d *pushDriver) ask() {
	d.mu.Lock()
	d.want = true
	d.mu.Unlock()
}

// count is the number of pushes so far.
func (d *pushDriver) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.pushes)
}

// since lists what each push after the first n handed over under key.
func (d *pushDriver) since(n int, key string) []any {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []any
	for _, p := range d.pushes[n:] {
		if v, ok := p[key]; ok {
			out = append(out, v)
		}
	}
	return out
}

// last is the latest push.
func (d *pushDriver) last(t *testing.T) nio.Values {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.pushes) == 0 {
		t.Fatal("nothing was pushed")
	}
	return d.pushes[len(d.pushes)-1]
}

// pushLead is a coordinator whose leadership a test flips.
type pushLead struct{ lead bool }

func (l *pushLead) IsLeader() bool { return l.lead }

type pushRig struct {
	r    *Runtime
	d    *pushDriver
	lead *pushLead
}

func newPushRig(t *testing.T, body string, def InstanceDef, coord bool, opts ...func(*Options)) *pushRig {
	t.Helper()
	p := &pushRig{d: &pushDriver{}}
	o := Options{
		Program:   "PROGRAM Main\nVAR_EXTERNAL P101 : Pump; END_VAR\n" + body + "\nEND_PROGRAM",
		Libraries: []string{pumpLib},
		Driver:    p.d,
		Tags:      []TagDef{InstanceTag("P101", "Pump", def)},
	}
	if coord {
		p.lead = &pushLead{lead: true}
		o.Coordinator = p.lead
	}
	for _, f := range opts {
		f(&o)
	}
	var err error
	if p.r, err = New(o); err != nil {
		t.Fatal(err)
	}
	return p
}

// takeover makes the runtime a standby for one scan and leader again: a
// new leadership.
func (p *pushRig) takeover() {
	p.lead.lead = false
	p.r.Scan()
	p.lead.lead = true
	p.r.Scan()
}

func wantAbsent(t *testing.T, got nio.Values, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if v, ok := got[k]; ok {
			t.Errorf("%s handed over (%v): the rule keeps it back", k, v)
		}
	}
}

func wantHanded(t *testing.T, got nio.Values, key string, want any) {
	t.Helper()
	v, ok := got[key]
	if !ok {
		t.Errorf("%s not handed over, want %v", key, want)
		return
	}
	if v != want {
		t.Errorf("%s handed over as %v, want %v", key, v, want)
	}
}

// A to: leaf nothing has written is not handed over at start, whatever it
// holds; a write of the very value it holds (the seed) is a write, and goes
// out. An instance with no output tags beside it still pushes.
func TestPushUnwrittenCommandNotHandedAtStart(t *testing.T) {
	p := newPushRig(t, "", pumpDef(), false)
	p.r.Scan()
	wantAbsent(t, p.d.last(t), "P101.Cmd.Start", "P101.Cmd.Stop", "P101.Cmd.SpeedRef")
	write(t, p.r, "P101.Cmd.Start", false) // the seed itself
	p.r.Scan()
	wantHanded(t, p.d.last(t), "P101.Cmd.Start", false)
	wantAbsent(t, p.d.last(t), "P101.Cmd.SpeedRef")
	// Memory and readings are never pushed.
	wantAbsent(t, p.d.last(t), "P101.RunHours", "P101.Speed", "P101")
}

// A program's assignment is a write, even of the value the leaf holds, and
// only the leaves it assigned are written.
func TestPushProgramAssignmentIsAWrite(t *testing.T) {
	p := newPushRig(t, "P101.Cmd.Start := P101.Cmd.Start;", pumpDef(), false)
	p.r.Scan()
	wantHanded(t, p.d.last(t), "P101.Cmd.Start", false)
	wantAbsent(t, p.d.last(t), "P101.Cmd.SpeedRef", "P101.Cmd.Stop")
}

// A write the API took before the first scan of a plain start stands: it is
// handed over on that scan.
func TestPushWriteBeforeFirstScanStands(t *testing.T) {
	p := newPushRig(t, "", pumpDef(), false)
	write(t, p.r, "P101.Cmd.SpeedRef", 30.0)
	p.r.Scan()
	wantHanded(t, p.d.last(t), "P101.Cmd.SpeedRef", 30.0)
}

// After a takeover, a command nothing has written in the new leadership is
// not handed over, though the store still holds the old one; written again,
// it goes out.
func TestPushUnwrittenCommandNotHandedAfterTakeover(t *testing.T) {
	p := newPushRig(t, "", pumpDef(), true)
	p.r.Scan()
	write(t, p.r, "P101.Cmd.SpeedRef", 30.0)
	p.r.Scan()
	wantHanded(t, p.d.last(t), "P101.Cmd.SpeedRef", 30.0)
	n := p.d.count()
	p.takeover()
	if p.d.count() == n {
		t.Fatal("the takeover pushed nothing")
	}
	wantPath(t, p.r, "P101.Cmd.SpeedRef", 30.0)
	wantAbsent(t, p.d.last(t), "P101.Cmd.SpeedRef")
	write(t, p.r, "P101.Cmd.SpeedRef", 30.0)
	p.r.Scan()
	wantHanded(t, p.d.last(t), "P101.Cmd.SpeedRef", 30.0)
}

// A member listed whole is pushed leaf by leaf, each by its own rule.
func TestPushWholeMemberSplitsIntoLeaves(t *testing.T) {
	def := InstanceDef{Members: map[string]MemberDef{"Speed": {Kind: MemberFrom}, "Cmd": {Kind: MemberTo}}}
	p := newPushRig(t, "P101.Cmd.Start := TRUE;", def, false)
	p.r.Scan()
	got := p.d.last(t)
	wantHanded(t, got, "P101.Cmd.Start", true)
	wantAbsent(t, got, "P101.Cmd", "P101.Cmd.Stop", "P101.Cmd.SpeedRef")
	write(t, p.r, "P101.Cmd", map[string]any{"SpeedRef": 12.0})
	p.r.Scan()
	got = p.d.last(t)
	wantHanded(t, got, "P101.Cmd.SpeedRef", 12.0)
	wantAbsent(t, got, "P101.Cmd.Stop")
}

// An empty object names no member: it writes nothing and hands nothing over.
func TestPushEmptyObjectWritesNothing(t *testing.T) {
	p := newPushRig(t, "", pumpDef(), false)
	p.r.Scan()
	write(t, p.r, "P101.Cmd", map[string]any{})
	p.r.Scan()
	wantAbsent(t, p.d.last(t), "P101.Cmd.Start", "P101.Cmd.Stop", "P101.Cmd.SpeedRef")
}

// A force of a command goes out, as a forced output does.
func TestPushForcedCommandGoesOut(t *testing.T) {
	p := newPushRig(t, "", pumpDef(), false)
	p.r.Scan()
	if err := p.r.Tags().Force("P101.Cmd.Start", false); err != nil {
		t.Fatal(err)
	}
	p.r.Scan()
	wantHanded(t, p.d.last(t), "P101.Cmd.Start", false)
}

// eventProg drives Cmd.Stop (an event: leaf) from a memory member and
// assigns it every scan, as a block that writes its pin each call does.
const eventProg = "P101.Cmd.Stop := P101.RunHours > 0.0;"

// An event: leaf assigned TRUE every scan is not sent again after the driver
// asks for every output (a gate that reopened), nor on a later push for
// another output; a change off that baseline goes out.
func TestPushEventNotResentAfterReopen(t *testing.T) {
	p := newPushRig(t, eventProg, pumpDef(), false)
	p.r.Scan() // the baseline: FALSE
	wantAbsent(t, p.d.last(t), "P101.Cmd.Stop")
	write(t, p.r, "P101.RunHours", 1.0)
	p.r.Scan()
	wantHanded(t, p.d.last(t), "P101.Cmd.Stop", true)

	p.d.ask() // the gate reopened: the driver forgot what it sent
	n := p.d.count()
	p.r.Scan()
	if p.d.count() == n {
		t.Fatal("the driver asked and nothing was pushed")
	}
	wantAbsent(t, p.d.last(t), "P101.Cmd.Stop")
	write(t, p.r, "P101.Cmd.SpeedRef", 5.0) // another output moves
	p.r.Scan()
	wantHanded(t, p.d.last(t), "P101.Cmd.SpeedRef", 5.0)
	if got := p.d.since(n, "P101.Cmd.Stop"); len(got) != 0 {
		t.Fatalf("the held TRUE went out again after the reopen: %v", got)
	}
	write(t, p.r, "P101.RunHours", 0.0)
	p.r.Scan()
	wantHanded(t, p.d.last(t), "P101.Cmd.Stop", false)
	write(t, p.r, "P101.RunHours", 1.0)
	p.r.Scan()
	wantHanded(t, p.d.last(t), "P101.Cmd.Stop", true)
}

// A change made on the very scan the driver asks is a change: the baseline
// is what the leaf showed at the push before, not what it shows now.
func TestPushEventEdgeOnTheAskScanGoesOut(t *testing.T) {
	p := newPushRig(t, eventProg, pumpDef(), false)
	p.r.Scan()
	write(t, p.r, "P101.RunHours", 1.0)
	p.d.ask()
	p.r.Scan()
	wantHanded(t, p.d.last(t), "P101.Cmd.Stop", true)
}

// The first value an event: leaf shows after a takeover is a baseline.
func TestPushEventBaselineAfterTakeover(t *testing.T) {
	p := newPushRig(t, eventProg, pumpDef(), true)
	p.r.Scan()
	write(t, p.r, "P101.RunHours", 1.0)
	p.r.Scan()
	wantHanded(t, p.d.last(t), "P101.Cmd.Stop", true)
	n := p.d.count()
	p.takeover()
	p.r.Scan()
	if got := p.d.since(n, "P101.Cmd.Stop"); len(got) != 0 {
		t.Fatalf("the held TRUE went out again after the takeover: %v", got)
	}
}

// A push that failed is retried with the event: change in it: the driver
// never had it, so it is not a replay.
func TestPushEventRetriedAfterAFailedWrite(t *testing.T) {
	p := newPushRig(t, eventProg, pumpDef(), false)
	p.r.Scan()
	write(t, p.r, "P101.RunHours", 1.0)
	p.d.fail = true
	p.r.Scan()
	p.d.fail = false
	p.r.Scan()
	wantHanded(t, p.d.last(t), "P101.Cmd.Stop", true)
}

// bothProg assigns the both: member Valve while Note says so.
const bothProg = "IF T1.Note > 0 THEN T1.Valve := 99.0; END_IF;"

// A both: leaf is never handed over before its delivery in this
// leadership; an outside write then is refused, naming the cause, and a
// program's assignment is replaced by the delivery. After the delivery a
// write goes out, though the next scan's read (before its push) still shows
// the peer's old value; once the peer moves off it, it is the peer's again,
// and not written back, on a reopen's full push either.
func TestPushBothRules(t *testing.T) {
	d := &pushDriver{echo: true}
	r, _ := newTankRuntime(t, func(o *Options) {
		o.Program = "PROGRAM Main\nVAR_EXTERNAL T1 : Tank; END_VAR\n" + bothProg + "\nEND_PROGRAM"
		o.Driver = d
	})
	write(t, r, "T1.Note", 1)
	r.Scan()
	wantPath(t, r, "T1.Valve", 99.0)
	wantAbsent(t, d.last(t), "T1.Valve")
	err := writeErr(r, "T1.Valve", 20.0)
	if !errors.Is(err, ErrNotDelivered) {
		t.Fatalf("a write before the delivery = %v, want ErrNotDelivered", err)
	}
	if !strings.Contains(err.Error(), "T1.Valve has not been read from the peer yet") {
		t.Errorf("the refusal does not name the member and the cause: %v", err)
	}
	if err := writeErr(r, "T1", map[string]any{"Valve": 20.0}); !errors.Is(err, ErrNotDelivered) {
		t.Errorf("an object write before the delivery = %v, want ErrNotDelivered", err)
	}

	write(t, r, "T1.Note", 0)
	d.set("T1.Valve", 12.0)
	r.Scan()
	wantPath(t, r, "T1.Valve", 12.0)
	wantAbsent(t, d.last(t), "T1.Valve")

	write(t, r, "T1.Valve", 20.0)
	r.Scan() // reads the peer's 12, then pushes the command
	wantHanded(t, d.last(t), "T1.Valve", 20.0)
	r.Scan() // the peer took it
	wantPath(t, r, "T1.Valve", 20.0)

	n := d.count()
	d.set("T1.Valve", 30.0) // another client of the peer wrote it
	r.Scan()
	wantPath(t, r, "T1.Valve", 30.0)
	if got := d.since(n, "T1.Valve"); len(got) != 0 {
		t.Errorf("the peer's own new value was written back to it: %v", got)
	}
	d.ask()
	r.Scan()
	wantAbsent(t, d.last(t), "T1.Valve")
}

// After a takeover a both: leaf waits for its delivery again.
func TestPushBothWaitsForDeliveryAfterTakeover(t *testing.T) {
	lead := &pushLead{lead: true}
	d := &pushDriver{echo: true}
	r, _ := newTankRuntime(t, func(o *Options) { o.Coordinator = lead; o.Driver = d })
	d.set("T1.Valve", 12.0)
	r.Scan()
	write(t, r, "T1.Valve", 20.0)
	r.Scan()
	wantHanded(t, d.last(t), "T1.Valve", 20.0)
	d.mu.Lock()
	delete(d.in, "T1.Valve")
	d.mu.Unlock()
	lead.lead = false
	r.Scan()
	lead.lead = true
	r.Scan()
	wantAbsent(t, d.last(t), "T1.Valve")
	if err := writeErr(r, "T1.Valve", 21.0); !errors.Is(err, ErrNotDelivered) {
		t.Errorf("a write in the new leadership before its delivery = %v, want ErrNotDelivered", err)
	}
}

// adoptDef gives Cmd.SpeedRef a read-back of its own type and the event:
// Cmd.Stop one too.
func adoptDef() InstanceDef {
	d := pumpDef()
	d.Members["Cmd.Stop"] = MemberDef{Kind: MemberTo, Event: true, Readback: "Running"}
	return d
}

// An unwritten command with a read-back of its own type adopts the
// read-back's value at its first delivery in the leadership, as a value and
// not a command; an event: command never adopts; a written one keeps its
// own; a later delivery adopts nothing.
func TestPushReadbackAdoption(t *testing.T) {
	p := newPushRig(t, "", adoptDef(), true)
	p.d.set("P101.Speed", 42.0)
	p.d.set("P101.Running", true)
	p.r.Scan()
	wantPath(t, p.r, "P101.Cmd.SpeedRef", 42.0)
	wantPath(t, p.r, "P101.Cmd.Stop", false)
	wantAbsent(t, p.d.last(t), "P101.Cmd.SpeedRef", "P101.Cmd.Stop")
	p.d.set("P101.Speed", 50.0)
	p.r.Scan()
	wantPath(t, p.r, "P101.Cmd.SpeedRef", 42.0)

	// A new leadership adopts again, at its first delivery.
	p.takeover()
	wantPath(t, p.r, "P101.Cmd.SpeedRef", 50.0)
	wantAbsent(t, p.d.last(t), "P101.Cmd.SpeedRef")

	// Written before the delivery: its own value stands.
	q := newPushRig(t, "", adoptDef(), false)
	write(t, q.r, "P101.Cmd.SpeedRef", 30.0)
	q.d.set("P101.Speed", 42.0)
	q.r.Scan()
	wantPath(t, q.r, "P101.Cmd.SpeedRef", 30.0)
	wantHanded(t, q.d.last(t), "P101.Cmd.SpeedRef", 30.0)
}

// A read-back of another type is not adopted (WRITTEN() is the guard for
// that case, §10).
func TestPushReadbackOfAnotherTypeNotAdopted(t *testing.T) {
	d := pumpDef()
	d.Members["Cmd.SpeedRef"] = MemberDef{Kind: MemberTo, Readback: "Running"}
	p := newPushRig(t, "", d, false)
	p.d.set("P101.Running", true)
	p.r.Scan()
	wantPath(t, p.r, "P101.Cmd.SpeedRef", 0.0)
}

// A retain file that holds an instance brings none of its commands back:
// an instance is never retained, so a restart cannot replay one.
func TestPushCommandNeverComesBackFromRetain(t *testing.T) {
	st := &memStore{st: retain.State{Tags: map[string]any{
		"P101":           map[string]any{"Cmd": map[string]any{"Start": true, "SpeedRef": 77.0}},
		"P101.Cmd.Start": true,
	}}}
	p := newPushRig(t, "", pumpDef(), false, func(o *Options) { o.Retain = st })
	p.r.Scan()
	wantPath(t, p.r, "P101.Cmd.Start", false)
	wantPath(t, p.r, "P101.Cmd.SpeedRef", 0.0)
	wantAbsent(t, p.d.last(t), "P101.Cmd.Start", "P101.Cmd.SpeedRef")
}

// The push leaves are found once, in New, with the delivery keys' spelling.
func TestPushKeysMirrorDeliveryKeys(t *testing.T) {
	p := newPushRig(t, "", pumpDef(), false)
	in := p.r.tags.instanceOf("P101")
	var got []string
	for _, l := range in.push {
		got = append(got, l.key)
	}
	want := []string{"P101.Cmd.Start", "P101.Cmd.Stop", "P101.Cmd.SpeedRef"}
	if len(got) != len(want) {
		t.Fatalf("push keys %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("push key %d = %s, want %s", i, got[i], want[i])
		}
	}
	if p.r.tags.instPush != 3 {
		t.Errorf("instPush = %d, want 3", p.r.tags.instPush)
	}
}

// A leadership resumed on the same term is no new leadership (its commands
// stand), but every output goes out again: an event: leaf takes a new
// baseline there, so its held TRUE is not sent twice.
func TestPushResumedLeadershipRebasesEvents(t *testing.T) {
	co := &keepTerm{}
	co.lead.Store(true)
	co.term.Store(4)
	p := newPushRig(t, eventProg, pumpDef(), false, func(o *Options) { o.Coordinator = co })
	p.r.Scan()
	write(t, p.r, "P101.Cmd.SpeedRef", 30.0)
	write(t, p.r, "P101.RunHours", 1.0)
	p.r.Scan()
	wantHanded(t, p.d.last(t), "P101.Cmd.Stop", true)
	co.lead.Store(false)
	p.r.Scan()
	n := p.d.count()
	co.lead.Store(true)
	p.r.Scan()
	if p.d.count() == n {
		t.Fatal("the resumed leadership pushed nothing")
	}
	wantHanded(t, p.d.last(t), "P101.Cmd.SpeedRef", 30.0)
	wantAbsent(t, p.d.last(t), "P101.Cmd.Stop")
}

// namedPush is a pushDriver a Multi can route to.
type namedPush struct {
	*pushDriver
	outs []string
}

func (n namedPush) InputNames() []string  { return nil }
func (n namedPush) OutputNames() []string { return n.outs }

// A driver swapped in is handed every output, but no event: command it was
// never handed: its held value is the new baseline.
func TestPushDriverSwapRebasesEvents(t *testing.T) {
	outs := []string{"P101.Cmd.Stop", "P101.Cmd.Start"}
	a := namedPush{&pushDriver{}, outs}
	m, err := nio.NewMulti(nio.NamedDriver{Name: "a", Driver: a})
	if err != nil {
		t.Fatal(err)
	}
	p := newPushRig(t, eventProg+"\nP101.Cmd.Start := TRUE;", pumpDef(), false, func(o *Options) { o.Driver = m })
	p.r.Scan()
	write(t, p.r, "P101.RunHours", 1.0)
	p.r.Scan()
	wantHanded(t, a.last(t), "P101.Cmd.Stop", true)
	b := namedPush{&pushDriver{}, outs}
	if _, err := p.r.ReplaceDriver(t.Context(), "a", b); err != nil {
		t.Fatal(err)
	}
	p.r.Scan()
	wantHanded(t, b.last(t), "P101.Cmd.Start", true)
	wantAbsent(t, b.last(t), "P101.Cmd.Stop")
	write(t, p.r, "P101.RunHours", 0.0)
	p.r.Scan()
	wantHanded(t, b.last(t), "P101.Cmd.Stop", false)
}

// bothRig is a tank whose peer (echo) takes what it is handed, with Valve
// delivered at 12 and the program assigning Valve := 99 while Note > 0.
func bothRig(t *testing.T) (*Runtime, *pushDriver) {
	t.Helper()
	d := &pushDriver{echo: true}
	r, _ := newTankRuntime(t, func(o *Options) {
		o.Program = "PROGRAM Main\nVAR_EXTERNAL T1 : Tank; END_VAR\n" + bothProg + "\nEND_PROGRAM"
		o.Driver = d
	})
	d.set("T1.Valve", 12.0)
	r.Scan()
	return r, d
}

// A program that assigns its command every scan hands it again once
// another client moved the peer off it: the same value, but a new command.
func TestPushBothProgramCommandReassertedAfterPeerMoved(t *testing.T) {
	r, d := bothRig(t)
	write(t, r, "T1.Note", 1)
	r.Scan()
	r.Scan()
	wantHanded(t, d.last(t), "T1.Valve", 99.0)
	n := d.count()
	d.set("T1.Valve", 30.0)
	r.Scan()
	if got := d.since(n, "T1.Valve"); len(got) != 1 || got[0] != 99.0 {
		t.Fatalf("after the peer moved to 30 the program's 99 was handed %v, want once", got)
	}
	r.Scan()
	r.Scan()
	if got := d.since(n, "T1.Valve"); len(got) != 1 {
		t.Errorf("a steady command was handed again: %v", got)
	}
}

// A write that repeats the command already handed is no new command: once
// the peer moves off it, it is the peer's, and a reopen does not write the
// stale copy back.
func TestPushBothRepeatedWriteIsNoNewCommand(t *testing.T) {
	r, d := bothRig(t)
	write(t, r, "T1.Valve", 20.0)
	r.Scan()
	r.Scan()
	write(t, r, "T1.Valve", 20.0)
	n := d.count()
	d.set("T1.Valve", 30.0)
	r.Scan()
	d.ask()
	r.Scan()
	if got := d.since(n, "T1.Valve"); len(got) != 0 {
		t.Errorf("the stale 20 was written over the peer's 30: %v", got)
	}
}

// A write that lands while the driver has the push is still a command: the
// hand-over of the older one does not clear it.
func TestPushBothWriteDuringThePushStands(t *testing.T) {
	r, d := bothRig(t)
	write(t, r, "T1.Valve", 20.0)
	d.during = func() { write(t, r, "T1.Valve", 25.0) }
	r.Scan()
	wantHanded(t, d.last(t), "T1.Valve", 20.0)
	d.set("T1.Valve", 12.0) // the peer has not applied either yet
	r.Scan()
	wantHanded(t, d.last(t), "T1.Valve", 25.0)
}

// A force of a both: member is the logic's view, as a force of an input:
// never a command, before or after its delivery.
func TestPushBothForceIsNoCommand(t *testing.T) {
	d := &pushDriver{}
	r, _ := newTankRuntime(t, func(o *Options) { o.Driver = d })
	if err := r.Tags().Force("T1.Valve", 77.0); err != nil {
		t.Fatal(err)
	}
	r.Scan()
	d.set("T1.Valve", 12.0)
	r.Scan()
	r.Scan()
	wantPath(t, r, "T1.Valve", 77.0)
	if got := d.since(0, "T1.Valve"); len(got) != 0 {
		t.Errorf("a forced both: member was handed to the peer: %v", got)
	}
}

// A both: member nested in a delivered member came with it: it is
// delivered, so it takes a write.
func TestPushBothNestedInADeliveredMember(t *testing.T) {
	def := InstanceDef{Members: map[string]MemberDef{"Sensor": {Kind: MemberFrom}, "Sensor.Offset": {Kind: MemberBoth}}}
	d := &pushDriver{}
	r, _ := newTankRuntime(t, func(o *Options) { o.Driver = d; o.Tags = []TagDef{InstanceTag("T1", "Tank", def)} })
	typ := r.tags.types[ir.NameKey("T1")]
	sv, err := ir.SetFieldTyped(ir.Zero(typ), typ, []string{"Sensor", "Offset"}, 3.0, "test")
	if err != nil {
		t.Fatal(err)
	}
	sensor, _ := valueAt(sv, []int{1})
	d.set("T1.Sensor", sensor)
	r.Scan()
	wantPath(t, r, "T1.Sensor.Offset", 3.0)
	if err := writeErr(r, "T1.Sensor.Offset", 4.0); err != nil {
		t.Fatalf("a write to the delivered nested both: member: %v", err)
	}
	r.Scan()
	wantHanded(t, d.last(t), "T1.Sensor.Offset", 4.0)
}

// An event: change whose push failed is not what the device holds: a driver
// asking next (it lost the write) gets it.
func TestPushEventFailedPushThenAsk(t *testing.T) {
	p := newPushRig(t, eventProg, pumpDef(), false)
	p.r.Scan()
	write(t, p.r, "P101.RunHours", 1.0)
	p.d.fail = true
	p.r.Scan()
	p.d.fail = false
	p.d.ask()
	p.r.Scan()
	wantHanded(t, p.d.last(t), "P101.Cmd.Stop", true)
}

// Removing a force takes its command away: the actual value under it was
// never commanded, so it is not handed over.
func TestPushUnforceUnwritesAForcedOnlyCommand(t *testing.T) {
	p := newPushRig(t, "", pumpDef(), false)
	p.r.Scan()
	if err := p.r.Tags().Force("P101.Cmd.SpeedRef", 40.0); err != nil {
		t.Fatal(err)
	}
	p.r.Scan()
	wantHanded(t, p.d.last(t), "P101.Cmd.SpeedRef", 40.0)
	p.r.Tags().Unforce("P101.Cmd.SpeedRef")
	p.r.Scan()
	wantAbsent(t, p.d.last(t), "P101.Cmd.SpeedRef")

	// Written under the force, it stays written with its own value.
	if err := p.r.Tags().Force("P101.Cmd.SpeedRef", 40.0); err != nil {
		t.Fatal(err)
	}
	write(t, p.r, "P101.Cmd.Start", true)
	p.r.Scan()
	if err := p.r.Tags().SetPath("P101.Cmd.SpeedRef", 15.0); err != nil {
		t.Fatal(err)
	}
	p.r.Tags().Unforce("P101.Cmd.SpeedRef")
	p.r.Scan()
	wantHanded(t, p.d.last(t), "P101.Cmd.SpeedRef", 15.0)
}

// The parameter import's all-or-nothing write is a write too.
func TestPushWriteSetIsAWrite(t *testing.T) {
	p := newPushRig(t, "", pumpDef(), false)
	p.r.Scan()
	for _, o := range p.r.Tags().WriteSet([]WriteOp{{Path: "P101.Cmd.SpeedRef", Value: 30.0}}, "", false) {
		if o.Err != nil {
			t.Fatal(o.Err)
		}
	}
	p.r.Scan()
	wantHanded(t, p.d.last(t), "P101.Cmd.SpeedRef", 30.0)
}
