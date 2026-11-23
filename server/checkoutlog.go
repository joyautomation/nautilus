//go:build !js

package server

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/joyautomation/nautilus/checkout"
	"github.com/joyautomation/nautilus/oplog"
)

// CheckoutLog adapts a pair's operator-intent log for the I/O checkout (#719
// slice 5): it applies the checkout entries the log commits to the store, and
// carries the proposals. Like the notes' index, the store is filled by replay
// at start and emptied when a history is superseded; unlike the file store it
// writes no file, so the runs are the log's and the same on both controllers.
type CheckoutLog struct {
	store *checkout.Store
	node  *oplog.Node
}

// NewCheckoutLog is a pair's checkout store: dir is the state directory (this
// controller's signing key lives there).
func NewCheckoutLog(dir string) (*CheckoutLog, error) {
	st, err := checkout.OpenLog(dir, nil)
	if err != nil {
		return nil, err
	}
	return &CheckoutLog{store: st}, nil
}

// Store is the checkout store the log fills.
func (l *CheckoutLog) Store() *checkout.Store { return l.store }

// Attach sets the oplog node proposals go to.
func (l *CheckoutLog) Attach(n *oplog.Node) {
	l.node = n
	l.store.AttachLog(func(ctx context.Context, by string, body []byte) (uint64, error) {
		// refused before anything is proposed, with the log's own reason, so
		// nothing is queued for when a majority returns
		if st := n.Status(); !st.Writable {
			msg := "neither the other controller nor the witness answers"
			switch {
			case st.NotSynced:
				msg = LogNotSynced
			case st.Uncounted:
				msg = LogCopiesNotSynced
			}
			if st.Reason != "" {
				msg += " (" + st.Reason + ")"
			}
			return 0, fmt.Errorf("%w: %s", checkout.ErrNoMajority, msg)
		}
		e, err := n.Propose(ctx, oplog.CheckoutEntryOf(by, body))
		if err == nil {
			// on a follower Propose can return once the leader committed even if
			// this replica's catch-up failed: until the entry is applied here
			// its refusal (if apply refused it) is not known
			if werr := waitApplied(ctx, n.Commit, e.Seq); werr != nil {
				return 0, werr
			}
			return e.Seq, nil
		}
		switch {
		case errors.Is(err, oplog.ErrTooLarge):
			return 0, fmt.Errorf("%w: %v", checkout.ErrInvalid, err)
		}
		// passed to the log and not confirmed: it may still commit
		return 0, fmt.Errorf("%w (%v)", checkout.ErrUnknown, err)
	})
}

// Apply takes a committed entry if it is a checkout entry, and reports whether
// it was (an Applier composed of several hands the others on). A build from
// before the checkout keeps the entry whole in its log and applies it when it
// is upgraded and replays.
func (l *CheckoutLog) Apply(e oplog.Entry) (bool, error) {
	if e.Kind != oplog.KindCheckout {
		return false, nil
	}
	body, ok := e.CheckoutBody()
	if !ok {
		return true, fmt.Errorf("checkout entry %d carries no readable body (written by a build that dropped it?)", e.Seq)
	}
	return true, l.store.ApplyLogged(e.Seq, e.TS, body)
}

// EndSuperseded empties the store when this copy's log history is replaced (a
// person accepted its loss, naut redundancy vouch): its runs are then exactly
// the new history's.
func (l *CheckoutLog) EndSuperseded() { l.store.Reset() }

// waitApplied waits, within ctx, until this replica's commit index reaches
// seq (the entry has been applied here), else says the outcome is not yet
// known on this controller.
func waitApplied(ctx context.Context, commit func() uint64, seq uint64) error {
	for commit() < seq {
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w (committed by the leader, not yet applied on this controller)", checkout.ErrUnknown)
		case <-time.After(20 * time.Millisecond):
		}
	}
	return nil
}
