package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/leader"
	"github.com/joyautomation/nautilus/quorum"
	"github.com/joyautomation/nautilus/quorum/node"
)

// A planned switchover (#710 d1c, redundancy design §4.7, `naut redundancy
// switchover`): the leader hands its lead to a current standby, which goes
// on hot from the leader's last scans with no seam, faster than a
// failover. In order:
//
//  1. Refuse, saying why, unless this controller leads, its standby is
//     current (it acknowledged the newest images: the PEER_READY() rule),
//     no force is held, no takeover hold holds, and the standby says it
//     may take over now (judgeSwitch: it may lead, is not in its hand-back
//     guard, has applied this leadership's images, and its I/O is
//     healthy).
//  2. Stop (runtime.BeginSwitch): each task finishes its scan and its
//     output push, then scans no more; writes from outside are refused.
//  3. The final image of those scans goes to the standby, and the leader
//     waits for its acknowledgement of exactly that image, at most a
//     quarter lease.
//  4. Resign (leader.Quorum.Resign): the gate shuts, every output path is
//     drained, and RESIGN goes to the voters; the standby campaigns at once
//     when its own voter takes it.
//
// Until step 4 nothing is lost: a refusal or a timeout ends the switching
// state and this controller leads on in the same leadership. Step 4 is
// the point of no return: Resign shuts the gate before anything else, so
// once it is called this controller has stopped leading, whatever comes
// back. A drain that does not finish sends no RESIGN, and the lease runs
// out as in a failover.

// switchAskPath is the leader's question to its standby, beside the
// stream's path: may it take over now?
func switchAskPath(cluster string) string { return streamPath(cluster) + "/switch-ask" }

// switchAsk is the question's body: the leadership asking.
type switchAsk struct {
	Epoch uint64 `json:"epoch"`
}

// switchAnswer is the standby's answer: good, or why not.
type switchAnswer struct {
	Good bool   `json:"good"`
	Why  string `json:"why,omitempty"`
}

// switchReport is a switchover's report, the command's answer.
type switchReport struct {
	From     string `json:"from"`
	To       string `json:"to"`
	Epoch    uint64 `json:"epoch,omitempty"`
	FinalSeq uint64 `json:"finalSeq,omitempty"`
	// StopMs is how long the scans under way took to finish (step 2),
	// ImageMs the final image to its acknowledgement (3), ResignMs the
	// gate's close to the last RESIGN's answer (4), TotalMs all of it.
	StopMs   float64 `json:"stopMs,omitempty"`
	ImageMs  float64 `json:"imageMs,omitempty"`
	ResignMs float64 `json:"resignMs,omitempty"`
	TotalMs  float64 `json:"totalMs"`
	// Witness and Peer: which voters took the RESIGN.
	Witness    bool   `json:"witness,omitempty"`
	Peer       bool   `json:"peer,omitempty"`
	WitnessErr string `json:"witnessErr,omitempty"`
	PeerErr    string `json:"peerErr,omitempty"`
}

func msSince(t time.Time) float64 { return float64(time.Since(t).Microseconds()) / 1000 }

// switchover runs the planned switchover for by; see the top of this file.
// It returns the report, the outcome (leader.Switch*) and, when not done,
// why.
func (s *pairStream) switchover(ctx context.Context, by string) (switchReport, string, string) {
	start := time.Now()
	rep := switchReport{From: string(s.m.o.Self), To: string(s.m.o.Peer)}
	done := func(outcome, why string) (switchReport, string, string) {
		rep.TotalMs = msSince(start)
		lvl := slog.LevelInfo
		if outcome != leader.SwitchDone {
			lvl = slog.LevelWarn
		}
		slog.Log(context.Background(), lvl, "redundancy: switchover "+outcome, "by", by, "epoch", rep.Epoch, "why", why, "totalMs", rep.TotalMs)
		return rep, outcome, why
	}
	refuse := func(why string) (switchReport, string, string) { return done(leader.SwitchRefused, why) }

	if !s.switchMu.TryLock() {
		return refuse("a switchover is already under way")
	}
	defer s.switchMu.Unlock()
	term, lead := s.rt.LeadingTerm()
	if !lead || !s.leads(term) {
		return refuse("this controller does not lead: run the switchover on the leader (a standby forwards it there)")
	}
	rep.Epoch = term
	lease := s.m.o.Lease
	if s.hotMaxAge == 0 {
		return refuse("redundancy.hot-max-age is 0, so every takeover is cold: a switchover could not hand over without a seam")
	}
	if n := s.rt.Tags().ForceCount(); n > 0 {
		return refuse(fmt.Sprintf("%d force(s) held: a takeover drops forces, so remove them first", n))
	}
	if s.rt.HoldStatus().Holding {
		return refuse("a takeover hold holds the outputs: release it first (the program's release tag, or naut redundancy release-hold)")
	}
	if _, current := s.syncMark(term); !current && !mutNaut("switch_unready_target") {
		return refuse("the standby is not current: it has not acknowledged the newest state images (see the state-stream condition in naut redundancy status)")
	}
	ans, err := s.askSwitch(term)
	switch {
	case err != nil:
		return refuse("the standby did not answer whether it may take over: " + err.Error())
	case !ans.Good && !mutNaut("switch_unready_target"):
		return refuse("the standby may not take over: " + ans.Why)
	}

	// 2. Stop the scans.
	t := time.Now()
	bctx, cancel := context.WithTimeout(ctx, lease/4)
	err = s.rt.BeginSwitch(bctx, term)
	cancel()
	if err != nil {
		return refuse(err.Error())
	}
	rep.StopMs = msSince(t)
	// Until the resign, every way out ends the switching state: the scans
	// go on in this leadership (a panic too).
	resigning := false
	defer func() {
		if resigning {
			return
		}
		if !mutNaut("abort_keeps_final") {
			s.final.Store(nil) // before the scans go on: the stream sends their images again
		}
		s.rt.EndSwitch()
	}()
	abort := func(why string) (switchReport, string, string) {
		if _, lead := s.rt.LeadingTerm(); s.rt.Switching() != term || !lead || !s.leads(term) {
			return done(leader.SwitchAborted, why+": the switchover is off; this controller's gate closed meanwhile, so whether it still leads is the election's")
		}
		return done(leader.SwitchAborted, why+": the switchover is off, and this controller leads on in the same leadership, nothing lost")
	}

	// 3. The final image, and its acknowledgement.
	t = time.Now()
	img, err := s.rt.FinalImage(term)
	if err != nil {
		return abort("the final image could not be made: " + err.Error())
	}
	img.Build = s.build
	rep.FinalSeq = img.Seq
	s.final.Store(&finalImage{term: term, seq: img.Seq, img: img})
	s.wakeAll()
	actx, cancel := context.WithTimeout(ctx, lease/4)
	err = s.waitAck(actx, term, img.Seq)
	cancel()
	if err != nil {
		return abort(fmt.Sprintf("the standby did not acknowledge the final image within a quarter lease (%s)", lease/4))
	}
	rep.ImageMs = msSince(t)

	// 4. Resign: the point of no return. Not on the request's context: a
	// client that goes away must not cut the drain short. Only while the
	// scans are still stopped in this leadership: the final image is then
	// still the whole state (Fable M1).
	if f := s.beforeResign.Load(); f != nil {
		(*f)()
	}
	if (s.rt.Switching() != term || !s.leads(term)) && !mutNaut("resign_unchecked") {
		return abort("the switching state or the leadership ended before the resign")
	}
	resigning = true
	t = time.Now()
	rr, err := s.m.q.Resign(context.WithoutCancel(ctx), img.Seq, true)
	rep.ResignMs = msSince(t)
	rep.Witness, rep.Peer, rep.WitnessErr, rep.PeerErr = rr.Witness, rr.Peer, rr.WitnessErr, rr.PeerErr
	s.final.Store(nil)
	// The gate is shut for this leadership for good: nothing scans or
	// writes here until a new one, so the switching state may end now
	// (a standby's API then answers "standby", not "switching"; the next
	// leadership's takeover clears it too).
	s.rt.EndSwitch()
	switch {
	case !rr.Resigned:
		return done(leader.SwitchFailover, "this controller stopped leading during the switchover (its lease ran out): the standby takes over as after a failover")
	case errors.Is(err, node.ErrDrainUnproven):
		return done(leader.SwitchFailover, err.Error())
	case err != nil:
		return done(leader.SwitchFailover, err.Error())
	case !rr.Witness && !rr.Peer:
		return done(leader.SwitchFailover, "no voter took the RESIGN: the standby takes over once the lease runs out, as after a failover")
	}
	return done(leader.SwitchDone, "")
}

// askSwitch asks the standby whether it may take over now.
func (s *pairStream) askSwitch(term uint64) (switchAnswer, error) {
	ctx, cancel := context.WithTimeout(s.ctx, s.handBackDeadline())
	defer cancel()
	b, _ := json.Marshal(switchAsk{Epoch: term})
	url := strings.TrimSuffix(s.url, streamPath(s.m.o.Cluster)) + switchAskPath(s.m.o.Cluster)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return switchAnswer{}, err
	}
	req.Header.Set(node.TokenHeader, s.m.o.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return switchAnswer{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return switchAnswer{}, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	var ans switchAnswer
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&ans); err != nil {
		return switchAnswer{}, err
	}
	return ans, nil
}

// serveSwitchAsk is the standby's side of the question.
func (s *pairStream) serveSwitchAsk(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var ask switchAsk
	if err := json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&ask); err != nil {
		http.Error(w, "bad question", http.StatusBadRequest)
		return
	}
	ans := s.judgeSwitch(ask)
	if !ans.Good {
		slog.Info("redundancy: refused the leader's switchover", "epoch", ask.Epoch, "why", ans.Why)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(ans)
}

// judgeSwitch answers whether this standby may take over now. It checks
// that its I/O is healthy (its drivers' inputs are live), not yet that it
// reaches every device the leader reaches (#710: the I/O-health
// comparison, before experimental).
func (s *pairStream) judgeSwitch(ask switchAsk) switchAnswer {
	if _, lead := s.rt.LeadingTerm(); lead || s.m.q.IsLeader() {
		return switchAnswer{Why: "it leads itself"}
	}
	e := s.m.q.Node()
	if why := e.MayLead(); why != "" {
		return switchAnswer{Why: why}
	}
	if held, _ := e.HandBackHeld(); held {
		return switchAnswer{Why: "it handed the lead back itself, within the last " + quorum.HandBackEvery(s.m.o.Lease).String()}
	}
	s.mu.Lock()
	applied := s.term
	s.mu.Unlock()
	if applied != ask.Epoch {
		return switchAnswer{Why: fmt.Sprintf("it has applied no image of the leader's term %d (its newest is of term %d)", ask.Epoch, applied)}
	}
	if why := s.ioUnhealthy(); why != "" && !mutNaut("switch_ignores_standby_io") {
		return switchAnswer{Why: "its I/O is not healthy: " + why}
	}
	return switchAnswer{Good: true}
}

// ioUnhealthy says why this controller's field I/O is not healthy, "" when
// it is: a field driver in error, offline, connecting or degraded (a
// no-session standby's "waiting" is healthy), or drivers that say their
// inputs are not live.
func (s *pairStream) ioUnhealthy() string {
	var bad []string
	if f := s.m.ioStatus.Load(); f != nil {
		for _, d := range (*f)() {
			switch d.State {
			case "error", "offline", "connecting", "degraded":
				bad = append(bad, fmt.Sprintf("%s %s is %s", d.Kind, d.Name, d.State))
			}
		}
	}
	if len(bad) > 0 {
		return strings.Join(bad, "; ")
	}
	if !s.rt.InputsLive() {
		return "its drivers' inputs are not live"
	}
	return ""
}
