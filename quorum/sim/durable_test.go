package sim

// Measurements of durable duplicate marks (#1029 3c-3b) over the
// clone-split generators, for the PR and for naut-lead's rulings. Off by
// default; ablations by mutation (-tags quorum_mut, QUORUM_MUT=...).
//
//	QUORUM_MEASURE_DURABLE=<cell,...> QUORUM_MEASURE_N=<seeds> \
//	  go test ./quorum/sim -run '^TestMeasureDurable$' -v
//
// cells: singles, singles-early, doubles, doubles-early, triples,
// triples-early, voter, split, window, wipe, falsemark, falsemark-kill,
// clearlive, renewal.

import (
	"fmt"
	"math/rand/v2"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// durTally counts one cell's runs.
type durTally struct {
	runs, two, other, restored, restoredName, lost, endMarked, spare int
	origSpare, copySpare                                             int
	twoMarkedBefore, twoLost                                         int
	oracles                                                          map[string]int
	twoSeeds                                                         []uint64
	firstOther                                                       string
}

func (t *durTally) add(seed uint64, o Options, r *Record) {
	t.runs++
	c, _ := cloneOfRun(o)
	vs := Check(r, cloneChecks(o))
	if overlap(vs) {
		t.two++
		// a two-leader run where some voter had marked the name before
		// the first overlap (else it is the unmarked early-cut residual)
		first := time.Duration(-1)
		for _, v := range vs {
			if v.Oracle == "I1" {
				first = v.At
				break
			}
		}
		for _, m := range r.Marks {
			if m.At < first {
				t.twoMarkedBefore++
				break
			}
		}
		if r.MarkWritesLost > 0 {
			t.twoLost++
		}
		if len(t.twoSeeds) < 5 {
			t.twoSeeds = append(t.twoSeeds, seed)
		}
	}
	seen := map[string]bool{}
	for _, v := range vs {
		if v.Oracle == "I1" || seen[v.Oracle] {
			continue
		}
		seen[v.Oracle] = true
		if t.oracles == nil {
			t.oracles = map[string]int{}
		}
		name := v.Oracle
		if v.Oracle == "steady" && markedLeader(r, v.What) {
			// "voter X holds no promise to the leader L" where X still
			// marks L at the end: the mark's stated cost until a clear.
			name = "steady(markHeld)"
		}
		t.oracles[name]++
		if t.firstOther == "" {
			t.firstOther = fmt.Sprintf("seed %d: %v", seed, v)
		}
	}
	if len(seen) > 0 {
		t.other++
	}
	if len(r.MarksRestored) > 0 {
		t.restored++
	}
	if c >= 0 {
		for _, m := range r.MarksRestored {
			if m.Name == nodeIDs[original(c)] {
				t.restoredName++
				break
			}
		}
	}
	if r.MarkWritesLost > 0 {
		t.lost++
	}
	if len(r.MarkedAtEnd) > 0 {
		t.endMarked++
	}
	for _, s := range r.Spares {
		if !s.Restart {
			t.spare++
			break
		}
	}
	if c >= 0 {
		for _, s := range r.Spares {
			if !s.Restart && s.Node == original(c) {
				t.origSpare++
				fmt.Printf("ORIGSPARE seed=%d at=%v marks=%v\n", seed, s.At, r.Marks)
				break
			}
		}
		for _, s := range r.Spares {
			if !s.Restart && s.Node == c {
				t.copySpare++
				break
			}
		}
	}
}

func (t *durTally) String() string {
	var os_ []string
	for k, v := range t.oracles {
		os_ = append(os_, fmt.Sprintf("%s:%d", k, v))
	}
	sort.Strings(os_)
	return fmt.Sprintf("runs=%d TWO=%d (markedBefore=%d lostWrite=%d) otherFail=%d [%s] restored=%d restoredName=%d lostWrite=%d markedAtEnd=%d spare=%d origSpare=%d copySpare=%d twoSeeds=%v %s",
		t.runs, t.two, t.twoMarkedBefore, t.twoLost, t.other, strings.Join(os_, " "), t.restored, t.restoredName, t.lost, t.endMarked, t.spare, t.origSpare, t.copySpare, t.twoSeeds, t.firstOther)
}

// durRun runs gen over seeds 1..n on GOMAXPROCS workers.
func durRun(n uint64, gen func(seed uint64) Options) *durTally {
	var mu sync.Mutex
	var tl durTally
	var wg sync.WaitGroup
	seeds := make(chan uint64)
	for range runtime.GOMAXPROCS(0) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for s := range seeds {
				o := gen(s)
				r := Run(o)
				mu.Lock()
				tl.add(s, o, r)
				mu.Unlock()
			}
		}()
	}
	for s := uint64(1); s <= n; s++ {
		seeds <- s
	}
	close(seeds)
	wg.Wait()
	return &tl
}

// voterRestartsRun is TestCloneSplitVoterRestarts' schedule.
func voterRestartsRun(seed uint64, early, mirror bool, who string) Options {
	o := cloneSplit(seed, mirror)
	c := o.Faults[0].Node
	if early {
		r := rand.New(rand.NewPCG(seed, 0x65617279))
		at := o.Faults[0].At + sec + time.Duration(r.Int64N(int64(sec)))
		for i := range o.Faults {
			if o.Faults[i].Kind == Cut {
				o.Faults[i].At = at
			}
		}
	}
	cut := o.Faults[1].At
	peer := peerOf(original(c))
	r := rand.New(rand.NewPCG(seed, 0x72737472))
	down := cut + lease + time.Duration(r.Int64N(int64(4*lease)))
	switch who {
	case "peer":
		o.Faults = append(o.Faults, Fault{Kind: Crash, At: down, For: 100 * ms, Node: peer})
	case "witness":
		o.Faults = append(o.Faults, Fault{Kind: Crash, At: down, For: 100 * ms, Node: W})
	default:
		o.Faults = append(o.Faults,
			Fault{Kind: Crash, At: down, For: 100 * ms, Node: W},
			Fault{Kind: Crash, At: down + lease/3, For: 100 * ms, Node: peer})
	}
	return o
}

// earlySplit moves a split shape's cuts to one to two seconds after the
// clone starts (after the mark, before the spare), as clearShapes does.
func earlySplit(o Options, seed uint64) Options {
	r := rand.New(rand.NewPCG(seed, 0x65617279))
	at := o.Faults[0].At + sec + time.Duration(r.Int64N(int64(sec)))
	var cut time.Duration
	for _, f := range o.Faults {
		if f.Kind == Cut {
			cut = max(cut, f.At)
		}
	}
	for i := range o.Faults {
		if o.Faults[i].Kind == Cut && o.Faults[i].At == cut {
			o.Faults[i].At = at
		}
	}
	return o
}

func TestMeasureDurable(t *testing.T) {
	cells := os.Getenv("QUORUM_MEASURE_DURABLE")
	if cells == "" {
		t.Skip("set QUORUM_MEASURE_DURABLE")
	}
	n := uint64(300)
	if v, err := strconv.Atoi(os.Getenv("QUORUM_MEASURE_N")); err == nil && v > 0 {
		n = uint64(v)
	}
	has := func(c string) bool { return strings.Contains(","+cells+",", ","+c+",") }
	mode := os.Getenv("QUORUM_MUT")
	if mode == "" {
		mode = "none"
	}
	out := func(cell, row string, tl *durTally) {
		fmt.Printf("DURABLE mut=%s cell=%s | %-75s | %v\n", mode, cell, row, tl)
	}
	who := []string{"original", "clone", "peer", "witness"}
	if has("singles") || has("singles-early") {
		for _, early := range []bool{false, true} {
			if (early && !has("singles-early")) || (!early && !has("singles")) {
				continue
			}
			for _, w := range who {
				all := &durTally{}
				for _, sh := range restartShapes() {
					tl := durRun(n, func(s uint64) Options {
						o := sh.run(s)
						if early {
							o = earlySplit(o, s)
						}
						return withRestarts(o, s, w)
					})
					if tl.two > 0 || tl.other > 0 {
						out("singles", fmt.Sprintf("early=%v %s / %s", early, w, sh.name), tl)
					}
					all.merge(tl)
				}
				out("singles", fmt.Sprintf("early=%v %s restarts, all shapes", early, w), all)
			}
		}
	}
	if has("doubles") || has("doubles-early") {
		for _, early := range []bool{false, true} {
			if (early && !has("doubles-early")) || (!early && !has("doubles")) {
				continue
			}
			for i, a := range who {
				for _, b := range who[i+1:] {
					all := &durTally{}
					for _, sh := range restartShapes() {
						tl := durRun(n, func(s uint64) Options {
							o := sh.run(s)
							if early {
								o = earlySplit(o, s)
							}
							return withRestarts(o, s, a, b)
						})
						if tl.two > 0 {
							out("doubles", fmt.Sprintf("early=%v %s+%s / %s", early, a, b, sh.name), tl)
						}
						all.merge(tl)
					}
					out("doubles", fmt.Sprintf("early=%v %s and %s restart, all shapes", early, a, b), all)
				}
			}
		}
	}
	if has("triples") || has("triples-early") {
		// The case 3c-1 left open: three members restart inside one split.
		for _, early := range []bool{false, true} {
			if (early && !has("triples-early")) || (!early && !has("triples")) {
				continue
			}
			for _, tri := range [][]string{{"witness", "peer", "clone"}, {"clone", "witness", "peer"}, {"original", "witness", "peer"}, {"original", "clone", "peer"}, {"original", "clone", "witness"}} {
				all := &durTally{}
				for _, sh := range restartShapes() {
					tl := durRun(n, func(s uint64) Options {
						o := sh.run(s)
						if early {
							o = earlySplit(o, s)
						}
						return withRestarts(o, s, tri...)
					})
					if tl.two > 0 {
						out("triples", fmt.Sprintf("early=%v %v / %s", early, tri, sh.name), tl)
					}
					all.merge(tl)
				}
				out("triples", fmt.Sprintf("early=%v %v restart, all shapes", early, tri), all)
			}
		}
	}
	if has("falsemark") {
		// 3c-1's false-mark residual (a restart's old-boot frames late past
		// the delay rule mark a name no copy runs), then the marking
		// voter restarts three leases on: today that restart forgets the
		// false mark; with durable marks it stays until a clear.
		for _, d := range lateDelays {
			tl := durRun(n, func(s uint64) Options { return falseMarkRun(s, d) })
			out("falsemark", fmt.Sprintf("late old-boot frames %v, marking voter restarts", d), tl)
		}
	}
	if has("falsemark-kill") {
		// As falsemark, then the controller NOT marked dies for good three
		// leases after the voter's restart: the marked one must lead on
		// the two voters left, one of which may be the marking voter.
		for _, d := range lateDelays {
			runs, marked, noLeader := 0, 0, 0
			for s := uint64(1); s <= n; s++ {
				o := falseMarkRun(s, d)
				at := o.Faults[2].At + 3*lease
				o.Until = at + bound + 8*lease
				rec := Run(o)
				if len(rec.Marks) == 0 {
					continue
				}
				marked++
				victim, marked_ := A, B
				if rec.Marks[0].Name == "A" {
					victim, marked_ = B, A
				}
				if os.Getenv("QUORUM_DURABLE_FK_RESTART") != "" {
					// The guide's restart clear: the marked controller
					// restarts too, after its voter did.
					o.Faults = append(o.Faults, Fault{Kind: Crash, At: o.Faults[2].At + lease, For: 50 * ms, Node: marked_})
				}
				o.Faults = append(o.Faults, Fault{Kind: Crash, At: at, For: time.Hour, Node: victim})
				o.Trace = os.Getenv("QUORUM_DURABLE_TRACE_FK") == fmt.Sprintf("%d,%v", s, d)
				rec = Run(o)
				if o.Trace {
					os.WriteFile(os.Getenv("QUORUM_DURABLE_TRACE_OUT"), []byte(strings.Join(rec.Trace, "\n")), 0o644)
					fmt.Printf("faults %#v marks %+v\n", o.Faults, rec.Marks)
				}
				runs++
				if maxGap(rec, at+bound, o.Until) > 0 {
					noLeader++
				}
			}
			fmt.Printf("DURABLE mut=%s cell=falsemark-kill | late frames %v | runs with a false mark=%d, then the other controller dies: no leader to the end (after the bound) in %d\n", mode, d, marked, noLeader)
		}
	}
	if has("clearlive") {
		// Liveness after the operator's clear: the copy stopped at the
		// heal, a voter (peer or witness) restarts two leases on, the full
		// guarded clear lands `gap` after that restart, and two leases
		// later the controller NOT marked dies for good: the marked one
		// must campaign and lead. gap 2 leases: the voter's new boot has
		// answered the held controller first; gap 0-300 ms: the clear may
		// beat that answer.
		for _, v := range []string{"peer", "witness"} {
			for _, race := range []bool{false, true} {
				runs, noLeader, held, refused := 0, 0, 0, 0
				for _, sh := range splitShapes() {
					for s := uint64(1); s <= n; s++ {
						o := sh.run(s)
						end := splitEnd(o)
						o = stopCopy(o, end)
						c := o.Faults[0].Node
						orig := original(c)
						voter := W
						if v == "peer" {
							voter = peerOf(orig)
						}
						r := rand.New(rand.NewPCG(s, 0x636c6c76))
						restart := end + 2*lease // the copy's boot aged out of LiveBoots by then
						at := restart + 2*lease
						if race {
							at = restart + 50*ms + time.Duration(r.Int64N(int64(300*ms)))
						}
						o.Faults = append(o.Faults, Fault{Kind: Crash, At: restart, For: 50 * ms, Node: voter})
						o = withClear(o, at, clearMembers, ClearGuarded|ClearPreflight)
						kill := at + 2*lease
						victim := peerOf(orig)
						o.Faults = append(o.Faults, Fault{Kind: Crash, At: kill, For: time.Hour, Node: victim})
						o.Until = kill + bound + 6*lease
						rec := Run(o)
						runs++
						if maxGap(rec, kill+bound, o.Until) > 0 {
							noLeader++
							if out := os.Getenv("QUORUM_DURABLE_TRACE_OUT"); out != "" && noLeader == 1 && race && v == "witness" {
								o.Trace = true
								rec := Run(o)
								os.WriteFile(out, []byte(strings.Join(rec.Trace, "\n")), 0o644)
								fmt.Printf("traced %s seed %d: restart %v clear %v kill %v\n", sh.name, s, restart, at, kill)
							}
						}
						if len(rec.MarkedAtEnd) > 0 {
							held++
						}
						for _, c := range rec.Clears {
							if len(c.Refused) > 0 {
								refused++
								break
							}
						}
					}
				}
				fmt.Printf("DURABLE mut=%s cell=clearlive | %s restarts, clear %s, then the original's peer dies | runs=%d noLeaderToEnd=%d markedAtEnd=%d clearRefused=%d\n",
					mode, v, map[bool]string{false: "2 leases after", true: "50-350 ms after"}[race], runs, noLeader, held, refused)
			}
		}
	}
	if has("voter") {
		for _, early := range []bool{false, true} {
			for _, mirror := range []bool{false, true} {
				for _, w := range []string{"peer", "witness", "both"} {
					tl := durRun(n, func(s uint64) Options { return voterRestartsRun(s, early, mirror, w) })
					out("voter", fmt.Sprintf("early=%v mirror=%v %s restarts", early, mirror, w), tl)
				}
			}
		}
	}
	if has("window") {
		// The fsync window: both a mark and its write in flight when the
		// voter crashes (SlowDisk on the voter(s) over the clone's start,
		// a crash 0.6 to 2 s after it), with the split one to two seconds
		// after the clone starts.
		for _, mirror := range []bool{false, true} {
			for _, slow := range []string{"peer", "both"} {
				for _, crash := range []string{"peer", "witness", "both"} {
					tl := durRun(n, func(s uint64) Options { return windowRun(s, mirror, slow, crash) })
					out("window", fmt.Sprintf("mirror=%v slow=%s crash=%s", mirror, slow, crash), tl)
				}
			}
		}
	}
	if has("slowonly") {
		// The window's slow disks with no crash: does holding the
		// incumbent's renewals behind the mark's fsync (real) cost a lease?
		for _, mirror := range []bool{false, true} {
			tl := durRun(n, func(s uint64) Options { return windowRun(s, mirror, "both", "none") })
			gaps := 0
			for s := uint64(1); s <= n; s++ {
				o := windowRun(s, mirror, "both", "none")
				r := Run(o)
				if maxGap(r, o.Faults[0].At, o.Faults[0].At+4*sec) > 0 {
					gaps++
				}
			}
			out("slowonly", fmt.Sprintf("mirror=%v slow=both, no crash (leaderless gap in clone..+4 s: %d)", mirror, gaps), tl)
		}
	}
	if has("restartclear") {
		// The guide's restart clear (W, peer, then the original) after the
		// copy stopped at the heal: do the marks survive it?
		for _, sh := range splitShapes() {
			out("restartclear", sh.name, durRun(n, func(s uint64) Options {
				o := sh.run(s)
				return withRestartClear(stopCopy(o, splitEnd(o)))
			}))
		}
	}
	if has("renewal") {
		// A clone beside a settled pair, no split; one voter's disk slow
		// (1.5 s) or failing across the mark: does the incumbent's lease
		// break (real holds a renewal behind the mark's write)?
		for _, kind := range []FaultKind{SlowDisk, DiskFail} {
			for _, v := range []string{"peer", "witness"} {
				runs, breaks := 0, 0
				tl := durRun(n, func(s uint64) Options { return renewalRun(s, kind, v) })
				for s := uint64(1); s <= n; s++ {
					o := renewalRun(s, kind, v)
					r := Run(o)
					runs++
					if maxGap(r, o.Faults[0].At-sec, o.Faults[0].At+5*sec) > 0 {
						breaks++
					}
				}
				out("renewal", fmt.Sprintf("%v on the %s across the mark: incumbent lease broken (clone-1s..+5s) in %d of %d", kindNames[kind], v, breaks, runs), tl)
			}
		}
	}
	if has("wipe") {
		// A restart that loses the file (Wipe): durable marks can't help.
		for _, early := range []bool{false, true} {
			for _, w := range [][]string{{"peer"}, {"witness"}, {"witness", "peer"}} {
				all := &durTally{}
				for _, sh := range restartShapes() {
					all.merge(durRun(n, func(s uint64) Options {
						o := sh.run(s)
						if early {
							o = earlySplit(o, s)
						}
						o = withRestarts(o, s, w...)
						for i := range o.Faults {
							if o.Faults[i].Kind == Crash {
								o.Faults[i].Kind = Wipe
							}
						}
						return o
					}))
				}
				out("wipe", fmt.Sprintf("early=%v %v wiped, all shapes", early, w), all)
			}
		}
	}
	if has("split") {
		for _, sh := range splitShapes() {
			out("split", sh.name, durRun(n, sh.run))
		}
	}
}

func (t *durTally) merge(u *durTally) {
	t.runs += u.runs
	t.two += u.two
	t.twoMarkedBefore += u.twoMarkedBefore
	t.twoLost += u.twoLost
	t.other += u.other
	t.restored += u.restored
	t.restoredName += u.restoredName
	t.lost += u.lost
	t.endMarked += u.endMarked
	t.spare += u.spare
	t.origSpare += u.origSpare
	t.copySpare += u.copySpare
	for k, v := range u.oracles {
		if t.oracles == nil {
			t.oracles = map[string]int{}
		}
		t.oracles[k] += v
	}
	for _, s := range u.twoSeeds {
		if len(t.twoSeeds) < 5 {
			t.twoSeeds = append(t.twoSeeds, s)
		}
	}
	if t.firstOther == "" {
		t.firstOther = u.firstOther
	}
}

// windowRun: an early split of a clone, with the voters' fsync slowed over
// the clone's start (300 ms to 1.5 s) and a voter crash 0.6 to 2 s after
// it: the mark is made, its write in flight, when the voter goes down.
func windowRun(seed uint64, mirror bool, slow, crash string) Options {
	o := earlySplit(cloneSplit(seed, mirror), seed)
	c, at := o.Faults[0].Node, o.Faults[0].At
	peer := peerOf(original(c))
	r := rand.New(rand.NewPCG(seed, 0x77696e64))
	lat := 300*ms + time.Duration(r.Int64N(int64(1200*ms)))
	nodes := map[string][]Addr{"peer": {peer}, "witness": {W}, "both": {W, peer}}
	for _, x := range nodes[slow] {
		o.Faults = append(o.Faults, Fault{Kind: SlowDisk, At: at - sec, For: 4 * sec, Node: x, Value: int64(lat)})
	}
	down := at + 600*ms + time.Duration(r.Int64N(int64(1400*ms)))
	for i, x := range nodes[crash] {
		o.Faults = append(o.Faults, Fault{Kind: Crash, At: down + time.Duration(i)*100*ms, For: 100 * ms, Node: x})
	}
	return o
}

// TestTraceDurable runs one restart cell's seed over every shape whose name
// holds QUORUM_DURABLE_TRACE_SHAPE (QUORUM_DURABLE_TRACE_WHO: comma list of restarts, or
// "window:<mirror>:<slow>:<crash>"; QUORUM_DURABLE_TRACE_EARLY=1), prints the
// violations, and writes the trace of the first failing one to
// QUORUM_DURABLE_TRACE_OUT.
func TestTraceDurable(t *testing.T) {
	who := os.Getenv("QUORUM_DURABLE_TRACE_WHO")
	if who == "" {
		t.Skip("set QUORUM_DURABLE_TRACE_WHO")
	}
	var seed uint64
	fmt.Sscan(os.Getenv("QUORUM_DURABLE_TRACE_SEED"), &seed)
	early := os.Getenv("QUORUM_DURABLE_TRACE_EARLY") == "1"
	shape := os.Getenv("QUORUM_DURABLE_TRACE_SHAPE")
	wrote := false
	for _, sh := range restartShapes() {
		if !strings.Contains(sh.name, shape) {
			continue
		}
		var o Options
		if strings.HasPrefix(who, "window:") {
			p := strings.Split(who, ":")
			o = windowRun(seed, p[1] == "true", p[2], p[3])
		} else {
			o = sh.run(seed)
			if early {
				o = earlySplit(o, seed)
			}
			o = withRestarts(o, seed, strings.Split(who, ",")...)
		}
		o.Trace = true
		rec := Run(o)
		vs := Check(rec, cloneChecks(o))
		fmt.Printf("%s: %d violations, marks %+v restored %+v spares %+v atEnd %v\n", sh.name, len(vs), rec.Marks, rec.MarksRestored, rec.Spares, rec.MarkedAtEnd)
		for i, v := range vs {
			if i < 4 {
				fmt.Printf("   %v\n", v)
			}
		}
		if os.Getenv("QUORUM_DURABLE_TRACE_FAULTS") != "" && strings.Contains(sh.name, os.Getenv("QUORUM_DURABLE_TRACE_FAULTS")) {
			for _, f := range o.Faults {
				fmt.Printf("   fault %v at=%v for=%v node=%v\n", f.Kind, f.At, f.For, f.Node)
			}
		}
		if len(vs) > 0 && !wrote {
			wrote = true
			fmt.Printf("   faults %#v\n", o.Faults)
			os.WriteFile(os.Getenv("QUORUM_DURABLE_TRACE_OUT"), []byte(strings.Join(rec.Trace, "\n")), 0o644)
		}
		if strings.HasPrefix(who, "window:") {
			break
		}
	}
}

// markedLeader: a steady violation "voter X holds no promise to the leader
// L" where voter X still marks L's name at the run's end.
func markedLeader(r *Record, what string) bool {
	var x, l string
	if _, err := fmt.Sscanf(what, "voter %s holds no promise to the leader %s", &x, &l); err != nil {
		return false
	}
	for _, m := range r.MarkedAtEnd {
		if m.Voter.String() == x && string(m.Name) == strings.TrimPrefix(l, "C") {
			return true
		}
	}
	return false
}

// falseMarkRun is lateOldBootFrames(seed, d) with the voter the late
// frames go to restarted three leases after the controller's restart.
func falseMarkRun(seed uint64, d time.Duration) Options {
	o := lateOldBootFrames(seed, d)
	r := rand.New(rand.NewPCG(seed, 0x4c415445))
	c := []Addr{A, B}[r.IntN(2)]
	to := []Addr{W, peerOf(c)}[r.IntN(2)]
	at := o.Faults[1].At
	o.Faults = append(o.Faults, Fault{Kind: Crash, At: at + 3*lease, For: 50 * ms, Node: to})
	o.Until = at + 3*lease + bound + 8*lease
	return o
}

// renewalRun: a clone of A or B beside the settled pair from 8 s, no split,
// with kind (SlowDisk 1.5 s, or DiskFail) on one voter from 7 s for 4 s.
func renewalRun(seed uint64, kind FaultKind, v string) Options {
	o := cloneSplit(seed, false)
	c := o.Faults[0].Node
	x := W
	if v == "peer" {
		x = peerOf(original(c))
	}
	o.Faults = []Fault{o.Faults[0], {Kind: kind, At: o.Faults[0].At - sec, For: 4 * sec, Node: x, Value: int64(1500 * ms)}}
	return o
}

// TestTraceDurableRenewal traces renewalRun(QUORUM_DURABLE_TRACE_SEED, DiskFail, QUORUM_DURABLE_TRACE_V).
func TestTraceDurableRenewal(t *testing.T) {
	v := os.Getenv("QUORUM_DURABLE_TRACE_V")
	if v == "" {
		t.Skip("set QUORUM_DURABLE_TRACE_V")
	}
	var seed uint64
	fmt.Sscan(os.Getenv("QUORUM_DURABLE_TRACE_SEED"), &seed)
	o := renewalRun(seed, DiskFail, v)
	o.Trace = true
	r := Run(o)
	fmt.Printf("faults %#v\ngates %v\n", o.Faults, r.Gates())
	os.WriteFile(os.Getenv("QUORUM_DURABLE_TRACE_OUT"), []byte(strings.Join(r.Trace, "\n")), 0o644)
}

// Marks survive the voters' restarts (#1029 3c-3b): the peer and the
// witness restart inside an early split, while the copy runs. Before, the
// restarted voters forgot the mark and marked again after the heal on
// whichever boot they heard first, which made the REAL box a spare in 309
// of 3,600 runs; now they restore it, and tell the copy, not the original,
// it is a spare. marks_volatile turns it red.
func TestRestartedVotersKeepTheMarkAndTheIncumbent(t *testing.T) {
	n := uint64(40)
	all := &durTally{}
	for _, sh := range restartShapes() {
		all.merge(durRun(n, func(s uint64) Options {
			return withRestarts(earlySplit(sh.run(s), s), s, "peer", "witness")
		}))
	}
	if all.twoMarkedBefore > 0 {
		t.Errorf("two leaders in %d runs with a mark before the overlap", all.twoMarkedBefore)
	}
	if all.restored < int(all.runs)/2 {
		t.Errorf("a restarted voter read a mark back in %d of %d runs", all.restored, all.runs)
	}
	if all.origSpare > all.runs/100 {
		t.Errorf("the original was made a spare in %d of %d runs", all.origSpare, all.runs)
	}
	if all.copySpare < all.runs*3/4 {
		t.Errorf("the copy was made a spare in only %d of %d runs", all.copySpare, all.runs)
	}
}

// Rules (i) and (ii) still matter in the fsync window, the one place a
// mark can be lost (#1029 3c-3b): a voter marks and tells, then crashes
// before its write lands, and the restarted voter says unmarked from a
// new boot. Rule (i) keeps the hold set by the old boot; rule (ii) keeps
// the held controller's voter from granting the other one. Both voters
// crash inside a slowed write here (the prototype's window cells, the two
// rows where both disks are slow). Measured over 300 seeds each: 20 and 24
// two-leader runs (22 and 24 with marks not kept); without rule (i) 44 and
// 49, without rule (ii) 36 and 35. hold_released_by_any_boot and
// held_voter_grants turn it red.
func TestTheFsyncWindowKeepsRulesIAndII(t *testing.T) {
	two := 0
	for _, mirror := range []bool{false, true} {
		for seed := uint64(1); seed <= 300; seed++ {
			o := windowRun(seed, mirror, "both", "both")
			if overlap(Check(Run(o), cloneChecks(o))) {
				two++
			}
		}
	}
	if two > 55 {
		t.Errorf("two leaders in %d of 600 fsync-window runs, want at most 55 (44 measured; 71 without rule (ii), 93 without rule (i))", two)
	}
	t.Logf("two leaders in %d of 600", two)
}
