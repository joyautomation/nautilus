// Package oplog is a small replicated log of operator intent for a
// redundant controller: acknowledgements, shelves and unshelves, agreed by
// a majority of instances. Display state is derived by every instance
// from the field and never replicated; only what an operator did is. It
// came from argonaut, the 3D operator view for clusters, where three
// instances run it on a cluster (its design brief, nautilus-seam.md §4).
//
// The shape is the small end of Raft with election delegated: a Leadership
// (a Kubernetes Lease elector, or a fixed leader for a bench) says who the
// sequencer is. A write arrives at any instance and is forwarded to the
// leader, which assigns the next sequence number, stores it, fans it out
// to every peer, and answers the client once a MAJORITY of instances
// (itself included) has stored it. Then it is applied to the local alarm
// engine; a peer applies an entry when it stores it. A peer that was away
// asks the leader for everything after its last sequence number.
//
// Each instance keeps the log in memory and appends it to a local file,
// which survives a process restart but not a reschedule; there is no
// durable copy on the cluster being watched, on purpose (§4). Two
// instances of three cannot form a majority, so a split leaves exactly
// one side writable, and the other says so (Writable).
//
// It pairs with leader/: leader.Elector satisfies Leadership through a
// thin adapter (IsLeader, and the leader's address as a base URL).
package oplog

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	TS     int64    `json:"ts"` // epoch ms, assigned by the leader
	Kind   string   `json:"kind"`
	IDs    []string `json:"ids,omitempty"`   // ack
	ID     string   `json:"id,omitempty"`    // shelve, unshelve
	Until  int64    `json:"until,omitempty"` // shelve, epoch ms
	By     string   `json:"by"`
	Origin string   `json:"origin,omitempty"` // the instance that took the write
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

// Static is a Leadership fixed by configuration.
type Static struct {
	Self, Leader string
}

func (s Static) IsLeader() bool     { return s.Self == s.Leader }
func (s Static) LeaderAddr() string { return s.Leader }

// Applier applies a committed entry to the local alarm engine.
type Applier func(Entry) error

// Options build a Node.
type Options struct {
	// Self is this instance's base URL as peers reach it; Peers are every
	// instance's base URL, self included (the membership). One entry means
	// standalone: every write commits at once.
	Self  string
	Peers []string
	// Leadership says who sequences. Nil means standalone.
	Leadership Leadership
	// File is the local append-only copy; "" keeps the log in memory only.
	File string
	// Apply is called for every committed entry, in order, exactly once per
	// process lifetime (replayed from File at startup).
	Apply Applier
	// Token, when set, must accompany every replica-to-replica request.
	Token string
	// Timeout bounds one replica call (default 2s).
	Timeout time.Duration
	Now     func() time.Time
}

// Node is one instance's view of the log.
type Node struct {
	o      Options
	client *http.Client
	mu     sync.Mutex
	log    []Entry
	file   *os.File
	// lastContact is when each peer last answered (for Writable).
	lastContact map[string]time.Time
}

// Status is what /api/quorum reports.
type Status struct {
	Self     string   `json:"self"`
	Leader   string   `json:"leader"`
	IsLeader bool     `json:"isLeader"`
	Peers    []string `json:"peers"`
	Reached  []string `json:"reached"` // peers answering within the last 10 s
	LastSeq  uint64   `json:"lastSeq"`
	Writable bool     `json:"writable"`
	Reason   string   `json:"reason,omitempty"`
}

// ErrReadOnly says this instance cannot commit a write right now.
var ErrReadOnly = errors.New("oplog: read-only: no quorum reachable")

// New opens the node, replaying the file into Apply.
func New(o Options) (*Node, error) {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Timeout <= 0 {
		o.Timeout = 2 * time.Second
	}
	if len(o.Peers) == 0 {
		o.Peers = []string{o.Self}
	}
	n := &Node{o: o, client: &http.Client{Timeout: o.Timeout}, lastContact: map[string]time.Time{}}
	if o.File != "" {
		f, err := os.OpenFile(o.File, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
		if err != nil {
			return nil, err
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			var e Entry
			if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
				return nil, fmt.Errorf("oplog: %s: %w", o.File, err)
			}
			n.log = append(n.log, e)
		}
		if err := sc.Err(); err != nil {
			return nil, err
		}
		n.file = f
		for _, e := range n.log {
			if o.Apply != nil {
				_ = o.Apply(e)
			}
		}
	}
	return n, nil
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

func (n *Node) standalone() bool { return len(n.o.Peers) <= 1 || n.o.Leadership == nil }

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

func (n *Node) majority() int { return len(n.o.Peers)/2 + 1 }

// LastSeq is the newest stored sequence number.
func (n *Node) LastSeq() uint64 {
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.log) == 0 {
		return 0
	}
	return n.log[len(n.log)-1].Seq
}

// After returns the entries after seq.
func (n *Node) After(seq uint64) []Entry {
	n.mu.Lock()
	defer n.mu.Unlock()
	i := sort.Search(len(n.log), func(i int) bool { return n.log[i].Seq > seq })
	out := make([]Entry, len(n.log)-i)
	copy(out, n.log[i:])
	return out
}

// store appends an entry if it is new and in order, persisting it. The
// caller holds no lock. Returns whether it was new.
func (n *Node) store(e Entry) (bool, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	last := uint64(0)
	if len(n.log) > 0 {
		last = n.log[len(n.log)-1].Seq
	}
	if e.Seq <= last {
		return false, nil
	}
	if e.Seq != last+1 {
		return false, fmt.Errorf("oplog: gap: have %d, got %d", last, e.Seq)
	}
	if n.file != nil {
		b, _ := json.Marshal(e)
		if _, err := n.file.Write(append(b, '\n')); err != nil {
			return false, err
		}
	}
	n.log = append(n.log, e)
	return true, nil
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
	var out Entry
	if err := n.call(ctx, leader, "/api/oplog/propose", e, &out); err != nil {
		return Entry{}, fmt.Errorf("%w (leader %s: %v)", ErrReadOnly, leader, err)
	}
	// The leader committed it; it will replicate to us too, but apply now so
	// the client that asked sees it at once (store dedups the replica).
	if fresh, err := n.store(out); err == nil && fresh && n.o.Apply != nil {
		_ = n.o.Apply(out)
	}
	return out, nil
}

// sequence is the leader's path: number, store, replicate to a majority, apply.
func (n *Node) sequence(ctx context.Context, e Entry) (Entry, error) {
	n.mu.Lock()
	last := uint64(0)
	if len(n.log) > 0 {
		last = n.log[len(n.log)-1].Seq
	}
	e.Seq = last + 1
	e.TS = n.o.Now().UnixMilli()
	n.mu.Unlock()
	if _, err := n.store(e); err != nil {
		return Entry{}, err
	}
	stored := 1
	var errs []string
	for _, p := range n.o.Peers {
		if p == n.o.Self {
			continue
		}
		if err := n.call(ctx, p, "/api/oplog/replicate", []Entry{e}, nil); err != nil {
			errs = append(errs, p+": "+err.Error())
			continue
		}
		stored++
	}
	if stored < n.majority() {
		// Stored here but not committed: the entry stays in the log (a later
		// leader with it will replicate it) but the client is told no.
		return Entry{}, fmt.Errorf("%w: %d of %d stored (%s)", ErrReadOnly, stored, len(n.o.Peers), strings.Join(errs, "; "))
	}
	if n.o.Apply != nil {
		if err := n.o.Apply(e); err != nil {
			return e, err
		}
	}
	return e, nil
}

// CatchUp asks the leader for everything after our last entry: at startup,
// and periodically from a follower.
func (n *Node) CatchUp(ctx context.Context) error {
	if n.isLeader() {
		return nil
	}
	leader := n.leaderAddr()
	if leader == "" {
		return ErrReadOnly
	}
	var got []Entry
	if err := n.call(ctx, leader, fmt.Sprintf("/api/oplog?after=%d", n.LastSeq()), nil, &got); err != nil {
		return err
	}
	for _, e := range got {
		fresh, err := n.store(e)
		if err != nil {
			return err
		}
		if fresh && n.o.Apply != nil {
			_ = n.o.Apply(e)
		}
	}
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
	n.mu.Unlock()
	s := Status{Self: n.o.Self, Leader: n.leaderAddr(), IsLeader: n.isLeader(), Peers: n.o.Peers, Reached: reached, LastSeq: n.LastSeq()}
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
		if err := n.call(ctx, p, "/api/quorum", nil, &st); err == nil {
			n.mu.Lock()
			n.lastContact[p] = n.o.Now()
			n.mu.Unlock()
		}
	}
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
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode/100 != 2 {
		if resp.StatusCode >= 500 {
			n.lost(base)
		}
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
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
			if n.o.Token != "" && r.Header.Get("X-Nautilus-Cluster") != n.o.Token {
				http.Error(w, "cluster token", http.StatusUnauthorized)
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("GET /api/quorum", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, n.Status())
	})
	mux.HandleFunc("GET /api/oplog", auth(func(w http.ResponseWriter, r *http.Request) {
		var after uint64
		_, _ = fmt.Sscanf(r.URL.Query().Get("after"), "%d", &after)
		writeJSON(w, n.After(after))
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
		writeJSON(w, out)
	}))
	mux.HandleFunc("POST /api/oplog/replicate", auth(func(w http.ResponseWriter, r *http.Request) {
		var es []Entry
		if err := json.NewDecoder(r.Body).Decode(&es); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// A replication is contact from the leader.
		if l := n.leaderAddr(); l != "" {
			n.mu.Lock()
			n.lastContact[l] = n.o.Now()
			n.mu.Unlock()
		}
		for _, e := range es {
			fresh, err := n.store(e)
			if err != nil {
				// A gap: we are behind; catch up from the leader instead.
				go func() { _ = n.CatchUp(context.Background()) }()
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
			if fresh && n.o.Apply != nil {
				_ = n.o.Apply(e)
			}
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	return mux
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
