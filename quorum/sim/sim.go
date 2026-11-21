// Package sim is a deterministic discrete-event simulator for the vote
// protocol of package quorum (test plan tier T2). It drives the real
// quorum.Voter and quorum.Candidate, through the real wire codec, over a
// simulated network, disk and set of clocks, and records what happened so
// the oracles (oracle.go) can judge the run on exact intervals.
//
// Everything is a function of Options: one seed and one fault schedule
// replay bit for bit. Time is integer nanoseconds, clock rates are integer
// ppm, every random draw comes from a PRNG seeded from Options.Seed, the
// event queue breaks ties by sequence number, and no map is iterated.
package sim

import (
	"container/heap"
	"fmt"
	"hash/fnv"
	"math/rand/v2"
	"time"

	"github.com/joyautomation/nautilus/quorum"
)

// Addr is a node's place in the simulated network. Several nodes may carry
// the same quorum.NodeID (a second witness copy is also "W"), so the sim
// routes by Addr, never by name.
type Addr int

// The nodes of every run. A, B and W are the pair and its witness. W2 is
// a second witness copy under the same name (F15b), and Z a voter under a
// name the pair does not know; both are down unless a fault starts them.
// CA and CB are MAC-spoofed clones of A and of B (#1029 layer 2): a
// controller under the original's name, booted from a copy of its disk by
// a Clone fault, that reaches the original's peer and the witness at its
// own address (the peer's messages still go to the original's).
const (
	A Addr = iota
	B
	W
	W2
	Z
	CA
	CB
	nNodes
)

var addrNames = [nNodes]string{"A", "B", "W", "W2", "Z", "CA", "CB"}

func (a Addr) String() string {
	if a >= 0 && a < nNodes {
		return addrNames[a]
	}
	return fmt.Sprintf("Addr(%d)", int(a))
}

var nodeIDs = [nNodes]quorum.NodeID{"A", "B", "W", "W", "Z", "A", "B"}

func isController(a Addr) bool { return a == A || a == B || a == CA || a == CB }

// peerOf is where a controller's votes and handshake go: a clone sends to
// the original's peer, and the peer to the original. A MAC-spoofed clone
// shares the original's address, so the peer's vote requests reach the
// clone too while it runs (sendPeerVote; #1113 review): its voter answers
// them like any voter. The handshake still goes to the original only.
func peerOf(a Addr) Addr {
	if a == A || a == CA {
		return B
	}
	return A
}

// simBoot is a voter boot id unique per node and boot, taken from neither
// random stream, so adding it moved no seed.
func simBoot(n *node) uint64 { return uint64(n.addr+1)<<32 | uint64(n.boot+1) }

// cloneOf is the clone slot of controller a (CA for A, CB for B), or a
// itself for a clone.
func cloneOf(a Addr) Addr {
	switch a {
	case A:
		return CA
	case B:
		return CB
	}
	return a
}

// sendPeerVote sends a vote request (a campaign, a renewal or a probe)
// from controller n to its peer, and to the peer's running clone too, over
// the link from n to that clone (whose faults apply): the clone shares
// the original's address (#1113 review; before, the peer's requests never
// reached a clone, so no sim run could see a clone's voter grant the peer).
func (w *World) sendPeerVote(n *node, b []byte) {
	to := peerOf(n.addr)
	w.send(n.addr, to, b)
	if c := cloneOf(to); c != to && !w.o.PeerNeverReachesClone {
		if cn := w.nodes[c]; cn != nil && cn.up {
			w.send(n.addr, c, b)
		}
	}
}

// original is the controller a clone copies, or a itself.
func original(a Addr) Addr {
	switch a {
	case CA:
		return A
	case CB:
		return B
	}
	return a
}

// Options is one run: the configuration, the start times, and the faults.
type Options struct {
	Seed  uint64
	Lease time.Duration // default 2 s
	Rho   float64       // default 0.01

	// Delay is every link's base one-way delay, Jitter the most added to
	// it at random per message (so messages reorder). LinkDelay overrides
	// Delay per directed link when non-zero.
	Delay, Jitter time.Duration
	LinkDelay     [nNodes][nNodes]time.Duration

	// Fsync is how long a durable write takes.
	Fsync time.Duration
	// InPlace: the state file is rewritten in place, so a crash during a
	// write leaves it torn (unreadable, F26). Otherwise a write is atomic
	// (temp file and rename) and a crash leaves the old file.
	InPlace bool

	// Rates are the clock rates of A, B and W (and W2, Z). 0 means Perfect.
	Rates [nNodes]PPM

	// Start is when A, B and W first start. W2 and Z start only by fault.
	Start [3]time.Duration

	// Tick is how often (on its own clock) a controller's candidate looks,
	// and Scan how often it writes the device when its gate is open.
	// Defaults lease/8 and lease/10.
	Tick, Scan time.Duration

	// NoJitter turns off the candidates' retry jitter, and SameNonce gives
	// every boot of every controller nonce 1: both only to set up hostile
	// cases (a nonce collision) deliberately.
	NoJitter, SameNonce bool
	// VoterBootApart keeps a controller's voter boot apart from its
	// candidate's nonce (the sim before #1029 3c-3b; the node never did):
	// only to measure the retired-boot reply rule against it.
	VoterBootApart bool

	// Faults is the schedule. Until is when the run stops.
	Faults []Fault
	Until  time.Duration

	// Trace keeps a human-readable line per event in Result.Trace.
	Trace bool
	// PeerNeverReachesClone: the peer's vote requests go to the original
	// only, never to a running clone (the sim before the #1113 review), for
	// measuring against the old model.
	PeerNeverReachesClone bool
}

func (o *Options) defaults() {
	if o.Lease == 0 {
		o.Lease = 2 * time.Second
	}
	if o.Rho == 0 {
		o.Rho = 0.01
	}
	if o.Tick == 0 {
		o.Tick = o.Lease / 8
	}
	if o.Scan == 0 {
		o.Scan = o.Lease / 10
	}
	for i := range o.Rates {
		if o.Rates[i] == 0 {
			o.Rates[i] = Perfect
		}
	}
	if o.Until == 0 {
		o.Until = 30 * o.Lease
	}
}

// --- events -------------------------------------------------------------

type evKind int

const (
	evStart evKind = iota
	evTick
	evScan
	evDeliver
	evDiskDone
	evFault
	evHeal
)

type event struct {
	at   time.Duration
	seq  uint64
	kind evKind
	node Addr
	boot int // the node's boot the event belongs to (timers, disk, deferred)
	gen  int // timer generation: a clock change re-arms timers

	from Addr
	msg  []byte
	// probe: a standby's probe of the witness or its reply (#1029 layer
	// 2), whose delays come from World.probeNet.
	probe bool

	fault int // index into Options.Faults
}

type queue []*event

func (q queue) Len() int { return len(q) }
func (q queue) Less(i, j int) bool {
	if q[i].at != q[j].at {
		return q[i].at < q[j].at
	}
	return q[i].seq < q[j].seq
}
func (q queue) Swap(i, j int) { q[i], q[j] = q[j], q[i] }
func (q *queue) Push(x any)   { *q = append(*q, x.(*event)) }
func (q *queue) Pop() any {
	old := *q
	e := old[len(old)-1]
	*q = old[:len(old)-1]
	return e
}

// --- nodes --------------------------------------------------------------

type write struct {
	seq   uint64
	bytes []byte
}

type disk struct {
	file     []byte // nil: no file
	queue    []write
	busy     bool
	inflight write
	slow     []time.Duration // active slow-disk faults: the latency is their max
	failing  int             // active EIO faults
}

type node struct {
	addr  Addr
	clock *Clock
	rng   *rand.Rand
	// markSeen is the voter's MarkCount last recorded (reset per boot).
	markSeen uint64

	up, paused bool
	deferred   []*event
	boot       int
	disk       disk

	voter *quorum.Voter
	cand  *quorum.Candidate
	adopt *quorum.Adoption
	nonce uint64
	// seen is the voter's handshake state after the last call into the
	// adoption, so a change (a proposal, an adoption in memory) is seen;
	// cnt is what the protocol says this controller counts (countTruth).
	seen quorum.VoterState
	cnt  countState
	sent []sentReq // this boot's requests, for the hostile-delivery check
	// legit are the grants this boot's candidate may count, by request:
	// a hostile grant is decisive when one of these is already in.
	legit []legitGrant

	// timers: local deadlines, re-armed in global time on a clock change.
	// tickGen names the one live tick event (it moves earlier when the
	// candidate's next look does), gen the live scan event.
	tickAt, scanAt quorum.Instant
	gen, tickGen   int

	// grantEnd remembers the promise end (local) each held grant was given
	// at Handle, keyed by the request it answers, until Persisted emits it.
	grantEnd []pendingGrant

	downs, pauses, freezes int // active crash, pause and freeze faults
	unhealthy              int // active Unhealthy faults: its scans hang
	savedRate              PPM

	// spareSeq: the write of the spare flag this boot decided on
	// (quorum.Voter.MakeSpare), 0 when none is in flight. spare: the flag
	// is durable and this process is silent (#1029 3c-2). A restart reads
	// the flag from the disk.
	spareSeq uint64
	spare    bool
}

type legitGrant struct {
	term, id uint64
	from     quorum.NodeID
}

type sentReq struct {
	term, id uint64
	at       quorum.Instant
}

type pendingGrant struct {
	to       Addr
	req      quorum.Request
	start    time.Duration
	endLocal quorum.Instant
}

// --- the world ----------------------------------------------------------

// World is one run in progress.
type World struct {
	// clearPlan is the operator's clear command's check, made once for
	// all the Clear faults at one instant (the real command checks once
	// and sends every member the same plan).
	clearPlan *clearPlan
	// fromSpareDone: FromSpare fault i went through, so its heal restarts
	// the former spare.
	fromSpareDone map[int]bool
	// tookAddress: a copy an operator's --from-spare made the member,
	// which the peer's Adopts now reach too (the copy took the original's
	// address, as a replacement does; votes reach a clone already).
	tookAddress map[Addr]bool
	o           Options
	now         time.Duration
	seq         uint64
	q           queue
	nodes       [nNodes]*node
	net         *rand.Rand
	// probeNet draws the network's chances for the standby's probes and
	// their replies (#1029 layer 2), and viaProbe says a send is one: the
	// probes leave every other frame's draws as they were, so a seed's
	// schedule stays comparable with the runs from before them.
	probeNet *rand.Rand
	viaProbe bool

	witnessInc uint64 // the incarnation A and B were commissioned with

	// link state, per directed link
	cut    [nNodes][nNodes][]int64         // active cuts: the frame kinds each drops (0: all)
	delay  [nNodes][nNodes][]time.Duration // active extra-delay faults
	loss   [nNodes][nNodes][]int           // active loss faults, percent
	dup    [nNodes][nNodes][]time.Duration // active duplicate faults: the copy's extra delay bound
	replay [nNodes][nNodes][]time.Duration // active replay faults: a copy exactly this much later
	mirror [nNodes][nNodes][]Addr          // active mirrors: a copy also goes to these

	rec  Record
	hash uint64
}

func newWorld(o Options) *World {
	o.defaults()
	w := &World{o: o, net: rand.New(rand.NewPCG(o.Seed, 0x6e6574)), probeNet: rand.New(rand.NewPCG(o.Seed, 0x70726f6265)), fromSpareDone: map[int]bool{}, tookAddress: map[Addr]bool{}}
	for a := Addr(0); a < nNodes; a++ {
		w.nodes[a] = &node{
			addr:  a,
			clock: NewClock(0, 0, o.Rates[a]),
			rng:   rand.New(rand.NewPCG(o.Seed, 0x6e6f6465+uint64(a))),
		}
	}
	w.witnessInc = drawNonZero(rand.New(rand.NewPCG(o.Seed, 0x696e63)))
	// Commissioning: A, B and W all hold the witness incarnation.
	st, _ := quorum.VoterState{Incarnation: w.witnessInc}.MarshalBinary()
	for _, a := range []Addr{A, B, W} {
		w.nodes[a].disk.file = append([]byte(nil), st...)
	}
	for i, a := range []Addr{A, B, W} {
		w.push(&event{at: o.Start[i], kind: evStart, node: a})
	}
	for i, f := range o.Faults {
		w.push(&event{at: f.At, kind: evFault, fault: i})
		if f.For > 0 {
			w.push(&event{at: f.At + f.For, kind: evHeal, fault: i})
		}
	}
	w.rec.lease = o.Lease
	w.rec.witnessInc = w.witnessInc
	return w
}

func drawNonZero(r *rand.Rand) uint64 {
	for {
		if v := r.Uint64(); v != 0 {
			return v
		}
	}
}

func (w *World) push(e *event) {
	w.seq++
	e.seq = w.seq
	heap.Push(&w.q, e)
}

func (w *World) tracef(format string, args ...any) {
	if w.o.Trace {
		w.rec.Trace = append(w.rec.Trace, fmt.Sprintf("%12v ", w.now)+fmt.Sprintf(format, args...))
	}
}

// mix folds an event into the run's hash, so two runs of one seed can be
// compared without keeping the trace.
func (w *World) mix(vals ...uint64) {
	h := fnv.New64a()
	var b [8]byte
	put := func(v uint64) {
		for i := range b {
			b[i] = byte(v >> (8 * i))
		}
		h.Write(b[:])
	}
	put(w.hash)
	for _, v := range vals {
		put(v)
	}
	w.hash = h.Sum64()
}

// Run runs o to completion and returns what happened.
func Run(o Options) *Record {
	w := newWorld(o)
	for w.q.Len() > 0 {
		e := heap.Pop(&w.q).(*event)
		if e.at > w.o.Until {
			break
		}
		w.now = e.at
		w.mix(uint64(e.at), uint64(e.kind), uint64(e.node), e.seq)
		w.step(e)
	}
	w.now = w.o.Until
	for _, n := range w.nodes {
		if n.up && isController(n.addr) {
			w.gate(n) // a last snapshot so open gates are closed at Until
		}
	}
	w.rec.until = w.o.Until
	w.rec.Hash = w.hash
	for _, n := range w.nodes {
		if n.up && n.voter != nil {
			for _, nm := range n.voter.Marked() {
				w.rec.MarkedAtEnd = append(w.rec.MarkedAtEnd, Mark{At: w.now, Voter: n.addr, Name: nm})
			}
		}
	}
	for a := Addr(0); a < nNodes; a++ {
		w.rec.clocks[a] = w.nodes[a].clock
	}
	return &w.rec
}

func (w *World) step(e *event) {
	switch e.kind {
	case evFault:
		w.applyFault(e.fault, true)
		return
	case evHeal:
		w.applyFault(e.fault, false)
		return
	case evStart:
		w.start(w.nodes[e.node])
		return
	}
	n := w.nodes[e.node]
	if !n.up || (e.boot >= 0 && e.boot != n.boot) {
		return // the process that armed it is gone (a message, boot -1, goes to whoever is up)
	}
	if n.spare {
		return // a spare is silent: no look, no answer, nothing heard (#1029 3c-2)
	}
	if n.paused {
		n.deferred = append(n.deferred, e)
		return
	}
	switch e.kind {
	case evTick:
		if e.gen == n.tickGen {
			w.tick(n)
		}
	case evScan:
		if e.gen == n.gen {
			w.scan(n)
		}
	case evDeliver:
		w.viaProbe = e.probe
		w.deliver(n, e.from, e.msg)
		w.viaProbe = false
	case evDiskDone:
		w.diskDone(n)
	}
}

func (w *World) local(n *node) quorum.Instant { return n.clock.Local(w.now) }

// start boots n's process from its disk: a voter, and on a controller a
// candidate, with a fresh nonce.
func (w *World) start(n *node) {
	if n.up || n.downs > 0 {
		return
	}
	n.up, n.deferred = true, nil
	n.paused = n.pauses > 0 || n.freezes > 0
	n.boot++
	n.sent, n.grantEnd, n.legit = nil, nil, nil
	n.disk.queue, n.disk.busy = nil, false
	n.spareSeq, n.spare = 0, false
	now := w.local(n)

	var st quorum.VoterState
	readable := false
	if n.disk.file != nil {
		if err := st.UnmarshalBinary(n.disk.file); err == nil {
			readable = true
		} else {
			st = quorum.VoterState{}
		}
	}
	for _, r := range st.Dups {
		if r.Marked {
			w.rec.MarksRestored = append(w.rec.MarksRestored, Mark{At: w.now, Voter: n.addr, Name: r.Name})
			w.tracef("%v start restores its mark on %v", n.addr, r.Name)
		}
	}
	id := nodeIDs[n.addr]
	if n.addr == W || n.addr == W2 {
		v, p := quorum.NewWitness(id, w.o.Lease, st, now, func() uint64 { return drawNonZero(n.rng) })
		v.SetBoot(simBoot(n))
		n.markSeen = 0
		n.voter = v
		w.rec.voterStart(n.addr, n.boot, w.now, readable, v.State().Incarnation)
		if p != nil {
			w.persist(n, p)
		}
		w.tracef("%v start (witness, readable=%v, inc=%d)", n.addr, readable, v.State().Incarnation)
	} else {
		n.voter = quorum.NewVoter(id, w.o.Lease, st, now)
		n.voter.SetBoot(simBoot(n))
		n.markSeen = 0
		w.rec.voterStart(n.addr, n.boot, w.now, readable, st.Incarnation)
	}
	if !isController(n.addr) {
		if n.addr != W && n.addr != W2 {
			w.tracef("%v start (voter, readable=%v)", n.addr, readable)
		}
		return
	}
	if st.Spare {
		// A spare's flag is read first, and it stays silent (#1029 3c-2).
		n.spare, n.voter = true, nil
		w.rec.Spares = append(w.rec.Spares, Spare{At: w.now, Node: n.addr, Boot: n.boot, Restart: true})
		w.tracef("%v start (spare: silent)", n.addr)
		return
	}
	n.nonce = drawNonZero(n.rng)
	if w.o.SameNonce {
		n.nonce = 1
	}
	if !w.o.VoterBootApart {
		// As the node: a controller's voter boot is its candidate's nonce
		// (node.Elector), so a reply's VoterBoot names the very boot an
		// operator's clear retired (Voter.RetiredReply).
		n.voter.SetBoot(n.nonce)
	}
	cfg := quorum.Config{
		Self: nodeIDs[n.addr], Peer: nodeIDs[peerOf(n.addr)], Witness: "W",
		Lease: w.o.Lease, Rho: w.o.Rho, Incarnation: st.Incarnation, Nonce: n.nonce,
	}
	if !w.o.NoJitter {
		r := n.rng
		cfg.Jitter = func() time.Duration { return time.Duration(r.Int64N(int64(w.o.Lease / 4))) }
	}
	c, err := quorum.NewCandidate(cfg, st.Promised, now)
	if err != nil {
		panic(err) // a sim bug: the config is built here
	}
	n.cand = c
	n.adopt = quorum.NewAdoption(n.voter, c, now)
	n.seen = st
	// The ground truth of what counts (design §3.1), kept apart from the
	// candidate's own bookkeeping so the oracles judge it: a restart owes
	// the quiet period; a proposal in flight counts nothing.
	switch {
	case st.PendingGen != 0 || st.Incarnation == 0:
		w.count(n, 0, 0)
	default:
		w.count(n, st.Incarnation, now.Add(w.o.Lease))
	}
	w.tracef("%v start (controller, readable=%v, promised=%d, inc=%d, gen=%d, pending=%d)", n.addr, readable, st.Promised, st.Incarnation, st.Gen, st.PendingGen)
	w.gate(n)
	n.tickAt, n.scanAt = now, now.Add(w.o.Scan)
	w.arm(n)
}

// arm schedules n's tick and scan timers at the global times its clock
// reaches their local deadlines. A clock change re-arms (gen).
func (w *World) arm(n *node) {
	if !isController(n.addr) || !n.up {
		return
	}
	n.gen++
	w.armTick(n, n.tickAt)
	if t := n.clock.Global(n.scanAt, w.now); t != Never {
		w.push(&event{at: t, kind: evScan, node: n.addr, boot: n.boot, gen: n.gen})
	}
}

// armTick makes the tick at local deadline at n's one live tick, as a
// driver arms one timer for its candidate's next look.
func (w *World) armTick(n *node, at quorum.Instant) {
	n.tickAt = at
	n.tickGen++
	if t := n.clock.Global(at, w.now); t != Never {
		w.push(&event{at: t, kind: evTick, node: n.addr, boot: n.boot, gen: n.tickGen})
	}
}

// soonerLook moves n's tick earlier when its candidate's next look is.
func (w *World) soonerLook(n *node) {
	if nl := n.cand.NextLook(); nl < n.tickAt {
		w.armTick(n, max(nl, w.local(n)))
	}
}

func (w *World) tick(n *node) {
	now := w.local(n)
	from, until := n.voter.LeaderSeen()
	if r := n.cand.Tick(now, from, until, n.unhealthy == 0); r != nil {
		n.sent = append(n.sent, sentReq{term: r.Term, id: r.ID, at: now})
		w.rec.Requests = append(w.rec.Requests, SentRequest{At: w.now, From: n.addr, Term: r.Term, Renewal: r.Renewal})
		w.tracef("%v sends term=%d id=%d", n.addr, r.Term, r.ID)
		b, _ := r.MarshalBinary()
		w.selfDeliver(n, b)
		r.Promise = n.voter.Relay(w.local(n)) // stamped as it leaves (the self copy carries none)
		b, _ = r.MarshalBinary()
		w.sendPeerVote(n, b)
		w.send(n.addr, W, b)
	}
	p, msgs, probe := n.adopt.Tick(now)
	standby := false
	if probe == nil {
		probe = n.cand.Probe(now) // the standby's probe of the witness (#1029 layer 2)
		standby = probe != nil
	} else {
		n.cand.Probe(now)
	}
	if p != nil {
		w.persist(n, p)
	}
	w.sendAdopts(n, msgs)
	if probe != nil {
		// Only to the witness, and not a request the candidate keeps.
		probe.Promise = n.voter.Relay(w.local(n))
		b, _ := probe.MarshalBinary()
		w.viaProbe = standby
		w.send(n.addr, W, b)
		if standby && quorum.ProbesPeer() {
			w.sendPeerVote(n, b) // the peer hears this boot too (#1029 step 1)
		}
		w.viaProbe = false
	}
	w.observe(n)
	w.gate(n)
	next := now.Add(w.o.Tick)
	if nl := n.cand.NextLook(); nl > now && nl < next {
		next = nl
	}
	w.armTick(n, next)
}

func (w *World) scan(n *node) {
	now := w.local(n)
	if n.cand.Leading(now) {
		w.rec.Writes = append(w.rec.Writes, DeviceWrite{At: w.now, From: n.addr, Term: n.cand.Term()})
	}
	n.scanAt = now.Add(w.o.Scan)
	if t := n.clock.Global(n.scanAt, w.now); t != Never {
		w.push(&event{at: t, kind: evScan, node: n.addr, boot: n.boot, gen: n.gen})
	}
}

// gate records n's gate after a call into its candidate: open until its
// lease end (local), or closed.
func (w *World) gate(n *node) {
	now := w.local(n)
	if n.up && n.cand != nil && n.cand.Leading(now) {
		w.rec.gate(n.addr, w.now, true, n.cand.LeaseEnd(), n.cand.VoteTerm())
		return
	}
	w.rec.gate(n.addr, w.now, false, 0, 0)
}

// selfDeliver hands a controller's request to its own voter: same process,
// no network, but through the queue so ordering stays deterministic.
func (w *World) selfDeliver(n *node, b []byte) {
	w.push(&event{at: w.now, kind: evDeliver, node: n.addr, boot: n.boot, from: n.addr, msg: b})
}

// send puts b on the directed link from→to, and on every mirror of it.
func (w *World) send(from, to Addr, b []byte) {
	w.sendOne(from, to, b)
	for _, m := range w.mirror[from][to] {
		w.sendOne(from, m, b)
	}
}

func (w *World) sendOne(from, to Addr, b []byte) {
	for _, m := range w.cut[from][to] {
		if m == 0 || len(b) > 1 && m&(1<<b[1]) != 0 {
			w.tracef("%v→%v dropped (cut)", from, to)
			return
		}
	}
	net := w.net
	if w.viaProbe {
		net = w.probeNet
	}
	if p := maxInt(w.loss[from][to]); p > 0 && net.IntN(100) < p {
		w.tracef("%v→%v lost", from, to)
		return
	}
	d := w.o.Delay
	if ld := w.o.LinkDelay[from][to]; ld != 0 {
		d = ld
	}
	d += maxDur(w.delay[from][to])
	if w.o.Jitter > 0 {
		d += time.Duration(net.Int64N(int64(w.o.Jitter) + 1))
	}
	w.push(&event{at: w.now + d, kind: evDeliver, node: to, from: from, msg: b, boot: -1, probe: w.viaProbe})
	for _, r := range w.replay[from][to] {
		w.push(&event{at: w.now + d + r, kind: evDeliver, node: to, from: from, msg: b, boot: -1, probe: w.viaProbe})
	}
	if bound := maxDur(w.dup[from][to]); bound > 0 {
		extra := time.Duration(net.Int64N(int64(bound) + 1))
		w.push(&event{at: w.now + d + extra, kind: evDeliver, node: to, from: from, msg: b, boot: -1, probe: w.viaProbe})
	}
}

func maxInt(xs []int) int {
	m := 0
	for _, x := range xs {
		m = max(m, x)
	}
	return m
}

func maxDur(xs []time.Duration) time.Duration {
	var m time.Duration
	for _, x := range xs {
		m = max(m, x)
	}
	return m
}

func (w *World) deliver(n *node, from Addr, b []byte) {
	now := w.local(n)
	var req quorum.Request
	if err := req.UnmarshalBinary(b); err == nil {
		if n.voter.FencedRequest(req) {
			w.tracef("%v refuses %v as fenced (gen %d below %d)", n.addr, from, req.Gen, n.voter.FenceOf(req.From))
			w.handle(n, from, now, req)
			return
		}
		n.voter.LearnFences(req.Fences, learnNames(n.addr)...) // its write goes with Handle's (MarkWrite)
		if n.adopt != nil && w.fromPeer(n, from) && !(req.Term == 0 && req.ID == quorum.ProbeID) {
			n.adopt.OnPeer(now) // a relay probe is for the voter only (#1029 step 1)
		}
		if from != n.addr {
			dups, _ := n.voter.Duplicates()
			n.voter.Hear(now, req.From, req.Nonce, req.Promise)
			after, who := n.voter.Duplicates()
			w.rec.duplicates[n.addr] += after - dups
			if after != dups {
				w.tracef("%v counts a duplicate of %s (relay on a request from %v: %+v)", n.addr, who, from, req.Promise)
			}
		}
		w.handle(n, from, now, req)
		return
	}
	var m quorum.Adopt
	if err := m.UnmarshalBinary(b); err == nil {
		if n.adopt == nil {
			return
		}
		w.tracef("%v hears %v: %v gen=%d inc=%d floor=%d pending=%d fresh=%v", n.addr, from, m.Kind, m.Gen, m.Incarnation, m.Floor, m.Pending, m.Echo == n.nonce)
		if st := n.voter.State(); m.Kind == quorum.Propose && m.Gen != 0 && m.Gen < st.Gen && m.Incarnation != st.Incarnation {
			// A late proposal of another incarnation, below what this
			// controller adopted since: the generations must refuse it.
			w.rec.StaleProposals = append(w.rec.StaleProposals, AdoptRec{Node: n.addr, At: w.now, Gen: m.Gen, Inc: m.Incarnation})
		}
		if m.From == nodeIDs[peerOf(n.addr)] && m.Echo != 0 && m.Echo != n.nonce {
			// Sent to an earlier boot of this controller.
			w.rec.EarlierBootAdopts = append(w.rec.EarlierBootAdopts, AdoptRec{Node: n.addr, At: w.now, Gen: m.Gen, Inc: m.Incarnation})
		}
		p, msgs := n.adopt.OnAdopt(now, m)
		if p != nil {
			w.persist(n, p)
		}
		w.sendAdopts(n, msgs)
		w.observe(n)
		w.gate(n)
		return
	}
	var rep quorum.Reply
	if err := rep.UnmarshalBinary(b); err != nil {
		w.tracef("%v drops a corrupt frame from %v", n.addr, from)
		return
	}
	if n.cand == nil {
		return
	}
	if n.voter.FencedReply(rep) {
		w.tracef("%v drops %v's reply as fenced (gen %d)", n.addr, from, rep.Gen)
		return
	}
	if n.voter.LearnFences(rep.Fences, learnNames(n.addr)...) {
		w.tracef("%v learns the fences %v from %v", n.addr, rep.Fences, from)
		if p := n.voter.MarkWrite(); p != nil {
			w.persist(n, p)
		}
	}
	if from != n.addr {
		dups, _ := n.voter.Duplicates()
		n.voter.Hear(now, rep.From, 0, rep.Promise)
		after, who := n.voter.Duplicates()
		w.rec.duplicates[n.addr] += after - dups
		if after != dups {
			w.tracef("%v counts a duplicate of %s (relay on a reply from %v: %+v)", n.addr, who, from, rep.Promise)
		}
	}
	n.cand.NoteMark(rep) // a probe's reply too: how a marked name hears it cleared
	n.cand.NoteHeld(now, rep)
	if n.spareSeq == 0 && n.cand.Spare(now) {
		// Both other voters hold another boot of this name the incumbent:
		// the flag is written first, and the copy goes quiet once it is
		// durable (diskDone). Until then it is the held copy it was.
		p := n.voter.MakeSpare()
		n.spareSeq = p.Seq
		w.rec.spareDecided = append(w.rec.spareDecided, Spare{At: w.now, Node: n.addr, Boot: n.boot})
		w.tracef("%v decides it is a spare (flag write seq=%d)", n.addr, p.Seq)
		w.persist(n, p)
	}
	if quorum.IsRelayProbe(rep) {
		return // the relay probe's reply is for the voter only
	}
	if rep.Granted {
		w.rec.received(from, rep, n.addr, w.now)
	}
	w.noteHostile(n, rep)
	w.tracef("%v hears %v: term=%d id=%d granted=%v to=%s inc=%d", n.addr, from, rep.Term, rep.ID, rep.Granted, rep.To, rep.Incarnation)
	n.cand.OnReply(now, rep)
	switch {
	case w.fromPeer(n, from):
		n.adopt.OnPeer(now)
	case rep.From == "W":
		n.adopt.OnWitness(now, rep)
		w.observe(n)
	}
	w.soonerLook(n)
	w.gate(n)
}

// learnNames are the names a's voter may learn fences on, as the node's:
// a controller its peer's, the witness the two controllers'.
func learnNames(a Addr) []quorum.NodeID {
	if nodeIDs[a] == "W" { // the witness, or a second copy of it
		return []quorum.NodeID{nodeIDs[A], nodeIDs[B]}
	}
	return []quorum.NodeID{nodeIDs[peerOf(a)]}
}

// fromPeer reports whether a frame from address from is from n's peer: the
// peer's own address, or a copy an operator's from-spare made the member
// at its address (tookAddress), as the node hears it by the peer link,
// not by box (#1164: before, a pair whose member was such a copy never
// heard its peer in the handshake, so it never switched to a rebuilt
// witness).
func (w *World) fromPeer(n *node, from Addr) bool {
	return from == peerOf(n.addr) || (w.tookAddress[from] && original(from) == peerOf(n.addr))
}

// sendAdopts sends a controller's handshake messages to its peer, over the
// A–B link, so every fault on it applies.
func (w *World) sendAdopts(n *node, msgs []quorum.Adopt) {
	for _, m := range msgs {
		w.tracef("%v sends %v gen=%d inc=%d floor=%d", n.addr, m.Kind, m.Gen, m.Incarnation, m.Floor)
		b, _ := m.MarshalBinary()
		to := peerOf(n.addr)
		w.send(n.addr, to, b)
		if c := cloneOf(to); c != to && w.tookAddress[c] {
			if cn := w.nodes[c]; cn != nil && cn.up {
				w.send(n.addr, c, b)
			}
		}
	}
}

// countState is what a controller counts, as the protocol says: the
// witness incarnation inc (0: none) from local instant from.
type countState struct {
	inc  uint64
	from quorum.Instant
}

func (w *World) count(n *node, inc uint64, from quorum.Instant) {
	if inc == 0 {
		from = 0
	}
	n.cnt = countState{inc: inc, from: from}
	w.rec.count(n.addr, w.now, n.cnt)
}

// observe applies design §3.1's counting rules to what the adoption just
// did, independently of the candidate: a proposal stops all counting
// (forward only); adopting a new incarnation stops it at once; a durable
// adoption counts its incarnation one lease after the write (the quiet
// period), unless it was already counted, when nothing changes but a
// later start stays. It also logs adoptions and events in the record.
func (w *World) observe(n *node) {
	st := n.voter.State()
	if st.PendingGen != 0 && (st.PendingGen != n.seen.PendingGen || st.PendingInc != n.seen.PendingInc) {
		w.count(n, 0, 0)
		w.rec.Proposals = append(w.rec.Proposals, AdoptRec{Node: n.addr, At: w.now, Gen: st.PendingGen, Inc: st.PendingInc})
	}
	if st.Incarnation != n.seen.Incarnation {
		w.count(n, 0, 0)
	}
	n.seen = st
	for _, e := range n.adopt.Events() {
		switch e.Kind {
		case quorum.EventAdopted:
			now := w.local(n)
			from := now.Add(w.o.Lease)
			if n.cnt.inc == e.Incarnation {
				from = max(now, n.cnt.from)
			}
			w.count(n, e.Incarnation, from)
			w.rec.Adoptions = append(w.rec.Adoptions, AdoptRec{Node: n.addr, At: w.now, Gen: e.Gen, Inc: e.Incarnation})
			w.tracef("%v adopted gen=%d inc=%d (durable), counts it from local %v", n.addr, e.Gen, e.Incarnation, from)
		case quorum.EventTwoIncarnations:
			w.rec.TwoIncarnations++
			w.tracef("%v hears two witness incarnations: %d and %d", n.addr, e.Incarnation, e.Other)
		case quorum.EventReadoptOverSwitch:
			w.rec.ReadoptsOverSwitch++
			w.tracef("%v re-adopted gen=%d inc=%d over its pending switch to %d", n.addr, e.Gen, e.Incarnation, e.Other)
		case quorum.EventRecommission:
			w.rec.Recommissions = append(w.rec.Recommissions, AdoptRec{Node: n.addr, At: w.now, Gen: e.Gen, Inc: e.Incarnation})
			w.tracef("%v: re-commission by hand: the peer's committed gen=%d inc=%d has not answered", n.addr, e.Gen, e.Incarnation)
		}
	}
}

// noteHostile records a reply a correct candidate must not count, that
// reached OnReply while this boot had a request of the same (term, id) in
// its last lease, and whether it was DECISIVE: a grant the candidate may
// count was already in for that request and it didn't lead yet, so
// counting the hostile one would have made a majority. The must-fail
// cases assert a decisive one, so they can't pass vacuously.
func (w *World) noteHostile(n *node, r quorum.Reply) {
	if !r.Granted {
		return
	}
	var why string
	switch {
	case r.From != "A" && r.From != "B" && r.From != "W":
		why = "unknown sender"
	case r.From == "W" && r.Incarnation != n.cnt.inc:
		why = "other witness incarnation"
	case r.From == "W" && w.local(n) < n.cnt.from:
		why = "quiet period"
	case r.To != nodeIDs[n.addr]:
		why = "meant for the peer"
	case r.Nonce != n.nonce:
		why = "earlier boot"
	default:
		for _, g := range n.legit {
			if g.term == r.Term && g.id == r.ID && g.from == r.From {
				return
			}
		}
		n.legit = append(n.legit, legitGrant{term: r.Term, id: r.ID, from: r.From})
		return
	}
	now := w.local(n)
	for _, s := range n.sent {
		if s.term == r.Term && s.id == r.ID && now < s.at.Add(w.o.Lease) {
			decisive := false
			for _, g := range n.legit {
				if g.term == r.Term && g.id == r.ID && g.from != r.From {
					decisive = !n.cand.Leading(now)
				}
			}
			w.rec.Hostile = append(w.rec.Hostile, Hostile{At: w.now, To: n.addr, Why: why, Decisive: decisive})
			w.tracef("%v receives a hostile grant (%s) matching an open request (decisive=%v)", n.addr, why, decisive)
			return
		}
	}
}

func (w *World) handle(n *node, from Addr, now quorum.Instant, r quorum.Request) {
	if n.cand != nil && from != n.addr && !n.voter.FencedRequest(r) {
		n.cand.Observe(r.Term)
		if quorum.YieldTo(now, n.voter, n.cand, r) {
			w.rec.released = append(w.rec.released, release{voter: n.addr, at: w.now})
			w.tracef("%v yields its own promise to %v's term %d", n.addr, from, r.Term)
			w.gate(n)
		}
	}
	before := n.voter.RelayRefused()
	dups, _ := n.voter.Duplicates()
	p, replies := n.voter.Handle(now, r)
	w.rec.relayRefused += n.voter.RelayRefused() - before
	after, who := n.voter.Duplicates()
	w.rec.duplicates[n.addr] += after - dups
	if after != dups {
		w.tracef("%v counts a duplicate of %s (request from %v: %s term=%d nonce=%#x)", n.addr, who, from, r.From, r.Term, r.Nonce)
	}
	if m, name := n.voter.MarkCount(); m != n.markSeen {
		// From the mark count, not the cause, which a relay rule counting
		// in the same Handle overwrites (#1113 review).
		n.markSeen = m
		w.rec.Marks = append(w.rec.Marks, Mark{At: w.now, Voter: n.addr, Name: name})
		w.tracef("%v marks %s a confirmed duplicate", n.addr, name)
	}
	if n.cand != nil && n.voter.RelayChanged(now) {
		if probe := n.cand.PushProbe(now); probe != nil {
			probe.Promise = n.voter.Relay(now)
			b, _ := probe.MarshalBinary()
			w.viaProbe = true
			w.send(n.addr, W, b)
			w.viaProbe = false
		}
	}
	term, to, until := n.voter.PromiseUntil()
	if p != nil || (len(replies) == 1 && replies[0].Granted) {
		if term == r.Term && to == r.From {
			n.grantEnd = append(n.grantEnd, pendingGrant{to: from, req: r, start: w.now, endLocal: until})
		}
	}
	if p != nil {
		w.persist(n, p)
	}
	w.reply(n, from, replies)
}

// reply sends a voter's replies back to where each request came from,
// recording every grant in the ledger.
func (w *World) reply(n *node, to Addr, replies []quorum.Reply) {
	for _, rep := range replies {
		dst := to
		if rep.Granted {
			dst = w.ledger(n, rep)
		}
		if dst != n.addr {
			rep.Promise = n.voter.Relay(w.local(n))
		}
		b, _ := rep.MarshalBinary()
		if dst == n.addr {
			w.push(&event{at: w.now, kind: evDeliver, node: n.addr, boot: n.boot, from: n.addr, msg: b})
			continue
		}
		w.send(n.addr, dst, b)
	}
}

// ledger records a grant n emits and returns where it goes.
func (w *World) ledger(n *node, rep quorum.Reply) Addr {
	for i, g := range n.grantEnd {
		if g.req.From == rep.To && g.req.Term == rep.Term && g.req.Nonce == rep.Nonce && g.req.ID == rep.ID {
			n.grantEnd = append(n.grantEnd[:i], n.grantEnd[i+1:]...)
			w.rec.grant(Grant{Voter: n.addr, Inc: rep.Incarnation, To: g.to, ToID: rep.To, Term: rep.Term,
				ID: rep.ID, Nonce: rep.Nonce, Start: g.start, EndLocal: g.endLocal})
			w.tracef("%v grants %s term=%d id=%d", n.addr, rep.To, rep.Term, rep.ID)
			return g.to
		}
	}
	panic(fmt.Sprintf("sim: %v emitted a grant it never decided: %+v", n.addr, rep))
}

// --- the disk -----------------------------------------------------------

func (w *World) persist(n *node, p *quorum.Persist) {
	b, _ := p.State.MarshalBinary()
	n.disk.queue = append(n.disk.queue, write{seq: p.Seq, bytes: b})
	w.nextWrite(n)
}

func (w *World) fsync(n *node) time.Duration {
	return max(w.o.Fsync, maxDur(n.disk.slow))
}

func (w *World) nextWrite(n *node) {
	if n.disk.busy || len(n.disk.queue) == 0 {
		return
	}
	n.disk.inflight, n.disk.queue = n.disk.queue[0], n.disk.queue[1:]
	n.disk.busy = true
	w.push(&event{at: w.now + w.fsync(n), kind: evDiskDone, node: n.addr, boot: n.boot})
}

func (w *World) diskDone(n *node) {
	wr := n.disk.inflight
	n.disk.busy = false
	ok := n.disk.failing == 0
	if ok {
		n.disk.file = wr.bytes
	}
	w.tracef("%v fsync seq=%d ok=%v", n.addr, wr.seq, ok)
	if n.adopt != nil {
		replies, msgs := n.adopt.Persisted(w.local(n), wr.seq, ok)
		w.replyHeld(n, replies)
		w.sendAdopts(n, msgs)
		w.observe(n)
		w.gate(n)
		if n.spareSeq != 0 && wr.seq >= n.spareSeq {
			if ok {
				w.becomeSpare(n)
				return
			}
			n.spareSeq = 0 // a failed write: still held, and it decides again on the next reply
			if p := n.voter.SpareWriteFailed(); p != nil {
				w.persist(n, p)
			}
		}
	} else {
		w.replyHeld(n, n.voter.Persisted(wr.seq, ok))
	}
	w.nextWrite(n)
}

// replyHeld sends grants released by Persisted to the node each request
// came from.
func (w *World) replyHeld(n *node, replies []quorum.Reply) {
	for _, rep := range replies {
		dst := w.ledger(n, rep)
		if dst != n.addr {
			rep.Promise = n.voter.Relay(w.local(n))
		}
		b, _ := rep.MarshalBinary()
		if dst == n.addr {
			w.push(&event{at: w.now, kind: evDeliver, node: n.addr, boot: n.boot, from: n.addr, msg: b})
			continue
		}
		w.send(n.addr, dst, b)
	}
}

// crash stops n's process. A write in flight tears the file when it is
// rewritten in place; queued writes are lost.
func (w *World) crash(n *node, wipe bool) {
	if !n.up {
		if wipe {
			n.disk.file = nil
		}
		return
	}
	if n.disk.busy && w.o.InPlace {
		b := n.disk.inflight.bytes
		k := w.net.IntN(len(b))
		n.disk.file = append([]byte(nil), b[:k]...)
		w.tracef("%v crash tears its state file at byte %d", n.addr, k)
	}
	if wipe {
		n.disk.file = nil
	}
	if n.voter != nil && n.voter.MarksOwed() {
		w.rec.MarkWritesLost++
		w.tracef("%v crash loses a mark write in flight", n.addr)
	}
	n.up = false
	w.rec.stops[n.addr] = append(w.rec.stops[n.addr], w.now)
	n.disk.busy, n.disk.queue = false, nil
	n.voter, n.cand, n.adopt = nil, nil, nil
	n.deferred = nil
	w.tracef("%v crash (wipe=%v)", n.addr, wipe)
	if isController(n.addr) {
		w.rec.gate(n.addr, w.now, false, 0, 0)
		w.count(n, 0, 0)
	}
}

// becomeSpare silences n once its spare flag is durable: nothing it had
// queued is sent, and it neither looks nor answers again (#1029 3c-2).
func (w *World) becomeSpare(n *node) {
	n.spare, n.spareSeq = true, 0
	w.rec.Spares = append(w.rec.Spares, Spare{At: w.now, Node: n.addr, Boot: n.boot})
	w.tracef("%v is a spare: silent", n.addr)
	n.voter, n.cand, n.adopt = nil, nil, nil
	n.deferred = nil
	n.disk.queue = nil
	w.rec.gate(n.addr, w.now, false, 0, 0)
	w.count(n, 0, 0)
}

func (w *World) pause(n *node, on bool) {
	if on || !n.up {
		n.paused = on
		return
	}
	n.paused = false
	d := n.deferred
	n.deferred = nil
	for _, e := range d {
		e.at = w.now
		w.push(e)
	}
}
