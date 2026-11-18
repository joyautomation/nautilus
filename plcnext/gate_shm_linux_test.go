package plcnext

import (
	"context"
	"encoding/binary"
	"math/bits"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
)

// With the write gate closed nothing is written to the page, the
// every-scan heartbeat included, so the shim's output watchdog drives the
// frame to zero: the shim's own safe state. Opened, writes resume with the
// whole frame (#863).
func TestWriteGateShm(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nautilus-plcnext-io")
	shim := startFakeShim(t, path)
	noBC := strings.Replace(benchManifest, "bus-conductor: BcComponent1", `bus-conductor: ""`, 1) // never dials
	m, err := ParseManifest([]byte("transport: shm\nshm: " + path + "\noutput-watchdog: 100ms\n" + noBC))
	if err != nil {
		t.Fatal(err)
	}
	d, err := New(m, WithScanRate(5*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	var open atomic.Bool
	open.Store(true)
	d.SetWriteGate(open.Load)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.Start(ctx)
	defer d.Stop()

	if err := d.WriteOutputs(nio.Values{"DO_00": true}); err != nil {
		t.Fatal(err)
	}
	set := func() int { return bits.OnesCount8(shim.out(0)) + bits.OnesCount8(shim.out(1)) } // DO_00..DO_15
	eventually(t, "the output never reached the frame", func() bool { return set() == 1 })

	open.Store(false)
	eventually(t, "the gate never showed closed", func() bool { return d.Health().GateClosed })
	gen := binary.LittleEndian.Uint64(shim.mem[shmOffOutGen:])
	if err := d.WriteOutputs(nio.Values{"DO_15": true}); err != nil {
		t.Fatal(err)
	}
	eventuallyWithin(t, 2*time.Second, "the shim's watchdog never zeroed the frame of a gated driver", func() bool { return set() == 0 })
	if g := binary.LittleEndian.Uint64(shim.mem[shmOffOutGen:]); g != gen {
		t.Fatalf("the page's output generation went %d → %d with the gate closed: the driver kept writing", gen, g)
	}

	open.Store(true)
	eventually(t, "gate open, but the whole frame never came back", func() bool { return set() == 2 })
	if !d.OutputsWanted() {
		t.Fatal("a reopened gate must ask for every output")
	}
}
