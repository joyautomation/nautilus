// Package runtime is the heart of nautilus: a fixed-interval scan loop that
// hosts an IEC 61131-3 program on a virtual machine, binding field I/O and
// operator values through a shared tag store — the same model a real PLC
// uses. Pure stdlib. Bring your own I/O driver, redundancy, and HMI.
package runtime

import (
	"fmt"
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
	// clock backs NowMs. Nil = wall clock; a Runtime built with
	// Options.Clock sets it once at construction, before any scan runs.
	clock Clock
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

// NowMs is the millisecond base every IEC timer counts from (ir.Host). It
// follows the runtime's Clock so TON/TOF/TP elapse in virtual time under
// test; production leaves the clock nil and reads the wall.
func (t *Tags) NowMs() int64 {
	if t.clock != nil {
		return t.clock.Now().UnixMilli()
	}
	return time.Now().UnixMilli()
}

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
//
// A name containing a "." is a MEMBER address, never a new tag: it is
// resolved through SetPath, and a path that resolves to nothing is dropped
// rather than stored. That is the whole point — a flat assignment of
// "P101.Speed" used to CREATE a top-level tag by that literal name, which no
// program reads and which a Sparkplug edge would then publish as a bogus
// metric. Callers that need to know WHY a write went nowhere (the HTTP API)
// call SetPath and report its error; the runtime's own paths — seeding,
// the per-scan driver copy, a retained restore — go through setAny, which
// creates tags by their configured name and never guesses at members.
func (t *Tags) Set(name string, v any) {
	if _, isMap := v.(map[string]any); isMap || strings.Contains(name, ".") {
		_ = t.SetPath(name, v)
		return
	}
	t.setAny(name, v)
}

// setAny is Set without the member-path guard: the flat, tag-creating store
// write the runtime uses for values that come from configuration (seeds,
// declared inputs, retained state) rather than from an operator.
func (t *Tags) setAny(name string, v any) {
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

// SetPath writes an operator value to a tag, or to ONE MEMBER of a struct
// tag, addressed by a dotted path — the write-path counterpart of the dotted
// paths a test's `expect:` and the HMI's tag bindings already use:
//
//	SetPath("TempSP", 65.0)                                    // whole tag
//	SetPath("P101.Drive.Speed", 60.0)                          // one member
//	SetPath("P101", map[string]any{"Cmd": true, "Speed": 60})  // partial merge
//
// The whole struct is read, modified and written back under one lock, so a
// member write is atomic against the scan and against another writer: the
// store holds whole tags, and a partial write is not a thing it can do.
//
// A map merges: members it names are set, every other member keeps its
// CURRENT value (unlike `init:` seeding, which zero-fills what it omits —
// see ir.SetField). Leaves are coerced to the member's own type and never
// retype it. Errors name the tag and the member path:
//
//	undefined tag WEL15_SUP_015
//	tag WEL15_SUP_015: unknown member STRAT (did you mean START?)
//	tag WEL15_SUP_015.START: want BOOL, got a number
//	tag TempSP is a REAL, not a struct — it has no member Hi
//
// No path ever creates a tag: an unknown root is an error, which is what
// keeps a typo out of the tag store (and off the Sparkplug wire).
func (t *Tags) SetPath(path string, v any) error {
	if path == "" {
		return fmt.Errorf("no tag name")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	// A tag whose own NAME contains a dot wins over member resolution.
	// Nothing in nautilus creates one today, but a driver is free to name a
	// tag after the field symbol it was read from, and such a tag must stay
	// writable as a whole.
	if cur, ok := t.vals[path]; ok {
		return t.setFieldLocked(path, cur, nil, v)
	}
	root, rest, dotted := strings.Cut(path, ".")
	if !dotted {
		return &UndefinedTagError{root}
	}
	cur, ok := t.vals[root]
	if !ok {
		return &UndefinedTagError{root}
	}
	return t.setFieldLocked(root, cur, strings.Split(rest, "."), v)
}

// setFieldLocked applies one member write to a tag already read under the
// lock, and stores the result. Caller holds t.mu.
func (t *Tags) setFieldLocked(root string, cur ir.Value, path []string, v any) error {
	nv, err := ir.SetField(cur, path, v, "tag "+root)
	if err != nil {
		return err
	}
	t.vals[root] = nv
	return nil
}

// ReadPath resolves a tag or a member path — "Tag" or "Tag.Member.Sub" — the
// read counterpart of SetPath's address space, and the walk the acceptance
// harness's `expect:`/`given:` and ST expressions already do to reach a
// struct field like RTU9_WEL15_FIT_001.HH. As SetPath does, a tag whose own
// NAME contains a dot wins over member resolution; failing that, the first
// segment names the tag and every remaining segment steps into one struct
// field.
//
// A leaf value comes back as a plain Go scalar — bool, int64, float64, or
// string, the same collapse All() applies to a whole tag — so a caller
// compares against a literal without importing ir. TIME collapses to its
// int64 milliseconds like INT does; nothing here needs to tell them apart
// (see All's `plain`). A struct, array, or FB sub-tree comes back as
// ir.Value, since there is no lossless plain form for one and a caller that
// wants a single field deeper just extends the path.
//
// ok is false for an unknown tag, an unknown member at any step, or a step
// into a non-struct — never an error, because this is meant to be tried
// speculatively (is "X.Y" a tag or a field?) far more often than SetPath's
// descriptive errors are wanted. Goroutine-safe: one RLock for the whole
// walk, the same guarantee ReadGlobal gives a single tag.
func (t *Tags) ReadPath(path string) (any, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if v, ok := t.vals[path]; ok {
		return plainLeaf(v), true
	}
	root, rest, dotted := strings.Cut(path, ".")
	if !dotted {
		return nil, false
	}
	v, ok := t.vals[root]
	if !ok {
		return nil, false
	}
	for rest != "" {
		var field string
		field, rest, _ = strings.Cut(rest, ".")
		if v.Kind != ir.TypeStruct || v.Struct == nil {
			return nil, false
		}
		i, ok := v.Struct.FieldIndex[field]
		if !ok || i >= len(v.Fld) {
			return nil, false
		}
		v = v.Fld[i]
	}
	return plainLeaf(v), true
}

// plainLeaf collapses a scalar ir.Value to its plain Go form; a composite
// value (struct/array/FB) comes back as-is, since ReadPath's contract only
// promises a plain form for leaves.
func plainLeaf(v ir.Value) any {
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
		return v
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
