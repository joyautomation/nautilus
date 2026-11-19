package sim

import (
	"strings"
	"testing"
	"time"
)

const (
	ms    = time.Millisecond
	sec   = time.Second
	lease = 2 * time.Second
)

// judged runs every oracle over a named case's run, and fails t if the
// run is the two-of-three case (Record.TwoOfThreeLost), whose oracles are
// partly excused: a named case must be judged in full.
func judged(t *testing.T, rec *Record, c Checks) []Violation {
	t.Helper()
	if rec.TwoOfThreeLost() {
		t.Error("the run is the two-of-three case: its oracles are partly excused, so the case proves less than it says")
	}
	return Check(rec, c)
}

// clean fails t on any violation, with the trace for the first ones.
func clean(t *testing.T, rec *Record, c Checks) {
	t.Helper()
	vs := judged(t, rec, c)
	for _, v := range vs {
		t.Error(v)
	}
	if len(vs) > 0 && rec.Trace != nil {
		t.Log("trace:\n" + strings.Join(rec.Trace, "\n"))
	}
}

// hostile asserts the run really delivered a DECISIVE reply of the named
// kind: one that, counted, would have completed a majority for an open
// request. Otherwise a must-fail case could pass because the hostile grant
// never mattered (naut-lead's review of #981).
func hostile(t *testing.T, rec *Record, why string, to Addr) {
	t.Helper()
	for _, h := range rec.Hostile {
		if h.Why == why && h.To == to && h.Decisive {
			return
		}
	}
	t.Fatalf("the case is vacuous: no decisive %q grant reached %v (got %+v)", why, to, rec.Hostile)
}

func ledBy(rec *Record) map[Addr]bool {
	m := map[Addr]bool{}
	for _, iv := range rec.Gates() {
		m[iv.Node] = true
	}
	return m
}

// F31: the witness's grant to A, misdelivered to B as well, must not count
// for B. Both controllers campaign in lockstep with the same nonce and the
// same request ids (a nonce collision set up on purpose), so only the
// grantee check (Reply.To) tells the two grants apart. Pins Candidate's
// `r.To != c.cfg.Self` check: without it, B leads on {B, W→A} while A
// leads on {A, W}.
func TestMustFailGrantMeantForThePeer(t *testing.T) {
	o := Options{
		Seed: 1, Delay: 1 * ms, Fsync: 1 * ms, NoJitter: true, SameNonce: true, Until: 12 * sec, Trace: true,
		Faults: []Fault{{Kind: Mirror, At: 0, From: W, To: A, Node: B}},
	}
	rec := Run(o) // equal delays: W hears A's request first and grants A
	hostile(t, rec, "meant for the peer", B)
	clean(t, rec, Checks{MaxTakeovers: -1})
	if l := ledBy(rec); !l[A] || l[B] {
		t.Fatalf("leaders %v, want A only (W granted A first)", l)
	}
}

// F31: a grant for A's request from before a restart must not count for
// the same (term, id) asked again after it. A's first boot asks W for
// term 1 with id 1 once its own voter's restart wait is over (it can't
// campaign before: the restart hold); its voter's write of term 1 fails
// (a disk fault), and A crashes; W's grant is delayed 2.55 s. A's new boot is paused (SIGSTOP)
// until its restart wait and the restart's quiet period are over, so the
// old grant can't be ignored as quiet, then asks its own voter for term 1
// with id 1 again (its voter never persisted term 1), but not W (cut).
// The old grant lands in the new round. Pins the nonce check
// (`r.Nonce != c.cfg.Nonce`): without it A counts {A, W-from-before},
// times its lease from the new send, and leads past W's promise (until
// about 4 s): the lease oracle fails. B never starts.
func TestMustFailPreRestartGrant(t *testing.T) {
	o := Options{
		Seed: 1, Delay: 1 * ms, Fsync: 1 * ms, NoJitter: true, Until: 12 * sec, Trace: true,
		Start: [3]time.Duration{0, 60 * sec, 0}, // A's and W's restart waits end together, at 2 s
		Faults: []Fault{
			{Kind: DiskFail, At: 1900 * ms, For: 300 * ms, Node: A}, // term 1 never reaches A's file
			{Kind: Cut, At: 2050 * ms, From: A, To: W},
			{Kind: Crash, At: 2100 * ms, For: 50 * ms, Node: A},
			{Kind: Pause, At: 2150 * ms, For: 2350 * ms, Node: A},
		},
	}
	o.LinkDelay[W][A] = 2550 * ms
	rec := Run(o)
	hostile(t, rec, "earlier boot", A)
	clean(t, rec, Checks{MaxTakeovers: -1})
}

// strayVoter runs B leading on {B, W} while A, cut from B both ways (so
// it sees no renewal of B's and campaigns once its own voter's restart
// wait is over), also reaches the voter at stray through a mirror of its
// link to W. A's own voter grants A; W refuses (promised to B); the stray
// grant would be the deciding second one.
func strayVoter(stray Addr) *Record {
	return Run(Options{
		Seed: 1, Delay: 1 * ms, Fsync: 1 * ms, Until: 14 * sec, Trace: true,
		Start: [3]time.Duration{4000 * ms, 0, 0}, // B leads first
		Faults: []Fault{
			{Kind: Start, At: 0, Node: stray},
			{Kind: Mirror, At: 0, From: A, To: W, Node: stray},
			{Kind: Cut, At: 0, From: A, To: B, Both: true},
		},
	})
}

// F31: a grant from a voter the pair doesn't know (a name that is not A,
// B or W) must not count. Pins the sender check in Candidate.OnReply:
// without it, A leads on {A, Z} while B leads on {B, W}.
func TestMustFailUnknownSender(t *testing.T) {
	rec := strayVoter(Z)
	hostile(t, rec, "unknown sender", A)
	clean(t, rec, Checks{MaxTakeovers: -1})
	if l := ledBy(rec); !l[B] || l[A] {
		t.Fatalf("leaders %v, want B only", l)
	}
}

// F15b: a second witness copy under the same name, at a new incarnation
// (a rescheduled pod without its volume), answers A. Its grants must not
// count. Pins the incarnation check: without it, A leads on {A, W2} while
// B leads on {B, W}.
func TestMustFailSecondWitnessCopy(t *testing.T) {
	rec := strayVoter(W2)
	hostile(t, rec, "other witness incarnation", A)
	clean(t, rec, Checks{MaxTakeovers: -1})
	if l := ledBy(rec); !l[B] || l[A] {
		t.Fatalf("leaders %v, want B only", l)
	}
}

// isolateLeader runs a case where the first leader, A, is cut from B and W
// at 6 s, and returns the record. A's, B's and W's clocks run at rA, rB and
// rW. B starts at bStart, which sets the phase of its looks against A's
// renewals. freezeA > 0 also stops A's clock for that long at the cut.
func isolateLeader(rA, rB, rW PPM, bStart, freezeA time.Duration) *Record {
	const cut = 8 * sec
	o := Options{Seed: 7, Delay: 1 * ms, Fsync: 1 * ms, Until: 20 * sec,
		Start: [3]time.Duration{0, bStart, 0}}
	o.Rates[A], o.Rates[B], o.Rates[W] = rA, rB, rW
	o.Faults = []Fault{
		{Kind: Cut, At: cut, From: A, To: B, Both: true},
		{Kind: Cut, At: cut, From: A, To: W, Both: true},
	}
	if freezeA > 0 {
		o.Faults = append(o.Faults, Fault{Kind: Freeze, At: cut, For: freezeA, Node: A})
	}
	return Run(o)
}

func overlap(vs []Violation) bool {
	for _, v := range vs {
		if v.Oracle == "I1" {
			return true
		}
	}
	return false
}

// F22b, an expected breach: a leader whose clock stops (a VM pause)
// believes its lease still runs. The I1 oracle must see it, or it has no
// teeth; the design lists pausable VMs as unsupported for that reason.
func TestExpectedBreachFrozenLeaderClock(t *testing.T) {
	rec := isolateLeader(Perfect, Perfect, Perfect, 4000*ms, 3*lease)
	if vs := Check(rec, Checks{MaxTakeovers: -1}); !overlap(vs) {
		t.Fatalf("no I1 breach with A's clock frozen 3 leases while isolated: %v", vs)
	}
}

// F22: the isolated leader A runs slow by δ and the voters fast by δ, at
// 32 phases of B's looks against A's renewals. ρ bounds the RELATIVE rate
// (design §3.1): A's gate closes (1−ρ)·lease/(1−δ) after its last renewal
// and the witness's promise ends lease/(1+δ) after it, so the lease holds
// while (1+δ)/(1−δ) ≤ 1/(1−ρ), i.e. δ ≤ ρ/(2−ρ) ≈ 0.5025%. ±0.25% and ±0.5%
// must hold at every phase, on every oracle. ±3% must break I1 at some
// phase, or the oracle can't see a skew breach at all. The run logs the
// smallest skew tried that broke I1 (F22: "the guide states the limit").
func TestSkewWithinRhoHoldsBeyondBreaks(t *testing.T) {
	first := ""
	for _, d := range []struct {
		name  string
		ppm   PPM
		holds bool
	}{
		{"±0.25%", 2_500, true}, {"±0.5%", 5_000, true}, {"±1%", 10_000, false},
		{"±1.5%", 15_000, false}, {"±2%", 20_000, false}, {"±3%", 30_000, false},
	} {
		breach := false
		for k := range 32 {
			rec := isolateLeader(Perfect-d.ppm, Perfect+d.ppm, Perfect+d.ppm, 4000*ms+time.Duration(k)*lease/32, 0)
			vs := Check(rec, Checks{MaxTakeovers: -1})
			if d.holds && len(vs) > 0 {
				t.Errorf("%s, phase %d: %v", d.name, k, vs)
			}
			breach = breach || overlap(vs)
		}
		if breach && first == "" {
			first = d.name
		}
		if d.name == "±3%" && !breach {
			t.Fatal("±3% skew never broke I1 at any phase: the oracle can't see a skew breach")
		}
	}
	t.Logf("smallest skew tried that broke I1: %s each way", first)
}
