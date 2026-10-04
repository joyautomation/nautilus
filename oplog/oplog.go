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
// misses the majority is removed from the leader's log and the client is
// told no; a follower that stored it replaces it when the leader reuses
// the number. A peer that was away asks the leader for everything after
// its last entry. Entries carry the leader's term; a stale leader's
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
	Until  int64    `json:"until,omitempty"` // shelve, epoch ms
	By     string   `json:"by"`
	Origin string   `json:"origin,omitempty"` // the instance that took the write
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
	// File is the local append-only copy; "" keeps the log in memory only.
	File string
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

// rewrite replaces the file with the current entries and commit mark.
// Caller holds mu.
func (n *Node) rewrite() error {
	if n.file == nil {
		return nil
	}
	if err := n.file.Truncate(0); err != nil {
		return err
	}
	if _, err := n.file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	for i := range n.entries {
		e := n.entries[i]
		if err := n.writeLine(fileLine{E: &e}); err != nil {
			return err
		}
	}
	c := n.commit
	return n.writeLine(fileLine{C: &c})
}

var errGap = errors.New("gap")
var errConflict = errors.New("conflict")

// accept stores entries from the leader (or the leader's own), in order:
// a duplicate with the same content is skipped; a different entry at an
// UNCOMMITTED number replaces it and everything after (the old one was
// never committed, or a newer term says otherwise); a different entry at a
// committed number is a conflict; a number beyond last+1 is a gap. Caller
// holds mu. Returns how many were new.
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
			if e.Term < have.Term {
				return fresh, fmt.Errorf("%w: seq %d from term %d, have term %d", errConflict, e.Seq, e.Term, have.Term)
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
	if n.o.Apply == nil {
		return
	}
	if err := n.o.Apply(e); err != nil {
		n.log.Warn("oplog: apply", "seq", e.Seq, "kind", e.Kind, "error", err)
	}
}

// truncateLocked drops the uncommitted suffix from seq on. Caller holds mu.
func (n *Node) truncateLocked(seq uint64) {
	i := sort.Search(len(n.entries), func(i int) bool { return n.entries[i].Seq >= seq })
	if i >= len(n.entries) {
		return
	}
	n.entries = n.entries[:i:i]
	if err := n.rewrite(); err != nil {
		n.log.Warn("oplog: truncate", "error", err)
	}
}

// ── the wire ──

type replicateReq struct {
	From    string  `json:"from"`
	Term    uint64  `json:"term"`
	Commit  uint64  `json:"commit"`
	Entries []Entry `json:"entries"`
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
	// Committed by the leader: take it and its commit mark now, so the
	// client that asked sees it at once; a gap means we are behind, so
	// catch up instead.
	n.mu.Lock()
	_, err := n.acceptLocked([]Entry{out.Entry})
	if err == nil {
		n.commitTo(out.Commit)
	}
	n.mu.Unlock()
	if err != nil {
		if cerr := n.CatchUp(ctx); cerr != nil {
			n.log.Warn("oplog: catch-up after propose", "error", cerr)
		}
	}
	return out.Entry, nil
}

// sequence is the leader's path, one proposal at a time: number, store,
// replicate to a majority, commit, apply. A proposal that misses the
// majority is removed again and the client is told no.
func (n *Node) sequence(ctx context.Context, e Entry) (Entry, error) {
	n.seqMu.Lock()
	defer n.seqMu.Unlock()
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
		n.mu.Lock()
		n.truncateLocked(e.Seq)
		n.mu.Unlock()
		return Entry{}, fmt.Errorf("%w: %d of %d stored (%s)", ErrReadOnly, stored, len(n.o.Peers), strings.Join(errs, "; "))
	}
	n.mu.Lock()
	n.commitTo(e.Seq)
	n.mu.Unlock()
	return e, nil
}

// replicateTo sends entries to one peer, filling a gap it reports.
func (n *Node) replicateTo(ctx context.Context, peer string, es []Entry, term, commit uint64) error {
	var resp replicateResp
	err := n.call(ctx, peer, "/api/oplog/replicate", replicateReq{From: n.o.Self, Term: term, Commit: commit, Entries: es}, &resp)
	var he *httpError
	if errors.As(err, &he) && he.code == http.StatusConflict && he.gap {
		// The peer is behind: send everything it lacks.
		n.mu.Lock()
		missing := n.afterLocked(resp.Have)
		n.mu.Unlock()
		if len(missing) == 0 {
			return err
		}
		err = n.call(ctx, peer, "/api/oplog/replicate", replicateReq{From: n.o.Self, Term: term, Commit: commit, Entries: missing}, &resp)
	}
	return err
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
	var got afterResp
	if err := n.call(ctx, leader, fmt.Sprintf("/api/oplog?after=%d", n.LastSeq()), nil, &got); err != nil {
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
	gap  bool
	body string
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
			he.gap = strings.Contains(he.body, `"gap"`)
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
		resp := afterResp{Entries: n.afterLocked(after), Commit: n.commit, Term: n.term}
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
		_, err := n.acceptLocked(req.Entries)
		if err == nil {
			n.commitTo(req.Commit)
		}
		have := n.lastLocked()
		commit := n.commit
		if l := n.leaderAddr(); l != "" {
			n.lastContact[l] = n.o.Now()
		}
		n.mu.Unlock()
		if err != nil {
			kind := "conflict"
			if errors.Is(err, errGap) {
				kind = "gap"
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
