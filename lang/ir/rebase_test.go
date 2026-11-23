package ir_test

import (
	"math/rand/v2"
	"reflect"
	"slices"
	"testing"

	"github.com/joyautomation/nautilus/lang/ir"
)

// clockReaders are the built-ins whose OUTPUTS read the clock with no
// state kept: a hot takeover has nothing of theirs to move, and on another
// clock they answer another time, as they should.
var clockReaders = map[string]bool{"LOCAL_TIME": true}

// Every registered function block, stepped through one input sequence on
// two clocks d apart, ends with the same slots, except the ones it lists
// in TimeBase, which differ by exactly d (or are 0 on both). So a block
// that keeps the clock in a slot it does not list fails here, and a hot
// takeover's RebaseFrame moves every clock a block keeps (#710 S2).
func TestTimeBaseCoversEveryClockSlot(t *testing.T) {
	names := make([]string, 0, len(ir.FBs))
	for n := range ir.FBs {
		names = append(names, n)
	}
	slices.Sort(names)
	const d = 7_777_777
	for _, name := range names {
		def := ir.FBs[name]
		if clockReaders[name] {
			continue
		}
		t.Run(name, func(t *testing.T) {
			listed := map[int]bool{}
			for _, tb := range def.TimeBase {
				listed[tb.Index] = true
				if tb.Index < 0 || tb.Index >= len(def.AllSlots()) || def.Slot(tb.Index).Type.Kind != ir.TypeTime {
					t.Fatalf("TimeBase slot %d is not a TIME slot of %s", tb.Index, name)
				}
			}
			for seed := range uint64(20) {
				a, b := ir.NewFBInstance(def), ir.NewFBInstance(def)
				rng := rand.New(rand.NewPCG(seed, 1))
				now := int64(1_700_000_000_000)
				for step := range 60 {
					setInputs(def, rng, a, b)
					now += int64(rng.IntN(40))
					if step%13 == 0 {
						now += 500 // past most PTs: timers finish too
					}
					ea := def.Step(a, ir.FBStepCtx{NowMs: now})
					eb := def.Step(b, ir.FBStepCtx{NowMs: now + d})
					if (ea == nil) != (eb == nil) {
						t.Fatalf("seed %d step %d: errors differ on the two clocks: %v / %v", seed, step, ea, eb)
					}
					for i := range a.Slots {
						va, vb := a.Slots[i], b.Slots[i]
						if listed[i] {
							if !(va.I == 0 && vb.I == 0) && vb.I-va.I != d {
								t.Fatalf("seed %d step %d: TimeBase slot %s is %d and %d on clocks %d apart", seed, step, def.Slot(i).Name, va.I, vb.I, d)
							}
							continue
						}
						if !reflect.DeepEqual(plainValue(va), plainValue(vb)) {
							t.Fatalf("seed %d step %d: slot %s is %v on one clock and %v on a clock %d ms ahead: a slot that keeps the clock must be in TimeBase", seed, step, def.Slot(i).Name, plainValue(va), plainValue(vb), d)
						}
					}
				}
			}
		})
	}
}

// setInputs gives both instances the same random inputs.
func setInputs(def *ir.FBDef, rng *rand.Rand, a, b *ir.FBInstance) {
	for i, s := range def.Inputs {
		var v ir.Value
		switch s.Type.Kind {
		case ir.TypeBool:
			v = ir.BoolVal(rng.IntN(3) != 0)
		case ir.TypeTime:
			v = ir.TimeVal(int64(10 + rng.IntN(200)))
		case ir.TypeReal:
			v = ir.RealVal(rng.Float64() * 100)
		case ir.TypeInt:
			v = ir.IntVal(int64(rng.IntN(10)))
		default:
			continue
		}
		a.Slots[i], b.Slots[i] = v, v
	}
}

func plainValue(v ir.Value) any {
	f := &ir.Frame{Slots: []ir.Value{v}}
	return ir.FlattenInto(nil, f)
}

// A program's whole frame run on one clock, then rebased by d, is the frame
// the same program run on a clock d ahead holds, nested FB instances and
// arrays of timers included; and the two run on, each on its own clock,
// stay equal. That is a hot takeover's timer move (#710 S2).
func TestRebaseFrameMovesEveryTimer(t *testing.T) {
	p := lowerOne(t, imageSrc)
	const d = 5_000_000
	run := func(f *ir.Frame, h *imgHost, scans int) {
		t.Helper()
		for range scans {
			h.now += 40
			if err := ir.Run(p, f, h); err != nil {
				t.Fatal(err)
			}
		}
	}
	fa, fb := ir.NewFrame(p), ir.NewFrame(p)
	ha := &imgHost{g: map[string]ir.Value{"Run": ir.BoolVal(true)}, now: 1_000_000}
	hb := &imgHost{g: map[string]ir.Value{"Run": ir.BoolVal(true)}, now: 1_000_000 + d}
	run(fa, ha, 7)
	run(fb, hb, 7)
	if reflect.DeepEqual(ir.FlattenInto(nil, fa), ir.FlattenInto(nil, fb)) {
		t.Fatal("the two clocks left the same frame: no timer started, the test proves nothing")
	}
	ir.RebaseFrame(fa, d)
	if a, b := ir.FlattenInto(nil, fa), ir.FlattenInto(nil, fb); !reflect.DeepEqual(a, b) {
		t.Fatalf("rebased frame differs from the frame run on the later clock:\n got %v\nwant %v", a, b)
	}
	ha.now = hb.now
	run(fa, ha, 60) // past every PT: the timers finish on both
	run(fb, hb, 60)
	if a, b := ir.FlattenInto(nil, fa), ir.FlattenInto(nil, fb); !reflect.DeepEqual(a, b) {
		t.Fatal("after the rebase the two frames ran apart")
	}
}

// A Clear slot goes to 0 rather than moving, an idle (0) slot stays 0, and
// a slot not in TimeBase is left alone.
func TestRebaseClearAndIdle(t *testing.T) {
	pid := ir.NewFBInstance(ir.FBs["PID"])
	ton := ir.NewFBInstance(ir.FBs["TON"])
	idle := ir.NewFBInstance(ir.FBs["TON"])
	last := ir.FBs["PID"].TimeBase[0].Index
	pid.Slots[last] = ir.TimeVal(123)
	ton.Slots[4] = ir.TimeVal(1000)
	ton.Slots[1] = ir.TimeVal(300) // PT: a TIME, not a clock
	f := &ir.Frame{Slots: []ir.Value{{Kind: ir.TypeFB, FB: pid}, {Kind: ir.TypeFB, FB: ton}, {Kind: ir.TypeFB, FB: idle}}}
	ir.RebaseFrame(f, 50)
	if pid.Slots[last].I != 0 {
		t.Fatalf("PID last call %d after a rebase, want 0 (cleared: dt 0 on the first scan)", pid.Slots[last].I)
	}
	if ton.Slots[4].I != 1050 || ton.Slots[1].I != 300 {
		t.Fatalf("TON start %d PT %d, want 1050 and 300", ton.Slots[4].I, ton.Slots[1].I)
	}
	if idle.Slots[4].I != 0 {
		t.Fatalf("an idle TON's start moved to %d", idle.Slots[4].I)
	}
}

// One tag's value flattens and applies back into a fresh value of its type;
// leaves of another shape are refused.
func TestValueLeavesRoundTrip(t *testing.T) {
	p := lowerOne(t, imageSrc)
	f := runImage(t, p, 5)
	for i, s := range p.Slots {
		v := f.Slots[i]
		leaves := ir.FlattenValueInto(nil, &v)
		w := ir.Zero(s.Type)
		if err := ir.ApplyValueLeaves(&w, leaves); err != nil {
			t.Fatalf("%s: %v", s.Name, err)
		}
		if !reflect.DeepEqual(ir.FlattenValueInto(nil, &w), leaves) {
			t.Fatalf("%s: applied value differs", s.Name)
		}
		bad := ir.Zero(ir.BoolT)
		if s.Type.Kind != ir.TypeBool && ir.ApplyValueLeaves(&bad, leaves) == nil {
			t.Fatalf("%s: leaves applied to a BOOL", s.Name)
		}
	}
}

// The tags' layout changes with a name, an order, or a declared type.
func TestTagsLayout(t *testing.T) {
	base := ir.TagsLayout([]string{"A", "B"}, []*ir.Type{ir.IntT, ir.RealT})
	for name, other := range map[string]string{
		"renamed":     ir.TagsLayout([]string{"A", "C"}, []*ir.Type{ir.IntT, ir.RealT}),
		"reordered":   ir.TagsLayout([]string{"B", "A"}, []*ir.Type{ir.RealT, ir.IntT}),
		"retyped":     ir.TagsLayout([]string{"A", "B"}, []*ir.Type{ir.IntT, ir.BoolT}),
		"one less":    ir.TagsLayout([]string{"A"}, []*ir.Type{ir.IntT}),
		"one untyped": ir.TagsLayout([]string{"A", "B"}, []*ir.Type{ir.IntT, nil}),
	} {
		if other == base {
			t.Errorf("%s: same layout", name)
		}
	}
	if ir.TagsLayout([]string{"A", "B"}, []*ir.Type{ir.IntT, ir.RealT}) != base {
		t.Error("the same tags hash differently")
	}
}
