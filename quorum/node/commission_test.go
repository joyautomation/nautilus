package node

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/quorum"
)

// #1029 3c-3c: commission, recommission and recommission --from-spare.

func opCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), leases(40))
	t.Cleanup(cancel)
	return ctx
}

// settledNewPair starts a new pair and waits until every member can answer
// the commission's check (settled, the witness's pair voter running).
func (c *cluster) settledNewPair() {
	c.t.Helper()
	c.startAll()
	a, b := c.elector("a"), c.elector("b")
	c.waitFor("both controllers to hear the witness", leases(20), func() bool {
		return a.Status().WitnessIncarnation != 0 && b.Status().WitnessIncarnation != 0
	})
	time.Sleep(leases(2)) // every voter settled: a lease and D since start
}

// commission on a new pair: every member answers, none was commissioned;
// the floor goes a margin above every term the members know, both
// controllers adopt the witness's incarnation, and a leader is elected.
func TestCommissionCommissionsANewPair(t *testing.T) {
	c := newCluster(t)
	c.settledNewPair()
	rep, err := c.elector("a").Commission(opCtx(t), "op")
	if err != nil {
		t.Fatalf("commission: %v (%s)", err, rep.Refused)
	}
	if len(rep.Members) != 3 || rep.Members[2].Member != "witness" || rep.Incarnation == 0 || rep.Incarnation != rep.Members[2].Incarnation {
		t.Fatalf("report %+v", rep)
	}
	var highest uint64
	for _, m := range rep.Members {
		highest = max(highest, m.Highest())
	}
	// A new pair's witness has promised terms to the campaigns already.
	if rep.Floor != highest+commissionMargin || commissionMargin > MaxTermJump/2 {
		t.Fatalf("floor %d: want the margin %d above the members' %d, within MaxTermJump/2", rep.Floor, commissionMargin, highest)
	}
	c.waitFor("the pair to commission", leases(20), func() bool {
		sa, sb := c.status("a"), c.status("b")
		return sa.Commissioned && sb.Commissioned && sa.Incarnation == rep.Incarnation && sb.Incarnation == rep.Incarnation
	})
	c.steady(c.settledLeader(leases(20)), 4)
}

// commission refuses a pair a controller says is commissioned: here b,
// after a re-install of a (its state wiped). Nothing changes: a stays uncommissioned until it re-adopts from
// b on its own. commission_unchecked commissions it over the pair.
func TestCommissionRefusesAPairThatWasCommissioned(t *testing.T) {
	c := newCluster(t)
	c.startAll()
	c.commission()
	c.settledLeader(leases(20))
	c.mu.Lock()
	c.noAdopt = map[quorum.NodeID]bool{"a": true} // a can't re-adopt from b meanwhile
	c.mu.Unlock()
	c.kill("a")
	c.wipe("a")
	a := c.start("a")
	time.Sleep(leases(2))
	rep, err := a.Commission(opCtx(t), "op")
	if !errors.Is(err, ErrCommandRefused) {
		t.Fatalf("commission over a commissioned pair: %v %+v", err, rep)
	}
	if !strings.Contains(rep.Refused, "b is commissioned") {
		t.Fatalf("the refusal doesn't name b: %s", rep.Refused)
	}
	if s := c.status("a"); s.Floor != 0 || s.Commissioned {
		t.Fatalf("a refused commission changed a: %+v", s)
	}
}

// commission needs every member to answer: with the witness stopped it
// refuses, naming it, and changes nothing.
func TestCommissionNeedsEveryMember(t *testing.T) {
	c := newCluster(t)
	c.settledNewPair()
	c.stopWitness()
	rep, err := c.elector("a").Commission(opCtx(t), "op")
	if !errors.Is(err, ErrCommandRefused) || !strings.Contains(rep.Refused, "witness is not reachable") {
		t.Fatalf("commission without the witness: %v %s", err, rep.Refused)
	}
	if c.status("a").Commissioned || c.status("a").Floor != 0 {
		t.Fatal("a refused commission changed a's state")
	}
}

// The floor's evidence is every answering member's highest term, not only
// this controller's: a controller that lost its file knows little, and a
// floor under the peer's terms would grant one again.
// recommission_local_floor_only takes this controller's alone.
func TestTheFloorsEvidenceIsEveryMembersTerms(t *testing.T) {
	ms := []MemberState{
		{Member: "a", KnownFloor: 10},
		{Member: "b", Promised: 50, Floor: 30},
		{Member: "witness", Promised: 40},
	}
	ev, who, unheard := floorEvidence(ms)
	if ev != 50 || who != "b" || len(unheard) != 0 {
		t.Fatalf("evidence %d from %s, unheard %v: want 50 from b", ev, who, unheard)
	}
	ms[1] = MemberState{Member: "b", Err: "b is not reachable"}
	ev, who, unheard = floorEvidence(ms)
	if ev != 40 || who != "witness" || !slices.Equal(unheard, []string{"b"}) {
		t.Fatalf("evidence %d from %s, unheard %v: want 40 from the witness, b unheard", ev, who, unheard)
	}
}

// Both controllers lost their state: recommission prints the evidence and
// refuses without a floor, refuses a floor at or under the evidence and
// another incarnation than the witness's, then re-commissions at a floor
// above it, and the pair leads again.
func TestRecommissionTakesAFloorAboveTheEvidence(t *testing.T) {
	c := newCluster(t)
	c.startAll()
	c.commission()
	c.settledLeader(leases(20))
	var promised uint64
	c.witnessVoter(func(v *quorum.Voter, _ quorum.Instant) { promised = v.State().Promised })
	for _, id := range []quorum.NodeID{"a", "b"} {
		c.kill(id)
		c.wipe(id)
	}
	a := c.start("a")
	c.start("b")
	c.waitFor("a to hear the witness", leases(20), func() bool { return a.Status().WitnessIncarnation != 0 })
	time.Sleep(leases(2))
	rep, err := a.RecommissionChecked(opCtx(t), "op", RecommissionRequest{})
	if !errors.Is(err, ErrCommandRefused) || rep.Evidence < promised || rep.Suggested <= rep.Evidence || !strings.Contains(rep.Refused, "give a floor above") {
		t.Fatalf("no floor: %v evidence %d (witness promised %d) suggested %d: %s", err, rep.Evidence, promised, rep.Suggested, rep.Refused)
	}
	if rep, err := a.RecommissionChecked(opCtx(t), "op", RecommissionRequest{Floor: rep.Evidence}); !errors.Is(err, ErrCommandRefused) || !strings.Contains(rep.Refused, "is not above") {
		t.Fatalf("a floor at the evidence: %v %s", err, rep.Refused)
	}
	if rep, err := a.RecommissionChecked(opCtx(t), "op", RecommissionRequest{Floor: rep.Suggested, Incarnation: rep.Members[2].Incarnation + 1}); !errors.Is(err, ErrCommandRefused) || !strings.Contains(rep.Refused, "witness's incarnation") {
		t.Fatalf("another incarnation: %v %s", err, rep.Refused)
	}
	if c.status("a").Commissioned {
		t.Fatal("a refused re-commission commissioned a")
	}
	done, err := a.RecommissionChecked(opCtx(t), "op", RecommissionRequest{Floor: rep.Suggested})
	if err != nil {
		t.Fatalf("re-commission: %v %s", err, done.Refused)
	}
	c.waitFor("the pair to commission again", leases(20), func() bool {
		return c.status("a").Commissioned && c.status("b").Commissioned
	})
	c.settledLeader(leases(20))
}

// A controller whose state was written on another box is re-commissioned
// only on the operator's word that the box was replaced (#1029 3c-3c):
// RecommissionBy refuses it with ErrBoxChanged, the checked command
// without BoxReplaced too. box_replaced_unchecked re-commissions it.
func TestABoxChangedControllerNeedsBoxReplaced(t *testing.T) {
	o := testOptions(t, "a", "b")
	o.Box = BoxFingerprint("machine-new", []string{"aa:bb:cc:00:00:02"})
	st := &State{Path: o.StatePath, Owner: "a", Cluster: testCluster, Box: BoxFingerprint("machine-1", []string{"aa:bb:cc:00:00:01"})}
	if err := st.Save(quorum.VoterState{Promised: 1002, GrantedTo: "a", Incarnation: 7, Gen: 1, Floor: 1000, Lease: o.Lease}); err != nil {
		t.Fatal(err)
	}
	e, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if e.Status().BoxChanged == "" || e.Status().Commissioned {
		t.Fatalf("status %+v: want a box change, uncommissioned", e.Status())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := e.RecommissionBy(ctx, "op", 7, 5000); !errors.Is(err, ErrBoxChanged) || !strings.Contains(err.Error(), "--box-replaced") {
		t.Fatalf("re-commission of a box-changed controller: %v, want ErrBoxChanged naming --box-replaced", err)
	}
	// Past the box check with the operator's word (the write then waits
	// for a writer this unit test doesn't run).
	if _, err := e.recommission(ctx, "op", 7, 5000, true, 0); errors.Is(err, ErrBoxChanged) {
		t.Fatalf("re-commission with --box-replaced: %v", err)
	}
}

// startSpare starts a second process under id's name from a spare's state
// file of its own (a copy made a spare), with no listener: a spare
// answers nothing, and the original keeps its address.
func (c *cluster) startSpare(id quorum.NodeID) *Elector {
	c.t.Helper()
	path := c.ctl[id].state + ".copy"
	if err := (&State{Path: path, Owner: id, Cluster: c.pair}).Save(quorum.VoterState{Spare: true}); err != nil {
		c.t.Fatal(err)
	}
	peer := c.other(id)
	e, err := New(Options{Cluster: c.pair, Self: id, Peer: peer, PeerAddr: c.addr[peer], WitnessAddr: c.addr[WitnessID],
		Lease: loopLease, StatePath: path, Token: c.tok[id], ClientToken: c.ctok[id],
		// Its own links: a cut of the original's leaves the copy's.
		client: &http.Client{Transport: &faultRT{c: c, from: "copy", base: c.transport()}}})
	if err != nil {
		c.t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ran := make(chan struct{})
	go func() { e.Run(ctx); close(ran) }()
	c.t.Cleanup(func() { cancel(); <-ran; e.Close() })
	if !e.Spare() {
		c.t.Fatal("the copy did not start as a spare")
	}
	return e
}

// --from-spare refuses while either other voter hears a process of the
// name: here the original a runs, and the witness can't hear it (a cut),
// so only b's voter does. Nothing changes: the spare's file stays, no
// boot is retired. from_spare_one_voter (the witness alone) and
// from_spare_own_view (the spare's own, silent voter) go ahead.
func TestFromSpareRefusesWhileEitherVoterHearsTheName(t *testing.T) {
	c := newCluster(t)
	c.startAll()
	c.commission()
	c.settledLeader(leases(20))
	sp := c.startSpare("a")
	c.mu.Lock()
	c.cut[[2]quorum.NodeID{"a", WitnessID}], c.cut[[2]quorum.NodeID{WitnessID, "a"}] = true, true
	c.mu.Unlock()
	time.Sleep(leases(2)) // the witness no longer hears a's boot live
	rep, err := sp.FromSpare(opCtx(t), "op")
	if !errors.Is(err, ErrCommandRefused) || !strings.Contains(rep.Refused, "b hear(s) a process named a") || strings.Contains(rep.Refused, "witness hear") {
		t.Fatalf("from-spare while b hears a: %v %+v", err, rep)
	}
	if _, err := os.Stat(c.ctl["a"].state + ".copy"); err != nil {
		t.Fatalf("the spare's file moved on a refusal: %v", err)
	}
	c.witnessVoter(func(v *quorum.Voter, _ quorum.Instant) {
		if len(v.RetiredBoots("a")) != 0 {
			t.Fatalf("the witness retired %v on a refusal", v.RetiredBoots("a"))
		}
	})
	if !sp.Spare() || sp.Status().FromSpare != "" {
		t.Fatal("a refused from-spare changed the spare")
	}
	// The spare's /dup requests carry no boot: no voter learns its nonce.
	known := func(v *quorum.Voter) {
		if slices.ContainsFunc(v.KnownBoots("a"), func(b quorum.BootSeq) bool { return b.Nonce == sp.nonce }) {
			t.Fatalf("a voter knows the spare's boot %x after its requests: %v", sp.nonce, v.KnownBoots("a"))
		}
	}
	c.witnessVoter(func(v *quorum.Voter, _ quorum.Instant) { known(v) })
	b := c.elector("b")
	b.mu.Lock()
	known(b.v)
	b.mu.Unlock()
}

// --from-spare runs only on a spare. from_spare_not_spare runs it on a
// member (b here), where the check would refuse for another reason.
func TestFromSpareRunsOnlyOnASpare(t *testing.T) {
	c := newCluster(t)
	c.startAll()
	c.commission()
	c.settledLeader(leases(20))
	rep, err := c.elector("b").FromSpare(opCtx(t), "op")
	if !errors.Is(err, ErrCommandRefused) || !strings.Contains(rep.Refused, "not a spare") {
		t.Fatalf("from-spare on a member: %v %s", err, rep.Refused)
	}
}

// The way back: a's original is stopped for good and the box left is a
// spare (its file says so). --from-spare checks that neither other voter
// hears a, retires every boot of a they knew (the original's among them)
// keeping none, and moves the spare's state file aside; the spare stays
// silent. Restarted, it starts uncommissioned, re-adopts from b, and
// leads once b is gone. The original, come back with its old boot's
// frames, would be marked: its boot is retired.
func TestFromSpareTurnsASpareIntoTheMember(t *testing.T) {
	c := newCluster(t)
	c.startAll()
	c.commission()
	c.settledLeader(leases(20))
	old := c.elector("a").nonce
	c.kill("a")
	// The box left holds a's file, made a spare's.
	st := &State{Path: c.ctl["a"].state, Owner: "a", Cluster: c.pair}
	vs, err := st.Load()
	if err != nil {
		t.Fatal(err)
	}
	vs.Spare = true
	if err := st.Save(vs); err != nil {
		t.Fatal(err)
	}
	sp := c.start("a")
	if !sp.Spare() {
		t.Fatal("not a spare")
	}
	// Boots of a only the witness knows (markOld's 0xA1 and 0xC1, long
	// silent): b must retire them too, from the check's union.
	c.witnessVoter(func(v *quorum.Voter, now quorum.Instant) { markOld(t, v, now, "a") })
	time.Sleep(leases(2)) // a's old boot unheard for a lease and D
	rep, err := sp.FromSpare(opCtx(t), "op")
	if err != nil {
		t.Fatalf("from-spare: %v %+v", err, rep)
	}
	if !slices.ContainsFunc(rep.Retired, func(b quorum.BootSeq) bool { return b.Nonce == old }) {
		t.Fatalf("the original's boot %x is not among the retired %v", old, rep.Retired)
	}
	c.witnessVoter(func(v *quorum.Voter, _ quorum.Instant) {
		if !slices.Contains(v.RetiredBoots("a"), old) || v.DupGen("a") == 0 || slices.Contains(v.Marked(), "a") {
			t.Fatalf("the witness: retired %v, gen %d, marked %v: want the original's boot retired, a generation, no mark", v.RetiredBoots("a"), v.DupGen("a"), v.Marked())
		}
	})
	bv := c.elector("b")
	bv.mu.Lock()
	bRetired, bGen := bv.v.RetiredBoots("a"), bv.v.DupGen("a")
	bv.mu.Unlock()
	if !slices.Contains(bRetired, 0xC1) || !slices.Contains(bRetired, old) || bGen == 0 {
		t.Fatalf("b retired %v under gen %d: want the witness-only boot c1 and the original's, under a generation", bRetired, bGen)
	}
	if _, err := os.Stat(c.ctl["a"].state); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the spare's state file is still in place: %v", err)
	}
	if aside, _ := filepath.Glob(c.ctl["a"].state + ".previous-spare-*"); len(aside) != 1 || rep.MovedAside != aside[0] {
		t.Fatalf("moved aside to %q, found %v", rep.MovedAside, aside)
	}
	if s := sp.Status(); !s.Spare || s.FromSpare != rep.MovedAside {
		t.Fatalf("the spare's status after the command: %+v", s)
	}
	if again, err := sp.FromSpare(opCtx(t), "op"); !errors.Is(err, ErrCommandRefused) || !strings.Contains(again.Refused, "Restart naut") {
		t.Fatalf("a second run: %v %s", err, again.Refused)
	}
	time.Sleep(leases(1))
	if _, err := os.Stat(c.ctl["a"].state); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a write after the move put the spare's state file back")
	}
	c.kill("a")
	a := c.start("a")
	c.waitFor("the new a to re-adopt from b", leases(30), func() bool {
		s := a.Status()
		return s.Commissioned && !s.Spare && s.Incarnation == c.status("b").Incarnation
	})
	c.kill("b")
	c.waitFor("a to lead", leases(30), func() bool { return a.IsLeader() })
}

// After MoveAside every Save is refused, so a write still queued can't put
// the spare's file back with its flag. from_spare_write_after_move writes
// it.
func TestNoWriteAfterTheMoveAside(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quorum.state")
	st := &State{Path: path, Owner: "a", Cluster: testCluster}
	if err := st.Save(quorum.VoterState{Spare: true}); err != nil {
		t.Fatal(err)
	}
	aside, err := st.MoveAside(path + ".previous-spare-1")
	if err != nil || aside != path+".previous-spare-1" {
		t.Fatalf("move aside: %q %v", aside, err)
	}
	if err := st.Save(quorum.VoterState{Spare: true}); err == nil || !strings.Contains(err.Error(), "moved aside") {
		t.Fatalf("a save after the move: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the state file is back")
	}
}

// The from-spare check keeps no boot (naut-lead's ruling on 3c-3c lifts
// #1135's "nothing to keep" on this path only): every boot either voter
// knew is retired, under a generation above both; one hearing a boot, or
// one not answering, refuses. The clear-duplicate check still refuses a
// clear with nothing to keep (TestTheCheckPhaseKeepsOneBootAndRetiresTheRest).
func TestTheFromSpareCheckKeepsNoBoot(t *testing.T) {
	b := []DupAnswer{
		{Member: "b", Known: []quorum.BootSeq{{Nonce: 0xA1, Seq: 3}}, Gen: 2, HoldGen: 5},
		{Member: "witness", Known: []quorum.BootSeq{{Nonce: 0xA1, Seq: 4}, {Nonce: 0xC1, Seq: 1}}, Gen: 1},
	}
	retire, gen, refused := fromSpareCheck("a", b)
	if refused != "" || gen != 6 || len(retire) != 2 {
		t.Fatalf("retire %v gen %d refused %q", retire, gen, refused)
	}
	if _, _, _, why := checkPhase("a", []DupAnswer{{Member: "a"}, b[0], b[1]}); !strings.Contains(why, "nothing running under a to keep") {
		t.Fatalf("clear-duplicate's check with nothing to keep: %q", why)
	}
	b[0].Boots = []uint64{0xA1}
	if _, _, refused := fromSpareCheck("a", b); !strings.Contains(refused, "b hear(s)") {
		t.Fatalf("b hears a: %q", refused)
	}
	b[0].Boots, b[1].Err = nil, "witness is not reachable"
	if _, _, refused := fromSpareCheck("a", b); !strings.Contains(refused, "both other voters must answer") {
		t.Fatalf("the witness silent: %q", refused)
	}
}

// commission refuses while a controller holds a floor (a commission or a
// re-commission under way there), and while one answers but has not
// settled; the witness's own terms and floor never refuse it (a new
// pair's witness promises terms to the campaigns before commissioning).
func TestTheCommissionCheck(t *testing.T) {
	fresh := func() []MemberState {
		return []MemberState{
			{Member: "a", Settled: true, KnownFloor: 9},
			{Member: "b", Settled: true, KnownFloor: 9},
			{Member: "witness", Settled: true, Incarnation: 7, Promised: 9},
		}
	}
	if why := commissionCheck(fresh()); why != "" {
		t.Fatalf("a new pair refused: %s", why)
	}
	ms := fresh()
	ms[1].Floor = 1009
	if why := commissionCheck(ms); !strings.Contains(why, "b holds a floor") || !strings.Contains(why, "recommission --floor above 1009") {
		t.Fatalf("b holding a floor: %q", why)
	}
	ms = fresh()
	ms[0].Settled = false
	if why := commissionCheck(ms); !strings.Contains(why, "a started less than a lease") {
		t.Fatalf("a unsettled: %q", why)
	}
	ms = fresh()
	ms[0].BoxChanged = "NIC"
	if why := commissionCheck(ms); !strings.Contains(why, "recommission --box-replaced") {
		t.Fatalf("a box-changed: %q", why)
	}
	ms = fresh()
	ms[2].Incarnation = 0
	if why := commissionCheck(ms); !strings.Contains(why, "no incarnation") {
		t.Fatalf("no witness incarnation: %q", why)
	}
}

// A new pair waiting for its commission is quiet: each uncommissioned
// controller looks at most every lease/8 (uncommissionedEvery). Unpaced,
// every answer poked the next look at once, and the two ping-ponged query
// Adopts and witness probes at network speed (some 30,000 requests a
// second each, 40,000 sockets left in TIME_WAIT per test).
// uncommissioned_looks_unpaced turns it red.
func TestANewPairWaitingForCommissionIsQuiet(t *testing.T) {
	c := newCluster(t)
	c.settledNewPair()
	count := func() int {
		c.mu.Lock()
		defer c.mu.Unlock()
		return len(c.votes)
	}
	before := count()
	time.Sleep(leases(2))
	// Two controllers, a probe to the witness (and maybe the peer) per
	// look, 8 looks a lease: 64 over two leases, twice that as slack.
	if n := count() - before; n > 128 {
		t.Fatalf("%d vote frames in two leases from a pair waiting for commission: want at most 128", n)
	}
	if c.status("a").Commissioned || c.status("b").Commissioned {
		t.Fatal("the pair commissioned itself")
	}
}

// spareWithFile makes a's next process a spare from a's own file (a is
// stopped first), on transport rt.
func (c *cluster) spareWithFile(rt http.RoundTripper) *Elector {
	c.t.Helper()
	c.kill("a")
	st := &State{Path: c.ctl["a"].state, Owner: "a", Cluster: c.pair}
	vs, err := st.Load()
	if err != nil {
		c.t.Fatal(err)
	}
	vs.Spare = true
	if err := st.Save(vs); err != nil {
		c.t.Fatal(err)
	}
	e, err := New(Options{Cluster: c.pair, Self: "a", Peer: "b", PeerAddr: c.addr["b"], WitnessAddr: c.addr[WitnessID],
		Lease: loopLease, StatePath: c.ctl["a"].state, Token: c.tok["a"], ClientToken: c.ctok["a"], client: &http.Client{Transport: rt}})
	if err != nil {
		c.t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ran := make(chan struct{})
	go func() { e.Run(ctx); close(ran) }()
	c.t.Cleanup(func() { cancel(); <-ran; e.Close() })
	if !e.Spare() {
		c.t.Fatal("not a spare")
	}
	return e
}

// failNth fails the nth /dup request (1-based) of its client.
type failNth struct {
	base http.RoundTripper
	n    int32
	seen atomic.Int32
}

func (f *failNth) RoundTrip(r *http.Request) (*http.Response, error) {
	if strings.HasSuffix(r.URL.Path, "/dup") && f.seen.Add(1) == f.n {
		return nil, errors.New("test: link dropped")
	}
	return f.base.RoundTrip(r)
}

// A --from-spare clear that misses a voter moves nothing aside: the spare
// stays a spare, and running it again finishes (#1157 review). The /dup
// requests are, in order: the peer's state, the peer's and the witness's
// checks, the peer's and the witness's clears. When the peer misses its
// clear, the witness is not asked: a voter that refuses heard the name
// since the check, and a clear at the other would mark that process there.
// from_spare_moves_after_partial and from_spare_clears_past_refusal turn
// it red.
func TestAFromSpareClearThatMissesAVoterMovesNothing(t *testing.T) {
	for _, tc := range []struct {
		name string
		nth  int32
	}{{"the peer missed", 4}, {"the witness missed", 5}} {
		t.Run(tc.name, func(t *testing.T) {
			c := newCluster(t)
			c.startAll()
			c.commission()
			c.settledLeader(leases(20))
			old := c.elector("a").nonce
			e := c.spareWithFile(&failNth{base: &faultRT{c: c, from: "copy", base: c.transport()}, n: tc.nth})
			time.Sleep(leases(2))
			rep, err := e.FromSpare(opCtx(t), "op")
			if !errors.Is(err, ErrFromSpareIncomplete) {
				t.Fatalf("want incomplete, got %v %+v", err, rep)
			}
			if _, err := os.Stat(c.ctl["a"].state); err != nil || !e.Spare() {
				t.Fatalf("a partial clear moved the spare's state file (%v) or unspared it", err)
			}
			if tc.nth == 4 {
				c.witnessVoter(func(v *quorum.Voter, _ quorum.Instant) {
					if len(v.RetiredBoots("a")) != 0 {
						t.Fatalf("the witness cleared after the peer missed: retired %v", v.RetiredBoots("a"))
					}
				})
			}
			if rep, err := e.FromSpare(opCtx(t), "op"); err != nil {
				t.Fatalf("the re-run: %v %+v", err, rep)
			}
			c.witnessVoter(func(v *quorum.Voter, _ quorum.Instant) {
				if !slices.Contains(v.RetiredBoots("a"), old) {
					t.Fatalf("the re-run did not retire the original's boot: %v", v.RetiredBoots("a"))
				}
			})
		})
	}
}

// --from-spare needs the other controller commissioned: it is what the
// restarted spare re-adopts from, and with it uncommissioned the spare's
// file may hold the pair's only terms (#1157 review). from_spare_peer_unchecked
// moves it aside.
func TestFromSpareNeedsACommissionedPeer(t *testing.T) {
	c := newCluster(t)
	c.startAll()
	c.commission()
	c.settledLeader(leases(20))
	e := c.spareWithFile(&faultRT{c: c, from: "copy", base: c.transport()})
	c.kill("b")
	c.wipe("b")
	c.start("b")
	time.Sleep(leases(2))
	rep, err := e.FromSpare(opCtx(t), "op")
	if !errors.Is(err, ErrCommandRefused) || !strings.Contains(rep.Refused, "b is not commissioned") {
		t.Fatalf("from-spare with b uncommissioned: %v %+v", err, rep)
	}
	if _, err := os.Stat(c.ctl["a"].state); err != nil {
		t.Fatalf("the spare's file moved: %v", err)
	}
}

// The re-commission's refusals, the evidence comparison included: a floor
// at or under a term the peer or the witness knows is refused though this
// controller (one that lost its file) knows less.
func TestTheRecommissionRefusals(t *testing.T) {
	self := MemberState{Member: "a", KnownFloor: 10}
	wit := MemberState{Member: "witness", Incarnation: 7, Promised: 40}
	ms := []MemberState{self, {Member: "b", Promised: 50}, wit}
	ev, who, unheard := floorEvidence(ms)
	for _, c := range []struct {
		name string
		self MemberState
		req  RecommissionRequest
		want string
	}{
		{"above the peer's terms", self, RecommissionRequest{Floor: 51}, ""},
		{"at the peer's term", self, RecommissionRequest{Floor: 50}, "is not above 50, the highest term b knows"},
		{"above this one's, under the peer's", self, RecommissionRequest{Floor: 30}, "is not above 50"},
		{"no floor", self, RecommissionRequest{}, "give a floor above 50"},
		{"another incarnation", self, RecommissionRequest{Floor: 51, Incarnation: 8}, "witness's incarnation for this pair is 7"},
		{"commissioned", MemberState{Member: "a", Commissioned: true, Incarnation: 7}, RecommissionRequest{Floor: 51}, "is commissioned"},
		{"box changed", MemberState{Member: "a", BoxChanged: "NIC"}, RecommissionRequest{Floor: 51}, "--box-replaced"},
		{"box replaced", MemberState{Member: "a", BoxChanged: "NIC"}, RecommissionRequest{Floor: 51, BoxReplaced: true}, ""},
	} {
		got := recommissionRefusal(c.self, wit, ev, who, unheard, c.req)
		if (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	if got := recommissionRefusal(self, MemberState{Member: "witness", Err: "witness is not reachable"}, ev, who, unheard, RecommissionRequest{Floor: 51}); !strings.Contains(got, "the witness must answer") {
		t.Errorf("no witness: %q", got)
	}
}
