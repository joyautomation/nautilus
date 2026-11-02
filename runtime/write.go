package runtime

import "errors"

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
)

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
func (t *Tags) Write(ops []WriteOp) []error {
	errs := make([]error, len(ops))
	t.mu.Lock()
	defer t.mu.Unlock()
	for i, op := range ops {
		if f := t.forcedOverlapLocked(op.Path); f != "" {
			errs[i] = &ForcedError{Path: op.Path, Force: f}
			continue
		}
		errs[i] = t.setPathLocked(op.Path, op.Value, true)
	}
	return errs
}
