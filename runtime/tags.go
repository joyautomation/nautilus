// Package runtime is the heart of nautilus: a fixed-interval scan loop that
// hosts an IEC 61131-3 program on a virtual machine, binding field I/O and
// operator values through a shared tag store — the same model a real PLC
// uses. Pure stdlib. Bring your own I/O driver, redundancy, and HMI.
package runtime

import (
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

// Set writes a Go value (float64/bool) to a tag, choosing the tag kind.
func (t *Tags) Set(name string, v any) {
	switch x := v.(type) {
	case bool:
		t.SetBool(name, x)
	case float64:
		t.SetReal(name, x)
	case int:
		t.SetReal(name, float64(x))
	}
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
	default:
		return nil
	}
}
