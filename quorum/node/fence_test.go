package node

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/quorum"
)

// #1029 3c-3c-1b: recommission --from-spare fences the name it replaces.
// The old box, switched back on with its old disk, carries a state from
// before the command: the peer and the witness refuse it at any term.

// fromSpareReplaced commissions a pair, runs --from-spare on a's spare and
// restarts it as the replacement, re-adopted from b. It returns a's state
// file as it was while a was the member (the old box's disk) and the
// command's report.
func fromSpareReplaced(t *testing.T) (*cluster, []byte, FromSpareReport) {
	t.Helper()
	c, old, sp := spareOfA(t)
	rep, err := sp.FromSpare(opCtx(t), "op")
	if err != nil {
		t.Fatalf("from-spare: %v %+v", err, rep)
	}
	c.kill("a")
	a := c.start("a")
	c.waitFor("the replacement to re-adopt from b", leases(30), func() bool {
		s := a.Status()
		return s.Commissioned && !s.Spare && s.Incarnation == c.status("b").Incarnation
	})
	return c, old, rep
}

// spareOfA commissions a pair, stops a and restarts its box as a spare,
// a's old boot unheard for a lease and D: --from-spare may run. It
// returns a's state file as it was while a was the member.
func spareOfA(t *testing.T) (*cluster, []byte, *Elector) {
	t.Helper()
	c := newCluster(t)
	c.startAll()
	c.commission()
	c.settledLeader(leases(20))
	c.kill("a")
	old, err := os.ReadFile(c.ctl["a"].state)
	if err != nil {
		t.Fatal(err)
	}
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
	time.Sleep(leases(2)) // a's old boot unheard for a lease and D
	return c, old, sp
}

// The command's last phase fences a at b, then at the witness at b's
// value, above every generation a's old state can carry; the replacement
// re-adopts at it. Restarted on its old disk, the box that was a is
// refused at both, raises EventSuperseded, campaigns no more, and never
// leads, at any term; b leads. fence_requests_off and fence_unnoted turn
// it red.
func TestTheOldBoxIsFencedAfterFromSpare(t *testing.T) {
	c, old, rep := fromSpareReplaced(t)
	var oldState quorum.VoterState
	if err := oldState.UnmarshalBinary(old); err != nil {
		t.Fatal(err)
	}
	if len(rep.Fence) != 2 || rep.Fence[0].Fence <= oldState.Gen || rep.Fence[1].Fence != rep.Fence[0].Fence {
		t.Fatalf("fence phase %+v: want b and the witness fenced above the old state's generation %d, the witness at b's value", rep.Fence, oldState.Gen)
	}
	var witFence uint64
	c.witnessVoter(func(v *quorum.Voter, _ quorum.Instant) { witFence = v.FenceOf("a") })
	if witFence != rep.Fence[0].Fence {
		t.Fatalf("the witness's fence on a is %d, want %d", witFence, rep.Fence[0].Fence)
	}
	if g := c.elector("a").v.State().Gen; g < witFence {
		t.Fatalf("the replacement re-adopted at generation %d, below the fence %d", g, witFence)
	}
	// The old box returns: the replacement goes, its old disk comes back.
	c.kill("a")
	if err := os.WriteFile(c.ctl["a"].state, old, 0o600); err != nil {
		t.Fatal(err)
	}
	a := c.start("a")
	c.waitFor("the old box to learn it is fenced", leases(20), func() bool { return a.Status().Superseded != "" })
	c.waitFor("the old box's alarm", leases(4), func() bool { n, _ := c.countEvents(EventSuperseded); return n > 0 })
	if n, detail := c.countEvents(EventSuperseded); n != 1 || !strings.Contains(detail, "from before a was replaced") || !strings.Contains(detail, "Wipe its state") {
		t.Fatalf("the old box's alarm: %d times, %q", n, detail)
	}
	if l := c.settledLeader(leases(20)); l != "b" {
		t.Fatalf("leader %s, want b", l)
	}
	for range 8 {
		if a.IsLeader() {
			t.Fatal("the old box leads")
		}
		time.Sleep(leases(1) / 2)
	}
	c.kill("b") // the old box alone with the witness: refused there too
	time.Sleep(leases(4))
	if a.IsLeader() {
		t.Fatal("the old box leads with the witness")
	}
}

// After a --from-spare, both controllers lose their files and an
// operator re-commissions: the witness keeps a's fence, so the
// re-commission adopts at or above it (MemberState.Fence), and a leads
// with the witness once b is gone. recommission_under_fence adopts at 1,
// below it: a can't lead.
func TestARecommissionAfterAFromSpareIsNotFenced(t *testing.T) {
	c, _, _ := fromSpareReplaced(t)
	for _, id := range []quorum.NodeID{"a", "b"} {
		c.kill(id)
		c.wipe(id)
	}
	a := c.start("a")
	c.start("b")
	c.waitFor("a to hear the witness", leases(20), func() bool { return a.Status().WitnessIncarnation != 0 })
	time.Sleep(leases(2))
	probe, _ := a.RecommissionChecked(opCtx(t), "op", RecommissionRequest{})
	if probe.Members[2].Fence == 0 {
		t.Fatalf("the witness reports no fence: %+v", probe.Members[2])
	}
	done, err := a.RecommissionChecked(opCtx(t), "op", RecommissionRequest{Floor: probe.Suggested})
	if err != nil {
		t.Fatalf("re-commission: %v %s", err, done.Refused)
	}
	c.waitFor("the pair to commission again", leases(20), func() bool {
		return c.status("a").Commissioned && c.status("b").Commissioned
	})
	if g := a.v.State().Gen; g < probe.Members[2].Fence {
		t.Fatalf("re-commissioned at generation %d, below the witness's fence %d", g, probe.Members[2].Fence)
	}
	c.kill("b")
	c.waitFor("a to lead with the witness", leases(30), func() bool { return a.IsLeader() })
}

// A clear or a fence is answered only once it is on disk (#1164 review,
// S1): with the witness's directory unwritable, its clear fails, and the
// command ends INCOMPLETE, the spare's file in place, never "done".
// dup_answered_before_write reports the witness cleared and fenced, and
// moves the file aside on records a crash loses.
func TestAFromSpareWhoseWitnessCantWriteIsIncomplete(t *testing.T) {
	c, _, sp := spareOfA(t)
	if err := os.Chmod(c.wit.dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(c.wit.dir, 0o700) })
	rep, err := sp.FromSpare(opCtx(t), "op")
	if !errors.Is(err, ErrFromSpareIncomplete) || rep.MovedAside != "" {
		t.Fatalf("from-spare with the witness unable to write: %v %+v", err, rep)
	}
	last := rep.Clear[len(rep.Clear)-1]
	if last.Member != "witness" || !strings.Contains(last.Err, "could not be written") {
		t.Fatalf("the witness's clear: %+v", last)
	}
	if _, err := os.Stat(c.ctl["a"].state); err != nil {
		t.Fatalf("the spare's file moved: %v", err)
	}
}

// A member takes a fence only on a pair controller's name, and a
// controller only on its peer's (#1164 review, S3): a fence on the
// witness's name would make the candidate drop every witness reply, one
// on its own name means nothing. fence_any_name takes them.
func TestAFenceNamesOnlyAPairController(t *testing.T) {
	c := newCluster(t)
	c.startAll()
	c.commission()
	c.settledLeader(leases(20))
	time.Sleep(leases(2)) // every voter settled: a lease and D since start
	a := c.elector("a")
	for _, tc := range []struct{ member, base, name string }{
		{"b", a.peer, "witness"}, {"b", a.peer, "b"}, {"witness", a.wit, "witness"}, {"witness", a.wit, "c"},
	} {
		ans := a.dupRemote(opCtx(t), tc.member, tc.base, dupOp{Op: "fence", Name: tc.name, By: "op", Fence: 99})
		if !strings.Contains(ans.Refused, "fences only") || ans.Fence != 0 {
			t.Errorf("%s fenced %q: %+v", tc.member, tc.name, ans)
		}
	}
	if !c.elector("b").IsLeader() && !a.IsLeader() {
		t.Fatal("no leader after the refused fences")
	}
}
