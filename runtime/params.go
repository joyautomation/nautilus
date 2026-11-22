package runtime

import (
	"math"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/lang/st"
	"github.com/joyautomation/nautilus/retain"
)

// Parameters (#718). A parameter is a scalar setpoint with a manifest
// init: the operator-tuned values of a plant. A retained value wins over
// the manifest's init on a restart, a takeover and a redeploy (so tuning
// survives a download, as retained values do on a PLC), which means the
// repo and the plant can disagree. This file keeps what is needed to say
// so: the init each value was written over (the base) and the state that
// follows from comparing value, init and base. Drift is reported, never
// resolved here.

// Parameter states.
const (
	// ParamInStep: the value is the manifest's init.
	ParamInStep = "in-step"
	// ParamPlantAhead: the plant's value differs and the repo has not moved
	// since they last agreed: an operator edit not yet in Git.
	ParamPlantAhead = "plant-ahead"
	// ParamRepoAhead: the repo's init moved and the plant still holds the
	// value the old init gave: a redeploy brought a new init that the
	// retained value is shadowing.
	ParamRepoAhead = "repo-ahead"
	// ParamConflict: both moved, to different values.
	ParamConflict = "conflict"
)

// Parameter is one row of the parameter listing.
type Parameter struct {
	Name  string   `json:"name"`
	Value any      `json:"value"`
	Init  any      `json:"init"`
	Base  any      `json:"base"`
	Unit  string   `json:"unit,omitempty"`
	Desc  string   `json:"desc,omitempty"`
	Min   *float64 `json:"min,omitempty"`
	Max   *float64 `json:"max,omitempty"`
	State string   `json:"state"`
	// OutOfRange: the value is outside min/max. An external write cannot do
	// it; the program's own write can (a range never clamps the logic), and
	// it is shown here so it is not invisible.
	OutOfRange bool `json:"outOfRange,omitempty"`
	// EditedBy and EditedAtMs say who last wrote the value from outside
	// the logic and when; empty for a value no recorded write produced
	// (set before attribution, or by a program).
	EditedBy   string `json:"editedBy,omitempty"`
	EditedAtMs int64  `json:"editedAtMs,omitempty"`
	// Unwritable is why a pending value is left as drift by the write-back
	// (set by the server from the controller's last attempt).
	Unwritable string `json:"unwritable,omitempty"`
	// Committed: the plant-ahead value is already in the repo (a write-back
	// commit made it), waiting for a redeploy to bring it into the running
	// manifest. It is not pending.
	Committed bool `json:"committed,omitempty"`
	// LastCommitted is the value a write-back commit last put in the repo,
	// while the running manifest's init has not caught up with it (a
	// redeploy does that). The repo holds it, whatever the plant does next.
	LastCommitted any `json:"lastCommitted,omitempty"`
}

// Pending is the parameters a write-back commit would write: plant-ahead
// and not already committed. Repo-ahead and conflict never are.
//
// A parameter tuned back to the running init after a commit is in step with
// the manifest that runs, yet the repo holds the committed value: that is
// pending too (the commit puts the init back).
func (p Parameter) Pending() bool {
	if p.State == ParamPlantAhead {
		return !p.Committed
	}
	return p.State == ParamInStep && p.LastCommitted != nil && !paramEq(p.LastCommitted, p.Value)
}

// paramBook holds the parameters' manifest inits and drift bases.
type paramBook struct {
	mu    sync.Mutex
	inits map[string]any // tag name (as declared) -> manifest init
	base  map[string]any // as of the last load or save
	// byKey maps ir.NameKey to the declared name, for the write hook.
	byKey     map[string]string
	edits     map[string]retain.ParamEdit
	committed map[string]any
	now       func() time.Time // tests set it
}

// noteWrite records an external write to a parameter. Called by
// Tags.Write under the store lock; it takes only the book's.
func (b *paramBook) noteWrite(path, by string, val any) {
	b.mu.Lock()
	defer b.mu.Unlock()
	name, ok := b.byKey[ir.NameKey(path)]
	if !ok {
		return
	}
	if by == "" {
		by = "external"
	}
	now := time.Now
	if b.now != nil {
		now = b.now
	}
	if b.edits == nil {
		b.edits = map[string]retain.ParamEdit{}
	}
	b.edits[name] = retain.ParamEdit{By: by, AtMs: now().UnixMilli(), Val: val}
}

// scalarParamType reports whether an IEC type name declared with `type:` is a
// plain number or BOOL: `type: REAL` on a setpoint is a parameter like the
// same setpoint with no type (it was silently left out, #718 demo). A UDT, a
// TIME, a STRING and an enumeration are not (their init reads back
// differently from what the manifest wrote).
func scalarParamType(name string) bool {
	t, err := st.ResolveTypeName(name, nil)
	if err != nil || t == nil || t.Enum != nil {
		return false
	}
	return t.Kind == ir.TypeBool || t.Kind == ir.TypeReal || t.Kind == ir.TypeInt
}

// initParams records each scalar setpoint's manifest init.
func (r *Runtime) initParams(defs []TagDef) {
	for _, d := range defs {
		if d.Role != RoleSetpoint || d.Name == "" || d.Init == nil {
			continue
		}
		if d.Type != "" && !scalarParamType(d.Type) {
			continue // a UDT (or a TIME, STRING, enumeration): not a parameter
		}
		switch d.Init.(type) {
		case float64, int, int64, bool:
		default:
			// A struct or array init is not a parameter, and neither is a
			// text init: a TIME written T#5s or an enumeration member name
			// reads back as a number, so a comparison would call it drift
			// for ever.
			continue
		}
		if r.retainStore != nil && !slices.Contains(r.retainTags, d.Name) {
			continue // not retained: it returns to its init on a restart, so it cannot drift
		}
		if r.params.inits == nil {
			r.params.inits = map[string]any{}
		}
		r.params.inits[d.Name] = d.Init
		if r.params.byKey == nil {
			r.params.byKey = map[string]string{}
		}
		r.params.byKey[ir.NameKey(d.Name)] = d.Name
	}
	if len(r.params.inits) > 0 {
		r.tags.paramNote = r.params.noteWrite
	}
}

func (b *paramBook) load(bases map[string]any, edits map[string]retain.ParamEdit, committed map[string]any) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.edits = make(map[string]retain.ParamEdit, len(edits))
	for k, v := range edits {
		b.edits[k] = v
	}
	b.committed = make(map[string]any, len(committed))
	for k, v := range committed {
		b.committed[k] = v
	}
	b.base = make(map[string]any, len(bases))
	for k, v := range bases {
		b.base[k] = v
	}
}

// programBy names the writer when the logic changed a parameter after an
// operator's edit: the commit then says who actually set the value.
const programBy = "program"

// reconcile is the edit record for name given the value it holds now. The
// caller holds b.mu. If the value is no longer the one the recorded write
// left, something else set it (the logic: every external write goes through
// noteWrite), so the record becomes the program's, as of now.
func (b *paramBook) reconcile(name string, e retain.ParamEdit, cur any) retain.ParamEdit {
	if e.Val == nil || e.By == programBy || paramEq(e.Val, cur) {
		return e
	}
	now := time.Now
	if b.now != nil {
		now = b.now
	}
	e = retain.ParamEdit{By: programBy, AtMs: now().UnixMilli(), Val: cur}
	b.edits[name] = e
	return e
}

// snapshot is what a save writes: bases, and the edit attribution and
// committed values of the parameters still off their init. A parameter in
// step has neither: its edit is history and its commit has landed.
func (b *paramBook) snapshot(values map[string]any) (bases map[string]any, edits map[string]retain.ParamEdit, committed map[string]any) {
	bases = b.bases(values)
	b.mu.Lock()
	defer b.mu.Unlock()
	for name, init := range b.inits {
		cur, ok := values[name]
		if !ok {
			continue
		}
		// Back at the init with nothing committed that differs from it:
		// the edit is history. A committed value the init has not caught up
		// with stays (the repo holds it) until the init does.
		c, hasC := b.committed[name]
		if hasC && paramEq(c, init) {
			delete(b.committed, name)
			hasC = false
		}
		if paramEq(cur, init) && !hasC {
			delete(b.edits, name)
		}
	}
	for name, e := range b.edits {
		if _, ok := b.inits[name]; !ok {
			delete(b.edits, name)
			continue
		}
		if cur, ok := values[name]; ok {
			e = b.reconcile(name, e, cur)
		}
		if edits == nil {
			edits = map[string]retain.ParamEdit{}
		}
		edits[name] = e
	}
	for name, v := range b.committed {
		if _, ok := b.inits[name]; !ok {
			delete(b.committed, name)
			continue
		}
		if committed == nil {
			committed = map[string]any{}
		}
		committed[name] = v
	}
	return bases, edits, committed
}

// bases is the base map a save writes: a parameter in step with its init
// takes the init as its base; otherwise its recorded base stands (the init
// it last agreed with), or, when none was recorded, the init (the plant
// is then ahead of an init it started from). It is nil when there are no
// parameters.
func (b *paramBook) bases(values map[string]any) map[string]any {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.inits) == 0 {
		return nil
	}
	out := make(map[string]any, len(b.inits))
	for name, init := range b.inits {
		cur, ok := values[name]
		if !ok {
			continue // not retained (no retain store): nothing to base
		}
		base, had := b.base[name]
		if !had || paramEq(cur, init) {
			base = init
		}
		out[name] = base
	}
	b.base = out
	return out
}

// paramEq compares two scalar values: numbers by value, others as is.
func paramEq(a, b any) bool {
	fa, oka := paramNum(a)
	fb, okb := paramNum(b)
	if oka && okb {
		return fa == fb || (math.IsNaN(fa) && math.IsNaN(fb))
	}
	return a == b
}

func paramNum(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	}
	return 0, false
}

// Parameters lists the parameters with their drift state, sorted by name.
// The value is the live one (a forced value is not shown as the setpoint's).
func (r *Runtime) Parameters() []Parameter {
	r.params.mu.Lock()
	inits := make(map[string]any, len(r.params.inits))
	for k, v := range r.params.inits {
		inits[k] = v
	}
	base := make(map[string]any, len(r.params.base))
	for k, v := range r.params.base {
		base[k] = v
	}
	edits := make(map[string]retain.ParamEdit, len(r.params.edits))
	for k, v := range r.params.edits {
		edits[k] = v
	}
	committed := make(map[string]any, len(r.params.committed))
	for k, v := range r.params.committed {
		committed[k] = v
	}
	r.params.mu.Unlock()
	out := make([]Parameter, 0, len(inits))
	for name, init := range inits {
		v, err := r.tags.readActual(name)
		if err != nil {
			continue
		}
		cur := plainNumeric(v)
		b, had := base[name]
		if !had || paramEq(cur, init) {
			b = init
		}
		p := Parameter{Name: name, Value: cur, Init: init, Base: b, State: paramState(cur, init, b)}
		if m, ok := r.meta[name]; ok {
			p.Unit, p.Desc = m.Unit, m.Desc
		}
		c, hasC := committed[name]
		if hasC && !paramEq(c, init) {
			p.LastCommitted = c
		}
		e, ok := edits[name]
		if ok {
			// Not under the book's lock while reading the tag: the write
			// hook takes the store's lock, then the book's.
			r.params.mu.Lock()
			e = r.params.reconcile(name, e, cur)
			r.params.mu.Unlock()
		}
		if ok && (p.State != ParamInStep || p.LastCommitted != nil) {
			p.EditedBy, p.EditedAtMs = e.By, e.AtMs
		}
		if hasC && p.State == ParamPlantAhead && paramEq(c, cur) {
			p.Committed = true
		}
		if rg, ok := r.tags.Range(name); ok {
			p.Min, p.Max = rg.Min, rg.Max
			if f, ok := paramNum(cur); ok {
				p.OutOfRange = (rg.Min != nil && f < *rg.Min) || (rg.Max != nil && f > *rg.Max)
			}
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func paramState(cur, init, base any) string {
	switch {
	case paramEq(cur, init):
		return ParamInStep
	case paramEq(init, base):
		return ParamPlantAhead
	case paramEq(cur, base):
		return ParamRepoAhead
	default:
		return ParamConflict
	}
}

// PendingParameters are the parameters a write-back commit would write,
// sorted by name. The set is derived, not queued: a restart or a takeover
// finds the same one.
func (r *Runtime) PendingParameters() []Parameter {
	var out []Parameter
	for _, p := range r.Parameters() {
		if p.Pending() {
			out = append(out, p)
		}
	}
	return out
}

// MarkParametersCommitted records that a commit put these values in the
// repo, so they are not committed again while the running manifest still
// holds the old init. It is saved with the retained state.
func (r *Runtime) MarkParametersCommitted(values map[string]any) {
	r.params.mu.Lock()
	defer r.params.mu.Unlock()
	if r.params.committed == nil {
		r.params.committed = map[string]any{}
	}
	for k, v := range values {
		r.params.committed[k] = v
	}
}

// ParamValuesEqual compares two parameter values as the drift state does:
// numbers by value.
func ParamValuesEqual(a, b any) bool { return paramEq(a, b) }
