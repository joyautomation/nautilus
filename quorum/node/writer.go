package node

import (
	"context"
	"sync"
	"time"

	"github.com/joyautomation/nautilus/quorum"
)

// writer is a voter's one disk writer. It writes the latest state asked
// for (a later Seq covers every earlier one, so asks coalesce) and reports
// each write, ok or failed, to done: the grants and Adopts the write holds
// leave only from there. took is how long the oldest ask the write
// served waited for it, its queue behind a write in flight included: what
// a grant held behind it waited (#1436).
//
// A Seq already written successfully is not written again (#1436): a
// campaign's re-ask, taken while the first ask's write was in flight,
// asks the same Seq, and the first write's success released its grant
// with the first's. Only a success counts: when the write in flight
// fails, the re-ask queued behind it is the retry, and its grant leaves
// on that write or is dropped with its failure (Voter.Persisted). A
// writer serves one voter (Seqs are that voter's): the elector makes one
// with the voter it builds in New, and a witness one per pair. waitBox
// rebuilds the voter before Run starts the writer, which has written
// nothing then (the handlers that ask it wait for running).
type writer struct {
	save func(quorum.VoterState) error
	done func(seq uint64, err error, took time.Duration)
	now  func() quorum.Instant

	mu   sync.Mutex
	want *quorum.Persist
	// wantAt is when the oldest ask want covers was made: a later ask
	// replaces want, never this.
	wantAt quorum.Instant
	// busyAt is the oldest ask the write in flight serves, busy while one
	// is.
	busyAt quorum.Instant
	busy   bool
	// written is the highest Seq written successfully: an ask of it or
	// below (a re-ask's, queued while the first was in flight) is not
	// written again.
	written uint64
	kick    chan struct{}
}

func newWriter(save func(quorum.VoterState) error, done func(uint64, error, time.Duration), now func() quorum.Instant) *writer {
	return &writer{save: save, done: done, now: now, kick: make(chan struct{}, 1)}
}

// ask queues p. Never blocks.
func (w *writer) ask(p *quorum.Persist) {
	if p == nil {
		return
	}
	w.mu.Lock()
	if w.want == nil {
		w.wantAt = w.now()
	}
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
		p, at := w.want, w.wantAt
		w.want = nil
		if p != nil && p.Seq <= w.written && !mut("write_dedup_off") {
			p = nil // on disk already: its grants left with that write
		}
		w.busy, w.busyAt = p != nil, at
		w.mu.Unlock()
		if p == nil {
			continue
		}
		if mut("write_timed_from_take") {
			at = w.now() // its queue not counted
		}
		if mut("persisted_before_write") {
			w.done(p.Seq, nil, w.now().Sub(at))
			_ = w.save(p.State)
			continue
		}
		err := w.save(p.State)
		took := w.now().Sub(at)
		w.mu.Lock()
		w.busy = false
		if (err == nil || mut("write_dedup_failed_counts")) && p.Seq > w.written {
			w.written = p.Seq
		}
		w.mu.Unlock()
		w.done(p.Seq, err, took)
	}
}

// waiting is how long the oldest ask not yet on disk has waited, in
// flight or queued; 0 when none.
func (w *writer) waiting(now quorum.Instant) time.Duration {
	w.mu.Lock()
	defer w.mu.Unlock()
	switch {
	case w.busy:
		return now.Sub(w.busyAt)
	case w.want != nil:
		return now.Sub(w.wantAt)
	}
	return 0
}

// waitKey names the grant a vote request waits for: the reply echoes the
// requester, its boot nonce and its request id.
type waitKey struct {
	to        quorum.NodeID
	nonce, id uint64
}

func keyOf(r quorum.Request) waitKey { return waitKey{to: r.From, nonce: r.Nonce, id: r.ID} }
func keyFor(r quorum.Reply) waitKey  { return waitKey{to: r.To, nonce: r.Nonce, id: r.ID} }
