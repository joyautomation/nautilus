package node

import (
	"context"
	crand "crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/joyautomation/nautilus/quorum"
)

// Defaults (design §2).
const (
	DefaultLease = 2 * time.Second
	DefaultRho   = 0.01
	// WitnessID is the witness's name in the vote protocol: one witness
	// per pair, so it needs no other.
	WitnessID quorum.NodeID = "w"
)

// bootClock is quorum.BootClock; a test flips it to check the refusal.
var bootClock = quorum.BootClock

// Options configures a controller's Elector.
type Options struct {
	// Cluster is the pair's cluster ID (NewClusterID): drawn once, when
	// the pair is commissioned, and kept in both controllers' state
	// directories. The witness keeps one voter per cluster, bound to the
	// pair's two names, so pairs never share one (not even two sites that
	// run one project).
	Cluster string
	// Self and Peer name the two controllers (NAUTILUS_NODE: a or b);
	// Witness defaults to WitnessID.
	Self, Peer, Witness quorum.NodeID
	// PeerAddr and WitnessAddr are where their peer listeners are:
	// host:port, or a base URL.
	PeerAddr, WitnessAddr string
	// Lease defaults to DefaultLease, Rho to DefaultRho.
	Lease time.Duration
	Rho   float64
	// StatePath is this controller's voter state file. It belongs in the
	// controller's state directory, never in the project tree: a deploy
	// replaces the project, and a lost file needs the peer, or an
	// operator, to re-commission.
	StatePath string
	// Token is the peer token (NAUTILUS_PEER_TOKEN), required.
	Token string
	// ClientToken is the tag API's token (NAUTILUS_TOKEN). It never leaves
	// this process: only an HMAC of it, for the peer to compare (design
	// §6.1).
	ClientToken string
	// SelfAPI and PeerAPI are the two controllers' tag API addresses
	// (host:port): Status.LeaderAddr names the leader's, for a standby's
	// proxy and for clients that follow the leader.
	SelfAPI, PeerAPI string
	// Log defaults to slog.Default.
	Log *slog.Logger
	// OnEvent, when set, gets every Event, in order, from one goroutine.
	OnEvent func(Event)

	// For tests: the clock (quorum.Now), the HTTP client, and the state
	// file's write.
	clock  func() quorum.Instant
	client *http.Client
	write  func(*os.File, []byte) (int, error)
}

// EventKind names an Event. The names follow design §7.1's audit actions.
type EventKind string

const (
	// EventAdopted: this controller durably adopted (Gen, Incarnation).
	EventAdopted EventKind = "redundancy.witness.incarnation"
	// EventTwoCopies: two witness incarnations answered within a lease:
	// two copies of the witness are running.
	EventTwoCopies EventKind = "redundancy.witness.copies"
	// EventRecommissionNeeded: this controller can't re-commission on its
	// own; an operator must, with a term floor (quorum.EventRecommission).
	EventRecommissionNeeded EventKind = "redundancy.witness.lost"
	// EventReadoptOverSwitch: quorum.EventReadoptOverSwitch.
	EventReadoptOverSwitch EventKind = "redundancy.witness.readopt-over-switch"
	// EventRecommissioned: an operator's Recommission was made durable.
	EventRecommissioned EventKind = "redundancy.recommission"
	// EventClientToken: the client-token check changed its answer
	// (Detail: same or differs).
	EventClientToken EventKind = "redundancy.client-token"
	// EventStateWrite: the state file could not be written (Detail: the
	// error); this voter grants nothing until a write succeeds. Raised
	// once per failing run, and again with Detail "ok" when it recovers.
	EventStateWrite EventKind = "redundancy.state-write"
	// EventLeaseMismatch: a voter refused a vote because its stated lease
	// is longer than the voter's (Detail says which voter and both
	// leases). The pair's members (or the witness's --lease) disagree.
	// Raised on each side once per change.
	EventLeaseMismatch EventKind = "redundancy.lease-mismatch"
)

// Event is something the operator should see.
type Event struct {
	Kind             EventKind
	Gen, Incarnation uint64
	Other            uint64 // EventTwoCopies, EventReadoptOverSwitch
	Detail           string
}

// Status is a snapshot of the elector, for the operator surfaces.
type Status struct {
	Self, Peer string
	// Leading reports the gate (IsLeader); Term is the last fencing token
	// this controller led under (Elector.Term).
	Leading bool
	Term    uint64
	// Leader is who leads as this controller sees it: itself, the peer
	// (seen holding a lease), or "". LeaderAddr is its tag API address
	// (Options.SelfAPI or PeerAPI), "" when none leads or it is unset.
	Leader     string
	LeaderAddr string
	// Commissioned: an incarnation adopted. A controller that isn't grants
	// nothing (a new pair, or a lost file).
	Commissioned           bool
	Gen, Incarnation       uint64
	PendingGen, PendingInc uint64
	Floor, KnownFloor      uint64
	// WitnessIncarnation is the last incarnation the witness answered
	// with, WitnessAge how long ago (-1: never in this boot).
	WitnessIncarnation uint64
	WitnessAge         time.Duration
	// PeerAge is how long ago the peer was last heard (-1: never).
	PeerAge time.Duration
	// RecommissionNeeded: EventRecommissionNeeded was raised and this
	// controller is still uncommissioned.
	RecommissionNeeded bool
	ClientToken        ClientToken
	// StateWriteError is the last state write's error, "" while writes
	// succeed.
	StateWriteError string
	// Acquisitions counts the leaderships this boot has won.
	Acquisitions int
}

// Elector is a controller's side of the vote: its voter, its candidate and
// its side of the witness-incarnation handshake, driven over the peer
// listener (Handler, behind Guard) and the state file.
type Elector struct {
	o     Options
	log   *slog.Logger
	now   func() quorum.Instant
	file  *State
	free  func()
	call  caller
	peer  string // base URLs
	wit   string
	nonce uint64

	mu        sync.Mutex
	v         *quorum.Voter
	c         *quorum.Candidate
	a         *quorum.Adoption
	health    func() bool
	released  bool
	unhealthy bool
	waiters   map[waitKey]chan quorum.Reply
	epoch     uint64 // the last epoch counted in acquisitions
	acquired  int
	witInc    uint64
	witAt     quorum.Instant
	witHeard  bool
	peerAt    quorum.Instant
	peerHeard bool
	needOp    bool
	token     ClientToken
	writeErr  string
	mismatch  map[string]string // the last lease-mismatch report per voter, to report each change once
	durable   uint64            // the highest Seq known on disk
	written   chan struct{}     // closed and replaced at every write report

	gate    atomic.Pointer[gateState]
	wake    chan struct{}
	w       *writer
	running atomic.Bool
	ctx     context.Context // Run's: sends end with it
	events  chan Event
	decided chan struct{}
	once    sync.Once
}

// gateState is what the write gate is judged on, published after every
// call into the protocol and read without the lock (IsLeader). The lease
// end is judged when the gate is READ, not when it was published.
type gateState struct {
	epoch uint64         // the last fencing token led under (only rises)
	end   quorum.Instant // the lease end, on this node's clock (0: none)
	open  bool           // healthy, not released, and leading when published
}

// New starts a controller's elector from its state file: its voter waits
// one lease before it grants anything, and its candidate draws a fresh
// boot nonce. A missing or unreadable state file starts it uncommissioned:
// it grants nothing and re-adopts its peer's incarnation (or waits for an
// operator, Recommission). Controllers run on CLOCK_BOOTTIME only, so New
// refuses any other platform.
func New(o Options) (*Elector, error) {
	if o.Witness == "" {
		o.Witness = WitnessID
	}
	if o.Lease == 0 {
		o.Lease = DefaultLease
	}
	if o.Rho == 0 {
		o.Rho = DefaultRho
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	switch {
	case o.clock == nil && !bootClock:
		return nil, errors.New("quorum: a redundant controller needs CLOCK_BOOTTIME (Linux): a lease must keep counting through a host suspend")
	case !ValidCluster(o.Cluster):
		return nil, fmt.Errorf("quorum: cluster ID %q: want 32 lowercase hex digits, drawn at commissioning", o.Cluster)
	case o.Token == "":
		return nil, errors.New("quorum: a pair member needs NAUTILUS_PEER_TOKEN")
	case o.PeerAddr == "" || o.WitnessAddr == "":
		return nil, errors.New("quorum: the peer's and the witness's addresses are both needed")
	case o.StatePath == "":
		return nil, errors.New("quorum: a state file path is needed")
	}
	now := o.clock
	if now == nil {
		now = quorum.Now
	}
	client := o.client
	if client == nil {
		client = newClient()
	}
	e := &Elector{
		o: o, log: o.Log.With("cluster", o.Cluster, "node", string(o.Self)), now: now,
		file: &State{Path: o.StatePath, write: o.write},
		call: caller{client: client, token: o.Token, lease: o.Lease, members: members(o.Self, o.Peer)},
		peer: baseURL(o.PeerAddr), wit: baseURL(o.WitnessAddr), nonce: drawNonce(),
		waiters: map[waitKey]chan quorum.Reply{}, written: make(chan struct{}), mismatch: map[string]string{},
		wake: make(chan struct{}, 1), events: make(chan Event, 64), decided: make(chan struct{}),
	}
	free := func() {}
	if !mut("controller_unlocked") {
		f, err := lock(o.StatePath + ".lock")
		if err != nil {
			return nil, err
		}
		free = f
	}
	st, err := e.file.Load()
	switch {
	case errors.Is(err, ErrNoState):
		e.log.Info("redundancy: no voter state: this controller is uncommissioned (a new pair, or a wiped disk) and grants nothing until it adopts the pair's witness incarnation", "file", o.StatePath)
	case err != nil:
		aside := fmt.Sprintf("%s.unreadable-%d", o.StatePath, time.Now().Unix())
		_ = os.Rename(o.StatePath, aside)
		e.log.Error("redundancy: the voter state can't be read: this controller is uncommissioned and grants nothing until it re-adopts the pair's witness incarnation, from its peer or an operator", "file", o.StatePath, "kept", aside, "err", err)
	}
	cfg := quorum.Config{Self: o.Self, Peer: o.Peer, Witness: o.Witness, Lease: o.Lease, Rho: o.Rho,
		Incarnation: st.Incarnation, Nonce: e.nonce, Jitter: jitter(o.Lease)}
	t := now()
	e.v = quorum.NewVoter(o.Self, o.Lease, st, t)
	c, err := quorum.NewCandidate(cfg, st.Promised, t)
	if err != nil {
		free()
		return nil, err
	}
	e.c = c
	e.a = quorum.NewAdoption(e.v, c, t)
	e.free = free
	e.w = newWriter(e.file.Save, e.persisted)
	e.publish(t)
	return e, nil
}

// drawNonce draws a boot nonce: random, non-zero, never persisted.
func drawNonce() uint64 {
	if mut("nonce_fixed") {
		return 1
	}
	for {
		var b [8]byte
		if _, err := crand.Read(b[:]); err != nil {
			panic("quorum: crypto/rand: " + err.Error())
		}
		if n := binary.BigEndian.Uint64(b[:]); n != 0 {
			return n
		}
	}
}

// jitter returns the candidate's retry jitter in [0, lease/4), seeded at
// random per boot.
func jitter(lease time.Duration) func() time.Duration {
	r := rand.New(rand.NewPCG(drawNonce(), drawNonce()))
	var mu sync.Mutex
	return func() time.Duration {
		mu.Lock()
		defer mu.Unlock()
		return time.Duration(r.Int64N(int64(lease / 4)))
	}
}

// Lease returns the lease this elector runs.
func (e *Elector) Lease() time.Duration { return e.o.Lease }

// Nonce returns this boot's nonce (for tests and the status page; it is
// not a secret, and changes every start).
func (e *Elector) Nonce() uint64 { return e.nonce }

// SetHealth makes renewing and campaigning depend on h (#621): a look
// that finds h false closes the gate at once and sends no renewal, so the
// lease lapses, and a replica that is not live never campaigns. Call it
// before Run.
func (e *Elector) SetHealth(h func() bool) {
	e.mu.Lock()
	e.health = h
	e.mu.Unlock()
}

// IsLeader reports whether the write gate is open: this controller holds a
// majority lease, judged against the clock now (not at the elector's last
// look, so a stalled elector can't hold the gate open past the lease), and
// its scans were live at its last look, and it hasn't released.
func (e *Elector) IsLeader() bool {
	g := e.gate.Load()
	if mut("gate_on_tick") {
		return g.open
	}
	return g.open && e.now() < g.end
}

// Term returns the last fencing token this controller led under: the term
// its current or last lease was acquired in, stable across renewals, 0
// before it first led. It only rises, as the Lease elector's does (the
// runtime's channels rely on it). A new lease after any gap has a new
// term, so the runtime takes over rather than resumes; a gate closed for
// unhealthy scans and opened again within the same lease keeps it, and
// the runtime resumes.
func (e *Elector) Term() uint64 { return e.gate.Load().epoch }

// Decided is closed once this controller leads, or has seen the peer
// leading.
func (e *Elector) Decided() <-chan struct{} { return e.decided }

// Release closes the gate and stops renewing and campaigning, so the lease
// lapses and the peer takes over one lease later (after a stop: the voter
// still answers while the process lives).
func (e *Elector) Release() {
	e.mu.Lock()
	e.released = true
	e.publish(e.now())
	e.mu.Unlock()
}

// Run drives the elector until ctx ends: the disk writer, the looks, and
// the client-token check. Messages are answered only while it runs, and it
// returns once its writer has finished. Run it once.
func (e *Elector) Run(ctx context.Context) {
	e.mu.Lock()
	if e.ctx != nil {
		e.mu.Unlock()
		return
	}
	e.ctx = ctx
	e.mu.Unlock()
	// Run returns only once its writer is done: nothing touches the state
	// file after it.
	var wg sync.WaitGroup
	for _, f := range []func(context.Context){e.w.run, e.emit, e.hello} {
		wg.Add(1)
		go func() { defer wg.Done(); f(ctx) }()
	}
	defer wg.Wait()
	e.running.Store(true)
	defer e.running.Store(false)
	t := time.NewTimer(0)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-e.wake:
		}
		t.Reset(e.look())
	}
}

// Close releases the state file's lock. Call it after Run has returned.
func (e *Elector) Close() {
	if e.free != nil {
		e.free()
		e.free = nil
	}
}

// poke asks the loop for a look now: a reply may have won a lease, which
// renews at once, or moved the next look earlier.
func (e *Elector) poke() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

// look is one look of the candidate and the handshake. It returns how long
// until the next.
func (e *Elector) look() time.Duration {
	e.mu.Lock()
	h := e.health
	e.mu.Unlock()
	healthy := h == nil || h()

	e.mu.Lock()
	now := e.now()
	from, until := e.v.LeaderSeen()
	e.unhealthy = !healthy
	req := e.c.Tick(now, from, until, healthy && !e.released)
	p, msgs, probe := e.a.Tick(now)
	var selfP *quorum.Persist
	if req != nil {
		// Its own voter first, in process.
		var rs []quorum.Reply
		selfP, rs = e.v.Handle(now, *req)
		e.route(now, rs)
	}
	e.after(now)
	next := e.c.NextLook().Sub(now)
	e.mu.Unlock()

	e.w.ask(p)
	e.w.ask(selfP)
	if req != nil {
		e.vote(e.o.Peer, e.peer, *req)
		e.vote(e.o.Witness, e.wit, *req)
	}
	e.adopts(msgs)
	if probe != nil {
		e.vote(e.o.Witness, e.wit, *probe)
	}
	return min(max(next, 0), e.o.Lease/8)
}

// route delivers replies the voter released: to its own candidate, or to
// the waiting request of the peer. Under mu.
func (e *Elector) route(now quorum.Instant, rs []quorum.Reply) {
	for _, r := range rs {
		if r.To == e.o.Self {
			e.c.OnReply(now, r)
			continue
		}
		if ch, ok := e.waiters[keyFor(r)]; ok {
			delete(e.waiters, keyFor(r))
			ch <- r
		}
	}
}

// after drains the handshake's events and publishes the gate. Under mu.
func (e *Elector) after(now quorum.Instant) {
	for _, ev := range e.a.Events() {
		var out Event
		switch ev.Kind {
		case quorum.EventAdopted:
			e.needOp = false
			out = Event{Kind: EventAdopted, Gen: ev.Gen, Incarnation: ev.Incarnation}
			e.log.Info("redundancy: witness incarnation adopted", "gen", ev.Gen, "incarnation", ev.Incarnation)
		case quorum.EventTwoIncarnations:
			out = Event{Kind: EventTwoCopies, Incarnation: ev.Incarnation, Other: ev.Other}
			e.log.Error("redundancy: two witness incarnations answer: two copies of the witness are running", "incarnation", ev.Incarnation, "other", ev.Other)
		case quorum.EventRecommission:
			e.needOp = true
			out = Event{Kind: EventRecommissionNeeded, Gen: ev.Gen, Incarnation: ev.Incarnation}
			e.log.Error("redundancy: re-commission by hand: this controller is uncommissioned and its peer's committed witness incarnation has not answered it (or the peer is uncommissioned too); see the operator guide before acting", "peerGen", ev.Gen, "peerIncarnation", ev.Incarnation)
		case quorum.EventReadoptOverSwitch:
			out = Event{Kind: EventReadoptOverSwitch, Gen: ev.Gen, Incarnation: ev.Incarnation, Other: ev.Other}
			e.log.Warn("redundancy: re-adopted the peer's incarnation over a pending switch", "gen", ev.Gen, "incarnation", ev.Incarnation, "pending", ev.Other)
		default:
			continue
		}
		e.event(out)
	}
	e.publish(now)
	if t := e.c.Term(); e.c.Leading(now) && t != e.epoch {
		e.epoch = t
		e.acquired++
		e.log.Info("redundancy: this controller leads", "term", t)
	}
	if from, until := e.v.LeaderSeen(); e.c.Leading(now) || (from == e.o.Peer && now < until) {
		e.once.Do(func() { close(e.decided) })
	}
}

// publish stores the gate. Under mu.
func (e *Elector) publish(now quorum.Instant) {
	g := &gateState{}
	if old := e.gate.Load(); old != nil {
		g.epoch = old.epoch
	}
	if e.c.Leading(now) {
		g.epoch, g.end = max(g.epoch, e.c.Term()), e.c.LeaseEnd()
		g.open = (!e.released && !e.unhealthy) || mut("gate_ignores_health")
	}
	e.gate.Store(g)
}

// event queues ev for OnEvent. Under mu; never blocks.
func (e *Elector) event(ev Event) {
	select {
	case e.events <- ev:
	default:
		e.log.Warn("redundancy: event dropped (the event handler is behind)", "kind", ev.Kind)
	}
}

func (e *Elector) emit(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-e.events:
			if e.o.OnEvent != nil {
				e.o.OnEvent(ev)
			}
		}
	}
}

// persisted is the writer's report of the write of seq.
func (e *Elector) persisted(seq uint64, err error) {
	e.mu.Lock()
	now := e.now()
	replies, msgs := e.a.Persisted(now, seq, err == nil || mut("controller_write_ignored"))
	e.route(now, replies)
	switch {
	case err != nil && e.writeErr == "":
		e.writeErr = err.Error()
		e.event(Event{Kind: EventStateWrite, Detail: e.writeErr})
		e.log.Error("redundancy: the voter state can't be written: this controller grants nothing until a write succeeds", "file", e.o.StatePath, "err", err)
	case err == nil && e.writeErr != "":
		e.writeErr = ""
		e.event(Event{Kind: EventStateWrite, Detail: "ok"})
		e.log.Info("redundancy: the voter state is written again", "file", e.o.StatePath)
	}
	if err == nil && seq > e.durable {
		e.durable = seq
	}
	close(e.written)
	e.written = make(chan struct{})
	e.after(now)
	e.mu.Unlock()
	e.adopts(msgs)
	e.poke()
}

// vote sends r to a voter and hands its reply back.
func (e *Elector) vote(to quorum.NodeID, base string, r quorum.Request) {
	ctx := e.ctx
	if ctx == nil {
		return
	}
	b, _ := r.MarshalBinary()
	go func() {
		body, err := e.call.call(ctx, e.o.Lease/8, http.MethodPost, base+Prefix+e.o.Cluster+"/vote", b)
		var se *statusError
		if errors.As(err, &se) && se.code == http.StatusConflict {
			e.mismatched(string(to), se.msg)
		}
		if err != nil || body == nil {
			return
		}
		var rep quorum.Reply
		if rep.UnmarshalBinary(body) != nil || rep.From != to {
			return // a voter answers as itself, or not at all
		}
		e.reply(to, rep)
	}()
}

// reply takes a voter's reply to this controller's request.
func (e *Elector) reply(from quorum.NodeID, r quorum.Reply) {
	e.mu.Lock()
	now := e.now()
	e.c.OnReply(now, r)
	switch from {
	case e.o.Peer:
		e.a.OnPeer(now)
		e.peerAt, e.peerHeard = now, true
	case e.o.Witness:
		e.a.OnWitness(now, r)
		if r.To == e.o.Self && r.Incarnation != 0 {
			e.witInc, e.witAt, e.witHeard = r.Incarnation, now, true
		}
	}
	e.after(now)
	e.mu.Unlock()
	e.poke()
}

// mismatched reports a lease refusal between this controller and voter,
// sent or received: logged and raised once per change of what it says.
func (e *Elector) mismatched(voter, msg string) {
	e.mu.Lock()
	changed := e.mismatch[voter] != msg
	e.mismatch[voter] = msg
	if changed {
		e.event(Event{Kind: EventLeaseMismatch, Detail: msg})
	}
	e.mu.Unlock()
	if changed {
		e.log.Error("redundancy: a vote was refused for its lease: the pair's leases (or the witness's --lease) disagree", "voter", voter, "detail", msg)
	}
}

// adopts sends handshake messages to the peer.
func (e *Elector) adopts(msgs []quorum.Adopt) {
	ctx := e.ctx
	if ctx == nil {
		return
	}
	for _, m := range msgs {
		b, _ := m.MarshalBinary()
		go func() {
			_, _ = e.call.call(ctx, e.o.Lease/8, http.MethodPost, e.peer+Prefix+e.o.Cluster+"/adopt", b)
		}()
	}
}

// Handler serves the vote protocol to the peer. Mount it on the peer
// listener behind Guard, never behind the tag API.
func (e *Elector) Handler() http.Handler {
	mux := http.NewServeMux()
	base := Prefix + e.o.Cluster
	mux.HandleFunc("POST "+base+"/vote", e.serveVote)
	mux.HandleFunc("POST "+base+"/adopt", e.serveAdopt)
	mux.HandleFunc("GET "+base+"/hello", e.serveHello)
	return mux
}

func (e *Elector) serveVote(w http.ResponseWriter, r *http.Request) {
	if !e.running.Load() {
		http.Error(w, "not running", http.StatusServiceUnavailable)
		return
	}
	b, err := readFrame(r)
	var req quorum.Request
	if err != nil || req.UnmarshalBinary(b) != nil {
		http.Error(w, "bad frame", http.StatusBadRequest)
		return
	}
	if req.From != e.o.Peer {
		http.Error(w, "not this pair's peer", http.StatusForbidden)
		return
	}
	stated := statedLease(r)
	e.mu.Lock()
	now := e.now()
	if limit := e.v.MaxRequestLease(now); !leaseFits(stated, limit) && !mut("controller_lease_unchecked") {
		e.mu.Unlock()
		msg := fmt.Sprintf("%s's lease %v is longer than this voter's %v (or missing)", req.From, stated, limit)
		e.mismatched(string(req.From), msg)
		http.Error(w, msg, http.StatusConflict)
		return
	}
	if st := e.v.State(); termJump(req.Term, st.Promised, st.Floor) && !mut("controller_no_term_cap") {
		e.mu.Unlock()
		e.log.Error("redundancy: refused a vote that would jump the terms", "from", req.From, "term", req.Term, "promised", st.Promised)
		http.Error(w, "term jump", http.StatusConflict)
		return
	}
	e.a.OnPeer(now)
	e.peerAt, e.peerHeard = now, true
	e.c.Observe(req.Term)
	quorum.YieldTo(now, e.v, e.c, req)
	p, replies := e.v.Handle(now, req)
	var ch chan quorum.Reply
	if len(replies) == 0 && p != nil {
		ch = make(chan quorum.Reply, 1)
		e.waiters[keyOf(req)] = ch
	}
	e.after(now)
	e.mu.Unlock()
	e.w.ask(p)
	e.poke()
	answer(w, r, replies, ch, e.o.Lease, func() {
		e.mu.Lock()
		if e.waiters[keyOf(req)] == ch {
			delete(e.waiters, keyOf(req))
		}
		e.mu.Unlock()
	})
}

// answer writes a voter's reply: the one Handle gave at once, or the grant
// its write releases, waited for at most a lease; 204 when there is none.
func answer(w http.ResponseWriter, r *http.Request, replies []quorum.Reply, ch chan quorum.Reply, lease time.Duration, drop func()) {
	if len(replies) > 0 {
		b, _ := replies[0].MarshalBinary()
		writeFrame(w, b)
		return
	}
	if ch == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	t := time.NewTimer(lease)
	defer t.Stop()
	select {
	case rep := <-ch:
		b, _ := rep.MarshalBinary()
		writeFrame(w, b)
		return
	case <-r.Context().Done():
	case <-t.C:
	}
	drop()
	w.WriteHeader(http.StatusNoContent)
}

func (e *Elector) serveAdopt(w http.ResponseWriter, r *http.Request) {
	if !e.running.Load() {
		http.Error(w, "not running", http.StatusServiceUnavailable)
		return
	}
	b, err := readFrame(r)
	var m quorum.Adopt
	if err != nil || m.UnmarshalBinary(b) != nil {
		http.Error(w, "bad frame", http.StatusBadRequest)
		return
	}
	if m.From != e.o.Peer {
		http.Error(w, "not this pair's peer", http.StatusForbidden)
		return
	}
	e.mu.Lock()
	now := e.now()
	p, msgs := e.a.OnAdopt(now, m)
	e.peerAt, e.peerHeard = now, true
	e.after(now)
	e.mu.Unlock()
	e.w.ask(p)
	e.adopts(msgs)
	e.poke()
	w.WriteHeader(http.StatusNoContent)
}

func (e *Elector) serveHello(w http.ResponseWriter, r *http.Request) {
	c := r.URL.Query().Get("c")
	if len(c) < 16 || len(c) > 128 {
		http.Error(w, "challenge", http.StatusBadRequest)
		return
	}
	if quorum.NodeID(r.URL.Query().Get("from")) != e.o.Peer {
		http.Error(w, "not this pair's peer", http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(helloAnswer{Node: string(e.o.Self), MAC: tokenMAC(e.o.Token, c, e.o.Peer, e.o.Self, e.o.ClientToken)})
}

// hello runs the client-token check against the peer: at start and every
// 16 leases (at least every 5 s), raising EventClientToken when the
// answer changes.
func (e *Elector) hello(ctx context.Context) {
	every := max(16*e.o.Lease, 5*time.Second)
	for {
		e.checkToken(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}

func (e *Elector) checkToken(ctx context.Context) {
	c := newChallenge()
	q := url.Values{"c": {c}, "from": {string(e.o.Self)}}
	body, err := e.call.call(ctx, max(e.o.Lease, time.Second), http.MethodGet, e.peer+Prefix+e.o.Cluster+"/hello?"+q.Encode(), nil)
	if err != nil {
		return
	}
	var h helloAnswer
	if json.Unmarshal(body, &h) != nil {
		return
	}
	if h.Node != string(e.o.Peer) {
		e.log.Error("redundancy: the peer's address answers as another node", "addr", e.o.PeerAddr, "want", e.o.Peer, "got", h.Node)
		return
	}
	got := TokenDiffers
	if sameMAC(h.MAC, tokenMAC(e.o.Token, c, e.o.Self, e.o.Peer, e.o.ClientToken)) {
		got = TokenSame
	}
	e.mu.Lock()
	changed := got != e.token
	e.token = got
	if changed {
		e.event(Event{Kind: EventClientToken, Detail: got.String()})
	}
	e.mu.Unlock()
	switch {
	case changed && got == TokenDiffers:
		e.log.Error("redundancy: client token differs between " + string(e.o.Self) + " and " + string(e.o.Peer) + ": the pair is one controller to its clients, so both need the same NAUTILUS_TOKEN")
	case changed:
		e.log.Info("redundancy: the peer holds the same client token")
	}
}

// Recommission is the operator's way out of EventRecommissionNeeded
// (quorum.Adoption.Recommission, design §7): adopt incarnation inc, with
// every term at or below floor never granted again. It returns once the
// floor is durable, or the error; the adoption itself then goes through
// the peer. The permission lasts this boot: after a restart, issue it
// again, above KnownFloor.
func (e *Elector) Recommission(ctx context.Context, inc, floor uint64) error {
	e.mu.Lock()
	if known := e.a.KnownFloor(); floor > known && floor-known > MaxTermJump/2 {
		e.mu.Unlock()
		return fmt.Errorf("quorum: re-commission floor %d is more than %d above the terms this controller knows (%d): the voters would refuse the pair's next campaign as a term jump", floor, MaxTermJump/2, known)
	}
	p, err := e.a.Recommission(inc, floor)
	if err != nil {
		known := e.a.KnownFloor()
		e.mu.Unlock()
		if errors.Is(err, quorum.ErrFloorTooLow) {
			return fmt.Errorf("%w: it knows terms up to %d", err, known)
		}
		return err
	}
	e.mu.Unlock()
	e.w.ask(p)
	if err := e.waitDurable(ctx, p.Seq); err != nil {
		return err
	}
	e.mu.Lock()
	e.event(Event{Kind: EventRecommissioned, Incarnation: inc, Detail: fmt.Sprintf("floor %d", floor)})
	e.mu.Unlock()
	e.log.Warn("redundancy: re-commissioned by an operator", "incarnation", inc, "floor", floor)
	e.poke()
	return nil
}

// waitDurable waits until a write covering seq is on disk.
func (e *Elector) waitDurable(ctx context.Context, seq uint64) error {
	for {
		e.mu.Lock()
		done, ch, werr := e.durable >= seq, e.written, e.writeErr
		e.mu.Unlock()
		if done {
			return nil
		}
		select {
		case <-ctx.Done():
			if werr != "" {
				return fmt.Errorf("quorum: the state file can't be written: %s", werr)
			}
			return ctx.Err()
		case <-ch:
		}
	}
}

// KnownFloor is the highest term this controller knows was promised: an
// operator's Recommission floor must be above it.
func (e *Elector) KnownFloor() uint64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.a.KnownFloor()
}

// Status returns a snapshot.
func (e *Elector) Status() Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.now()
	st := e.v.State()
	s := Status{
		Self: string(e.o.Self), Peer: string(e.o.Peer),
		Commissioned: st.Incarnation != 0, Gen: st.Gen, Incarnation: st.Incarnation,
		PendingGen: st.PendingGen, PendingInc: st.PendingInc, Floor: st.Floor, KnownFloor: e.a.KnownFloor(),
		WitnessIncarnation: e.witInc, WitnessAge: -1, PeerAge: -1,
		RecommissionNeeded: e.needOp && st.Incarnation == 0, ClientToken: e.token,
		StateWriteError: e.writeErr, Acquisitions: e.acquired,
	}
	g := e.gate.Load()
	s.Leading = g.open && now < g.end
	s.Term = g.epoch
	switch from, until := e.v.LeaderSeen(); {
	case s.Leading:
		s.Leader, s.LeaderAddr = s.Self, e.o.SelfAPI
	case from == e.o.Peer && now < until:
		s.Leader, s.LeaderAddr = s.Peer, e.o.PeerAPI
	}
	if e.witHeard {
		s.WitnessAge = now.Sub(e.witAt)
	}
	if e.peerHeard {
		s.PeerAge = now.Sub(e.peerAt)
	}
	return s
}
