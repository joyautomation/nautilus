package node

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/joyautomation/nautilus/quorum"
)

// WitnessOptions configures a Witness.
type WitnessOptions struct {
	// Dir holds one state file per pair (<pair>.quorum).
	Dir string
	// Token is the peer token (NAUTILUS_PEER_TOKEN), required.
	Token string
	// Lease is this witness's promise. It must be at least every pair's
	// lease: a vote whose lease is longer is refused. Defaults to
	// DefaultLease.
	Lease time.Duration
	// Self defaults to WitnessID.
	Self quorum.NodeID
	// Rebuild lets the witness start over state files it can't read: each
	// is moved aside, and its pair gets a new incarnation, which the
	// controllers adopt only together, after its quiet period (design
	// §3.1). Without it such a file stops the start.
	Rebuild bool
	// MaxPairs caps how many pairs one witness serves (default 64).
	MaxPairs int
	Log      *slog.Logger
	// StateWrite is Options.StateWrite, for the witness's pair files.
	StateWrite func(*os.File, []byte) (int, error)

	clock func() quorum.Instant
	draw  func() uint64
	write func(*os.File, []byte) (int, error) // State.write, for tests
}

// Witness is the third voter of any number of pairs (design §6: one
// witness can serve several pairs, keyed by name). It runs only the peer
// listener (Handler, behind Guard), and answers votes; it never
// campaigns.
type Witness struct {
	o       WitnessOptions
	log     *slog.Logger
	now     func() quorum.Instant
	free    func()
	running atomic.Bool

	mu      sync.Mutex
	ctx     context.Context
	pairs   map[string]*witnessPair
	wg      sync.WaitGroup // the pairs' writers
	logInfo func(cluster string) (last, commit uint64, ok bool)
	// versionSeen: the version-mismatch report last logged per cluster.
	versionSeen map[string]string
}

type witnessPair struct {
	name     string
	members  string // "a,b": the two controllers bound to this cluster
	mu       sync.Mutex
	v        *quorum.Voter
	w        *writer
	waiters  map[waitKey]chan quorum.Reply
	failing  string
	refused  map[string]string // the last lease refusal reported per sender
	dupSeen  uint64            // the voter's Duplicates count last reported
	markSeen uint64            // the voter's MarkCount last reported
	dupAt    time.Time         // when it was last reported
	log      *slog.Logger
	// lease is the lease the pair's latest vote stated (0 before one):
	// its writes are judged by it, not by --lease (#1436). writes: the
	// pair's write times and their warnings.
	lease  time.Duration
	own    time.Duration // --lease
	writes writeNote
	now    func() quorum.Instant
}

// stateSuffix names a cluster's state file in the witness's directory;
// membersSuffix the file binding it to its two controllers.
const (
	stateSuffix   = ".quorum"
	membersSuffix = ".members"
)

var memberName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,15}$`)

// validMembers reports whether h is a MembersHeader value: two distinct
// names, sorted.
func validMembers(h string) bool {
	x, y, ok := strings.Cut(h, ",")
	return ok && memberName.MatchString(x) && memberName.MatchString(y) && x < y
}

// NewWitness opens the witness's state directory. A state file it can't
// read stops the start, unless Rebuild: then it is moved aside and the
// pair draws a new incarnation at its first vote. A pair with no file at
// all is new: a new incarnation too.
func NewWitness(o WitnessOptions) (*Witness, error) {
	if o.Token == "" {
		return nil, errors.New("quorum: the witness needs NAUTILUS_PEER_TOKEN")
	}
	if o.Dir == "" {
		return nil, errors.New("quorum: the witness needs a state directory (--state)")
	}
	if o.Lease == 0 {
		o.Lease = DefaultLease
	}
	if o.Self == "" {
		o.Self = WitnessID
	}
	if o.MaxPairs == 0 {
		o.MaxPairs = 64
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.draw == nil {
		o.draw = drawNonce
	}
	now := o.clock
	if now == nil {
		now = quorum.Now
	}
	if err := os.MkdirAll(o.Dir, 0o700); err != nil {
		return nil, err
	}
	free := func() {}
	if !mut("witness_unlocked") {
		f, err := lock(filepath.Join(o.Dir, ".lock"))
		if err != nil {
			return nil, err
		}
		free = f
	}
	w := &Witness{o: o, log: o.Log, now: now, free: free, pairs: map[string]*witnessPair{}}
	files, _ := filepath.Glob(filepath.Join(o.Dir, "*"+stateSuffix))
	sort.Strings(files)
	var bad []string
	for _, f := range files {
		s := &State{Path: f}
		_, err := s.Load()
		if err == nil || errors.Is(err, ErrNoState) {
			continue
		}
		if !o.Rebuild && !mut("witness_rebuilds_silently") {
			bad = append(bad, err.Error())
			continue
		}
		aside := fmt.Sprintf("%s.unreadable-%d", f, time.Now().Unix())
		if err := os.Rename(f, aside); err != nil {
			free()
			return nil, err
		}
		w.log.Warn("witness: state moved aside; the pair gets a new incarnation, which its controllers adopt together after a quiet period", "pair", strings.TrimSuffix(filepath.Base(f), stateSuffix), "kept", aside)
	}
	if len(bad) > 0 {
		free()
		return nil, fmt.Errorf("quorum: the witness's state can't be read (%s). Start it with --rebuild to draw a new incarnation for each such pair; its controllers then adopt it together, and if one of them has lost its own state too, an operator re-commissions the pair (design §7)", strings.Join(bad, "; "))
	}
	return w, nil
}

// Run answers votes until ctx ends, and returns once every pair's writer
// has finished. Run it once.
func (w *Witness) Run(ctx context.Context) {
	w.mu.Lock()
	w.ctx = ctx
	w.mu.Unlock()
	w.running.Store(true)
	<-ctx.Done()
	w.mu.Lock() // no pair starts after this (pair checks ctx under mu)
	w.running.Store(false)
	w.mu.Unlock()
	w.wg.Wait()
}

// Close releases the state directory's lock. Call it after Run returned.
func (w *Witness) Close() {
	if w.free != nil {
		w.free()
		w.free = nil
	}
}

// Handler serves votes for every cluster: POST Prefix + cluster + "/vote",
// and the operator's clear-duplicate check and clear from a pair member:
// POST Prefix + cluster + "/dup" (#1029 3c-3a).
// Mount it behind Guard.
func (w *Witness) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+Prefix+"{cluster}/vote", w.serveVote)
	mux.HandleFunc("POST "+Prefix+"{cluster}/dup", w.serveDup)
	return mux
}

// PairStatus is one pair's state at the witness.
type PairStatus struct {
	Cluster     string
	Members     string
	Incarnation uint64
	Promised    uint64
	GrantedTo   string
	// The pair's operator-intent log on this witness (SetLogInfo): its
	// last entry and its commit index; HasLog false when it has none open.
	HasLog             bool
	LogLast, LogCommit uint64
	// Duplicates counts votes this witness refused because two processes
	// claim one member's name (redundancy.duplicate-member).
	Duplicates uint64
}

// SetLogInfo lets Pairs report each pair's operator-intent log (#710
// 3b-4b): the log is composed outside this package (cmd/naut), which
// knows it. Call it before Run.
func (w *Witness) SetLogInfo(f func(cluster string) (last, commit uint64, ok bool)) {
	w.mu.Lock()
	w.logInfo = f
	w.mu.Unlock()
}

// Bound reports the two controllers ("a,b") the cluster is bound to, and
// whether it is bound: it has had a vote, now or before a restart (its
// members file). Nothing here binds a cluster: only a vote does.
func (w *Witness) Bound(cluster string) (string, bool) {
	if !ValidCluster(cluster) {
		return "", false
	}
	w.mu.Lock()
	p, ok := w.pairs[cluster]
	w.mu.Unlock()
	if ok {
		return p.members, true
	}
	b, err := os.ReadFile(filepath.Join(w.o.Dir, cluster+membersSuffix))
	if err != nil {
		return "", false
	}
	m := strings.TrimSpace(string(b))
	return m, validMembers(m)
}

// Grant is the cluster's voter's state: whom it last granted, at which
// vote term, and its incarnation. ok is false when the cluster has had no
// vote since this witness started (its voter is not running): the pair's
// next renewal starts it. The log path never starts a voter.
func (w *Witness) Grant(cluster string) (grantedTo quorum.NodeID, promised, incarnation uint64, ok bool) {
	w.mu.Lock()
	p, found := w.pairs[cluster]
	w.mu.Unlock()
	if !found {
		return "", 0, 0, false
	}
	p.mu.Lock()
	st := p.v.State()
	p.mu.Unlock()
	return st.GrantedTo, st.Promised, st.Incarnation, st.Incarnation != 0
}

// Incarnation is the cluster's incarnation at this witness, 0 when it has
// none yet (no vote ever, or its state rebuilt and no vote since): what
// the witness's peer TLS certificate names to that pair's controllers
// (#710 2b). Read only: it never starts a voter or binds a cluster, since
// a handshake asking it has proved nothing yet.
func (w *Witness) Incarnation(cluster string) uint64 {
	if !ValidCluster(cluster) {
		return 0
	}
	w.mu.Lock()
	p, ok := w.pairs[cluster]
	w.mu.Unlock()
	if ok {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.v.State().Incarnation
	}
	st, err := (&State{Path: filepath.Join(w.o.Dir, cluster+stateSuffix)}).Load()
	if err != nil || st.Cluster != cluster {
		return 0
	}
	return st.Incarnation
}

// Pairs lists the pairs this witness has answered since it started.
func (w *Witness) Pairs() []PairStatus {
	w.mu.Lock()
	ps := make([]*witnessPair, 0, len(w.pairs))
	for _, p := range w.pairs {
		ps = append(ps, p)
	}
	info := w.logInfo
	w.mu.Unlock()
	out := make([]PairStatus, 0, len(ps))
	for _, p := range ps {
		p.mu.Lock()
		st := p.v.State()
		dups, _ := p.v.Duplicates()
		p.mu.Unlock()
		ps := PairStatus{Cluster: p.name, Members: p.members, Incarnation: st.Incarnation, Promised: st.Promised, GrantedTo: string(st.GrantedTo), Duplicates: dups}
		if info != nil {
			ps.LogLast, ps.LogCommit, ps.HasLog = info(p.name)
		}
		out = append(out, ps)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Cluster < out[j].Cluster })
	return out
}

// errMembers: a vote names other members than the cluster is bound to.
var errMembers = errors.New("this cluster is bound to other controllers")

// pair returns the cluster's voter, starting it from its state file at its
// first vote. A cluster is bound to its two controllers (members) at its
// first vote, durably, before its voter exists; a vote naming others is
// refused (errMembers).
func (w *Witness) pair(name, members string) (*witnessPair, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if p, ok := w.pairs[name]; ok {
		if p.members != members {
			return nil, errMembers
		}
		return p, nil
	}
	if w.ctx == nil || w.ctx.Err() != nil {
		return nil, errors.New("not running")
	}
	if len(w.pairs) >= w.o.MaxPairs {
		return nil, fmt.Errorf("this witness serves %d pairs already", w.o.MaxPairs)
	}
	mpath := filepath.Join(w.o.Dir, name+membersSuffix)
	switch b, err := os.ReadFile(mpath); {
	case err == nil && strings.TrimSpace(string(b)) != members:
		return nil, errMembers
	case errors.Is(err, fs.ErrNotExist):
		if err := writeAtomic(mpath, []byte(members+"\n"), nil); err != nil {
			return nil, err
		}
	case err != nil:
		return nil, err
	}
	file := &State{Path: filepath.Join(w.o.Dir, name+stateSuffix), Owner: w.o.Self, Cluster: name, write: firstWrite(w.o.write, w.o.StateWrite)}
	st, err := file.Load()
	if err != nil && !errors.Is(err, ErrNoState) {
		// Unreadable since the start checked it: never redrawn quietly.
		return nil, err
	}
	if err == nil {
		if err := file.CheckOwner(st); err != nil {
			return nil, err // another pair's file under this one's name
		}
	}
	p := &witnessPair{name: name, members: members, waiters: map[waitKey]chan quorum.Reply{},
		refused: map[string]string{}, log: w.log.With("cluster", name, "members", members), now: w.now, own: w.o.Lease}
	v, first := quorum.NewWitness(w.o.Self, w.o.Lease, st, w.now(), w.o.draw)
	v.SetBoot(w.o.draw()) // #1113 review: a hold ends only on this boot's word
	p.v = v
	p.w = newWriter(file.Save, p.persisted, w.now)
	w.wg.Add(1)
	go func() { defer w.wg.Done(); p.w.run(w.ctx) }()
	if first != nil {
		p.log.Info("witness: new incarnation for this pair; its controllers adopt it together, after a quiet period", "incarnation", v.State().Incarnation)
		p.w.ask(first)
	}
	w.pairs[name] = p
	return p, nil
}

// controllers are the pair's two controller names.
func (p *witnessPair) controllers() []quorum.NodeID {
	var out []quorum.NodeID
	for _, n := range strings.Split(p.members, ",") {
		out = append(out, quorum.NodeID(n))
	}
	return out
}

func (p *witnessPair) persisted(seq uint64, err error, took time.Duration) {
	p.mu.Lock()
	var missed, hit map[quorum.NodeID]bool
	for _, r := range p.v.Persisted(seq, err == nil || mut("witness_write_ignored")) {
		if ch, ok := p.waiters[keyFor(r)]; ok {
			delete(p.waiters, keyFor(r))
			ch <- r
			if hit == nil {
				hit = map[quorum.NodeID]bool{}
			}
			hit[r.To] = true
		} else if r.Granted {
			if missed == nil {
				missed = map[quorum.NodeID]bool{}
			}
			missed[r.To] = true
		}
	}
	// Judged by the pair's own lease: --lease is only the longest any
	// pair may have.
	lease := p.lease
	if mut("witness_write_by_own_lease") {
		lease = p.own
	}
	if lease > 0 {
		now := p.now()
		if warn, n, worst := p.writes.took(now, took, lease); warn {
			p.log.Warn(slowWriteMsg("witness", n, worst, lease))
		}
		for range lateGrants(missed, hit) {
			if warn, n := p.writes.lateGrant(now); warn {
				p.log.Warn(fmt.Sprintf("witness: granted %d vote(s) of this pair only after the asker had stopped waiting: the state write (%v) outlasted the vote's wait, "+
					"so that campaign is lost", n, took.Round(time.Millisecond)), "lease", lease)
			}
		}
	}
	switch {
	case err != nil && p.failing == "":
		p.failing = err.Error()
		p.log.Error("witness: the pair's state can't be written: this witness grants it nothing until a write succeeds", "err", err)
	case err == nil && p.failing != "":
		p.failing = ""
		p.log.Info("witness: the pair's state is written again")
	}
	p.mu.Unlock()
}

func (w *Witness) serveVote(rw http.ResponseWriter, r *http.Request) {
	rw.Header().Set(VoterLeaseHeader, strconv.FormatInt(int64(w.o.Lease), 10))
	name := r.PathValue("cluster")
	if !ValidCluster(name) {
		http.NotFound(rw, r)
		return
	}
	if !w.running.Load() {
		http.Error(rw, "not running", http.StatusServiceUnavailable)
		return
	}
	b, err := readFrame(r)
	var req quorum.Request
	if err == nil && errors.Is(req.UnmarshalBinary(b), quorum.ErrVersion) {
		msg := fmt.Sprintf("a controller of cluster %s speaks vote protocol version %d and this witness %d: run one release on both controllers and the witness", name, quorum.FrameVersion(b), quorum.WireVersion)
		w.mu.Lock()
		if w.versionSeen[name] != msg {
			if w.versionSeen == nil {
				w.versionSeen = map[string]string{}
			}
			w.versionSeen[name] = msg
			w.log.Error("witness: " + msg)
		}
		w.mu.Unlock()
		refuse(rw, "version", msg)
		return
	}
	if err != nil || req.UnmarshalBinary(b) != nil {
		http.Error(rw, "bad frame", http.StatusBadRequest)
		return
	}
	members := r.Header.Get(MembersHeader)
	if !SenderOK(r, name, string(req.From), "witness_sender_unbound_vote") {
		// The vote names another member, or another cluster, than the
		// sender's certificate (#710 per-box keys): a pinned a must never
		// vote as b, nor in a cluster it is not a member of.
		w.log.Error("witness: refused a vote whose sender is not the member its certificate names", "cluster", name, "from", req.From)
		http.Error(rw, "the vote's sender is not the member this connection's certificate names", http.StatusForbidden)
		return
	}
	if !validMembers(members) || (members != memberPair(req.From, members)) {
		w.log.Error("witness: refused a vote from outside the pair", "cluster", name, "from", req.From, "members", members)
		http.Error(rw, "not a member of this pair", http.StatusForbidden)
		return
	}
	p, err := w.pair(name, members)
	if errors.Is(err, errMembers) {
		w.log.Error("witness: refused a vote naming other members than the cluster is bound to", "cluster", name, "from", req.From, "members", members)
		http.Error(rw, err.Error(), http.StatusForbidden)
		return
	}
	if err != nil {
		w.log.Error("witness: refusing a cluster", "cluster", name, "err", err)
		http.Error(rw, err.Error(), http.StatusServiceUnavailable)
		return
	}
	stated := statedLease(r)
	p.mu.Lock()
	now := w.now()
	if limit := p.v.MaxRequestLease(now); !leaseFits(stated, limit) && !mut("lease_unchecked") {
		msg := fmt.Sprintf("%s's lease %v is longer than this witness's %v (or missing)", req.From, stated, limit)
		if p.refused[string(req.From)] != msg {
			p.refused[string(req.From)] = msg
			p.log.Error("witness: refused a vote for its lease: the pair's lease is longer than --lease", "detail", msg)
		}
		p.mu.Unlock()
		refuse(rw, "lease", msg)
		return
	}
	if stated > 0 {
		p.lease = stated
	}
	if st := p.v.State(); termJump(req.Term, st.Promised, st.Floor) && !mut("no_term_cap") {
		p.mu.Unlock()
		p.log.Error("witness: refused a vote that would jump the terms", "from", req.From, "term", req.Term, "promised", st.Promised)
		refuse(rw, "term-jump", fmt.Sprintf("term %d is more than %d above the %d this witness promised", req.Term, uint64(MaxTermJump), max(st.Promised, st.Floor)))
		return
	}
	if !p.v.FencedRequest(req) {
		p.v.Hear(now, req.From, req.Nonce, req.Promise) // a fenced state's relay is not heard (#1029 3c-3c-1b)
		p.v.LearnFences(req.Fences, p.controllers()...) // a rebuilt witness learns them (its write goes with Handle's)
	}
	persist, replies := p.v.Handle(now, req)
	if msg := confirmedAlarm(p.v, &p.markSeen); msg != "" {
		p.dupSeen, _ = p.v.Duplicates() // the mark's own count, reported here
		p.log.Error("witness: " + msg)
	}
	if n, who := p.v.Duplicates(); n != p.dupSeen {
		p.dupSeen = n
		if t := time.Now(); t.Sub(p.dupAt) >= dupEvery {
			p.dupAt = t
			p.log.Error("witness: " + duplicateMsg(who, p.v.DuplicateCause()))
		}
	}
	var ch chan quorum.Reply
	if len(replies) == 0 && persist != nil {
		ch = make(chan quorum.Reply, 1)
		p.waiters[keyOf(req)] = ch
	}
	p.mu.Unlock()
	p.w.ask(persist)
	stamp := func(rep quorum.Reply) quorum.Reply {
		p.mu.Lock()
		rep.Promise = p.v.Relay(w.now())
		p.mu.Unlock()
		return rep
	}
	answer(rw, r, replies, ch, w.o.Lease, stamp, func() {
		p.mu.Lock()
		if p.waiters[keyOf(req)] == ch {
			delete(p.waiters, keyOf(req))
		}
		p.mu.Unlock()
	})
}

// witnessSenderOK reports whether a request to the witness about cluster,
// whose members are members, comes from one of them by its certificate
// (SenderOK for a request that names no sender).
func witnessSenderOK(r *http.Request, cluster, members, mutation string) bool {
	if r.TLS == nil || mut(mutation) {
		return true
	}
	c, n, ok := VerifiedPeer(r)
	return ok && c == cluster && slices.Contains(strings.Split(members, ","), n)
}

// memberPair returns members when from is one of its two names, else "".
func memberPair(from quorum.NodeID, members string) string {
	x, y, _ := strings.Cut(members, ",")
	if string(from) == x || string(from) == y {
		return members
	}
	return ""
}
