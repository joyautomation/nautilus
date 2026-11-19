// Package oplog is a small replicated log of operator intent for a
// redundant controller: acknowledgements, shelves and unshelves, agreed by
// a majority of instances. Display state is derived by every instance
// from the field and never replicated; only what an operator did is.
//
// The reasoning: what genuinely needs agreement between instances is
// operator state, so that a failover neither loses an acknowledgement nor
// doubles one. That is small and slow-changing, which is why this is a log
// with leader election delegated rather than a replicated state machine.
//
// Shape: a Leadership (leader.Elector through a thin adapter, or a fixed
// leader on a bench) says who sequences. A write from any instance is
// forwarded to the leader, which, one at a time, numbers it with the
// current term, stores it, replicates it to every peer, and once a
// MAJORITY (itself included) has stored it advances the commit index,
// applies it, and answers the client. Followers store what the leader
// sends and apply only up to the commit index the leader advertises, so
// no instance applies an entry a majority does not hold. A write that
// misses the majority is NOT CONFIRMED (ErrUnconfirmed): it stays in the
// leader's log and on any follower that stored it, and it may still be
// committed and applied (by this leader's next write, or by the next
// leader, which takes the most up-to-date log), so its outcome is unknown,
// never "no"; a client re-reads the state. A number is never reused for a
// different entry in the same term. Replication carries the leader's
// previous entry (Raft's consistency check), so a follower takes entries
// only on top of a log equal to the leader's, and a stale entry it holds is
// replaced. A peer that was away asks the leader for everything after its
// commit index. Entries carry the leader's term; a stale leader's
// replication is refused.
//
// Each instance keeps the log in memory and appends it to a local file
// (synced per write; a torn final line from a crash is dropped), which
// survives a process restart but not a reschedule. There is no durable
// copy on the cluster being watched, on purpose: the log must not depend
// on the platform it reports on. Two instances of three cannot form a
// majority, so a split leaves exactly one side writable, and the other
// says so (Status). The log is not compacted; at operator rates it grows
// by kilobytes a day.
//
// Replica-to-replica calls require a shared token (constant-time compared)
// whenever there is more than one peer, and replication is accepted only
// from the address the Leadership names as leader.
package oplog

import (
	"bufio"
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Entry is one operator event.
type Entry struct {
	Seq    uint64   `json:"seq"`
	Term   uint64   `json:"term"`
	TS     int64    `json:"ts"` // epoch ms, assigned by the leader
	Kind   string   `json:"kind"`
	IDs    []string `json:"ids,omitempty"`   // ack
	ID     string   `json:"id,omitempty"`    // shelve, unshelve
	Until  int64    `json:"until,omitempty"` // shelve, substitute: epoch ms
	By     string   `json:"by"`
	Origin string   `json:"origin,omitempty"` // the instance that took the write

	// Substitute values (#707). A substitute entry's Seq is the
	// substitute's ID; unsubstitute and suback name entries by that Seq
	// (Ref, Refs), never by tag, so a stale stop cannot end a newer
	// substitute of the same tag. Every field is omitempty: a log written
	// before them loads unchanged, and an entry of another kind carries
	// none.
	Tag        string          `json:"tag,omitempty"`        // substitute: the address
	Value      json.RawMessage `json:"value,omitempty"`      // substitute: the value, as JSON
	GrantedMs  int64           `json:"grantedMs,omitempty"`  // substitute: the duration granted
	AckOnStart bool            `json:"ackOnStart,omitempty"` // substitute
	Ref        uint64          `json:"ref,omitempty"`        // unsubstitute: the substitute's Seq
	Refs       []uint64        `json:"refs,omitempty"`       // suback: the substitutes' Seqs
	Reason     string          `json:"reason,omitempty"`     // unsubstitute: "" (a person) or "expired"
	// Req is the client's request ID on a substitute (optional): a start
	// retried after an unknown outcome carries the same one, and the
	// runtime applies only the first entry that names it.
	Req string `json:"req,omitempty"`
}

func (e Entry) same(o Entry) bool {
	a, _ := json.Marshal(e)
	b, _ := json.Marshal(o)
	return bytes.Equal(a, b)
}

const (
	KindAck      = "ack"
	KindShelve   = "shelve"
	KindUnshelve = "unshelve"

	// Substitute values (#707): see Entry.
	KindSubstitute   = "substitute"
	KindUnsubstitute = "unsubstitute"
	KindSuback       = "suback"

	// KindNoop is a new leader's empty entry of its own term (SyncLeader):
	// committing it commits everything before it, which is how a leader
	// commits entries an earlier term stored but never committed (Raft
	// §5.4.2). Apply never sees it.
	KindNoop = "noop"
)

// Leadership is who the sequencer is right now. A Kubernetes Lease elector
// answers it; so does a fixed configuration on a bench.
type Leadership interface {
	IsLeader() bool
	// LeaderAddr is the leader's base URL ("" when unknown).
	LeaderAddr() string
}

// Termed is a Leadership that can number its leaderships: a lease's
// transition count, for instance. Entries carry the term, and a stale
// leader's replication is refused. Without it the term is always 1.
type Termed interface {
	Term() uint64
}

// Static is a Leadership fixed by configuration.
type Static struct {
	Self, Leader string
	// Generation is the term (default 1).
	Generation uint64
}

func (s Static) IsLeader() bool     { return s.Self == s.Leader }
func (s Static) LeaderAddr() string { return s.Leader }
func (s Static) Term() uint64 {
	if s.Generation == 0 {
		return 1
	}
	return s.Generation
}

// Applier applies a committed entry to the local alarm engine.
type Applier func(Entry) error

// Options build a Node.
type Options struct {
	// Self is this instance's base URL as peers reach it; Peers are every
	// instance's base URL, self included (the membership). One entry means
	// standalone: every write commits at once.
	Self  string
	Peers []string
	// Leadership says who sequences. Required with more than one peer.
	Leadership Leadership
	// File is the local append-only copy. "" keeps the log in memory only,
	// which New refuses with more than one peer unless Ephemeral is set:
	// a restarted leader with an empty log and an unchanged term would
	// number entries again with (seq, term) pairs a follower already holds
	// for other entries, and the previous-entry check would pass them.
	File string
	// Ephemeral allows several peers with no File. For tests only: it is
	// unsafe in production (see File).
	Ephemeral bool
	// Apply is called for every committed entry, in order, exactly once per
	// process lifetime (replayed from File at startup).
	Apply Applier
	// Token must accompany every replica-to-replica request. Required with
	// more than one peer.
	Token string
	// Timeout bounds one replica call (default 2s).
	Timeout time.Duration
	Now     func() time.Time
	Log     *slog.Logger
}

// Node is one instance's view of the log.
type Node struct {
	o      Options
	client *http.Client
	log    *slog.Logger

	// seqMu serializes sequencing on the leader: number, store, replicate,
	// commit, one proposal at a time. That is what makes the numbers dense
	// and the replication arrive in order.
	seqMu sync.Mutex
	// mu guards the state below.
	mu          sync.Mutex
	entries     []Entry
	commit      uint64 // applied up to here
	term        uint64 // highest term seen
	syncedTerm  uint64 // the term this leader last caught up for
	file        *os.File
	lastContact map[string]time.Time
}

// Status is what /api/quorum reports.
type Status struct {
	Self     string   `json:"self"`
	Leader   string   `json:"leader"`
	IsLeader bool     `json:"isLeader"`
	Term     uint64   `json:"term"`
	Peers    []string `json:"peers"`
	Reached  []string `json:"reached"` // peers heard from within the last 10 s
	LastSeq  uint64   `json:"lastSeq"`
	Commit   uint64   `json:"commit"`
	Writable bool     `json:"writable"`
	Reason   string   `json:"reason,omitempty"`
}

// ErrReadOnly says this instance cannot commit a write right now.
var ErrReadOnly = errors.New("oplog: read-only: no quorum reachable")

// ErrUnconfirmed says a write was stored by the leader but not confirmed
// by a majority: it may still be committed and applied later, so the
// write's outcome is unknown, not refused. It always comes wrapped with
// ErrReadOnly (errors.Is matches both).
var ErrUnconfirmed = errors.New("oplog: write not confirmed: it may still be applied")

// New opens the node, replaying the file into Apply up to its commit mark.
func New(o Options) (*Node, error) {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Timeout <= 0 {
		o.Timeout = 2 * time.Second
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if len(o.Peers) == 0 {
		o.Peers = []string{o.Self}
	}
	if len(o.Peers) > 1 {
		if o.Leadership == nil {
			return nil, errors.New("oplog: several peers need a Leadership; without one each instance would commit alone")
		}
		if o.Token == "" {
			return nil, errors.New("oplog: several peers need a Token; without one any host reaching the port could write")
		}
		if !contains(o.Peers, o.Self) {
			return nil, fmt.Errorf("oplog: Self %q is not among Peers", o.Self)
		}
		if o.File == "" && !o.Ephemeral {
			return nil, errors.New("oplog: several peers need a File; an in-memory log restarted under the same term would reuse sequence numbers its peers hold for other entries")
		}
	}
	n := &Node{o: o, client: &http.Client{Timeout: o.Timeout}, log: o.Log, lastContact: map[string]time.Time{}}
	if o.File != "" {
		if err := n.open(); err != nil {
			return nil, err
		}
	}
	return n, nil
}

// fileLine is one line of the local copy: an entry, or a commit mark.
type fileLine struct {
	E *Entry  `json:"e,omitempty"`
	C *uint64 `json:"c,omitempty"`
}

// open reads the file, dropping a torn final line, and replays the
// committed prefix.
func (n *Node) open() error {
	f, err := os.OpenFile(n.o.File, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		f.Close()
		return err
	}
	good := 0
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Bytes()
		var l fileLine
		if err := json.Unmarshal(line, &l); err != nil {
			break // a torn last line: keep what parsed, drop the rest
		}
		good += len(line) + 1
		switch {
		case l.E != nil:
			n.entries = append(n.entries, *l.E)
			if l.E.Term > n.term {
				n.term = l.E.Term
			}
		case l.C != nil:
			n.commit = *l.C
		default:
			// The first format wrote one bare Entry per line, every one
			// applied as written: load it as committed.
			var e Entry
			if err := json.Unmarshal(line, &e); err == nil && e.Seq > 0 {
				n.entries = append(n.entries, e)
				n.commit = e.Seq
				continue
			}
			return fmt.Errorf("oplog: %s: unrecognised line %q", n.o.File, truncateStr(string(line), 80))
		}
	}
	if good < len(data) {
		n.log.Warn("oplog: dropping a torn final line", "file", n.o.File, "bytes", len(data)-good)
		if err := f.Truncate(int64(good)); err != nil {
			f.Close()
			return err
		}
	}
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		f.Close()
		return err
	}
	n.file = f
	for _, e := range n.entries {
		if e.Seq <= n.commit {
			n.apply(e)
		}
	}
	return nil
}

// Close releases the file.
func (n *Node) Close() error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.file != nil {
		return n.file.Close()
	}
	return nil
}

func (n *Node) standalone() bool { return len(n.o.Peers) <= 1 }

func (n *Node) isLeader() bool {
	if n.standalone() {
		return true
	}
	return n.o.Leadership.IsLeader()
}

func (n *Node) leaderAddr() string {
	if n.standalone() {
		return n.o.Self
	}
	return n.o.Leadership.LeaderAddr()
}

func (n *Node) currentTerm() uint64 {
	if t, ok := n.o.Leadership.(Termed); ok && !n.standalone() {
		if v := t.Term(); v > 0 {
			return v
		}
	}
	return 1
}

func (n *Node) majority() int { return len(n.o.Peers)/2 + 1 }

// LastSeq is the newest stored sequence number.
func (n *Node) LastSeq() uint64 {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.lastLocked()
}

// Commit is the sequence number applied up to.
func (n *Node) Commit() uint64 {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.commit
}

func (n *Node) lastLocked() uint64 {
	if len(n.entries) == 0 {
		return 0
	}
	return n.entries[len(n.entries)-1].Seq
}

// After returns the entries after seq.
func (n *Node) After(seq uint64) []Entry {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.afterLocked(seq)
}

func (n *Node) afterLocked(seq uint64) []Entry {
	i := sort.Search(len(n.entries), func(i int) bool { return n.entries[i].Seq > seq })
	out := make([]Entry, len(n.entries)-i)
	copy(out, n.entries[i:])
	return out
}

// writeLine appends one line to the file and syncs it. Caller holds mu.
func (n *Node) writeLine(l fileLine) error {
	if n.file == nil {
		return nil
	}
	b, _ := json.Marshal(l)
	if _, err := n.file.Write(append(b, '\n')); err != nil {
		return err
	}
	return n.file.Sync()
}

// rewrite replaces the file with the current entries and commit mark,
// atomically: a temp file beside it, synced, renamed over it, the
// directory synced. A crash anywhere leaves either the old file or the
// new one. Caller holds mu.
func (n *Node) rewrite() error {
	if n.file == nil {
		return nil
	}
	dir := filepath.Dir(n.o.File)
	tmp, err := os.CreateTemp(dir, filepath.Base(n.o.File)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	fail := func(err error) error {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	w := bufio.NewWriter(tmp)
	for i := range n.entries {
		e := n.entries[i]
		b, _ := json.Marshal(fileLine{E: &e})
		if _, err := w.Write(append(b, '\n')); err != nil {
			return fail(err)
		}
	}
	c := n.commit
	b, _ := json.Marshal(fileLine{C: &c})
	if _, err := w.Write(append(b, '\n')); err != nil {
		return fail(err)
	}
	if err := w.Flush(); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		return fail(err)
	}
	// Close the old handle before the rename, not after: Windows refuses to
	// replace a file that is open (Go opens without FILE_SHARE_DELETE), so
	// renaming over a live handle fails there. Then reopen whichever file
	// is in place, the new one or, if the rename failed, the old.
	n.file.Close()
	renameErr := os.Rename(tmpName, n.o.File)
	if renameErr != nil {
		os.Remove(tmpName)
	} else if d, err := os.Open(dir); err == nil {
		_ = d.Sync() // not supported on Windows; NTFS journals the rename
		d.Close()
	}
	f, err := os.OpenFile(n.o.File, os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		// n.file stays the closed handle, so every later write fails
		// loudly rather than being skipped as if there were no file.
		return errors.Join(renameErr, err)
	}
	n.file = f
	return renameErr
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

var errGap = errors.New("gap")
var errConflict = errors.New("conflict")
var errMismatch = errors.New("mismatch")

// accept stores entries from the leader (or the leader's own), in order:
// a duplicate with the same content is skipped; a different entry at an
// UNCOMMITTED number replaces it and everything after; a different entry
// at a committed number is a conflict; a number beyond last+1 is a gap.
// Caller holds mu. Returns how many were new.
//
// Every caller hands it an authoritative log, so an uncommitted entry is
// replaced whatever its term (Raft's follower rule): replication from the
// current leader whose previous entry matched (the replicate handler's
// prevSeq/prevTerm check), a follower's catch-up from the leader, and a
// new leader's catch-up from the most up-to-date log a majority holds
// (leaderCatchUp). An older term's uncommitted entry can sit at a seq where
// that log holds a lower term's entry, so a term comparison here would
// refuse the very replacement that keeps the logs equal.
func (n *Node) acceptLocked(es []Entry) (int, error) {
	fresh := 0
	for _, e := range es {
		last := n.lastLocked()
		switch {
		case e.Seq > last+1:
			return fresh, fmt.Errorf("%w: have %d, got %d", errGap, last, e.Seq)
		case e.Seq == last+1:
			n.entries = append(n.entries, e)
			if err := n.writeLine(fileLine{E: &e}); err != nil {
				return fresh, err
			}
			fresh++
		default:
			i := sort.Search(len(n.entries), func(i int) bool { return n.entries[i].Seq >= e.Seq })
			have := n.entries[i]
			if have.same(e) {
				continue
			}
			if e.Seq <= n.commit {
				return fresh, fmt.Errorf("%w: a different entry at committed seq %d", errConflict, e.Seq)
			}
			// Replace the uncommitted suffix.
			n.entries = append(n.entries[:i:i], e)
			if err := n.rewrite(); err != nil {
				return fresh, err
			}
			fresh++
		}
		if e.Term > n.term {
			n.term = e.Term
		}
	}
	return fresh, nil
}

// commitTo applies entries up to seq (bounded by what is stored) and
// records the mark. Caller holds mu.
func (n *Node) commitTo(seq uint64) {
	if seq > n.lastLocked() {
		seq = n.lastLocked()
	}
	if seq <= n.commit {
		return
	}
	for _, e := range n.afterLocked(n.commit) {
		if e.Seq > seq {
			break
		}
		n.apply(e)
	}
	n.commit = seq
	c := seq
	if err := n.writeLine(fileLine{C: &c}); err != nil {
		n.log.Warn("oplog: commit mark", "error", err)
	}
}

func (n *Node) apply(e Entry) {
	if n.o.Apply == nil || e.Kind == KindNoop {
		return
	}
	if err := n.o.Apply(e); err != nil {
		n.log.Warn("oplog: apply", "seq", e.Seq, "kind", e.Kind, "error", err)
	}
}

// ── the wire ──

type replicateReq struct {
	From   string `json:"from"`
	Term   uint64 `json:"term"`
	Commit uint64 `json:"commit"`
	// PrevSeq and PrevTerm name the leader's entry just before Entries (its
	// last entry, for a heartbeat): the follower takes Entries, and the
	// commit index, only when it holds that same entry (Raft's
	// prevLogIndex/prevLogTerm), so a stale entry it holds is replaced,
	// never kept and later applied.
	PrevSeq  uint64  `json:"prevSeq"`
	PrevTerm uint64  `json:"prevTerm"`
	Entries  []Entry `json:"entries"`
}

type replicateResp struct {
	Have   uint64 `json:"have"` // the follower's last seq, so a gap can be filled
	Commit uint64 `json:"commit"`
}

type proposeResp struct {
	Entry  Entry  `json:"entry"`
	Commit uint64 `json:"commit"`
}

type afterResp struct {
	Entries []Entry `json:"entries"`
	Commit  uint64  `json:"commit"`
	Term    uint64  `json:"term"`
	// LastSeq and LastTerm are the answering log's last entry: a new
	// leader compares whole logs by them (Raft's up-to-date rule), not by
	// the entries after the commit index, which may be none.
	LastSeq  uint64 `json:"lastSeq"`
	LastTerm uint64 `json:"lastTerm"`
}

// Propose takes an operator event from this instance's client: sequenced
// here when leader, forwarded to the leader otherwise. It returns once the
// entry is committed and applied locally, or ErrReadOnly.
func (n *Node) Propose(ctx context.Context, e Entry) (Entry, error) {
	e.Origin = n.o.Self
	if n.isLeader() {
		return n.sequence(ctx, e)
	}
	leader := n.leaderAddr()
	if leader == "" {
		return Entry{}, ErrReadOnly
	}
	var out proposeResp
	if err := n.call(ctx, leader, "/api/oplog/propose", e, &out); err != nil {
		return Entry{}, fmt.Errorf("%w (leader %s: %v)", ErrReadOnly, leader, err)
	}
	// Committed by the leader: catch up from it now, so the client that
	// asked sees it at once. Through CatchUp (from this instance's commit
	// index), not by taking the answer's entry and commit index alone: an
	// entry this instance holds below it may be stale, and only the
	// leader's log from the commit on proves which entries are the
	// committed ones (the previous-entry check, as replication has it).
	if err := n.CatchUp(ctx); err != nil {
		n.log.Warn("oplog: catch-up after propose", "error", err)
	}
	return out.Entry, nil
}

// leaderCatchUp is what a new leader does before its first write of a
// term: fetch from every peer whatever is after our last entry and take
// the most advanced answer, so an instance that lost its file (a
// reschedule) and then took the lease does not number its first write
// over entries its peers already committed. Raft restricts votes to the
// most advanced log; here the leader is elected elsewhere, so it catches
// up instead. Caller holds seqMu.
//
// It succeeds only when the peers that answered and this instance make a
// majority: a committed entry is stored on a majority, and any majority
// meets it, so then the most advanced answer holds every committed entry.
// Fewer answers and it returns ErrReadOnly and the term stays unsynced, so
// the next call asks again.
func (n *Node) leaderCatchUp(ctx context.Context, term uint64) error {
	n.mu.Lock()
	if n.syncedTerm == term {
		n.mu.Unlock()
		return nil
	}
	// From the COMMIT index, not the last entry: an uncommitted suffix of
	// this leader's own may be stale (stored, never replicated, and since
	// overtaken by a committed entry at the same seq), and only answers
	// from its commit on carry the entries that replace it. Its own suffix
	// is one of the candidates.
	from := n.commit
	last := n.lastLocked()
	own := afterResp{Entries: n.afterLocked(from), Commit: n.commit, LastSeq: last, LastTerm: n.termAtLocked(last)}
	n.mu.Unlock()
	// The most advanced log is Raft's "up to date", compared on each WHOLE
	// log's last entry: the higher term wins, then the higher seq, so an
	// older term's uncommitted leftovers never outrank a newer term's
	// committed log, even one that ends at the commit index (and so sends
	// no entries). This leader's own log wins a tie.
	better := func(a, b afterResp) bool {
		return a.LastTerm > b.LastTerm || a.LastTerm == b.LastTerm && a.LastSeq > b.LastSeq
	}
	best, bestSelf := own, true
	commit := own.Commit
	answered := 1 // this instance
	var errs []string
	for _, p := range n.o.Peers {
		if p == n.o.Self {
			continue
		}
		var got afterResp
		if err := n.call(ctx, p, fmt.Sprintf("/api/oplog?after=%d", from), nil, &got); err != nil {
			errs = append(errs, p+": "+err.Error())
			continue
		}
		if got.Term > term {
			// A peer has seen a newer term: this leadership is over.
			return fmt.Errorf("%w: a peer is at term %d, this leader at %d", ErrReadOnly, got.Term, term)
		}
		answered++
		commit = max(commit, got.Commit)
		if better(got, best) {
			best, bestSelf = got, false
		}
	}
	if answered < n.majority() {
		return fmt.Errorf("%w: catch-up heard from %d of %d (%s)", ErrReadOnly, answered, len(n.o.Peers), strings.Join(errs, "; "))
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if !bestSelf && len(best.Entries) > 0 {
		if _, err := n.acceptLocked(best.Entries); err != nil {
			n.log.Warn("oplog: leader catch-up", "error", err)
			return err
		}
	}
	// The best log a majority answered with holds every committed entry,
	// so any commit index one of them reported is safe here.
	n.commitTo(commit)
	n.syncedTerm = term
	return nil
}

// SyncLeader brings a new leader's log up to every committed entry, and
// commits and applies them, before it returns: what a takeover needs
// before its first scan, since the lazy catch-up in front of the first
// write (Propose) may come much later, or never. It catches up (see
// leaderCatchUp: an error unless a majority answered) and then, if it
// holds entries past its commit index (another term stored them and its
// leader died before advertising the commit), appends and commits a
// KindNoop of its own term, which commits everything before it.
//
// Nil on a standalone node and once this term is synced. Errors:
//   - ErrReadOnly: this instance is not the leader, a majority did not
//     answer, or a peer has seen a newer term (this leadership is over);
//   - ErrReadOnly wrapping ErrUnconfirmed: the noop missed its majority
//     (the entries before it are still not known committed);
//   - a conflict: the most up-to-date log differs from this one at a seq
//     this instance had committed, which means the logs diverged and
//     needs a person;
//   - the log file's write error.
//
// On any error the term stays unsynced, and the next call tries again.
func (n *Node) SyncLeader(ctx context.Context) error {
	if n.standalone() {
		return nil
	}
	n.seqMu.Lock()
	defer n.seqMu.Unlock()
	if !n.isLeader() {
		return fmt.Errorf("%w: not the leader", ErrReadOnly)
	}
	if err := n.leaderCatchUp(ctx, n.currentTerm()); err != nil {
		return err
	}
	n.mu.Lock()
	behind := n.lastLocked() > n.commit
	n.mu.Unlock()
	if !behind {
		return nil
	}
	_, err := n.sequenceLocked(ctx, Entry{Kind: KindNoop, By: "system", Origin: n.o.Self})
	return err
}

// sequence is the leader's path, one proposal at a time: number, store,
// replicate to a majority, commit, apply. A proposal that misses the
// majority stays in the log, unconfirmed (see ErrUnconfirmed).
func (n *Node) sequence(ctx context.Context, e Entry) (Entry, error) {
	n.seqMu.Lock()
	defer n.seqMu.Unlock()
	if !n.isLeader() {
		return Entry{}, fmt.Errorf("%w: no longer the leader", ErrReadOnly)
	}
	// No write is numbered without a synced term: catch-up and
	// replication are different requests, and a write that replicated to
	// a majority after a failed catch-up could take the number of an
	// entry that majority already committed.
	if err := n.leaderCatchUp(ctx, n.currentTerm()); err != nil {
		return Entry{}, err
	}
	return n.sequenceLocked(ctx, e)
}

// sequenceLocked numbers, stores, replicates and commits one entry.
// Caller holds seqMu.
func (n *Node) sequenceLocked(ctx context.Context, e Entry) (Entry, error) {
	if !n.isLeader() {
		return Entry{}, fmt.Errorf("%w: no longer the leader", ErrReadOnly)
	}
	term := n.currentTerm()
	n.mu.Lock()
	e.Seq = n.lastLocked() + 1
	e.Term = term
	e.TS = n.o.Now().UnixMilli()
	fresh, err := n.acceptLocked([]Entry{e})
	commit := n.commit
	n.mu.Unlock()
	if err != nil {
		return Entry{}, err
	}
	if fresh != 1 {
		return Entry{}, fmt.Errorf("oplog: seq %d was not fresh", e.Seq)
	}
	stored := 1
	var errs []string
	for _, p := range n.o.Peers {
		if p == n.o.Self {
			continue
		}
		if err := n.replicateTo(ctx, p, []Entry{e}, term, commit); err != nil {
			errs = append(errs, p+": "+err.Error())
			continue
		}
		stored++
	}
	if stored < n.majority() {
		// Kept, not truncated: a follower whose reply was lost may hold
		// it, and the number reused for another entry of this term would
		// pass that follower's previous-entry check and leave it applying
		// a different entry at the same seq. The next write (or leader)
		// carries it to a majority, or a newer term's leader replaces it.
		n.mu.Lock()
		n.syncedTerm = 0 // a peer may know more than we do: catch up before the next write
		n.mu.Unlock()
		return Entry{}, fmt.Errorf("%w: %w: %d of %d stored (%s)", ErrReadOnly, ErrUnconfirmed, stored, len(n.o.Peers), strings.Join(errs, "; "))
	}
	n.mu.Lock()
	n.commitTo(e.Seq)
	n.mu.Unlock()
	return e, nil
}

// replicateTo sends entries to one peer, filling a gap it reports.
func (n *Node) replicateTo(ctx context.Context, peer string, es []Entry, term, commit uint64) error {
	send := func(es []Entry) (replicateResp, error) {
		n.mu.Lock()
		prev := n.lastLocked() // a heartbeat: the leader's last entry
		if len(es) > 0 {
			prev = es[0].Seq - 1
		}
		pt := n.termAtLocked(prev)
		n.mu.Unlock()
		var resp replicateResp
		err := n.call(ctx, peer, "/api/oplog/replicate",
			replicateReq{From: n.o.Self, Term: term, Commit: commit, PrevSeq: prev, PrevTerm: pt, Entries: es}, &resp)
		return resp, err
	}
	resp, err := send(es)
	// The peer is behind (a gap: Have is its last entry) or its entry
	// before these is not ours (a mismatch: Have is its commit index):
	// send everything after Have, and again from what the peer answers
	// then (a gap's resend can meet a mismatch below it), until it takes
	// them. Each step moves Have down, to the peer's commit index at
	// worst, where the logs agree, so a few steps settle it; the bound
	// only guards a peer that answers nonsense, and the next heartbeat
	// tries again.
	for range 8 {
		var he *httpError
		if !errors.As(err, &he) || he.code != http.StatusConflict || !he.resend {
			return err
		}
		n.mu.Lock()
		missing := n.afterLocked(resp.Have)
		n.mu.Unlock()
		if len(missing) == 0 {
			return err
		}
		resp, err = send(missing)
	}
	return err
}

// termAtLocked is the term of the entry at seq (0 for seq 0, or none).
// Caller holds mu.
func (n *Node) termAtLocked(seq uint64) uint64 {
	i := sort.Search(len(n.entries), func(i int) bool { return n.entries[i].Seq >= seq })
	if seq == 0 || i >= len(n.entries) || n.entries[i].Seq != seq {
		return 0
	}
	return n.entries[i].Term
}

// Heartbeat is the leader's periodic empty replication: it carries the
// commit index, so followers apply what a majority holds, and counts as
// contact. Run it on a ticker; it is a no-op on a follower.
func (n *Node) Heartbeat(ctx context.Context) {
	if n.standalone() || !n.isLeader() {
		return
	}
	n.mu.Lock()
	commit := n.commit
	n.mu.Unlock()
	term := n.currentTerm()
	for _, p := range n.o.Peers {
		if p == n.o.Self {
			continue
		}
		if err := n.replicateTo(ctx, p, nil, term, commit); err != nil {
			n.log.Debug("oplog: heartbeat", "peer", p, "error", err)
		}
	}
}

// CatchUp asks the leader for everything after our last entry and its
// commit index: at startup, on a gap, and periodically from a follower.
func (n *Node) CatchUp(ctx context.Context) error {
	if n.standalone() || n.isLeader() {
		return nil
	}
	leader := n.leaderAddr()
	if leader == "" {
		return ErrReadOnly
	}
	// From the commit index, not the last entry: an uncommitted entry this
	// instance holds may be stale, and only the leader's entries from the
	// commit on replace it (see acceptLocked).
	var got afterResp
	if err := n.call(ctx, leader, fmt.Sprintf("/api/oplog?after=%d", n.Commit()), nil, &got); err != nil {
		return err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if got.Term < n.term {
		return fmt.Errorf("%w: leader at term %d, have seen %d", errConflict, got.Term, n.term)
	}
	if _, err := n.acceptLocked(got.Entries); err != nil {
		return err
	}
	n.commitTo(got.Commit)
	return nil
}

// Status for /api/quorum.
func (n *Node) Status() Status {
	n.mu.Lock()
	now := n.o.Now()
	var reached []string
	for _, p := range n.o.Peers {
		if p == n.o.Self || now.Sub(n.lastContact[p]) < 10*time.Second {
			reached = append(reached, p)
		}
	}
	s := Status{Self: n.o.Self, Leader: n.leaderAddr(), IsLeader: n.isLeader(), Term: n.term, Peers: n.o.Peers, Reached: reached, LastSeq: n.lastLocked(), Commit: n.commit}
	n.mu.Unlock()
	switch {
	case n.standalone():
		s.Writable = true
	case s.IsLeader:
		s.Writable = len(reached) >= n.majority()
		if !s.Writable {
			s.Reason = fmt.Sprintf("leader, but only %d of %d instances reachable", len(reached), len(n.o.Peers))
		}
	default:
		s.Writable = s.Leader != "" && contains(reached, s.Leader)
		if !s.Writable {
			s.Reason = "not the leader, and the leader is unreachable"
		}
	}
	return s
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// Ping records whether each peer answers, for Status. Run it on a ticker.
func (n *Node) Ping(ctx context.Context) {
	for _, p := range n.o.Peers {
		if p == n.o.Self {
			continue
		}
		var st Status
		_ = n.call(ctx, p, "/api/quorum", nil, &st)
	}
}

type httpError struct {
	code int
	// resend: a replicate refused for a gap or a mismatched previous
	// entry; the answer's Have says where to resend from.
	resend bool
	body   string
}

func (e *httpError) Error() string { return fmt.Sprintf("HTTP %d: %s", e.code, e.body) }

// call is one replica-to-replica request: POST with a body, GET without.
func (n *Node) call(ctx context.Context, base, path string, body any, out any) error {
	var req *http.Request
	var err error
	if body != nil {
		b, _ := json.Marshal(body)
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+path, bytes.NewReader(b))
	} else {
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+path, nil)
	}
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if n.o.Token != "" {
		req.Header.Set("X-Nautilus-Cluster", n.o.Token)
	}
	resp, err := n.client.Do(req)
	if err != nil {
		n.lost(base)
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode/100 != 2 {
		if resp.StatusCode >= 500 {
			n.lost(base)
		}
		he := &httpError{code: resp.StatusCode, body: strings.TrimSpace(string(data))}
		if resp.StatusCode == http.StatusConflict && out != nil {
			_ = json.Unmarshal(data, out) // a gap answer carries the peer's last seq
			he.resend = strings.Contains(he.body, `"gap"`) || strings.Contains(he.body, `"mismatch"`)
		}
		return he
	}
	n.mu.Lock()
	n.lastContact[base] = n.o.Now()
	n.mu.Unlock()
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

// lost forgets a peer's last contact: Status stops counting it at once.
func (n *Node) lost(base string) {
	n.mu.Lock()
	delete(n.lastContact, base)
	n.mu.Unlock()
}

// Handler serves the replica-to-replica API and /api/quorum.
func (n *Node) Handler() http.Handler {
	mux := http.NewServeMux()
	auth := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if n.o.Token != "" && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Nautilus-Cluster")), []byte(n.o.Token)) != 1 {
				http.Error(w, "cluster token", http.StatusUnauthorized)
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("GET /api/quorum", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, n.Status())
	})
	mux.HandleFunc("GET /api/oplog", auth(func(w http.ResponseWriter, r *http.Request) {
		var after uint64
		_, _ = fmt.Sscanf(r.URL.Query().Get("after"), "%d", &after)
		n.mu.Lock()
		last := n.lastLocked()
		resp := afterResp{Entries: n.afterLocked(after), Commit: n.commit, Term: n.term, LastSeq: last, LastTerm: n.termAtLocked(last)}
		n.mu.Unlock()
		writeJSON(w, http.StatusOK, resp)
	}))
	mux.HandleFunc("POST /api/oplog/propose", auth(func(w http.ResponseWriter, r *http.Request) {
		if !n.isLeader() {
			http.Error(w, "not the leader", http.StatusConflict)
			return
		}
		var e Entry
		if err := json.NewDecoder(r.Body).Decode(&e); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		out, err := n.sequence(r.Context(), e)
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, http.StatusOK, proposeResp{Entry: out, Commit: n.Commit()})
	}))
	mux.HandleFunc("POST /api/oplog/replicate", auth(func(w http.ResponseWriter, r *http.Request) {
		var req replicateReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// Only the leader the Leadership names may replicate, and never
		// from an older term than one already seen.
		if l := n.leaderAddr(); !n.standalone() && l != "" && req.From != l {
			http.Error(w, fmt.Sprintf("replication from %s; the leader is %s", req.From, l), http.StatusForbidden)
			return
		}
		n.mu.Lock()
		if req.Term < n.term {
			have := n.lastLocked()
			n.mu.Unlock()
			writeJSON(w, http.StatusConflict, map[string]any{"error": "stale term", "have": have})
			return
		}
		if req.Term > n.term {
			n.term = req.Term
		}
		// Raft's consistency check: take these entries, and the commit
		// index, only on top of the leader's own previous entry. Without
		// it a stale entry this instance holds (stored, never committed)
		// would stay under the leader's next ones and be applied once the
		// commit index passed it.
		var err error
		switch {
		case req.PrevSeq > n.lastLocked():
			err = fmt.Errorf("%w: have %d, the leader's previous entry is %d", errGap, n.lastLocked(), req.PrevSeq)
		case n.termAtLocked(req.PrevSeq) != req.PrevTerm:
			err = errMismatch
		default:
			_, err = n.acceptLocked(req.Entries)
		}
		if err == nil {
			// Only as far as this request proved the logs equal.
			n.commitTo(min(req.Commit, req.PrevSeq+uint64(len(req.Entries))))
		}
		have := n.lastLocked()
		if errors.Is(err, errMismatch) {
			have = n.commit // resend from what is committed here
		}
		commit := n.commit
		if l := n.leaderAddr(); l != "" {
			n.lastContact[l] = n.o.Now()
		}
		n.mu.Unlock()
		if err != nil {
			kind := "conflict"
			switch {
			case errors.Is(err, errGap):
				kind = "gap"
			case errors.Is(err, errMismatch):
				kind = "mismatch"
			}
			writeJSON(w, http.StatusConflict, map[string]any{"error": kind, "detail": err.Error(), "have": have, "commit": commit})
			return
		}
		writeJSON(w, http.StatusOK, replicateResp{Have: have, Commit: commit})
	}))
	return mux
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
