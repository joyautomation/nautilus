package cip

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

// Stop is safe from any goroutine at any time: before Run (Run then
// returns as soon as it has listened), and while Run is starting (the
// race detector once caught Run writing cancel as Stop read it).
func TestStopBeforeAndDuringRun(t *testing.T) {
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	run := func(s *Server) chan error {
		done := make(chan error, 1)
		go func() { done <- s.Run(context.Background()) }()
		return done
	}
	wait := func(what string, done chan error) {
		t.Helper()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("%s: Run = %v", what, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: Run did not return after Stop", what)
		}
	}

	s := NewServer("127.0.0.1:0", quiet)
	s.Stop()
	wait("Stop before Run", run(s))

	for i := 0; i < 20; i++ {
		s := NewServer("127.0.0.1:0", quiet)
		done := run(s)
		s.Stop()
		wait("Stop while Run starts", done)
	}
}
