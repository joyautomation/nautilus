package tiadtest

import (
	"errors"
	"slices"
	"testing"

	"github.com/joyautomation/nautilus/siemens/tiad"
)

// contractOnline: an agent started with TIAD_UI=1 holds a session's project
// online to its CPU, in TIA's window, until it is taken offline or the
// session closes; nothing else on the session goes online meanwhile. Any
// other agent refuses before anything goes online.
func contractOnline(t *testing.T, start Starter) {
	t.Run("RefusedWithoutUI", func(t *testing.T) { contractOnlineRefusedWithoutUI(t, start) })
	t.Run("Held", func(t *testing.T) { contractOnlineHeld(t, start) })
	t.Run("CloseGoesOffline", func(t *testing.T) { contractOnlineCloseGoesOffline(t, start) })
	t.Run("OnlyATarget", func(t *testing.T) { contractOnlineOnlyATarget(t, start) })
}

func contractOnlineRefusedWithoutUI(t *testing.T, start Starter) {
	a, s := compiledBench(t, start)
	_, err := s.Online(ctx, tiad.OnlineRequest{Online: true})
	var e *tiad.Error
	if !errors.As(err, &e) || e.Kind != tiad.KindBadRequest || e.Message != OnlineNeedsUI {
		t.Fatalf("online without UI: %v, want bad_request %q", err, OnlineNeedsUI)
	}
	if got := a.OnlineEvents(t); len(got) != 0 {
		t.Fatalf("online events without UI: %v, want none", got)
	}
}

func contractOnlineHeld(t *testing.T, start Starter) {
	a, s := compiledBenchWith(t, start, Config{UI: true})
	want := tiad.OnlineResult{Online: true, Address: benchCPU, State: "Online"}
	for i := 0; i < 2; i++ {
		// A second call on a session held online changes nothing.
		r, err := s.Online(ctx, tiad.OnlineRequest{Online: true, Secrets: BenchLogin})
		if err != nil || r != want {
			t.Fatalf("online (call %d): %+v %v, want %+v", i+1, r, err, want)
		}
	}
	if got := a.OnlineEvents(t); !slices.Equal(got, []string{"online " + benchCPU}) {
		t.Fatalf("online events: %v", got)
	}
	// One connection: nothing else on the session goes online.
	_, err := s.CompareWith(ctx, tiad.CompareRequest{Secrets: BenchLogin})
	if tiad.Kind(err) != tiad.KindBusy {
		t.Fatalf("compare while held online: %v, want busy", err)
	}
	if _, err := s.Fingerprint(ctx, tiad.FingerprintRequest{Secrets: BenchLogin}); tiad.Kind(err) != tiad.KindBusy {
		t.Fatalf("fingerprint while held online: %v, want busy", err)
	}
	if err := s.Upload(ctx); tiad.Kind(err) != tiad.KindBusy {
		t.Fatalf("upload while held online: %v, want busy", err)
	}
	r, err := s.Online(ctx, tiad.OnlineRequest{Online: false})
	if err != nil || r != (tiad.OnlineResult{Online: false, Address: benchCPU}) {
		t.Fatalf("offline: %+v %v", r, err)
	}
	if r, err := s.Online(ctx, tiad.OnlineRequest{Online: false}); err != nil || r != (tiad.OnlineResult{}) {
		t.Fatalf("offline again: %+v %v, want nothing held", r, err)
	}
	if got := a.OnlineEvents(t); !slices.Equal(got, []string{"online " + benchCPU, "offline " + benchCPU}) {
		t.Fatalf("online events: %v", got)
	}
	// Offline, the session goes online for a compare again.
	if _, err := s.CompareWith(ctx, tiad.CompareRequest{Secrets: BenchLogin}); tiad.Kind(err) == tiad.KindBusy {
		t.Fatalf("compare after offline: %v", err)
	}
}

func contractOnlineCloseGoesOffline(t *testing.T, start Starter) {
	a, s := compiledBenchWith(t, start, Config{UI: true})
	if _, err := s.Online(ctx, tiad.OnlineRequest{Online: true, Secrets: BenchLogin}); err != nil {
		t.Fatal(err)
	}
	closeFirst(t, s)
	if got := a.OnlineEvents(t); !slices.Equal(got, []string{"online " + benchCPU, "offline " + benchCPU}) {
		t.Fatalf("online events after a close: %v, want offline before the close", got)
	}
}

func contractOnlineOnlyATarget(t *testing.T, start Starter) {
	a, s := compiledBenchWith(t, start, Config{UI: true, Targets: []string{"192.0.2.99"}})
	if _, err := s.Online(ctx, tiad.OnlineRequest{Online: true}); tiad.Kind(err) != tiad.KindTargetRefused {
		t.Fatalf("online to a CPU not in TIAD_TARGETS: %v, want target_refused", err)
	}
	if got := a.OnlineEvents(t); len(got) != 0 {
		t.Fatalf("online events: %v, want none", got)
	}
}
