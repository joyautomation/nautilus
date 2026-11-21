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

	mu        sync.Mutex
	unhealthy string
	warnings  []string
	conds     map[string]string // SetCondition, by key
	logStatus func() *LogStatus // SetLogStatus
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

func (q *Quorum) Term() uint64             { return q.n.Term() }
func (q *Quorum) Decided() <-chan struct{} { return q.n.Decided() }
func (q *Quorum) Run(ctx context.Context)  { q.n.Run(ctx) }
func (q *Quorum) Release()                 { q.n.Release() }
func (q *Quorum) Status() Status {
	st := quorumStatus(q.n.Status())
	q.mu.Lock()
	ls := q.logStatus
	q.mu.Unlock()
	if ls != nil {
		st.Log = ls()
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
