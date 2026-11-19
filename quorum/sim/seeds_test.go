package sim

import (
	"fmt"
	"math/rand/v2"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// profile picks the fault mix of a seeded schedule.
type profile int

const (
	// profDefault: every fault of test plan §5 the protocol must survive.
	profDefault profile = iota
	// profFreeze adds a frozen controller clock (F22b), an expected breach.
	profFreeze
)

// bound is how soon after the last heal a leader must exist (I6), at a
// 2 s lease. Design §4.5 gives lease + ρ·lease + lease/4 + a vote round
// for a takeover; on top of it, after a heal: a leader that lapsed just
// after it backs off two leases (§3.1), but the other controller can win
// meanwhile once the last renewal it saw is a lease old; and a late
// duplicate of a renewal sent before the heal (up to 2·lease late here)
// can hold a standby off one lease past its arrival. 3.5 leases covers the
// worst of these with a look and a vote to spare.
const bound = 3*lease + lease/2

// randomRun draws one seeded schedule (test plan §3.2): a random network,
// disk and clocks, then one to three faults that may overlap, each from
// 0.1 to 10 leases long.
func randomRun(seed uint64, p profile) Options {
	r := rand.New(rand.NewPCG(seed, 0x7363686564))
	dur := func(lo, hi time.Duration) time.Duration { return lo + time.Duration(r.Int64N(int64(hi-lo)+1)) }
	o := Options{Seed: seed,
		Delay:   dur(200*time.Microsecond, 5*ms),
		Fsync:   dur(100*time.Microsecond, 5*ms),
		InPlace: r.IntN(2) == 0,
	}
	o.Jitter = dur(0, o.Delay)
	for _, a := range []Addr{A, B, W} {
		o.Rates[a] = Perfect + PPM(r.Int64N(10_001)-5_000) // within ρ of each other (δ ≤ ρ/(2−ρ))
	}
	for i := range o.Start {
		o.Start[i] = dur(0, 2*lease)
	}
	pick := func(as ...Addr) Addr { return as[r.IntN(len(as))] }
	link := func(f *Fault) {
		l := voteLinks[r.IntN(len(voteLinks))]
		f.From, f.To = l.x, l.y
		switch r.IntN(3) {
		case 0:
			f.Both = true
		case 1:
			f.From, f.To = l.y, l.x
		}
	}
	// At most one controller may lose its state file per schedule (a wipe,
	// or a crash mid-write when the file is rewritten in place): two is
	// the known gap #976, a named expected failure until the handshake PR.
	lost := 0
	loses := func(f Fault) bool {
		return (f.Node == A || f.Node == B) && (f.Kind == Wipe || (f.Kind == Crash && o.InPlace))
	}
	n := 1 + r.IntN(3)
	for range n {
		f := Fault{At: dur(6*sec, 6*sec+8*lease), For: dur(lease/10, 10*lease)}
		switch r.IntN(14) {
		case 0, 1, 2:
			f.Kind = Cut
			link(&f)
		case 3:
			// Within the documented rule, lease ≥ 4 × the worst one-way
			// vote-link delay (test plan §3.3): base + jitter + fault ≤
			// lease/4. Longer links are the measured F21 case, not a
			// liveness guarantee (#998).
			f.Kind, f.Value = Delay, int64(dur(ms, max(ms, lease/4-o.Delay-o.Jitter)))
			link(&f)
		case 4:
			f.Kind, f.Value = Loss, int64(1+r.IntN(30))
			link(&f)
		case 5:
			f.Kind, f.Value = Duplicate, int64(dur(ms, 2*lease))
			link(&f)
		case 6:
			f.Kind, f.Node = Crash, pick(A, B, W)
		case 7:
			f.Kind, f.Node = Wipe, pick(A, B, W) // the witness (F15), or a controller: the I4 probe
		case 8:
			f.Kind, f.Node, f.For = Pause, pick(A, B, W), dur(lease/10, 3*lease)
		case 9:
			f.Kind, f.Node, f.Value = SlowDisk, pick(A, B, W), int64(dur(lease/20, lease+lease/2))
		case 10:
			f.Kind, f.Node = DiskFail, pick(A, B, W)
		case 11:
			f.Kind, f.Node, f.Value = Skew, pick(A, B, W), int64(Perfect+PPM(r.Int64N(10_001)-5_000))
		case 12: // a reply to one controller misdelivered to the other too
			x := pick(A, B)
			f.Kind, f.From, f.To, f.Node = Mirror, pick(W, peerOf(x)), x, peerOf(x)
		case 13: // a voter the pair doesn't know, or a second witness copy, answers
			stray := pick(Z, W2)
			o.Faults = append(o.Faults, Fault{Kind: Start, At: f.At, For: f.For, Node: stray})
			f.Kind, f.From, f.To, f.Node = Mirror, pick(A, B), W, stray
		}
		if loses(f) {
			if lost++; lost > 1 {
				f.Node = W // a second loss goes to the witness instead
			}
		}
		o.Faults = append(o.Faults, f)
	}
	if p == profFreeze {
		o.Faults = append(o.Faults, Fault{Kind: Freeze, At: dur(6*sec, 6*sec+8*lease), For: dur(lease/2, 3*lease), Node: pick(A, B)})
	}
	// Within the documented loss rule (test plan F20): loss on a link that
	// may be the sole vote path stays at or under maxSoleLoss. Capped after
	// the draws, so the other faults of every seed stay as they were.
	for i, f := range o.Faults {
		if f.Kind == Loss && f.Value > maxSoleLoss && solePath(o, i) {
			o.Faults[i].Value = maxSoleLoss
		}
	}
	h := healAt(o)
	o.Until = h + bound + 6*lease
	return o
}

// maxSoleLoss is the documented loss rule (test plan F20): I7 is claimed
// on a sole vote path only up to this loss, in percent, each way. Measured
// by TestMeasureSolePathLoss: over 20,000 windows of 10 leases per rate, a
// second takeover in one window (more than the loss fault's own one) never
// happened at 1–3% and first did at 4% (1, then 4 at 5%, 156 at 10%).
const maxSoleLoss = 3

// solePath reports whether link fault i of o may be the sole vote path: a
// controller can win a majority only over it while the third voter is out,
// because o also faults that voter (down, paused, its disk) or a link to
// it. Judged over the whole schedule, overlap or not: a wiped witness stays
// out until the handshake PR, past its fault's end.
func solePath(o Options, i int) bool {
	f := o.Faults[i]
	if f.From > W || f.To > W || f.From == f.To {
		return false // not a vote link between A, B and W
	}
	third := A + B + W - f.From - f.To
	for j, g := range o.Faults {
		if j == i {
			continue
		}
		switch g.Kind {
		case Crash, Wipe, Pause, Freeze, SlowDisk, DiskFail:
			if g.Node == third {
				return true
			}
		case Cut, Loss, Delay:
			if g.From == third || g.To == third {
				return true
			}
		}
	}
	return false
}

func healAt(o Options) time.Duration {
	var h time.Duration
	for _, f := range o.Faults {
		h = max(h, f.At+f.For)
	}
	return h
}

// checksFor derives the oracles' parameters from a schedule alone, so a
// shrunk schedule is judged by its own faults.
func checksFor(o Options) Checks {
	h := healAt(o)
	faults := 0
	for _, f := range o.Faults {
		if f.Kind == Start {
			continue // half of a stray-voter fault; its Mirror counts
		}
		faults++
	}
	// The witness backs the leader in steady state unless a node lost its
	// state file; Check drops it then, from the record (#998).
	voters := []Addr{A, B, W}
	c := Checks{HealAt: h, Bound: bound, MaxTakeovers: faults,
		SteadyFrom: h + bound + 2*lease, SteadyTo: o.Until, SteadyVoters: voters}
	for _, f := range o.Faults {
		if f.Kind == Freeze {
			c.Exclude = []string{"I1", "I2", "lease"} // F22b: an expected breach
		}
	}
	return c
}

// runSeed runs one seed and returns its violations and run hash.
func runSeed(seed uint64, p profile, trace bool) ([]Violation, *Record, Options) {
	return runSeedWith(seed, p, trace, checksFor)
}

func runSeedWith(seed uint64, p profile, trace bool, checks func(Options) Checks) ([]Violation, *Record, Options) {
	o := randomRun(seed, p)
	o.Trace = trace
	rec := Run(o)
	return Check(rec, checks(o)), rec, o
}

// shrink drops faults one at a time while the run still fails on the same
// oracle, and returns the smallest schedule found (test plan §3.2).
func shrink(o Options, oracle string, checks func(Options) Checks) Options {
	fails := func(o Options) bool {
		for _, v := range Check(Run(o), checks(o)) {
			if v.Oracle == oracle {
				return true
			}
		}
		return false
	}
	for again := true; again; {
		again = false
		for i := range o.Faults {
			try := o
			try.Faults = append(append([]Fault(nil), o.Faults[:i]...), o.Faults[i+1:]...)
			if len(try.Faults) > 0 && fails(try) {
				o, again = try, true
				break
			}
		}
	}
	return o
}

// GoString prints the options a regression case needs, as a Go literal.
func (o Options) GoString() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Options{Seed: %d, Delay: %d, Jitter: %d, Fsync: %d, InPlace: %v,\n", o.Seed, o.Delay, o.Jitter, o.Fsync, o.InPlace)
	fmt.Fprintf(&b, "\tRates: [nNodes]PPM{%d, %d, %d}, Start: [3]time.Duration{%d, %d, %d}, Until: %d,\n",
		o.Rates[A], o.Rates[B], o.Rates[W], o.Start[0], o.Start[1], o.Start[2], o.Until)
	b.WriteString("\tFaults: []Fault{\n")
	for _, f := range o.Faults {
		fmt.Fprintf(&b, "\t\t%#v,\n", f)
	}
	b.WriteString("\t}}")
	return b.String()
}

// The seed contract (posted on #710 for the nightly job):
//
//	go test ./quorum/sim                      seeds 1–2000
//	QUORUM_SEEDS=N go test ./quorum/sim       seeds 1–N (nightly: 200000)
//	QUORUM_SEEDS=a-b go test ./quorum/sim     seeds a–b (to shard)
//	QUORUM_SEED=k go test ./quorum/sim -run '^TestSeeds$' -v
//	                                          seed k alone, with its trace
const defaultSeeds = 2000

var rangeRE = regexp.MustCompile(`^(\d+)-(\d+)$`)

// seedRange reads the contract's environment: first..last, and whether a
// single seed was asked for.
func seedRange(seeds, seed string) (first, last uint64, single bool, err error) {
	if seed != "" {
		k, err := strconv.ParseUint(seed, 10, 64)
		if err != nil || k == 0 {
			return 0, 0, false, fmt.Errorf("QUORUM_SEED=%q: want a seed ≥ 1", seed)
		}
		return k, k, true, nil
	}
	switch m := rangeRE.FindStringSubmatch(seeds); {
	case seeds == "":
		return 1, defaultSeeds, false, nil
	case m != nil:
		a, _ := strconv.ParseUint(m[1], 10, 64)
		b, _ := strconv.ParseUint(m[2], 10, 64)
		if a == 0 || b < a {
			return 0, 0, false, fmt.Errorf("QUORUM_SEEDS=%q: want a-b with 1 ≤ a ≤ b", seeds)
		}
		return a, b, false, nil
	default:
		n, err := strconv.ParseUint(seeds, 10, 64)
		if err != nil || n == 0 {
			return 0, 0, false, fmt.Errorf("QUORUM_SEEDS=%q: want N or a-b", seeds)
		}
		return 1, n, false, nil
	}
}

func reproLine(test string, seed uint64) string {
	return fmt.Sprintf("QUORUM_SEED=%d go test ./quorum/sim -run '^%s$' -v", seed, test)
}

// reporter is the part of *testing.T runSeeds uses, so a test can run the
// real reporting path and read what it printed.
type reporter interface {
	Errorf(format string, args ...any)
	Logf(format string, args ...any)
	Fatal(args ...any)
}

// seedRun is one run of the seed runner: which test's name goes in the
// reproduce line, the profile, where the contract's environment is read,
// and the checks.
type seedRun struct {
	test   string
	p      profile
	env    func(string) string
	checks func(Options) Checks
}

// runSeeds runs the seed range the environment names, reporting each
// failing seed with its one-line reproduce command and shrunk schedule.
func runSeeds(r reporter, run seedRun) (failing []uint64) {
	first, last, single, err := seedRange(run.env("QUORUM_SEEDS"), run.env("QUORUM_SEED"))
	if err != nil {
		r.Fatal(err)
		return nil
	}
	for seed := first; seed <= last; seed++ {
		vs, rec, o := runSeedWith(seed, run.p, single, run.checks)
		if single {
			r.Logf("schedule: %s", o.GoString())
			r.Logf("trace:\n%s", strings.Join(rec.Trace, "\n"))
		}
		if len(vs) == 0 {
			continue
		}
		failing = append(failing, seed)
		if len(failing) > 5 {
			continue // the first five are reported in full
		}
		min := shrink(o, vs[0].Oracle, run.checks)
		r.Errorf("seed %d: %v\n  repro: %s\n  shrunk to %d of %d faults: %#v",
			seed, vs[0], reproLine(run.test, seed), len(min.Faults), len(o.Faults), min)
	}
	if len(failing) > 5 {
		r.Errorf("%d failing seeds in all: %v", len(failing), failing)
	}
	r.Logf("seeds %d–%d: %d failing", first, last, len(failing))
	return failing
}

// TestSeeds runs the seeded random schedules (test plan §3.2): 2,000 per
// PR, QUORUM_SEEDS for the nightly 200,000.
func TestSeeds(t *testing.T) {
	runSeeds(t, seedRun{test: "TestSeeds", p: profDefault, env: os.Getenv, checks: checksFor})
}

// One seed run twice gives the same run, event for event, with or without
// its trace kept.
func TestSeedDeterminism(t *testing.T) {
	for _, seed := range []uint64{1, 7, 1234} {
		_, r1, _ := runSeed(seed, profDefault, true)
		_, r2, _ := runSeed(seed, profDefault, true)
		_, r3, _ := runSeed(seed, profDefault, false)
		if r1.Hash != r2.Hash || r1.Hash != r3.Hash || strings.Join(r1.Trace, "\n") != strings.Join(r2.Trace, "\n") {
			t.Fatalf("seed %d is not deterministic: hashes %x %x %x", seed, r1.Hash, r2.Hash, r3.Hash)
		}
	}
}

// The seed contract's environment parses as posted on #710.
func TestSeedRange(t *testing.T) {
	for _, c := range []struct {
		seeds, seed  string
		first, last  uint64
		single, fail bool
	}{
		{"", "", 1, 2000, false, false},
		{"200000", "", 1, 200000, false, false},
		{"1001-2000", "", 1001, 2000, false, false},
		{"", "42", 42, 42, true, false},
		{"5", "42", 42, 42, true, false}, // one seed wins
		{"0", "", 0, 0, false, true},
		{"9-3", "", 0, 0, false, true},
		{"x", "", 0, 0, false, true},
		{"", "0", 0, 0, false, true},
	} {
		f, l, s, err := seedRange(c.seeds, c.seed)
		if (err != nil) != c.fail || f != c.first || l != c.last || s != c.single {
			t.Errorf("seedRange(%q, %q) = %d, %d, %v, %v", c.seeds, c.seed, f, l, s, err)
		}
	}
}

// strict judges a schedule with every oracle on, F22b's exclusions
// included.
func strict(o Options) Checks {
	c := checksFor(o)
	c.Exclude = nil
	return c
}

// F22b measured as an expected breach over seeds: a frozen controller
// clock must break I1 in some schedules (the oracle sees it), and every
// oracle the breach doesn't excuse (all but I1, I2 and the lease) still
// holds in every schedule.
func TestFrozenClockSeeds(t *testing.T) {
	breaches := 0
	for seed := uint64(1); seed <= 100; seed++ {
		o := randomRun(seed, profFreeze)
		rec := Run(o)
		if overlap(Check(rec, strict(o))) {
			breaches++
		}
		for _, v := range Check(rec, checksFor(o)) {
			t.Errorf("seed %d: %v (only I1, I2 and the lease oracle are excused for F22b)", seed, v)
		}
	}
	if breaches == 0 {
		t.Fatal("a frozen controller clock never broke I1 in 100 schedules: the oracle can't see F22b")
	}
	t.Logf("F22b: %d of 100 schedules with a frozen controller clock broke I1 (expected; pausable VMs unsupported)", breaches)
}

// recorder is a reporter that keeps what the runner printed.
type recorder struct{ errs, logs []string }

func (r *recorder) Errorf(f string, a ...any) { r.errs = append(r.errs, fmt.Sprintf(f, a...)) }
func (r *recorder) Logf(f string, a ...any)   { r.logs = append(r.logs, fmt.Sprintf(f, a...)) }
func (r *recorder) Fatal(a ...any)            { r.errs = append(r.errs, fmt.Sprint(a...)) }

func env(kv ...string) func(string) string {
	return func(k string) string {
		for i := 0; i+1 < len(kv); i += 2 {
			if kv[i] == k {
				return kv[i+1]
			}
		}
		return ""
	}
}

var reproRE = regexp.MustCompile(`repro: QUORUM_SEED=(\d+) go test \./quorum/sim -run '\^(\w+)\$' -v`)

// The real runner path, end to end: seeds that fail (a frozen clock judged
// strictly, so failures exist) are reported with a reproduce line; the
// environment that line names, run through the same runner, reports that
// seed alone with the same violation and the same shrunk schedule.
func TestReproLine(t *testing.T) {
	run := seedRun{test: "TestSeeds", p: profFreeze, env: env("QUORUM_SEEDS", "1-40"), checks: strict}
	var batch recorder
	if failing := runSeeds(&batch, run); len(failing) == 0 || len(batch.errs) == 0 {
		t.Fatal("no failing seed to reproduce")
	}
	m := reproRE.FindStringSubmatch(batch.errs[0])
	if m == nil || m[2] != run.test {
		t.Fatalf("the report carries no reproduce line for %s: %q", run.test, batch.errs[0])
	}
	run.env = env("QUORUM_SEED", m[1])
	var again recorder
	failing := runSeeds(&again, run)
	if fmt.Sprint(failing) != "["+m[1]+"]" || len(again.errs) != 1 || again.errs[0] != batch.errs[0] {
		t.Fatalf("QUORUM_SEED=%s reported %v:\n%q\nwant exactly\n%q", m[1], failing, again.errs, batch.errs[0])
	}
	if len(again.logs) < 2 || !strings.HasPrefix(again.logs[0], "schedule: Options{Seed: "+m[1]) {
		t.Fatal("a single seed did not log its schedule and trace")
	}
}
