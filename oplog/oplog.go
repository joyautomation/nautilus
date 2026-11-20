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

	// Lapsed is set by the node, never stored: this entry is one a
	// KindLapse entry committed with it names (see KindLapse). Apply sees
	// it on the entry and records it instead of carrying it out.
	Lapsed bool `json:"-"`
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
	// KindAckClear: the alarm ID's acknowledged occurrence returned to
	// normal on the leader, so the ack the log holds for it no longer
	// applies: a takeover must not carry it to a new occurrence (#710 PR
	// 3b-2c). Proposed by the leader, retried until committed.
	KindAckClear = "ack-clear"

	// Substitute values (#707): see Entry.
	KindSubstitute   = "substitute"
	KindUnsubstitute = "unsubstitute"
	KindSuback       = "suback"

	// KindNoop is a new leader's empty entry of its own term (SyncLeader):
	// committing it commits everything before it, which is how a leader
	// commits entries an earlier term stored but never committed (Raft
	// §5.4.2). Apply never sees it.
	KindNoop = "noop"

	// KindLapse is a new leader's KindNoop that also names, in Refs, the
	// entries it commits that an earlier term stored but never committed
	// and that are too old to carry out now (Options.Lapse): a substitute
	// start whose proposer answered "outcome unknown" must not begin
	// minutes or hours later, when that proposer comes back (#1030
	// review). Every replica applies the named entries as lapsed (Entry.
	// Lapsed), deciding by this entry alone, never by its own clock, so
	// they all agree; and Apply sees the KindLapse itself, so a replica
	// that had already carried out a named entry (its commit was known
	// only outside the majority the new leader heard) ends it.
	KindLapse = "lapse"
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
	// Lapse, when set, picks the entries a new leader names in its
	// KindLapse: those it commits from an earlier term for which
	// Lapse(entry, the leader's now) is true (by age: carrierLocked). Nil:
	// nothing lapses.
	Lapse func(e Entry, now time.Time) bool

	// Node is this replica's member name (a, b, the witness), sent with
	// its replication so a replica that admits by name (Admit) can check
	// it. Optional.
	Node string
	// Admit, when set, decides whether replication may be taken from the
	// member named node at term, in place of the check that it comes from
	// LeaderAddr: a witness knows its pair's controllers by name, from its
	// own votes, not by address (#710 3b-4). A refusal is answered 403.
	Admit func(node string, term uint64) error

	// Mark is the path of this replica's log mark (#710 3b-4): whether it
	// has stored an entry (held) and whether its log is known to hold
	// every committed entry (synced), for Identity. "" turns marking off,
	// and every replica counts, as before (Kubernetes, a bench).
	//
	// With a mark, a replica counts toward a commit or a leader's
	// catch-up only once synced, and a log lost under the held mark (the
	// file missing, empty, or damaged before its last line) is moved
	// aside as File+".lost-<unix>", started empty and unsynced: Lost says
	// so. A replica becomes synced when a synced leader's replication
	// proves its log equal up to that leader's commit index, or, as a
	// leader, when it catches up from a majority that counts.
	Mark string
	// Identity names this replica's incarnation (the witness's, or a
	// controller's state identity). A mark written for another identity
	// is not this replica's: its log is moved aside as
	// File+".previous-<unix>", and it starts unsynced.
	Identity string
	// Pending: the Identity can't be known yet (a controller whose box
	// can't be read): the log opens as Identity says (the mark's, when the
	// caller read it), but counts for nothing until Rebind names the real
	// one.
	Pending bool
	// UnmarkedSynced: a replica with no mark at all starts synced. Only
	// for a caller that knows the log is complete: a new cluster's first
	// start (nothing committed yet), or a log kept from before marks.
	UnmarkedSynced bool
	// Counts, when set, also decides whether a synced peer's answer
	// counts, by the identity it reports: a witness counts only at the
	// incarnation the election counts.
	Counts func(peer, identity string) bool
	// Names, when set, names each peer URL to a person (Vouch's
	// refusals): "controller a", "the witness".
	Names map[string]string

	transport http.RoundTripper // tests: an in-process network
	lim       limits            // tests: smaller chunks and bounds
}

// Mark is a replica's log mark, kept in Options.Mark.
type Mark struct {
	Identity string `json:"identity"`
	Held     bool   `json:"held"`
	Synced   bool   `json:"synced"`
	// Lost is where a log lost under the held mark was kept ("(missing)"
	// when there was no file): kept until the replica is synced again,
	// across restarts, so the alarm it raises lasts as long as the loss.
	Lost string `json:"lost,omitempty"`
	// Term is the highest term this replica has seen, written when it
	// rises, before the request that raised it is answered: a log lost
	// under the mark (which starts empty) still refuses a deposed
	// leader's replication by its term (#1050 review). A new identity
	// starts from 0.
	Term uint64 `json:"term,omitempty"`
	// Vouch is the last time a person vouched for this log (Node.Vouch),
	// and Alone says that vouch's one-shot catch-up is still unused.
	Vouch *Vouch `json:"vouch,omitempty"`
	Alone bool   `json:"alone,omitempty"`
}

// Vouch is a person's word that a replica's log holds every committed
// entry (Node.Vouch): who, when, and the log's last entry then.
type Vouch struct {
	By       string `json:"by"`
	At       int64  `json:"at"` // epoch ms
	LastTerm uint64 `json:"lastTerm"`
	LastSeq  uint64 `json:"lastSeq"`
}

// Chunking: a resend or a catch-up read carries at most maxChunkEntries
// entries, or about maxChunkBytes; a replica refuses a request body over
// maxBody, and a reply over maxResp is not read. resendRounds bounds one
// replicateTo's resends; the next heartbeat goes on from where the peer
// is. A Node holds its own copy (limits), which tests set per node at New:
// never a package variable changed under goroutines still running.
const (
	maxChunkEntries = 512
	maxChunkBytes   = 1 << 20
	maxBody         = int64(8 << 20)
	maxResp         = int64(8 << 20) // at least maxBody: what one replica took, another can read
	resendRounds    = 32
	// maxEntryBytes caps one entry, as JSON, at its proposal: well under
	// the file's line limit (1 MiB, open), a chunk and a body, so an entry
	// once stored can always be read back, sent and fetched.
	maxEntryBytes = 512 << 10
)

// limits are a Node's chunking and size bounds (Options.lim; zero fields
// take the defaults above).
type limits struct {
	chunkEntries, chunkBytes, rounds int
	body, resp                       int64
}

// ErrTooLarge refuses a proposal whose entry is over maxEntryBytes. It was
// never proposed: its outcome is known.
var ErrTooLarge = errors.New("oplog: entry too large")

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
	mark        Mark                // with Options.Mark
	pending     bool                // Options.Pending, until Rebind
	peers       map[string]peerInfo // what each peer last said of itself
}

// peerInfo is what a peer's last answer said of its log.
type peerInfo struct {
	unsynced bool
	identity string
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
	// Unsynced: this replica's log is not known complete (Options.Mark),
	// so it counts for nothing. Lost names where a lost log was kept.
	Unsynced bool   `json:"unsynced,omitempty"`
	Identity string `json:"identity,omitempty"`
	Lost     string `json:"lost,omitempty"`
	// NotSynced: not writable because the log that would sequence a write
	// (this one, leading, or the leader's, by its last answer) is not
	// synced, rather than for want of replicas.
	NotSynced bool `json:"notSynced,omitempty"`
	// Uncounted: not writable because too few copies count, though enough
	// answer: the others are reached but not synced (lost, rebuilt), so
	// the no-majority text ("none answers") would be wrong.
	Uncounted bool `json:"uncounted,omitempty"`
	// Synced lists the peers whose last answer said their log is synced
	// (and counts); Vouch is this replica's last vouch.
	Synced []string `json:"synced,omitempty"`
	Vouch  *Vouch   `json:"vouch,omitempty"`
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
	if o.Mark != "" && o.File == "" {
		return nil, errors.New("oplog: a Mark needs a File")
	}
	for _, d := range []struct {
		v   *int
		def int
	}{{&o.lim.chunkEntries, maxChunkEntries}, {&o.lim.chunkBytes, maxChunkBytes}, {&o.lim.rounds, resendRounds}} {
		if *d.v <= 0 {
			*d.v = d.def
		}
	}
	if o.lim.body <= 0 {
		o.lim.body = maxBody
	}
	if o.lim.resp <= 0 {
		o.lim.resp = maxResp
	}
	n := &Node{o: o, client: &http.Client{Timeout: o.Timeout, Transport: o.transport}, log: o.Log,
		lastContact: map[string]time.Time{}, peers: map[string]peerInfo{}, pending: o.Pending}
	if o.Mark != "" {
		if err := n.openMarked(); err != nil {
			return nil, err
		}
		return n, nil
	}
	if o.File != "" {
		if _, err := n.open(); err != nil {
			return nil, err
		}
	}
	return n, nil
}

// openMarked reads the mark, then the log, and decides whether the log is
// this replica's and whole (Options.Mark).
func (n *Node) openMarked() error {
	m, err := readMark(n.o.Mark)
	write := false
	switch {
	case errors.Is(err, os.ErrNotExist):
		m = Mark{Identity: n.o.Identity, Synced: n.o.UnmarkedSynced}
		// Before the log file can be created (open): a log file with no
		// mark beside it reads as one from before marks, which a caller
		// may count as synced (UnmarkedSynced), so it must never be left
		// by a start that dies, or fails to write the mark, in between.
		if !mutOplog("mark_after_log") {
			if err := writeMark(n.o.Mark, m); err != nil {
				return fmt.Errorf("oplog: the log's mark: %w", err)
			}
		} else {
			write = true
		}
	case err != nil:
		// Unreadable: trust nothing it would say. The log stays (it may
		// well be whole) but counts for nothing until synced.
		n.log.Error("oplog: the log's mark can't be read: this replica counts for nothing until it has synced from the leader", "mark", n.o.Mark, "error", err)
		m, write = Mark{Identity: n.o.Identity, Held: true}, true
	case m.Identity != n.o.Identity:
		// Another incarnation's log: kept aside, never counted as ours.
		if kept, err := n.moveAside(".previous-"); err != nil {
			return err
		} else if kept != "" {
			n.log.Warn("oplog: the log belongs to another identity of this replica; kept aside, starting empty and unsynced", "kept", kept, "was", m.Identity, "now", n.o.Identity)
		}
		m, write = Mark{Identity: n.o.Identity}, true
	}
	_, statErr := os.Stat(n.o.File)
	missing := errors.Is(statErr, os.ErrNotExist)
	damaged, err := n.open()
	if err != nil {
		return err
	}
	if damaged || m.Held && (missing || len(n.entries) == 0) && !mutOplog("log_loss_silent") {
		// Lost under the held mark (or damaged, which loses its tail):
		// this replica acknowledged entries it no longer has. Keep what
		// is there, start empty, count for nothing until a synced leader
		// has given it the log again.
		if n.file != nil {
			n.file.Close()
		}
		n.file, n.entries, n.commit, n.term = nil, nil, 0, 0
		kept := "(missing)"
		if !missing {
			if kept, err = n.moveAside(".lost-"); err != nil {
				return err
			}
		}
		if _, err := n.open(); err != nil {
			return err
		}
		m, write = Mark{Identity: n.o.Identity, Lost: kept, Term: m.Term}, true
		n.log.Error("oplog: this replica's operator-intent log was LOST: it counts for nothing (writes, the majority, a takeover's catch-up) until it has synced from the leader", "file", n.o.File, "kept", kept)
	}
	if len(n.entries) > 0 && !m.Held {
		m.Held, write = true, true // a log from before marks
	}
	if !mutOplog("term_not_persisted") {
		n.term = max(n.term, m.Term)
	}
	n.mark = m
	if write {
		if err := writeMark(n.o.Mark, m); err != nil {
			n.file.Close()
			return fmt.Errorf("oplog: the log's mark: %w", err)
		}
	}
	return nil
}

// Rebind gives a Pending replica its identity, once it can be known: the
// same as the mark's, and the log counts again as its mark says; another,
// and the log is another identity's, kept aside as File+".previous-<unix>",
// and this one starts empty and unsynced. placeholder is the identity the
// caller opened with when it had none to go by: a mark under it is taken
// as this identity's. A replica that is not Pending: nothing to do.
func (n *Node) Rebind(identity, placeholder string) error {
	n.seqMu.Lock()
	defer n.seqMu.Unlock()
	n.mu.Lock()
	defer n.mu.Unlock()
	if !n.pending {
		return nil
	}
	switch n.mark.Identity {
	case identity:
	case placeholder:
		m := n.mark
		m.Identity = identity
		if err := n.setMarkLocked(m); err != nil {
			return fmt.Errorf("oplog: the log's mark: %w", err)
		}
	default:
		if n.file != nil {
			n.file.Close()
		}
		kept, err := n.moveAside(".previous-")
		if err != nil {
			return err
		}
		n.file, n.entries, n.commit, n.term, n.syncedTerm = nil, nil, 0, 0, 0
		if _, err := n.open(); err != nil {
			return err
		}
		if err := n.setMarkLocked(Mark{Identity: identity}); err != nil {
			return fmt.Errorf("oplog: the log's mark: %w", err)
		}
		n.log.Warn("oplog: the log belongs to another identity of this replica; kept aside, starting empty and unsynced", "kept", kept, "was", n.mark.Identity, "now", identity)
	}
	n.pending = false
	return nil
}

// moveAside renames the log file to File+suffix+<unix> ("" when there is
// none). Caller holds mu or is New.
func (n *Node) moveAside(suffix string) (string, error) {
	if _, err := os.Stat(n.o.File); errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	to := fmt.Sprintf("%s%s%d", n.o.File, suffix, n.o.Now().Unix())
	if err := os.Rename(n.o.File, to); err != nil {
		return "", fmt.Errorf("oplog: moving the log aside: %w", err)
	}
	syncDir(filepath.Dir(n.o.File))
	return to, nil
}

func readMark(path string) (Mark, error) {
	var m Mark
	b, err := os.ReadFile(path)
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, fmt.Errorf("%s: %w", path, err)
	}
	return m, nil
}

// writeMark replaces the mark atomically and durably: a temp file beside
// it, synced, renamed over it, the directory synced.
func writeMark(path string, m Mark) error {
	b, _ := json.Marshal(m)
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	syncDir(filepath.Dir(path))
	return nil
}

func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync() // not supported on Windows; NTFS journals the rename
		d.Close()
	}
}

// setMarkLocked records m durably, then holds it. Caller holds mu.
func (n *Node) setMarkLocked(m Mark) error {
	if err := writeMark(n.o.Mark, m); err != nil {
		return err
	}
	n.mark = m
	return nil
}

// syncedLocked: this replica's log is known to hold every committed entry
// (always, without a mark). Caller holds mu.
func (n *Node) syncedLocked() bool { return n.o.Mark == "" || n.mark.Synced && !n.pending }

// Synced reports whether this replica's log is known to hold every
// committed entry (Options.Mark). A replica that isn't counts for nothing,
// and its controller must not lead (#710 3b-4).
func (n *Node) Synced() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.syncedLocked()
}

// CanSync reports whether this replica, synced, could catch up as leader:
// a vouch's one-shot catch-up is pending (Mark.Alone), or another copy
// that counts was heard within the last 10 s (Status's reach). A synced
// replica whose other copies are all lost can't: entries committed while
// it lagged may exist on them, so only a person can decide (#1053).
func (n *Node) CanSync() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if !n.syncedLocked() {
		return false
	}
	if n.o.Mark != "" && n.mark.Alone {
		return true
	}
	now := n.o.Now()
	for _, p := range n.o.Peers {
		if p == n.o.Self || now.Sub(n.lastContact[p]) >= 10*time.Second {
			continue
		}
		if pi, ok := n.peers[p]; ok && n.counts(p, pi.unsynced, pi.identity) {
			return true
		}
	}
	return false
}

// Lost is where a log lost under the held mark was kept ("(missing)" when
// there was no file), from the loss until this replica is synced again,
// across restarts; "" otherwise.
func (n *Node) Lost() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.mark.Lost
}

// becomeSyncedLocked marks this replica synced, once. Caller holds mu.
func (n *Node) becomeSyncedLocked(why string) {
	if n.syncedLocked() {
		return
	}
	m := n.mark
	m.Synced, m.Lost = true, ""
	if err := n.setMarkLocked(m); err != nil {
		n.log.Error("oplog: can't record that the log is synced; it keeps counting for nothing", "error", err)
		return
	}
	n.log.Info("oplog: this replica's log is synced: it counts again", "how", why, "commit", n.commit)
}

// raiseTermLocked records that this replica has seen term t: with a mark,
// durably before it is held (Mark.Term). An error leaves the term as it
// was, and the request that carried t fails. Caller holds mu.
func (n *Node) raiseTermLocked(t uint64) error {
	if t <= n.term {
		return nil
	}
	if n.o.Mark != "" && t > n.mark.Term && !mutOplog("term_not_persisted") {
		m := n.mark
		m.Term = t
		if err := n.setMarkLocked(m); err != nil {
			return fmt.Errorf("oplog: recording term %d in the log's mark: %w", t, err)
		}
	}
	n.term = t
	return nil
}

// usedVouchLocked ends a vouch's one-shot catch-up (Mark.Alone): this log
// has since been caught up with a majority or by a synced leader. Caller
// holds mu.
func (n *Node) usedVouchLocked() {
	if !n.mark.Alone || mutOplog("vouch_alone_kept") {
		return
	}
	m := n.mark
	m.Alone = false
	if err := n.setMarkLocked(m); err != nil {
		n.log.Warn("oplog: can't record that the vouch's catch-up is used", "error", err)
	}
}

// counts reports whether a peer's answer counts toward a commit or a
// catch-up: only a synced one, at an identity Options.Counts accepts.
func (n *Node) counts(peer string, unsynced bool, identity string) bool {
	if unsynced && !mutOplog("unsynced_counted") {
		return false
	}
	return n.o.Counts == nil || n.o.Counts(peer, identity)
}

// notePeer records what a peer said of its log. Caller holds mu.
func (n *Node) notePeerLocked(peer string, unsynced bool, identity string) {
	n.peers[peer] = peerInfo{unsynced: unsynced, identity: identity}
}

// fileLine is one line of the local copy: an entry, or a commit mark.
type fileLine struct {
	E *Entry  `json:"e,omitempty"`
	C *uint64 `json:"c,omitempty"`
}

// open reads the file, dropping a torn final line, and replays the
// committed prefix. damaged: a line before the last could not be read
// (not a torn write, which is only ever the last line); everything from it
// on was dropped. With a mark that is a lost log (openMarked).
func (n *Node) open() (damaged bool, err error) {
	f, err := os.OpenFile(n.o.File, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return false, err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		f.Close()
		return false, err
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
			f.Close()
			return false, fmt.Errorf("oplog: %s: unrecognised line %q", n.o.File, truncateStr(string(line), 80))
		}
	}
	if good < len(data) {
		// A torn write is only ever the last line: each line goes out in
		// one write, synced before it is acknowledged. A bad line with
		// more after it is damage.
		rest := data[good:]
		if i := bytes.IndexByte(rest, '\n'); i >= 0 && len(bytes.TrimSpace(rest[i+1:])) > 0 {
			damaged = true
		}
		if damaged && n.o.Mark != "" {
			// Nothing applied, nothing truncated: openMarked keeps the
			// file whole, aside, and starts empty.
			f.Close()
			n.entries, n.commit, n.term = nil, 0, 0
			return true, nil
		}
		n.log.Warn("oplog: dropping a torn final line", "file", n.o.File, "bytes", len(data)-good, "damaged", damaged)
		if err := f.Truncate(int64(good)); err != nil {
			f.Close()
			return false, err
		}
	}
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		f.Close()
		return false, err
	}
	n.file = f
	n.applyRange(n.entries, n.commit)
	return damaged, nil
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

// errTermNotSynced: this leader has not caught up for its term, so it
// sends nothing (replicateTo) and a follower takes nothing from it
// (CatchUp).
var errTermNotSynced = errors.New("oplog: the leader has not caught up for its term")

// errBehind: a peer took what it was sent but still ends short of this
// leader's log, so it does not hold the entry that was being replicated.
var errBehind = errors.New("oplog: the peer is still behind")
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
	if len(es) > 0 && n.o.Mark != "" && !n.mark.Held {
		// Held before the first entry is stored, so it is durable before
		// any entry is acknowledged: a log lost after that is seen.
		m := n.mark
		m.Held = true
		if err := n.setMarkLocked(m); err != nil {
			return 0, fmt.Errorf("oplog: the log's mark: %w", err)
		}
	}
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
		if err := n.raiseTermLocked(e.Term); err != nil {
			return fresh, err
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
	n.applyRange(n.afterLocked(n.commit), seq)
	n.commit = seq
	c := seq
	if err := n.writeLine(fileLine{C: &c}); err != nil {
		n.log.Warn("oplog: commit mark", "error", err)
	}
}

// applyRange applies the entries of es up to seq, the range being
// committed, in order. An entry a KindLapse in that same range names is
// applied as Lapsed: the look ahead stops at seq, since an uncommitted
// KindLapse may yet be replaced by a newer term's leader.
func (n *Node) applyRange(es []Entry, seq uint64) {
	lapsed := map[uint64]bool{}
	for _, e := range es {
		if e.Seq <= seq && e.Kind == KindLapse && !mutOplog("lapse_ignored") {
			for _, r := range e.Refs {
				lapsed[r] = true
			}
		}
	}
	for _, e := range es {
		if e.Seq > seq {
			break
		}
		e.Lapsed = lapsed[e.Seq] && e.Kind != KindLapse
		n.apply(e)
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
	// Node is the sender's member name (Options.Node), for Admit.
	Node string `json:"node,omitempty"`
	// Synced: the sender is synced and has caught up for this term, so
	// its log holds every committed entry: a follower this request proves
	// equal up to Commit is synced too.
	Synced bool `json:"synced,omitempty"`
}

type replicateResp struct {
	Have   uint64 `json:"have"` // the follower's last seq, so a gap can be filled
	Commit uint64 `json:"commit"`
	// Unsynced and Identity: what the follower says of its log
	// (Options.Mark). Unsynced is omitted when false, so a replica from
	// before marks counts, as it did.
	Unsynced bool   `json:"unsynced,omitempty"`
	Identity string `json:"identity,omitempty"`
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
	// LeaderSynced: the answering replica leads and has caught up for its
	// term, so a follower may take its entries (CatchUp).
	LeaderSynced bool `json:"leaderSynced,omitempty"`
	// More: Entries stops short of LastSeq (a page); ask again after its
	// last entry.
	More     bool   `json:"more,omitempty"`
	Unsynced bool   `json:"unsynced,omitempty"`
	Identity string `json:"identity,omitempty"`
}

// Propose takes an operator event from this instance's client: sequenced
// here when leader, forwarded to the leader otherwise. It returns once the
// entry is committed and applied locally, or ErrReadOnly.
func (n *Node) Propose(ctx context.Context, e Entry) (Entry, error) {
	e.Origin = n.o.Self
	if err := checkSize(e); err != nil {
		return Entry{}, err
	}
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
	// is one of the candidates, when it counts.
	from := n.commit
	last := n.lastLocked()
	own := afterResp{Entries: n.afterLocked(from), Commit: n.commit, LastSeq: last, LastTerm: n.termAtLocked(last)}
	selfCounts := n.syncedLocked() || mutOplog("unsynced_self_counted")
	// A vouched log's first catch-up as leader (Vouch): it waits for every
	// peer, then takes what those that count say, itself included, even
	// when that is itself alone. Once.
	alone := selfCounts && n.mark.Alone
	n.mu.Unlock()
	// The most advanced log is Raft's "up to date", compared on each WHOLE
	// log's last entry: the higher term wins, then the higher seq, so an
	// older term's uncommitted leftovers never outrank a newer term's
	// committed log, even one that ends at the commit index (and so sends
	// no entries). This leader's own log wins a tie.
	better := func(a, b afterResp) bool {
		return a.LastTerm > b.LastTerm || a.LastTerm == b.LastTerm && a.LastSeq > b.LastSeq
	}
	// Only a replica that counts is a candidate, or counts toward the
	// majority: one whose log is not synced (lost, rebuilt, another
	// identity) may lack committed entries, and a majority that counted
	// it might hold them nowhere (#710 3b-4).
	var best afterResp
	bestSelf, answered, commit := false, 0, uint64(0)
	if selfCounts {
		best, bestSelf, answered, commit = own, true, 1, own.Commit
	}
	var errs []string
	var newer error
	type answer struct {
		got afterResp
		err error
	}
	// Every peer at once; decided once a majority that counts answered
	// (any majority meets the commit majority, so its most advanced log
	// holds every committed entry), after a short wait for the rest. A
	// straggler that has seen a newer term is not waited for: this
	// leader's own lease ends it, and its replication is refused by term.
	fanOut(ctx, n, n.others(), func(ctx context.Context, p string) answer {
		got, err := n.fetchAfter(ctx, p, from)
		return answer{got, err}
	}, func(p string, a answer) bool {
		switch {
		case a.err != nil:
			errs = append(errs, p+": "+a.err.Error())
		case a.got.Term > term:
			// A peer has seen a newer term: this leadership is over.
			newer = fmt.Errorf("%w: a peer is at term %d, this leader at %d", ErrReadOnly, a.got.Term, term)
			return true
		case !n.counts(p, a.got.Unsynced, a.got.Identity):
			errs = append(errs, p+": its log is not synced")
		default:
			answered++
			commit = max(commit, a.got.Commit)
			if better(a.got, best) {
				best, bestSelf = a.got, false
			}
		}
		return !alone && answered >= n.majority()
	})
	if newer != nil {
		return newer
	}
	need := n.majority()
	if alone {
		need = 1
	}
	if answered < need {
		why := ""
		if !selfCounts {
			why = "; this replica's own log is not synced"
		}
		return fmt.Errorf("%w: catch-up heard from %d of %d that count (%s)%s", ErrReadOnly, answered, len(n.o.Peers), strings.Join(errs, "; "), why)
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
	n.becomeSyncedLocked("caught up from a majority as leader")
	if alone && answered < n.majority() {
		n.log.Warn("oplog: caught up on a vouched log, without a majority that counts", "answered", answered, "commit", n.commit)
	}
	n.usedVouchLocked()
	return nil
}

// carrierLocked is what a leader of term sequences to commit the entries
// an earlier term stored past its commit index: a KindLapse naming those
// Options.Lapse picks, or a KindNoop.
//
// By age alone (#1032 review): how many replicas hold an earlier term's
// entry proves nothing (Raft §5.4.2: an old-term entry on a majority is
// not committed), and copies an earlier catch-up adopted would count. The
// accepted cost: a committed start whose commit push was lost, caught up
// more than the bound later, lapses too, and a replica that ran it ends it
// loudly. Caller holds mu.
func (n *Node) carrierLocked(term uint64) Entry {
	var refs []uint64
	if n.o.Lapse != nil {
		now := n.o.Now()
		for _, e := range n.afterLocked(n.commit) {
			if e.Term >= term || e.Kind == KindNoop || e.Kind == KindLapse {
				continue
			}
			if n.o.Lapse(e, now) {
				refs = append(refs, e.Seq)
			}
		}
	}
	if len(refs) > 0 {
		return Entry{Kind: KindLapse, Refs: refs, By: "system", Origin: n.o.Self}
	}
	return Entry{Kind: KindNoop, By: "system", Origin: n.o.Self}
}

// Self is this instance's base URL (Options.Self).
func (n *Node) Self() string { return n.o.Self }

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
	n.mu.Lock()
	c := n.carrierLocked(n.currentTerm())
	n.mu.Unlock()
	_, err := n.sequenceLocked(ctx, c)
	return err
}

// TermSynced reports whether this replica leads and has caught up for its
// current term (SyncLeader, or a write's catch-up). Always true
// standalone. A leader that is not writes nothing and replicates nothing
// until it is: run SyncLeader again.
func (n *Node) TermSynced() bool {
	if n.standalone() {
		return true
	}
	if !n.isLeader() {
		return false
	}
	t := n.currentTerm()
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.syncedTerm != 0 && n.syncedTerm == t
}

// ErrVouchRefused: a person may not vouch for this log now (Vouch says
// why, naming the replica).
var ErrVouchRefused = errors.New("oplog: vouch refused")

// Vouch is a person's word that this replica's log holds every committed
// entry, given when a controller is commissioned or recommissioned (#710
// 3b-4b): with only synced replicas counting, a new pair (three empty,
// unsynced logs) could never sync, and nor could a pair whose logs were
// all lost or rebuilt. The log counts as one synced replica from then on,
// and its first catch-up as leader may succeed with no other replica that
// counts (Mark.Alone, leaderCatchUp): it still asks every peer, and takes
// a longer log a counting one holds.
//
// A log that is already synced needs no word: Vouch does nothing and
// returns a zero Vouch. Otherwise it refuses (ErrVouchRefused):
//   - a log that was lost (Lost): recommission the other controller;
//   - when any peer does not answer: it may hold entries this log lacks
//     (a synced witness rebooting). Bring it back, or rebuild it;
//   - when any peer, counted or not, holds a later log (its last term,
//     then its last seq), naming it.
//
// Without a mark (no synced state) it does nothing.
func (n *Node) Vouch(ctx context.Context, by string) (Vouch, error) {
	if n.o.Mark == "" || n.standalone() {
		return Vouch{}, nil
	}
	n.seqMu.Lock()
	defer n.seqMu.Unlock()
	n.mu.Lock()
	if n.syncedLocked() {
		n.mu.Unlock()
		return Vouch{}, nil
	}
	lost, last := n.mark.Lost, n.lastLocked()
	lastTerm := n.termAtLocked(last)
	n.mu.Unlock()
	check := !mutOplog("vouch_unchecked")
	if lost != "" && check {
		return Vouch{}, fmt.Errorf("%w: this controller's operator-intent log was lost (kept at %s): recommission the other controller instead", ErrVouchRefused, lost)
	}
	type answer struct {
		got afterResp
		err error
	}
	var problems []string
	// Every peer, each to its own call timeout: none is skipped.
	fanOut(ctx, n, n.others(), func(ctx context.Context, p string) answer {
		var got afterResp
		err := n.call(ctx, p, fmt.Sprintf("/api/oplog?after=%d&page=1", last), nil, &got)
		return answer{got, err}
	}, func(p string, a answer) bool {
		switch {
		case a.err != nil:
			problems = append(problems, fmt.Sprintf("%s does not answer (%v): it may hold entries this log lacks; bring it back, or rebuild it, then vouch again", n.name(p), a.err))
		case a.got.LastTerm > lastTerm || a.got.LastTerm == lastTerm && a.got.LastSeq > last:
			problems = append(problems, fmt.Sprintf("%s holds a later log (term %d, seq %d; this one ends at term %d, seq %d)", n.name(p), a.got.LastTerm, a.got.LastSeq, lastTerm, last))
		}
		return false
	})
	if len(problems) > 0 && check {
		sort.Strings(problems)
		return Vouch{}, fmt.Errorf("%w: %s", ErrVouchRefused, strings.Join(problems, "; "))
	}
	v := Vouch{By: by, At: n.o.Now().UnixMilli(), LastTerm: lastTerm, LastSeq: last}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.syncedLocked() {
		return Vouch{}, nil // synced meanwhile, by a synced leader
	}
	m := n.mark
	m.Synced, m.Lost, m.Vouch, m.Alone = true, "", &v, true
	if err := n.setMarkLocked(m); err != nil {
		return Vouch{}, fmt.Errorf("oplog: recording the vouch: %w", err)
	}
	n.log.Warn("oplog: a person vouched for this replica's log: it counts as synced", "by", by, "lastTerm", lastTerm, "lastSeq", last)
	return v, nil
}

// name is how a peer is named to a person (Options.Names), else its URL.
func (n *Node) name(peer string) string {
	if nm, ok := n.o.Names[peer]; ok {
		return nm
	}
	return peer
}

// sequence is the leader's path, one proposal at a time: number, store,
// replicate to a majority, commit, apply. A proposal that misses the
// majority stays in the log, unconfirmed (see ErrUnconfirmed).
func (n *Node) sequence(ctx context.Context, e Entry) (Entry, error) {
	if err := checkSize(e); err != nil {
		return Entry{}, err
	}
	n.seqMu.Lock()
	defer n.seqMu.Unlock()
	if !n.isLeader() {
		return Entry{}, fmt.Errorf("%w: no longer the leader", ErrReadOnly)
	}
	// No write is numbered without a synced term: catch-up and
	// replication are different requests, and a write that replicated to
	// a majority after a failed catch-up could take the number of an
	// entry that majority already committed.
	term := n.currentTerm()
	if err := n.leaderCatchUp(ctx, term); err != nil {
		return Entry{}, err
	}
	// The first write of a term commits what an earlier term left: when
	// some of it must lapse, the KindLapse goes first, so it is committed
	// with them (KindLapse).
	n.mu.Lock()
	c := n.carrierLocked(term)
	n.mu.Unlock()
	if c.Kind == KindLapse {
		if _, err := n.sequenceLocked(ctx, c); err != nil {
			return Entry{}, err
		}
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
	stored := 0
	if n.syncedLocked() {
		stored = 1
	}
	n.mu.Unlock()
	if err != nil {
		return Entry{}, err
	}
	if fresh != 1 {
		return Entry{}, fmt.Errorf("oplog: seq %d was not fresh", e.Seq)
	}
	type answer struct {
		resp replicateResp
		err  error
	}
	var errs, holders []string
	// Every peer at once: committed once a majority that counts stored it,
	// after a short wait for the rest, so a silent peer doesn't hold up
	// every write by its call timeout (#710 3b-4). A peer cut off then gets
	// it at its next heartbeat.
	fanOut(ctx, n, n.others(), func(ctx context.Context, p string) answer {
		r, err := n.replicateTo(ctx, p, []Entry{e}, term)
		return answer{r, err}
	}, func(p string, a answer) bool {
		switch {
		case a.err != nil:
			errs = append(errs, p+": "+a.err.Error())
		case a.resp.Have < e.Seq && !mutOplog("behind_counted"):
			errs = append(errs, fmt.Sprintf("%s: answered, but holds only up to %d", p, a.resp.Have))
		case !n.counts(p, a.resp.Unsynced, a.resp.Identity):
			holders = append(holders, p)
			errs = append(errs, p+": stored, but its log is not synced")
		default:
			holders = append(holders, p)
			stored++
		}
		return stored >= n.majority()
	})
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
	// The followers stored the entry under the commit index from before
	// it: tell those that hold it that it is committed before the proposer
	// hears so. A leader that dies after answering would otherwise leave
	// them holding it uncommitted, and a follower that takes over without
	// a majority to catch up with would not apply a write the operator
	// was told was done. Best effort, and only to the replicas that stored
	// it (one that is down would cost every write a second timeout), all
	// at once: one the push misses (the leader dies first, or the push to
	// it fails, only logged) learns it at its next heartbeat (≤ lease/2 in
	// a pair) or write. It runs under seqMu, so a holder that stalls
	// delays every write by up to the call timeout.
	fanOut(ctx, n, holders, func(ctx context.Context, p string) error {
		return n.replicateToErr(ctx, p, nil, term)
	}, func(p string, err error) bool {
		if err != nil {
			n.log.Debug("oplog: commit push", "peer", p, "error", err)
		}
		return false
	})
	return e, nil
}

// replicateTo sends entries to one peer, filling a gap it reports.
//
// Each request carries the commit index as it is when the request is
// built, read under the same lock as its previous entry, so a follower's
// proof of being synced (its log equal up to the commit) is never judged
// against an older one. A new entry is sent before it is committed, so
// it goes out under the commit index from before it.
func (n *Node) replicateTo(ctx context.Context, peer string, es []Entry, term uint64) (replicateResp, error) {
	send := func(es []Entry) (replicateResp, error) {
		n.mu.Lock()
		prev := n.lastLocked() // a heartbeat: the leader's last entry
		if len(es) > 0 {
			prev = es[0].Seq - 1
		}
		pt := n.termAtLocked(prev)
		commit := n.commit
		synced := n.syncedLocked() && n.syncedTerm == term
		termSynced := n.syncedTerm == term
		n.mu.Unlock()
		// Nothing leaves a leader that has not caught up for this term
		// (#1050 review): what it holds past its commit index may be an
		// earlier term's leftover, and a follower would replace with it an
		// entry a later term committed (one whose commit push it missed).
		if !termSynced && !mutOplog("unsynced_leader_writes") {
			return replicateResp{}, errTermNotSynced
		}
		var resp replicateResp
		err := n.call(ctx, peer, "/api/oplog/replicate",
			replicateReq{From: n.o.Self, Node: n.o.Node, Synced: synced, Term: term, Commit: commit, PrevSeq: prev, PrevTerm: pt, Entries: es}, &resp)
		if err == nil {
			n.mu.Lock()
			n.notePeerLocked(peer, resp.Unsynced, resp.Identity)
			n.mu.Unlock()
		}
		return resp, err
	}
	resp, err := send(es)
	// The peer is behind (a gap: Have is its last entry) or its entry
	// before these is not ours (a mismatch: Have is its commit index), or
	// it took a chunk that stops short of our last entry: send what is
	// after Have, a chunk at a time, and again from what the peer answers
	// (a gap's resend can meet a mismatch below it), until it has them.
	// Each step moves Have, to the peer's commit index at worst, where the
	// logs agree; the bound only guards a peer that answers nonsense, and
	// the next heartbeat goes on (a rebuilt replica is filled over several).
	for range n.o.lim.rounds {
		var he *httpError
		var from uint64
		switch {
		case errors.As(err, &he) && he.code == http.StatusConflict && he.resend:
			from = resp.Have
		case err == nil:
			n.mu.Lock()
			last := n.lastLocked()
			n.mu.Unlock()
			if resp.Have >= last {
				return resp, nil
			}
			from = resp.Have
		default:
			return resp, err
		}
		n.mu.Lock()
		missing := n.afterLocked(from)
		n.mu.Unlock()
		if len(missing) == 0 {
			return resp, err
		}
		if !mutOplog("resend_unchunked") {
			missing, _ = n.chunk(missing)
		}
		resp, err = send(missing)
	}
	if err == nil && !mutOplog("behind_counted") {
		// Out of rounds with the peer still short of our last entry
		// (#1050 review): it does not hold what we meant to replicate, so
		// it must not count as having stored it. The next heartbeat goes on.
		n.mu.Lock()
		last := n.lastLocked()
		n.mu.Unlock()
		if resp.Have < last {
			return resp, fmt.Errorf("%w: it has %d of %d", errBehind, resp.Have, last)
		}
	}
	return resp, err
}

// checkSize refuses an entry over maxEntryBytes (ErrTooLarge).
func checkSize(e Entry) error {
	if mutOplog("entry_unbounded") {
		return nil
	}
	if b, _ := json.Marshal(e); len(b) > maxEntryBytes {
		return fmt.Errorf("%w: %d bytes, at most %d", ErrTooLarge, len(b), maxEntryBytes)
	}
	return nil
}

// replicateToErr is replicateTo for a caller that needs only its error.
func (n *Node) replicateToErr(ctx context.Context, peer string, es []Entry, term uint64) error {
	_, err := n.replicateTo(ctx, peer, es, term)
	return err
}

// chunk is the head of es one request carries: at most the limits' chunk
// entries, and about their chunk bytes (always at least one entry). more:
// es goes on.
func (n *Node) chunk(es []Entry) (head []Entry, more bool) {
	size := 0
	for i, e := range es {
		b, _ := json.Marshal(e)
		size += len(b) + 1
		if i > 0 && (i >= n.o.lim.chunkEntries || size > n.o.lim.chunkBytes) {
			return es[:i], true
		}
	}
	return es, false
}

// others are the peers but this instance.
func (n *Node) others() []string {
	out := make([]string, 0, len(n.o.Peers))
	for _, p := range n.o.Peers {
		if p != n.o.Self {
			out = append(out, p)
		}
	}
	return out
}

// grace is how long a fan-out waits for the remaining peers once enough
// have answered.
func (n *Node) grace() time.Duration { return min(n.o.Timeout/8, 50*time.Millisecond) }

// fanOut runs f against every peer of peers at once and hands each result
// to take, one at a time, in the order they arrive. Once take has said
// enough, it waits at most n.grace() for the rest, then cancels them and
// returns; take may still be called for those that arrive in that time.
func fanOut[T any](ctx context.Context, n *Node, peers []string, f func(context.Context, string) T, take func(string, T) bool) {
	if mutOplog("replicate_sequential") {
		for _, p := range peers {
			take(p, f(ctx, p))
		}
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type result struct {
		p string
		v T
	}
	ch := make(chan result, len(peers))
	for _, p := range peers {
		go func() { ch <- result{p, f(ctx, p)} }()
	}
	var grace <-chan time.Time
	for left := len(peers); left > 0; left-- {
		select {
		case r := <-ch:
			if take(r.p, r.v) && grace == nil {
				t := time.NewTimer(n.grace())
				defer t.Stop()
				grace = t.C
			}
		case <-grace:
			return
		}
	}
}

// fetchAfter reads a peer's log after seq, page by page.
func (n *Node) fetchAfter(ctx context.Context, peer string, after uint64) (afterResp, error) {
	var all afterResp
	for page := 0; ; page++ {
		var got afterResp
		if err := n.call(ctx, peer, fmt.Sprintf("/api/oplog?after=%d&page=1", after), nil, &got); err != nil {
			return afterResp{}, err
		}
		if page == 0 {
			all = got
			all.Entries = append([]Entry(nil), got.Entries...)
		} else {
			if got.Term != all.Term || len(got.Entries) > 0 && got.Entries[0].Seq != after+1 {
				return afterResp{}, fmt.Errorf("oplog: %s's log moved while it was read page by page", peer)
			}
			all.Entries = append(all.Entries, got.Entries...)
			all.Commit, all.LastSeq, all.LastTerm = got.Commit, got.LastSeq, got.LastTerm
			all.Unsynced, all.Identity = got.Unsynced, got.Identity
			all.LeaderSynced = all.LeaderSynced && got.LeaderSynced
		}
		if !got.More || len(got.Entries) == 0 {
			all.More = false
			n.mu.Lock()
			n.notePeerLocked(peer, all.Unsynced, all.Identity)
			n.mu.Unlock()
			return all, nil
		}
		if page >= 1<<14 {
			return afterResp{}, fmt.Errorf("oplog: %s's log did not end after %d pages", peer, page)
		}
		after = got.Entries[len(got.Entries)-1].Seq
	}
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
	term := n.currentTerm()
	n.mu.Lock()
	termSynced := n.syncedTerm == term
	n.mu.Unlock()
	if !termSynced && !mutOplog("unsynced_leader_writes") {
		// Until SyncLeader (or a write) has caught it up for this term,
		// it sends nothing: see replicateTo. Followers keep pulling.
		return
	}
	fanOut(ctx, n, n.others(), func(ctx context.Context, p string) error {
		return n.replicateToErr(ctx, p, nil, term)
	}, func(p string, err error) bool {
		if err != nil {
			n.log.Debug("oplog: heartbeat", "peer", p, "error", err)
		}
		return false
	})
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
	got, err := n.fetchAfter(ctx, leader, n.Commit())
	if err != nil {
		return err
	}
	if !got.LeaderSynced && !mutOplog("unsynced_leader_writes") {
		// Its entries past our commit index may be an earlier term's
		// leftovers that would replace one a later term committed (#1050
		// review). Its heartbeat or a later pull brings them once it has
		// caught up.
		return errTermNotSynced
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
	counted := 0
	var synced []string
	for _, p := range n.o.Peers {
		if p != n.o.Self {
			if pi, ok := n.peers[p]; ok && n.counts(p, pi.unsynced, pi.identity) {
				synced = append(synced, p)
			}
		}
		if p == n.o.Self || now.Sub(n.lastContact[p]) < 10*time.Second {
			reached = append(reached, p)
			pi := n.peers[p]
			switch {
			case p == n.o.Self && n.syncedLocked():
				counted++
			case p != n.o.Self && n.counts(p, pi.unsynced, pi.identity):
				counted++
			}
		}
	}
	leader := n.leaderAddr()
	leaderUnsynced := n.peers[leader].unsynced
	s := Status{Self: n.o.Self, Leader: leader, IsLeader: n.isLeader(), Term: n.term, Peers: n.o.Peers, Reached: reached, LastSeq: n.lastLocked(), Commit: n.commit,
		Unsynced: !n.syncedLocked(), Identity: n.mark.Identity, Lost: n.mark.Lost, Synced: synced, Vouch: n.mark.Vouch}
	n.mu.Unlock()
	switch {
	case n.standalone():
		s.Writable = true
	case s.IsLeader:
		s.Writable = counted >= n.majority() || s.Unsynced && mutOplog("unsynced_log_writes")
		switch {
		case s.Unsynced && !s.Writable:
			s.NotSynced = true
			s.Reason = "leader, but this replica's log is not synced, and too few that count are reachable"
		case !s.Writable:
			s.Uncounted = len(reached) >= n.majority()
			s.Reason = fmt.Sprintf("leader, but only %d of %d instances that count reachable", counted, len(n.o.Peers))
		}
	default:
		s.Writable = s.Leader != "" && contains(reached, s.Leader)
		switch {
		case !s.Writable:
			s.Reason = "not the leader, and the leader is unreachable"
		case leaderUnsynced && !mutOplog("unsynced_log_writes"):
			// The leader's log is not synced (its last answer): it can
			// commit nothing until it is, so a write is refused here,
			// for certain, rather than forwarded to an unknown outcome.
			s.Writable, s.NotSynced = false, true
			s.Reason = "the leader's log is not synced"
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

// Ping records whether each peer answers, and what it says of its log,
// for Status. Run it on a ticker.
func (n *Node) Ping(ctx context.Context) {
	fanOut(ctx, n, n.others(), func(ctx context.Context, p string) bool {
		var st Status
		if n.call(ctx, p, "/api/quorum", nil, &st) != nil {
			return false
		}
		n.mu.Lock()
		n.notePeerLocked(p, st.Unsynced, st.Identity)
		n.mu.Unlock()
		return true
	}, func(string, bool) bool { return false })
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
	data, _ := io.ReadAll(io.LimitReader(resp.Body, n.o.lim.resp))
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
		resp := afterResp{Entries: n.afterLocked(after), Commit: n.commit, Term: n.term, LastSeq: last, LastTerm: n.termAtLocked(last),
			Unsynced: !n.syncedLocked(), Identity: n.mark.Identity}
		resp.LeaderSynced = n.isLeader() && n.syncedTerm != 0 && n.syncedTerm == n.currentTerm()
		n.mu.Unlock()
		// Pages only for a reader that asks (page=1): one from before
		// pages would take the first page for the whole tail.
		if r.URL.Query().Get("page") == "1" && !mutOplog("resend_unchunked") {
			resp.Entries, resp.More = n.chunk(resp.Entries)
		}
		writeJSON(w, http.StatusOK, resp)
	}))
	mux.HandleFunc("POST /api/oplog/propose", auth(func(w http.ResponseWriter, r *http.Request) {
		if !n.isLeader() {
			http.Error(w, "not the leader", http.StatusConflict)
			return
		}
		var e Entry
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, n.o.lim.body)).Decode(&e); err != nil {
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
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, n.o.lim.body)).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// Only the leader the Leadership names may replicate (or, with
		// Admit, the member it admits by name), and never from an older
		// term than one already seen.
		switch {
		case n.o.Admit != nil:
			if mutOplog("admit_skipped") {
				break
			}
			if err := n.o.Admit(req.Node, req.Term); err != nil {
				http.Error(w, fmt.Sprintf("replication from %q at term %d refused: %v", req.Node, req.Term, err), http.StatusForbidden)
				return
			}
		default:
			l := n.leaderAddr()
			if !n.standalone() && l != "" && req.From != l {
				http.Error(w, fmt.Sprintf("replication from %s; the leader is %s", req.From, l), http.StatusForbidden)
				return
			}
			// With a mark, only from a leader this replica's own election
			// names: a replica that has not seen the current term (a new
			// identity starts at term 0) would otherwise take a deposed
			// leader's word that its log is synced.
			if !n.standalone() && l == "" && n.o.Mark != "" && !mutOplog("unnamed_leader_admitted") {
				http.Error(w, fmt.Sprintf("replication from %s, but this replica's election names no leader", req.From), http.StatusForbidden)
				return
			}
		}
		n.mu.Lock()
		if req.Term < n.term {
			have := n.lastLocked()
			n.mu.Unlock()
			writeJSON(w, http.StatusConflict, map[string]any{"error": "stale term", "have": have})
			return
		}
		if err := n.raiseTermLocked(req.Term); err != nil {
			n.mu.Unlock()
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
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
			proved := req.PrevSeq + uint64(len(req.Entries))
			n.commitTo(min(req.Commit, proved))
			// A synced leader's log holds every committed entry, and this
			// request proved ours equal to it up to its commit index:
			// ours holds them too.
			if (req.Synced || mutOplog("synced_by_unsynced_leader")) && (proved >= req.Commit || mutOplog("synced_before_commit")) {
				n.becomeSyncedLocked("replicated from a synced leader")
				n.usedVouchLocked()
			}
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
		n.mu.Lock()
		unsynced, identity := !n.syncedLocked(), n.mark.Identity
		n.mu.Unlock()
		writeJSON(w, http.StatusOK, replicateResp{Have: have, Commit: commit, Unsynced: unsynced, Identity: identity})
	}))
	return mux
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
