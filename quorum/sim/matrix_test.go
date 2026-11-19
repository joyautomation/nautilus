package sim

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// link is one vote link, between two of the three voters.
type link struct{ x, y Addr }

var voteLinks = []link{{A, B}, {A, W}, {B, W}}

func (l link) String() string { return l.x.String() + l.y.String() }

// cutMode is how a link is cut: both ways, or one way in either direction.
type cutMode int

const (
	both cutMode = iota
	xToY         // only x→y dropped
	yToX         // only y→x dropped
)

func (l link) cut(m cutMode, at time.Duration) Fault {
	switch m {
	case xToY:
		return Fault{Kind: Cut, At: at, From: l.x, To: l.y}
	case yToX:
		return Fault{Kind: Cut, At: at, From: l.y, To: l.x}
	}
	return Fault{Kind: Cut, At: at, From: l.x, To: l.y, Both: true}
}

func (l link) mode(m cutMode) string {
	switch m {
	case xToY:
		return l.x.String() + ">" + l.y.String()
	case yToX:
		return l.y.String() + ">" + l.x.String()
	}
	return l.String()
}

// matrixCase is one case of the exhaustive matrix (test plan §3.1).
type matrixCase struct {
	x, y   link
	mx, my cutMode
	yFirst bool // heal Y before X
	phase  int  // the cut lands k·lease/8 into the renewal cycle
	first  Addr // the first leader
	name   string
}

func matrixCases() []matrixCase {
	var cs []matrixCase
	for _, x := range voteLinks {
		for _, y := range voteLinks {
			if x == y {
				continue
			}
			for _, mx := range []cutMode{both, xToY, yToX} {
				for _, my := range []cutMode{both, xToY, yToX} {
					for _, yFirst := range []bool{false, true} {
						for phase := range 8 {
							for _, first := range []Addr{A, B} {
								c := matrixCase{x: x, y: y, mx: mx, my: my, yFirst: yFirst, phase: phase, first: first}
								heal := "XY"
								if yFirst {
									heal = "YX"
								}
								c.name = fmt.Sprintf("cut-%s_cut-%s_heal-%s_phase%d_lead%v", x.mode(mx), y.mode(my), heal, phase, first)
								cs = append(cs, c)
							}
						}
					}
				}
			}
		}
	}
	return cs
}

const stage = 4 * lease // long enough for any takeover the rule calls for

// The matrix timeline: the first leader is settled by 8 s; X is cut at
// 8 s + phase·lease/8, Y one stage later; the heals follow a stage apart;
// the run settles for two more stages.
func (c matrixCase) options() (Options, [5]time.Duration) {
	o := Options{Seed: 1, Delay: ms, Jitter: ms / 2, Fsync: ms}
	// The other controller starts once the first has won (by 2.75 s: the
	// restart wait, then a jittered retry): a smaller head start can lose
	// to the jitter.
	if c.first == A {
		o.Start = [3]time.Duration{0, 4000 * ms, 0}
	} else {
		o.Start = [3]time.Duration{4000 * ms, 0, 0}
	}
	t0 := 8*sec + time.Duration(c.phase)*lease/8
	ev := [5]time.Duration{t0, t0 + stage, t0 + 2*stage, t0 + 3*stage, t0 + 5*stage}
	fx, fy := c.x.cut(c.mx, ev[0]), c.y.cut(c.my, ev[1])
	fx.For, fy.For = ev[2]-ev[0], ev[3]-ev[1]
	if c.yFirst {
		fx.For, fy.For = ev[3]-ev[0], ev[2]-ev[1]
	}
	o.Faults = []Fault{fx, fy}
	o.Until = ev[4]
	return o, ev
}

// up reports whether x→y delivers at a stage, given which cuts are on.
func up(x, y Addr, cuts []Fault) bool {
	for _, f := range cuts {
		if (f.From == x && f.To == y) || (f.Both && f.From == y && f.To == x) {
			return false
		}
	}
	return true
}

// expected is the connectivity rule, independent of the protocol code:
// the leader P stays while it has a both-ways link to another voter; else
// the other controller O leads if it has a both-ways link to the witness.
// With at most two of three links cut, one link is always fully up, so
// one of the two always holds.
func expected(p Addr, cuts []Fault) Addr {
	bi := func(x, y Addr) bool { return up(x, y, cuts) && up(y, x, cuts) }
	o := peerOf(p)
	if bi(p, o) || bi(p, W) {
		return p
	}
	if bi(o, W) {
		return o
	}
	panic("matrix: no leader by the rule; a case has three links cut")
}

func holderAt(gates []Interval, t time.Duration) (Addr, bool) {
	for _, iv := range gates {
		if iv.From <= t && t < iv.To {
			return iv.Node, true
		}
	}
	return 0, false
}

// runMatrixCase checks one case: at the end of each stage the leader the
// rule expects holds the gate; the leader changes exactly as often as the
// rule says (I7); the safety oracles hold throughout; and after the heals
// all three voters back the leader with no term moving (steady).
func runMatrixCase(c matrixCase, trace bool) (vs []Violation, takeover time.Duration, rec *Record) {
	o, ev := c.options()
	o.Trace = trace
	rec = Run(o)
	gates := rec.Gates()
	stages := [][]Fault{{o.Faults[0]}, o.Faults, nil, nil}
	if c.yFirst {
		stages[2] = []Fault{o.Faults[0]}
	} else {
		stages[2] = []Fault{o.Faults[1]}
	}
	lead := c.first
	if h, ok := holderAt(gates, ev[0]-1); !ok || h != lead {
		return []Violation{{"matrix", ev[0], fmt.Sprintf("%v not leading before the first cut", lead)}}, 0, rec
	}
	changes := 0
	for i, cuts := range stages {
		want := expected(lead, cuts)
		if want != lead {
			changes++
		}
		end := ev[i+1] - 1
		if h, ok := holderAt(gates, end); !ok || h != want {
			got := "nobody"
			if ok {
				got = h.String()
			}
			vs = append(vs, Violation{"matrix", end, fmt.Sprintf("stage %d: %s leads, the connectivity rule says %v", i+1, got, want)})
		}
		if want != lead {
			for _, iv := range gates {
				if iv.Node == want && iv.From >= ev[i] {
					takeover = max(takeover, iv.From-ev[i])
					break
				}
			}
		}
		lead = want
	}
	vs = append(vs, Check(rec, Checks{MaxTakeovers: changes,
		SteadyFrom: ev[3] + stage, SteadyTo: ev[4], SteadyVoters: []Addr{A, B, W}})...)
	return vs, takeover, rec
}

// TestMatrix is the exhaustive partition matrix over the three vote links
// (test plan §3.1): every ordered pair of links, each cut both ways or one
// way in either direction, both heal orders, eight renewal phases, and A
// or B as the first leader: 6 × 9 × 2 × 8 × 2 = 1,728 cases.
// QUORUM_CASE=<name> runs one case with its trace.
func TestMatrix(t *testing.T) {
	cs := matrixCases()
	if len(cs) != 1728 {
		t.Fatalf("%d cases, want 1728", len(cs))
	}
	only := os.Getenv("QUORUM_CASE")
	var worst time.Duration
	var worstName string
	failed := 0
	for _, c := range cs {
		if only != "" && c.name != only {
			continue
		}
		vs, took, rec := runMatrixCase(c, only != "")
		if took > worst {
			worst, worstName = took, c.name
		}
		if len(vs) == 0 {
			continue
		}
		failed++
		if failed <= 10 {
			t.Errorf("%s: %v\n  repro: QUORUM_CASE=%s go test ./quorum/sim -run '^TestMatrix$' -v", c.name, vs[0], c.name)
		}
		if only != "" {
			t.Log("trace:\n" + strings.Join(rec.Trace, "\n"))
		}
	}
	if failed > 10 {
		t.Errorf("... %d failing cases in all", failed)
	}
	t.Logf("slowest takeover after its fault: %v (%s)", worst, worstName)
}
