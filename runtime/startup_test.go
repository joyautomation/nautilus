package runtime

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
)

// The start-up hold (Options.StartupWait): the first scan waits for the
// field driver's first delivery, like a Siemens CPU's start-up waiting for
// its modules' ready messages, for at most a monitoring time.

const startupSrc = `PROGRAM Main
VAR_EXTERNAL
  DI_00 : BOOL;
  DO_00 : BOOL;
  Firsts : DINT;
  Runs : DINT;
END_VAR
IF FIRST_SCAN() THEN
  Firsts := Firsts + 1;
END_IF;
Runs := Runs + 1;
DO_00 := DI_00;
END_PROGRAM`

// startupDriver delivers DI_00 once `failFor` reads have failed, and
// counts every WriteOutputs. With image set it instead behaves like a
// driver that keeps its own image (io.DeliveryReporter): every read
// succeeds, carrying only a synthesised status tag until the image is
// marked delivered.
type startupDriver struct {
	mu       sync.Mutex
	failFor  int // reads that fail before the first delivery; -1 = never deliver
	reads    int
	writes   int
	image    bool
	imageSet bool // the image has been delivered (image mode)
}

func (d *startupDriver) ReadInputs() (nio.Values, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.reads++
	if d.image {
		if !d.imageSet {
			return nio.Values{"Dev__Online": false}, nil // a status tag: not a delivery
		}
		return nio.Values{"Dev__Online": true, "DI_00": true}, nil
	}
	if d.failFor < 0 || d.reads <= d.failFor {
		return nil, errors.New("dev: not connected yet")
	}
	return nio.Values{"DI_00": true}, nil
}

func (d *startupDriver) WriteOutputs(nio.Values) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.writes++
	return nil
}

func (d *startupDriver) counts() (reads, writes int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.reads, d.writes
}

// imageDriver is startupDriver in image mode, with the DeliveryReporter
// half: what plcnext, optommp, modbus, eip and the hw drivers implement.
type imageDriver struct{ *startupDriver }

func (d imageDriver) Delivered() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.imageSet
}

func (d imageDriver) deliver() {
	d.mu.Lock()
	d.imageSet = true
	d.mu.Unlock()
}

// manualClock is a clock the test moves by hand.
type manualClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *manualClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newStartupRuntime(t *testing.T, drv nio.Driver, wait time.Duration, clk Clock) *Runtime {
	t.Helper()
	rt, err := New(Options{
		Program:     startupSrc,
		Driver:      drv,
		Inputs:      []string{"DI_00"},
		Outputs:     []string{"DO_00"},
		Seed:        nio.Values{"Firsts": int64(0), "Runs": int64(0), "DO_00": false},
		StartupWait: wait,
		DriverName:  "dev",
		Clock:       clk,
	})
	if err != nil {
		t.Fatal(err)
	}
	return rt
}

// captureLog points slog at a buffer for the test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func dint(rt *Runtime, name string) int64 {
	v, _ := rt.Tags().ReadPath(name)
	n, _ := v.(int64)
	return n
}

// A driver that fails its first reads holds the first scan: the program
// does not run, nothing is written, the stats say it is waiting for that
// driver — and the scan after the first successful read runs the program
// with FIRST_SCAN() TRUE.
func TestStartupWaitHoldsUntilFirstDelivery(t *testing.T) {
	captureLog(t)
	clk := &manualClock{t: time.Unix(1_700_000_000, 0)}
	drv := &startupDriver{failFor: 3}
	rt := newStartupRuntime(t, drv, time.Minute, clk)

	if s := rt.Stats(); !s.WaitingForIO {
		t.Fatal("before the first scan the stats should already say waiting for I/O")
	}
	for i := 0; i < 3; i++ {
		rt.Scan()
		clk.advance(100 * time.Millisecond)
	}
	if n := dint(rt, "Runs"); n != 0 {
		t.Fatalf("the program ran %d times while the driver had not delivered", n)
	}
	if _, w := drv.counts(); w != 0 {
		t.Fatalf("%d output writes while waiting for I/O, want none", w)
	}
	s := rt.Stats()
	if !s.WaitingForIO || len(s.WaitingFor) != 1 || s.WaitingFor[0] != "dev" {
		t.Fatalf("stats while held: waitingForIO=%v waitingFor=%v, want true [dev]", s.WaitingForIO, s.WaitingFor)
	}
	if s.Count != 0 || s.LogicErrors != 0 {
		t.Fatalf("held scans counted as scans: count=%d logicErrors=%d", s.Count, s.LogicErrors)
	}
	if s.IOHealthy || !strings.Contains(s.LastIOError, "not connected") {
		t.Fatalf("the held read's failure should show: ioHealthy=%v lastIOError=%q", s.IOHealthy, s.LastIOError)
	}

	rt.Scan() // the 4th read succeeds
	if n := dint(rt, "Runs"); n != 1 {
		t.Fatalf("Runs=%d after the first delivery, want 1", n)
	}
	if n := dint(rt, "Firsts"); n != 1 {
		t.Fatalf("FIRST_SCAN() was not TRUE on the first scan that ran (Firsts=%d)", n)
	}
	if v, _ := rt.Tags().ReadPath("DO_00"); v != true {
		t.Fatalf("DO_00=%v, want the delivered DI_00 copied through", v)
	}
	if _, w := drv.counts(); w != 1 {
		t.Fatalf("%d output writes after the first scan, want 1", w)
	}
	s = rt.Stats()
	if s.WaitingForIO || s.WaitingFor != nil || s.LogicErrors != 0 || s.Count != 1 {
		t.Fatalf("after release: waitingForIO=%v waitingFor=%v logicErrors=%d count=%d", s.WaitingForIO, s.WaitingFor, s.LogicErrors, s.Count)
	}
	rt.Scan()
	if f, r := dint(rt, "Firsts"), dint(rt, "Runs"); f != 1 || r != 2 {
		t.Fatalf("Firsts=%d Runs=%d, want 1 2", f, r)
	}
}

// A driver whose reads succeed with only its own status tags in them (a
// background poller that has not heard from the field yet) is not a
// delivery: the hold waits for io.DeliveryReporter to say so.
func TestStartupWaitIgnoresStatusTagsBeforeDelivery(t *testing.T) {
	captureLog(t)
	clk := &manualClock{t: time.Unix(1_700_000_000, 0)}
	drv := imageDriver{&startupDriver{image: true}}
	rt, err := New(Options{
		Program:     startupSrc,
		Driver:      drv,
		Inputs:      []string{"DI_00", "Dev__Online"},
		Outputs:     []string{"DO_00"},
		Seed:        nio.Values{"Firsts": int64(0), "Runs": int64(0), "DO_00": false},
		StartupWait: time.Minute,
		Clock:       clk,
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		rt.Scan()
		clk.advance(100 * time.Millisecond)
	}
	if n := dint(rt, "Runs"); n != 0 {
		t.Fatalf("the program ran on status tags alone (Runs=%d)", n)
	}
	if _, w := drv.counts(); w != 0 {
		t.Fatalf("%d output writes before the delivery", w)
	}
	// The status tag itself is in the store: the HMI can show it while held.
	if v, ok := rt.Tags().ReadPath("Dev__Online"); !ok || v != false {
		t.Fatalf("Dev__Online=%v (%v), want the held delivery's false", v, ok)
	}
	if s := rt.Stats(); !s.WaitingForIO || !s.IOHealthy {
		t.Fatalf("waitingForIO=%v ioHealthy=%v, want held with a healthy read", s.WaitingForIO, s.IOHealthy)
	}

	drv.deliver()
	rt.Scan()
	if f, r := dint(rt, "Firsts"), dint(rt, "Runs"); f != 1 || r != 1 {
		t.Fatalf("after delivery Firsts=%d Runs=%d, want 1 1", f, r)
	}
	if s := rt.Stats(); s.LogicErrors != 0 {
		t.Fatalf("logic errors after delivery: %s", s.LastError)
	}
}

// A driver that never delivers: once the wait runs out (on the runtime's
// clock — no real sleeping), scanning starts anyway, one WARN names the
// driver, and from then on missing inputs fault exactly as before.
func TestStartupWaitTimesOutAndNamesTheDriver(t *testing.T) {
	logs := captureLog(t)
	clk := &manualClock{t: time.Unix(1_700_000_000, 0)}
	drv := &startupDriver{failFor: -1}
	rt := newStartupRuntime(t, drv, 2*time.Second, clk)

	rt.Scan() // starts the hold
	clk.advance(1999 * time.Millisecond)
	rt.Scan()
	if n := dint(rt, "Runs"); n != 0 {
		t.Fatalf("ran before the wait was up (Runs=%d)", n)
	}
	clk.advance(time.Millisecond)
	rt.Scan() // 2 s: the wait is over
	if s := rt.Stats(); s.WaitingForIO || s.Count != 1 {
		t.Fatalf("after the timeout: waitingForIO=%v count=%d, want a real scan", s.WaitingForIO, s.Count)
	}
	// DI_00 was never delivered: reading it faults, as it always has.
	if s := rt.Stats(); s.LogicErrors != 1 || !strings.Contains(s.LastError, "DI_00") {
		t.Fatalf("logicErrors=%d lastError=%q, want the undefined DI_00 fault", s.LogicErrors, s.LastError)
	}
	out := logs.String()
	if n := strings.Count(out, "did not deliver within the start-up wait"); n != 1 {
		t.Fatalf("%d timeout WARNs, want exactly 1:\n%s", n, out)
	}
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "drivers=[dev]") {
		t.Fatalf("the WARN should name the driver:\n%s", out)
	}
	for i := 0; i < 3; i++ {
		clk.advance(100 * time.Millisecond)
		rt.Scan()
	}
	if n := strings.Count(logs.String(), "did not deliver within the start-up wait"); n != 1 {
		t.Fatalf("the WARN repeated (%d times)", n)
	}
	// Late delivery after the timeout is ordinary I/O: nothing restarts.
	drv.mu.Lock()
	drv.failFor = 0
	drv.mu.Unlock()
	clk.advance(100 * time.Millisecond)
	rt.Scan()
	if v, _ := rt.Tags().ReadPath("DO_00"); v != true {
		t.Fatalf("DO_00=%v after the late delivery, want true", v)
	}
}

// Timeout 0 is today's behaviour: the first Scan runs the program, and a
// missing input faults it.
func TestStartupWaitZeroDoesNotWait(t *testing.T) {
	logs := captureLog(t)
	drv := &startupDriver{failFor: 2}
	rt := newStartupRuntime(t, drv, 0, nil)
	if rt.Stats().WaitingForIO {
		t.Fatal("waitingForIO with StartupWait 0")
	}
	rt.Scan()
	s := rt.Stats()
	if s.Count != 1 || s.LogicErrors != 1 || !strings.Contains(s.LastError, "undefined tag DI_00") {
		t.Fatalf("count=%d logicErrors=%d lastError=%q, want today's per-scan fault", s.Count, s.LogicErrors, s.LastError)
	}
	if r, _ := drv.counts(); r != 1 {
		t.Fatalf("%d reads on the first scan, want 1", r)
	}
	if strings.Contains(logs.String(), "start-up wait") || strings.Contains(logs.String(), "waiting for field I/O") {
		t.Fatalf("StartupWait 0 logged a hold:\n%s", logs.String())
	}
}

// No driver, or a driver with no inputs: nothing to wait for, whatever
// the wait is set to.
func TestStartupWaitSkippedWithoutDriverOrInputs(t *testing.T) {
	captureLog(t)
	rt, err := New(Options{
		Program:     startupSrc,
		Seed:        nio.Values{"Firsts": int64(0), "Runs": int64(0), "DI_00": true, "DO_00": false},
		StartupWait: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	rt.Scan()
	if r := dint(rt, "Runs"); r != 1 || rt.Stats().WaitingForIO {
		t.Fatalf("no driver: Runs=%d waitingForIO=%v, want 1 false", r, rt.Stats().WaitingForIO)
	}

	drv := &startupDriver{failFor: -1}
	rt, err = New(Options{
		Program:     startupSrc,
		Driver:      drv,
		Outputs:     []string{"DO_00"},
		Seed:        nio.Values{"Firsts": int64(0), "Runs": int64(0), "DI_00": true, "DO_00": false},
		StartupWait: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	rt.Scan()
	if r := dint(rt, "Runs"); r != 1 || rt.Stats().WaitingForIO {
		t.Fatalf("no inputs: Runs=%d waitingForIO=%v, want 1 false", r, rt.Stats().WaitingForIO)
	}
}

// A healthy driver delivers on the first read, so the hold costs nothing:
// the very first Scan runs the program. This is the path every simulation
// takes — the acceptance harness's memory driver, a replay recording.
func TestStartupWaitHealthyDriverRunsFirstScan(t *testing.T) {
	logs := captureLog(t)
	drv := nio.NewMemory()
	_ = drv.WriteOutputs(nio.Values{"DI_00": true})
	rt := newStartupRuntime(t, drv, time.Hour, nil)
	rt.Scan()
	if f, r := dint(rt, "Firsts"), dint(rt, "Runs"); f != 1 || r != 1 {
		t.Fatalf("Firsts=%d Runs=%d, want 1 1", f, r)
	}
	if strings.Contains(logs.String(), "waiting for field I/O") {
		t.Fatalf("a first-read delivery logged a wait:\n%s", logs.String())
	}
}

// Additional tasks are held too: no program in the resource runs before
// the main task's driver has delivered.
func TestStartupWaitHoldsAdditionalTasks(t *testing.T) {
	captureLog(t)
	drv := &startupDriver{failFor: 1}
	rt, err := New(Options{
		Program: startupSrc,
		Tasks: []Task{{Name: "slow", Program: `PROGRAM Slow
VAR_EXTERNAL SlowRuns : DINT; END_VAR
SlowRuns := SlowRuns + 1;
END_PROGRAM`}},
		Driver:      drv,
		Inputs:      []string{"DI_00"},
		Outputs:     []string{"DO_00"},
		Seed:        nio.Values{"Firsts": int64(0), "Runs": int64(0), "DO_00": false, "SlowRuns": int64(0)},
		StartupWait: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.ScanTask("slow"); err != nil {
		t.Fatal(err)
	}
	rt.Scan() // held: the first read fails
	if err := rt.ScanTask("slow"); err != nil {
		t.Fatal(err)
	}
	if n := dint(rt, "SlowRuns"); n != 0 {
		t.Fatalf("a task ran %d times during the hold", n)
	}
	rt.Scan() // delivered
	if err := rt.ScanTask("slow"); err != nil {
		t.Fatal(err)
	}
	if n := dint(rt, "SlowRuns"); n != 1 {
		t.Fatalf("SlowRuns=%d after the hold, want 1", n)
	}
}

// A multi-driver set waits for every child, and names the ones it is
// still waiting for.
func TestStartupWaitMultiWaitsForEveryChild(t *testing.T) {
	logs := captureLog(t)
	clk := &manualClock{t: time.Unix(1_700_000_000, 0)}
	a := &ownedDriver{startupDriver: &startupDriver{failFor: 0}, in: []string{"DI_00"}, out: []string{"DO_00"}}
	b := &ownedDriver{startupDriver: &startupDriver{failFor: -1}, in: []string{"DI_01"}}
	m, err := nio.NewMulti(nio.NamedDriver{Name: "rack", Driver: a}, nio.NamedDriver{Name: "meter", Driver: b})
	if err != nil {
		t.Fatal(err)
	}
	rt, err := New(Options{
		Program:     startupSrc,
		Driver:      m,
		Inputs:      []string{"DI_00", "DI_01"},
		Outputs:     []string{"DO_00"},
		Seed:        nio.Values{"Firsts": int64(0), "Runs": int64(0), "DO_00": false},
		StartupWait: time.Second,
		Clock:       clk,
	})
	if err != nil {
		t.Fatal(err)
	}
	rt.Scan()
	if s := rt.Stats(); !s.WaitingForIO || len(s.WaitingFor) != 1 || s.WaitingFor[0] != "meter" {
		t.Fatalf("waitingFor=%v, want [meter] — rack has delivered", s.WaitingFor)
	}
	if _, w := a.counts(); w != 0 {
		t.Fatal("an output went to the rack during the hold")
	}
	clk.advance(time.Second)
	rt.Scan()
	if r := dint(rt, "Runs"); r != 1 {
		t.Fatalf("Runs=%d after the timeout, want 1", r)
	}
	if !strings.Contains(logs.String(), "drivers=[meter]") {
		t.Fatalf("the WARN should name meter only:\n%s", logs.String())
	}
}

// ownedDriver gives startupDriver the tag ownership io.Multi requires; its
// reads deliver its own inputs.
type ownedDriver struct {
	*startupDriver
	in, out []string
}

func (d *ownedDriver) InputNames() []string  { return d.in }
func (d *ownedDriver) OutputNames() []string { return d.out }
func (d *ownedDriver) ReadInputs() (nio.Values, error) {
	if _, err := d.startupDriver.ReadInputs(); err != nil {
		return nil, err
	}
	out := nio.Values{}
	for _, n := range d.in {
		out[n] = true
	}
	return out, nil
}
