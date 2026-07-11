package sparkplug

import "sync"

// Store-and-forward buffers data messages while the node can't deliver them —
// broker disconnected, or (with a primary host configured) the host offline —
// and replays them, marked historical, once delivery resumes. Without it a
// gap in connectivity is a gap in the historian; with it the host backfills.
//
// The buffer is bounded (WithStoreForward max); when full the oldest record is
// dropped so recent data always survives (a ring). Drain is rate-limited so a
// large backlog doesn't flood the broker on reconnect.

// sfRecord is a buffered publish: the topic it was destined for and its
// already-encoded (non-historical) metrics, kept as metrics so the replay can
// stamp them historical.
type sfRecord struct {
	topic   string
	metrics []Metric
	ts      uint64
}

// storeForward is a bounded FIFO of undelivered data messages.
type storeForward struct {
	mu      sync.Mutex
	buf     []sfRecord
	max     int
	dropped int
}

// WithStoreForward enables store-and-forward with a maximum record count.
// A zero or negative max disables it (the default).
func WithStoreForward(maxRecords int) Option {
	return func(n *Node) {
		if maxRecords > 0 {
			n.sf = &storeForward{max: maxRecords}
		}
	}
}

// enqueue appends a record, dropping the oldest when full.
func (s *storeForward) enqueue(r sfRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.buf) >= s.max {
		s.buf = s.buf[1:]
		s.dropped++
	}
	s.buf = append(s.buf, r)
}

// drainBatch removes and returns up to n records from the front.
func (s *storeForward) drainBatch(n int) []sfRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n > len(s.buf) {
		n = len(s.buf)
	}
	out := s.buf[:n:n]
	s.buf = s.buf[n:]
	return out
}

func (s *storeForward) len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.buf)
}
