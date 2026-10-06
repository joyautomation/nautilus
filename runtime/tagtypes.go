package runtime

import "github.com/joyautomation/nautilus/lang/ir"

// TagTypes reports the declared type of every tag that has one, keyed by
// the spelling the store shows (the name a frame carries): the type a
// program binds it as (Globals), and a manifest tag's own `type:` (#246) — so a client can tell an enumerated value, which
// streams as its member's name, from a STRING. A tag whose type comes only
// from its seed is absent. Read at call time, so an online edit's new
// bindings show up on the next ask.
func (r *Runtime) TagTypes() map[string]*ir.Type {
	out := map[string]*ir.Type{}
	add := func(name string, t *ir.Type) {
		if t == nil {
			return
		}
		if k, ok := r.tags.Canonical(name); ok {
			name = k
		}
		out[name] = t
	}
	for name, t := range r.Globals() {
		add(name, t)
	}
	// The store's own table (Tags.TypeOf): every tag a program binds or
	// the manifest types, as of New. Globals above adds what an online
	// edit has bound since.
	for key, t := range r.tags.types {
		add(key, t)
	}
	return out
}

// LocalTypes reports the declared type of every program local the watch
// streams (AllLocals), merged the same way: tasks first, the main program
// last.
func (r *Runtime) LocalTypes() map[string]*ir.Type {
	out := map[string]*ir.Type{}
	for _, tr := range r.tasks {
		tr.prog.localTypes(out)
	}
	r.prog.localTypes(out)
	return out
}

// localTypes adds this program's locals (the slots Locals streams) to out.
func (p *Program) localTypes(out map[string]*ir.Type) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.prog == nil {
		return
	}
	for _, s := range p.prog.Slots {
		if s.Kind == ir.VarLocal && s.Type != nil {
			out[s.Name] = s.Type
		}
	}
}
