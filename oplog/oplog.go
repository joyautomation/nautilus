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
// (synced per batch of entries; a torn final line from a crash is dropped), which
// survives a process restart but not a reschedule. There is no durable
// copy on the cluster being watched, on purpose: the log must not depend
// on the platform it reports on. Two instances of three cannot form a
// majority, so a split leaves exactly one side writable, and the other
// says so (Status). The log is not compacted (#1455): about 225 bytes per
// entry on disk and 0.5 MB per 1,000 in memory, at a busy plant's rate
// about 2,400 entries a day.
//
// Replica-to-replica calls require a shared token (constant-time compared)
// whenever there is more than one peer, and replication is accepted only
// from the address the Leadership names as leader.
package oplog

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
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
	// Principal is who proposed the entry, in the audit journal's form
	// ("user:alice", "token:historian"), when a person or a token did; ""
	// for the system's own entries. By stays the name an operator sees.
	// It is a label for the journal, carried as By is: never a credential,
	// never an authorization decision at apply. A replica on an older
	// binary drops it when it re-marshals the entry; readers fall back to
	// By (#708 PR 8c).
	Principal string `json:"principal,omitempty"`

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

	// Occs names alarm occurrences (#715): on an ack, the occurrence each
	// acknowledged alarm was on when the ack was checked (IDs stays for a
	// replica that predates them); on an ack-clear, the occurrence whose
	// ack ended; on occurrence-start and occurrence-end, the occurrences
	// begun (or whose wait moved) and ended. omitempty: a log written
	// before them loads unchanged.
	Occs []Occ `json:"occs,omitempty"`

	// Link is, on an ack made from a notification's link (#715), the
	// link's ID (alarm/acklink Claim.ID: not a secret, never the token).
	// A link is spent once: every replica records it when the ack is
	// applied, and a later ack naming it is not applied, so a link used on
	// one controller is refused on the other after a takeover. omitempty:
	// a log written before it loads unchanged; a replica on an older
	// binary drops it when it re-marshals the entry, and then cannot
	// refuse that link's second use (the occurrence it names is already
	// acknowledged, so the replay acknowledges nothing).
	Link string `json:"link,omitempty"`

	// Lapsed is set by the node, never stored: this entry is one a
	// KindLapse entry committed with it names (see KindLapse). Apply sees
	// it on the entry and records it instead of carrying it out.
	Lapsed bool `json:"-"`
}

// Occ is one alarm's occurrence (#715): the alarm, the occurrence's ID,
// its activation and when it began waiting on a person (0: it is not).
type Occ struct {
	ID       string `json:"id"`
	Occ      string `json:"occ"`
	ActiveMs int64  `json:"activeMs,omitempty"`
	WaitMs   int64  `json:"waitMs,omitempty"`
}

// NoteEntry is one revision of an operator note (#720). It is data the log
// carries for the notes package (docs/design/context-map.md §2): the log
// decides nothing about it. The entry carries it as JSON in Value, the one
// field every build keeps and replicates verbatim, so a replica from before
// notes stores and sends the body intact (a new field would be dropped by
// its typed re-marshal) and indexes the notes when it is upgraded and
// replays. ID and Rev name the revision; the leader's entry TS is its time;
// the author is the entry's Principal and By.
type NoteEntry struct {
	ID  string `json:"id"`  // the note, chosen by the proposer (random)
	Rev int    `json:"rev"` // 1 is the note itself, 2... its revisions
	// Target is what the note is about: Kind is asset, tag, alarm, area or
	// log; Occurrence and ActiveMs link an alarm note to one occurrence.
	TargetKind string `json:"tk,omitempty"`
	TargetID   string `json:"ti,omitempty"`
	Occurrence string `json:"occ,omitempty"`
	ActiveMs   int64  `json:"act,omitempty"`
	// Kind is "note" (the default) or "handover", on revision 1.
	Kind string `json:"nk,omitempty"`
	Text string `json:"text"`
	// Re names the note this one is an addendum to (revision 1 only).
	Re string `json:"re,omitempty"`
	// Retract: this revision withdraws the note, Reason says why. The text
	// of every earlier revision stays.
	Retract bool   `json:"retract,omitempty"`
	Reason  string `json:"reason,omitempty"`
	// Override: a retraction by someone other than the author, which the
	// proposer checked (a label, like Principal: apply decides nothing
	// from it).
	Override bool `json:"override,omitempty"`
}

// NoteEntryOf is the log entry that carries n, by the person named by.
func NoteEntryOf(by string, n NoteEntry) Entry {
	b, _ := json.Marshal(n)
	return Entry{Kind: KindNote, By: by, Value: b}
}

// NoteBody is the note a KindNote entry carries. ok is false for another
// kind and for an entry whose body is missing or is not a note.
func (e Entry) NoteBody() (n NoteEntry, ok bool) {
	if e.Kind != KindNote || len(e.Value) == 0 {
		return NoteEntry{}, false
	}
	if json.Unmarshal(e.Value, &n) != nil || n.ID == "" || n.Rev < 1 {
		return NoteEntry{}, false
	}
	return n, true
}

// CheckoutEntryOf is the log entry that carries one I/O checkout entry (#719):
// a run begun, a result, a force and its end, an abandon, a seal. The body is
// the checkout store's own entry JSON, in Value (the field every build keeps),
// and the store, not the log, decides at apply what it means: the same
// decision on every replica, from the same sequence.
func CheckoutEntryOf(by string, body []byte) Entry {
	return Entry{Kind: KindCheckout, By: by, Value: json.RawMessage(body)}
}

// CheckoutBody is the checkout entry a KindCheckout entry carries; ok is false
// for another kind and for an entry with no body.
func (e Entry) CheckoutBody() (body []byte, ok bool) {
	if e.Kind != KindCheckout || len(e.Value) == 0 {
		return nil, false
	}
	return e.Value, true
}

// SessionEntry is one change to a browser sign-in (#716): a session opened
// on the leader, or ended. It is data the log carries for the server's
// session store, which decides what it means; like a note it travels in
// Value, the one field every build keeps. It holds the session id's
// SHA-256 (Hash), never the id: a log reader cannot sign in with it. The
// elevation time is not carried: after a takeover a person re-enters the
// password for an elevated action.
type SessionEntry struct {
	Op      string   `json:"op"`   // "open" or "end"
	Hash    string   `json:"hash"` // hex SHA-256 of the session id
	User    string   `json:"user,omitempty"`
	OIDC    bool     `json:"oidc,omitempty"`
	Issuer  string   `json:"iss,omitempty"`
	Sub     string   `json:"sub,omitempty"`
	Groups  []string `json:"groups,omitempty"`
	Created int64    `json:"created,omitempty"` // epoch ms
	Expires int64    `json:"expires"`           // epoch ms: after it the entry means nothing
	Cred    string   `json:"cred,omitempty"`    // fingerprint of the password it was proved against
}

// Session entry ops.
const (
	SessionOpen = "open"
	SessionEnd  = "end"
)

// SessionEntryOf is the log entry that carries s, proposed for the person or
// system named by.
func SessionEntryOf(by string, s SessionEntry) Entry {
	b, _ := json.Marshal(s)
	return Entry{Kind: KindSession, By: by, Value: b}
}

// SessionBody is the session change a KindSession entry carries; ok is
// false for another kind and for a body that is missing or unreadable.
func (e Entry) SessionBody() (s SessionEntry, ok bool) {
	if e.Kind != KindSession || len(e.Value) == 0 {
		return SessionEntry{}, false
	}
	if json.Unmarshal(e.Value, &s) != nil || s.Hash == "" || (s.Op != SessionOpen && s.Op != SessionEnd) {
		return SessionEntry{}, false
	}
	return s, true
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
	// KindOccurrenceStart and KindOccurrenceEnd keep, for a takeover, the
	// occurrences of the alarms an escalation policy covers (#715): a new
	// leader continues an occurrence the field re-asserts, so escalation
	// goes on at its level rather than starting again at level 1. Proposed
	// by the leader in batches, once per log tick; never lapsed (they
	// describe the leader's engine, not an operator's wish). A replica
	// that predates them ignores them.
	KindOccurrenceStart = "occurrence-start"
	KindOccurrenceEnd   = "occurrence-end"

	// KindAlarmDetect keeps, for a takeover, the alarm floods and
	// chattering episodes the leader's engine is in (#1249): Value is a
	// versioned JSON body (server's detectEntry) naming the episodes
	// begun and ended since the last one. Proposed by the leader when an
	// episode begins or ends, at most one per log tick, never as its
	// numbers move; never lapsed. A replica that predates it ignores it,
	// and its takeover starts with no episode.
	KindAlarmDetect = "alarm-detect"

	// KindOutOfService takes a piece of equipment's alarms out of service
	// until Until (#715 PR 5): ID is the equipment, Reason why, By who.
	// Lapses as a shelve does: late, it would hide alarms nobody meant
	// to. KindInService returns it to service (ID) and never lapses. A
	// replica that predates them ignores them, and its takeover shows
	// the alarms (the safe side). Only fields every build keeps are used.
	KindOutOfService = "out-of-service"
	KindInService    = "in-service"

	// Substitute values (#707): see Entry.
	KindSubstitute   = "substitute"
	KindUnsubstitute = "unsubstitute"
	KindSuback       = "suback"

	// KindNote is an operator note's revision (#720): see NoteEntry. The
	// log is never compacted, so a copy that was wiped is brought every
	// note back by catch-up, as it is every acknowledgement.
	KindNote = "note"

	// KindCheckout is one entry of an I/O checkout run (#719 slice 5): see
	// CheckoutEntryOf. A build from before it keeps the entry whole (the body
	// is in Value) and applies it when it is upgraded and replays.
	KindCheckout = "checkout"

	// KindSession is a browser session opening or ending on the leader
	// (#716): see SessionEntry. It lets a session survive a takeover, and
	// an end (a sign-out, a revoked password) reach the other controller
	// before it can lead.
	KindSession = "session"

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
	// only outside the majority the new leader heard) ends it. A lapse
	// counts only for entries committed in the same range as it, so every
	// replica moves its commit index only to one some leader committed at
	// (#1466): the lapse's leader, and every later one, commits it and its
	// entries together. (An earlier term's commit past a named entry,
	// known only outside the new leader's majority, is the case above.)
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
	// TLS, when set, is the pair's peer TLS client side (#710): every call
	// to a peer then runs https, though Peers still name them as given. A
	// peer's URL is its identity in the log's files (an operator's
	// acceptance names peers by it), so peer TLS changes the wire, never
	// the names. It runs the handshake over raw, dialed to addr (a peer
	// URL's host:port), and returns the connection once the other end is
	// the member at that address. Nil: as the URLs say.
	TLS func(ctx context.Context, raw net.Conn, addr string) (net.Conn, error)
	// Sender, when set, binds a replication request to its connection
	// (#710 per-box keys): it reports whether r, which names the member
	// node (Options.Node) at the URL from, comes from that member by its
	// peer TLS certificate. A refusal is answered 403.
	Sender func(r *http.Request, node, from string) bool
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

	// OnSuperseded, when set, is called (in its own goroutine) when this
	// copy records a higher log history it waits for a restart to take
	// (Mark.SupersedeTo): what the runtime applied of the old history must
	// stop at once, leading or not.
	OnSuperseded func(to Lineage)

	// OnEmptied, when set, is called when this process empties its own log
	// (Rebind finds the log belongs to another box and moves it aside),
	// under the node's lock: what was applied from it must be forgotten
	// in step, before the next entry is applied. It must not call the node.
	OnEmptied func()

	// ResetInPlace: a replica that hears a higher log history moves its
	// log aside and starts empty at once, in this process (the witness's
	// copy: no runtime applies it). Without it (a controller's) the move
	// waits for the next start (Mark.SupersedeTo).
	ResetInPlace bool

	transport     http.RoundTripper    // tests: an in-process network
	afterVouchAsk func()               // tests: Vouch, between its asks and its mark
	lim           limits               // tests: smaller chunks and bounds
	markWrite     func(Mark) error     // tests: fails a mark write
	fileSync      func(*os.File) error // tests: observes or fails a log file sync
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
	// SetAside is where another identity's log that held entries was
	// kept when this replica started over it (a box changed, a copy
	// rebuilt in place): kept until the replica is synced again. Its
	// entries may be ones the pair committed, so a new pair's plain vouch
	// refuses beside it (#1029 3c-3c-2b, the #1207 review).
	SetAside string `json:"setAside,omitempty"`
	// Term is the highest term this replica has seen, written when it
	// rises, before the request that raised it is answered: a log lost
	// under the mark (which starts empty) still refuses a deposed
	// leader's replication by its term (#1050 review). A new identity
	// starts from 0.
	Term uint64 `json:"term,omitempty"`
	// Vouch is the last time a person vouched for this log (Node.Vouch),
	// and Alone says that vouch's one-shot catch-up is still unused, in
	// this boot: a start drops Alone and Accept (naut-lead, #1177).
	Vouch *Vouch `json:"vouch,omitempty"`
	Alone bool   `json:"alone,omitempty"`
	// Accept is what the person accepted to lose at that vouch (Picture,
	// VouchAccept), per peer: the one-shot catch-up overrides a peer only
	// within it. Spent with Alone, and dropped when the log syncs by the
	// normal path (#1029 3c-3c-2a).
	Accept *Accept `json:"accept,omitempty"`

	// Lineage is the log's history (#1029 3c-3c-2a): 0 until a vouched
	// catch-up overrides a copy (a silent one, a later one that does not
	// count, a forked one), which starts history+1 under a new random id.
	// A copy at a lower history does not count; one that hears a higher
	// history is superseded: its log moves aside (File+".superseded-<unix>")
	// and it syncs from scratch. The same number under another id is a
	// fork: neither counts the other, and a person chooses (Vouch). It
	// survives a lost log, as Term does.
	Lineage
	// SupersedeTo (SupersedeID, SupersedeBy): a higher history was heard,
	// and this copy (a controller's) moves its log aside at its next start,
	// taking it. It counts for nothing and takes nothing until then: the
	// runtime holds what the old history applied.
	SupersedeTo uint64 `json:"supersedeTo,omitempty"`
	SupersedeID uint64 `json:"supersedeId,omitempty,string"`
	SupersedeBy string `json:"supersedeBy,omitempty"`
	// Superseded is where the last superseded log was kept: one, the
	// newest.
	Superseded string `json:"superseded,omitempty"`
	// DropRetained: a superseded log was moved aside, and what the runtime
	// retained of its entries (substitutes, by the old history's seqs) is
	// not yet dropped (RetainedDropped): set in the same write, so a crash
	// in between drops them at the next start.
	DropRetained bool `json:"dropRetained,omitempty"`
}

// Lineage is a log history (Mark.Lineage): its number, the random id it
// was started under (0 for history 0) and who accepted the loss that
// started it. Carried by every answer that says what a log holds.
type Lineage struct {
	History   uint64 `json:"history,omitempty"`
	HistoryID uint64 `json:"historyId,omitempty,string"`
	HistoryBy string `json:"historyBy,omitempty"`
}

// same: the same history (number and id; who is a label).
func (l Lineage) same(o Lineage) bool { return l.History == o.History && l.HistoryID == o.HistoryID }

// forks: the same number under another id, started by another override.
func (l Lineage) forks(o Lineage) bool { return l.History == o.History && l.HistoryID != o.HistoryID }

// Fork is a copy whose log forked from this one (Status.Forked): the
// same history number, started by another person's override.
type Fork struct {
	Peer    string `json:"peer"`
	History uint64 `json:"history"`
	Ours    string `json:"ours,omitempty"`   // who accepted this copy's
	Theirs  string `json:"theirs,omitempty"` // who accepted the peer's
}

// Accept is a person's acceptance of a loss at a vouch (Mark.Accept):
// who, when, the picture's digest, and per peer (by URL) what it was
// accepted to hold.
type Accept struct {
	By     string                  `json:"by"`
	At     int64                   `json:"at"` // epoch ms
	Digest string                  `json:"digest"`
	Peers  map[string]AcceptedPeer `json:"peers,omitempty"`
}

// AcceptedPeer is what a peer was accepted as: silent (it may be silent,
// or answer anything), forked (its log is another override's), or
// holding a later log that does not count, up to (LastTerm, LastSeq).
type AcceptedPeer struct {
	Silent   bool   `json:"silent,omitempty"`
	Forked   bool   `json:"forked,omitempty"`
	LastTerm uint64 `json:"lastTerm,omitempty"`
	LastSeq  uint64 `json:"lastSeq,omitempty"`
}

// covers reports whether the acceptance covers what peer answered now:
// silent (err), forked, or a later log that does not count at (term, seq).
func (a *Accept) covers(peer string, silent, forked bool, term, seq uint64) bool {
	if a == nil {
		return false
	}
	if mutOplog("accept_loss_blanket") {
		return true
	}
	p, ok := a.Peers[peer]
	switch {
	case !ok:
		return false
	case p.Silent:
		return true
	case forked:
		return p.Forked
	case silent:
		return false
	}
	return term < p.LastTerm || term == p.LastTerm && seq <= p.LastSeq
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
	o Options
	// client sends to a peer. An interface, so code that holds a Node without
	// ever building one (the browser playground's server, which has no peers)
	// does not keep net/http's client, TLS and HTTP/2 linked: only New names
	// the concrete *http.Client.
	client peerClient
	log    *slog.Logger

	// seqMu serializes sequencing on the leader: number, store, replicate,
	// commit, one proposal at a time. That is what makes the numbers dense
	// and the replication arrive in order.
	seqMu sync.Mutex
	// mu guards the state below.
	mu          sync.Mutex
	entries     []stored // the log, in order (see stored)
	commit      uint64   // applied up to here
	term        uint64   // highest term seen
	syncedTerm  uint64   // the term this leader last caught up for
	file        *os.File
	lastContact map[string]time.Time
	// leaderHeard is when a leader's replication last matched here
	// (epoch ms). Atomic: read without mu, so a caller holding a lock
	// that Apply takes (Apply runs under mu) can read it.
	leaderHeard atomic.Int64
	mark        Mark                // with Options.Mark
	pending     bool                // Options.Pending, until Rebind
	peers       map[string]peerInfo // what each peer last said of itself
	// supersededAtStart: this process's start moved a superseded log aside
	// (Mark.SupersedeTo), so a restart for a later one is a person's call.
	supersededAtStart bool
	// stale is RetainedStale's answer, kept with the mark (setMarkMem) so
	// it is read without mu.
	stale atomic.Bool
	// spendOwed: a vouch's one-shot was spent in memory but the mark that
	// records it could not be written (usedVouchLocked retries).
	spendOwed bool
}

// peerInfo is what a peer's last answer said of its log.
type peerInfo struct {
	unsynced bool
	identity string
	lineage  Lineage
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
	// History is this log's history number (Mark.History); SupersedeTo a
	// higher one it heard and takes at its next start, and Superseded
	// where the last superseded log was kept.
	Lineage
	SupersedeTo uint64 `json:"supersedeTo,omitempty"`
	Superseded  string `json:"superseded,omitempty"`
	// Forked: copies whose log forked from this one (same history number,
	// another override): neither counts the other until a person chooses
	// one (naut redundancy vouch).
	Forked []Fork `json:"forked,omitempty"`
}

// ErrReadOnly says this instance cannot commit a write right now.
var ErrReadOnly = errors.New("oplog: read-only: no quorum reachable")

// ErrNoLeader says a proposal was refused before anything was sent: this
// instance is not the leader and knows none to forward to. It wraps
// ErrReadOnly (errors.Is matches both), and unlike a forwarding failure it
// is a definite no: nothing reached any peer.
var ErrNoLeader = fmt.Errorf("%w: no leader known, nothing was sent", ErrReadOnly)

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
	rt := o.transport
	if rt == nil && o.TLS != nil && !mutOplog("peer_tls_log_plain") {
		dial := (&net.Dialer{Timeout: 2 * time.Second, KeepAlive: 15 * time.Second}).DialContext
		wrap := o.TLS
		rt = tlsWire{&http.Transport{Proxy: nil, MaxIdleConnsPerHost: 4, IdleConnTimeout: 90 * time.Second,
			DialContext: dial,
			// Dial and handshake bounded, so a peer that accepts and never
			// speaks holds no connection past them (#1230 review).
			DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				raw, err := dial(ctx, network, addr)
				if err != nil {
					return nil, err
				}
				hctx, cancel := context.WithTimeout(ctx, tlsHandshake)
				defer cancel()
				return wrap(hctx, raw, addr)
			}}}
	}
	n := &Node{o: o, client: &http.Client{Timeout: o.Timeout, Transport: rt}, log: o.Log,
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
			if err := n.writeMarkFile(m); err != nil {
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
		kept, err := n.moveAside(".previous-")
		if err != nil {
			return err
		} else if kept != "" {
			n.log.Warn("oplog: the log belongs to another identity of this replica; kept aside, starting empty and unsynced", "kept", kept, "was", m.Identity, "now", n.o.Identity)
		}
		prev := m
		m, write = Mark{Identity: n.o.Identity}, true
		if fi, err := os.Stat(kept); kept != "" && err == nil && fi.Size() > 0 {
			m.SetAside = kept
		} else if !prev.Synced && !mutOplog("set_aside_forgotten") {
			// A second change before it synced: the first one's entries
			// are still set aside (the #1207 re-check).
			m.SetAside = prev.SetAside
		}
	case m.SupersedeTo > m.History && !mutOplog("superseded_not_reset"):
		// A higher log history was heard (learnHistoryLocked): the log is
		// moved aside BEFORE it is replayed, so nothing of the old history
		// is applied in this process, and this copy starts empty at the
		// new history, unsynced (#1029 3c-3c-2a).
		var err error
		if m, err = n.supersede(m); err != nil {
			return err
		}
		n.supersededAtStart = true
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
		m, write = Mark{Identity: n.o.Identity, Lost: kept, Term: m.Term, Lineage: m.Lineage, Superseded: m.Superseded, DropRetained: m.DropRetained}, true
		n.log.Error("oplog: this replica's operator-intent log was LOST: it counts for nothing (writes, the majority, a takeover's catch-up) until it has synced from the leader", "file", n.o.File, "kept", kept)
	}
	if len(n.entries) > 0 && !m.Held {
		m.Held, write = true, true // a log from before marks
	}
	if (m.Alone || m.Accept != nil) && !mutOplog("accept_survives_reboot") {
		// A vouch's one-shot and what the person accepted hold for the
		// boot they were given in (naut-lead, #1177): the picture they
		// were bound to may not be true after a restart, and a spend that
		// could not be written before a crash must not re-arm them. The
		// copy stays synced (the vouch's word stands); a person vouches
		// again against the picture as it is now.
		n.log.Warn("oplog: a vouch's one-shot catch-up from before this start is dropped: if it is still needed, naut redundancy vouch again")
		m.Alone, m.Accept, write = false, nil, true
	}
	if !mutOplog("term_not_persisted") {
		n.term = max(n.term, m.Term)
	}
	n.setMarkMem(m)
	if write {
		if err := n.writeMarkFile(m); err != nil {
			n.file.Close()
			return fmt.Errorf("oplog: the log's mark: %w", err)
		}
	}
	return nil
}

// supersede moves a superseded log aside (Mark.SupersedeTo) and returns
// the mark that starts the new history: empty, never held, unsynced, at
// SupersedeTo, its term kept. Exactly one superseded log is kept, the
// newest: older ones are removed only once the mark naming the newest is
// durable. The log file must be closed (New, or the caller closed it).
// The returned mark is durable when the error is nil.
func (n *Node) supersede(m Mark) (Mark, error) {
	kept, err := n.moveAside(".superseded-")
	if err != nil {
		return m, err
	}
	if kept == "" {
		// Moved by an earlier start that died before its mark: the newest
		// one there is it.
		kept = newestSuperseded(n.o.File)
	}
	if kept == "" {
		kept = "(empty)"
	}
	nm := Mark{Identity: m.Identity, Term: m.Term, Lineage: Lineage{History: m.SupersedeTo, HistoryID: m.SupersedeID, HistoryBy: m.SupersedeBy}, Superseded: kept,
		DropRetained: !n.o.ResetInPlace}
	if err := n.writeMarkFile(nm); err != nil {
		return m, fmt.Errorf("oplog: the log's mark: %w", err)
	}
	olds, _ := filepath.Glob(n.o.File + ".superseded-*")
	for _, f := range olds {
		if f != kept {
			os.Remove(f)
		}
	}
	n.log.Error("oplog: this copy's operator-intent log was SUPERSEDED by a newer history a person accepted elsewhere: kept aside, starting empty; it syncs from the leader", "kept", kept, "history", nm.History)
	return nm, nil
}

// SupersededInMark is SupersededLog for a stopped controller: the replaced
// history the log mark file names, and who accepted the loss. "" when the
// mark is absent or names none; the file must still exist.
func SupersededInMark(markPath string) (file, by string) {
	m, err := readMark(markPath)
	if err != nil || m.Superseded == "" || m.Superseded == "(empty)" {
		return "", ""
	}
	if _, err := os.Stat(m.Superseded); err != nil {
		return "", m.HistoryBy
	}
	return m.Superseded, m.HistoryBy
}

// NewestSuperseded is the newest File+".superseded-<unix>" there is, "" if
// none: a tool reading a stopped controller's directory finds the history
// that was replaced beside the log.
func NewestSuperseded(file string) string { return newestSuperseded(file) }

// newestSuperseded is the newest File+".superseded-<unix>" there is, "" if
// none.
func newestSuperseded(file string) string {
	fs, _ := filepath.Glob(file + ".superseded-*")
	newest, at := "", int64(-1)
	for _, f := range fs {
		var u int64
		if _, err := fmt.Sscanf(strings.TrimPrefix(f, file+".superseded-"), "%d", &u); err == nil && u >= at {
			newest, at = f, u
		}
	}
	return newest
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
		// Adopted as this identity's, but not as synced: it was opened
		// before the box could be told, so nothing vouches that it is
		// this box's (#1053 review). It syncs again from the leader.
		m := n.mark
		m.Identity = identity
		if !mutOplog("placeholder_keeps_synced") {
			m.Synced, m.Alone, m.Accept = false, false, nil
		}
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
		if f := n.o.OnEmptied; f != nil {
			f()
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
	if err := n.writeMarkFile(m); err != nil {
		return err
	}
	n.setMarkMem(m)
	return nil
}

// writeMarkFile writes this replica's mark (writeMark), through the
// tests' hook.
func (n *Node) writeMarkFile(m Mark) error {
	if n.o.markWrite != nil {
		if err := n.o.markWrite(m); err != nil {
			return err
		}
	}
	return writeMark(n.o.Mark, m)
}

// syncedLocked: this replica's log is known to hold every committed entry
// (always, without a mark). Caller holds mu.
func (n *Node) syncedLocked() bool {
	return n.o.Mark == "" || n.mark.Synced && !n.pending && !n.supersedingLocked()
}

// supersedingLocked: this copy heard a higher log history and moves its
// log aside at its next start (Mark.SupersedeTo): it takes, commits and
// applies nothing until then. Caller holds mu.
func (n *Node) supersedingLocked() bool {
	return n.mark.SupersedeTo > n.mark.History && !mutOplog("superseded_takes")
}

// errSuperseded refuses what a superseded copy would take or do.
var errSuperseded = errors.New("oplog: this copy's log was superseded by a newer history: restart naut on this controller to move it aside and sync")

// learnHistoryLocked takes a higher log history h, heard from a peer
// (#1029 3c-3c-2a): this copy's log is another history's, so it counts
// for nothing from now on. A controller's records SupersedeTo durably and
// moves the log aside at its next start (the runtime holds what the old
// history applied); with ResetInPlace (the witness's) the log moves aside
// now and the copy starts empty at h. Caller holds mu (and seqMu, or is a
// handler that takes nothing more of the request when it is superseded).
func (n *Node) learnHistoryLocked(l Lineage) error {
	h := l.History
	if n.o.Mark != "" && h == n.mark.SupersedeTo && l.HistoryID == n.mark.SupersedeID && n.mark.SupersedeBy == "" && l.HistoryBy != "" {
		// Learnt first from the hello, which carries no name: an
		// authenticated answer says who accepted it (the journal's row).
		m := n.mark
		m.SupersedeBy = l.HistoryBy
		if err := n.setMarkLocked(m); err != nil {
			n.log.Warn("oplog: can't record who accepted the newer history", "error", err)
		}
		return nil
	}
	if n.o.Mark == "" || h <= n.mark.History || h <= n.mark.SupersedeTo {
		return nil
	}
	m := n.mark
	m.SupersedeTo, m.SupersedeID, m.SupersedeBy = h, l.HistoryID, l.HistoryBy
	m.Synced, m.Alone, m.Accept = false, false, nil
	if mutOplog("supersede_not_durable") {
		n.setMarkMem(m)
	} else if err := n.setMarkLocked(m); err != nil {
		return fmt.Errorf("oplog: recording the newer log history %d: %w", h, err)
	}
	n.syncedTerm = 0
	if !n.o.ResetInPlace {
		n.log.Error("oplog: a newer operator-intent log history was heard: this copy counts for nothing; restart naut on this controller to move its log aside and sync", "history", n.mark.History, "heard", h)
		if f := n.o.OnSuperseded; f != nil {
			go f(l)
		}
		return nil
	}
	if n.file != nil {
		n.file.Close()
	}
	n.file, n.entries, n.commit, n.syncedTerm = nil, nil, 0, 0
	nm, err := n.supersede(m)
	if err != nil {
		return err
	}
	n.setMarkMem(nm)
	if _, err := n.open(); err != nil {
		return err
	}
	return nil
}

// LearnHistory takes a higher log history heard from the other controller
// by an authenticated channel (its hello), as learnHistoryLocked: a
// controller's copy is superseded from first contact.
func (n *Node) LearnHistory(l Lineage) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.learnHistoryLocked(l)
}

// History is this copy's log history (Mark.Lineage), and the higher one it
// waits for a restart to take (Mark.SupersedeTo; History 0 when none).
func (n *Node) History() (own, to Lineage) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.supersedingLocked() {
		to = Lineage{History: n.mark.SupersedeTo, HistoryID: n.mark.SupersedeID, HistoryBy: n.mark.SupersedeBy}
	}
	return n.mark.Lineage, to
}

// SupersededLog is the older history this copy's log was replaced by: the
// file it was moved aside to (Mark.Superseded; "" when there is none or it
// is gone) and who accepted the loss (the history's HistoryBy). Read it only
// with ReadCommitted: it is a record, never applied.
func (n *Node) SupersededLog() (file, by string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	file, by = n.mark.Superseded, n.mark.HistoryBy
	if file == "" || file == "(empty)" {
		return "", by
	}
	if _, err := os.Stat(file); err != nil {
		return "", by
	}
	return file, by
}

// RetainedStale: what the runtime retained of this log's entries (its
// substitutes, numbered by the log's seqs) may be another log history's:
// this copy is superseded and waits for its restart, or its restart moved
// a superseded log aside and the caller has not yet dropped them
// (RetainedDropped). A takeover must restore none of them.
func (n *Node) RetainedStale() bool {
	if mutOplog("stale_under_lock") {
		n.mu.Lock()
		defer n.mu.Unlock()
	}
	return n.stale.Load()
}

// setMarkMem holds m as this replica's mark, and the verdicts read
// without mu from it (RetainedStale: the runtime and the alarm log ask it
// under their own locks, which a commit's Apply takes under mu, #1177
// review). Caller holds mu, or is New.
func (n *Node) setMarkMem(m Mark) {
	n.mark = m
	n.stale.Store(n.supersedingLocked() || m.DropRetained)
}

// RetainedDropped records that the caller dropped what the runtime
// retained of a superseded log's entries (Mark.DropRetained).
func (n *Node) RetainedDropped() error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if !n.mark.DropRetained {
		return nil
	}
	m := n.mark
	m.DropRetained = false
	return n.setMarkLocked(m)
}

// SupersededAtStart: this process's start moved a superseded log aside,
// so a self-restart for a later one would loop: a person decides.
func (n *Node) SupersededAtStart() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.supersededAtStart
}

// LeaderHeard is when a leader's replication (an entry or a heartbeat)
// last reached this replica and matched its log; zero if none has in this
// process's life. It says a leader was alive then, not what it held.
// It takes no lock: safe to call from inside Options.Apply's callers'
// own locks.
func (n *Node) LeaderHeard() time.Time {
	if ms := n.leaderHeard.Load(); ms != 0 {
		return time.UnixMilli(ms)
	}
	return time.Time{}
}

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
// that counts was heard within the last `within` (a pair: its lease). A synced
// replica whose other copies are all lost can't: entries committed while
// it lagged may exist on them, so only a person can decide (#1053).
func (n *Node) CanSync(within time.Duration) bool {
	if mutOplog("can_sync_stale") {
		within = 10 * time.Second
	}
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
		if p == n.o.Self || now.Sub(n.lastContact[p]) >= within {
			continue
		}
		if n.countsLocked(p) {
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
	m.Synced, m.Lost, m.SetAside = true, "", ""
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
	if mutOplog("vouch_alone_kept") {
		return
	}
	if !n.mark.Alone && (n.mark.Accept == nil || mutOplog("accept_kept_after_sync")) && !n.spendOwed {
		return
	}
	m := n.mark
	m.Alone = false
	if !mutOplog("accept_kept_after_sync") {
		m.Accept = nil
	}
	if err := n.setMarkLocked(m); err != nil {
		// Fail closed (#1177 CI, naut-lead): spent in this process at once,
		// so no catch-up uses it again here, and the durable write is owed:
		// every later proving replication retries it.
		n.spendOwed = true
		if !mutOplog("spend_failure_keeps_alone") {
			n.setMarkMem(m)
		}
		n.log.Error("oplog: can't record that the vouch's catch-up is used: spent in this process, retried at the next replication", "error", err)
		return
	}
	n.spendOwed = false
}

// counts reports whether a peer's answer counts toward a commit or a
// catch-up: only a synced one, at an identity Options.Counts accepts, and
// at this copy's log history (own): a lower history's log is one a person
// accepted to lose (#1029 3c-3c-2a), a higher one supersedes this copy.
func (n *Node) counts(peer string, unsynced bool, identity string, l, own Lineage) bool {
	if unsynced && !mutOplog("unsynced_counted") {
		return false
	}
	if !l.same(own) && !(l.History < own.History && mutOplog("history_lower_counted")) && !(l.forks(own) && mutOplog("history_fork_counted")) {
		return false
	}
	return n.o.Counts == nil || n.o.Counts(peer, identity)
}

// notePeer records what a peer said of its log. Caller holds mu.
func (n *Node) notePeerLocked(peer string, unsynced bool, identity string, l Lineage) {
	n.peers[peer] = peerInfo{unsynced: unsynced, identity: identity, lineage: l}
}

// countsLocked is counts for a peer's last answer. Caller holds mu.
func (n *Node) countsLocked(peer string) bool {
	pi, ok := n.peers[peer]
	return ok && n.counts(peer, pi.unsynced, pi.identity, pi.lineage, n.mark.Lineage)
}

// fileLine is one line of the local copy: an entry, or a commit mark.
type fileLine struct {
	E *Entry  `json:"e,omitempty"`
	C *uint64 `json:"c,omitempty"`
}

// open reads the file, dropping a torn final line, and replays the
// committed prefix. It streams the file, one line at a time (#1455).
// damaged: a line before the last could not be read (not a torn write,
// which is only ever the last line); everything from it on was dropped.
// With a mark that is a lost log (openMarked).
func (n *Node) open() (damaged bool, err error) {
	f, err := os.OpenFile(n.o.File, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return false, err
	}
	good, size, damaged, err := scanLines(f, func(line []byte) (bool, error) {
		e, c, old, ok := storedLine(line)
		switch {
		case !ok:
			return false, nil // a torn last line: keep what parsed, drop the rest
		case e != nil:
			n.entries = append(n.entries, *e)
			n.term = max(n.term, e.Term)
			if old {
				// The first format wrote one bare Entry per line, every
				// one applied as written: load it as committed.
				n.commit = e.Seq
			}
		case c != nil:
			n.commit = *c
		default:
			return false, fmt.Errorf("oplog: %s: unrecognised line %q", n.o.File, truncateStr(string(line), 80))
		}
		return true, nil
	})
	if err != nil {
		f.Close()
		n.entries, n.commit, n.term = nil, 0, 0
		return false, err
	}
	if good < size {
		// A torn write is only ever at the end: lines go out in one write
		// (appendLines), newline last, synced before any is acknowledged.
		// A bad line with more after it is damage.
		if damaged && n.o.Mark != "" {
			// Nothing applied, nothing truncated: openMarked keeps the
			// file whole, aside, and starts empty.
			f.Close()
			n.entries, n.commit, n.term = nil, 0, 0
			return true, nil
		}
		n.log.Warn("oplog: dropping a torn final line", "file", n.o.File, "bytes", size-good, "damaged", damaged)
		if err := f.Truncate(good); err != nil {
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

// ReadCommitted reads a log file without opening a node: the entries it
// holds that are committed, in order. A tool that reads a stopped
// controller's log (naut notes export) uses it. A torn final line is
// ignored; a file that does not parse before its end is an error. It
// streams the file (#1455); what it returns is the committed log.
func ReadCommitted(path string) ([]Entry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var es []Entry
	var commit uint64
	var bad string
	_, _, damaged, err := scanLines(f, func(line []byte) (bool, error) {
		var l fileLine
		if err := json.Unmarshal(line, &l); err != nil {
			bad = truncateStr(string(line), 80)
			return false, nil
		}
		switch {
		case l.E != nil:
			es = append(es, *l.E)
		case l.C != nil:
			commit = *l.C
		default:
			var e Entry
			if err := json.Unmarshal(line, &e); err == nil && e.Seq > 0 {
				es = append(es, e)
				commit = e.Seq
			}
		}
		return true, nil
	})
	if err != nil {
		return nil, err
	}
	if damaged {
		if bad == "" {
			bad = "(a line over 1 MiB)"
		}
		return nil, fmt.Errorf("oplog: %s: unrecognised line %q", path, bad)
	}
	out := es[:0]
	for _, e := range es {
		if e.Seq <= commit {
			out = append(out, e)
		}
	}
	return out, nil
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

// entriesForTest is every stored entry, decoded. Tests only.
func (n *Node) entriesForTest() []Entry {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.afterLocked(0)
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
	return decodeAll(n.entries[n.indexAfterLocked(seq):])
}

// indexAfterLocked is the index of the first entry after seq. Caller
// holds mu.
func (n *Node) indexAfterLocked(seq uint64) int {
	return sort.Search(len(n.entries), func(i int) bool { return n.entries[i].Seq > seq })
}

// decodeAll decodes stored entries.
func decodeAll(ss []stored) []Entry {
	out := make([]Entry, len(ss))
	for i, s := range ss {
		out[i] = s.entry()
	}
	return out
}

// writeLine appends one line to the file and syncs it. Caller holds mu.
func (n *Node) writeLine(l fileLine) error {
	return n.appendLines(appendLine(nil, l))
}

// appendLine adds l to buf as one line of the file.
func appendLine(buf []byte, l fileLine) []byte {
	b, _ := json.Marshal(l)
	return append(append(buf, b...), '\n')
}

// appendLines appends whole lines to the file in one write and syncs
// once: a crash leaves the lines before it and, at most, the last one cut
// short (a torn write, which open drops). Nothing they hold counts as
// stored until it returns. A failed write or sync cuts the file back to
// where it was, so a line that may not be on disk is not on it later
// either; if even that fails the file is closed, and every later write
// fails, so nothing more is stored or acknowledged here. Caller holds mu.
func (n *Node) appendLines(buf []byte) error {
	if n.file == nil || len(buf) == 0 {
		return nil
	}
	end, err := n.file.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}
	_, err = n.file.Write(buf)
	switch {
	case err != nil || mutOplog("batch_not_synced"):
	default:
		if n.o.fileSync != nil {
			err = n.o.fileSync(n.file)
		} else {
			err = n.file.Sync()
		}
	}
	if err != nil {
		if terr := n.file.Truncate(end); terr != nil {
			n.file.Close()
			return errors.Join(err, terr)
		}
	}
	return err
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
	var line []byte
	for _, s := range n.entries {
		line = s.line(line[:0])
		if _, err := w.Write(line); err != nil {
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
	// Not O_APPEND: appendLines seeks to the end before each write, and on
	// Windows an append-only handle cannot truncate, so a failed write
	// could not be cut back (#1455).
	f, err := os.OpenFile(n.o.File, os.O_RDWR, 0o600)
	if err != nil {
		// n.file stays the closed handle, so every later write fails
		// loudly rather than being skipped as if there were no file.
		if renameErr == nil {
			err = fmt.Errorf("%w: %w", errRewritten, err) // the new file is in place
		}
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

// errRewritten: rewrite put the new file in place, then could not reopen
// it. The file holds what memory does; nothing more can be written.
var errRewritten = errors.New("oplog: the log was rewritten but could not be reopened")

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
func (n *Node) acceptLocked(es []Entry) (fresh int, err error) {
	if len(es) > 0 && n.o.Mark != "" && !n.mark.Held {
		// Held before the first entry is stored, so it is durable before
		// any entry is acknowledged: a log lost after that is seen.
		m := n.mark
		m.Held = true
		if err := n.setMarkLocked(m); err != nil {
			return 0, fmt.Errorf("oplog: the log's mark: %w", err)
		}
	}
	// The new lines go out together, in one write and one sync, on every
	// way out (#1455: one fsync per batch, not per entry): an entry taken
	// into memory is counted by the next answer that carries this log's
	// last seq, so it is on disk before this returns, error or not.
	// If they can't be written and synced, the entries they hold leave
	// memory too: an answer must never count what the disk may not hold.
	var buf []byte
	durable := len(n.entries)
	defer func() {
		if ferr := n.appendLines(buf); ferr != nil {
			if !mutOplog("batch_kept_on_sync_failure") {
				n.entries = n.entries[:durable]
			}
			err = errors.Join(err, ferr)
		}
	}()
	for _, e := range es {
		last := n.lastLocked()
		switch {
		case e.Seq > last+1:
			return fresh, fmt.Errorf("%w: have %d, got %d", errGap, last, e.Seq)
		case e.Seq == last+1:
			s := storedOf(e)
			n.entries = append(n.entries, s)
			buf = s.line(buf)
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
			// Replace the uncommitted suffix. The lines so far go to the
			// file first, as each did before the batch, so a rewrite that
			// fails leaves the file as it would have been.
			if err := n.appendLines(buf); err != nil {
				buf = nil
				n.entries = n.entries[:durable]
				return fresh, err
			}
			buf = nil
			// A new slice (capacity i), so the old one is whole if the
			// rewrite fails: then the file still holds the old suffix, and
			// so must memory (Fable, #1455 review).
			before := n.entries
			n.entries = append(n.entries[:i:i], storedOf(e))
			if err := n.rewrite(); err != nil {
				if !errors.Is(err, errRewritten) && !mutOplog("rewrite_failure_kept") {
					n.entries = before
				}
				return fresh, err
			}
			durable = len(n.entries)
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
	n.applyRange(n.entries[n.indexAfterLocked(n.commit):], seq)
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
func (n *Node) applyRange(es []stored, seq uint64) {
	// Two passes over the stored range, each decoding one entry at a time
	// (#1455): at open the range is the whole log.
	lapsed := map[uint64]bool{}
	for _, s := range es {
		if s.Seq > seq {
			break
		}
		if !s.mayLapse() {
			continue
		}
		if e := s.entry(); e.Kind == KindLapse && !mutOplog("lapse_ignored") {
			for _, r := range e.Refs {
				lapsed[r] = true
			}
		}
	}
	for _, s := range es {
		if s.Seq > seq {
			break
		}
		e := s.entry()
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
	// Lineage is the sender's log history (Mark.Lineage): a follower at a
	// higher one refuses it (409 "history"), at a forked one too (409
	// "forked"); one at a lower one is superseded (#1029 3c-3c-2a).
	// Omitted (0) by a build from before.
	Lineage
}

type replicateResp struct {
	Have   uint64 `json:"have"` // the follower's last seq, so a gap can be filled
	Commit uint64 `json:"commit"`
	// Unsynced and Identity: what the follower says of its log
	// (Options.Mark). Unsynced is omitted when false, so a replica from
	// before marks counts, as it did.
	Unsynced bool   `json:"unsynced,omitempty"`
	Identity string `json:"identity,omitempty"`
	// Lineage is the follower's log history; on a 409 "history",
	// "forked" or "superseded" it says which.
	Lineage
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
	// Lineage is the answering log's history (Mark.Lineage), Lost whether
	// it was lost and not synced since (#1029 3c-3c-2a).
	Lineage
	Lost bool `json:"lost,omitempty"`
	// SetAside: it started over another identity's log that held entries
	// and has not synced since (Mark.SetAside).
	SetAside bool `json:"setAside,omitempty"`
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
		return Entry{}, ErrNoLeader
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
	if n.supersedingLocked() {
		n.mu.Unlock()
		return fmt.Errorf("%w: %w", ErrReadOnly, errSuperseded)
	}
	// From the COMMIT index, not the last entry: an uncommitted suffix of
	// this leader's own may be stale (stored, never replicated, and since
	// overtaken by a committed entry at the same seq), and only answers
	// from its commit on carry the entries that replace it. Its own suffix
	// is one of the candidates, when it counts.
	from := n.commit
	last := n.lastLocked()
	own := afterResp{Commit: n.commit, LastSeq: last, LastTerm: n.termAtLocked(last)}
	selfCounts := n.syncedLocked() || mutOplog("unsynced_self_counted")
	// A vouched log's first catch-up as leader (Vouch): it waits for every
	// peer, and only when EVERY peer answered, takes the most up-to-date
	// log among itself and those that count, even when that is itself
	// alone. Once. A silent peer may hold a write a majority committed
	// without this log (#1053 review: two vouched controllers, one silent
	// witness): then it fails, and the vouch is kept for when it answers,
	// unless the person accepted that peer's loss (Mark.Accept).
	alone := selfCounts && n.mark.Alone
	accept := n.mark.Accept
	hist := n.mark.Lineage
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
	var bestPeer string
	bestSelf, answered, commit := false, 0, uint64(0)
	if selfCounts {
		best, bestSelf = own, true
		answered, commit = 1, own.Commit
	}
	var errs []string
	var newer error
	var silent []string
	type ahead struct {
		p   string
		got afterResp
	}
	var uncounted []ahead // answers that don't count, at this history
	var forked []string   // copies whose log forked from this one
	type answer struct {
		got afterResp
		err error
	}
	// Every peer at once; decided once a majority that counts answered
	// (any majority meets the commit majority, so its most advanced log
	// holds every committed entry), after a short wait for the rest. A
	// straggler that has seen a newer term is not waited for: this
	// leader's own lease ends it, and its replication is refused by term.
	// Each peer's first page decides: every page names its whole log (its
	// last entry, commit, term, history). Only the chosen log's further
	// pages are read, after the choice (#1455).
	fanOut(ctx, n, n.others(), func(ctx context.Context, p string) answer {
		got, err := n.fetchPage(ctx, p, from)
		return answer{got, err}
	}, func(p string, a answer) bool {
		switch {
		case a.err != nil:
			errs = append(errs, p+": "+a.err.Error())
			silent = append(silent, p)
		case a.got.History > hist.History:
			// A newer log history (a person accepted a loss elsewhere):
			// this copy's log is superseded (fetchAfter has learnt it).
			newer = fmt.Errorf("%w: %s holds a newer log history (%d; this copy %d): %w", ErrReadOnly, n.name(p), a.got.History, hist.History, errSuperseded)
			return true
		case a.got.Term > term:
			// A peer has seen a newer term: this leadership is over.
			newer = fmt.Errorf("%w: a peer is at term %d, this leader at %d", ErrReadOnly, a.got.Term, term)
			return true
		case a.got.History < hist.History && !mutOplog("history_lower_counted"):
			// An older history's copy: its loss was accepted when this one
			// started; it neither counts nor holds anything to wait for.
			errs = append(errs, p+": its log is an older history's (superseded)")
		case a.got.Lineage.forks(hist) && !mutOplog("history_fork_counted"):
			// Another override's log at the same number: it never counts,
			// and only a person's choice (a vouch here, its catch-up
			// overriding it) ends the fork.
			errs = append(errs, fmt.Sprintf("%s: its log forked from this one at history %d (accepted by %s)", p, a.got.History, a.got.HistoryBy))
			forked = append(forked, p)
		case !n.counts(p, a.got.Unsynced, a.got.Identity, a.got.Lineage, hist):
			errs = append(errs, p+": its log is not synced")
			uncounted = append(uncounted, ahead{p, a.got})
		default:
			answered++
			commit = max(commit, a.got.Commit)
			if better(a.got, best) {
				best, bestSelf, bestPeer = a.got, false, p
			}
		}
		return !alone && answered >= n.majority()
	})
	if newer != nil {
		return newer
	}
	// What going ahead alone would override: a silent peer (it may hold a
	// write a majority committed without this log), and a copy that
	// answers but doesn't count yet with a later log (a witness in its
	// quiet period after a re-commission, #1053), whose entries would be
	// lost. Each only as the person accepted (Mark.Accept), else it waits.
	var outside []string
	for _, p := range silent {
		if !accept.covers(p, true, false, 0, 0) && !mutOplog("vouch_alone_partial") {
			outside = append(outside, n.name(p)+" does not answer")
		}
	}
	overrides := len(silent) > 0
	waitAhead := ""
	for _, u := range uncounted {
		if !better(u.got, best) {
			continue
		}
		overrides = true
		if accept.covers(u.p, false, false, u.got.LastTerm, u.got.LastSeq) || mutOplog("vouch_alone_ignores_ahead") {
			continue
		}
		if accept == nil {
			waitAhead = fmt.Sprintf("%s holds a later log (term %d, seq %d) but does not count yet", n.name(u.p), u.got.LastTerm, u.got.LastSeq)
		}
		outside = append(outside, fmt.Sprintf("%s holds a later log (term %d, seq %d) that does not count", n.name(u.p), u.got.LastTerm, u.got.LastSeq))
	}
	// A fork the person accepted at the vouch is overridden by this
	// catch-up even with a majority that counts: the only case a vouch
	// acts on a synced copy whose majority counts, since choosing one side
	// of a fork is what it is for. A fork not accepted waits.
	resolves := false
	for _, p := range forked {
		switch {
		case alone && accept.covers(p, false, true, 0, 0):
			resolves = !mutOplog("fork_resolved_without_bump")
		case alone:
			outside = append(outside, n.name(p)+"'s log forked from this one")
		}
	}
	useVouch := answered < n.majority() && alone
	if (useVouch || alone && len(forked) > 0) && len(outside) > 0 {
		if waitAhead != "" && len(outside) == 1 {
			return fmt.Errorf("%w: a vouched catch-up waits: %s", ErrReadOnly, waitAhead)
		}
		if accept == nil {
			return fmt.Errorf("%w: catch-up heard from %d of %d that count (%s); a vouched catch-up needs every copy to answer", ErrReadOnly, answered, len(n.o.Peers), strings.Join(errs, "; "))
		}
		sort.Strings(outside)
		return fmt.Errorf("%w: a vouched catch-up waits: not what was accepted (%s): naut redundancy vouch again", ErrReadOnly, strings.Join(outside, "; "))
	}
	if useVouch && overrides && len(silent) == len(n.others()) && !mutOplog("override_all_silent") {
		// Every other copy silent (naut-lead, 3c-3c-2a (i)): the new
		// history would sit on this copy alone, and the other two, back at
		// the old one, could commit there and lose it all at first contact.
		// Alone this copy commits nothing anyway.
		return fmt.Errorf("%w: a loss accepted with every other copy silent would leave the new history on this copy alone: bring one back or rebuild it (a rebuilt witness answers), then naut redundancy vouch again", ErrReadOnly)
	}
	if answered < n.majority() && !useVouch {
		why := ""
		switch {
		case !selfCounts:
			why = "; this replica's own log is not synced"
		case alone:
			why = "; a vouched catch-up needs every copy to answer"
		}
		return fmt.Errorf("%w: catch-up heard from %d of %d that count (%s)%s", ErrReadOnly, answered, len(n.o.Peers), strings.Join(errs, "; "), why)
	}
	// A counting copy's entries (the same history's) are taken first: a
	// crash before the bump below then leaves this copy at the old history
	// with them and its one-shot pending, never at the new one without
	// them (#1177 review nit). Page by page, each synced as it is taken
	// (#1455); nothing is committed until the last.
	moved := fmt.Errorf("%w: the log history moved during the catch-up", ErrReadOnly)
	deposed := func() error {
		if n.term > term && !mutOplog("leader_catchup_term_unchecked") {
			return fmt.Errorf("%w: a newer term (%d) was seen during the catch-up for term %d", ErrReadOnly, n.term, term)
		}
		return nil
	}
	take := func(es []Entry) error {
		n.mu.Lock()
		defer n.mu.Unlock()
		if !n.mark.Lineage.same(hist) || n.supersedingLocked() {
			return moved // a newer history was heard while the peers were asked
		}
		if err := deposed(); err != nil {
			return err
		}
		if _, err := n.acceptLocked(es); err != nil {
			n.log.Warn("oplog: leader catch-up", "error", err)
			return err
		}
		return nil
	}
	if !bestSelf && len(best.Entries) > 0 {
		if err := take(best.Entries); err != nil {
			return err
		}
		if best.More {
			// Every page from the log that was chosen, still the same and
			// still counting (Fable, #1455 review: a copy that lost its log
			// mid-read comes back empty at the same term and history, and
			// its empty page would end the read short of what was chosen).
			_, err := n.fetchAfter(ctx, bestPeer, best.Entries[len(best.Entries)-1].Seq, func(p afterResp) error {
				same := p.Term == best.Term && p.Lineage.same(best.Lineage) && p.LastSeq == best.LastSeq && p.LastTerm == best.LastTerm
				if !mutOplog("leader_catchup_pages_unchecked") && (!same || !n.counts(bestPeer, p.Unsynced, p.Identity, p.Lineage, hist)) {
					return fmt.Errorf("%w: %s's log moved while it was read page by page", ErrReadOnly, n.name(bestPeer))
				}
				return take(p.Entries)
			})
			if err != nil {
				return err
			}
		}
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if !n.mark.Lineage.same(hist) || n.supersedingLocked() {
		return moved
	}
	if err := deposed(); err != nil {
		return err
	}
	if useVouch || resolves {
		// Spent before it is used: a mark that can't record that never
		// leaves a second use. An override starts a new log history in
		// the same write, durable before this copy is synced or takes or
		// sequences anything at it: the copies it overrides then count for
		// nothing, and are moved aside when they meet it.
		m := n.mark
		if !mutOplog("vouch_alone_kept") {
			m.Alone = false
		}
		m.Accept = nil
		bump := (overrides && useVouch || resolves) && !mutOplog("history_not_bumped") && !mutOplog("history_after_write")
		if bump {
			m.Lineage = newLineage(hist, accept)
		}
		if err := n.setMarkLocked(m); err != nil {
			return fmt.Errorf("oplog: recording that the vouch's catch-up is used: %w", err)
		}
		if bump {
			n.log.Warn("oplog: a vouched catch-up overrode a copy, as a person accepted: a new log history starts; the overridden copies count for nothing and are moved aside when they meet it", "history", m.History)
		}
	}
	// The best log a majority answered with holds every committed entry,
	// so any commit index one of them reported is safe here.
	n.commitTo(commit)
	n.syncedTerm = term
	n.becomeSyncedLocked("caught up from a majority as leader")
	if useVouch && overrides && mutOplog("history_after_write") {
		m := n.mark
		m.Lineage = newLineage(hist, accept)
		if err := n.setMarkLocked(m); err != nil {
			n.syncedTerm = 0
			return fmt.Errorf("oplog: recording the new log history: %w", err)
		}
	}
	if useVouch {
		n.log.Warn("oplog: caught up on a vouched log, without a majority that counts", "answered", answered, "commit", n.commit)
	}
	n.usedVouchLocked()
	return nil
}

// newLineage is the history an override starts after hist: the next
// number, under a fresh random id (never 0), accepted by the acceptance's
// person.
func newLineage(hist Lineage, a *Accept) Lineage {
	var b [8]byte
	var id uint64
	for id == 0 {
		_, _ = rand.Read(b[:])
		id = binary.BigEndian.Uint64(b[:])
	}
	l := Lineage{History: hist.History + 1, HistoryID: id}
	if a != nil {
		l.HistoryBy = a.By
	}
	return l
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

// VouchAccept is what a person accepts at a vouch (#1029 3c-3c-2a), each
// as the digest of the picture they saw (Picture.Digest; "" accepts
// nothing): Loss, what the other copies may hold (a silent copy, a later
// log that does not count, or every other copy lost while this one is
// synced); LostLog, that this copy's own log was lost.
type VouchAccept struct {
	Loss    string `json:"loss,omitempty"`
	LostLog string `json:"lostLog,omitempty"`
}

// Picture is what a vouch saw of the copies: this one, and each other's
// answer. Digest names it (Err left out): an accepting run must see the
// same picture. Needs is what the vouch needs accepted ("loss",
// "lost-log"), and Why says, a line each, what would be lost.
type Picture struct {
	Self   CopyState   `json:"self"`
	Copies []CopyState `json:"copies"`
	Digest string      `json:"digest"`
	Needs  []string    `json:"needs,omitempty"`
	Why    []string    `json:"why,omitempty"`
}

// CopyState is one copy of the log, as a vouch saw it.
type CopyState struct {
	Name     string `json:"name"`
	Answers  bool   `json:"answers"`
	Err      string `json:"err,omitempty"`
	Counts   bool   `json:"counts"`
	Synced   bool   `json:"synced"`
	Lost     bool   `json:"lost,omitempty"`
	SetAside bool   `json:"setAside,omitempty"`
	LastTerm uint64 `json:"lastTerm"`
	LastSeq  uint64 `json:"lastSeq"`
	// Lineage is its log history; Forked, the same number under another
	// override's id.
	Lineage
	Forked bool `json:"forked,omitempty"`
}

// The needs a vouch names (Picture.Needs).
const (
	NeedLoss    = "loss"
	NeedLostLog = "lost-log"
)

// digest is the picture's short digest: sha256 over its copies (Err left
// out, so a silent copy's changing error text does not change it) and its
// needs.
func (p Picture) digest() string {
	strip := func(c CopyState) CopyState { c.Err = ""; return c }
	cs := make([]CopyState, 0, len(p.Copies))
	for _, c := range p.Copies {
		cs = append(cs, strip(c))
	}
	b, _ := json.Marshal(struct {
		Self   CopyState
		Copies []CopyState
		Needs  []string
	}{strip(p.Self), cs, p.Needs})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:10]
}

// emptyPairRefusal is why the picture is not a new pair's (#1029 3c-3c-2b,
// S2), "" when it is: every other copy answers, none was lost or set
// aside another identity's log that held entries, every copy is at
// history 0, and every log is empty, this one's too. No copy then shows
// anything the pair committed, so a vouch for its synced copy alone
// accepts no loss it can see (a copy wiped outright shows nothing: that
// double loss looks the same as a new pair). The first condition that
// fails is named.
func emptyPairRefusal(pic Picture) string {
	if mutOplog("empty_pair_unchecked") {
		return ""
	}
	for _, c := range pic.Copies {
		if !c.Answers {
			return c.Name + " does not answer"
		}
	}
	for _, c := range append([]CopyState{pic.Self}, pic.Copies...) {
		switch {
		case c.Lost:
			return c.Name + "'s log was lost"
		case c.SetAside:
			return c.Name + " set aside another identity's log that held entries"
		case c.Lineage.History != 0:
			return fmt.Sprintf("%s is at history %d", c.Name, c.Lineage.History)
		case c.LastSeq != 0:
			return fmt.Sprintf("%s's log holds entries (to seq %d)", c.Name, c.LastSeq)
		}
	}
	return ""
}

// Vouch is a person's word that this replica's log holds every committed
// entry, given when a controller is commissioned or recommissioned (#710
// 3b-4b), or by itself (naut redundancy vouch, #1029 3c-3c-2a): with only
// synced replicas counting, a new pair (three empty, unsynced logs) could
// never sync, and nor could a pair whose logs were all lost or rebuilt.
// The log counts as one synced replica from then on, and its first
// catch-up as leader may succeed with no other replica that counts
// (Mark.Alone, leaderCatchUp): when every peer answers, or as the person
// accepted (Mark.Accept), taking the most up-to-date log among those that
// count.
//
// It returns the picture it saw. Without an acceptance it refuses
// (ErrVouchRefused) what would lose entries, naming the flag that accepts
// it with the picture's digest:
//   - this copy's log was lost (Lost): --accept-lost-log (or recommission
//     the other controller);
//   - a peer does not answer: it may hold entries this log lacks.
//     --accept-loss, or bring it back or rebuild it;
//   - a peer that does NOT count holds a later log (its last term, then
//     its last seq): its entries would be lost. --accept-loss. A later log
//     on a peer that counts is fine: the catch-up takes it (#1053 review);
//   - this copy is synced, and no other copy counts (both lost or
//     rebuilt): entries committed while it lagged may be on them.
//     --accept-loss grants the one-shot catch-up. Not on a new pair
//     (emptyPairRefusal): with nothing committed anywhere a plain vouch
//     grants it.
//
// An accepting run must see the same picture (the digest), else it
// refuses and returns the new one. A synced log another copy counts
// beside needs no word: Vouch does nothing and returns a zero Vouch.
//
// counts says whether a peer's copy counts, by the identity it reports,
// for this vouch (a re-commission judges the witness at the incarnation
// it adopts); nil: Options.Counts. Without a mark it does nothing.
func (n *Node) Vouch(ctx context.Context, by string, counts func(peer, identity string) bool, acc VouchAccept) (Vouch, Picture, error) {
	if n.o.Mark == "" || n.standalone() {
		return Vouch{}, Picture{}, nil
	}
	n.seqMu.Lock()
	defer n.seqMu.Unlock()
	n.mu.Lock()
	if n.supersedingLocked() {
		n.mu.Unlock()
		return Vouch{}, Picture{}, fmt.Errorf("%w: %w", ErrVouchRefused, errSuperseded)
	}
	synced, alone, lost, last := n.syncedLocked(), n.mark.Alone, n.mark.Lost, n.lastLocked()
	setAside := n.mark.SetAside != ""
	lastTerm := n.termAtLocked(last)
	hist := n.mark.Lineage
	n.mu.Unlock()
	check := !mutOplog("vouch_unchecked")
	pic := Picture{Self: CopyState{Name: n.name(n.o.Self), Answers: true, Counts: synced, Synced: synced, Lost: lost != "", SetAside: setAside, LastTerm: lastTerm, LastSeq: last, Lineage: hist}}
	type answer struct {
		got afterResp
		err error
	}
	got := map[string]answer{}
	// Every peer, each to its own call timeout: none is skipped.
	fanOut(ctx, n, n.others(), func(ctx context.Context, p string) answer {
		var a afterResp
		err := n.call(ctx, p, fmt.Sprintf("/api/oplog?after=%d&page=1", last), nil, &a)
		if err == nil {
			n.mu.Lock()
			n.notePeerLocked(p, a.Unsynced, a.Identity, a.Lineage)
			err = n.learnHistoryLocked(a.Lineage)
			n.mu.Unlock()
		}
		return answer{a, err}
	}, func(p string, a answer) bool {
		got[p] = a
		return false
	})
	acceptPeers := map[string]AcceptedPeer{}
	var needs, why []string
	need := func(what, line string) {
		if !slices.Contains(needs, what) {
			needs = append(needs, what)
		}
		why = append(why, line)
	}
	if lost != "" {
		need(NeedLostLog, fmt.Sprintf("this controller's operator-intent log was lost (kept at %s): recommission the other controller instead, or accept its loss with --accept-lost-log", lost))
	}
	otherCounts, anyForked := false, false
	for _, p := range n.others() {
		a := got[p]
		c := CopyState{Name: n.name(p), Answers: a.err == nil}
		if a.err != nil {
			c.Err = a.err.Error()
			pic.Copies = append(pic.Copies, c)
			acceptPeers[p] = AcceptedPeer{Silent: true}
			need(NeedLoss, fmt.Sprintf("%s does not answer: it may hold entries this log lacks; bring it back or rebuild it, or accept what it may hold with --accept-loss", n.name(p)))
			continue
		}
		g := a.got
		if g.History > hist.History {
			return Vouch{}, pic, fmt.Errorf("%w: %s holds a newer log history (%d; this copy %d): %w", ErrVouchRefused, n.name(p), g.History, hist.History, errSuperseded)
		}
		c.Synced, c.Lost, c.SetAside, c.LastTerm, c.LastSeq, c.Lineage = !g.Unsynced, g.Lost, g.SetAside, g.LastTerm, g.LastSeq, g.Lineage
		c.Forked = g.Lineage.forks(hist)
		c.Counts = g.Lineage.same(hist) && n.vouchCounts(counts, p, g)
		pic.Copies = append(pic.Copies, c)
		later := g.LastTerm > lastTerm || g.LastTerm == lastTerm && g.LastSeq > last
		otherCounts = otherCounts || c.Counts
		switch {
		case g.History < hist.History:
			// An older history's copy: its loss was accepted already.
		case c.Forked:
			// Another override's log at this number: choosing this copy
			// loses that side (its catch-up overrides it and starts the
			// next history).
			anyForked = true
			acceptPeers[p] = AcceptedPeer{Forked: true}
			need(NeedLoss, fmt.Sprintf("%s's log forked from this one at history %d (this one accepted by %s, that one by %s): choosing this one loses that side's entries; accept that with --accept-loss", n.name(p), g.History, hist.HistoryBy, g.HistoryBy))
		case later && (!c.Counts || mutOplog("vouch_refuses_counting_ahead")):
			// A later log on a copy that counts is fine: the catch-up
			// takes it (#1053 review).
			acceptPeers[p] = AcceptedPeer{LastTerm: g.LastTerm, LastSeq: g.LastSeq}
			need(NeedLoss, fmt.Sprintf("%s holds a later log that does not count (term %d, seq %d; this one ends at term %d, seq %d): its entries would be lost; accept that with --accept-loss", n.name(p), g.LastTerm, g.LastSeq, lastTerm, last))
		}
	}
	if synced && !anyForked {
		if otherCounts || alone && mutOplog("vouch_again_noop") {
			// Another copy counts: its catch-up takes from it, nothing to
			// vouch for. (A pending one-shot alone is no reason: its
			// catch-up may have refused as not what was accepted, and
			// "vouch again" must judge the picture anew, #1177 review.) A
			// digest given must still name this picture.
			pic.Digest = pic.digest()
			for _, d := range []struct{ flag, given string }{{"--accept-loss", acc.Loss}, {"--accept-lost-log", acc.LostLog}} {
				if d.given != "" && d.given != pic.Digest && !mutOplog("noop_digest_unchecked") {
					return Vouch{}, pic, fmt.Errorf("%w: %s=%s does not match what the copies show now (%s; nothing needs accepting): read the picture again", ErrVouchRefused, d.flag, d.given, pic.Digest)
				}
			}
			return Vouch{}, pic, nil
		}
		// A new pair restarted before its first catch-up (#1029
		// 3c-3c-2b, S2): every copy answers, none lost, all at history
		// 0, every log empty, this one's too. Nothing any copy shows can
		// be lost, so a plain vouch grants the one-shot.
		if why := emptyPairRefusal(pic); why != "" || mutOplog("empty_pair_needs_accept") {
			line := "no other copy counts (each was lost or rebuilt): entries committed while this one lagged may have been on them; accept that with --accept-loss"
			if why != "" {
				line += " (a plain vouch serves only a new pair whose every copy answers, none lost, all at history 0, every log empty: " + why + ")"
			}
			need(NeedLoss, line)
		}
		if mutOplog("synced_alone_without_accept") {
			needs = slices.DeleteFunc(needs, func(s string) bool { return s == NeedLoss })
		}
	}
	if mutOplog("lost_log_vouch_unchecked") {
		needs = slices.DeleteFunc(needs, func(s string) bool { return s == NeedLostLog })
	}
	pic.Needs, pic.Why = needs, why
	pic.Digest = pic.digest()
	var problems, flags []string
	for _, f := range []struct{ need, flag, given string }{{NeedLoss, "--accept-loss", acc.Loss}, {NeedLostLog, "--accept-lost-log", acc.LostLog}} {
		if slices.Contains(needs, f.need) {
			flags = append(flags, f.flag+"="+pic.Digest)
		}
		switch {
		case f.given != "" && f.given != pic.Digest:
			problems = append(problems, fmt.Sprintf("%s=%s does not match what the copies show now (%s): read the picture again", f.flag, f.given, pic.Digest))
		case f.given == "" && slices.Contains(needs, f.need):
			problems = append(problems, "not accepted")
		}
	}
	if len(problems) > 0 && len(flags) > 0 {
		problems = append(problems, "if that loss is acceptable, run on this controller: naut redundancy vouch "+strings.Join(flags, " "))
	}
	if len(problems) > 0 && check {
		return Vouch{}, pic, fmt.Errorf("%w: %s; %s", ErrVouchRefused, strings.Join(why, "; "), strings.Join(problems, "; "))
	}
	v := Vouch{By: by, At: n.o.Now().UnixMilli(), LastTerm: lastTerm, LastSeq: last}
	var acceptance *Accept
	if acc.Loss != "" && (len(acceptPeers) > 0 || synced) {
		acceptance = &Accept{By: by, At: v.At, Digest: pic.Digest, Peers: acceptPeers}
	}
	if n.o.afterVouchAsk != nil {
		n.o.afterVouchAsk()
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if !n.mark.Lineage.same(hist) || n.supersedingLocked() {
		return Vouch{}, pic, fmt.Errorf("%w: the log history moved during the vouch: run it again", ErrVouchRefused)
	}
	if !synced && n.syncedLocked() {
		return Vouch{}, pic, nil // synced meanwhile, by a synced leader
	}
	m := n.mark
	m.Synced, m.Lost, m.Vouch, m.Alone, m.Accept = true, "", &v, true, acceptance
	if err := n.setMarkLocked(m); err != nil {
		return Vouch{}, pic, fmt.Errorf("oplog: recording the vouch: %w", err)
	}
	if !mutOplog("vouch_keeps_term_synced") {
		// A leader already caught up for its term catches up again, so the
		// one-shot is used now (a fork chosen on the leader is resolved),
		// not at its next term (#1177 review).
		n.syncedTerm = 0
	}
	n.log.Warn("oplog: a person vouched for this replica's log: it counts as synced", "by", by, "lastTerm", lastTerm, "lastSeq", last, "accepted", pic.Needs, "digest", pic.Digest)
	return v, pic, nil
}

// vouchCounts: a peer's answer counts for a vouch (Vouch's counts).
func (n *Node) vouchCounts(counts func(peer, identity string) bool, p string, a afterResp) bool {
	if a.Unsynced {
		return false
	}
	if counts != nil {
		return counts(p, a.Identity)
	}
	return n.counts(p, false, a.Identity, a.Lineage, a.Lineage) // its history is judged by the caller
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
	if n.supersedingLocked() {
		n.mu.Unlock()
		return Entry{}, fmt.Errorf("%w: %w", ErrReadOnly, errSuperseded)
	}
	hist := n.mark.Lineage
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
		case !n.counts(p, a.resp.Unsynced, a.resp.Identity, a.resp.Lineage, hist):
			holders = append(holders, p)
			errs = append(errs, p+": stored, but its log is not synced")
		default:
			holders = append(holders, p)
			stored++
		}
		return stored >= n.majority()
	})
	// The history checked and the commit made under one lock: a higher
	// history learnt in between (the hello, a straggler's answer) would
	// otherwise let a superseded leader apply an old-history entry (#1177
	// review).
	n.mu.Lock()
	moved := !n.mark.Lineage.same(hist) || n.supersedingLocked()
	if moved && !mutOplog("history_moved_commits") {
		// A newer log history was heard while replicating: this copy is
		// superseded, and what it stored is the old history's.
		stored = 0
		errs = append(errs, errSuperseded.Error())
	}
	if stored < n.majority() {
		// Kept, not truncated: a follower whose reply was lost may hold
		// it, and the number reused for another entry of this term would
		// pass that follower's previous-entry check and leave it applying
		// a different entry at the same seq. The next write (or leader)
		// carries it to a majority, or a newer term's leader replaces it.
		n.syncedTerm = 0 // a peer may know more than we do: catch up before the next write
		n.mu.Unlock()
		return Entry{}, fmt.Errorf("%w: %w: %d of %d stored (%s)", ErrReadOnly, ErrUnconfirmed, stored, len(n.o.Peers), strings.Join(errs, "; "))
	}
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
		termSynced := n.syncedTerm == term && !n.supersedingLocked()
		hist := n.mark.Lineage
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
			replicateReq{From: n.o.Self, Node: n.o.Node, Synced: synced, Term: term, Commit: commit, PrevSeq: prev, PrevTerm: pt, Entries: es, Lineage: hist}, &resp)
		var he *httpError
		switch {
		case err == nil:
			n.mu.Lock()
			n.notePeerLocked(peer, resp.Unsynced, resp.Identity, resp.Lineage)
			herr := n.learnHistoryLocked(resp.Lineage)
			n.mu.Unlock()
			if herr != nil {
				return resp, herr
			}
		case errors.As(err, &he) && he.history:
			// Refused for its log history: a higher one supersedes this
			// leader's log; a forked one is noted (#1029 3c-3c-2a).
			n.mu.Lock()
			n.notePeerLocked(peer, true, "", resp.Lineage)
			herr := n.learnHistoryLocked(resp.Lineage)
			n.mu.Unlock()
			err = errors.Join(err, herr)
		}
		return resp, err
	}
	resp, err := send(es)
	// The peer is behind (a gap: Have is its last entry) or its entry
	// before these is not ours (a mismatch: Have is its commit index), or
	// it took a chunk that stops short of our last entry (Have is as far
	// as the request proved its log ours): send what is after Have, a
	// chunk at a time, and again from what the peer answers (a gap's
	// resend can meet a mismatch below it), until it has them. Each step
	// after a mismatch moves Have past the chunk just taken, so a stale
	// tail of the peer's within the rounds' reach of its commit index is
	// reached and replaced (one further off, by its own pull); the bound
	// guards a peer that answers nonsense, and the next heartbeat goes on
	// (a rebuilt replica is filled over several).
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
		var missing []Entry
		if mutOplog("resend_unchunked") {
			missing = n.afterLocked(from)
		} else {
			missing, _ = n.pageLocked(from)
		}
		n.mu.Unlock()
		if len(missing) == 0 {
			return resp, err
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

// pageLocked is one page of the log after seq, for a reader that reads it
// page by page (a catch-up) or a lagging peer's resend: at most
// chunkEntries entries and chunkBytes of them, at least one. It copies
// only the page, never the whole tail (#1455: a replacement's catch-up
// asked for every page with the whole log copied each time). more: the
// log goes on past it. Caller holds mu.
func (n *Node) pageLocked(seq uint64) (page []Entry, more bool) {
	i := n.indexAfterLocked(seq)
	size := 0
	for k := i; k < len(n.entries); k++ {
		size += len(n.entries[k].raw) + 1
		if k > i && (k-i >= n.o.lim.chunkEntries || size > n.o.lim.chunkBytes) {
			return decodeAll(n.entries[i:k]), true
		}
	}
	return decodeAll(n.entries[i:]), false
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

// fetchPage reads one page of a peer's log after seq and notes what it
// says of itself (its sync, identity and history).
func (n *Node) fetchPage(ctx context.Context, peer string, after uint64) (afterResp, error) {
	var got afterResp
	if err := n.call(ctx, peer, fmt.Sprintf("/api/oplog?after=%d&page=1", after), nil, &got); err != nil {
		return afterResp{}, err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.notePeerLocked(peer, got.Unsynced, got.Identity, got.Lineage)
	if err := n.learnHistoryLocked(got.Lineage); err != nil {
		return afterResp{}, err
	}
	return got, nil
}

// fetchAfter reads a peer's log after seq page by page, handing each page
// to take as it arrives: it holds one page at a time, and a log of any
// length is read (#1455: it gathered every page first, so a replacement
// held the log twice, and refused one over 16,384 pages). The pages must
// come from one log: the same term and history, each starting where the
// last ended. It returns the last page, its entries dropped.
func (n *Node) fetchAfter(ctx context.Context, peer string, after uint64, take func(afterResp) error) (afterResp, error) {
	var first afterResp
	for page := 0; ; page++ {
		got, err := n.fetchPage(ctx, peer, after)
		if err != nil {
			return afterResp{}, err
		}
		if page == 0 {
			first = got
		} else if got.Term != first.Term || !got.Lineage.same(first.Lineage) || len(got.Entries) > 0 && got.Entries[0].Seq != after+1 {
			return afterResp{}, fmt.Errorf("oplog: %s's log moved while it was read page by page", peer)
		}
		if err := take(got); err != nil {
			return afterResp{}, err
		}
		if !got.More || len(got.Entries) == 0 {
			got.Entries, got.More = nil, false
			return got, nil
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
	// commit on replace it (see acceptLocked). Each page is checked and
	// taken as it arrives, synced with it (#1455); the commit index moves
	// once, with the last page, so a lapse and the entries it names are
	// applied in one range (see applyRange, #1466).
	_, err := n.fetchAfter(ctx, leader, n.Commit(), func(got afterResp) error {
		if !got.LeaderSynced && !mutOplog("unsynced_leader_writes") {
			// Its entries past our commit index may be an earlier term's
			// leftovers that would replace one a later term committed
			// (#1050 review). Its heartbeat or a later pull brings them
			// once it has caught up.
			return errTermNotSynced
		}
		n.mu.Lock()
		defer n.mu.Unlock()
		switch {
		case n.supersedingLocked():
			return errSuperseded
		case !got.Lineage.same(n.mark.Lineage) && !mutOplog("history_lower_counted") && !mutOplog("history_fork_counted"):
			// A lower history's leader (its log is one a person accepted
			// to lose; a higher one superseded this copy in fetchPage),
			// or a forked one.
			return fmt.Errorf("%w: the leader's log is at history %d (id %x), this copy's at %d (id %x)", errConflict, got.History, got.HistoryID, n.mark.History, n.mark.HistoryID)
		}
		if got.Term < n.term {
			return fmt.Errorf("%w: leader at term %d, have seen %d", errConflict, got.Term, n.term)
		}
		if _, err := n.acceptLocked(got.Entries); err != nil {
			return err
		}
		switch {
		case !got.More || len(got.Entries) == 0:
			n.commitTo(got.Commit)
		case mutOplog("catchup_commit_per_page"):
			n.commitTo(min(got.Commit, got.Entries[len(got.Entries)-1].Seq))
		}
		return nil
	})
	return err
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
			if n.countsLocked(p) {
				synced = append(synced, p)
			}
		}
		if p == n.o.Self || now.Sub(n.lastContact[p]) < 10*time.Second {
			reached = append(reached, p)
			switch {
			case p == n.o.Self && n.syncedLocked():
				counted++
			case p != n.o.Self && n.countsLocked(p):
				counted++
			}
		}
	}
	leader := n.leaderAddr()
	leaderUnsynced := n.peers[leader].unsynced || !n.peers[leader].lineage.same(n.mark.Lineage)
	var forked []Fork
	for _, p := range n.o.Peers {
		if pi, ok := n.peers[p]; ok && p != n.o.Self && pi.lineage.forks(n.mark.Lineage) {
			forked = append(forked, Fork{Peer: n.name(p), History: pi.lineage.History, Ours: n.mark.HistoryBy, Theirs: pi.lineage.HistoryBy})
		}
	}
	s := Status{Self: n.o.Self, Leader: leader, IsLeader: n.isLeader(), Term: n.term, Peers: n.o.Peers, Reached: reached, LastSeq: n.lastLocked(), Commit: n.commit,
		Unsynced: !n.syncedLocked(), Identity: n.mark.Identity, Lost: n.mark.Lost, Synced: synced, Vouch: n.mark.Vouch,
		Lineage: n.mark.Lineage, SupersedeTo: n.mark.SupersedeTo, Superseded: n.mark.Superseded, Forked: forked}
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
		// Noted, never learnt: /api/quorum is answered without the token,
		// so only an authenticated answer supersedes this copy.
		n.mu.Lock()
		n.notePeerLocked(p, st.Unsynced, st.Identity, st.Lineage)
		n.mu.Unlock()
		return true
	}, func(string, bool) bool { return false })
}

type httpError struct {
	code int
	// resend: a replicate refused for a gap or a mismatched previous
	// entry; the answer's Have says where to resend from.
	resend bool
	// history: refused for its log history ("history": the follower's is
	// higher; "superseded": it heard a higher one); the answer's History
	// says which.
	history bool
	body    string
}

func (e *httpError) Error() string { return fmt.Sprintf("HTTP %d: %s", e.code, e.body) }

// peerClient is the one call the node makes of its HTTP client.
type peerClient interface {
	Do(*http.Request) (*http.Response, error)
}

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
			he.history = strings.Contains(he.body, `"history"`) || strings.Contains(he.body, `"superseded"`) || strings.Contains(he.body, `"forked"`)
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
		resp := afterResp{Commit: n.commit, Term: n.term, LastSeq: last, LastTerm: n.termAtLocked(last),
			Unsynced: !n.syncedLocked(), Identity: n.mark.Identity, Lineage: n.mark.Lineage, Lost: n.mark.Lost != "", SetAside: n.mark.SetAside != ""}
		resp.LeaderSynced = n.isLeader() && n.syncedTerm != 0 && n.syncedTerm == n.currentTerm()
		// Pages only for a reader that asks (page=1): one from before
		// pages would take the first page for the whole tail.
		if r.URL.Query().Get("page") == "1" && !mutOplog("resend_unchunked") {
			resp.Entries, resp.More = n.pageLocked(after)
		} else {
			resp.Entries = n.afterLocked(after)
		}
		n.mu.Unlock()
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
		// The sender is the member its connection's certificate names
		// (#710 per-box keys): a member may not replicate as another.
		if n.o.Sender != nil && !mutOplog("log_sender_unbound") && !n.o.Sender(r, req.Node, req.From) {
			http.Error(w, fmt.Sprintf("replication naming %q (%s) from a connection whose certificate names another member", req.Node, req.From), http.StatusForbidden)
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
		// The log history first (#1029 3c-3c-2a): a sender at a lower one
		// is refused, as a lower term is, and answered with ours, so it
		// learns it is superseded; a higher one supersedes this copy. A
		// controller's then takes nothing more until its restart moves the
		// log aside; the witness's (ResetInPlace) starts empty at once and
		// takes the request on that.
		if n.o.Mark != "" {
			if req.History > n.mark.History {
				if err := n.learnHistoryLocked(req.Lineage); err != nil {
					n.mu.Unlock()
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
			}
			switch {
			case n.supersedingLocked():
				h := n.mark.Lineage
				n.mu.Unlock()
				writeJSON(w, http.StatusConflict, historyRefusal("superseded", h, errSuperseded.Error()))
				return
			case req.History < n.mark.History && !mutOplog("history_lower_counted"):
				h := n.mark.Lineage
				n.mu.Unlock()
				writeJSON(w, http.StatusConflict, historyRefusal("history", h, fmt.Sprintf("this copy's log is at history %d, the sender's at %d: a person accepted the loss of the older one", h.History, req.History)))
				return
			case req.Lineage.forks(n.mark.Lineage) && !mutOplog("history_fork_counted"):
				h := n.mark.Lineage
				n.peers[req.From] = peerInfo{unsynced: true, lineage: req.Lineage}
				n.mu.Unlock()
				writeJSON(w, http.StatusConflict, historyRefusal("forked", h, fmt.Sprintf("this copy's log forked from the sender's at history %d (accepted by %s; the sender's by %s): a person chooses one with naut redundancy vouch", h.History, h.HistoryBy, req.HistoryBy)))
				return
			}
		}
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
			// Only at the leader's own commit index, and only once this
			// request proved the logs equal that far (#1466). A leader
			// commits a KindLapse and every entry it names in one range,
			// so no index the lapse's leader or a later one committed at
			// falls between them; a commit cut short at what one chunk of a
			// resend proved could, and the entries it names were then
			// carried out here, while the leader and this replica's own
			// replay record them as lapsed.
			proved := req.PrevSeq + uint64(len(req.Entries))
			switch {
			case proved >= req.Commit:
				n.commitTo(req.Commit)
			case mutOplog("follower_commit_mid_range"):
				n.commitTo(proved)
			}
			// A synced leader's log holds every committed entry, and this
			// request proved ours equal to it up to its commit index:
			// ours holds them too.
			if (req.Synced || mutOplog("synced_by_unsynced_leader")) && (proved >= req.Commit || mutOplog("synced_before_commit")) {
				n.becomeSyncedLocked("replicated from a synced leader")
				n.usedVouchLocked()
			}
		}
		have := n.lastLocked()
		switch {
		case errors.Is(err, errMismatch):
			have = n.commit // resend from what is committed here
		case err == nil && !mutOplog("have_past_proved"):
			// Only as far as this request proved the logs equal (#1472):
			// entries this replica holds past that may be an old term's,
			// not the leader's, and the leader counts Have as holding its
			// entries. Answering them made a write count on a copy that
			// did not hold it, and kept a resend from reaching them.
			have = req.PrevSeq + uint64(len(req.Entries))
		}
		commit := n.commit
		if l := n.leaderAddr(); l != "" {
			n.lastContact[l] = n.o.Now()
		}
		if err == nil {
			n.leaderHeard.Store(n.o.Now().UnixMilli())
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
		unsynced, identity, hist := !n.syncedLocked(), n.mark.Identity, n.mark.Lineage
		n.mu.Unlock()
		writeJSON(w, http.StatusOK, replicateResp{Have: have, Commit: commit, Unsynced: unsynced, Identity: identity, Lineage: hist})
	}))
	return mux
}

// historyRefusal is a replicate's 409 for its log history: kind is
// "history", "forked" or "superseded"; h this copy's history.
func historyRefusal(kind string, h Lineage, detail string) map[string]any {
	out := map[string]any{"error": kind, "history": h.History, "detail": detail}
	if h.HistoryID != 0 {
		out["historyId"] = fmt.Sprint(h.HistoryID)
		out["historyBy"] = h.HistoryBy
	}
	return out
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// CloseIdle closes the log's kept peer connections: after a peer's pin is
// dropped (#710 per-box keys), its next call meets the new key.
func (n *Node) CloseIdle() {
	if c, ok := n.client.(*http.Client); ok {
		c.CloseIdleConnections()
	}
}

// tlsHandshake bounds a peer TLS handshake (#1230 review).
const tlsHandshake = 2 * time.Second

// tlsWire sends a peer's http:// URL as https (Options.TLS): the URL stays
// the peer's name, the wire is TLS.
type tlsWire struct{ rt http.RoundTripper }

func (w tlsWire) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme == "http" {
		r = r.Clone(r.Context())
		r.URL.Scheme = "https"
	}
	return w.rt.RoundTrip(r)
}
