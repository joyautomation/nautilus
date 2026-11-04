package main

import (
	"context"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/internal/project"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/modbus"
	"github.com/joyautomation/nautilus/modbus/slave"
	"github.com/joyautomation/nautilus/runtime"
)

// hangingModbus is a Modbus driver whose next read, as the scan makes it,
// can be made to block: a scan hung in a driver call. Its own loop (polls,
// rewrite: keep-alives) carries on, as a real driver's does.
type hangingModbus struct {
	*modbus.Driver
	mu         sync.Mutex
	hang, hung chan struct{}
}

func (h *hangingModbus) arm() (hung <-chan struct{}, release func()) {
	hg, s := make(chan struct{}), make(chan struct{})
	h.mu.Lock()
	h.hang, h.hung = hg, s
	h.mu.Unlock()
	var once sync.Once
	return s, func() { once.Do(func() { close(hg) }) }
}

func (h *hangingModbus) wait() {
	h.mu.Lock()
	hg, s := h.hang, h.hung
	h.hang, h.hung = nil, nil
	h.mu.Unlock()
	if hg != nil {
		close(s)
		<-hg
	}
}

func (h *hangingModbus) ReadInputs() (nio.Values, error) {
	h.wait()
	return h.Driver.ReadInputs()
}

func (h *hangingModbus) ReadInputsInto(dst nio.Values) error {
	h.wait()
	return h.Driver.ReadInputsInto(dst)
}

// writeCounter counts the Modbus register-write requests a connection
// sends (function codes 6 and 16; the function code follows the 7-byte
// MBAP header).
type writeCounter struct {
	net.Conn
	n *atomic.Int64
}

func (c writeCounter) Write(b []byte) (int, error) {
	if len(b) > 7 && (b[7] == 6 || b[7] == 16) {
		c.n.Add(1)
	}
	return c.Conn.Write(b)
}

// Without `redundancy:`, naut run still gates the drivers on live scans
// (#625): a hung scan stops a Modbus rewrite: keep-alive, so the device's
// watchdog sees the silence it exists for, and the keep-alive resumes once
// scans complete. While healthy, nothing changes: the runtime is not
// coordinated (no takeover) and the gate is open from the start.
func TestNoRedundancyAHungScanStopsTheRewriteKeepAlive(t *testing.T) {
	srv := slave.NewServer("", nil)
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Stop)
	host, port, _ := net.SplitHostPort(srv.Addr())
	p, _ := strconv.Atoi(port)

	var writes atomic.Int64
	d, err := modbus.New(modbus.Manifest{
		Sources: []modbus.Source{{
			ID: "DEV", Host: host, Port: p, UnitID: 1,
			Timeout: 500 * time.Millisecond, RetryMin: 20 * time.Millisecond, RetryMax: 80 * time.Millisecond,
		}},
		Tags: []modbus.TagBinding{{
			Name: "KA", Source: "DEV", Table: modbus.TableHolding, Address: 400, Format: "uint16",
			Writable: true, WriteOnly: true, Rewrite: 30 * time.Millisecond,
		}},
	}, modbus.WithScanRate(20*time.Millisecond), modbus.WithDialer(func(ctx context.Context, addr string) (net.Conn, error) {
		var nd net.Dialer
		c, err := nd.DialContext(ctx, "tcp", addr)
		if err != nil {
			return nil, err
		}
		return writeCounter{Conn: c, n: &writes}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	drv := &hangingModbus{Driver: d}

	proj := &project.Project{Name: "plant", Runtime: runtime.Options{
		Program: `PROGRAM KeepAlive
VAR_EXTERNAL KA : DINT; END_VAR
KA := 7;
END_PROGRAM`,
		Driver:  drv,
		Outputs: []string{"KA"},
		Seed:    nio.Values{"KA": int64(0)},
		Scan:    10 * time.Millisecond,
	}}
	rt, err := runtime.New(proj.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	gate := gateOnLiveScans(proj, nil, rt, 200*time.Millisecond)
	if proj.Runtime.Coordinator != nil {
		t.Fatal("without redundancy the runtime must stay uncoordinated (no takeover at start)")
	}
	if !gate.IsLeader() {
		t.Fatal("the gate is closed before anything ran: a healthy project's drivers would see it closed")
	}

	ctx, cancel := context.WithCancel(context.Background())
	gateDone, runDone := make(chan struct{}), make(chan struct{})
	go func() { gate.Run(ctx); close(gateDone) }()
	drv.Start(ctx)
	go func() { rt.Run(ctx); close(runDone) }()
	var releaseHang func()
	t.Cleanup(func() {
		if releaseHang != nil {
			releaseHang()
		}
		cancel()
		<-runDone
		<-gateDone
		drv.Stop()
	})

	waitUntil := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatal("timed out waiting for " + what)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	waitUntil("the keep-alive to flow", func() bool { return srv.Unit(1).Holding(400) == 7 && writes.Load() >= 4 })

	hung, rel := drv.arm()
	releaseHang = rel
	select {
	case <-hung:
	case <-time.After(5 * time.Second):
		t.Fatal("the scan's read never blocked")
	}
	waitUntil("the gate to close on the hung scan", func() bool { return !gate.IsLeader() })
	time.Sleep(60 * time.Millisecond) // drain a fire in flight
	quiet := writes.Load()
	time.Sleep(200 * time.Millisecond) // more than six keep-alive periods
	if now := writes.Load(); now != quiet {
		t.Fatalf("the keep-alive kept writing while the scan was hung: %d → %d write requests", quiet, now)
	}

	releaseHang()
	waitUntil("the keep-alive to resume", func() bool { return gate.IsLeader() && writes.Load() > quiet+1 })
}
