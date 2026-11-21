package sim

import (
	"fmt"
	"math/rand/v2"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// #1029 3c-2: a copy of a member that both other voters hold another boot
// of its name the incumbent for a lease demotes itself to a spare: its
// flag durable first, then silent (quorum.Candidate.Spare).

// spareFam is one family of schedules the spare is measured on.
type spareFam struct {
	name string
	n    int
	run  func(seed uint64) Options
}

// spareTally classifies runs: which copy went spare, two leaders, and how
// the run ended.
type spareTally struct {
	runs, marked, cloneSpare, origSpare, both, none, two, other int
	noLeader, steadyOther, onTwo                                int
	gapMax, gapSum                                              time.Duration
	gapN                                                        int
	toSpareMax, toSpareSum                                      time.Duration
	firstOther                                                  string
}

// cloneOfRun is the clone slot a schedule boots, or -1.
func cloneOfRun(o Options) (Addr, time.Duration) {
	for _, f := range o.Faults {
		if f.Kind == Clone {
			return f.Node, f.At
		}
	}
	return -1, 0
}

func (t *spareTally) add(o Options, r *Record) {
	t.runs++
	c, at := cloneOfRun(o)
	if len(r.Marks) > 0 {
		t.marked++
	}
	cs, os_ := false, false
	for _, s := range r.Spares {
		if s.Restart {
			continue
		}
		if c >= 0 && s.Node == c {
			if !cs {
				d := s.At - at
				t.toSpareSum += d
				t.toSpareMax = max(t.toSpareMax, d)
			}
			cs = true
		} else {
			os_ = true
		}
	}
	switch {
	case cs && os_:
		t.both++
	case cs:
		t.cloneSpare++
	case os_:
		t.origSpare++
	default:
		t.none++
	}
	if from := clearFrom(o); from > 0 {
		g := maxGap(r, from, o.Until)
		t.gapMax = max(t.gapMax, g)
		t.gapSum += g
		t.gapN++
	}
	ck := checksFor(o)
	if c >= 0 {
		ck = cloneChecks(o)
	}
	vs := Check(r, ck)
	if overlap(vs) {
		t.two++
	}
	if len(vs) == 0 && len(r.Spares) > 0 {
		// Clean only because the steady oracle accepts the stated cost:
		// the original leads on two voters to the end. (add runs under
		// TestMeasureSpare's lock: no other Check runs meanwhile.)
		acceptSpareCost = false
		strict := Check(r, ck)
		acceptSpareCost = true
		if len(strict) > 0 {
			t.onTwo++
		}
	}
	for _, v := range vs {
		switch {
		case v.Oracle == "steady" && strings.Contains(v.What, "no leader"):
			t.noLeader++
		case v.Oracle == "steady":
			t.steadyOther++
		case v.Oracle != "I1" && v.Oracle != "I2":
			t.other++
			if t.firstOther == "" {
				t.firstOther = v.String()
			}
		}
		break
	}
}

func (t *spareTally) String() string {
	avg := time.Duration(0)
	if n := t.cloneSpare + t.both; n > 0 {
		avg = t.toSpareSum / time.Duration(n)
	}
	gap := time.Duration(0)
	if t.gapN > 0 {
		gap = t.gapSum / time.Duration(t.gapN)
	}
	return fmt.Sprintf("runs=%d marked=%d cloneSpare=%d origSpare=%d both=%d none=%d TWO=%d other=%d noLeader=%d steadyOther=%d endsOnTwoVoters=%d toSpare(avg/max)=%v/%v clearGap(avg/max)=%v/%v %s",
		t.runs, t.marked, t.cloneSpare, t.origSpare, t.both, t.none, t.two, t.other, t.noLeader, t.steadyOther, t.onTwo,
		avg.Round(time.Millisecond), t.toSpareMax.Round(time.Millisecond), gap.Round(time.Millisecond), t.gapMax.Round(time.Millisecond), t.firstOther)
}

// lateOldBootFrames: a controller's frames to one voter delayed d (past
// the lease/4 rule) until it restarts, so the old boot's late frames land
// after the new boot's first: the false-mark residual of 3c-1, where a
// voter marks a name no copy runs. No spare may follow.
func lateOldBootFrames(seed uint64, d time.Duration) Options {
	o := randomRun(seed, profDefault)
	r := rand.New(rand.NewPCG(seed, 0x4c415445))
	c := []Addr{A, B}[r.IntN(2)]
	to := []Addr{W, peerOf(c)}[r.IntN(2)]
	at := 8*sec + time.Duration(r.Int64N(int64(4*lease)))
	gap := 20*ms + time.Duration(r.Int64N(int64(lease/4)))
	o.Faults = []Fault{
		{Kind: Delay, At: at - 3*lease, For: 3 * lease, From: c, To: to, Value: int64(d)},
		{Kind: Crash, At: at, For: gap, Node: c},
	}
	o.Until = at + 12*lease
	return o
}

// lateDelays are the delays lateOldBootFrames runs, to 2.5 leases.
var lateDelays = []time.Duration{lease / 4, lease / 2, 3 * lease / 4, lease, 5 * lease / 4, 3 * lease / 2, 2 * lease, 5 * lease / 2}

// incumbentDown: a clone of the leader or the standby comes up beside a
// settled pair (cloneRace "peer" without its restart), and three to six
// leases later, past the decision, its original restarts (or, dies: stays
// down to the end).
func incumbentDown(seed uint64, dies bool) Options {
	o := cloneRace(seed, "peer")
	o.Faults = o.Faults[:1]
	c := o.Faults[0]
	r := rand.New(rand.NewPCG(seed, 0x5350))
	at := c.At + 3*lease + time.Duration(r.Int64N(int64(3*lease)))
	d := 50*ms + time.Duration(r.Int64N(int64(lease/4)))
	if dies {
		d = o.Until
	}
	o.Faults = append(o.Faults, Fault{Kind: Crash, At: at, For: d, Node: original(c.Node)})
	return o
}

// decisionUnderASplit: a voter restarts between the mark and the
// demotion, and the original (or the clone) is cut from the other voter.
func decisionUnderASplit(seed uint64) Options {
	o := cloneRace(seed, "peer")
	c := o.Faults[0]
	r := rand.New(rand.NewPCG(seed, 0x44555350))
	orig := original(c.Node)
	v := []Addr{W, peerOf(orig)}[r.IntN(2)]
	other := W
	if v == W {
		other = peerOf(orig)
	}
	at := c.At + 800*ms + time.Duration(r.Int64N(int64(2*sec)))
	o.Faults = []Fault{c,
		{Kind: Crash, At: at, For: 50*ms + time.Duration(r.Int64N(int64(lease/2))), Node: v},
		{Kind: Cut, At: at + time.Duration(r.Int64N(int64(lease/2))), For: 6 * lease, From: orig, To: other, Both: true}}
	if r.IntN(2) == 0 {
		o.Faults[2].From = c.Node
	}
	o.Until = max(o.Until, at+6*lease+bound+6*lease)
	return o
}

// withRestartClear adds the clear an operator can do before the 3c-3
// command (the guide's three steps), a lease after the split healed and
// the copy stopped: restart the witness, then the original's peer, then
// the marked original, two leases apart. The zero-length cut of W2 marks
// where the clear starts (clearFrom), and moves the run's settle point
// past it.
func withRestartClear(o Options) Options {
	c := o.Faults[0].Node
	end := o.Faults[0].At + o.Faults[0].For
	for _, f := range o.Faults {
		if f.Kind == Cut {
			end = max(end, f.At+f.For)
		}
	}
	at := end + lease
	o.Faults = append(o.Faults,
		Fault{Kind: Cut, At: at, For: ms, From: W2, To: Z},
		Fault{Kind: Crash, At: at, For: 50 * ms, Node: W},
		Fault{Kind: Crash, At: at + 2*lease, For: 50 * ms, Node: peerOf(original(c))},
		Fault{Kind: Crash, At: at + 4*lease, For: 50 * ms, Node: original(c)})
	o.Until = at + 4*lease + bound + 6*lease
	return o
}

// withCommandClear is withRestartClear's schedule with the operator's
// clear-duplicate command (a Clear at every member, guarded, behind the
// command's check) in place of the restarts, which no longer clear a mark
// (#1029 3c-3b).
func withCommandClear(o Options) Options {
	end := o.Faults[0].At + o.Faults[0].For
	for _, f := range o.Faults {
		if f.Kind == Cut {
			end = max(end, f.At+f.For)
		}
	}
	at := end + lease
	o.Faults = append(o.Faults, Fault{Kind: Cut, At: at, For: ms, From: W2, To: Z})
	o = withClear(o, at+lease+lease/2, clearMembers, ClearGuarded|ClearPreflight)
	o.Until = at + 4*lease + bound + 6*lease
	return o
}

// clearFrom is where withRestartClear's clear starts, 0 when none.
func clearFrom(o Options) time.Duration {
	for _, f := range o.Faults {
		if f.Kind == Cut && f.From == W2 && f.To == Z {
			return f.At
		}
	}
	return 0
}

// maxGap is the longest time in [from, until) that no controller held an
// open gate.
func maxGap(r *Record, from, until time.Duration) time.Duration {
	gates := r.Gates()
	cur, worst := from, time.Duration(0)
	for _, g := range gates {
		if g.To <= cur || g.From >= until {
			continue
		}
		if g.From > cur {
			worst = max(worst, g.From-cur)
		}
		cur = max(cur, g.To)
	}
	if cur < until {
		worst = max(worst, until-cur)
	}
	return worst
}

// earlyCut: a split shape whose split comes bin to bin+0.5 s after the
// clone starts (3c-1's early-cut residual, which the spare must not
// widen).
func earlyCut(shapes []restartShape, bin time.Duration) func(uint64) Options {
	return func(s uint64) Options {
		sh := shapes[int(s)%len(shapes)]
		seed := s/uint64(len(shapes)) + 1
		o := sh.run(seed)
		r := rand.New(rand.NewPCG(seed, 0x6561726c))
		var cut time.Duration
		for _, f := range o.Faults {
			if f.Kind == Cut {
				cut = max(cut, f.At)
			}
		}
		at := o.Faults[0].At + bin + time.Duration(r.Int64N(int64(sec/2)))
		for i := range o.Faults {
			if o.Faults[i].Kind == Cut && o.Faults[i].At == cut {
				o.Faults[i].At = at
			}
		}
		return o
	}
}

// originalRestartsEarly: a clone beside a settled pair, and its original
// restarts d (plus up to a quarter second) after it, before the clone has
// gone spare (#1133 review).
func originalRestartsEarly(seed uint64, d time.Duration) Options {
	o := cloneRace(seed, "original")
	r := rand.New(rand.NewPCG(seed, 77))
	o.Faults[1].At = o.Faults[0].At + d + time.Duration(r.Int64N(int64(250*ms)))
	o.Faults[1].For = 50*ms + time.Duration(r.Int64N(int64(lease/2)))
	return o
}

// lossyOriginal: a clone beside a settled pair while the original's
// links to both voters lose pct% both ways (#1133 review).
func lossyOriginal(seed uint64, pct int64) Options {
	o := cloneRace(seed, "original")
	o.Faults = o.Faults[:1]
	c := o.Faults[0]
	orig := original(c.Node)
	o.Faults[0].For = 20 * lease
	o.Faults = append(o.Faults,
		Fault{Kind: Loss, At: c.At - lease, For: 21 * lease, From: orig, To: W, Both: true, Value: pct},
		Fault{Kind: Loss, At: c.At - lease, For: 21 * lease, From: orig, To: peerOf(orig), Both: true, Value: pct})
	o.Until = c.At + 22*lease
	return o
}

// lateOldBootFramesBoth is lateOldBootFrames with the delay on the
// controller's links to BOTH voters (its own uplink, #1133 review).
func lateOldBootFramesBoth(seed uint64, d time.Duration) Options {
	o := lateOldBootFrames(seed, d)
	f := o.Faults[0]
	other := W
	if f.To == W {
		other = peerOf(f.From)
	}
	f.To = other
	o.Faults = append(o.Faults, f)
	return o
}

var earlyRestarts = []time.Duration{250 * ms, 500 * ms, sec, 3 * sec / 2, 2 * sec, 5 * sec / 2, 3 * sec, 7 * sec / 2}

func spareFams() []spareFam {
	n := func(k string, d int) int {
		if v, err := strconv.Atoi(os.Getenv(k)); err == nil && v > 0 {
			return v
		}
		return d
	}
	nr := n("QUORUM_MEASURE_SPARE_N", 300)
	var fs []spareFam
	fs = append(fs, spareFam{"default seeds (no clone)", 2000, func(s uint64) Options { return randomRun(s, profDefault) }})
	fs = append(fs, spareFam{"clone seeds", 2000, func(s uint64) Options { return randomRun(s, profClone) }})
	for _, m := range []string{"start", "original", "peer", "unhealthy", "rich"} {
		fs = append(fs, spareFam{"race " + m, nr, func(s uint64) Options { return cloneRace(s, m) }})
	}
	fs = append(fs, spareFam{"incumbent restarts while the spare runs", nr, func(s uint64) Options { return incumbentDown(s, false) }})
	fs = append(fs, spareFam{"incumbent dies while the spare runs", nr, func(s uint64) Options { return incumbentDown(s, true) }})
	for _, d := range lateDelays {
		fs = append(fs, spareFam{fmt.Sprintf("late old-boot frames, delay %v", d), nr, func(s uint64) Options { return lateOldBootFrames(s, d) }})
	}
	shapes := restartShapes()
	for _, bin := range []time.Duration{0, sec / 2, sec, 3 * sec / 2, 2 * sec, 3 * sec, 4 * sec} {
		fs = append(fs, spareFam{fmt.Sprintf("early cut %v after the clone (all shapes)", bin), 100 * len(shapes), earlyCut(shapes, bin)})
	}
	for _, sh := range shapes {
		fs = append(fs, spareFam{"split " + sh.name, 200, sh.run})
		fs = append(fs, spareFam{"split + restart clear " + sh.name, 200, func(s uint64) Options { return withRestartClear(sh.run(s)) }})
	}
	fs = append(fs, spareFam{"decision under a split", 4 * nr, decisionUnderASplit})
	for _, d := range earlyRestarts {
		fs = append(fs, spareFam{fmt.Sprintf("original restarts %v after the clone", d), nr, func(s uint64) Options { return originalRestartsEarly(s, d) }})
	}
	for _, pct := range []int64{30, 50, 60, 70, 80} {
		fs = append(fs, spareFam{fmt.Sprintf("lossy original, %d%% both ways to both voters", pct), nr, func(s uint64) Options { return lossyOriginal(s, pct) }})
	}
	for _, d := range lateDelays {
		fs = append(fs, spareFam{fmt.Sprintf("late old-boot frames to BOTH voters, delay %v", d), nr, func(s uint64) Options { return lateOldBootFramesBoth(s, d) }})
	}
	return fs
}

// TestMeasureSpare prints the spare's table (the PR body's): every family,
// which copy went spare, two leaders, how the run ended. Off unless
// QUORUM_MEASURE_SPARE=1; QUORUM_MEASURE_SPARE_ONLY=<substring> picks
// families.
func TestMeasureSpare(t *testing.T) {
	if os.Getenv("QUORUM_MEASURE_SPARE") == "" {
		t.Skip("QUORUM_MEASURE_SPARE=1 runs it")
	}
	only := os.Getenv("QUORUM_MEASURE_SPARE_ONLY")
	for _, f := range spareFams() {
		if only != "" && !strings.Contains(f.name, only) {
			continue
		}
		var mu sync.Mutex
		var tl spareTally
		var wg sync.WaitGroup
		seeds := make(chan uint64)
		for range runtime.GOMAXPROCS(0) {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for s := range seeds {
					o := f.run(s)
					r := Run(o)
					mu.Lock()
					tl.add(o, r)
					mu.Unlock()
				}
			}()
		}
		for s := 1; s <= f.n; s++ {
			seeds <- uint64(s)
		}
		close(seeds)
		wg.Wait()
		fmt.Printf("%-75s %v\n", f.name, &tl)
	}
}

// spareSeeds is how many seeds a spare gate runs per family: 100 (20
// under -race), or QUORUM_SPARE_SEEDS (the nightly runs more).
func spareSeeds() uint64 {
	if v, err := strconv.Atoi(os.Getenv("QUORUM_SPARE_SEEDS")); err == nil && v > 0 {
		return uint64(v)
	}
	if raceOn {
		// Under -race (CI's race job, 10-minute package timeout): a fifth.
		// The plain test job runs 100, the nightly 2,000.
		return 20
	}
	return 100
}

// spareBound is how long after the clone starts it may take to go spare
// with no fault in the way: the mark (a quarter lease and a look past the
// clone's first frame), a lease of both voters' word, a look, and the
// flag's fsync. Measured: 3.4 s on average, 4.0 s at most.
const spareBound = 5 * lease / 2

// A clone that comes up beside a settled pair, with the original or its
// peer restarting, or the leader's scans hanging, goes spare in every run
// within spareBound of its start, and the original never does: the
// voters hold the original, the boot they heard first and still hear,
// the incumbent. Every oracle but I7 holds (cloneChecks). held_by_last_
// heard demotes the original instead.
func TestACloneBesideASettledPairBecomesASpare(t *testing.T) {
	n := spareSeeds()
	for _, mode := range []string{"original", "peer", "unhealthy"} {
		for seed := uint64(1); seed <= n; seed++ {
			o := cloneRace(seed, mode)
			c, at := cloneOfRun(o)
			r := Run(o)
			if vs := Check(r, cloneChecks(o)); len(vs) > 0 {
				t.Errorf("%s seed %d: %v", mode, seed, vs[0])
				continue
			}
			var spare []Spare
			for _, s := range r.Spares {
				if !s.Restart {
					spare = append(spare, s)
				}
			}
			switch {
			case len(spare) != 1 || spare[0].Node != c:
				t.Errorf("%s seed %d: spares %v, want the clone %v alone", mode, seed, spare, c)
			case spare[0].At-at > spareBound:
				t.Errorf("%s seed %d: the clone went spare %v after it started (bound %v)", mode, seed, spare[0].At-at, spareBound)
			}
		}
	}
}

// A real restart never makes a spare, even where 3c-1's residual marks
// it: old-boot frames late past the delay rule (a quarter lease to 2.5
// leases) mark the name at one voter, and that voter alone then names the
// old boot the incumbent. A copy demotes only on BOTH other voters' word.
// With one voter's word (spare_one_voter): false spares in up to 300 of
// 300 runs at 1.25 leases and more. Not vacuous: the runs must mark.
func TestLateFramesOfARestartMakeNoSpare(t *testing.T) {
	n := spareSeeds()
	marked := 0
	for _, d := range lateDelays {
		for seed := uint64(1); seed <= n; seed++ {
			o := lateOldBootFrames(seed, d)
			r := Run(o)
			if len(r.Marks) > 0 {
				marked++
			}
			if len(r.Spares) > 0 {
				t.Errorf("delay %v seed %d: %v went spare with no clone running", d, seed, r.Spares[0].Node)
			}
			if vs := Check(r, checksFor(o)); overlap(vs) {
				t.Errorf("delay %v seed %d: two leaders: %v", d, seed, vs[0])
			}
		}
	}
	if marked < int(n) {
		t.Errorf("only %d runs marked: the test no longer reaches the false-mark residual", marked)
	}
	t.Logf("%d of %d runs marked a name; none went spare", marked, uint64(len(lateDelays))*n)
}

// Once the clone is a spare, its original restarting (or dying for good)
// changes nothing: the spare stays silent, the original is never demoted
// (its new boot is unknown to the voters' marked evidence, and the dead
// boot is stale within half a lease, so no voter names a boot over it),
// and the pair keeps a leader (on the other controller and the witness
// when the original died: redundancy lost until an operator acts, never
// the leader). The restarted original's new boot is one the voters first
// heard after the mark, so they tell it nothing at all.
func TestTheIncumbentRestartsOrDiesWhileTheSpareRuns(t *testing.T) {
	n := spareSeeds()
	for _, dies := range []bool{false, true} {
		for seed := uint64(1); seed <= n; seed++ {
			o := incumbentDown(seed, dies)
			c, _ := cloneOfRun(o)
			r := Run(o)
			if vs := Check(r, cloneChecks(o)); len(vs) > 0 {
				t.Errorf("dies %v seed %d: %v", dies, seed, vs[0])
			}
			for _, s := range r.Spares {
				if s.Node != c {
					t.Errorf("dies %v seed %d: the original %v went spare at %v", dies, seed, s.Node, s.At)
				}
			}
			if !containsSpare(r, c) {
				t.Errorf("dies %v seed %d: the clone never went spare", dies, seed)
			}
		}
	}
}

func containsSpare(r *Record, a Addr) bool {
	for _, s := range r.Spares {
		if s.Node == a {
			return true
		}
	}
	return false
}

// A voter restarting between the mark and the demotion (forgetting the
// mark), then a cut between the original or the clone and the other
// voter: the decision may not come (the forgetting voter names nobody, the
// cut voter's word stays), but it never demotes the original, never both,
// and never gives two leaders.
func TestTheSpareDecisionUnderASplit(t *testing.T) {
	n := 4 * spareSeeds()
	spares := 0
	for seed := uint64(1); seed <= n; seed++ {
		o := decisionUnderASplit(seed)
		c, _ := cloneOfRun(o)
		r := Run(o)
		if overlap(Check(r, cloneChecks(o))) {
			t.Errorf("seed %d: two leaders", seed)
		}
		for _, s := range r.Spares {
			if s.Node != c {
				t.Errorf("seed %d: the original %v went spare", seed, s.Node)
			}
		}
		if containsSpare(r, c) {
			spares++
		}
	}
	if spares == 0 {
		t.Errorf("no run demoted the clone: the test no longer reaches a decision")
	}
	t.Logf("the clone went spare in %d of %d", spares, n)
}

// After a spare, every split shape heals to one leader once an operator
// runs the clear-duplicate command with the copy stopped: full steady
// state, every voter backing the leader, and no mark left. Without the
// clear the original often leads on two voters to the end (the stated
// cost, oracle spareCost), and TestCloneSplitNoTwoLeaders judges that run.
// Restarts no longer clear a mark (#1029 3c-3b).
func TestASplitWithASpareClearsByTheCommand(t *testing.T) {
	n := spareSeeds() / 2
	for _, sh := range restartShapes() {
		failing := 0
		for seed := uint64(1); seed <= n; seed++ {
			o := withCommandClear(sh.run(seed))
			r := Run(o)
			if len(r.MarkedAtEnd) > 0 {
				failing++
				if failing <= 2 {
					t.Errorf("%s seed %d: still marked at the end: %v", sh.name, seed, r.MarkedAtEnd)
				}
				continue
			}
			if vs := Check(r, cloneChecks(o)); len(vs) > 0 {
				failing++
				if failing <= 2 {
					t.Errorf("%s seed %d: %v", sh.name, seed, vs[0])
				}
			}
		}
		if failing > 0 {
			t.Errorf("%s: %d of %d failing", sh.name, failing, n)
		}
	}
}

// The flag is durable before the copy goes quiet: a crash while its
// write is in flight leaves no spare (its restart is a boot the voters
// first heard after the mark, so they name it nothing: it stays the held
// copy, the 3c-1 state), a crash once it is written leaves a copy that
// restarts silent, reading the flag before anything else.
func TestTheSpareFlagIsWrittenBeforeTheCopyGoesQuiet(t *testing.T) {
	checked := 0
	for seed := uint64(1); seed <= 50 && checked < 10; seed++ {
		o := cloneRace(seed, "original")
		o.Faults = o.Faults[:1] // the clone alone
		o.InPlace = false       // the driver writes whole (a torn file is uncommissioned: no member at all)
		c, _ := cloneOfRun(o)
		r := Run(o)
		if len(r.spareDecided) == 0 || len(r.Spares) == 0 {
			continue
		}
		decided, durable := r.spareDecided[0].At, r.Spares[0].At
		if durable <= decided {
			continue // a zero fsync: no window
		}
		checked++
		// Crash inside the write: not a spare when it comes back.
		in := o
		in.Faults = append(append([]Fault(nil), o.Faults...), Fault{Kind: Crash, At: decided + (durable-decided)/2, For: 100 * ms, Node: c})
		ri := Run(in)
		if containsSpare(ri, c) {
			t.Errorf("seed %d: crashed inside the flag's write, yet a spare: %v", seed, ri.Spares)
		}
		if vs := Check(ri, cloneChecks(in)); len(vs) > 0 {
			t.Errorf("seed %d: %v", seed, vs[0])
		}
		// Crash once written: it restarts silent.
		after := o
		after.Faults = append(append([]Fault(nil), o.Faults...), Fault{Kind: Crash, At: durable + lease, For: 100 * ms, Node: c})
		ra := Run(after)
		restarted := false
		for _, s := range ra.Spares {
			if s.Node == c && s.Restart {
				restarted = true
			}
		}
		if !restarted {
			t.Errorf("seed %d: the spare's restart did not read its flag", seed)
		}
		if vs := Check(ra, cloneChecks(after)); len(vs) > 0 {
			t.Errorf("seed %d: %v", seed, vs[0])
		}
	}
	if checked < 10 {
		t.Fatalf("only %d seeds had a write window", checked)
	}
}

// A failed write of the flag leaves the held copy it was, not silent; it
// decides again on a later reply and goes spare once a write lands. The
// clone's disk fails from just before its decision for a lease.
func TestAFailedSpareFlagWriteIsRetried(t *testing.T) {
	checked := 0
	for seed := uint64(1); seed <= 50 && checked < 10; seed++ {
		o := cloneRace(seed, "original")
		o.Faults = o.Faults[:1]
		o.InPlace = false
		c, _ := cloneOfRun(o)
		r := Run(o)
		if len(r.spareDecided) == 0 {
			continue
		}
		checked++
		decided := r.spareDecided[0].At
		failFrom, failFor := decided-lease/8, lease
		f := o
		f.Faults = append(append([]Fault(nil), o.Faults...), Fault{Kind: DiskFail, At: failFrom, For: failFor, Node: c})
		rf := Run(f)
		if len(rf.spareDecided) < 2 {
			t.Errorf("seed %d: decided %d times with its disk failing, want a retry", seed, len(rf.spareDecided))
		}
		for _, s := range rf.Spares {
			if s.Node == c && s.At < failFrom+failFor {
				t.Errorf("seed %d: silent at %v though no flag write landed before %v", seed, s.At, failFrom+failFor)
			}
		}
		if !containsSpare(rf, c) {
			t.Errorf("seed %d: never a spare once the disk was back", seed)
		}
		if vs := Check(rf, cloneChecks(f)); len(vs) > 0 {
			t.Errorf("seed %d: %v", seed, vs[0])
		}
	}
	if checked < 10 {
		t.Fatalf("only %d seeds decided", checked)
	}
}

// A clone beside a lossy original (30 to 80% loss both ways on its links
// to both voters): the original is never demoted, and never both. A
// marked standby gets only refusals, and a refusal names the incumbent as
// of the frame it answers, so the original's own frame makes it fresh
// (#1133 review: naming it before the frame gave 2 to 121 original spares
// in 300 and both copies spare in 3, held_by_before_frame).
func TestALossyOriginalNeverGoesSpare(t *testing.T) {
	n := spareSeeds()
	spares := 0
	for _, pct := range []int64{30, 50, 60, 70, 80} {
		for seed := uint64(1); seed <= n; seed++ {
			o := lossyOriginal(seed, pct)
			c, _ := cloneOfRun(o)
			r := Run(o)
			if overlap(Check(r, cloneChecks(o))) {
				t.Errorf("loss %d%% seed %d: two leaders", pct, seed)
			}
			for _, s := range r.Spares {
				if s.Node != c {
					t.Errorf("loss %d%% seed %d: the original %v went spare at %v", pct, seed, s.Node, s.At)
				}
			}
			if containsSpare(r, c) {
				spares++
			}
		}
	}
	if spares == 0 {
		t.Error("no clone went spare: the test no longer reaches a decision")
	}
}

// The original restarting 1.5 to 3.5 s after the clone starts (after the
// voters marked the name, before the clone went spare) is never demoted:
// its new boot is one the voters first heard after the mark, and they
// name no incumbent to it. It stays held, or the clone goes spare. Naming
// one (held_by_names_to_unknown) demoted it in 300, 300, 288, 196 and 3
// of 300 at 1.5, 2, 2.5 and 3 s. A restart before the mark (within about
// a second of the clone's start) can still go to the copy: stated.
func TestAnOriginalRestartingAfterTheMarkStaysAMember(t *testing.T) {
	n := spareSeeds()
	for _, d := range earlyRestarts {
		if d < 3*sec/2 {
			continue
		}
		for seed := uint64(1); seed <= n; seed++ {
			o := originalRestartsEarly(seed, d)
			c, _ := cloneOfRun(o)
			r := Run(o)
			if vs := Check(r, cloneChecks(o)); len(vs) > 0 {
				t.Errorf("restart %v seed %d: %v", d, seed, vs[0])
			}
			for _, s := range r.Spares {
				if s.Node != c {
					t.Errorf("restart %v seed %d: the original %v went spare at %v", d, seed, s.Node, s.At)
				}
			}
		}
	}
}

// Late frames of a restart's old boot on the controller's links to BOTH
// voters, up to a lease late (four times the delay rule): no spare. Later
// than about 1.25 leases both voters can name the old boot, and the
// restarted controller goes spare (24, 150 and 299 of 300 at 2.5, 3 and
// 4 s): stated, outside the delay rule.
func TestLateFramesToBothVotersUpToALeaseMakeNoSpare(t *testing.T) {
	n := spareSeeds()
	marked := 0
	for _, d := range lateDelays {
		if d > lease {
			continue
		}
		for seed := uint64(1); seed <= n; seed++ {
			o := lateOldBootFramesBoth(seed, d)
			r := Run(o)
			if len(r.Marks) > 0 {
				marked++
			}
			if len(r.Spares) > 0 {
				t.Errorf("delay %v seed %d: %v went spare with no clone running", d, seed, r.Spares[0].Node)
			}
		}
	}
	if marked == 0 {
		t.Error("no run marked: the test no longer reaches the false-mark residual")
	}
}
