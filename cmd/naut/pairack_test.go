package main

import (
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/joyautomation/nautilus/quorum"
)

// The standby's in-sync acknowledgement and the voters' hot check, end to
// end (#710 S2c-1, pairack.go).

// A leader takes its standby's acknowledgement only of its own epoch, and
// never of an image it has not sent. ack_old_epoch_counts turns it red.
func TestAckOfAnotherEpochOrAnUnsentImageRefused(t *testing.T) {
	s := &pairStream{}
	now := time.Now()
	s.noteSent(7, 1, now.Add(-200*time.Millisecond), false)
	s.noteSent(7, 2, now.Add(-100*time.Millisecond), false)
	if ok, why := s.takeAck(7, streamAck{Epoch: 6, Seq: 2}, now); ok || !strings.Contains(why, "epoch 6") {
		t.Fatalf("an acknowledgement of another epoch: %v %q", ok, why)
	}
	if ok, _ := s.takeAck(7, streamAck{Epoch: 7, Seq: 3}, now); ok {
		t.Fatal("an acknowledgement of an image never sent taken")
	}
	if seq, _ := s.syncMark(7); seq != 0 {
		t.Fatalf("a refused acknowledgement moved the mark to %d", seq)
	}
	if ok, _ := s.takeAck(7, streamAck{Epoch: 7, Seq: 2}, now); !ok {
		t.Fatal("the acknowledgement of the last image refused")
	}
	if ok, _ := s.takeAck(7, streamAck{Epoch: 7, Seq: 1}, now); !ok {
		t.Fatal("a late acknowledgement of an earlier image refused")
	}
	if seq, cur := s.syncMark(7); seq != 2 || !cur {
		t.Fatalf("the mark %d %v; want 2, current (the maximum, never lowered)", seq, cur)
	}
	if seq, _ := s.syncMark(8); seq != 0 {
		t.Fatalf("a mark asked for another epoch: %d", seq)
	}
	// A new epoch starts the acknowledgements over.
	s.noteSent(8, 1, now, false)
	if seq, cur := s.syncMark(8); seq != 0 || cur {
		t.Fatalf("a new epoch's mark %d %v; want none", seq, cur)
	}
}

// Current is judged on sequence numbers, with a time cap from the
// measured send-to-send time: an image every 600 ms (a slow task) whose
// last acknowledgement came 700 ms ago is current, one image behind and
// within the cap; two behind, or one out longer than the cap, is not.
// current_time_based turns it red.
func TestCurrentIsJudgedOnSequences(t *testing.T) {
	now := time.Now()
	s := &pairStream{}
	s.noteSent(3, 1, now.Add(-1200*time.Millisecond), false)
	s.noteSent(3, 2, now.Add(-600*time.Millisecond), false)
	s.noteSent(3, 3, now, false)
	s.takeAck(3, streamAck{Epoch: 3, Seq: 2}, now.Add(-700*time.Millisecond))
	if seq, cur := s.syncMark(3); seq != 2 || !cur {
		t.Fatalf("one image behind, the next just sent: %d %v; want 2, current", seq, cur)
	}
	s = &pairStream{}
	s.noteSent(3, 1, now.Add(-1200*time.Millisecond), false)
	s.noteSent(3, 2, now.Add(-600*time.Millisecond), false)
	s.noteSent(3, 3, now, false)
	s.takeAck(3, streamAck{Epoch: 3, Seq: 1}, now)
	if _, cur := s.syncMark(3); cur {
		t.Fatal("two images behind counted current")
	}
	s = &pairStream{}
	s.noteSent(3, 1, now.Add(-5*time.Second), false)
	s.noteSent(3, 2, now.Add(-4400*time.Millisecond), false)
	s.takeAck(3, streamAck{Epoch: 3, Seq: 1}, now)
	if _, cur := s.syncMark(3); cur {
		t.Fatal("an image out 4.4 s unacknowledged (cap 3 × 600 ms) counted current")
	}
}

// slowPairProject is the hot pair's project with its chart task scanning
// every 500 ms: a capture waits for the slowest task, so images go out
// about that often.
func slowPairProject(a, b, w string) fstest.MapFS {
	fs := hotPairProject(a, b, w)
	y := string(fs["nautilus.yaml"].Data)
	y = strings.Replace(y, "    program: chart.sfc\n    scan: 50ms", "    program: chart.sfc\n    scan: 500ms", 1)
	fs["nautilus.yaml"] = &fstest.MapFile{Data: []byte(y)}
	return fs
}

// A plain kill with a 500 ms task beside a 50 ms one still goes hot: the
// standby is current by sequence though its acknowledgements come about
// every 500 ms. current_time_based (current only within two 100 ms
// stream intervals of the last acknowledgement) turns it red.
func TestPairHotTakeoverWithASlowTask(t *testing.T) {
	testPairFiles = slowPairProject
	t.Cleanup(func() { testPairFiles = nil })
	r := newPairRigFull(t, nil, nil)
	lead := r.leader()
	stand := other(lead)
	r.inSync(t, stand)
	r.set(t, lead, "Mode", 5)
	r.shadowed(t, lead)
	r.shadowed(t, lead) // a few renewals carry the acknowledgement
	time.Sleep(time.Second)
	r.nodes[lead].crash()
	if got := r.leader(); got != stand {
		t.Fatalf("after the kill: leader %q, want %s", got, stand)
	}
	pairWait(t, "the new leader's takeover report", 5*time.Second, func() bool {
		s := r.streamStatus(stand)
		return s != nil && s.Takeover != ""
	})
	if s := r.streamStatus(stand); s.Takeover != "hot" {
		t.Fatalf("takeover %q (%s) with a 500 ms task; want hot", s.Takeover, s.TakeoverCold)
	}
}

// Ruling B (naut-lead, on S2b): the standby's image is of the leader's
// first lease, the leader leads again in a later epoch without streaming,
// and dies. The voters' records show the later epoch: cold, named.
// hot_voters_unchecked turns it red.
func TestPairLaterLeadershipGoesCold(t *testing.T) {
	r := newHotPairRig(t, nil)
	lead := r.leader()
	stand := other(lead)
	r.inSync(t, stand)
	r.shadowed(t, lead)
	first := r.elector(lead).Term()
	// The standby holds an image of the first epoch from here on.
	r.nodes[lead].m.stream.hold.Store(true)
	pairWait(t, "the leader to stop sending", 5*time.Second, r.nodes[lead].m.stream.held.Load)
	standOK, leadOK := r.healthy(stand), r.healthy(lead)
	standOK.Store(false) // the standby does not campaign
	leadOK.Store(false)  // the leader's scans hang: it steps down
	// Its gate closes at once; its lease runs out unrenewed (500 ms).
	before := r.elector(lead).Status().Acquisitions
	pairWait(t, "the leader's gate to close", 10*time.Second, func() bool { return !r.elector(lead).Status().Leading })
	time.Sleep(time.Second)
	leadOK.Store(true)
	t.Cleanup(func() {
		if t.Failed() && r.nodes[lead].m != nil {
			t.Logf("lead %s: %+v", lead, r.elector(lead).Status())
			t.Logf("stand %s: %+v", stand, r.elector(stand).Status())
		}
	})
	pairWait(t, "the leader to lead again, in a later epoch", 20*time.Second, func() bool {
		e := r.elector(lead)
		return e.IsLeader() && e.Term() > first && e.Status().Acquisitions > before
	})
	standOK.Store(true)
	time.Sleep(time.Second) // renewals of the later epoch reach the voters
	r.nodes[lead].crash()
	pairWait(t, "the standby to take over", 10*time.Second, func() bool { return r.leader() == stand })
	pairWait(t, "the new leader's takeover report", 5*time.Second, func() bool {
		s := r.streamStatus(stand)
		return s != nil && s.Takeover != ""
	})
	s := r.streamStatus(stand)
	if s.Takeover != "cold" || !strings.Contains(s.TakeoverCold, coldWhy(quorum.ColdLaterLeader)) {
		t.Fatalf("takeover %q (%s); want cold: %s", s.Takeover, s.TakeoverCold, coldWhy(quorum.ColdLaterLeader))
	}
}

// A stream that stopped sending (closed, an image over the size limit, a
// capture that timed out) leaves the standby behind while the leader runs
// on: in step with the last image it is current only while that image is
// recent (Fable, #1345 M1). A reconnect's gap is no send-to-send time.
// current_without_fresh_send and interval_across_reconnect turn it red.
func TestAStoppedStreamIsNotCurrent(t *testing.T) {
	now := time.Now()
	s := &pairStream{}
	s.noteSent(7, 1, now.Add(-10*time.Minute), true)
	s.noteSent(7, 2, now.Add(-10*time.Minute+100*time.Millisecond), false)
	s.takeAck(7, streamAck{Epoch: 7, Seq: 2}, now.Add(-10*time.Minute))
	if seq, cur := s.syncMark(7); seq != 2 || cur {
		t.Fatalf("acknowledged the last image, sent 10 min ago: %d %v; want 2, not current", seq, cur)
	}
	// A reconnect after a minute's gap: the next image's cap is from the
	// send-to-send time on the new connection, not the gap.
	s = &pairStream{}
	s.noteSent(7, 1, now.Add(-62*time.Second), true)
	s.noteSent(7, 2, now.Add(-2*time.Second), true) // a new connection
	s.takeAck(7, streamAck{Epoch: 7, Seq: 1}, now)
	if _, cur := s.syncMark(7); cur {
		t.Fatal("an image out 2 s unacknowledged counted current: the cap took the reconnect's 60 s gap as the send-to-send time")
	}
}
