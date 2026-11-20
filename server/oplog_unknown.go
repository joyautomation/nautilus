package server

// oplog_unknown.go is the journal's second look at a proposal whose
// outcome was unknown (#708 PR 8c, #709). A route that passes an operator
// action to the replicated log and does not hear it confirmed answers
// "outcome unknown" and journals result "unknown": the action may still
// take effect. When this replica later sees that very proposal committed
// (or lapsed), it journals the resolution, oplog.resolved, linked to the
// unknown entry by its id (detail.resolves). "Committed" says the log has
// it and this replica applied it in order; it does not say the action
// took effect (an engine or the runtime may still refuse it at apply):
// the state, and the leader's own journal lines, say that.
//
// The correlation is in memory, on the replica that answered, and only
// on evidence. A proposal is matched by its request id (Entry.Req: the
// server's own for an alarm action, a substitute's stop or ack; the
// client's requestId for a substitute start), its origin (this replica)
// and its content (every field the proposer sets). Without a request id the key is
// incomplete, and two proposals in flight with one request id are
// ambiguous: either way it stays "unknown", never a guess. A proposal that
// never commits, or whose replica restarts first, stays "unknown". The
// set is bounded: an unresolved proposal pushed out of it is journalled
// as staying unknown (evicted), never dropped silently. A substitute start
// retried with the same requestId while the first is still tracked makes
// both ambiguous: they stay unknown.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/joyautomation/nautilus/audit"
	"github.com/joyautomation/nautilus/oplog"
)

// maxUnknowns bounds the proposals kept for a resolution.
const maxUnknowns = 256

// Outcomes a resolution entry records (detail.outcome).
const (
	outcomeCommitted = "committed"
	outcomeLapsed    = "lapsed"
	outcomeEvicted   = "stays unknown (evicted)"
)

// actionResolved is a resolution's journal action; detail.action is the
// action it resolves.
const actionResolved = "oplog.resolved"

// proposalState is where a tracked proposal is.
type proposalState int

const (
	proposalInFlight  proposalState = iota // the route is waiting for it
	proposalUnknown                        // the route answered "outcome unknown"
	proposalConfirmed                      // the route heard it commit; kept until it is applied here, so a later proposal with its request id is ambiguous
)

// tracked is one proposal kept for a resolution.
type tracked struct {
	entry   oplog.Entry // as proposed
	state   proposalState
	seen    string // outcomeCommitted or outcomeLapsed once applied here, before the route answered
	seenSeq uint64
	// The unknown entry's id, action and target (the seam's), once the
	// route has answered.
	auditID, action, target string
	ambiguous               bool // another proposal in flight with the same request id
}

// unknowns is the set, and where resolutions are journalled.
type unknowns struct {
	mu     sync.Mutex
	byReq  map[string][]*tracked
	order  []*tracked // oldest first, for the bound
	record func(audit.Entry)
}

func newUnknowns(record func(audit.Entry)) *unknowns {
	return &unknowns{byReq: map[string][]*tracked{}, record: record}
}

// newRequestID is a fresh request id for a proposal that has none.
func newRequestID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return "srv-" + hex.EncodeToString(b[:])
}

// begin starts tracking e, about to be proposed; nil (untracked) without a
// request id. A second proposal with a request id already tracked makes
// both ambiguous.
func (u *unknowns) begin(e oplog.Entry) *tracked {
	if u == nil || e.Req == "" {
		return nil
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	t := &tracked{entry: e}
	if others := u.byReq[e.Req]; len(others) > 0 {
		t.ambiguous = true
		for _, o := range others {
			o.ambiguous = true
		}
	}
	u.byReq[e.Req] = append(u.byReq[e.Req], t)
	u.order = append(u.order, t)
	u.evictLocked()
	return t
}

// confirmed marks t heard committed by its route. It is kept until it is
// applied here (a forwarded proposal can be confirmed before this replica
// catches up): until then a proposal reusing its request id is ambiguous,
// and its own apply resolves nothing.
func (u *unknowns) confirmed(t *tracked) {
	if u == nil || t == nil {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if t.seen != "" {
		u.dropLocked(t)
		return
	}
	t.state = proposalConfirmed
}

// refusedBeforeProposing ends tracking t: it was never proposed.
func (u *unknowns) refusedBeforeProposing(t *tracked) {
	if u == nil || t == nil {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.dropLocked(t)
}

// unknown marks t answered "outcome unknown", journalled as auditID
// (action, target). One already applied here is resolved at once.
func (u *unknowns) unknown(t *tracked, auditID, action, target string) {
	if u == nil || t == nil {
		return
	}
	u.mu.Lock()
	t.state, t.auditID, t.action, t.target = proposalUnknown, auditID, action, target
	var out []audit.Entry
	if t.seen != "" && !t.ambiguous && auditID != "" {
		out = append(out, u.resolution(t, t.seen, t.seenSeq))
		u.dropLocked(t)
	}
	u.mu.Unlock()
	u.journal(out)
}

// applied hears a committed entry applied on this replica (origin is
// self, replay excluded: see the logs' observe). It resolves the tracked
// proposal it is, when exactly one is.
func (u *unknowns) applied(e oplog.Entry) {
	if u == nil || e.Req == "" {
		return
	}
	outcome := outcomeCommitted
	if e.Lapsed {
		outcome = outcomeLapsed
	}
	u.mu.Lock()
	var match *tracked
	n := 0
	for _, t := range u.byReq[e.Req] {
		if sameProposal(t.entry, e) {
			match = t
			n++
		}
	}
	var out []audit.Entry
	if i := slices.IndexFunc(u.byReq[e.Req], func(t *tracked) bool {
		return t.state == proposalConfirmed && sameProposal(t.entry, e)
	}); i >= 0 {
		// The apply of a proposal its route heard confirmed: nothing to
		// resolve, and it no longer makes a later one ambiguous.
		u.dropLocked(u.byReq[e.Req][i])
		u.mu.Unlock()
		return
	}
	switch {
	case n != 1 || match.ambiguous:
		// Ambiguous: it stays unknown.
	case match.state == proposalUnknown && match.auditID != "":
		out = append(out, u.resolution(match, outcome, e.Seq))
		u.dropLocked(match)
	case match.seen == "":
		match.seen, match.seenSeq = outcome, e.Seq // the route has not answered yet
	}
	u.mu.Unlock()
	u.journal(out)
}

// sameProposal reports whether the committed entry c is the proposal p,
// comparing every field the proposer sets: the leader assigns seq, term,
// time and origin, the node sets Lapsed, and an older binary may drop the
// principal, so only those are not compared.
func sameProposal(p, c oplog.Entry) bool {
	return p.Kind == c.Kind && p.Req == c.Req && p.ID == c.ID && p.Tag == c.Tag && p.By == c.By &&
		p.Until == c.Until && p.GrantedMs == c.GrantedMs && p.AckOnStart == c.AckOnStart &&
		p.Ref == c.Ref && p.Reason == c.Reason && bytes.Equal(p.Value, c.Value) &&
		slices.Equal(p.IDs, c.IDs) && slices.Equal(p.Refs, c.Refs)
}

// resolution is the journal entry for t's outcome.
func (u *unknowns) resolution(t *tracked, outcome string, seq uint64) audit.Entry {
	e := audit.Entry{Source: audit.SourceSystem, Action: actionResolved, Target: t.target,
		Detail: map[string]any{"resolves": t.auditID, "action": t.action, "outcome": outcome}}
	e.User = principalOfEntryIn(t.entry, e.Detail)
	switch outcome {
	case outcomeCommitted:
		e.Result = audit.ResultOK
		e.Detail["seq"] = seq
		e.Detail["note"] = "committed to the operator-intent log and applied in order here; whether it took effect (an alarm engine or the runtime may still refuse it) is in the state"
	case outcomeLapsed:
		e.Result = audit.ResultRefused // it never took effect
		e.Detail["seq"] = seq
		e.Detail["note"] = "lapsed: committed too late to be carried out, so it never took effect"
	default:
		e.Result = audit.ResultUnknown
		e.Detail["reason"] = "no longer tracked: whether it took effect is not known; read the state"
	}
	return e
}

// evictLocked keeps the set to maxUnknowns: the oldest proposal the route
// answered "unknown" goes first, journalled as staying unknown; one still
// in flight is kept (its route is waiting for it, bounded by the propose
// wait).
func (u *unknowns) evictLocked() {
	for len(u.order) > maxUnknowns {
		// A confirmed proposal first: nothing is owed for it.
		if i := slices.IndexFunc(u.order, func(t *tracked) bool { return t.state == proposalConfirmed }); i >= 0 {
			u.dropLocked(u.order[i])
			continue
		}
		i := slices.IndexFunc(u.order, func(t *tracked) bool { return t.state == proposalUnknown })
		if i < 0 {
			return
		}
		t := u.order[i]
		if t.auditID != "" {
			e := u.resolution(t, outcomeEvicted, 0)
			go u.record(e)
		}
		u.dropLocked(t)
	}
}

func (u *unknowns) dropLocked(t *tracked) {
	u.order = slices.DeleteFunc(u.order, func(o *tracked) bool { return o == t })
	rest := slices.DeleteFunc(u.byReq[t.entry.Req], func(o *tracked) bool { return o == t })
	if len(rest) == 0 {
		delete(u.byReq, t.entry.Req)
	} else {
		u.byReq[t.entry.Req] = rest
	}
}

// journal records es off the caller's path (an apply holds the log).
func (u *unknowns) journal(es []audit.Entry) {
	for _, e := range es {
		go u.record(e)
	}
}

// principalOfEntry is the journal's principal for a log entry: its
// Principal, else its By (an entry from before the field, or one an older
// binary re-marshalled).
func principalOfEntry(e oplog.Entry) string {
	if e.Principal != "" {
		return e.Principal
	}
	return e.By
}

// principalOfEntryIn is principalOfEntry, saying in detail when it fell
// back to By: that is a display name (for a token's alarm action, the name
// its client typed), a label, not a principal.
func principalOfEntryIn(e oplog.Entry, detail map[string]any) string {
	if e.Principal == "" {
		detail["principalFrom"] = "by: a display name, not a verified principal (an entry from an older binary)"
	}
	return principalOfEntry(e)
}

// seamRef is the audit seam's entry for a request, before it is recorded:
// a route that answers "outcome unknown" links its resolution to it.
type seamRef struct {
	id, action, target string
	at                 time.Time
}

type seamRefKey struct{}

// withSeamRef gives r the seam entry's id, action and target.
func withSeamRef(r *http.Request, ref seamRef) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), seamRefKey{}, ref))
}

// seamRefOf is r's seam entry, if it goes through the seam.
func seamRefOf(r *http.Request) (seamRef, bool) {
	ref, ok := r.Context().Value(seamRefKey{}).(seamRef)
	return ref, ok
}

// proposeFor proposes e for request r through propose: it carries r's
// principal (the journal's form), a request id when it has none (an alarm
// action, a substitute's stop or ack: a substitute start keeps the
// client's, or none), and it is tracked so that an "outcome unknown" can
// be resolved later on evidence. The route answers unknownOutcome on an
// error, as before.
func (s *Server) proposeFor(ctx context.Context, r *http.Request, e oplog.Entry, ownReq bool,
	propose func(context.Context, oplog.Entry) (oplog.Entry, error)) (oplog.Entry, error) {
	e.Principal = s.principal(r)
	if ownReq && e.Req == "" {
		e.Req = newRequestID()
	}
	t := s.unknowns.begin(e)
	out, err := propose(ctx, e)
	if errors.Is(err, oplog.ErrTooLarge) {
		s.unknowns.refusedBeforeProposing(t) // its outcome is known
		return out, err
	}
	if err != nil {
		ref, _ := seamRefOf(r)
		s.unknowns.unknown(t, ref.id, ref.action, ref.target)
		return out, err
	}
	s.unknowns.confirmed(t)
	return out, nil
}
