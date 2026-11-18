package sparkplug

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Store-and-forward buffers data messages while the node can't deliver them —
// broker disconnected, or (with a primary host configured) the host offline —
// and replays them, marked historical, once delivery resumes. Without it a
// gap in connectivity is a gap in the historian; with it the host backfills.
//
// The buffer is bounded (WithStoreForward max); when full the oldest record is
// dropped so recent data always survives (a ring). Drain is rate-limited so a
// large backlog doesn't flood the broker on reconnect.
//
// The ring is memory. WithStoreForwardFile adds a journal beside it, so a
// process that exits during an outage (a restart to fix it, an upgrade, a
// crash) does not lose what it buffered: the next process loads the journal
// and replays it, as historical, after its first birth. The memory ring stays
// the source of truth while the process runs; the journal only mirrors it, and
// without a file nothing below the ring runs at all.

// sfRecord is a buffered publish: the topic it was destined for and its
// already-encoded (non-historical) metrics, kept as metrics so the replay can
// stamp them historical.
type sfRecord struct {
	topic   string
	metrics []Metric
	ts      uint64
	idx     uint64 // its index in the journal; unused without one
}

// storeForward is a bounded FIFO of undelivered data messages.
//
// Locking: mu guards the ring (buf, dropped) and is all len() takes, so
// Status — which holds Node.mu — never waits on disk. jmu guards the journal
// and is taken before mu by every operation that changes the ring, so the
// journal's order is the ring's. Every such operation is issued by the
// publish tick under Node.pubMu anyway; jmu makes the type safe on its own.
type storeForward struct {
	mu      sync.Mutex
	buf     []sfRecord
	max     int
	dropped int

	jmu    sync.Mutex
	path   string // WithStoreForwardFile; "" = memory only (the default)
	log    *slog.Logger
	loaded bool       // the journal's backlog has been read into the ring
	j      *sfJournal // open journal; nil when memory only or not started
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

// WithStoreForwardFile journals the store-and-forward buffer to path so it
// survives a process exit. Needs WithStoreForward. See sfJournal for the
// file format and the durability it buys.
func WithStoreForwardFile(path string) Option {
	return func(n *Node) { n.sfFile = path }
}

// enqueue appends a record, dropping the oldest when full.
func (s *storeForward) enqueue(r sfRecord) {
	s.jmu.Lock()
	defer s.jmu.Unlock()
	if s.j != nil {
		idx, ok := s.j.append(r)
		if !ok {
			return // cannot be encoded, so it could never be replayed either
		}
		r.idx = idx
	}
	s.mu.Lock()
	if len(s.buf) >= s.max {
		s.buf = s.buf[1:]
		s.dropped++
	}
	s.buf = append(s.buf, r)
	s.mu.Unlock()
	if s.j != nil && s.j.frames > 2*s.max {
		s.compactLocked()
	}
}

// drainBatch removes and returns up to n records from the front. The journal
// keeps them until delivered says they went out.
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

// requeue puts records back at the front, in order, ahead of anything
// buffered since: an interrupted drain hands back what it did not deliver.
// The ring still holds: if that overflows max, the oldest go.
func (s *storeForward) requeue(recs []sfRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	buf := make([]sfRecord, 0, len(recs)+len(s.buf))
	buf = append(append(buf, recs...), s.buf...)
	if over := len(buf) - s.max; over > 0 {
		buf = buf[over:]
		s.dropped += over
	}
	s.buf = buf
}

// delivered tells the journal that sent — the front of the last drain —
// went out; the caller has already requeued the rest. Once the ring is
// empty the journal is cleared; until then an ack marks everything before
// the last sent record's successor as delivered. A no-op without a file.
func (s *storeForward) delivered(sent []sfRecord) {
	s.jmu.Lock()
	defer s.jmu.Unlock()
	if s.j == nil || len(sent) == 0 {
		return
	}
	if s.len() == 0 {
		s.j.clear()
		return
	}
	s.j.ack(sent[len(sent)-1].idx + 1)
}

func (s *storeForward) len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.buf)
}

// open attaches the journal (WithStoreForwardFile). The first open loads
// what a previous process left in it into the ring; every open then
// rewrites the file from the ring, so it starts as exactly what the ring
// holds. An error means the file cannot be read or written at all (a
// missing directory, permissions); content it cannot make sense of is set
// aside, not an error. A no-op without a file, or when already open.
func (s *storeForward) open() error {
	s.jmu.Lock()
	defer s.jmu.Unlock()
	if s.path == "" || s.j != nil {
		return nil
	}
	if !s.loaded {
		recs, err := loadJournal(s.path, s.log)
		if err != nil {
			return err
		}
		s.loaded = true
		s.mu.Lock()
		buf := append(recs, s.buf...)
		if over := len(buf) - s.max; over > 0 {
			buf = buf[over:]
			s.dropped += over
		}
		s.buf = buf
		s.mu.Unlock()
		if len(recs) > 0 {
			s.log.Info("sparkplug: store-forward backlog loaded; it replays as historical after the birth",
				"file", s.path, "records", len(recs))
		}
	}
	j := &sfJournal{path: s.path, log: s.log}
	if err := s.rewriteLocked(j); err != nil {
		return fmt.Errorf("sparkplug: store-forward file: %w", err)
	}
	s.j = j
	return nil
}

// close syncs and closes the journal; the ring keeps running in memory.
func (s *storeForward) close() {
	s.jmu.Lock()
	defer s.jmu.Unlock()
	if s.j != nil {
		s.j.close()
		s.j = nil
	}
}

// compactLocked rewrites the journal as exactly the ring: no acked records,
// no records the ring has dropped. Caller holds jmu.
func (s *storeForward) compactLocked() {
	if err := s.rewriteLocked(s.j); err != nil {
		s.j.fail("rewrite", err)
	}
}

// rewriteLocked writes the ring to j's file and renumbers the ring's
// records to their new journal indices. Nothing is mid-drain: rewrites run
// from open and enqueue, never between a drain and its delivered. Caller
// holds jmu.
func (s *storeForward) rewriteLocked(j *sfJournal) error {
	s.mu.Lock()
	for i := range s.buf {
		s.buf[i].idx = uint64(i)
	}
	recs := append([]sfRecord(nil), s.buf...)
	s.mu.Unlock()
	return j.rewrite(recs)
}

// ── journal ───────────────────────────────────────────────────────────────

// sfJournal is the store-and-forward file: an append-only log that mirrors
// the ring.
//
// Format: the 8-byte header "NAUTSF1\n", then frames. A frame is a 4-byte
// big-endian body length, the body's 4-byte big-endian CRC-32C, and the body:
//
//	'D' uvarint(len(topic)) topic payload   a buffered record; payload is
//	                                        the Sparkplug B protobuf of the
//	                                        record (its timestamp and its
//	                                        metrics' own, no seq)
//	'A' uvarint(i)                          every record before the i-th
//	                                        (0-based, counting 'D' frames)
//	                                        went out
//
// The ring is always a window of that record sequence — drops and
// deliveries both take from its front — so loading keeps the records not
// yet acked and then, like the ring, the newest max of them: the file can
// hold more than the ring (records the ring has since dropped, acked ones)
// and still load as the ring was. A rewrite (compaction) once
// it holds more than twice the ring's frames keeps it bounded; once a
// drain empties the ring it is truncated to the header.
//
// Durability: each frame is one write(2) to a file opened O_APPEND, so a
// frame is in the kernel the moment the record is buffered, and a process
// exit of any kind — a restart, an upgrade, kill -9, a crash — loses
// nothing. fsync is what a power loss or a kernel crash needs, and costs a
// disk flush, so the journal syncs at most once per sfSyncInterval (the
// first record of an outage at once, then at most once a second), plus on
// close, on a rewrite and on clearing. A power loss can lose the last
// second of buffered records; a torn frame at the tail is skipped on load.
// Records drained but not yet acked replay again after a crash: delivery
// across a restart is at least once.
type sfJournal struct {
	path     string
	log      *slog.Logger
	f        *os.File
	frames   int    // frames since the last rewrite
	next     uint64 // index the next record gets
	lastSync time.Time
	enc      []byte // scratch frame buffer
}

const (
	sfMagic        = "NAUTSF1\n"
	sfSyncInterval = time.Second
	// sfMaxFrame bounds a frame's declared length on load: a length past it
	// is garbage, not a record (a record is one tick's metrics).
	sfMaxFrame = 64 << 20
	sfData     = 'D'
	sfAck      = 'A'
)

var sfCRC = crc32.MakeTable(crc32.Castagnoli)

// frame wraps body into j.enc as one frame.
func (j *sfJournal) frame(body []byte) []byte {
	out := j.enc[:0]
	out = binary.BigEndian.AppendUint32(out, uint32(len(body)))
	out = binary.BigEndian.AppendUint32(out, crc32.Checksum(body, sfCRC))
	out = append(out, body...)
	j.enc = out
	return out
}

func encodeRecord(r sfRecord) ([]byte, error) {
	p, err := Payload{Timestamp: r.ts, OmitSeq: true, Metrics: r.metrics}.Encode()
	if err != nil {
		return nil, err
	}
	body := make([]byte, 0, 1+binary.MaxVarintLen64+len(r.topic)+len(p))
	body = append(body, sfData)
	body = binary.AppendUvarint(body, uint64(len(r.topic)))
	body = append(body, r.topic...)
	return append(body, p...), nil
}

// append journals a record and returns its index. It reports false only
// when the record cannot be encoded (it could never be published either); a
// write failure stops the journal and leaves the ring to carry on in memory.
func (j *sfJournal) append(r sfRecord) (uint64, bool) {
	body, err := encodeRecord(r)
	if err != nil {
		j.log.Warn("sparkplug: store-forward record not buffered", "topic", r.topic, "error", err)
		return 0, false
	}
	idx := j.next
	if j.f == nil {
		return idx, true
	}
	if _, err := j.f.Write(j.frame(body)); err != nil {
		j.fail("append", err)
		return idx, true
	}
	j.next++
	j.frames++
	if time.Since(j.lastSync) >= sfSyncInterval {
		if err := j.f.Sync(); err != nil {
			j.fail("sync", err)
			return idx, true
		}
		j.lastSync = time.Now()
	}
	return idx, true
}

// ack journals that every record before index through went out.
func (j *sfJournal) ack(through uint64) {
	if j.f == nil {
		return
	}
	body := binary.AppendUvarint([]byte{sfAck}, through)
	if _, err := j.f.Write(j.frame(body)); err != nil {
		j.fail("ack", err)
		return
	}
	j.frames++
}

// clear empties the journal to its header: the backlog has gone out.
func (j *sfJournal) clear() {
	if j.f == nil {
		return
	}
	if err := j.f.Truncate(int64(len(sfMagic))); err != nil {
		j.fail("clear", err)
		return
	}
	if err := j.f.Sync(); err != nil {
		j.fail("clear", err)
		return
	}
	j.frames, j.next = 0, 0
}

// rewrite replaces the file with exactly recs: written to a temporary file,
// synced, and renamed over the journal, so a crash mid-rewrite leaves the
// old journal or the new one, never half of either.
func (j *sfJournal) rewrite(recs []sfRecord) error {
	if j.f != nil {
		_ = j.f.Close()
		j.f = nil
	}
	var b bytes.Buffer
	b.WriteString(sfMagic)
	for _, r := range recs {
		// enqueue refuses a record that does not encode, so this fails only
		// if something is badly wrong — and a record left out would shift
		// every index after it.
		body, err := encodeRecord(r)
		if err != nil {
			return err
		}
		b.Write(j.frame(body))
	}
	tmp := j.path + ".tmp"
	if err := writeSynced(tmp, b.Bytes()); err != nil {
		return err
	}
	if err := os.Rename(tmp, j.path); err != nil {
		return err
	}
	syncDir(filepath.Dir(j.path))
	f, err := os.OpenFile(j.path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	j.f, j.frames, j.next, j.lastSync = f, len(recs), uint64(len(recs)), time.Now()
	return nil
}

// fail stops journaling after a disk error (a full disk, say): the ring
// carries on in memory, as without a file, and a Start of this node
// rewrites the journal from it. What the file held stays: a process that
// exits before then replays it, at least once, as it would have.
func (j *sfJournal) fail(op string, err error) {
	j.log.Error("sparkplug: store-forward file failed; buffering in memory only",
		"file", j.path, "op", op, "error", err)
	if j.f != nil {
		_ = j.f.Close()
		j.f = nil
	}
}

func (j *sfJournal) close() {
	if j.f == nil {
		return
	}
	if err := j.f.Sync(); err != nil {
		j.log.Warn("sparkplug: store-forward file sync", "file", j.path, "error", err)
	}
	_ = j.f.Close()
	j.f = nil
}

func writeSynced(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := fsyncFile(f); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// fsyncFile and syncDir are variables so a test can see when they run
// (the bdSeq and journal saves sync before their rename and after it).
var fsyncFile = (*os.File).Sync

// syncDir makes a rename durable where the platform allows it (best effort:
// a directory cannot be synced on Windows).
var syncDir = func(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}

// errSFUnreadable: the file is not a store-forward journal at all.
var errSFUnreadable = errors.New("not a store-forward file")

// loadJournal reads the records a journal holds, oldest first, replaying its
// acks. A missing or empty file is an empty backlog. A torn last frame — a
// crash mid-write — is skipped with a log line. Anything worse is set aside
// (renamed to <path>.corrupt-<time>) and logged: a file that is not a
// journal at all loads as empty, and one damaged part-way loads the records
// before the damage. Only an error reading the file is returned.
func loadJournal(path string, log *slog.Logger) ([]sfRecord, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("sparkplug: store-forward file: %w", err)
	}
	recs, rest, perr := parseJournal(data)
	switch {
	case perr == nil:
	case errors.Is(perr, errSFUnreadable):
		setAside(path, log, "unreadable; buffering starts empty", perr)
		return nil, nil
	case rest == 0:
		// Nothing after the bad frame: the last write was torn.
		log.Warn("sparkplug: store-forward file: skipped a torn last record",
			"file", path, "error", perr, "records", len(recs))
	default:
		setAside(path, log, "damaged part-way; loading the records before the damage",
			fmt.Errorf("%w (%d bytes after it)", perr, rest), "records", len(recs))
	}
	return recs, nil
}

func setAside(path string, log *slog.Logger, what string, err error, args ...any) {
	aside := path + ".corrupt-" + time.Now().UTC().Format("20060102T150405Z")
	if rerr := os.Rename(path, aside); rerr != nil {
		log.Error("sparkplug: store-forward file "+what+"; could not set it aside",
			append([]any{"file", path, "error", err, "rename", rerr}, args...)...)
		return
	}
	log.Error("sparkplug: store-forward file "+what,
		append([]any{"file", path, "error", err, "setAside", aside}, args...)...)
}

// parseJournal decodes a journal's bytes. On a bad frame it returns the
// records before it, the error, and how many bytes lie past the end of the
// bad frame (0: it ran to the end of the file, so it is a torn tail).
func parseJournal(data []byte) (recs []sfRecord, rest int, err error) {
	if len(data) == 0 {
		return nil, 0, nil
	}
	if len(data) < len(sfMagic) {
		if bytes.HasPrefix([]byte(sfMagic), data) {
			return nil, 0, errors.New("torn header") // crash while creating it
		}
		return nil, 0, errSFUnreadable
	}
	if string(data[:len(sfMagic)]) != sfMagic {
		return nil, 0, errSFUnreadable
	}
	b := data[len(sfMagic):]
	var next uint64 // index of the next 'D' frame
	for len(b) > 0 {
		if len(b) < 8 {
			return recs, 0, errors.New("short frame header")
		}
		l := binary.BigEndian.Uint32(b)
		sum := binary.BigEndian.Uint32(b[4:])
		if l > sfMaxFrame || int(l) > len(b)-8 {
			return recs, 0, fmt.Errorf("frame of %d bytes runs past the end of the file", l)
		}
		body := b[8 : 8+l]
		b = b[8+l:]
		if crc32.Checksum(body, sfCRC) != sum {
			return recs, len(b), errors.New("frame checksum mismatch")
		}
		if recs, err = applyFrame(recs, body, &next); err != nil {
			return recs, len(b), err
		}
	}
	return recs, 0, nil
}

// applyFrame applies one frame: a record joins recs as index *next, an ack
// drops the records before its index (recs is in index order).
func applyFrame(recs []sfRecord, body []byte, next *uint64) ([]sfRecord, error) {
	if len(body) == 0 {
		return recs, errors.New("empty frame")
	}
	switch body[0] {
	case sfData:
		tl, k := binary.Uvarint(body[1:])
		if k <= 0 || tl > uint64(len(body)-1-k) {
			return recs, errors.New("bad record topic")
		}
		topic := string(body[1+k : 1+k+int(tl)])
		p, err := DecodePayload(body[1+k+int(tl):])
		if err != nil {
			return recs, fmt.Errorf("record payload: %w", err)
		}
		r := sfRecord{topic: topic, metrics: p.Metrics, ts: p.Timestamp, idx: *next}
		*next++
		return append(recs, r), nil
	case sfAck:
		through, k := binary.Uvarint(body[1:])
		if k <= 0 {
			return recs, errors.New("bad ack")
		}
		i := 0
		for i < len(recs) && recs[i].idx < through {
			i++
		}
		return recs[i:], nil
	}
	return recs, fmt.Errorf("unknown frame kind %q", body[0])
}
