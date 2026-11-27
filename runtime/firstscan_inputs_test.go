package runtime_test

import (
	"context"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/retain"
	"github.com/joyautomation/nautilus/runtime"
)

// lateDriver delivers nothing until deliver is called: the field bus has
// not answered yet (a driver connecting at start). It is an
// io.InputsReadier, ready from its first delivery on. It records every
// push.
type lateDriver struct {
	mu     sync.Mutex
	in     nio.Values // nil: nothing delivered yet
	pushes []nio.Values
	ready  atomic.Bool
}

func (d *lateDriver) deliver(v nio.Values) {
	d.mu.Lock()
	d.in = v
	d.mu.Unlock()
	d.ready.Store(true)
}

func (d *lateDriver) InputsReady() bool { return d.ready.Load() }

func (d *lateDriver) ReadInputs() (nio.Values, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := nio.Values{}
	for k, v := range d.in {
		out[k] = v
	}
	return out, nil
}

func (d *lateDriver) WriteOutputs(v nio.Values) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	cp := nio.Values{}
	for k, x := range v {
		cp[k] = x
	}
	d.pushes = append(d.pushes, cp)
	return nil
}

func (d *lateDriver) pushCount() int { d.mu.Lock(); defer d.mu.Unlock(); return len(d.pushes) }

// #786: a standalone controller's first scan does not run before its
// drivers have delivered the inputs: no logic, no outputs. Before the fix
// the runtime scanned on the seeds and drove Out from them (Out = 5).
func TestFirstScanWaitsForInputs(t *testing.T) {
	drv := &lateDriver{}
	rt := newStandbyRuntime(t, drv, nil)

	rt.Scan()
	rt.Scan()
	if n := drv.pushCount(); n != 0 {
		t.Fatalf("before any input was delivered the runtime pushed %d times: %v", n, drv.pushes)
	}
	if n := rt.Stats().Count; n != 0 {
		t.Errorf("held ticks counted as scans: Count = %d", n)
	}

	drv.deliver(nio.Values{"Level": 10.0, "Flow": 1.0})
	rt.Scan()
	if n := drv.pushCount(); n != 1 {
		t.Fatalf("after delivery: %d pushes, want 1", n)
	}
	if got := drv.pushes[0]["Out"]; got != 31.0 {
		t.Errorf("first push Out = %v, want 31 (Level 10 * SP 3 + Flow 1)", got)
	}
	if n := rt.Stats().Count; n != 1 {
		t.Errorf("Count = %d after the first real scan, want 1", n)
	}
}

// A driver that cannot say (Memory, a simulation, acceptance tests) is
// ready at once: the first tick scans, as before #786.
func TestFirstScanDoesNotWaitForADriverThatCannotSay(t *testing.T) {
	rt := newStandbyRuntime(t, nio.NewMemory(), nil, func(o *runtime.Options) { o.InputsWait = time.Hour })
	rt.Scan()
	if n := rt.Stats().Count; n != 1 {
		t.Fatalf("Count = %d after one tick with a Memory driver, want 1", n)
	}
	if w := rt.Stats().InputsWait; w != nil {
		t.Errorf("Stats().InputsWait = %+v with nothing to wait for", w)
	}
}

const firstScanProgram = `PROGRAM F
VAR_EXTERNAL
  Level : REAL;
  Out : REAL;
  Firsts : DINT;
  FirstLevel : REAL;
  Runs : DINT;
  Et : DINT;
  DtSeen : REAL;
  Dt : REAL;
END_VAR
VAR
  tmr : TON;
END_VAR
IF FIRST_SCAN() THEN
  Firsts := Firsts + 1;
  FirstLevel := Level;
  DtSeen := Dt;
END_IF;
Runs := Runs + 1;
tmr(IN := TRUE, PT := T#10s);
Et := TIME_TO_DINT(tmr.ET);
Out := Level;
END_PROGRAM
`

const firstScanTask = `PROGRAM Tk
VAR_EXTERNAL
  TaskRuns : DINT;
END_VAR
TaskRuns := TaskRuns + 1;
END_PROGRAM
`

// stepClock is a virtual Clock a test moves by hand.
type stepClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *stepClock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *stepClock) add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

func newFirstScanRuntime(t *testing.T, drv nio.Driver, clk runtime.Clock, more ...func(*runtime.Options)) *runtime.Runtime {
	t.Helper()
	o := runtime.Options{
		Program: firstScanProgram,
		Driver:  drv,
		Inputs:  []string{"Level"},
		Outputs: []string{"Out"},
		DtTag:   "Dt",
		Scan:    100 * time.Millisecond,
		Seed: nio.Values{"Level": 1.0, "Out": 0.0, "Firsts": int64(0), "FirstLevel": 0.0,
			"Runs": int64(0), "Et": int64(0), "DtSeen": 0.0, "TaskRuns": int64(0)},
		Tasks:      []runtime.Task{{Name: "side", Program: firstScanTask, Scan: 100 * time.Millisecond}},
		Clock:      clk,
		InputsWait: time.Hour,
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

func tagNum(t *testing.T, rt *runtime.Runtime, name string) float64 {
	t.Helper()
	v, err := rt.Tags().ReadGlobal(name)
	if err != nil {
		t.Fatal(err)
	}
	if v.Kind == ir.TypeReal {
		return v.F
	}
	return float64(v.I)
}

// The lead's conditions on #786: FIRST_SCAN() is TRUE on the first real
// scan, once, on live inputs; a held tick runs no program, no task and no
// observer, and counts as no scan; the hold is no elapsed time to a TON;
// the first real scan's dt is a normal cycle, not the hold.
func TestFirstScanHoldIsInvisibleToTheProgram(t *testing.T) {
	drv := &lateDriver{}
	clk := &stepClock{t: time.Unix(1_700_000_000, 0)}
	rt := newFirstScanRuntime(t, drv, clk)
	var observed atomic.Int32
	rt.OnScan(func(*runtime.Tags) { observed.Add(1) })

	for range 20 { // two seconds of held ticks on the program's clock
		rt.Scan()
		if err := rt.ScanTask("side"); err != nil {
			t.Fatal(err)
		}
		clk.add(100 * time.Millisecond)
	}
	if n := tagNum(t, rt, "Runs"); n != 0 {
		t.Fatalf("the program ran %v times while held", n)
	}
	if n := tagNum(t, rt, "TaskRuns"); n != 0 {
		t.Errorf("the task ran %v times while held", n)
	}
	if n := observed.Load(); n != 0 {
		t.Errorf("OnScan observers fired %d times while held", n)
	}
	st := rt.Stats()
	if st.Count != 0 || len(st.Tasks) != 1 || st.Tasks[0].Count != 0 {
		t.Errorf("held ticks counted: main %d, tasks %+v", st.Count, st.Tasks)
	}
	if st.Lateness.Late != 0 || st.Lateness.Overruns != 0 || st.Lateness.Missed != 0 || st.Lateness.LastUs != 0 {
		t.Errorf("held ticks reached the lateness diagnostics: %+v", st.Lateness)
	}

	drv.deliver(nio.Values{"Level": 7.0})
	rt.Scan()
	if got := tagNum(t, rt, "Firsts"); got != 1 {
		t.Errorf("FIRST_SCAN() TRUE %v times on the first real scan, want 1", got)
	}
	if got := tagNum(t, rt, "FirstLevel"); got != 7 {
		t.Errorf("the first scan saw Level %v, want the delivered 7 (not the seed 1)", got)
	}
	if got := tagNum(t, rt, "Et"); got != 0 {
		t.Errorf("TON ET = %v ms on the first real scan: the hold counted as elapsed time", got)
	}
	if got := tagNum(t, rt, "DtSeen"); got != 0.1 {
		t.Errorf("the first real scan's dt = %v s, want the 0.1 s cycle", got)
	}
	clk.add(100 * time.Millisecond)
	rt.Scan()
	if got := tagNum(t, rt, "Firsts"); got != 1 {
		t.Errorf("FIRST_SCAN() TRUE again on the second scan (Firsts = %v)", got)
	}
	if got := tagNum(t, rt, "Et"); got != 100 {
		t.Errorf("TON ET = %v ms a cycle later, want 100", got)
	}
	if err := rt.ScanTask("side"); err != nil {
		t.Fatal(err)
	}
	if got := tagNum(t, rt, "TaskRuns"); got != 1 {
		t.Errorf("the task ran %v times after the hold, want 1", got)
	}
	if st := rt.Stats(); st.Count != 2 || st.InputsWait != nil {
		t.Errorf("after the hold: Count %d (want 2), InputsWait %+v (want none)", st.Count, st.InputsWait)
	}
}

// A driver that never delivers: past InputsWait the first scan runs, on
// what the driver holds, and a warning names the driver. While held,
// Stats says so and names it too.
func TestFirstScanWaitGivesUpWithAWarning(t *testing.T) {
	var buf syncBuf
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })

	a, b := &lateDriver{}, &namedLate{}
	b.ready.Store(true)
	set, err := nio.NewMulti(nio.NamedDriver{Name: "plc1", Driver: a}, nio.NamedDriver{Name: "io2", Driver: b})
	if err != nil {
		t.Fatal(err)
	}
	rt := newFirstScanRuntime(t, set, nil, func(o *runtime.Options) {
		o.InputsWait = 50 * time.Millisecond
		o.Tasks = nil
	})
	rt.Scan()
	w := rt.Stats().InputsWait
	if w == nil || !slices.Equal(w.WaitingFor, []string{"plc1"}) || w.Since.IsZero() {
		t.Fatalf("while held: Stats().InputsWait = %+v, want waiting for [plc1]", w)
	}
	if !strings.Contains(buf.String(), "waiting_for=plc1") {
		t.Errorf("no hold log naming plc1: %q", buf.String())
	}
	time.Sleep(80 * time.Millisecond)
	rt.Scan()
	if n := rt.Stats().Count; n != 1 {
		t.Fatalf("Count = %d past the wait, want 1: a driver that never delivers must not stop the controller", n)
	}
	if rt.Stats().InputsWait != nil {
		t.Errorf("InputsWait still reported after giving up")
	}
	if !strings.Contains(buf.String(), "level=WARN") || !strings.Contains(buf.String(), "inputs still not live") {
		t.Errorf("no WARN on giving up: %q", buf.String())
	}
}

// The same hold with a retain store (the slower gate path): nothing runs
// until the inputs arrive.
func TestFirstScanWaitsWithARetainStore(t *testing.T) {
	drv := &lateDriver{}
	rt := newFirstScanRuntime(t, drv, nil, func(o *runtime.Options) {
		o.Retain = retain.New("", filepath.Join(t.TempDir(), "retain.json"))
		o.Tasks = nil
	})
	rt.Scan()
	if n := rt.Stats().Count; n != 0 {
		t.Fatalf("Count = %d before delivery with a retain store, want 0", n)
	}
	drv.deliver(nio.Values{"Level": 4.0})
	rt.Scan()
	if got := tagNum(t, rt, "FirstLevel"); got != 4 {
		t.Errorf("first scan saw Level %v, want 4", got)
	}
}

// Held ticks keep the loop live: the liveness check (the systemd watchdog,
// the local write gate) does not read a held start as a hung controller.
func TestFirstScanHoldStaysLive(t *testing.T) {
	drv := &lateDriver{}
	rt := newFirstScanRuntime(t, drv, nil, func(o *runtime.Options) {
		o.Scan = 10 * time.Millisecond
		o.Tasks = nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go rt.Run(ctx)
	max := 100 * time.Millisecond
	deadline := time.Now().Add(600 * time.Millisecond)
	for time.Now().Before(deadline) {
		if err := rt.Liveness(time.Now(), max); err != nil {
			t.Fatalf("a held start read as not live: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if n := rt.Stats().Count; n != 0 {
		t.Fatalf("Count = %d while held", n)
	}
	drv.deliver(nio.Values{"Level": 2.0})
	for end := time.Now().Add(2 * time.Second); rt.Stats().Count == 0; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatal("no scan after the inputs arrived")
		}
	}
}

// namedLate is a second InputsReadier for a set.
type namedLate struct{ lateDriver }

func (n *namedLate) InputNames() []string  { return nil }
func (n *namedLate) OutputNames() []string { return nil }
func (d *lateDriver) InputNames() []string { return []string{"Level"} }
func (d *lateDriver) OutputNames() []string {
	return []string{"Out"}
}

// The wait is for the FIRST scan only: a lone controller that has started
// never holds again, even if a driver's answer goes back to false (a
// reconnect), so a running controller never stops scanning for it.
func TestFirstScanWaitIsOnlyAtStart(t *testing.T) {
	drv := &lateDriver{}
	rt := newFirstScanRuntime(t, drv, nil, func(o *runtime.Options) { o.Tasks = nil })
	drv.deliver(nio.Values{"Level": 3.0})
	rt.Scan()
	drv.ready.Store(false)
	rt.Scan()
	rt.Scan()
	if n := rt.Stats().Count; n != 3 {
		t.Fatalf("Count = %d, want 3: a started controller held its scans again", n)
	}
}
