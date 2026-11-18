package uaserver

import (
	"testing"
	"time"
)

// Fence is a barrier: while a publish response is being composed and sent
// (publishMu held, as the publish loop holds it), Fence must not return;
// once the send is done, it must. SetMonitoringMode and
// DeleteMonitoredItems rely on it so that nothing checked against an
// item's old mode reaches the client after they answer (black-box F-27).
// The end-to-end test (opcua/server TestDisablingDropsWhatTheItemHadQueued)
// rarely meets the race window, so the barrier is pinned here.
func TestFenceWaitsForAPublish(t *testing.T) {
	s := NewSubscription()
	s.publishMu.Lock() // a publish in flight
	fenced := make(chan struct{})
	go func() {
		s.Fence()
		close(fenced)
	}()
	select {
	case <-fenced:
		t.Fatal("Fence returned while a publish held publishMu")
	case <-time.After(100 * time.Millisecond):
	}
	s.publishMu.Unlock() // the publish is sent
	select {
	case <-fenced:
	case <-time.After(5 * time.Second):
		t.Fatal("Fence did not return once the publish was sent")
	}
}
