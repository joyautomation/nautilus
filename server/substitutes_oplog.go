package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/joyautomation/nautilus/oplog"
	"github.com/joyautomation/nautilus/runtime"
)

// Substitutes through the replicated log of operator intent (#707 PR 3b).
//
// In a redundant pair a start, stop or acknowledgement is not applied by
// the instance that took the request: it is proposed to the oplog, and
// every instance applies the committed entry (SubstituteLog.Apply into
// runtime.ApplySubstitute and its siblings), so a takeover keeps them
// exactly. A substitute's ID is its entry's seq.
//
// SubstituteLog is the seam both ways: it adapts an oplog node to
// runtime.IntentLog (the takeover's catch-up, the leader's expiry
// entries), and its Apply turns committed substitute entries into runtime
// changes. Whoever builds the oplog node (the `naut run` wiring of #710 PR
// 3) hands Apply its entries and gives the SubstituteLog to SetIntentLog
// and to Options.SubstituteLog.
//
// # An unknown outcome
//
// A proposal the log did not confirm (oplog.ErrUnconfirmed: it missed its
// majority; or any error forwarding it, or a timeout) may still be
// committed and applied later. Its outcome is unknown, never "no": the
// answer is 503 "outcome unknown, re-read GET /api/substitutes", and the
// journal records result "unknown". A start may carry a client request ID
// (requestId): retried with the same ID, it is answered with the entry the
// first one started if that committed, and the log never applies a second
// entry with the same ID (runtime.ApplySubstitute), so a retry never
// starts two.

// # No majority
//
// A log that cannot reach a majority of its replicas (oplog Status not
// Writable: a pair's peer down, while the witness is not a replica, #710)
// refuses a start, a stop or an acknowledgement BEFORE proposing it:
// nothing is stored, so the answer is a certain "no" (503, outcome
// refused), never "unknown". A write proposed while the log looked
// writable and then missing its majority is still "outcome unknown".

// SubstituteLog adapts an oplog node for a runtime's substitutes.
type SubstituteLog struct {
	rt   *runtime.Runtime
	node *oplog.Node
	// noMajority is the no-majority refusal's text (SetNoMajority).
	noMajority string
}

// DefaultNoMajority is the no-majority refusal's text unless the wiring
// names its replicas (SetNoMajority).
const DefaultNoMajority = "operator-intent log has no majority: substitutes cannot be started, stopped or acknowledged until a majority of its replicas is reachable again"

// SetNoMajority sets the no-majority refusal's text: a redundant pair
// names its peer. Call it before the server serves.
func (l *SubstituteLog) SetNoMajority(msg string) { l.noMajority = msg }

// refuseWithoutMajority answers the no-majority refusal and reports true
// when the log cannot reach a majority now; nothing has been proposed.
func (s *Server) refuseWithoutMajority(w http.ResponseWriter) bool {
	st := s.subLog.node.Status()
	if st.Writable {
		return false
	}
	msg := s.subLog.noMajority
	if msg == "" {
		msg = DefaultNoMajority
	}
	if st.Reason != "" {
		msg += " (" + st.Reason + ")"
	}
	refuse(w, msg, http.StatusServiceUnavailable)
	return true
}

// NewSubstituteLog is a SubstituteLog for rt. Attach its node once the
// node exists (its Options.Apply must already reach Apply).
func NewSubstituteLog(rt *runtime.Runtime) *SubstituteLog { return &SubstituteLog{rt: rt} }

// Attach sets the oplog node proposals go to.
func (l *SubstituteLog) Attach(n *oplog.Node) { l.node = n }

// Apply applies a committed entry if it is a substitute kind, and reports
// whether it was one (an Applier composed of several hands the others on).
func (l *SubstituteLog) Apply(e oplog.Entry) (bool, error) {
	switch e.Kind {
	case oplog.KindSubstitute:
		var v any
		if len(e.Value) > 0 {
			if err := json.Unmarshal(e.Value, &v); err != nil {
				return true, fmt.Errorf("substitute entry %d: value: %w", e.Seq, err)
			}
		}
		rs := runtime.ReplicatedSubstitute{
			ID: e.Seq, Name: e.Tag, Value: v, By: e.By,
			UntilMs: e.Until, GrantedMs: e.GrantedMs, AckOnStart: e.AckOnStart, RequestID: e.Req,
		}
		if e.Lapsed {
			// Committed too late to be carried out (Lapse, oplog.KindLapse).
			l.rt.ApplyLapsedSubstitute(rs)
			return true, nil
		}
		return true, l.rt.ApplySubstitute(rs)
	case oplog.KindLapse:
		// Journalled once, by the leader that named the starts.
		l.rt.ApplyLapse(e.Refs, e.By, l.node != nil && e.Origin == l.node.Self())
		return true, nil
	case oplog.KindUnsubstitute:
		l.rt.ApplyUnsubstitute(e.Ref, e.By, e.Reason)
		return true, nil
	case oplog.KindSuback:
		l.rt.ApplySuback(e.Refs, e.By)
		return true, nil
	}
	return false, nil
}

// Lapse is the log's Options.Lapse for substitutes: a start a new leader
// commits from an earlier term lapses when it was proposed more than
// runtime.SubstituteStartCommitBound before now, on that leader's clock
// (#1030 review). The leader decides, once, and writes it in the log.
func (l *SubstituteLog) Lapse(e oplog.Entry, now time.Time) bool {
	return e.Kind == oplog.KindSubstitute && now.Sub(time.UnixMilli(e.TS)) > runtime.SubstituteStartCommitBound
}

// SyncLeader is runtime.IntentLog's: the node's catch-up at a takeover.
func (l *SubstituteLog) SyncLeader(ctx context.Context) error { return l.node.SyncLeader(ctx) }

// ProposeExpiry is runtime.IntentLog's: the leader's expiry, as an entry.
func (l *SubstituteLog) ProposeExpiry(ctx context.Context, id uint64) error {
	_, err := l.node.Propose(ctx, oplog.Entry{Kind: oplog.KindUnsubstitute, Ref: id, Reason: runtime.SubstituteReasonExpired, By: "system"})
	return err
}

// propose sends one entry and waits for it to commit (here applied too).
func (l *SubstituteLog) propose(ctx context.Context, e oplog.Entry) (oplog.Entry, error) {
	return l.node.Propose(ctx, e)
}

// outcomeHeader marks an answer whose outcome is unknown, for the audit
// seam (journalled as result "unknown").
const outcomeHeader = "X-Nautilus-Outcome"

// substituteProposeWait bounds a proposal's wait for its commit.
const substituteProposeWait = 10 * time.Second

// unknownOutcome answers a proposal the log did not confirm, or one it
// refused before proposing it: an entry over the log's size cap (oplog.
// ErrTooLarge) was never proposed, so its outcome is known (413).
func unknownOutcome(w http.ResponseWriter, err error) {
	if errors.Is(err, oplog.ErrTooLarge) {
		refuse(w, "too large for the operator-intent log: "+err.Error(), http.StatusRequestEntityTooLarge)
		return
	}
	w.Header().Set(outcomeHeader, "unknown")
	http.Error(w, "outcome unknown: the change was passed to the replicated log but not confirmed, so it may still take effect; re-read GET /api/substitutes ("+err.Error()+")", http.StatusServiceUnavailable)
}

// startThroughLog proposes a start; the committed entry is applied here
// before Propose returns.
func (s *Server) startThroughLog(w http.ResponseWriter, r *http.Request, tag string, value any, d time.Duration, ackOnStart bool, reqID string) {
	if err := s.rt.ValidateSubstitute(tag, value); err != nil {
		refuse(w, err.Error(), http.StatusBadRequest)
		return
	}
	raw, err := json.Marshal(value)
	if err != nil {
		refuse(w, err.Error(), http.StatusBadRequest)
		return
	}
	if s.refuseWithoutMajority(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), substituteProposeWait)
	defer cancel()
	e, err := s.subLog.propose(ctx, oplog.Entry{
		Kind: oplog.KindSubstitute, Tag: tag, Value: raw, GrantedMs: d.Milliseconds(),
		Until: s.rt.WallNow().Add(d).UnixMilli(), AckOnStart: ackOnStart, By: s.principal(r), Req: reqID,
	})
	if err != nil {
		unknownOutcome(w, err)
		return
	}
	started, ok := s.substituteByIDOrRequest(e.Seq, reqID)
	if ok && started.ID != e.Seq && !runtime.SameRequest(started, tag, value, d) {
		refuse(w, fmt.Sprintf("%v: entry %d was started with it for something else", runtime.ErrRequestReused, started.ID), http.StatusConflict)
		return
	}
	if !ok {
		refuse(w, fmt.Sprintf("entry %d committed but did not start here (see the controller log): re-read GET /api/substitutes", e.Seq), http.StatusConflict)
		return
	}
	logAction(r, "substitute: started (replicated)", "tag", started.Name, "id", started.ID, "for", d)
	resp := s.substitutesResponse()
	v := s.substituteView(started)
	resp.Started = &v
	kept := true
	resp.Kept = &kept
	writeJSON(w, http.StatusOK, resp)
}

// substituteByIDOrRequest is the active entry with this ID or, for a
// retried start whose first entry stands, this request ID.
func (s *Server) substituteByIDOrRequest(id uint64, reqID string) (runtime.Substitution, bool) {
	if _, e := s.substituteByID(id); e.ID == id {
		return e, true
	}
	return s.rt.SubstituteByRequest(reqID)
}

// stopThroughLog proposes an unsubstitute naming the entry.
func (s *Server) stopThroughLog(w http.ResponseWriter, r *http.Request, id uint64) {
	name, _ := s.substituteByID(id)
	if name == "" {
		refuse(w, noSubstitute(id), http.StatusNotFound)
		return
	}
	if s.refuseWithoutMajority(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), substituteProposeWait)
	defer cancel()
	if _, err := s.subLog.propose(ctx, oplog.Entry{Kind: oplog.KindUnsubstitute, Ref: id, By: s.principal(r)}); err != nil {
		unknownOutcome(w, err)
		return
	}
	logAction(r, "substitute: ended (replicated)", "tag", name, "id", id)
	resp := s.substitutesResponse()
	kept := true
	resp.Kept = &kept
	writeJSON(w, http.StatusOK, resp)
}

// ackThroughLog proposes a suback naming the entries that await one.
func (s *Server) ackThroughLog(w http.ResponseWriter, r *http.Request, ids []uint64) {
	want := map[uint64]bool{}
	for _, id := range ids {
		want[id] = true
	}
	var refs []uint64
	for _, e := range s.rt.Substitutes() {
		if want[e.ID] && e.AckOnStart && !e.Acked {
			refs = append(refs, e.ID)
		}
	}
	if len(refs) == 0 {
		refuse(w, "nothing to acknowledge: those substitutes ended, were replaced, do not wait for an acknowledgement, or already have one", http.StatusConflict)
		return
	}
	if s.refuseWithoutMajority(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), substituteProposeWait)
	defer cancel()
	if _, err := s.subLog.propose(ctx, oplog.Entry{Kind: oplog.KindSuback, Refs: refs, By: s.principal(r)}); err != nil {
		unknownOutcome(w, err)
		return
	}
	logAction(r, "substitute: acknowledged (replicated)", "count", len(refs))
	resp := s.substitutesResponse()
	kept := true
	resp.Kept = &kept
	writeJSON(w, http.StatusOK, resp)
}
