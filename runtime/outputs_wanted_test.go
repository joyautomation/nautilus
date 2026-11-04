package runtime_test

import (
	"sync/atomic"
	"testing"

	nio "github.com/joyautomation/nautilus/io"
)

// askingDriver is a scriptDriver that implements io.OutputsRequester: the
// test sets want, as a driver does when it forgot a command, and counts
// how often the runtime asked. ins/outs make it routable in an io.Multi.
type askingDriver struct {
	*scriptDriver
	ins, outs []string
	want      atomic.Bool
	asks      atomic.Int64
}

func (d *askingDriver) OutputsWanted() bool {
	d.asks.Add(1)
	return d.want.Swap(false)
}

func (d *askingDriver) InputNames() []string  { return d.ins }
func (d *askingDriver) OutputNames() []string { return d.outs }

// A driver that asks for the outputs gets all of them on the next scan,
// none changed, and only on that scan: the request is answered once. It is
// asked on every scan, and a push made anyway (an output moved) answers a
// request made before it, so the scan after is quiet again (#601).
func TestOutputsWantedPushesEveryOutputOnce(t *testing.T) {
	drv := &askingDriver{scriptDriver: &scriptDriver{in: nio.Values{"In": 1.0}}}
	rt := newDoubler(t, drv, nil)
	rt.Scan()
	rt.Scan()
	if got := drv.writeCount(); got != 1 {
		t.Fatalf("first scan, then a quiet one: WriteOutputs called %d times, want 1", got)
	}

	drv.want.Store(true) // the driver forgot a command
	rt.Scan()
	if got := drv.writeCount(); got != 2 {
		t.Fatalf("the scan after the request: WriteOutputs total %d, want 2", got)
	}
	drv.mu.Lock()
	out := drv.lastOut["Out"]
	drv.mu.Unlock()
	if out != 2.0 {
		t.Errorf("the requested push handed Out = %v, want the unchanged 2", out)
	}
	rt.Scan()
	rt.Scan()
	if got := drv.writeCount(); got != 2 {
		t.Fatalf("quiet scans after the requested push: WriteOutputs total %d, want still 2", got)
	}

	// A request a push answers anyway: the output moves on the same scan.
	drv.want.Store(true)
	drv.mu.Lock()
	drv.in["In"] = 3.0
	drv.mu.Unlock()
	rt.Scan()
	rt.Scan()
	if got := drv.writeCount(); got != 3 {
		t.Fatalf("a request and a change on one scan: WriteOutputs total %d, want 3 (one push)", got)
	}
	if got := drv.asks.Load(); got != 7 {
		t.Errorf("OutputsWanted asked %d times over 7 scans, want once per scan", got)
	}
}

// io.Multi forwards the request: a child that asks gets the runtime to
// push, and every child is asked on each scan (each answer is a
// read-and-clear), so two children asking at once get one push between
// them, not one now and one on the next scan.
func TestMultiForwardsOutputsWanted(t *testing.T) {
	writer := &askingDriver{scriptDriver: &scriptDriver{in: nio.Values{"In": 1.0}}, ins: []string{"In"}, outs: []string{"Out"}}
	other := &askingDriver{scriptDriver: &scriptDriver{in: nio.Values{"X": 0.0}}, ins: []string{"X"}}
	m, err := nio.NewMulti(nio.NamedDriver{Name: "a", Driver: writer}, nio.NamedDriver{Name: "b", Driver: other})
	if err != nil {
		t.Fatal(err)
	}
	rt := newDoubler(t, m, nil)
	rt.Scan()
	rt.Scan()
	if got := writer.writeCount(); got != 1 {
		t.Fatalf("first scan, then a quiet one: WriteOutputs called %d times, want 1", got)
	}

	other.want.Store(true) // the child without the output asks
	rt.Scan()
	if got := writer.writeCount(); got != 2 {
		t.Fatalf("one child asked: WriteOutputs total %d, want 2", got)
	}

	writer.want.Store(true)
	other.want.Store(true)
	rt.Scan()
	rt.Scan()
	if got := writer.writeCount(); got != 3 {
		t.Fatalf("both children asked on one scan: WriteOutputs total %d, want 3 (one push)", got)
	}
	if a, b := writer.asks.Load(), other.asks.Load(); a != 5 || b != 5 {
		t.Errorf("children asked %d and %d times over 5 scans, want 5 each", a, b)
	}
}
