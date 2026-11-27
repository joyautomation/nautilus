package runtime_test

import (
	"sync"
	"testing"

	nio "github.com/joyautomation/nautilus/io"
)

// lateDriver delivers nothing until deliver is called: the field bus has
// not answered yet (a driver connecting at start). It records every push.
type lateDriver struct {
	mu     sync.Mutex
	in     nio.Values // nil: nothing delivered yet
	pushes []nio.Values
}

func (d *lateDriver) deliver(v nio.Values) { d.mu.Lock(); d.in = v; d.mu.Unlock() }

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

// #786: a standalone controller's first scan must not run before its
// drivers have delivered the inputs: no logic, no outputs. With seeds,
// today's runtime scans on the placeholders and drives Out from them.
func TestFirstScanWaitsForInputs(t *testing.T) {
	drv := &lateDriver{}
	rt := newStandbyRuntime(t, drv, nil)

	rt.Scan()
	rt.Scan()
	if n := drv.pushCount(); n != 0 {
		t.Fatalf("before any input was delivered the runtime pushed %d times: %v", n, drv.pushes)
	}

	drv.deliver(nio.Values{"Level": 10.0, "Flow": 1.0})
	rt.Scan()
	if n := drv.pushCount(); n != 1 {
		t.Fatalf("after delivery: %d pushes, want 1", n)
	}
	if got := drv.pushes[0]["Out"]; got != 31.0 {
		t.Errorf("first push Out = %v, want 31 (Level 10 * SP 3 + Flow 1)", got)
	}
}
