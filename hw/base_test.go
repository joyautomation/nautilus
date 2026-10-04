package hw

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/runtime"
)

// fakeWire is the protocol stand-in: a scripted PollFunc/WriteFunc with a
// notification channel so tests wait for polls instead of sleeping.
type fakeWire struct {
	mu      sync.Mutex
	results map[string]Result // key "source/class"
	fail    map[string]error
	polls   int
	writes  []string
	polled  chan string
}

func newFakeWire() *fakeWire {
	return &fakeWire{results: map[string]Result{}, fail: map[string]error{}, polled: make(chan string, 64)}
}

func (w *fakeWire) set(source, class string, r Result) {
	w.mu.Lock()
	w.results[source+"/"+class] = r
	w.mu.Unlock()
}

func (w *fakeWire) setErr(source, class string, err error) {
	w.mu.Lock()
	if err == nil {
		delete(w.fail, source+"/"+class)
	} else {
		w.fail[source+"/"+class] = err
	}
	w.mu.Unlock()
}

func (w *fakeWire) poll(ctx context.Context, source, class string) (Result, error) {
	w.mu.Lock()
	w.polls++
	err := w.fail[source+"/"+class]
	res := w.results[source+"/"+class]
	w.mu.Unlock()
	select {
	case w.polled <- source + "/" + class:
	default:
	}
	if err != nil {
		return Result{}, err
	}
	return res, nil
}

func (w *fakeWire) write(ctx context.Context, source string, d WriteDecl, v ir.Value) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if strings.HasPrefix(d.Name, "REFUSE") {
		return errors.New("wrongValue")
	}
	w.writes = append(w.writes, fmt.Sprintf("%s/%s.%s=%v", source, d.Tag, d.Member, v.B))
	return nil
}

func (w *fakeWire) waitPoll(t *testing.T, want string) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case got := <-w.polled:
			if got == want {
				return
			}
		case <-deadline:
			t.Fatalf("no poll of %s within 3s", want)
		}
	}
}

func portDecl(name, src string) TagDecl {
	return TagDecl{Name: name, Type: "SwitchPort", Source: src, Members: map[string]Binding{
		"Index":     {Const: 1},
		"Name":      {ScanClass: "slow"},
		"AdminUp":   {},
		"OperUp":    {},
		"Down":      {Derived: "AdminUp && !OperUp"},
		"SpeedMbps": {},
		"InBps":     {},
		"InPct":     {Derived: "100 * InBps / (SpeedMbps * 1e6)"},
	}}
}

func switchConfig(w *fakeWire) Config {
	return Config{
		Kind:       "test",
		Sources:    []SourceConfig{{ID: "SW1", Addr: "sw1:161", Interval: 20 * time.Millisecond, RetryMin: 10 * time.Millisecond, RetryMax: 20 * time.Millisecond}},
		Tags:       []TagDecl{portDecl("SW1_Port01", "SW1"), portDecl("SW1_Port02", "SW1")},
		ClassRates: map[string]time.Duration{"slow": 50 * time.Millisecond},
		Poll:       w.poll,
	}
}

func up(tag string, admin, oper bool, bps float64) []Update {
	return []Update{
		{tag, "AdminUp", ir.BoolVal(admin)},
		{tag, "OperUp", ir.BoolVal(oper)},
		{tag, "SpeedMbps", ir.RealVal(1000)},
		{tag, "InBps", ir.RealVal(bps)},
	}
}

func TestBaseDeliversDerivesAndReports(t *testing.T) {
	w := newFakeWire()
	b, err := NewBase(switchConfig(w))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(b.InputNames(), ","); got != "SW1_Port01,SW1_Port02,SW1__LastPollMs,SW1__Online" {
		t.Fatalf("InputNames = %s", got)
	}
	if cls := b.ScanClasses(); strings.Join(cls["slow"], ",") != "SW1_Port01.Name,SW1_Port02.Name" || len(cls[DefaultClass]) != 8 {
		t.Fatalf("ScanClasses = %v", cls)
	}

	// Before Start: nothing delivered, everything NotConnected, offline.
	vals := nio.Values{}
	_ = b.ReadInputsInto(vals)
	if vals["SW1__Online"] != false || vals["SW1__LastPollMs"] != int64(0) || len(vals) != 2 {
		t.Fatalf("pre-start read = %v", vals)
	}
	if q := b.Quality(); q["SW1_Port01"] != nio.NotConnected || q["SW1_Port02"] != nio.NotConnected {
		t.Fatalf("pre-start quality = %v", q)
	}

	w.set("SW1", DefaultClass, Result{Updates: append(up("SW1_Port01", true, false, 250e6), up("SW1_Port02", true, true, 0)...)})
	w.set("SW1", "slow", Result{Updates: []Update{{"SW1_Port01", "Name", ir.StringVal("Gi0/1")}, {"SW1_Port02", "Name", ir.StringVal("Gi0/2")}}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b.Start(ctx)
	defer b.Stop()
	w.waitPoll(t, "SW1/default")
	w.waitPoll(t, "SW1/slow")

	_ = b.ReadInputsInto(vals)
	if vals["SW1__Online"] != true {
		t.Fatalf("Online after poll = %v", vals["SW1__Online"])
	}
	p1 := vals["SW1_Port01"].(ir.Value)
	if p1.Struct != StructDef("SwitchPort") {
		t.Fatal("delivered value does not carry the shared StructDef")
	}
	get := func(v ir.Value, m string) ir.Value { return v.Fld[v.Struct.FieldIndex[m]] }
	if get(p1, "Index").I != 1 || get(p1, "Name").S != "Gi0/1" || !get(p1, "AdminUp").B || get(p1, "OperUp").B {
		t.Fatalf("Port01 = %+v", p1)
	}
	if !get(p1, "Down").B || get(p1, "InPct").F != 25 {
		t.Fatalf("derived: Down=%v InPct=%v", get(p1, "Down").B, get(p1, "InPct").F)
	}
	p2 := vals["SW1_Port02"].(ir.Value)
	if get(p2, "Down").B || get(p2, "Name").S != "Gi0/2" {
		t.Fatalf("Port02 = %+v", p2)
	}
	if q := b.Quality(); len(q) != 0 {
		t.Fatalf("quality after a clean poll = %v", q)
	}
	h := b.Health()
	if len(h.Sources) != 1 || h.Sources[0].State != "connected" || !h.Sources[0].Fresh || h.Sources[0].Tags != 2 || h.Polls == 0 {
		t.Fatalf("health = %+v", h)
	}

	// Copy-on-write: the value handed to the store must not change when the
	// next poll lands.
	w.set("SW1", DefaultClass, Result{Updates: up("SW1_Port01", true, true, 0)})
	w.waitPoll(t, "SW1/default")
	w.waitPoll(t, "SW1/default")
	if get(p1, "OperUp").B {
		t.Fatal("a delivered value was mutated in place")
	}
	_ = b.ReadInputsInto(vals)
	if get(vals["SW1_Port01"].(ir.Value), "Down").B {
		t.Fatal("Down did not clear after the port came up")
	}

	// A member absent from the update keeps its value (Name is on the slow
	// class and was not in this poll).
	if get(vals["SW1_Port01"].(ir.Value), "Name").S != "Gi0/1" {
		t.Fatal("untouched member lost its value")
	}
}

func TestBasePartialBadAndStale(t *testing.T) {
	w := newFakeWire()
	cfg := switchConfig(w)
	cfg.Sources[0].StaleAfter = 60 * time.Millisecond
	b, err := NewBase(cfg)
	if err != nil {
		t.Fatal(err)
	}
	w.set("SW1", DefaultClass, Result{Updates: up("SW1_Port01", true, true, 0), Bad: []string{"SW1_Port02"}})
	w.set("SW1", "slow", Result{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b.Start(ctx)
	defer b.Stop()
	w.waitPoll(t, "SW1/default")
	w.waitPoll(t, "SW1/slow")

	// Port02's default-class requests failed while its slow-class poll
	// succeeded: delivered (by the slow class) and Bad (any class failing
	// makes the whole tag Bad); Port01 Good.
	w.waitPoll(t, "SW1/default")
	if q := b.Quality(); q["SW1_Port02"] != nio.Bad || q["SW1_Port01"] != nio.Good {
		t.Fatalf("quality = %v", q)
	}
	if b.Health().Sources[0].BadTags != 1 {
		t.Fatal("BadTags")
	}
	vals, _ := b.ReadInputs()
	if _, ok := vals["SW1_Port02"]; !ok {
		t.Fatal("a Bad tag that was delivered by another class must be present")
	}
	// Once every class answers for it, Bad clears.
	w.set("SW1", DefaultClass, Result{Updates: append(up("SW1_Port01", true, true, 0), up("SW1_Port02", true, true, 0)...)})
	w.waitPoll(t, "SW1/default")
	w.waitPoll(t, "SW1/default")
	if q := b.Quality(); len(q) != 0 {
		t.Fatalf("quality after clean polls = %v", q)
	}
	w.set("SW1", DefaultClass, Result{Updates: up("SW1_Port01", true, true, 0), Bad: []string{"SW1_Port02"}})
	w.waitPoll(t, "SW1/default")
	w.waitPoll(t, "SW1/default")
	if q := b.Quality(); q["SW1_Port02"] != nio.Bad {
		t.Fatalf("quality = %v", q)
	}

	// Transport failures: the first two keep the source connected; the third
	// moves it to error. Meanwhile stale-after turns everything Stale.
	w.setErr("SW1", DefaultClass, errors.New("timeout"))
	w.setErr("SW1", "slow", errors.New("timeout"))
	w.waitPoll(t, "SW1/default")
	w.waitPoll(t, "SW1/default")
	if h := b.Health(); h.Sources[0].State != "connected" {
		t.Fatalf("after two failures: %+v", h.Sources[0])
	}
	deadline := time.Now().Add(2 * time.Second)
	for b.Health().Sources[0].State != "error" && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	h := b.Health()
	if h.Sources[0].State != "error" || h.Sources[0].LastError != "timeout" || h.Sources[0].Fresh {
		t.Fatalf("after three failures: %+v", h.Sources[0])
	}
	if q := b.Quality(); q["SW1_Port01"] != nio.Stale || q["SW1_Port02"] != nio.Stale {
		t.Fatalf("quality while down = %v", q)
	}
	vals, _ = b.ReadInputs()
	if vals["SW1__Online"] != false {
		t.Fatal("__Online must be false while down")
	}
	if _, ok := vals["SW1_Port01"]; !ok {
		t.Fatal("values must hold while down")
	}

	// Recovery: the backoff ladder retries, a good poll returns to connected.
	w.setErr("SW1", DefaultClass, nil)
	w.setErr("SW1", "slow", nil)
	w.set("SW1", DefaultClass, Result{Updates: append(up("SW1_Port01", true, true, 0), up("SW1_Port02", true, true, 0)...)})
	deadline = time.Now().Add(2 * time.Second)
	for b.Health().Sources[0].State != "connected" && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if h := b.Health(); h.Sources[0].State != "connected" || !h.Sources[0].Fresh {
		t.Fatalf("no recovery: %+v", h.Sources[0])
	}
	if q := b.Quality(); len(q) != 0 {
		t.Fatalf("quality after recovery = %v", q)
	}
}

func TestBaseNeverAnsweredErrorsAtOnce(t *testing.T) {
	w := newFakeWire()
	w.setErr("SW1", DefaultClass, errors.New("refused"))
	w.setErr("SW1", "slow", errors.New("refused"))
	b, _ := NewBase(switchConfig(w))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b.Start(ctx)
	defer b.Stop()
	w.waitPoll(t, "SW1/default")
	deadline := time.Now().Add(time.Second)
	for b.Health().Sources[0].State != "error" && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if h := b.Health(); h.Sources[0].State != "error" || h.Sources[0].Retries == 0 {
		t.Fatalf("%+v", h.Sources[0])
	}
	if q := b.Quality(); q["SW1_Port01"] != nio.NotConnected {
		t.Fatalf("quality = %v", q)
	}
}

func TestBaseEnableParks(t *testing.T) {
	w := newFakeWire()
	cfg := switchConfig(w)
	cfg.Sources[0].Enable = "CFG_PollSwitch"
	b, err := NewBase(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(b.OutputNames(), ","); got != "CFG_PollSwitch" {
		t.Fatalf("OutputNames = %s", got)
	}
	w.set("SW1", DefaultClass, Result{Updates: up("SW1_Port01", true, true, 0)})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b.Start(ctx)
	defer b.Stop()
	time.Sleep(30 * time.Millisecond)
	if h := b.Health(); h.Sources[0].State != "parked" || h.Polls != 0 {
		t.Fatalf("with enable false at start: %+v polls=%d", h.Sources[0], h.Polls)
	}
	if err := b.WriteOutputs(nio.Values{"CFG_PollSwitch": true}); err != nil {
		t.Fatal(err)
	}
	w.waitPoll(t, "SW1/default")
	if h := b.Health(); h.Sources[0].State != "connected" {
		t.Fatalf("after enable: %+v", h.Sources[0])
	}
	_ = b.WriteOutputs(nio.Values{"CFG_PollSwitch": ir.BoolVal(false)})
	deadline := time.Now().Add(time.Second)
	for b.Health().Sources[0].State != "parked" && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if h := b.Health(); h.Sources[0].State != "parked" {
		t.Fatalf("after disable: %+v", h.Sources[0])
	}
	// Parked: values hold, quality Stale, __Online false.
	if q := b.Quality(); q["SW1_Port01"] != nio.Stale {
		t.Fatalf("quality parked = %v", q)
	}
	if err := b.WriteOutputs(nio.Values{"CFG_PollSwitch": "yes"}); err == nil {
		t.Fatal("a non-BOOL enable value must error")
	}
}

func TestBaseWrites(t *testing.T) {
	w := newFakeWire()
	cfg := Config{
		Kind:    "test",
		Sources: []SourceConfig{{ID: "PDU1", Interval: 20 * time.Millisecond, RetryMin: 10 * time.Millisecond, RetryMax: 20 * time.Millisecond}},
		Tags: []TagDecl{{Name: "PDU1_Outlet03", Type: "PDUOutlet", Source: "PDU1", Members: map[string]Binding{
			"Index": {Const: 3}, "On": {},
		}}},
		Writes: []WriteDecl{{Name: "PDU1_Outlet03_Cmd", Tag: "PDU1_Outlet03", Member: "On"}, {Name: "REFUSE_Cmd", Tag: "PDU1_Outlet03", Member: "On"}},
		Poll:   w.poll,
		Write:  w.write,
	}
	b, err := NewBase(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(b.OutputNames(), ","); got != "PDU1_Outlet03_Cmd,REFUSE_Cmd" {
		t.Fatalf("OutputNames = %s", got)
	}
	w.set("PDU1", DefaultClass, Result{Updates: []Update{{"PDU1_Outlet03", "On", ir.BoolVal(true)}}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b.Start(ctx)
	defer b.Stop()
	w.waitPoll(t, "PDU1/default")

	// First value is a baseline: never written.
	_ = b.WriteOutputs(nio.Values{"PDU1_Outlet03_Cmd": false})
	time.Sleep(30 * time.Millisecond)
	w.mu.Lock()
	n := len(w.writes)
	w.mu.Unlock()
	if n != 0 {
		t.Fatalf("baseline was written: %v", w.writes)
	}
	// A change goes out, once, even when re-handed unchanged.
	_ = b.WriteOutputs(nio.Values{"PDU1_Outlet03_Cmd": true})
	_ = b.WriteOutputs(nio.Values{"PDU1_Outlet03_Cmd": true})
	deadline := time.Now().Add(time.Second)
	for {
		w.mu.Lock()
		n = len(w.writes)
		w.mu.Unlock()
		if n > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	w.mu.Lock()
	got := strings.Join(w.writes, ",")
	w.mu.Unlock()
	if got != "PDU1/PDU1_Outlet03.On=true" {
		t.Fatalf("writes = %q", got)
	}
	if b.Health().Writes != 1 {
		t.Fatal("Writes counter")
	}
	// A refused write is dropped with its error on the row.
	_ = b.WriteOutputs(nio.Values{"REFUSE_Cmd": false})
	_ = b.WriteOutputs(nio.Values{"REFUSE_Cmd": true})
	deadline = time.Now().Add(time.Second)
	for !strings.Contains(b.Health().Sources[0].LastError, "wrongValue") && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if h := b.Health(); !strings.Contains(h.Sources[0].LastError, "REFUSE_Cmd") || h.Sources[0].QueuedWrites != 0 {
		t.Fatalf("refused write: %+v", h.Sources[0])
	}
	// A closed gate (a standby) neither queues nor records a command...
	gate := true
	var gmu sync.Mutex
	b.SetWriteGate(func() bool { gmu.Lock(); defer gmu.Unlock(); return gate })
	setGate := func(v bool) { gmu.Lock(); gate = v; gmu.Unlock() }
	setGate(false)
	_ = b.WriteOutputs(nio.Values{"PDU1_Outlet03_Cmd": false})
	_ = b.WriteOutputs(nio.Values{"PDU1_Outlet03_Cmd": true})
	time.Sleep(40 * time.Millisecond)
	if h := b.Health(); h.Sources[0].QueuedWrites != 0 || h.Writes != 1 {
		t.Fatalf("gated write: %+v writes=%d", h.Sources[0], h.Writes)
	}
	// ...so becoming leader replays nothing: the first value after the gate
	// opens is a baseline, and only a change after it is a command.
	setGate(true)
	_ = b.WriteOutputs(nio.Values{"PDU1_Outlet03_Cmd": true})
	time.Sleep(40 * time.Millisecond)
	if h := b.Health(); h.Writes != 1 {
		t.Fatalf("promotion replayed a command: writes=%d", h.Writes)
	}
	_ = b.WriteOutputs(nio.Values{"PDU1_Outlet03_Cmd": false})
	deadline = time.Now().Add(time.Second)
	for b.Health().Writes != 2 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	w.mu.Lock()
	got = strings.Join(w.writes, ",")
	w.mu.Unlock()
	if !strings.HasSuffix(got, "PDU1/PDU1_Outlet03.On=false") || b.Health().Writes != 2 {
		t.Fatalf("change after promotion: %q writes=%d", got, b.Health().Writes)
	}
	if err := b.WriteOutputs(nio.Values{"PDU1_Outlet03_Cmd": "off"}); err == nil {
		t.Fatal("a string into a BOOL command must error")
	}
}

func TestBaseClassAssignments(t *testing.T) {
	w := newFakeWire()
	cfg := switchConfig(w)
	cfg.Assignments = []ClassAssignment{{Class: "slow", Patterns: []string{"*.SpeedMbps", "SW1_Port02"}}}
	b, err := NewBase(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cls := b.ScanClasses()
	if strings.Join(cls["slow"], ",") != "SW1_Port01.Name,SW1_Port01.SpeedMbps,SW1_Port02.AdminUp,SW1_Port02.InBps,SW1_Port02.Name,SW1_Port02.OperUp,SW1_Port02.SpeedMbps" {
		t.Fatalf("slow = %v", cls["slow"])
	}
	if strings.Join(cls[DefaultClass], ",") != "SW1_Port01.AdminUp,SW1_Port01.InBps,SW1_Port01.OperUp" {
		t.Fatalf("default = %v", cls[DefaultClass])
	}
	cfg.Assignments = []ClassAssignment{{Class: "nope", Patterns: []string{"*"}}}
	if _, err := NewBase(cfg); err == nil || !strings.Contains(err.Error(), "undefined scan class") {
		t.Fatalf("undefined class: %v", err)
	}
}

func TestBaseValidation(t *testing.T) {
	w := newFakeWire()
	ok := switchConfig(w)
	cases := []struct {
		name string
		mut  func(c *Config)
		want string
	}{
		{"no poll", func(c *Config) { c.Poll = nil }, "Poll is required"},
		{"no sources", func(c *Config) { c.Sources = nil }, "at least one source"},
		{"dup source", func(c *Config) { c.Sources = append(c.Sources, c.Sources[0]) }, "both named"},
		{"unknown type", func(c *Config) { c.Tags[0].Type = "Router" }, "unknown type"},
		{"unknown source", func(c *Config) { c.Tags[0].Source = "SW9" }, "unknown source"},
		{"unknown member", func(c *Config) { c.Tags[0].Members["Colour"] = Binding{} }, "no member"},
		{"dup tag", func(c *Config) { c.Tags[1].Name = c.Tags[0].Name }, "declared twice"},
		{"dotted tag", func(c *Config) { c.Tags[0].Name = "SW1.Port" }, "cannot contain"},
		{"derived unknown", func(c *Config) { c.Tags[0].Members["Down"] = Binding{Derived: "AdminUp && !Link"} }, "not a member"},
		{"member class", func(c *Config) { c.Tags[0].Members["OperUp"] = Binding{ScanClass: "fastest"} }, "undefined scan class"},
		{"bad binding", func(c *Config) { c.Tags[0].Members["OperUp"] = Binding{Scale: 2} }, "scale/offset"},
		{"write no path", func(c *Config) { c.Writes = []WriteDecl{{Name: "X", Tag: "SW1_Port01", Member: "AdminUp"}} }, "no write path"},
		{"write bad tag", func(c *Config) {
			c.Write = w.write
			c.Writes = []WriteDecl{{Name: "X", Tag: "Nope", Member: "AdminUp"}}
		}, "unknown tag"},
		{"write clash", func(c *Config) {
			c.Write = w.write
			c.Writes = []WriteDecl{{Name: "SW1_Port01", Tag: "SW1_Port01", Member: "AdminUp"}}
		}, "same name as a struct tag"},
		{"bad class rate", func(c *Config) { c.ClassRates["slow"] = 0 }, "non-positive"},
	}
	for _, c := range cases {
		cfg := ok
		cfg.Sources = append([]SourceConfig(nil), ok.Sources...)
		cfg.Tags = []TagDecl{portDecl("SW1_Port01", "SW1"), portDecl("SW1_Port02", "SW1")}
		cfg.ClassRates = map[string]time.Duration{"slow": 50 * time.Millisecond}
		c.mut(&cfg)
		_, err := NewBase(cfg)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}
}

// The end-to-end proof: an ST program declares SW1_Port01 : SwitchPort from
// the generated hw_types.st, the runtime reads it from Base, and the
// program's member reads land on the members the driver filled — slot
// order agreement between the table, the rendered TYPE and the delivered
// value, checked through the real compiler and VM.
func TestBaseThroughRuntime(t *testing.T) {
	w := newFakeWire()
	cfg := switchConfig(w)
	cfg.Tags = []TagDecl{portDecl("SW1_Port01", "SW1")}
	b, err := NewBase(cfg)
	if err != nil {
		t.Fatal(err)
	}
	w.set("SW1", DefaultClass, Result{Updates: up("SW1_Port01", true, false, 125e6)})
	w.set("SW1", "slow", Result{Updates: []Update{{"SW1_Port01", "Name", ir.StringVal("Gi0/1")}}})
	types, err := TypesST("snmp")
	if err != nil {
		t.Fatal(err)
	}
	rt, err := runtime.New(runtime.Options{
		Libraries: []string{string(types)},
		Program: `PROGRAM Main
VAR_EXTERNAL
  SW1_Port01 : SwitchPort;
  SW1__Online : BOOL;
  PortDown : BOOL;
  PortName : STRING;
  Util : REAL;
END_VAR
IF SW1__Online THEN
  PortDown := SW1_Port01.Down;
  PortName := SW1_Port01.Name;
  Util := SW1_Port01.InPct;
END_IF;
END_PROGRAM`,
		Driver: b,
		Inputs: b.InputNames(),
		Tags: []runtime.TagDef{
			runtime.State("PortDown", false), runtime.State("PortName", ""), runtime.State("Util", 0.0),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b.Start(ctx)
	defer b.Stop()
	w.waitPoll(t, "SW1/default")
	w.waitPoll(t, "SW1/slow")
	rt.Scan()
	if v, _ := rt.Tags().ReadPath("PortDown"); v != true {
		t.Fatalf("PortDown = %v", v)
	}
	if v, _ := rt.Tags().ReadPath("PortName"); v != "Gi0/1" {
		t.Fatalf("PortName = %v", v)
	}
	if v, _ := rt.Tags().ReadPath("Util"); v != 12.5 {
		t.Fatalf("Util = %v", v)
	}
	if v, _ := rt.Tags().ReadPath("SW1_Port01.OperUp"); v != false {
		t.Fatalf("SW1_Port01.OperUp = %v", v)
	}
}

// The fixes the protocol builders asked for (2026-09-26 review round).

func TestBaseOnlineMemberAndRequests(t *testing.T) {
	w := newFakeWire()
	cfg := Config{
		Kind:    "test",
		Sources: []SourceConfig{{ID: "SW1", Interval: 20 * time.Millisecond, RetryMin: 10 * time.Millisecond, RetryMax: 20 * time.Millisecond, StaleAfter: 60 * time.Millisecond}},
		Tags: []TagDecl{{Name: "SW1", Type: "Switch", Source: "SW1", Members: map[string]Binding{
			"Name": {}, "PortsTotal": {Const: 28},
		}}},
		Poll: w.poll,
	}
	b, err := NewBase(cfg)
	if err != nil {
		t.Fatal(err)
	}
	w.set("SW1", DefaultClass, Result{Updates: []Update{{"SW1", "Name", ir.StringVal("sw1")}}, Requests: 7})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b.Start(ctx)
	defer b.Stop()
	w.waitPoll(t, "SW1/default")
	w.waitPoll(t, "SW1/default")
	vals, _ := b.ReadInputs()
	sw := vals["SW1"].(ir.Value)
	get := func(v ir.Value, m string) ir.Value { return v.Fld[v.Struct.FieldIndex[m]] }
	if !get(sw, "Online").B || get(sw, "PortsTotal").I != 28 {
		t.Fatalf("Switch = %+v", sw)
	}
	if h := b.Health(); h.Sources[0].Requests != 7 {
		t.Fatalf("Requests = %d", h.Sources[0].Requests)
	}
	// The same value is handed out while nothing changed (no copy).
	vals2, _ := b.ReadInputs()
	if sw2 := vals2["SW1"].(ir.Value); &sw2.Fld[0] != &sw.Fld[0] {
		t.Fatal("unchanged struct was copied between reads")
	}
	// The source goes silent: Online flips false, values hold.
	w.setErr("SW1", DefaultClass, errors.New("timeout"))
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		vals, _ = b.ReadInputs()
		if !get(vals["SW1"].(ir.Value), "Online").B {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	sw = vals["SW1"].(ir.Value)
	if get(sw, "Online").B || get(sw, "Name").S != "sw1" || vals["SW1__Online"] != false {
		t.Fatalf("after outage: %+v online=%v", sw, vals["SW1__Online"])
	}
	// A manifest that binds Online itself is left alone.
	cfg.Tags[0].Members["Online"] = Binding{Const: true}
	b2, err := NewBase(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if b2.tags["SW1"].onlineIdx != -1 {
		t.Fatal("a bound Online member must not be reconciled by Base")
	}
}

func TestBaseBadBeforeFirstDelivery(t *testing.T) {
	w := newFakeWire()
	b, _ := NewBase(switchConfig(w))
	// Port02's requests are refused from the very first poll: the source is
	// fresh and answering, so the tag is Bad, not NotConnected.
	w.set("SW1", DefaultClass, Result{Updates: up("SW1_Port01", true, true, 0), Bad: []string{"SW1_Port02"}})
	w.set("SW1", "slow", Result{Bad: []string{"SW1_Port02"}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b.Start(ctx)
	defer b.Stop()
	w.waitPoll(t, "SW1/default")
	w.waitPoll(t, "SW1/slow")
	w.waitPoll(t, "SW1/default")
	if q := b.Quality(); q["SW1_Port02"] != nio.Bad || q["SW1_Port01"] != nio.Good {
		t.Fatalf("quality = %v", q)
	}
	if vals, _ := b.ReadInputs(); vals["SW1_Port02"] != nil {
		t.Fatal("a never-delivered tag must not be handed out")
	}
}

func TestBaseWriteKindNoWriteAndLatch(t *testing.T) {
	w := newFakeWire()
	var got []string
	var mu sync.Mutex
	write := func(ctx context.Context, source string, d WriteDecl, v ir.Value) error {
		mu.Lock()
		defer mu.Unlock()
		if v.Kind == ir.TypeInt && v.I == 0 {
			return ErrNoWrite
		}
		if d.Name == "REFUSE_Cmd" {
			return errors.New("refused")
		}
		got = append(got, fmt.Sprintf("%s=%v/%d", d.Name, v.Kind, v.I))
		return nil
	}
	cfg := Config{
		Kind:    "test",
		Sources: []SourceConfig{{ID: "N1", Interval: 20 * time.Millisecond, RetryMin: 10 * time.Millisecond, RetryMax: 20 * time.Millisecond}},
		Tags:    []TagDecl{{Name: "N1", Type: "Server", Source: "N1", Members: map[string]Binding{"PowerOn": {}}}},
		Writes: []WriteDecl{
			{Name: "N1_PowerCmd", Tag: "N1", Member: "PowerOn", Kind: ir.TypeInt},
			{Name: "REFUSE_Cmd", Tag: "N1", Member: "PowerOn"},
		},
		Poll:  w.poll,
		Write: write,
	}
	b, err := NewBase(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewBase(Config{Kind: "x", Sources: cfg.Sources, Tags: cfg.Tags, Poll: w.poll, Write: write,
		Writes: []WriteDecl{{Name: "C", Tag: "N1", Member: "PowerOn", Kind: ir.TypeStruct}}}); err == nil {
		t.Fatal("a non-scalar write kind must be refused")
	}
	w.set("N1", DefaultClass, Result{Updates: []Update{{"N1", "PowerOn", ir.BoolVal(true)}}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b.Start(ctx)
	defer b.Stop()
	w.waitPoll(t, "N1/default")
	_ = b.WriteOutputs(nio.Values{"N1_PowerCmd": int64(0), "REFUSE_Cmd": false}) // baselines
	_ = b.WriteOutputs(nio.Values{"N1_PowerCmd": int64(3)})                      // ForceOff, as an INT
	_ = b.WriteOutputs(nio.Values{"N1_PowerCmd": int64(0)})                      // back to idle: nothing on the wire
	_ = b.WriteOutputs(nio.Values{"REFUSE_Cmd": true})
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n >= 1 && b.Health().Sources[0].LastWriteError != "" {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	mu.Lock()
	s := strings.Join(got, ",")
	mu.Unlock()
	if s != "N1_PowerCmd=INT/3" {
		t.Fatalf("writes = %q (the INT must not be flattened to the member's BOOL)", s)
	}
	h := b.Health()
	if h.Writes != 1 {
		t.Fatalf("Writes = %d; ErrNoWrite must not count", h.Writes)
	}
	if !strings.Contains(h.Sources[0].LastWriteError, "refused") {
		t.Fatalf("LastWriteError = %q", h.Sources[0].LastWriteError)
	}
	// The latch survives good polls and clears on the next accepted write.
	w.waitPoll(t, "N1/default")
	w.waitPoll(t, "N1/default")
	if b.Health().Sources[0].LastWriteError == "" {
		t.Fatal("a refused command must stay on the row across good polls")
	}
	_ = b.WriteOutputs(nio.Values{"N1_PowerCmd": int64(1)})
	deadline = time.Now().Add(time.Second)
	for b.Health().Sources[0].LastWriteError != "" && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if b.Health().Sources[0].LastWriteError != "" {
		t.Fatal("an accepted command must clear the latch")
	}
}

func TestBaseOnReconnect(t *testing.T) {
	w := newFakeWire()
	cfg := switchConfig(w)
	cfg.Sources[0].Enable = "EN"
	var mu sync.Mutex
	var calls []string
	cfg.OnReconnect = func(src string) { mu.Lock(); calls = append(calls, src); mu.Unlock() }
	b, _ := NewBase(cfg)
	w.set("SW1", DefaultClass, Result{Updates: up("SW1_Port01", true, true, 0)})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b.Start(ctx)
	defer b.Stop()
	deadline := time.Now().Add(time.Second)
	for b.Health().Sources[0].State != "parked" && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	_ = b.WriteOutputs(nio.Values{"EN": true}) // parked → polling: one reconnect
	w.waitPoll(t, "SW1/default")
	_ = b.WriteOutputs(nio.Values{"EN": false})
	deadline = time.Now().Add(time.Second)
	for b.Health().Sources[0].State != "parked" && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	_ = b.WriteOutputs(nio.Values{"EN": true}) // second reconnect
	w.waitPoll(t, "SW1/default")
	w.waitPoll(t, "SW1/default") // steady polling: no more calls
	mu.Lock()
	n := len(calls)
	mu.Unlock()
	if n != 2 {
		t.Fatalf("OnReconnect calls = %d, want 2", n)
	}
}

// A command queued while the device is down is sent when it comes back —
// unless it waited longer than StaleAfter: a ForceOff from an hour ago is
// not what anyone means now. Dropped with the reason on the device row.
func TestBaseStaleCommandsDropped(t *testing.T) {
	w := newFakeWire()
	cfg := Config{
		Kind:    "test",
		Sources: []SourceConfig{{ID: "PDU1", Interval: 10 * time.Millisecond, StaleAfter: 80 * time.Millisecond, RetryMin: 5 * time.Millisecond, RetryMax: 10 * time.Millisecond}},
		Tags: []TagDecl{{Name: "PDU1_Outlet03", Type: "PDUOutlet", Source: "PDU1", Members: map[string]Binding{
			"Index": {Const: 3}, "On": {},
		}}},
		Writes: []WriteDecl{{Name: "PDU1_Outlet03_Cmd", Tag: "PDU1_Outlet03", Member: "On"}},
		Poll:   w.poll,
		Write:  w.write,
	}
	b, err := NewBase(cfg)
	if err != nil {
		t.Fatal(err)
	}
	w.set("PDU1", DefaultClass, Result{Updates: []Update{{"PDU1_Outlet03", "On", ir.BoolVal(true)}}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b.Start(ctx)
	defer b.Stop()
	w.waitPoll(t, "PDU1/default")
	_ = b.WriteOutputs(nio.Values{"PDU1_Outlet03_Cmd": true}) // baseline
	writes := func() string { w.mu.Lock(); defer w.mu.Unlock(); return strings.Join(w.writes, ",") }

	// The device goes down for longer than StaleAfter; the command waits, then is dropped.
	w.setErr("PDU1", DefaultClass, errors.New("timeout"))
	time.Sleep(30 * time.Millisecond)
	_ = b.WriteOutputs(nio.Values{"PDU1_Outlet03_Cmd": false})
	time.Sleep(150 * time.Millisecond)
	w.setErr("PDU1", DefaultClass, nil)
	deadline := time.Now().Add(time.Second)
	for !strings.Contains(b.Health().Sources[0].LastWriteError, "dropped") && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if got := writes(); got != "" {
		t.Fatalf("a stale command reached the device: %q", got)
	}
	if h := b.Health(); !strings.Contains(h.Sources[0].LastWriteError, "dropped") || h.Sources[0].QueuedWrites != 0 {
		t.Fatalf("stale command: %+v", h.Sources[0])
	}

	// A fresh command still goes out.
	_ = b.WriteOutputs(nio.Values{"PDU1_Outlet03_Cmd": true})
	deadline = time.Now().Add(time.Second)
	for writes() == "" && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if got := writes(); got != "PDU1/PDU1_Outlet03.On=true" {
		t.Fatalf("fresh command: %q", got)
	}
}

// A class that never answers, next to one that does, does not hide: its
// tags go Bad after failuresToError of its own failures, while the device
// stays online for the class that answers; a good poll brings them back.
func TestBaseFailingClassGoesBad(t *testing.T) {
	w := newFakeWire()
	cfg := switchConfig(w)
	cfg.Assignments = []ClassAssignment{{Class: "slow", Patterns: []string{"SW1_Port02"}}}
	cfg.ClassRates = map[string]time.Duration{"slow": 20 * time.Millisecond}
	b, err := NewBase(cfg)
	if err != nil {
		t.Fatal(err)
	}
	w.set("SW1", DefaultClass, Result{Updates: up("SW1_Port01", true, true, 0)})
	w.set("SW1", "slow", Result{Updates: up("SW1_Port02", true, true, 0)})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b.Start(ctx)
	defer b.Stop()
	w.waitPoll(t, "SW1/slow")
	w.waitPoll(t, "SW1/default")
	if q := b.Quality(); q["SW1_Port02"] != nio.Good {
		t.Fatalf("before: %v", q)
	}
	w.setErr("SW1", "slow", errors.New("timeout"))
	deadline := time.Now().Add(2 * time.Second)
	for b.Quality()["SW1_Port02"] != nio.Bad && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	q := b.Quality()
	if q["SW1_Port02"] != nio.Bad || q["SW1_Port01"] != nio.Good {
		t.Fatalf("a failing class hid behind an answering one: %v", q)
	}
	if v, _ := b.ReadInputs(); v["SW1__Online"] == false {
		t.Fatal("the device still answers: online")
	}
	w.setErr("SW1", "slow", nil)
	deadline = time.Now().Add(2 * time.Second)
	for b.Quality()["SW1_Port02"] != nio.Good && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if q := b.Quality(); q["SW1_Port02"] != nio.Good {
		t.Fatalf("recovered class: %v", q)
	}
}
