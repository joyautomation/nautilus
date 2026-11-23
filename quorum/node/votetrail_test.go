package node

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/quorum"
)

// steady()'s report (#1120): one summary line a runner keeps, saying per
// voter what was sent, answered, refused, failed and pending, and the
// longest gap between sends, so a lapse tells a stalled process from slow
// answers, and each answer's verdict, so it names the voter that refused
// a lapsing leader and why (#1329).
func TestVoteTrailSumsUpTheLapse(t *testing.T) {
	now := time.Now()
	c := &cluster{votes: []sentVote{
		{from: "a", to: "b", at: now.Add(-5 * time.Second), done: true}, // outside the span
		{from: "a", to: "b", at: now.Add(-900 * time.Millisecond), req: quorum.Request{Term: 7, Renewal: true}, done: true, took: 20 * time.Millisecond, verdict: "granted"},
		{from: "a", to: WitnessID, at: now.Add(-900 * time.Millisecond), done: true, took: 62 * time.Millisecond, err: errors.New("context deadline exceeded")},
		{from: "b", to: "a", at: now.Add(-800 * time.Millisecond), done: true}, // another sender
		{from: "a", to: "b", at: now.Add(-300 * time.Millisecond), req: quorum.Request{Term: 8, Renewal: true}, done: true, took: 45 * time.Millisecond, verdict: "refused, promised 8 (its promise: to b, 400ms left)"},
		{from: "a", to: WitnessID, at: now.Add(-300 * time.Millisecond)},
	}}
	got := c.voteTrail("a", time.Second)
	head, _, _ := strings.Cut(got, "\n")
	for _, want := range []string{
		"votes a sent in the last 1s:",
		"longest gap between sends 600ms, none since 300ms ago;",
		"to b: 2 sent, 2 answered (slowest 45ms), 1 refused, 0 failed, 0 pending;",
		"to w: 2 sent, 0 answered (slowest 0s), 0 refused, 1 failed, 1 pending (last: context deadline exceeded);",
	} {
		if !strings.Contains(head, want) {
			t.Errorf("summary %q\nlacks %q", head, want)
		}
	}
	if n := strings.Count(got, "\n"); n != 4 {
		t.Errorf("%d detail lines, want 4 (a's sends in the span):\n%s", n, got)
	}
	for _, want := range []string{
		"to b term 7 renewal=true answered in 20ms: granted",
		"to b term 8 renewal=true answered in 45ms: refused, promised 8 (its promise: to b, 400ms left)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("detail lacks %q:\n%s", want, got)
		}
	}
	if got := (&cluster{}).voteTrail("a", time.Second); !strings.HasSuffix(got, "none") {
		t.Errorf("no votes: %q", got)
	}
}

// voteVerdict says what a vote's answer said, and leaves the body for the
// elector to read.
func TestVoteVerdictSaysWhatTheAnswerSaid(t *testing.T) {
	reply := func(r quorum.Reply) *http.Response {
		b, err := r.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(b))}
	}
	refused := &http.Response{StatusCode: http.StatusConflict, Header: http.Header{RefusedHeader: {"lease"}},
		Body: io.NopCloser(strings.NewReader("a's lease 2s is longer than this voter's 1s\n"))}
	for _, c := range []struct {
		resp *http.Response
		want string
	}{
		{reply(quorum.Reply{From: "b", Granted: true, Promised: 9}), "granted"},
		{reply(quorum.Reply{From: "b", Promised: 9, Promise: quorum.Relay{To: "b", Left: 300 * time.Millisecond}}), "refused, promised 9 (its promise: to b, 300ms left)"},
		{reply(quorum.Reply{From: "w", Promised: 9, Marked: true}), "refused, promised 9, marked"},
		{refused, "409 lease: a's lease 2s is longer than this voter's 1s"},
	} {
		var before []byte
		if c.resp.StatusCode == http.StatusOK {
			before, _ = io.ReadAll(c.resp.Body)
			c.resp.Body = io.NopCloser(bytes.NewReader(before))
		}
		if got := voteVerdict(c.resp); got != c.want {
			t.Errorf("verdict %q, want %q", got, c.want)
		}
		after, _ := io.ReadAll(c.resp.Body)
		if before != nil && !bytes.Equal(after, before) {
			t.Errorf("the body left for the elector is not the answer: %d bytes of %d", len(after), len(before))
		}
	}
}

// On the real transport: a leader's renewals are granted, and a standby's
// probes refused, in the trail.
func TestVoteTrailCarriesTheVerdicts(t *testing.T) {
	c := newCluster(t)
	c.startAll()
	c.commission()
	who := c.settledLeader(leases(20))
	c.steady(who, 2)
	if got := c.voteTrail(who, leases(2)); !strings.Contains(got, "renewal=true answered in") || !strings.Contains(got, ": granted") {
		t.Errorf("the leader's trail shows no granted renewal:\n%s", got)
	}
	if got := c.voteTrail(c.other(who), leases(2)); !strings.Contains(got, "term 0 renewal=false answered in") || !strings.Contains(got, ": refused, promised") {
		t.Errorf("the standby's trail shows no refused probe:\n%s", got)
	}
}
