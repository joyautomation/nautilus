package importer

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/joyautomation/nautilus/lang/l5x"
)

// A preset the logic computes (#700). Logix programs set a timer's or a
// counter's preset with a data instruction: MUL(DelaySec,1000,t.PRE) ahead
// of TON(t,?,?), mostly inside Add-On Instructions. In Logix the block runs
// on whatever .PRE holds; a nautilus call binds its preset pin every scan.
// So binding the export's static PRE there would quietly undo the logic's
// write. Instead, the preset gets a variable of its own, a DINT as .PRE is
// in Logix (milliseconds for a timer): seeded with the export's PRE,
// written where the logic writes .PRE, read where it reads it, and bound
// at the call (through TO_TIME for a timer):
//
//	XIC(go)MUL(sp,1000,t.PRE)TON(t,?,?);   (t: TIMER, PRE 5000)
//
//	t_PRE : DINT := 5000;
//	go { t_PRE := sp * 1000 } t:TON(PT := TO_TIME(t_PRE))
//
// The writer turns that binding back into a MOVE onto .PRE ahead of the
// block's rung, as it does for any variable preset, and the pre-pass folds
// that MOVE again (presetMs).
//
// Only an instance the routine runs itself gets the variable: a plain one
// (t) or an element at a literal index (Ts[3], the timer files of a
// converted SLC or PLC-5 program), which gets Ts_3_PRE. A preset written on
// an element at a computed index or on a member, or one a MOVE ahead of the
// block also sets (the writer's own preset fold), has no such form: the
// writing rung is refused by name rather than imported with the preset
// ignored.

// presetDest is the destination operand of a data instruction with an
// assignment form, or "".
func presetDest(in *l5x.Instr) string {
	a := in.Args
	switch strings.ToUpper(in.Mnemonic) {
	case "MOVE", "MOV", "NEG", "ABS", "SQR":
		if len(a) == 2 {
			return a[1]
		}
	case "ADD", "SUB", "MUL", "DIV", "MOD", "XPY":
		if len(a) == 3 {
			return a[2]
		}
	case "CPT", "CLR":
		if len(a) >= 1 {
			return a[0]
		}
	}
	return ""
}

// presetOperand splits t.PRE into its instance t (spaces dropped, so Ts[ 3]
// and Ts[3] are one instance); ok is false for any other operand.
func presetOperand(a string) (inst string, ok bool) {
	if len(a) <= 4 || !strings.EqualFold(a[len(a)-4:], ".PRE") {
		return "", false
	}
	return strings.ReplaceAll(a[:len(a)-4], " ", ""), true
}

var literalIndex = regexp.MustCompile(`^\[(\d+)\]$`)

// presetInst reports whether inst can carry a preset of its own: a plain
// instance, or an element of an array of blocks at a literal index.
func presetInst(inst string, f *found) bool {
	rest := strings.ReplaceAll(inst[len(baseOf(inst)):], " ", "")
	return rest == "" || f.dims != "" && literalIndex.MatchString(rest)
}

// scanPresetWrites finds the timers and counters whose preset the
// routine's logic writes, after the pre-pass has folded the writer's own
// MOVE-ahead-of-the-block presets, and declares each one's variable.
func (rt *routine) scanPresetWrites() {
	for _, pr := range rt.rungs {
		if pr.drop {
			continue
		}
		walkTerms(pr.terms, func(in *l5x.Instr) {
			if !assignInstr[strings.ToUpper(in.Mnemonic)] {
				return
			}
			inst, ok := presetOperand(presetDest(in))
			if !ok {
				return
			}
			f := rt.sc.lookup(baseOf(inst))
			if f == nil || f.alias != nil {
				return
			}
			up := strings.ToUpper(f.dtype)
			if up != "TIMER" && up != "COUNTER" {
				return
			}
			key, raw := presetKey(f, inst), strings.ToLower(inst)
			if _, done := rt.presetCalc[key]; done {
				return
			}
			if _, done := rt.presetBad[key]; done {
				return
			}
			bad := func(why string) {
				if rt.presetBad == nil {
					rt.presetBad = map[string]string{}
				}
				rt.presetBad[key] = why
			}
			_, lit := rt.presetLit[raw]
			_, v := rt.presetVar[raw]
			_, ms := rt.presetMs[raw]
			switch {
			case !presetInst(inst, f):
				bad("the logic writes the preset of " + inst + ", an element at a computed index or a member; a computed preset is carried only for a plain " + strings.ToLower(up) + " or an array element at a literal index")
			case lit || v || ms:
				bad("the logic writes the preset of " + inst + ", which a MOVE ahead of its block also sets")
			case rt.runsBlock(inst):
				name, of := f.name, f.name
				if m := literalIndex.FindStringSubmatch(inst[len(baseOf(inst)):]); m != nil {
					name += "_" + m[1]
					of += "[" + m[1] + "]"
				}
				name = rt.presetName(name)
				what := ""
				if up == "TIMER" {
					what = " (milliseconds)"
				}
				rt.p.declare(decl{Name: name, Type: "DINT", Init: strconv.FormatInt(presetOf(f, inst), 10), Section: "VAR",
					Comment: "the export's " + of + ".PRE" + what + "; the logic writes it, and " + of + " runs on it"})
				if rt.presetCalc == nil {
					rt.presetCalc = map[string]string{}
				}
				rt.presetCalc[key] = name
			}
		})
	}
}

// presetKey names an instance for presetCalc and presetBad: its nautilus
// name and its literal index, as block, ref and the writes all see it.
func presetKey(f *found, inst string) string {
	return strings.ToLower(f.name + strings.ReplaceAll(inst[len(baseOf(inst)):], " ", ""))
}

// presetCalcOf is the variable carrying inst's computed preset, if any.
func (rt *routine) presetCalcOf(inst string) (string, bool) {
	f := rt.sc.lookup(baseOf(inst))
	if f == nil {
		return "", false
	}
	v, ok := rt.presetCalc[presetKey(f, inst)]
	return v, ok
}

// runsBlock reports whether a TON/TOF/CTU/… of this routine runs inst.
func (rt *routine) runsBlock(inst string) bool {
	found := false
	for _, pr := range rt.rungs {
		walkTerms(pr.terms, func(in *l5x.Instr) {
			if blockInstr[strings.ToUpper(in.Mnemonic)] && len(in.Args) > 0 && strings.EqualFold(strings.ReplaceAll(in.Args[0], " ", ""), inst) {
				found = true
			}
		})
	}
	return found
}

// presetName is t_PRE, or that with a number when the name is taken.
func (rt *routine) presetName(inst string) string {
	base := inst + "_PRE"
	taken := func(n string) bool {
		if rt.sc.lookup(n) != nil {
			return true
		}
		for k := range rt.p.declIdx {
			if _, name, _ := strings.Cut(k, ":"); name == strings.ToLower(n) {
				return true
			}
		}
		return false
	}
	name := base
	for i := 2; taken(name); i++ {
		name = base + strconv.Itoa(i)
	}
	return name
}

// presetWrite refuses a data instruction's assignment to t.PRE by name
// when that preset has no carried form.
func (rt *routine) presetWrite(target string) *refusal {
	inst, ok := presetOperand(target)
	if !ok {
		return nil
	}
	f := rt.sc.lookup(baseOf(inst))
	if f == nil {
		return nil
	}
	if why, bad := rt.presetBad[presetKey(f, inst)]; bad {
		return refuse("preset", "%s", why)
	}
	return nil
}
