package eip

import (
	"context"
	"net"
	"testing"
	"time"
)

// InputsReady (io.InputsReadier, #786): false until the first connection's
// priming poll delivered or the first attempt failed; sticky.

func TestInputsReadyOnFirstDeliveryThenSticky(t *testing.T) {
	host, port, store, _, stop := startEmulatorFull(t, false)
	store.UpdateValue("Speed", 42.5)
	d, err := New(host, testManifest(), WithPort(port), WithScanRate(30*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if d.InputsReady() {
		t.Fatal("ready before Start")
	}
	d.Start(context.Background())
	t.Cleanup(d.Stop)
	waitFor(t, "ready", d.InputsReady)
	if v, _ := d.ReadInputs(); v["Speed"] != 42.5 {
		t.Fatalf("ready before the first poll was delivered: %v", v)
	}
	stop()
	waitFor(t, "disconnect", func() bool { return !d.Health().Connected })
	if !d.InputsReady() {
		t.Fatal("InputsReady went false after a disconnect")
	}
}

func TestInputsReadyOnFailedFirstConnect(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	host, ps, _ := net.SplitHostPort(ln.Addr().String())
	ln.Close() // nothing listens on it now
	port, _ := net.LookupPort("tcp", ps)
	d, err := New(host, testManifest(), WithPort(port), WithScanRate(30*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if d.InputsReady() {
		t.Fatal("ready before Start")
	}
	d.Start(context.Background())
	t.Cleanup(d.Stop)
	waitFor(t, "ready after the failed connect", d.InputsReady)
}

func TestInputsReadyFalseWhileFirstAttemptHangs(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() { // accepts, never answers
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { c.Close() })
		}
	}()
	host, ps, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := net.LookupPort("tcp", ps)
	d, err := New(host, testManifest(), WithPort(port), WithScanRate(30*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	d.Start(context.Background())
	t.Cleanup(d.Stop)
	time.Sleep(150 * time.Millisecond)
	if d.InputsReady() {
		t.Fatal("ready while the first attempt is still waiting on the controller")
	}
}
