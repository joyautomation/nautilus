package leader

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/quorum"
	"github.com/joyautomation/nautilus/quorum/node"
)

func TestQuorumStatusMapping(t *testing.T) {
	got := quorumStatus(node.Status{Self: "a", Peer: "b", Leader: "b", Leading: false, Acquisitions: 3})
	want := Status{Mode: "quorum", Pod: "a", Leader: "b", IsLeader: false, Transitions: 3}
	if got != want {
		t.Fatalf("status %+v, want %+v", got, want)
	}
}

// The health func is asked with half the lease as its bound, as the Lease
// elector asks it, and a failing verdict reads as not live.
func TestQuorumHealth(t *testing.T) {
	if !quorum.BootClock {
		t.Skip("controllers need CLOCK_BOOTTIME")
	}
	q, err := NewQuorum(node.Options{Pair: "plant", Self: "a", Peer: "b", PeerAddr: "127.0.0.1:1", WitnessAddr: "127.0.0.1:1",
		Lease: 800 * time.Millisecond, StatePath: filepath.Join(t.TempDir(), "q.state"), Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	defer q.Node().Close()
	asked := make(chan time.Duration, 8)
	q.SetHealth(func(_ time.Time, max time.Duration) error {
		select {
		case asked <- max:
		default:
		}
		return errors.New("stuck")
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { q.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	select {
	case d := <-asked:
		if d != 400*time.Millisecond {
			t.Fatalf("asked with bound %v, want half the lease", d)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("health never asked")
	}
	if q.IsLeader() || q.Status().Mode != "quorum" {
		t.Fatalf("status %+v", q.Status())
	}
}
