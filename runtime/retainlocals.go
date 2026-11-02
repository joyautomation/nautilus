package runtime

import (
	"fmt"
	"strings"

	"github.com/joyautomation/nautilus/lang/ir"
)

// RetainedLocals is the value of every VAR RETAIN variable the program
// declares at its top level, in the plain JSON form the retain store keeps
// (plainNumeric: an enumeration as its integer, a struct as a mapping, an
// array as a list). A function-block instance declared VAR RETAIN keeps
// its variables, the pins and the user-declared internals, the same way,
// as a mapping by name; a built-in's hidden state (the underscore-named
// internals of a TON) is not kept, so a retained timer restarts its timing.
// This is CODESYS's VAR RETAIN: run hours, a start count, an FB that
// totalises, survive a restart.
func (p *Program) RetainedLocals() map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out map[string]any
	for i, s := range p.prog.Slots {
		if !s.Retained || s.Kind != ir.VarLocal || s.Temp || i >= len(p.frame.Slots) {
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
		if v.FB == nil || v.FB.Def == nil {
			return nil, false
		}
		out := map[string]any{}
		for i, s := range v.FB.Def.AllSlots() {
			if i >= len(v.FB.Slots) || strings.HasPrefix(s.Name, "_") {
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
			if s.Retained && s.Kind == ir.VarLocal && !s.Temp && ir.SameName(s.Name, name) {
				i = j
				break
			}
		}
		if i < 0 || i >= len(p.frame.Slots) {
			continue
		}
		s := p.prog.Slots[i]
		nv, err := restoreLocal(p.frame.Slots[i], s.Type, raw, s.Name)
		if err != nil {
			errs = append(errs, fmt.Errorf("retained %s: %w", s.Name, err))
			continue
		}
		p.frame.Slots[i] = nv
	}
	return errs
}

// restoreLocal assigns raw onto cur, an FB instance variable by variable,
// anything else through ir.SetFieldTyped (each leaf coerced to the kind it
// has, so a DINT stays a DINT and an enumeration comes back named).
func restoreLocal(cur ir.Value, t *ir.Type, raw any, what string) (ir.Value, error) {
	if cur.Kind != ir.TypeFB {
		return ir.SetFieldTyped(cur, t, nil, raw, what)
	}
	m, ok := raw.(map[string]any)
	if !ok || cur.FB == nil || cur.FB.Def == nil {
		return cur, fmt.Errorf("%s: a function-block instance is kept as a mapping of its variables", what)
	}
	for i, s := range cur.FB.Def.AllSlots() {
		v, has := m[s.Name]
		if !has || i >= len(cur.FB.Slots) || strings.HasPrefix(s.Name, "_") {
			continue
		}
		nv, err := restoreLocal(cur.FB.Slots[i], s.Type, v, what+"."+s.Name)
		if err != nil {
			return cur, err
		}
		cur.FB.Slots[i] = nv
	}
	return cur, nil
}
