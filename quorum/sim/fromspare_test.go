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
	case "wipe":
		// Wiped while off (the alarm's own advice, or a lost disk): back
		// uncommissioned, a lookalike of the replacement (the stated
		// boundary, #1029 3c-3c-1b).
		o.Faults = append(o.Faults, Fault{Kind: Wipe, At: ts, For: tR - ts, Node: incumbent})
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
	// noLeaderAtEnd: runs with no leader in the last two leases.
	noLeaderAtEnd int
	// incompleteFam: only runs the command left INCOMPLETE (the peer
	// fenced, the witness not) are judged beyond two leaders; incomplete
	// counts them. In the others the command refused (the incumbent was
	// heard), its marks still hold it, and a peer that dies leaves no
	// leader until an operator clears them: #1135's cost, not the fence's.
	incompleteFam        bool
	incomplete           int
	firstOther, firstTwo string
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
	if t.incompleteFam && (rec == nil || !strings.HasPrefix(rec.Refused, "incomplete")) {
		if overlap(vs) {
			t.two++
			if t.firstTwo == "" {
				t.firstTwo = fmt.Sprintf("seed %d: %v", seed, vs[0])
			}
		}
		return
	}
	if t.incompleteFam {
		t.incomplete++
	}
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
	end := o.Until - 2*lease
	led := false
	for _, g := range r.Gates() {
		led = led || g.To > end
	}
	if !led {
		t.noLeaderAtEnd++
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
	return fmt.Sprintf("runs=%d notSpare=%d refused=%d done=%d incomplete=%d TWO=%d other=%d replacementLed=%d/%d noLeaderAtEnd=%d %s %s",
		t.runs, t.notSpare, t.refused, t.done, t.incomplete, t.two, t.other, t.led, t.ledOf, t.noLeaderAtEnd, t.firstTwo, t.firstOther)
}

type fromSpareFam struct {
	name    string
	realBox bool
	how     string
	// split: fromSpareSplit, with mirror, touch and pre.
	split, mirror, pre bool
	touch              time.Duration
	// voterRestart: the peer, then the witness, restart between the
	// command and the incumbent's return (a fence is durable).
	voterRestart bool
	// incomplete: the witness's fence fails (FromSpareWitnessUnfenced):
	// the spare stays one, the peer fenced; the incumbent, switched off,
	// comes back on its own disk, the pair's member still, and the peer
	// dies at the end (#1029 3c-3c-1b, naut-lead's ruling C: a stated
	// cost).
	incomplete bool
	// later: a member's file lost after the command, before the old box
	// is back (#1164 review, B1): "peer" wiped, or "witness, peer" (the
	// witness rebuilt, then the peer wiped): only a voter that lost its
	// state learns the fence back (naut-lead's ruling (ii)).
	later string
	// moving: the old box wiped while off comes back reaching only the
	// witness, then the peer only, the replacement with the witness: a
	// split that moves (#1164 re-check, N1). A fenced voter still hears an
	// uncommissioned lookalike's boot, so the witness marks the name.
	moving bool
	// pending: the incumbent lost its file shortly before it was
	// switched off, and died re-adopting, its proposal durable, the link
	// to its peer lossy (#1164 review, B3).
	pending bool
}

func (f fromSpareFam) run(seed uint64) (Options, Addr, Addr, time.Duration) {
	var o Options
	var spare, inc Addr
	var tf time.Duration
	if f.split {
		o, spare, inc, tf = fromSpareSplit(seed, f.realBox, f.mirror, f.how, f.touch, f.pre)
	} else {
		o, spare, inc, tf = fromSpareRun(seed, f.realBox, f.how)
	}
	if f.moving {
		// Fable's N1 schedule: the family's own split is replaced.
		peer := peerOf(original(spare))
		var tR time.Duration
		var fs []Fault
		for _, x := range o.Faults {
			if x.Kind == Wipe && x.Node == inc {
				tR = x.At + x.For
			}
			if x.Kind == Cut && tR != 0 && x.At >= tR {
				continue
			}
			fs = append(fs, x)
		}
		gap := 4 * lease
		o.Faults = append(fs,
			Fault{Kind: Cut, At: tR - lease, For: lease + gap, From: inc, To: peer, Both: true}, // the old box: the witness only
			Fault{Kind: Cut, At: tR + lease, For: 12 * lease, From: spare, To: peer, Both: true},
			Fault{Kind: Cut, At: tR + lease, For: 12 * lease, From: peer, To: W, Both: true},
			Fault{Kind: Cut, At: tR + gap, For: 9 * lease, From: inc, To: W, Both: true}, // then the peer only
			Fault{Kind: Cut, At: tR - lease, For: 14 * lease, From: inc, To: spare, Both: true})
		o.Until = tR + 21*lease
	}
	for i := range o.Faults {
		if o.Faults[i].Kind != FromSpare {
			continue
		}
		fs := o.Faults[i]
		peer := peerOf(original(spare))
		switch f.later {
		case "peer":
			o.Faults = append(o.Faults, Fault{Kind: Wipe, At: tf + fs.For + 3*lease, For: 300 * ms, Node: peer})
		case "witness, peer":
			o.Faults = append(o.Faults,
				Fault{Kind: Wipe, At: tf + fs.For + 2*lease, For: 500 * ms, Node: W},
				Fault{Kind: Wipe, At: tf + fs.For + 6*lease, For: 300 * ms, Node: peer})
		}
		if f.pending {
			var ts time.Duration
			for _, x := range o.Faults {
				if x.Kind == Crash && x.Node == inc && x.At < tf {
					ts = x.At
				}
			}
			r := rand.New(rand.NewPCG(seed, 0x5252))
			wipeAt := ts - lease/4 - time.Duration(r.Int64N(int64(lease)))
			o.Faults = append(o.Faults,
				Fault{Kind: Wipe, At: wipeAt, For: 50 * ms, Node: inc},
				Fault{Kind: Loss, At: wipeAt, For: ts - wipeAt, From: inc, To: peer, Value: 60})
		}
		if f.incomplete {
			o.Faults[i].Value = FromSpareWitnessUnfenced
			// The peer dies at the end: the incumbent, fenced at the peer
			// only, leads with the witness (the stated cost).
			o.Faults = append(o.Faults, Fault{Kind: Crash, At: o.Until, Node: peerOf(original(spare))})
			o.Until += 8 * lease
		}
		if f.voterRestart {
			// Staggered: the pair keeps two voters up.
			at := tf + fs.For + 2*lease
			o.Faults = append(o.Faults,
				Fault{Kind: Crash, At: at, For: 300 * ms, Node: peerOf(original(spare))},
				Fault{Kind: Crash, At: at + 2*lease, For: 300 * ms, Node: W})
		}
		break
	}
	return o, spare, inc, tf
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
	// #1029 3c-3c-1b: a fence is durable (the voters restart before the
	// old box returns into the split), and an incomplete command (the
	// witness unfenced) with the incumbent switched off and on again.
	for _, realBox := range []bool{true, false} {
		box := map[bool]string{true: "real box spare", false: "copy spare"}[realBox]
		for _, mirror := range []bool{false, true} {
			side := map[bool]string{false: "with the peer", true: "with the witness"}[mirror]
			out = append(out, fromSpareFam{name: box + ", incumbent power, back " + side + " at the heal, the voters restarted first",
				realBox: realBox, how: "power", split: true, mirror: mirror, voterRestart: true})
		}
		out = append(out, fromSpareFam{name: box + ", incomplete (the witness unfenced), incumbent power", realBox: realBox, how: "power", incomplete: true})
		// #1164 review: a member's file lost after the command (B1, and
		// the witness rebuilt then the peer), the incumbent dead
		// re-adopting (B3), and the stated boundary: the old box wiped,
		// back into the split.
		for _, mirror := range []bool{false, true} {
			side := map[bool]string{false: "with the peer", true: "with the witness"}[mirror]
			for _, later := range []string{"peer", "witness, peer"} {
				out = append(out, fromSpareFam{name: box + ", incumbent power, back " + side + " at the heal, then " + later + " wiped first",
					realBox: realBox, how: "power", split: true, mirror: mirror, later: later})
			}
			out = append(out, fromSpareFam{name: box + ", incumbent wiped re-adopting then off, back " + side + " at the heal",
				realBox: realBox, how: "power", split: true, mirror: mirror, pending: true})
			if !mirror {
				out = append(out, fromSpareFam{name: box + ", incumbent wiped while off, back to the witness only, then the peer only",
					realBox: realBox, how: "wipe", split: true, moving: true})
			}
			for _, pre := range []bool{false, true} {
				at := map[bool]string{false: "at the heal", true: "into a split 2 leases old"}[pre]
				out = append(out, fromSpareFam{name: box + ", incumbent wiped while off, back " + side + " " + at,
					realBox: realBox, how: "wipe", split: true, mirror: mirror, pre: pre})
			}
		}
	}
	return out
}

func runFromSpareFam(f fromSpareFam, n uint64) *fromSpareTally {
	var mu sync.Mutex
	t := &fromSpareTally{incompleteFam: f.incomplete}
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
// "stopped" was wrong (#1029 3c-3c): an incumbent cut off, paused or
// frozen returns with a boot the clear retired, and is marked before any
// grant; a hung one is still heard, so the command refuses; one switched
// back on returns on its old disk, below the fence the command set at the
// peer and the witness, and is refused at any term, beside the
// replacement or into a split where no voter hears both (#1029 3c-3c-1b;
// the same term: Fable's note on #1157). With the incumbent really off,
// the command goes through in most runs and the replacement takes over
// when its peer dies.
func TestFromSpareNeverMakesTwoLeaders(t *testing.T) {
	n := spareSeeds() / 2
	for _, f := range fromSpareFams() {
		tl := runFromSpareFam(f, n)
		if tl.two > 0 || tl.other > 0 {
			t.Errorf("%s: %v", f.name, tl)
		}
		switch {
		case f.incomplete:
			// The stated cost (naut-lead's ruling C): the command ends
			// INCOMPLETE, the spare stays one, and the incumbent, fenced
			// at the peer only, leads with the witness once the peer dies.
			if tl.done > 0 || tl.incomplete*2 < tl.runs || tl.noLeaderAtEnd > 0 {
				t.Errorf("%s: %v", f.name, tl)
			}
			continue
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
