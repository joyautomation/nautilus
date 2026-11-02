package runtime

import (
	"fmt"
	"math"
	"strings"

	"github.com/joyautomation/nautilus/lang/ir"
)

// RetainedLocals is the value of every VAR RETAIN variable the program
// declares at its top level, in the plain JSON form the retain store keeps
// (plainNumeric: an enumeration as its integer, a struct as a mapping, an
// array as a list). This is CODESYS's VAR RETAIN: run hours, a start count,
// an FB that totalises, survive a restart.
//
// A function-block instance declared VAR RETAIN keeps every variable, as a
// mapping by name, its hidden ones too: a CTU's or an R_TRIG's edge memory
// must come back with its count, or a count input held TRUE through the
// restart counts again. An instance that keeps time against the controller's
// clock (a TON, TOF, TP or PID: see clockFB) is not kept at all and starts
// over, since a timestamp from before the restart means nothing after it.
// A CONSTANT is never kept: its value is its declaration's.
func (p *Program) RetainedLocals() map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out map[string]any
	for i, s := range p.prog.Slots {
		if !s.Retained || s.Kind != ir.VarLocal || s.Temp || s.Constant || i >= len(p.frame.Slots) {
			continue
		}
		v, ok := retainLocal(p.frame.Slots[i])
		if !ok {
			continue
		}
		if out == nil {
			out = map[string]any{}
		}
		out[s.Name] = v
	}
	return out
}

// retainLocal is a local's retained form, and whether it has one: a
// scalar, a struct or array of scalars, or an FB instance (its visible
// variables, recursively).
func retainLocal(v ir.Value) (any, bool) {
	if v.Kind == ir.TypeFB {
		if v.FB == nil || v.FB.Def == nil || clockFB(v.FB.Def) {
			return nil, false
		}
		out := map[string]any{}
		for i, s := range v.FB.Def.AllSlots() {
			if i >= len(v.FB.Slots) {
				continue
			}
			if e, ok := retainLocal(v.FB.Slots[i]); ok {
				out[s.Name] = e
			}
		}
		return out, true
	}
	switch v.Kind {
	case ir.TypeArray, ir.TypeStruct:
		if !retainable(v) {
			return nil, false
		}
	case ir.TypeBool, ir.TypeReal, ir.TypeInt, ir.TypeTime, ir.TypeString:
	default:
		return nil, false
	}
	return plainNumeric(v), true
}

// clockFB reports a built-in that keeps time against the controller's clock:
// a hidden TIME variable (TON's _started, TOF's _stopped, PID's _lastMs).
// User variables do not begin with an underscore, so a user FB's own TIME
// variable does not count.
func clockFB(d *ir.FBDef) bool {
	for _, s := range d.AllSlots() {
		if strings.HasPrefix(s.Name, "_") && s.Type != nil && s.Type.Kind == ir.TypeTime {
			return true
		}
	}
	return false
}

// RestoreLocals writes retained values back into the program's VAR RETAIN
// variables, by name: the load side of RetainedLocals, before the first
// scan. A name the program no longer declares RETAIN is skipped (the
// variable was renamed or is no longer retained); a value that no longer
// fits its variable's type is an error and the variable keeps its initial
// value, as a warm online edit resets a variable whose type changed.
func (p *Program) RestoreLocals(vals map[string]any) []error {
	p.mu.Lock()
	defer p.mu.Unlock()
	var errs []error
	for name, raw := range vals {
		i := -1
		for j, s := range p.prog.Slots {
			if s.Retained && s.Kind == ir.VarLocal && !s.Temp && !s.Constant && ir.SameName(s.Name, name) {
				i = j
				break
			}
		}
		if i < 0 || i >= len(p.frame.Slots) {
			continue
		}
		s := p.prog.Slots[i]
		nv, err := restoreLocal(p.frame.Slots[i], s.Type, raw, s.Name)
		errs = append(errs, err...)
		p.frame.Slots[i] = nv
	}
	return errs
}

// restoreLocal assigns raw onto cur, an FB instance variable by variable,
// anything else through ir.SetFieldTyped (each leaf coerced to the kind it
// has, so a DINT stays a DINT and an enumeration comes back named). It
// returns the value to keep: a variable that fits takes its retained value,
// one that does not keeps cur and is reported, so one bad member of an FB
// does not cost the others theirs. A REAL kept as null was NaN (JSON has
// none) and comes back NaN.
func restoreLocal(cur ir.Value, t *ir.Type, raw any, what string) (ir.Value, []error) {
	if cur.Kind != ir.TypeFB {
		if raw == nil && cur.Kind == ir.TypeReal {
			cur.F = math.NaN()
			return cur, nil
		}
		nv, err := ir.SetFieldTyped(cur, t, nil, raw, "retained "+what)
		if err != nil {
			return cur, []error{err}
		}
		return nv, nil
	}
	if cur.FB == nil || cur.FB.Def == nil || clockFB(cur.FB.Def) {
		return cur, nil // a clock-based instance was never kept: it starts over
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return cur, []error{fmt.Errorf("retained %s: a function-block instance is kept as a mapping of its variables", what)}
	}
	var errs []error
	for i, s := range cur.FB.Def.AllSlots() {
		if i >= len(cur.FB.Slots) {
			continue
		}
		v, has := m[s.Name]
		if !has {
			for k, kv := range m { // a member renamed only in case is the same member
				if ir.SameName(k, s.Name) {
					v, has = kv, true
					break
				}
			}
		}
		if !has {
			continue
		}
		nv, err := restoreLocal(cur.FB.Slots[i], s.Type, v, what+"."+s.Name)
		errs = append(errs, err...)
		cur.FB.Slots[i] = nv
	}
	return cur, errs
}
