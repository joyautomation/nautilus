package node

import (
	"context"
	"sync"

	"github.com/joyautomation/nautilus/quorum"
)

// writer is a voter's one disk writer. It writes the latest state asked
// for (a later Seq covers every earlier one, so asks coalesce) and reports
// each write, ok or failed, to done: the grants and Adopts the write holds
// leave only from there.
type writer struct {
	save func(quorum.VoterState) error
	done func(seq uint64, err error)

	mu   sync.Mutex
	want *quorum.Persist
	kick chan struct{}
}

func newWriter(save func(quorum.VoterState) error, done func(uint64, error)) *writer {
	return &writer{save: save, done: done, kick: make(chan struct{}, 1)}
}

// ask queues p. Never blocks.
func (w *writer) ask(p *quorum.Persist) {
	if p == nil {
		return
	}
	w.mu.Lock()
	if w.want == nil || p.Seq > w.want.Seq {
		w.want = p
	}
	w.mu.Unlock()
	select {
	case w.kick <- struct{}{}:
	default:
	}
}

// run writes until ctx ends.
func (w *writer) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.kick:
		}
		w.mu.Lock()
		p := w.want
		w.want = nil
		w.mu.Unlock()
		if p == nil {
			continue
		}
		if mut("persisted_before_write") {
			w.done(p.Seq, nil)
			_ = w.save(p.State)
			continue
		}
		w.done(p.Seq, w.save(p.State))
	}
}

// waitKey names the grant a vote request waits for: the reply echoes the
// requester, its boot nonce and its request id.
type waitKey struct {
	to        quorum.NodeID
	nonce, id uint64
}

func keyOf(r quorum.Request) waitKey { return waitKey{to: r.From, nonce: r.Nonce, id: r.ID} }
func keyFor(r quorum.Reply) waitKey  { return waitKey{to: r.To, nonce: r.Nonce, id: r.ID} }
