package runtime

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
)

// A Comms input — one a communications driver delivers — is seeded so the
// programs that read it run before the device first answers: its Init when
// it has one, else the zero of its tag type, else of the type the programs
// declare, else of the driver binding's type. One with no knowable type is
// left absent and reported. A plain input keeps the strict rule.
func TestCommsInputSeedsInitOrZeroOfItsType(t *testing.T) {
	rt, err := New(Options{
		Program: `PROGRAM Main
VAR_EXTERNAL
  Declared : DINT;
  WithInit : REAL;
  Out : REAL;
END_VAR
Out := WithInit + Declared;
END_PROGRAM`,
		Driver: nio.NewMemory(),
		Tags: []TagDef{
			Input("Declared", Comms()),                 // the program's DINT
			Input("WithInit", Comms(), Init(4.5)),      // its init
			Typed("Typed", RoleInput, "BOOL", Comms()), // its own type:
			{Name: "Wire", Role: RoleInput, Comms: true, DriverType: "REAL"},
			{Name: "Unknown", Role: RoleInput, Comms: true}, // nothing says
			Input("Local"), // not Comms: strict
			State("Out", 0.0),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	tags := rt.Tags()
	for name, want := range map[string]ir.Value{
		"Declared": ir.IntVal(0),
		"WithInit": ir.RealVal(4.5),
		"Typed":    ir.BoolVal(false),
		"Wire":     ir.RealVal(0),
	} {
		got, err := tags.ReadGlobal(name)
		if err != nil {
			t.Errorf("%s was not seeded: %v", name, err)
			continue
		}
		if got.Kind != want.Kind || got.I != want.I || got.F != want.F || got.B != want.B {
			t.Errorf("%s seeded %+v, want %+v", name, got, want)
		}
	}
	for _, name := range []string{"Unknown", "Local"} {
		if _, err := tags.ReadGlobal(name); err == nil {
			t.Errorf("%s was seeded; it must stay absent until delivered", name)
		}
	}
	if got := rt.UntypedCommsInputs(); len(got) != 1 || got[0] != "Unknown" {
		t.Errorf("UntypedCommsInputs = %v, want [Unknown]", got)
	}

	// The program reads two never-delivered protocol inputs and runs.
	rt.Scan()
	if st := rt.Stats(); st.LogicErrors != 0 {
		t.Fatalf("a program reading undelivered Comms inputs faulted: %s", st.LastError)
	}
	if got := tags.Real("Out"); got != 4.5 {
		t.Errorf("Out = %v, want 4.5 (the init plus the zero)", got)
	}
}

// The same program reading a plain (local I/O) input still faults until it
// is delivered — the loud failure RoleInput exists for — and runs after.
func TestNonCommsInputStillFaultsUntilDelivered(t *testing.T) {
	drv := nio.NewMemory()
	rt, err := New(Options{
		Program: `PROGRAM Main
VAR_EXTERNAL
  Local : REAL;
  Out : REAL;
END_VAR
Out := Local;
END_PROGRAM`,
		Driver: drv,
		Tags:   []TagDef{Input("Local"), State("Out", 0.0)},
	})
	if err != nil {
		t.Fatal(err)
	}
	rt.Scan()
	if st := rt.Stats(); st.LogicErrors != 1 || !strings.Contains(st.LastError, "undefined tag Local") {
		t.Fatalf("undelivered local input: LogicErrors=%d LastError=%q, want the undefined-tag fault", st.LogicErrors, st.LastError)
	}
	_ = drv.WriteOutputs(nio.Values{"Local": 2.0})
	rt.Scan()
	if st := rt.Stats(); st.LogicErrors != 1 {
		t.Errorf("faulted again after delivery: %s", st.LastError)
	}
	if got := rt.Tags().Real("Out"); got != 2 {
		t.Errorf("Out = %v, want 2", got)
	}
}

// pruningDriver is a BatchReader that delivers exactly what it holds THIS
// scan and prunes everything else from the runtime's map — the modbus
// ReadInputsInto contract at its strictest.
type pruningDriver struct {
	mu   sync.Mutex
	vals nio.Values
}

func (p *pruningDriver) set(v nio.Values) { p.mu.Lock(); p.vals = v; p.mu.Unlock() }
func (p *pruningDriver) ReadInputs() (nio.Values, error) {
	out := nio.Values{}
	return out, p.ReadInputsInto(out)
}
func (p *pruningDriver) ReadInputsInto(dst nio.Values) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for k := range dst {
		delete(dst, k)
	}
	for k, v := range p.vals {
		dst[k] = v
	}
	return nil
}
func (p *pruningDriver) WriteOutputs(nio.Values) error { return nil }

// The seed lives in the tag store, not in the driver's delivery map, so a
// driver that prunes its map every scan can neither delete the seed nor
// overwrite it with anything but a real delivery. A real value replaces the
// seed; a later scan that delivers nothing for the tag holds the last real
// value (the existing last-known rule), not the seed.
func TestCommsSeedSurvivesPruningUntilARealValueReplacesIt(t *testing.T) {
	drv := &pruningDriver{}
	rt, err := New(Options{
		Program: `PROGRAM Main
VAR_EXTERNAL
  Flow : REAL;
  Out : REAL;
END_VAR
Out := Flow;
END_PROGRAM`,
		Driver: drv,
		Tags:   []TagDef{Input("Flow", Comms()), Input("Other", Comms(), Init(1.0)), State("Out", 0.0)},
	})
	if err != nil {
		t.Fatal(err)
	}
	drv.set(nio.Values{"Other": 7.0}) // Flow's device is down
	for range 3 {
		rt.Scan()
	}
	if st := rt.Stats(); st.LogicErrors != 0 {
		t.Fatalf("faulted: %s", st.LastError)
	}
	if got := rt.Tags().Real("Flow"); got != 0 {
		t.Errorf("Flow = %v while undelivered, want the seeded 0", got)
	}
	if got := rt.Tags().Real("Other"); got != 7 {
		t.Errorf("Other = %v, want the delivered 7 over its init", got)
	}

	drv.set(nio.Values{"Flow": 3.25}) // the device answers
	rt.Scan()
	if got := rt.Tags().Real("Out"); got != 3.25 {
		t.Errorf("Out = %v after delivery, want 3.25", got)
	}
	drv.set(nio.Values{}) // pruned from the map again
	rt.Scan()
	if got := rt.Tags().Real("Flow"); got != 3.25 {
		t.Errorf("Flow = %v after the driver stopped delivering, want last-known 3.25", got)
	}
}

// captureLog is a slog.Handler that keeps every record, for asserting how
// many times the journal heard about something.
type captureLog struct {
	mu   sync.Mutex
	recs []slog.Record
}

func (c *captureLog) Enabled(context.Context, slog.Level) bool { return true }
func (c *captureLog) Handle(_ context.Context, r slog.Record) error {
	c.mu.Lock()
	c.recs = append(c.recs, r.Clone())
	c.mu.Unlock()
	return nil
}
func (c *captureLog) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *captureLog) WithGroup(string) slog.Handler      { return c }

// count returns how many records at level carry msg, and the attrs of the
// last one.
func (c *captureLog) count(level slog.Level, msg string) (int, map[string]string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n, attrs := 0, map[string]string{}
	for _, r := range c.recs {
		if r.Level != level || r.Message != msg {
			continue
		}
		n++
		attrs = map[string]string{}
		r.Attrs(func(a slog.Attr) bool { attrs[a.Key] = a.Value.String(); return true })
	}
	return n, attrs
}

// A program that faults every scan is logged ONCE when it starts faulting
// and once when it recovers — not once per scan — for the main task and for
// an additional task alike.
func TestLogicFaultTransitionsAreLoggedOncePerEpisode(t *testing.T) {
	drv := nio.NewMemory()
	rt, err := New(Options{
		Program: `PROGRAM Main
VAR_EXTERNAL
  A : REAL;
  Out : REAL;
END_VAR
Out := A;
END_PROGRAM`,
		Tasks: []Task{{Name: "slow", Program: `PROGRAM Slow
VAR_EXTERNAL
  B : REAL;
  Out2 : REAL;
END_VAR
Out2 := B;
END_PROGRAM`}},
		Driver: drv,
		Tags:   []TagDef{Input("A"), State("Out", 0.0), State("Out2", 0.0)}, // B: nothing seeds it
	})
	if err != nil {
		t.Fatal(err)
	}
	logs := &captureLog{}
	rt.log = slog.New(logs)

	for range 50 {
		rt.Scan()
		if err := rt.ScanTask("slow"); err != nil {
			t.Fatal(err)
		}
	}
	if st := rt.Stats(); st.LogicErrors != 50 || st.Tasks[0].LogicErrors != 50 {
		t.Fatalf("expected 50 faulting scans per task, got main %d, slow %d", st.LogicErrors, st.Tasks[0].LogicErrors)
	}
	if n, _ := logs.count(slog.LevelWarn, "runtime: logic fault"); n != 2 {
		t.Fatalf("logic fault WARNs = %d over 50 faulting scans of two tasks, want 2 (one per task)", n)
	}

	_ = drv.WriteOutputs(nio.Values{"A": 1.0}) // main's input arrives
	rt.Tags().SetReal("B", 2.0)                // the task's tag gets written
	for range 5 {
		rt.Scan()
		_ = rt.ScanTask("slow")
	}
	n, attrs := logs.count(slog.LevelInfo, "runtime: logic fault cleared")
	if n != 2 {
		t.Fatalf("recovery INFOs = %d, want 2 (one per task)", n)
	}
	if attrs["scans"] != "50" {
		t.Errorf("recovery attrs %v: want scans=50 (the episode's length)", attrs)
	}
	if _, ok := attrs["for"]; !ok {
		t.Errorf("recovery attrs %v carry no duration", attrs)
	}
	if n, _ := logs.count(slog.LevelWarn, "runtime: logic fault"); n != 2 {
		t.Errorf("logic fault WARNs = %d after recovery, want still 2", n)
	}
}

// failingReader fails its first n reads, then delivers.
type failingReader struct {
	mu sync.Mutex
	n  int
}

func (f *failingReader) ReadInputs() (nio.Values, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.n > 0 {
		f.n--
		return nil, errors.New("device 10.0.0.9 not answering")
	}
	return nio.Values{"A": 1.0}, nil
}
func (f *failingReader) WriteOutputs(nio.Values) error { return nil }

// An input read that fails every scan is an I/O fault episode: one WARN,
// one INFO when reads come back.
func TestIOFaultTransitionsAreLoggedOncePerEpisode(t *testing.T) {
	rt, err := New(Options{
		Program: `PROGRAM Main
VAR_EXTERNAL
  A : REAL;
  Out : REAL;
END_VAR
Out := A;
END_PROGRAM`,
		Driver: &failingReader{n: 30},
		Tags:   []TagDef{Input("A", Comms()), State("Out", 0.0)},
	})
	if err != nil {
		t.Fatal(err)
	}
	logs := &captureLog{}
	rt.log = slog.New(logs)
	for range 40 {
		rt.Scan()
	}
	if n, attrs := logs.count(slog.LevelWarn, "runtime: I/O fault"); n != 1 || !strings.Contains(attrs["error"], "not answering") {
		t.Errorf("I/O fault WARNs = %d (%v), want 1 carrying the error", n, attrs)
	}
	if n, attrs := logs.count(slog.LevelInfo, "runtime: I/O fault cleared"); n != 1 || attrs["scans"] != "30" {
		t.Errorf("I/O recovery INFOs = %d (%v), want 1 with scans=30", n, attrs)
	}
	// The Comms input ran on its seed throughout: no logic fault at all.
	if n, _ := logs.count(slog.LevelWarn, "runtime: logic fault"); n != 0 {
		t.Errorf("logic fault logged while the device was down; the seed should have carried the scan")
	}
}

// While a task keeps faulting, a changed message is logged — but at most
// once per faultChangeEvery, so a message that flaps every scan cannot flood
// the journal. Changes inside the window are counted, and the next scan
// after it that still differs from the last logged message logs.
func TestFaultTrackRateLimitsChangedMessages(t *testing.T) {
	var f faultTrack
	t0 := time.Unix(1_800_000_000, 0)
	at := func(d time.Duration) time.Time { return t0.Add(d) }

	if ev := f.observe("", at(0)); ev.kind != faultNone {
		t.Fatalf("healthy scan produced %v", ev.kind)
	}
	if ev := f.observe("undefined tag A", at(0)); ev.kind != faultStart || ev.msg != "undefined tag A" {
		t.Fatalf("first fault = %+v, want faultStart", ev)
	}
	logged := 0
	for i := 1; i <= 610; i++ { // flapping every 100 ms for 61 s
		msg := "undefined tag A"
		if i%2 == 1 {
			msg = "undefined tag B"
		}
		if ev := f.observe(msg, at(time.Duration(i)*100*time.Millisecond)); ev.kind != faultNone {
			logged++
			if ev.kind != faultChanged || ev.suppressed == 0 {
				t.Errorf("scan %d: %+v, want one rate-limited faultChanged with a suppressed count", i, ev)
			}
		}
	}
	if logged != 1 {
		t.Errorf("61 s of flapping logged %d changes, want exactly 1 (once the window passed)", logged)
	}
	if ev := f.observe("", at(62*time.Second)); ev.kind != faultRecovered || ev.scans != 611 {
		t.Errorf("recovery = %+v, want faultRecovered after 611 scans", ev)
	}
	if ev := f.observe("", at(63*time.Second)); ev.kind != faultNone {
		t.Errorf("a second healthy scan produced %v", ev.kind)
	}
}

// The healthy path of the fault tracker allocates nothing: the scan pays one
// boolean test for the journal.
func TestFaultTrackHealthyPathDoesNotAllocate(t *testing.T) {
	var f faultTrack
	now := time.Now()
	if n := testing.AllocsPerRun(1000, func() { _ = f.observe("", now) }); n != 0 {
		t.Errorf("healthy observe allocates %v per call", n)
	}
	f.observe("x", now)
	if n := testing.AllocsPerRun(1000, func() { _ = f.observe("x", now) }); n != 0 {
		t.Errorf("an unchanged faulting observe allocates %v per call", n)
	}
}
