package modbus

import (
	"context"
	"net"
	"testing"
	"time"
)

// InputsReady (io.InputsReadier, #786): false until every source's first
// attempt is over, true after a first delivery or a first failure, and
// sticky.

func inputsReadyManifest(srcs ...Source) Manifest {
	m := Manifest{Sources: srcs}
	for _, s := range srcs {
		m.Tags = append(m.Tags, TagBinding{Name: s.ID + "_PV", Source: s.ID, Table: TableHolding, Address: 0, Format: "uint16"})
	}
	return m
}

// closedAddr is an address nothing listens on.
func closedAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

func TestInputsReadyFalseUntilStartedThenTrueOnFirstRead(t *testing.T) {
	srv := newSlave(t)
	srv.Unit(1).SetHolding(0, 5)
	d, err := New(inputsReadyManifest(fastSource("DEV", srv.Addr())), WithScanRate(15*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if d.InputsReady() {
		t.Fatal("ready before Start")
	}
	startDriver(t, d)
	waitFor(t, "ready", d.InputsReady)
	if v, _ := d.ReadInputs(); v["DEV_PV"] != int64(5) {
		t.Fatalf("ready before the first read was delivered: %v", v)
	}
}

func TestInputsReadyTrueOnFailedFirstConnect(t *testing.T) {
	d, err := New(inputsReadyManifest(fastSource("DEV", closedAddr(t))), WithScanRate(15*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if d.InputsReady() {
		t.Fatal("ready before Start")
	}
	startDriver(t, d)
	waitFor(t, "ready after the failed connect", d.InputsReady)
}

func TestInputsReadyWaitsForEverySource(t *testing.T) {
	srv, srv2 := newSlave(t), newSlave(t)
	srv.Unit(1).SetHolding(0, 5)
	srv2.Unit(1).SetHolding(0, 6)
	// The second source's dial waits until released, so only the first settles.
	release := make(chan struct{})
	slow := fastSource("SLOW", srv2.Addr())
	slow.Timeout = 10 * time.Second
	d, err := New(inputsReadyManifest(fastSource("DEV", srv.Addr()), slow),
		WithScanRate(15*time.Millisecond),
		WithDialer(func(ctx context.Context, addr string) (net.Conn, error) {
			if addr == srv2.Addr() {
				select {
				case <-release:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			var nd net.Dialer
			return nd.DialContext(ctx, "tcp", addr)
		}))
	if err != nil {
		t.Fatal(err)
	}
	startDriver(t, d)
	waitFor(t, "first source delivered", func() bool { v, _ := d.ReadInputs(); return v["DEV_PV"] != nil })
	if d.InputsReady() {
		t.Fatal("ready while a source has not settled")
	}
	close(release)
	waitFor(t, "ready once the second settles", d.InputsReady)
}

func TestInputsReadyStaysTrueAfterALaterFailure(t *testing.T) {
	srv := newSlave(t)
	srv.Unit(1).SetHolding(0, 5)
	d, err := New(inputsReadyManifest(fastSource("DEV", srv.Addr())), WithScanRate(15*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	startDriver(t, d)
	waitFor(t, "ready", d.InputsReady)
	srv.Stop()
	waitFor(t, "source reported dead", func() bool { return d.Health().Sources[0].State != "connected" })
	for i := 0; i < 20; i++ {
		if !d.InputsReady() {
			t.Fatal("InputsReady went false after an outage")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestInputsReadyWithNoSources(t *testing.T) {
	d, err := New(Manifest{}, WithScanRate(15*time.Millisecond))
	if err != nil {
		t.Skipf("an empty manifest is refused: %v", err)
	}
	if !d.InputsReady() {
		t.Fatal("a driver with no sources must be ready at once")
	}
}
