package quorum

import (
	"testing"
	"time"
)

// The clear's check phase takes the members' live boots together: a copy
// behind a split may reach one member and its original another, so each
// alone hears one (#1029 3c-3a). clear_per_member turns it red.
func TestCopyLiveTakesTheMembersTogether(t *testing.T) {
	cases := []struct {
		answers [][]uint64
		want    bool
	}{
		{[][]uint64{{1}, {1}, {}}, false},
		{[][]uint64{{1}, {}, {}}, false},
		{[][]uint64{{}, {}, {}}, false},
		{[][]uint64{{1}, {2}, {}}, true}, // the split: no member alone hears both
		{[][]uint64{{1, 2}, {1}, {1}}, true},
	}
	for _, c := range cases {
		if got := CopyLive(c.answers...); got != c.want {
			t.Errorf("CopyLive(%v) = %v, want %v", c.answers, got, c.want)
		}
	}
}

// A boot is live at a voter for a lease and D after its last request,
// whether or not the name is marked (boots stop being added at the mark;
// LiveBoots must still see a copy restarted after it).
func TestLiveBootsLastALeaseAndD(t *testing.T) {
	const lease = 500 * time.Millisecond
	v := NewVoter("w", lease, VoterState{}, 0)
	now := Instant(10 * time.Second)
	v.Handle(now, Request{From: "a", Nonce: 7, ID: ProbeID, Seq: 1})
	d := time.Duration(float64(lease/4) * (1 + MaxRho))
	if got := v.LiveBoots(now.Add(lease+d-time.Millisecond), "a"); len(got) != 1 {
		t.Fatalf("within a lease and D: %v", got)
	}
	if got := v.LiveBoots(now.Add(lease+d+time.Millisecond), "a"); len(got) != 0 {
		t.Fatalf("past a lease and D: %v", got)
	}
}
