package sim

import (
	"fmt"
	"math/rand/v2"
	"os"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"
)

// #1029 3c-3c: recommission --from-spare, the operator's way back from a
// spare. The spare is either the real box (its copy ran first) or the
// copy; the incumbent (the boot the voters held) is silenced, the command
// runs on the spare once both other voters have not heard the incumbent
// for a lease and D, and the operator restarts the spare. The families
// are the ways the operator's "stopped" can be wrong: the incumbent only
// cut off, paused, frozen or hung (it returns with the same boot, which
// the clear retired), or switched off and later back on (a fresh boot
// with its old state, which no clear can name), with or without a split
// when it returns beside the replacement.

// fromSpareHow is how the incumbent is silenced at the command.
var fromSpareHow = []string{"off", "power", "cut", "pause", "freeze", "unhealthy"}

// fromSpareRun is one schedule: realBox picks which box is the spare, how
// the incumbent's silence; it returns at a random time with every link up.
func fromSpareRun(seed uint64, realBox bool, how string) (o Options, spare, incumbent Addr, tf time.Duration) {
	o = randomRun(seed, profClone)
	r := rand.New(rand.NewPCG(seed, 0x46535052))
	c := []Addr{CA, CB}[r.IntN(2)]
	at := 6*sec + time.Duration(r.Int64N(int64(4*lease)))
	o.Faults, spare, incumbent = withSpare(r, realBox, c, at)
	ts := at + 10*lease // the spare decided long before
	tf = ts + lease + lease/4 + lease/8 + time.Duration(r.Int64N(int64(2*lease)))
	restart := 100*ms + time.Duration(r.Int64N(int64(2*sec)))
	// When the incumbent is back: from a lease before the command to
	// eight leases after the spare's restart.
	back := tf - ts - lease/2 + time.Duration(r.Int64N(int64(restart+8*lease)))
	var silence Fault
	switch how {
	case "off":
		silence = Fault{Kind: Crash, At: ts, Node: incumbent} // for good
	case "power":
		silence = Fault{Kind: Crash, At: ts, For: back, Node: incumbent}
	case "pause":
		silence = Fault{Kind: Pause, At: ts, For: back, Node: incumbent}
	case "freeze":
		silence = Fault{Kind: Freeze, At: ts, For: back, Node: incumbent}
	case "unhealthy":
		silence = Fault{Kind: Unhealthy, At: ts, For: back, Node: incumbent}
	case "cut":
		o.Faults = append(o.Faults,
			Fault{Kind: Cut, At: ts, For: back, From: incumbent, To: peerOf(incumbent), Both: true},
			Fault{Kind: Cut, At: ts, For: back, From: incumbent, To: W, Both: true})
	}
	if how != "cut" {
		o.Faults = append(o.Faults, silence)
	}
	o.Faults = append(o.Faults, Fault{Kind: FromSpare, At: tf, For: restart, Node: spare})
	end := tf + restart + 12*lease
	if how == "off" {
		// Liveness: the replacement must take over once its peer dies.
		o.Faults = append(o.Faults, Fault{Kind: Crash, At: tf + restart + 6*lease, Node: peerOf(spare)})
		end = tf + restart + 14*lease
	}
	o.Until = max(end, ts+back+12*lease)
	return o, spare, incumbent, tf
}

// withSpare makes a spare of c's name from at: realBox, the original (down
// when its copy starts; back up, a boot first heard after the copy's:
// held, then a spare), else the copy (booted beside the settled original).
func withSpare(r *rand.Rand, realBox bool, c Addr, at time.Duration) (faults []Fault, spare, incumbent Addr) {
	orig := original(c)
	if realBox {
		return []Fault{
			{Kind: Crash, At: at, For: 2*lease + time.Duration(r.Int64N(int64(2*lease))), Node: orig},
			{Kind: Clone, At: at + 50*ms, Node: c},
		}, orig, c
	}
	return []Fault{{Kind: Clone, At: at, Node: c}}, c, orig
}

// fromSpareSplit is #1135's return-into-split shape (returnIntoSplit) for
// --from-spare: the incumbent silenced (cut from every member, paused,
// frozen, or switched off) from ts, the command a lease and D on, the
// spare restarted, the incumbent back eight to ten leases after the
// restart, then, after a touch of under touchMax of full connectivity (or,
// pre, from two leases before it is back), a split for eight leases that
// puts the incumbent with one voter and the replacement with the other,
// the peer cut from the witness: no voter hears both.
func fromSpareSplit(seed uint64, realBox, mirror bool, how string, touchMax time.Duration, pre bool) (o Options, spare, incumbent Addr, tf time.Duration) {
	o = randomRun(seed, profClone)
	r := rand.New(rand.NewPCG(seed, 0x46535350))
	c := []Addr{CA, CB}[r.IntN(2)]
	at := 6*sec + time.Duration(r.Int64N(int64(4*lease)))
	o.Faults, spare, incumbent = withSpare(r, realBox, c, at)
	peer := peerOf(original(c))
	ts := at + 10*lease
	tf = ts + lease + lease/4 + lease/8 + time.Duration(r.Int64N(int64(2*lease)))
	restart := 100*ms + time.Duration(r.Int64N(int64(2*sec)))
	tR := tf + restart + 8*lease + time.Duration(r.Int64N(int64(2*lease)))
	switch how {
	case "power":
		o.Faults = append(o.Faults, Fault{Kind: Crash, At: ts, For: tR - ts, Node: incumbent})
	case "pause":
		o.Faults = append(o.Faults, Fault{Kind: Pause, At: ts, For: tR - ts, Node: incumbent})
	case "freeze":
		o.Faults = append(o.Faults, Fault{Kind: Freeze, At: ts, For: tR - ts, Node: incumbent})
	case "cut":
		for _, m := range []Addr{peer, W, spare} {
			o.Faults = append(o.Faults, Fault{Kind: Cut, At: ts, For: tR - ts, From: incumbent, To: m, Both: true})
		}
	}
	o.Faults = append(o.Faults, Fault{Kind: FromSpare, At: tf, For: restart, Node: spare})
	split := tR
	if touchMax > 0 {
		split += time.Duration(r.Int64N(int64(touchMax)))
	}
	if pre {
		split = tR - 2*lease
	}
	far, near := W, peer // the replacement with the witness, the incumbent with the peer
	if mirror {
		far, near = peer, W
	}
	o.Faults = append(o.Faults,
		Fault{Kind: Cut, At: split, For: 8 * lease, From: spare, To: near, Both: true},
		Fault{Kind: Cut, At: split, For: 8 * lease, From: peer, To: W, Both: true},
		Fault{Kind: Cut, At: split, For: 8 * lease, From: incumbent, To: far, Both: true})
	o.Until = split + 8*lease + 2*lease + bound + 6*lease
	return o, spare, incumbent, tf
}

// fromSpareTally classifies runs.
type fromSpareTally struct {
	runs, notSpare, refused, done, two, other, led, ledOf int
	firstOther, firstTwo                                  string
}

func (t *fromSpareTally) add(seed uint64, o Options, r *Record, spare Addr, tf time.Duration, how string) {
	t.runs++
	var rec *FromSpareRec
	for i := range r.FromSpares {
		rec = &r.FromSpares[i]
	}
	switch {
	case rec == nil || rec.Refused == "not a spare":
		t.notSpare++
	case rec.Refused != "":
		t.refused++
	default:
		t.done++
	}
	vs := Check(r, cloneChecks(o))
	if overlap(vs) {
		t.two++
		if t.firstTwo == "" {
			t.firstTwo = fmt.Sprintf("seed %d: %v", seed, vs[0])
		}
	} else if len(vs) > 0 {
		t.other++
		if t.firstOther == "" {
			t.firstOther = fmt.Sprintf("seed %d: %v", seed, vs[0])
		}
	}
	if how == "off" && rec != nil && rec.Refused == "" {
		t.ledOf++
		for _, g := range r.Gates() {
			if g.Node == spare && g.From > tf {
				t.led++
				break
			}
		}
	}
}

func (t *fromSpareTally) String() string {
	return fmt.Sprintf("runs=%d notSpare=%d refused=%d done=%d TWO=%d other=%d replacementLed=%d/%d %s %s",
		t.runs, t.notSpare, t.refused, t.done, t.two, t.other, t.led, t.ledOf, t.firstTwo, t.firstOther)
}

type fromSpareFam struct {
	name    string
	realBox bool
	how     string
	// split: fromSpareSplit, with mirror, touch and pre.
	split, mirror, pre bool
	touch              time.Duration
}

// residual: the stated #1029 residual of a copy booted beyond an existing
// split. An incumbent switched back on (a fresh boot, its old disk) right
// into the split, never heard beside the replacement, leads on its side:
// two leaders in every run until 3c-3c-1b (naut-lead: a MUST before
// experimental lifts). After a touch of full connectivity it is marked: 0.
func (f fromSpareFam) residual() bool { return f.split && f.how == "power" && f.touch == 0 }

func (f fromSpareFam) run(seed uint64) (Options, Addr, Addr, time.Duration) {
	if f.split {
		return fromSpareSplit(seed, f.realBox, f.mirror, f.how, f.touch, f.pre)
	}
	return fromSpareRun(seed, f.realBox, f.how)
}

func fromSpareFams() []fromSpareFam {
	var out []fromSpareFam
	for _, realBox := range []bool{true, false} {
		box := map[bool]string{true: "real box spare", false: "copy spare"}[realBox]
		for _, how := range fromSpareHow {
			out = append(out, fromSpareFam{name: box + ", incumbent " + how, realBox: realBox, how: how})
		}
		for _, how := range []string{"power", "cut", "pause", "freeze"} {
			for _, mirror := range []bool{false, true} {
				side := map[bool]string{false: "with the peer", true: "with the witness"}[mirror]
				for _, tc := range []struct {
					name  string
					touch time.Duration
					pre   bool
				}{{"at the heal", 0, false}, {"after a touch <lease/2", lease / 2, false}, {"after a touch <lease", lease, false}, {"into a split 2 leases old", 0, true}} {
					out = append(out, fromSpareFam{name: fmt.Sprintf("%s, incumbent %s, back %s %s", box, how, side, tc.name),
						realBox: realBox, how: how, split: true, mirror: mirror, touch: tc.touch, pre: tc.pre})
				}
			}
		}
	}
	return out
}

func runFromSpareFam(f fromSpareFam, n uint64) *fromSpareTally {
	var mu sync.Mutex
	t := &fromSpareTally{}
	seeds := make(chan uint64)
	var wg sync.WaitGroup
	for range runtime.GOMAXPROCS(0) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for seed := range seeds {
				o, spare, _, tf := f.run(seed)
				r := Run(o)
				mu.Lock()
				t.add(seed, o, r, spare, tf, f.how)
				mu.Unlock()
			}
		}()
	}
	for seed := uint64(1); seed <= n; seed++ {
		seeds <- seed
	}
	close(seeds)
	wg.Wait()
	return t
}

// TestMeasureFromSpare prints every family's table (the PR body's):
// QUORUM_MEASURE_FROMSPARE=<seeds per family>.
func TestMeasureFromSpare(t *testing.T) {
	n, _ := strconv.ParseUint(os.Getenv("QUORUM_MEASURE_FROMSPARE"), 10, 64)
	if n == 0 {
		t.Skip("set QUORUM_MEASURE_FROMSPARE=<seeds> to measure")
	}
	for _, f := range fromSpareFams() {
		t.Logf("%-86s %v", f.name, runFromSpareFam(f, n))
	}
}

// --from-spare never makes two leaders, whichever way the operator's
// "stopped" was wrong (#1029 3c-3c), but for the stated residual
// (fromSpareFam.residual): an incumbent cut off, paused or
// frozen returns with a boot the clear retired, and is marked before any
// grant; a hung one is still heard, so the command refuses; one switched back on returns as a fresh boot beside the
// replacement, and the two boots are marked as a copy's are. With the
// incumbent really off, the command goes through in most runs and the
// replacement takes over when its peer dies.
func TestFromSpareNeverMakesTwoLeaders(t *testing.T) {
	n := spareSeeds() / 2
	for _, f := range fromSpareFams() {
		if f.residual() {
			continue // measured by TestMeasureFromSpare; #1029 3c-3c-1b closes it
		}
		tl := runFromSpareFam(f, n)
		if tl.two > 0 || tl.other > 0 {
			t.Errorf("%s: %v", f.name, tl)
		}
		switch {
		case f.how == "unhealthy" && tl.done > 0:
			// A hung incumbent's voter and probes still run: it is heard,
			// so the command refuses (fail closed).
			t.Errorf("%s: the command went through beside a hung incumbent: %v", f.name, tl)
		case f.how != "unhealthy" && tl.done == 0:
			t.Errorf("%s: the command never went through: the family no longer reaches it (%v)", f.name, tl)
		}
		if f.how == "off" && tl.led < tl.ledOf {
			t.Errorf("%s: the replacement did not take over in every run: %v", f.name, tl)
		}
	}
}
