package opcua

import (
	"net"
	"strconv"
	"testing"
	"time"
)

// InputsReady (io.InputsReadier, #786): false until the first session's
// priming read has delivered or the first attempt failed; sticky.

func TestInputsReadyOnFirstDeliveryThenSticky(t *testing.T) {
	for _, mode := range []string{ReadModePoll, ReadModeSubscribe} {
		t.Run(mode, func(t *testing.T) {
			ts := startServer(t, freePort(t))
			d, err := New(ts.url, manifest(2), fastOpts(WithReadMode(mode))...)
			if err != nil {
				t.Fatal(err)
			}
			if d.InputsReady() {
				t.Fatal("ready before Start")
			}
			d.Start(t.Context())
			t.Cleanup(d.Stop)
			waitFor(t, "ready", d.InputsReady)
			if _, ok := read(t, d)["Speed"]; !ok {
				t.Fatal("ready before the first read was delivered")
			}
			if mode != ReadModePoll {
				return // the loss below takes seconds; poll mode covers it
			}
			_ = ts.srv.Close()
			waitFor(t, "offline", func() bool { return read(t, d)["plant__Online"] == false })
			if !d.InputsReady() {
				t.Fatal("InputsReady went false after the session was lost")
			}
		})
	}
}

// A server that accepts and never answers holds the first attempt open
// (not ready), and its timeout counts as a failed first attempt (ready).
func TestInputsReadyFalseWhileFirstAttemptHangsTrueAfterItFails(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { c.Close() })
		}
	}()
	d, err := New("opc.tcp://"+ln.Addr().String(), manifest(2), fastOpts(WithTimeout(600*time.Millisecond))...)
	if err != nil {
		t.Fatal(err)
	}
	d.Start(t.Context())
	t.Cleanup(d.Stop)
	time.Sleep(100 * time.Millisecond)
	if d.InputsReady() {
		t.Fatal("ready while the first attempt is still hanging")
	}
	waitFor(t, "ready after the first attempt failed", d.InputsReady)
}

func TestInputsReadyOnConnectionRefused(t *testing.T) {
	d, err := New("opc.tcp://127.0.0.1:"+strconv.Itoa(freePort(t)), manifest(2), fastOpts()...)
	if err != nil {
		t.Fatal(err)
	}
	d.Start(t.Context())
	t.Cleanup(d.Stop)
	waitFor(t, "ready after the refused connect", d.InputsReady)
}

func TestInputsReadyWithNoBindings(t *testing.T) {
	d, err := New("opc.tcp://127.0.0.1:4840", Manifest{}, fastOpts()...)
	if err != nil {
		t.Skipf("an empty manifest is refused: %v", err)
	}
	if !d.InputsReady() {
		t.Fatal("a driver with no bindings must be ready at once")
	}
}
