package uaserver

import (
	"testing"
	"time"

	"github.com/joyautomation/nautilus/opcua/internal/uaserver/internal/require"
)

// A Modify that arrives after the subscription's task has returned changes
// nothing and says so, rather than waiting on a task that will never take
// it. (That Modify changes a live subscription's rate is tested through the
// service, in opcua/server: TestModifySubscriptionChangesThePublishingRate.)
func TestModifyAfterTheTaskReturnedChangesNothing(t *testing.T) {
	s := NewSubscription()
	s.RevisedPublishingInterval, s.RevisedLifetimeCount, s.RevisedMaxKeepAliveCount = 100, 30, 10
	close(s.done)
	got := make(chan bool, 1)
	go func() { got <- s.Modify(500, 60, 20) }()
	select {
	case ok := <-got:
		require.True(t, !ok, "Modify reported a change to a subscription whose task had returned")
	case <-time.After(2 * time.Second):
		t.Fatal("Modify waited on a task that had returned")
	}
	iv, lt, ka := s.Revised()
	require.Equal(t, 100.0, iv)
	require.Equal(t, uint32(30), lt)
	require.Equal(t, uint32(10), ka)
}
