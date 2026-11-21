package sim

import (
	"fmt"
	"math/rand/v2"
	"os"
	"strings"
	"testing"
	"time"
)

// clearMembers are the members an operator's clear reaches.
var clearMembers = []Addr{A, B, W}

// withClear adds the operator's clear command at at, reaching only the
// members in reach (a partial clear: the others were cut between the
// check phase and the clear), with value's guard bits.
func withClear(o Options, at time.Duration, reach []Addr, value int64) Options {
	for _, m := range reach {
		o.Faults = append(o.Faults, Fault{Kind: Clear, At: at, Node: m, Value: value})
	}
	return o
}

// splitEnd is when the last cut of a split schedule heals.
func splitEnd(o Options) time.Duration {
	var end time.Duration
	for _, f := range o.Faults {
		if f.Kind == Cut {
			end = max(end, f.At+f.For)
		}
	}
	return end
}

// splitStart is when the first cut of a split schedule starts.
func splitStart(o Options) time.Duration {
	start := time.Duration(-1)
	for _, f := range o.Faults {
		if f.Kind == Cut && (start < 0 || f.At < start) {
			start = f.At
		}
	}
	return start
}

// stopCopy ends the clone (Faults[0]) at at.
func stopCopy(o Options, at time.Duration) Options {
	o.Faults[0].For = at - o.Faults[0].At
	return o
}

// reachOf is seed's members for a partial clear: one or two of the three.
func reachOf(r *rand.Rand) []Addr {
	switch r.IntN(6) {
	case 0:
		return []Addr{A}
	case 1:
		return []Addr{B}
	case 2:
		return []Addr{W}
	case 3:
		return []Addr{A, B}
	case 4:
		return []Addr{A, W}
	default:
		return []Addr{B, W}
	}
}

// clearFam is one family of TestMeasureClear: a split shape with the
// operator's clear added by mk.
type clearFam struct {
	name string
	mk   func(o Options, r *rand.Rand) (Options, time.Duration) // the run, and when the clear is issued
}

func clearFams() []clearFam {
	const guarded = ClearGuarded | ClearPreflight
	inSplit := func(o Options, r *rand.Rand, honest, partial bool, value int64) (Options, time.Duration) {
		cut := splitStart(o)
		end := splitEnd(o)
		at := cut + time.Duration(r.Int64N(int64(end-cut)))
		if honest {
			stop := cut + lease + time.Duration(r.Int64N(int64(2*lease)))
			o = stopCopy(o, stop)
			// The operator stops the copy, then clears: the members stop
			// hearing it a lease and a quarter on (LiveBoots), so a clear
			// before that is refused, as the command's would be.
			wait := lease + lease/2
			at = stop + wait + time.Duration(r.Int64N(int64(max(end-stop-wait, ms))))
		}
		reach := clearMembers
		if partial {
			reach = reachOf(r)
		}
		return withClear(o, at, reach, value), at
	}
	return []clearFam{
		{"marks on, no clear", func(o Options, r *rand.Rand) (Options, time.Duration) { return o, 0 }},
		{"copy stopped in the split, full clear", func(o Options, r *rand.Rand) (Options, time.Duration) {
			return inSplit(o, r, true, false, guarded)
		}},
		{"copy stopped in the split, partial clear", func(o Options, r *rand.Rand) (Options, time.Duration) {
			return inSplit(o, r, true, true, guarded)
		}},
		{"copy running, full clear", func(o Options, r *rand.Rand) (Options, time.Duration) {
			return inSplit(o, r, false, false, guarded)
		}},
		{"copy running, partial clear", func(o Options, r *rand.Rand) (Options, time.Duration) {
			return inSplit(o, r, false, true, guarded)
		}},
		{"copy running, full clear, no check phase", func(o Options, r *rand.Rand) (Options, time.Duration) {
			return inSplit(o, r, false, false, ClearGuarded)
		}},
		{"copy running, full clear, unguarded", func(o Options, r *rand.Rand) (Options, time.Duration) {
			return inSplit(o, r, false, false, 0)
		}},
		{"copy running after the heal, full clear", func(o Options, r *rand.Rand) (Options, time.Duration) {
			end := splitEnd(o)
			o = stopCopy(o, end+6*lease)
			at := end + lease
			return withClear(o, at, clearMembers, guarded), at
		}},
		{"copy running after the heal, full clear, unguarded", func(o Options, r *rand.Rand) (Options, time.Duration) {
			end := splitEnd(o)
			o = stopCopy(o, end+6*lease)
			at := end + lease
			return withClear(o, at, clearMembers, 0), at
		}},
		{"copy cut from every member at the clear, then back", func(o Options, r *rand.Rand) (Options, time.Duration) {
			// The copy runs on, cut from A, B and W from the heal for two
			// leases plus up to two; the clear lands inside that cut,
			// once the members have stopped hearing it (lease and D on).
			end := splitEnd(o)
			c := o.Faults[0].Node
			iso := 2*lease + time.Duration(r.Int64N(int64(2*lease)))
			o = stopCopy(o, end+iso+6*lease)
			for _, m := range clearMembers {
				o.Faults = append(o.Faults, Fault{Kind: Cut, At: end, For: iso, From: c, To: m, Both: true})
			}
			at := end + lease + lease/2 + time.Duration(r.Int64N(int64(iso-lease-lease/2)))
			o.Until = end + iso + 6*lease + bound + 6*lease
			return withClear(o, at, clearMembers, guarded), at
		}},
		{"copy stopped after the heal, full clear", func(o Options, r *rand.Rand) (Options, time.Duration) {
			end := splitEnd(o)
			o = stopCopy(o, end)
			at := end + lease + lease/2
			o.Until = at + bound + 6*lease
			return withClear(o, at, clearMembers, guarded), at
		}},
		{"copy stopped after the heal, restart clear (today)", func(o Options, r *rand.Rand) (Options, time.Duration) {
			o = stopCopy(o, splitEnd(o))
			o = withRestartClear(o)
			return o, clearFrom(o)
		}},
	}
}

// TestMeasureClear measures the operator's clear-duplicate command in the
// split shapes (#1029 3c-3a; naut-lead's condition (i): a partial clear is
// never worse than marks on). Per family and shape: runs with two leaders,
// clears refused at the check phase, and, where the copy is gone, the
// longest leaderless time from the clear to the run's end. Run with
// QUORUM_MEASURE_CLEAR=1 (QUORUM_MEASURE_CLEAR_N seeds per shape, default
// 300; QUORUM_MEASURE_CLEAR_ONLY a family substring).
func TestMeasureClear(t *testing.T) {
	if os.Getenv("QUORUM_MEASURE_CLEAR") == "" {
		t.Skip("set QUORUM_MEASURE_CLEAR=1")
	}
	n := uint64(300)
	if v := os.Getenv("QUORUM_MEASURE_CLEAR_N"); v != "" {
		fmt.Sscan(v, &n)
	}
	only := os.Getenv("QUORUM_MEASURE_CLEAR_ONLY")
	for _, fam := range clearFams() {
		if only != "" && !contains(fam.name, only) {
			continue
		}
		for _, sh := range clearShapes() {
			two, refused, cleared, live, gaps := 0, 0, 0, 0, []time.Duration{}
			for seed := uint64(1); seed <= n; seed++ {
				r := rand.New(rand.NewPCG(seed, 0x636c656172))
				o, at := fam.mk(sh.run(seed), r)
				rec := Run(o)
				if overlap(Check(rec, cloneChecks(o))) {
					two++
				}
				ref := false
				for _, c := range rec.Clears {
					if len(c.Refused) > 0 {
						ref = true
					}
					if len(c.Cleared) > 0 {
						cleared++
					}
				}
				if ref {
					refused++
				}
				if at > 0 && o.Faults[0].At+o.Faults[0].For > at && !spareBefore(rec, o.Faults[0].Node, at) {
					live++ // the copy ran, not a spare, when the clear was issued
				}
				if at > 0 && o.Faults[0].At+o.Faults[0].For <= at && o.Faults[0].For > 0 {
					gaps = append(gaps, maxGap(rec, max(at, splitEnd(o)), o.Until))
				}
			}
			line := fmt.Sprintf("%-50s %-40s two leaders %3d/%d  refused %3d  member clears %4d  copy live %3d", fam.name, sh.name, two, n, refused, cleared, live)
			if len(gaps) > 0 {
				var sum, worst time.Duration
				for _, g := range gaps {
					sum += g
					worst = max(worst, g)
				}
				line += fmt.Sprintf("  gap avg %.2fs max %.2fs", (sum / time.Duration(len(gaps))).Seconds(), worst.Seconds())
			}
			t.Log(line)
		}
	}
}

// clearShapes are the split shapes, plus the two clone splits brought
// forward to one to two seconds after the clone starts: after the mark,
// before the copy can go spare (as TestCloneSplitVoterRestarts' early
// runs).
func clearShapes() []struct {
	name string
	run  func(seed uint64) Options
} {
	out := splitShapes()
	for _, mirror := range []bool{false, true} {
		out = append(out, struct {
			name string
			run  func(seed uint64) Options
		}{fmt.Sprintf("early split, mirror %v", mirror), func(seed uint64) Options {
			o := cloneSplit(seed, mirror)
			r := rand.New(rand.NewPCG(seed, 0x65617279))
			at := o.Faults[0].At + sec + time.Duration(r.Int64N(int64(sec)))
			for i := range o.Faults {
				if o.Faults[i].Kind == Cut {
					o.Faults[i].At = at
				}
			}
			return o
		}})
	}
	return out
}

// spareBefore reports whether copy c had made itself a spare by at.
func spareBefore(r *Record, c Addr, at time.Duration) bool {
	for _, s := range r.Spares {
		if s.Node == c && s.At <= at {
			return true
		}
	}
	return false
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// TestTraceClear prints the failing seeds of one family and shape
// (QUORUM_TRACE_CLEAR_FAM, _SHAPE), and traces QUORUM_TRACE_CLEAR_SEED.
func TestTraceClear(t *testing.T) {
	fam, shape := os.Getenv("QUORUM_TRACE_CLEAR_FAM"), os.Getenv("QUORUM_TRACE_CLEAR_SHAPE")
	if fam == "" {
		t.Skip("set QUORUM_TRACE_CLEAR_FAM")
	}
	var one uint64
	fmt.Sscan(os.Getenv("QUORUM_TRACE_CLEAR_SEED"), &one)
	for _, f := range clearFams() {
		if f.name != fam {
			continue
		}
		for _, sh := range clearShapes() {
			if sh.name != shape {
				continue
			}
			for seed := uint64(1); seed <= 300; seed++ {
				if one != 0 && seed != one {
					continue
				}
				r := rand.New(rand.NewPCG(seed, 0x636c656172))
				o, at := f.mk(sh.run(seed), r)
				o.Trace = one != 0
				rec := Run(o)
				if one != 0 {
					os.WriteFile(os.Getenv("QUORUM_TRACE_CLEAR_OUT"), []byte(strings.Join(rec.Trace, "\n")), 0o644)
				}
				if vs := Check(rec, cloneChecks(o)); overlap(vs) {
					t.Logf("seed %d: clear at %v, clears %+v, faults %#v: %v", seed, at, rec.Clears, o.Faults, vs[0])
				}
			}
		}
	}
}

// A clear issued while the copy still runs behind a split that puts it
// with one member and its original with another: no member alone hears
// two boots, but the members together do, and the command's check phase
// refuses (quorum.CopyLive). Early splits (after the mark, before the copy
// can go spare), the copy live at every clear. Measured at 300 seeds per
// shape: refused 300 of 300, no two leaders; with the check asking each
// member alone (clear_per_member) two leaders in 7 of 300, unguarded 7.
func TestAClearWhileTheCopyRunsBehindASplitIsRefused(t *testing.T) {
	n := splitSeeds()
	sh := clearShapes()
	for _, s := range sh[len(sh)-2:] {
		two, cleared := 0, 0
		for seed := uint64(1); seed <= n; seed++ {
			r := rand.New(rand.NewPCG(seed, 0x636c656172))
			o := s.run(seed)
			cut, end := splitStart(o), splitEnd(o)
			at := cut + time.Duration(r.Int64N(int64(end-cut)))
			o = withClear(o, at, clearMembers, ClearGuarded|ClearPreflight)
			rec := Run(o)
			if overlap(Check(rec, cloneChecks(o))) {
				two++
				if two <= 3 {
					t.Errorf("%s, seed %d: two leaders after a clear at %v", s.name, seed, at)
				}
			}
			for _, c := range rec.Clears {
				cleared += len(c.Cleared)
			}
		}
		if two > 0 {
			t.Errorf("%s: two leaders in %d of %d", s.name, two, n)
		}
		if cleared > 0 {
			t.Errorf("%s: %d marks cleared while the copy ran; want every clear refused", s.name, cleared)
		}
	}
}

// The operator stopped the copy inside a split and the clear reached only
// one or two of the three members (the others cut between the check phase
// and the clear): never worse than leaving the marks on, no two leaders in
// any split shape (naut-lead's condition (i)). The command reports the
// members still marking; the operator runs it again a lease after the
// heal, and every oracle holds (a leader, steady on all three).
// Measured (TestMeasureClear, 300 seeds per shape): 0 two leaders; the
// longest leaderless time after the heal averaged up to 1.5 s (4.0 s at
// most) partial, 0.1-0.5 s (1.0 s) full; a clear after the heal 0.0 s
// (0.7 s), against 3.2-4.5 s (5.3 s) for the restart clear.
func TestAPartialClearAfterTheCopyStopsIsNeverWorse(t *testing.T) {
	n := splitSeeds()
	fam := clearFams()[2]
	if fam.name != "copy stopped in the split, partial clear" {
		t.Fatalf("family %q", fam.name)
	}
	for _, s := range clearShapes() {
		failing := 0
		for seed := uint64(1); seed <= n; seed++ {
			r := rand.New(rand.NewPCG(seed, 0x636c656172))
			o, _ := fam.mk(s.run(seed), r)
			again := splitEnd(o) + lease
			o = withClear(o, again, clearMembers, ClearGuarded|ClearPreflight)
			o.Until = max(o.Until, again+bound+6*lease)
			if vs := Check(Run(o), cloneChecks(o)); len(vs) > 0 {
				failing++
				if failing <= 3 {
					t.Errorf("%s, seed %d: %v", s.name, seed, vs[0])
				}
			}
		}
		if failing > 0 {
			t.Errorf("%s: %d of %d failing", s.name, failing, n)
		}
	}
}

// returnIntoSplit is the #1135 review's schedule: a clone of A or B from
// 8 s, cut from every member one to two seconds later (alive, unheard:
// unplugged or paused, not stopped), the operator's clear a lease and a
// half to two and a half on (the check passes: no member hears it), the
// copy's cuts healing eight to ten leases after the clear, then, after a
// touch of under touchMax of full connectivity, a split that puts the copy
// with one member and its original with another for eight leases.
func returnIntoSplit(seed uint64, mirror bool, touchMax time.Duration, clear bool) Options {
	o := randomRun(seed, profClone)
	r := rand.New(rand.NewPCG(seed, 0x7072))
	at := 8 * sec
	c := []Addr{CA, CB}[r.IntN(2)]
	orig, peer := original(c), peerOf(original(c))
	tIso := at + sec + time.Duration(r.Int64N(int64(sec)))
	t0 := tIso + lease + lease/2 + time.Duration(r.Int64N(int64(lease)))
	tR := t0 + time.Duration(r.Int64N(int64(2*lease))) + 8*lease
	var touch time.Duration
	if touchMax > 0 {
		touch = time.Duration(r.Int64N(int64(touchMax)))
	}
	ts := tR + touch
	end := ts + 8*lease
	o.Faults = []Fault{{Kind: Clone, At: at, For: end + 2*lease - at, Node: c}}
	for _, m := range clearMembers {
		o.Faults = append(o.Faults, Fault{Kind: Cut, At: tIso, For: tR - tIso, From: c, To: m, Both: true})
	}
	far, near := W, peer // the original with the witness, the copy with the peer
	if mirror {
		far, near = peer, W
	}
	o.Faults = append(o.Faults,
		Fault{Kind: Cut, At: ts, For: 8 * lease, From: orig, To: near, Both: true},
		Fault{Kind: Cut, At: ts, For: 8 * lease, From: peer, To: W, Both: true},
		Fault{Kind: Cut, At: ts, For: 8 * lease, From: c, To: far, Both: true})
	o.Until = end + 2*lease + bound + 6*lease
	if clear {
		o = withClear(o, t0, clearMembers, ClearGuarded|ClearPreflight)
	}
	return o
}

// A copy that is alive but unheard when the operator clears (unplugged,
// paused, cut off: the attestation was wrong), and later comes back into a
// split beside one member, is marked again by its first new frame: the
// clear retired every boot of the name but the one the members heard
// (Voter.ClearDuplicateKeeping), so it gets no grant and its voter's vote
// counts nowhere (#1135 review). Before: two leaders in 26, 23, 14 of 500
// (touch 0, <1 s, <2 s, the copy with the peer), more than the restart
// route's 25, 6, 2; now 0. clear_forgets_boots turns it red.
func TestACopyUnheardAtTheClearIsMarkedWhenItReturns(t *testing.T) {
	n := splitSeeds()
	for _, touch := range []time.Duration{0, lease / 2, lease} {
		for _, mirror := range []bool{false, true} {
			two, cleared := 0, 0
			for seed := uint64(1); seed <= n; seed++ {
				o := returnIntoSplit(seed, mirror, touch, true)
				rec := Run(o)
				if overlap(Check(rec, cloneChecks(o))) {
					two++
					if two <= 3 {
						t.Errorf("touch <%v, mirror %v, seed %d: two leaders", touch, mirror, seed)
					}
				}
				for _, c := range rec.Clears {
					cleared += len(c.Cleared)
				}
			}
			if two > 0 {
				t.Errorf("touch <%v, mirror %v: two leaders in %d of %d", touch, mirror, two, n)
			}
			if cleared == 0 {
				t.Errorf("touch <%v, mirror %v: no clear went through; the test proves nothing", touch, mirror)
			}
		}
	}
}

// returnBesideTheUnknowing is returnIntoSplit with the copy cut from the
// member it later returns beside from its start, so only the other member
// ever heard it: only that one marks, and only that one knew its boot.
func returnBesideTheUnknowing(seed uint64, mirror bool, touchMax time.Duration) Options {
	o := returnIntoSplit(seed, mirror, touchMax, true)
	c := o.Faults[0].Node
	near := peerOf(original(c))
	if mirror {
		near = W
	}
	tIso := o.Faults[1].At // the copy cut from every member (returnIntoSplit's first cuts)
	o.Faults = append(o.Faults, Fault{Kind: Cut, At: o.Faults[0].At, For: tIso - o.Faults[0].At, From: c, To: near, Both: true})
	return o
}

// A copy known to only one member (heard and marked by the witness alone,
// say), cut off at the clear, comes back beside the member that never
// heard it. Each member retires every boot ANY member knew (the check
// gathers them, naut-lead's ruling on #1135), so that member marks the
// copy on its first frame too. clear_retires_own_only shows what the
// union adds.
func TestACopyKnownToOneMemberIsRetiredAtAll(t *testing.T) {
	n := splitSeeds()
	for _, touch := range []time.Duration{0, lease / 2, lease} {
		for _, mirror := range []bool{false, true} {
			two, cleared := 0, 0
			for seed := uint64(1); seed <= n; seed++ {
				o := returnBesideTheUnknowing(seed, mirror, touch)
				rec := Run(o)
				if overlap(Check(rec, cloneChecks(o))) {
					two++
					if two <= 3 {
						t.Errorf("touch <%v, mirror %v, seed %d: two leaders", touch, mirror, seed)
					}
				}
				for _, c := range rec.Clears {
					cleared += len(c.Cleared)
				}
			}
			if two > 0 {
				t.Errorf("touch <%v, mirror %v: two leaders in %d of %d", touch, mirror, two, n)
			}
			if cleared == 0 {
				t.Errorf("touch <%v, mirror %v: no clear went through; the test proves nothing", touch, mirror)
			}
			t.Logf("touch <%v, mirror %v: two leaders %d of %d, member clears %d", touch, mirror, two, n, cleared)
		}
	}
}

// returnSilencedIntoSplit is returnIntoSplit with the copy paused (Pause:
// its process stopped, its sockets open) or hung (Freeze: its clock and
// timers stopped) at the clear instead of cut off, or cut off with its
// scans hung from then on (Unhealthy): alive, unheard, and back as the
// same boot. The #1135 re-check's residual: 4 (pause) and up to 13 (hung
// scans) of 1,000 two-leader runs (#1029 3c-3b closes it).
func returnSilencedIntoSplit(seed uint64, mirror bool, touchMax time.Duration, how FaultKind) Options {
	o := returnIntoSplit(seed, mirror, touchMax, true)
	cut := o.Faults[1] // the copy cut from every member: faults 1..3
	if how == Unhealthy {
		clone := o.Faults[0]
		o.Faults = append(o.Faults, Fault{Kind: Unhealthy, At: cut.At, For: clone.At + clone.For - cut.At, Node: cut.From})
		return o
	}
	f := []Fault{o.Faults[0], {Kind: how, At: cut.At, For: cut.For, Node: cut.From}}
	o.Faults = append(f, o.Faults[1+len(clearMembers):]...)
	return o
}

// A copy paused or hung at the operator's clear runs on as the boot the
// clear retired: its first new frame marks the name again (retiredFrame),
// and its voter's replies count nowhere and mark the name at the
// controller that hears them (Voter.RetiredReply), so it never backs a
// second leader when it returns into a split. Before 3c-3b: 4 and up to 13
// of 1,000 two-leader runs (pause, hung scans). retired_reply_counted
// turns it red.
func TestACopyPausedOrHungAtTheClearIsMarkedWhenItReturns(t *testing.T) {
	n := splitSeeds()
	for _, how := range []FaultKind{Pause, Freeze, Unhealthy} {
		for _, touch := range []time.Duration{0, lease / 2, lease} {
			for _, mirror := range []bool{false, true} {
				two, cleared := 0, 0
				for seed := uint64(1); seed <= n; seed++ {
					o := returnSilencedIntoSplit(seed, mirror, touch, how)
					rec := Run(o)
					if overlap(Check(rec, cloneChecks(o))) {
						two++
						if two <= 3 {
							t.Errorf("%v, touch <%v, mirror %v, seed %d: two leaders", kindNames[how], touch, mirror, seed)
						}
					}
					for _, c := range rec.Clears {
						cleared += len(c.Cleared)
					}
				}
				if two > 0 {
					t.Errorf("%v, touch <%v, mirror %v: two leaders in %d of %d", kindNames[how], touch, mirror, two, n)
				}
				if cleared == 0 {
					t.Errorf("%v, touch <%v, mirror %v: no clear went through; the test proves nothing", kindNames[how], touch, mirror)
				}
			}
		}
	}
}

// TestMeasurePausedAtTheClear prints the two-leader counts of
// returnSilencedIntoSplit (QUORUM_MEASURE_PAUSED=1, QUORUM_MEASURE_N seeds,
// default 1000), for the PR and the ablations.
func TestMeasurePausedAtTheClear(t *testing.T) {
	if os.Getenv("QUORUM_MEASURE_PAUSED") == "" {
		t.Skip("set QUORUM_MEASURE_PAUSED=1")
	}
	n := uint64(1000)
	fmt.Sscan(os.Getenv("QUORUM_MEASURE_N"), &n)
	apart := os.Getenv("QUORUM_VOTER_BOOT_APART") != ""
	for _, how := range []FaultKind{Pause, Freeze, Unhealthy, Cut} {
		for _, touch := range []time.Duration{0, lease / 2, lease} {
			for _, mirror := range []bool{false, true} {
				two := 0
				for seed := uint64(1); seed <= n; seed++ {
					var o Options
					if how == Cut {
						o = returnIntoSplit(seed, mirror, touch, true)
					} else {
						o = returnSilencedIntoSplit(seed, mirror, touch, how)
					}
					o.VoterBootApart = apart
					if overlap(Check(Run(o), cloneChecks(o))) {
						two++
					}
				}
				fmt.Printf("PAUSED mut=%s apart=%v %-6v touch<%-5v mirror=%-5v two leaders %d of %d\n", os.Getenv("QUORUM_MUT"), apart, kindNames[how], touch, mirror, two, n)
			}
		}
	}
}
