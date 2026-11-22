package runtime

import (
	"errors"
	"fmt"

	"github.com/joyautomation/nautilus/lang/ir"
)

// The write front door: every write from outside a program — an operator's
// POST /api/tags, an OPC UA client's Write — goes through Tags.Write, so
// one set of rules decides what lands (docs/design/opcua-server-writes.md
// §3.1):
//
//   - It resolves through the same paths SetPath does and never creates a
//     tag: an unknown root or member is refused (ErrUndefined for a root).
//   - It converts to the tag's declared type, and a value lands exactly as
//     sent or is refused: ir.ErrTypeMismatch for the wrong kind,
//     ir.ErrOutOfRange for an integer outside its declared width, a NaN or
//     infinite REAL, a fraction of a millisecond, or an integer that is no
//     member of an enumeration (#421: an INT used to take 70000).
//   - It refuses a forced address (ErrForced): the force would swallow the
//     write, and answering "done" for it would be a lie.
//   - It refuses a tag only the runtime sets (ErrReadOnly): a substitute
//     companion follows the substitute table, nothing else.
//
// A request's items apply in order under one store lock, so a scan's
// snapshot sees all of a request's accepted items or none: a Hi/Lo limit
// pair written together cannot straddle a scan. Each item stands alone —
// one refused item does not undo the others, which is how both HTTP (one
// item per request) and an OPC UA Write (a status per item) report.

// WriteOp is one item of a write: a tag or member path ("TempSP",
// "P101.Drive.Speed", "Line.Steps[2].Mode") and the value, as the protocol
// decoded it (bool, a number, a string, a map or list for a composite).
type WriteOp struct {
	Path  string
	Value any
}

var (
	// ErrUndefined: the path's tag does not exist.
	ErrUndefined = errors.New("undefined tag")
	// ErrForced: the address, a member of it or a tag containing it is
	// forced.
	ErrForced = errors.New("forced")
	// ErrReadOnly: the tag is one only the runtime sets (a substitute
	// companion, such as LS_1__Substituted).
	ErrReadOnly = errors.New("read-only")
	// ErrMoved: the tag no longer holds the value the caller expected.
	ErrMoved = errors.New("moved")
)

// MovedError says what a guarded write expected and what the tag holds.
type MovedError struct {
	Path     string
	Expected any
	Actual   any
}

func (e *MovedError) Error() string {
	return fmt.Sprintf("%s changed since you looked: you saw %v, it holds %v", e.Path, e.Expected, e.Actual)
}

// Is makes errors.Is(err, ErrMoved) true.
func (e *MovedError) Is(target error) bool { return target == ErrMoved }

// readOnlyLocked returns ErrReadOnly, named, when path is (in) a tag only
// the runtime sets. Caller holds t.mu.
func (t *Tags) readOnlyLocked(path string) error {
	tv, key, ok := t.lookupLocked(path)
	if !ok {
		root, _ := ir.SplitAddress(path)
		tv, key, ok = t.lookupLocked(root)
	}
	if ok && tv.owned {
		return fmt.Errorf("tag %s is set by the runtime from the substitute table: %w", key, ErrReadOnly)
	}
	return nil
}

// ForcedError names the force a write was refused for.
type ForcedError struct {
	Path  string // what was written
	Force string // the forced address that overlaps it
}

func (e *ForcedError) Error() string {
	return "tag " + e.Force + " is forced — change the force value or remove the force first"
}

// Is makes errors.Is(err, ErrForced) true.
func (e *ForcedError) Is(target error) bool { return target == ErrForced }

// Write applies ops in order under one lock and returns one error per op,
// nil where it landed. See the front door's rules above.
func (t *Tags) Write(ops []WriteOp) []error { return t.WriteAs(ops, "") }

// WriteAs is Write by a named principal (a journal user such as
// "user:akhan", or a system writer such as "sparkplug"), which a
// parameter's edit attribution records; "" is recorded as "external".
func (t *Tags) WriteAs(ops []WriteOp, by string) []error {
	errs := make([]error, len(ops))
	t.mu.Lock()
	defer t.mu.Unlock()
	for i, op := range ops {
		if f := t.forcedOverlapLocked(op.Path); f != "" {
			errs[i] = &ForcedError{Path: op.Path, Force: f}
			continue
		}
		if err := t.readOnlyLocked(op.Path); err != nil {
			errs[i] = err
			continue
		}
		if err := t.rangeCheck(op); err != nil {
			errs[i] = err
			continue
		}
		errs[i] = t.setPathLocked(op.Path, op.Value, true)
		if errs[i] == nil && t.paramNote != nil {
			// The value as the store now holds it, for the attribution to
			// tell later a program's write from this one.
			if tv, name, ok := t.lookupLocked(op.Path); ok {
				t.paramNote(op.Path, by, plainNumeric(t.liveLocked(name, tv)))
			}
		}
	}
	return errs
}

// SetOutcome is one op of WriteSet: what the tag held, what it holds (or,
// on a dry run, would hold) after, and the refusal if there was one.
type SetOutcome struct {
	Path      string
	From, To  any
	Unchanged bool // the write would not change the value; it is not made
	Err       error
}

// WriteSet applies ops all or nothing, under one hold of the store's lock:
// every op is checked first (a force, a read-only tag, a range, and the
// strict conversion to the tag's declared type, which is what a write
// answers 400 or 422 for), and if any is refused NOTHING is written and the
// refusals are in the outcomes. An op that would not change the value is
// reported Unchanged and not written, so it takes no attribution. dry
// checks and reports and writes nothing. From and To are read inside the
// lock, as the store holds them (To after conversion), so they cannot
// disagree with what the controller holds.
//
// Ops must name distinct whole scalar tags (a parameter): two members of one
// tag would both be planned from the pre-write value, and a composite value
// is not compared. The import route guarantees this.
func (t *Tags) WriteSet(ops []WriteOp, by string, dry bool) []SetOutcome {
	return t.WriteSetGuarded(ops, nil, by, dry)
}

// WriteSetGuarded is WriteSet with an expected value per tag (expect is keyed
// by ir.NameKey of the op's path; a tag with no entry is not guarded). A tag
// that no longer holds its expected value is refused with ErrMoved, which
// refuses the whole set, so a change made since the caller looked is never
// overwritten unseen (#718). The comparison is made inside the store's lock,
// in the same step as the write.
func (t *Tags) WriteSetGuarded(ops []WriteOp, expect map[string]any, by string, dry bool) []SetOutcome {
	out := make([]SetOutcome, len(ops))
	type plan struct {
		key string
		nv  ir.Value
	}
	plans := make([]plan, len(ops))
	t.mu.Lock()
	defer t.mu.Unlock()
	refused := false
	for i, op := range ops {
		out[i].Path = op.Path
		if cur, key, ok := t.lookupLocked(op.Path); ok {
			out[i].From = plainNumeric(t.liveLocked(key, cur))
		}
		if want, guarded := expect[ir.NameKey(op.Path)]; guarded && !paramEq(out[i].From, want) {
			out[i].Err = &MovedError{Path: op.Path, Expected: want, Actual: out[i].From}
		} else if f := t.forcedOverlapLocked(op.Path); f != "" {
			out[i].Err = &ForcedError{Path: op.Path, Force: f}
		} else if err := t.readOnlyLocked(op.Path); err != nil {
			out[i].Err = err
		} else if err := t.rangeCheck(op); err != nil {
			out[i].Err = err
		} else if key, nv, err := t.planPathLocked(op.Path, op.Value, true); err != nil {
			out[i].Err = err
		} else {
			plans[i] = plan{key, nv}
			out[i].To = plainNumeric(nv)
			out[i].Unchanged = paramEq(out[i].From, out[i].To)
		}
		if out[i].Err != nil {
			refused = true
		}
	}
	if refused || dry {
		return out
	}
	for i, op := range ops {
		if out[i].Unchanged {
			continue
		}
		t.writeLocked(plans[i].key, plans[i].nv)
		if t.paramNote != nil {
			if tv, name, ok := t.lookupLocked(op.Path); ok {
				out[i].To = plainNumeric(t.liveLocked(name, tv))
				t.paramNote(op.Path, by, out[i].To)
			}
		}
	}
	return out
}

// TagRange is the bound a setpoint's external writes must stay inside
// (#718). A nil side is open.
type TagRange struct {
	Name     string
	Min, Max *float64
}

// rangeCheck refuses a whole-tag write of a number outside the tag's
// declared min/max with ir.ErrOutOfRange and a message that names the range.
// Only this front door applies it: a program's own assignment never comes
// through here, so a range never changes control behaviour. A value that is
// not a number is left to the type rules.
func (t *Tags) rangeCheck(op WriteOp) error {
	r, ok := t.ranges[ir.NameKey(op.Path)]
	if !ok {
		return nil
	}
	var f float64
	switch x := op.Value.(type) {
	case float64:
		f = x
	case float32:
		f = float64(x)
	case int:
		f = float64(x)
	case int32:
		f = float64(x)
	case int64:
		f = float64(x)
	case uint32:
		f = float64(x)
	case uint64:
		f = float64(x)
	case ir.Value:
		switch x.Kind {
		case ir.TypeReal:
			f = x.F
		case ir.TypeInt, ir.TypeTime:
			f = float64(x.I)
		default:
			return nil
		}
	default:
		// A text or boolean value for a ranged tag (a TIME literal such as
		// T#99h would otherwise slip past the numeric bound): a range
		// applies to numbers.
		return fmt.Errorf("tag %s has the range %s: write a number inside it: %w", r.Name, r.String(), ir.ErrTypeMismatch)
	}
	if (r.Min != nil && f < *r.Min) || (r.Max != nil && f > *r.Max) {
		return fmt.Errorf("tag %s: %v is outside its range %s — write a value inside it (the range is the tag's min:/max: in the manifest): %w",
			r.Name, f, r.String(), ir.ErrOutOfRange)
	}
	return nil
}

// String renders the range as "[min, max]", with ".." for an open side.
func (r TagRange) String() string {
	lo, hi := "..", ".."
	if r.Min != nil {
		lo = fmt.Sprint(*r.Min)
	}
	if r.Max != nil {
		hi = fmt.Sprint(*r.Max)
	}
	return "[" + lo + ", " + hi + "]"
}

// Range reports a tag's declared bounds, for the parameter listing.
func (t *Tags) Range(name string) (TagRange, bool) {
	r, ok := t.ranges[ir.NameKey(name)]
	return r, ok
}
