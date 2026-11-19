package server

import (
	"context"
	"encoding/json"
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

// SubstituteLog adapts an oplog node for a runtime's substitutes.
type SubstituteLog struct {
	rt   *runtime.Runtime
	node *oplog.Node
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
		return true, l.rt.ApplySubstitute(runtime.ReplicatedSubstitute{
			ID: e.Seq, Name: e.Tag, Value: v, By: e.By,
			UntilMs: e.Until, GrantedMs: e.GrantedMs, AckOnStart: e.AckOnStart, RequestID: e.Req,
		})
	case oplog.KindUnsubstitute:
		l.rt.ApplyUnsubstitute(e.Ref, e.By, e.Reason)
		return true, nil
	case oplog.KindSuback:
		l.rt.ApplySuback(e.Refs, e.By)
		return true, nil
	}
	return false, nil
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

// unknownOutcome answers a proposal the log did not confirm.
func unknownOutcome(w http.ResponseWriter, err error) {
	w.Header().Set(outcomeHeader, "unknown")
	http.Error(w, "outcome unknown: the change was passed to the replicated log but not confirmed, so it may still take effect; re-read GET /api/substitutes ("+err.Error()+")", http.StatusServiceUnavailable)
}

// startThroughLog proposes a start; the committed entry is applied here
// before Propose returns.
func (s *Server) startThroughLog(w http.ResponseWriter, r *http.Request, tag string, value any, d time.Duration, ackOnStart bool, reqID string) {
	if err := s.rt.ValidateSubstitute(tag, value); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	raw, err := json.Marshal(value)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
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
	if !ok {
		http.Error(w, fmt.Sprintf("entry %d committed but did not start here (see the controller log): re-read GET /api/substitutes", e.Seq), http.StatusConflict)
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
		http.Error(w, fmt.Sprintf("no substitute %d is active (it ended, expired, or a newer one replaced it)", id), http.StatusNotFound)
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
		http.Error(w, "nothing to acknowledge: those substitutes ended, were replaced, do not wait for an acknowledgement, or already have one", http.StatusConflict)
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
