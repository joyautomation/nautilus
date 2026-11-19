package sim

import (
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/joyautomation/nautilus/quorum"
)

// Record is everything a run did that the oracles judge. Times are global.
type Record struct {
	lease      time.Duration
	witnessInc uint64
	until      time.Duration
	clocks     [nNodes]*Clock

	gates  [2][]gateSnap // A and B, after every call into the candidate
	Grants []Grant       // the ledger: every grant a real Voter emitted
	starts []voterStart
	faults []faultEdge

	// Writes are what the simulated device received (I2): a controller
	// writes it on each scan while its gate is open.
	Writes []DeviceWrite
	// Requests are every request a candidate sent: who, when, which term,
	// and whether it was a renewal.
	Requests []SentRequest
	// Hostile are replies a correct candidate must not count that reached
	// OnReply while a matching request of this boot was open.
	Hostile []Hostile

	// Adoptions are the durable witness-incarnation adoptions (EventAdopted)
	// and Proposals the proposals made, by controller, in order.
	Adoptions, Proposals []AdoptRec
	// StaleProposals are proposals delivered below the receiver's adopted
	// generation, of another incarnation (a late or earlier-boot one).
	StaleProposals []AdoptRec
	// EarlierBootAdopts are Adopts from the peer delivered to a controller
	// whose earlier boot they were sent to (they echo an older nonce).
	EarlierBootAdopts []AdoptRec
	// TwoIncarnations counts the "two witness copies" events.
	TwoIncarnations int
	// Recommissions are the "re-commission by hand" alarms (an
	// uncommissioned controller whose peer's committed incarnation never
	// answered it), by controller.
	Recommissions []AdoptRec

	Trace []string
	Hash  uint64

	// counts is, per controller, what the protocol says it counts
	// (countTruth in sim.go), after each change.
	counts [2][]countSnap
	// stops are when each node's process stopped (crashed, or a Start
	// fault ended).
	stops [nNodes][]time.Duration
	// grantIdx finds a ledger grant by (voter, nonce, id, term), to note
	// when its grantee first received it.
	grantIdx map[grantKey]int

	ends     []time.Duration // promise end of each Grant, global; built once by Check
	released []release
}

// AdoptRec is one adoption (durable) or proposal (made) by a controller.
type AdoptRec struct {
	Node     Addr
	At       time.Duration
	Gen, Inc uint64
}

type countSnap struct {
	at time.Duration
	countState
}

type grantKey struct {
	voter           Addr
	to              quorum.NodeID
	nonce, id, term uint64
}

func (r *Record) count(a Addr, at time.Duration, c countState) {
	r.counts[a] = append(r.counts[a], countSnap{at: at, countState: c})
}

// countsAt reports whether controller a counted witness incarnation inc at
// global time t, by the protocol's rules (the quiet period included).
func (r *Record) countsAt(a Addr, inc uint64, t time.Duration) bool {
	if a != A && a != B || inc == 0 {
		return false
	}
	snaps := r.counts[a]
	i := sort.Search(len(snaps), func(i int) bool { return snaps[i].at > t }) - 1
	if i < 0 || snaps[i].inc != inc {
		return false
	}
	return r.clocks[a].Local(t) >= snaps[i].from
}

// received notes that grant rep from voter reached controller to at t.
func (r *Record) received(voter Addr, rep quorum.Reply, to Addr, t time.Duration) {
	i, ok := r.grantIdx[grantKey{voter, rep.To, rep.Nonce, rep.ID, rep.Term}]
	if !ok {
		return
	}
	if g := &r.Grants[i]; g.To == to && g.Recv == 0 {
		g.Recv = t
	}
}

// release is a voter dropping its promise to its own candidate at At (a
// yield: Voter.WouldYield, Candidate.Yield, Voter.ReleaseSelf). Ground
// truth for the ledger, so its next grant to the peer is judged against
// the shortened promise; the lease oracle still sees any lease that rested
// on it.
type release struct {
	voter Addr
	at    time.Duration
}

type gateSnap struct {
	at       time.Duration
	open     bool
	endLocal quorum.Instant
	term     uint64
}

// Grant is one ledger entry: Voter promised Term to To from Start (the
// request's receipt) until EndLocal on Voter's own clock.
type Grant struct {
	Voter    Addr
	Inc      uint64 // the witness incarnation stamped on it (0 from a controller)
	To       Addr
	ToID     quorum.NodeID
	Term     uint64
	ID       uint64
	Nonce    uint64
	Start    time.Duration
	EndLocal quorum.Instant
	// Recv is when the grantee first received it (0: never).
	Recv time.Duration
}

// DeviceWrite is one write the simulated device received.
type DeviceWrite struct {
	At   time.Duration
	From Addr
	Term uint64
}

// SentRequest is one request a candidate sent.
type SentRequest struct {
	At      time.Duration
	From    Addr
	Term    uint64
	Renewal bool
}

// Hostile is a reply that must not count, delivered while it could have.
// Decisive: a countable grant for the same request was already in, so
// counting this one would have made a majority.
type Hostile struct {
	At       time.Duration
	To       Addr
	Why      string
	Decisive bool
}

type voterStart struct {
	node     Addr
	boot     int
	at       time.Duration
	readable bool
	inc      uint64 // a witness's incarnation, or a controller's adopted one, at this start
}

type faultEdge struct {
	at    time.Duration
	fault int
	on    bool
}

func (r *Record) gate(a Addr, at time.Duration, open bool, end quorum.Instant, term uint64) {
	s := gateSnap{at: at, open: open, endLocal: end, term: term}
	g := &r.gates[a]
	if n := len(*g); n > 0 && (*g)[n-1] == s {
		return
	}
	*g = append(*g, s)
}

func (r *Record) grant(g Grant) {
	if r.grantIdx == nil {
		r.grantIdx = map[grantKey]int{}
	}
	r.grantIdx[grantKey{g.Voter, g.ToID, g.Nonce, g.ID, g.Term}] = len(r.Grants)
	r.Grants = append(r.Grants, g)
}

func (r *Record) voterStart(a Addr, boot int, at time.Duration, readable bool, inc uint64) {
	r.starts = append(r.starts, voterStart{node: a, boot: boot, at: at, readable: readable, inc: inc})
}

func (r *Record) faultEvent(at time.Duration, i int, on bool) {
	r.faults = append(r.faults, faultEdge{at: at, fault: i, on: on})
}

// lostFile reports whether A, B or W ever restarted from a state file it
// could not read: wiped, or torn by a crash mid-write. Taken from what the
// run did, not from the schedule, so a torn file counts (#998).
func (r *Record) lostFile() bool {
	for _, s := range r.starts {
		if (s.node == A || s.node == B || s.node == W) && !s.readable {
			return true
		}
	}
	return false
}

// TwoOfThreeLost reports whether the run is design §3.1's two-of-three
// case: two of the three voters' state lost before either re-commissioned,
// so no surviving majority remembers the terms and the pair needs an
// operator (§7). Two shapes:
//
//   - a controller lost its file, and before it re-commissioned (its next
//     durable adoption), an incarnation its peer had committed (durably
//     adopted) in that window died for good (the witness wiped, or an adopted second copy gone): it
//     can only re-adopt what its peer counts, after hearing it, and the
//     terms it led on the dead one are known to nobody;
//   - both controllers lost their files, their windows overlapping, and the
//     adoption that ended one of them was of an incarnation the real witness
//     never had (a stray copy's).
//
// Both controllers' files lost with the witness intact is NOT this case,
// nor one controller's file or the witness's alone.
func (r *Record) TwoOfThreeLost() bool {
	dead, stray := r.twoOfThree()
	return dead || stray
}

// TwoOfThreeStray reports the second shape of TwoOfThreeLost: both files
// lost and re-commissioned on a stray's incarnation. Neither controller
// had anything committed, so the committed-incarnation rule doesn't stop
// it, and the floor may miss terms only the lost files knew: I2 and I4
// stay excused there (design §3.1).
func (r *Record) TwoOfThreeStray() bool {
	_, stray := r.twoOfThree()
	return stray
}

// twoOfThree reports TwoOfThreeLost's two shapes: dead, a controller's
// peer's committed incarnation died before it re-commissioned; stray,
// both files lost and re-commissioned on a stray's incarnation.
func (r *Record) twoOfThree() (dead, stray bool) {
	const never = time.Duration(1<<63 - 1)
	type window struct {
		node     Addr
		from, to time.Duration
		inc      uint64 // the incarnation the ending adoption took
	}
	var ws []window
	for _, s := range r.starts {
		if s.readable || (s.node != A && s.node != B) {
			continue
		}
		w := window{node: s.node, from: s.at, to: never}
		for _, a := range r.Adoptions {
			if a.Node == s.node && a.At >= s.at {
				w.to, w.inc = a.At, a.Inc
				break
			}
		}
		ws = append(ws, w)
	}
	for _, w := range ws {
		peer := peerOf(w.node)
		for _, inc := range r.committedIn(peer, w.from, w.to) {
			if r.death(inc) < w.to {
				dead = true
			}
		}
	}
	real := map[uint64]bool{}
	for _, s := range r.starts {
		if s.node == W {
			real[s.inc] = true
		}
	}
	for i, x := range ws {
		for _, y := range ws[i+1:] {
			if x.node != y.node && x.from < y.to && y.from < x.to &&
				(x.to != never && !real[x.inc] || y.to != never && !real[y.inc]) {
				stray = true
			}
		}
	}
	return dead, stray
}

// committedIn returns the incarnations controller a had durably adopted
// (from its file at a start, or by an adoption) at some time in [from, to).
func (r *Record) committedIn(a Addr, from, to time.Duration) []uint64 {
	type ev struct {
		at  time.Duration
		inc uint64
	}
	var evs []ev
	for _, s := range r.starts {
		if s.node == a {
			evs = append(evs, ev{s.at, s.inc})
		}
	}
	for _, ad := range r.Adoptions {
		if ad.Node == a {
			evs = append(evs, ev{ad.At, ad.Inc})
		}
	}
	sort.SliceStable(evs, func(i, j int) bool { return evs[i].at < evs[j].at })
	var incs []uint64
	for i, e := range evs {
		end := to
		if i+1 < len(evs) {
			end = evs[i+1].at
		}
		if e.inc != 0 && e.at < to && end > from && !slices.Contains(incs, e.inc) {
			incs = append(incs, e.inc)
		}
	}
	return incs
}

// death is when witness incarnation inc stopped answering for good: the
// stop of the node running it after which that node never starts again at
// it. Never, while it runs at the end.
func (r *Record) death(inc uint64) time.Duration {
	const never = time.Duration(1<<63 - 1)
	for _, n := range []Addr{W, W2} {
		alive := false
		var stopped time.Duration
		for _, e := range r.lifeOf(n) {
			switch {
			case e.start && e.inc == inc:
				alive = true
			case e.start:
				if alive {
					return stopped // restarted at another incarnation: inc is gone
				}
			case alive:
				alive, stopped = false, e.at
			}
		}
		if stopped != 0 && !alive {
			return stopped
		}
	}
	return never
}

type lifeEvent struct {
	at    time.Duration
	start bool
	inc   uint64
}

// lifeOf merges node n's starts and stops in time order.
func (r *Record) lifeOf(n Addr) []lifeEvent {
	var ev []lifeEvent
	for _, s := range r.starts {
		if s.node == n {
			ev = append(ev, lifeEvent{at: s.at, start: true, inc: s.inc})
		}
	}
	for _, t := range r.stops[n] {
		ev = append(ev, lifeEvent{at: t})
	}
	sort.SliceStable(ev, func(i, j int) bool {
		if ev[i].at != ev[j].at {
			return ev[i].at < ev[j].at
		}
		return !ev[i].start && ev[j].start // a stop before a start at one instant
	})
	return ev
}

// Interval is one stretch of a controller's open gate, in global time:
// [From, To) in Term.
type Interval struct {
	Node     Addr
	Term     uint64
	From, To time.Duration
}

func (iv Interval) String() string {
	return fmt.Sprintf("%v term %d [%v, %v)", iv.Node, iv.Term, iv.From, iv.To)
}

// Gates returns every controller's gate-open intervals, exact: each
// snapshot's lease end mapped through the node's whole clock history.
func (r *Record) Gates() []Interval {
	var out []Interval
	for a := A; a <= B; a++ {
		snaps := r.gates[a]
		var cur *Interval
		for i, s := range snaps {
			if !s.open {
				continue
			}
			next := r.until
			if i+1 < len(snaps) {
				next = snaps[i+1].at
			}
			end := min(next, r.clocks[a].Global(s.endLocal, s.at), r.until)
			if end <= s.at {
				continue
			}
			if cur != nil && cur.Term == s.term && cur.To == s.at {
				cur.To = end
				continue
			}
			out = append(out, Interval{Node: a, Term: s.term, From: s.at, To: end})
			cur = &out[len(out)-1]
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].From < out[j].From })
	return out
}

// promiseEnd is when grant i's promise runs out, in global time.
func (r *Record) promiseEnd(i int) time.Duration {
	if r.ends == nil {
		r.ends = make([]time.Duration, len(r.Grants))
		for j, g := range r.Grants {
			r.ends[j] = min(r.clocks[g.Voter].Global(g.EndLocal, g.Start), r.until)
			for _, rl := range r.released { // one promise per voter: a release ends all its self-promises so far
				if rl.voter == g.Voter && g.To == g.Voter && g.Start <= rl.at {
					r.ends[j] = min(r.ends[j], rl.at)
				}
			}
		}
	}
	return r.ends[i]
}

// counted reports whether g comes from one of the pair's three voters as
// its grantee counted them when it received it: A, B, or a witness copy
// (W, or W2 under the same name) at the incarnation the grantee counted
// then, past its quiet period (design §3.1). Z never counts.
func (r *Record) counted(g Grant) bool {
	switch g.Voter {
	case A, B:
		return true
	case W, W2:
		return g.Recv > 0 && r.countsAt(g.To, g.Inc, g.Recv)
	}
	return false
}

// slot is the voter a grant counts as: both witness copies are "W".
func slot(a Addr) Addr {
	if a == W2 {
		return W
	}
	return a
}

// Violation is one oracle failing.
type Violation struct {
	Oracle string
	At     time.Duration
	What   string
}

func (v Violation) String() string { return fmt.Sprintf("%s at %v: %s", v.Oracle, v.At, v.What) }

// Checks are the per-run parameters of the oracles.
type Checks struct {
	// Exclude names oracles not to report (only for the expected-breach
	// cases, F22b and skew beyond ρ, which assert the breach instead).
	Exclude []string
	// HealAt is when the last fault healed; Bound how soon after it a
	// leader must exist (I6). Zero HealAt skips I6.
	HealAt, Bound time.Duration
	// MaxTakeovers bounds leadership changes after the first election
	// (I7); negative skips it.
	MaxTakeovers int
	// SteadyFrom, SteadyTo is a window with everything up, connected and
	// settled; SteadyVoters the voters that must then back the leader.
	// Zero SteadyTo skips the steady-state check.
	SteadyFrom, SteadyTo time.Duration
	SteadyVoters         []Addr
	// SettleAdoptions, when non-zero, is how long after HealAt the pair may
	// still adopt (the switch hold-off, design §3.1: a copy that answered
	// one controller may have made the pair switch, and the switch back
	// waits up to 32 leases). Steady state is then judged from a quiet
	// period and a lease after the last adoption, and an adoption later
	// than HealAt + SettleAdoptions is a violation ("settle").
	SettleAdoptions time.Duration
}

// Check runs every oracle over r.
func Check(r *Record, c Checks) []Violation {
	var vs []Violation
	add := func(v Violation) {
		if !slices.Contains(c.Exclude, v.Oracle) {
			vs = append(vs, v)
		}
	}
	if dead, stray := r.twoOfThree(); dead || stray {
		// The operator's case: nothing re-commissions without a person
		// (I6, I7, steady). The lost controller waits for the operator
		// rather than re-commission at a floor that may miss its terms
		// (the committed-incarnation rule, design §3.1), so I2 and I4
		// hold, except when both files were lost and a stray re-commissioned
		// them: the rule has nothing committed to go by there. I1, the
		// lease and the ledger always hold.
		c.Exclude = append(slices.Clone(c.Exclude), "I6", "I7", "steady")
		if stray {
			c.Exclude = append(c.Exclude, "I2", "I4")
		}
	}
	gates := r.Gates()
	for _, v := range checkI1(gates) {
		add(v)
	}
	for _, v := range r.checkLease(gates) {
		add(v)
	}
	for _, v := range r.checkLedger() {
		add(v)
	}
	for _, v := range r.checkI2() {
		add(v)
	}
	for _, v := range r.checkI4(gates) {
		add(v)
	}
	if c.HealAt > 0 {
		for _, v := range checkI6(gates, c.HealAt, c.Bound, r.until) {
			add(v)
		}
	}
	if c.MaxTakeovers >= 0 {
		for _, v := range checkI7(gates, c.MaxTakeovers, c.HealAt+c.Bound) {
			add(v)
		}
	}
	if c.SettleAdoptions > 0 && len(r.Adoptions) > 0 {
		last := r.Adoptions[len(r.Adoptions)-1].At
		if last > c.HealAt+c.SettleAdoptions {
			add(Violation{"settle", last, fmt.Sprintf("an adoption %v after the last heal: the switch hold-off allows %v", last-c.HealAt, c.SettleAdoptions)})
		}
		c.SteadyFrom = max(c.SteadyFrom, last+2*r.lease)
	}
	if c.SteadyTo > c.SteadyFrom {
		for _, v := range r.checkSteady(gates, c.SteadyFrom, c.SteadyTo, c.SteadyVoters) {
			add(v)
		}
	}
	sort.SliceStable(vs, func(i, j int) bool { return vs[i].At < vs[j].At })
	return vs
}

// Steady: with all up, connected and settled, one controller holds the
// gate without a break over [from, to), every listed voter holds a running
// promise to it throughout, and no vote term is granted anew in the
// window. Every safety oracle passed while a standby that refused the
// leader churned through terms (#973); this is the oracle that sees that.
func (r *Record) checkSteady(gates []Interval, from, to time.Duration, voters []Addr) []Violation {
	var holder *Interval
	end := from
	for i := range gates {
		iv := &gates[i]
		if iv.To <= from || iv.From >= to {
			continue
		}
		switch {
		case holder == nil && iv.From > from:
			return []Violation{{"steady", from, "no leader when the run settled"}}
		case holder == nil:
			holder = iv
		case iv.Node != holder.Node:
			return []Violation{{"steady", iv.From, fmt.Sprintf("leadership moved to %v after the run settled", iv)}}
		case iv.From != end:
			return []Violation{{"steady", end, fmt.Sprintf("%v's lease broke after the run settled", holder.Node)}}
		}
		end = iv.To
	}
	if holder == nil {
		return []Violation{{"steady", from, "no leader when the run settled"}}
	}
	if end < to {
		return []Violation{{"steady", end, fmt.Sprintf("%v's lease broke after the run settled", holder.Node)}}
	}
	var vs []Violation
	for _, v := range voters {
		// Union of v's running promises to the holder over [from, to).
		cov := from
		for {
			next := cov
			for i, g := range r.Grants {
				if g.Voter == v && g.To == holder.Node && r.counted(g) &&
					g.Start <= cov && r.promiseEnd(i) > next {
					next = r.promiseEnd(i)
				}
			}
			if next == cov {
				break
			}
			cov = next
			if cov >= to {
				break
			}
		}
		if cov < to {
			vs = append(vs, Violation{"steady", cov, fmt.Sprintf("voter %v holds no promise to the leader %v", v, holder.Node)})
		}
	}
	var terms []uint64
	for _, g := range r.Grants {
		if g.Start >= from && g.Start < to && !slices.Contains(terms, g.Term) {
			terms = append(terms, g.Term)
		}
	}
	if len(terms) > 1 {
		vs = append(vs, Violation{"steady", from, fmt.Sprintf("vote terms still moving after the run settled: %v", terms)})
	}
	return vs
}

// I1: no instant has two open gates.
func checkI1(gates []Interval) []Violation {
	var vs []Violation
	for i, x := range gates {
		for _, y := range gates[i+1:] {
			if x.Node != y.Node && x.From < y.To && y.From < x.To {
				vs = append(vs, Violation{"I1", max(x.From, y.From), fmt.Sprintf("%v overlaps %v", x, y)})
			}
		}
	}
	return vs
}

// Lease: while a gate is open in term T, at least two counted voters each
// hold a running promise of T to that controller (ground truth, from the
// ledger, not from the candidate's own bookkeeping).
func (r *Record) checkLease(gates []Interval) []Violation {
	var vs []Violation
	for _, iv := range gates {
		type span struct {
			voter    Addr
			from, to time.Duration
		}
		var spans []span
		points := []time.Duration{iv.From, iv.To}
		for i, g := range r.Grants {
			if g.To != iv.Node || g.Term < iv.Term || !r.counted(g) {
				continue
			}
			s := span{g.Voter, g.Start, r.promiseEnd(i)}
			if s.to <= iv.From || s.from >= iv.To {
				continue
			}
			spans = append(spans, s)
			points = append(points, max(s.from, iv.From), min(s.to, iv.To))
		}
		slices.Sort(points)
		points = slices.Compact(points)
		for k := 0; k+1 < len(points); k++ {
			p := points[k]
			var by [nNodes]bool
			n := 0
			for _, s := range spans {
				if s.from <= p && p < s.to && !by[slot(s.voter)] {
					by[slot(s.voter)] = true
					n++
				}
			}
			if n < 2 {
				vs = append(vs, Violation{"lease", p, fmt.Sprintf("%v open with %d running promise(s) of term %d", iv.Node, n, iv.Term)})
				break
			}
		}
	}
	return vs
}

// Ledger: a voter never grants one candidate while its promise to another
// is still running. Judged per box (Addr): a restart, even with the file
// lost, must not break it (the restart wait).
func (r *Record) checkLedger() []Violation {
	var vs []Violation
	var by [nNodes][]int
	for i, g := range r.Grants {
		by[g.Voter] = append(by[g.Voter], i)
	}
	for _, idx := range by {
		for k, i2 := range idx {
			g2 := r.Grants[i2]
			for _, i1 := range idx[:k] {
				g1 := r.Grants[i1]
				if g1.To == g2.To || g1.Start > g2.Start || g2.Start >= r.promiseEnd(i1) {
					continue
				}
				vs = append(vs, Violation{"ledger", g2.Start, fmt.Sprintf("%v granted %v term %d inside its promise to %v term %d",
					g2.Voter, g2.To, g2.Term, g1.To, g1.Term)})
			}
		}
	}
	return vs
}

// I2: the device sees one writer at a time; a change of writer needs a
// strictly higher term, and no write carries a term below one already
// seen.
func (r *Record) checkI2() []Violation {
	var vs []Violation
	var last *DeviceWrite
	var maxTerm uint64
	for i := range r.Writes {
		w := &r.Writes[i]
		switch {
		case w.Term < maxTerm:
			vs = append(vs, Violation{"I2", w.At, fmt.Sprintf("%v wrote term %d after term %d", w.From, w.Term, maxTerm)})
		case last != nil && w.From != last.From && w.Term <= last.Term:
			vs = append(vs, Violation{"I2", w.At, fmt.Sprintf("%v wrote term %d after %v's term %d", w.From, w.Term, last.From, last.Term)})
		}
		maxTerm = max(maxTerm, w.Term)
		last = w
	}
	return vs
}

// I4: a voter's grants never go down in term, nor give one term to two
// candidates, while its file stays readable; and no term is led twice: by
// two controllers, or by one before and after a restart of it (a restarted
// candidate campaigns above its durable promise, so only a lost file
// could bring a term back: the two-of-three case, design §3.1).
func (r *Record) checkI4(gates []Interval) []Violation {
	var vs []Violation
	for v := Addr(0); v < nNodes; v++ {
		var top uint64
		var topTo Addr
		si := 0
		var vStarts []voterStart
		for _, s := range r.starts {
			if s.node == v {
				vStarts = append(vStarts, s)
			}
		}
		for _, g := range r.Grants {
			if g.Voter != v {
				continue
			}
			for si < len(vStarts) && vStarts[si].at <= g.Start {
				if !vStarts[si].readable {
					top = 0 // the file was lost: a new lifetime
				}
				si++
			}
			switch {
			case g.Term < top:
				vs = append(vs, Violation{"I4", g.Start, fmt.Sprintf("%v granted term %d after term %d", v, g.Term, top)})
			case g.Term == top && g.To != topTo:
				vs = append(vs, Violation{"I4", g.Start, fmt.Sprintf("%v granted term %d to %v and to %v", v, g.Term, topTo, g.To)})
			}
			if g.Term >= top {
				top, topTo = g.Term, g.To
			}
		}
	}
	type led struct {
		node Addr
		to   time.Duration
	}
	leader := map[uint64]led{}
	for _, iv := range gates {
		l, ok := leader[iv.Term]
		if ok && l.node != iv.Node {
			vs = append(vs, Violation{"I4", iv.From, fmt.Sprintf("term %d led by %v and by %v", iv.Term, l.node, iv.Node)})
			continue
		}
		if ok {
			for _, s := range r.starts {
				if s.node == iv.Node && l.to <= s.at && s.at <= iv.From {
					vs = append(vs, Violation{"I4", iv.From, fmt.Sprintf("term %d led by %v again after its restart at %v", iv.Term, iv.Node, s.at)})
					break
				}
			}
		}
		leader[iv.Term] = led{iv.Node, max(l.to, iv.To)}
	}
	return vs
}

// I6: from heal + bound to the end of the run, a leader holds the gate at
// every instant (no leaderless gap once the bound has passed).
func checkI6(gates []Interval, heal, bound, until time.Duration) []Violation {
	at := heal + bound
	for at < until {
		next := at
		for _, iv := range gates {
			if iv.From <= at && at < iv.To {
				next = max(next, iv.To)
			}
		}
		if next == at {
			return []Violation{{"I6", at, fmt.Sprintf("no leader at %v, %v after the last heal", at, at-heal)}}
		}
		at = next
	}
	return nil
}

// I7: at most maxChanges changes of gate holder after the first election,
// and none once settled (after settle). A re-term on renewal moves the
// vote term but not the holder, so it isn't a takeover.
func checkI7(gates []Interval, maxChanges int, settle time.Duration) []Violation {
	var vs []Violation
	n := 0
	for i := 1; i < len(gates); i++ {
		if gates[i].Node == gates[i-1].Node {
			continue
		}
		n++
		if settle > 0 && gates[i].From > settle {
			vs = append(vs, Violation{"I7", gates[i].From, fmt.Sprintf("a takeover (%v) after the run settled", gates[i])})
		}
	}
	if n > maxChanges {
		vs = append(vs, Violation{"I7", 0, fmt.Sprintf("%d takeovers, at most %d expected", n, maxChanges)})
	}
	return vs
}
