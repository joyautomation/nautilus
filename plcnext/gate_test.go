package plcnext

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
)

// With the write gate closed nothing leaves the driver over gRPC: no output
// span, and no BusConductor configure/start (commands too), while outputs
// keep changing; the status says the outputs hold. Opened, the bus is
// conducted, the whole frame goes out and every output is asked for once
// (#863).
func TestWriteGateGRPC(t *testing.T) {
	f := newFakeGDS()
	addr := serve(t, f)
	m, err := ParseManifest([]byte(benchManifest))
	if err != nil {
		t.Fatal(err)
	}
	d, err := New(m, WithScanRate(5*time.Millisecond), WithDialer(func(ctx context.Context) (dataAccess, error) { return Dial(ctx, addr) }))
	if err != nil {
		t.Fatal(err)
	}
	var open atomic.Bool
	if n := nio.SetPlainGate(d, open.Load); n != 1 {
		t.Fatalf("io.SetPlainGate installed %d gates, want the plcnext driver's", n)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.Start(ctx)
	defer d.Stop()

	deadline := time.Now().Add(5 * time.Second)
	for !d.Health().Connected {
		if time.Now().After(deadline) {
			t.Fatalf("never connected: %+v", d.Health())
		}
		time.Sleep(5 * time.Millisecond)
	}
	for _, v := range []bool{true, false, true} { // outputs changing while closed
		if err := d.WriteOutputs(nio.Values{"DO_00": v}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(30 * time.Millisecond)
	}
	if w := f.wrote(); strings.Contains(w, DefaultOutputs) || strings.Contains(w, "CONFIG_REQ") || strings.Contains(w, "START_IO_REQ") || strings.Contains(w, "CONFIG_MUST_MATCH") {
		t.Fatalf("with the gate closed the driver wrote %q", w)
	}
	if h := d.Health(); !h.GateClosed {
		t.Fatalf("health %+v, want the gate shown closed", h)
	}

	open.Store(true)
	deadline = time.Now().Add(5 * time.Second)
	for w := f.wrote(); !strings.Contains(w, "START_IO_REQ=true") || !strings.Contains(w, DefaultOutputs); w = f.wrote() {
		if time.Now().After(deadline) {
			t.Fatalf("gate open, but the bus was not conducted or the frame not written: %q", w)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !d.OutputsWanted() || d.OutputsWanted() {
		t.Fatal("a reopened gate must ask for every output, once")
	}
	if d.Health().GateClosed {
		t.Fatal("the gate still shows closed")
	}

	// Closed and reopened with nothing changed meanwhile: the frame still
	// goes out on the reopen, whether or not the runtime answers
	// OutputsWanted with a push.
	open.Store(false)
	deadline = time.Now().Add(5 * time.Second)
	for !d.Health().GateClosed {
		if time.Now().After(deadline) {
			t.Fatal("the gate never showed closed again")
		}
		time.Sleep(5 * time.Millisecond)
	}
	before := strings.Count(f.wrote(), DefaultOutputs)
	open.Store(true)
	deadline = time.Now().Add(5 * time.Second)
	for strings.Count(f.wrote(), DefaultOutputs) == before {
		if time.Now().After(deadline) {
			t.Fatal("reopened with no change: the frame was not written again")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
