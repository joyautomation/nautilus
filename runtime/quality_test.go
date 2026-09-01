package runtime_test

import (
	"errors"
	"sync"
	"testing"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/runtime"
)

const qualityProgram = `PROGRAM Q
VAR_EXTERNAL
  Level : REAL;
  Flow : REAL;
  Out : REAL;
END_VAR
Out := Level + Flow;
END_PROGRAM
`

// flakyDriver is a Memory driver whose reads can be made to fail — the
// case per-tag quality exists for, and one no in-tree driver could produce
// on demand before.
type flakyDriver struct {
	*nio.Memory
	mu   sync.Mutex
	fail bool
}

func (d *flakyDriver) setFail(b bool) {
	d.mu.Lock()
	d.fail = b
	d.mu.Unlock()
}

func (d *flakyDriver) ReadInputs() (nio.Values, error) {
	d.mu.Lock()
	fail := d.fail
	d.mu.Unlock()
	if fail {
		return nil, errors.New("bus timeout")
	}
	return d.Memory.ReadInputs()
}

// The runtime prefers ReadInputsInto when a driver has it; route that
// through the same failure switch or the test would silently never fail.
func (d *flakyDriver) ReadInputsInto(dst nio.Values) error {
	d.mu.Lock()
	fail := d.fail
	d.mu.Unlock()
	if fail {
		return errors.New("bus timeout")
	}
	return d.Memory.ReadInputsInto(dst)
}

func newQualityRuntime(t *testing.T, drv nio.Driver) *runtime.Runtime {
	t.Helper()
	rt, err := runtime.New(runtime.Options{
		Program: qualityProgram,
		Driver:  drv,
		Inputs:  []string{"Level", "Flow"},
		Outputs: []string{"Out"},
		Seed:    nio.Values{"Level": 1.0, "Flow": 2.0, "Out": 0.0},
	})
	if err != nil {
		t.Fatal(err)
	}
	return rt
}

// A healthy controller reports nothing. This is the property the whole
// design rests on: quality costs a working plant zero bytes.
func TestQualityIsEmptyWhenHealthy(t *testing.T) {
	drv := &flakyDriver{Memory: nio.NewMemory()}
	rt := newQualityRuntime(t, drv)
	rt.Scan()
	if q := rt.Quality(); len(q) != 0 {
		t.Errorf("healthy controller reported quality %v", q)
	}
	if rt.TagQuality("Level") != nio.Good {
		t.Error("healthy input is not Good")
	}
}

// Before the first scan nothing has been delivered, so the seeds must not
// present themselves as field readings.
func TestQualityBeforeFirstScan(t *testing.T) {
	drv := &flakyDriver{Memory: nio.NewMemory()}
	rt := newQualityRuntime(t, drv)
	q := rt.Quality()
	if q["Level"] != nio.Stale || q["Flow"] != nio.Stale {
		t.Errorf("pre-scan quality = %v, want both inputs stale", q)
	}
	// Only INPUTS: a setpoint or an output is the controller's own value,
	// and nothing about the field says anything about it.
	if _, said := q["Out"]; said {
		t.Errorf("output tag got a quality: %v", q)
	}
	rt.Scan()
	if q := rt.Quality(); len(q) != 0 {
		t.Errorf("after a good scan: %v", q)
	}
}

// The runtime's own derivation: a failed READ means the scan ran on
// last-known values, which is exactly Stale — and it self-clears.
func TestQualityDerivesStaleFromFailedReads(t *testing.T) {
	drv := &flakyDriver{Memory: nio.NewMemory()}
	rt := newQualityRuntime(t, drv)
	rt.Scan()

	drv.setFail(true)
	rt.Scan()
	q := rt.Quality()
	if q["Level"] != nio.Stale || q["Flow"] != nio.Stale {
		t.Errorf("after a failed read: %v, want both inputs stale", q)
	}
	if rt.TagQuality("Flow") != nio.Stale {
		t.Error("TagQuality disagrees with Quality")
	}

	drv.setFail(false)
	rt.Scan()
	if q := rt.Quality(); len(q) != 0 {
		t.Errorf("after recovery: %v, want empty", q)
	}
}

// A failed WRITE must NOT make the readings stale: the outputs not landing
// says nothing about how old the inputs are, and calling every input stale
// over it would put a bad-quality badge on every screen in the plant every
// time an actuator refused a command.
func TestQualityIgnoresFailedWrites(t *testing.T) {
	drv := &writeFailDriver{Memory: nio.NewMemory()}
	rt := newQualityRuntime(t, drv)
	rt.Scan()
	if !rt.Stats().IOHealthy {
		// Sanity: the write really did fail and the scan noticed.
		t.Log("write failed as intended")
	} else {
		t.Fatal("the write driver did not fail")
	}
	if q := rt.Quality(); len(q) != 0 {
		t.Errorf("a failed write produced quality %v", q)
	}
}

type writeFailDriver struct{ *nio.Memory }

func (d *writeFailDriver) WriteOutputs(nio.Values) error {
	return errors.New("actuator refused")
}

// A driver that reports quality is the authority: whatever it says stands,
// including over the runtime's derived staleness.
func TestQualityFromDriverWins(t *testing.T) {
	drv := &flakyDriver{Memory: nio.NewMemory()}
	drv.SetQuality("Level", nio.NotConnected)
	drv.SetQuality("Flow", nio.Bad)
	rt := newQualityRuntime(t, drv)
	rt.Scan()

	q := rt.Quality()
	if q["Level"] != nio.NotConnected || q["Flow"] != nio.Bad {
		t.Fatalf("driver-reported quality = %v", q)
	}

	// Now break the reads too. Level and Flow keep the driver's verdict —
	// "this node never birthed" is more specific than "the read failed" —
	// and nothing else appears, because those are the only inputs.
	drv.setFail(true)
	rt.Scan()
	q = rt.Quality()
	if q["Level"] != nio.NotConnected || q["Flow"] != nio.Bad {
		t.Errorf("driver verdict overwritten by derivation: %v", q)
	}

	// A driver reporting one input but not the other: the unreported one
	// falls through to the derived Stale.
	drv.SetQuality("Flow", nio.Good) // clears it
	rt.Scan()
	q = rt.Quality()
	if q["Level"] != nio.NotConnected || q["Flow"] != nio.Stale {
		t.Errorf("mixed sources = %v", q)
	}
}

// Good entries from a driver are dropped rather than published: "absent
// means Good" is the payload budget, and a chatty driver must not blow it.
func TestQualityDropsGoodEntries(t *testing.T) {
	drv := &chattyDriver{Memory: nio.NewMemory()}
	rt := newQualityRuntime(t, drv)
	rt.Scan()
	q := rt.Quality()
	if len(q) != 1 || q["Flow"] != nio.Bad {
		t.Errorf("quality = %v, want only Flow:bad", q)
	}
}

type chattyDriver struct{ *nio.Memory }

func (d *chattyDriver) Quality() map[string]nio.Quality {
	return map[string]nio.Quality{"Level": nio.Good, "Flow": nio.Bad, "Out": nio.Good}
}

// Quality belongs to the delivery, and a UDT is delivered whole — so a
// member address is exactly as trustworthy as its root.
func TestTagQualityResolvesDottedPathsToTheirRoot(t *testing.T) {
	drv := &flakyDriver{Memory: nio.NewMemory()}
	drv.SetQuality("Level", nio.Stale)
	rt := newQualityRuntime(t, drv)
	rt.Scan()
	if got := rt.TagQuality("Level.Anything.Deep"); got != nio.Stale {
		t.Errorf("TagQuality(dotted) = %v, want stale", got)
	}
	if got := rt.TagQuality("Nothing.At.All"); got != nio.Good {
		t.Errorf("unknown tag = %v, want good", got)
	}
}

// The capability flag: "everything is good" must be distinguishable from
// "this controller has no idea", or an HMI paints a reassuring badge on a
// runtime that cannot report anything.
func TestReportsQuality(t *testing.T) {
	drv := &flakyDriver{Memory: nio.NewMemory()}
	if rt := newQualityRuntime(t, drv); !rt.ReportsQuality() {
		t.Error("a driver-bound runtime does not report quality")
	}
	// No driver, no inputs: nothing here can ever be non-Good.
	rt, err := runtime.New(runtime.Options{Program: qualityProgram,
		Seed: nio.Values{"Level": 0.0, "Flow": 0.0, "Out": 0.0}})
	if err != nil {
		t.Fatal(err)
	}
	if rt.ReportsQuality() {
		t.Error("a driverless runtime claims to report quality")
	}
	if q := rt.Quality(); len(q) != 0 {
		t.Errorf("driverless quality = %v", q)
	}
}
