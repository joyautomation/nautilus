package sparkplug

import (
	"math"
	"time"

	"github.com/joyautomation/nautilus/lang/ir"
)

// RBE is a report-by-exception rule for a publish class or a single tag —
// the Sparkplug analogue of a scan class. It decides, per value, whether a
// change is worth publishing.
//
//   - Deadband: publish only when a numeric value moves more than this in
//     absolute units. Zero means publish on any change.
//   - MinInterval: never publish more often than this, even on change
//     (rate limit / debounce). Zero disables.
//   - MaxInterval: publish at least this often even when unchanged
//     (heartbeat / keepalive). Zero disables.
type RBE struct {
	Deadband    float64
	MinInterval time.Duration
	MaxInterval time.Duration
	// Disable publishes every change unconditionally (deadband/min ignored),
	// for tags where every transition matters (alarms, counters).
	Disable bool
}

// rbeState is the per-metric memory RBE needs across evaluations.
type rbeState struct {
	last     ir.Value
	lastTime time.Time
	primed   bool
}

// shouldPublish applies the RBE rule to a new value at time now. Rules run in
// the same order tentacle uses so behavior matches a known-good implementation:
// disable → first-value → heartbeat → rate-limit → deadband → any-change.
func (r RBE) shouldPublish(st *rbeState, v ir.Value, now time.Time) bool {
	if r.Disable || !st.primed {
		return true
	}
	if r.MaxInterval > 0 && now.Sub(st.lastTime) >= r.MaxInterval {
		return true // heartbeat forces a publish regardless of change
	}
	if r.MinInterval > 0 && now.Sub(st.lastTime) < r.MinInterval {
		return false // rate-limited
	}
	if newF, oldF, ok := numeric(v, st.last); ok {
		if r.Deadband > 0 {
			return math.Abs(newF-oldF) > r.Deadband
		}
		return newF != oldF
	}
	return !valuesEqual(v, st.last)
}

// record updates the state after a publish.
func (st *rbeState) record(v ir.Value, now time.Time) {
	st.last, st.lastTime, st.primed = v, now, true
}

// numeric returns both values as float64 when both are numeric.
func numeric(a, b ir.Value) (float64, float64, bool) {
	af, ok := asFloat(a)
	if !ok {
		return 0, 0, false
	}
	bf, ok := asFloat(b)
	if !ok {
		return 0, 0, false
	}
	return af, bf, true
}

func asFloat(v ir.Value) (float64, bool) {
	switch v.Kind {
	case ir.TypeReal:
		return v.F, true
	case ir.TypeInt, ir.TypeTime:
		return float64(v.I), true
	}
	return 0, false
}

// valuesEqual compares two ir.Values for change detection (scalars and, for
// completeness, structs/arrays by deep comparison).
func valuesEqual(a, b ir.Value) bool {
	if a.Kind != b.Kind {
		return false
	}
	switch a.Kind {
	case ir.TypeBool:
		return a.B == b.B
	case ir.TypeReal:
		return a.F == b.F
	case ir.TypeInt, ir.TypeTime:
		return a.I == b.I
	case ir.TypeString:
		return a.S == b.S
	case ir.TypeArray:
		if len(a.Arr) != len(b.Arr) {
			return false
		}
		for i := range a.Arr {
			if !valuesEqual(a.Arr[i], b.Arr[i]) {
				return false
			}
		}
		return true
	case ir.TypeStruct:
		if len(a.Fld) != len(b.Fld) {
			return false
		}
		for i := range a.Fld {
			if !valuesEqual(a.Fld[i], b.Fld[i]) {
				return false
			}
		}
		return true
	}
	return false
}
