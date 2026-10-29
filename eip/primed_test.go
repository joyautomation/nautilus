package eip

import (
	"context"
	"fmt"
	"io"
	"net"
	"testing"
	"time"
)

// slowProxy forwards to the emulator at target, holding every request for
// delay — a controller whose first poll takes long enough for a reader to
// land in the middle of it, as a real one reading AOIs member by member
// does. It returns the port to dial.
func slowProxy(t *testing.T, target string, delay time.Duration) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			s, err := net.Dial("tcp", target)
			if err != nil {
				_ = c.Close()
				return
			}
			go func() { _, _ = io.Copy(c, s); _ = c.Close() }()
			go func() {
				buf := make([]byte, 64<<10)
				for {
					n, err := c.Read(buf)
					if err != nil {
						_ = s.Close()
						return
					}
					time.Sleep(delay)
					if _, err := s.Write(buf[:n]); err != nil {
						return
					}
				}
			}()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

// TestPrimedMeansAWholeRead — the snapshot fills tag by tag during a poll, so
// "connected" can be true while a reader sees only the first tags of the
// first poll; on a controller that refuses struct roots, the structs come
// last, read member by member. Primed is the signal a Sparkplug device
// births on: whenever it reads true, every input is already in the
// snapshot. Health is sampled before ReadInputs, and the snapshot only
// grows, so the check is race-free.
func TestPrimedMeansAWholeRead(t *testing.T) {
	for _, deny := range []bool{false, true} {
		host, port, _ := startEmulatorWith(t, deny)
		port = slowProxy(t, fmt.Sprintf("%s:%d", host, port), 25*time.Millisecond)
		d, err := New(host, testManifest(), WithPort(port), WithScanRate(30*time.Millisecond))
		if err != nil {
			t.Fatalf("new driver: %v", err)
		}
		if d.Health().Primed || d.Delivered() {
			t.Fatal("primed (or delivered) before Start")
		}
		ctx, cancel := context.WithCancel(context.Background())
		d.Start(ctx)

		partial := false // saw a connected driver with some inputs but not all
		deadline := time.Now().Add(5 * time.Second)
		for {
			connected := d.Health().Connected
			// Delivered first: it only ever turns on, so if it was true here
			// Primed (no reconnect in this test) is true when sampled next.
			delivered := d.Delivered() // what the runtime's start-up hold waits on
			primed := d.Health().Primed
			vals, _ := d.ReadInputs()
			if delivered && !primed {
				t.Fatalf("denyStructRoots=%v: Delivered before the first whole read", deny)
			}
			if primed {
				for _, name := range d.InputNames() {
					if _, ok := vals[name]; !ok {
						t.Fatalf("denyStructRoots=%v: primed, but %s is not in the snapshot yet", deny, name)
					}
				}
				break
			}
			if connected && len(vals) > 0 && len(vals) < len(d.InputNames()) {
				partial = true
			}
			if time.Now().After(deadline) {
				t.Fatalf("denyStructRoots=%v: never primed", deny)
			}
			time.Sleep(time.Millisecond)
		}
		if !d.Health().Connected {
			t.Errorf("denyStructRoots=%v: primed but not connected", deny)
		}
		if !partial {
			t.Errorf("denyStructRoots=%v: never saw a half-filled first poll; the proxy is not slow enough for this test to mean anything", deny)
		}
		cancel()
		d.Stop()
		if !d.Delivered() {
			t.Errorf("denyStructRoots=%v: Delivered cleared by Stop — it is a first-delivery latch", deny)
		}
	}
}
