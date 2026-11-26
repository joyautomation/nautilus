package tiadtest

import (
	"errors"
	"slices"
	"strings"
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
	t.Run("OfflineFailsKeepsIt", func(t *testing.T) { contractOnlineOfflineFailsKeepsIt(t, start) })
	t.Run("NoBlindTrust", func(t *testing.T) { contractOnlineNoBlindTrust(t, start) })
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
	var e *tiad.Error
	heldMsg := "the session is held online to " + benchCPU + " in TIA's window: take it offline first (POST /v1/sessions/" + s.ID + "/online {\"online\": false}), then compare"
	if !errors.As(err, &e) || e.Kind != tiad.KindBusy || e.Message != heldMsg {
		t.Fatalf("compare while held online: %v, want busy %q", err, heldMsg)
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

// contractOnlineOfflineFailsKeepsIt: an offline step that fails inside
// TIA's side leaves the project held, never dropped: the session stays
// held (busy), its close fails and keeps it, and the next close goes
// offline, then closes.
func contractOnlineOfflineFailsKeepsIt(t *testing.T, start Starter) {
	a, s := compiledBenchWith(t, start, Config{UI: true})
	if _, err := s.Online(ctx, tiad.OnlineRequest{Online: true, Secrets: BenchLogin}); err != nil {
		t.Fatal(err)
	}
	a.SetReleaseOnlineFails(t, "GoOffline failed")
	if _, err := s.Online(ctx, tiad.OnlineRequest{Online: false}); tiad.Kind(err) != tiad.KindOperationFailed {
		t.Fatalf("offline that fails: %v, want operation_failed", err)
	}
	if _, err := s.CompareWith(ctx, tiad.CompareRequest{Secrets: BenchLogin}); tiad.Kind(err) != tiad.KindBusy {
		t.Fatalf("compare after a failed offline: %v, want busy (still held)", err)
	}
	if err := s.Close(ctx); err == nil {
		t.Fatal("a close whose offline step fails succeeded")
	}
	if ss, err := a.Client().Sessions(ctx); err != nil || len(ss) != 1 {
		t.Fatalf("sessions after a failed close: %+v %v, want the session kept", ss, err)
	}
	if got := a.OnlineEvents(t); !slices.Equal(got, []string{"online " + benchCPU}) {
		t.Fatalf("online events: %v, want no offline yet", got)
	}
	a.SetReleaseOnlineFails(t, "")
	closeFirst(t, s)
	if got := a.OnlineEvents(t); !slices.Equal(got, []string{"online " + benchCPU, "offline " + benchCPU}) {
		t.Fatalf("online events after the retried close: %v", got)
	}
}

// contractOnlineNoBlindTrust: an instance that trusted a certificate
// blindly holds nothing online: refused before anything goes online.
func contractOnlineNoBlindTrust(t *testing.T, start Starter) {
	a, s := compiledBenchWith(t, start, Config{UI: true})
	a.SetFingerprint(t, FingerprintScript{AsksTLS: true})
	if r, err := s.Fingerprint(ctx, tiad.FingerprintRequest{Secrets: BenchLogin, TrustUnverifiedCertificate: true}); err != nil || r.BlindTrust == nil {
		t.Fatalf("a read with the opt-in: %+v %v", r, err)
	}
	_, err := s.Online(ctx, tiad.OnlineRequest{Online: true, Secrets: BenchLogin})
	if tiad.Kind(err) != tiad.KindBadRequest || !strings.Contains(err.Error(), "nothing went online") {
		t.Fatalf("online after a blind trust: %v, want bad_request naming nothing went online", err)
	}
	if got := a.OnlineEvents(t); len(got) != 0 {
		t.Fatalf("online events: %v, want none", got)
	}
}
