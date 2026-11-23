package node

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/quorum"
)

// steady()'s report (#1120): one summary line a runner keeps, saying per
// voter what was sent, answered, failed and pending, and the longest gap
// between sends, so a lapse tells a stalled process from slow answers.
func TestVoteTrailSumsUpTheLapse(t *testing.T) {
	now := time.Now()
	c := &cluster{votes: []sentVote{
		{from: "a", to: "b", at: now.Add(-5 * time.Second), done: true}, // outside the span
		{from: "a", to: "b", at: now.Add(-900 * time.Millisecond), req: quorum.Request{Term: 7, Renewal: true}, done: true, took: 20 * time.Millisecond},
		{from: "a", to: WitnessID, at: now.Add(-900 * time.Millisecond), done: true, took: 62 * time.Millisecond, err: errors.New("context deadline exceeded")},
		{from: "b", to: "a", at: now.Add(-800 * time.Millisecond), done: true}, // another sender
		{from: "a", to: "b", at: now.Add(-300 * time.Millisecond), done: true, took: 45 * time.Millisecond},
		{from: "a", to: WitnessID, at: now.Add(-300 * time.Millisecond)},
	}}
	got := c.voteTrail("a", time.Second)
	head, _, _ := strings.Cut(got, "\n")
	for _, want := range []string{
		"votes a sent in the last 1s:",
		"longest gap between sends 600ms, none since 300ms ago;",
		"to b: 2 sent, 2 answered (slowest 45ms), 0 failed, 0 pending;",
		"to w: 2 sent, 0 answered (slowest 0s), 1 failed, 1 pending (last: context deadline exceeded);",
	} {
		if !strings.Contains(head, want) {
			t.Errorf("summary %q\nlacks %q", head, want)
		}
	}
	if n := strings.Count(got, "\n"); n != 4 {
		t.Errorf("%d detail lines, want 4 (a's sends in the span):\n%s", n, got)
	}
	if !strings.Contains(got, "to b term 7 renewal=true answered in 20ms") {
		t.Errorf("detail lacks the renewal's outcome:\n%s", got)
	}
	if got := (&cluster{}).voteTrail("a", time.Second); !strings.HasSuffix(got, "none") {
		t.Errorf("no votes: %q", got)
	}
}
