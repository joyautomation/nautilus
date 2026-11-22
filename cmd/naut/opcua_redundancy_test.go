package main

import (
	"testing"
	"time"

	"github.com/joyautomation/nautilus/leader"
)

// The ServiceLevel a pair member reports: a leader is always 200 or more and
// a standby always less, a standby that cannot take over cleanly is 1, and a
// spare or held copy is 0 (#716).
func TestPairServiceLevel(t *testing.T) {
	for _, c := range []struct {
		name string
		st   leader.Status
		want byte
	}{
		{"leader", leader.Status{IsLeader: true, Leader: "a"}, 250},
		{"leader with a warning", leader.Status{IsLeader: true, Leader: "a", Warnings: []string{"x"}}, 200},
		{"standby in sync", leader.Status{Leader: "a", Log: &leader.LogStatus{Synced: true}}, 100},
		{"standby with no log", leader.Status{Leader: "a"}, 100},
		{"standby, log not synced", leader.Status{Leader: "a", Log: &leader.LogStatus{}}, 1},
		{"standby, no leader known", leader.Status{}, 1},
		{"spare", leader.Status{Spare: true, IsLeader: true}, 0},
		{"held by a duplicate mark", leader.Status{Held: true, Leader: "a"}, 0},
	} {
		if got := pairServiceLevel(c.st); got != c.want {
			t.Errorf("%s: %d, want %d", c.name, got, c.want)
		}
	}
}

// The level is held for the ttl, then read again: a takeover shows within it.
func TestCachedServiceLevel(t *testing.T) {
	calls, level := 0, byte(250)
	now := time.Unix(1000, 0)
	f := cachedServiceLevel(func() byte { calls++; return level }, 250*time.Millisecond, func() time.Time { return now })
	for i := 0; i < 5; i++ {
		if f() != 250 {
			t.Fatal("first level")
		}
	}
	if calls != 1 {
		t.Errorf("%d reads of the elector within the ttl, want 1", calls)
	}
	level = 100
	now = now.Add(250 * time.Millisecond)
	if got := f(); got != 100 || calls != 2 {
		t.Errorf("after the ttl: level %d after %d reads, want 100 after 2", got, calls)
	}
}
