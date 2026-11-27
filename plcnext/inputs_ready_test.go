package plcnext

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	gdspb "github.com/joyautomation/nautilus/plcnext/gen/plc/gds"
	"github.com/joyautomation/nautilus/plcnext/plcnexttest"
)

// InputsReady (io.InputsReadier, #786): false until the first poll of the
// input image is over or the first session failed; sticky.

func readyDriver(t *testing.T, dial func(ctx context.Context) (dataAccess, error)) *Driver {
	t.Helper()
	m, err := ParseManifest([]byte(benchManifest))
	if err != nil {
		t.Fatal(err)
	}
	d, err := New(m, WithScanRate(10*time.Millisecond), WithDialer(dial))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func startReady(t *testing.T, d *Driver) {
	t.Helper()
	d.Start(context.Background())
	t.Cleanup(d.Stop)
}

func waitReady(t *testing.T, d *Driver, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if d.InputsReady() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("InputsReady never became true: %s", what)
}

func TestInputsReadyAfterTheFirstImageThenSticky(t *testing.T) {
	f := plcnexttest.New()
	addr := SocketTarget(f.Listen(t))
	release := make(chan struct{})
	d := readyDriver(t, func(ctx context.Context) (dataAccess, error) {
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return Dial(ctx, addr)
	})
	if d.InputsReady() {
		t.Fatal("ready before Start")
	}
	startReady(t, d)
	time.Sleep(50 * time.Millisecond)
	if d.InputsReady() {
		t.Fatal("ready while the first attempt is still dialling")
	}
	f.SetInput(1, 0x01)
	close(release)
	waitReady(t, d, "after the first image")
	if v, _ := d.ReadInputs(); v["DI_00"] != true {
		t.Fatalf("ready before the first image was delivered: %v", v)
	}
	// The input port goes missing: every poll now fails, the session ends.
	f.SetPortError(DefaultInputs, gdspb.DataAccessError_DAE_NotExists)
	deadline := time.Now().Add(5 * time.Second)
	for d.Health().Errors == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if d.Health().Errors == 0 {
		t.Fatal("the later failure never happened")
	}
	if !d.InputsReady() {
		t.Fatal("InputsReady went false after a later failure")
	}
}

func TestInputsReadyAfterAFailedFirstDial(t *testing.T) {
	var tries atomic.Int32
	d := readyDriver(t, func(ctx context.Context) (dataAccess, error) {
		tries.Add(1)
		return nil, errors.New("connection refused")
	})
	startReady(t, d)
	waitReady(t, d, "after the first dial failed")
	if tries.Load() == 0 {
		t.Fatal("never dialled")
	}
}

func TestInputsReadyWhenTheFirstReadFails(t *testing.T) {
	f := plcnexttest.New()
	f.SetPortError(DefaultInputs, gdspb.DataAccessError_DAE_NotExists)
	addr := SocketTarget(f.Listen(t))
	d := readyDriver(t, func(ctx context.Context) (dataAccess, error) { return Dial(ctx, addr) })
	startReady(t, d)
	waitReady(t, d, "after the first read failed")
}
