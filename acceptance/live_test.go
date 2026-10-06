package acceptance

import (
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/joyautomation/nautilus/runtime"
)

// A stand-in controller: a mirror store, a write that lands after a
// "poll", and seal-in logic that runs on every write — enough to prove
// the live runner's mechanics without a PLC.
type fakeController struct {
	rt   *runtime.Runtime
	mu   sync.Mutex
	vals map[string]any
}

func newFakeController(t *testing.T) *fakeController {
	t.Helper()
	rt, err := runtime.New(runtime.Options{Program: "PROGRAM Logix\nEND_PROGRAM\n"})
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeController{rt: rt, vals: map[string]any{
		"MainProgram_StartPB": false, "MainProgram_StopPB": false, "MainProgram_RunCmd": false,
		"MainProgram_LevelPct": 0.0, "MainProgram_HiLevelSP": 85.0, "MainProgram_HiLevelAlm": false,
		"Heartbeat": 0,
	}}
	f.publish()
	return f
}

// publish is "the next poll": the mirror takes the controller's values.
func (f *fakeController) publish() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for k, v := range f.vals {
		f.rt.Tags().Set(k, v)
	}
}

// write is the EtherNet/IP write plus the logic the controller scans.
func (f *fakeController) write(name string, v any) error {
	f.mu.Lock()
	if _, ok := f.vals[name]; !ok {
		f.mu.Unlock()
		return nil // silently lost, like a wrong tag on a real controller
	}
	f.vals[name] = v
	start, _ := f.vals["MainProgram_StartPB"].(bool)
	stop, _ := f.vals["MainProgram_StopPB"].(bool)
	run, _ := f.vals["MainProgram_RunCmd"].(bool)
	f.vals["MainProgram_RunCmd"] = (start || run) && !stop
	lvl, _ := f.vals["MainProgram_LevelPct"].(float64)
	sp, _ := f.vals["MainProgram_HiLevelSP"].(float64)
	f.vals["MainProgram_HiLevelAlm"] = lvl >= sp
	f.mu.Unlock()
	// The value is visible after the next poll, not instantly.
	time.AfterFunc(5*time.Millisecond, f.publish)
	return nil
}

func (f *fakeController) live() Live {
	return Live{Runtime: f.rt, Write: f.write, Poll: 10 * time.Millisecond, Scan: 10 * time.Millisecond,
		Resolve: ResolveLogix(f.rt, "MainProgram")}
}

func liveSuite(t *testing.T, yaml string) *Suite {
	t.Helper()
	fsys := fstest.MapFS{"live_test.yaml": {Data: []byte(yaml)}}
	s, err := LoadSuite(fsys, "live_test.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// DemoLine's two rungs, as the scenarios the plant would write — names as
// in the nautilus source, resolved to the controller's program scope.
func TestLiveSealInAndAlarm(t *testing.T) {
	c := newFakeController(t)
	s := liveSuite(t, `
tests:
  - name: start seals in and stop drops it
    steps:
      - given: { StartPB: true }
        scans: 1
        expect: { RunCmd: true }
      - given: { StartPB: false }
        scans: 1
        expect: { RunCmd: true }
      - given: { StopPB: true }
        scans: 1
        expect: { RunCmd: false }
  - name: high level alarm at the setpoint
    given: { LevelPct: 84.9 }
    steps:
      - scans: 1
        expect: { HiLevelAlm: false }
      - given: { LevelPct: 85.0 }
        until: 1s
        expect: { HiLevelAlm: true }
  - name: a wrong expectation fails with the controller's value
    given: { StopPB: true, StartPB: true }
    scans: 1
    expect: { RunCmd: true }
`)
	res, err := RunSuiteLive(s, c.live())
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 3 || !res[0].Passed || !res[1].Passed {
		t.Fatalf("results = %+v", res)
	}
	if res[2].Passed || res[2].Failure == nil || !strings.Contains(res[2].Failure.Detail, "MainProgram_RunCmd = false, want true") {
		t.Errorf("third test should fail naming the controller's value: %+v", res[2].Failure)
	}
	if res[0].Elapsed <= 0 {
		t.Error("elapsed is wall time and must be positive")
	}
}

func TestLiveUntilHoldAndAlways(t *testing.T) {
	c := newFakeController(t)
	s := liveSuite(t, `
tests:
  - name: hold keeps the condition for its window
    given: { StartPB: true }
    steps:
      - until: 500ms
        hold: 50ms
        expect: { RunCmd: true }
        always: { StopPB: false }
  - name: an invariant that breaks fails the step
    given: { StartPB: true }
    steps:
      - given: { StopPB: true }
        advance: 30ms
        always: { RunCmd: true }
`)
	res, err := RunSuiteLive(s, c.live())
	if err != nil {
		t.Fatal(err)
	}
	if !res[0].Passed {
		t.Errorf("hold: %+v", res[0].Failure)
	}
	if res[1].Passed || res[1].Failure.Reason != "invariant broke" {
		t.Errorf("always: %+v", res[1].Failure)
	}
}

func TestLiveRefusesWhatAControllerCannotDo(t *testing.T) {
	c := newFakeController(t)
	s := liveSuite(t, `
tests:
  - name: unknown tag
    given: { NoSuchTag: true }
    scans: 1
`)
	_, err := RunSuiteLive(s, c.live())
	if err == nil || !strings.Contains(err.Error(), `no tag "NoSuchTag" on the controller`) {
		t.Fatalf("err = %v", err)
	}
	s = liveSuite(t, `
tests:
  - name: a write that never reads back
    given: { Heartbeat: 5 }
    scans: 1
`)
	// Heartbeat is written but the fake "controller" owns it: a real one
	// overwrites an input it computes, and the runner must say so.
	lost := func(name string, v any) error { return nil }
	_, err = RunSuiteLive(s, Live{Runtime: c.rt, Write: lost, Poll: 5 * time.Millisecond, Resolve: ResolveLogix(c.rt, "MainProgram")})
	if err == nil || !strings.Contains(err.Error(), "still reads") {
		t.Fatalf("err = %v", err)
	}
}

// With a heartbeat, `scans: n` is exactly n counts of the controller's
// scan counter, and a counter that stops is an error.
func TestLiveScansWaitOnTheHeartbeat(t *testing.T) {
	c := newFakeController(t)
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		tk := time.NewTicker(4 * time.Millisecond)
		defer tk.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tk.C:
				c.mu.Lock()
				c.vals["Heartbeat"] = c.vals["Heartbeat"].(int) + 1
				c.mu.Unlock()
				c.publish()
			}
		}
	}()
	live := c.live()
	live.Heartbeat = "Heartbeat"
	s := liveSuite(t, `
tests:
  - name: twenty scans
    given: { StartPB: true }
    scans: 20
    expect: { RunCmd: true }
`)
	res, err := RunSuiteLive(s, live)
	if err != nil {
		t.Fatal(err)
	}
	if !res[0].Passed || res[0].Scans < 20 || res[0].Scans > 24 {
		t.Fatalf("result = %+v", res[0])
	}
	// A stopped counter: the controller is not scanning.
	c2 := newFakeController(t)
	live2 := c2.live()
	live2.Heartbeat = "Heartbeat"
	live2.Scan = 5 * time.Millisecond
	_, err = RunSuiteLive(liveSuite(t, "tests:\n  - name: stuck\n    scans: 3\n"), live2)
	if err == nil || !strings.Contains(err.Error(), "did not advance") {
		t.Fatalf("err = %v", err)
	}
}

// Each test starts from the input seeds, whatever the previous test left.
func TestLiveReseedsInputsPerTest(t *testing.T) {
	c := newFakeController(t)
	live := c.live()
	live.Seeds = map[string]any{"StartPB": false, "StopPB": false}
	s := liveSuite(t, `
tests:
  - name: leaves the start button pressed
    given: { StartPB: true }
    scans: 1
    expect: { RunCmd: true }
  - name: starts from the seeds anyway
    given: { StopPB: true }
    scans: 1
    expect: { StartPB: false, RunCmd: false }
`)
	res, err := RunSuiteLive(s, live)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res {
		if !r.Passed {
			t.Errorf("%s: %+v", r.Name, r.Failure)
		}
	}
}
