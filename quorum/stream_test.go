package quorum

import (
	"testing"
	"time"
)

// The standby's term check on its leader's state images (#710 S1b)
// accepts the leader's fencing term through a re-term: the vote term the
// standby's voter sees moves above the epoch the images carry, and an
// equality with it would stall the stream for the rest of the leadership.
// The contested cold start of TestEpochStableAcrossReterm, where the
// leader must re-term to win B's voter back.
func TestImageTermBoundHoldsThroughAReterm(t *testing.T) {
	p := newPair(t)
	p.start("A", 0)
	p.start("B", 0) // contested: B's voter promises itself A's term
	accepted, steps := 0, 0
	for now := Instant(0); now < at(8*time.Second); now += step {
		p.step(now)
		a := p.c["A"]
		if !a.Leading(now) || p.c["B"].Leading(now) {
			continue
		}
		// B has seen A's renewals for a lease: from then on, every image
		// A could send must be taken.
		if from, until := p.v["B"].LeaderSeen(); from != "A" || now >= until || now < at(3*time.Second) {
			continue
		}
		steps++
		if ok, why := ImageTermOK(p.v["B"], now, "A", a.Term(), 0, 0); ok {
			accepted++
		} else {
			t.Fatalf("at %v: B refuses A's image of term %d (A's vote term %d, B's voter promised %d): %s",
				time.Duration(now), a.Term(), a.VoteTerm(), p.v["B"].State().Promised, why)
		}
	}
	if a := p.c["A"]; a.VoteTerm() == a.Term() {
		t.Fatalf("no re-term happened (vote term %d = epoch): the case proves nothing", a.VoteTerm())
	}
	if steps == 0 {
		t.Fatal("A never led with B following: the case proves nothing")
	}
}

// The race a deposed leader's open stream could win (#710 S1b, naut-lead's
// question): A leads and is cut off from the votes, its stream to B still
// open. Until B's voter stops seeing A's lease (a lease after A's last
// renewal there), B takes A's images, all of A's own epoch, and A's gate
// was open less than a lease earlier: nothing older than that, and nothing
// of a leadership after A's, since B is the only other controller and
// leads only once its voter no longer sees A's lease. Once B leads, an
// image of A's old epoch is refused (B's own epoch is the floor), and when
// A comes back as B's standby, B's images are taken by A.
func TestImageTermBoundAcrossADeposedLeader(t *testing.T) {
	p := newPair(t)
	p.start("A", 0)
	p.start("B", at(2500*time.Millisecond)) // after A has won
	var aEpoch, bEpoch uint64
	var lastAOpen Instant // the last step A's gate was open
	tookFromA := Instant(-1)
	for now := Instant(0); now < at(30*time.Second); now += step {
		switch now {
		case at(4 * time.Second):
			if p.leader(now) != "A" {
				t.Fatalf("leader at 4 s: %q", p.leader(now))
			}
			aEpoch = p.c["A"].Term()
			p.down["A"] = true // its votes go nowhere; its stream is not modelled here, so ask the check directly
		case at(14 * time.Second):
			if p.leader(now) != "B" {
				t.Fatalf("B did not take over: %q", p.leader(now))
			}
			bEpoch = p.c["B"].Term()
			p.down["A"] = false
			p.start("A", now) // A restarts as B's standby
		}
		p.step(now)
		if c := p.c["A"]; c.Leading(now) && !p.down["A"] {
			lastAOpen = now
		}
		bLed := p.c["B"].Term()
		if aEpoch != 0 && now < at(14*time.Second) {
			// An image of A's epoch reaching B now: taken only while B's
			// voter sees A's lease, B has not led, and A led within a lease.
			own := uint64(0)
			if bLed != 0 {
				own = bLed
			}
			if ok, _ := ImageTermOK(p.v["B"], now, "A", aEpoch, own, 0); ok {
				tookFromA = now
				if bLed != 0 {
					t.Fatalf("at %v: B took A's epoch-%d image while leading in term %d", time.Duration(now), aEpoch, bLed)
				}
				if now-lastAOpen > Instant(lease) {
					t.Fatalf("at %v: B took an image of A's epoch %v after A's gate last closed", time.Duration(now), time.Duration(now-lastAOpen))
				}
			}
		}
	}
	if tookFromA < 0 {
		t.Fatal("B never took an image from A after the cut: the case proves nothing")
	}
	if bEpoch <= aEpoch {
		t.Fatalf("B's epoch %d is not above A's %d", bEpoch, aEpoch)
	}
	now := at(30*time.Second) - step
	if p.leader(now) != "B" {
		t.Fatalf("leader at the end: %q, want B", p.leader(now))
	}
	if ok, why := ImageTermOK(p.v["A"], now, "B", bEpoch, aEpoch, 0); !ok {
		t.Fatalf("A, back as B's standby, refuses B's images: %s", why)
	}
	// An image of A's old epoch reaching B now (a late frame): B's own
	// epoch is above it, and B's voter does not see A lead.
	if ok, _ := ImageTermOK(p.v["B"], now, "A", aEpoch, bEpoch, 0); ok {
		t.Fatal("B, leading, takes an image of A's old epoch")
	}
	// The same image with no floor to fail it: B's voter sees no lease of
	// A (A stands by), so it is still refused.
	if p.v["B"].Asked("A") < aEpoch {
		t.Fatalf("B's voter heard A ask at %d at most, below A's old epoch %d: the case proves nothing", p.v["B"].Asked("A"), aEpoch)
	}
	if ok, _ := ImageTermOK(p.v["B"], now, "A", aEpoch, 0, 0); ok {
		t.Fatal("B takes an image from A, which holds no lease B's voter sees")
	}
	// A pre-own-leadership term is refused even with a live lease seen: A
	// once led aEpoch, so an image of that term (or below) is never B's.
	if ok, _ := ImageTermOK(p.v["A"], now, "B", aEpoch, aEpoch, 0); ok {
		t.Fatal("A takes an image of a term it led itself")
	}
	// Below the newest term already taken: refused.
	if ok, _ := ImageTermOK(p.v["A"], now, "B", bEpoch, aEpoch, bEpoch+1); ok {
		t.Fatal("A takes an image below one it already holds")
	}
	// A term B never asked at: refused.
	if ok, _ := ImageTermOK(p.v["A"], now, "B", p.v["A"].Asked("B")+1, aEpoch, 0); ok {
		t.Fatal("A takes an image of a term B never asked its voter at")
	}
}

// A late renewal from a deposed leader re-arms the first and third
// checks (its lease seen, its term asked): only the floors refuse its
// image then (review F4). B led epoch 6 after A's epoch 5; A's epoch-5
// renewal reaches B's voter late. With B's own epoch as the floor, or
// the term B's retain file records (a restarted B remembers nothing
// else), the image is refused; with neither it would be taken, which is
// why the standby passes both.
func TestImageTermBoundAfterALateRenewal(t *testing.T) {
	v := NewVoter("B", lease, VoterState{Promised: 6, GrantedTo: "B", Incarnation: 42}, 0)
	now := at(10 * time.Second)
	v.Handle(now, Request{From: "A", Term: 5, ID: 9, Renewal: true, Seq: 9})
	if from, until := v.LeaderSeen(); from != "A" || now >= until || v.Asked("A") != 5 {
		t.Fatalf("the late renewal did not re-arm the checks (seen %q until %v, asked %d): the case proves nothing", from, time.Duration(until), v.Asked("A"))
	}
	if ok, _ := ImageTermOK(v, now, "A", 5, 6, 0); ok {
		t.Error("B, which led epoch 6, takes A's epoch-5 image")
	}
	if ok, _ := ImageTermOK(v, now, "A", 5, 0, 6); ok {
		t.Error("B, restarted with its file at epoch 6, takes A's epoch-5 image")
	}
	if ok, _ := ImageTermOK(v, now, "A", 5, 0, 0); !ok {
		t.Error("with no floor the image is refused anyway, so the two refusals above are not the floors' doing: the case proves nothing")
	}
}
