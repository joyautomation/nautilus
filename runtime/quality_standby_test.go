package runtime_test

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/runtime"
)

// standbyQualityProgram has every kind of tag a standby must tell apart:
// inputs (Level, Flow), an output the program writes (Out), an internal
// tag the program writes (Calc), and a setpoint the program only reads
// (SP), which is the operator's value and not scan-fed.
const standbyQualityProgram = `PROGRAM Q
VAR_EXTERNAL
  Level : REAL;
  Flow : REAL;
  SP : REAL;
  Calc : REAL;
  Out : REAL;
END_VAR
Calc := Level * SP;
Out := Calc + Flow;
END_PROGRAM
`

// scanFed is what a standby holds frozen: everything a scan would have
// refreshed. Hand-listed from the program above, not derived from the
// runtime's own answer.
var scanFed = []string{"Level", "Flow", "Calc", "Out", "Dt"}

// standbyFlag is a Coordinator a test flips by hand (atomic: the race
// detector runs these tests).
type standbyFlag struct{ lead atomic.Bool }

func (f *standbyFlag) IsLeader() bool { return f.lead.Load() }

func newStandbyRuntime(t *testing.T, drv nio.Driver, coord runtime.Coordinator, more ...func(*runtime.Options)) *runtime.Runtime {
	t.Helper()
	o := runtime.Options{
		Program:     standbyQualityProgram,
		Driver:      drv,
		Inputs:      []string{"Level", "Flow"},
		Outputs:     []string{"Out"},
		DtTag:       "Dt",
		Seed:        nio.Values{"Level": 1.0, "Flow": 2.0, "SP": 3.0, "Calc": 0.0, "Out": 0.0},
		Coordinator: coord,
		// A takeover waits for live inputs only when the driver says;
		// keep the wait long so a test that holds it is not racing it.
		TakeoverInputsWait: time.Minute,
	}
	for _, f := range more {
		f(&o)
	}
	rt, err := runtime.New(o)
	if err != nil {
		t.Fatal(err)
	}
	return rt
}

// assertStandby checks the standby's whole answer: every scan-fed tag is
// Stale (or a driver's more specific non-Good verdict), and the setpoint,
// which nothing scans, is not reported.
func assertStandby(t *testing.T, rt *runtime.Runtime, driverSays map[string]nio.Quality) {
	t.Helper()
	q := rt.Quality()
	for _, name := range scanFed {
		want := nio.Stale
		if d, ok := driverSays[name]; ok {
			want = d
		}
		if q[name] != want {
			t.Errorf("standby Quality()[%s] = %v, want %v (all: %v)", name, q[name], want, q)
		}
		if got := rt.TagQuality(name); got != want {
			t.Errorf("standby TagQuality(%s) = %v, want %v", name, got, want)
		}
	}
	if _, said := q["SP"]; said {
		t.Errorf("standby reported the setpoint SP, which no scan writes: %v", q)
	}
	if got := rt.TagQuality("SP"); got != nio.Good {
		t.Errorf("standby TagQuality(SP) = %v, want good", got)
	}
}

// A standby never presents a frozen value as Good (#924): it does not
// scan, so its inputs and everything the program writes are as old as its
// last scan as leader. That is Stale, the same code a failed read sets.
// The test walks a leader down to standby and back: Good, Stale, Good.
func TestQualityStandbyIsStale(t *testing.T) {
	coord := &standbyFlag{}
	coord.lead.Store(true)
	rt := newStandbyRuntime(t, nio.NewMemory(), coord)

	rt.Scan()
	if q := rt.Quality(); len(q) != 0 {
		t.Fatalf("leader after a good scan reported %v, want nothing", q)
	}

	coord.lead.Store(false)
	rt.Scan() // the gate steps down; no I/O, no logic
	assertStandby(t, rt, nil)
	rt.Scan() // and stays down
	assertStandby(t, rt, nil)

	// Takeover: the first scan as leader restores the drivers' answer.
	coord.lead.Store(true)
	rt.Scan()
	if q := rt.Quality(); len(q) != 0 {
		t.Errorf("after takeover the first scan left %v, want nothing", q)
	}
	for _, name := range scanFed {
		if got := rt.TagQuality(name); got != nio.Good {
			t.Errorf("after takeover TagQuality(%s) = %v, want good", name, got)
		}
	}
}

// A replica that starts as standby and has never led: the same answer,
// before its first tick and after it.
func TestQualityStandbyFromStart(t *testing.T) {
	coord := &standbyFlag{}
	rt := newStandbyRuntime(t, nio.NewMemory(), coord)
	assertStandby(t, rt, nil)
	rt.Scan()
	assertStandby(t, rt, nil)
}

// A driver's own non-Good verdict is more specific than "standby" and
// stands; its Good does not, because the value it vouches for has not
// reached the tag store since the last scan.
func TestQualityStandbyKeepsDriverVerdict(t *testing.T) {
	drv := nio.NewMemory()
	drv.SetQuality("Level", nio.NotConnected)
	coord := &standbyFlag{}
	rt := newStandbyRuntime(t, drv, coord)
	rt.Scan()
	assertStandby(t, rt, map[string]nio.Quality{"Level": nio.NotConnected})
}

// notReady is a driver whose inputs are not live yet: a takeover holds
// (#626) and the runtime does not scan, even though the coordinator says
// it leads. Its values are still the frozen ones.
type notReady struct{ *nio.Memory }

func (notReady) InputsReady() bool { return false }

func TestQualityStaleWhileTakeoverWaitsForInputs(t *testing.T) {
	coord := &standbyFlag{}
	coord.lead.Store(true)
	rt := newStandbyRuntime(t, notReady{nio.NewMemory()}, coord)
	rt.Scan() // holds: the inputs are not live
	assertStandby(t, rt, nil)
}

// A replica with nothing driver-bound can still report Stale, so the
// capability flag must say so, or an HMI hides the badge on a standby.
func TestReportsQualityWithCoordinator(t *testing.T) {
	coord := &standbyFlag{}
	rt, err := runtime.New(runtime.Options{
		Program:     standbyQualityProgram,
		Seed:        nio.Values{"Level": 1.0, "Flow": 2.0, "SP": 3.0, "Calc": 0.0, "Out": 0.0},
		Coordinator: coord,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !rt.ReportsQuality() {
		t.Error("a replica with a Coordinator does not report quality")
	}
	// Without a driver nothing feeds Level and Flow: like SP they are
	// values someone writes, not readings a scan refreshes.
	q := rt.Quality()
	for _, name := range []string{"Calc", "Out"} {
		if q[name] != nio.Stale {
			t.Errorf("driverless standby Quality()[%s] = %v, want stale (all: %v)", name, q[name], q)
		}
	}
	for _, name := range []string{"Level", "Flow", "SP"} {
		if _, said := q[name]; said {
			t.Errorf("driverless standby reported %s, which no scan writes: %v", name, q)
		}
	}
}

// A leader whose coordinator has already handed the lease on, but whose
// scan has not run since (stalled, #858): its gate has not stepped down,
// yet its values are as frozen as a standby's. No Scan after the flip.
func TestQualityStaleOnceLeaseIsGoneBeforeTheNextScan(t *testing.T) {
	coord := &standbyFlag{}
	coord.lead.Store(true)
	rt := newStandbyRuntime(t, nio.NewMemory(), coord)
	rt.Scan()
	coord.lead.Store(false)
	assertStandby(t, rt, nil)
}

// The same leadership resumed (#621): a leader whose gate closed and opens
// again in the same term carries on without a takeover, and must not stay
// marked Stale.
func TestQualityGoodAfterSameTermResume(t *testing.T) {
	coord := &termFlag{}
	coord.term.Store(1)
	coord.lead.Store(true)
	rt := newStandbyRuntime(t, nio.NewMemory(), coord)
	rt.Scan()
	coord.lead.Store(false)
	rt.Scan()
	assertStandby(t, rt, nil)
	coord.lead.Store(true) // same term: a resume, not a takeover
	rt.Scan()
	if q := rt.Quality(); len(q) != 0 {
		t.Errorf("after a same-term resume quality = %v, want nothing", q)
	}
	if got := rt.TagQuality("Out"); got != nio.Good {
		t.Errorf("after a same-term resume TagQuality(Out) = %v, want good", got)
	}
}

// A driver that says Good out loud (chattyDriver: Level and Out Good, Flow
// Bad). Quality drops Good entries anyway, but TagQuality, which the OPC
// UA server asks per node, returns what the driver said; on a standby that
// Good must not stand.
func TestTagQualityStandbyOverridesDriverGood(t *testing.T) {
	coord := &standbyFlag{}
	rt := newStandbyRuntime(t, &chattyDriver{Memory: nio.NewMemory()}, coord)
	rt.Scan()
	assertStandby(t, rt, map[string]nio.Quality{"Flow": nio.Bad})
}

// The program spells a written tag differently from the manifest. Tag
// names are case-insensitive (#197): the quality is reported under the
// stored spelling, and TagQuality answers for either.
func TestQualityStandbyMatchesTheStoredSpelling(t *testing.T) {
	rt, err := runtime.New(runtime.Options{
		Program:     strings.ReplaceAll(standbyQualityProgram, "Calc", "CALC"),
		Driver:      nio.NewMemory(),
		Inputs:      []string{"Level", "Flow"},
		Outputs:     []string{"Out"},
		Seed:        nio.Values{"Level": 1.0, "Flow": 2.0, "SP": 3.0, "Calc": 0.0, "Out": 0.0},
		Coordinator: &standbyFlag{},
	})
	if err != nil {
		t.Fatal(err)
	}
	q := rt.Quality()
	if q["Calc"] != nio.Stale {
		t.Errorf("Quality()[Calc] = %v, want stale under the stored spelling (all: %v)", q["Calc"], q)
	}
	if _, said := q["CALC"]; said {
		t.Errorf("Quality reported the program's spelling CALC: %v", q)
	}
	for _, name := range []string{"Calc", "CALC", "calc.member"} {
		if got := rt.TagQuality(name); got != nio.Stale {
			t.Errorf("TagQuality(%s) = %v, want stale", name, got)
		}
	}
}

// gatedDriver is a Memory driver whose readiness (io.InputsReadier) and
// reads a test controls: a read can be held until the test releases it.
type gatedDriver struct {
	*nio.Memory
	ready   atomic.Bool
	mu      sync.Mutex
	hold    chan struct{} // non-nil: the next read waits on it
	entered chan struct{} // signalled when a held read starts
}

func newGatedDriver() *gatedDriver {
	d := &gatedDriver{Memory: nio.NewMemory(), entered: make(chan struct{}, 1)}
	d.ready.Store(true)
	return d
}

func (d *gatedDriver) InputsReady() bool { return d.ready.Load() }

func (d *gatedDriver) wait() {
	d.mu.Lock()
	h := d.hold
	d.mu.Unlock()
	if h != nil {
		d.entered <- struct{}{}
		<-h
	}
}

func (d *gatedDriver) ReadInputs() (nio.Values, error) { d.wait(); return d.Memory.ReadInputs() }
func (d *gatedDriver) ReadInputsInto(dst nio.Values) error {
	d.wait()
	return d.Memory.ReadInputsInto(dst)
}

// A takeover on a NEW term held for live inputs (#626): this replica led
// term 1, the lease moved on and came back as term 2 between two scans,
// and the drivers' inputs are not live. The gate refuses, nothing scans,
// and the values are as frozen as a standby's, although the coordinator
// says this replica leads and it never saw its gate close.
func TestQualityStaleWhileNewTermTakeoverWaitsForInputs(t *testing.T) {
	drv := newGatedDriver()
	coord := &termFlag{}
	coord.term.Store(1)
	coord.lead.Store(true)
	rt := newStandbyRuntime(t, drv, coord)
	rt.Scan()
	if q := rt.Quality(); len(q) != 0 {
		t.Fatalf("leader in term 1 reported %v", q)
	}
	drv.ready.Store(false)
	coord.term.Store(2)
	rt.Scan() // holds: a new term, inputs not live
	assertStandby(t, rt, nil)
}

// A takeover's first read has not come back yet. The gate has opened,
// but the tags still hold what this replica read when it last led, and
// the read-OK flag still says that old read worked. They stay Stale until
// the main scan's read lands.
func TestQualityStaleUntilTheTakeoverReadLands(t *testing.T) {
	drv := newGatedDriver()
	coord := &standbyFlag{}
	coord.lead.Store(true)
	rt := newStandbyRuntime(t, drv, coord)
	rt.Scan() // leads, reads OK
	coord.lead.Store(false)
	rt.Scan() // steps down

	release := make(chan struct{})
	drv.mu.Lock()
	drv.hold = release
	drv.mu.Unlock()
	coord.lead.Store(true)
	done := make(chan struct{})
	go func() { rt.Scan(); close(done) }()
	<-drv.entered // the takeover ran; its first read is in flight
	assertStandby(t, rt, nil)

	drv.mu.Lock()
	drv.hold = nil
	drv.mu.Unlock()
	close(release)
	<-done
	if q := rt.Quality(); len(q) != 0 {
		t.Errorf("after the takeover's read landed quality = %v, want nothing", q)
	}
}

// An additional task's scan can be the one whose gate performs the
// takeover. It reads no inputs (the main task owns the field), so the
// tags stay Stale until the main scan has read.
func TestQualityStaleWhenATaskOpensTheTakeover(t *testing.T) {
	coord := &standbyFlag{}
	coord.lead.Store(true)
	rt := newStandbyRuntime(t, nio.NewMemory(), coord, func(o *runtime.Options) {
		o.Tasks = []runtime.Task{{Name: "t", Program: `PROGRAM T
VAR_EXTERNAL TaskOut : REAL; END_VAR
TaskOut := TaskOut + 1.0;
END_PROGRAM
`}}
		o.Seed["TaskOut"] = 0.0
	})
	rt.Scan()
	if err := rt.ScanTask("t"); err != nil {
		t.Fatal(err)
	}
	coord.lead.Store(false)
	rt.Scan()
	coord.lead.Store(true)
	if err := rt.ScanTask("t"); err != nil { // this gate takes over
		t.Fatal(err)
	}
	assertStandby(t, rt, nil)
	rt.Scan()
	if q := rt.Quality(); len(q) != 0 {
		t.Errorf("after the main scan read quality = %v, want nothing", q)
	}
}

// Stepped down, then the coordinator says this replica leads again, but no
// scan has run since: nothing has been read, so the tags are still the
// frozen ones.
func TestQualityStaleWhenLeadReturnsBeforeAnyScan(t *testing.T) {
	coord := &standbyFlag{}
	coord.lead.Store(true)
	rt := newStandbyRuntime(t, nio.NewMemory(), coord)
	rt.Scan()
	coord.lead.Store(false)
	rt.Scan() // steps down
	coord.lead.Store(true)
	assertStandby(t, rt, nil) // no scan yet
}

// A new term won while this replica was leading (lost and regained between
// two scans), inputs live: the gate takes over at once, and until that
// scan's read lands the tags are the previous term's.
func TestQualityStaleWhileANewTermReadIsInFlight(t *testing.T) {
	drv := newGatedDriver()
	coord := &termFlag{}
	coord.term.Store(1)
	coord.lead.Store(true)
	rt := newStandbyRuntime(t, drv, coord)
	rt.Scan()
	release := make(chan struct{})
	drv.mu.Lock()
	drv.hold = release
	drv.mu.Unlock()
	coord.term.Store(2)
	done := make(chan struct{})
	go func() { rt.Scan(); close(done) }()
	<-drv.entered
	assertStandby(t, rt, nil)
	drv.mu.Lock()
	drv.hold = nil
	drv.mu.Unlock()
	close(release)
	<-done
	if q := rt.Quality(); len(q) != 0 {
		t.Errorf("after the new term's read landed quality = %v, want nothing", q)
	}
}

// An online edit changes which tags the program writes, and a rollback
// changes them back: the standby's Stale set follows the running program,
// not the one it booted with.
func TestQualityStandbyFollowsOnlineEdits(t *testing.T) {
	rt := newStandbyRuntime(t, nio.NewMemory(), &standbyFlag{})
	p := rt.TaskProgram("main")
	edited := `PROGRAM Q
VAR_EXTERNAL
  Level : REAL;
  Flow : REAL;
  SP : REAL;
  Calc : REAL;
  Out : REAL;
END_VAR
SP := Level;
Out := Flow;
END_PROGRAM
`
	check := func(when string, stale, notReported string) {
		t.Helper()
		q := rt.Quality()
		if q[stale] != nio.Stale {
			t.Errorf("%s: Quality()[%s] = %v, want stale (all: %v)", when, stale, q[stale], q)
		}
		if _, said := q[notReported]; said {
			t.Errorf("%s: %s, which the running program doesn't write, was reported: %v", when, notReported, q)
		}
	}
	check("as booted", "Calc", "SP")
	if _, err := p.SwapWarm(edited); err != nil {
		t.Fatal(err)
	}
	check("after the edit", "SP", "Calc")
	if _, err := p.Rollback(); err != nil {
		t.Fatal(err)
	}
	check("after the rollback", "Calc", "SP")
}

// taskOpts adds an additional task whose gate the test can drive while a
// main scan is held in its read.
func taskOpts(o *runtime.Options) {
	o.Tasks = []runtime.Task{{Name: "t", Program: `PROGRAM T
VAR_EXTERNAL TaskOut : REAL; END_VAR
TaskOut := TaskOut + 1.0;
END_PROGRAM
`}}
	o.Seed["TaskOut"] = 0.0
}

// holdMainRead starts a main scan and returns once its read is in flight;
// release lets the read finish and waits for the scan.
func holdMainRead(t *testing.T, rt *runtime.Runtime, drv *gatedDriver) (release func()) {
	t.Helper()
	ch := make(chan struct{})
	drv.mu.Lock()
	drv.hold = ch
	drv.mu.Unlock()
	done := make(chan struct{})
	go func() { rt.Scan(); close(done) }()
	<-drv.entered
	return func() {
		drv.mu.Lock()
		drv.hold = nil
		drv.mu.Unlock()
		close(ch)
		<-done
	}
}

// A main scan passed its gate as a steady leader and is in its read when
// an additional task's gate steps down; the coordinator then says "leader"
// again before any gate has run. The read that lands belongs to the old
// leadership, so it must not mark the tags current.
func TestQualityStaleWhenAStepDownRacesTheMainRead(t *testing.T) {
	drv := newGatedDriver()
	coord := &standbyFlag{}
	coord.lead.Store(true)
	rt := newStandbyRuntime(t, drv, coord, taskOpts)
	rt.Scan()
	release := holdMainRead(t, rt, drv)
	coord.lead.Store(false)
	if err := rt.ScanTask("t"); err != nil { // steps down
		t.Fatal(err)
	}
	coord.lead.Store(true) // back, but no gate has run
	release()
	assertStandby(t, rt, nil)

	rt.Scan() // this gate takes over, and its read lands
	if q := rt.Quality(); len(q) != 0 {
		t.Errorf("after the next scan quality = %v, want nothing", q)
	}
}

// The same race with a new term: during the main read the lease moves on
// and comes back as term 2, the inputs are not live, and the task's gate
// holds the takeover. Nothing of term 2 has read anything.
func TestQualityStaleWhenAHeldTakeoverRacesTheMainRead(t *testing.T) {
	drv := newGatedDriver()
	coord := &termFlag{}
	coord.term.Store(1)
	coord.lead.Store(true)
	rt := newStandbyRuntime(t, drv, coord, taskOpts)
	rt.Scan()
	release := holdMainRead(t, rt, drv)
	drv.ready.Store(false)
	coord.term.Store(2)
	if err := rt.ScanTask("t"); err != nil { // holds: new term, inputs not live
		t.Fatal(err)
	}
	release()
	assertStandby(t, rt, nil)

	drv.ready.Store(true)
	rt.Scan() // term 2's takeover runs, and its read lands
	if q := rt.Quality(); len(q) != 0 {
		t.Errorf("after term 2's first scan quality = %v, want nothing", q)
	}
}
