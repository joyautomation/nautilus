package sim

import (
	"testing"
	"time"
)

// Targeted cases for the rules test plan §3.3 lists as not caught by the
// matrix or the seeds (2c): each fails with its rule removed.

// Fsync before answer: without it a voter can grant a term it then
// forgets, and the term is led twice. A leads term 5 with W and B. B is
// paused (SIGSTOP) from 11 s, cut from A, so it learns nothing further.
// A's lease lapses on a 3 s cut from W; after its back-off it campaigns
// for term 6 while W's fsync takes 5 s, and W crashes inside it (19 s),
// restarting without term 6 on disk. A goes down at 20 s. B resumes at
// 22 s knowing term 5 and campaigns for 6, and W grants it. With the grant
// held until the fsync (the rule), A never leads term 6 and B leads alone.
// Answered early, A led term 6 too: I4 (one term, two leaders) and I2
// (the fencing token doesn't rise across the change of writer).
func TestTargetedFsyncBeforeAnswer(t *testing.T) {
	rec := Run(Options{Seed: 3, Delay: ms, Fsync: ms, Until: 45 * sec, Trace: true,
		Start: [3]time.Duration{0, 4 * sec, 0},
		Faults: []Fault{
			{Kind: Pause, At: 11 * sec, For: 11 * sec, Node: B},
			{Kind: Cut, At: 11 * sec, From: A, To: B, Both: true},
			{Kind: Cut, At: 12 * sec, For: 3 * sec, From: A, To: W, Both: true},
			{Kind: SlowDisk, At: 15 * sec, For: 10 * sec, Node: W, Value: int64(5 * time.Second)},
			{Kind: Crash, At: 19 * sec, For: 100 * ms, Node: W},
			{Kind: Crash, At: 20 * sec, Node: A},
		}})
	clean(t, rec, Checks{MaxTakeovers: -1})
	if !ledBy(rec)[B] {
		t.Fatal("B never led: the case proves nothing")
	}
}

// A grant counts only toward the request it answers (matched by id, not by
// term). B is down; A leads on {A, W}, and W's grants reach A 0.6 s late,
// after A's next renewal (every 0.5 s) has left. Once A→W is cut, the last
// grant W sent is the last promise A has. Matched by id, it extends the
// lease only from its own renewal's send, inside W's promise. Matched by
// term, it lands on the newer renewal and the lease outlives W's promise
// by about half a second: the lease oracle fails.
func TestTargetedGrantMatchedByRequest(t *testing.T) {
	o := Options{Seed: 5, Delay: ms, Fsync: ms, Until: 20 * sec, Trace: true,
		Start: [3]time.Duration{0, 0, 0},
		Faults: []Fault{
			{Kind: Crash, At: 3 * sec, Node: B},
			{Kind: Cut, At: 10 * sec, From: A, To: W},
		}}
	o.LinkDelay[W][A] = 600 * ms
	rec := Run(o)
	clean(t, rec, Checks{MaxTakeovers: -1})
	if !ledBy(rec)[A] {
		t.Fatal("A never led: the case proves nothing")
	}
}

// F9: a leader that can send but not hear, once its lease lapses, sends
// nothing for two leases (the lapse back-off), so the voters' promises to
// it run out and the standby wins. Since #979 the standby wins the race in
// about 3.5 s even with a shorter back-off (the yield and the moved retry
// get its campaign in first), so the case checks the rule where it shows:
// at all 8 phases of the renewal cycle, A sends no request in the two
// leases after its gate closed, and B leads within 4.6 s of the cut. With
// no lapse back-off, or a one-lease one, A sends again inside the window.
func TestTargetedOneWayLeaderBacksOff(t *testing.T) {
	for k := range 8 {
		cut := 10*sec + time.Duration(k)*lease/8
		o := Options{Seed: 9, Delay: ms, Fsync: ms, Until: 25 * sec,
			Start: [3]time.Duration{0, 4 * sec, 0},
			Faults: []Fault{
				{Kind: Cut, At: cut, From: B, To: A},
				{Kind: Cut, At: cut, From: W, To: A},
			}}
		rec := Run(o)
		clean(t, rec, Checks{MaxTakeovers: 1})
		var lapsed, took time.Duration
		for _, iv := range rec.Gates() {
			if iv.Node == A && iv.From < cut {
				lapsed = iv.To
			}
			if iv.Node == B && iv.From >= cut && took == 0 {
				took = iv.From - cut
			}
		}
		for _, r := range rec.Requests {
			if r.From == A && r.At >= lapsed && r.At < lapsed+2*lease {
				t.Errorf("phase %d: A sent term %d at %v, inside its back-off (lapsed at %v)", k, r.Term, r.At, lapsed)
				break
			}
		}
		if took == 0 || took > 4600*ms {
			t.Errorf("phase %d: B led %v after the cut, want within 4.6 s", k, took)
		}
	}
}

// Why the lapse back-off is two leases, not one (naut-lead asked for the
// concrete case before keeping #973's (c)): a one-way leader whose clock
// runs 0.5% slow, a standby 0.25% fast (0.75% apart, within ρ), 1 ms links
// with jitter. With two leases B leads 3.9 s after the cut at worst; with
// one, the deaf leader's campaign returns about when B's wait ends, wins
// B's voter, and B leads only after 6.4 s. Found by sweeping 160 skewed
// F9 schedules per delay (the worst with two leases at 1-100 ms was 3.9-4.3 s;
// with one, 6.4-6.9 s).
func TestTargetedOneWayLeaderSkewed(t *testing.T) {
	const cut = 11035 * ms
	o := Options{Seed: 5, Delay: ms, Jitter: ms / 2, Fsync: ms, Until: 40 * sec,
		Start:  [3]time.Duration{0, 4 * sec, 0},
		Faults: []Fault{{Kind: Cut, At: cut, From: B, To: A}, {Kind: Cut, At: cut, From: W, To: A}}}
	o.Rates[A], o.Rates[B], o.Rates[W] = 995_000, 1_002_500, Perfect
	rec := Run(o)
	clean(t, rec, Checks{MaxTakeovers: 1})
	for _, iv := range rec.Gates() {
		if iv.Node == B && iv.From >= cut {
			if took := iv.From - cut; took > 4600*ms {
				t.Fatalf("B led %v after the cut, want within 4.6 s", took)
			}
			return
		}
	}
	t.Fatal("B never led")
}

// A campaign asked again (#710, the lost switchover campaign) still lets
// the reachable controller win. A can send but never hear, from its
// start: its campaign reaches both voters and is asked again every look
// up to three quarters of a lease, each ask keeping their promise to A a
// lease from then. A backs off two leases from its LAST ask, not its
// first send, so B gets a lease of silence after those promises run out
// and leads; A sends nothing in that back-off. Every phase of B's start
// across a lease, a dozen seeds each. A's first campaign is at 2 s (its
// restart wait); its last ask by 3.25 s keeps the voters promised to
// 5.25 s, and B leads by 6.25 s.
func TestTargetedDeafCandidateReasksAndStillLoses(t *testing.T) {
	for seed := uint64(1); seed <= 12; seed++ {
		for k := range 4 {
			bStart := time.Duration(k) * lease / 4
			rec := Run(Options{Seed: seed, Delay: ms, Jitter: ms, Fsync: ms, Until: 12 * sec,
				Start: [3]time.Duration{0, bStart, 0},
				Faults: []Fault{
					{Kind: Cut, At: 0, From: B, To: A},
					{Kind: Cut, At: 0, From: W, To: A},
				}})
			clean(t, rec, Checks{MaxTakeovers: -1})
			var took time.Duration
			for _, iv := range rec.Gates() {
				if iv.Node == A {
					t.Fatalf("seed %d, B at %v: A led though it hears nobody: %+v", seed, bStart, iv)
				}
				if iv.Node == B && took == 0 {
					took = iv.From
				}
			}
			if took == 0 || took > 6250*ms {
				t.Errorf("seed %d, B at %v: B led at %v, want by 6.25 s", seed, bStart, took)
			}
			// A's asks of its first campaign, and its silence after.
			var first, last time.Duration = -1, -1
			for _, r := range rec.Requests {
				if r.From != A {
					continue
				}
				if first < 0 {
					first = r.At
				}
				if r.Term == rec.Requests[0].Term && last < r.At && r.At < first+lease {
					last = r.At
				}
			}
			for _, r := range rec.Requests {
				if r.From == A && r.At > last && r.At < last+2*lease {
					t.Errorf("seed %d, B at %v: A sent term %d at %v, inside its back-off from its last ask at %v", seed, bStart, r.Term, r.At, last)
					break
				}
			}
			if last < first+lease/2 {
				t.Errorf("seed %d, B at %v: A asked its campaign again only until %v (first %v): the case proves nothing", seed, bStart, last, first)
			}
		}
	}
}
