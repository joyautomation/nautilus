package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/quorum"
	"github.com/joyautomation/nautilus/quorum/node"
)

// The standby's in-sync acknowledgement (#710 S2c-1). A standby that
// applied an image whole (its shadow took every task and the tags: a
// takeover could go on hot from it) tells the leader so: POST
// <stream path>/ack {epoch, seq}, behind the peer token like the stream.
// The leader keeps the highest it was told in its own epoch, never one of
// another epoch or above the last image it sent, and turns it into the
// in-sync mark its renewals carry to the voters (Elector.SetSyncMark). At
// the next takeover the voters' marks say whether the standby was current
// (quorum.HotCheck.Verdict).
//
// Current is judged on sequence numbers: the acknowledgement is of this
// epoch and at most one image behind the last sent, and the oldest image
// not yet acknowledged went out no longer ago than streamAckCap times the
// measured send-to-send time. A time-only rule (an acknowledgement within
// two stream intervals) went out of sync whenever a slow task stretched
// the capture (it waits for the slowest task) or the budget stretched the
// interval.

// streamAckCap is how many measured send-to-send times the oldest
// unacknowledged image may have been out before the standby counts as
// behind.
const streamAckCap = 3

// ackPath is the acknowledgement's path, beside the stream's.
func ackPath(cluster string) string { return streamPath(cluster) + "/ack" }

// streamAck is the acknowledgement's body.
type streamAck struct {
	Epoch uint64 `json:"epoch"`
	Seq   uint64 `json:"seq"`
}

// sentImage is one image the leader sent, for the acknowledgement's
// judgement.
type sentImage struct {
	seq uint64
	at  time.Time
}

// ackState is the leader's side: what it sent and what its standby
// acknowledged, in one epoch. Its own lock (markMu), never held while
// calling out: the election asks syncMark from its look.
type ackState struct {
	epoch    uint64      // the epoch the rest is of
	sends    []sentImage // the last few images sent, oldest first
	interval time.Duration
	acked    uint64
	ackedAt  time.Time
}

const ackSends = 16

// reset starts the state over for epoch, if it is of another.
func (a *ackState) reset(epoch uint64) {
	if a.epoch != epoch {
		*a = ackState{epoch: epoch}
	}
}

// noteSent records an image of epoch sent at now. first: the first image
// on its connection, whose gap from the last send is a reconnect, not the
// send-to-send time (Fable, #1345 M1).
func (s *pairStream) noteSent(epoch, seq uint64, now time.Time, first bool) {
	s.markMu.Lock()
	defer s.markMu.Unlock()
	a := &s.acks
	a.reset(epoch)
	if n := len(a.sends); n > 0 && (!first || mutNaut("interval_across_reconnect")) {
		a.interval = now.Sub(a.sends[n-1].at)
	}
	if first && !mutNaut("ack_kept_across_reconnect") {
		// A new connection may be a standby that restarted, maybe on
		// another build or with an empty shadow: an acknowledgement made
		// before it is no word on what it holds now (#710 (b): PEER_READY()
		// read a restarted standby of another build as current).
		a.acked, a.ackedAt = 0, time.Time{}
	}
	a.sends = append(a.sends, sentImage{seq: seq, at: now})
	if len(a.sends) > ackSends {
		a.sends = a.sends[len(a.sends)-ackSends:]
	}
}

// takeAck takes a standby's acknowledgement while this controller leads
// in term: false, with why, for one of another epoch or above the last
// image sent.
func (s *pairStream) takeAck(term uint64, ack streamAck, now time.Time) (bool, string) {
	s.markMu.Lock()
	defer s.markMu.Unlock()
	a := &s.acks
	a.reset(term)
	if ack.Epoch != term && !mutNaut("ack_old_epoch_counts") {
		return false, fmt.Sprintf("an acknowledgement of epoch %d; this leader leads in %d", ack.Epoch, term)
	}
	if n := len(a.sends); n == 0 || ack.Seq > a.sends[n-1].seq {
		return false, fmt.Sprintf("an acknowledgement of image %d, above the last this leader sent", ack.Seq)
	}
	if ack.Seq > a.acked {
		a.acked, a.ackedAt = ack.Seq, now
	}
	return true, ""
}

// syncMark is the standby's in-sync mark in epoch, for the election
// (Elector.SetSyncMark): the last image it acknowledged, and whether that
// is current (above).
func (s *pairStream) syncMark(epoch uint64) (uint64, bool) {
	s.markMu.Lock()
	defer s.markMu.Unlock()
	a := &s.acks
	n := len(a.sends)
	if a.epoch != epoch || a.acked == 0 || n == 0 {
		return 0, false
	}
	now := time.Now()
	if mutNaut("current_time_based") {
		return a.acked, now.Sub(a.ackedAt) <= 2*streamMinInterval
	}
	last := a.sends[n-1].seq
	if a.acked+1 < last {
		return a.acked, false // more than one image behind
	}
	limit := streamAckCap * max(a.interval, streamMinInterval)
	if a.acked == last {
		// In step with the last image sent, if that was lately: a stream
		// that stopped (closed, an image over the size limit, a capture
		// that timed out) leaves a standby that falls behind while the
		// leader runs on (Fable, #1345 M1).
		return a.acked, now.Sub(a.sends[n-1].at) <= limit || mutNaut("current_without_fresh_send")
	}
	// One image out unacknowledged: within the cap of the measured
	// send-to-send time.
	for _, x := range a.sends {
		if x.seq == a.acked+1 {
			return a.acked, now.Sub(x.at) <= limit
		}
	}
	return a.acked, false
}

// serveAck is the leader's side of the acknowledgement.
func (s *pairStream) serveAck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var ack streamAck
	if err := json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&ack); err != nil {
		http.Error(w, "bad acknowledgement", http.StatusBadRequest)
		return
	}
	term, lead := s.rt.LeadingTerm()
	if !lead || !s.leads(term) {
		http.Error(w, "not leading", http.StatusServiceUnavailable)
		return
	}
	if ok, why := s.takeAck(term, ack, time.Now()); !ok {
		http.Error(w, why, http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ack tells the leader this standby applied the image (epoch, seq) whole.
// One acknowledgement is in flight at a time; a newer one waiting replaces
// an older one, so a slow leader never queues them up.
func (s *pairStream) ack(epoch, seq uint64) {
	s.ackMu.Lock()
	s.ackWant = streamAck{Epoch: epoch, Seq: seq}
	busy := s.ackBusy
	s.ackBusy = true
	s.ackMu.Unlock()
	if busy {
		return
	}
	go func() {
		for {
			s.ackMu.Lock()
			want := s.ackWant
			if want == s.ackSent {
				s.ackBusy = false
				s.ackMu.Unlock()
				return
			}
			s.ackMu.Unlock()
			s.postAck(want)
			s.ackMu.Lock()
			s.ackSent = want
			s.ackMu.Unlock()
		}
	}()
}

// postAck sends one acknowledgement, within half a lease. A failure is
// only logged at debug: the next image's acknowledgement carries the news.
func (s *pairStream) postAck(ack streamAck) {
	ctx, cancel := context.WithTimeout(s.ctx, s.m.o.Lease/2)
	defer cancel()
	b, _ := json.Marshal(ack)
	url := strings.TrimSuffix(s.url, streamPath(s.m.o.Cluster)) + ackPath(s.m.o.Cluster)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return
	}
	req.Header.Set(node.TokenHeader, s.m.o.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 512))
	resp.Body.Close()
}

// peerReady is PEER_READY() on this leader (#710 (b)): a takeover by its
// standby now would go hot. The standby is current by the acknowledgement
// rule (syncMark: it acknowledges only images it applied whole, of its own
// build), this leader holds no forces (a takeover drops them), and
// hot-max-age is not 0. FALSE on a standby.
func (s *pairStream) peerReady() bool {
	epoch, lead := s.rt.LeadingTerm()
	if !lead || !s.leads(epoch) {
		return false
	}
	if s.hotMaxAge == 0 {
		return false
	}
	if s.rt.Tags().ForceCount() > 0 && !mutNaut("peer_ready_ignores_forces") {
		return false
	}
	_, current := s.syncMark(epoch)
	return current
}

// coldWhy says a hot check's cold reason (quorum.Cold*) in words, for the
// takeover-cold condition and the journal.
func coldWhy(why string) string {
	switch why {
	case quorum.ColdRecordsUnknown:
		return "the voters can't vouch that no leadership came after the image (a voter lost or rebuilt its state, or runs an older release)"
	case quorum.ColdLaterLeader:
		return "the voters granted the old leader a later leadership than the image's: the image predates it"
	case quorum.ColdImageUnseen:
		return "no voter records the leadership the image is of"
	case quorum.ColdOwnLeadership:
		return "this controller led after the image was made"
	case quorum.ColdNoMark:
		return "no voter knows how far this standby was in sync (the old leader's last renewals carried no acknowledgement)"
	case quorum.ColdMarkOtherEpoch:
		return "the voters' last word on this standby is of another leadership than the image's"
	case quorum.ColdNotCurrent:
		return "the old leader's last word was that this standby was behind"
	case quorum.ColdBehindMark:
		return "the image is older than one this standby had acknowledged: a stale standby"
	case quorum.ColdNotLeading:
		return "the lease ended before the check"
	}
	return "the voters' check: " + why
}
