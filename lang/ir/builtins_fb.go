package ir

import "fmt"

// FBs holds every built-in function block the IR knows about. The
// lowering pass consults it when a VAR declaration names an FB type
// that isn't a user UDT.
var FBs = map[string]*FBDef{}

// RegisterFB adds a built-in function block to the registry. Slot
// indices are computed once when the type is registered so call sites
// resolve named args without re-walking the slot list every scan.
func RegisterFB(def *FBDef) {
	def.SlotIndex = make(map[string]int, len(def.Inputs)+len(def.Outputs)+len(def.Internals))
	idx := 0
	for _, s := range def.Inputs {
		def.SlotIndex[s.Name] = idx
		idx++
	}
	for _, s := range def.Outputs {
		def.SlotIndex[s.Name] = idx
		idx++
	}
	for _, s := range def.Internals {
		def.SlotIndex[s.Name] = idx
		idx++
	}
	FBs[def.Name] = def
}

// LookupFBType returns the IR type for a built-in FB by name, or nil.
// The returned *Type wraps the def so multiple instances share the
// same FBDef pointer (which the runtime compares by identity).
func LookupFBType(name string) *Type {
	def, ok := FBs[name]
	if !ok {
		return nil
	}
	return &Type{Kind: TypeFB, FB: def}
}

func init() {
	registerTON()
	registerTOF()
	registerTP()
	registerEdgeTriggers()
	registerCounters()
	registerLatches()
}

// ─── TON: timer on-delay ────────────────────────────────────────────
//
// Inputs:  IN (BOOL), PT (TIME, ms)
// Outputs: Q (BOOL), ET (TIME, elapsed ms — clamped to PT)
// Internal: started (TIME, NowMs at rising edge of IN; 0 = idle)
//
// Q goes high once IN has been continuously true for PT.

func registerTON() {
	RegisterFB(&FBDef{
		Name:      "TON",
		Inputs:    []FBSlot{{"IN", BoolT}, {"PT", TimeT}},
		Outputs:   []FBSlot{{"Q", BoolT}, {"ET", TimeT}},
		Internals: []FBSlot{{"_started", TimeT}},
		Step: func(inst *FBInstance, ctx FBStepCtx) error {
			in := inst.Slots[0].B
			pt := inst.Slots[1].I
			startedIdx := 4
			if !in {
				inst.Slots[2] = BoolVal(false)
				inst.Slots[3] = TimeVal(0)
				inst.Slots[startedIdx] = TimeVal(0)
				return nil
			}
			started := inst.Slots[startedIdx].I
			if started == 0 {
				started = ctx.NowMs
				inst.Slots[startedIdx] = TimeVal(started)
			}
			elapsed := ctx.NowMs - started
			if elapsed >= pt {
				inst.Slots[2] = BoolVal(true)
				inst.Slots[3] = TimeVal(pt)
			} else {
				inst.Slots[2] = BoolVal(false)
				inst.Slots[3] = TimeVal(elapsed)
			}
			return nil
		},
	})
}

// ─── TOF: timer off-delay ───────────────────────────────────────────
//
// Q tracks IN on the rising edge but stays high for PT after IN goes
// false. _stopped records NowMs at the falling edge.

func registerTOF() {
	RegisterFB(&FBDef{
		Name:      "TOF",
		Inputs:    []FBSlot{{"IN", BoolT}, {"PT", TimeT}},
		Outputs:   []FBSlot{{"Q", BoolT}, {"ET", TimeT}},
		Internals: []FBSlot{{"_stopped", TimeT}, {"_prevIN", BoolT}},
		Step: func(inst *FBInstance, ctx FBStepCtx) error {
			in := inst.Slots[0].B
			pt := inst.Slots[1].I
			stoppedIdx, prevIdx := 4, 5
			prev := inst.Slots[prevIdx].B
			stopped := inst.Slots[stoppedIdx].I
			switch {
			case in:
				inst.Slots[2] = BoolVal(true)
				inst.Slots[3] = TimeVal(0)
				inst.Slots[stoppedIdx] = TimeVal(0)
			case prev && !in:
				stopped = ctx.NowMs
				inst.Slots[stoppedIdx] = TimeVal(stopped)
				inst.Slots[2] = BoolVal(true)
				inst.Slots[3] = TimeVal(0)
			default:
				if stopped == 0 {
					inst.Slots[2] = BoolVal(false)
					inst.Slots[3] = TimeVal(0)
				} else {
					elapsed := ctx.NowMs - stopped
					if elapsed >= pt {
						inst.Slots[2] = BoolVal(false)
						inst.Slots[3] = TimeVal(pt)
					} else {
						inst.Slots[2] = BoolVal(true)
						inst.Slots[3] = TimeVal(elapsed)
					}
				}
			}
			inst.Slots[prevIdx] = BoolVal(in)
			return nil
		},
	})
}

// ─── TP: pulse timer ────────────────────────────────────────────────
//
// On rising edge of IN, Q goes high for exactly PT (regardless of
// whether IN stays true). _started is the rising-edge timestamp.

func registerTP() {
	RegisterFB(&FBDef{
		Name:      "TP",
		Inputs:    []FBSlot{{"IN", BoolT}, {"PT", TimeT}},
		Outputs:   []FBSlot{{"Q", BoolT}, {"ET", TimeT}},
		Internals: []FBSlot{{"_started", TimeT}, {"_prevIN", BoolT}},
		Step: func(inst *FBInstance, ctx FBStepCtx) error {
			in := inst.Slots[0].B
			pt := inst.Slots[1].I
			startedIdx, prevIdx := 4, 5
			prev := inst.Slots[prevIdx].B
			started := inst.Slots[startedIdx].I
			if in && !prev {
				started = ctx.NowMs
				inst.Slots[startedIdx] = TimeVal(started)
			}
			if started > 0 {
				elapsed := ctx.NowMs - started
				if elapsed >= pt {
					inst.Slots[2] = BoolVal(false)
					inst.Slots[3] = TimeVal(pt)
					if !in {
						inst.Slots[startedIdx] = TimeVal(0)
					}
				} else {
					inst.Slots[2] = BoolVal(true)
					inst.Slots[3] = TimeVal(elapsed)
				}
			} else {
				inst.Slots[2] = BoolVal(false)
				inst.Slots[3] = TimeVal(0)
			}
			inst.Slots[prevIdx] = BoolVal(in)
			return nil
		},
	})
}

// ─── R_TRIG / F_TRIG: edge detectors ────────────────────────────────

func registerEdgeTriggers() {
	RegisterFB(&FBDef{
		Name:      "R_TRIG",
		Inputs:    []FBSlot{{"CLK", BoolT}},
		Outputs:   []FBSlot{{"Q", BoolT}},
		Internals: []FBSlot{{"_prev", BoolT}},
		Step: func(inst *FBInstance, _ FBStepCtx) error {
			clk := inst.Slots[0].B
			prev := inst.Slots[2].B
			inst.Slots[1] = BoolVal(clk && !prev)
			inst.Slots[2] = BoolVal(clk)
			return nil
		},
	})
	RegisterFB(&FBDef{
		Name:      "F_TRIG",
		Inputs:    []FBSlot{{"CLK", BoolT}},
		Outputs:   []FBSlot{{"Q", BoolT}},
		Internals: []FBSlot{{"_prev", BoolT}},
		Step: func(inst *FBInstance, _ FBStepCtx) error {
			clk := inst.Slots[0].B
			prev := inst.Slots[2].B
			inst.Slots[1] = BoolVal(prev && !clk)
			inst.Slots[2] = BoolVal(clk)
			return nil
		},
	})
}

// ─── CTU / CTD / CTUD: counters ─────────────────────────────────────

func registerCounters() {
	RegisterFB(&FBDef{
		Name:      "CTU",
		Inputs:    []FBSlot{{"CU", BoolT}, {"R", BoolT}, {"PV", IntT}},
		Outputs:   []FBSlot{{"Q", BoolT}, {"CV", IntT}},
		Internals: []FBSlot{{"_prevCU", BoolT}},
		Step: func(inst *FBInstance, _ FBStepCtx) error {
			cu := inst.Slots[0].B
			r := inst.Slots[1].B
			pv := inst.Slots[2].I
			cv := inst.Slots[4].I
			prev := inst.Slots[5].B
			switch {
			case r:
				cv = 0
			case cu && !prev:
				cv++
			}
			inst.Slots[3] = BoolVal(cv >= pv)
			inst.Slots[4] = IntVal(cv)
			inst.Slots[5] = BoolVal(cu)
			return nil
		},
	})
	RegisterFB(&FBDef{
		Name:      "CTD",
		Inputs:    []FBSlot{{"CD", BoolT}, {"LD", BoolT}, {"PV", IntT}},
		Outputs:   []FBSlot{{"Q", BoolT}, {"CV", IntT}},
		Internals: []FBSlot{{"_prevCD", BoolT}},
		Step: func(inst *FBInstance, _ FBStepCtx) error {
			cd := inst.Slots[0].B
			ld := inst.Slots[1].B
			pv := inst.Slots[2].I
			cv := inst.Slots[4].I
			prev := inst.Slots[5].B
			switch {
			case ld:
				cv = pv
			case cd && !prev:
				cv--
			}
			inst.Slots[3] = BoolVal(cv <= 0)
			inst.Slots[4] = IntVal(cv)
			inst.Slots[5] = BoolVal(cd)
			return nil
		},
	})
	RegisterFB(&FBDef{
		Name:      "CTUD",
		Inputs:    []FBSlot{{"CU", BoolT}, {"CD", BoolT}, {"R", BoolT}, {"LD", BoolT}, {"PV", IntT}},
		Outputs:   []FBSlot{{"QU", BoolT}, {"QD", BoolT}, {"CV", IntT}},
		Internals: []FBSlot{{"_prevCU", BoolT}, {"_prevCD", BoolT}},
		Step: func(inst *FBInstance, _ FBStepCtx) error {
			cu := inst.Slots[0].B
			cd := inst.Slots[1].B
			r := inst.Slots[2].B
			ld := inst.Slots[3].B
			pv := inst.Slots[4].I
			cv := inst.Slots[7].I
			prevCU := inst.Slots[8].B
			prevCD := inst.Slots[9].B
			switch {
			case r:
				cv = 0
			case ld:
				cv = pv
			default:
				if cu && !prevCU {
					cv++
				}
				if cd && !prevCD {
					cv--
				}
			}
			inst.Slots[5] = BoolVal(cv >= pv)
			inst.Slots[6] = BoolVal(cv <= 0)
			inst.Slots[7] = IntVal(cv)
			inst.Slots[8] = BoolVal(cu)
			inst.Slots[9] = BoolVal(cd)
			return nil
		},
	})
}

// ─── SR / RS: bistable latches ──────────────────────────────────────
//
// IEC 61131-3 names the dominant input with a trailing "1": SR is
// set-dominant (S1 wins when both asserted), RS is reset-dominant
// (R1 wins). Q1 is retained across scans via the output slot, so no
// extra internal is needed.

func registerLatches() {
	RegisterFB(&FBDef{
		Name:    "SR",
		Inputs:  []FBSlot{{"S1", BoolT}, {"R", BoolT}},
		Outputs: []FBSlot{{"Q1", BoolT}},
		Step: func(inst *FBInstance, _ FBStepCtx) error {
			s1 := inst.Slots[0].B
			r := inst.Slots[1].B
			q := inst.Slots[2].B
			inst.Slots[2] = BoolVal(s1 || (q && !r))
			return nil
		},
	})
	RegisterFB(&FBDef{
		Name:    "RS",
		Inputs:  []FBSlot{{"S", BoolT}, {"R1", BoolT}},
		Outputs: []FBSlot{{"Q1", BoolT}},
		Step: func(inst *FBInstance, _ FBStepCtx) error {
			s := inst.Slots[0].B
			r1 := inst.Slots[1].B
			q := inst.Slots[2].B
			inst.Slots[2] = BoolVal(!r1 && (s || q))
			return nil
		},
	})
}

// fbSlotByName returns the slot index for a named FB input/output/internal.
// Used by lowering to translate `t1(IN := …)` into an InputBinding.
func fbSlotByName(def *FBDef, name string) (int, error) {
	idx, ok := def.SlotIndex[name]
	if !ok {
		return 0, fmt.Errorf("FB %s has no field %q", def.Name, name)
	}
	return idx, nil
}
