// Package runtime is the heart of nautilus: a fixed-interval scan loop that
// hosts an IEC 61131-3 program on a virtual machine, binding field I/O and
// operator values through a shared tag store — the same model a real PLC
// uses. Pure stdlib. Bring your own I/O driver, redundancy, and HMI.
package runtime

import (
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/joyautomation/nautilus/lang/ir"
)

// Tags is the ir.Host for the program's virtual machine: every VAR_EXTERNAL
// reference resolves here by name. The runtime writes field inputs before
// each scan and reads outputs after; operator commands write between scans.
// Safe for concurrent use.
type Tags struct {
	mu   sync.RWMutex
	vals map[string]ir.Value
}

func NewTags() *Tags { return &Tags{vals: make(map[string]ir.Value)} }

func (t *Tags) ReadGlobal(name string) (ir.Value, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	v, ok := t.vals[name]
	if !ok {
		return ir.Value{}, &UndefinedTagError{name}
	}
	return v, nil
}

func (t *Tags) WriteGlobal(name string, v ir.Value) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.vals[name] = v
	return nil
}

func (t *Tags) NowMs() int64 { return time.Now().UnixMilli() }

// UndefinedTagError is returned when a program reads a tag that was never set.
type UndefinedTagError struct{ Name string }

func (e *UndefinedTagError) Error() string { return "undefined tag " + e.Name }

// Typed accessors (zero value if absent).

func (t *Tags) Real(name string) float64 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.vals[name].F
}

func (t *Tags) Bool(name string) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.vals[name].B
}

func (t *Tags) SetReal(name string, v float64) { _ = t.WriteGlobal(name, ir.RealVal(v)) }
func (t *Tags) SetBool(name string, v bool)    { _ = t.WriteGlobal(name, ir.BoolVal(v)) }

// Set writes a Go value to a tag, choosing the tag kind. Scalars accept
// bool/float64/int/int64/string; drivers with typed values (integer widths,
// UDT structs, arrays) pass an ir.Value directly and it is stored as-is.
func (t *Tags) Set(name string, v any) {
	switch x := v.(type) {
	case bool:
		t.SetBool(name, x)
	case float64:
		t.SetReal(name, x)
	case int:
		t.SetReal(name, float64(x))
	case int64:
		_ = t.WriteGlobal(name, ir.IntVal(x))
	case string:
		_ = t.WriteGlobal(name, ir.StringVal(x))
	case ir.Value:
		_ = t.WriteGlobal(name, x)
	}
}

// Snapshot returns a typed copy of every tag as ir.Value — for consumers
// that need the kind (e.g. the Sparkplug node's faithful datatype mapping),
// where All()'s plain-JSON collapse would lose int-vs-real.
func (t *Tags) Snapshot() map[string]ir.Value {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make(map[string]ir.Value, len(t.vals))
	for k, v := range t.vals {
		out[k] = v
	}
	return out
}

// All returns a plain-JSON snapshot of every tag — for an HMI's live watch.
func (t *Tags) All() map[string]any {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make(map[string]any, len(t.vals))
	for k, v := range t.vals {
		out[k] = plain(v)
	}
	return out
}

func plain(v ir.Value) any {
	switch v.Kind {
	case ir.TypeBool:
		return v.B
	case ir.TypeReal:
		return v.F
	case ir.TypeInt, ir.TypeTime:
		return v.I
	case ir.TypeString:
		return v.S
	case ir.TypeArray:
		out := make([]any, len(v.Arr))
		for i, e := range v.Arr {
			out[i] = plain(e)
		}
		return out
	case ir.TypeStruct:
		out := make(map[string]any, len(v.Fld))
		for i, f := range v.Fld {
			name := ""
			if v.Struct != nil && i < len(v.Struct.Fields) {
				name = v.Struct.Fields[i].Name
			}
			if name == "" {
				name = "_" + strconv.Itoa(i)
			}
			out[name] = plain(f)
		}
		return out
	case ir.TypeFB:
		// A function-block instance renders like a struct of its pins, so a
		// watch (editor inline values, HMI) can show t1.Q and t1.ET live.
		// Internals with the built-ins' underscore prefix stay hidden.
		if v.FB == nil || v.FB.Def == nil {
			return nil
		}
		slots := v.FB.Def.AllSlots()
		out := make(map[string]any, len(slots))
		for i, s := range slots {
			if i >= len(v.FB.Slots) || strings.HasPrefix(s.Name, "_") {
				continue
			}
			out[s.Name] = plain(v.FB.Slots[i])
		}
		return out
	default:
		return nil
	}
}
