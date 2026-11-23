package leader

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/quorum"
	"github.com/joyautomation/nautilus/quorum/node"
)

func TestQuorumStatusMapping(t *testing.T) {
	got := quorumStatus(node.Status{Self: "a", Peer: "b", Leader: "b", LeaderAddr: "ctl-b:8080", Leading: false, Acquisitions: 3})
	want := Status{Mode: "quorum", Pod: "a", Leader: "b", LeaderAddr: "ctl-b:8080", IsLeader: false, Transitions: 3}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("status %+v, want %+v", got, want)
	}
}

// The health func is asked with half the lease as its bound, as the Lease
// elector asks it, and a failing verdict reads as not live.
func TestQuorumHealth(t *testing.T) {
	if !quorum.BootClock {
		t.Skip("controllers need CLOCK_BOOTTIME")
	}
	q, err := NewQuorum(node.Options{Cluster: node.NewClusterID(), Self: "a", Peer: "b", PeerAddr: "127.0.0.1:1", WitnessAddr: "127.0.0.1:1",
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

// #710: a pair member's Status carries the writes cut at the send deadline,
// by device, as /api/cluster serves it; with none cut the field is absent.
func TestQuorumStatusCarriesWritesCut(t *testing.T) {
	if !quorum.BootClock {
		t.Skip("controllers need CLOCK_BOOTTIME")
	}
	q, err := NewQuorum(node.Options{Cluster: node.NewClusterID(), Self: "a", Peer: "b", PeerAddr: "127.0.0.1:1", WitnessAddr: "127.0.0.1:1",
		Lease: 800 * time.Millisecond, StatePath: filepath.Join(t.TempDir(), "q.state"), Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	defer q.Node().Close()
	js := func() string {
		b, err := json.Marshal(q.Status())
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if strings.Contains(js(), "writesCut") {
		t.Fatalf("no source set, no field: %s", js())
	}
	cuts := map[string]int{}
	q.SetWritesCut(func() map[string]int { return cuts })
	if strings.Contains(js(), "writesCut") {
		t.Fatalf("nothing cut, no field: %s", js())
	}
	cuts["plc1"], cuts["bmc2"] = 14, 3
	if got := js(); !strings.Contains(got, `"writesCut":{"bmc2":3,"plc1":14}`) {
		t.Fatalf("status %s lacks the writes cut", got)
	}
}
