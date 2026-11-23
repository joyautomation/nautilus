package node

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/internal/peertls"
	"github.com/joyautomation/nautilus/quorum"
)

// The loopback tests (T2 on the real transport, test plan §3.5's first
// step): two Electors and a Witness in one process, each on its own
// listener on 127.0.0.1, each with its own state file, talking HTTP
// through a transport that can cut any directed link. A monitor reads both
// gates every 100 µs and fails the test if both are open at once, or if
// one term is led by both controllers. Liveness is asserted with generous
// deadlines (tens of leases); safety is asserted strictly.

const loopLease = 500 * time.Millisecond

var errCut = errors.New("link cut by the test")

// cluster is one pair and its witness.
type cluster struct {
	t    *testing.T
	pair string
	mu   sync.Mutex
	cut  map[[2]quorum.NodeID]bool // [from, to]
	mute map[quorum.NodeID]bool    // its vote requests are lost (the rest goes through)
	// noAdopt: every Adopt to or from it is lost (the rest goes through).
	noAdopt map[quorum.NodeID]bool
	// noHello: its hellos to the peer are lost (the rest goes through).
	noHello map[quorum.NodeID]bool
	hellos  map[quorum.NodeID]int    // hellos sent, by sender
	addr    map[quorum.NodeID]string // the name peers use: a DNS name nothing allows
	byHP    map[string]quorum.NodeID
	real    map[string]string // name -> the address it listens on now
	ctl     map[quorum.NodeID]*member
	wit     *witnessMember
	tok     map[quorum.NodeID]string // peer token per node
	ctok    map[quorum.NodeID]string // client token per node
	led     map[uint64]quorum.NodeID // term -> who led it
	stop    chan struct{}
	done    chan struct{}
	evMu    sync.Mutex
	evs     []Event
	bytes   map[quorum.NodeID]*tap
	// plain: the node runs with peer TLS off (its listener and its calls
	// plain HTTP). Every other node runs the pair's peer TLS, keyed by its
	// peer token (#710), as naut run does by default.
	plain map[quorum.NodeID]bool
	pairs map[string]*peertls.Pair // by token
	// fail reports an oracle breach: t.Errorf, or a recorder in the
	// monitor's own test.
	fail func(format string, args ...any)
	// lease is every member's: loopLease, unless a measurement sets
	// another before the start. leaseOf overrides it for one member (the
	// witness's --lease, or one controller's manifest) at its next start.
	lease   time.Duration
	leaseOf map[quorum.NodeID]time.Duration
	// evBy is every event, by the controller that raised it.
	evBy map[quorum.NodeID][]Event
	// votes is every vote request sent, as it left (faultRT).
	votes []sentVote
}

type sentVote struct {
	from, to quorum.NodeID
	at       time.Time
	req      quorum.Request
	// The call's outcome at the transport, once it returned: how long it
	// took and its error (nil: an answer of any status). For steady()'s
	// report when a lease lapses (#1120).
	done bool
	took time.Duration
	err  error
	// verdict is what the answer said (voteVerdict): granted, or why not.
	// Answered in time says nothing of which voter refused a lapsing
	// leader, or why (#1329).
	verdict string
}

type member struct {
	id     quorum.NodeID
	state  string
	e      atomic.Pointer[Elector]
	srv    *http.Server
	cancel context.CancelFunc
	ran    chan struct{}
	sick   atomic.Bool // its scans are not live (SetHealth)
	born   time.Time   // when its current Elector was built
	// A stand-in for the operator-intent log's report (SetLog), when
	// logOn: whether its log is synced, and stuck (leading, catch-up
	// failed).
	logOn, logUnsynced, logStuck, logCannotSync atomic.Bool
}

type witnessMember struct {
	dir    string
	w      *Witness
	srv    *http.Server
	cancel context.CancelFunc
	ran    chan struct{}
}

func newCluster(t *testing.T) *cluster {
	if coreMutation() {
		t.Skip("a core mutation: package quorum's suite and the simulator catch it")
	}
	t.Parallel()
	return newClusterHere(t)
}

// newClusterHere is newCluster without t.Parallel: a measurement that
// times one cluster at a time.
func newClusterHere(t *testing.T) *cluster {
	c := &cluster{t: t, pair: NewClusterID(), lease: loopLease, cut: map[[2]quorum.NodeID]bool{}, addr: map[quorum.NodeID]string{},
		byHP: map[string]quorum.NodeID{}, noHello: map[quorum.NodeID]bool{}, hellos: map[quorum.NodeID]int{}, real: map[string]string{}, ctl: map[quorum.NodeID]*member{},
		tok:  map[quorum.NodeID]string{"a": testToken, "b": testToken, WitnessID: testToken},
		ctok: map[quorum.NodeID]string{"a": "client", "b": "client"}, led: map[uint64]quorum.NodeID{},
		stop: make(chan struct{}), done: make(chan struct{}), bytes: map[quorum.NodeID]*tap{}, fail: t.Errorf,
		plain: map[quorum.NodeID]bool{}, pairs: map[string]*peertls.Pair{}}
	// Peers address each other by name (test plan §8.2 case 2): the
	// dialer maps each name to wherever its node listens now, so a
	// restarted node binds a fresh port.
	for _, id := range []quorum.NodeID{"a", "b", WitnessID} {
		name := fmt.Sprintf("%s.site.test:9450", id)
		c.addr[id], c.byHP[name] = name, id
	}
	dir := t.TempDir()
	c.wit = &witnessMember{dir: dir + "/witness"}
	for _, id := range []quorum.NodeID{"a", "b"} {
		c.ctl[id] = &member{id: id, state: fmt.Sprintf("%s/%s.state", dir, id)}
	}
	t.Cleanup(c.shutdown)
	// Any failure, not only steady()'s, says what each controller's votes
	// did in its last two leases: a /dup or a wait that timed out on a
	// stalled process shows as a gap in the sends (#1120).
	t.Cleanup(func() {
		if t.Failed() {
			for _, id := range []quorum.NodeID{"a", "b"} {
				t.Logf("%s", c.voteTrail(id, leases(2)))
			}
		}
	})
	go c.monitor()
	return c
}

// listen binds a fresh port for id and points its name at it: TLS unless
// id is plain. A tap records the bytes on the wire, under the TLS.
func (c *cluster) listen(id quorum.NodeID) net.Listener {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		c.t.Fatal(err)
	}
	c.mu.Lock()
	c.real[c.addr[id]] = ln.Addr().String()
	tp := c.bytes[id]
	plain := c.plain[id]
	c.mu.Unlock()
	if tp != nil {
		ln = &tapListener{Listener: ln, tap: tp}
	}
	if plain {
		return ln
	}
	return tls.NewListener(ln, c.peerTLS(id).Server)
}

// peerTLS is id's peer TLS, from its peer token.
func (c *cluster) peerTLS(id quorum.NodeID) *peertls.Pair {
	c.mu.Lock()
	defer c.mu.Unlock()
	tok := c.tok[id]
	if p := c.pairs[tok]; p != nil {
		return p
	}
	p, err := peertls.New(tok)
	if err != nil {
		c.t.Fatal(err)
	}
	c.pairs[tok] = p
	return p
}

// clientTLS is the TLS id's calls dial with: nil when id is plain.
func (c *cluster) clientTLS(id quorum.NodeID) *tls.Config {
	c.mu.Lock()
	plain := c.plain[id]
	c.mu.Unlock()
	if plain {
		return nil
	}
	return c.peerTLS(id).Client
}

func (c *cluster) startWitness() {
	c.t.Helper()
	w, err := NewWitness(WitnessOptions{Dir: c.wit.dir, Token: c.tok[WitnessID], Lease: c.leaseFor(WitnessID)})
	if err != nil {
		c.t.Fatal(err)
	}
	h, _ := Guard(c.tok[WitnessID], w.Handler())
	ctx, cancel := context.WithCancel(context.Background())
	c.wit.w, c.wit.cancel, c.wit.ran = w, cancel, make(chan struct{})
	go func() { w.Run(ctx); close(c.wit.ran) }()
	c.wit.srv = &http.Server{Handler: h}
	go c.wit.srv.Serve(c.listen(WitnessID))
}

func (c *cluster) stopWitness() {
	if c.wit.w == nil {
		return
	}
	c.wit.srv.Close()
	c.wit.cancel()
	<-c.wit.ran
	c.wit.w.Close()
	c.wit.w = nil
}

func (c *cluster) start(id quorum.NodeID) *Elector {
	c.t.Helper()
	m := c.ctl[id]
	m.born = time.Now()
	peer := quorum.NodeID("b")
	if id == "b" {
		peer = "a"
	}
	e, err := New(Options{Cluster: c.pair, Self: id, Peer: peer, PeerAddr: c.addr[peer], WitnessAddr: c.addr[WitnessID],
		Lease: c.leaseFor(id), StatePath: m.state, Token: c.tok[id], ClientToken: c.ctok[id],
		SelfAPI: apiAddr(id), TLS: c.clientTLS(id),
		client: &http.Client{Transport: &faultRT{c: c, from: id, base: c.transportFor(id)}},
		OnEvent: func(ev Event) {
			c.evMu.Lock()
			c.evs = append(c.evs, ev)
			if c.evBy == nil {
				c.evBy = map[quorum.NodeID][]Event{}
			}
			c.evBy[id] = append(c.evBy[id], ev)
			c.evMu.Unlock()
		}})
	if err != nil {
		c.t.Fatal(err)
	}
	e.SetHealth(func() bool { return !m.sick.Load() })
	if m.logOn.Load() {
		e.SetLog(func() LogState {
			synced := !m.logUnsynced.Load()
			return LogState{Synced: synced, Stuck: m.logStuck.Load() && e.IsLeader(), CanSync: synced && !m.logCannotSync.Load()}
		})
	}
	h, _ := Guard(c.tok[id], e.Handler())
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel, m.ran = cancel, make(chan struct{})
	go func() { e.Run(ctx); close(m.ran) }()
	m.srv = &http.Server{Handler: h}
	go m.srv.Serve(c.listen(id))
	m.e.Store(e)
	return e
}

// kill stops id's process: no Release, as a power cut.
func (c *cluster) kill(id quorum.NodeID) {
	m := c.ctl[id]
	e := m.e.Swap(nil)
	if e == nil {
		return
	}
	m.srv.Close()
	m.cancel()
	<-m.ran
	e.Close()
}

// apiAddr is a controller's tag API: on its own port, as two boxes whose
// manifests differ only there have it (the standby must name the leader's
// port, not its own: #710 demo finding 1).
func apiAddr(id quorum.NodeID) string {
	if id == "a" {
		return "api-a:8081"
	}
	return "api-" + string(id) + ":8082"
}

func (c *cluster) wipe(id quorum.NodeID) {
	if err := os.Remove(c.ctl[id].state); err != nil {
		c.t.Fatal(err)
	}
}

func (c *cluster) elector(id quorum.NodeID) *Elector { return c.ctl[id].e.Load() }

// isolate cuts every link to and from id.
func (c *cluster) isolate(id quorum.NodeID, on bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, o := range []quorum.NodeID{"a", "b", WitnessID} {
		if o != id {
			c.cut[[2]quorum.NodeID{id, o}] = on
			c.cut[[2]quorum.NodeID{o, id}] = on
		}
	}
}

func (c *cluster) isCut(from, to quorum.NodeID) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cut[[2]quorum.NodeID{from, to}]
}

// transport is the production peer transport, dialing names through the
// cluster's map, with controller a's peer TLS (a second process under a
// name holds the same token).
func (c *cluster) transport() http.RoundTripper { return c.transportFor("a") }

// transportFor is the production peer transport with id's peer TLS.
func (c *cluster) transportFor(id quorum.NodeID) http.RoundTripper {
	tr := newClient(nil, c.clientTLS(id)).Transport.(*http.Transport).Clone()
	var d net.Dialer
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		c.mu.Lock()
		real, ok := c.real[addr]
		c.mu.Unlock()
		if !ok {
			return nil, fmt.Errorf("no such host %s", addr)
		}
		return d.DialContext(ctx, network, real)
	}
	return tr
}

// faultRT applies the cuts: a request on a cut link never arrives; one
// whose way back is cut arrives, and its answer is lost.
type faultRT struct {
	c    *cluster
	from quorum.NodeID
	base http.RoundTripper
}

func (f *faultRT) RoundTrip(r *http.Request) (*http.Response, error) {
	to := f.c.byHP[r.URL.Host]
	sent := -1 // its index in c.votes, when it is a vote
	if strings.HasSuffix(r.URL.Path, "/vote") && r.Body != nil {
		b, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(b))
		var q quorum.Request
		if q.UnmarshalBinary(b) == nil {
			f.c.mu.Lock()
			sent = len(f.c.votes)
			f.c.votes = append(f.c.votes, sentVote{from: f.from, to: to, at: time.Now(), req: q})
			f.c.mu.Unlock()
		}
	}
	resp, err := f.roundTrip(r, to)
	if sent >= 0 {
		verdict := ""
		if err == nil {
			verdict = voteVerdict(resp)
		}
		f.c.mu.Lock()
		v := &f.c.votes[sent]
		v.done, v.took, v.err, v.verdict = true, time.Since(v.at), err, verdict
		f.c.mu.Unlock()
	}
	return resp, err
}

// voteVerdict reads a vote's answer, leaving resp's body for the caller,
// and says what it was: "granted"; "refused, promised <term>" with the
// promise the voter relayed and a mark; or a refusal by status, with its
// RefusedHeader reason and the voter's words.
func voteVerdict(resp *http.Response) string {
	b, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(b))
	if err != nil {
		return "unread: " + err.Error()
	}
	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(b))
		if len(msg) > 160 {
			msg = msg[:160] + "..."
		}
		if why := resp.Header.Get(RefusedHeader); why != "" {
			return fmt.Sprintf("%d %s: %s", resp.StatusCode, why, msg)
		}
		return fmt.Sprintf("%d: %s", resp.StatusCode, msg)
	}
	var rep quorum.Reply
	if err := rep.UnmarshalBinary(b); err != nil {
		return "unreadable: " + err.Error()
	}
	if rep.Granted {
		return "granted"
	}
	out := fmt.Sprintf("refused, promised %d", rep.Promised)
	if rep.Promise.To != "" {
		out += fmt.Sprintf(" (its promise: to %s, %v left)", rep.Promise.To, rep.Promise.Left.Round(time.Millisecond))
	}
	if rep.Marked {
		out += ", marked"
	}
	return out
}

func (f *faultRT) roundTrip(r *http.Request, to quorum.NodeID) (*http.Response, error) {
	if f.c.isCut(f.from, to) {
		return nil, errCut
	}
	f.c.mu.Lock()
	if strings.HasSuffix(r.URL.Path, "/hello") {
		f.c.hellos[f.from]++
	}
	mute := f.c.mute[f.from] && strings.HasSuffix(r.URL.Path, "/vote") ||
		f.c.noHello[f.from] && strings.HasSuffix(r.URL.Path, "/hello") ||
		(f.c.noAdopt[f.from] || f.c.noAdopt[to]) && strings.HasSuffix(r.URL.Path, "/adopt")
	f.c.mu.Unlock()
	if mute {
		return nil, errCut
	}
	resp, err := f.base.RoundTrip(r)
	if err == nil && f.c.isCut(to, f.from) {
		resp.Body.Close()
		return nil, errCut
	}
	return resp, err
}

// monitor is the I1 and I4 oracle on the real gates.
func (c *cluster) monitor() {
	defer close(c.done)
	t := time.NewTicker(100 * time.Microsecond)
	defer t.Stop()
	flip := false
	for {
		select {
		case <-c.stop:
			return
		case <-t.C:
		}
		flip = !flip
		order := []quorum.NodeID{"a", "b"}
		if flip {
			order = []quorum.NodeID{"b", "a"}
		}
		var open []quorum.NodeID
		for _, id := range order {
			e := c.elector(id)
			if e == nil || !e.IsLeader() {
				continue
			}
			open = append(open, id)
			if term := e.Term(); term != 0 {
				c.mu.Lock()
				if who, ok := c.led[term]; ok && who != id {
					c.fail("I4: term %d led by %s and by %s", term, who, id)
				}
				c.led[term] = id
				c.mu.Unlock()
			}
		}
		if len(open) > 1 {
			c.mu.Lock()
			fail := c.fail
			c.mu.Unlock()
			fail("I1: both gates open at once (%v)", open)
		}
	}
}

func (c *cluster) shutdown() {
	close(c.stop)
	<-c.done
	for id := range c.ctl {
		c.kill(id)
	}
	c.stopWitness()
}

// leases is n leases, the unit every deadline here is in.
func leases(n int) time.Duration { return time.Duration(n) * loopLease }

// leases is n of this cluster's leases.
func (c *cluster) leases(n int) time.Duration { return time.Duration(n) * c.lease }

func (c *cluster) waitFor(what string, d time.Duration, ok func() bool) {
	c.t.Helper()
	end := time.Now().Add(d)
	for !ok() {
		if time.Now().After(end) {
			c.t.Fatalf("timed out after %v waiting for %s; a=%+v b=%+v", d, what, c.status("a"), c.status("b"))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (c *cluster) status(id quorum.NodeID) Status {
	if e := c.elector(id); e != nil {
		return e.Status()
	}
	return Status{}
}

// leader is whoever holds the gate now, or "".
func (c *cluster) leader() quorum.NodeID {
	for _, id := range []quorum.NodeID{"a", "b"} {
		if e := c.elector(id); e != nil && e.IsLeader() {
			return id
		}
	}
	return ""
}

// settledLeader waits for a leader whose lease the witness backs too.
// Right after commissioning the witness may still be in its restart wait,
// so the first lease rests on the two controllers alone: a test that cuts
// A–B or replaces the standby at that moment takes a majority away (a
// lapse the protocol allows), not the case it means to test.
func (c *cluster) settledLeader(d time.Duration) quorum.NodeID {
	c.t.Helper()
	who := c.waitLeader(d)
	c.waitFor("the witness to back the leader", d, func() bool {
		ps := c.wit.w.Pairs()
		return len(ps) == 1 && ps[0].GrantedTo == string(who) && c.leader() == who
	})
	return who
}

func (c *cluster) waitLeader(d time.Duration) quorum.NodeID {
	c.t.Helper()
	var who quorum.NodeID
	c.waitFor("a leader", d, func() bool { who = c.leader(); return who != "" })
	return who
}

// commission is the install flow's first step (#710 B2): both controllers
// start uncommissioned and wait for the operator, who re-commissions one
// with the witness's incarnation and a floor above what either knows.
func (c *cluster) commission() {
	c.t.Helper()
	a, b := c.elector("a"), c.elector("b")
	c.waitFor("both controllers to hear the witness", c.leases(20), func() bool {
		return a.Status().WitnessIncarnation != 0 && b.Status().WitnessIncarnation != 0
	})
	if a.Status().Commissioned || b.Status().Commissioned {
		c.t.Fatal("a new pair commissioned itself without the operator")
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.leases(10))
	defer cancel()
	// A margin above the known terms, as the operator's command gives:
	// campaigns move them while the command is typed.
	if err := a.Recommission(ctx, a.Status().WitnessIncarnation, max(a.KnownFloor(), b.KnownFloor())+1000); err != nil {
		c.t.Fatal(err)
	}
	c.waitFor("the pair to commission", c.leases(20), func() bool {
		sa, sb := a.Status(), b.Status()
		return sa.Commissioned && sb.Commissioned && sa.Incarnation == sb.Incarnation
	})
}

func (c *cluster) other(id quorum.NodeID) quorum.NodeID {
	if id == "a" {
		return "b"
	}
	return "a"
}

// steady asserts id leads, in one term, for n leases.
func (c *cluster) steady(id quorum.NodeID, n int) {
	c.t.Helper()
	e := c.elector(id)
	term := e.Term()
	end := time.Now().Add(leases(n))
	var lapsed time.Time // when a lapse at the same term began
	for time.Now().Before(end) {
		switch leading := e.IsLeader(); {
		case e.Term() != term:
			c.t.Fatalf("%s did not hold its lease steadily: leading=%v term %d (was %d); %s", id, leading, e.Term(), term, c.voteTrail(id, leases(2)))
		case leading:
			lapsed = time.Time{}
		case !raceOn:
			c.t.Fatalf("%s did not hold its lease steadily: leading=%v term %d (was %d); %s", id, leading, e.Term(), term, c.voteTrail(id, leases(2)))
		case lapsed.IsZero():
			// Under -race a renewal can come late on a loaded runner
			// (#1120): its gate closes until the renewal lands, at the same
			// term. Allowed for up to a lease. Half a lease was not enough:
			// a CI race job saw 255 ms. A yield or a lost lease still fails
			// under -race: the gate stays shut past a lease, or opens again
			// at a new term (a new lease after any gap gets one). Without
			// -race, and in the mutation runner, any lapse fails.
			lapsed = time.Now()
		case time.Since(lapsed) > loopLease:
			c.t.Fatalf("%s did not hold its lease steadily: not leading for %v at term %d; %s", id, time.Since(lapsed), term, c.voteTrail(id, leases(2)))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// voteTrail sums up, then lists, the vote requests id sent in the last
// span, with each call's outcome: when a lease lapses it tells a renewal
// never sent (the process stalled: a long gap between sends) from one sent
// and answered late, or not at all (#1120), and an answer in time from
// what it said: granted, or refused and why (#1329). The summary is one
// line, so
// the mutation runner, which keeps only report lines, keeps it.
func (c *cluster) voteTrail(id quorum.NodeID, span time.Duration) string {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	type sum struct {
		sent, answered, refused, failed, pending int
		slowest                                  time.Duration
		lastErr                                  error
	}
	per := map[quorum.NodeID]*sum{}
	var detail strings.Builder
	var prev time.Time
	var gap time.Duration
	for _, v := range c.votes {
		if v.from != id || now.Sub(v.at) > span {
			continue
		}
		if !prev.IsZero() {
			gap = max(gap, v.at.Sub(prev))
		}
		prev = v.at
		s := per[v.to]
		if s == nil {
			s = &sum{}
			per[v.to] = s
		}
		s.sent++
		out := "pending"
		switch {
		case v.done && v.err != nil:
			s.failed++
			s.lastErr = v.err
			out = fmt.Sprintf("%v: %v", v.took.Round(time.Millisecond), v.err)
		case v.done:
			s.answered++
			s.slowest = max(s.slowest, v.took)
			out = fmt.Sprintf("answered in %v", v.took.Round(time.Millisecond))
			if v.verdict != "" {
				out += ": " + v.verdict
			}
			if v.verdict != "" && v.verdict != "granted" {
				s.refused++
			}
		default:
			s.pending++
		}
		fmt.Fprintf(&detail, "\n  -%v to %s term %d renewal=%v %s", now.Sub(v.at).Round(time.Millisecond), v.to, v.req.Term, v.req.Renewal, out)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "votes %s sent in the last %v:", id, span)
	if prev.IsZero() {
		b.WriteString(" none")
	} else {
		fmt.Fprintf(&b, " longest gap between sends %v, none since %v ago;", gap.Round(time.Millisecond), now.Sub(prev).Round(time.Millisecond))
	}
	for _, to := range []quorum.NodeID{"a", "b", WitnessID} {
		if s := per[to]; s != nil {
			fmt.Fprintf(&b, " to %s: %d sent, %d answered (slowest %v), %d refused, %d failed, %d pending", to, s.sent, s.answered, s.slowest.Round(time.Millisecond), s.refused, s.failed, s.pending)
			if s.lastErr != nil {
				fmt.Fprintf(&b, " (last: %v)", s.lastErr)
			}
			b.WriteString(";")
		}
	}
	return b.String() + detail.String()
}

// leaseFor is id's lease at its next start.
func (c *cluster) leaseFor(id quorum.NodeID) time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	if d, ok := c.leaseOf[id]; ok {
		return d
	}
	return c.lease
}

// setLease gives id a lease of its own from its next start.
func (c *cluster) setLease(id quorum.NodeID, d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.leaseOf == nil {
		c.leaseOf = map[quorum.NodeID]time.Duration{}
	}
	c.leaseOf[id] = d
}

func (c *cluster) startAll() {
	c.startWitness()
	c.start("a")
	c.start("b")
}

// F1-style: a new pair waits for the operator, then elects one leader that
// renews in one term while the standby sees it.
func TestLoopbackNewPairCommissionsAndElects(t *testing.T) {
	c := newCluster(t)
	c.startAll()
	c.commission()
	who := c.settledLeader(leases(20))
	c.steady(who, 8)
	c.waitFor("the standby to see the leader", leases(4), func() bool {
		s := c.status(c.other(who))
		return s.Leader == string(who) && s.LeaderAddr == apiAddr(who)
	})
	if s := c.status(who); s.Leader != string(who) || s.LeaderAddr != apiAddr(who) {
		t.Fatalf("the leader's status %+v", s)
	}
	if ps := c.wit.w.Pairs(); len(ps) != 1 || ps[0].Cluster != c.pair || ps[0].Members != "a,b" || ps[0].Incarnation != c.status("a").Incarnation {
		t.Fatalf("witness pairs %+v", ps)
	}
}

// F3: the leader's process dies (no release): the standby takes over with
// a higher term, and never while the old gate could be open. The dead one
// restarts from its state file and rejoins as the standby.
func TestLoopbackLeaderDiesStandbyTakesOver(t *testing.T) {
	c := newCluster(t)
	c.startAll()
	c.commission()
	old := c.settledLeader(leases(20))
	term := c.elector(old).Term()
	c.kill(old)
	next := c.waitLeader(leases(20))
	if next == old || c.elector(next).Term() <= term {
		t.Fatalf("after %s died: leader %s, term %d (was %d)", old, next, c.elector(next).Term(), term)
	}
	c.start(old)
	c.waitFor("the restarted controller to see the leader", leases(10), func() bool { return c.status(old).Leader == string(next) })
	c.steady(next, 8)
	if s := c.status(old); !s.Commissioned {
		t.Fatalf("restarted from its state file, uncommissioned: %+v", s)
	}
}

// F5-style: the leader is cut off from everything. Its gate closes when
// its lease runs out, and only then does the standby's open. Healed, it
// stays the standby.
func TestLoopbackLeaderIsolated(t *testing.T) {
	c := newCluster(t)
	c.startAll()
	c.commission()
	old := c.settledLeader(leases(20))
	c.isolate(old, true)
	c.waitFor("the isolated leader's gate to close", leases(2), func() bool { return !c.elector(old).IsLeader() })
	next := c.waitLeader(leases(20))
	if next == old {
		t.Fatal("the isolated controller leads")
	}
	c.isolate(old, false)
	c.steady(next, 10)
}

// One-way partition (F4, the sim's TestTargetedOneWayLeaderBacksOff): the
// leader's messages still arrive everywhere, but nothing reaches it, so
// it hears no grant. Its gate closes when its lease runs out, it backs
// off, and the standby leads in a higher term, never overlapping it.
func TestLoopbackLeaderDeaf(t *testing.T) {
	c := newCluster(t)
	c.startAll()
	c.commission()
	old := c.settledLeader(leases(20))
	term := c.elector(old).Term()
	c.mu.Lock()
	for _, o := range []quorum.NodeID{c.other(old), WitnessID} {
		c.cut[[2]quorum.NodeID{o, old}] = true
	}
	c.mu.Unlock()
	c.waitFor("the deaf leader's gate to close", leases(2), func() bool { return !c.elector(old).IsLeader() })
	next := c.waitLeader(leases(20))
	if next == old || c.elector(next).Term() <= term {
		t.Fatalf("leader %s term %d after %s went deaf in term %d", next, c.elector(next).Term(), old, term)
	}
	c.steady(next, 8)
}

// The A–B link cut, both still reaching the witness: the leader keeps
// its lease on {itself, witness}, and the standby, refused by the witness,
// never leads. Healed, nothing changes.
func TestLoopbackPeerLinkCut(t *testing.T) {
	c := newCluster(t)
	c.startAll()
	c.commission()
	old := c.settledLeader(leases(20))
	c.mu.Lock()
	c.cut[[2]quorum.NodeID{"a", "b"}] = true
	c.cut[[2]quorum.NodeID{"b", "a"}] = true
	c.mu.Unlock()
	c.steady(old, 10)
	c.mu.Lock()
	c.cut = map[[2]quorum.NodeID]bool{}
	c.mu.Unlock()
	c.steady(old, 5)
}

// A mute leader: nothing it sends arrives, though it hears everything.
// Its lease lapses, its gate closes, and the standby leads in a higher
// term, never overlapping it.
func TestLoopbackLeaderMute(t *testing.T) {
	c := newCluster(t)
	c.startAll()
	c.commission()
	old := c.settledLeader(leases(20))
	term := c.elector(old).Term()
	c.mu.Lock()
	for _, o := range []quorum.NodeID{c.other(old), WitnessID} {
		c.cut[[2]quorum.NodeID{old, o}] = true
	}
	c.mu.Unlock()
	c.waitFor("the mute leader's gate to close", leases(2), func() bool { return !c.elector(old).IsLeader() })
	var next quorum.NodeID
	c.waitFor("the standby to lead", leases(20), func() bool { next = c.leader(); return next == c.other(old) })
	if c.elector(next).Term() <= term {
		t.Fatalf("term %d after the mute leader's %d", c.elector(next).Term(), term)
	}
	c.steady(next, 6)
}

// The witness restarts from its state (same incarnation; the pair goes
// on), then is rebuilt with its state wiped: a new incarnation, which both
// controllers adopt together, while the leader is never doubled.
func TestLoopbackWitnessRestartAndRebuild(t *testing.T) {
	c := newCluster(t)
	c.startAll()
	c.commission()
	who := c.settledLeader(leases(20))
	inc := c.status("a").Incarnation
	c.stopWitness()
	c.startWitness()
	c.steady(who, 8)
	if a, b := c.status("a").Incarnation, c.status("b").Incarnation; a != inc || b != inc {
		t.Fatalf("incarnations %d, %d after a restart from state, want %d", a, b, inc)
	}
	c.stopWitness()
	files, _ := filepath.Glob(filepath.Join(c.wit.dir, "*"))
	for _, f := range files {
		if err := os.Remove(f); err != nil {
			t.Fatal(err)
		}
	}
	c.startWitness()
	c.waitFor("the pair to adopt the rebuilt witness", leases(60), func() bool {
		a, b := c.status("a"), c.status("b")
		return a.Incarnation != inc && a.Incarnation == b.Incarnation && a.Commissioned && b.Commissioned
	})
	if ps := c.wit.w.Pairs(); len(ps) != 1 || ps[0].Incarnation != c.status("a").Incarnation {
		t.Fatalf("witness %+v, pair at %d", ps, c.status("a").Incarnation)
	}
	c.waitLeader(leases(20))
}

// Scans not live close the leader's gate at its next look, well inside
// its lease, and it stops renewing: the standby takes over. Live again,
// it stays the standby.
func TestLoopbackUnhealthyLeaderClosesItsGate(t *testing.T) {
	c := newCluster(t)
	c.startAll()
	c.commission()
	old := c.settledLeader(leases(20))
	c.steady(old, 2)
	c.ctl[old].sick.Store(true)
	start := time.Now()
	c.waitFor("the unhealthy leader's gate to close", loopLease/2, func() bool { return !c.elector(old).IsLeader() })
	if d := time.Since(start); d > loopLease/4+loopLease/8 {
		t.Fatalf("the gate closed %v after the scans stopped, want within a look", d)
	}
	next := c.waitLeader(leases(20))
	if next == old {
		t.Fatal("the unhealthy controller leads")
	}
	c.ctl[old].sick.Store(false)
	c.steady(next, 6)
}

// Release closes the gate at once; the peer takes over a lease later.
func TestLoopbackReleaseClosesTheGate(t *testing.T) {
	c := newCluster(t)
	c.startAll()
	c.commission()
	old := c.settledLeader(leases(20))
	c.elector(old).Release()
	if c.elector(old).IsLeader() {
		t.Fatal("still leading after Release")
	}
	next := c.waitLeader(leases(20))
	if next == old {
		t.Fatal("the released controller leads")
	}
	c.steady(next, 4)
}

// The restart hold on the real transport (#1021 review): a standby
// restarted right after the pair's first election, while the witness may
// not back the leader yet, sends no campaign until its own voter's restart
// wait is over; before the hold it campaigned at once and could take the
// witness's first grant from the leader.
func TestLoopbackRestartedStandbyHoldsItsCampaigns(t *testing.T) {
	c := newCluster(t)
	c.startAll()
	c.commission()
	who := c.waitLeader(leases(20)) // not settled: the case needs it raw
	sb := c.other(who)
	c.kill(sb)
	c.start(sb)
	born := c.ctl[sb].born
	time.Sleep(leases(3))
	c.mu.Lock()
	for _, v := range c.votes {
		if v.from == sb && v.req.Term != 0 && !v.req.Renewal && v.at.After(born) && v.at.Before(born.Add(loopLease)) {
			c.mu.Unlock()
			t.Fatalf("the restarted %s campaigned (term %d) %v after its start, inside its voter's restart wait (%v)",
				sb, v.req.Term, v.at.Sub(born), loopLease)
		}
	}
	c.mu.Unlock()
	c.settledLeader(leases(20))
}

// F8: the witness is down. The two controllers are a majority: the leader
// keeps its lease in its term.
func TestLoopbackWitnessDownLeaderHolds(t *testing.T) {
	c := newCluster(t)
	c.startAll()
	c.commission()
	who := c.settledLeader(leases(20))
	c.steady(who, 2)
	c.stopWitness()
	c.steady(who, 10)
}

// Restarts of the standby: from its state file it stays commissioned at
// the same incarnation; with its file wiped (#976) it comes back
// uncommissioned, grants nothing, and re-adopts its peer's committed
// incarnation (the witness alive) on its own. The leader holds throughout.
func TestLoopbackStandbyRestarts(t *testing.T) {
	c := newCluster(t)
	c.startAll()
	c.commission()
	who := c.settledLeader(leases(20))
	sb := c.other(who)
	inc, gen := c.status(sb).Incarnation, c.status(sb).Gen
	c.kill(sb)
	c.start(sb)
	if s := c.status(sb); !s.Commissioned || s.Incarnation != inc {
		t.Fatalf("restarted from its file: %+v, want incarnation %d", s, inc)
	}
	c.steady(who, 4)
	c.kill(sb)
	c.wipe(sb)
	c.start(sb)
	if s := c.status(sb); s.Commissioned {
		t.Fatalf("restarted with no file, commissioned: %+v", s)
	}
	c.waitFor("the wiped standby to re-adopt", leases(20), func() bool {
		s := c.status(sb)
		return s.Commissioned && s.Incarnation == inc && s.Gen > gen
	})
	c.steady(who, 4)
}

// A controller with the wrong peer token is refused by both other voters:
// it never hears the pair, never leads, and the leader holds on {itself,
// witness}.
func TestLoopbackWrongPeerTokenRefused(t *testing.T) {
	c := newCluster(t)
	c.startAll()
	c.commission()
	who := c.settledLeader(leases(20))
	sb := c.other(who)
	c.kill(sb)
	c.mu.Lock()
	c.tok[sb] = "not-the-peer-token-but-just-as-long-0123456789abcdef"
	c.mu.Unlock()
	c.start(sb)
	c.steady(who, 12)
	if s := c.status(sb); s.PeerAge != -1 || s.WitnessAge != -1 || s.Leading {
		t.Fatalf("the controller with the wrong token was answered: %+v", s)
	}
}

// Client tokens that differ between A and B are reported at the handshake
// (design §6.1, test plan §8.2 case 5), and neither token crosses the peer
// port.
func TestLoopbackClientTokenDiffers(t *testing.T) {
	c := newCluster(t)
	c.ctok["a"], c.ctok["b"] = "client-token-of-a-1234", "client-token-of-b-5678"
	c.bytes["a"], c.bytes["b"] = &tap{}, &tap{}
	c.startAll()
	a, b := c.elector("a"), c.elector("b")
	ctx := context.Background()
	c.waitFor("the client-token check", leases(20), func() bool {
		a.checkToken(ctx)
		b.checkToken(ctx)
		return a.Status().ClientToken == TokenDiffers && b.Status().ClientToken == TokenDiffers
	})
	c.waitFor("the event", leases(4), func() bool {
		c.evMu.Lock()
		defer c.evMu.Unlock()
		for _, ev := range c.evs {
			if ev.Kind == EventClientToken && ev.Detail == "differs" {
				return true
			}
		}
		return false
	})
	for id, tp := range c.bytes {
		seen := tp.bytes()
		if len(seen) == 0 {
			t.Fatalf("the tap on %s saw nothing", id)
		}
		for _, tok := range []string{c.ctok["a"], c.ctok["b"]} {
			if bytes.Contains(seen, []byte(tok)) {
				t.Fatalf("a client token crossed %s's peer port", id)
			}
		}
	}
}

// The monitor has teeth: two gates forced open at once, or one term
// published by both controllers, are both reported.
func TestLoopbackMonitorTeeth(t *testing.T) {
	c := newCluster(t)
	var mu sync.Mutex
	var got []string
	c.mu.Lock()
	c.fail = func(f string, args ...any) {
		mu.Lock()
		got = append(got, fmt.Sprintf(f, args...))
		mu.Unlock()
	}
	c.mu.Unlock()
	clk := newFakeClock()
	for _, id := range []quorum.NodeID{"a", "b"} {
		o := testOptions(t, id, c.other(id))
		o.clock = clk.now
		e, err := New(o)
		if err != nil {
			t.Fatal(err)
		}
		defer e.Close()
		e.gate.Store(&gateState{epoch: 9, end: clk.now().Add(time.Hour), open: true})
		c.ctl[id].e.Store(e)
	}
	waitFor(t, 2*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		var i1, i4 bool
		for _, g := range got {
			i1 = i1 || bytes.HasPrefix([]byte(g), []byte("I1:"))
			i4 = i4 || bytes.HasPrefix([]byte(g), []byte("I4:"))
		}
		return i1 && i4
	})
	for _, id := range []quorum.NodeID{"a", "b"} {
		c.ctl[id].e.Store(nil)
	}
}

// The same client token on both: same.
func TestLoopbackClientTokenSame(t *testing.T) {
	c := newCluster(t)
	c.startAll()
	a := c.elector("a")
	c.waitFor("the client-token check", leases(20), func() bool {
		a.checkToken(context.Background())
		return a.Status().ClientToken == TokenSame
	})
}

// tap records every byte a listener's connections read and write.
type tap struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (t *tap) write(p []byte) {
	t.mu.Lock()
	t.b.Write(p)
	t.mu.Unlock()
}

func (t *tap) bytes() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]byte(nil), t.b.Bytes()...)
}

type tapListener struct {
	net.Listener
	tap *tap
}

func (l *tapListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &tapConn{Conn: conn, tap: l.tap}, nil
}

type tapConn struct {
	net.Conn
	tap *tap
}

func (c *tapConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.tap.write(p[:n])
	return n, err
}

func (c *tapConn) Write(p []byte) (int, error) {
	c.tap.write(p)
	return c.Conn.Write(p)
}
