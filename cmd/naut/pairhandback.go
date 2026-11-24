package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/joyautomation/nautilus/audit"
	"github.com/joyautomation/nautilus/quorum"
	"github.com/joyautomation/nautilus/quorum/node"
)

// The bad-retain hand-back (#710 S2c-3). A controller that wins a lease
// and would take over cold with its own retain store unreadable would run
// the plant on initial values. Before it does (runtime.SetHandBack, the
// edge's last gate: nothing loaded, saved, scanned or sent yet), it asks
// its peer, once, with a short deadline, whether the peer's retain file
// is good:
//
//   - readable, and holding a stream position (a file kept aside as
//     corrupt reads back as nothing: no position);
//   - at or past the newest earlier leadership the voters of this
//     controller's winning round know of (quorum.HotCheck.HandBackFloor):
//     their epoch records, never what this controller remembers, which is
//     nothing after a restart with a corrupt file;
//   - the peer could lead now and keep it (node.Elector.MayLead), and
//     has not handed back itself within the guard's window.
//
// If so it hands the lead back (node.Elector.HandBack): its gate closes
// and it neither renews nor campaigns for handBackHold leases, so the
// peer takes over once the lease runs out, about 1.3 leases after the
// election. Otherwise, or with no answer within the deadline (a dead peer
// refuses the connection at once), it takes over cold on initial values,
// alarmed as before, with the reason. A controller hands back at most
// once per quorum.HandBackEvery, recorded in its voter state so a crash
// loop counts too: A to B to A, then A runs.

// handBackHold is how many leases a controller that handed back leaves
// the lead to its peer before it campaigns again: the fallback when the
// peer never takes it costs about this plus half a lease.
const handBackHold = 3

// handBackWriteWait bounds the hand-back's durable write: with the
// question's deadline (250 ms at most) it stays well inside the runtime's
// wait for the answer (runtime.handBackWait, 2 s).
const handBackWriteWait = time.Second

// handBackPath is the peer's hand-back question, beside the stream's.
func handBackPath(cluster string) string { return streamPath(cluster) + "/handback" }

// handBackAsk is the question's body: the epoch the asker now leads, and
// the floor its voters set (the newest earlier leadership).
type handBackAsk struct {
	Epoch uint64 `json:"epoch"`
	Floor uint64 `json:"floor"`
}

// handBackAnswer is the peer's answer: good, or why not, and where its
// retain file stands.
type handBackAnswer struct {
	Good bool   `json:"good"`
	Why  string `json:"why,omitempty"`
	Term uint64 `json:"term,omitempty"`
	Seq  uint64 `json:"seq,omitempty"`
	// AtMs is when the file took its last image (Unix ms), 0 when unknown
	// (the peer's own save as leader).
	AtMs int64 `json:"atMs,omitempty"`
}

// handBackOffer is the hand-back a controller said yes to: its takeover
// within the hold is the peer's hand-back, and says so.
type handBackOffer struct {
	epoch uint64
	at    time.Time
	ans   handBackAnswer
}

// handBackDeadline is how long the asker waits for the peer's answer: a
// few round trips on a pair's link, far below the lease, so a dead peer
// (the usual case) adds next to nothing with nobody driving the plant.
func (s *pairStream) handBackDeadline() time.Duration {
	if mutNaut("handback_long_deadline") {
		return 2 * time.Second
	}
	return min(s.m.o.Lease/4, 250*time.Millisecond)
}

// handBack is the runtime's hand-back hook (runtime.SetHandBack): on a
// takeover in term that would go cold (for the reason cold) with this
// controller's retain store unreadable.
func (s *pairStream) handBack(term uint64, cold string) (bool, string) {
	e := s.m.q.Node()
	start := time.Now()
	if held, at := e.HandBackHeld(); held {
		return false, fmt.Sprintf("this controller handed the lead back %s ago, and hands back at most once every %s",
			time.Since(at).Round(time.Second), quorum.HandBackEvery(s.m.o.Lease))
	}
	floor, ok, why := e.HandBackFloor()
	if !ok {
		return false, handBackFloorWhy(why)
	}
	ans, err := s.askHandBack(term, floor)
	switch {
	case err != nil:
		return false, "the peer did not answer within " + s.handBackDeadline().String() + ": " + err.Error()
	case !ans.Good:
		return false, "the peer's retain file is not good: " + ans.Why
	}
	detail := fmt.Sprintf("this controller's retain store could not be read at its takeover (%s); %s's retain file is at term %d, image %d", cold, s.m.o.Peer, ans.Term, ans.Seq)
	// The write must land well inside the runtime's wait (2 s): a hand-back
	// recorded after the runtime gave up and took over would close the
	// gate on a running controller (Fable, S2).
	ctx, cancel := context.WithTimeout(s.ctx, handBackWriteWait)
	defer cancel()
	if err := e.HandBack(ctx, term, handBackHold*s.m.o.Lease, detail); err != nil {
		return false, "the hand-back could not be recorded: " + err.Error()
	}
	slog.Warn("redundancy: handed the lead back to "+string(s.m.o.Peer)+": this controller's retain store could not be read",
		"epoch", term, "peerFileTerm", ans.Term, "peerFileSeq", ans.Seq, "took", time.Since(start).Round(time.Millisecond))
	s.journal("redundancy.handback", map[string]any{
		"epoch": term, "to": string(s.m.o.Peer), "cold": cold,
		"peerFileTerm": ans.Term, "peerFileSeq": ans.Seq, "floor": floor,
	})
	return true, ""
}

// suspectEmptyFile runs at the member's start: a controller that handed
// back within the guard's window did so over an unreadable file, which
// that read kept aside, so the file now reads as "nothing retained". If
// it still holds no place in the stream (no carry since), the next
// takeover counts it as failed: on initial values, alarmed, its stream
// held, never streaming them over the peer's good file (Fable, M1).
func (s *pairStream) suspectEmptyFile() {
	held, at := s.m.q.Node().HandBackHeld()
	if !held || mutNaut("handback_restart_empty_trusted") {
		return
	}
	if pos, err := s.rt.StreamPosition(); err == nil && pos == nil {
		s.rt.MarkRetainUnreadable()
		slog.Warn("redundancy: this controller handed the lead back over an unreadable retain file and its file holds nothing since: a takeover before a state image is kept counts it as unreadable", "handedBackAt", at)
	}
}

// askHandBack asks the peer, within the deadline.
func (s *pairStream) askHandBack(term, floor uint64) (handBackAnswer, error) {
	ctx, cancel := context.WithTimeout(s.ctx, s.handBackDeadline())
	defer cancel()
	b, _ := json.Marshal(handBackAsk{Epoch: term, Floor: floor})
	url := strings.TrimSuffix(s.url, streamPath(s.m.o.Cluster)) + handBackPath(s.m.o.Cluster)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return handBackAnswer{}, err
	}
	req.Header.Set(node.TokenHeader, s.m.o.Token)
	req.Header.Set("Content-Type", "application/json")
	if f := testHandBackAsk.Load(); f != nil {
		(*f)(s)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return handBackAnswer{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return handBackAnswer{}, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	var ans handBackAnswer
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&ans); err != nil {
		return handBackAnswer{}, err
	}
	return ans, nil
}

// testHandBackAsk, in tests, runs on the asker just before it sends.
var testHandBackAsk atomic.Pointer[func(*pairStream)]

// setTestHandBackAsk sets testHandBackAsk (nil: none).
func setTestHandBackAsk(f func(*pairStream)) {
	if f == nil {
		testHandBackAsk.Store(nil)
		return
	}
	testHandBackAsk.Store(&f)
}

// serveHandBack is the peer's side: is this controller's retain file good
// to take the lead on (see the top of this file)?
func (s *pairStream) serveHandBack(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var ask handBackAsk
	if err := json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&ask); err != nil {
		http.Error(w, "bad question", http.StatusBadRequest)
		return
	}
	ans := s.judgeHandBack(ask)
	if ans.Good {
		s.mu.Lock()
		s.offer = &handBackOffer{epoch: ask.Epoch, at: time.Now(), ans: ans}
		s.mu.Unlock()
	} else {
		slog.Info("redundancy: refused the peer's hand-back", "epoch", ask.Epoch, "why", ans.Why)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(ans)
}

// judgeHandBack answers the question.
func (s *pairStream) judgeHandBack(ask handBackAsk) handBackAnswer {
	e := s.m.q.Node()
	if _, lead := s.rt.LeadingTerm(); lead || s.m.q.IsLeader() {
		return handBackAnswer{Why: "this controller leads"}
	}
	if why := e.MayLead(); why != "" && !mutNaut("handback_ineligible_peer") {
		return handBackAnswer{Why: why}
	}
	if held, _ := e.HandBackHeld(); held && !mutNaut("handback_peer_guard_off") {
		return handBackAnswer{Why: "this controller handed the lead back itself, within " + quorum.HandBackEvery(s.m.o.Lease).String()}
	}
	pos, err := s.rt.StreamPosition()
	switch {
	case err != nil:
		return handBackAnswer{Why: "its retain store could not be read either"}
	case pos == nil && !mutNaut("handback_empty_file_good"):
		return handBackAnswer{Why: "its retain file holds no place in the state stream (none yet, or kept aside as corrupt)"}
	case pos == nil:
		return handBackAnswer{Good: true}
	case pos.Term < ask.Floor && !mutNaut("handback_peer_file_unjudged"):
		return handBackAnswer{Why: fmt.Sprintf("its retain file is of term %d, before the newest leadership the voters know of (term %d)", pos.Term, ask.Floor), Term: pos.Term, Seq: pos.Seq}
	}
	ans := handBackAnswer{Good: true, Term: pos.Term, Seq: pos.Seq}
	if !pos.At.IsZero() {
		ans.AtMs = pos.At.UnixMilli()
	}
	return ans
}

// handBackTaken is, on a takeover in term, the hand-back it comes from:
// this controller said yes to its peer's question within the hold before
// it, for an earlier epoch. nil when none. It is taken once.
func (s *pairStream) handBackTaken(term uint64) *handBackOffer {
	s.mu.Lock()
	defer s.mu.Unlock()
	o := s.offer
	s.offer = nil
	if o == nil || term <= o.epoch || time.Since(o.at) > (handBackHold+1)*s.m.o.Lease {
		return nil
	}
	return o
}

// handBackWarning is the leader's condition after a takeover that took
// the lead from its peer's hand-back: it runs on its own retain file,
// which may be older than the peer's last values.
func (s *pairStream) handBackWarning(o *handBackOffer) string {
	age := "of unknown age (this controller's own save as leader)"
	if o.ans.AtMs != 0 {
		age = "taken " + time.Since(time.UnixMilli(o.ans.AtMs)).Round(time.Second).String() + " before the takeover"
	}
	return fmt.Sprintf("this controller took the lead from %s's hand-back (%s's retain store could not be read): its retained values are its own retain file's, at term %d, image %d, %s; "+
		"a value changed on %s after that is not in it. This clears once a standby follows this leader's state stream again",
		s.m.o.Peer, s.m.o.Peer, o.ans.Term, o.ans.Seq, age, s.m.o.Peer)
}

// journal records a system entry of the pair's.
func (s *pairStream) journal(action string, detail map[string]any) {
	j := s.m.journal.Load()
	if j == nil {
		return
	}
	_, _ = (*j).Record(audit.Entry{
		User: "system", Source: audit.SourceSystem,
		Action: action, Target: string(s.m.o.Self),
		Detail: detail, Result: audit.ResultOK,
	})
}

// handBackFloorWhy says a floor refusal in words.
func handBackFloorWhy(why string) string {
	switch why {
	case quorum.HandBackRecordsUnknown:
		return "the voters can't say how new the peer's retain file must be (a voter lost or rebuilt its state, or runs an older release)"
	case quorum.HandBackNotLeading:
		return "the lease ended before the hand-back was asked"
	}
	return "the voters' check: " + why
}
