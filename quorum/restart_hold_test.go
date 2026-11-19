package quorum

import (
	"testing"
	"time"
)

// trio drives two controllers (voter, candidate, adoption) and the witness
// by hand: instant delivery and fsync, every node looking each step, and
// each voter taking the step's requests in the given order.
type trio struct {
	t   *testing.T
	ctl map[NodeID]*ctl
	w   *Voter
}

func (x *trio) step(now Instant, order []NodeID) {
	var reqs []*Request
	for _, id := range order {
		c := x.ctl[id]
		from, until := c.v.LeaderSeen()
		if r := c.c.Tick(now, from, until, true); r != nil {
			reqs = append(reqs, r)
		}
	}
	for _, vid := range []NodeID{"A", "B", "W"} {
		if vid == "W" && x.w == nil {
			continue // not up yet
		}
		for _, r := range reqs {
			v := x.w
			if vid != "W" {
				v = x.ctl[vid].v
				if r.From != vid {
					c := x.ctl[vid].c
					c.Observe(r.Term)
					YieldTo(now, v, c, *r)
				}
			}
			p, reps := v.Handle(now, *r)
			if p != nil {
				if vid == "W" {
					reps = append(reps, v.Persisted(p.Seq, true)...)
				} else {
					rs, _ := x.ctl[vid].a.Persisted(now, p.Seq, true)
					reps = append(reps, rs...)
				}
			}
			for _, rep := range reps {
				to := x.ctl[rep.To]
				to.c.OnReply(now, rep)
				if rep.From == "W" {
					to.a.OnWitness(now, rep)
				}
			}
		}
	}
}

func (x *trio) leader(now Instant) NodeID {
	n := NodeID("")
	for _, id := range []NodeID{"A", "B"} {
		if x.ctl[id].c.Leading(now) {
			if n != "" {
				x.t.Fatalf("both controllers lead at %v", time.Duration(now))
			}
			n = id
		}
	}
	return n
}

// The post-commission lapse (#1021 review, found by the loopback tests):
// the pair elects on {A, B} while the witness is not yet granting (here,
// not yet up). The standby restarts at once, and the witness's restart
// wait ends at the standby's first look. Before the restart hold the
// standby campaigned there (it had seen no leader in this boot), reached
// the witness ahead of the leader's renewal, and took its first grant; its
// own voter, in its restart wait, refused the leader's renewals too, so
// the leader's lease lapsed with nobody able to take over. Now it doesn't
// campaign while its own voter waits: the leader's renewal takes the
// witness, and the leader holds.
func TestRestartedStandbyHoldsItsCampaigns(t *testing.T) {
	x := &trio{t: t, ctl: map[NodeID]*ctl{
		"A": newCtl(t, "A", commissioned(1, incI, 0), 0),
		"B": newCtl(t, "B", commissioned(1, incI, 0), 0),
	}}
	dt := lease / 16
	now := Instant(0)
	var who NodeID
	for ; who == "" && now < at(3*lease); now = now.Add(dt) {
		x.step(now, []NodeID{"A", "B"})
		who = x.leader(now)
	}
	if who == "" {
		t.Fatal("no leader on {A, B}")
	}
	sb := NodeID("A")
	if who == "A" {
		sb = "B"
	}
	// Right after the election (now is the next step): the standby
	// restarts from its file, and the witness comes up with its restart
	// wait ending at this very step.
	x.ctl[sb] = newCtl(t, sb, x.ctl[sb].v.State(), now.Add(-dt))
	x.w, _ = NewWitness("W", lease, VoterState{Incarnation: incI}, now.Add(-lease), nil)
	order := []NodeID{sb, who} // the standby's requests reach every voter first
	for end := now.Add(6 * lease); now < end; now = now.Add(dt) {
		x.step(now, order)
		if l := x.leader(now); l != who {
			t.Fatalf("at %v the leader is %q, want %s holding its lease (the witness backs %q)",
				time.Duration(now), l, who, func() NodeID { _, to, _ := x.w.PromiseUntil(); return to }())
		}
	}
	if _, to, _ := x.w.PromiseUntil(); to != who {
		t.Fatalf("the witness backs %q, want the leader %s", to, who)
	}
}

// The hold covers exactly the voter's restart wait: the candidate's first
// campaign comes at its end, not before (and NextLook says so).
func TestCampaignHoldEndsWithTheRestartWait(t *testing.T) {
	x := newCtl(t, "A", commissioned(1, incI, 0), at(lease))
	if nl := x.c.NextLook(); nl != at(2*lease) {
		t.Fatalf("NextLook %v, want the restart wait's end %v", time.Duration(nl), 2*lease)
	}
	if r := x.c.Tick(at(3*lease/2), "", 0, true); r != nil {
		t.Fatalf("campaigned inside its voter's restart wait: %+v", r)
	}
	if r := x.c.Tick(at(2*lease), "", 0, true); r == nil {
		t.Fatal("no campaign once its voter's restart wait was over")
	}
}
