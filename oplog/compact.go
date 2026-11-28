package oplog

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Compaction (#1455, docs/design/oplog-compaction.md). The leader proposes
// a KindCompact entry (K) naming the committed entries up to Through that
// still matter (the keep list, decided outside this package); every copy
// carries out the same drop once K commits, checked by a digest over the
// kept entries' (seq, term) pairs. Kept entries stay verbatim. The file then
// holds a header line, the kept entries, a commit line and the tail.
//
// Carried out logically at commit (cut): the copy answers as compacted at
// once. Physically in the background (disk): a goroutine builds the new
// file outside the lock and swaps it in under it.

// KindCompact is a compaction decision (see above). Its body is in Value
// (compactBody). Apply never sees it; it is never lapsed.
const KindCompact = "compact"

// compactWhy leads the header line, so a build from before compaction,
// which refuses the file and shows its first 80 bytes, shows the fix. Short
// and with no character JSON escapes (`<`, `>`, `&`), so it fits after
// `{"b":{"why":"`.
const compactWhy = "compacted log: update naut, or export notes and start a new log"

// baseHeader is the file's first line ({"b":...}) once it is compacted.
// Why must stay the first field (see compactWhy).
type baseHeader struct {
	Why         string  `json:"why"`
	Through     uint64  `json:"through"`
	ThroughTerm uint64  `json:"throughTerm"`
	Compact     uint64  `json:"compact"` // the K it carries out
	Kept        int     `json:"kept"`
	Digest      string  `json:"digest"`
	Rules       int     `json:"rules"` // the K's keep-rules version
	Lineage     Lineage `json:"lineage"`
	// K is the compaction itself, for a base installed from another copy
	// (compact_wire.go) before its tail (which holds K) has come.
	K *Entry `json:"k,omitempty"`
}

// compactBody is K's Value.
type compactBody struct {
	V           int    `json:"v"`
	Rules       int    `json:"rules"`
	Through     uint64 `json:"through"`
	ThroughTerm uint64 `json:"throughTerm"`
	PrevThrough uint64 `json:"prevThrough"`
	// Keep is the new kept seqs in (PrevThrough, Through]; Drop the seqs
	// kept before that are dropped now. Each a list of [first, last] runs.
	Keep   [][2]uint64 `json:"keep,omitempty"`
	Drop   [][2]uint64 `json:"drop,omitempty"`
	Kept   int         `json:"kept"`
	Digest string      `json:"digest"`
}

// baseState is a base: everything up to Through is committed and only the
// Kept seqs of it remain; Compact is the K that made it (0: none, the log
// is dense).
type baseState struct {
	Through, ThroughTerm, Compact uint64
	Kept                          []uint64 // sorted; never changed in place
	Digest                        string
	Rules                         int
	K                             *Entry // an installed base's K (baseHeader.K)
}

// MarkBase is the base the log file was last rewritten to (Mark.Base): a
// file put back from before it (its base is lower) is a lost log.
type MarkBase struct {
	Through uint64 `json:"through"`
	Digest  string `json:"digest"`
}

var (
	errCompactOff = errors.New("oplog: compaction is not switched on")
	// errBehindBase: a peer needs entries below this copy's base, which it
	// no longer holds; an install refills it (not built yet: it refuses).
	errBehindBase = errors.New("oplog: the peer is behind this copy's compacted base")
	errDiverged   = errors.New("oplog: this copy's log diverged from the others' (a compaction's digest did not match): restart naut on this controller to move it aside and sync")
)

// pairDigest is the hex SHA-256 over (seq, term) pairs, 16 bytes each, in
// order.
func pairDigest(seqs []uint64, term func(uint64) uint64) string {
	h := sha256.New()
	var b [16]byte
	for _, s := range seqs {
		binary.BigEndian.PutUint64(b[:8], s)
		binary.BigEndian.PutUint64(b[8:], term(s))
		h.Write(b[:])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// mayCompact: raw may be a KindCompact entry (as stored.mayLapse).
func (s stored) mayCompact() bool {
	return bytes.Contains(s.raw, []byte("compact")) || bytes.Contains(s.raw, []byte(`\u`))
}

// compactOf is K's body when s is a KindCompact entry.
func compactOf(s stored) (compactBody, bool) {
	if !s.mayCompact() {
		return compactBody{}, false
	}
	e := s.entry()
	if e.Kind != KindCompact {
		return compactBody{}, false
	}
	var b compactBody
	if json.Unmarshal(e.Value, &b) != nil {
		return compactBody{}, true // a K, unreadable: carryOut refuses it
	}
	return b, true
}

// noteFloorLocked raises the commit floor for a stored entry that is a K:
// its Through was at or below a leader's commit index, so everything up to
// it is committed, whether K itself commits or not. Caller holds mu.
func (n *Node) noteFloorLocked(s stored) {
	if b, ok := compactOf(s); ok && !mutOplog("compact_floor_ignored") {
		n.floor = max(n.floor, b.Through)
	}
}

// termLocked is termAtLocked for a seq the log holds or its base's Through.
func (n *Node) termLocked(seq uint64) uint64 { return n.termAtLocked(seq) }

// divergeLocked records that this copy's log is not the others' (K at seq
// does not fit it, why): it counts, takes, commits and applies nothing more
// (stoppedLocked). A controller's log moves aside at its next start, as a
// superseded one does; the witness's (ResetInPlace) at once, after the
// commit that found it (commitTo). Caller holds mu.
func (n *Node) divergeLocked(seq uint64, why string) {
	if n.mark.Diverged != "" {
		return
	}
	m := n.mark
	m.Diverged = fmt.Sprintf("the compaction at seq %d does not fit this copy's log: %s", seq, why)
	m.Synced, m.Alone, m.Accept = false, false, nil
	if n.o.Mark == "" {
		n.setMarkMem(m) // no mark file: stopped in this process
	} else if err := n.setMarkLocked(m); err != nil {
		n.setMarkMem(m) // fail closed: stopped in this process all the same
		n.log.Error("oplog: can't record that the log diverged; stopped in this process", "error", err)
	}
	n.syncedTerm = 0
	n.log.Error("oplog: this copy's operator-intent log DIVERGED from the others': it counts for nothing; restart naut on this controller to move it aside and sync", "why", m.Diverged)
}

func cmp(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// stoppedLocked: this copy takes, commits and applies nothing: it is
// superseded (it waits for a restart to take a newer history) or its log
// diverged. Caller holds mu.
func (n *Node) stoppedLocked() bool {
	return n.supersedingLocked() || n.mark.Diverged != "" && !mutOplog("diverged_counts")
}

// stopErrLocked is why a stopped copy refuses. Caller holds mu.
func (n *Node) stopErrLocked() error {
	if n.mark.Diverged != "" && !n.supersedingLocked() {
		return errDiverged
	}
	return errSuperseded
}

// kickCompact wakes the background worker, starting it the first time.
// Caller holds mu.
func (n *Node) kickCompact() {
	if n.kick == nil || n.closed {
		return // a Node not built by New (tests), or closing: no worker
	}
	if !n.built {
		// open, inside New, holds no lock: the worker starts once New is
		// done (kickOwed).
		n.kickOwed = true
		return
	}
	if !n.workerOn {
		n.workerOn = true
		n.worker.Add(1)
		go n.compactLoop()
	}
	select {
	case n.kick <- struct{}{}:
	default:
	}
}

// readHeader reads a header line ({"b":...}); ok false when line is not
// one.
func readHeader(line []byte) (baseHeader, bool, error) {
	if !bytes.HasPrefix(line, []byte(`{"b":`)) {
		return baseHeader{}, false, nil
	}
	var l fileLine
	if err := json.Unmarshal(line, &l); err != nil || l.B == nil {
		return baseHeader{}, true, fmt.Errorf("a compacted log's header can't be read: %q", truncateStr(string(line), 80))
	}
	if l.B.Through == 0 || l.B.ThroughTerm == 0 || l.B.Compact <= l.B.Through || l.B.K != nil && (l.B.K.Seq != l.B.Compact || l.B.K.Kind != KindCompact) {
		return baseHeader{}, true, fmt.Errorf("a compacted log's header is not one: through %d at term %d, compact %d", l.B.Through, l.B.ThroughTerm, l.B.Compact)
	}
	if k := l.B.K; k != nil {
		var b compactBody
		if json.Unmarshal(k.Value, &b) != nil || b.Through != l.B.Through || b.ThroughTerm != l.B.ThroughTerm || b.Kept != l.B.Kept || b.Digest != l.B.Digest {
			return baseHeader{}, true, fmt.Errorf("a compacted log's header names a compaction that did not make its base (at seq %d)", k.Seq)
		}
	}
	return *l.B, true, nil
}

// compactStatus is what Status says of compaction, "" fields omitted.
func (n *Node) compactStatusLocked(s *Status) {
	s.CompactThrough = n.cut.Through
	s.CompactPending = n.cut.Compact > n.disk.Compact
	s.CompactRefused = n.compactRefused
	s.Diverged = n.mark.Diverged
	n.wireStatusLocked(s)
}

// DivergedAtStart: this process's start moved a diverged log aside, so it
// does not restart itself for another (a person decides a second time).
func (n *Node) DivergedAtStart() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.divergedAtStart
}

// compactedForTest waits until the file holds the newest committed base.
func (n *Node) compactedForTest(ctx context.Context) error {
	for {
		n.mu.Lock()
		ok := n.cut.Compact <= n.disk.Compact
		n.mu.Unlock()
		if ok {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
}
