package uaserver

import (
	"testing"
	"time"

	"github.com/gopcua/opcua/ua"
	"github.com/joyautomation/nautilus/opcua/internal/require"
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

// SetPublishing and Transfer, like Modify, change nothing on a subscription
// whose task has returned, and say so.
func TestChangesAfterTheTaskReturnedChangeNothing(t *testing.T) {
	s := NewSubscription()
	s.publishingEnabled = true
	owner := &session{}
	s.Session = owner
	close(s.done)
	got := make(chan bool, 2)
	go func() { got <- s.SetPublishing(false); _, ok := s.Transfer(&session{}, nil); got <- ok }()
	for range 2 {
		select {
		case ok := <-got:
			require.True(t, !ok, "a change reported to a subscription whose task had returned")
		case <-time.After(2 * time.Second):
			t.Fatal("a change waited on a task that had returned")
		}
	}
	require.True(t, s.PublishingEnabled(), "publishing was turned off")
	if o, _ := s.Owner(); o != owner {
		t.Fatal("the subscription changed owner")
	}
}

// Part 4 §5.14.7's rule for who may take a subscription: the same user,
// and for an anonymous one the same client application on a signed
// channel.
func TestMayTransfer(t *testing.T) {
	signed := func(uri string) *session { return &session{identity: "anonymous", secured: true, applicationURI: uri} }
	none := &session{identity: "anonymous"}
	for _, tc := range []struct {
		name     string
		from, to *session
		want     bool
	}{
		{"same application, signed", signed("urn:a"), signed("urn:a"), true},
		{"another application", signed("urn:a"), signed("urn:b"), false},
		{"to a session on None", signed("urn:a"), none, false},
		{"from a session on None", none, signed("urn:a"), false},
		{"None to None", none, &session{identity: "anonymous"}, false},
		{"no ApplicationUri", signed(""), signed(""), false},
		{"another user", &session{identity: "alice", secured: true, applicationURI: "urn:a"}, signed("urn:a"), false},
		{"the same named user", &session{identity: "alice"}, &session{identity: "alice"}, true},
	} {
		got := mayTransfer(tc.from, tc.to) == ua.StatusOK
		require.True(t, got == tc.want, "%s: may transfer = %v, want %v", tc.name, got, tc.want)
	}
}
