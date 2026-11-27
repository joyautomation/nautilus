package node

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/internal/peertls"
	"github.com/joyautomation/nautilus/quorum"
)

// A witness reply counts only under the certificate of its own
// incarnation (#710 2b): one whose certificate names another (a token
// holder answering as a fresh incarnation, pinned at its first contact)
// is heard by nothing, and the pair runs on its two controllers. The
// pair's own commissioning rode witness@0 for its first vote: that reply
// did not count, the connection was closed, and the next vote met the
// incarnation's certificate.
func TestLoopbackAWitnessReplyCountsOnlyUnderItsIncarnation(t *testing.T) {
	c := newCluster(t)
	c.startAll()
	c.commission()
	who := c.settledLeader(leases(20))
	inc := c.wit.w.Incarnation(c.pair)
	if inc == 0 {
		t.Fatal("the witness has no incarnation for the pair")
	}
	// The leader's renewals are what hear the witness (a standby's relay
	// probes don't count as hearing it).
	heard := func() time.Duration { return c.elector(who).Status().WitnessAge }
	fresh := func() bool { a := heard(); return a >= 0 && a < c.lease }
	c.waitFor("the leader hears the witness", leases(20), fresh)
	// The witness's certificate now names another incarnation than its
	// replies carry.
	c.witCertLie.Store(1)
	// A restart drops every connection at the witness's end. CloseIdle
	// alone left one that was in use at that moment (#1120, CI on #1401):
	// it kept the old certificate, the counted one, so nothing closed it
	// and the controllers never met the other.
	c.stopWitness()
	c.startWitness()
	time.Sleep(6 * c.lease)
	if age := heard(); age >= 0 && age < 4*c.lease {
		t.Errorf("%s heard the witness %v ago under a certificate naming witness@%d: its reply counted", who, age, inc+1)
	}
	for _, id := range []quorum.NodeID{"a", "b"} {
		if p := c.peerTLS(id).Pinned(""); p[peertls.WitnessName(inc+1)] == "" {
			t.Errorf("%s never met the other certificate (pins %v): the test proves nothing", id, p)
		}
	}
	if l := c.elector(who).Status(); !l.Leading {
		t.Errorf("%s lost the lead without the witness: %+v", who, l)
	}
	// A call that is not a vote: only from the incarnation the pair
	// counts.
	ms := c.elector("a").membersState(context.Background())
	if w := ms[2]; !strings.Contains(w.Err, "does not count") {
		t.Fatalf("the witness's state under another incarnation's certificate: %+v, want refused", w)
	}
	c.witCertLie.Store(0)
	for _, id := range []quorum.NodeID{"a", "b"} {
		c.elector(id).CloseIdle()
	}
	c.waitFor("the witness heard again", leases(20), fresh)
	if w := c.elector("a").membersState(context.Background())[2]; w.Err != "" {
		t.Fatalf("the witness's state under its own certificate: %s", w.Err)
	}
}

// A reply from a witness incarnation this commissioned controller has not
// adopted, bound to its own certificate, reaches the adoption alone (#710
// 2b): its fence, marks and held state count for nothing until the pair
// adopts it. Without that, one reply from a token holder answering as a
// fresh incarnation fences a controller (superseded: it campaigns no
// more, across restarts, until an operator clears it).
func TestLoopbackAnUnadoptedWitnessReplyReachesOnlyTheAdoption(t *testing.T) {
	c := newCluster(t)
	c.startAll()
	c.commission()
	c.settledLeader(leases(20))
	e := c.elector("b")
	inc := e.AdoptedIncarnation()
	if inc == 0 {
		t.Fatal("b adopted nothing")
	}
	rep := quorum.Reply{From: WitnessID, To: "b", Nonce: e.nonce, Incarnation: inc + 1, Fence: 1 << 40, Marked: true, Promised: 1 << 30}
	if e.witnessReplyCounts(WitnessID, peertls.WitnessName(inc+1), rep) {
		t.Fatal("a reply bound to an unadopted incarnation goes to reply() (row unadopted_reply_heard)")
	}
	if !e.witnessReplyCounts(WitnessID, peertls.WitnessName(inc), quorum.Reply{From: WitnessID, To: "b", Incarnation: inc}) {
		t.Fatal("a reply bound to the adopted incarnation does not count")
	}
	time.Sleep(2 * c.lease)
	if st := e.Status(); st.Superseded != "" || st.Held || len(st.Marked) != 0 {
		t.Fatalf("an unadopted incarnation's reply counted: superseded %q, held %v, marked %v", st.Superseded, st.Held, st.Marked)
	}
}

// A refusal under the certificate of an incarnation the pair does not
// count raises nothing (#710 2b): a token holder answering as a fresh
// incarnation can't raise or clear a controller's lease condition with
// its own text. The same refusal under the counted incarnation's
// certificate is raised (the control).
func TestLoopbackAnUnboundWitnessRefusalRaisesNothing(t *testing.T) {
	c := newCluster(t)
	c.startAll()
	c.commission()
	who := c.settledLeader(leases(20))
	from := map[quorum.NodeID]int{"a": c.eventCount("a"), "b": c.eventCount("b")}
	c.witCertLie.Store(1)
	c.stopWitness()
	c.setLease(WitnessID, loopLease/2)
	c.startWitness()
	lie := time.Now()
	for _, id := range []quorum.NodeID{"a", "b"} {
		c.elector(id).CloseIdle()
	}
	if raceOn {
		// With the witness uncounted the leader rests on its peer's vote
		// alone, and under -race on a loaded runner four renewals in a row
		// can miss their lease/8 (#1120: CI run 45912113980, calls back 30
		// to 110 ms past their deadline). A lapse is allowed here; what the
		// test is for is the lease reports, checked below either way. The
		// pair must still lead.
		time.Sleep(leases(6))
		c.waitLeader(leases(20))
	} else {
		c.steady(who, 6)
	}
	// The refusals the test is about reached both controllers, more than
	// once each (a raise follows its refusal at once, long before the next).
	c.waitFor("the witness to refuse both controllers' votes by its lease, under the uncounted certificate", leases(20), func() bool {
		return c.leaseRefusals("a", lie) >= 2 && c.leaseRefusals("b", lie) >= 2
	})
	for _, id := range []quorum.NodeID{"a", "b"} {
		c.noLeaseReports(id, from[id])
	}
	c.witCertLie.Store(0)
	for _, id := range []quorum.NodeID{"a", "b"} {
		c.elector(id).CloseIdle()
	}
	for _, id := range []quorum.NodeID{"a", "b"} {
		c.waitLease(id, WitnessID, false, "longer than this witness's")
	}
}

// A refusal by status under an uncounted witness certificate closes the
// kept connections too (#1120, the unbound refusal): it has no reply to
// bind, and a kept connection would keep that certificate for as long as
// the witness only refuses, so the controller would never hear the
// refusal under the right one. A refusal under witness@0 and a body that
// is not the witness's reply close them the same way. At most once a
// lease (the client is shared with the peer's votes, and a witness at
// another incarnation refuses every vote until an operator acts), and
// never on no answer at all (a timeout: no certificate to drop).
func TestAnUnboundWitnessRefusalClosesTheConnections(t *testing.T) {
	newE := func(now *quorum.Instant) (*Elector, *closeSpy) {
		spy := &closeSpy{}
		e := &Elector{o: Options{Witness: WitnessID, Lease: loopLease}, log: slog.Default(), now: func() quorum.Instant { return *now },
			call: caller{tls: true, client: &http.Client{Transport: spy}}}
		e.adopted.Store(7)
		return e, spy
	}
	lease409 := &statusError{code: http.StatusConflict, reason: "lease"}
	for _, c := range []struct {
		name string
		cn   string
		body []byte
		err  error
	}{
		{"a 409 lease under another incarnation", "witness@8", nil, lease409},
		{"a 409 term-jump under witness@0", "witness@0", nil, &statusError{code: http.StatusConflict, reason: "term-jump"}},
		{"a body that is not a reply", "witness@8", []byte("not a reply"), nil},
	} {
		now := quorum.Instant(0)
		e, spy := newE(&now)
		e.unboundWitnessReply(WitnessID, c.cn, c.body, c.err)
		if spy.closed != 1 {
			t.Errorf("%s: %d closes, want 1", c.name, spy.closed)
		}
	}

	now := quorum.Instant(0)
	e, spy := newE(&now)
	e.unboundWitnessReply(WitnessID, "", nil, context.DeadlineExceeded)
	if spy.closed != 0 {
		t.Fatalf("no answer (a timeout): %d closes, want none", spy.closed)
	}
	e.unboundWitnessReply(WitnessID, "witness@8", nil, lease409)
	now = now.Add(loopLease - time.Millisecond)
	e.unboundWitnessReply(WitnessID, "witness@8", nil, lease409)
	if spy.closed != 1 {
		t.Fatalf("two refusals inside a lease: %d closes, want 1", spy.closed)
	}
	now = now.Add(time.Millisecond)
	e.unboundWitnessReply(WitnessID, "witness@8", nil, lease409)
	if spy.closed != 2 {
		t.Fatalf("a refusal a lease after the close: %d closes, want 2", spy.closed)
	}
}

// leaseRefusals counts id's votes to the witness, sent since, that it
// refused by its lease (a 409 "lease").
func (c *cluster) leaseRefusals(id quorum.NodeID, since time.Time) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, v := range c.votes {
		if v.from == id && v.to == WitnessID && v.done && !v.at.Before(since) && strings.HasPrefix(v.verdict, "409 lease") {
			n++
		}
	}
	return n
}

// closeSpy counts CloseIdleConnections.
type closeSpy struct {
	http.RoundTripper
	closed int
}

func (s *closeSpy) CloseIdleConnections() { s.closed++ }

// A witness reply refused by its certificate closes the kept connections
// (#710 2b): a pair's first vote rides witness@0 (the witness draws the
// pair's incarnation answering it), and a kept connection would keep that
// certificate for every later vote. A bound reply closes nothing.
func TestARefusedWitnessReplyClosesTheConnections(t *testing.T) {
	spy := &closeSpy{}
	e := &Elector{o: Options{Witness: WitnessID}, log: slog.Default(), call: caller{tls: true, client: &http.Client{Transport: spy}}}
	if e.witnessBound(WitnessID, "witness@0", quorum.Reply{Incarnation: 7}) {
		t.Fatal("a reply over witness@0 counted")
	}
	if spy.closed != 1 {
		t.Fatalf("%d closes after a reply over witness@0, want 1", spy.closed)
	}
	if !e.witnessBound(WitnessID, "witness@7", quorum.Reply{Incarnation: 7}) || spy.closed != 1 {
		t.Fatalf("a bound reply: closes %d", spy.closed)
	}
	if e.witnessBound(WitnessID, "witness@8", quorum.Reply{Incarnation: 7}) || spy.closed != 2 {
		t.Fatalf("a reply under another incarnation's certificate: closes %d", spy.closed)
	}
}

// A witness reply counts only when it carries the incarnation its
// certificate names (#710 2b), even when that certificate is the adopted
// incarnation's: the adoption check (witnessOpOK) reads the certificate
// alone, so this is the case only the binding refuses. (The loopback test
// above, a certificate naming another incarnation than the adopted one,
// is refused by the adoption check too, #1096.)
func TestAReplyCarryingAnotherIncarnationThanItsCertificateDoesNotCount(t *testing.T) {
	spy := &closeSpy{}
	e := &Elector{o: Options{Witness: WitnessID}, log: slog.Default(), call: caller{tls: true, client: &http.Client{Transport: spy}}}
	e.adopted.Store(7)
	if e.witnessReplyCounts(WitnessID, "witness@7", quorum.Reply{From: WitnessID, To: "b", Incarnation: 8}) {
		t.Fatal("a reply carrying incarnation 8 under the adopted witness@7's certificate counted")
	}
	if !e.witnessReplyCounts(WitnessID, "witness@7", quorum.Reply{From: WitnessID, To: "b", Incarnation: 7}) {
		t.Fatal("a bound reply from the adopted incarnation does not count")
	}
}
