//go:build !js

// The pair election is controller-only: this file is !js so cmd/naut-wasm
// (the playground, which runs no redundant pair) does not import quorum or
// quorum/node (TestWasmStaysFreeOfControllerCode). Status and NewLocal, which
// the playground uses, are in leader.go.

package leader

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/joyautomation/nautilus/internal/fence"
	"github.com/joyautomation/nautilus/quorum/node"
)

// Election decides which replica leads and gates its writes. naut run
// drives one: the Kubernetes Lease (Elector, which is also the standalone
// and local gate), or a redundant pair with a witness, without Kubernetes
// (Quorum, docs/design/redundancy.md). runtime.Coordinator and
// runtime.Termed are the parts the runtime reads.
type Election interface {
	// IsLeader reports whether the write gate is open now.
	IsLeader() bool
	// Term names the current leadership (runtime.Termed).
	Term() uint64
	// Status is what /api/cluster and the HMI show.
	Status() Status
	// Decided is closed once the first decision is made.
	Decided() <-chan struct{}
	// Run drives the election until ctx ends.
	Run(ctx context.Context)
	// Release hands leadership over on an orderly stop.
	Release()
	// SetHealth makes leading depend on live scans (#621).
	SetHealth(h Health)
}

var (
	_ Election = (*Elector)(nil)
	_ Election = (*Quorum)(nil)
)

// Quorum is the Election of a redundant controller pair and its witness
// (design §3): a majority of three voters, over the peer listener, on
// CLOCK_BOOTTIME. Its protocol is package quorum; node drives it.
type Quorum struct {
	n *node.Elector

	mu           sync.Mutex
	unhealthy    string
	warnings     []string
	conds        map[string]string                        // SetCondition, by key
	logStatus    func() *LogStatus                        // SetLogStatus
	streamStatus func() *StreamStatus                     // SetStreamStatus
	peerKeys     func() *PeerKeys                         // SetPeerKeys
	writesCut    func() map[string]int                    // SetWritesCut
	switchover   SwitchoverFunc                           // SetSwitchover
	failback     func(suspend bool, by, why string) error // SetFailback
	unpin        UnpinFunc                                // SetUnpin
}

// UnpinFunc is `naut redundancy unpin` (#710 2b): name is the other
// controller, by the person. It returns the report (JSON), the outcome
// ("done", "refused", "incomplete") and, when not done, why.
type UnpinFunc func(ctx context.Context, name, by string) (report any, outcome, reason string)

// SetUnpin gives the Quorum its unpin (cmd/naut, which holds the pair's
// peer TLS).
func (q *Quorum) SetUnpin(f UnpinFunc) {
	q.mu.Lock()
	q.unpin = f
	q.mu.Unlock()
}

// Unpin is `naut redundancy unpin <name>` on this controller (SetUnpin);
// refused when none is set (peer TLS off).
func (q *Quorum) Unpin(ctx context.Context, name, by string) (report any, outcome, reason string) {
	q.mu.Lock()
	f := q.unpin
	q.mu.Unlock()
	if f == nil {
		why := "peer TLS is off on this controller (redundancy.peer-tls: off): nothing is pinned, so there is nothing to unpin"
		return map[string]string{"message": why}, "refused", why
	}
	return f(ctx, name, by)
}

// The outcomes of Switchover.
const (
	SwitchDone     = "done"     // the RESIGN went to the voters: the standby takes over hot now
	SwitchRefused  = "refused"  // nothing was done: this controller leads on
	SwitchAborted  = "aborted"  // stopped before the resign: this controller leads on, in the same leadership, nothing lost
	SwitchFailover = "failover" // past the resign, no RESIGN sent (the drain did not finish): the lease runs out as in a failover
)

// SwitchoverFunc runs a planned switchover (#710 d1c, the pair's
// composition in cmd/naut): by is the principal. It returns the report
// (JSON), the outcome (Switch*) and, when not done, why.
type SwitchoverFunc func(ctx context.Context, by string) (report any, outcome, reason string)

// SetSwitchover installs the pair's switchover (Switchover).
func (q *Quorum) SetSwitchover(f SwitchoverFunc) {
	q.mu.Lock()
	q.switchover = f
	q.mu.Unlock()
}

// SetFailback sets the pair's failback suspend and resume (#710 d2):
// suspend true suspends, false resumes. by is the principal.
func (q *Quorum) SetFailback(f func(suspend bool, by, why string) error) {
	q.mu.Lock()
	q.failback = f
	q.mu.Unlock()
}

// Failback suspends or resumes the pair's failback, on the leader
// (`naut redundancy failback suspend|resume`, POST /api/cluster/failback).
func (q *Quorum) Failback(suspend bool, by, why string) error {
	q.mu.Lock()
	f := q.failback
	q.mu.Unlock()
	if f == nil {
		return ErrNoFailback
	}
	return f(suspend, by, why)
}

// Switchover hands the lead to the standby in a planned switchover
// (`naut redundancy switchover`, POST /api/cluster/switchover).
func (q *Quorum) Switchover(ctx context.Context, by string) (report any, outcome, reason string) {
	q.mu.Lock()
	f := q.switchover
	q.mu.Unlock()
	if f == nil {
		return nil, SwitchRefused, "this controller's state stream is not running, so it cannot hand over without a seam"
	}
	return f(ctx, by)
}

// SetWritesCut adds the writes cut at their send deadline, by device, to
// Status (its WritesCut field). f is called outside the Quorum's lock.
func (q *Quorum) SetWritesCut(f func() map[string]int) {
	q.mu.Lock()
	q.writesCut = f
	q.mu.Unlock()
}

// SetStreamStatus adds the pair's state stream to Status (its Stream
// block). f is called outside the Quorum's lock.
func (q *Quorum) SetStreamStatus(f func() *StreamStatus) {
	q.mu.Lock()
	q.streamStatus = f
	q.mu.Unlock()
}

// SetPeerKeys adds the pair's peer TLS keys to Status (its PeerKeys
// block). f is called outside the Quorum's lock.
func (q *Quorum) SetPeerKeys(f func() *PeerKeys) {
	q.mu.Lock()
	q.peerKeys = f
	q.mu.Unlock()
}

// SetLogStatus adds the pair's operator-intent log to Status (its Log
// block). f is called outside the Quorum's lock.
func (q *Quorum) SetLogStatus(f func() *LogStatus) {
	q.mu.Lock()
	q.logStatus = f
	q.mu.Unlock()
}

// SetCondition shows msg in Status's warnings under key until it is set
// again; "" clears it. For a condition that comes and goes (the pair's
// members differing, #710).
func (q *Quorum) SetCondition(key, msg string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if msg == "" {
		delete(q.conds, key)
		return
	}
	if q.conds == nil {
		q.conds = map[string]string{}
	}
	q.conds[key] = msg
}

// Warn adds a standing warning to Status (shown by /api/cluster and the
// dashboard).
func (q *Quorum) Warn(msg string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.warnings = append(q.warnings, msg)
}

// NewQuorum builds a pair member's election. Mount Node().Handler() on the
// peer listener, behind node.Guard.
func NewQuorum(o node.Options) (*Quorum, error) {
	n, err := node.New(o)
	if err != nil {
		return nil, err
	}
	return &Quorum{n: n}, nil
}

// Node is the elector underneath: its handler, its operator commands
// (Recommission) and its detailed status.
func (q *Quorum) Node() *node.Elector { return q.n }

func (q *Quorum) IsLeader() bool { return q.n.IsLeader() }

// SendGate is the pair's write gate with its send budget and term, read in
// one look (io.SendGate, design §3.4): drivers that check before each send
// take it.
func (q *Quorum) SendGate() (bool, time.Duration, uint64) { return q.n.SendGate() }

func (q *Quorum) Term() uint64 { return q.n.Term() }

// Resign hands the lead to the standby in a planned switchover (#710 d1,
// node.Elector.Resign): the gate shuts, every output path is drained
// (fence.Drain: every send that passed the gate before it closed has
// ended, cut short where it would wait, and the fence margin has passed
// after the last one), and only then does RESIGN go to the voters. seq and
// current are the standby's acknowledgement of the final image. A drain
// that does not finish sends no RESIGN (node.ErrDrainUnproven): the lease
// runs out as in a failover. The caller has stopped the scans and their
// outputs first (d1c).
func (q *Quorum) Resign(ctx context.Context, seq uint64, current bool) (node.ResignReport, error) {
	return q.n.Resign(ctx, seq, current, fence.Drain)
}

// IsTakeover is runtime.TakeoverKnower: whether the leadership acquired
// last took over from the peer (node.Elector.IsTakeover).
func (q *Quorum) IsTakeover() bool         { return q.n.IsTakeover() }
func (q *Quorum) Decided() <-chan struct{} { return q.n.Decided() }
func (q *Quorum) Run(ctx context.Context)  { q.n.Run(ctx) }
func (q *Quorum) Release()                 { q.n.Release() }
func (q *Quorum) Status() Status {
	st := quorumStatus(q.n.Status())
	q.mu.Lock()
	ls, ss, wc, pk := q.logStatus, q.streamStatus, q.writesCut, q.peerKeys
	q.mu.Unlock()
	if pk != nil {
		st.PeerKeys = pk()
	}
	if wc != nil {
		st.WritesCut = wc()
	}
	if ls != nil {
		st.Log = ls()
	}
	if ss != nil {
		st.Stream = ss()
	}
	q.mu.Lock()
	st.Warnings = append([]string(nil), q.warnings...)
	for _, k := range slices.Sorted(maps.Keys(q.conds)) {
		st.Warnings = append(st.Warnings, q.conds[k])
	}
	q.mu.Unlock()
	return st
}
func quorumStatus(s node.Status) Status {
	return Status{Mode: "quorum", Pod: s.Self, Leader: s.Leader, LeaderAddr: s.LeaderAddr, IsLeader: s.Leading, Transitions: s.Acquisitions,
		Marked: s.Marked, Held: s.Held, Spare: s.Spare}
}

// The outcomes of ClearDuplicate.
const (
	ClearDone       = "done"       // every member answered, and none marks the name a lease later
	ClearRefused    = "refused"    // the check phase refused: nothing was cleared
	ClearIncomplete = "incomplete" // cleared, but a member still marks the name, or was not reached
)

// ClearDuplicate is the operator's clear-duplicate command (#1029 3c-3a,
// node.Elector.ClearDuplicateAll): name is the marked member, by the
// person, for the members' logs. It returns the report (JSON), the outcome
// (ClearDone, ClearRefused, ClearIncomplete) and, when not done, why.
func (q *Quorum) ClearDuplicate(ctx context.Context, name, by string) (report any, outcome, reason string) {
	rep, err := q.n.ClearDuplicateAll(ctx, name, by)
	switch {
	case err == nil:
		return rep, ClearDone, ""
	case errors.Is(err, node.ErrClearRefused):
		return rep, ClearRefused, rep.Refused
	case len(rep.Clear) == 0:
		return rep, ClearRefused, err.Error()
	}
	return rep, ClearIncomplete, incompleteReason(rep, err)
}

// Commission commissions a new pair from this controller (#1029 3c-3c,
// node.Elector.Commission): the report (JSON), the outcome (ClearDone,
// ClearRefused) and, when refused, why.
func (q *Quorum) Commission(ctx context.Context, by, acceptLoss, acceptLostLog string) (report any, outcome, reason string) {
	rep, err := q.n.Commission(ctx, by, node.LogAccept{Loss: acceptLoss, LostLog: acceptLostLog})
	if err != nil {
		return rep, ClearRefused, refusedReason(rep.Refused, err)
	}
	return rep, ClearDone, ""
}

// Recommission is the operator's re-commission of this controller (#1029
// 3c-3c, node.Elector.RecommissionChecked), or with fromSpare its
// recommission --from-spare (node.Elector.FromSpare).
func (q *Quorum) Recommission(ctx context.Context, by string, incarnation, floor uint64, boxReplaced, fromSpare bool, acceptLoss, acceptLostLog string) (report any, outcome, reason string) {
	if fromSpare {
		rep, err := q.n.FromSpare(ctx, by)
		switch {
		case err == nil:
			return rep, ClearDone, ""
		case errors.Is(err, node.ErrFromSpareIncomplete):
			if rep.Refused != "" {
				return rep, ClearIncomplete, rep.Refused // both cleared; a fence or the move aside failed
			}
			var cleared, missed []string
			for _, a := range rep.Clear {
				if a.Err != "" || a.Refused != "" {
					missed = append(missed, a.Err+a.Refused)
				} else {
					cleared = append(cleared, a.Member)
				}
			}
			done := "no voter cleared " + rep.Name
			if len(cleared) > 0 {
				done = strings.Join(cleared, " and ") + " cleared " + rep.Name
			}
			return rep, ClearIncomplete, done + ", the other did not (" + strings.Join(missed, "; ") + "); nothing was moved aside, and this box is still a spare. Run it again once both answer; a voter that hears " + rep.Name + " means the stopped controller runs again"
		}
		return rep, ClearRefused, refusedReason(rep.Refused, err)
	}
	rep, err := q.n.RecommissionChecked(ctx, by, node.RecommissionRequest{Incarnation: incarnation, Floor: floor, BoxReplaced: boxReplaced, Accept: node.LogAccept{Loss: acceptLoss, LostLog: acceptLostLog}})
	if err != nil {
		return rep, ClearRefused, refusedReason(rep.Refused, err)
	}
	return rep, ClearDone, ""
}

// Vouch is the operator's vouch for this controller's operator-intent
// log (#1029 3c-3c-2a, node.Elector.Vouch): log only. acceptLoss and
// acceptLostLog are the digests the person accepts ("" accepts nothing).
func (q *Quorum) Vouch(ctx context.Context, by, acceptLoss, acceptLostLog string) (report any, outcome, reason string) {
	rep, err := q.n.Vouch(ctx, by, node.LogAccept{Loss: acceptLoss, LostLog: acceptLostLog})
	if err != nil {
		return rep, ClearRefused, refusedReason(rep.Refused, err)
	}
	return rep, ClearDone, ""
}

func refusedReason(refused string, err error) string {
	if refused != "" {
		return refused
	}
	return err.Error()
}

// incompleteReason says which members still mark the name, or were not
// reached, after a clear.
func incompleteReason(rep node.ClearReport, err error) string {
	var still []string
	for _, a := range rep.Recheck {
		switch {
		case a.Err != "":
			still = append(still, a.Member+" not reached ("+a.Err+")")
		case a.Marked:
			still = append(still, a.Member+" marks "+rep.Name+" again: a copy still runs where it can hear it")
		}
	}
	for _, a := range rep.Clear {
		switch {
		case a.Err != "":
			still = append(still, a.Member+" missed the clear ("+a.Err+")")
		case a.Refused != "":
			still = append(still, a.Refused)
		}
	}
	for _, a := range rep.Recheck {
		if a.Refused != "" {
			still = append(still, a.Refused)
		}
	}
	if rep.CopyHeardAfter {
		still = append(still, "the members heard two processes named "+rep.Name+" since the clear: a copy is still running (perhaps behind a split, where no single member hears both); find it and stop it, then run this again")
	}
	if len(still) == 0 {
		return err.Error()
	}
	return "cleared, but " + strings.Join(still, "; ") + ". Run it again once that member answers; a mark that comes back means the copy is not stopped"
}

// SetHealth makes renewing and campaigning depend on h, asked at every
// look with half the lease as the bound (as Elector does), and logs a
// change of answer once each way. Call it before Run.
func (q *Quorum) SetHealth(h Health) {
	half := q.n.Lease() / 2
	q.n.SetHealth(func() bool { return q.live(h(time.Now(), half)) })
}

// live logs a change of the health verdict and reports it.
func (q *Quorum) live(err error) bool {
	why, stopped := "", false
	if err != nil {
		why, stopped = err.Error(), errors.Is(err, context.Canceled)
	}
	q.mu.Lock()
	was := q.unhealthy
	q.unhealthy = why
	q.mu.Unlock()
	switch {
	case why == was:
	case why == "":
		slog.Info("redundancy: scans are completing again; this controller renews and campaigns")
	case stopped:
		slog.Info("redundancy: the scan loops have stopped; this controller no longer renews its lease")
	case was != "":
		slog.Error("redundancy: scans are still not completing", "reason", why)
	default:
		slog.Error("redundancy: scans are not completing; the gate is closed and this controller neither renews nor campaigns", "reason", why)
	}
	return why == ""
}
