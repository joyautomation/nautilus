package runtime_test

import (
	"context"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/modbus"
	"github.com/joyautomation/nautilus/modbus/slave"
	"github.com/joyautomation/nautilus/runtime"
)

const swapProgram = `PROGRAM Main
VAR_EXTERNAL A_In : REAL; B_In : REAL; A_Out : REAL; B_Out : REAL; END_VAR
A_Out := A_In;
B_Out := B_In * 2.0;
END_PROGRAM`

func swapDriver(ins, outs []string, in nio.Values) *askingDriver {
	return &askingDriver{scriptDriver: &scriptDriver{in: in}, ins: ins, outs: outs}
}

func swapRig(t *testing.T) (*runtime.Runtime, *askingDriver, *askingDriver) {
	t.Helper()
	a := swapDriver([]string{"A_In"}, []string{"A_Out"}, nio.Values{"A_In": 1.0})
	b := swapDriver([]string{"B_In"}, []string{"B_Out"}, nio.Values{"B_In": 3.0})
	m, err := nio.NewMulti(nio.NamedDriver{Name: "a", Driver: a}, nio.NamedDriver{Name: "b", Driver: b})
	if err != nil {
		t.Fatal(err)
	}
	rt, err := runtime.New(runtime.Options{
		Program: swapProgram,
		Driver:  m,
		Tags: []runtime.TagDef{
			runtime.Input("A_In", runtime.Init(0.0)), runtime.Input("B_In", runtime.Init(0.0)),
			runtime.Output("A_Out", runtime.Init(0.0)), runtime.Output("B_Out", runtime.Init(0.0)),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return rt, a, b
}

// #706: one driver of a running set is swapped between two scans. The scan
// after the swap reads the new driver and hands it every output, even
// though none changed (it starts from the current commands); the old
// driver is written no more; the other driver goes on as before.
func TestReplaceDriverSwapsBetweenScans(t *testing.T) {
	rt, a, b := swapRig(t)
	rt.Scan()
	rt.Scan() // steady: B_Out sent once, unchanged since
	b.mu.Lock()
	bWrites := b.writes
	b.mu.Unlock()

	b2 := swapDriver([]string{"B_In"}, []string{"B_Out"}, nio.Values{"B_In": 3.0})
	old, err := rt.ReplaceDriver(context.Background(), "b", b2)
	if err != nil {
		t.Fatal(err)
	}
	if old != nio.Driver(b) {
		t.Errorf("ReplaceDriver returned %T, want the driver it replaced", old)
	}
	rt.Scan()
	b2.mu.Lock()
	got := b2.lastOut["B_Out"]
	b2.mu.Unlock()
	if got != 6.0 {
		t.Errorf("the new driver got B_Out = %v on the scan after the swap, want the current 6 (unchanged, pushed anyway)", got)
	}
	b.mu.Lock()
	if b.writes != bWrites {
		t.Errorf("the replaced driver was written %d more times", b.writes-bWrites)
	}
	b.mu.Unlock()

	b2.mu.Lock()
	b2.in = nio.Values{"B_In": 7.0}
	b2.mu.Unlock()
	rt.Scan()
	if v := rt.Tags().Real("B_In"); v != 7.0 {
		t.Errorf("B_In = %v, want 7 read from the new driver", v)
	}
	if v := rt.Tags().Real("A_Out"); v != 1.0 {
		t.Errorf("A_Out = %v: the other driver's path changed", v)
	}
	a.mu.Lock()
	if a.reads == 0 {
		t.Error("the other driver is no longer read")
	}
	a.mu.Unlock()
}

// The tag set is fixed at New: a replacement that delivers or accepts other
// tags is refused, and so is a swap when the driver is not composed. A
// refusal changes nothing.
func TestReplaceDriverRefuses(t *testing.T) {
	rt, _, b := swapRig(t)
	for _, c := range []struct {
		name, child string
		d           nio.Driver
		want        string
	}{
		{"another input", "b", swapDriver([]string{"B_In", "C_In"}, []string{"B_Out"}, nil), "tag set cannot change live"},
		{"another output", "b", swapDriver([]string{"B_In"}, nil, nil), "tag set cannot change live"},
		{"no such driver", "c", swapDriver([]string{"B_In"}, []string{"B_Out"}, nil), "no such driver"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := rt.ReplaceDriver(context.Background(), c.child, c.d); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("ReplaceDriver: %v, want %q", err, c.want)
			}
		})
	}
	rt.Scan()
	b.mu.Lock()
	if b.reads == 0 {
		t.Error("after refusals the original driver is not read")
	}
	b.mu.Unlock()

	lone, err := runtime.New(runtime.Options{Program: swapProgram, Driver: swapDriver([]string{"A_In", "B_In"}, []string{"A_Out", "B_Out"}, nil),
		Tags: []runtime.TagDef{runtime.Input("A_In", runtime.Init(0.0)), runtime.Input("B_In", runtime.Init(0.0)), runtime.Output("A_Out", runtime.Init(0.0)), runtime.Output("B_Out", runtime.Init(0.0))}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lone.ReplaceDriver(context.Background(), "b", swapDriver(nil, nil, nil)); err == nil || !strings.Contains(err.Error(), "not a composite") {
		t.Errorf("a lone driver: %v, want a refusal naming the composite", err)
	}
}

// Under the running loop: a swap mid-run costs no failed read and no failed
// scan (the replacement was started first), and the loop keeps scanning.
func TestReplaceDriverWhileRunning(t *testing.T) {
	rt, _, _ := swapRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { rt.Run(ctx); close(done) }()
	time.Sleep(50 * time.Millisecond)
	before := rt.Stats().Count
	for i := 0; i < 20; i++ {
		if _, err := rt.ReplaceDriver(context.Background(), "b", swapDriver([]string{"B_In"}, []string{"B_Out"}, nio.Values{"B_In": float64(i)})); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done
	s := rt.Stats()
	if s.Count <= before {
		t.Errorf("the loop stopped scanning across the swaps: %d then %d", before, s.Count)
	}
	if s.IOErrors != 0 || s.LogicErrors != 0 {
		t.Errorf("swaps cost %d I/O and %d logic errors, want none", s.IOErrors, s.LogicErrors)
	}
}

// blockingDriver holds its read until released, so a test can stop a scan
// half-way.
type blockingDriver struct {
	*askingDriver
	entered, release chan struct{}
}

func (d *blockingDriver) ReadInputs() (nio.Values, error) {
	if d.entered != nil {
		close(d.entered)
		d.entered = nil
		<-d.release
	}
	return d.askingDriver.ReadInputs()
}

// A swap never lands inside a scan: one requested while a scan is reading
// waits for that scan to end, so the scan writes to the driver it read from.
// (Without the scan lock the swap would slip in after the read and the
// scan's write would go to the new driver.)
func TestReplaceDriverWaitsForTheScanInProgress(t *testing.T) {
	a := swapDriver([]string{"A_In"}, []string{"A_Out"}, nio.Values{"A_In": 1.0})
	b := &blockingDriver{askingDriver: swapDriver([]string{"B_In"}, []string{"B_Out"}, nio.Values{"B_In": 3.0}),
		entered: make(chan struct{}), release: make(chan struct{})}
	m, err := nio.NewMulti(nio.NamedDriver{Name: "a", Driver: a}, nio.NamedDriver{Name: "b", Driver: b})
	if err != nil {
		t.Fatal(err)
	}
	rt, err := runtime.New(runtime.Options{Program: swapProgram, Driver: m,
		Tags: []runtime.TagDef{runtime.Input("A_In", runtime.Init(0.0)), runtime.Input("B_In", runtime.Init(0.0)),
			runtime.Output("A_Out", runtime.Init(0.0)), runtime.Output("B_Out", runtime.Init(0.0))}})
	if err != nil {
		t.Fatal(err)
	}
	entered := b.entered
	scanned := make(chan struct{})
	go func() { rt.Scan(); close(scanned) }()
	<-entered // the scan is inside b's read
	b2 := swapDriver([]string{"B_In"}, []string{"B_Out"}, nio.Values{"B_In": 3.0})
	swapped := make(chan struct{})
	go func() {
		if _, err := rt.ReplaceDriver(context.Background(), "b", b2); err != nil {
			t.Error(err)
		}
		close(swapped)
	}()
	time.Sleep(20 * time.Millisecond) // let the swap queue behind the scan
	close(b.release)
	<-scanned
	<-swapped
	b.mu.Lock()
	oldGot := b.lastOut["B_Out"]
	b.mu.Unlock()
	b2.mu.Lock()
	newWrites := b2.writes
	b2.mu.Unlock()
	if oldGot != 6.0 || newWrites != 0 {
		t.Errorf("the scan read from the old driver but wrote B_Out=%v to it and %d times to the new one: the swap landed inside the scan", oldGot, newWrites)
	}
}

// modbusRig is a runtime over a set of one real Modbus driver (Cmd := In +
// 1 on holding 10, In on holding 0) and the slave it polls.
func modbusRig(t *testing.T, hold bool) (*runtime.Runtime, *nio.Multi, *modbus.Driver, *slave.Server, context.Context) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s1 := newModbusSlave(t)
	s1.Unit(1).SetHolding(0, 4)
	m1 := newModbus(t, s1)
	m1.Start(ctx)
	t.Cleanup(m1.Stop)
	set, err := nio.NewMulti(nio.NamedDriver{Name: "mb", Driver: m1})
	if err != nil {
		t.Fatal(err)
	}
	rt, err := runtime.New(runtime.Options{
		Program: "PROGRAM Main\nVAR_EXTERNAL In : DINT; Cmd : DINT; END_VAR\nCmd := In + 1;\nEND_PROGRAM",
		Driver:  set,
		Tags: []runtime.TagDef{runtime.Input("In", runtime.Init(int64(0))), runtime.Output("Cmd", runtime.Init(int64(0))),
			runtime.Input("DEV__Online", runtime.Init(false)), runtime.Input("DEV__Outages", runtime.Init(int64(0)))},
		HoldOutputsOnSwap: hold,
	})
	if err != nil {
		t.Fatal(err)
	}
	waitScan(t, rt, func() bool { v, _ := rt.Tags().ReadGlobal("In"); return v.I == 4 })
	return rt, set, m1, s1, ctx
}

func newModbusSlave(t *testing.T) *slave.Server {
	t.Helper()
	srv := slave.NewServer("", nil)
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Stop)
	return srv
}

func newModbus(t *testing.T, srv *slave.Server) *modbus.Driver {
	t.Helper()
	host, port, _ := net.SplitHostPort(srv.Addr())
	p, _ := strconv.Atoi(port)
	d, err := modbus.New(modbus.Manifest{
		Sources: []modbus.Source{{ID: "DEV", Host: host, Port: p, UnitID: 1, Timeout: 500 * time.Millisecond, RetryMin: 20 * time.Millisecond, RetryMax: 80 * time.Millisecond}},
		Tags: []modbus.TagBinding{
			{Name: "In", Source: "DEV", Table: modbus.TableHolding, Address: 0, Format: "uint16"},
			{Name: "Cmd", Source: "DEV", Table: modbus.TableHolding, Address: 10, Format: "uint16", Writable: true},
		},
	}, modbus.WithScanRate(10*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// waitScan scans until cond holds.
func waitScan(t *testing.T, rt *runtime.Runtime, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		rt.Scan()
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out")
}

// swapIn gates and starts a new Modbus driver on srv and swaps it in.
func swapIn(t *testing.T, rt *runtime.Runtime, set *nio.Multi, ctx context.Context, srv *slave.Server) *modbus.Driver {
	t.Helper()
	m2 := newModbus(t, srv)
	set.Gate(m2)
	m2.Start(ctx)
	t.Cleanup(m2.Stop)
	if _, err := rt.ReplaceDriver(ctx, "mb", m2); err != nil {
		t.Fatal(err)
	}
	return m2
}

// With a real Modbus driver (its start-up baseline armed by Start), the
// new device gets the CURRENT command on the scans after the swap; the
// push is not swallowed as the baseline (#706, #879 review).
func TestReplaceDriverRealModbusGetsTheCurrentCommand(t *testing.T) {
	rt, set, _, _, ctx := modbusRig(t, false)
	s2 := newModbusSlave(t)
	s2.Unit(1).SetHolding(0, 4) // same In on the new device: Cmd stays 5, unchanged
	swapIn(t, rt, set, ctx, s2)
	waitScan(t, rt, func() bool { return s2.Unit(1).Holding(10) == 5 })
}

// HoldOutputsOnSwap: the push is the new driver's baseline; the device gets
// a command only once it changes.
func TestReplaceDriverHoldOutputsOnSwap(t *testing.T) {
	rt, set, _, _, ctx := modbusRig(t, true)
	s2 := newModbusSlave(t)
	s2.Unit(1).SetHolding(0, 4)
	swapIn(t, rt, set, ctx, s2)
	for i := 0; i < 20; i++ {
		rt.Scan()
		time.Sleep(10 * time.Millisecond)
	}
	if v := s2.Unit(1).Holding(10); v != 0 {
		t.Fatalf("held: the new device got Cmd %d before it changed", v)
	}
	s2.Unit(1).SetHolding(0, 8) // In changes, so Cmd changes to 9
	waitScan(t, rt, func() bool { return s2.Unit(1).Holding(10) == 9 })
}

// The old Modbus driver, gated closed by the swap, sends nothing more to
// its device, even when handed a new command before it is stopped.
func TestReplaceDriverOldRealModbusWritesNothingMore(t *testing.T) {
	rt, set, m1, s1, ctx := modbusRig(t, false)
	// A command before the swap lands first: a write queued, or in flight,
	// before the swap may finish after it. It is a change (In 6, Cmd 7):
	// Cmd 5 may have been the start-up baseline, recorded and never sent.
	s1.Unit(1).SetHolding(0, 6)
	waitScan(t, rt, func() bool { return s1.Unit(1).Holding(10) == 7 })
	before := s1.Unit(1).Holding(10)
	s2 := newModbusSlave(t)
	s2.Unit(1).SetHolding(0, 4)
	swapIn(t, rt, set, ctx, s2)
	m1.WriteOutputs(nio.Values{"Cmd": int64(77)})
	time.Sleep(150 * time.Millisecond)
	if v := s1.Unit(1).Holding(10); v != before {
		t.Errorf("the old device got Cmd %d after the swap (was %d)", v, before)
	}
}

// A hung scan does not hang the apply: the wait for it ends with ctx.
func TestReplaceDriverGivesUpOnAHungScan(t *testing.T) {
	a := swapDriver([]string{"A_In"}, []string{"A_Out"}, nio.Values{"A_In": 1.0})
	b := &blockingDriver{askingDriver: swapDriver([]string{"B_In"}, []string{"B_Out"}, nio.Values{"B_In": 3.0}),
		entered: make(chan struct{}), release: make(chan struct{})}
	m, _ := nio.NewMulti(nio.NamedDriver{Name: "a", Driver: a}, nio.NamedDriver{Name: "b", Driver: b})
	rt, err := runtime.New(runtime.Options{Program: swapProgram, Driver: m,
		Tags: []runtime.TagDef{runtime.Input("A_In", runtime.Init(0.0)), runtime.Input("B_In", runtime.Init(0.0)),
			runtime.Output("A_Out", runtime.Init(0.0)), runtime.Output("B_Out", runtime.Init(0.0))}})
	if err != nil {
		t.Fatal(err)
	}
	entered := b.entered
	go rt.Scan()
	<-entered
	defer close(b.release)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = rt.ReplaceDriver(ctx, "b", swapDriver([]string{"B_In"}, []string{"B_Out"}, nil))
	if err == nil || !strings.Contains(err.Error(), "deadline") {
		t.Errorf("ReplaceDriver during a hung scan: %v, want the context's deadline", err)
	}
	if time.Since(start) > time.Second {
		t.Errorf("it waited %v", time.Since(start))
	}
}
