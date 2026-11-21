package runtime

import (
	"math"
	"sort"
	"sync"
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
}

// paramBook holds the parameters' manifest inits and drift bases.
type paramBook struct {
	mu    sync.Mutex
	inits map[string]any // tag name (as declared) -> manifest init
	base  map[string]any // as of the last load or save
}

// initParams records each scalar setpoint's manifest init.
func (r *Runtime) initParams(defs []TagDef) {
	for _, d := range defs {
		if d.Role != RoleSetpoint || d.Name == "" || d.Type != "" || d.Init == nil {
			continue
		}
		switch d.Init.(type) {
		case float64, int, int64, bool, string:
		default:
			continue // a struct or array init is not a parameter
		}
		if r.params.inits == nil {
			r.params.inits = map[string]any{}
		}
		r.params.inits[d.Name] = d.Init
	}
}

func (b *paramBook) load(bases map[string]any) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.base = make(map[string]any, len(bases))
	for k, v := range bases {
		b.base[k] = v
	}
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
