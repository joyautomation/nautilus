package server

import (
	"context"
	"net"
	"strconv"
	"testing"
)

// A port 0 endpoint probes a free port and releases it before the bind
// (nautilus#542). If something takes the port in that window, Start
// probes again instead of failing with "address already in use".
func TestPortZeroSurvivesAProbedPortBeingTaken(t *testing.T) {
	rt, _ := newRuntime(t)
	var thieves []net.Listener
	probeGap = func(port int) {
		if len(thieves) > 0 {
			return
		}
		l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err != nil {
			t.Errorf("take probed port %d: %v", port, err)
			return
		}
		thieves = append(thieves, l)
	}
	t.Cleanup(func() {
		probeGap = nil
		for _, l := range thieves {
			l.Close()
		}
	})
	s, err := New(rt, Options{Endpoint: "127.0.0.1:0", Name: "port-zero"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(s.Stop)
	if len(thieves) != 1 {
		t.Fatalf("the probed port was not taken (%d thieves)", len(thieves))
	}
}
